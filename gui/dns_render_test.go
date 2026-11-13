package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Remote DNS field is plain text, not sing-box JSON: every shape a
// user can copy out of a resolver's own docs must both save and render a
// server block the engine starts. These tests pin the accepted shapes,
// the rendered entry (including the detour, which is what makes a
// hostname resolver startable at all), the byte-stable default, the
// rejected garbage and the load-time reset.

// dnsProxyProfile is a real proxy outbound, so route.final is "proxy"
// and the resolvers may ride it — the normal case for this field.
func dnsProxyProfile() Profile {
	return Profile{Name: "t", Protocol: "vless", Address: "127.0.0.1", Port: 443,
		IDOrPassword: "54f5c4aad-2c94-4f74-9b8b-a69cf3f1f9c2"}
}

// decodeDNSConfig renders the config and decodes it with json.Number, so
// ports compare as text instead of float64.
func decodeDNSConfig(t *testing.T, p Profile, s Settings) map[string]any {
	t.Helper()
	raw, err := BuildSingBoxConfig(&p, s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var cfg map[string]any
	if err := dec.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// dnsServers returns the rendered dns block plus its server list.
func dnsServers(t *testing.T, cfg map[string]any) (map[string]any, []any) {
	t.Helper()
	dns, _ := cfg["dns"].(map[string]any)
	if dns == nil {
		t.Fatalf("config rendered no dns block: %v", cfg["dns"])
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) == 0 {
		t.Fatalf("dns block has no servers: %v", dns)
	}
	return dns, servers
}

func entryString(entry map[string]any, key string) string {
	v, _ := entry[key].(string)
	return v
}

// TestDnsRemoteAcceptsAllShapes: the documented plain-text shapes all
// save and all render the typed sing-box entry with the right address
// and a real detour (never detour:"" and never a missing one for
// https/tls — without it the engine refuses a hostname resolver).
func TestDnsRemoteAcceptsAllShapes(t *testing.T) {
	cases := []struct {
		in       string
		wantType string
		wantAddr string
		wantPort string
		wantPath string
	}{
		{"https://1.1.1.1/dns-query", "https", "1.1.1.1", "", "/dns-query"},
		{"https://8.8.8.8/dns-query", "https", "8.8.8.8", "", "/dns-query"},
		{"https://dns.google/dns-query", "https", "dns.google", "", "/dns-query"},
		{"tls://8.8.8.8", "tls", "8.8.8.8", "", ""},
		{"tls://dns.google", "tls", "dns.google", "", ""},
		{"tcp://8.8.8.8", "tcp", "8.8.8.8", "", ""},
		{"udp://8.8.8.8", "udp", "8.8.8.8", "", ""},
		{"8.8.8.8", "udp", "8.8.8.8", "", ""},
		{"dns.google", "udp", "dns.google", "", ""},
		{"  https://1.1.1.1/dns-query  ", "https", "1.1.1.1", "", "/dns-query"},
		{"https://1.1.1.1:8443/dns-query", "https", "1.1.1.1", "8443", "/dns-query"},
		{"tls://dns.google:853", "tls", "dns.google", "853", ""},
		{"127.0.0.1:5354", "udp", "127.0.0.1", "5354", ""},
		{"udp://[2001:4860:4860::8888]:53", "udp", "2001:4860:4860::8888", "53", ""},
		{"[::1]", "udp", "::1", "", ""},
		{"quic://dns.adguard.com", "quic", "dns.adguard.com", "", ""},
		{"local", "local", "", "", ""},
		{`{"type":"https","server":"1.1.1.1","path":"/dns-query"}`, "https", "1.1.1.1", "", "/dns-query"},
	}
	for _, tc := range cases {
		if err := validateDNSServer(tc.in); err != nil {
			t.Fatalf("validateDNSServer(%q): %v", tc.in, err)
		}
		s := DefaultSettings()
		s.DnsRemote = tc.in
		cfg := decodeDNSConfig(t, dnsProxyProfile(), s)
		dns, servers := dnsServers(t, cfg)
		if len(servers) != 1 {
			t.Fatalf("%q rendered %d servers, want the one resolver: %v", tc.in, len(servers), servers)
		}
		if entryString(dns, "final") != "dns-remote" {
			t.Fatalf("%q: dns.final = %v, want dns-remote", tc.in, dns["final"])
		}
		entry, _ := servers[0].(map[string]any)
		if got := entryString(entry, "type"); got != tc.wantType {
			t.Fatalf("%q: type = %q, want %q (%v)", tc.in, got, tc.wantType, entry)
		}
		if got := entryString(entry, "server"); got != tc.wantAddr {
			t.Fatalf("%q: server = %q, want %q (%v)", tc.in, got, tc.wantAddr, entry)
		}
		if tc.wantPort == "" {
			if _, ok := entry["server_port"]; ok {
				t.Fatalf("%q: unexpected server_port %v (%v)", tc.in, entry["server_port"], entry)
			}
		} else if got, _ := entry["server_port"].(json.Number); string(got) != tc.wantPort {
			t.Fatalf("%q: server_port = %v, want %s (%v)", tc.in, entry["server_port"], tc.wantPort, entry)
		}
		if got := entryString(entry, "path"); got != tc.wantPath {
			t.Fatalf("%q: path = %q, want %q (%v)", tc.in, got, tc.wantPath, entry)
		}
		if _, ok := entry["address"]; ok {
			t.Fatalf("%q: legacy address key still rendered: %v", tc.in, entry)
		}
		// A port must never be folded into "server": sing-box re-parses
		// the value as ONE address and then cannot look it up.
		if strings.Contains(tc.wantAddr, ":") && net.ParseIP(entryString(entry, "server")) == nil {
			t.Fatalf("%q: server is not a bare IP literal: %v", tc.in, entry)
		}
		if detour := entryString(entry, "detour"); detour != "proxy" {
			t.Fatalf("%q: detour = %q, want proxy (route.final) — empty or missing detours make the engine refuse to start: %v",
				tc.in, detour, entry)
		}
	}
}

// TestDnsRemoteHostnameBootstrap: without a proxy to ride (a
// direct-only profile), a hostname resolver still needs something that
// can answer its own lookup, so the block carries a domain_resolver and
// the system-resolver bootstrap instead of a detour.
func TestDnsRemoteHostnameBootstrap(t *testing.T) {
	s := DefaultSettings()
	s.DnsRemote = "tls://dns.google"
	cfg := decodeDNSConfig(t, Profile{Name: "t", Protocol: "direct", Address: "h", Port: 1}, s)
	dns, servers := dnsServers(t, cfg)
	remote, _ := servers[0].(map[string]any)
	if _, has := remote["detour"]; has {
		t.Fatalf("direct profile's remote dns carries a detour: %v", remote)
	}
	dr, _ := remote["domain_resolver"].(map[string]any)
	if dr == nil {
		t.Fatalf("hostname resolver without a detour has no domain_resolver: %v", remote)
	}
	if entryString(dns, "final") != "dns-remote" {
		t.Fatalf("dns.final = %v, want dns-remote", dns["final"])
	}
	bootTag, _ := dr["server"].(string)
	if bootTag != dnsBootstrapTag {
		t.Fatalf("domain_resolver.server = %q, want %q", bootTag, dnsBootstrapTag)
	}
	if len(servers) != 2 || servers[1].(map[string]any)["tag"] != dnsBootstrapTag {
		t.Fatalf("bootstrap resolver missing: %v", servers)
	}
	route, _ := cfg["route"].(map[string]any)
	ddb, _ := route["default_domain_resolver"].(map[string]any)
	if ddb == nil || ddb["server"] != dnsBootstrapTag {
		t.Fatalf("route.default_domain_resolver = %v, want the bootstrap tag (sing-box 1.14 refuses a multi-server block without it)",
			route["default_domain_resolver"])
	}
}

// TestDnsRemoteEmptyKeepsDefault: an empty field renders no dns block at
// all in proxy mode (system DNS, byte-for-byte as before) and the exact
// default resolver block in TUN mode — the proxied DoH entry plus the
// detour-free bootstrap that resolves the proxy's own hostname.
func TestDnsRemoteEmptyKeepsDefault(t *testing.T) {
	const goldenTunBlock = `{"final":"remote","servers":[{"detour":"proxy","path":"/dns-query","server":"8.8.8.8","tag":"remote","type":"https"},{"server":"8.8.8.8","tag":"dns-bootstrap","type":"udp"}]}`

	proxy := DefaultSettings()
	cfg := decodeDNSConfig(t, dnsProxyProfile(), proxy)
	if _, ok := cfg["dns"]; ok {
		t.Fatalf("empty Remote DNS rendered a dns block: %v", cfg["dns"])
	}
	route, _ := cfg["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	first, _ := rules[0].(map[string]any)
	if first["protocol"] != "dns" || first["outbound"] != "direct" {
		t.Fatalf("empty Remote DNS changed the dns rule: %v", first)
	}
	if _, ok := route["default_domain_resolver"]; ok {
		t.Fatalf("empty Remote DNS set route.default_domain_resolver: %v", route["default_domain_resolver"])
	}

	tun := DefaultSettings()
	tun.TunEnabled = true
	cfg = decodeDNSConfig(t, dnsProxyProfile(), tun)
	dns, _ := dnsServers(t, cfg)
	got, err := json.Marshal(dns)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goldenTunBlock {
		t.Fatalf("TUN default dns block = %s, want golden %s", got, goldenTunBlock)
	}
}

// TestDnsRemoteRejectsGarbage: values that cannot be a resolver fail at
// save, not at engine start.
func TestDnsRemoteRejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"://nope",
		"https://",
		"https://:443/dns-query",
		"ftp://1.1.1.1",
		"tls://dns google",
		"not_a_host",
		"8.8.8.8:0",
		"8.8.8.8:99999",
		`{"server":"1.1.1.1"}`,
		`{"type":"ftp","server":"1.1.1.1"}`,
	} {
		if err := validateDNSServer(bad); err == nil {
			t.Fatalf("validateDNSServer(%q): want error, got nil", bad)
		}
		s := DefaultSettings()
		s.DnsRemote = bad
		if _, err := BuildSingBoxConfig(dnsProxyProfilePtr(), s); err == nil {
			t.Fatalf("BuildSingBoxConfig accepted %q", bad)
		}
		store, err := NewConfigStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		app := NewApp(store, make(chan LogEntry, 8))
		if err := app.SaveSettings(s); err == nil {
			t.Fatalf("SaveSettings accepted %q", bad)
		}
	}
}

func dnsProxyProfilePtr() *Profile {
	p := dnsProxyProfile()
	return &p
}

// TestDnsRemoteTrimsAtSave: a pasted value keeps its surrounding
// whitespace out of the store while the value itself persists.
func TestDnsRemoteTrimsAtSave(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	s := DefaultSettings()
	s.DnsRemote = "  https://1.1.1.1/dns-query "
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if got := store.GetSettings().DnsRemote; got != "https://1.1.1.1/dns-query" {
		t.Fatalf("stored DnsRemote = %q, want the trimmed value", got)
	}
}

// TestDnsRemoteMigration: a value the parser rejects cannot survive a
// restart as a permanent Connect failure — the load resets it to the
// system default, says so once, and writes that back.
func TestDnsRemoteMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	const saved = `{"profiles":[],"settings":{"mixedPort":20122,"dnsRemote":"ftp://1.1.1.1","dnsDirect":"has space"}}`
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	flags := log.Flags()
	log.SetFlags(0)
	log.SetOutput(&logs)
	defer func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	}()

	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	log.SetOutput(os.Stderr)

	if got := store.GetSettings().DnsRemote; got != "" {
		t.Fatalf("migrated DnsRemote = %q, want empty (system default)", got)
	}
	if got := store.GetSettings().DnsDirect; got != "" {
		t.Fatalf("migrated DnsDirect = %q, want empty", got)
	}
	text := logs.String()
	if !strings.Contains(text, `[config] invalid DnsRemote "ftp://1.1.1.1", resetting to empty`) {
		t.Fatalf("migration log missing the DnsRemote line, got:\n%s", text)
	}
	if !strings.Contains(text, `[config] invalid DnsDirect "has space", resetting to empty`) {
		t.Fatalf("migration log missing the DnsDirect line, got:\n%s", text)
	}

	// The reset persists: a second load must not repeat the message.
	logs.Reset()
	store2, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	log.SetOutput(os.Stderr)
	if strings.Contains(logs.String(), "resetting to empty") {
		t.Fatalf("reset logged again on reload:\n%s", logs.String())
	}
	if store2.GetSettings().DnsRemote != "" {
		t.Fatalf("reloaded DnsRemote = %q, want empty", store2.GetSettings().DnsRemote)
	}

	// And a good value is never reset.
	log.SetFlags(0)
	log.SetOutput(&logs)
	if err := os.WriteFile(path, []byte(`{"profiles":[],"settings":{"mixedPort":20122,"dnsRemote":"tls://dns.google"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store3, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	log.SetOutput(os.Stderr)
	if got := store3.GetSettings().DnsRemote; got != "tls://dns.google" {
		t.Fatalf("valid DnsRemote = %q, want it kept", got)
	}
	if strings.Contains(logs.String(), "resetting to empty") {
		t.Fatalf("a valid value was reset:\n%s", logs.String())
	}
}

// TestDnsShapesPassEngineCheck: the rendered blocks are validated by the
// engines themselves, not just by the shape assertions above — the whole
// point of the detour and the bootstrap resolvers is that sing-box
// refuses to start without them. Both profiles matter: a proxy profile
// gets detour "proxy", a direct-only one gets a domain_resolver plus the
// local bootstrap instead.
func TestDnsShapesPassEngineCheck(t *testing.T) {
	cores := []string{}
	for _, candidate := range []string{
		filepath.Join("build", "bin", "nekobox_core.exe"),
		filepath.Join("build", "bin", "sing-box.exe"),
		filepath.Join("bin", "sing-box.exe"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			cores = append(cores, candidate)
		}
	}
	if len(cores) == 0 {
		t.Skip("no engine binaries built in the tree")
	}
	profiles := map[string]Profile{
		"proxy":  dnsProxyProfile(),
		"direct": {Name: "d", Protocol: "direct", Address: "h", Port: 1},
	}
	for _, in := range []string{
		"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query", "https://dns.google/dns-query",
		"tls://8.8.8.8", "tls://dns.google", "tcp://8.8.8.8", "udp://8.8.8.8", "8.8.8.8",
		"dns.google", "https://1.1.1.1:8443/dns-query", "tls://dns.google:853",
		"127.0.0.1:5354", "udp://[2001:4860:4860::8888]:53", "local",
		`{"type":"https","server":"1.1.1.1","path":"/dns-query"}`,
	} {
		for _, tun := range []bool{false, true} {
			for kind, p := range profiles {
				s := DefaultSettings()
				s.DnsRemote = in
				s.TunEnabled = tun
				raw, err := BuildSingBoxConfig(&p, s)
				if err != nil {
					t.Fatalf("%s/tun=%v %q: %v", kind, tun, in, err)
				}
				path := filepath.Join(t.TempDir(), "dns.json")
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				for _, core := range cores {
					out, err := exec.Command(core, "check", "-c", path).CombinedOutput()
					if err != nil {
						t.Fatalf("%s (%s/tun=%v %q) rejected the config: %v\n%s\nconfig: %s",
							core, kind, tun, in, err, out, raw)
					}
				}
			}
		}
	}
}
