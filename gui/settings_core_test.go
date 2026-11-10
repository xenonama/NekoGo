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

// An override that carries no settings must not delete the section the
// renderer produced. A stored {"dns":{}} did exactly that, and in TUN
// mode it is fatal rather than cosmetic: every query is hijacked into
// sing-box, so the rendered block is the tunnel's only resolver. With it
// wiped, lookups fell back to the local resolver — which on a filtering
// network answers blocked names with the interceptor's address — so
// Google and YouTube failed while the connection still showed as up.
func TestEmptySectionOverrideKeepsTheRenderedBlock(t *testing.T) {
	app := phaseETestApp(t)
	p, ok := app.store.GetProfile(app.store.GetSettings().ActiveProfileID)
	if !ok {
		t.Fatal("no active profile")
	}
	base := app.store.GetSettings()
	base.TunEnabled = true

	serversOf := func(s Settings) []any {
		t.Helper()
		raw, err := BuildSingBoxConfig(&p, s)
		if err != nil {
			t.Fatalf("BuildSingBoxConfig: %v", err)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		dns, ok := cfg["dns"].(map[string]any)
		if !ok {
			t.Fatalf("the rendered config has no dns block: %s", raw)
		}
		servers, _ := dns["servers"].([]any)
		return servers
	}

	rendered := serversOf(base)
	if len(rendered) == 0 {
		t.Fatal("the TUN default DNS block rendered no servers — test premise is wrong")
	}

	empty := base
	empty.CoreOverrides = map[string]json.RawMessage{"dns": json.RawMessage(`{}`)}
	if got := serversOf(empty); len(got) != len(rendered) {
		t.Fatalf("an empty {} override left %d dns servers, want the rendered %d", len(got), len(rendered))
	}

	emptyArr := base
	emptyArr.CoreOverrides = map[string]json.RawMessage{"inbound": json.RawMessage(`[]`)}
	rawArr, err := BuildSingBoxConfig(&p, emptyArr)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig(inbound []): %v", err)
	}
	var cfgArr map[string]any
	if err := json.Unmarshal([]byte(rawArr), &cfgArr); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if ins, _ := cfgArr["inbounds"].([]any); len(ins) == 0 {
		t.Fatal("an empty [] override deleted the rendered inbounds")
	}

	// A real override still replaces the block — the fix is about empty
	// ones, not about weakening the editor.
	real := base
	real.CoreOverrides = map[string]json.RawMessage{
		"dns": json.RawMessage(`{"servers":[{"tag":"mine","address":"9.9.9.9"}]}`),
	}
	if got := serversOf(real); len(got) != 1 {
		t.Fatalf("a real dns override stopped replacing the block: %v", got)
	}
}

// Storing an empty section is how that override got there in the first
// place, so the setter treats "nothing" as "no override" and clears the
// key: the editor then shows the rendered default again instead of a
// value that silently deletes it at connect.
func TestSetCoreConfigSectionEmptyObjectClearsTheOverride(t *testing.T) {
	app := phaseETestApp(t)
	if _, err := app.SetCoreConfigSection("dns", `{"servers":[{"tag":"x","address":"1.1.1.1"}]}`); err != nil {
		t.Fatalf("storing a real override: %v", err)
	}
	if _, err := app.SetCoreConfigSection("dns", `{}`); err != nil {
		t.Fatalf("SetCoreConfigSection(dns, {}): %v", err)
	}
	if n := len(app.store.GetSettings().CoreOverrides); n != 0 {
		t.Fatalf("an empty object kept %d override(s); it must clear instead of deleting the rendered block", n)
	}
	// Same for the array-shaped section.
	if _, err := app.SetCoreConfigSection("inbound", `[{"type":"mixed","tag":"mixed-in","listen":"127.0.0.1","listen_port":9999}]`); err != nil {
		t.Fatalf("storing a real inbound override: %v", err)
	}
	if _, err := app.SetCoreConfigSection("inbound", `[]`); err != nil {
		t.Fatalf("SetCoreConfigSection(inbound, []): %v", err)
	}
	if n := len(app.store.GetSettings().CoreOverrides); n != 0 {
		t.Fatalf("an empty array kept %d override(s)", n)
	}
	// Blank text stays an error: it is a malformed submission, not an
	// intent to clear (Reset is the explicit way).
	if _, err := app.SetCoreConfigSection("dns", `   `); err == nil {
		t.Fatal("SetCoreConfigSection(dns, blank) = nil, want an error")
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

// An empty section override is dropped on load, so a config.json written
// before the merge learned to skip them heals instead of being silently
// ignored forever (and the section editor stops showing {} as though the
// user had configured a DNS block).
func TestMigrateEmptyCoreOverridesDropsThem(t *testing.T) {
	s := DefaultSettings()
	s.CoreOverrides = map[string]json.RawMessage{
		"dns":          json.RawMessage(`{}`),
		"inbound":      json.RawMessage(`[]`),
		"route":        json.RawMessage(`{"final":"block"}`),
		"experimental": json.RawMessage(`not json`),
	}
	if !migrateEmptyCoreOverrides(&s) {
		t.Fatal("migrateEmptyCoreOverrides reported no change")
	}
	if _, ok := s.CoreOverrides["dns"]; ok {
		t.Fatal("the empty dns override survived")
	}
	if _, ok := s.CoreOverrides["inbound"]; ok {
		t.Fatal("the empty inbound override survived")
	}
	if _, ok := s.CoreOverrides["route"]; !ok {
		t.Fatal("the real route override was dropped")
	}
	// A corrupt entry stays: mergeCoreOverrides fails fast on those, and
	// quietly deleting one would hide the reason a connect stops working.
	if _, ok := s.CoreOverrides["experimental"]; !ok {
		t.Fatal("the unparseable override was dropped instead of kept for its fail-fast")
	}
	if migrateEmptyCoreOverrides(&s) {
		t.Fatal("a second migration pass reported a change")
	}
}

// The same thing through the real load path: this is the shape of the
// reported config.json ({"dns":{}}), re-opened the way the app opens it.
func TestConfigLoadDropsEmptyCoreOverrides(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	if err := store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m["dns"] = json.RawMessage(`{}`)
		return true
	}); err != nil {
		t.Fatalf("UpdateCoreOverrides: %v", err)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got := reopened.GetSettings().CoreOverrides; len(got) != 0 {
		t.Fatalf("re-opened config kept %d override(s): %v", len(got), got)
	}
}
