//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// isolateSysproxy points the helper at a scratch registry subkey and a
// temp marker file. Returns a restore func. Never touches the real
// HKCU Internet Settings.
func isolateSysproxy(t *testing.T) func() {
	t.Helper()
	sub := fmt.Sprintf(`Software\NekoGoTest\%s\Internet Settings`, strings.ReplaceAll(t.Name(), "/", "_"))
	oldPath := sysproxyKeyPath
	oldMarker := sysproxyMarkerOverride
	oldCached := sysproxyCached
	oldPrev := sysproxyPrev
	oldIsSet := sysproxyIsSet

	sysproxyKeyPath = sub
	sysproxyMarkerOverride = filepath.Join(t.TempDir(), ".sysproxy_set")
	sysproxyMu.Lock()
	sysproxyCached = false
	sysproxyPrev = sysproxyState{}
	sysproxyIsSet = false
	sysproxyMu.Unlock()

	// Ensure the scratch key exists with known previous values.
	key, _, err := registry.CreateKey(registry.CURRENT_USER, sub, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("create scratch key: %v", err)
	}
	_ = key.SetDWordValue("ProxyEnable", 0)
	_ = key.SetStringValue("ProxyServer", "prev-proxy:8888")
	key.Close()

	return func() {
		sysproxyMu.Lock()
		sysproxyKeyPath = oldPath
		sysproxyMarkerOverride = oldMarker
		sysproxyCached = oldCached
		sysproxyPrev = oldPrev
		sysproxyIsSet = oldIsSet
		sysproxyMu.Unlock()
		_ = registry.DeleteKey(registry.CURRENT_USER, sub)
		_ = registry.DeleteKey(registry.CURRENT_USER, fmt.Sprintf(`Software\NekoGoTest\%s`, strings.ReplaceAll(t.Name(), "/", "_")))
	}
}

// runningProxyApp builds an App whose core manager reports the given
// core as running. No core process starts: ActiveProxyPorts never
// dials, it only reads GUI-owned state.
func runningProxyApp(t *testing.T, mixedPort int, core CoreType) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.SystemProxy = true
	s.MixedPort = mixedPort
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	app.cores.mu.Lock()
	app.cores.activeCore = core
	app.cores.running = true
	app.cores.mu.Unlock()
	return app
}

func readScratchProxy(t *testing.T) (uint64, string) {
	t.Helper()
	key, err := registry.OpenKey(registry.CURRENT_USER, sysproxyKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open scratch key: %v", err)
	}
	defer key.Close()
	en, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil {
		t.Fatalf("read ProxyEnable: %v", err)
	}
	srv, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		t.Fatalf("read ProxyServer: %v", err)
	}
	return en, srv
}

// TestActiveProxyPortsSingbox: mixed=true, both ports equal
// Settings.MixedPort.
func TestActiveProxyPortsSingbox(t *testing.T) {
	app := runningProxyApp(t, 2280, CoreSingBox)
	p := app.ActiveProxyPorts()
	if !p.Mixed {
		t.Fatal("Mixed = false, want true for sing-box")
	}
	if p.HTTP != 2280 || p.SOCKS != 2280 {
		t.Fatalf("ports = %+v, want HTTP=SOCKS=2280", p)
	}
}

// TestActiveProxyPortsXray: mixed=false, HTTP and SOCKS are MixedPort
// and MixedPort+1.
func TestActiveProxyPortsXray(t *testing.T) {
	app := runningProxyApp(t, 2280, CoreXray)
	p := app.ActiveProxyPorts()
	if p.Mixed {
		t.Fatal("Mixed = true, want false for xray")
	}
	if p.HTTP != 2280 || p.SOCKS != 2281 {
		t.Fatalf("ports = %+v, want HTTP=2280 SOCKS=2281", p)
	}
}

// TestSetSystemProxyWritesRegistry: using the scratch subkey, assert
// ProxyEnable=1 and ProxyServer contains the right port(s) for both
// the mixed (sing-box) and split (xray) layouts.
func TestSetSystemProxyWritesRegistry(t *testing.T) {
	t.Run("mixed", func(t *testing.T) {
		restore := isolateSysproxy(t)
		defer restore()
		app := runningProxyApp(t, 2080, CoreSingBox)
		if err := app.SetSystemProxy(app.ActiveProxyPorts()); err != nil {
			t.Fatalf("SetSystemProxy: %v", err)
		}
		en, srv := readScratchProxy(t)
		if en != 1 {
			t.Fatalf("ProxyEnable = %d, want 1", en)
		}
		if srv != "127.0.0.1:2080" {
			t.Fatalf("ProxyServer = %q, want %q", srv, "127.0.0.1:2080")
		}
	})
	t.Run("split", func(t *testing.T) {
		restore := isolateSysproxy(t)
		defer restore()
		app := runningProxyApp(t, 2080, CoreXray)
		if err := app.SetSystemProxy(app.ActiveProxyPorts()); err != nil {
			t.Fatalf("SetSystemProxy: %v", err)
		}
		en, srv := readScratchProxy(t)
		if en != 1 {
			t.Fatalf("ProxyEnable = %d, want 1", en)
		}
		// The OS proxy is HTTP-only (WinINET tunnels HTTPS with CONNECT).
		// A "host:2080;host:2081" pair was read as a failover list by
		// Windows, which then showed it as the Address with an empty Port.
		if srv != "127.0.0.1:2080" {
			t.Fatalf("ProxyServer = %q, want the single HTTP entry 127.0.0.1:2080", srv)
		}
	})
}

// TestClearSystemProxyRestoresPrevious: set then clear, assert the
// previous values return and the marker is gone.
func TestClearSystemProxyRestoresPrevious(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	app := runningProxyApp(t, 2080, CoreSingBox)
	if err := app.SetSystemProxy(app.ActiveProxyPorts()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := app.ClearSystemProxy(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	en, srv := readScratchProxy(t)
	if en != 0 {
		t.Fatalf("ProxyEnable after clear = %d, want 0", en)
	}
	if srv != "prev-proxy:8888" {
		t.Fatalf("ProxyServer after clear = %q, want %q", srv, "prev-proxy:8888")
	}
	if sysproxyMarkerExists() {
		t.Fatal("marker file still exists after clear")
	}
}

// TestSystemProxyNoCoreIsNoop: ActiveProxyPorts with no core returns
// zero, and SetSystemProxy with zero ports is a no-op (no error, no
// registry write, no marker).
func TestSystemProxyNoCoreIsNoop(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	p := app.ActiveProxyPorts()
	if p != (ProxyPorts{}) {
		t.Fatalf("ports with no core = %+v, want zero", p)
	}
	if err := app.SetSystemProxy(p); err != nil {
		t.Fatalf("SetSystemProxy(zero) = %v, want nil", err)
	}
	en, srv := readScratchProxy(t)
	if en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("scratch key touched by no-op: (%d, %q)", en, srv)
	}
	if sysproxyMarkerExists() {
		t.Fatal("marker file created by no-op set")
	}
}

// TestCrashRecoveryClearsStaleProxy: create the marker via a set,
// simulate a restart (drop the in-memory cache, keep the marker),
// run the startup cleanup, and assert the PREVIOUS OS proxy values are
// restored (state the marker now carries) and the marker is gone.
func TestCrashRecoveryClearsStaleProxy(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	// A user proxy was enabled before NekoGo took over.
	if err := setScratchProxyEnable(t, 1); err != nil {
		t.Fatal(err)
	}
	app := runningProxyApp(t, 2080, CoreXray)
	if err := app.SetSystemProxy(app.ActiveProxyPorts()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !sysproxyMarkerExists() {
		t.Fatal("marker file missing after set")
	}
	// Simulate a process restart: in-memory session state is gone,
	// the marker file and the OS proxy survive.
	sysproxyMu.Lock()
	sysproxyCached = false
	sysproxyPrev = sysproxyState{}
	sysproxyIsSet = false
	sysproxyMu.Unlock()

	cleanupStaleSysproxy()

	if sysproxyMarkerExists() {
		t.Fatal("marker file still exists after startup cleanup")
	}
	en, srv := readScratchProxy(t)
	if en != 1 {
		t.Fatalf("ProxyEnable after cleanup = %d, want the previous 1", en)
	}
	if srv != "prev-proxy:8888" {
		t.Fatalf("ProxyServer after cleanup = %q, want the previous value restored", srv)
	}
}

// TestCrashRecoveryLegacyMarker: an old "set" marker carries no previous
// values, so recovery keeps the disable-only behaviour instead of
// guessing.
func TestCrashRecoveryLegacyMarker(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	if err := sysproxyApply(ProxyPorts{HTTP: 2080, SOCKS: 2080, Mixed: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sysproxyMarkerPath(), []byte("set"), 0o644); err != nil {
		t.Fatal(err)
	}
	sysproxyMu.Lock()
	sysproxyCached = false
	sysproxyPrev = sysproxyState{}
	sysproxyIsSet = false
	sysproxyMu.Unlock()

	cleanupStaleSysproxy()

	if en, _ := readScratchProxy(t); en != 0 {
		t.Fatalf("ProxyEnable after legacy cleanup = %d, want 0", en)
	}
}

func setScratchProxyEnable(t *testing.T, v uint32) error {
	t.Helper()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, sysproxyKeyPath, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetDWordValue("ProxyEnable", v)
}

func readScratchOverride(t *testing.T) (string, bool) {
	t.Helper()
	key, err := registry.OpenKey(registry.CURRENT_USER, sysproxyKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open scratch key: %v", err)
	}
	defer key.Close()
	v, _, err := key.GetStringValue("ProxyOverride")
	if err != nil {
		return "", false
	}
	return v, true
}

func setScratchProxyOverride(t *testing.T, v string) error {
	t.Helper()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, sysproxyKeyPath, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue("ProxyOverride", v)
}

// TestSystemProxyOverrideRoundTrip: "<local>" is a value NekoGo invents
// so the loopback stays reachable. It used to be written on apply and
// never removed on restore — one session permanently edited the user's
// exception list, even though the ProxyServer case right next to it
// already deleted what it had created.
func TestSystemProxyOverrideRoundTrip(t *testing.T) {
	t.Run("created then deleted", func(t *testing.T) {
		restore := isolateSysproxy(t)
		defer restore()
		if err := sysproxyApply(ProxyPorts{HTTP: 2080, SOCKS: 2080, Mixed: true}); err != nil {
			t.Fatal(err)
		}
		if v, ok := readScratchOverride(t); !ok || v != "<local>" {
			t.Fatalf("apply override = %q/%v, want \"<local>\"", v, ok)
		}
		if err := sysproxyRestore(); err != nil {
			t.Fatal(err)
		}
		if v, ok := readScratchOverride(t); ok {
			t.Fatalf("restore left ProxyOverride=%q, want the value gone (the user had none)", v)
		}
	})
	t.Run("the user's own list survives", func(t *testing.T) {
		restore := isolateSysproxy(t)
		defer restore()
		if err := setScratchProxyOverride(t, "10.0.0.0/8"); err != nil {
			t.Fatal(err)
		}
		if err := sysproxyApply(ProxyPorts{HTTP: 2080, SOCKS: 2080, Mixed: true}); err != nil {
			t.Fatal(err)
		}
		if err := sysproxyRestore(); err != nil {
			t.Fatal(err)
		}
		if v, ok := readScratchOverride(t); !ok || v != "10.0.0.0/8" {
			t.Fatalf("restore override = %q/%v, want the user's list back untouched", v, ok)
		}
	})
}

// TestApplyOrderNeverEnablesAStaleServer: the commit order is only a
// claim until one write is failed on purpose. ProxyServer is what the
// switch reads, so a session that cannot install it must leave
// ProxyEnable exactly where the user had it — enabling first, as the
// old order did, handed WinINET a switched-on proxy at an address it
// never finished writing.
func TestApplyOrderNeverEnablesAStaleServer(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	oldWrite := sysproxyWriteString
	defer func() { sysproxyWriteString = oldWrite }()
	sysproxyWriteString = func(k registry.Key, name, v string) error {
		if name == "ProxyServer" {
			return fmt.Errorf("injected ProxyServer failure")
		}
		return oldWrite(k, name, v)
	}
	app := runningProxyApp(t, 2080, CoreSingBox)
	if err := app.SetSystemProxy(app.ActiveProxyPorts()); err == nil {
		t.Fatal("SetSystemProxy with a failing ProxyServer write = nil, want an error")
	}
	if en, srv := readScratchProxy(t); en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("partial apply left ProxyEnable=%d ProxyServer=%q, want the user's pair intact", en, srv)
	}
	if sysproxyOwned() {
		t.Fatal("a failed apply claimed ownership of the OS proxy")
	}
	if sysproxyMarkerExists() {
		t.Fatal("a failed apply wrote the crash-recovery marker")
	}
}

// TestSysproxyOwnedTracksApplyAndRestore: ownership is what the UI and
// the exit revert key off, so it must appear on apply and vanish on
// restore — and a fresh process must not start out owning it.
func TestSysproxyOwnedTracksApplyAndRestore(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	if sysproxyOwned() {
		t.Fatal("a fresh process already owns the OS proxy")
	}
	if err := sysproxyApply(ProxyPorts{HTTP: 2080, SOCKS: 2080, Mixed: true}); err != nil {
		t.Fatal(err)
	}
	if !sysproxyOwned() {
		t.Fatal("apply did not claim ownership")
	}
	if err := sysproxyRestore(); err != nil {
		t.Fatal(err)
	}
	if sysproxyOwned() {
		t.Fatal("restore still claims ownership")
	}
}

// TestExitRevertsManuallyEnabledProxy: the toggle path records no
// autoProxyOn, which is precisely why Shutdown and noteFault used to
// skip it — so after a crash or a quit the browser stayed pointed at
// our closed port until NekoGo happened to start again.
func TestExitRevertsManuallyEnabledProxy(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	app := runningProxyApp(t, 2080, CoreSingBox)
	if err := app.EnableSystemProxyGUI(); err != nil {
		t.Fatalf("EnableSystemProxyGUI: %v", err)
	}
	if app.getAutoProxy() {
		t.Fatal("the manual path set autoProxyOn, which is not its job")
	}
	if en, srv := readScratchProxy(t); en != 1 || srv != "127.0.0.1:2080" {
		t.Fatalf("after manual enable ProxyEnable=%d ProxyServer=%q", en, srv)
	}

	app.revertSystemProxyOnExit("test")

	if en, srv := readScratchProxy(t); en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("after exit revert ProxyEnable=%d ProxyServer=%q, want the user's pair back", en, srv)
	}
	if sysproxyMarkerExists() {
		t.Fatal("exit revert left the crash marker behind")
	}
	if !app.store.GetSettings().SystemProxy {
		t.Fatal("exit revert cleared the remembered setting: Disconnect owns that, not shutdown")
	}
}

// TestExitLeavesAForeignProxyAlone: the widen-from-autoProxyOn-to-
// ownership change must not turn into "clear whatever the OS says",
// which would silently switch off a corporate proxy on quit.
func TestExitLeavesAForeignProxyAlone(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	if err := setScratchProxyEnable(t, 1); err != nil {
		t.Fatal(err)
	}
	app := runningProxyApp(t, 2080, CoreSingBox)
	app.revertSystemProxyOnExit("test")
	if en, srv := readScratchProxy(t); en != 1 || srv != "prev-proxy:8888" {
		t.Fatalf("exit reverted a proxy it never installed: (%d, %q)", en, srv)
	}
}

// failingConnectApp seeds an owned OS proxy on a running core, then
// arranges for the next Connect to fail. coreStillRunning=false models
// the real Start, which stops the previous group before probing the new
// one; true models the epoch-abort loser, where the winner's core lives.
func failingConnectApp(t *testing.T, coreStillRunning bool) (*App, Profile) {
	t.Helper()
	t.Cleanup(isolateSysproxy(t))
	app := runningProxyApp(t, 2080, CoreSingBox)
	prof, err := app.store.SaveProfile(Profile{Name: "p", Core: CoreSingBox, Address: "203.0.113.7", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetActiveProfileID(prof.ID); err != nil {
		t.Fatal(err)
	}
	app.cores.mu.Lock()
	app.cores.activeID = prof.ID
	app.cores.mu.Unlock()
	if err := app.EnableSystemProxyGUI(); err != nil {
		t.Fatalf("EnableSystemProxyGUI: %v", err)
	}
	if en, srv := readScratchProxy(t); en != 1 || srv != "127.0.0.1:2080" {
		t.Fatalf("seed ProxyEnable=%d ProxyServer=%q", en, srv)
	}
	if !coreStillRunning {
		app.cores.mu.Lock()
		app.cores.running = false
		app.cores.mu.Unlock()
	}
	app.coreStarter = func(*Profile, Settings) error { return fmt.Errorf("probe failed") }
	return app, prof
}

// TestFailedConnectRevertsOwnedProxy: Start tears the previous group
// down before probing this one, and a deliberate stop reports no
// EngineExit — so noteFault never ran and a failed connect left the OS
// proxy pointing at a closed port until Disconnect or a later success.
func TestFailedConnectRevertsOwnedProxy(t *testing.T) {
	app, prof := failingConnectApp(t, false)
	if err := app.Connect(prof.ID); err == nil {
		t.Fatal("Connect succeeded with a failing starter")
	}
	if en, srv := readScratchProxy(t); en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("after failed connect ProxyEnable=%d ProxyServer=%q, want the user's pair back", en, srv)
	}
	if sysproxyMarkerExists() {
		t.Fatal("failed connect left the crash marker behind")
	}
	if app.getAutoProxy() {
		t.Fatal("autoProxyOn still true with no core running")
	}
}

// TestFailedConnectKeepsProxyWhenACoreStillRuns: the revert must stay
// conditional. An epoch-abort loser returns an error while the winner's
// core is listening on that very port; clearing the proxy there would
// break a connection that actually succeeded.
func TestFailedConnectKeepsProxyWhenACoreStillRuns(t *testing.T) {
	app, prof := failingConnectApp(t, true)
	if err := app.Connect(prof.ID); err == nil {
		t.Fatal("Connect succeeded with a failing starter")
	}
	if en, srv := readScratchProxy(t); en != 1 || srv != "127.0.0.1:2080" {
		t.Fatalf("failed connect cleared a live core's proxy: (%d, %q)", en, srv)
	}
	if !sysproxyMarkerExists() {
		t.Fatal("failed connect dropped ownership of a live proxy")
	}
}

// TestFailedConnectLeavesAForeignProxyAlone: the failed-connect revert
// reuses the ownership rule, so a corporate proxy this process never
// installed must survive a connect that could not start anything.
func TestFailedConnectLeavesAForeignProxyAlone(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	if err := setScratchProxyEnable(t, 1); err != nil {
		t.Fatal(err)
	}
	app := runningProxyApp(t, 2080, CoreSingBox)
	prof, err := app.store.SaveProfile(Profile{Name: "p", Core: CoreSingBox, Address: "203.0.113.7", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	app.cores.mu.Lock()
	app.cores.running = false
	app.cores.mu.Unlock()
	app.coreStarter = func(*Profile, Settings) error { return fmt.Errorf("probe failed") }
	if err := app.Connect(prof.ID); err == nil {
		t.Fatal("Connect succeeded with a failing starter")
	}
	if en, srv := readScratchProxy(t); en != 1 || srv != "prev-proxy:8888" {
		t.Fatalf("failed connect reverted a proxy it never installed: (%d, %q)", en, srv)
	}
}

// TestFailedReloadRevertsOwnedProxy: ReloadCoreNow stops the running core
// itself and then restarts it, so a failed restart leaves the same dead
// port as a failed Connect — and the same absence of an EngineExit, since
// the stop was deliberate.
func TestFailedReloadRevertsOwnedProxy(t *testing.T) {
	app, _ := failingConnectApp(t, true)
	if err := app.ReloadCoreNow(); err == nil {
		t.Fatal("ReloadCoreNow succeeded with a failing starter")
	}
	if _, _, running := app.cores.Status(); running {
		t.Fatal("Stop did not run before the failing restart")
	}
	if en, srv := readScratchProxy(t); en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("after failed reload ProxyEnable=%d ProxyServer=%q, want the user's pair back", en, srv)
	}
	if sysproxyMarkerExists() {
		t.Fatal("failed reload left the crash marker behind")
	}
}
