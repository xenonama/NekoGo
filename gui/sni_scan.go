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
	"sort"
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
	// Warning is Plan A's non-fatal note: the handshake worked against an
	// address that is not a Cloudflare edge, so the engine dials the SNI's
	// own IP rather than riding a CF edge.
	Warning string `json:"warning,omitempty"`
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
	// The hello goes out split, exactly like the engine sends it: scanning
	// with a whole ClientHello measures the DPI's verdict, not the
	// endpoint's, and reports working edges as dead.
	conn := tls.Client(&splitHelloConn{Conn: raw}, tlsCfg)
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
	if spoofPlanA(a.store.GetSettings()) {
		// Plan A: the endpoint comes from the SNI itself, so the IP
		// argument has no meaning here (the tab sends none) and the scan
		// becomes resolve-then-dial.
		return a.scanPlanA(snis)
	}
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

// probePlanAPair is Plan A's probe of one SNI: resolve it over DoH, then
// run one real TLS handshake against the address that answer produced —
// precisely what the engine will dial on the next connect. A failed
// resolve is reported as the row's error with the handshake skipped, so
// the tab shows which half broke.
func probePlanAPair(ctx context.Context, sni string) SNIPairResult {
	res := SNIPairResult{SNI: sni}
	ip, err := resolveA(ctx, sni)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.IP = ip
	out := probeOnePair(ctx, ip, sni)
	if out.OK && !isCloudflareIP(ip) {
		out.Warning = fmt.Sprintf("resolved to %s, which is not a Cloudflare edge — the engine dials this address directly", ip)
	}
	return out
}

// scanPlanA probes every checked SNI the way Plan A will actually
// connect (resolve + handshake), one job per SNI, and stamps each entry
// with the outcome plus the address it resolved to. The IP pool is never
// read and never written: in Plan A the "IPs" card is a read-out of
// these stamps, and the curated Plan B pool stays exactly as it was.
func (a *App) scanPlanA(snis []string) ([]SNIPairResult, error) {
	clean := make([]string, 0, len(snis))
	seen := map[string]bool{}
	for _, s := range snis {
		host := normalizeFakeSNI(s)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		clean = append(clean, host)
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("nothing to scan: plan A needs at least one SNI")
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
	sniScanState.total = len(clean)
	sniScanState.last = ""
	sniScanState.mu.Unlock()
	defer func() {
		sniScanState.mu.Lock()
		sniScanState.running = false
		sniScanState.cancel = nil
		sniScanState.mu.Unlock()
	}()

	jobs := make(chan string)
	results := make(chan SNIPairResult, len(clean))
	var wg sync.WaitGroup
	workers := 8
	if len(clean) < workers {
		workers = len(clean)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for host := range jobs {
				r := probePlanAPair(ctx, host)
				results <- r
				sniScanState.mu.Lock()
				sniScanState.done++
				sniScanState.last = r.IP + " / " + host
				sniScanState.mu.Unlock()
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, host := range clean {
			select {
			case <-ctx.Done():
				return
			case jobs <- host:
			}
		}
	}()
	wg.Wait()
	close(results)
	out := make([]SNIPairResult, 0, len(clean))
	bySNI := map[string]SNIPairResult{}
	for r := range results {
		out = append(out, r)
		bySNI[r.SNI] = r
	}
	// Persist into the curated SNI list only. NEVER add or remove
	// entries, and leave SNIIPs untouched (Plan B's pool).
	now := time.Now().Format(time.RFC3339)
	if err := a.store.UpdateSNI(func(s *Settings) {
		for i := range s.SNISNIs {
			r, ok := bySNI[s.SNISNIs[i].Host]
			if !ok {
				continue
			}
			s.SNISNIs[i].LastOK = r.OK
			s.SNISNIs[i].LastMs = r.LatencyMs
			s.SNISNIs[i].LastAt = now
			s.SNISNIs[i].LastTLS = r.TLSVersion
			// An empty IP means the resolve failed: drop the previous
			// answer, it is no longer known-good.
			s.SNISNIs[i].LastIP = r.IP
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

// --- the scan rides the same shape the engine puts on the wire ----------
//
// A plain Go TLS handshake writes the ClientHello as ONE TCP segment. On a
// network whose DPI matches the hostname inside a single segment, that hello
// is killed for every blocked name — so the scanner was reporting live
// Cloudflare edges and valid SNIs as dead, and its verdicts could not be used
// to choose an endpoint. Measured on this gateway: y6-3cj.pages.dev through a
// working edge is 0/2 with a whole hello and 2/2 once the hello is split.
//
// These constants mirror core/src/fake_tcp.rs (MIN_SPLIT_LABEL,
// MAX_HELLO_CUTS) so the scan measures what hello_split actually sends.
const (
	minSplitLabel = 5
	maxHelloCuts  = 3
)

// splitHelloConn cuts the first ClientHello record into several TCP writes.
// Go's net.Conn already sets TCP_NODELAY, so each Write is its own segment
// and the server's stack reassembles the identical byte stream. Every other
// write — and every write after the hello — passes through untouched.
//
// Only a COPY of the hello is kept: crypto/tls hands Write its own reusable
// buffer, so retaining the pieces (which are views into it) would leave this
// struct holding bytes that change under it once the next record is written.
type splitHelloConn struct {
	net.Conn
	hello []byte
}

func (c *splitHelloConn) Write(b []byte) (int, error) {
	if c.hello == nil && isTLSClientHello(b) {
		c.hello = append([]byte(nil), b...)
		for _, p := range splitClientHello(b) {
			if _, err := c.Conn.Write(p); err != nil {
				return 0, err
			}
		}
		return len(b), nil
	}
	return c.Conn.Write(b)
}

// isTLSClientHello reports whether b is a handshake record carrying a
// ClientHello: content type 0x16, handshake type 0x01 at offset 5.
func isTLSClientHello(b []byte) bool {
	return len(b) > 6 && b[0] == 0x16 && b[1] == 0x03 && b[5] == 0x01
}

// sniHostSpan locates the hostname inside a ClientHello and returns its
// offset and length. Same walk as the engine's: extension type 0x0000, two
// length fields, a name-type byte, then the name.
func sniHostSpan(h []byte) (off, length int, ok bool) {
	if len(h) < 50 || h[0] != 0x16 {
		return 0, 0, false
	}
	end := len(h) - 10
	for i := 5; i < end && i+9 < len(h); i++ {
		if h[i] != 0 || h[i+1] != 0 {
			continue
		}
		extLen := int(h[i+2])<<8 | int(h[i+3])
		if extLen < 7 || extLen > 260 || i+4+extLen > len(h) {
			continue
		}
		if lstLen := int(h[i+4])<<8 | int(h[i+5]); lstLen != extLen-2 {
			continue
		}
		if h[i+6] != 0 {
			continue
		}
		nameLen := int(h[i+7])<<8 | int(h[i+8])
		if nameLen < 1 || nameLen > 253 || nameLen != extLen-5 {
			continue
		}
		return i + 9, nameLen, true
	}
	return 0, 0, false
}

// splitClientHello cuts h into contiguous pieces so that no piece holds a
// whole hostname label long enough to be a blocklist key. The cut is the
// label midpoint, not a random offset: this is a measurement tool, and a
// stable shape makes two scans of the same endpoint comparable. Lossless
// and in order. Returns h as a single piece when there is nothing to hide.
func splitClientHello(h []byte) [][]byte {
	off, n, ok := sniHostSpan(h)
	if !ok {
		return [][]byte{h}
	}
	host := h[off : off+n]
	type label struct{ at, size int }
	var labels []label
	for i := 0; i < len(host); {
		j := i
		for j < len(host) && host[j] != '.' {
			j++
		}
		if j-i >= minSplitLabel {
			labels = append(labels, label{off + i, j - i})
		}
		i = j + 1
	}
	sort.Slice(labels, func(a, b int) bool {
		if labels[a].size != labels[b].size {
			return labels[a].size > labels[b].size
		}
		return labels[a].at < labels[b].at
	})
	if len(labels) > maxHelloCuts {
		labels = labels[:maxHelloCuts]
	}
	var cuts []int
	for _, l := range labels {
		cuts = append(cuts, l.at+l.size/2)
	}
	sort.Ints(cuts)
	out := make([][]byte, 0, len(cuts)+1)
	prev := 0
	for _, c := range cuts {
		if c <= prev || c >= len(h) {
			continue
		}
		out = append(out, h[prev:c])
		prev = c
	}
	out = append(out, h[prev:])
	if len(out) < 2 {
		return [][]byte{h}
	}
	return out
}
