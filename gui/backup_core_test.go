package main

// Advanced Mode Phase F tests: ExportAllData / ImportAllData /
// ViewRawConfig / ResetToDefaults.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// phaseFTestApp builds an App with one profile, one subscription and
// one core override, nothing running.
func phaseFTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	saved, err := app.SaveProfile(Profile{Name: "backup-me", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	// Fixed stamp so export/import round-trips compare exactly
	// (time.Now carries a monotonic reading JSON drops).
	stamp := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	saved.LastTestedAt = &stamp
	saved.LastLatency = 42
	if _, err := app.store.SaveProfile(saved); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.AddSubscription(Subscription{URL: "https://example.com/sub", Name: "ex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetCoreConfigSection("route", `{"final":"block"}`); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetActiveProfileID(saved.ID); err != nil {
		t.Fatal(err)
	}
	return app
}

func snapshotState(t *testing.T, app *App) (Settings, []Profile) {
	t.Helper()
	return app.store.GetSettings(), app.store.ListProfiles()
}

func TestExportAllDataRoundTrip(t *testing.T) {
	app := phaseFTestApp(t)
	blob, err := app.ExportAllData()
	if err != nil {
		t.Fatalf("ExportAllData = %v, want nil", err)
	}
	var f backupFile
	if err := json.Unmarshal([]byte(blob), &f); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	if f.Version != backupVersion {
		t.Fatalf("export version = %d, want %d", f.Version, backupVersion)
	}
	wantSettings, wantProfiles := snapshotState(t, app)
	// Import into a fresh store: state must come back identical.
	store2, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app2 := NewApp(store2, make(chan LogEntry, 512))
	if err := app2.ImportAllData(blob); err != nil {
		t.Fatalf("ImportAllData(export) = %v, want nil", err)
	}
	gotSettings, gotProfiles := snapshotState(t, app2)
	if !reflect.DeepEqual(wantSettings, gotSettings) {
		t.Fatalf("settings differ after round-trip:\nwant %+v\ngot  %+v", wantSettings, gotSettings)
	}
	if !reflect.DeepEqual(wantProfiles, gotProfiles) {
		t.Fatalf("profiles differ after round-trip:\nwant %+v\ngot  %+v", wantProfiles, gotProfiles)
	}
}

func TestImportAllDataRejectsBadVersion(t *testing.T) {
	app := phaseFTestApp(t)
	wantSettings, wantProfiles := snapshotState(t, app)
	for _, blob := range []string{
		`{"version":2,"settings":{},"profiles":[]}`,
		`{"version":0,"settings":{},"profiles":[]}`,
		`{"settings":{},"profiles":[]}`,
	} {
		if err := app.ImportAllData(blob); err == nil {
			t.Fatalf("ImportAllData(%s) = nil, want version error", blob)
		} else if !strings.Contains(err.Error(), "version") {
			t.Fatalf("ImportAllData(%s) = %v, want a version-naming error", blob, err)
		}
	}
	if gotS, gotP := snapshotState(t, app); !reflect.DeepEqual(wantSettings, gotS) || !reflect.DeepEqual(wantProfiles, gotP) {
		t.Fatal("rejected import touched the store")
	}
}

func TestImportAllDataRejectsBadShape(t *testing.T) {
	app := phaseFTestApp(t)
	wantSettings, wantProfiles := snapshotState(t, app)
	s, err := json.Marshal(wantSettings)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"profiles":      `{"version":1,"settings":` + string(s) + `,"profiles":{"not":"array"}}`,
		"subscriptions": `{"version":1,"settings":` + string(s) + `,"profiles":[],"subscriptions":"nope"}`,
		"coreOverrides": `{"version":1,"settings":` + string(s) + `,"profiles":[],"coreOverrides":{"route":[1,2]}}`,
		"settings":      `{"version":1,"settings":[1],"profiles":[]}`,
		"profile-name":  `{"version":1,"settings":` + string(s) + `,"profiles":[{"id":"x","name":"","core":0}]}`,
		"json":          `{oops`,
	}
	for name, blob := range cases {
		if err := app.ImportAllData(blob); err == nil {
			t.Fatalf("ImportAllData(%s) = nil, want shape error", name)
		} else if !strings.Contains(err.Error(), "backup:") {
			t.Fatalf("ImportAllData(%s) = %v, want a backup:-prefixed error", name, err)
		}
	}
	if gotS, gotP := snapshotState(t, app); !reflect.DeepEqual(wantSettings, gotS) || !reflect.DeepEqual(wantProfiles, gotP) {
		t.Fatal("rejected import touched the store")
	}
}

func TestResetToDefaultsRequiresToken(t *testing.T) {
	app := phaseFTestApp(t)
	for _, bad := range []string{"", "reset-all", "RESET", "RESET-ALL "} {
		if err := app.ResetToDefaults(bad); err == nil {
			t.Fatalf("ResetToDefaults(%q) = nil, want token error", bad)
		}
	}
	if n := len(app.store.ListProfiles()); n != 1 {
		t.Fatalf("refused reset touched profiles (n=%d)", n)
	}
	if err := app.ResetToDefaults("RESET-ALL"); err != nil {
		t.Fatalf("ResetToDefaults(token) = %v, want nil", err)
	}
	if n := len(app.store.ListProfiles()); n != 0 {
		t.Fatalf("reset left %d profiles, want 0", n)
	}
	got := app.store.GetSettings()
	if !reflect.DeepEqual(got, DefaultSettings()) {
		t.Fatalf("reset settings = %+v, want defaults", got)
	}
}

func TestViewRawConfigMergesOverrides(t *testing.T) {
	app := phaseFTestApp(t)
	// Idle: no active connection, so the viewer is empty (not an error).
	got, err := app.ViewRawConfig()
	if err != nil {
		t.Fatalf("ViewRawConfig idle = %v, want nil", err)
	}
	if got != "" {
		t.Fatalf("ViewRawConfig idle = %q, want empty", got)
	}
	// The render helper (what a running core would receive) carries
	// the stored override.
	p, ok := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
	if !ok {
		t.Fatal("no active profile")
	}
	raw, err := renderActiveConfig(&p, app.store.GetSettings())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"block"`) {
		t.Fatalf("rendered config missing the route override: %s", raw)
	}
}
