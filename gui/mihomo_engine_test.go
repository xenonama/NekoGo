package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests measure the REAL mihomo binary. Everything else in
// mihomo_manager_test.go pins the rendered document's shape, which cannot
// tell whether mihomo accepts that shape, whether -d/-f/-t mean what the
// manager assumes, or whether the controller answers the reads GetStats
// and ListConnections make. They skip when the engine was never
// downloaded, exactly like testCoreBins.

// testMihomoBin returns the shipped engine next to the test's working
// directory (gui/build/bin), the same layout the app resolves from.
func testMihomoBin(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("build", "bin", "mihomo.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("missing %s", bin)
	}
	return bin
}

// mihomoEngineSettings points the manager at that binary and pins a
// loopback mixed inbound, so no test touches the user's real port or the
// system proxy.
func mihomoEngineSettings(t *testing.T, mixedPort int) Settings {
	t.Helper()
	s := DefaultSettings()
	s.MihomoPath = testMihomoBin(t)
	s.MixedPort = mixedPort
	s.InboundAddress = "127.0.0.1"
	return s
}

// TestMihomoEngineAcceptsRenderedConfigs runs mihomo -t over a document
// per protocol and per transport block. A key mihomo rejects (or silently
// ignores) fails here rather than at the user's first Connect.
func TestMihomoEngineAcceptsRenderedConfigs(t *testing.T) {
	s := mihomoEngineSettings(t, 0)
	m := NewMihomoManager(nil)
	// A 32-byte X25519 point in mihomo's unpadded base64url spelling.
	realityKey := strings.Repeat("A", 43)
	cases := []struct {
		name string
		p    Profile
	}{
		{"vless ws tls", Profile{
			Name: "v", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
			IDOrPassword: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "ws", TLS: true,
			ServerName: "sni.example", Path: "/ws", Host: "host.example",
		}},
		{"vless reality vision", Profile{
			Name: "r", Core: CoreMihomo, Protocol: "vless", Address: "1.2.3.4", Port: 2053,
			IDOrPassword: "b831381d-6324-4d53-ad4f-8cda48b30811", RealityEnabled: true,
			RealityPubKey: realityKey, RealityShortID: "0123456789abcdef",
			Flow: "xtls-rprx-vision", TLSFingerprint: "firefox",
		}},
		{"vless h2", Profile{
			Name: "h2", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
			IDOrPassword: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "h2", TLS: true,
			Path: "/h2", Host: "h2.example",
		}},
		{"trojan grpc alpn", Profile{
			Name: "t", Core: CoreMihomo, Protocol: "trojan", Address: "example.com", Port: 443,
			IDOrPassword: "pw", Network: "grpc", Path: "Svc", AllowInsecure: true, ALPN: "h2,http/1.1",
		}},
		{"vmess", Profile{
			Name: "m", Core: CoreMihomo, Protocol: "vmess", Address: "example.com", Port: 8443,
			IDOrPassword: "b831381d-6324-4d53-ad4f-8cda48b30811", AlterID: 0, Security: "auto", TLS: true,
		}},
		{"shadowsocks", Profile{
			Name: "ss", Core: CoreMihomo, Protocol: "shadowsocks", Address: "example.com", Port: 8388,
			IDOrPassword: "pw", Method: "aes-256-gcm",
		}},
		{"shadowsocks 2022", Profile{
			Name: "ss22", Core: CoreMihomo, Protocol: "shadowsocks", Address: "example.com", Port: 8388,
			// SS2022 ciphers take a base64 key of the cipher's length, not a
			// passphrase: 16 zero bytes for aes-128.
			IDOrPassword: strings.Repeat("A", 22) + "==", Method: "2022-blake3-aes-128-gcm",
		}},
		{"socks5 auth", Profile{
			Name: "so", Core: CoreMihomo, Protocol: "socks", Address: "127.0.0.1", Port: 1080,
			IDOrPassword: "user:pass",
		}},
		{"http auth", Profile{
			Name: "hp", Core: CoreMihomo, Protocol: "http", Address: "127.0.0.1", Port: 8080,
			IDOrPassword: "user:pass",
		}},
		{"aether", Profile{
			Name: "ae", Core: CoreMihomo, Protocol: "aether", AetherEnginePort: 40000,
		}},
		{"smux", Profile{
			Name: "mux", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
			IDOrPassword: "b831381d-6324-4d53-ad4f-8cda48b30811", TLS: true,
			MuxEnabled: true, MuxProtocol: "smux", MuxConcurrency: 8,
		}},
		{"raw outbound", Profile{
			Name: "raw", Core: CoreMihomo, Protocol: "trojan",
			RawOutbound: `{"type":"trojan","name":"pasted","server":"example.com","port":443,"password":"pw","sni":"example.com"}`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiPort, err := freePort()
			if err != nil {
				t.Fatal(err)
			}
			p := tc.p
			cfg, err := BuildMihomoConfig(&p, s, apiPort, "test-secret")
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if err := m.CheckConfig(cfg, s); err != nil {
				t.Fatalf("mihomo -t rejected the rendered document: %v\n%s", err, cfg)
			}
		})
	}
}

// TestMihomoEngineRejectsBadConfig proves -t is a real gate: a document
// with an unknown proxy type must fail, otherwise the test above would
// pass on a binary that ignores its config entirely.
func TestMihomoEngineRejectsBadConfig(t *testing.T) {
	s := mihomoEngineSettings(t, 0)
	apiPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildMihomoConfig(&Profile{
		Name: "bad", Core: CoreMihomo, Protocol: "trojan",
		RawOutbound: `{"type":"not-a-proxy","name":"x","server":"example.com","port":443}`,
	}, s, apiPort, "test-secret")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	err = NewMihomoManager(nil).CheckConfig(cfg, s)
	if err == nil {
		t.Fatal("mihomo -t accepted an unknown proxy type")
	}
	// mihomo prints the reason on stdout. Surfacing it is the difference
	// between an actionable error and a bare "exit status 1".
	if !strings.Contains(err.Error(), "not-a-proxy") {
		t.Fatalf("CheckConfig dropped mihomo's own diagnostic: %v", err)
	}
}

// localForwardProxy stands in for the profile's remote server: a minimal
// HTTP forward proxy that answers both spellings an upstream may be asked
// for — an absolute-URI GET and a CONNECT tunnel — and records which one
// arrived. It makes the whole route measurable without the internet (DoH
// and live TLS are intercepted on the build network).
func localForwardProxy(t *testing.T) (addr string, seen <-chan string) {
	t.Helper()
	requests := make(chan string, 16)
	record := func(s string) {
		select {
		case requests <- s:
		default:
		}
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			record("CONNECT " + r.Host)
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			target, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
			if err != nil {
				_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
				return
			}
			defer target.Close()
			if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
				return
			}
			go func() { _, _ = io.Copy(target, buf) }()
			_, _ = io.Copy(conn, target)
			return
		}
		target := r.URL.String()
		if !r.URL.IsAbs() {
			target = "http://" + r.Host + r.URL.RequestURI()
		}
		record(r.Method + " " + target)
		resp, err := client.Get(target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host, requests
}

// TestMihomoEngineRoutesTrafficThroughProfile is the end-to-end proof:
// a client dials the rendered mixed inbound, mihomo's MATCH rule sends it
// to the NekoGo group, the group's only member is the profile's proxy,
// and that proxy forwards to a local origin. The origin's body coming
// back means every hop in the rendered document actually ran.
func TestMihomoEngineRoutesTrafficThroughProfile(t *testing.T) {
	const body = "nekogo-mihomo-route-ok"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer origin.Close()
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	originAuthority := originURL.Host

	upstream, seen := localForwardProxy(t)
	host, portStr, err := net.SplitHostPort(upstream)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	// Both halves: mihomo binds UDP on the mixed port too, and Windows
	// reserves UDP ranges (52303-52402 on the build machine) that a
	// TCP-only probe hands out as free.
	mixedPort, err := freePortTCPAndUDP()
	if err != nil {
		t.Fatal(err)
	}
	s := mihomoEngineSettings(t, mixedPort)
	logs := make(chan LogEntry, 512)
	m := NewMihomoManager(logs)
	p := &Profile{
		ID: "route", Name: "route", Core: CoreMihomo, Protocol: "http",
		Address: host, Port: port,
	}
	if err := m.Start(p, s); err != nil {
		t.Fatalf("start mihomo: %v\n%s", err, drainMihomoLogs(logs))
	}
	defer m.Stop()

	if !m.IsRunning() {
		t.Fatal("mihomo not running after Start")
	}
	if cfg := m.LiveConfig(); !strings.Contains(cfg, `"mixed-port"`) {
		t.Fatalf("LiveConfig is not the rendered document: %q", cfg)
	}

	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort))}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	resp, err := client.Get(origin.URL + "/hello")
	if err != nil {
		t.Fatalf("GET through the mihomo mixed inbound: %v\n%s", err, drainMihomoLogs(logs))
	}
	got, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != body {
		t.Fatalf("through mihomo: HTTP %d body %q, want 200 %q\nupstream saw: %v\nengine log:\n%s",
			resp.StatusCode, got, body, drainMihomoRequests(seen), drainMihomoLogs(logs))
	}
	select {
	case req := <-seen:
		// Measured against v1.19.33: mihomo's "http" proxy adapter tunnels
		// with CONNECT even for a plain-HTTP target, so the authority it
		// asks for — not the path — is what proves the route.
		if want := "CONNECT " + originAuthority; req != want {
			t.Fatalf("upstream proxy saw %q, want %q", req, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the profile's proxy was never dialed — the MATCH rule did not route through it")
	}

	// The controller reads the GUI polls must answer on a live engine.
	stats, err := m.GetStats()
	if err != nil || stats == nil {
		t.Fatalf("GetStats: %v %+v", err, stats)
	}
	if !stats.Running || stats.Core != mihomoCoreName || stats.ApiDown {
		t.Fatalf("GetStats on a live engine: %+v", stats)
	}
	conns, err := m.ListConnections()
	if err != nil || conns == nil {
		t.Fatalf("ListConnections: %v %+v", err, conns)
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := m.apiGet("/version", 3*time.Second, &version); err != nil {
		t.Fatalf("controller /version: %v", err)
	}
	if version.Version == "" {
		t.Fatal("controller /version returned no version")
	}
	t.Logf("mihomo controller reports %s; stats up=%d down=%d; %d connection rows",
		version.Version, stats.Uplink, stats.Downlink, len(conns.Connections))

	// URLTest is the Proxies-tab spinner: it must measure through this
	// same inbound, not through a throwaway engine.
	res, err := m.URLTest(origin.URL+"/hello", 8000)
	if err != nil || res == nil {
		t.Fatalf("URLTest through the live engine: %v %+v", err, res)
	}
	if res.OutboundTag != mihomoProxyName || res.LatencyMs < 0 {
		t.Fatalf("URLTest result: %+v", res)
	}

	m.mu.Lock()
	dir := m.tempConfigPath
	m.mu.Unlock()
	if err := m.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if m.IsRunning() {
		t.Fatal("still running after Stop")
	}
	assertPortState(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort)), false)
	if dir != "" {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("Stop left the secret-bearing temp dir behind: %s (err %v)", dir, err)
		}
	}
}

// TestMihomoEngineEphemeralRun pins the Test-button path: a throwaway
// engine on its own port, torn down without touching any active core.
func TestMihomoEngineEphemeralRun(t *testing.T) {
	s := mihomoEngineSettings(t, 0)
	m := NewMihomoManager(make(chan LogEntry, 512))
	p := &Profile{
		ID: "eph", Name: "eph", Core: CoreMihomo, Protocol: "socks",
		Address: "127.0.0.1", Port: 1080,
	}
	port, err := m.EphemeralRun(p, s, 0)
	if err != nil {
		t.Fatalf("EphemeralRun: %v", err)
	}
	defer m.StopEphemeral()
	if port <= 0 {
		t.Fatalf("EphemeralRun returned port %d", port)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	assertPortState(t, addr, true)
	if m.IsRunning() {
		t.Fatal("an ephemeral probe marked the manager as the active core")
	}
	m.StopEphemeral()
	assertPortState(t, addr, false)
}

// drainMihomoLogs renders the child's own output for a failure message:
// mihomo explains a rejected config on stdout, and the manager logs it.
func drainMihomoLogs(ch chan LogEntry) string {
	var b strings.Builder
	for {
		select {
		case e := <-ch:
			fmt.Fprintf(&b, "[%s] %s\n", e.Level, e.Message)
		default:
			return b.String()
		}
	}
}

func drainMihomoRequests(ch <-chan string) []string {
	var out []string
	for {
		select {
		case r := <-ch:
			out = append(out, r)
		default:
			return out
		}
	}
}
