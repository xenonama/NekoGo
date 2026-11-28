package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tor_core_test.go — Tor is its own engine, with its own binary.
//
// NekoGo can run Tor inside aether.exe (arti is linked into it) or use
// the standalone Tor binary. They remain separate runtime engines with
// their own paths and versions; only Aether belongs in the Updates panel.

// writeTorExe lays out a fake Tor install: [layout] picks where tor.exe
// sits — flat beside the engines, in a tor\ subfolder, or inside a Tor
// Browser tree. The binaries are inert files; every version query in
// these tests is stubbed.
func writeTorExe(t *testing.T, dir, layout string) string {
	t.Helper()
	rel := map[string]string{
		"flat":    "tor.exe",
		"sub":     filepath.Join("tor", "tor.exe"),
		"browser": filepath.Join("TorBrowser", "Tor", "tor.exe"),
	}[layout]
	if rel == "" {
		t.Fatalf("unknown layout %q", layout)
	}
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// stubTorResolver pins the standalone-Tor resolver for one test: the
// binary it resolves to ("" clears the override so the settings and bin\
// paths are walked instead) and the answer its version query gives.
// Non-tor binaries are delegated to whatever query was installed before,
// because runtime diagnostics still report both engines and a test that
// pins both must not have the second stub silence the first.
func stubTorResolver(t *testing.T, exe, version string) {
	t.Helper()
	prevExe, prevDir, prevQuery := torExeOverride, torExeDirOverride, exeVersionQuery
	torExeOverride, torExeDirOverride = exe, ""
	exeVersionQuery = func(path string, args ...string) string {
		if strings.EqualFold(filepath.Base(path), torExeName) {
			return version
		}
		return prevQuery(path, args...)
	}
	t.Cleanup(func() {
		torExeOverride, torExeDirOverride, exeVersionQuery = prevExe, prevDir, prevQuery
	})
}

// setTorPath points Settings at [p] the way the Advanced card would.
func setTorPath(t *testing.T, a *App, p string) {
	t.Helper()
	s := a.store.GetSettings()
	s.TorPath = p
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTorExeFindsTheStandaloneBinary(t *testing.T) {
	prevExe, prevDir := torExeOverride, torExeDirOverride
	t.Cleanup(func() { torExeOverride, torExeDirOverride = prevExe, prevDir })

	// The test hook wins, so no other test's binary can leak in.
	hook := filepath.Join(t.TempDir(), "tor.exe")
	if err := os.WriteFile(hook, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	torExeOverride, torExeDirOverride = hook, ""
	if got, err := resolveTorExe(Settings{}); err != nil || got != hook {
		t.Fatalf("override: got %q, %v; want %q", got, err, hook)
	}
	torExeOverride = filepath.Join(t.TempDir(), "absent", "tor.exe")
	if _, err := resolveTorExe(Settings{}); err == nil {
		t.Fatal("a missing test hook resolved anyway")
	}
	torExeOverride = ""

	a := newUpdaterTestApp(t)

	// A file path is the binary itself.
	dir := t.TempDir()
	exePath := writeTorExe(t, dir, "flat")
	setTorPath(t, a, exePath)
	if got, err := resolveTorExe(a.store.GetSettings()); err != nil || got != exePath {
		t.Errorf("file override: got %q, %v; want %q", got, err, exePath)
	}

	// A directory override finds tor.exe inside it…
	dir = t.TempDir()
	exePath = writeTorExe(t, dir, "flat")
	setTorPath(t, a, dir)
	if got, err := resolveTorExe(a.store.GetSettings()); err != nil || got != exePath {
		t.Errorf("dir override: got %q, %v; want %q", got, err, exePath)
	}

	// …and a Tor Browser tree is a directory a user has, so tor.exe two
	// levels down has to be found rather than reported missing.
	dir = t.TempDir()
	exePath = writeTorExe(t, dir, "browser")
	setTorPath(t, a, dir)
	if got, err := resolveTorExe(a.store.GetSettings()); err != nil || got != exePath {
		t.Errorf("Tor Browser tree: got %q, %v; want %q", got, err, exePath)
	}

	// A directory with no Tor in it says so, naming the file it wanted.
	empty := t.TempDir()
	setTorPath(t, a, empty)
	_, err := resolveTorExe(a.store.GetSettings())
	if err == nil || !strings.Contains(err.Error(), "tor.exe") {
		t.Errorf("empty dir: err = %v, want one naming tor.exe", err)
	}

	// A path that does not exist is an error, not a silent fallback.
	setTorPath(t, a, filepath.Join(t.TempDir(), "nope", "tor.exe"))
	if _, err := resolveTorExe(a.store.GetSettings()); err == nil {
		t.Error("a nonexistent torPath resolved")
	}

	// Nothing configured: the shipped layout, bin\tor.exe and the
	// bundle's own tor\tor.exe folder.
	torExeDirOverride = ""
	dir = t.TempDir()
	exePath = writeTorExe(t, dir, "flat")
	torExeDirOverride = dir
	if got, err := resolveTorExe(Settings{}); err != nil || got != exePath {
		t.Errorf("bin layout: got %q, %v; want %q", got, err, exePath)
	}
	dir = t.TempDir()
	exePath = writeTorExe(t, dir, "sub")
	torExeDirOverride = dir
	if got, err := resolveTorExe(Settings{}); err != nil || got != exePath {
		t.Errorf("tor\\ subfolder: got %q, %v; want %q", got, err, exePath)
	}

	// Nothing anywhere: the error tells a user how to get Tor.
	torExeDirOverride = filepath.Join(t.TempDir(), "absent")
	_, err = resolveTorExe(Settings{})
	if err == nil {
		t.Fatal("a machine with no Tor resolved")
	}
	if !strings.Contains(err.Error(), "Settings") && !strings.Contains(err.Error(), "torPath") {
		t.Errorf("err = %v, want it to name where to set or install Tor", err)
	}
}

// TestTorPathSurvivesASave: the override is a setting, so it has to reach
// disk the way aetherPath does and come back from a re-opened store.
func TestTorPathSurvivesASave(t *testing.T) {
	a := newUpdaterTestApp(t)
	p := filepath.Join(t.TempDir(), "tor.exe")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	setTorPath(t, a, p)

	raw, err := os.ReadFile(filepath.Join(a.store.Dir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk fileConfig
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Settings.TorPath != p {
		t.Errorf("config.json holds torPath %q, want %q", onDisk.Settings.TorPath, p)
	}

	reopened, err := NewConfigStore(a.store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.GetSettings().TorPath; got != p {
		t.Errorf("re-opened torPath = %q, want %q", got, p)
	}
}

// TestTorStaysOutOfUpdatesAndPsiphonKeepsAether: removing Tor from the
// updater must not remove its runtime resolver, version diagnostics or
// the Aether updater row.
func TestTorStaysOutOfUpdatesAndPsiphonKeepsAether(t *testing.T) {
	a := newUpdaterTestApp(t)
	stubAetherResolver(t, "aether 2.3.0")
	aetherDir := fakeAetherBin(t, false)
	setAetherPath(t, a, filepath.Join(aetherDir, "aether.exe"))
	setTorPath(t, a, filepath.Join(t.TempDir(), "absent", "tor.exe"))
	stubTorResolver(t, "", "")

	rows, err := a.ListUpdatables()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "tor" {
			t.Fatal("Tor still has an Updates row")
		}
	}
	if got := a.TorVersion(); got != "" {
		t.Errorf("TorVersion = %q with no tor.exe on disk", got)
	}

	psRow := updatableRow(t, a, "psiphon")
	if psRow.Current != "aether 2.3.0" {
		t.Errorf("Psiphon Current = %q, want the aether engine's line", psRow.Current)
	}
	if psRow.Name != "Aether" {
		t.Errorf("the aether row is named %q", psRow.Name)
	}

	dir := t.TempDir()
	torPath := writeTorExe(t, dir, "flat")
	setTorPath(t, a, torPath)
	stubTorResolver(t, torPath, "tor 0.4.8.16")
	if got := a.TorVersion(); got != "tor 0.4.8.16" {
		t.Errorf("TorVersion = %q, want the standalone binary's line", got)
	}
	if a.currentCorePath("tor") == a.currentCorePath("psiphon") {
		t.Errorf("tor and psiphon resolve to the same file %q", a.currentCorePath("tor"))
	}
}

// TestTorVersionIsCachedPerBinary mirrors the other engines: the query
// spawns a process, and a replaced binary must be re-queried.
func TestTorVersionIsCachedPerBinary(t *testing.T) {
	a := newUpdaterTestApp(t)
	calls := 0
	prevExe, prevDir, prevQuery := torExeOverride, torExeDirOverride, exeVersionQuery
	t.Cleanup(func() {
		torExeOverride, torExeDirOverride, exeVersionQuery = prevExe, prevDir, prevQuery
	})
	exeVersionQuery = func(string, ...string) string { calls++; return "tor 0.4.8.16" }

	dir := t.TempDir()
	writeTorExe(t, dir, "flat")
	torExeOverride, torExeDirOverride = filepath.Join(dir, "tor.exe"), ""
	if got := a.TorVersion(); got != "tor 0.4.8.16" {
		t.Fatalf("TorVersion = %q", got)
	}
	if got := a.TorVersion(); got != "tor 0.4.8.16" || calls != 1 {
		t.Errorf("the second call queried again (%d calls, %q)", calls, got)
	}

	// A different binary is a different answer.
	dir2 := t.TempDir()
	writeTorExe(t, dir2, "flat")
	torExeOverride = filepath.Join(dir2, "tor.exe")
	exeVersionQuery = func(string, ...string) string { calls++; return "tor 0.4.8.17" }
	if got := a.TorVersion(); got != "tor 0.4.8.17" {
		t.Errorf("TorVersion after a replace = %q", got)
	}

	// A binary that will not answer is never cached as an answer.
	dir3 := t.TempDir()
	writeTorExe(t, dir3, "flat")
	torExeOverride = filepath.Join(dir3, "tor.exe")
	exeVersionQuery = func(string, ...string) string { calls++; return "" }
	if got := a.TorVersion(); got != "" {
		t.Errorf("TorVersion = %q for a binary that does not answer", got)
	}
	exeVersionQuery = func(string, ...string) string { calls++; return "tor 0.4.8.16" }
	if got := a.TorVersion(); got != "tor 0.4.8.16" {
		t.Errorf("a failed query poisoned the cache: %q", got)
	}
}
