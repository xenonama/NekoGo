package main

// sni_scan.go — on-demand (IP, SNI) health scanner for the SNI tab.
//
// There is deliberately NO auto-discovery and NO background scan:
// the scanner runs only when the user presses "Scan all" (bug B7 was
// an undisablable auto-scan). Results persist into the curated
// settings.SNIIPs / settings.SNISNIs entries (LastOK/LastMs/LastAt/
// LastTLS). The scanner NEVER adds or removes entries (bug B6 was a
// scan that dropped its results on the floor; the mirror bug —
// inventing entries — is refused here too).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"sync"
	"time"
)

// SNIPairResult is one (IP, SNI) TLS probe outcome.
type SNIPairResult struct {
	IP         string `json:"ip"`
	SNI        string `json:"sni"`
	OK         bool   `json:"ok"`
	LatencyMs  int    `json:"latencyMs"`
	TLSVersion string `json:"tlsVersion,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SNIScanStatus is the live scanner state polled by the SNI tab.
type SNIScanStatus struct {
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Last    string `json:"last,omitempty"`
}

// sniScanState guards the one in-flight scan (a second ScanSNIPairs
// while one runs fails fast instead of piling up). running stays true
// until the scan's own cleanup: a cancel only asks the workers to stop,
// and they keep draining for as long as their handshakes take, so keying
// the guard (and the UI's progress readout) on cancel would let a second
// scan start on top of the first and double-count done/total.
var sniScanState = struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    int
	total   int
	last    string
}{}

// sniScanPortOverride is a test hook: 0 means the production CF
// HTTPS port 443; tests point it at a local httptest TLS listener.
var sniScanPortOverride int

// sniScanTestRootCAs is a test hook returning extra TLS roots: nil
// in production (system roots only); the local-TLS test supplies its
// httptest server's cert pool. Chain validation is ALWAYS on — there
// is no insecure skip.
var sniScanTestRootCAs = func() *x509.CertPool { return nil }

// scanPairPort resolves the probe port (production 443).
func scanPairPort() int {
	if sniScanPortOverride != 0 {
		return sniScanPortOverride
	}
	return 443
}

// probeOnePair runs one real TLS handshake against ip:port with
// ServerName sni and full certificate-chain validation. The TLS
// version reported comes from the negotiated connection state.
func probeOnePair(ctx context.Context, ip, sni string) SNIPairResult {
	res := SNIPairResult{IP: ip, SNI: sni}
	port := scanPairPort()
	start := time.Now()
	pairCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dialer := &net.Dialer{}
	raw, err := dialer.DialContext(pairCtx, "tcp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(8 * time.Second))
	tlsCfg := &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12}
	if pool := sniScanTestRootCAs(); pool != nil {
		tlsCfg.RootCAs = pool
	}
	conn := tls.Client(raw, tlsCfg)
	if err := conn.HandshakeContext(pairCtx); err != nil {
		res.Error = err.Error()
		return res
	}
	_ = conn.Close()
	res.OK = true
	res.LatencyMs = int(time.Since(start).Milliseconds())
	if res.LatencyMs < 1 {
		res.LatencyMs = 1
	}
	switch conn.ConnectionState().Version {
	case tls.VersionTLS13:
		res.TLSVersion = "1.3"
	case tls.VersionTLS12:
		res.TLSVersion = "1.2"
	default:
		res.TLSVersion = fmt.Sprintf("0x%04x", conn.ConnectionState().Version)
	}
	return res
}

// ScanSNIPairs probes every (ip, sni) combination with real TLS
// handshakes (bounded concurrency 8), then persists each outcome
// into the matching settings.SNIIPs / settings.SNISNIs entries.
// Unknown ips/snis (not in the curated lists) are still probed and
// returned — they just have nowhere to persist to.
func (a *App) ScanSNIPairs(ips []string, snis []string) ([]SNIPairResult, error) {
	if len(ips) == 0 || len(snis) == 0 {
		return nil, fmt.Errorf("nothing to scan: need at least one IP and one SNI")
	}
	sniScanState.mu.Lock()
	if sniScanState.running {
		sniScanState.mu.Unlock()
		return nil, fmt.Errorf("scan already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sniScanState.running = true
	sniScanState.cancel = cancel
	sniScanState.done = 0
	total := len(ips) * len(snis)
	sniScanState.total = total
	sniScanState.mu.Unlock()
	defer func() {
		sniScanState.mu.Lock()
		sniScanState.running = false
		sniScanState.cancel = nil
		sniScanState.mu.Unlock()
	}()

	type job struct{ ip, sni string }
	jobs := make(chan job)
	results := make(chan SNIPairResult, total)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := probeOnePair(ctx, j.ip, j.sni)
				results <- r
				sniScanState.mu.Lock()
				sniScanState.done++
				sniScanState.last = j.ip + " / " + j.sni
				sniScanState.mu.Unlock()
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, ip := range ips {
			for _, sni := range snis {
				select {
				case <-ctx.Done():
					return
				case jobs <- job{ip, sni}:
				}
			}
		}
	}()
	wg.Wait()
	close(results)
	out := make([]SNIPairResult, 0, total)
	byIP := map[string]SNIPairResult{}
	bySNI := map[string]SNIPairResult{}
	for r := range results {
		out = append(out, r)
		// Best result per endpoint/SNI, not last-write-wins: workers finish
		// in any order, so a failing pair could overwrite a passing one for
		// the same IP and the stamped verdict became random.
		keepBestSNIResult(byIP, r.IP, r)
		keepBestSNIResult(bySNI, r.SNI, r)
	}
	// Persist into the curated lists. NEVER add or remove entries:
	// only stamp the ones the user already has.
	now := time.Now().Format(time.RFC3339)
	if err := a.store.UpdateSNI(func(s *Settings) {
		for i := range s.SNIIPs {
			if r, ok := byIP[s.SNIIPs[i].IP]; ok {
				s.SNIIPs[i].LastOK = r.OK
				s.SNIIPs[i].LastMs = r.LatencyMs
				s.SNIIPs[i].LastAt = now
				s.SNIIPs[i].LastTLS = r.TLSVersion
			}
		}
		for i := range s.SNISNIs {
			if r, ok := bySNI[s.SNISNIs[i].Host]; ok {
				s.SNISNIs[i].LastOK = r.OK
				s.SNISNIs[i].LastMs = r.LatencyMs
				s.SNISNIs[i].LastAt = now
				s.SNISNIs[i].LastTLS = r.TLSVersion
			}
		}
	}); err != nil {
		a.emitLog("gui", "error", fmt.Sprintf("SNI scan: save results failed: %v", err))
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

// keepBestSNIResult stores the best result for one endpoint/SNI: any
// success beats a failure, and among equals the lowest positive latency
// wins.
func keepBestSNIResult(m map[string]SNIPairResult, key string, r SNIPairResult) {
	prev, ok := m[key]
	if !ok {
		m[key] = r
		return
	}
	if r.OK != prev.OK {
		if r.OK {
			m[key] = r
		}
		return
	}
	if r.LatencyMs > 0 && (prev.LatencyMs <= 0 || r.LatencyMs < prev.LatencyMs) {
		m[key] = r
	}
}

// ScanSNIStatus reports the live scanner progress for the SNI tab.
func (a *App) ScanSNIStatus() (SNIScanStatus, error) {
	sniScanState.mu.Lock()
	defer sniScanState.mu.Unlock()
	return SNIScanStatus{
		Running: sniScanState.running,
		Done:    sniScanState.done,
		Total:   sniScanState.total,
		Last:    sniScanState.last,
	}, nil
}

// CancelSNIScan aborts the in-flight scan, if any. Reports whether
// anything was actually cancelled.
func (a *App) CancelSNIScan() error {
	sniScanState.mu.Lock()
	defer sniScanState.mu.Unlock()
	if sniScanState.cancel == nil {
		return nil
	}
	// Signal only: running/cancel stay set until the scan's own deferred
	// cleanup, so the UI keeps reporting the drain and a second scan
	// cannot start while the first one's workers are still in flight.
	sniScanState.cancel()
	return nil
}
