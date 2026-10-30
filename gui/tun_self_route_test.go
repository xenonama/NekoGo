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

func buildTunProbe(t *testing.T, tun bool) routeProbe {
	t.Helper()
	s := DefaultSettings()
	s.TunEnabled = tun
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
	if len(cidrs) != 1 || cidrs[0] != tunSelfAddress {
		t.Fatalf("ip_cidr = %v, want [%s]", first["ip_cidr"], tunSelfAddress)
	}
	if cfg.Route.Rules[1]["action"] != "hijack-dns" {
		t.Fatalf("second rule = %v, want the TUN DNS hijack", cfg.Route.Rules[1])
	}
	tunInbound := cfg.Inbounds[len(cfg.Inbounds)-1]
	if tunInbound["type"] != "tun" || tunInbound["address"] != tunAddress {
		t.Fatalf("tun inbound = %v, want type tun at %s", tunInbound, tunAddress)
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
