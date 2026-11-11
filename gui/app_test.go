package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// versionFileWant reads the VERSION file build.bat stamps. Every other
// version literal in the tree has to agree with it.
func versionFileWant(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "VERSION"))
	if err != nil {
		t.Fatalf("reading the VERSION file build.bat stamps: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Fatal("VERSION is empty")
	}
	return want
}

// TestAppVersionMatchesVERSIONFile pins the fallback stamp to the file
// build.bat reads. appVersion's comment says to keep the two in step, but
// nothing enforced it: a plain `go build` / `go run` reported
// "1.0.0-beta.3-dev" while VERSION, the installer and the README all said
// "1.0.0-beta.3". The right-panel card and the About tab then showed a
// version no release ever had, and the updater compared that phantom
// prerelease against the published tag instead of the real one.
func TestAppVersionMatchesVERSIONFile(t *testing.T) {
	if want := versionFileWant(t); appVersion != want {
		t.Errorf("appVersion = %q, want %q", appVersion, want)
	}
}

// TestBuildMetadataVersionMatchesVERSIONFile: wails.json and the frontend
// package manifest carried the pre-rename "5.11.28.3-fork.1" long after
// VERSION moved to 1.0.0-beta.3, so the packaged app metadata and the
// version the UI reports described two different products.
func TestBuildMetadataVersionMatchesVERSIONFile(t *testing.T) {
	want := versionFileWant(t)
	for _, path := range []string{
		filepath.Join("wails.json"),
		filepath.Join("frontend", "package.json"),
		filepath.Join("frontend", "package-lock.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading %s: %v", path, err)
			continue
		}
		var meta struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Errorf("parsing %s: %v", path, err)
			continue
		}
		if meta.Version != want {
			t.Errorf("%s version = %q, want %q", path, meta.Version, want)
		}
	}
}

// TestCoreVersionCacheInvalidatesOnPathChange — changing corePath must
// drop the cached version instead of serving it stale (P3-15).
func TestCoreVersionCacheInvalidatesOnPathChange(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	app.singbox.setCorePath(filepath.Join(dir, "core-a.exe"))
	app.coreVer.mu.Lock()
	app.coreVer.path = app.singbox.getCorePath()
	app.coreVer.version = "v1-stale"
	app.coreVer.mu.Unlock()
	if got := app.CoreVersion(); got != "v1-stale" {
		t.Fatalf("got %q, want cached v1-stale", got)
	}
	app.singbox.setCorePath(filepath.Join(dir, "core-b.exe"))
	if got := app.CoreVersion(); got == "v1-stale" {
		t.Fatal("stale version survived corePath change")
	}
	app.coreVer.mu.Lock()
	cur := app.coreVer.path
	app.coreVer.mu.Unlock()
	if cur != app.singbox.getCorePath() {
		t.Fatalf("cache path = %q, want %q", cur, app.singbox.getCorePath())
	}
}

// proxyRecorder is the fake GUI proxy path: it records
// SetSystemProxy/ClearSystemProxy enable/disable calls plus the
// MixedPort visible at each enable, standing in for the OS write.
type proxyRecorder struct {
	enables    int
	disables   int
	ports      []int
	mixedPort  func() int
	failEnable error
}

func (r *proxyRecorder) hook(enable bool) error {
	if enable {
		if r.failEnable != nil {
			return r.failEnable
		}
		r.enables++
		if r.mixedPort != nil {
			r.ports = append(r.ports, r.mixedPort())
		}
		return nil
	}
	r.disables++
	return nil
}

func testProxyApp(t *testing.T, s Settings, core CoreType) (*App, Profile) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	saved, err := app.store.SaveProfile(Profile{Name: "p", Core: core, Address: "127.0.0.1", Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	app.coreStarter = func(p *Profile, s Settings) error { return nil }
	return app, saved
}

// TestConnectEnablesSystemProxyWhenSet — the remembered
// Settings.systemProxy flag gates the auto path: true ensures the OS
// proxy exactly once with the configured MixedPort (Disconnect
// disables); false leaves the OS proxy untouched on Connect.
func TestConnectEnablesSystemProxyWhenSet(t *testing.T) {
	s := DefaultSettings()
	s.SystemProxy = true
	s.MixedPort = 2280
	app, prof := testProxyApp(t, s, CoreSingBox)
	rec := &proxyRecorder{mixedPort: func() int { return app.store.GetSettings().MixedPort }}
	app.sysProxyHook = rec.hook

	if err := app.Connect(prof.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if rec.enables != 1 {
		t.Fatalf("enables = %d, want 1", rec.enables)
	}
	if len(rec.ports) != 1 || rec.ports[0] != 2280 {
		t.Fatalf("enable ports = %v, want [2280]", rec.ports)
	}
	if !app.getAutoProxy() {
		t.Fatal("autoProxyOn false after Connect with SystemProxy set")
	}
	_ = app.Disconnect()
	if rec.disables != 1 {
		t.Fatalf("disables = %d, want 1", rec.disables)
	}
	if app.getAutoProxy() {
		t.Fatal("autoProxyOn still true after Disconnect")
	}

	// SystemProxy false: Connect leaves the OS proxy untouched (the
	// remembered setting is honored), and Disconnect stays a safe
	// no-op on the proxy path.
	s2 := DefaultSettings()
	s2.SystemProxy = false
	app2, prof2 := testProxyApp(t, s2, CoreSingBox)
	rec2 := &proxyRecorder{mixedPort: func() int { return app2.store.GetSettings().MixedPort }}
	app2.sysProxyHook = rec2.hook
	if err := app2.Connect(prof2.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = app2.Disconnect()
	if rec2.enables != 0 || rec2.disables != 0 {
		t.Fatalf("proxy calls with SystemProxy=false: enables=%d disables=%d, want 0/0",
			rec2.enables, rec2.disables)
	}
	if app2.getAutoProxy() {
		t.Fatal("autoProxyOn true after Connect with SystemProxy=false")
	}
}

// TestConnectTunModeLeavesSystemProxyOff — the rendered mode decides
// between TUN and the OS proxy. A TUN connect (tunEnabled or the SNI
// spoof → Psiphon chain) must not also point the OS proxy at our mixed
// port: an auto-set proxy from an earlier connect is cleared, and
// SystemProxy=true does not resurrect it.
func TestConnectTunModeLeavesSystemProxyOff(t *testing.T) {
	withElevated(t, true)

	s := DefaultSettings()
	s.SystemProxy = true
	s.TunEnabled = true
	app, prof := testProxyApp(t, s, CoreSingBox)
	rec := &proxyRecorder{}
	app.sysProxyHook = rec.hook
	app.setAutoProxy(true) // a previous proxy-mode connect left it on
	if err := app.Connect(prof.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if rec.enables != 0 {
		t.Fatalf("enables = %d, want 0 (TUN carries traffic)", rec.enables)
	}
	if rec.disables != 1 {
		t.Fatalf("disables = %d, want 1 (auto proxy cleared)", rec.disables)
	}
	if app.getAutoProxy() {
		t.Fatal("autoProxyOn true after a TUN connect")
	}
	_ = app.Disconnect()
	if rec.disables != 1 {
		t.Fatalf("disables after Disconnect = %d, want 1 (already cleared)", rec.disables)
	}

	// The chain forces TUN on its own, with tunEnabled false.
	s2 := DefaultSettings()
	s2.SystemProxy = true
	s2.SpoofPsiphon = true
	app2, prof2 := testProxyApp(t, s2, CoreSpoof)
	rec2 := &proxyRecorder{}
	app2.sysProxyHook = rec2.hook
	if err := app2.Connect(prof2.ID); err != nil {
		t.Fatalf("Connect (chain): %v", err)
	}
	if rec2.enables != 0 || rec2.disables != 0 {
		t.Fatalf("proxy calls on the chain: enables=%d disables=%d, want 0/0",
			rec2.enables, rec2.disables)
	}
}

// TestTunModeFor — only a config that actually renders the TUN inbound
// counts: Xray ignores tunEnabled and a full raw override replaces the
// rendered config (with its TUN append), so both stay in proxy mode.
func TestTunModeFor(t *testing.T) {
	sing := Profile{Core: CoreSingBox}
	xray := Profile{Core: CoreXray}
	spoof := Profile{Core: CoreSpoof}

	s := DefaultSettings()
	if tunModeFor(&sing, s) {
		t.Fatal("default settings = TUN mode")
	}
	s.TunEnabled = true
	if !tunModeFor(&sing, s) {
		t.Fatal("tunEnabled with sing-box = proxy mode")
	}
	if tunModeFor(&xray, s) {
		t.Fatal("xray never renders TUN")
	}

	chain := DefaultSettings()
	chain.SpoofPsiphon = true
	if !tunModeFor(&spoof, chain) {
		t.Fatal("the spoof→Psiphon chain must count as TUN mode")
	}
	if tunModeFor(&spoof, DefaultSettings()) {
		t.Fatal("spoof without the chain = proxy mode")
	}
	// Spoof on the Xray transport never renders TUN, even with
	// tunEnabled; the chain stays on sing-box and keeps TUN.
	xraySpoof := DefaultSettings()
	xraySpoof.SpoofXray = true
	xraySpoof.TunEnabled = true
	if tunModeFor(&spoof, xraySpoof) {
		t.Fatal("spoof on xray must not claim TUN mode")
	}
	xraySpoof.SpoofPsiphon = true
	if !tunModeFor(&spoof, xraySpoof) {
		t.Fatal("the chain stays on sing-box and keeps TUN")
	}

	raw := DefaultSettings()
	raw.TunEnabled = true
	raw.CoreOverrides = map[string]json.RawMessage{"*": json.RawMessage(`{"inbounds":[]}`)}
	if tunModeFor(&sing, raw) {
		t.Fatal("a full raw override replaces the TUN append")
	}
}

// TestEnsureSystemProxySkipsAndRepairs — the Connect-time ensure
// leaves an already-correct proxy it OWNS alone (no rewrite) and
// (re-)applies when it is off, foreign, unreadable, or already reading
// our own address without this process having installed it.
func TestEnsureSystemProxySkipsAndRepairs(t *testing.T) {
	ports := ProxyPorts{HTTP: 2280, SOCKS: 2280, Mixed: true}
	want := proxyServerValue(ports)
	owned := true
	oldOwned := sysproxyOwned
	defer func() { sysproxyOwned = oldOwned }()
	sysproxyOwned = func() bool { return owned }
	mk := func(state func() (bool, string, error)) (*App, *proxyRecorder) {
		app, _ := testProxyApp(t, DefaultSettings(), CoreSingBox)
		rec := &proxyRecorder{}
		app.sysProxyHook = rec.hook
		app.sysStateHook = state
		return app, rec
	}

	// Already ON + pointing at us + ours: no rewrite, still owned.
	owned = true
	app, rec := mk(func() (bool, string, error) { return true, want, nil })
	if err := app.ensureSystemProxy(ports); err != nil {
		t.Fatalf("ensure (correct): %v", err)
	}
	if rec.enables != 0 {
		t.Fatalf("enables (correct) = %d, want 0 (left alone)", rec.enables)
	}
	if !app.getAutoProxy() {
		t.Fatal("autoProxyOn false after skip (still owned)")
	}

	// ON + our address, but this process never installed it (a stale
	// proxy from another client on the same port): skipping it here
	// would claim ownership the revert cannot honour later, because
	// sysproxyRestore only acts on a proxy it records itself.
	owned = false
	app, rec = mk(func() (bool, string, error) { return true, want, nil })
	if err := app.ensureSystemProxy(ports); err != nil {
		t.Fatalf("ensure (adopted): %v", err)
	}
	if rec.enables != 1 {
		t.Fatalf("enables (adopted) = %d, want 1 (take ownership of the stale proxy)", rec.enables)
	}

	// OFF: applied.
	app, rec = mk(func() (bool, string, error) { return false, "", nil })
	if err := app.ensureSystemProxy(ports); err != nil {
		t.Fatalf("ensure (off): %v", err)
	}
	if rec.enables != 1 {
		t.Fatalf("enables (off) = %d, want 1", rec.enables)
	}

	// ON but foreign: repaired.
	app, rec = mk(func() (bool, string, error) { return true, "127.0.0.1:9999", nil })
	if err := app.ensureSystemProxy(ports); err != nil {
		t.Fatalf("ensure (foreign): %v", err)
	}
	if rec.enables != 1 {
		t.Fatalf("enables (foreign) = %d, want 1", rec.enables)
	}

	// Unreadable: fail open (applied).
	app, rec = mk(func() (bool, string, error) { return false, "", fmt.Errorf("registry boom") })
	if err := app.ensureSystemProxy(ports); err != nil {
		t.Fatalf("ensure (unreadable): %v", err)
	}
	if rec.enables != 1 {
		t.Fatalf("enables (unreadable) = %d, want 1", rec.enables)
	}
}

// TestSystemProxyPointsAt — the pure check: off/foreign/correct.
func TestSystemProxyPointsAt(t *testing.T) {
	ports := ProxyPorts{HTTP: 2280, SOCKS: 2280, Mixed: true}
	defer func() { sysProxyReader = nil }()
	sysProxyReader = func() (bool, string, error) { return false, "", nil }
	if ok, _ := systemProxyPointsAt(ports); ok {
		t.Fatal("pointsAt (off) = true, want false")
	}
	sysProxyReader = func() (bool, string, error) { return true, "127.0.0.1:9999", nil }
	if ok, _ := systemProxyPointsAt(ports); ok {
		t.Fatal("pointsAt (foreign) = true, want false")
	}
	sysProxyReader = func() (bool, string, error) { return true, proxyServerValue(ports), nil }
	if ok, _ := systemProxyPointsAt(ports); !ok {
		t.Fatal("pointsAt (correct) = false, want true")
	}
}

// TestGetSystemProxyStateReportsOwnership — the toggle answers "is the
// OS proxy OURS", not "is ProxyEnable set". A corporate or third-party
// proxy reads OFF, so the card offers to take it over instead of
// promising a disable that ClearSystemProxy rightly refuses to perform;
// our own port reads ON; and once no core is running the ownership
// record answers, because there is no live port to compare against.
func TestGetSystemProxyStateReportsOwnership(t *testing.T) {
	app, _ := testProxyApp(t, DefaultSettings(), CoreSingBox)
	app.cores.mu.Lock()
	app.cores.running = true
	app.cores.mu.Unlock()
	want := proxyServerValue(app.ActiveProxyPorts())

	owned := false
	oldOwned := sysproxyOwned
	defer func() { sysproxyOwned = oldOwned }()
	sysproxyOwned = func() bool { return owned }

	cases := []struct {
		name   string
		on     bool
		server string
		own    bool
		want   bool
	}{
		{"off", false, "", false, false},
		{"foreign proxy", true, "proxy.corp:8080", false, false},
		{"our port", true, want, false, true},
		{"ours but the user edited the address", true, "127.0.0.1:3128", true, true},
		{"switch killed by hand while we still hold it", false, "", true, false},
	}
	for _, tc := range cases {
		owned = tc.own
		app.sysStateHook = func() (bool, string, error) { return tc.on, tc.server, nil }
		got, err := app.GetSystemProxyState()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: GetSystemProxyState = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Disconnected, no port matches, so only the ownership record can
	// answer — and a foreign proxy must still read OFF.
	app.cores.mu.Lock()
	app.cores.running = false
	app.cores.mu.Unlock()
	app.sysStateHook = func() (bool, string, error) { return true, want, nil }
	owned = true
	if got, err := app.GetSystemProxyState(); err != nil || !got {
		t.Fatalf("proxy owned across a disconnect = %v/%v, want true/nil", got, err)
	}
	owned = false
	if got, _ := app.GetSystemProxyState(); got {
		t.Fatal("foreign proxy reads as ours while disconnected")
	}

	// An unreadable OS stays an error: the cards fall back to their
	// remembered setting, they do not guess OFF and re-enable over the
	// user's head.
	app.sysStateHook = func() (bool, string, error) { return false, "", fmt.Errorf("registry boom") }
	if _, err := app.GetSystemProxyState(); err == nil {
		t.Fatal("unreadable OS proxy = nil error, want the failure surfaced")
	}
}

// TestEnableSystemProxyGUINeedsCore — the manual toggle is a live
// control: with no core running there is no port to point the OS at, so
// it must refuse rather than return nil and let the tile claim "On".
func TestEnableSystemProxyGUINeedsCore(t *testing.T) {
	s := DefaultSettings()
	s.SystemProxy = false
	app, _ := testProxyApp(t, s, CoreSingBox)
	app.sysProxyHook = func(bool) error { return nil }
	if err := app.EnableSystemProxyGUI(); err == nil {
		t.Fatal("EnableSystemProxyGUI with no core = nil, want an error")
	}
	if app.store.GetSettings().SystemProxy {
		t.Fatal("refused enable still persisted SystemProxy=true")
	}
}

// TestCorePathRace — BUG-022: SaveSettings/recordSubscription writers
// vs CoreVersion/XrayVersion readers must be race-clean. Missing
// binaries make the version reads fail fast (""), so the loop stays
// quick; run with -race.
func TestCorePathRace(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				s := app.store.GetSettings()
				s.CorePath = filepath.Join(dir, fmt.Sprintf("core-%d.exe", (i+j)%3))
				s.XrayPath = filepath.Join(dir, fmt.Sprintf("xray-%d.exe", (i+j)%3))
				_ = app.SaveSettings(s)
				_ = app.CoreVersion()
				_ = app.XrayVersion()
			}
		}(i)
	}
	wg.Wait()
}

// TestSaveSettingsRejectsBadMixedPort — BUG-019: App.SaveSettings
// refuses an out-of-range MixedPort instead of storing a value that
// would wrap on the int32 proxy-RPC cast.
func TestSaveSettingsRejectsBadMixedPort(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	for _, p := range []int{0, -1, 65536, 70000, 1 << 30} {
		s := DefaultSettings()
		s.MixedPort = p
		if err := app.SaveSettings(s); err == nil {
			t.Errorf("SaveSettings(MixedPort=%d) = nil, want range error", p)
		}
	}
	for _, p := range []int{1, 2080, 65535} {
		s := DefaultSettings()
		s.MixedPort = p
		if err := app.SaveSettings(s); err != nil {
			t.Errorf("SaveSettings(MixedPort=%d) = %v, want nil", p, err)
		}
	}
}

// TestSpoofGetLogsPollsByID — the log buffer is a 2000-entry ring that
// evicts from the head, so the SNI tab's cursor must be a log ID: an
// offset into the filtered slice drifts (skipped/duplicated lines) and
// then freezes for good once it passes the filtered length.
func TestSpoofGetLogsPollsByID(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(store, make(chan LogEntry, 8))
	a.emitLog("spoof", "info", "one")
	a.emitLog("gui", "info", "noise")
	a.emitLog("psiphon", "info", "two")

	all, err := a.SpoofGetLogs(0)
	if err != nil {
		t.Fatalf("SpoofGetLogs(0): %v", err)
	}
	if len(all) != 2 || all[0].Message != "one" || all[1].Message != "two" {
		t.Fatalf("first poll = %+v, want the two engine lines only", all)
	}
	seen := all[len(all)-1].ID

	a.emitLog("spoof", "info", "three")
	chunk, err := a.SpoofGetLogs(seen)
	if err != nil {
		t.Fatalf("SpoofGetLogs(%d): %v", seen, err)
	}
	if len(chunk) != 1 || chunk[0].Message != "three" {
		t.Fatalf("incremental poll = %+v, want only 'three'", chunk)
	}
	seen = chunk[0].ID
	if again, err := a.SpoofGetLogs(seen); err != nil || len(again) != 0 {
		t.Fatalf("re-poll = %+v, %v; want nothing new", again, err)
	}

	// Evict the whole ring: the cursor is an ID, so the next engine line
	// still comes back exactly once instead of being lost to the shift.
	for i := 0; i < 2100; i++ {
		a.emitLog("gui", "info", "filler")
	}
	a.emitLog("spoof", "info", "after-eviction")
	chunk2, err := a.SpoofGetLogs(seen)
	if err != nil {
		t.Fatalf("SpoofGetLogs after eviction: %v", err)
	}
	if len(chunk2) != 1 || chunk2[0].Message != "after-eviction" {
		t.Fatalf("post-eviction poll = %+v, want only 'after-eviction'", chunk2)
	}
}

// TestImportRejectsUnroutableLink — imports saved parsed links straight to
// the store, so a link the core builders refuse (mux together with an xtls
// flow) was listed in Proxies and only failed at Connect, with a message
// about a field the user never saw. It must be counted invalid at import.
func TestImportRejectsUnroutableLink(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 256))
	s := store.GetSettings()
	s.GeoIPAutoDetect = false // no background DNS in tests
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	bad := "vless://aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@real.example:443?security=reality&sni=real.example&fp=chrome&pbk=PUBKEY&sid=abcd&flow=xtls-rprx-vision&type=tcp&mux=8#MuxFlow"
	res, err := app.ImportLinks(bad)
	if err != nil {
		t.Fatalf("ImportLinks: %v", err)
	}
	if len(res.Profiles) != 0 || res.Invalid != 1 {
		t.Fatalf("ImportLinks = %d profiles / %d invalid, want 0 / 1", len(res.Profiles), res.Invalid)
	}
	if n := len(store.ListProfiles()); n != 0 {
		t.Fatalf("store holds %d profiles, want none", n)
	}

	good := "vless://aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@real.example:443?security=reality&sni=real.example&fp=chrome&pbk=PUBKEY&sid=abcd&flow=xtls-rprx-vision&type=tcp#Routable"
	res, err = app.ImportLinks(good)
	if err != nil {
		t.Fatalf("ImportLinks (routable): %v", err)
	}
	if len(res.Profiles) != 1 || res.Invalid != 0 {
		t.Fatalf("ImportLinks = %d profiles / %d invalid, want 1 / 0", len(res.Profiles), res.Invalid)
	}
	if got := res.Profiles[0].Flow; got != "xtls-rprx-vision" {
		t.Fatalf("Flow = %q, want the link's xtls-rprx-vision", got)
	}
}

// TestLaunchCountryRefreshSkippedAfterQuit — once Shutdown has begun, a
// background refresh that has not started yet must be skipped, so the
// waitCountryRefresh drain never blocks on a fresh run and no SaveFile
// writes config after exit.
func TestLaunchCountryRefreshSkippedAfterQuit(t *testing.T) {
	a := newGeoTestApp(t)
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.launchCountryRefresh("test")
	a.refreshWG.Wait()
	if got := a.RefreshCountriesProgress(); got.Running || got.Total != 0 {
		t.Fatalf("background refresh ran after quit: %+v", got)
	}
}
