package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/apache/thrift/lib/go/thrift"

	gen "nekogo/thriftgen"
)

// Advanced Mode Phase C: core network tools (Console -> Network tab).
//
// Model is SYNCHRONOUS: the LibcoreService Start RPCs (IPTest, Test,
// SpeedTest) block until done and return full results, so the bound
// methods block too and the UI awaits them. StopTest cancels the
// server's shared testCtx (core/server/test_utils.go: batch loops
// select on ctx.Done and abort with "test aborted"/Cancelled), which
// makes an in-flight blocking RPC return early — no polling needed.
// The Query* RPCs read the server reporters (draining, except
// QuerySpeedTest) and back the "last result" display without
// re-running anything.
//
// Concurrency rule: Phase C NEVER uses the manager's shared Thrift
// client. Long blocking calls would stall the 1s GetStats poll and,
// worse, two concurrent calls on one TSocket corrupt both streams
// (notably Start-then-Stop). Every Phase C RPC dials a dedicated
// throwaway transport that is closed on return; StopTest especially
// must not contend with the call it is trying to cancel.
//
// xray has no such RPCs (upstream Xray-core speaks only its stats
// API), so the xray legs are implemented client-side against the
// running core's local mixed proxy — the Phase 2 real-latency probe
// pattern. Endpoints mirror the core server where one exists
// (ip2location for IP).

// ipInfoAPI mirrors core/server/test_utils.go: the sing-box IP test
// endpoint, reused here so the xray client-side test resolves the
// same way. Responds {"ip","country_code"}.
const ipInfoAPI = "https://api.ip2location.io/"

// speedDownloadURL is the xray client-side speed download: Cloudflare's
// dedicated speed-test object (25 MB, designed for throughput tests).
const speedDownloadURLFull = "https://speed.cloudflare.com/__down?bytes=25000000"

// thriftConfLong sizes the socket for blocking speed tests (Ookla
// download+upload can run ~a minute); the shared 30s conf would kill
// the read mid-test and trigger a bogus reconnect+retry.
var thriftConfLong = &thrift.TConfiguration{
	ConnectTimeout: 5 * time.Second,
	SocketTimeout:  150 * time.Second,
	MaxMessageSize: 1024 * 1024 * 50,
}

func dialRPCLong(addr string) (thrift.TTransport, *gen.LibcoreServiceClient, error) {
	trans := thrift.NewTSocketConf(addr, thriftConfLong)
	if trans == nil {
		return nil, nil, fmt.Errorf("create thrift socket for %s", addr)
	}
	if err := trans.Open(); err != nil {
		return nil, nil, fmt.Errorf("open thrift transport: %w", err)
	}
	return trans, gen.NewLibcoreServiceClientFactory(trans, thriftProtoFact), nil
}

// sbTestDial opens a dedicated transport to the RUNNING sing-box core.
// Every Phase C call gets its own socket (see the concurrency rule above).
func (m *SingBoxManager) sbTestDial(long bool) (*gen.LibcoreServiceClient, func(), error) {
	m.mu.Lock()
	addr, running := m.addr, m.running
	m.mu.Unlock()
	if !running || addr == "" {
		return nil, nil, fmt.Errorf("core not running")
	}
	var trans thrift.TTransport
	var client *gen.LibcoreServiceClient
	var err error
	if long {
		trans, client, err = dialRPCLong(addr)
	} else {
		trans, client, err = dialRPCAddr(addr)
	}
	if err != nil {
		return nil, nil, err
	}
	return client, func() { trans.Close() }, nil
}

// parseBrateMbps parses the core's BrateToStr output ("12.34Mbps",
// "5.67Kbps", "1.23Gbps" — no space) back into Mbit/s. Unknown shapes
// yield 0 rather than an error: a missing rate must not fail the card.
func parseBrateMbps(s string) float64 {
	s = strings.TrimSpace(s)
	mult := 0.0
	switch {
	case strings.HasSuffix(s, "Gbps"):
		mult, s = 1000, strings.TrimSpace(strings.TrimSuffix(s, "Gbps"))
	case strings.HasSuffix(s, "Mbps"):
		mult, s = 1, strings.TrimSpace(strings.TrimSuffix(s, "Mbps"))
	case strings.HasSuffix(s, "Kbps"):
		mult, s = 1.0/1000, strings.TrimSpace(strings.TrimSuffix(s, "Kbps"))
	default:
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f * mult
}

func mapIPRows(rows []*gen.IPTestResp) []IPTestResult {
	out := make([]IPTestResult, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		out = append(out, IPTestResult{
			OutboundTag: r.GetOutboundTag(),
			IP:          r.GetIP(),
			CC:          strings.ToUpper(strings.TrimSpace(r.GetCountryCode())),
			Error:       r.GetError(),
		})
	}
	return out
}

func mapSpeedRow(r *gen.SpeedTestResult_) *SpeedTestResult {
	if r == nil {
		return nil
	}
	return &SpeedTestResult{
		DownloadMbps:  parseBrateMbps(r.GetDlSpeed()),
		UploadMbps:    parseBrateMbps(r.GetUlSpeed()),
		LatencyMs:     int(r.GetLatency()),
		ServerName:    r.GetServerName(),
		ServerCountry: r.GetServerCountry(),
		OutboundTag:   r.GetOutboundTag(),
		Cancelled:     r.GetCancelled(),
		Error:         r.GetError(),
	}
}

// IPTest runs the egress-IP probe. The server RPC always builds an
// ephemeral instance from the given config (it has no TestCurrent
// mode), so the caller passes the active profile's config.
func (m *SingBoxManager) IPTest(profile *Profile, settings Settings) (*IPTestResult, error) {
	cfg, err := BuildSingBoxConfig(profile, settings)
	if err != nil {
		return nil, err
	}
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.IPTest(ctx, &gen.IPTestRequest{
		Config:             cfg,
		UseDefaultOutbound: true,
		TestTimeoutMs:      8000,
	})
	if err != nil {
		return nil, err
	}
	rows := mapIPRows(resp.GetResults())
	if len(rows) == 0 {
		return nil, fmt.Errorf("no IP test result")
	}
	return &rows[0], nil
}

// QueryIPTest reads the reporter's last results without re-running.
func (m *SingBoxManager) QueryIPTest() ([]IPTestResult, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.QueryIPTest(ctx, &gen.EmptyReq{})
	if err != nil {
		return nil, err
	}
	return mapIPRows(resp.GetResults()), nil
}

// SpeedTest runs a full download+upload measurement against the
// RUNNING instance (TestCurrent). Blocking; StopTest aborts it.
func (m *SingBoxManager) SpeedTest() (*SpeedTestResult, error) {
	client, done, err := m.sbTestDial(true)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	resp, err := client.SpeedTest(ctx, &gen.SpeedTestRequest{
		TestCurrent:  true,
		TestDownload: true,
		TestUpload:   true,
		TimeoutMs:    45000,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range resp.GetResults() {
		if out := mapSpeedRow(r); out != nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("no speed test result")
}

// QuerySpeedTest reads the last speed result + running flag (non-draining).
func (m *SingBoxManager) QuerySpeedTest() (*SpeedTestResult, bool, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, false, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.QuerySpeedTest(ctx, &gen.EmptyReq{})
	if err != nil {
		return nil, false, err
	}
	out := mapSpeedRow(resp.GetResult_())
	if out != nil && out.OutboundTag == "" && out.ServerName == "" && out.Error == "" &&
		out.DownloadMbps == 0 && out.UploadMbps == 0 && out.LatencyMs == 0 {
		return nil, resp.GetIsRunning(), nil
	}
	return out, resp.GetIsRunning(), nil
}

// QueryCountryTest reads country-scan rows (populated only by
// country-mode runs; normally empty — not an error).
func (m *SingBoxManager) QueryCountryTest() ([]SpeedTestResult, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.QueryCountryTest(ctx, &gen.EmptyReq{})
	if err != nil {
		return nil, err
	}
	var out []SpeedTestResult
	for _, r := range resp.GetResults() {
		if mapped := mapSpeedRow(r); mapped != nil {
			out = append(out, *mapped)
		}
	}
	return out, nil
}

// ProbeURL runs one Test-RPC probe against the RUNNING instance and
// records the URL for later reporter reads. Dedicated transport (the
// shared URLTest has identical logic on the shared client).
func (m *SingBoxManager) ProbeURL(url string, timeoutMs int32) (*TestResult, error) {
	m.mu.Lock()
	running := m.running
	hasClient := m.client != nil
	m.mu.Unlock()
	if !running || !hasClient {
		return nil, fmt.Errorf("core not running")
	}
	if url == "" {
		url = defaultTestURL
	}
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	// BUG-020: clamp before any arithmetic and compute in int64 —
	// timeoutMs+15000 wraps in 32-bit arithmetic near MaxInt32.
	timeoutMs = clampProbeTimeoutMs(timeoutMs)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(int64(timeoutMs)+15000)*time.Millisecond)
	defer cancel()
	tResp, err := client.Test(ctx, &gen.TestReq{
		TestCurrent:   true,
		URL:           url,
		TestTimeoutMs: timeoutMs,
	})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.lastProbeURL = url
	m.mu.Unlock()
	if len(tResp.GetResults()) == 0 {
		return nil, fmt.Errorf("no test result")
	}
	r := tResp.GetResults()[0]
	return &TestResult{OutboundTag: r.GetOutboundTag(), LatencyMs: r.GetLatencyMs(), Error: r.GetError()}, nil
}

// QueryURLTest reads the reporter's last URL rows, stamped with the
// last-probed URL (the wire shape carries no URL).
func (m *SingBoxManager) QueryURLTest() ([]URLTestResult, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.QueryURLTest(ctx, &gen.EmptyReq{})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	lastURL := m.lastProbeURL
	m.mu.Unlock()
	var out []URLTestResult
	for _, r := range resp.GetResults() {
		if r == nil {
			continue
		}
		out = append(out, URLTestResult{
			URL:       lastURL,
			LatencyMs: int(r.GetLatencyMs()),
			OK:        r.GetError() == "",
			Error:     r.GetError(),
		})
	}
	return out, nil
}

// StopTest cancels in-flight probes server-side (shared testCtx) over
// a FRESH transport — never the shared client, which may be blocked
// inside the very call being cancelled.
func (m *SingBoxManager) StopTest() error {
	m.mu.Lock()
	addr, running := m.addr, m.running
	m.mu.Unlock()
	if !running || addr == "" {
		return fmt.Errorf("core not running")
	}
	trans, client, err := dialRPCAddr(addr)
	if err != nil {
		return err
	}
	defer trans.Close()
	// Integration BUG-004: a 10s stop budget always lost to the 150s
	// SpeedTest socket (plus the 45s server timeout), leaving a stuck
	// spinner under a misleading stop error. 30s covers abort round-trips
	// while the server batch loops select on the cancelled scope.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.StopTest(ctx, &gen.EmptyReq{})
	return err
}

// --- xray client-side legs (no such RPCs upstream) ---

// proxiedGetBodyCtx GETs rawURL through the HTTP proxy at proxyAddr,
// honouring ctx (so StopTest aborts it). Used by the xray legs.
func proxiedGetBodyCtx(ctx context.Context, proxyAddr, rawURL string, timeout time.Duration) ([]byte, error) {
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr})}
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GET %s: status %s", rawURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// xrayTestCtx opens a cancellable scope for one client-side probe,
// aborting any previous one (the UI runs at most one test at a time).
func (m *XrayManager) xrayTestCtx() (context.Context, context.CancelFunc) {
	m.testMu.Lock()
	defer m.testMu.Unlock()
	if m.testCancel != nil {
		m.testCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.testCancel = cancel
	return ctx, cancel
}

// xrayProxyAddr resolves the running core's mixed proxy address.
func (m *XrayManager) xrayProxyAddr() (string, error) {
	m.mu.Lock()
	running, mixedPort := m.running, m.mixedPort
	m.mu.Unlock()
	if !running {
		return "", fmt.Errorf("xray not running")
	}
	return fmt.Sprintf("127.0.0.1:%d", effectiveMixedPort(mixedPort)), nil
}

// IPTest resolves the egress IP through the running xray's proxy,
// hitting the same ip2location endpoint the sing-box RPC uses.
func (m *XrayManager) IPTest() (*IPTestResult, error) {
	proxy, err := m.xrayProxyAddr()
	if err != nil {
		return nil, err
	}
	ctx, cancel := m.xrayTestCtx()
	defer cancel()
	body, err := proxiedGetBodyCtx(ctx, proxy, ipInfoAPI, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var info struct {
		IP          string `json:"ip"`
		CountryCode string `json:"country_code"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("parse IP response: %w", err)
	}
	if info.IP == "" {
		return nil, fmt.Errorf("empty IP response")
	}
	out := &IPTestResult{
		OutboundTag: "proxy",
		IP:          strings.TrimSpace(info.IP),
		CC:          strings.ToUpper(strings.TrimSpace(info.CountryCode)),
	}
	m.testMu.Lock()
	m.lastIP = out
	m.testMu.Unlock()
	return out, nil
}

// SpeedTest measures a download through the running xray's proxy plus
// a latency companion probe. Upload has no client-side mirror and is
// omitted (omitempty).
func (m *XrayManager) SpeedTest() (*SpeedTestResult, error) {
	proxy, err := m.xrayProxyAddr()
	if err != nil {
		return nil, err
	}
	ctx, cancel := m.xrayTestCtx()
	defer cancel()
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxy})}
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	dlCtx, dlCancel := context.WithTimeout(ctx, 60*time.Second)
	defer dlCancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, speedDownloadURLFull, nil)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	n, copyErr := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GET %s: status %s", speedDownloadURLFull, resp.Status)
	}
	elapsed := time.Since(start).Seconds()
	var mbps float64
	if elapsed > 0 {
		mbps = float64(n) * 8 / elapsed / 1e6
	}
	latMs := 0
	// BUG-031: the latency leg runs under the probe ctx, so StopTest
	// aborts it instead of blocking up to 10s past the cancel.
	if d, err := proxiedHeadOrGetCtx(ctx, proxy, defaultTestURL, 10*time.Second); err == nil {
		latMs = int(d.Milliseconds())
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := &SpeedTestResult{
		DownloadMbps: mbps,
		LatencyMs:    latMs,
		ServerName:   "Cloudflare",
		OutboundTag:  "proxy",
	}
	m.testMu.Lock()
	m.lastSpeed = out
	m.testMu.Unlock()
	return out, nil
}

// StopTest aborts the in-flight client-side probe, if any.
func (m *XrayManager) StopTest() {
	m.testMu.Lock()
	defer m.testMu.Unlock()
	if m.testCancel != nil {
		m.testCancel()
		m.testCancel = nil
	}
}

// --- CoreManager routing ---

// netTarget snapshots the active core; all Phase C tools require a
// running connection (they probe the ACTIVE core, never ephemeral).
func (m *CoreManager) netTarget() (CoreType, error) {
	m.mu.RLock()
	core, running := m.effectiveCoreLocked(), m.running
	m.mu.RUnlock()
	if !running {
		return core, fmt.Errorf("no active connection")
	}
	return core, nil
}

func (m *CoreManager) netManagers() (*SingBoxManager, *XrayManager, error) {
	if m.singboxMgr == nil || m.xrayMgr == nil {
		return nil, nil, fmt.Errorf("core managers not initialized")
	}
	return m.singboxMgr, m.xrayMgr, nil
}

// IPTest probes the egress IP via the active core.
func (m *CoreManager) IPTest(profile *Profile, settings Settings) (*IPTestResult, error) {
	core, err := m.netTarget()
	if err != nil {
		return nil, err
	}
	sb, xr, err := m.netManagers()
	if err != nil {
		return nil, err
	}
	if core == CoreXray {
		return xr.IPTest()
	}
	if profile == nil {
		return nil, fmt.Errorf("nil profile")
	}
	return sb.IPTest(profile, settings)
}

// SpeedTest measures throughput via the active core.
func (m *CoreManager) SpeedTest() (*SpeedTestResult, error) {
	core, err := m.netTarget()
	if err != nil {
		return nil, err
	}
	sb, xr, err := m.netManagers()
	if err != nil {
		return nil, err
	}
	if core == CoreXray {
		return xr.SpeedTest()
	}
	return sb.SpeedTest()
}

// URLTest probes url through the active core and reports latency.
func (m *CoreManager) URLTest(url string, timeoutMs int) (*URLTestResult, error) {
	core, err := m.netTarget()
	if err != nil {
		return nil, err
	}
	sb, xr, err := m.netManagers()
	if err != nil {
		return nil, err
	}
	if url == "" {
		url = defaultTestURL
	}
	if timeoutMs <= 0 {
		timeoutMs = 10000
	}
	if core == CoreXray {
		r, err := xr.URLTest(url, int32(timeoutMs))
		if err != nil {
			return nil, err
		}
		out := &URLTestResult{URL: url, LatencyMs: int(r.LatencyMs), OK: r.Error == "", Error: r.Error}
		xr.testMu.Lock()
		xr.lastURL = out
		xr.testMu.Unlock()
		return out, nil
	}
	r, err := sb.ProbeURL(url, int32(timeoutMs))
	if err != nil {
		return nil, err
	}
	return &URLTestResult{URL: url, LatencyMs: int(r.LatencyMs), OK: r.Error == "", Error: r.Error}, nil
}

// StopTest cancels the in-flight probe (server testCtx for sing-box,
// local probe ctx for xray).
func (m *CoreManager) StopTest() error {
	core, err := m.netTarget()
	if err != nil {
		return err
	}
	sb, xr, err := m.netManagers()
	if err != nil {
		return err
	}
	if core == CoreXray {
		xr.StopTest()
		return nil
	}
	return sb.StopTest()
}

// LastNetwork returns the last-known tool results without running
// anything: sing-box reporter reads (IP/URL/country drain on read;
// speed does not) or the xray in-memory cache. Nil sections = no data.
//
// HOTFIX: this is a best-effort seeding call made by the Console Network
// tab on mount, so it must never panic and never return nil: idle (or a
// core that stopped mid-read) yields an empty struct, and it never
// touches the shared Thrift client — sing-box reporter reads each dial a
// dedicated throwaway transport (sbTestDial), never m.trans, so there is
// no contention with the 1s GetStats poll.
func (m *CoreManager) LastNetwork() (*LastNetworkResults, error) {
	if m == nil {
		return &LastNetworkResults{}, fmt.Errorf("core manager is nil")
	}
	m.mu.RLock()
	running := m.running
	sb, xr := m.singboxMgr, m.xrayMgr
	m.mu.RUnlock()
	if !running {
		return &LastNetworkResults{}, nil
	}
	if sb == nil || xr == nil {
		return &LastNetworkResults{}, fmt.Errorf("core managers not initialized")
	}
	core, err := m.netTarget()
	if err != nil {
		// Stopped between the snapshot above and now: same as no data.
		return &LastNetworkResults{}, nil
	}
	out := &LastNetworkResults{}
	if core == CoreXray {
		xr.testMu.Lock()
		defer xr.testMu.Unlock()
		out.IP, out.Speed, out.URL = xr.lastIP, xr.lastSpeed, xr.lastURL
		return out, nil
	}
	if rows, err := sb.QueryIPTest(); err == nil && len(rows) > 0 {
		r := rows[0]
		out.IP = &r
	}
	if sp, running, err := sb.QuerySpeedTest(); err == nil {
		out.Speed, out.SpeedRunning = sp, running
	}
	if rows, err := sb.QueryURLTest(); err == nil && len(rows) > 0 {
		r := rows[0]
		out.URL = &r
	}
	if rows, err := sb.QueryCountryTest(); err == nil && len(rows) > 0 {
		out.Country = rows
	}
	return out, nil
}
