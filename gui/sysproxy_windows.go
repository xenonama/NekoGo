//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// Windows OS proxy writer for the GUI-owned system proxy.
//
// Writes the WinINET proxy keys directly from the GUI process — no
// core RPC. Only stdlib + golang.org/x/sys/windows.

// sysproxyKeyPath is the registry subkey used. A variable (not a const)
// so tests can point it at a scratch subkey and never touch the real
// HKCU Internet Settings.
var sysproxyKeyPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// sysproxyMarkerOverride pins the marker file path in tests.
var sysproxyMarkerOverride = ""

// sysproxyState stores the pre-set OS proxy state once per session.
type sysproxyState struct {
	enable       uint64
	server       string
	override     string
	haveEnable   bool
	haveServer   bool
	haveOverride bool
}

var (
	sysproxyMu     sync.Mutex
	sysproxyPrev   sysproxyState
	sysproxyCached bool
	sysproxyIsSet  bool
)

// sysproxyMarkerPath returns the crash-recovery marker file:
// %APPDATA%/NekoGo/.sysproxy_set (overridden in tests).
func sysproxyMarkerPath() string {
	if sysproxyMarkerOverride != "" {
		return sysproxyMarkerOverride
	}
	d, err := defaultConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, ".sysproxy_set")
}

// sysproxyMarkerState is the crash-recovery record: the OS proxy values
// the GUI replaced. A crashed session restores THESE on the next launch.
// The marker used to carry just the word "set", so recovery could only
// disable the proxy and leave our dead 127.0.0.1:<port> behind.
type sysproxyMarkerState struct {
	HaveEnable   bool   `json:"haveEnable"`
	Enable       uint64 `json:"enable"`
	HaveServer   bool   `json:"haveServer"`
	Server       string `json:"server"`
	HaveOverride bool   `json:"haveOverride"`
	Override     string `json:"override"`
}

// sysproxyWriteMarker records the cached previous state. Callers hold
// sysproxyMu.
func sysproxyWriteMarker() {
	p := sysproxyMarkerPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	st := sysproxyMarkerState{
		HaveEnable:   sysproxyPrev.haveEnable,
		Enable:       sysproxyPrev.enable,
		HaveServer:   sysproxyPrev.haveServer,
		Server:       sysproxyPrev.server,
		HaveOverride: sysproxyPrev.haveOverride,
		Override:     sysproxyPrev.override,
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

// readSysproxyMarker loads a marker written by sysproxyWriteMarker.
// ok=false for a legacy "set" marker (nothing to restore).
func readSysproxyMarker() (sysproxyMarkerState, bool) {
	p := sysproxyMarkerPath()
	if p == "" {
		return sysproxyMarkerState{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return sysproxyMarkerState{}, false
	}
	var st sysproxyMarkerState
	if err := json.Unmarshal(raw, &st); err != nil {
		return sysproxyMarkerState{}, false
	}
	return st, true
}

func sysproxyRemoveMarker() {
	p := sysproxyMarkerPath()
	if p == "" {
		return
	}
	_ = os.Remove(p)
}

func sysproxyMarkerExists() bool {
	p := sysproxyMarkerPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// refreshWinINET notifies WinINET so the new proxy applies without an
// app restart. Via LazyDLL so no new modules are needed.
func refreshWinINET() {
	const (
		settingsChanged = 39
		refresh         = 37
	)
	dll := syscall.NewLazyDLL("wininet.dll")
	proc := dll.NewProc("InternetSetOptionW")
	_, _, _ = proc.Call(0, uintptr(settingsChanged), 0, 0)
	_, _, _ = proc.Call(0, uintptr(refresh), 0, 0)
}

// sysproxyApply sets the OS proxy for the given core ports, caching
// the previous OS values on the first call of the session.
func sysproxyApply(p ProxyPorts) error {
	sysproxyMu.Lock()
	defer sysproxyMu.Unlock()

	if !sysproxyCached {
		key, err := registry.OpenKey(registry.CURRENT_USER, sysproxyKeyPath, registry.QUERY_VALUE)
		if err != nil {
			return fmt.Errorf("open internet settings: %w", err)
		}
		if v, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
			sysproxyPrev.enable, sysproxyPrev.haveEnable = v, true
		}
		if v, _, err := key.GetStringValue("ProxyServer"); err == nil {
			sysproxyPrev.server, sysproxyPrev.haveServer = v, true
		}
		if v, _, err := key.GetStringValue("ProxyOverride"); err == nil {
			sysproxyPrev.override, sysproxyPrev.haveOverride = v, true
		}
		key.Close()
		sysproxyCached = true
	}

	key, err := registry.OpenKey(registry.CURRENT_USER, sysproxyKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open internet settings for write: %w", err)
	}
	defer key.Close()
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("set ProxyEnable: %w", err)
	}
	if err := key.SetStringValue("ProxyServer", proxyServerValue(p)); err != nil {
		return fmt.Errorf("set ProxyServer: %w", err)
	}
	if !sysproxyPrev.haveOverride {
		if err := key.SetStringValue("ProxyOverride", "<local>"); err != nil {
			return fmt.Errorf("set ProxyOverride: %w", err)
		}
	}
	refreshWinINET()
	sysproxyIsSet = true
	sysproxyWriteMarker()
	return nil
}

// sysproxyRestore reverts the OS proxy to the cached values (or
// disables it when nothing was cached). Only acts when we set it
// (in-memory flag OR the crash-recovery marker) — never clears a
// proxy we did not set.
func sysproxyRestore() error {
	sysproxyMu.Lock()
	defer sysproxyMu.Unlock()

	if !sysproxyIsSet && !sysproxyMarkerExists() {
		return nil
	}
	if !sysproxyCached {
		// Crash recovery in a fresh process: the marker carries the values
		// the crashed session replaced. A legacy "set" marker (older
		// builds) carries none — fall back to the disable-only path.
		if st, ok := readSysproxyMarker(); ok {
			sysproxyPrev = sysproxyState{
				enable:       st.Enable,
				server:       st.Server,
				override:     st.Override,
				haveEnable:   st.HaveEnable,
				haveServer:   st.HaveServer,
				haveOverride: st.HaveOverride,
			}
			sysproxyCached = true
		}
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, sysproxyKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open internet settings for write: %w", err)
	}
	defer key.Close()
	if sysproxyCached {
		if sysproxyPrev.haveEnable {
			if err := key.SetDWordValue("ProxyEnable", uint32(sysproxyPrev.enable)); err != nil {
				return fmt.Errorf("restore ProxyEnable: %w", err)
			}
		} else {
			if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
				return fmt.Errorf("restore ProxyEnable: %w", err)
			}
		}
		if sysproxyPrev.haveServer {
			if err := key.SetStringValue("ProxyServer", sysproxyPrev.server); err != nil {
				return fmt.Errorf("restore ProxyServer: %w", err)
			}
		} else {
			_ = key.DeleteValue("ProxyServer")
		}
		if sysproxyPrev.haveOverride {
			if err := key.SetStringValue("ProxyOverride", sysproxyPrev.override); err != nil {
				return fmt.Errorf("restore ProxyOverride: %w", err)
			}
		}
	} else {
		if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
			return fmt.Errorf("restore ProxyEnable: %w", err)
		}
	}
	refreshWinINET()
	sysproxyIsSet = false
	sysproxyRemoveMarker()
	return nil
}

// cleanupStaleSysproxy is a best-effort startup cleanup: if a previous
// GUI session crashed with the proxy set (marker file present), clear
// it and remove the marker before any Connect. Never fails startup.
func cleanupStaleSysproxy() {
	if !sysproxyMarkerExists() {
		return
	}
	_ = sysproxyRestore()
}
