package main

import (
	"encoding/json"
	"testing"
)

// sing-box refuses to START on a config with a dangling tag reference:
// route.final must name an outbound, every rule's "outbound" must name
// one, and a DNS server's "detour" must too. The builders assemble those
// names from several independent rewrites (raw outbound re-tagging, SNI
// front, chaining, DNS), so a fuzz over the user-pasteable parts checks
// the whole rendered document against what the engine will accept.

func outboundTags(cfg map[string]any) map[string]bool {
	tags := map[string]bool{}
	list, _ := cfg["outbounds"].([]any)
	for _, o := range list {
		m, _ := o.(map[string]any)
		if t, _ := m["tag"].(string); t != "" {
			tags[t] = true
		}
	}
	return tags
}

func assertSingBoxReferences(t *testing.T, cfg map[string]any) {
	t.Helper()
	tags := outboundTags(cfg)
	if !tags["proxy"] {
		t.Fatalf("no outbound tagged \"proxy\" (route.final names it): %s", mustJSON(cfg))
	}
	if !tags["direct"] || !tags["block"] {
		t.Fatalf("outbounds lost the \"direct\"/\"block\" fall-backs: %s", mustJSON(cfg))
	}
	route, _ := cfg["route"].(map[string]any)
	if final, _ := route["final"].(string); final != "proxy" {
		t.Fatalf("route.final = %q, want \"proxy\": %s", final, mustJSON(cfg))
	}
	if rules, ok := route["rules"].([]any); ok {
		for _, r := range rules {
			rm, _ := r.(map[string]any)
			ref, _ := rm["outbound"].(string)
			if ref != "" && !tags[ref] {
				t.Fatalf("route rule names unknown outbound %q: %s", ref, mustJSON(cfg))
			}
		}
	}
	dns, _ := cfg["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	for _, s := range servers {
		sm, _ := s.(map[string]any)
		det, _ := sm["detour"].(string)
		if det != "" && !tags[det] {
			t.Fatalf("dns server %q detours to unknown outbound %q: %s",
				sm["tag"], det, mustJSON(cfg))
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(b)
}

func FuzzBuildSingBoxConfig(f *testing.F) {
	seeds := []string{
		`{"type":"vless","uuid":"b63b7e2a-1c2d-3e4f-5a6b-7c8d9e0f1a2b","server":"example.com","server_port":443}`,
		`{"type":"socks","server":"127.0.0.1","server_port":1080}`,
		`{"type":"direct"}`,
		`{"tag":"vless-out","type":"trojan","password":"p","server":"h.example"}`,
		`{"type":"wireguard"}`,
		"null", "{}", "[]", `[{"a":1}]`, "true", "42", `"str"`, "{", "",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		p := &Profile{
			Name: "fz", Protocol: "vless", Address: "example.com", Port: 443,
			IDOrPassword: "b63b7e2a-1c2d-3e4f-5a6b-7c8d9e0f1a2b", RawOutbound: raw,
		}
		cfg, err := BuildSingBoxConfig(p, DefaultSettings())
		if err != nil {
			return
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
			t.Fatalf("builder emitted config that is not a JSON object: %v (%q)", err, cfg)
		}
		assertSingBoxReferences(t, doc)
	})
}

// The section editors store arbitrary JSON per section, merged over the
// rendered config. Neither the merge nor the following rewrite may
// panic, and a successful merge must still yield an object.
func FuzzMergeCoreOverrides(f *testing.F) {
	values := []string{
		`null`, `{}`, `[]`, `{"final":"proxy"}`, `{"rules":[{"outbound":"direct"}]}`,
		`{"servers":[{"type":"https","tag":"dns-remote","server":"dns.example","detour":"proxy"}]}`,
		`[{"type":"tun"}]`, `"str"`, `3`, `[`, `{"a":{"b":[{"c":1}]}}`,
	}
	for _, sec := range append([]string{rawOverrideKey}, coreConfigSections...) {
		for _, v := range values {
			f.Add(sec, v)
		}
	}
	f.Fuzz(func(t *testing.T, sec, val string) {
		for _, xray := range []bool{false, true} {
			settings := DefaultSettings()
			settings.CoreOverrides = map[string]json.RawMessage{sec: json.RawMessage(val)}
			p := &Profile{
				Name: "fz", Protocol: "vless", Address: "example.com", Port: 443,
				IDOrPassword: "b63b7e2a-1c2d-3e4f-5a6b-7c8d9e0f1a2b",
			}
			cfg, err := BuildSingBoxConfig(p, settings)
			if xray {
				cfg, err = BuildXrayConfig(p, settings, "127.0.0.1:8080")
			}
			if err != nil {
				continue
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
				t.Fatalf("override %s=%s produced a non-object config: %v (%q)", sec, val, err, cfg)
			}
		}
	})
}
