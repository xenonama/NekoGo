package main

import (
	"encoding/json"
	"testing"
)

// chainTestProfile builds a minimal vless profile for builder tests.
func chainTestProfile(core CoreType) *Profile {
	return &Profile{
		Name: "t", Core: core, Protocol: "vless",
		Address: "example.com", Port: 443,
		IDOrPassword: "11111111-1111-1111-1111-111111111111",
		TLS:          true,
	}
}

// findOutbound returns the outbound with the given tag.
func findOutbound(t *testing.T, cfg string, tag string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	obs, ok := doc["outbounds"].([]any)
	if !ok {
		t.Fatal("config has no outbounds array")
	}
	for _, o := range obs {
		if m, ok := o.(map[string]any); ok && m["tag"] == tag {
			return m
		}
	}
	t.Fatalf("outbound %q not found", tag)
	return nil
}

// TestBuildSingBoxConfigWithFingerprint: the TLSFingerprint preset
// lands in the outbound utls JSON; empty keeps the legacy default.
func TestBuildSingBoxConfigWithFingerprint(t *testing.T) {
	p := chainTestProfile(CoreSingBox)
	p.TLSFingerprint = "firefox"
	sb, err := BuildSingBoxConfig(p, DefaultSettings())
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	proxy := findOutbound(t, sb, "proxy")
	tls, _ := proxy["tls"].(map[string]any)
	utls, _ := tls["utls"].(map[string]any)
	if utls["fingerprint"] != "firefox" {
		t.Fatalf("utls fingerprint = %v, want firefox", utls["fingerprint"])
	}

	p2 := chainTestProfile(CoreSingBox)
	sb2, err := BuildSingBoxConfig(p2, DefaultSettings())
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	proxy2 := findOutbound(t, sb2, "proxy")
	tls2, _ := proxy2["tls"].(map[string]any)
	utls2, _ := tls2["utls"].(map[string]any)
	if utls2["fingerprint"] != "chrome" {
		t.Fatalf("default utls fingerprint = %v, want chrome", utls2["fingerprint"])
	}
}

// TestBuildXrayConfigWithFragment: the fragment keys land in the
// stream settings JSON.
func TestBuildXrayConfigWithFragment(t *testing.T) {
	p := chainTestProfile(CoreXray)
	p.TLSFragment = "tlshello"
	p.TLSFragmentLen = "10-20"
	p.TLSFragmentInt = 5
	xr, err := BuildXrayConfig(p, DefaultSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	proxy := findOutbound(t, xr, "proxy")
	stream, _ := proxy["streamSettings"].(map[string]any)
	fm, _ := stream["finalmask"].(map[string]any)
	tcp, _ := fm["tcp"].([]any)
	if len(tcp) != 1 {
		t.Fatalf("finalmask.tcp len = %d, want 1", len(tcp))
	}
	mask, _ := tcp[0].(map[string]any)
	if mask["type"] != "fragment" {
		t.Fatalf("mask type = %v, want fragment", mask["type"])
	}
	st, _ := mask["settings"].(map[string]any)
	if st["packets"] != "tlshello" {
		t.Fatalf("packets = %v, want tlshello", st["packets"])
	}
	length, _ := st["length"].(map[string]any)
	if length["from"] != float64(10) || length["to"] != float64(20) {
		t.Fatalf("length = %v, want {from:10 to:20}", st["length"])
	}
	delay, _ := st["delay"].(map[string]any)
	if delay["to"] != float64(5) {
		t.Fatalf("delay = %v, want to:5", st["delay"])
	}

	// Disabled fragment emits no finalmask.
	p2 := chainTestProfile(CoreXray)
	xr2, err := BuildXrayConfig(p2, DefaultSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	proxy2 := findOutbound(t, xr2, "proxy")
	stream2, _ := proxy2["streamSettings"].(map[string]any)
	if _, ok := stream2["finalmask"]; ok {
		t.Fatal("finalmask present with fragment disabled")
	}
}
