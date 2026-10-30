package main

// Tests for the real proxied latency path (probe.go + ephemeral cores).
// Binary-dependent tests skip when build/bin lacks the core executables.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// startForwardProxy runs a minimal absolute-URI forward proxy that forwards
// the original method (so HEAD stays HEAD end to end).
func startForwardProxy(t *testing.T) string {
	t.Helper()
	direct := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if os.Getenv("NEKOBOX_PROXY_DEBUG") != "" {
				fmt.Printf("[fwdproxy] %s %s host=%q abs=%v\n", r.Method, r.URL.String(), r.Host, r.URL.IsAbs())
				for k, vs := range r.Header {
					fmt.Printf("[fwdproxy]   H %s: %s\n", k, strings.Join(vs, ","))
				}
			}
			if !r.URL.IsAbs() {
				http.Error(w, "absolute URI required", http.StatusBadRequest)
				return
			}
			out, err := http.NewRequest(r.Method, r.URL.String(), nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			resp, err := direct.Do(out)
			if err != nil {
				if os.Getenv("NEKOBOX_PROXY_DEBUG") != "" {
					fmt.Printf("[fwdproxy] direct.Do error: %v\n", err)
				}
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			for k, vs := range resp.Header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}))
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func TestProxiedHeadOrGetAgainstLocalListener(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("origin got method %s, want GET (Throne-style single GET)", r.Method)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "probe-ok")
	}))
	defer origin.Close()
	proxyAddr := startForwardProxy(t)

	d, err := proxiedHeadOrGet(proxyAddr, origin.URL+"/", 10*time.Second)
	if err != nil {
		t.Fatalf("GET case: %v", err)
	}
	// NOTE: d == 0 is possible on Windows (time.Now granularity is
	// ~0.5ms; loopback GET can complete within one tick). Only
	// negative or timed-out durations are failures.
	if d < 0 || d >= 10*time.Second {
		t.Fatalf("GET case: odd duration %v", d)
	}

	if _, err := proxiedHeadOrGet(closedLoopbackPort(t), origin.URL+"/", 3*time.Second); err == nil {
		t.Fatal("refused proxy: expected error, got nil")
	}
}

// startStaticResponder answers every accepted connection with the same
// raw HTTP bytes, then closes. Used to shape exact proxy/inbound
// response bytes.
func startStaticResponder(t *testing.T, raw string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte(raw))
				time.Sleep(50 * time.Millisecond)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// TestProxiedHeadOrGetRejectsSynthetic502: sing's HTTP inbound answers
// a failed outbound with a bare "502 Bad Gateway" (no Date/Server/
// Content-Type; only Content-Length: 0 from the write path). Accepting
// it gave dead paths a ~2ms "latency".
func TestProxiedHeadOrGetRejectsSynthetic502(t *testing.T) {
	proxyAddr := startStaticResponder(t, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
	if _, err := proxiedHeadOrGet(proxyAddr, "http://probe.example/", 3*time.Second); err == nil {
		t.Fatal("synthetic 502: expected error, got nil")
	}
}

// TestProxiedHeadOrGetKeepsOrigin502: an origin 502 carrying its own
// headers is a real HTTP response and still proves the proxy path
// works (the historical "any response counts" rule).
func TestProxiedHeadOrGetKeepsOrigin502(t *testing.T) {
	proxyAddr := startStaticResponder(t, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nboom\n")
	if _, err := proxiedHeadOrGet(proxyAddr, "http://probe.example/", 3*time.Second); err != nil {
		t.Fatalf("origin 502: %v, want success", err)
	}
}

func TestResolveTestURL(t *testing.T) {
	def := defaultTestURL
	withBoth := DefaultSettings()
	withBoth.TestUrl = "https://settings.example/"
	p := &Profile{TestURL: "https://profile.example/"}
	if got := resolveTestURL(p, withBoth); got != "https://profile.example/" {
		t.Fatalf("profile override: got %q", got)
	}
	if got := resolveTestURL(&Profile{}, withBoth); got != "https://settings.example/" {
		t.Fatalf("settings override: got %q", got)
	}
	if got := resolveTestURL(&Profile{}, Settings{}); got != def {
		t.Fatalf("default: got %q", got)
	}
	if got := resolveTestURL(nil, withBoth); got != "https://settings.example/" {
		t.Fatalf("nil profile: got %q", got)
	}
}

func testCoreBins(t *testing.T) (core, xray string) {
	t.Helper()
	binDir, err := filepath.Abs("build/bin")
	if err != nil {
		t.Fatal(err)
	}
	core = filepath.Join(binDir, "nekobox_core.exe")
	xray = filepath.Join(binDir, "xray.exe")
	if _, err := os.Stat(core); err != nil {
		t.Skipf("missing %s", core)
	}
	if _, err := os.Stat(xray); err != nil {
		t.Skipf("missing %s", xray)
	}
	return core, xray
}

func directProfile(core CoreType) *Profile {
	if core == CoreXray {
		return &Profile{ID: "eph", Name: "eph", Core: core, Protocol: "direct",
			RawOutbound: `{"protocol":"freedom","tag":"proxy"}`}
	}
	return &Profile{ID: "eph", Name: "eph", Core: core, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
}

func assertPortState(t *testing.T, addr string, wantOpen bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			if wantOpen {
				return
			}
		} else if !wantOpen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %s: want open=%v, last err=%v", addr, wantOpen, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestEphemeralURLTestTearsDown(t *testing.T) {
	coreBin, xrayBin := testCoreBins(t)
	logs := make(chan LogEntry, 512)
	settings := DefaultSettings()

	t.Run("singbox", func(t *testing.T) {
		mgr := NewSingBoxManager(coreBin, logs)
		port, err := mgr.EphemeralRun(directProfile(CoreSingBox), settings, 0)
		if err != nil {
			t.Fatalf("EphemeralRun: %v", err)
		}
		if port <= 0 {
			t.Fatalf("EphemeralRun reported no port (got %d)", port)
		}
		assertPortState(t, fmt.Sprintf("127.0.0.1:%d", port), true)
		mgr.StopEphemeral()
		assertPortState(t, fmt.Sprintf("127.0.0.1:%d", port), false)
	})

	t.Run("xray", func(t *testing.T) {
		mgr := NewXrayManager(xrayBin, "", logs)
		port, err := mgr.EphemeralRun(directProfile(CoreXray), settings, 0)
		if err != nil {
			t.Fatalf("EphemeralRun: %v", err)
		}
		if port <= 0 {
			t.Fatalf("EphemeralRun reported no port (got %d)", port)
		}
		assertPortState(t, fmt.Sprintf("127.0.0.1:%d", port), true)
		mgr.StopEphemeral()
		assertPortState(t, fmt.Sprintf("127.0.0.1:%d", port), false)
	})

	t.Run("full-path", func(t *testing.T) {
		sb := NewSingBoxManager(coreBin, logs)
		xr := NewXrayManager(xrayBin, "", logs)
		cm := NewCoreManager(sb, xr)
		failed := false
		defer func() {
			if !failed {
				return
			}
			t.Log("--- core log tail ---")
			for {
				select {
				case e := <-logs:
					t.Logf("[%s] %s", e.Source, e.Message)
				default:
					return
				}
			}
		}()
		for _, core := range []CoreType{CoreSingBox, CoreXray} {
			res, err := cm.EphemeralURLTest(directProfile(core), settings, "", 15000, probeOwnerNetwork)
			if err != nil {
				failed = true
				t.Fatalf("core %v: %v", core, err)
			}
			if res == nil || res.Error != "" || res.LatencyMs < 0 {
				failed = true
				t.Fatalf("core %v: bad result %+v", core, res)
			}
			t.Logf("core %v: %d ms", core, res.LatencyMs)
		}
	})
}

// TestTestProfileLeavesActiveUndisturbed — integration BUG-008:
// TestProfile ALWAYS probes through a throwaway ephemeral core now, even
// for the active profile, so a Network-tab StopTest (shared server test
// scope) can no longer cancel the sibling Proxies-tab spinner. Pins the
// undisturbed active connection for both the active and idle paths.
func TestTestProfileLeavesActiveUndisturbed(t *testing.T) {
	coreBin, xrayBin := testCoreBins(t)
	dir := t.TempDir()
	logs := make(chan LogEntry, 512)
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.CorePath = coreBin
	settings.XrayPath = xrayBin
	settings.MixedPort = 28080
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, logs)
	app.singbox.corePath = coreBin
	app.xray.xrayPath = xrayBin

	p, err := app.SaveProfile(Profile{Name: "live-direct", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Connect(p.ID); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer app.Disconnect()
	time.Sleep(2 * time.Second)

	res, err := app.TestProfile(p.ID, 15000)
	if err != nil {
		t.Fatalf("TestProfile(active): %v", err)
	}
	if res == nil || res.Error != "" || res.LatencyMs < 0 {
		t.Fatalf("TestProfile(active): bad result %+v", res)
	}
	t.Logf("active profile: %d ms", res.LatencyMs)
	if core, activeID, running := app.cores.Status(); !running || activeID != p.ID {
		t.Fatalf("active connection disturbed: core=%s id=%s running=%v", core, activeID, running)
	}

	p2, err := app.SaveProfile(Profile{Name: "other-direct", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	res2, err := app.TestProfile(p2.ID, 15000)
	if err != nil {
		t.Fatalf("TestProfile(other): %v", err)
	}
	if res2 == nil || res2.Error != "" || res2.LatencyMs < 0 {
		t.Fatalf("TestProfile(other): bad result %+v", res2)
	}
	if core, activeID, running := app.cores.Status(); !running || activeID != p.ID {
		t.Fatalf("ephemeral test disturbed active: core=%s id=%s running=%v", core, activeID, running)
	}
}

// TestClampProbeTimeoutMs — BUG-020: probe timeouts pin to 100..60000ms
// so int32 headroom addition cannot wrap into a negative context.
func TestClampProbeTimeoutMs(t *testing.T) {
	for timeoutMs, want := range map[int32]int32{
		-2147483648: 100,
		-1:          100,
		0:           100,
		50:          100,
		100:         100,
		8000:        8000,
		60000:       60000,
		2147483647:  60000,
	} {
		if got := clampProbeTimeoutMs(timeoutMs); got != want {
			t.Errorf("clampProbeTimeoutMs(%d) = %d, want %d", timeoutMs, got, want)
		}
	}
}

// TestProxiedHeadOrGetCtxHonorsCancel — BUG-031: a cancelled parent
// scope aborts the probe instead of waiting out the timeout. The
// httptest server doubles as origin and (absolute-URI) pseudo-proxy:
// it sleeps past the parent deadline, so only cancellation returns.
func TestProxiedHeadOrGetCtxHonorsCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	proxy := strings.TrimPrefix(srv.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := proxiedHeadOrGetCtx(ctx, proxy, srv.URL+"/x", 10*time.Second)
	if err == nil {
		t.Fatal("cancelled probe returned nil error, want abort")
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("cancelled probe took %v, want fast abort", d)
	}
}
