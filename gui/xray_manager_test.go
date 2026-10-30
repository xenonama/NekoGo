package main

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestXrayGetStatsNotRunning — idle manager reports not-running without
// ApiDown and without error.
func TestXrayGetStatsNotRunning(t *testing.T) {
	m := NewXrayManager("/nonexistent-xray-xyz", t.TempDir(), make(chan LogEntry, 8))
	st, err := m.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.ApiDown {
		t.Fatalf("got %+v, want idle snapshot", st)
	}
}

// TestParseXrayStatsPerTag — 3 inbound + 2 outbound counters fold into
// totals and per-tag maps; the internal api-in listener is skipped and
// malformed names are ignored.
func TestParseXrayStatsPerTag(t *testing.T) {
	resp := &QueryStatsResponse{Stat: []*Stat{
		{Name: "inbound>>>http-in>>>traffic>>>uplink", Value: 100},
		{Name: "inbound>>>http-in>>>traffic>>>downlink", Value: 1000},
		{Name: "inbound>>>socks-in>>>traffic>>>uplink", Value: 50},
		{Name: "outbound>>>proxy>>>traffic>>>uplink", Value: 150},
		{Name: "outbound>>>proxy>>>traffic>>>downlink", Value: 2000},
		{Name: "inbound>>>api-in>>>traffic>>>downlink", Value: 99999},
		{Name: "outbound>>>api>>>traffic>>>uplink", Value: 7},
		{Name: "garbage", Value: 1},
		{Name: "inbound>>>>>>uplink", Value: 2},
	}}
	up, down, upByTag, downByTag := parseXrayStats(resp)
	if up != 100+50+150+7 {
		t.Fatalf("uplink = %d, want %d", up, 100+50+150+7)
	}
	if down != 1000+2000 {
		t.Fatalf("downlink = %d, want %d", down, 1000+2000)
	}
	if upByTag["http-in"] != 100 || upByTag["socks-in"] != 50 || upByTag["proxy"] != 150 {
		t.Fatalf("uplinkByTag = %v", upByTag)
	}
	if downByTag["http-in"] != 1000 || downByTag["proxy"] != 2000 {
		t.Fatalf("downlinkByTag = %v", downByTag)
	}
	if _, ok := upByTag["api-in"]; ok {
		t.Fatalf("api-in must be skipped, got %v", upByTag)
	}
	if _, ok := downByTag["api-in"]; ok {
		t.Fatalf("api-in must be skipped, got %v", downByTag)
	}
}

// TestSplitAPIAddrRejectsBadPorts — illegal ports fall back to the
// default, empty input gets the full default, valid hosts pass through.
func TestSplitAPIAddrRejectsBadPorts(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"127.0.0.1:0", "127.0.0.1", xrayAPIDefaultPort},
		{"127.0.0.1:99999", "127.0.0.1", xrayAPIDefaultPort},
		{"127.0.0.1:10085", "127.0.0.1", 10085},
		{"", "127.0.0.1", xrayAPIDefaultPort},
		{"localhost:10085", "localhost", 10085},
		{"127.0.0.1:abc", "127.0.0.1", xrayAPIDefaultPort},
		{"[::1]:10085", "::1", 10085},
	}
	for _, c := range cases {
		h, p := splitAPIAddr(c.in)
		if h != c.host || p != c.port {
			t.Errorf("splitAPIAddr(%q) = (%q,%d), want (%q,%d)",
				c.in, h, p, c.host, c.port)
		}
	}
}

// TestStatsApiDownJSONShape — the apiDown flag must serialize under the
// exact key the frontend reads (api.ts / TrafficStats / ConnectionCard),
// and must be omitted when false so the Stats shape is unchanged.
func TestStatsApiDownJSONShape(t *testing.T) {
	down, err := json.Marshal(&Stats{Running: true, Core: "xray", ApiDown: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(down), `"apiDown":true`) {
		t.Fatalf("ApiDown snapshot = %s, want apiDown:true", down)
	}
	idle, err := json.Marshal(&Stats{Running: false, Core: "xray"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(idle), "apiDown") {
		t.Fatalf("idle snapshot = %s, want no apiDown key", idle)
	}
}

// TestXrayPortInUse — HOTFIX Bug 2: when the mixed port is already held
// (zombie xray from a crashed session), Start must fail fast with the
// clear "in use" message — naming the port — instead of a bare bind error
// 5s later. The bogus binary path proves the check runs before resolve.
func TestXrayPortInUse(t *testing.T) {
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer hold.Close()
	mixed := hold.Addr().(*net.TCPAddr).Port
	if mixed <= 0 || mixed > 65534 {
		t.Skipf("ephemeral mixed port %d out of testable range", mixed)
	}
	apiHold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no second loopback listener: %v", err)
	}
	defer apiHold.Close()
	apiPort := apiHold.Addr().(*net.TCPAddr).Port

	m := NewXrayManager("/nonexistent-xray-xyz", "", make(chan LogEntry, 8))
	p := testProfile()
	err = m.Start(p, Settings{
		MixedPort:      mixed,
		XrayAPIAddress: "127.0.0.1:" + strconv.Itoa(apiPort),
		LogLevel:       "warning",
	})
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start() = nil with mixed port held, want port-in-use error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "in use") || !strings.Contains(msg, strconv.Itoa(mixed)) {
		t.Fatalf("Start() err = %q, want port %d in-use hint", msg, mixed)
	}
	if !strings.Contains(msg, "still running") {
		t.Fatalf("Start() err = %q, want zombie hint", msg)
	}
}
