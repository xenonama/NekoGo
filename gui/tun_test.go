package main

import (
	"encoding/json"
	"testing"
)

// tunInbound returns the rendered tun-inbound map (nil when absent).
func tunInbound(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	inbounds, _ := cfg["inbounds"].([]any)
	for _, in := range inbounds {
		m, _ := in.(map[string]any)
		if m["type"] == "tun" {
			return m
		}
	}
	return nil
}

// routeBlock returns the rendered route map.
func routeBlock(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	route, _ := cfg["route"].(map[string]any)
	if route == nil {
		t.Fatal("no route block rendered")
	}
	return route
}

// TestSingBoxTunMTUAndDNSHijack: TUN renders a 1350 MTU (sing-box would
// otherwise default to 9000 and black-hole big packets) and hijacks DNS
// into its own module instead of leaking every query to the ISP
// resolver, backed by the default remote resolver when the user set
// none. auto_detect_interface keeps the proxied traffic out of the TUN
// it just installed.
func TestSingBoxTunMTUAndDNSHijack(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	cfg := singDNS(t, s)

	tun := tunInbound(t, cfg)
	if tun == nil {
		t.Fatal("TUN enabled but no tun inbound rendered")
	}
	if got := tun["mtu"]; got != float64(1350) {
		t.Fatalf("tun mtu = %v, want 1350", got)
	}
	if got := tun["stack"]; got != tunStackDefault {
		t.Fatalf("tun stack = %v, want the default %v", got, tunStackDefault)
	}
	if tun["auto_route"] != true || tun["strict_route"] != false {
		t.Fatalf("tun routes = %v/%v, want true/false", tun["auto_route"], tun["strict_route"])
	}

	route := routeBlock(t, cfg)
	if route["auto_detect_interface"] != true {
		t.Fatalf("route.auto_detect_interface = %v, want true", route["auto_detect_interface"])
	}
	rules, _ := route["rules"].([]any)
	if len(rules) < 2 {
		t.Fatalf("rules = %v, want the TUN self-address guard then the DNS rule", rules)
	}
	// The drop guard for the TUN's own address comes first (it must
	// precede any rule that could send the packet back to the outbound).
	guard, _ := rules[0].(map[string]any)
	if guard["action"] != "reject" || guard["method"] != "drop" {
		t.Fatalf("first rule = %v, want the TUN self-address reject/drop guard", guard)
	}
	// The hijack is whichever rule carries it — the TUN leak guards
	// (tun_pattn_test.go) render between the self-address guard and it,
	// and the rule itself is a logical-or over port 53 and protocol dns.
	var hijack map[string]any
	for _, r := range rules[1:] {
		m, _ := r.(map[string]any)
		if m["action"] == "hijack-dns" {
			hijack = m
			break
		}
	}
	if hijack == nil {
		t.Fatalf("no DNS rule among %v", rules)
	}
	if !hijackRuleCoversPort53(hijack) {
		t.Fatalf("DNS rule does not catch port 53: %v", hijack)
	}
	if hijack["action"] != "hijack-dns" {
		t.Fatalf("DNS rule = %v, want action hijack-dns", hijack)
	}
	if _, leaked := hijack["outbound"]; leaked {
		t.Fatalf("DNS rule still carries an outbound (leaks outside the tunnel): %v", hijack)
	}

	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		t.Fatal("TUN mode rendered no dns block")
	}
	if dns["final"] != "remote" {
		t.Fatalf("dns.final = %v, want remote", dns["final"])
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("dns.servers = %v, want the default resolver and the detour-free bootstrap", servers)
	}
	srv, _ := servers[0].(map[string]any)
	if srv["tag"] != "remote" || srv["type"] != "https" || srv["server"] != "8.8.8.8" || srv["path"] != "/dns-query" {
		t.Fatalf("default dns server = %v", srv)
	}
	if _, hasAddr := srv["address"]; hasAddr {
		t.Fatalf("default dns server still has legacy address key: %v", srv)
	}
	if _, detour := srv["detour"]; detour {
		t.Fatalf("default dns server carries a detour; want the tunnel (route.final): %v", srv)
	}
}

// TestSingBoxTunKeepsConfiguredDNS: a user-configured upstream is never
// replaced by the TUN default; the hijack only changes where the query
// is answered.
func TestSingBoxTunKeepsConfiguredDNS(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	s.DnsRemote = "https://1.1.1.1/dns-query"
	s.DnsDirect = "local"
	cfg := singDNS(t, s)

	dns, _ := cfg["dns"].(map[string]any)
	if dns["final"] != "dns-remote" {
		t.Fatalf("dns.final = %v, want dns-remote", dns["final"])
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("dns.servers = %v, want the user's two servers only", servers)
	}
	for _, srv := range servers {
		m, _ := srv.(map[string]any)
		if m["tag"] == "remote" {
			t.Fatalf("TUN default resolver was injected over the user's DNS: %v", servers)
		}
	}
	first, _ := servers[0].(map[string]any)
	if first["tag"] != "dns-remote" || first["type"] != "https" || first["server"] != "1.1.1.1" || first["path"] != "/dns-query" {
		t.Fatalf("user remote dns = %v", first)
	}
	if _, hasAddr := first["address"]; hasAddr {
		t.Fatalf("user remote dns still has legacy address key: %v", first)
	}
}

// TestSingBoxProxyModeDNSUnchanged: without TUN the generated config
// keeps the past shape byte-for-byte — direct DNS rule, no dns block,
// no auto_detect_interface, no MTU (no tun inbound at all).
func TestSingBoxProxyModeDNSUnchanged(t *testing.T) {
	cfg := singDNS(t, DefaultSettings())
	if tunInbound(t, cfg) != nil {
		t.Fatal("TUN disabled but a tun inbound was rendered")
	}
	if _, ok := cfg["dns"]; ok {
		t.Fatal("proxy mode rendered a dns block; want system DNS")
	}
	route := routeBlock(t, cfg)
	if _, ok := route["auto_detect_interface"]; ok {
		t.Fatal("proxy mode set auto_detect_interface; want unchanged")
	}
	rules, _ := route["rules"].([]any)
	first, _ := rules[0].(map[string]any)
	if first["protocol"] != "dns" || first["outbound"] != "direct" {
		t.Fatalf("proxy-mode dns rule = %v, want protocol dns + outbound direct", first)
	}
	if route["final"] != "proxy" {
		t.Fatalf("route.final = %v, want proxy", route["final"])
	}
}

// TestSingBoxTunConfigIsValidJSON keeps the rendered TUN config
// parseable by the core: the route/dns additions must not carry Go
// values json cannot encode.
func TestSingBoxTunConfigIsValidJSON(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	p := Profile{Name: "t", Protocol: "direct", Address: "h", Port: 1}
	raw, err := BuildSingBoxConfig(&p, s)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("rendered TUN config is not valid JSON: %v", err)
	}
}
