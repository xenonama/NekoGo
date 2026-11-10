package main

import (
	"encoding/json"
	"net"
	"testing"
)

// spoofOffSettings returns defaults with the global SNI-spoofing switch
// turned off (the new Settings → Connection toggle).
func spoofOffSettings() Settings {
	s := DefaultSettings()
	off := false
	s.SpoofEnabled = &off
	return s
}

func firstOutbound(t *testing.T, raw string) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	obs, _ := cfg["outbounds"].([]any)
	if len(obs) == 0 {
		t.Fatalf("no outbounds in %s", raw)
	}
	ob, _ := obs[0].(map[string]any)
	return ob
}

func portOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// TestSpoofEnabledDefaultsOn: configs written before the toggle existed
// (nil) keep the engine enabled; only an explicit false disables it.
func TestSpoofEnabledDefaultsOn(t *testing.T) {
	if !spoofEnabled(DefaultSettings()) {
		t.Fatal("unset must mean enabled")
	}
	on := true
	s := DefaultSettings()
	s.SpoofEnabled = &on
	if !spoofEnabled(s) {
		t.Fatal("explicit true must enable")
	}
	if spoofEnabled(spoofOffSettings()) {
		t.Fatal("explicit false must disable")
	}
}

// TestSpoofDisabledDialsProfileAddress: with SNI spoofing off a CoreSpoof
// profile renders against its own endpoint (e.g. a local 127.0.0.1
// tunnel) on both transports, never through a published front port.
func TestSpoofDisabledDialsProfileAddress(t *testing.T) {
	t.Cleanup(clearSpoofFrontPort)
	setSpoofFrontPort(45678)
	p := Profile{
		Name: "sp", Protocol: "trojan", Address: "example.com", Port: 443,
		IDOrPassword: "pw", TLS: true, Network: "ws", Core: CoreSpoof,
	}

	// Enabled (default): rewritten to the engine's front listener.
	ob := firstOutbound(t, mustBuildSingBox(t, &p, DefaultSettings()))
	if ob["server"] != "127.0.0.1" || portOf(ob["server_port"]) != 45678 {
		t.Fatalf("enabled outbound = %v, want the spoof front", ob)
	}
	// Disabled: the profile's own endpoint.
	ob = firstOutbound(t, mustBuildSingBox(t, &p, spoofOffSettings()))
	if ob["server"] != "example.com" || portOf(ob["server_port"]) != 443 {
		t.Fatalf("disabled outbound = %v, want the profile address", ob)
	}

	// The Xray transport follows the same switch.
	xob := xrayProxyOutbound(t, &p, DefaultSettings())
	if xob["address"] != "127.0.0.1" || portOf(xob["port"]) != 45678 {
		t.Fatalf("xray enabled outbound = %v, want the spoof front", xob)
	}
	xob = xrayProxyOutbound(t, &p, spoofOffSettings())
	if xob["address"] != "example.com" || portOf(xob["port"]) != 443 {
		t.Fatalf("xray disabled outbound = %v, want the profile address", xob)
	}
}

func mustBuildSingBox(t *testing.T, p *Profile, s Settings) string {
	t.Helper()
	raw, err := BuildSingBoxConfig(p, s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func xrayProxyOutbound(t *testing.T, p *Profile, s Settings) map[string]any {
	t.Helper()
	raw, err := BuildXrayConfig(p, s, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	obs, _ := cfg["outbounds"].([]any)
	for _, o := range obs {
		ob, _ := o.(map[string]any)
		if ob["tag"] != "proxy" {
			continue
		}
		settings, _ := ob["settings"].(map[string]any)
		if servers, _ := settings["servers"].([]any); len(servers) > 0 {
			first, _ := servers[0].(map[string]any)
			return first
		}
		if vnext, _ := settings["vnext"].([]any); len(vnext) > 0 {
			first, _ := vnext[0].(map[string]any)
			return first
		}
	}
	t.Fatal("xray proxy outbound not found")
	return nil
}

// TestSpoofDisabledDisarmsChainAndXray: no engine means no Psiphon chain
// and no spoof-on-Xray transport choice.
func TestSpoofDisabledDisarmsChainAndXray(t *testing.T) {
	p := Profile{Protocol: "trojan", Address: "a.example", Port: 443, TLS: true, Network: "ws", Core: CoreSpoof}
	off := spoofOffSettings()
	off.SpoofPsiphon = true
	off.SpoofXray = true
	if spoofPsiphonChain(&p, off) {
		t.Fatal("chain armed while SNI spoofing is off")
	}
	if spoofTransportXray(&p, off) {
		t.Fatal("xray transport chosen while SNI spoofing is off")
	}
	on := DefaultSettings()
	on.SpoofXray = true
	if !spoofTransportXray(&p, on) {
		t.Fatal("default behaviour must still pick xray when asked")
	}
}

// TestSpoofDisabledStopsRescues: with the engine off, a loopback address
// is the user's own tunnel and a bare domain is not converted behind
// their back.
func TestSpoofDisabledStopsRescues(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	dead := Profile{
		Protocol: "trojan", Address: "127.0.0.1", Port: port, IDOrPassword: "pw",
		TLS: true, Network: "ws", ServerName: "www.example.com",
	}
	if shouldRescueLoopbackProfile(&dead, spoofOffSettings()) {
		t.Fatal("loopback rescue ran while SNI spoofing is off")
	}
	if !shouldRescueLoopbackProfile(&dead, DefaultSettings()) {
		t.Fatal("loopback rescue must stay on by default")
	}
	dom := Profile{Protocol: "trojan", Address: "www.example.com", Port: 443, TLS: true, Network: "ws", ServerName: "www.example.com"}
	if shouldRescueDirectSpoofProfile(&dom, spoofOffSettings()) {
		t.Fatal("direct rescue ran while SNI spoofing is off")
	}
	if !shouldRescueDirectSpoofProfile(&dom, DefaultSettings()) {
		t.Fatal("direct rescue must stay on by default")
	}
}
