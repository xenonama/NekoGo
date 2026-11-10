package main

// Tests for the on-demand (IP, SNI) TLS scanner (sni_scan.go).
//
// The positive test runs against a local httptest TLS server with a
// valid cert chain (the test supplies the server's cert as the only
// trust root — chain validation stays ON). The negative test probes
// a closed port. No internet, no Cloudflare, no admin needed.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scanTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	if err := store.UpdateSNI(func(s *Settings) {
		s.SNIIPs = []SNIIP{{IP: "127.0.0.1", Source: "test"}}
		s.SNISNIs = []SNISNI{{Host: "example.com", Source: "test"}}
	}); err != nil {
		t.Fatalf("seed SNI lists: %v", err)
	}
	return NewApp(store, make(chan LogEntry, 64))
}

func withLocalTLS(t *testing.T, srv *httptest.Server) {
	t.Helper()
	addr := srv.Listener.Addr().(*net.TCPAddr)
	oldPort := sniScanPortOverride
	sniScanPortOverride = addr.Port
	oldRoots := sniScanTestRootCAs
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	sniScanTestRootCAs = func() *x509.CertPool { return pool }
	t.Cleanup(func() {
		sniScanPortOverride = oldPort
		sniScanTestRootCAs = oldRoots
	})
}

func TestScanSNIPairsAgainstLocalTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	withLocalTLS(t, ts)

	a := scanTestApp(t)
	res, err := a.ScanSNIPairs([]string{"127.0.0.1"}, []string{"example.com"})
	if err != nil {
		t.Fatalf("ScanSNIPairs: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	r := res[0]
	if !r.OK {
		t.Fatalf("pair not OK: %s", r.Error)
	}
	if r.LatencyMs <= 0 {
		t.Fatalf("LatencyMs = %d, want positive", r.LatencyMs)
	}
	if r.TLSVersion == "" {
		t.Fatal("TLSVersion empty on success")
	}
	// Results persist into the curated lists (BUG B6: never dropped).
	got := a.store.GetSettings()
	if len(got.SNIIPs) != 1 || len(got.SNISNIs) != 1 {
		t.Fatalf("scanner added/removed entries: %+v / %+v", got.SNIIPs, got.SNISNIs)
	}
	if !got.SNIIPs[0].LastOK || got.SNIIPs[0].LastMs <= 0 || got.SNIIPs[0].LastAt == "" || got.SNIIPs[0].LastTLS == "" {
		t.Fatalf("SNIIPs[0] not stamped: %+v", got.SNIIPs[0])
	}
	if !got.SNISNIs[0].LastOK || got.SNISNIs[0].LastMs <= 0 || got.SNISNIs[0].LastAt == "" {
		t.Fatalf("SNISNIs[0] not stamped: %+v", got.SNISNIs[0])
	}
	st, err := a.ScanSNIStatus()
	if err != nil {
		t.Fatalf("ScanSNIStatus: %v", err)
	}
	if st.Running {
		t.Fatal("scanner still running after return")
	}
	if st.Total != 1 || st.Done != 1 {
		t.Fatalf("status = %+v, want done/total 1/1", st)
	}
}

func TestScanSNIPairsClosedPort(t *testing.T) {
	// Reserve then release a port so nothing listens on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	oldPort := sniScanPortOverride
	sniScanPortOverride = port
	t.Cleanup(func() { sniScanPortOverride = oldPort })

	a := scanTestApp(t)
	res, err := a.ScanSNIPairs([]string{"127.0.0.1"}, []string{"example.com"})
	if err != nil {
		t.Fatalf("ScanSNIPairs: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].OK || res[0].Error == "" {
		t.Fatalf("closed port reported OK: %+v", res[0])
	}
	got := a.store.GetSettings()
	if got.SNIIPs[0].LastOK {
		t.Fatalf("failure persisted as LastOK=true: %+v", got.SNIIPs[0])
	}
}

func TestScanSNIPairsValidation(t *testing.T) {
	a := scanTestApp(t)
	if _, err := a.ScanSNIPairs(nil, []string{"example.com"}); err == nil {
		t.Fatal("empty ips accepted, want error")
	}
	if _, err := a.ScanSNIPairs([]string{"127.0.0.1"}, nil); err == nil {
		t.Fatal("empty snis accepted, want error")
	}
	if err := a.CancelSNIScan(); err != nil {
		t.Fatalf("CancelSNIScan idle: %v", err)
	}
}

// TestCancelSNIScanKeepsTheScanRunning — cancelling only signals the
// workers, which keep draining for as long as their handshakes take. The
// one-scan guard and the UI's progress readout must stay on "running"
// until the scan's own cleanup, or a second scan starts on top of the
// first and both write done/total (progress double-counts past 100%).
func TestCancelSNIScanKeepsTheScanRunning(t *testing.T) {
	a := scanTestApp(t)
	sniScanState.mu.Lock()
	if sniScanState.running {
		sniScanState.mu.Unlock()
		t.Fatal("scanner busy before the test started")
	}
	cancelled := false
	sniScanState.running = true
	sniScanState.cancel = func() { cancelled = true }
	sniScanState.done, sniScanState.total = 1, 4
	sniScanState.mu.Unlock()
	t.Cleanup(func() {
		sniScanState.mu.Lock()
		sniScanState.running = false
		sniScanState.cancel = nil
		sniScanState.done, sniScanState.total = 0, 0
		sniScanState.last = ""
		sniScanState.mu.Unlock()
	})

	if err := a.CancelSNIScan(); err != nil {
		t.Fatalf("CancelSNIScan: %v", err)
	}
	if !cancelled {
		t.Fatal("cancel func not called")
	}
	st, err := a.ScanSNIStatus()
	if err != nil {
		t.Fatalf("ScanSNIStatus: %v", err)
	}
	if !st.Running || st.Done != 1 || st.Total != 4 {
		t.Fatalf("status after cancel = %+v, want running with 1/4", st)
	}
	if _, err := a.ScanSNIPairs([]string{"127.0.0.1"}, []string{"example.com"}); err == nil {
		t.Fatal("second scan accepted while the first still drains")
	}
}

// The scan's handshake must really go out split, and the split must be
// lossless — otherwise the scanner would be measuring something the engine
// never sends. This captures a real Go ClientHello on its way to a local TLS
// server and asserts on the pieces it was cut into.
func TestSplitHelloConnCutsARealClientHello(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	withLocalTLS(t, ts)

	raw, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()
	sc := &splitHelloConn{Conn: raw}
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	conn := tls.Client(sc, &tls.Config{ServerName: "example.com", RootCAs: pool, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatalf("handshake through the splitter: %v", err)
	}
	_ = conn.Close()

	if sc.hello == nil {
		t.Fatal("no ClientHello was captured")
	}
	pieces := splitClientHello(sc.hello)
	if len(pieces) < 2 {
		t.Fatalf("the hello went out as %d piece(s), want it split", len(pieces))
	}
	var joined []byte
	for _, p := range pieces {
		joined = append(joined, p...)
	}
	if !bytes.Equal(joined, sc.hello) {
		t.Fatal("the pieces do not reassemble into the original hello")
	}
	off, n, ok := sniHostSpan(sc.hello)
	if !ok {
		t.Fatal("the captured hello has no SNI extension")
	}
	host := string(sc.hello[off : off+n])
	if host != "example.com" {
		t.Fatalf("captured SNI = %q, want example.com", host)
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < minSplitLabel {
			continue
		}
		for i, p := range pieces {
			if bytes.Contains(p, []byte(label)) {
				t.Fatalf("piece %d still holds the whole label %q", i, label)
			}
		}
	}
}

// Nothing that is not a ClientHello may be touched: application data and
// every write after the hello go through as one piece.
func TestSplitClientHelloOnlyCutsTheHello(t *testing.T) {
	appData := []byte{0x17, 0x03, 0x03, 0x00, 0x04, 1, 2, 3, 4}
	if isTLSClientHello(appData) {
		t.Fatal("application data classified as a ClientHello")
	}
	if got := splitClientHello(appData); len(got) != 1 || !bytes.Equal(got[0], appData) {
		t.Fatalf("application data was altered: %v", got)
	}
	// A hello with no SNI extension has nothing to hide and stays whole.
	noSNI := make([]byte, 80)
	noSNI[0], noSNI[1], noSNI[5] = 0x16, 0x03, 0x01
	for i := range noSNI {
		if i > 5 {
			noSNI[i] = 0xAB
		}
	}
	if _, _, ok := sniHostSpan(noSNI); ok {
		t.Fatal("found an SNI extension in filler bytes")
	}
	if got := splitClientHello(noSNI); len(got) != 1 {
		t.Fatalf("a hello with no hostname was split into %d pieces", len(got))
	}
}
