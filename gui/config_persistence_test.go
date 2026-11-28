package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readPersistenceJSON(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\n")) {
		t.Fatalf("%s is not compact JSON:\n%s", filepath.Base(path), data)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func TestConfigSeparatesDurableAndVolatileState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tested := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	country := tested.Add(-time.Hour)
	if _, err := store.SaveProfile(Profile{
		ID: "stable-id", Name: "node", Protocol: "vless", Address: "node.example", Port: 443,
		LastLatency: 42, LastTestedAt: &tested, CountryCode: "NL", CountryName: "Netherlands",
		CountryAt: &country, LastConnectedAt: tested.Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAetherScanResults(func([]AetherScanResult) []AetherScanResult {
		return []AetherScanResult{{Endpoint: "192.0.2.10:443", Protocol: "wg", LatencyMs: 42}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSNI(func(s *Settings) {
		s.SNIIPs = []SNIIP{{IP: "192.0.2.20", Source: "test", LastOK: true, LastMs: 12, LastAt: tested.Format(time.RFC3339), LastTLS: "TLS 1.3"}}
		s.SNISNIs = []SNISNI{{Host: "example.com", Source: "test", LastOK: true, LastIP: "192.0.2.21"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	configRaw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lastLatency", "lastTestedAt", "countryCode", "countryName", "countryAt", "lastConnectedAt", "aetherScanResults", "lastOk", "lastMs", "lastAt", "lastTls", "lastIp"} {
		if bytes.Contains(configRaw, []byte(`"`+key+`"`)) {
			t.Fatalf("durable config contains volatile key %q:\n%s", key, configRaw)
		}
	}
	readPersistenceJSON(t, filepath.Join(dir, "config.json"))
	state := readPersistenceJSON(t, filepath.Join(dir, "state.json"))
	var version int
	if err := json.Unmarshal(state["version"], &version); err != nil || version != 1 {
		t.Fatalf("state version = %d, %v; want 1", version, err)
	}

	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reloaded.GetProfile("stable-id")
	if !ok || p.LastLatency != 42 || p.LastTestedAt == nil || !p.LastTestedAt.Equal(tested) || p.CountryCode != "NL" || p.LastConnectedAt == "" {
		t.Fatalf("reloaded profile runtime = %+v", p)
	}
	s := reloaded.GetSettings()
	if len(s.AetherScanResults) != 1 || s.AetherScanResults[0].LatencyMs != 42 {
		t.Fatalf("reloaded Aether results = %+v", s.AetherScanResults)
	}
	if len(s.SNIIPs) != 1 || !s.SNIIPs[0].LastOK || s.SNIIPs[0].LastMs != 12 || len(s.SNISNIs) != 1 || s.SNISNIs[0].LastIP != "192.0.2.21" {
		t.Fatalf("reloaded SNI state = %+v / %+v", s.SNIIPs, s.SNISNIs)
	}
}

func TestConfigMigratesLegacyVolatileState(t *testing.T) {
	dir := t.TempDir()
	tested := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	legacy := fileConfig{
		Profiles: []Profile{{ID: "legacy-id", Name: "node", LastLatency: 7, LastTestedAt: &tested, CountryCode: "DE", LastConnectedAt: tested.Format(time.RFC3339)}},
		Settings: Settings{
			MixedPort:         2080,
			AetherScanResults: []AetherScanResult{{Endpoint: "192.0.2.30:443", Protocol: "wg"}},
			SNIIPs:            []SNIIP{{IP: "192.0.2.31", LastOK: true, LastMs: 8}},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := store.GetProfile("legacy-id")
	if !ok || p.LastLatency != 7 || p.CountryCode != "DE" || p.LastConnectedAt == "" {
		t.Fatalf("legacy runtime was not migrated: %+v", p)
	}
	if len(store.GetSettings().AetherScanResults) != 1 || !store.GetSettings().SNIIPs[0].LastOK {
		t.Fatalf("legacy settings runtime was not migrated: %+v", store.GetSettings())
	}
	readPersistenceJSON(t, filepath.Join(dir, "state.json"))
	configRaw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(configRaw, []byte(`"lastLatency"`)) || bytes.Contains(configRaw, []byte(`"aetherScanResults"`)) {
		t.Fatalf("legacy volatile fields remain in config.json:\n%s", configRaw)
	}
}

func TestRuntimeStateSidecarOverridesLegacyValues(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"profiles":[{"id":"id","name":"node","lastLatency":1}],"settings":{"mixedPort":2080}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	state := `{"version":1,"profiles":{"id":{"lastLatency":2}}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.GetProfile("id")
	if p.LastLatency != 2 {
		t.Fatalf("LastLatency = %d, want sidecar value 2", p.LastLatency)
	}
}

func TestRuntimeStateWriteIsTrailingEdgeDebounced(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.stateDelay = 150 * time.Millisecond
	store.stateMaxDelay = time.Second
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, err := store.updateProfileFields("id", func(p *Profile) { p.LastLatency = 2 }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("state.json was written before the trailing delay elapsed: %v", err)
	}
	waitFor(t, "debounced state write", func() bool {
		_, err := os.Stat(filepath.Join(dir, "state.json"))
		return err == nil
	})
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reloaded.GetProfile("id")
	if p.LastLatency != 2 {
		t.Fatalf("LastLatency = %d, want latest value 2", p.LastLatency)
	}
}

func TestRuntimeStateWriteHasMaximumDelay(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.stateDelay = 2 * time.Second
	store.stateMaxDelay = 100 * time.Millisecond
	started := time.Now()
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 6; i++ {
		time.Sleep(30 * time.Millisecond)
		if _, _, err := store.updateProfileFields("id", func(p *Profile) { p.LastLatency = i }); err != nil {
			t.Fatal(err)
		}
	}
	deadline := started.Add(750 * time.Millisecond)
	for {
		if _, err := os.Stat(filepath.Join(dir, "state.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("state.json was not written by the maximum delay")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reloaded.GetProfile("id")
	if p.LastLatency != 6 {
		t.Fatalf("LastLatency = %d, want latest value 6", p.LastLatency)
	}
}

func TestFlushPersistsPendingRuntimeState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.stateDelay = time.Hour
	if _, _, err := store.updateProfileFields("missing", func(*Profile) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("state.json exists before Flush: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reloaded.GetProfile("id")
	if p.LastLatency != 9 {
		t.Fatalf("LastLatency after Flush = %d, want 9", p.LastLatency)
	}
}

func TestRuntimeOnlyUpdateDoesNotRewriteDurableConfig(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node"}); err != nil {
		t.Fatal(err)
	}
	before := saveCount.Load()
	if _, _, err := store.updateProfileFields("id", func(p *Profile) { p.LastLatency = 10 }); err != nil {
		t.Fatal(err)
	}
	if got := saveCount.Load(); got != before {
		t.Fatalf("runtime-only update wrote config.json (%d -> %d)", before, got)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestStateWriteFailureDoesNotRollbackDurableProfile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.stateDelay = time.Hour
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 4}); err != nil {
		t.Fatalf("durable save failed because the runtime sidecar is unavailable: %v", err)
	}
	if err := store.Flush(); err == nil {
		t.Fatal("Flush succeeded with state.json blocked by a directory")
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reloaded.GetProfile("id")
	if !ok || p.Name != "node" {
		t.Fatalf("durable profile was rolled back: %+v, %v", p, ok)
	}
}

func TestSaveProfilesWritesOnceAndRollsBackOnFailure(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := saveCount.Load()
	profiles := make([]Profile, 5000)
	for i := range profiles {
		profiles[i] = Profile{Name: fmt.Sprintf("node-%d", i), Protocol: "vless", Address: fmt.Sprintf("node%d.example", i), Port: 443}
	}
	saved, err := store.SaveProfiles(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != len(profiles) || len(store.ListProfiles()) != len(profiles) {
		t.Fatalf("saved %d profiles, store holds %d", len(saved), len(store.ListProfiles()))
	}
	if got := saveCount.Load() - before; got != 1 {
		t.Fatalf("SaveProfiles wrote config.json %d times, want 1", got)
	}
	seen := make(map[string]bool, len(saved))
	for _, p := range saved {
		if p.ID == "" || seen[p.ID] {
			t.Fatalf("missing or duplicate profile id %q", p.ID)
		}
		seen[p.ID] = true
	}

	beforeProfiles := store.ListProfiles()
	store.path = t.TempDir()
	if _, err := store.SaveProfiles([]Profile{{ID: "rejected", Name: "rejected"}}); err == nil {
		t.Fatal("SaveProfiles with broken persistence succeeded")
	}
	if got := store.ListProfiles(); len(got) != len(beforeProfiles) {
		t.Fatalf("failed batch left %d profiles, want %d", len(got), len(beforeProfiles))
	}
}

func TestImportLinksWritesLargeBatchOnce(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := store.GetSettings()
	settings.GeoIPAutoDetect = false
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	var payload strings.Builder
	for i := 1; i <= 2000; i++ {
		payload.WriteString(nodeLink(i))
		payload.WriteByte('\n')
	}
	before := saveCount.Load()
	app := NewApp(store, make(chan LogEntry, 64))
	res, err := app.ImportLinks(payload.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 2000 || res.Invalid != 0 {
		t.Fatalf("import = %d profiles / %d invalid", len(res.Profiles), res.Invalid)
	}
	if got := saveCount.Load() - before; got != 1 {
		t.Fatalf("large import wrote config.json %d times, want 1", got)
	}
}

func TestDeleteProfilePrunesRuntimeState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 3}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProfile("id"); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	state := readPersistenceJSON(t, filepath.Join(dir, "state.json"))
	var doc struct {
		Profiles map[string]json.RawMessage `json:"profiles"`
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Profiles["id"]; ok {
		t.Fatal("deleted profile remains in state.json")
	}
}

func TestSubscriptionRepointUsesAtomicStoreUpdate(t *testing.T) {
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "a.store.SaveProfiles(repoint)") {
		t.Fatal("subscription repoint reads and saves profiles in separate lock acquisitions")
	}
	if !strings.Contains(string(raw), "a.store.RepointSubscriptionProfiles(oldURL, newURL)") {
		t.Fatal("subscription repoint must use the store's locked read-modify-write")
	}
}

func TestRepointSubscriptionProfilesUpdatesOnlyLiveMatches(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	profiles := []Profile{
		{ID: "one", Name: "one", SubURL: "https://old.example/feed", LastLatency: 11},
		{ID: "two", Name: "two", SubURL: "https://other.example/feed", LastLatency: 22},
		{ID: "three", Name: "three", SubURL: "https://old.example/feed", LastLatency: 33},
	}
	if _, err := store.SaveProfiles(profiles); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProfile("three"); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	before := saveCount.Load()
	n, err := store.RepointSubscriptionProfiles("https://old.example/feed", "https://new.example/feed")
	if err != nil || n != 1 {
		t.Fatalf("repoint = %d, %v; want 1, nil", n, err)
	}
	if got := saveCount.Load() - before; got != 1 {
		t.Fatalf("repoint wrote config.json %d times, want 1", got)
	}
	one, _ := store.GetProfile("one")
	two, _ := store.GetProfile("two")
	if one.SubURL != "https://new.example/feed" || one.LastLatency != 11 {
		t.Fatalf("repointed profile = %+v", one)
	}
	if two.SubURL != "https://other.example/feed" || two.LastLatency != 22 {
		t.Fatalf("unrelated profile changed = %+v", two)
	}
	if _, ok := store.GetProfile("three"); ok {
		t.Fatal("repoint resurrected a deleted profile")
	}
	before = saveCount.Load()
	if n, err := store.RepointSubscriptionProfiles("https://old.example/feed", "https://new.example/feed"); err != nil || n != 0 {
		t.Fatalf("second repoint = %d, %v; want 0, nil", n, err)
	}
	if got := saveCount.Load() - before; got != 0 {
		t.Fatalf("no-op repoint wrote config.json %d times", got)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	one, _ = reloaded.GetProfile("one")
	if one.SubURL != "https://new.example/feed" || one.LastLatency != 11 {
		t.Fatalf("reloaded repointed profile = %+v", one)
	}
}

func TestRepointSubscriptionProfilesRollsBackOnFailure(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", SubURL: "https://old.example/feed"}); err != nil {
		t.Fatal(err)
	}
	before := store.ListProfiles()
	store.path = t.TempDir()
	if _, err := store.RepointSubscriptionProfiles("https://old.example/feed", "https://new.example/feed"); err == nil {
		t.Fatal("repoint with broken persistence succeeded")
	}
	if got := store.ListProfiles(); len(got) != 1 || got[0].SubURL != before[0].SubURL {
		t.Fatalf("failed repoint changed profiles: %+v", got)
	}
}

func TestFailedLegacyRuntimeMigrationPreservesSourceConfig(t *testing.T) {
	dir := t.TempDir()
	settings := DefaultSettings()
	settings.SpoofAutoDetectReverted = true
	settings.TunOSStackMigrated = true
	settings.AetherScanResults = []AetherScanResult{{Endpoint: "192.0.2.30:443", Protocol: "wg", LatencyMs: 12}}
	legacy := fileConfig{
		Profiles: []Profile{{ID: "legacy-id", Name: "node", LastLatency: 7, CountryCode: "DE"}},
		Settings: settings,
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "legacy-id", Name: "renamed"}); err == nil {
		t.Fatal("durable save stripped the only legacy runtime copy while state.json was unavailable")
	}
	preserved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(preserved, []byte(`"lastLatency":7`)) || !bytes.Contains(preserved, []byte(`"aetherScanResults"`)) {
		t.Fatalf("legacy runtime data was removed before migration succeeded:\n%s", preserved)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "legacy-id", Name: "renamed"}); err != nil {
		t.Fatalf("durable save after migration recovery: %v", err)
	}
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reloaded.GetProfile("legacy-id")
	if !ok || p.Name != "renamed" || p.LastLatency != 7 || p.CountryCode != "DE" {
		t.Fatalf("recovered profile = %+v, %v", p, ok)
	}
	if got := reloaded.GetSettings().AetherScanResults; len(got) != 1 || got[0].LatencyMs != 12 {
		t.Fatalf("recovered Aether results = %+v", got)
	}
}

func TestFailedStateFlushSchedulesRetry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.stateDelay = 10 * time.Millisecond
	store.stateMaxDelay = time.Second
	statePath := filepath.Join(dir, "state.json")
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 9}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err == nil {
		t.Fatal("Flush succeeded with state.json blocked by a directory")
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "automatic state write retry", func() bool {
		_, err := os.Stat(statePath)
		return err == nil
	})
	reloaded, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reloaded.GetProfile("id")
	if p.LastLatency != 9 {
		t.Fatalf("LastLatency after retry = %d, want 9", p.LastLatency)
	}
}

func TestStateWriteRetriesAreBounded(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.stateDelay = time.Millisecond
	statePath := filepath.Join(dir, "state.json")
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "node", LastLatency: 9}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bounded state write retries", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.stateRetry >= maxStateWriteRetries && store.stateTimer == nil
	})
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * defaultStateRetryDelay)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state write retried after its bounded attempts: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("explicit Flush after retry exhaustion: %v", err)
	}
}

func TestBackupRefreshFailurePreservesPreviousBackup(t *testing.T) {
	old := configBackupEvery
	configBackupEvery = 0
	t.Cleanup(func() { configBackupEvery = old })
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "before", LastLatency: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id", Name: "after"}); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "config.json.bak")
	before, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateProfile(Profile{ID: "id", Name: "latest"})
	if err := os.Mkdir(backupPath+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFile(); err != nil {
		t.Fatalf("durable save failed because its backup could not be replaced: %v", err)
	}
	after, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed atomic backup replacement modified the previous backup")
	}
}
