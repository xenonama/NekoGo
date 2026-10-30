package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Startup orphan cleanup (HOTFIX Bug 2).
//
// When the GUI process dies abnormally (e.g. the Bug 1 crash), its child
// xray.exe keeps running and keeps holding the mixed port (2080), the
// SOCKS port (2081) and the API port (10085). On relaunch the next Connect
// then fails with a bind error until the user kills xray.exe by hand in
// Task Manager. cleanupOrphanCores runs once at startup — before the first
// Connect is possible — and kills only strays that provably belong to us:
//
//   - image name is one of orphanImageNames (our engines only), AND
//   - the executable path resolves under exeDir (the directory holding the
//     GUI binary, i.e. gui/build/bin), AND
//   - the parent process is dead (ppid not running) and is not us.
//
// A same-named binary elsewhere on disk, or a child of another live GUI
// instance, is never touched. Every failure degrades to a log line and a 0
// return — orphan cleanup must never fail startup.

// orphanProc is one candidate core process.
type orphanProc struct {
	pid     int
	ppid    int
	name    string
	exePath string
}

// orphanImageNames are the only process names cleanupOrphanCores may kill.
// sing-box.exe is the official upstream CLI child (CoreEngine=upstream):
// a GUI crash leaves it holding the mixed port and the Clash API port.
// aether.exe and pt\psiphon-tunnel-core.exe are the Aether engine and the
// Psiphon exit both engine shapes spawn; a crashed GUI leaves the latter
// serving traffic with no supervisor to end it. sni-spoof.exe is the
// WinDivert SNI engine: an orphan keeps intercepting and faking TLS for
// the WHOLE machine after the GUI is gone, and pt\lyrebird.exe is the Tor
// transport the Aether engine spawns.
var orphanImageNames = []string{
	"xray.exe", "nekobox_core.exe", "sing-box.exe",
	"aether.exe", "psiphon-tunnel-core.exe",
	"sni-spoof.exe", "lyrebird.exe",
}

// orphanProcFilter renders the image-name list as a WQL/CIM filter
// ("name='xray.exe' or ...") so the enumeration can never drift from
// orphanImageNames.
func orphanProcFilter(colName string) string {
	parts := make([]string, 0, len(orphanImageNames))
	for _, n := range orphanImageNames {
		parts = append(parts, fmt.Sprintf("%s='%s'", colName, n))
	}
	return strings.Join(parts, " or ")
}

// cleanupOrphanCores scans for and kills stray core processes from a
// previous crashed session. Returns the number killed.
func cleanupOrphanCores(logCh chan LogEntry, exeDir string) int {
	log := func(level, msg string) {
		if logCh != nil {
			logCh <- LogEntry{Level: level, Source: "startup", Message: msg}
		}
	}
	if runtime.GOOS != "windows" {
		return 0
	}
	if strings.TrimSpace(exeDir) == "" {
		return 0
	}
	procs, err := listCoreProcs()
	if err != nil {
		log("warn", fmt.Sprintf("[startup] orphan scan skipped: %v", err))
		return 0
	}
	if len(procs) == 0 {
		return 0
	}
	alive, err := listAllPIDs()
	if err != nil {
		// Without the PID table we cannot tell orphans apart from
		// another live instance's children: skip conservatively.
		log("warn", fmt.Sprintf("[startup] orphan scan skipped (no PID table): %v", err))
		return 0
	}
	killed := 0
	for _, v := range selectOrphans(procs, exeDir, os.Getpid(), alive) {
		if err := killPID(v.pid); err != nil {
			log("error", fmt.Sprintf("[startup] failed to kill orphan %s (PID %d): %v", v.name, v.pid, err))
			continue
		}
		log("info", fmt.Sprintf("[startup] killed orphan %s (PID %d)", v.name, v.pid))
		killed++
	}
	return killed
}

// selectOrphans filters candidate processes down to killable strays (pure:
// unit-tested without touching any real process).
func selectOrphans(procs []orphanProc, exeDir string, selfPID int, alive map[int]bool) []orphanProc {
	var out []orphanProc
	for _, p := range procs {
		if p.pid <= 0 || p.pid == selfPID || p.ppid == selfPID {
			continue
		}
		if !isOrphanImage(p.name) {
			continue
		}
		if p.exePath == "" || !isUnderDir(p.exePath, exeDir) {
			continue
		}
		if p.ppid > 0 && alive[p.ppid] {
			// Owned by another live process (e.g. a second GUI
			// instance): not ours to kill.
			continue
		}
		out = append(out, p)
	}
	return out
}

func isOrphanImage(name string) bool {
	for _, n := range orphanImageNames {
		if strings.EqualFold(strings.TrimSpace(name), n) {
			return true
		}
	}
	return false
}

// isUnderDir reports whether path resolves inside dir (case-insensitive,
// separator-aware so C:\a\bin2 is NOT under C:\a\bin).
func isUnderDir(path, dir string) bool {
	ap, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	ad, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	ap = strings.ToLower(ap)
	ad = strings.ToLower(strings.TrimRight(ad, `\/`))
	return ap == ad || strings.HasPrefix(ap, ad+string(filepath.Separator))
}

// listCoreProcs enumerates the engine images (orphanImageNames) with
// their parent PIDs and executable paths: WMIC one-shot, PowerShell
// fallback.
func listCoreProcs() ([]orphanProc, error) {
	where := orphanProcFilter("name")
	if out, err := hiddenOutput("wmic", "process", "where", where,
		"get", "ProcessId,ParentProcessId,ExecutablePath", "/FORMAT:CSV"); err == nil {
		return parseWmicCSV(out)
	} else {
		wmicErr := err
		ps := `Get-CimInstance Win32_Process -Filter "` + orphanProcFilter("Name") + `" | ` +
			`Select-Object ProcessId,ParentProcessId,ExecutablePath | ConvertTo-Csv -NoTypeInformation`
		if out, err := hiddenOutput("powershell", "-NoProfile", "-NonInteractive", "-Command", ps); err == nil {
			return parsePowershellCSV(out)
		}
		return nil, wmicErr
	}
}

// listAllPIDs returns the set of running PIDs (single tasklist call).
func listAllPIDs() (map[int]bool, error) {
	out, err := hiddenOutput("tasklist", "/FO", "CSV", "/NH")
	if err != nil {
		return nil, err
	}
	return parseTasklistPIDs(out)
}

// killPID force-kills one process.
func killPID(pid int) error {
	return hiddenOutputErr("taskkill", "/F", "/PID", strconv.Itoa(pid))
}

// hiddenOutput runs a command with its console hidden on Windows
// (no-op elsewhere via setHideWindow) and returns stdout.
func hiddenOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	setHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

func hiddenOutputErr(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	setHideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// parseWmicCSV parses `wmic ... /FORMAT:CSV` output:
// header Node,ExecutablePath,ParentProcessId,ProcessId.
func parseWmicCSV(out string) ([]orphanProc, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var procs []orphanProc
	for _, f := range rows {
		if len(f) < 4 || isWmicHeader(f) {
			continue
		}
		exe := strings.TrimSpace(f[1])
		ppid, _ := strconv.Atoi(strings.TrimSpace(f[2]))
		pid, perr := strconv.Atoi(strings.TrimSpace(f[3]))
		if perr != nil || pid <= 0 {
			continue
		}
		procs = append(procs, orphanProc{pid: pid, ppid: ppid, name: nameFromPath(exe), exePath: exe})
	}
	return procs, nil
}

func isWmicHeader(f []string) bool {
	for _, c := range f {
		if strings.EqualFold(strings.TrimSpace(c), "ExecutablePath") {
			return true
		}
	}
	return false
}

// parsePowershellCSV parses ConvertTo-Csv output:
// header "ProcessId","ParentProcessId","ExecutablePath".
func parsePowershellCSV(out string) ([]orphanProc, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var procs []orphanProc
	for _, f := range rows {
		if len(f) < 3 || strings.EqualFold(strings.TrimSpace(f[0]), "ProcessId") {
			continue
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(f[0]))
		if perr != nil || pid <= 0 {
			continue
		}
		ppid, _ := strconv.Atoi(strings.TrimSpace(f[1]))
		exe := strings.TrimSpace(f[2])
		procs = append(procs, orphanProc{pid: pid, ppid: ppid, name: nameFromPath(exe), exePath: exe})
	}
	return procs, nil
}

// parseTasklistPIDs parses `tasklist /FO CSV /NH` into a PID set.
// Row shape: "Image Name","PID","Session Name","Session#","Mem Usage".
func parseTasklistPIDs(out string) (map[int]bool, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	pids := map[int]bool{}
	for _, f := range rows {
		if len(f) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(f[1])); err == nil && pid > 0 {
			pids[pid] = true
		}
	}
	return pids, nil
}

func nameFromPath(exe string) string {
	base := exe
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	return base
}
