package main

// Regression tests for the engine-choice contract: Settings.CoreEngine
// selects which sing-box binary runs EVERY sing-box transport — a plain
// profile, the SNI-spoof transport, and the Aether transport. Before
// this, a spoof profile always ran nekobox_core even with the official
// sing-box selected, which is exactly what users reported.
//
// The official CLI is stood in for by the test binary copied to
// sing-box.exe (fake_singbox_test.go); the spoof engine by
// testdata/spoofmock. No real binaries, drivers or admin rights needed.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type engineRig struct {
	cores    *CoreManager
	upstream *UpstreamSingBoxManager
	custom   *SingBoxManager
	spoof    *SpoofManager
	settings Settings
}

func newEngineRig(t *testing.T, coreEngine string) *engineRig {
	t.Helper()
	buildSpoofMock(t)
	logCh := make(chan LogEntry, 8192)
	custom := NewSingBoxManager("", logCh)
	up := NewUpstreamSingBoxManager(logCh)
	cores := NewCoreManager(custom, NewXrayManager("", t.TempDir(), logCh))
	spoofMgr := NewSpoofManager(logCh)
	cores.SetSpoofManager(spoofMgr)
	cores.SetUpstreamSingBoxManager(up)

	s := spoofTestSettings()
	s.SystemProxy = false
	s.TunEnabled = false
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	s.MixedPort = port
	s.CoreEngine = coreEngine
	if coreEngine == "upstream" {
		s.UpstreamCorePath = installFakeSingBox(t)
	}
	return &engineRig{cores: cores, upstream: up, custom: custom, spoof: spoofMgr, settings: s}
}

// TestSpoofTransportHonorsCoreEngine: a CoreSpoof connect runs the
// selected engine as its transport, not always the custom core.
func TestSpoofTransportHonorsCoreEngine(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SNI spoof engine is Windows-only")
	}
	rig := newEngineRig(t, "upstream")
	if err := rig.cores.Start(spoofTestProfile(), rig.settings); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rig.cores.Stop()
	if !rig.upstream.IsRunning() {
		t.Fatal("spoof transport is not the official sing-box (UpstreamCorePath)")
	}
	if rig.custom.IsRunning() {
		t.Fatal("custom nekobox_core ran even though CoreEngine=upstream")
	}
	if !rig.cores.groupMemberAlive(true, false, false) {
		t.Fatal("group membership does not see the upstream transport")
	}
	if err := rig.cores.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if rig.upstream.IsRunning() {
		t.Fatal("upstream engine survived Stop")
	}
	if rig.spoof.IsRunning() {
		t.Fatal("spoof engine survived Stop")
	}
}

// TestPlainProfileHonorsCoreEngine: a plain profile with the upstream
// engine spawns the official CLI (the existing behaviour, pinned here).
func TestPlainProfileHonorsCoreEngine(t *testing.T) {
	rig := newEngineRig(t, "upstream")
	p := &Profile{
		ID: "plain-1", Name: "plain", Core: CoreSingBox, Protocol: "trojan",
		Address: "example.com", Port: 443, IDOrPassword: "pw", TLS: true,
	}
	if err := rig.cores.Start(p, rig.settings); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rig.cores.Stop()
	if !rig.upstream.IsRunning() {
		t.Fatal("plain profile is not running the official sing-box")
	}
	if !rig.cores.groupMemberAlive(false, false, true) {
		t.Fatal("group membership does not see the upstream core")
	}
}

// TestUpstreamTransportDeathTearsDownSpoofGroup: an unexpected death of
// the upstream transport faults the whole spoof group (engine included),
// exactly like a custom-core death.
func TestUpstreamTransportDeathTearsDownSpoofGroup(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SNI spoof engine is Windows-only")
	}
	rig := newEngineRig(t, "upstream")
	if err := rig.cores.Start(spoofTestProfile(), rig.settings); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rig.cores.Stop()

	rig.upstream.mu.Lock()
	cmd := rig.upstream.cmd
	rig.upstream.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("upstream child not published")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill upstream child: %v", err)
	}
	waitFor(t, "upstream death", func() bool { return !rig.upstream.IsRunning() })
	waitFor(t, "group teardown", func() bool {
		rig.cores.mu.RLock()
		defer rig.cores.mu.RUnlock()
		return !rig.cores.running
	})
	waitFor(t, "spoof engine teardown", func() bool { return !rig.spoof.IsRunning() })
}

// TestFirstFileInPrefersBinFolder pins the lookup order: bin\ first, the
// legacy exe folder second, name order within a folder.
func TestFirstFileInPrefersBinFolder(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "nekobox_core.exe")
	inBin := filepath.Join(bin, "nekobox_core.exe")
	for _, p := range []string{legacy, inBin} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := firstFileIn([]string{bin, root}, "nekobox_core.exe", "nekobox_core")
	if !ok || got != inBin {
		t.Fatalf("picked %q (ok=%v), want the bin\\ copy %q", got, ok, inBin)
	}
	if got, ok := firstFileIn([]string{bin, root}, "xray.exe"); ok {
		t.Fatalf("picked %q for a missing name", got)
	}
	if err := os.Remove(inBin); err != nil {
		t.Fatal(err)
	}
	if got, ok := firstFileIn([]string{bin, root}, "nekobox_core.exe"); !ok || got != legacy {
		t.Fatalf("legacy fallback picked %q (ok=%v), want %q", got, ok, legacy)
	}
}

// TestEngineInstallDirIsBinFolder: updater installs land in bin\.
func TestEngineInstallDirIsBinFolder(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	want := filepath.Join(filepath.Dir(exe), binDirName)
	if got := engineInstallDir(); got != want {
		t.Fatalf("engineInstallDir = %q, want %q", got, want)
	}
	if got := coreExeDir(); got != want {
		t.Fatalf("coreExeDir = %q, want %q", got, want)
	}
}
