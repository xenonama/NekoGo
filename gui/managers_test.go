package main

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/thrift/lib/go/thrift"

	gen "nekogo/thriftgen"
)

func testProfile() *Profile {
	return &Profile{
		ID: "p1", Name: "Test", Core: CoreXray, Protocol: "vless",
		Address: "example.com", Port: 443, IDOrPassword: "11111111-2222-4333-8444-555555555555",
		Network: "tcp", TLS: true, ServerName: "example.com", Fingerprint: "chrome",
		RealityEnabled: true, RealityPubKey: "pubkey", RealityShortID: "shortid",
		Flow: "xtls-rprx-vision",
	}
}

func TestBuildXrayConfig(t *testing.T) {
	cfg, err := BuildXrayConfig(testProfile(), DefaultSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"log", "stats", "api", "inbounds", "outbounds", "routing"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("missing top-level key %q", k)
		}
	}
	outs := v["outbounds"].([]any)
	first := outs[0].(map[string]any)
	if first["protocol"] != "vless" {
		t.Fatalf("want vless outbound, got %v", first["protocol"])
	}
	ss := first["streamSettings"].(map[string]any)
	if ss["security"] != "reality" {
		t.Fatalf("want reality security, got %v", ss["security"])
	}
}

func TestBuildXrayConfigShadowsocks(t *testing.T) {
	p := &Profile{ID: "s", Name: "ss", Core: CoreXray, Protocol: "shadowsocks",
		Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw", Method: "aes-256-gcm"}
	cfg, err := BuildXrayConfig(p, DefaultSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatal(err)
	}
	if v["outbounds"].([]any)[0].(map[string]any)["protocol"] != "shadowsocks" {
		t.Fatal("want shadowsocks outbound")
	}
}

func inboundByTag(t *testing.T, inbounds []any) map[string]map[string]any {
	t.Helper()
	byTag := map[string]map[string]any{}
	for _, in := range inbounds {
		m, ok := in.(map[string]any)
		if !ok {
			t.Fatal("inbound is not an object")
		}
		tag, _ := m["tag"].(string)
		if tag == "" {
			t.Fatal("inbound missing tag")
		}
		byTag[tag] = m
	}
	return byTag
}

func TestBuildXrayConfigInbounds(t *testing.T) {
	cfg, err := BuildXrayConfig(testProfile(), DefaultSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatal(err)
	}
	byTag := inboundByTag(t, v["inbounds"].([]any))
	// HTTP proxy on MixedPort (default 2080).
	httpIn, ok := byTag["http-in"]
	if !ok {
		t.Fatal("missing http-in inbound")
	}
	if httpIn["protocol"] != "http" {
		t.Fatalf("http-in protocol = %v, want http", httpIn["protocol"])
	}
	if int(httpIn["port"].(float64)) != 2080 {
		t.Fatalf("http-in port = %v, want 2080", httpIn["port"])
	}
	// SOCKS proxy on MixedPort+1 with UDP enabled and no auth.
	socksIn, ok := byTag["socks-in"]
	if !ok {
		t.Fatal("missing socks-in inbound")
	}
	if socksIn["protocol"] != "socks" {
		t.Fatalf("socks-in protocol = %v, want socks", socksIn["protocol"])
	}
	if int(socksIn["port"].(float64)) != 2081 {
		t.Fatalf("socks-in port = %v, want 2081", socksIn["port"])
	}
	socksSettings := socksIn["settings"].(map[string]any)
	if socksSettings["auth"] != "noauth" || socksSettings["udp"] != true {
		t.Fatalf("socks-in settings = %v, want auth=noauth udp=true", socksSettings)
	}
	// The legacy dokodemo-door mixed inbound must be gone.
	if _, ok := byTag["mixed-in"]; ok {
		t.Fatal("legacy mixed-in inbound must not be present")
	}
	// StatsService listener must stay a dokodemo-door on the API port.
	apiIn, ok := byTag["api-in"]
	if !ok {
		t.Fatal("missing api-in inbound")
	}
	if apiIn["protocol"] != "dokodemo-door" {
		t.Fatalf("api-in protocol = %v, want dokodemo-door", apiIn["protocol"])
	}
	if int(apiIn["port"].(float64)) != 10085 {
		t.Fatalf("api-in port = %v, want 10085", apiIn["port"])
	}
	// System stats policy must enable all four traffic counters,
	// otherwise the API serves but registers nothing.
	pol, ok := v["policy"].(map[string]any)
	if !ok {
		t.Fatal("missing policy block")
	}
	sys, ok := pol["system"].(map[string]any)
	if !ok {
		t.Fatal("missing policy.system block")
	}
	for _, k := range []string{"statsInboundUplink", "statsInboundDownlink", "statsOutboundUplink", "statsOutboundDownlink"} {
		if sys[k] != true {
			t.Fatalf("policy.system.%s = %v, want true", k, sys[k])
		}
	}
}

func TestBuildXrayConfigCustomMixedPort(t *testing.T) {
	s := DefaultSettings()
	s.MixedPort = 18080
	cfg, err := BuildXrayConfig(testProfile(), s, "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatal(err)
	}
	byTag := inboundByTag(t, v["inbounds"].([]any))
	if int(byTag["http-in"]["port"].(float64)) != 18080 {
		t.Fatalf("http-in port = %v, want 18080", byTag["http-in"]["port"])
	}
	if int(byTag["socks-in"]["port"].(float64)) != 18081 {
		t.Fatalf("socks-in port = %v, want 18081", byTag["socks-in"]["port"])
	}
}

func TestBuildXrayConfigRejectsBadPorts(t *testing.T) {
	// 65535 leaves no room for the SOCKS listener on mixedPort+1.
	s := DefaultSettings()
	s.MixedPort = 65535
	if _, err := BuildXrayConfig(testProfile(), s, "127.0.0.1:10085"); err == nil {
		t.Fatal("expected error for mixedPort 65535")
	}
	// apiAddr colliding with the HTTP inbound.
	s = DefaultSettings()
	if _, err := BuildXrayConfig(testProfile(), s, "127.0.0.1:2080"); err == nil {
		t.Fatal("expected error for apiAddr colliding with mixedPort")
	}
	// apiAddr colliding with the SOCKS inbound (mixedPort+1).
	if _, err := BuildXrayConfig(testProfile(), s, "127.0.0.1:2081"); err == nil {
		t.Fatal("expected error for apiAddr colliding with the SOCKS port")
	}
	// Out-of-range API port falls back to the default (no error).
	cfg, err := BuildXrayConfig(testProfile(), s, "127.0.0.1:99999")
	if err != nil {
		t.Fatalf("out-of-range api port should fall back, got: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatal(err)
	}
	if port := int(inboundByTag(t, v["inbounds"].([]any))["api-in"]["port"].(float64)); port != 10085 {
		t.Fatalf("api-in port = %d, want fallback 10085", port)
	}
	// A valid combo still builds.
	if _, err := BuildXrayConfig(testProfile(), s, "127.0.0.1:10085"); err != nil {
		t.Fatalf("valid combo rejected: %v", err)
	}
}

func TestBuildSingBoxConfig(t *testing.T) {
	p := &Profile{ID: "s", Name: "ss", Core: CoreSingBox, Protocol: "shadowsocks",
		Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
	cfg, err := BuildSingBoxConfig(p, DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatal(err)
	}
	outs := v["outbounds"].([]any)
	if outs[0].(map[string]any)["type"] != "shadowsocks" {
		t.Fatal("want shadowsocks outbound")
	}
	// CPU audit #2: the default log level is warn (no per-connection
	// info lines unless the user opts in).
	if lvl := v["log"].(map[string]any)["level"]; lvl != "warn" {
		t.Fatalf("default log level = %v, want warn", lvl)
	}
}

// TestBuildSingBoxTunStack: the TUN stack defaults to the one that does
// not need an inbound firewall allow for the core. "system" terminates
// TCP inside the OS: sing-tun rewrites the app's SYN to come from the
// tunnel's peer address to the engine's own listener, which Windows
// Firewall treats as a new inbound connection. sing-tun tries to add the
// allow rule and discards the error, and the live probe saw no rule and
// a 15s TCP timeout through a tunnel whose DNS leg answered in 200ms.
// gVisor keeps TCP in user space, so it carries traffic with no firewall
// change at all - which is also PattN's default (Global.TunStacks.First).
func TestBuildSingBoxTunStack(t *testing.T) {
	stackFor := func(s Settings) string {
		t.Helper()
		s.TunEnabled = true
		p := &Profile{ID: "s", Name: "ss", Core: CoreSingBox, Protocol: "shadowsocks",
			Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
		raw, err := BuildSingBoxConfig(p, s)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Inbounds []struct {
				Type  string `json:"type"`
				Stack string `json:"stack"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		for _, in := range cfg.Inbounds {
			if in.Type == "tun" {
				return in.Stack
			}
		}
		t.Fatal("no tun inbound rendered")
		return ""
	}
	if got := stackFor(DefaultSettings()); got != tunStackDefault {
		t.Fatalf("default TUN stack = %q, want %q", got, tunStackDefault)
	}
	if tunStackDefault != "gvisor" {
		t.Fatalf("tunStackDefault = %q: the OS stack needs a Windows Firewall inbound allow for the core, so it must not be the default", tunStackDefault)
	}
	for _, tc := range []struct{ in, want string }{
		{"", "gvisor"},
		{"system", "system"},
		{"gvisor", "gvisor"},
		{" GVISOR ", "gvisor"},
		{"mixed", "mixed"},
		{"bogus", "gvisor"},
	} {
		s := DefaultSettings()
		s.TunStack = tc.in
		if got := stackFor(s); got != tc.want {
			t.Fatalf("TunStack %q rendered %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConfigStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	_ = filepath.Join(dir) // keep import if unused in future edits
	c, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := c.SaveProfile(*testProfile())
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		saved.ID = "p1" // SaveProfile keeps provided ID
	}
	got, ok := c.GetProfile("p1")
	if !ok {
		t.Fatal("profile not found after save")
	}
	if got.Address != "example.com" {
		t.Fatalf("wrong address %q", got.Address)
	}
	c2, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c2.ListProfiles()) != 1 {
		t.Fatal("profile not persisted")
	}
	// Latency fields must round-trip through config.json.
	persisted, _ := c2.GetProfile("p1")
	persisted.LastLatency = 87
	stamp := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	persisted.LastTestedAt = &stamp
	if _, err := c2.SaveProfile(persisted); err != nil {
		t.Fatal(err)
	}
	c3, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got3, ok := c3.GetProfile("p1")
	if !ok || got3.LastLatency != 87 {
		t.Fatalf("lastLatency not persisted: %+v", got3)
	}
	if got3.LastTestedAt == nil || !got3.LastTestedAt.Equal(*persisted.LastTestedAt) {
		t.Fatalf("lastTestedAt not persisted: %+v", got3)
	}
	if err := c2.DeleteProfile("p1"); err != nil {
		t.Fatal(err)
	}
	if len(c2.ListProfiles()) != 0 {
		t.Fatal("profile not deleted")
	}
}

func TestCoreManagerRoutes(t *testing.T) {
	logCh := make(chan LogEntry, 8)
	m := NewCoreManager(
		NewSingBoxManager("/nonexistent-core-xyz", logCh),
		NewXrayManager("/nonexistent-xray-xyz", t.TempDir(), logCh),
	)
	// Both backends must fail fast on missing binaries.
	if err := m.Start(testProfile(), DefaultSettings()); err == nil {
		t.Fatal("expected error for missing xray binary")
	}
	sb := &Profile{ID: "s", Name: "ss", Core: CoreSingBox, Protocol: "shadowsocks",
		Address: "1.2.3.4", Port: 1}
	if err := m.Start(sb, DefaultSettings()); err == nil {
		t.Fatal("expected error for missing core binary")
	}
	st, err := m.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatal("should not be running")
	}
}

// TestWithTransportReconnects — a transport-level failure triggers exactly
// one redial: the second invoke succeeds and the manager's transport is
// replaced. Uses a bare TCP listener (TSocket.Open only connects) plus a
// closure-controlled fake call, so no real core is needed.
func TestWithTransportReconnects(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	addr := ln.Addr().String()

	oldTrans, _, err := dialRPCAddr(addr)
	if err != nil {
		t.Fatalf("seed dial: %v", err)
	}
	m := &SingBoxManager{addr: addr, trans: oldTrans, client: &gen.LibcoreServiceClient{}}

	calls := 0
	err = m.withClient(func(*gen.LibcoreServiceClient) error {
		calls++
		if calls == 1 {
			return thrift.NewTTransportException(thrift.UNKNOWN_TRANSPORT_EXCEPTION, "boom")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withClient: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want exactly 2 (one retry)", calls)
	}
	if m.trans == oldTrans {
		t.Fatal("manager transport was not replaced after reconnect")
	}
	m.trans.Close()
}

// TestWithTransportNoReconnectOnAppError — application-level errors pass
// through untouched: single invoke, transport kept.
func TestWithTransportNoReconnectOnAppError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	oldTrans, _, err := dialRPCAddr(ln.Addr().String())
	if err != nil {
		t.Fatalf("seed dial: %v", err)
	}
	defer oldTrans.Close()
	m := &SingBoxManager{addr: ln.Addr().String(), trans: oldTrans, client: &gen.LibcoreServiceClient{}}

	calls := 0
	appErr := errors.New("invalid config: bad outbound")
	if err := m.withClient(func(*gen.LibcoreServiceClient) error {
		calls++
		return appErr
	}); err != appErr {
		t.Fatalf("err=%v, want passthrough", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want exactly 1 (no retry)", calls)
	}
	if m.trans != oldTrans {
		t.Fatal("transport replaced on an application error")
	}
}
