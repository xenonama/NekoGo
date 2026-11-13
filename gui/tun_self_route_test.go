package main

import (
	"encoding/json"
	"testing"
)

type routeProbe struct {
	Route struct {
		Rules []map[string]any `json:"rules"`
	} `json:"route"`
	Inbounds []map[string]any `json:"inbounds"`
}

// tunProbeSettings is the settings buildTunProbe renders, exported to
// the test so the guard it asserts is derived from the same values
// rather than from a constant that could drift from them.
func tunProbeSettings(tun bool) Settings {
	s := DefaultSettings()
	s.TunEnabled = tun
	return s
}

func buildTunProbe(t *testing.T, tun bool) routeProbe {
	t.Helper()
	s := tunProbeSettings(tun)
	raw, err := BuildSingBoxConfig(&Profile{Protocol: "direct", Core: CoreSingBox}, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg routeProbe
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The TUN's own address must be dropped first, before the DNS rule, so
// auto_route cannot loop a packet back into the outbound.
func TestBuildSingBoxConfigDropsTunSelfAddress(t *testing.T) {
	cfg := buildTunProbe(t, true)
	if len(cfg.Route.Rules) < 2 {
		t.Fatalf("rules = %v, want the drop rule plus the DNS rule", cfg.Route.Rules)
	}
	first := cfg.Route.Rules[0]
	if first["action"] != "reject" || first["method"] != "drop" {
		t.Fatalf("first rule = %v, want reject/drop", first)
	}
	cidrs, _ := first["ip_cidr"].([]any)
	// The guard must name the address the interface actually got, as a
	// single host: a constant that repeats it in another form goes stale
	// the moment the TUN address is a setting.
	want := tunSelfHostPrefixes(tunAddressList(tunProbeSettings(true)))
	if len(cidrs) != len(want) {
		t.Fatalf("ip_cidr = %v, want the host of every interface address %v", first["ip_cidr"], want)
	}
	for i, c := range cidrs {
		if c != want[i] {
			t.Fatalf("ip_cidr[%d] = %v, want %v", i, c, want[i])
		}
	}
	// The hijack still has to come after the guard that keeps the
	// tunnel's own address out of it; how many rules sit between them is
	// the leak guards' business (tun_pattn_test.go).
	hijack := -1
	for i, r := range cfg.Route.Rules {
		if r["action"] == "hijack-dns" {
			hijack = i
			break
		}
	}
	if hijack < 1 {
		t.Fatalf("no DNS hijack rule after the first rule: %v", cfg.Route.Rules)
	}
	tunInbound := cfg.Inbounds[len(cfg.Inbounds)-1]
	if tunInbound["type"] != "tun" {
		t.Fatalf("tun inbound = %v, want type tun", tunInbound)
	}
	// The address is a list now: sing-box reads it as one
	// (badoption.Listable[netip.Prefix]), and an IPv6 entry joins it
	// when the OS gate allows.
	addrs, _ := tunInbound["address"].([]any)
	if len(addrs) != 1 || addrs[0] != tunAddress {
		t.Fatalf("tun address = %v, want only %s", tunInbound["address"], tunAddress)
	}
}

func TestBuildSingBoxConfigNoTunSelfRuleWithoutTun(t *testing.T) {
	cfg := buildTunProbe(t, false)
	for _, r := range cfg.Route.Rules {
		if r["action"] == "reject" || r["method"] == "drop" {
			t.Fatalf("unexpected drop rule without TUN: %v", cfg.Route.Rules)
		}
	}
}
