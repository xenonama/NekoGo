package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNormalizeInboundAddress: empty means loopback, valid IPs pass,
// hostnames and garbage are rejected (fail fast at save, never a
// late bind failure at Connect).
func TestNormalizeInboundAddress(t *testing.T) {
	for in, want := range map[string]string{
		"":              "127.0.0.1",
		"  ":            "127.0.0.1",
		"127.0.0.1":     "127.0.0.1",
		"0.0.0.0":       "0.0.0.0",
		" 192.168.1.1 ": "192.168.1.1",
		"::1":           "::1",
	} {
		got, err := normalizeInboundAddress(in)
		if err != nil || got != want {
			t.Fatalf("normalizeInboundAddress(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"localhost", "example.com", "999.1.1.1", "127.0.0.1/24"} {
		if _, err := normalizeInboundAddress(bad); err == nil {
			t.Fatalf("normalizeInboundAddress(%q): want error, got nil", bad)
		}
	}
}

// TestValidateDNSServer: empty is legal (system default); URLs, IPs
// and "local" pass; whitespace and absurd lengths fail.
func TestValidateDNSServer(t *testing.T) {
	for _, ok := range []string{"", "https://8.8.8.8/dns-query", "tls://1.1.1.1", "8.8.8.8", "local", "localhost"} {
		if err := validateDNSServer(ok); err != nil {
			t.Fatalf("validateDNSServer(%q): %v", ok, err)
		}
	}
	if err := validateDNSServer("has space"); err == nil {
		t.Fatalf("spaced value: want error")
	}
	if err := validateDNSServer(strings.Repeat("x", 300)); err == nil {
		t.Fatalf("300-char value: want error")
	}
}

// singDNS renders the default config for assertions.
func singDNS(t *testing.T, s Settings) map[string]any {
	t.Helper()
	p := Profile{Name: "t", Protocol: "direct", Address: "h", Port: 1}
	raw, err := BuildSingBoxConfig(&p, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func mixedListen(t *testing.T, cfg map[string]any) string {
	t.Helper()
	inbounds, _ := cfg["inbounds"].([]any)
	mixed, _ := inbounds[0].(map[string]any)
	listen, _ := mixed["listen"].(string)
	return listen
}

// TestSingBoxInboundAndDNSDefaults: default configs are byte-stable
// with the past (loopback listen, NO dns block — system DNS kept).
func TestSingBoxInboundAndDNSDefaults(t *testing.T) {
	cfg := singDNS(t, DefaultSettings())
	if got := mixedListen(t, cfg); got != "127.0.0.1" {
		t.Fatalf("listen = %q, want 127.0.0.1", got)
	}
	if _, ok := cfg["dns"]; ok {
		t.Fatalf("default config renders a dns block; want none (system DNS)")
	}
}

// TestSingBoxInboundAddress: LAN share renders into mixed-in.
func TestSingBoxInboundAddress(t *testing.T) {
	s := DefaultSettings()
	s.InboundAddress = "0.0.0.0"
	cfg := singDNS(t, s)
	if got := mixedListen(t, cfg); got != "0.0.0.0" {
		t.Fatalf("listen = %q, want 0.0.0.0", got)
	}
}

// TestSingBoxDNSBlock: remote-only, both-sides, and direct-only
// shapes; final always points at a listed tag.
func TestSingBoxDNSBlock(t *testing.T) {
	s := DefaultSettings()
	s.DnsRemote = "https://8.8.8.8/dns-query"
	cfg := singDNS(t, s)
	dns, _ := cfg["dns"].(map[string]any)
	if dns == nil || dns["final"] != "dns-remote" {
		t.Fatalf("dns = %v, want final dns-remote", cfg["dns"])
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("dns.servers = %v, want exactly 1 server", servers)
	}
	remote, _ := servers[0].(map[string]any)
	if remote["tag"] != "dns-remote" || remote["type"] != "https" || remote["server"] != "8.8.8.8" || remote["path"] != "/dns-query" {
		t.Fatalf("remote dns server = %v, want typed https form", remote)
	}
	if _, hasAddr := remote["address"]; hasAddr {
		t.Fatalf("remote dns server still has legacy address key: %v", remote)
	}
	s.DnsDirect = "local"
	cfg = singDNS(t, s)
	dns, _ = cfg["dns"].(map[string]any)
	servers, _ = dns["servers"].([]any)
	if len(servers) != 2 || dns["final"] != "dns-remote" {
		t.Fatalf("dns = %v, want 2 servers + remote final", dns)
	}
	direct, _ := servers[1].(map[string]any)
	// "local" is the system resolver; the entry must stay detour-free:
	// the engine rejects a detour to the bare direct outbound.
	if direct["tag"] != "dns-direct" || direct["type"] != "local" {
		t.Fatalf("direct server = %v, want the local resolver", direct)
	}
	if _, hasDetour := direct["detour"]; hasDetour {
		t.Fatalf("direct server carries a detour: %v", direct)
	}

	directOnly := DefaultSettings()
	directOnly.DnsDirect = "local"
	cfg = singDNS(t, directOnly)
	dns, _ = cfg["dns"].(map[string]any)
	if dns["final"] != "dns-direct" {
		t.Fatalf("dns = %v, want final dns-direct", dns)
	}
}

// TestXrayInboundAddress: http-in/socks-in follow the setting;
// api-in still follows apiAddr.
func TestXrayInboundAddress(t *testing.T) {
	s := DefaultSettings()
	s.InboundAddress = "0.0.0.0"
	p := Profile{Name: "t", Protocol: "vless", Address: "h", Port: 443, IDOrPassword: "uuid"}
	raw, err := BuildXrayConfig(&p, s, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	inbounds, _ := cfg["inbounds"].([]any)
	want := map[string]string{"http-in": "0.0.0.0", "socks-in": "0.0.0.0", "api-in": "127.0.0.1"}
	for _, in := range inbounds {
		m, _ := in.(map[string]any)
		tag, _ := m["tag"].(string)
		if w, ok := want[tag]; ok {
			if listen, _ := m["listen"].(string); listen != w {
				t.Fatalf("%s listen = %q, want %q", tag, listen, w)
			}
		}
	}
}

// TestSaveSettingsValidatesInboundDNS: bad values fail at save
// (never reach a late Connect bind failure); good values persist
// normalized.
func TestSaveSettingsValidatesInboundDNS(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	bad := DefaultSettings()
	bad.InboundAddress = "not-an-ip"
	if err := app.SaveSettings(bad); err == nil {
		t.Fatalf("bad inbound address: want error")
	}
	bad = DefaultSettings()
	bad.DnsRemote = "has space"
	if err := app.SaveSettings(bad); err == nil {
		t.Fatalf("bad dns server: want error")
	}
	good := DefaultSettings()
	good.InboundAddress = ""
	good.DnsRemote = "https://8.8.8.8/dns-query"
	if err := app.SaveSettings(good); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if got := store.GetSettings().InboundAddress; got != "127.0.0.1" {
		t.Fatalf("stored inbound = %q, want normalized loopback", got)
	}
}
