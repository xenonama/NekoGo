package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A pasted rawOutbound is merged verbatim, and every builder rewrites
// its tag/name afterwards so the route, the SNI front and the latency
// probe can address it by the fixed name. JSON "null" is the one input
// that unmarshals into a map WITHOUT an error and leaves that map nil,
// so the rewrite used to panic ("assignment to entry in nil map") — a
// GUI crash on Connect for a profile whose raw outbound was saved as
// null, e.g. cleared in a hand-edited config.json.

func TestRawOutboundNullDoesNotPanic(t *testing.T) {
	builds := []struct {
		name  string
		build func() (string, error)
	}{
		{"singbox", func() (string, error) {
			return BuildSingBoxConfig(&Profile{Name: "raw", Protocol: "vless", RawOutbound: "null"}, DefaultSettings())
		}},
		{"xray", func() (string, error) {
			return BuildXrayConfig(&Profile{Name: "raw", Protocol: "vless", RawOutbound: "null"}, DefaultSettings(), "127.0.0.1:8080")
		}},
		{"mihomo", func() (string, error) {
			return BuildMihomoConfig(&Profile{Name: "raw", Protocol: "vless", RawOutbound: "null"}, DefaultSettings(), 9090, "secret")
		}},
	}
	for _, b := range builds {
		t.Run(b.name, func(t *testing.T) {
			var out string
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s: rawOutbound \"null\" panicked the builder: %v", b.name, r)
					}
				}()
				out, err = b.build()
			}()
			if err == nil {
				t.Fatalf("%s: rawOutbound \"null\" accepted, rendered %s", b.name, out)
			}
			if !strings.Contains(err.Error(), "rawOutbound") {
				t.Errorf("%s: error %q does not name rawOutbound", b.name, err)
			}
		})
	}
}

// The same guard must keep rejecting the other non-object JSON shapes,
// with the decoder's own message rather than a rewrite panic. An empty
// object is a legitimate direct-style outbound and stays accepted.
func TestRawOutboundNonObjectShapesRejected(t *testing.T) {
	for _, raw := range []string{"[]", "{}", "true", "42", `"x"`, "{"} {
		wantErr := raw != "{}"
		_, err := BuildSingBoxConfig(&Profile{Name: "raw", Protocol: "vless", RawOutbound: raw}, DefaultSettings())
		if wantErr && err == nil {
			t.Errorf("rawOutbound %s: accepted, want rejection", raw)
		}
		if !wantErr && err != nil {
			t.Errorf("rawOutbound %s: %v, want acceptance", raw, err)
		}
	}
}

// A full raw override (Phase G, CoreOverrides["*"]) replaces the whole
// rendered config. "null" is the same decoder trap: it unmarshals into
// a nil map without an error, so it used to be accepted as an override
// and marshal straight back to "null" — a config no engine can start,
// which failed every Connect for a profile already holding one.
func TestRawOverrideNullFallsBackToRenderedDefault(t *testing.T) {
	for _, raw := range []string{"null", "{}"} {
		s := DefaultSettings()
		s.CoreOverrides = map[string]json.RawMessage{rawOverrideKey: json.RawMessage(raw)}
		cfg, err := BuildSingBoxConfig(&Profile{
			Name: "p", Protocol: "vless", Address: "example.com", Port: 443,
			IDOrPassword: "8e4f0a0c-1111-2222-3333-444444444444",
		}, s)
		if err != nil {
			t.Fatalf("raw override %s: %v", raw, err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
			t.Fatalf("raw override %s: rendered config is not a JSON object: %v (%s)", raw, err, cfg)
		}
		if _, ok := doc["outbounds"]; !ok {
			t.Errorf("raw override %s: no outbounds section rendered: %s", raw, cfg)
		}
	}
}

// The editor must refuse a raw config that cannot start an engine,
// rather than storing it and silently wiping every section override.
func TestSetRawConfigRejectsNullAndEmpty(t *testing.T) {
	// An explicit dir, not "": defaultConfigDir resolves through APPDATA on
	// Windows, so "" would load and migrate the developer's real config.
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(store, make(chan LogEntry, 256))
	for _, in := range []string{"null", "{}"} {
		if got, err := a.SetRawConfig(in); err == nil {
			t.Errorf("SetRawConfig(%s): accepted as %s, want rejection", in, got)
		}
	}
	if _, ok := store.GetSettings().CoreOverrides[rawOverrideKey]; ok {
		t.Error("rejected raw config was still stored")
	}
}
