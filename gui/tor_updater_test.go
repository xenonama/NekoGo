package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// aetherReleaseZip builds the archive the way CluvexStudio/Aether
// publishes it: aether.exe at the root with the pluggable transports
// under pt\ (measured in the 2.3.0 zip: aether.exe, pt/lyrebird.exe,
// pt/psiphon-tunnel-core.exe, run-aether.bat).
func aetherReleaseZip(t *testing.T, files []struct{ name, body string }) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// torReleaseFiles is a whole release: the engine and both transports.
func torReleaseFiles(exe string) []struct{ name, body string } {
	return []struct{ name, body string }{
		{"aether.exe", exe},
		{"pt/lyrebird.exe", "lyrebird-bytes"},
		{"pt/psiphon-tunnel-core.exe", "psiphon-bytes"},
		{"run-aether.bat", "@echo off\r\n"},
	}
}

// fakeAetherServer serves the Aether release at /releases/latest (the
// endpoint build.bat fetches the same asset from) plus the downloads.
func fakeAetherServer(t *testing.T, tag string, assets map[string][]byte, digests map[string]string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	assetJSON := func() string {
		var parts []string
		for name, payload := range assets {
			part := `{"name":` + strconv.Quote(name) +
				`,"browser_download_url":` + strconv.Quote(srv.URL+"/dl/"+name) +
				`,"size":` + strconv.Itoa(len(payload))
			if d := digests[name]; d != "" {
				part += `,"digest":` + strconv.Quote(d)
			}
			parts = append(parts, part+`}`)
		}
		return strings.Join(parts, ",")
	}
	mux.HandleFunc("/repos/"+aetherOwnerRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":` + strconv.Quote(tag) + `,"name":"stable","assets":[` + assetJSON() + `]}`))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		if p, ok := assets[name]; ok {
			_, _ = w.Write(p)
			return
		}
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	return srv
}

func setAetherPath(t *testing.T, a *App, p string) {
	t.Helper()
	s := a.store.GetSettings()
	s.AetherPath = p
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
}

// stubAetherResolver pins the resolver for one test: no override left
// over from another test, and the version query answered without
// spawning anything.
func stubAetherResolver(t *testing.T, version string) {
	t.Helper()
	prevExe, prevQuery := aetherExeOverride, exeVersionQuery
	aetherExeOverride = ""
	exeVersionQuery = func(string, ...string) string { return version }
	t.Cleanup(func() { aetherExeOverride, exeVersionQuery = prevExe, prevQuery })
}

// TestUpdatePsiphonInstallsTheEngineAndItsTransports: the Psiphon row has to
// install the whole engine, not just the binary the version query reads.
// A folder with aether.exe and no pt\ starts Tor and then fails to spawn
// a transport for every bridge line, which is the stall a user cannot
// diagnose — so the archive's pt\ entries land beside the binary they
// belong to, and the row stops reporting them missing.
func TestUpdatePsiphonInstallsTheEngineAndItsTransports(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the psiphon row installs the Windows archive only")
	}
	stubAetherResolver(t, "aether 2.4.0")
	zipBytes := aetherReleaseZip(t, torReleaseFiles("new-aether-bytes"))
	srv := fakeAetherServer(t, "v2.4.0",
		map[string][]byte{aetherAssetName: zipBytes},
		map[string]string{aetherAssetName: "sha256:" + sha256Hex(zipBytes)})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dir := t.TempDir()
	setAetherPath(t, a, dir)

	got, err := a.UpdateComponent("psiphon")
	if err != nil {
		t.Fatalf("UpdateComponent(psiphon): %v", err)
	}
	for name, want := range map[string]string{
		filepath.Join(dir, "aether.exe"):                    "new-aether-bytes",
		filepath.Join(dir, "pt", "lyrebird.exe"):            "lyrebird-bytes",
		filepath.Join(dir, "pt", "psiphon-tunnel-core.exe"): "psiphon-bytes",
	} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s was not installed: %v", name, err)
		}
		if string(raw) != want {
			t.Errorf("%s holds %q, want %q", name, raw, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "aether.exe.bak")); !os.IsNotExist(err) {
		t.Error("a stale .bak survived a good install")
	}
	if got.Latest != "v2.4.0" || got.UpdateAvail {
		t.Errorf("row = %+v, want Latest=v2.4.0 UpdateAvail=false", *got)
	}
	// The version cache is keyed on path, and the path did not change.
	if v := a.AetherVersion(); v != "aether 2.4.0" {
		t.Errorf("AetherVersion after the install = %q, want the new binary's line", v)
	}
	// …and the row that reported the missing transports is quiet now.
	if row := updatableRow(t, a, "psiphon"); row.Notes != "" {
		t.Errorf("Notes = %q after installing the whole archive", row.Notes)
	}
}

// TestPsiphonRowChecksTheAetherRelease: the row follows the repo that
// publishes the engine, offers an update when the release is newer and
// when nothing is installed at all, and stays quiet on parity.
func TestPsiphonRowChecksTheAetherRelease(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the psiphon row installs the Windows archive only")
	}
	zipBytes := aetherReleaseZip(t, torReleaseFiles("aether"))
	name := aetherAssetName
	digests := map[string]string{name: "sha256:" + sha256Hex(zipBytes)}

	for _, tc := range []struct {
		tag, installed, wantAvail string
		avail                     bool
	}{
		{tag: "v2.4.0", installed: "aether 2.3.0", wantAvail: "v2.4.0", avail: true},
		{tag: "v2.3.0", installed: "aether 2.3.0", wantAvail: "v2.3.0", avail: false},
		{tag: "v2.3.0", installed: "", wantAvail: "v2.3.0", avail: true},
	} {
		srv := fakeAetherServer(t, tc.tag, map[string][]byte{name: zipBytes}, digests)
		updateBaseURL["github-api"] = srv.URL
		a := newUpdaterTestApp(t)
		stubAetherResolver(t, tc.installed)
		// A path that does not exist keeps this machine's own aether.exe
		// out of the assertion; the installed case answers through the stub.
		if tc.installed == "" {
			setAetherPath(t, a, filepath.Join(t.TempDir(), "absent", "aether.exe"))
		} else {
			setAetherPath(t, a, filepath.Join(fakeAetherBin(t, true), "aether.exe"))
		}
		row := updatableRow(t, a, "psiphon")
		if tc.installed == "" && !strings.Contains(row.Notes, "Not installed") {
			t.Errorf("%s: Notes = %q, want Not installed", tc.tag, row.Notes)
		}
		rows, err := a.CheckUpdates(nil)
		if err != nil {
			t.Fatalf("CheckUpdates: %v", err)
		}
		var checked UpdatableComponent
		for _, r := range rows {
			if r.ID == "psiphon" {
				checked = r
			}
		}
		if checked.Latest != tc.wantAvail {
			t.Errorf("installed %q vs %s: Latest = %q, want %q", tc.installed, tc.tag, checked.Latest, tc.wantAvail)
		}
		if checked.UpdateAvail != tc.avail {
			t.Errorf("installed %q vs %s: UpdateAvail = %v, want %v", tc.installed, tc.tag, checked.UpdateAvail, tc.avail)
		}
		srv.Close()
	}
}

// TestUpdatePsiphonRefusesDigestMismatch: an archive whose bytes do not match
// the published sha256 must never reach the install path — neither the
// engine nor a transport — and the working binary has to survive.
func TestUpdatePsiphonRefusesDigestMismatch(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the psiphon row installs the Windows archive only")
	}
	stubAetherResolver(t, "aether 2.4.0")
	zipBytes := aetherReleaseZip(t, torReleaseFiles("tampered"))
	srv := fakeAetherServer(t, "v2.4.0",
		map[string][]byte{aetherAssetName: zipBytes},
		map[string]string{aetherAssetName: "sha256:" + sha256Hex([]byte("the-real-archive"))})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "aether.exe")
	before := []byte("working-aether")
	if err := os.WriteFile(dest, before, 0o644); err != nil {
		t.Fatal(err)
	}
	setAetherPath(t, a, dest)

	_, err := a.UpdateComponent("psiphon")
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want a sha256 mismatch refusal", err)
	}
	after, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("an unverified download replaced the working engine")
	}
	if _, err := os.Stat(filepath.Join(dir, "pt", "lyrebird.exe")); !os.IsNotExist(err) {
		t.Fatal("an unverified download wrote a transport binary")
	}
	if _, err := os.Stat(dest + ".bak"); !os.IsNotExist(err) {
		t.Fatal("a .bak was written before the digest was checked")
	}
}

// TestUpdatePsiphonStopsOnlyExactImageUsers: Windows cannot replace a
// mapped image, so the updater stops every aether.exe child using the
// destination while leaving an unrelated engine running.
func TestUpdatePsiphonStopsOnlyExactImageUsers(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the psiphon row installs the Windows archive only")
	}
	stubAetherResolver(t, "aether 2.4.0")
	zipBytes := aetherReleaseZip(t, torReleaseFiles("aether"))
	srv := fakeAetherServer(t, "v2.4.0",
		map[string][]byte{aetherAssetName: zipBytes},
		map[string]string{aetherAssetName: "sha256:" + sha256Hex(zipBytes)})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	for _, tc := range []struct {
		name  string
		which func(*App) (*AetherManager, *AetherManager)
	}{
		{"engine", func(a *App) (*AetherManager, *AetherManager) { return a.aetherMgr, a.psiphonMgr }},
		{"psiphon chain", func(a *App) (*AetherManager, *AetherManager) { return a.psiphonMgr, a.torMgr }},
		{"tor chain", func(a *App) (*AetherManager, *AetherManager) { return a.torMgr, a.aetherMgr }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newUpdaterTestApp(t)
			dir := t.TempDir()
			dest := filepath.Join(dir, "aether.exe")
			if err := os.WriteFile(dest, []byte("old-aether"), 0o644); err != nil {
				t.Fatal(err)
			}
			setAetherPath(t, a, dest)
			m, other := tc.which(a)
			if m == nil || other == nil {
				t.Fatal("an aether manager is not wired")
			}
			m.mu.Lock()
			m.cmd, m.running = exec.Command(dest), true
			m.mu.Unlock()
			otherPath := filepath.Join(t.TempDir(), "other-aether.exe")
			other.mu.Lock()
			other.cmd, other.running = exec.Command(otherPath), true
			other.mu.Unlock()

			if _, err := a.UpdateComponent("psiphon"); err != nil {
				t.Fatalf("UpdateComponent(psiphon): %v", err)
			}
			m.mu.Lock()
			stopped := !m.running && m.cmd == nil
			m.mu.Unlock()
			if !stopped {
				t.Fatal("the exact-image aether child is still running")
			}
			other.mu.Lock()
			untouched := other.running && other.cmd != nil && samePath(other.cmd.Path, otherPath)
			other.mu.Unlock()
			if !untouched {
				t.Fatal("an unrelated aether child was stopped")
			}
			raw, err := os.ReadFile(dest)
			if err != nil || string(raw) != "aether" {
				t.Fatalf("installed engine = %q, %v; want aether", raw, err)
			}
		})
	}
}

// TestUpdatePsiphonRefreshesTransportsWithIdenticalEngine: a release can
// change pt\ without changing aether.exe. The transaction must still stop
// an in-use engine and install the transports instead of taking the
// identical-binary no-op.
func TestUpdatePsiphonRefreshesTransportsWithIdenticalEngine(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the psiphon row installs the Windows archive only")
	}
	stubAetherResolver(t, "aether 2.4.0")
	zipBytes := aetherReleaseZip(t, torReleaseFiles("same-aether"))
	srv := fakeAetherServer(t, "v2.4.0",
		map[string][]byte{aetherAssetName: zipBytes},
		map[string]string{aetherAssetName: "sha256:" + sha256Hex(zipBytes)})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "aether.exe")
	if err := os.WriteFile(dest, []byte("same-aether"), 0o644); err != nil {
		t.Fatal(err)
	}
	pt := filepath.Join(dir, "pt")
	if err := os.MkdirAll(pt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pt, "lyrebird.exe"), []byte("old-lyrebird"), 0o644); err != nil {
		t.Fatal(err)
	}
	setAetherPath(t, a, dest)

	if _, err := a.UpdateComponent("psiphon"); err != nil {
		t.Fatalf("UpdateComponent(psiphon): %v", err)
	}
	for name, want := range map[string]string{
		"lyrebird.exe":            "lyrebird-bytes",
		"psiphon-tunnel-core.exe": "psiphon-bytes",
	} {
		raw, err := os.ReadFile(filepath.Join(pt, name))
		if err != nil || string(raw) != want {
			t.Errorf("%s = %q, %v; want %q", name, raw, err, want)
		}
	}
	raw, err := os.ReadFile(dest)
	if err != nil || string(raw) != "same-aether" {
		t.Fatalf("identical engine changed to %q, %v", raw, err)
	}
}
