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
