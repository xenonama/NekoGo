package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateFromOldAppDataDir — old config seeds the new dir once;
// a pre-existing new config wins silently; the old file is kept.
func TestMigrateFromOldAppDataDir(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	oldPath := filepath.Join(oldRoot, "config.json")
	raw := `{"profiles":[],"settings":{"theme":"light","mixedPort":2080,"corePath":"C:\\Users\\x\\AppData\\Roaming\\SNI-NekoBox\\nekobox_core.exe"}}`
	if err := os.WriteFile(oldPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateFromOldAppDataDir(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("expected migration to happen")
	}
	got, err := os.ReadFile(filepath.Join(newRoot, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "SNI-NekoBox") {
		t.Fatalf("stale path survived migration: %s", got)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old file must be kept: %v", err)
	}
	// Second run with both present: silent no-op.
	migrated, err = migrateFromOldAppDataDir(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("must prefer the new config silently when both exist")
	}
}

// TestSaveFailureLogs — a broken store path makes persistence fail and
// the App surfaces it in RecentLogs (P3-5: Console tab visibility).
func TestSaveFailureLogs(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	logCh := make(chan LogEntry, 64)
	app := NewApp(store, logCh)
	// Break persistence deterministically: point the store at a path
	// inside a nonexistent directory (read-only-dir chmod is not
	// reliable on Windows CI).
	store.path = filepath.Join(dir, "no-such-dir", "config.json")
	store.dir = filepath.Join(dir, "no-such-dir")
	app.recordSubscription("https://example.com/sub", "example", SubUserInfo{})
	found := false
	for _, e := range app.RecentLogs(50) {
		if strings.Contains(e.Message, "save settings") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'save settings' error in RecentLogs, got %+v", app.RecentLogs(50))
	}
	// Direct store error still propagates to callers.
	if err := store.SetActiveProfileID("x"); err == nil {
		t.Fatal("expected SetActiveProfileID to fail with broken path")
	}
}

// TestNewIDReturnsError — BUG-029: a CSPRNG failure returns an error
// (and SaveProfile propagates it) instead of panicking the GUI.
func TestNewIDReturnsError(t *testing.T) {
	old := randRead
	defer func() { randRead = old }()
	randRead = func([]byte) (int, error) {
		return 0, errors.New("CSPRNG unavailable")
	}
	if _, err := newID(); err == nil {
		t.Fatal("newID with failing CSPRNG = nil error, want error")
	}
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{Name: "x", Address: "h", Port: 1}); err == nil {
		t.Fatal("SaveProfile with failing CSPRNG = nil error, want error, not panic")
	}
	// Happy path still mints 16 lowercase hex chars, unique per call.
	randRead = old
	a, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 16 || a == b {
		t.Fatalf("newID = (%q, %q), want distinct 16-char ids", a, b)
	}
}

// TestSpoofAutoDetectRevert — the one-time load migration applies the
// import rule to already-saved profiles: engine-eligible loopback
// AND bare-domain links get the spoof tag, while IP literals and
// ineligible profiles are untouched. The marker is persisted even
// when nothing changed, so a later explicit conversion survives
// restarts.
// TestLegacyAetherPathMigration: a config saved with the old External
// subsystem keeps its custom aether.exe path in Settings.AetherPath.
func TestLegacyAetherPathMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "profiles": [],
  "settings": {
    "mixedPort": 2080,
    "externals": [
      {"kind": "mhr", "exePath": "C:\\Tools\\mhr.exe"},
      {"kind": "aether", "exePath": "C:\\Tools\\aether.exe"}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().AetherPath; got != `C:\Tools\aether.exe` {
		t.Fatalf("AetherPath = %q, want migrated aether external path", got)
	}
}

func TestSpoofAutoDetectRevert(t *testing.T) {
	dir := t.TempDir()
	// A config written by the old build: normal-core profiles and no
	// spoofAutoDetectReverted marker.
	legacy := `{
  "profiles": [
    {"id": "d1", "name": "direct", "core": 0, "protocol": "trojan", "address": "www.calmlunch.com", "port": 443, "tls": true, "network": "ws"},
    {"id": "i1", "name": "iplit", "core": 0, "protocol": "trojan", "address": "104.18.9.83", "port": 443, "tls": true, "network": "ws"},
    {"id": "c1", "name": "chain", "core": 0, "protocol": "trojan", "address": "127.0.0.1", "port": 40443, "tls": true, "network": "ws"},
    {"id": "v1", "name": "vmess", "core": 0, "protocol": "vmess", "address": "cf.example", "port": 443, "tls": true, "network": "ws"}
  ],
  "settings": {"mixedPort": 2080}
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	// Loading runs the migration and persists it in one save.
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Profile{}
	for _, p := range store.ListProfiles() {
		byName[p.Name] = p
	}
	if byName["direct"].Core != CoreSpoof {
		t.Fatalf("direct domain core after migration = %v, want spoof (engine-served shape)", byName["direct"].Core)
	}
	if byName["iplit"].Core != CoreSingBox {
		t.Fatalf("ip-literal core after migration = %v, want sing-box (never auto-tag)", byName["iplit"].Core)
	}
	if byName["chain"].Core != CoreSpoof {
		t.Fatalf("loopback chain core after revert = %v, want spoof (engine-served shape)", byName["chain"].Core)
	}
	if byName["vmess"].Core != CoreSingBox {
		t.Fatalf("vmess fixture core after migration = %v, want sing-box (fixture lacks password, ineligible)", byName["vmess"].Core)
	}
	// A later explicit conversion must survive the next restart.
	direct := byName["direct"]
	direct.Core = CoreSpoof
	if _, err := store.SaveProfile(direct); err != nil {
		t.Fatal(err)
	}
	store, err = NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range store.ListProfiles() {
		if p.Name == "direct" && p.Core != CoreSpoof {
			t.Fatalf("explicit conversion reverted on restart: core = %v", p.Core)
		}
	}
}

// A config.json written before tunStack existed reports the effective
// default (tunStackDefault) on read, without rewriting the file.
func TestLegacyConfigDefaultsTunStack(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"profiles":[],"settings":{"mixedPort":2080}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().TunStack; got != tunStackDefault {
		t.Fatalf("legacy tunStack = %q, want %q", got, tunStackDefault)
	}
}

// A config.json holding tunStack "system" is the case that broke TUN for
// real users. The value was not chosen by them: NekoGo hardcoded the OS
// stack until the setting existed, and its own first save persisted it —
// so a config from before the row still says "system", and the new
// default never gets a chance to apply. The OS stack terminates TCP in
// Windows, which needs an inbound firewall allow for the core that
// sing-tun asks for and discards the error of, so the tunnel carries DNS
// and every TCP dial times out ("connects, nothing loads"). One-time
// migration moves that stored value to the stack that works and marks
// the file, so a LATER deliberate choice of System/Mixed is honored from
// then on and is not quietly reverted on the next launch.
func TestMigrateStoredOSStackTunStack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	stored := `{"profiles":[],"settings":{"mixedPort":2080,"tunEnabled":true,"tunStack":"system"}}`
	if err := os.WriteFile(path, []byte(stored), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().TunStack; got != tunStackDefault {
		t.Fatalf("stored system stack = %q, want it migrated to %q", got, tunStackDefault)
	}

	// The marker must reach the disk, or every launch re-migrates and
	// eventually overwrites a user's own pick.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Settings struct {
			TunStack           string `json:"tunStack"`
			TunOSStackMigrated bool   `json:"tunOSStackMigrated"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if !onDisk.Settings.TunOSStackMigrated {
		t.Fatal("migration marker was not persisted")
	}
	if onDisk.Settings.TunStack != tunStackDefault {
		t.Fatalf("tunStack on disk = %q, want %q", onDisk.Settings.TunStack, tunStackDefault)
	}

	// A deliberate later choice survives a restart: the marker is set, so
	// nothing rewrites the stack any more.
	s := store.GetSettings()
	s.TunStack = "system"
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	again, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.GetSettings().TunStack; got != "system" {
		t.Fatalf("explicit system stack after restart = %q, want it honored", got)
	}
}

// TestSaveSettingsKeepsBackendOwnedFields — the frontend posts the settings
// snapshot it loaded at startup, while feeds and Aether scan results are
// written by the backend afterwards. A wholesale replace therefore deleted
// them: add a feed, click a server (which persists settings), and the feed
// was gone from config.json after a restart while its profiles stayed.
func TestSaveSettingsKeepsBackendOwnedFields(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.GetSettings() // what the frontend loaded at startup
	if _, err := store.AddSubscription(Subscription{URL: "https://example.com/sub", Name: "example"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAetherScanResults(func([]AetherScanResult) []AetherScanResult {
		return []AetherScanResult{{Endpoint: "1.2.3.4:51820", Protocol: "wg", Transport: "gool", LatencyMs: 42}}
	}); err != nil {
		t.Fatal(err)
	}
	snapshot.Theme = "dark"
	if err := store.SaveSettings(snapshot); err != nil {
		t.Fatal(err)
	}
	got := store.GetSettings()
	if got.Theme != "dark" {
		t.Fatalf("Theme = %q, want the saved dark", got.Theme)
	}
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].URL != "https://example.com/sub" {
		t.Fatalf("Subscriptions = %+v, want the feed the backend added", got.Subscriptions)
	}
	if len(got.AetherScanResults) != 1 || got.AetherScanResults[0].LatencyMs != 42 {
		t.Fatalf("AetherScanResults = %+v, want the scan result the backend added", got.AetherScanResults)
	}
	reloaded, err := NewConfigStore(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(reloaded.GetSettings().Subscriptions); n != 1 {
		t.Fatalf("persisted Subscriptions = %d, want 1", n)
	}
}

// TestProxyGroupsRoundTrip — user-created Proxies remark groups have no
// profile to be derived from, so they must survive SaveSettings and a
// restart even while empty.
func TestProxyGroupsRoundTrip(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := store.GetSettings()
	s.ProxyGroups = []string{"Asia", "Europe"}
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewConfigStore(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.GetSettings().ProxyGroups
	if len(got) != 2 || got[0] != "Asia" || got[1] != "Europe" {
		t.Fatalf("ProxyGroups = %v, want [Asia Europe]", got)
	}
}

// TestLoadConfigKeepsSettingsWhenMixedPortZero — a zero/missing mixedPort
// used to discard the WHOLE settings block (feeds, core paths, theme) and
// the next save persisted the loss. Only the port itself is repaired now.
func TestLoadConfigKeepsSettingsWhenMixedPortZero(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{Name: "keep", Protocol: "trojan", Address: "a.example", Port: 443, Core: CoreSingBox}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSubscription(Subscription{URL: "https://example.com/sub", Name: "example"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zeroed := strings.Replace(string(raw), `"mixedPort": 2080`, `"mixedPort": 0`, 1)
	if zeroed == string(raw) {
		t.Fatalf("config.json has no mixedPort 2080 to zero out:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(zeroed), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := reloaded.GetSettings()
	if s.MixedPort != DefaultSettings().MixedPort {
		t.Fatalf("MixedPort = %d, want the default %d", s.MixedPort, DefaultSettings().MixedPort)
	}
	if len(s.Subscriptions) != 1 {
		t.Fatalf("Subscriptions = %+v, want the saved feed to survive", s.Subscriptions)
	}
	if n := len(reloaded.ListProfiles()); n != 1 {
		t.Fatalf("profiles = %d, want 1", n)
	}
}

// TestLoadConfigFallsBackToBackup — a torn config.json (power loss during
// the rename leaves unparsable JSON) used to be fatal: main.go exits, so the
// only recovery was deleting the file and losing every profile. The last
// good backup is restored instead and the broken copy is kept for inspection.
func TestLoadConfigFallsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	old := configBackupEvery
	configBackupEvery = 0
	t.Cleanup(func() { configBackupEvery = old })
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{Name: "keep", Protocol: "trojan", Address: "a.example", Port: 443, Core: CoreSingBox}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFile(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("no config.json.bak was written: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"profiles": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("a corrupt config.json must fall back to the backup, got %v", err)
	}
	if n := len(reloaded.ListProfiles()); n != 1 {
		t.Fatalf("profiles = %d, want the 1 restored from the backup", n)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("the broken file was not kept for inspection: %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), "keep") {
		t.Fatalf("config.json was not restored from the backup:\n%s", restored)
	}
}
