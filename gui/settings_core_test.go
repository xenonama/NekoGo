package main

// Advanced Mode Phase E tests: the three CoreConfigSection bound
// methods plus the manager merge of Settings.CoreOverrides.

import (
	"encoding/json"
	"strings"
	"testing"
)

// phaseETestApp builds an App with one sing-box profile and no running
// core (section editors resolve the stored/first profile while idle).
func phaseETestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	saved, err := app.SaveProfile(Profile{Name: "e2e", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetActiveProfileID(saved.ID); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestGetCoreConfigSectionReturnsDefault(t *testing.T) {
	app := phaseETestApp(t)
	for _, section := range []string{"route", "dns", "inbound", "experimental"} {
		got, err := app.GetCoreConfigSection(section)
		if err != nil {
			t.Fatalf("GetCoreConfigSection(%q) = %v, want default JSON", section, err)
		}
		var v any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Fatalf("GetCoreConfigSection(%q) is not JSON: %v", section, err)
		}
		if section == "inbound" {
			if _, ok := v.([]any); !ok {
				t.Fatalf("inbound default is %T, want array", v)
			}
			continue
		}
		if _, ok := v.(map[string]any); !ok {
			t.Fatalf("%s default is %T, want object", section, v)
		}
	}
	// The route default must carry the proxy final (i.e. it came from
	// the real rendered config, not a stub).
	got, err := app.GetCoreConfigSection("route")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"proxy"`) {
		t.Fatalf("route default missing proxy final: %s", got)
	}
	if _, err := app.GetCoreConfigSection("bogus"); err == nil {
		t.Fatal("GetCoreConfigSection(bogus) = nil, want error")
	}
}

func TestSetCoreConfigSectionValidatesJSON(t *testing.T) {
	app := phaseETestApp(t)
	if _, err := app.SetCoreConfigSection("route", "{not json"); err == nil {
		t.Fatal("SetCoreConfigSection(invalid JSON) = nil, want error")
	}
	if _, err := app.SetCoreConfigSection("route", ""); err == nil {
		t.Fatal("SetCoreConfigSection(empty) = nil, want error")
	}
	if _, err := app.SetCoreConfigSection("route", `[1,2]`); err == nil {
		t.Fatal("SetCoreConfigSection(route=array) = nil, want object error")
	}
	if _, err := app.SetCoreConfigSection("inbound", `{"type":"mixed"}`); err == nil {
		t.Fatal("SetCoreConfigSection(inbound=object) = nil, want array error")
	}
	if _, err := app.SetCoreConfigSection("bogus", `{}`); err == nil {
		t.Fatal("SetCoreConfigSection(bogus section) = nil, want error")
	}
	got, err := app.SetCoreConfigSection("route", `{"final":"direct"}`)
	if err != nil {
		t.Fatalf("SetCoreConfigSection(valid) = %v, want nil", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil || v["final"] != "direct" {
		t.Fatalf("SetCoreConfigSection did not return normalized JSON: %q", got)
	}
}

func TestSetCoreConfigSectionMergesIntoManagerConfig(t *testing.T) {
	app := phaseETestApp(t)
	p, ok := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
	if !ok {
		t.Fatal("no active profile")
	}
	if _, err := app.SetCoreConfigSection("route", `{"final":"block","rules":[]}`); err != nil {
		t.Fatal(err)
	}
	settings := app.store.GetSettings()
	raw, err := BuildSingBoxConfig(&p, settings)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	route, ok := cfg["route"].(map[string]any)
	if !ok || route["final"] != "block" {
		t.Fatalf("route override not merged: %v", cfg["route"])
	}
	// Inbound overrides replace the whole inbounds array.
	if _, err := app.SetCoreConfigSection("inbound", `[{"type":"mixed","tag":"mixed-in","listen":"127.0.0.1","listen_port":9999}]`); err != nil {
		t.Fatal(err)
	}
	settings = app.store.GetSettings()
	raw, err = BuildSingBoxConfig(&p, settings)
	if err != nil {
		t.Fatal(err)
	}
	cfg = map[string]any{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	ins, ok := cfg["inbounds"].([]any)
	if !ok || len(ins) != 1 {
		t.Fatalf("inbound override not merged: %v", cfg["inbounds"])
	}
}

func TestResetCoreConfigSectionClearsOverride(t *testing.T) {
	app := phaseETestApp(t)
	if _, err := app.SetCoreConfigSection("dns", `{"servers":["8.8.8.8"]}`); err != nil {
		t.Fatal(err)
	}
	if len(app.store.GetSettings().CoreOverrides) != 1 {
		t.Fatal("override not stored")
	}
	if err := app.ResetCoreConfigSection("dns"); err != nil {
		t.Fatal(err)
	}
	if len(app.store.GetSettings().CoreOverrides) != 0 {
		t.Fatalf("override not cleared: %v", app.store.GetSettings().CoreOverrides)
	}
	// Resetting again is a no-op success.
	if err := app.ResetCoreConfigSection("dns"); err != nil {
		t.Fatalf("double reset = %v, want nil", err)
	}
	if err := app.ResetCoreConfigSection("bogus"); err == nil {
		t.Fatal("ResetCoreConfigSection(bogus) = nil, want error")
	}
	// After reset the manager renders the default again (no "dns"
	// key in the minimal config, or at least not our override).
	p, ok := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
	if !ok {
		t.Fatal("no active profile")
	}
	raw, err := BuildSingBoxConfig(&p, app.store.GetSettings())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "8.8.8.8") {
		t.Fatalf("override survived reset: %s", raw)
	}
}
