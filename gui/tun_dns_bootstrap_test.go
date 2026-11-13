package main

import (
	"encoding/json"
	"net/netip"
	"testing"
)

// PattN never renders a TUN DNS block that is a single server. Beside the
// resolver that answers the hijacked queries it always keeps one
// detour-free entry — GenDnsServers' direct-dns-1 / GenDnsProtectCustom's
// local-local, plain UDP — and names that entry in
// route.default_domain_resolver (SingboxRoutingService sets it
// unconditionally, TUN or not).
//
// The reason is a cycle that only bites when the tunnel is up: the proxy's
// own hostname has to be resolved before the proxy can be dialed, and
// sing-box looks that hostname up through default_domain_resolver, or
// through the DNS block's default transport when the field is absent. If
// the only server there detours to "proxy", the lookup is asked to ride
// the connection it exists to establish. The adapter comes up, the core
// reports nothing wrong, and no page ever loads.

func tunProxyRender(t *testing.T, tune func(*Settings)) map[string]any {
	t.Helper()
	s := DefaultSettings()
	s.TunEnabled = true
	if tune != nil {
		tune(&s)
	}
	// A hostname-addressed proxy: exactly the profile shape whose address
	// must be resolved before anything can be dialed.
	p := Profile{
		Name: "host", Protocol: "shadowsocks",
		Address: "de1.example-vps.net", Port: 443,
		Method: "aes-256-gcm", IDOrPassword: "pw", Core: CoreSingBox,
	}
	raw, err := BuildSingBoxConfig(&p, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("rendered TUN config is not valid JSON: %v", err)
	}
	return cfg
}

// dnsServersOf returns dns.servers as maps.
func dnsServersOf(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	dnsBlock, _ := cfg["dns"].(map[string]any)
	if dnsBlock == nil {
		t.Fatal("TUN mode rendered no dns block")
	}
	raw, _ := dnsBlock["servers"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		m, _ := s.(map[string]any)
		out = append(out, m)
	}
	return out
}

// serverNeedsNoLookup reports whether sing-box can dial a DNS server
// without resolving a name first: it must carry no detour (a detour to
// "proxy" puts the proxy dial in front of the answer), and it must be
// either the OS resolver or an IP literal.
func serverNeedsNoLookup(srv map[string]any) bool {
	if _, detour := srv["detour"]; detour {
		return false
	}
	switch typ, _ := srv["type"].(string); typ {
	case "local", "hosts", "fakeip":
		return true
	}
	addr, err := netip.ParseAddr(asString(srv["server"]))
	return err == nil && addr.IsValid()
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// TestTunFallbackDNSResolvesTheProxyHostWithoutTheProxy is the cycle
// check. The proxied resolver stays the answer for everything the tunnel
// carries — that is the leak protection — but the block must also hold a
// server that needs nothing from the proxy, and
// route.default_domain_resolver must name it.
func TestTunFallbackDNSResolvesTheProxyHostWithoutTheProxy(t *testing.T) {
	cfg := tunProxyRender(t, nil)
	servers := dnsServersOf(t, cfg)

	var free []string
	for _, srv := range servers {
		if serverNeedsNoLookup(srv) {
			free = append(free, asString(srv["tag"]))
		}
	}
	if len(free) == 0 {
		t.Fatalf("every dns server detours or names a host to look up first; "+
			"nothing can resolve the proxy address without the proxy: %v", servers)
	}

	route, _ := cfg["route"].(map[string]any)
	ddb, _ := route["default_domain_resolver"].(map[string]any)
	if ddb == nil {
		t.Fatalf("route.default_domain_resolver absent, so a hostname proxy address is "+
			"resolved by the DNS block default (%v); want it pinned to a detour-free server (%v)",
			route["default_domain_resolver"], free)
	}
	want := asString(ddb["server"])
	found := false
	for _, srv := range servers {
		if asString(srv["tag"]) == want {
			found = true
			if !serverNeedsNoLookup(srv) {
				t.Fatalf("default_domain_resolver %q cannot be dialed without a prior lookup: %v", want, srv)
			}
		}
	}
	if !found {
		t.Fatalf("default_domain_resolver names %q, which is not among %v", want, servers)
	}
}

// TestTunFallbackDNSStillAnswersFromTheProxiedResolver keeps the fix from
// throwing the leak protection away: the query the tunnel hijacks must
// still be answered by a resolver that rides the proxy, with the
// detour-free entry used only as the bootstrap.
func TestTunFallbackDNSStillAnswersFromTheProxiedResolver(t *testing.T) {
	cfg := tunProxyRender(t, nil)
	dnsBlock, _ := cfg["dns"].(map[string]any)
	servers := dnsServersOf(t, cfg)

	final, _ := dnsBlock["final"].(string)
	if final == "" {
		t.Fatalf("dns.final absent from %v", dnsBlock)
	}
	for _, srv := range servers {
		if asString(srv["tag"]) != final {
			continue
		}
		if asString(srv["detour"]) != "proxy" {
			t.Fatalf("dns.final %q does not ride the proxy: %v", final, srv)
		}
		return
	}
	t.Fatalf("dns.final %q names no server in %v", final, servers)
}

// TestTunConfiguredSingleDNSKeepsABootstrap: the same cycle is reachable
// without the fallback block at all. One user-configured server that
// rides the proxy is the block's only transport and the only resolver,
// so sing-box still asks it where the proxy is. The bootstrap is added
// to the user's own block, and the user's server stays dns.final.
func TestTunConfiguredSingleDNSKeepsABootstrap(t *testing.T) {
	cfg := tunProxyRender(t, func(s *Settings) {
		s.DnsRemote = "https://1.1.1.1/dns-query"
	})
	servers := dnsServersOf(t, cfg)
	dnsBlock, _ := cfg["dns"].(map[string]any)
	if dnsBlock["final"] != "dns-remote" {
		t.Fatalf("dns.final = %v, want the user's resolver untouched", dnsBlock["final"])
	}

	route, _ := cfg["route"].(map[string]any)
	ddb, _ := route["default_domain_resolver"].(map[string]any)
	if ddb == nil {
		t.Fatalf("route.default_domain_resolver absent with one proxied server: %v", servers)
	}
	want := asString(ddb["server"])
	for _, srv := range servers {
		if asString(srv["tag"]) != want {
			continue
		}
		if !serverNeedsNoLookup(srv) {
			t.Fatalf("default_domain_resolver %q rides the proxy it must resolve: %v", want, srv)
		}
		return
	}
	t.Fatalf("default_domain_resolver names %q, which is not among %v", want, servers)
}
