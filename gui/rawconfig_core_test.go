package main

// Advanced Mode Phase G tests: GetRawConfig / SetRawConfig /
// ReloadCoreNow plus the raw-over-sections precedence in both
// managers.

import (
	"encoding/json"
	"strings"
	"testing"
)

// phaseGTestApp builds an App with one sing-box profile and no
// running core (raw methods resolve the stored/first profile idle).
func phaseGTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	saved, err := app.SaveProfile(Profile{Name: "raw", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetActiveProfileID(saved.ID); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestGetRawConfigMergesSectionOverrides(t *testing.T) {
	app := phaseGTestApp(t)
	if _, err := app.SetCoreConfigSection("route", `{"final":"block"}`); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetRawConfig()
	if err != nil {
		t.Fatalf("GetRawConfig = %v, want nil", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("GetRawConfig is not JSON: %v", err)
	}
	route, ok := v["route"].(map[string]any)
	if !ok || route["final"] != "block" {
		t.Fatalf("section override missing from raw config: %s", got)
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("GetRawConfig is not pretty-printed: %s", got)
	}
}

func TestSetRawConfigRejectsBadSyntax(t *testing.T) {
	app := phaseGTestApp(t)
	for name, text := range map[string]string{
		"syntax":   `{oops`,
		"array":    `[1,2]`,
		"inbounds": `{"inbounds":{"type":"mixed"}}`,
		"route":    `{"route":[1]}`,
		"empty":    ``,
	} {
		if name == "empty" {
			continue // empty clears; covered by the reset path below
		}
		if _, err := app.SetRawConfig(text); err == nil {
			t.Fatalf("SetRawConfig(%s) = nil, want error", name)
		}
	}
	if n := len(app.store.GetSettings().CoreOverrides); n != 0 {
		t.Fatalf("rejected raw edits stored %d overrides", n)
	}
}

func TestSetRawConfigClearsSectionOverrides(t *testing.T) {
	app := phaseGTestApp(t)
	if _, err := app.SetCoreConfigSection("route", `{"final":"block"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetCoreConfigSection("dns", `{"servers":["8.8.8.8"]}`); err != nil {
		t.Fatal(err)
	}
	raw := `{"log":{"level":"info"},"inbounds":[],"outbounds":[],"route":{"final":"proxy"}}`
	got, err := app.SetRawConfig(raw)
	if err != nil {
		t.Fatalf("SetRawConfig = %v, want nil", err)
	}
	ov := app.store.GetSettings().CoreOverrides
	if _, ok := ov[rawOverrideKey]; !ok {
		t.Fatal("raw blob not stored under \"*\"")
	}
	for _, sec := range []string{"route", "dns"} {
		if _, ok := ov[sec]; ok {
			t.Fatalf("section override %q survived the raw save", sec)
		}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("SetRawConfig return is not JSON: %v", err)
	}
	// Empty input clears the raw override and re-renders defaults.
	got, err = app.SetRawConfig("   ")
	if err != nil {
		t.Fatalf("SetRawConfig(empty) = %v, want nil", err)
	}
	if _, ok := app.store.GetSettings().CoreOverrides[rawOverrideKey]; ok {
		t.Fatal("empty SetRawConfig did not clear the raw override")
	}
	if !strings.Contains(got, `"proxy"`) {
		t.Fatalf("cleared raw did not re-render defaults: %s", got)
	}
}

func TestManagersPreferRawOverrideOverSections(t *testing.T) {
	app := phaseGTestApp(t)
	if _, err := app.SetCoreConfigSection("route", `{"final":"block"}`); err != nil {
		t.Fatal(err)
	}
	raw := `{"log":{"level":"warning"},"inbounds":[],"outbounds":[],"route":{"final":"proxy"}}`
	if _, err := app.SetRawConfig(raw); err != nil {
		t.Fatal(err)
	}
	// A stale section entry underneath must not win (hand-crafted to
	// prove precedence even if a clear were ever skipped).
	if err := app.store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m["route"] = json.RawMessage(`{"final":"block"}`)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	p, ok := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
	if !ok {
		t.Fatal("no active profile")
	}
	out, err := BuildSingBoxConfig(&p, app.store.GetSettings())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	route, ok := cfg["route"].(map[string]any)
	if !ok || route["final"] != "proxy" {
		t.Fatalf("raw override did not win: %v", cfg["route"])
	}
	if cfg["log"] == nil {
		t.Fatalf("raw-only keys dropped: %s", out)
	}
	// Section editors slice the raw blob, not the stale entry.
	got, err := app.GetCoreConfigSection("route")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"proxy"`) || strings.Contains(got, `"block"`) {
		t.Fatalf("section getter did not prefer raw: %s", got)
	}
}

// TestReloadCoreNowRoundTrip reloads through a stub starter (no core
// binary needed). The live-binary path is skipped when no core is
// present; Connect/Disconnect integration tests cover real starts.
func TestReloadCoreNowRoundTrip(t *testing.T) {
	app := phaseGTestApp(t)
	if _, err := app.singbox.resolveCore(); err != nil {
		t.Skipf("no core present: %v", err)
	}
	if err := app.ReloadCoreNow(); err != nil {
		t.Fatalf("live ReloadCoreNow = %v", err)
	}
	if _, _, running := app.cores.Status(); !running {
		t.Fatal("core not running after live reload")
	}
	if err := app.cores.Stop(); err != nil {
		t.Fatal(err)
	}
}

// TestReloadCoreNowUsesStarter exercises the stop-then-start flow
// deterministically through the coreStarter hook (no binary).
func TestReloadCoreNowUsesStarter(t *testing.T) {
	app := phaseGTestApp(t)
	var calls []string
	app.coreStarter = func(p *Profile, s Settings) error {
		calls = append(calls, p.ID)
		return nil
	}
	// Idle with an active profile: start only.
	if err := app.ReloadCoreNow(); err != nil {
		t.Fatalf("ReloadCoreNow idle = %v, want nil", err)
	}
	if len(calls) != 1 {
		t.Fatalf("starter calls = %d, want 1", len(calls))
	}
	// No profiles at all: clear error, not a silent no-op.
	empty, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare := NewApp(empty, make(chan LogEntry, 512))
	if err := bare.ReloadCoreNow(); err == nil {
		t.Fatal("ReloadCoreNow without profiles = nil, want error")
	}
}
