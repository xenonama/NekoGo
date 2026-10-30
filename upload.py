import os
import sys
import json
import shutil
import subprocess
import threading
import time
import zipfile
from datetime import datetime
from pathlib import Path
import tkinter as tk
from tkinter import ttk, filedialog, messagebox, scrolledtext

# ---------- settings ----------
RC_FILE = Path(__file__).parent / ".uploadrc.json"

DEFAULT_SETTINGS = {
    "src": str(Path(__file__).parent),
    "repo": "https://github.com/xenonama/nekogo.git",
    "exdirs": ".git,source,dist,node_modules,build,bin,target",
    "exfiles": "*.exe,*.dll,*.sys,*.syso,*.log,*.tmp,*.zip,*.pdb,*.map",
    "commit": "NekoGo: Update source",
    "dry": False,
    "incremental": True,
    "force": False,
    "progress": True,
    "netcheck": True,
    "browser": False,
    "proxy_on": False,
    "proxy_url": "socks5://127.0.0.1:1080",
}

def load_settings():
    if RC_FILE.exists():
        try:
            with open(RC_FILE, "r", encoding="utf-8") as f:
                data = json.load(f)
            merged = DEFAULT_SETTINGS.copy()
            merged.update(data)
            return merged
        except Exception:
            return DEFAULT_SETTINGS.copy()
    return DEFAULT_SETTINGS.copy()

def save_settings(s):
    try:
        with open(RC_FILE, "w", encoding="utf-8") as f:
            json.dump(s, f, indent=2, ensure_ascii=False)
    except Exception:
        pass

# ---------- git helpers ----------
def run_cmd(args, cwd=None, capture=True):
    try:
        proc = subprocess.run(
            args,
            cwd=cwd,
            capture_output=capture,
            text=True,
            encoding="utf-8",
            errors="replace",
            shell=False,
        )
        return proc.returncode, proc.stdout or "", proc.stderr or ""
    except FileNotFoundError:
        return 127, "", f"Command not found: {args[0]}"
    except Exception as e:
        return 1, "", str(e)

# ---------- uploader core ----------
class Uploader:
    def __init__(self, settings, log_cb, progress_cb):
        self.s = settings
        self.log = log_cb
        self.progress = progress_cb
        self.start_time = None
        self.proxy_applied = False
        self.prev_http = None
        self.prev_https = None
        self.temp_repo = None

    def _log(self, msg, level="info"):
        self.log(msg, level)

    def _progress(self, step, pct, detail=""):
        self.progress(step, pct, detail)

    def _apply_proxy(self):
        if not self.s.get("proxy_on") or not self.s.get("proxy_url", "").strip():
            return
        url = self.s["proxy_url"].strip()
        rc, out, _ = run_cmd(["git", "config", "--global", "--get", "http.proxy"])
        if rc == 0 and out.strip():
            self.prev_http = out.strip()
        rc, out, _ = run_cmd(["git", "config", "--global", "--get", "https.proxy"])
        if rc == 0 and out.strip():
            self.prev_https = out.strip()
        self._log(f"[proxy] Applying {url} to git (temporary)", "info")
        run_cmd(["git", "config", "--global", "http.proxy", url])
        run_cmd(["git", "config", "--global", "https.proxy", url])
        self.proxy_applied = True

    def _restore_proxy(self):
        if not self.proxy_applied:
            return
        self._log("[proxy] Restoring previous git proxy settings", "info")
        run_cmd(["git", "config", "--global", "--unset", "http.proxy"])
        run_cmd(["git", "config", "--global", "--unset", "https.proxy"])
        if self.prev_http:
            run_cmd(["git", "config", "--global", "http.proxy", self.prev_http])
        if self.prev_https:
            run_cmd(["git", "config", "--global", "https.proxy", self.prev_https])
        self.proxy_applied = False

    def _force_remove(self, path: Path):
        """Robustly remove a folder on Windows, handling read-only files and locks."""
        shutil.rmtree(path, ignore_errors=True)
        if not path.exists():
            return

        def _on_error(func, p, exc_info):
            try:
                os.chmod(p, 0o777)
                func(p)
            except Exception:
                pass

        shutil.rmtree(path, onerror=_on_error)
        if not path.exists():
            return

        run_cmd(["cmd", "/c", "rmdir", "/s", "/q", str(path)])
        if not path.exists():
            return

        time.sleep(1.0)
        shutil.rmtree(path, ignore_errors=True)

    def _prepare_clone(self):
        src = Path(self.s["src"]).resolve()
        temp_repo = src.parent / "NekoGo-upload"
        self._progress("clone", 10, "Preparing clone...")
        self._log("[1/6] Preparing clone...", "info")

        if temp_repo == src or str(temp_repo).startswith(str(src) + os.sep):
            raise RuntimeError(f"Refusing to use {temp_repo}: it is inside the source tree")

        clone_exists = (temp_repo.exists() and (temp_repo / ".git").exists())

        if self.s.get("incremental") and clone_exists:
            self._log("[1/6] Reusing existing clone", "ok")
            run_cmd(["git", "fetch", "origin", "main"], cwd=str(temp_repo))
            run_cmd(["git", "reset", "--hard", "origin/main"], cwd=str(temp_repo))
            self._progress("clone", 20, "Clone ready")
            self.temp_repo = temp_repo
            return temp_repo

        if temp_repo.exists():
            self._log("[1/6] Removing stale clone folder...", "warn")
            self._force_remove(temp_repo)

        if temp_repo.exists():
            fallback = src.parent / f"NekoGo-upload-{int(time.time())}"
            self._log(f"[1/6] Stale folder is locked; using fallback: {fallback}", "warn")
            temp_repo = fallback

        self._log(f"[1/6] Cloning {self.s['repo']} -> {temp_repo} ...", "info")
        rc, out, err = run_cmd(["git", "clone", self.s["repo"], str(temp_repo)])
        if rc != 0:
            raise RuntimeError(f"git clone failed: {err.strip()}")
        if not (temp_repo / ".git").exists():
            raise RuntimeError("git clone reported success but .git is missing")
        self._log("[1/6] Cloned fresh", "ok")
        self._progress("clone", 20, "Clone ready")
        self.temp_repo = temp_repo
        return temp_repo

    def _backup(self, temp_repo):
        self._progress("backup", 30, "Backing up...")
        self._log("[2/6] Backing up...", "info")
        try:
            backup_dir = temp_repo.parent / "nekogo-backup"
            backup_dir.mkdir(exist_ok=True)
            ts = datetime.now().strftime("%Y%m%d-%H%M%S")
            zip_path = backup_dir / f"nekogo-backup-{ts}.zip"
            with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
                for p in temp_repo.rglob("*"):
                    if p.is_file() and ".git" not in p.parts:
                        zf.write(p, p.relative_to(temp_repo))
            self._log(f"[2/6] Backup: {zip_path}", "ok")
        except Exception as e:
            self._log(f"[2/6] Backup failed (continuing): {e}", "warn")
        self._progress("backup", 40, "Backup done")

    def _clear_tracked(self, temp_repo):
        self._progress("clear", 45, "Clearing tracked files...")
        self._log("[3/6] Clearing tracked files...", "info")
        run_cmd(["git", "rm", "-rf", ".", "--quiet"], cwd=str(temp_repo))
        self._log("[3/6] Cleared", "ok")

    def _copy_files(self, temp_repo):
        self._progress("copy", 50, "Copying files...")
        self._log("[4/6] Copying files...", "info")
        if self.s.get("dry"):
            self._log(f"[4/6] DRY-RUN: would copy {self.s['src']} -> {temp_repo}", "warn")
            self._progress("copy", 75, "Copy skipped (dry-run)")
            return

        src = Path(self.s["src"]).resolve()
        exdirs = [x.strip() for x in self.s["exdirs"].split(",") if x.strip()]
        exfiles = [x.strip() for x in self.s["exfiles"].split(",") if x.strip()]

        args = ["robocopy", str(src), str(temp_repo), "/E", "/NFL", "/NDL", "/NJH", "/NJS"]
        for d in exdirs:
            args += ["/XD", d]
        for f in exfiles:
            args += ["/XF", f]

        rc, out, err = run_cmd(args)
        if rc >= 8:
            raise RuntimeError(f"robocopy failed (code {rc}): {err.strip()}")
        self._log("[4/6] Copied", "ok")
        self._progress("copy", 75, "Copy complete")

    def _commit_and_push(self, temp_repo):
        self._progress("commit", 80, "Committing...")
        self._log("[5/6] Committing...", "info")
        run_cmd(["git", "add", "."], cwd=str(temp_repo))
        rc, _, _ = run_cmd(["git", "diff", "--cached", "--quiet"], cwd=str(temp_repo))
        has_changes = (rc != 0)

        if has_changes:
            if self.s.get("dry"):
                self._log(f"[5/6] DRY-RUN: would commit '{self.s['commit']}'", "warn")
            else:
                rc, out, err = run_cmd(["git", "commit", "-m", self.s["commit"]], cwd=str(temp_repo))
                if rc != 0:
                    raise RuntimeError(f"git commit failed: {err.strip()}")
                self._log("[5/6] Committed", "ok")
        else:
            self._log("[5/6] Nothing to commit", "dim")
        self._progress("commit", 90, "Commit done")

        self._progress("push", 95, "Pushing...")
        if self.s.get("dry"):
            self._log("[6/6] DRY-RUN: would push", "warn")
            self._log("DRY RUN COMPLETE", "ok")
            return False
        if not has_changes:
            self._log("[6/6] No changes; skipping push", "dim")
            return False

        self._log("[6/6] Pushing to GitHub...", "info")
        push_args = ["git", "push"]
        if self.s.get("force"):
            self._log("[6/6] FORCE push", "warn")
            push_args.append("--force")
        push_args += ["origin", "main"]
        rc, out, err = run_cmd(push_args, cwd=str(temp_repo))
        if rc != 0:
            raise RuntimeError(f"git push failed: {err.strip()}")
        self._progress("push", 100, "Push done")
        return True

    def _report(self, temp_repo):
        rc, out, _ = run_cmd(["git", "rev-parse", "--short", "HEAD"], cwd=str(temp_repo))
        commit_hash = out.strip() if rc == 0 else "?"
        elapsed = int(time.time() - self.start_time) if self.start_time else 0

        files_count = 0
        total_bytes = 0
        for p in temp_repo.rglob("*"):
            if p.is_file() and ".git" not in p.parts:
                files_count += 1
                try:
                    total_bytes += p.stat().st_size
                except Exception:
                    pass
        total_mb = round(total_bytes / (1024 * 1024), 1)

        report = {
            "repo": self.s["repo"],
            "commit": commit_hash,
            "files": files_count,
            "sizeMB": total_mb,
            "durationS": elapsed,
        }
        self._log(json.dumps(report, ensure_ascii=False), "report")
        return report

    def run(self):
        self.start_time = time.time()
        try:
            self._apply_proxy()

            self._progress("pre", 5, "Scanning source...")
            self._log("[pre] Scanning source...", "info")
            if not Path(self.s["src"]).exists():
                raise RuntimeError(f"Source folder not found: {self.s['src']}")

            if self.s.get("netcheck"):
                self._log("[pre] Checking GitHub connectivity...", "info")
                rc, _, _ = run_cmd(["git", "ls-remote", "--heads", self.s["repo"]])
                if rc != 0:
                    raise RuntimeError(f"Cannot reach {self.s['repo']}")
                self._log("[pre] GitHub reachable", "ok")

            temp_repo = self._prepare_clone()
            self._backup(temp_repo)
            self._clear_tracked(temp_repo)
            self._copy_files(temp_repo)
            pushed = self._commit_and_push(temp_repo)
            self._report(temp_repo)

            if pushed:
                self._log("UPLOAD OK", "ok")
            else:
                self._log("UPLOAD SKIPPED", "warn")
            return True
        except Exception as e:
            self._log(f"UPLOAD FAILED: {e}", "err")
            return False
        finally:
            self._restore_proxy()


# ---------- GUI ----------
class UploaderGUI:
    def __init__(self, root):
        self.root = root
        self.settings = load_settings()
        self.root.title("NekoGo - Upload to GitHub")
        self.root.geometry("780x820")
        self.root.configure(bg="#1e1e1e")

        style = ttk.Style()
        style.theme_use("clam")
        style.configure("TLabel", background="#1e1e1e", foreground="#c8c8c8")
        style.configure("TFrame", background="#1e1e1e")

        self._build_ui()

    def _build_ui(self):
        pad = {"padx": 10, "pady": 4}

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        ttk.Label(frm, text="Source folder", width=16).pack(side="left")
        self.var_src = tk.StringVar(value=self.settings["src"])
        ttk.Entry(frm, textvariable=self.var_src).pack(side="left", fill="x", expand=True)
        ttk.Button(frm, text="...", command=self._browse).pack(side="left", padx=(4, 0))

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        ttk.Label(frm, text="Repository URL", width=16).pack(side="left")
        self.var_repo = tk.StringVar(value=self.settings["repo"])
        ttk.Entry(frm, textvariable=self.var_repo).pack(side="left", fill="x", expand=True)

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        ttk.Label(frm, text="Excluded folders", width=16).pack(side="left")
        self.var_exdirs = tk.StringVar(value=self.settings["exdirs"])
        ttk.Entry(frm, textvariable=self.var_exdirs).pack(side="left", fill="x", expand=True)

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        ttk.Label(frm, text="Excluded files", width=16).pack(side="left")
        self.var_exfiles = tk.StringVar(value=self.settings["exfiles"])
        ttk.Entry(frm, textvariable=self.var_exfiles).pack(side="left", fill="x", expand=True)

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        ttk.Label(frm, text="Commit message", width=16).pack(side="left")
        self.var_commit = tk.StringVar(value=self.settings["commit"])
        ttk.Entry(frm, textvariable=self.var_commit).pack(side="left", fill="x", expand=True)

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        self.var_dry = tk.BooleanVar(value=self.settings["dry"])
        self.var_inc = tk.BooleanVar(value=self.settings["incremental"])
        self.var_force = tk.BooleanVar(value=self.settings["force"])
        self.var_progress = tk.BooleanVar(value=self.settings["progress"])
        self.var_net = tk.BooleanVar(value=self.settings["netcheck"])
        self.var_browser = tk.BooleanVar(value=self.settings["browser"])

        tk.Checkbutton(frm, text="Dry run", variable=self.var_dry, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e").pack(side="left", padx=4)
        tk.Checkbutton(frm, text="Incremental", variable=self.var_inc, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e").pack(side="left", padx=4)
        tk.Checkbutton(frm, text="Force push", variable=self.var_force, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e").pack(side="left", padx=4)
        tk.Checkbutton(frm, text="Progress", variable=self.var_progress, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e").pack(side="left", padx=4)
        tk.Checkbutton(frm, text="Net check", variable=self.var_net, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e").pack(side="left", padx=4)
        tk.Checkbutton(frm, text="Open browser", variable=self.var_browser, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e").pack(side="left", padx=4)

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        self.var_proxy_on = tk.BooleanVar(value=self.settings["proxy_on"])
        tk.Checkbutton(frm, text="Use Proxy", variable=self.var_proxy_on, bg="#1e1e1e", fg="#ffffff",
                       selectcolor="#333333", activebackground="#1e1e1e",
                       command=self._toggle_proxy).pack(side="left")
        self.var_proxy_url = tk.StringVar(value=self.settings["proxy_url"])
        self.entry_proxy = ttk.Entry(frm, textvariable=self.var_proxy_url, width=40)
        self.entry_proxy.pack(side="left", padx=4)
        self.entry_proxy.configure(state="normal" if self.var_proxy_on.get() else "disabled")
        ttk.Label(frm, text="e.g. socks5://127.0.0.1:1080").pack(side="left", padx=4)

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        ttk.Label(frm, text="Progress", width=16).pack(side="left")
        self.progress = ttk.Progressbar(frm, orient="horizontal", mode="determinate", maximum=100)
        self.progress.pack(side="left", fill="x", expand=True)

        self.var_step = tk.StringVar(value="Idle")
        ttk.Label(self.root, textvariable=self.var_step, foreground="#9ab").pack(fill="x", padx=10)

        frm = ttk.Frame(self.root)
        frm.pack(fill="both", expand=True, **pad)
        self.log = scrolledtext.ScrolledText(frm, bg="#141414", fg="#c8ffc8",
                                             insertbackground="#c8ffc8", font=("Consolas", 9),
                                             wrap="word", state="disabled")
        self.log.pack(fill="both", expand=True)
        self.log.tag_config("ok", foreground="#78dc78")
        self.log.tag_config("warn", foreground="#e6c864")
        self.log.tag_config("err", foreground="#f07878")
        self.log.tag_config("info", foreground="#a0c8f0")
        self.log.tag_config("dim", foreground="#a0a0a0")
        self.log.tag_config("report", foreground="#b4b4ff")

        frm = ttk.Frame(self.root)
        frm.pack(fill="x", **pad)
        self.btn_upload = tk.Button(frm, text="Upload", bg="#287838", fg="white",
                                    activebackground="#3a9a4a", activeforeground="white",
                                    font=("Segoe UI", 9, "bold"), command=self._start_upload)
        self.btn_upload.pack(side="left", padx=4, ipadx=20, ipady=4)
        tk.Button(frm, text="Clear log", command=self._clear_log).pack(side="left", padx=4)
        tk.Button(frm, text="Close", command=self.root.destroy).pack(side="right", padx=4)

    def _browse(self):
        p = filedialog.askdirectory(initialdir=self.var_src.get())
        if p:
            self.var_src.set(p)

    def _toggle_proxy(self):
        self.entry_proxy.configure(state="normal" if self.var_proxy_on.get() else "disabled")

    def _clear_log(self):
        self.log.configure(state="normal")
        self.log.delete("1.0", "end")
        self.log.configure(state="disabled")
        self.progress["value"] = 0
        self.var_step.set("Idle")

    def _write_log(self, msg, level="info"):
        def _do():
            self.log.configure(state="normal")
            self.log.insert("end", msg + "\n", level)
            self.log.see("end")
            self.log.configure(state="disabled")
        self.root.after(0, _do)

    def _set_progress(self, step, pct, detail=""):
        def _do():
            self.progress["value"] = max(0, min(100, pct))
            self.var_step.set(f"{step} - {pct}% {detail}")
        self.root.after(0, _do)

    def _start_upload(self):
        s = {
            "src": self.var_src.get().strip(),
            "repo": self.var_repo.get().strip(),
            "exdirs": self.var_exdirs.get().strip(),
            "exfiles": self.var_exfiles.get().strip(),
            "commit": self.var_commit.get().strip(),
            "dry": bool(self.var_dry.get()),
            "incremental": bool(self.var_inc.get()),
            "force": bool(self.var_force.get()),
            "progress": bool(self.var_progress.get()),
            "netcheck": bool(self.var_net.get()),
            "browser": bool(self.var_browser.get()),
            "proxy_on": bool(self.var_proxy_on.get()),
            "proxy_url": self.var_proxy_url.get().strip(),
        }
        if not s["src"] or not s["repo"]:
            messagebox.showerror("Error", "Source folder and Repository URL are required.")
            return

        save_settings(s)
        self.btn_upload.configure(state="disabled")
        self._write_log("=== Starting upload ===", "info")

        def _runner():
            uploader = Uploader(s, self._write_log, self._set_progress)
            ok = uploader.run()
            def _finish():
                self.btn_upload.configure(state="normal")
                if ok:
                    self._set_progress("done", 100, "Finished")
                    if s["browser"]:
                        try:
                            import webbrowser
                            webbrowser.open(s["repo"])
                        except Exception:
                            pass
                else:
                    self._set_progress("failed", 0, "See log")
            self.root.after(0, _finish)

        threading.Thread(target=_runner, daemon=True).start()


def main():
    root = tk.Tk()
    app = UploaderGUI(root)
    root.mainloop()


if __name__ == "__main__":
    main()
