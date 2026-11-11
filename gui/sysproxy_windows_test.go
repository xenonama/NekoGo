//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func setScratchProxyServer(t *testing.T, v string) error {
	t.Helper()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, sysproxyKeyPath, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue("ProxyServer", v)
}

// readScratchProxyLoose is readScratchProxy for the cases where a value
// is legitimately gone: a reverted ProxyServer is deleted, not emptied.
func readScratchProxyLoose(t *testing.T) (uint64, string, bool) {
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
		return en, "", false
	}
	return en, srv, true
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
	// The ProxyOverride write DID land, so this apply is not a no-op: the
	// record has to survive and the revert has to take the "<local>" we
	// installed back out. An earlier design deleted the marker here,
	// which left our exception-list edit in the user's settings for good.
	if !sysproxyMarkerExists() {
		t.Fatal("a partial apply kept no crash record: the ProxyOverride it wrote is now permanent")
	}
	if _, ok := readScratchOverride(t); !ok {
		t.Fatal("setup: the partial apply wrote no ProxyOverride, so there is nothing to check")
	}
	// Release the injection: the revert below is the thing under test,
	// not another victim of it.
	sysproxyWriteString = oldWrite
	if err := sysproxyRestore(); err != nil {
		t.Fatal(err)
	}
	if _, ok := readScratchOverride(t); ok {
		t.Fatal("restore left our ProxyOverride behind after a partial apply")
	}
	if en, srv := readScratchProxy(t); en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("restore after a partial apply changed the user's pair: ProxyEnable=%d ProxyServer=%q", en, srv)
	}
	if sysproxyOwned() {
		t.Fatal("restore left the OS proxy claimed as ours")
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

// TestApplyRecapturesAProxyChangedWhileWeWereOff: the previous OS proxy
// is what the revert puts back, so it has to be read at every take-over.
// One capture per process meant the SECOND connect/disconnect cycle
// restored (or deleted) the value cached at the first — a user who moves
// to another proxy while NekoGo is disconnected loses it on the next
// Disconnect.
func TestApplyRecapturesAProxyChangedWhileWeWereOff(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	ports := ProxyPorts{HTTP: 2080, SOCKS: 2080, Mixed: true}
	if err := sysproxyApply(ports); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := sysproxyRestore(); err != nil {
		t.Fatalf("first restore: %v", err)
	}
	// NekoGo is idle; the user (or IT) moves to a new proxy.
	if err := setScratchProxyServer(t, "corp2.example:3128"); err != nil {
		t.Fatal(err)
	}
	if err := setScratchProxyEnable(t, 1); err != nil {
		t.Fatal(err)
	}
	if err := sysproxyApply(ports); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if err := sysproxyRestore(); err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if en, srv := readScratchProxy(t); en != 1 || srv != "corp2.example:3128" {
		t.Fatalf("second revert gave back ProxyEnable=%d ProxyServer=%q, want the proxy the user actually had (1, corp2.example:3128)", en, srv)
	}
}

// TestAdoptedStaleProxyIsClearedNotRestored: the OS switch already reads
// NekoGo's own address when this process takes the proxy — a leftover
// from NekoBox on the same default 2080, from a crash whose marker is
// gone, or typed by hand. Connect used to treat that as "already on,
// nothing to do" and claim ownership anyway, so the later
// ClearSystemProxy refused to act (no flag, no marker) and reported
// success: the machine kept ProxyEnable=1 pointed at the port that had
// just closed, i.e. no internet until the registry is fixed by hand.
func TestAdoptedStaleProxyIsClearedNotRestored(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	oldReader := sysProxyReader
	defer func() { sysProxyReader = oldReader }()
	sysProxyReader = func() (bool, string, error) {
		en, srv, _ := readScratchProxyLoose(t)
		return en != 0, srv, nil
	}
	if err := setScratchProxyServer(t, "127.0.0.1:2080"); err != nil {
		t.Fatal(err)
	}
	if err := setScratchProxyEnable(t, 1); err != nil {
		t.Fatal(err)
	}
	app := runningProxyApp(t, 2080, CoreSingBox)
	if err := app.ensureSystemProxy(app.ActiveProxyPorts()); err != nil {
		t.Fatalf("ensureSystemProxy: %v", err)
	}
	if !sysproxyOwned() {
		t.Fatal("ensure adopted a proxy this process did not install without recording ownership: the revert below has nothing to undo")
	}
	if err := app.ClearSystemProxy(); err != nil {
		t.Fatalf("ClearSystemProxy: %v", err)
	}
	en, srv, haveSrv := readScratchProxyLoose(t)
	if en != 0 {
		t.Fatalf("revert left ProxyEnable=%d pointed at the closed port (ProxyServer=%q)", en, srv)
	}
	if haveSrv && srv == "127.0.0.1:2080" {
		t.Fatal("revert wrote NekoGo's own address back as the previous proxy")
	}
}

// A crash report can describe an engine that a newer connect already
// replaced: handleEngineExit flips the group state, then a Start commits
// while the report is still in flight. noteFault is the App-side observer
// and used to tear the session's system proxy down on sight, so browsers
// stopped using the tunnel while the app still reported it connected and
// traffic still counted.
func TestEngineFaultKeepsALiveSessionsProxy(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	app := runningProxyApp(t, 2080, CoreSpoof)
	if err := app.EnableSystemProxyGUI(); err != nil {
		t.Fatalf("EnableSystemProxyGUI: %v", err)
	}
	if en, srv := readScratchProxy(t); en != 1 || srv != "127.0.0.1:2080" {
		t.Fatalf("rig is wrong: ProxyEnable=%d ProxyServer=%q", en, srv)
	}

	// The superseding connect commits inside the fault window.
	app.cores.faultRaceHook = func() {
		app.cores.mu.Lock()
		app.cores.epoch++
		app.cores.running = true
		app.cores.activeID = "p2"
		app.cores.mu.Unlock()
	}
	defer func() { app.cores.faultRaceHook = nil }()

	app.cores.handleEngineExit(newEngineExit("spoof", nil, errors.New("engine died"), time.Now().Add(-time.Minute), nil))

	if en, srv := readScratchProxy(t); en != 1 || srv != "127.0.0.1:2080" {
		t.Fatalf("a superseded engine's death took the live session's proxy off: ProxyEnable=%d ProxyServer=%q", en, srv)
	}
	if msg := app.LastError(); msg != "" {
		t.Fatalf("the live session was shown a disconnect banner for an engine it no longer uses: %q", msg)
	}
}

// The other half: with nothing live the report IS the disconnect, and the
// proxy has to come off — the tunnel's listener is gone.
func TestEngineFaultWithNoLiveSessionStillRevertsTheProxy(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	app := runningProxyApp(t, 2080, CoreSpoof)
	if err := app.EnableSystemProxyGUI(); err != nil {
		t.Fatalf("EnableSystemProxyGUI: %v", err)
	}
	// handleEngineExit flips the group state before notifying, which is
	// what "nothing is live any more" looks like from here.
	app.cores.mu.Lock()
	app.cores.running = false
	app.cores.activeID = ""
	app.cores.mu.Unlock()

	app.noteFault(newEngineExit("spoof", nil, errors.New("engine died"), time.Now().Add(-time.Minute), nil))

	if en, _ := readScratchProxy(t); en != 0 {
		t.Fatalf("ProxyEnable=%d after a real engine death: traffic keeps dialing a closed listener", en)
	}
	if msg := app.LastError(); msg == "" {
		t.Fatal("the engine death left no banner: the user cannot tell why it disconnected")
	}
}

// TestMarkerWriteFailureKeepsTheOldRecord: the marker is the ONLY
// record of the OS proxy values the GUI replaced, so an interrupted
// write — a process killed mid-flush, or the ENOSPC this machine hits
// daily — must not leave the user's own proxy unrecoverable. The record
// is a whole value: it either reaches the disk complete or the previous
// complete one stays readable.
func TestMarkerWriteFailureKeepsTheOldRecord(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()

	sysproxyMu.Lock()
	sysproxyCached = true
	sysproxyPrev = sysproxyState{enable: 1, haveEnable: true, server: "corp:8888", haveServer: true}
	sysproxyMu.Unlock()
	sysproxyWriteMarker()
	if st, ok := readSysproxyMarker(); !ok || st.Server != "corp:8888" {
		t.Fatalf("setup: marker = %+v (readable=%v)", st, ok)
	}

	old := sysproxyMarkerWrite
	// Exactly what a crash during the write leaves behind: some bytes
	// reached the disk, the operation never completed.
	sysproxyMarkerWrite = func(p string, data []byte, perm os.FileMode) error {
		_ = os.WriteFile(p, data[:len(data)/2], perm)
		return errors.New("write interrupted")
	}
	sysproxyMu.Lock()
	sysproxyPrev = sysproxyState{enable: 0, haveEnable: true, server: "next:1", haveServer: true}
	sysproxyMu.Unlock()
	sysproxyWriteMarker()
	sysproxyMarkerWrite = old

	st, ok := readSysproxyMarker()
	if !ok {
		t.Fatal("the interrupted write left no readable marker: the user's own proxy is gone, and the next restore can only disable")
	}
	if st.Server != "corp:8888" {
		t.Fatalf("marker now records %q, want the previous complete record %q", st.Server, "corp:8888")
	}
}

// TestProxyIsNeverInstalledWithoutACrashRecord: the registry commit and
// the marker used to be ordered the wrong way round — ProxyServer and
// ProxyEnable went to the OS first, and the record of what they
// replaced went second, unchecked. An apply interrupted in that window
// (marker write fails, or the process dies) leaves the machine pointing
// at our 127.0.0.1:<port> with nothing marking it as ours, so the next
// launch treats a dead proxy as somebody else's setting and leaves it
// alone for good.
func TestProxyIsNeverInstalledWithoutACrashRecord(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	old := sysproxyMarkerWrite
	sysproxyMarkerWrite = func(string, []byte, os.FileMode) error {
		return errors.New("no space left on device")
	}
	defer func() { sysproxyMarkerWrite = old }()

	app := runningProxyApp(t, 2080, CoreSingBox)
	err := app.EnableSystemProxyGUI()
	en, srv := readScratchProxy(t)
	if err == nil {
		t.Fatalf("a session that cannot record its own revert reported success: ProxyEnable=%d ProxyServer=%q is now unrecoverable by us", en, srv)
	}
	if en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("the OS proxy was changed even though the record failed: ProxyEnable=%d ProxyServer=%q", en, srv)
	}
}

// TestApplyFailingBeforeAnyWriteLeavesNoClaim: the marker moved in
// front of the registry commit, so the other half of the invariant needs
// its own test — an apply that changed nothing at all must not start
// claiming the user's proxy as ours. Losing that would make a failed
// toggle disable a corporate proxy at the next exit for nothing.
func TestApplyFailingBeforeAnyWriteLeavesNoClaim(t *testing.T) {
	restore := isolateSysproxy(t)
	defer restore()
	oldWrite := sysproxyWriteString
	defer func() { sysproxyWriteString = oldWrite }()
	sysproxyWriteString = func(k registry.Key, name, v string) error {
		if name == "ProxyOverride" {
			return fmt.Errorf("injected ProxyOverride failure")
		}
		return oldWrite(k, name, v)
	}
	app := runningProxyApp(t, 2080, CoreSingBox)
	if err := app.SetSystemProxy(app.ActiveProxyPorts()); err == nil {
		t.Fatal("SetSystemProxy with a failing first write = nil, want an error")
	}
	if en, srv := readScratchProxy(t); en != 0 || srv != "prev-proxy:8888" {
		t.Fatalf("registry changed despite the failure: ProxyEnable=%d ProxyServer=%q", en, srv)
	}
	if sysproxyMarkerExists() {
		t.Fatal("an apply that wrote nothing left a crash record claiming the user's proxy")
	}
	if sysproxyOwned() {
		t.Fatal("an apply that wrote nothing claimed ownership of the OS proxy")
	}
}
