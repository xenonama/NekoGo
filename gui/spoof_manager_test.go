package main

// Tests for the sni-spoof out-of-process manager (spoof_manager.go)
// and the loopback-only front layer (sni_front.go).
//
// The Start/Stop tests build a mock exe from testdata/spoofmock
// (a trivial [INFO]/[STATS] printer), so no WinDivert driver or
// admin rights are needed. All profiles use loopback addresses so
// no DNS resolution happens.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildSpoofMock compiles testdata/spoofmock once per test and
// points the manager overrides at it. Restores the overrides on
// cleanup.
func buildSpoofMock(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	name := "spoofmock"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	mock := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", mock, "./testdata/spoofmock")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build spoofmock: %v\n%s", err, out)
	}
	oldExe, oldDir := spoofExeOverride, spoofExeDirOverride
	spoofExeOverride = mock
	spoofExeDirOverride = dir
	t.Cleanup(func() {
		spoofExeOverride, spoofExeDirOverride = oldExe, oldDir
		clearSpoofFrontPort()
	})
}

func spoofTestSettings() Settings {
	s := DefaultSettings()
	s.SNISelectedIP = "104.21.0.1"
	s.SNISelectedSNI = "www.example.com"
	return s
}

func spoofTestProfile() *Profile {
	return &Profile{
		ID: "spoof-1", Name: "spoof", Core: CoreSpoof,
		Protocol: "trojan", Address: "127.0.0.1", Port: 443,
		IDOrPassword: "pw",
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSpoofManagerStartWithMockExe(t *testing.T) {
	buildSpoofMock(t)
	m := NewSpoofManager(make(chan LogEntry, 1024))
	if err := m.Start(spoofTestProfile(), spoofTestSettings()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()
	if !m.IsRunning() {
		t.Fatal("IsRunning = false after Start")
	}
	port := m.GetListenPort()
	if port < 1 || port > 65535 {
		t.Fatalf("GetListenPort = %d, want 1..65535", port)
	}
	cfgPath := m.GetConfigPath()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read temp config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("temp config is not JSON: %v", err)
	}
	if cfg["MODE"] != "SNI Only" {
		t.Fatalf("MODE = %v, want SNI Only", cfg["MODE"])
	}
	if cfg["LISTEN_HOST"] != "127.0.0.1" {
		t.Fatalf("LISTEN_HOST = %v", cfg["LISTEN_HOST"])
	}
	if int(cfg["LISTEN_PORT"].(float64)) != port {
		t.Fatalf("LISTEN_PORT = %v, want %d", cfg["LISTEN_PORT"], port)
	}
	eps, _ := cfg["ENDPOINTS"].([]any)
	// Primary endpoint first, curated defaults as failover pool.
	if len(eps) != 1+len(defaultCloudflareIPs) {
		t.Fatalf("ENDPOINTS has %d entries, want 1 primary + %d curated", len(eps), len(defaultCloudflareIPs))
	}
	ep := eps[0].(map[string]any)
	if ep["ip"] != "104.21.0.1" || int(ep["port"].(float64)) != 443 {
		t.Fatalf("ENDPOINTS[0] = %v, want 104.21.0.1:443", ep)
	}
	snis, _ := cfg["FAKE_SNIS"].([]any)
	if len(snis) != 1+len(defaultDecoySNIs) || snis[0] != "www.example.com" {
		t.Fatalf("FAKE_SNIS = %v, want primary www.example.com + %d curated", snis, len(defaultDecoySNIs))
	}
	// The mock emits [STATS] every 100ms; the scraper must pick them up.
	waitFor(t, "stats snapshot", func() bool {
		st, _ := m.GetStats()
		return st != nil && st.Total > 0
	})
	// Every Ranked field has to survive the stdout scrape. The engine sorts
	// these rows on `score` and times them into `latency_ms`; dropping either
	// leaves the tab showing a 100% row that `auto` demoted for no visible
	// reason, which is the whole point of the two fields.
	st, _ := m.GetStats()
	if st == nil {
		t.Fatal("GetStats = nil after the scraper reported a snapshot")
	}
	if len(st.Methods) != 2 {
		t.Fatalf("methods rows = %d, want 2", len(st.Methods))
	}
	if first := st.Methods[0]; first.Key != "split_seq" || first.LatencyMs == nil ||
		*first.LatencyMs != 42.5 || first.Score != 0.912 {
		t.Fatalf("ranked row lost its latency/score in the scrape: %+v", first)
	}
	if second := st.Methods[1]; second.LatencyMs != nil || second.Score != 0.8 {
		t.Fatalf("unmeasured row must scrape as nil latency: %+v", second)
	}
	if got := m.GetLastError(); got != "" {
		t.Fatalf("GetLastError = %q, want empty", got)
	}
}

func TestSpoofManagerStopCleansConfig(t *testing.T) {
	buildSpoofMock(t)
	m := NewSpoofManager(make(chan LogEntry, 1024))
	if err := m.Start(spoofTestProfile(), spoofTestSettings()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cfgPath := m.GetConfigPath()
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.IsRunning() {
		t.Fatal("IsRunning = true after Stop")
	}
	if m.GetListenPort() != 0 {
		t.Fatalf("GetListenPort = %d after Stop, want 0", m.GetListenPort())
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("temp config still exists after Stop: %s", cfgPath)
	}
	// Second Stop is a nil op.
	if err := m.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// Stop must ask the engine to leave rather than knock it over, because only
// an answered request runs EngineHandle::shutdown (WinDivert handles,
// in-flight relays, the "[WARN] engine stopped" line). The proc.Signal
// (os.Interrupt) call that sat here could never have worked: Go's Windows
// os.Process.signal returns EWINDOWS for every signal except Kill — read
// from src/os/exec_windows.go in this toolchain, and confirmed by the fact
// that the stopped-engine log line never appears in a real run.
func TestSpoofManagerStopIsGraceful(t *testing.T) {
	buildSpoofMock(t)
	logs := make(chan LogEntry, 1024)
	m := NewSpoofManager(logs)
	if err := m.Start(spoofTestProfile(), spoofTestSettings()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.IsRunning() {
		t.Fatal("IsRunning = true after a graceful Stop")
	}
	// The mock prints this line only after it reads "stop" on stdin, so
	// seeing it proves the request travelled the pipe and was acted on. The
	// scraper strips the "[INFO] " tag before it reaches the log bus.
	// Duplicated from testdata/spoofmock/main.go, which cannot be imported
	// (package main).
	const marker = "mock graceful stop"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case le := <-logs:
			if strings.Contains(le.Message, marker) {
				return
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("no graceful-stop line: Stop fell back to TerminateProcess without asking the engine")
}

func TestSpoofManagerStartRejectsMissingExe(t *testing.T) {
	oldExe, oldDir := spoofExeOverride, spoofExeDirOverride
	spoofExeOverride = filepath.Join(t.TempDir(), "no-such-spoof.exe")
	spoofExeDirOverride = ""
	t.Cleanup(func() { spoofExeOverride, spoofExeDirOverride = oldExe, oldDir })
	m := NewSpoofManager(make(chan LogEntry, 16))
	err := m.Start(spoofTestProfile(), spoofTestSettings())
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start with missing exe succeeded, want clear error")
	}
	if !strings.Contains(err.Error(), "sni-spoof") {
		t.Fatalf("error %q does not name sni-spoof", err)
	}
}

func TestSpoofManagerStartValidation(t *testing.T) {
	buildSpoofMock(t)
	m := NewSpoofManager(make(chan LogEntry, 16))
	base := spoofTestSettings()
	cases := []struct {
		name    string
		mutate  func(*Profile, *Settings)
		wantErr string
	}{
		{"wrong core", func(p *Profile, s *Settings) { p.Core = CoreSingBox }, "CoreSpoof"},
		{"bad protocol", func(p *Profile, s *Settings) { p.Protocol = "vmess" }, "trojan or vless"},
		{"empty address", func(p *Profile, s *Settings) { p.Address = "" }, "address"},
		{"loopback without selection", func(p *Profile, s *Settings) {
			s.SNISelectedIP = ""
			s.SNISelectedSNI = ""
			s.SNIIPs = nil
			s.SNISNIs = nil
		}, "has no SNI"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := spoofTestProfile()
			s := base
			tc.mutate(p, &s)
			if err := m.Start(p, s); err == nil {
				_ = m.Stop()
				t.Fatal("Start succeeded, want error")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
			if m.IsRunning() {
				_ = m.Stop()
				t.Fatal("manager running after failed Start")
			}
		})
	}
}

func TestBuildSpoofConfigJSON(t *testing.T) {
	p := spoofTestProfile()
	p.Address = "www.calmlunch.com"
	p.Port = 8443
	s := DefaultSettings()
	s.SNIConfig = SNIConfig{Method: "wrong_seq", Fingerprint: "chrome_120", QUICMode: "block", IPMode: "ipv4"}
	raw, err := buildSpoofConfigJSON(p, s, 40443, "172.67.1.2", 8443, normalizeFakeSNI("Example.COM."))
	if err != nil {
		t.Fatalf("buildSpoofConfigJSON: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if cfg["MODE"] != "SNI Only" {
		t.Fatalf("MODE = %v", cfg["MODE"])
	}
	if cfg["BYPASS_METHOD"] != "wrong_seq" || cfg["TLS_FINGERPRINT"] != "chrome_120" ||
		cfg["QUIC_MODE"] != "block" || cfg["IP_MODE"] != "ipv4" {
		t.Fatalf("engine options not rendered: %v", cfg)
	}
	eps := cfg["ENDPOINTS"].([]any)
	ep := eps[0].(map[string]any)
	if ep["ip"] != "172.67.1.2" || int(ep["port"].(float64)) != 8443 {
		t.Fatalf("ENDPOINTS[0] = %v, want 172.67.1.2:8443", ep)
	}
	snis := cfg["FAKE_SNIS"].([]any)
	// Primary SNI first (normalized), curated defaults as rotation.
	if len(snis) != 1+len(defaultDecoySNIs) || snis[0] != "example.com" {
		t.Fatalf("FAKE_SNIS = %v, want normalized primary + %d curated", snis, len(defaultDecoySNIs))
	}
	// Zero/empty options are omitted (engine default applies).
	s2 := DefaultSettings()
	s2.SNIConfig = SNIConfig{}
	raw2, err := buildSpoofConfigJSON(p, s2, 40443, "104.16.0.1", 443, "example.com")
	if err != nil {
		t.Fatalf("buildSpoofConfigJSON empty: %v", err)
	}
	var cfg2 map[string]any
	_ = json.Unmarshal(raw2, &cfg2)
	for _, k := range []string{"BYPASS_METHOD", "TLS_FINGERPRINT", "QUIC_MODE", "IP_MODE", "HANDSHAKE_TIMEOUT", "MAX_CONNECTIONS", "FAKE_DELAY", "SEQ_OVERLAP", "PADDING_SIZE"} {
		if _, ok := cfg2[k]; ok {
			t.Fatalf("empty option %s rendered, want omitted", k)
		}
	}
}

// Regression: loopback profiles must dial 443 upstream, never the
// stale LOCAL port from the link (e.g. 127.0.0.1:40443 -> CF:40443
// timed out every handshake).
func TestSpoofEndpointPortLoopback(t *testing.T) {
	loop := &Profile{Address: "127.0.0.1", Port: 40443}
	if got := spoofEndpointPort(loop); got != 443 {
		t.Fatalf("spoofEndpointPort(loopback:40443) = %d, want 443", got)
	}
	direct := &Profile{Address: "www.calmlunch.com", Port: 8443}
	if got := spoofEndpointPort(direct); got != 8443 {
		t.Fatalf("spoofEndpointPort(direct:8443) = %d, want 8443", got)
	}
}

// Regression: a chained (loopback) link already carries sni=/host=
// — Start must use it when neither the pin nor a checked entry
// provides a SNI, instead of failing with "needs an SNI endpoint".
func TestResolveSNITargetLoopbackFallsBackToProfile(t *testing.T) {
	s := DefaultSettings()
	// No pins and no checked entries left: the profile's own SNI must
	// win (defaults ship pinned + checked, so clear both to exercise
	// the fallback).
	s.SNISelectedIP = ""
	s.SNISelectedSNI = ""
	s.SNIIPs = nil
	s.SNISNIs = nil
	p := &Profile{
		ID: "appxa", Name: "APPXA", Core: CoreSpoof, Protocol: "trojan",
		Address: "127.0.0.1", Port: 40443, IDOrPassword: "humanity",
		ServerName: "www.calmlunch.com", Host: "www.calmlunch.com",
		Network: "ws", TLS: true,
	}
	// Stub DNS: resolveDomainToCFIP does real lookup; point the pin
	// instead and check SNI fallback only.
	s.SNISelectedIP = "104.21.0.1"
	ip, sni, port, err := resolveSNITarget(p, s)
	if err != nil {
		t.Fatalf("resolveSNITarget loopback: %v", err)
	}
	if ip != "104.21.0.1" || sni != "www.calmlunch.com" || port != 443 {
		t.Fatalf("resolveSNITarget = %s/%s/%d, want 104.21.0.1/www.calmlunch.com/443", ip, sni, port)
	}
	// Manual SNI pin wins over the profile's.
	s.SNISelectedSNI = "decoy.example.com"
	_, sni2, _, err := resolveSNITarget(p, s)
	if err != nil {
		t.Fatalf("resolveSNITarget pinned: %v", err)
	}
	if sni2 != "decoy.example.com" {
		t.Fatalf("pinned sni = %q, want decoy.example.com", sni2)
	}
	// No SNI pin but the curated pool checked: the first checked entry
	// wins over the profile's own SNI (the front follows the same
	// selection policy as the engine pools).
	pool := DefaultSettings()
	pool.SNISelectedIP = "104.21.0.1"
	pool.SNISelectedSNI = ""
	_, sni3, _, err := resolveSNITarget(p, pool)
	if err != nil {
		t.Fatalf("resolveSNITarget pooled: %v", err)
	}
	if sni3 != defaultDecoySNIs[0] {
		t.Fatalf("pooled sni = %q, want first checked %q", sni3, defaultDecoySNIs[0])
	}
}

// Regression (prompt.txt Test-CF): a direct-CF trojan+TLS+WS profile
// must honor the SNI tab's decoy pins exactly like loopback does.
// Previously the direct branch ignored the pins — FAKE_SNIS[0] was
// the profile's own SNI (www.calmlunch.com), so the engine faked the
// REAL identity at DPI and Test-CF failed while APPXA (loopback,
// pin-first) worked. Uses a CF IP literal as the profile address so
// no DNS lookup happens.
func TestResolveSNITargetDirectHonorsPins(t *testing.T) {
	p := &Profile{
		ID: "test-cf", Name: "Test-CF", Core: CoreSpoof, Protocol: "trojan",
		Address: "104.21.0.1", Port: 443, IDOrPassword: "humanity",
		ServerName: "www.calmlunch.com", Host: "www.calmlunch.com",
		Network: "ws", TLS: true,
	}
	// Pinned decoy pair wins over the profile's own SNI/endpoint.
	s := DefaultSettings()
	s.SNISelectedIP = "162.159.192.1"
	s.SNISelectedSNI = "developers.cloudflare.com"
	ip, sni, port, err := resolveSNITarget(p, s)
	if err != nil {
		t.Fatalf("resolveSNITarget direct pinned: %v", err)
	}
	if ip != "162.159.192.1" || sni != "developers.cloudflare.com" || port != 443 {
		t.Fatalf("resolveSNITarget = %s/%s/%d, want 162.159.192.1/developers.cloudflare.com/443", ip, sni, port)
	}
	// First checked list entry wins when no explicit pin is set.
	pool := DefaultSettings()
	pool.SNISelectedIP = ""
	pool.SNISelectedSNI = ""
	_, sni2, _, err := resolveSNITarget(p, pool)
	if err != nil {
		t.Fatalf("resolveSNITarget direct pooled: %v", err)
	}
	if sni2 != defaultDecoySNIs[0] {
		t.Fatalf("pooled sni = %q, want first checked %q", sni2, defaultDecoySNIs[0])
	}
	// No pins, no checked entries: the profile's own SNI + CF IP
	// literal carry through.
	bare := Settings{}
	ip3, sni3, port3, err := resolveSNITarget(p, bare)
	if err != nil {
		t.Fatalf("resolveSNITarget direct bare: %v", err)
	}
	if ip3 != "104.21.0.1" || sni3 != "www.calmlunch.com" || port3 != 443 {
		t.Fatalf("resolveSNITarget = %s/%s/%d, want 104.21.0.1/www.calmlunch.com/443", ip3, sni3, port3)
	}
}

// Regression: the old 4-prefix check rejected real Cloudflare IPs
// (172.64.x.x, 162.158.x.x, 188.114.x.x) that v2rayN accepts.
func TestIsCloudflareIPFullRanges(t *testing.T) {
	for _, ip := range []string{
		"104.21.0.1", "104.16.5.4", "172.67.10.20", "172.64.0.5",
		"172.68.1.1", "141.101.120.7", "108.162.192.5",
		"162.158.10.3", "188.114.96.7", "173.245.48.9",
		"190.93.240.11", "198.41.128.9", "103.21.244.9",
	} {
		if !isCloudflareIP(ip) {
			t.Errorf("isCloudflareIP(%q) = false, want true", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "127.0.0.1", "93.184.216.34", "not-an-ip"} {
		if isCloudflareIP(ip) {
			t.Errorf("isCloudflareIP(%q) = true, want false", ip)
		}
	}
}

// Regression: the resolver used to require a Cloudflare answer, so every
// non-CF site (youtube.com, instagram.com, google.com) failed inside Start
// before the engine was spawned. A Cloudflare edge is now a preference and
// the site's own public IPv4 is the fallback.
func TestPickSpoofEndpointIP(t *testing.T) {
	addrs := func(ips ...string) []net.IPAddr {
		out := make([]net.IPAddr, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out
	}

	got, err := pickSpoofEndpointIP("www.youtube.com", addrs("216.239.38.119"))
	if err != nil || got != "216.239.38.119" {
		t.Fatalf("non-CF site = %s/%v, want its own IP and no error", got, err)
	}

	got, err = pickSpoofEndpointIP("example.com", addrs("93.184.216.34", "104.21.0.1"))
	if err != nil || got != "104.21.0.1" {
		t.Fatalf("mixed set = %s/%v, want the Cloudflare edge", got, err)
	}

	got, err = pickSpoofEndpointIP("example.com", addrs("2001:4860:4802:32::78", "172.217.14.110"))
	if err != nil || got != "172.217.14.110" {
		t.Fatalf("v6+v4 set = %s/%v, want the IPv4", got, err)
	}

	if _, err := pickSpoofEndpointIP("example.com", addrs("10.10.34.35")); err == nil {
		t.Fatal("private-only answer set must fail, not dial the interceptor")
	} else if !strings.Contains(err.Error(), "only private answers") {
		t.Fatalf("private-only error = %v, want the interceptor hint", err)
	}

	if _, err := pickSpoofEndpointIP("example.com", nil); err == nil {
		t.Fatal("empty answer set must fail")
	}
}

// The engine config must carry the curated lists as failover pools,
// primary first.
func TestBuildSpoofConfigRendersPools(t *testing.T) {
	p := &Profile{Address: "www.calmlunch.com", Port: 443}
	s := DefaultSettings()
	s.SNIIPs = []SNIIP{{IP: "104.21.0.2"}, {IP: "104.21.0.2"}, {IP: "162.158.0.9"}}
	s.SNISNIs = []SNISNI{{Host: "b.example.com"}, {Host: "B.EXAMPLE.COM."}}
	raw, err := buildSpoofConfigJSON(p, s, 40443, "104.21.0.1", 443, "a.example.com")
	if err != nil {
		t.Fatalf("buildSpoofConfigJSON: %v", err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	eps := cfg["ENDPOINTS"].([]any)
	if len(eps) != 3 {
		t.Fatalf("ENDPOINTS has %d entries, want 3 (primary + 2 deduped failover)", len(eps))
	}
	if eps[0].(map[string]any)["ip"] != "104.21.0.1" {
		t.Fatalf("ENDPOINTS[0] = %v, want primary first", eps[0])
	}
	snis := cfg["FAKE_SNIS"].([]any)
	if len(snis) != 2 || snis[0] != "a.example.com" || snis[1] != "b.example.com" {
		t.Fatalf("FAKE_SNIS = %v, want [a.example.com b.example.com]", snis)
	}
}

func TestShouldApplySNIFront(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "127.0.0.5", "::1", "localhost", "LOCALHOST", " 127.0.0.1 "} {
		if !shouldApplySNIFront(&Profile{Address: addr}) {
			t.Errorf("shouldApplySNIFront(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"", "example.com", "1.1.1.1", "104.21.0.1", "::ffff:1.2.3.4"} {
		if shouldApplySNIFront(&Profile{Address: addr}) {
			t.Errorf("shouldApplySNIFront(%q) = true, want false", addr)
		}
	}
	if shouldApplySNIFront(nil) {
		t.Error("shouldApplySNIFront(nil) = true, want false")
	}
}

func TestApplySNIFrontSingBox(t *testing.T) {
	// CoreSpoof profile through the real builder with a published port.
	setSpoofFrontPort(40543)
	defer clearSpoofFrontPort()
	p := spoofTestProfile()
	p.ServerName = "decoy.example.com"
	s := DefaultSettings()
	raw, err := BuildSingBoxConfig(p, s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	var cfg map[string]any
	_ = json.Unmarshal([]byte(raw), &cfg)
	found := false
	for _, o := range cfg["outbounds"].([]any) {
		ob := o.(map[string]any)
		if ob["tag"] == "proxy" {
			found = true
			if ob["server"] != "127.0.0.1" || int(ob["server_port"].(float64)) != 40543 {
				t.Fatalf("proxy outbound = %v:%v, want 127.0.0.1:40543", ob["server"], ob["server_port"])
			}
			tls, _ := ob["tls"].(map[string]any)
			if tls == nil || tls["server_name"] != "decoy.example.com" {
				t.Fatalf("server_name touched by front: %v", tls)
			}
		}
	}
	if !found {
		t.Fatal("no proxy outbound in rendered config")
	}
	// Domain profile with a NON-spoof core is never rewritten (B2).
	clearSpoofFrontPort()
	setSpoofFrontPort(40543)
	defer clearSpoofFrontPort()
	direct := &Profile{ID: "x", Core: CoreSingBox, Protocol: "trojan", Address: "www.calmlunch.com", Port: 443, IDOrPassword: "pw"}
	raw2, err := BuildSingBoxConfig(direct, s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig direct: %v", err)
	}
	var cfg2 map[string]any
	_ = json.Unmarshal([]byte(raw2), &cfg2)
	for _, o := range cfg2["outbounds"].([]any) {
		if ob := o.(map[string]any); ob["tag"] == "proxy" && ob["server"] == "127.0.0.1" {
			t.Fatalf("direct CF profile was rewritten: %v", ob)
		}
	}
}

func TestApplySNIFrontXray(t *testing.T) {
	cfg := map[string]any{
		"outbounds": []any{
			map[string]any{
				"tag": "proxy", "protocol": "trojan",
				"settings": map[string]any{
					"servers": []any{map[string]any{"address": "www.calmlunch.com", "port": 443}},
				},
			},
		},
	}
	applySNIFrontXray(cfg, 40643)
	obs := cfg["outbounds"].([]any)
	srv := obs[0].(map[string]any)["settings"].(map[string]any)["servers"].([]any)
	first := srv[0].(map[string]any)
	if first["address"] != "127.0.0.1" || first["port"] != 40643 {
		t.Fatalf("xray proxy server = %v:%v, want 127.0.0.1:40643", first["address"], first["port"])
	}
}

// TestSpoofEngineDeathReportsExit — a committed spoof engine that dies on its
// own must be reported upward: the group's transport core keeps dialing the
// dead loopback port, so without the report the UI stays "connected" over a
// black hole and the auto-enabled system proxy is never reverted. A Stop the
// user asked for is not a fault and must stay quiet.
func TestSpoofEngineDeathReportsExit(t *testing.T) {
	buildSpoofMock(t)
	m := NewSpoofManager(make(chan LogEntry, 1024))
	reports := make(chan EngineExit, 4)
	m.SetExitHandler(func(ev EngineExit) { reports <- ev })

	if err := m.Start(spoofTestProfile(), spoofTestSettings()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.mu.Lock()
	proc := m.process
	m.mu.Unlock()
	if proc == nil {
		t.Fatal("no child process after Start")
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the mock engine: %v", err)
	}
	var ev EngineExit
	select {
	case ev = <-reports:
	case <-time.After(10 * time.Second):
		t.Fatal("no EngineExit report after the engine died")
	}
	if ev.Engine != "spoof" {
		t.Fatalf("Engine = %q, want spoof", ev.Engine)
	}
	if ev.PID != proc.Pid {
		t.Fatalf("PID = %d, want %d", ev.PID, proc.Pid)
	}
	if ev.Err == nil {
		t.Fatal("Err = nil, want the exit error")
	}
	if !strings.Contains(ev.Summary(), "spoof") {
		t.Fatalf("Summary = %q, want it to name the spoof engine", ev.Summary())
	}
	waitFor(t, "running=false after the crash", func() bool { return !m.IsRunning() })

	if err := m.Start(spoofTestProfile(), spoofTestSettings()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case got := <-reports:
		t.Fatalf("a clean Stop reported a fault: %+v", got)
	case <-time.After(750 * time.Millisecond):
	}
}

// TestSpoofEngineDeathTearsTheGroupDown — CoreManager must accept a "spoof"
// death report for a committed CoreSpoof group (and ignore one from a
// generation it already replaced).
func TestSpoofEngineDeathTearsTheGroupDown(t *testing.T) {
	sb := NewSingBoxManager("", nil)
	xr := NewXrayManager("", "", nil)
	m := NewCoreManager(sb, xr)
	spoof := NewSpoofManager(nil)
	m.SetSpoofManager(spoof)

	m.mu.Lock()
	m.running = true
	m.activeCore = CoreSpoof
	m.activeID = "p1"
	epoch := m.epoch
	m.mu.Unlock()
	setSpoofFrontPort(40999)
	t.Cleanup(clearSpoofFrontPort)

	m.handleEngineExit(newEngineExit("spoof", nil, errors.New("engine died"), time.Now().Add(-time.Minute), nil))

	m.mu.Lock()
	running, activeID, curEpoch := m.running, m.activeID, m.epoch
	m.mu.Unlock()
	if running || activeID != "" {
		t.Fatalf("group still published: running=%v activeID=%q", running, activeID)
	}
	if curEpoch <= epoch {
		t.Fatalf("epoch = %d, want it advanced past %d by the teardown", curEpoch, epoch)
	}
	if p, ok := currentSpoofFrontPort(); ok {
		t.Fatalf("the dead engine's front port is still published: %d", p)
	}
}

// --- endpoint resolution against a lying resolver -------------------------
//
// Measured on this network: UDP/53 to every public resolver is redirected, and
// the system resolver alternates between the real edge IP and a captive 10.x
// for the same name. That is per-query, not permanent, so one poisoned answer
// set must not end Start — and the retry must stay bounded, because this runs
// synchronously while the manager mutex is held.

type spoofLookupAnswer struct {
	ips []net.IPAddr
	err error
}

// queueSpoofLookups replaces the resolver with a scripted one, one entry per
// call, and reports how many calls were actually made. Calls beyond the script
// return an error so an unbounded retry fails the test instead of hanging.
func queueSpoofLookups(t *testing.T, answers ...spoofLookupAnswer) *int {
	t.Helper()
	old := lookupSpoofEndpointAddr
	calls := new(int)
	lookupSpoofEndpointAddr = func(_ context.Context, _ string) ([]net.IPAddr, error) {
		i := *calls
		*calls = i + 1
		if i >= len(answers) {
			return nil, fmt.Errorf("resolver called %d times, only %d answers scripted", i+1, len(answers))
		}
		return answers[i].ips, answers[i].err
	}
	t.Cleanup(func() { lookupSpoofEndpointAddr = old })
	return calls
}

func ipAddrs(ips ...string) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(ips))
	for _, s := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out
}

func TestResolveSpoofEndpointIPRetriesPastAPoisonedAnswer(t *testing.T) {
	calls := queueSpoofLookups(t,
		spoofLookupAnswer{ips: ipAddrs("10.10.34.36")},
		spoofLookupAnswer{ips: ipAddrs("57.144.244.34")},
	)
	got, err := resolveSpoofEndpointIP("www.instagram.com")
	if err != nil {
		t.Fatalf("poisoned first answer = %v, want the retry to find the real edge", err)
	}
	if got != "57.144.244.34" {
		t.Fatalf("got %s, want the public answer from the second lookup", got)
	}
	if *calls != 2 {
		t.Fatalf("resolver calls = %d, want 2", *calls)
	}
}

// The success path must not pay for the retries: a usable first answer returns
// immediately.
func TestResolveSpoofEndpointIPStopsAtTheFirstUsableAnswer(t *testing.T) {
	calls := queueSpoofLookups(t, spoofLookupAnswer{ips: ipAddrs("93.184.216.34", "104.21.5.6")})
	got, err := resolveSpoofEndpointIP("example.com")
	if err != nil || got != "104.21.5.6" {
		t.Fatalf("got %s/%v, want the Cloudflare edge", got, err)
	}
	if *calls != 1 {
		t.Fatalf("resolver calls = %d, want exactly 1", *calls)
	}
}

func TestResolveSpoofEndpointIPIsBoundedWhenEveryAnswerIsPoison(t *testing.T) {
	calls := queueSpoofLookups(t,
		spoofLookupAnswer{ips: ipAddrs("10.10.34.36")},
		spoofLookupAnswer{ips: ipAddrs("10.10.34.36")},
		spoofLookupAnswer{ips: ipAddrs("10.10.34.36")},
	)
	_, err := resolveSpoofEndpointIP("www.instagram.com")
	if err == nil {
		t.Fatal("all-poison answers must fail, not dial the interceptor")
	}
	if !strings.Contains(err.Error(), "only private answers") {
		t.Fatalf("error = %v, want the interceptor hint", err)
	}
	if !strings.Contains(err.Error(), "3 lookups") {
		t.Fatalf("error = %v, want it to say how many lookups ran", err)
	}
	if strings.Count(err.Error(), "10.10.34.36") != 1 {
		t.Fatalf("error = %v, want each distinct answer listed once", err)
	}
	if *calls != spoofResolveAttempts {
		t.Fatalf("resolver calls = %d, want the budget of %d", *calls, spoofResolveAttempts)
	}
}

// A lookup error is retried too, and answer sets accumulate: the second
// response can add a usable edge next to the address the first one poisoned.
func TestResolveSpoofEndpointIPAccumulatesAnswersAcrossLookups(t *testing.T) {
	calls := queueSpoofLookups(t,
		spoofLookupAnswer{err: errors.New("dns server misbehaving")},
		spoofLookupAnswer{ips: ipAddrs("10.10.34.36")},
		spoofLookupAnswer{ips: ipAddrs("10.10.34.36", "172.67.10.1")},
	)
	got, err := resolveSpoofEndpointIP("www.youtube.com")
	if err != nil || got != "172.67.10.1" {
		t.Fatalf("got %s/%v, want the Cloudflare edge from the accumulated set", got, err)
	}
	if *calls != 3 {
		t.Fatalf("resolver calls = %d, want 3", *calls)
	}
}

func TestAppendSeenIPAddrsDeduplicates(t *testing.T) {
	seen := appendSeenIPAddrs(nil, ipAddrs("10.10.34.36", "216.239.38.119"))
	seen = appendSeenIPAddrs(seen, ipAddrs("216.239.38.119", "57.144.244.34"))
	if len(seen) != 3 {
		t.Fatalf("seen = %v, want 3 distinct addresses", seen)
	}
}
