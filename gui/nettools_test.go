package main

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Pure-helper tests for Phase C (no core process needed).
func TestParseBrateMbps(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"12.34Mbps", 12.34},
		{"5.67Kbps", 0.00567},
		{"1.23Gbps", 1230},
		{"0.00Mbps", 0},
		{"", 0},
		{"N/A", 0},
		{"12.34 Mbps", 12.34}, // surrounding space tolerated by design
		{"-1.5Mbps", 0},
		{"abcMbps", 0},
	}
	for _, c := range cases {
		if got := parseBrateMbps(c.in); got != c.want {
			t.Errorf("parseBrateMbps(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// HOTFIX repro (Bug 1): LastNetworkResults with no core running must
// return a non-nil, empty struct — never panic, never nil.
func TestLastNetworkResultsNoCore(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	out, err := app.LastNetworkResults()
	if err != nil {
		t.Fatalf("LastNetworkResults() err = %v, want nil (idle = empty, not error)", err)
	}
	if out == nil {
		t.Fatal("LastNetworkResults() = nil, want non-nil empty struct")
	}
	if out.IP != nil || out.Speed != nil || out.URL != nil || len(out.Country) != 0 {
		t.Fatalf("LastNetworkResults() = %+v, want empty", out)
	}
}

// Nil managers must not panic either (still non-nil struct back).
// Idle wins over misconfiguration: empty + nil error. Only a running
// core with missing managers reports an error (still with a struct).
func TestLastNetworkResultsNilManagers(t *testing.T) {
	m := NewCoreManager(nil, nil)
	if out, err := m.LastNetwork(); out == nil || err != nil {
		t.Fatalf("idle LastNetwork() = %v, %v; want non-nil, nil", out, err)
	}
	m.running = true
	out, err := m.LastNetwork()
	if out == nil {
		t.Fatal("LastNetwork() = nil, want non-nil struct")
	}
	if err == nil {
		t.Fatal("LastNetwork() err = nil, want managers-not-initialized")
	}
}

// Concurrent mount + stats-poll shape: LastNetwork and GetStats side by
// side must neither panic nor deadlock (run with -race in CI).
func TestLastNetworkResultsConcurrent(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := app.LastNetworkResults(); err != nil || out == nil {
				t.Errorf("LastNetworkResults() = %v, %v; want non-nil, nil", out, err)
			}
			if _, err := app.GetStats(); err != nil {
				t.Errorf("GetStats() err = %v", err)
			}
		}()
	}
	wg.Wait()
}

// --- mihomo Phase C routing ---
//
// mihomo exposes no test RPCs, so its tools must take the client-side
// legs against its OWN mixed inbound. Before this they fell through to
// the sing-box Thrift path, which dialed a core that was not running.
// No mihomo.exe is involved: a local listener stands in for the inbound.

// recordingInbound accepts connections and reports the request line an
// HTTP proxy would see. The IP/speed endpoints are https, so Go issues
// CONNECT — the recorded authority names the inbound that was dialed.
func recordingInbound(t *testing.T) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	lines := make(chan string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				line, _ := bufio.NewReader(c).ReadString('\n')
				if line = strings.TrimSpace(line); line != "" {
					select {
					case lines <- line:
					default:
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), lines
}

// mihomoNetRig wires a CoreManager that believes mihomo is the active
// core, with mh's mixed port pointed at inbound. sing-box and xray are
// present but idle, exactly as in a real mihomo connect.
func mihomoNetRig(t *testing.T, inbound string) (*CoreManager, *MihomoManager, *XrayManager) {
	t.Helper()
	_, portStr, err := net.SplitHostPort(inbound)
	if err != nil {
		t.Fatalf("inbound %q: %v", inbound, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	logCh := make(chan LogEntry, 64)
	mh := NewMihomoManager(logCh)
	mh.mu.Lock()
	mh.running, mh.mixedPort = true, port
	mh.mu.Unlock()
	xr := NewXrayManager("", t.TempDir(), logCh)
	cores := NewCoreManager(NewSingBoxManager("", logCh), xr)
	cores.SetMihomoManager(mh)
	cores.mu.Lock()
	cores.running, cores.activeCore = true, CoreMihomo
	cores.mu.Unlock()
	return cores, mh, xr
}

func assertXrayCacheUntouched(t *testing.T, xr *XrayManager) {
	t.Helper()
	if ip, speed, url := xr.probes.snapshot(); ip != nil || speed != nil || url != nil {
		t.Fatalf("mihomo tools wrote the xray cache: ip=%v speed=%v url=%v", ip, speed, url)
	}
}

func TestMihomoIPTestDialsOwnInbound(t *testing.T) {
	inbound, lines := recordingInbound(t)
	cores, _, xr := mihomoNetRig(t, inbound)
	// A nil profile is rejected by the sing-box leg ("nil profile") and
	// an idle Thrift core by "core not running": neither may appear here.
	_, err := cores.IPTest(nil, Settings{})
	if err == nil {
		t.Fatal("IPTest() err = nil, want the fake inbound to fail the tunnel")
	}
	if strings.Contains(err.Error(), "nil profile") || strings.Contains(err.Error(), "core not running") {
		t.Fatalf("IPTest took another engine's path: %v", err)
	}
	select {
	case line := <-lines:
		if !strings.HasPrefix(line, "CONNECT api.ip2location.io:443") {
			t.Fatalf("inbound saw %q, want the ip2location CONNECT", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mihomo's mixed inbound never saw the IP probe")
	}
	assertXrayCacheUntouched(t, xr)
}

func TestMihomoSpeedTestDialsOwnInbound(t *testing.T) {
	inbound, lines := recordingInbound(t)
	cores, _, xr := mihomoNetRig(t, inbound)
	_, err := cores.SpeedTest()
	if err == nil {
		t.Fatal("SpeedTest() err = nil, want the fake inbound to fail the tunnel")
	}
	if strings.Contains(err.Error(), "core not running") {
		t.Fatalf("SpeedTest took the sing-box path: %v", err)
	}
	select {
	case line := <-lines:
		if !strings.HasPrefix(line, "CONNECT speed.cloudflare.com:443") {
			t.Fatalf("inbound saw %q, want the Cloudflare CONNECT", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mihomo's mixed inbound never saw the speed probe")
	}
	assertXrayCacheUntouched(t, xr)
}

// TestMihomoURLTestCachesForLastNetwork: the positive path. The httptest
// server doubles as origin and absolute-URI pseudo-proxy (probe_test.go
// uses the same shape), so a plain-http target needs no TLS tunnel.
func TestMihomoURLTestCachesForLastNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	cores, _, xr := mihomoNetRig(t, strings.TrimPrefix(srv.URL, "http://"))
	target := srv.URL + "/generate_204"
	out, err := cores.URLTest(target, 5000)
	if err != nil {
		t.Fatalf("URLTest() err = %v", err)
	}
	if out == nil || !out.OK || out.URL != target || out.Error != "" {
		t.Fatalf("URLTest() = %+v, want OK row for %s", out, target)
	}
	if out.LatencyMs < 0 {
		t.Fatalf("URLTest() latency = %d, want >= 0", out.LatencyMs)
	}
	last, err := cores.LastNetwork()
	if err != nil || last == nil {
		t.Fatalf("LastNetwork() = %v, %v; want non-nil, nil", last, err)
	}
	if last.URL == nil || last.URL.URL != target || !last.URL.OK {
		t.Fatalf("LastNetwork().URL = %+v, want the cached row for %s", last.URL, target)
	}
	// mihomo has no country reporter and neither IP nor speed has run.
	if last.IP != nil || last.Speed != nil || len(last.Country) != 0 || last.SpeedRunning {
		t.Fatalf("LastNetwork() = %+v, want only the URL row", last)
	}
	assertXrayCacheUntouched(t, xr)
}

// TestMihomoStopTestAbortsProbe: StopTest must cancel the in-flight
// client-side probe (BUG-009 parity with xray), not wait out its timeout.
func TestMihomoStopTestAbortsProbe(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	defer close(release)
	cores, _, _ := mihomoNetRig(t, strings.TrimPrefix(srv.URL, "http://"))
	done := make(chan error, 1)
	go func() {
		_, err := cores.URLTest(srv.URL+"/slow", 30000)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe never reached mihomo's inbound")
	}
	start := time.Now()
	if err := cores.StopTest(); err != nil {
		t.Fatalf("StopTest() err = %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("aborted probe returned nil error")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("abort took %v, want a fast cancel", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StopTest did not abort the in-flight probe")
	}
}

// TestMihomoNetToolsGateOnRunning: a stopped mihomo must fail in its own
// leg, never dial another engine's inbound.
func TestMihomoNetToolsGateOnRunning(t *testing.T) {
	inbound, lines := recordingInbound(t)
	cores, mh, _ := mihomoNetRig(t, inbound)
	mh.mu.Lock()
	mh.running = false
	mh.mu.Unlock()
	if _, err := cores.IPTest(nil, Settings{}); err == nil || !strings.Contains(err.Error(), "mihomo not running") {
		t.Fatalf("IPTest() err = %v, want mihomo not running", err)
	}
	if _, err := cores.SpeedTest(); err == nil || !strings.Contains(err.Error(), "mihomo not running") {
		t.Fatalf("SpeedTest() err = %v, want mihomo not running", err)
	}
	if _, err := cores.URLTest("http://probe.example/", 3000); err == nil || !strings.Contains(err.Error(), "mihomo not running") {
		t.Fatalf("URLTest() err = %v, want mihomo not running", err)
	}
	select {
	case line := <-lines:
		t.Fatalf("a stopped mihomo still dialed its inbound: %q", line)
	default:
	}
}

// TestMihomoNetToolsNilManager: a wiring bug reports itself instead of
// silently taking the sing-box path. LastNetwork stays non-nil (it seeds
// the Network tab on mount).
func TestMihomoNetToolsNilManager(t *testing.T) {
	logCh := make(chan LogEntry, 8)
	cores := NewCoreManager(NewSingBoxManager("", logCh), NewXrayManager("", t.TempDir(), logCh))
	cores.mu.Lock()
	cores.running, cores.activeCore = true, CoreMihomo
	cores.mu.Unlock()
	if _, err := cores.IPTest(nil, Settings{}); err == nil || !strings.Contains(err.Error(), "mihomo manager is nil") {
		t.Fatalf("IPTest() err = %v, want mihomo manager is nil", err)
	}
	if _, err := cores.SpeedTest(); err == nil || !strings.Contains(err.Error(), "mihomo manager is nil") {
		t.Fatalf("SpeedTest() err = %v, want mihomo manager is nil", err)
	}
	if _, err := cores.URLTest("", 0); err == nil || !strings.Contains(err.Error(), "mihomo manager is nil") {
		t.Fatalf("URLTest() err = %v, want mihomo manager is nil", err)
	}
	if err := cores.StopTest(); err == nil || !strings.Contains(err.Error(), "mihomo manager is nil") {
		t.Fatalf("StopTest() err = %v, want mihomo manager is nil", err)
	}
	if out, err := cores.LastNetwork(); out == nil || err == nil {
		t.Fatalf("LastNetwork() = %v, %v; want non-nil struct plus an error", out, err)
	}
}
