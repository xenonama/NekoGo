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

// sysproxyMarkerWrite is how the marker bytes reach the disk. It exists
// so a test can stop a write half way through — the way a killed process
// or a full disk does — and then look at what the on-disk record is
// (same purpose as the registry write seams below).
var sysproxyMarkerWrite = os.WriteFile

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
// sysproxyMu. It writes through a temporary file and renames, so the
// destination only ever holds a complete record: an interrupted write
// costs the new value, never the old one the next launch has to restore.
func sysproxyWriteMarker() error {
	p := sysproxyMarkerPath()
	if p == "" {
		return nil
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
		return err
	}
	tmp := p + ".tmp"
	if err := sysproxyMarkerWrite(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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

// sysproxyOwned reports whether the OS proxy is OURS: installed by this
// process, or left behind by one that crashed (the marker is only ever
// removed by a successful restore). It is the ownership half of the
// answer the UI needs — ProxyEnable alone cannot tell our proxy from a
// corporate one. A var so app-level tests can pin ownership without a
// registry or a marker on disk.
var sysproxyOwned = func() bool {
	sysproxyMu.Lock()
	defer sysproxyMu.Unlock()
	return sysproxyIsSet || sysproxyMarkerExists()
}

// sysproxyWriteDWord/sysproxyWriteString/sysproxyDelete are the registry
// write seams. They exist so a test can fail ONE value mid-transaction
// and check which state the registry is left in: the commit orders below
// are only a claim until that is measured (see sysproxyApply).
var (
	sysproxyWriteDWord  = func(k registry.Key, name string, v uint32) error { return k.SetDWordValue(name, v) }
	sysproxyWriteString = func(k registry.Key, name, v string) error { return k.SetStringValue(name, v) }
	sysproxyDelete      = func(k registry.Key, name string) error { return k.DeleteValue(name) }
)

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

// sysproxyApply sets the OS proxy for the given core ports. The
// previous OS values are captured for the revert on every take-over,
// kept across a failed one so the original still comes back, and
// dropped again by sysproxyRestore (see there).
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
		// A previous state that already IS our target address is not
		// something to restore. That shape is a stale pointer at a NekoGo
		// port — NekoBox used the same default 2080, and a crashed session
		// whose marker did not survive reads the same — so keeping it would
		// have the revert put the dead address back instead of clearing it.
		// Drop only the enable/server pair: the user's own ProxyOverride is
		// still theirs and still comes back.
		if sysproxyPrev.haveEnable && sysproxyPrev.enable != 0 &&
			sysproxyPrev.haveServer && sysproxyPrev.server == proxyServerValue(p) {
			sysproxyPrev.enable, sysproxyPrev.haveEnable = 0, false
			sysproxyPrev.server, sysproxyPrev.haveServer = "", false
		}
		sysproxyCached = true
	}

	// The crash record goes down BEFORE the OS values change. It used to
	// be written last, and its error ignored: an apply that committed
	// ProxyServer and ProxyEnable and then died here (or hit a full disk)
	// left the machine dialing our 127.0.0.1:<port> with nothing marking
	// that address as ours, so the next launch read a foreign proxy and
	// the dead pointer stayed until the user found the setting by hand.
	hadMarker := sysproxyMarkerExists()
	if err := sysproxyWriteMarker(); err != nil {
		return fmt.Errorf("record the revert: %w", err)
	}
	// keep the invariant that a marker is only left behind when the
	// registry may really have changed: no change, no claim.
	rollback := func() {
		if !hadMarker {
			sysproxyRemoveMarker()
		}
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, sysproxyKeyPath, registry.SET_VALUE)
	if err != nil {
		rollback()
		return fmt.Errorf("open internet settings for write: %w", err)
	}
	defer key.Close()
	// Commit order: values first, ProxyEnable last. ProxyEnable is what
	// makes WinINET read ProxyServer at all, so a write that fails part
	// way through this list leaves the switch wherever the user had it —
	// never switched on at an address we did not finish installing. A
	// failure after the first value write keeps the marker: the registry
	// now holds something we did change, and the record is what puts it
	// back (the old order dropped it, leaking our "<local>" ProxyOverride
	// into the user's exception list for good).
	if !sysproxyPrev.haveOverride {
		if err := sysproxyWriteString(key, "ProxyOverride", "<local>"); err != nil {
			rollback()
			return fmt.Errorf("set ProxyOverride: %w", err)
		}
	}
	if err := sysproxyWriteString(key, "ProxyServer", proxyServerValue(p)); err != nil {
		return fmt.Errorf("set ProxyServer: %w", err)
	}
	if err := sysproxyWriteDWord(key, "ProxyEnable", 1); err != nil {
		return fmt.Errorf("set ProxyEnable: %w", err)
	}
	refreshWinINET()
	sysproxyIsSet = true
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
	// Mirror of the apply order: take the switch off first, so a write
	// that fails below leaves the proxy disabled rather than enabled at
	// our address, which no longer has a listener. The user's own switch
	// is put back last, once their addresses are home again.
	if err := sysproxyWriteDWord(key, "ProxyEnable", 0); err != nil {
		return fmt.Errorf("restore ProxyEnable: %w", err)
	}
	if sysproxyCached {
		if sysproxyPrev.haveServer {
			if err := sysproxyWriteString(key, "ProxyServer", sysproxyPrev.server); err != nil {
				return fmt.Errorf("restore ProxyServer: %w", err)
			}
		} else {
			_ = sysproxyDelete(key, "ProxyServer")
		}
		if sysproxyPrev.haveOverride {
			if err := sysproxyWriteString(key, "ProxyOverride", sysproxyPrev.override); err != nil {
				return fmt.Errorf("restore ProxyOverride: %w", err)
			}
		} else {
			// sysproxyApply created this value; the user had none, so
			// leaving it behind would be a permanent edit of their
			// exception list (the ProxyServer case above already
			// deleted what it had created).
			_ = sysproxyDelete(key, "ProxyOverride")
		}
		if sysproxyPrev.haveEnable && sysproxyPrev.enable != 0 {
			if err := sysproxyWriteDWord(key, "ProxyEnable", uint32(sysproxyPrev.enable)); err != nil {
				return fmt.Errorf("restore ProxyEnable: %w", err)
			}
		}
	}
	refreshWinINET()
	sysproxyIsSet = false
	// The captured snapshot has now been handed back, so it describes a
	// proxy the OS no longer has. Keeping it alive for the whole process
	// made the SECOND connect/disconnect cycle restore (or delete, when
	// the first capture found nothing) what was there at the FIRST — a
	// user who changed their proxy while NekoGo was off lost it on the
	// next Disconnect. The next apply re-reads instead.
	sysproxyCached = false
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
