package main

// Port pre-flight for the two sing-box paths (custom nekobox_core and
// the upstream CLI).
//
// A user reported that Connect failed, that they clicked it many times,
// and that it finally connected. That is the shape of a held inbound
// port: NekoGo names one fixed port (Settings.MixedPort, default 2080)
// in the rendered config, so every click fails the same way until
// whatever held the port goes away — the retries are what waits, not
// what fixes.
//
// Xray and mihomo already refuse this up front (checkPortFree in
// xray_manager.go / mihomo_manager.go). The sing-box paths spawned the
// core, paid the RPC handshake and only then took the engine's own
// bind error seconds later. These tests pin the parity, and pin the two
// ways parity alone would be wrong: a port released a moment later must
// still connect (the user must not have to click again), and a config
// that does not bind the configured port must not be refused for it.

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// holdFreePort binds a kernel-assigned loopback port and keeps it, so a
// connect aimed at that number starts into a collision.
func holdFreePort(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener available: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// portHeldSettings points the mixed inbound at a listener the test owns.
func portHeldSettings(t *testing.T, port int) Settings {
	t.Helper()
	s := DefaultSettings()
	s.MixedPort = port
	return s
}

func portTestProfile() *Profile {
	return &Profile{ID: "x", Name: "held-port", Core: CoreSingBox,
		Protocol: "shadowsocks", Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
}

// rawOverride builds a full raw core override binding one loopback port.
func rawOverride(t *testing.T, port int) map[string]json.RawMessage {
	t.Helper()
	doc := map[string]any{
		"log": map[string]any{"level": "warning"},
		"inbounds": []any{map[string]any{
			"type": "mixed", "tag": "mixed-in",
			"listen": "127.0.0.1", "listen_port": port,
		}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]json.RawMessage{rawOverrideKey: raw}
}

// TestSingBoxStartRefusesHeldInboundPortBeforeSpawning: the core path
// used to spawn and only report the engine's bind error after the RPC
// handshake. The bogus core path proves the refusal comes first, and the
// message must name the port the user has to change.
func TestSingBoxStartRefusesHeldInboundPortBeforeSpawning(t *testing.T) {
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port
	m := NewSingBoxManager("/nonexistent-nekobox-core-xyz", make(chan LogEntry, 8))
	err := m.Start(portTestProfile(), portHeldSettings(t, port))
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start accepted a config whose inbound port is held")
	}
	msg := err.Error()
	if !strings.Contains(msg, "in use") || !strings.Contains(msg, strconv.Itoa(port)) {
		t.Errorf("Start error = %q, want it to name port %d as in use", msg, port)
	}
	if strings.Contains(msg, "nonexistent-nekobox-core-xyz") {
		t.Errorf("Start spawned the core before checking the port: %q", msg)
	}
	if m.IsRunning() {
		t.Error("manager reports running after a refused start")
	}
}

// TestUpstreamSingBoxStartRefusesHeldInboundPortBeforeSpawning: the
// official CLI runs the same rendered config, so it collides the same
// way and must refuse the same way.
func TestUpstreamSingBoxStartRefusesHeldInboundPortBeforeSpawning(t *testing.T) {
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port
	s := portHeldSettings(t, port)
	s.UpstreamCorePath = "/nonexistent-sing-box-xyz"
	m := NewUpstreamSingBoxManager(make(chan LogEntry, 8))
	err := m.Start(portTestProfile(), s)
	if err == nil {
		_ = m.Stop()
		t.Fatal("upstream Start accepted a config whose inbound port is held")
	}
	msg := err.Error()
	if !strings.Contains(msg, "in use") || !strings.Contains(msg, strconv.Itoa(port)) {
		t.Errorf("upstream Start error = %q, want it to name port %d as in use", msg, port)
	}
	if strings.Contains(msg, "nonexistent-sing-box-xyz") {
		t.Errorf("upstream Start resolved a core before checking the port: %q", msg)
	}
}

// TestSingBoxStartWaitsForAPortFreedAtOnce: a port released by the
// previous attempt's core (the reason the next click worked) must be
// waited out inside one connect, not handed back to the user as a click
// to repeat. The failure then falls through to the deliberately missing
// core, proving the pre-flight let the connect continue.
func TestSingBoxStartWaitsForAPortFreedAtOnce(t *testing.T) {
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port
	grace := inboundPortGrace
	if grace <= 0 {
		t.Fatalf("inboundPortGrace = %v, want a window to wait the release out", grace)
	}
	go func() {
		time.Sleep(min(200*time.Millisecond, grace/2))
		_ = held.Close()
	}()
	m := NewSingBoxManager("/nonexistent-nekobox-core-xyz", make(chan LogEntry, 8))
	err := m.Start(portTestProfile(), portHeldSettings(t, port))
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded with no core binary")
	}
	msg := err.Error()
	if strings.Contains(msg, "in use") {
		t.Errorf("Start refused a port freed during the window: %q", msg)
	}
	if !strings.Contains(msg, "nonexistent-nekobox-core-xyz") {
		t.Errorf("Start error = %q, want the connect to reach the core lookup", msg)
	}
}

// TestSingBoxStartDoesNotWaitOnAFreePort: the grace window may only cost
// time when something actually holds the port. Every connect that works
// today must keep working at the same speed.
func TestSingBoxStartDoesNotWaitOnAFreePort(t *testing.T) {
	free, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	m := NewSingBoxManager("/nonexistent-nekobox-core-xyz", make(chan LogEntry, 8))
	start := time.Now()
	err = m.Start(portTestProfile(), portHeldSettings(t, free))
	elapsed := time.Since(start)
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded with no core binary")
	}
	if strings.Contains(err.Error(), "in use") {
		t.Errorf("Start refused a free port %d: %v", free, err)
	}
	if elapsed >= inboundPortGrace {
		t.Errorf("Start took %v on a free port, want no grace wait (window %v)", elapsed, inboundPortGrace)
	}
}

// TestSingBoxStartChecksThePortsTheConfigBinds: a full raw core override
// replaces the rendered config, so its own inbound ports are the ones
// that collide — and a held Settings.MixedPort the override ignores must
// not block it.
func TestSingBoxStartChecksThePortsTheConfigBinds(t *testing.T) {
	held := holdFreePort(t)
	heldPort := held.Addr().(*net.TCPAddr).Port
	free, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}

	t.Run("raw override binding a held port is refused", func(t *testing.T) {
		s := portHeldSettings(t, free)
		s.CoreOverrides = rawOverride(t, heldPort)
		m := NewSingBoxManager("/nonexistent-nekobox-core-xyz", make(chan LogEntry, 8))
		err := m.Start(portTestProfile(), s)
		if err == nil {
			_ = m.Stop()
			t.Fatal("Start accepted a raw config whose inbound port is held")
		}
		if !strings.Contains(err.Error(), "in use") {
			t.Errorf("Start error = %q, want the port-in-use refusal", err)
		}
	})

	t.Run("raw override ignoring the setting connects anyway", func(t *testing.T) {
		s := portHeldSettings(t, heldPort)
		s.CoreOverrides = rawOverride(t, free)
		m := NewSingBoxManager("/nonexistent-nekobox-core-xyz", make(chan LogEntry, 8))
		err := m.Start(portTestProfile(), s)
		if err == nil {
			_ = m.Stop()
			t.Fatal("Start succeeded with no core binary")
		}
		if strings.Contains(err.Error(), "in use") {
			t.Errorf("Start refused a port the raw config never binds: %q", err)
		}
	})
}

// TestEphemeralProbeStillPicksItsOwnPort: latency probes bind throwaway
// ports, so they must not grow the fixed-port pre-flight — a probe that
// refuses on the user's mixed port would report every node as failed.
func TestEphemeralProbeStillPicksItsOwnPort(t *testing.T) {
	held := holdFreePort(t)
	s := portHeldSettings(t, held.Addr().(*net.TCPAddr).Port)
	ports, err := singBoxBindPorts(portTestProfile(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) == 0 {
		t.Fatal("no bind ports derived from a rendered config")
	}
	found := false
	want := "127.0.0.1:" + strconv.Itoa(held.Addr().(*net.TCPAddr).Port)
	for _, p := range ports {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("singBoxBindPorts = %v, want the mixed inbound %q", ports, want)
	}
}
