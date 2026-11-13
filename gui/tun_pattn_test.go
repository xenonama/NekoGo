package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// PattN builds its TUN from a settings group (ServiceLib/Models/Configs/
// ConfigItems.cs TunModeItem) and renders it from a template
// (ServiceLib/Sample/tun_singbox_inbound) plus fixed guards
// (ServiceLib/Sample/tun_singbox_rules). NekoGo rendered the inbound from
// constants and emitted none of the guards, so a rewritten TUN must prove
// two things separately: that every setting reaches the config it names,
// and that the guards the tunnel needs are there whether or not the user
// asked for them.

// tunRender decodes a TUN-mode config for the given settings tweak.
func tunRender(t *testing.T, tune func(*Settings)) map[string]any {
	t.Helper()
	s := DefaultSettings()
	s.TunEnabled = true
	if tune != nil {
		tune(&s)
	}
	raw, err := BuildSingBoxConfig(&Profile{Protocol: "direct", Core: CoreSingBox}, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("rendered TUN config is not valid JSON: %v", err)
	}
	return cfg
}

// routeRulesOf returns the rendered route.rules.
func routeRulesOf(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	route, _ := cfg["route"].(map[string]any)
	raw, _ := route["rules"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

// TestTunRendersRejectsNetBIOSAndMulticast: PattN's tun_singbox_rules
// rejects UDP 135/137/138/139/5353 and the 224.0.0.0/3 + ff00::/8
// multicast ranges. Inside a TUN those are answered by nothing — the
// core either dials them at the proxy or, for the multicast ranges,
// loops them back into the interface auto_route just pointed at the
// tunnel — so the rules are not a preference but the absence of a
// destination. NekoGo rendered neither.
func TestTunRendersRejectsNetBIOSAndMulticast(t *testing.T) {
	rules := routeRulesOf(t, tunRender(t, nil))
	ports := map[float64]bool{}
	cidrs := map[string]bool{}
	var udpOnly bool
	for _, r := range rules {
		if r["action"] != "reject" {
			continue
		}
		for _, p := range asList(r["port"]) {
			if f, ok := p.(float64); ok {
				ports[f] = true
				// A port reject must be udp-only: 135/tcp is RPC
				// the tunnel may genuinely need to carry.
				if n := asList(r["network"]); len(n) == 1 && n[0] == "udp" {
					udpOnly = true
				}
			}
		}
		for _, c := range asList(r["ip_cidr"]) {
			if s, ok := c.(string); ok {
				cidrs[s] = true
			}
		}
	}
	for _, want := range []float64{135, 137, 138, 139, 5353} {
		if !ports[want] {
			t.Errorf("TUN rendered no reject for udp port %v (NetBIOS/RPC/LLMNR)", want)
		}
	}
	if len(ports) > 0 && !udpOnly {
		t.Error("the port reject is not limited to udp, so it blocks RPC over TCP too")
	}
	for _, want := range []string{"224.0.0.0/3", "ff00::/8"} {
		if !cidrs[want] {
			t.Errorf("TUN rendered no reject for %s (multicast the tunnel cannot answer)", want)
		}
	}
}

// asList reads a rendered JSON array without caring which Go type the
// renderer used.
func asList(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out
	}
	return nil
}

// TestProxyModeKeepsTunGuardsOut: the same rules are TUN-only. Without an
// interface that captures the whole route table there is no loop to
// break, and an unconditional multicast reject would take down LAN
// discovery for a user who only set a proxy.
func TestProxyModeKeepsTunGuardsOut(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = false
	raw, err := BuildSingBoxConfig(&Profile{Protocol: "direct", Core: CoreSingBox}, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"224.0.0.0/3", "ff00::/8", "5353"} {
		if strings.Contains(raw, want) {
			t.Errorf("proxy-mode config carries the TUN-only rule %q", want)
		}
	}
}
