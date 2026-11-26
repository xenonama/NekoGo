package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAetherBin lays out the shipped engine folder: aether.exe in bin\
// and, when [withPT] is set, the pluggable transports in bin\pt\ beside
// it. The binaries are empty files — the version query is stubbed, so
// nothing here has to run.
func fakeAetherBin(t *testing.T, withPT bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aether.exe"), []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withPT {
		pt := filepath.Join(dir, "pt")
		if err := os.MkdirAll(pt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pt, "lyrebird.exe"), []byte("not a real binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stubAetherExe points the resolver at [path] for the duration of the
// test and stubs the version query, so no row here spawns a binary.
func stubAetherExe(t *testing.T, path, version string) {
	t.Helper()
	prevExe, prevQuery := aetherExeOverride, exeVersionQuery
	aetherExeOverride, exeVersionQuery = path, func(string, ...string) string { return version }
	t.Cleanup(func() { aetherExeOverride, exeVersionQuery = prevExe, prevQuery })
}

// updatableRow picks one row of the Updates panel by id.
func updatableRow(t *testing.T, a *App, id string) UpdatableComponent {
	t.Helper()
	rows, err := a.ListUpdatables()
	if err != nil {
		t.Fatalf("ListUpdatables: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	t.Fatalf("no %q row in the Updates panel; it lists %v", id, ids)
	return UpdatableComponent{}
}

// TestPsiphonRowReportsTheEngineAndItsTransports: the Psiphon engine is
// aether.exe, which links arti too and launches pt\lyrebird.exe for
// bridges. The Updates panel is where every other engine says whether it
// is installed and which version it is, and aether said neither, so a
// folder without aether.exe (or without pt\) looked exactly like one with
// it right up to the connect that failed inside the engine.
func TestPsiphonRowReportsTheEngineAndItsTransports(t *testing.T) {
	a := newUpdaterTestApp(t)

	// Nothing installed: the row says so instead of vanishing.
	stubAetherExe(t, filepath.Join(t.TempDir(), "aether.exe"), "")
	row := updatableRow(t, a, "psiphon")
	if row.Current != "" {
		t.Errorf("a missing engine reported Current = %q", row.Current)
	}
	if !strings.Contains(row.Notes, "Not installed") {
		t.Errorf("a missing engine reported Notes = %q, want it to say Not installed", row.Notes)
	}
	if row.Name != "Aether" {
		t.Errorf("the engine row is named %q, want Aether", row.Name)
	}

	// Installed with its transports: the version, and nothing to fix.
	stubAetherExe(t, filepath.Join(fakeAetherBin(t, true), "aether.exe"), "aether 2.3.0")
	row = updatableRow(t, a, "psiphon")
	if row.Current != "aether 2.3.0" {
		t.Errorf("Current = %q, want the engine's own version line", row.Current)
	}
	if row.Notes != "" {
		t.Errorf("a complete install reported %q", row.Notes)
	}

	// Installed without pt\: aether runs and every bridge fails to
	// launch, so the row has to name the missing binary.
	stubAetherExe(t, filepath.Join(fakeAetherBin(t, false), "aether.exe"), "aether 2.3.0")
	row = updatableRow(t, a, "psiphon")
	if row.Current != "aether 2.3.0" {
		t.Errorf("Current = %q, want the version even with pt\\ missing", row.Current)
	}
	if !strings.Contains(row.Notes, "lyrebird") {
		t.Errorf("Notes = %q, want it to name the missing transport binary", row.Notes)
	}
}

// TestAetherVersionIsCachedPerBinary mirrors the other cores: the query
// spawns a process, the About card and the Updates panel both ask, and a
// replaced binary has to be re-queried rather than served from cache.
func TestAetherVersionIsCachedPerBinary(t *testing.T) {
	a := newUpdaterTestApp(t)
	calls := 0
	prevExe, prevQuery := aetherExeOverride, exeVersionQuery
	aetherExeOverride = filepath.Join(fakeAetherBin(t, true), "aether.exe")
	exeVersionQuery = func(string, ...string) string { calls++; return "aether 2.3.0" }
	t.Cleanup(func() { aetherExeOverride, exeVersionQuery = prevExe, prevQuery })

	if got := a.AetherVersion(); got != "aether 2.3.0" {
		t.Fatalf("AetherVersion = %q", got)
	}
	if got := a.AetherVersion(); got != "aether 2.3.0" || calls != 1 {
		t.Errorf("the second call queried again (%d calls, %q)", calls, got)
	}
	// A different binary at a different path is a different answer.
	aetherExeOverride = filepath.Join(fakeAetherBin(t, true), "aether.exe")
	exeVersionQuery = func(string, ...string) string { calls++; return "aether 2.4.0" }
	if got := a.AetherVersion(); got != "aether 2.4.0" {
		t.Errorf("AetherVersion after a replace = %q, want the new binary's line", got)
	}
	// A binary that will not run is never cached as an answer.
	aetherExeOverride = filepath.Join(fakeAetherBin(t, true), "aether.exe")
	exeVersionQuery = func(string, ...string) string { calls++; return "" }
	if got := a.AetherVersion(); got != "" {
		t.Errorf("AetherVersion = %q, want empty for a binary that does not answer", got)
	}
	exeVersionQuery = func(string, ...string) string { calls++; return "aether 2.3.0" }
	if got := a.AetherVersion(); got != "aether 2.3.0" {
		t.Errorf("a failed query poisoned the cache: AetherVersion = %q", got)
	}
}

// TestVersionStatsNamesTheTorEngine: the About card and its Copy
// diagnostics button are what a bug report carries, and they listed every
// core but the one that runs Tor.
func TestVersionStatsNamesTheTorEngine(t *testing.T) {
	diag := readFrontendSrc(t, "diag.ts")
	if !strings.Contains(diag, "aetherVersion") {
		t.Error("the version collector never asks the backend about the Tor engine")
	}
	rows := diag[strings.Index(diag, "export function versionStatRows"):]
	if !strings.Contains(rows, "aether") {
		t.Error("the Version stats card has no row for the Tor engine")
	}
	if !strings.Contains(diag, "aether ${v.aether}") {
		t.Error("Copy diagnostics omits the Tor engine, so a report cannot say whether it is installed")
	}
	api := readFrontendSrc(t, "api.ts")
	if !strings.Contains(api, "aetherVersion") {
		t.Error("api.ts does not expose the Tor engine version query")
	}
}
