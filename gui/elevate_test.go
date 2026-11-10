package main

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// withElevated stubs the process-elevation probe for one test (the
// paths under test declare their side explicitly instead of
// depending on how the dev machine launched `go test`).
func withElevated(t *testing.T, elevated bool) {
	t.Helper()
	old := guiElevated
	guiElevated = func() (bool, error) { return elevated, nil }
	t.Cleanup(func() { guiElevated = old })
}

// withRelaunchStub captures elevated-relaunch requests and swallows
// the process exit so tests can assert the handoff.
func withRelaunchStub(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	oldSpawn := relaunchElevated
	oldExit := elevateExiter
	relaunchElevated = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	elevateExiter = func(code int) {}
	t.Cleanup(func() {
		relaunchElevated = oldSpawn
		elevateExiter = oldExit
	})
	return &calls
}

// discoveryTestApp builds an App on a temp store.
func discoveryTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewApp(store, make(chan LogEntry, 64))
}

func TestParseConnectArg(t *testing.T) {
	if got := parseConnectArg([]string{"--connect", "abc"}); got != "abc" {
		t.Fatalf("got %q, want abc", got)
	}
	if got := parseConnectArg([]string{"--connect"}); got != "" {
		t.Fatalf("dangling flag = %q, want empty", got)
	}
	if got := parseConnectArg([]string{"--other", "x"}); got != "" {
		t.Fatalf("unknown args = %q, want empty", got)
	}
	if got := parseConnectArg(nil); got != "" {
		t.Fatalf("nil = %q, want empty", got)
	}
}

func TestQuoteElevateArgs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ShellExecute arg quoting is a Windows path")
	}
	if got := quoteElevateArgs([]string{"--connect", "abc"}); got != "--connect abc" {
		t.Fatalf("got %q", got)
	}
	if got := quoteElevateArgs([]string{"--connect", "a b"}); got != `--connect "a b"` {
		t.Fatalf("got %q, want quoted", got)
	}
}

// TestRelaunchAsAdminAlreadyElevated: honest error, no spawn.
func TestRelaunchAsAdminAlreadyElevated(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("elevated relaunch is a Windows path")
	}
	withElevated(t, true)
	calls := withRelaunchStub(t)
	app := discoveryTestApp(t)
	if err := app.RelaunchAsAdmin(); err == nil {
		t.Fatal("RelaunchAsAdmin elevated = nil, want error")
	} else if !strings.Contains(strings.ToLower(err.Error()), "already") {
		t.Fatalf("error = %q, want already-elevated hint", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("relaunch calls = %v, want none", *calls)
	}
}

// TestConnectHonorsSystemProxySetting: the remembered setting gates
// the auto path — OFF leaves the OS proxy untouched on Connect.
func TestConnectHonorsSystemProxySetting(t *testing.T) {
	newHookedApp := func(t *testing.T, sysProxy bool) (*App, *[]bool) {
		t.Helper()
		store, err := NewConfigStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		s := DefaultSettings()
		s.SystemProxy = sysProxy
		if err := store.SaveSettings(s); err != nil {
			t.Fatal(err)
		}
		p, err := store.SaveProfile(Profile{Name: "x", Core: CoreSingBox,
			Protocol: "trojan", Address: "example.com", Port: 443,
			IDOrPassword: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		app := NewApp(store, make(chan LogEntry, 64))
		var calls []bool
		app.sysProxyHook = func(enable bool) error {
			calls = append(calls, enable)
			return nil
		}
		app.coreStarter = func(p *Profile, s Settings) error { return nil }
		if err := store.SetActiveProfileID(p.ID); err != nil {
			t.Fatal(err)
		}
		return app, &calls
	}
	t.Run("off skips auto proxy", func(t *testing.T) {
		app, calls := newHookedApp(t, false)
		prof, _ := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
		if err := app.Connect(prof.ID); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		for _, c := range *calls {
			if c {
				t.Fatalf("hook calls = %v, want no enable with SystemProxy=false", *calls)
			}
		}
	})
	t.Run("on ensures auto proxy", func(t *testing.T) {
		app, calls := newHookedApp(t, true)
		prof, _ := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
		if err := app.Connect(prof.ID); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		found := false
		for _, c := range *calls {
			if c {
				found = true
			}
		}
		if !found {
			t.Fatalf("hook calls = %v, want an enable with SystemProxy=true", *calls)
		}
	})
}

// tunConnectTestApp builds a TUN-enabled store with one profile and an
// App whose core start is stubbed, so the gate can be exercised
// without spawning a core.
func tunConnectTestApp(t *testing.T, core CoreType) (*App, string, *bool) {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.TunEnabled = true
	s.SystemProxy = false
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	p, err := store.SaveProfile(Profile{Name: "tun", Core: core,
		Protocol: "trojan", Address: "203.0.113.10", Port: 443,
		IDOrPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	started := false
	app.coreStarter = func(*Profile, Settings) error {
		started = true
		return nil
	}
	return app, p.ID, &started
}

// TestConnectTunWithoutAdminRefusesAndPersistsTarget: the gate must
// fail with the actionable message BEFORE the core starts, and it
// must leave the requested profile active so the elevated relaunch
// (--connect <id>) resumes it.
func TestConnectTunWithoutAdminRefusesAndPersistsTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN elevation gate is a Windows path")
	}
	withElevated(t, false)
	app, id, started := tunConnectTestApp(t, CoreSingBox)
	err := app.Connect(id)
	if err == nil {
		t.Fatal("Connect = nil, want TUN elevation error")
	}
	if !strings.Contains(err.Error(), "TUN mode requires administrator rights") {
		t.Fatalf("error = %q, want the administrator hint", err)
	}
	if *started {
		t.Fatal("core started despite unelevated TUN")
	}
	if got := app.store.GetSettings().ActiveProfileID; got != id {
		t.Fatalf("active profile = %q, want %q for the elevated resume", got, id)
	}
}

// TestConnectTunElevatedStartsCore: elevation is the only thing
// missing; with it the same Connect proceeds.
func TestConnectTunElevatedStartsCore(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN elevation gate is a Windows path")
	}
	withElevated(t, true)
	app, id, started := tunConnectTestApp(t, CoreSingBox)
	if err := app.Connect(id); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !*started {
		t.Fatal("core not started while elevated with TUN")
	}
}

// TestConnectTunIgnoredForXray: Xray never renders the TUN inbound, so
// a stale tunEnabled setting must not gate a proxy-only profile.
func TestConnectTunIgnoredForXray(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN elevation gate is a Windows path")
	}
	withElevated(t, false)
	app, id, started := tunConnectTestApp(t, CoreXray)
	if err := app.Connect(id); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !*started {
		t.Fatal("xray core not started; tunEnabled must not gate it")
	}
}

// TestConnectTunIgnoredForRawOverride: a full raw config replaces the
// rendered config including the TUN append, so the gate must not fire.
func TestConnectTunIgnoredForRawOverride(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("TUN elevation gate is a Windows path")
	}
	withElevated(t, false)
	app, id, started := tunConnectTestApp(t, CoreSingBox)
	// CoreOverrides is backend-owned: SaveSettings carries the live map
	// over, so the write goes through the lock-guarded mutator.
	if err := app.store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m["*"] = json.RawMessage(`{"inbounds":[]}`)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Connect(id); err != nil {
		t.Fatalf("Connect with full raw override: %v", err)
	}
	if !*started {
		t.Fatal("core not started; the raw override skips the TUN append")
	}
}

// TestTunStartHint: only permission-shaped failures gain the admin
// hint, the exact core error is preserved, and an elevated probe gets
// the original error untouched.
func TestTunStartHint(t *testing.T) {
	p := &Profile{Core: CoreSingBox}
	s := DefaultSettings()
	s.TunEnabled = true
	withElevated(t, false)

	denied := errors.New("core Start: start service: unable to create adapter: Access is denied.")
	got := tunStartHint(denied, p, s)
	if !errors.Is(got, denied) {
		t.Fatalf("hint = %v, want the original error wrapped", got)
	}
	if !strings.Contains(got.Error(), "unable to create adapter: Access is denied.") {
		t.Fatalf("hint = %q, want the exact core error preserved", got)
	}
	if !strings.Contains(got.Error(), "administrator") {
		t.Fatalf("hint = %q, want the administrator remedy", got)
	}

	portErr := errors.New("core Start: bind: address already in use")
	if got := tunStartHint(portErr, p, s); got != portErr {
		t.Fatalf("non-permission error = %v, want untouched", got)
	}

	s.TunEnabled = false
	if got := tunStartHint(denied, p, s); got != denied {
		t.Fatalf("TUN-disabled error = %v, want untouched", got)
	}

	s.TunEnabled = true
	withElevated(t, true)
	if got := tunStartHint(denied, p, s); got != denied {
		t.Fatalf("elevated error = %v, want untouched", got)
	}
}

// TestManualProxyToggleRemembers: the Console toggles persist the
// remembered setting for the next Connect.
func TestManualProxyToggleRemembers(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	app.sysProxyHook = func(enable bool) error { return nil }
	// EnableSystemProxyGUI refuses without a running core: it is the
	// live OS-proxy control, and with no core there is no port to point
	// the OS at, so a nil return would have been a lie.
	app.cores.mu.Lock()
	app.cores.activeCore = CoreSingBox
	app.cores.running = true
	app.cores.mu.Unlock()
	if err := app.DisableSystemProxyGUI(); err != nil {
		t.Fatalf("DisableSystemProxyGUI: %v", err)
	}
	if app.store.GetSettings().SystemProxy {
		t.Fatal("SystemProxy still true after manual disable, want remembered false")
	}
	if err := app.EnableSystemProxyGUI(); err != nil {
		t.Fatalf("EnableSystemProxyGUI: %v", err)
	}
	if !app.store.GetSettings().SystemProxy {
		t.Fatal("SystemProxy still false after manual enable, want remembered true")
	}
}

// spoofConnectTestApp mirrors tunConnectTestApp with TUN off and a
// CoreSpoof profile, so only the WinDivert gate can fire.
func spoofConnectTestApp(t *testing.T) (*App, string, *bool) {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.TunEnabled = false
	s.SystemProxy = false
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	p, err := store.SaveProfile(Profile{Name: "spoof", Core: CoreSpoof,
		Protocol: "trojan", Address: "203.0.113.10", Port: 443,
		IDOrPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	started := false
	app.coreStarter = func(*Profile, Settings) error {
		started = true
		return nil
	}
	return app, p.ID, &started
}

// The spoof engine injects through WinDivert, so an unelevated connect has
// to be refused with the admin remedy BEFORE anything starts. Without the
// gate the engine died on "Access is denied (code 5)" while the UI still
// reported a connection: every site dead, nothing actionable on screen.
func TestConnectSpoofWithoutAdminRefusesAndPersistsTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the WinDivert elevation gate is a Windows path")
	}
	withElevated(t, false)
	app, id, started := spoofConnectTestApp(t)
	err := app.Connect(id)
	if err == nil {
		t.Fatal("Connect = nil, want the spoof elevation error")
	}
	if !strings.Contains(err.Error(), "requires administrator rights") {
		t.Fatalf("error = %q, want the phrase the frontend turns into a relaunch", err)
	}
	if !strings.Contains(err.Error(), "WinDivert") {
		t.Fatalf("error = %q, want the driver named", err)
	}
	if *started {
		t.Fatal("the core started despite an unelevated spoof connect")
	}
	if got := app.store.GetSettings().ActiveProfileID; got != id {
		t.Fatalf("active profile = %q, want %q for the elevated resume", got, id)
	}
}

// Elevated, the same connect proceeds: the gate is about the driver, not
// about the profile.
func TestConnectSpoofElevatedStartsCore(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the WinDivert elevation gate is a Windows path")
	}
	withElevated(t, true)
	app, id, started := spoofConnectTestApp(t)
	if err := app.Connect(id); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !*started {
		t.Fatal("core not started while elevated")
	}
}

// With Settings -> SNI spoofing off the profile dials its own address on
// the normal transport: no driver is involved, so an unelevated connect
// must not be gated.
func TestConnectSpoofDisabledNeedsNoAdmin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the WinDivert elevation gate is a Windows path")
	}
	withElevated(t, false)
	app, id, started := spoofConnectTestApp(t)
	off := false
	s := app.store.GetSettings()
	s.SpoofEnabled = &off
	if err := app.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := app.Connect(id); err != nil {
		t.Fatalf("Connect with the engine disabled: %v", err)
	}
	if !*started {
		t.Fatal("core not started for a spoof-disabled profile")
	}
}
