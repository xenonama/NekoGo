package main

// Aether endpoint scanning: the local mode of PattNG's Aether editor
// scan button. The candidate ranges live inside aether.exe, so NekoGo
// runs the core with the scan arguments and reads the endpoint it names
// in its log (PattNG's AetherScanner). Scan results persist in Settings
// so switching tabs or restarting the GUI never loses them.

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// aetherScanTimeout is PattNG's SCAN_TIMEOUT_MS.
const aetherScanTimeout = 8 * time.Minute

// AetherScanResult is the endpoint a scan found. One-hop profiles use
// Host and Port; two-hop profiles use Outer and Inner; WireGuard over
// MASQUE keeps the profile's own inner endpoint, as PattNG's
// applyScanResult does. The remaining fields are the persisted scan
// record the Aether tab lists across tab switches.
type AetherScanResult struct {
	Host          string     `json:"host,omitempty"`
	Port          int        `json:"port,omitempty"`
	Outer         string     `json:"outer,omitempty"`
	Inner         string     `json:"inner,omitempty"`
	Endpoint      string     `json:"endpoint"`
	Protocol      string     `json:"protocol"`
	Transport     string     `json:"transport"`
	LatencyMs     int        `json:"latencyMs"`
	TLSVersion    string     `json:"tlsVersion,omitempty"`
	LastScannedAt *time.Time `json:"lastScannedAt,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// AetherScanStatus is the live scan progress polled by the Aether tab.
type AetherScanStatus struct {
	Running     bool   `json:"running"`
	Done        int    `json:"done"`
	Total       int    `json:"total"`
	LastError   string `json:"lastError,omitempty"`
	LastErrorAt string `json:"lastErrorAt,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
}

// AetherConfig is the profile shape a saved scan result becomes.
type AetherConfig = Profile

// aetherScanRunner is replaced by tests with a fake scanner. Production
// runs the core through aetherScanRun.
var aetherScanRunner = func(a *App, ctx context.Context, p Profile) (*AetherScanResult, error) {
	return a.aetherScanRun(ctx, p)
}

var (
	aetherScanMasqueGateway  = regexp.MustCompile(`selected MASQUE gateway (\S+)`)
	aetherScanWireGuard      = regexp.MustCompile(`selected WireGuard endpoint (\S+)`)
	aetherScanGoolHops       = regexp.MustCompile(`using cloudflare edge (\S+) \(outer\) and (\S+) \(inner\)`)
	aetherScanMimHops        = regexp.MustCompile(`masque-in-masque ready: (\S+) \(outer\) and (\S+) \(inner\)`)
	aetherScanGoolOverMasque = regexp.MustCompile(`gool ready: masque (\S+) carries wireguard (\S+)`)
	aetherScanExitAccepted   = regexp.MustCompile(`exit location \S+ accepted`)
	aetherScanExitRejected   = regexp.MustCompile(`exit location \S+ rejected`)
)

// buildAetherScanArguments turns the connect arguments into the scan
// ones (AetherCoreManager.buildArguments' scan=true): the core looks
// for the endpoints afresh, so a saved --peer (one-hop or the outer hop
// of WireGuard over MASQUE) and saved two-hop outer/inner flags give
// way to the protocol's scan flag. The WireGuard endpoint inside a
// MASQUE tunnel stays the profile's. The reconnect cache is off for a
// scan, as PattNG does.
func buildAetherScanArguments(p *Profile, port int) []string {
	args := buildAetherArguments(p, port)
	protocol := aetherProtocolDefault(p.Aether.Protocol)
	scanFlag := ""
	if aetherIsTwoHops(protocol) {
		scanFlag = "--wiw-scan"
		if protocol == "mim" {
			scanFlag = "--mim-scan"
		}
	}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--peer":
			i++ // a scan selects the endpoint afresh
		case "--wiw-outer", "--wiw-inner", "--mim-outer", "--mim-inner":
			i++
			if scanFlag != "" && !containsStr(out, scanFlag) {
				out = append(out, scanFlag)
			}
		case "--quick-reconnect":
			out = append(out, "--no-quick-reconnect")
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// aetherScanParse reads one core log line as the endpoint for
// [protocol] (AetherScanner.parse).
func aetherScanParse(protocol, line string) *AetherScanResult {
	switch protocol {
	case "gool":
		return aetherScanHops(aetherScanGoolHops, line)
	case "mim":
		return aetherScanHops(aetherScanMimHops, line)
	case "masque":
		return aetherScanOne(aetherScanMasqueGateway, line)
	case "wg-over-masque":
		// The MASQUE gateway is the outer hop; the WireGuard endpoint
		// inside the tunnel stays the profile's (PattNG applyScanResult).
		result := aetherScanOne(aetherScanGoolOverMasque, line)
		if result != nil {
			result.Outer = aetherEndpointString(aetherEndpoint{host: result.Host, port: result.Port})
		}
		return result
	default:
		return aetherScanOne(aetherScanWireGuard, line)
	}
}

func aetherScanOne(re *regexp.Regexp, line string) *AetherScanResult {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	ep, ok := aetherEndpointParse(m[1])
	if !ok {
		return nil
	}
	return &AetherScanResult{Host: ep.host, Port: ep.port}
}

func aetherScanHops(re *regexp.Regexp, line string) *AetherScanResult {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	outer, ok := aetherEndpointParse(m[1])
	if !ok {
		return nil
	}
	inner, ok := aetherEndpointParse(m[2])
	if !ok {
		return nil
	}
	return &AetherScanResult{
		Host:  outer.host,
		Port:  outer.port,
		Outer: aetherEndpointString(outer),
		Inner: aetherEndpointString(inner),
	}
}

// aetherScanMatcher gates the endpoint line on the exit rule
// (AetherScanner.matcher): without a rule, the named endpoint ends the
// scan; with one, the core names an endpoint before it checks the exit
// behind it and looks again when the rule refuses it, so the last named
// endpoint counts only once the core accepts its exit. Masque-in-masque
// and WireGuard over MASQUE say they are ready after that check.
func aetherScanMatcher(protocol string, exitRuled bool) func(string) *AetherScanResult {
	if !exitRuled || protocol == "mim" || protocol == "wg-over-masque" {
		return func(line string) *AetherScanResult {
			return aetherScanParse(protocol, line)
		}
	}
	var named *AetherScanResult
	return func(line string) *AetherScanResult {
		if found := aetherScanParse(protocol, line); found != nil {
			named = found
			return nil
		}
		if aetherScanExitAccepted.MatchString(line) {
			return named
		}
		if aetherScanExitRejected.MatchString(line) {
			named = nil
		}
		return nil
	}
}

// aetherScanWriter relays the scan core's output to the log viewer and
// the matcher, line by line, from stdout and stderr alike.
type aetherScanWriter struct {
	mu      sync.Mutex
	pending string
	match   func(string) *AetherScanResult
	log     func(level, msg string)
	found   chan<- *AetherScanResult
}

func (w *aetherScanWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(strings.TrimSuffix(w.pending[:i], "\r"))
		w.pending = w.pending[i+1:]
		w.line(line)
	}
	return len(p), nil
}

func (w *aetherScanWriter) line(line string) {
	if line == "" {
		return
	}
	level, msg := aetherLinePriority(line)
	if w.log != nil {
		w.log(level, msg)
	}
	if result := w.match(line); result != nil {
		select {
		case w.found <- result:
		default:
		}
	}
}

// flush handles a final line the core wrote without a newline. The
// caller runs it after the process has been waited for.
func (w *aetherScanWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := strings.TrimSpace(strings.TrimSuffix(w.pending, "\r"))
	w.pending = ""
	w.line(line)
}

// aetherScanBegin registers the one in-flight scan and gives it a
// cancellable deadline. The caller must call aetherScanEnd.
func (a *App) aetherScanBegin(timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if a.aetherMgr == nil {
		return nil, nil, fmt.Errorf("aether manager is not configured")
	}
	if a.aetherMgr.Running() {
		return nil, nil, fmt.Errorf("disconnect the running Aether tunnel before scanning")
	}
	if timeout <= 0 {
		timeout = aetherScanTimeout
	}
	a.aetherScanMu.Lock()
	defer a.aetherScanMu.Unlock()
	if a.aetherScanCancel != nil {
		return nil, nil, fmt.Errorf("an Aether scan is already running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	a.aetherScanCancel = cancel
	return ctx, cancel, nil
}

func (a *App) aetherScanEnd(cancel context.CancelFunc) {
	cancel()
	a.aetherScanMu.Lock()
	a.aetherScanCancel = nil
	a.aetherScanMu.Unlock()
}

// AetherScan runs aether.exe once for [p] and returns the endpoint the
// core selected. It refuses while the Aether engine is up: a scan opens
// a second tunnel on the same WARP identity, which would disturb the
// live session (PattNG's AetherSession.disturbedByScanOf).
func (a *App) AetherScan(p Profile) (*AetherScanResult, error) {
	if p.Protocol != "aether" {
		return nil, fmt.Errorf("profile is not an Aether profile")
	}
	ctx, cancel, err := a.aetherScanBegin(aetherScanTimeout)
	if err != nil {
		return nil, err
	}
	defer a.aetherScanEnd(cancel)
	startedAt := time.Now().UTC().Format(time.RFC3339)
	a.setAetherScanStatus(AetherScanStatus{Running: true, Total: 1, StartedAt: startedAt})
	r, err := aetherScanRunner(a, ctx, p)
	status := AetherScanStatus{Running: false, Done: 1, Total: 1, StartedAt: startedAt}
	if err != nil {
		status.LastError = err.Error()
		status.LastErrorAt = time.Now().UTC().Format(time.RFC3339)
	}
	a.setAetherScanStatus(status)
	return r, err
}

// aetherScanRun runs the core once and returns the endpoint the core
// selected. The caller holds the one-scan guard through aetherScanBegin.
func (a *App) aetherScanRun(ctx context.Context, p Profile) (*AetherScanResult, error) {
	settings := a.store.GetSettings()
	exe, err := a.aetherMgr.resolveAetherExe(settings)
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}

	protocol := aetherProtocolDefault(p.Aether.Protocol)
	exitRuled := strings.TrimSpace(p.Aether.ExitLoc) != ""
	matcher := aetherScanMatcher(protocol, exitRuled)
	args := buildAetherScanArguments(&p, port)
	// The scan ends on an info line; a quiet app level would hide it
	// (PattNG forces the default level when readiness needs the word).
	args = append(args, "--log-level", "info")

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	workDir := aetherWorkDir()
	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.Dir = workDir
	cmd.Env = aetherEnv(workDir)
	cmd.WaitDelay = 5 * time.Second
	setHideWindow(cmd)

	found := make(chan *AetherScanResult, 1)
	writer := &aetherScanWriter{
		match: matcher,
		log:   func(level, msg string) { a.emitLog("aether-scan", level, msg) },
		found: found,
	}
	cmd.Stdout = writer
	cmd.Stderr = writer
	started := time.Now()
	// The job takes the scan's whole tree down: CommandContext's cancel
	// only kills aether.exe, and a scan draft that arms Psiphon leaves
	// pt\psiphon-tunnel-core.exe behind to keep serving.
	job := newProcJob()
	if err := cmd.Start(); err != nil {
		job.close()
		return nil, fmt.Errorf("start aether scan: %w", err)
	}
	job.attach(cmd.Process.Pid)
	stopJob := context.AfterFunc(runCtx, job.kill)
	defer stopJob()
	defer job.close()

	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		job.kill()
		done <- err
	}()

	result := func(r *AetherScanResult) (*AetherScanResult, error) {
		runCancel()
		<-done
		r = aetherScanResultPrepared(p, r, time.Since(started))
		a.emitLog("aether-scan", "info", "scan found "+aetherScanResultText(r))
		return r, nil
	}
	select {
	case r := <-found:
		return result(r)
	case <-done:
		// The process is gone: flush a last line written without a
		// newline, then take a result that raced the exit.
		writer.flush()
		select {
		case r := <-found:
			return result(r)
		default:
		}
		return nil, aetherScanContextError(ctx)
	case <-ctx.Done():
		<-done
		return nil, aetherScanContextError(ctx)
	}
}

func aetherScanContextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.Canceled:
		return fmt.Errorf("aether scan cancelled")
	case context.DeadlineExceeded:
		return fmt.Errorf("aether scan timed out")
	default:
		return fmt.Errorf("aether scan ended without an endpoint")
	}
}

// AetherScanEndpoints scans the active (or first) Aether profile until
// [count] endpoints were attempted or [timeoutMs] expires.
func (a *App) AetherScanEndpoints(count, timeoutMs int) ([]AetherScanResult, error) {
	return a.aetherScanEndpointsForProfile(a.aetherScanDefaultProfile(), count, timeoutMs)
}

// AetherScanProfileEndpoints scans [p] until [count] endpoints were
// attempted or [timeoutMs] expires. The Aether tab uses this so an
// unsaved draft can be scanned without overwriting the active profile.
func (a *App) AetherScanProfileEndpoints(p Profile, count, timeoutMs int) ([]AetherScanResult, error) {
	if p.Protocol != "aether" {
		return nil, fmt.Errorf("profile is not an Aether profile")
	}
	return a.aetherScanEndpointsForProfile(p, count, timeoutMs)
}

func (a *App) aetherScanDefaultProfile() Profile {
	if a.store != nil {
		settings := a.store.GetSettings()
		if p, ok := a.store.GetProfile(settings.ActiveProfileID); ok && p.Protocol == "aether" {
			return p
		}
		for _, p := range a.store.ListProfiles() {
			if p.Protocol == "aether" {
				return p
			}
		}
	}
	return Profile{Name: "Aether scan", Protocol: "aether", Aether: aetherSettings{Protocol: "wg"}}
}

func (a *App) aetherScanEndpointsForProfile(p Profile, count, timeoutMs int) ([]AetherScanResult, error) {
	if count < 1 {
		count = 1
	}
	timeout := aetherScanTimeout
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	ctx, cancel, err := a.aetherScanBegin(timeout)
	if err != nil {
		return nil, err
	}
	defer a.aetherScanEnd(cancel)

	startedAt := time.Now().UTC().Format(time.RFC3339)
	a.setAetherScanStatus(AetherScanStatus{Running: true, Total: count, StartedAt: startedAt})
	done := 0
	var lastErr error
	for done < count {
		if err := ctx.Err(); err != nil {
			lastErr = aetherScanContextError(ctx)
			break
		}
		scanStart := time.Now()
		r, err := aetherScanRunner(a, ctx, p)
		if r != nil {
			r = aetherScanResultPrepared(p, r, time.Since(scanStart))
			if err != nil {
				r.Error = err.Error()
			}
			if perr := a.aetherScanPersistResult(*r); perr != nil {
				lastErr = perr
				done++
				a.setAetherScanStatus(AetherScanStatus{Running: true, Done: done, Total: count, StartedAt: startedAt})
				break
			}
		}
		done++
		a.setAetherScanStatus(AetherScanStatus{Running: true, Done: done, Total: count, StartedAt: startedAt})
		if err != nil {
			lastErr = err
			break
		}
		if r == nil {
			lastErr = fmt.Errorf("aether scan ended without an endpoint")
			break
		}
	}

	status := AetherScanStatus{Running: false, Done: done, Total: count, StartedAt: startedAt}
	if lastErr != nil {
		status.LastError = lastErr.Error()
		status.LastErrorAt = time.Now().UTC().Format(time.RFC3339)
	}
	a.setAetherScanStatus(status)
	results, listErr := a.AetherListScanResults()
	if lastErr == nil {
		lastErr = listErr
	}
	return results, lastErr
}

func (a *App) setAetherScanStatus(s AetherScanStatus) {
	a.aetherScanStatusMu.Lock()
	a.aetherScanStatus = s
	a.aetherScanStatusMu.Unlock()
}

// AetherScanStatus reports the live scan progress for the Aether tab.
func (a *App) AetherScanStatus() (AetherScanStatus, error) {
	a.aetherScanStatusMu.Lock()
	defer a.aetherScanStatusMu.Unlock()
	return a.aetherScanStatus, nil
}

// AetherCancelScan aborts the scan in flight, if any.
func (a *App) AetherCancelScan() error {
	return a.AetherScanCancel()
}

// AetherScanCancel aborts the scan in flight, if any.
func (a *App) AetherScanCancel() error {
	a.aetherScanMu.Lock()
	cancel := a.aetherScanCancel
	a.aetherScanMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// AetherListScanResults returns the persisted scan results.
func (a *App) AetherListScanResults() ([]AetherScanResult, error) {
	if a.store == nil {
		return []AetherScanResult{}, nil
	}
	settings := a.store.GetSettings()
	out := make([]AetherScanResult, len(settings.AetherScanResults))
	copy(out, settings.AetherScanResults)
	return out, nil
}

// AetherClearScanResults drops every persisted scan result.
func (a *App) AetherClearScanResults() error {
	if a.store == nil {
		return nil
	}
	return a.store.UpdateAetherScanResults(func([]AetherScanResult) []AetherScanResult {
		return []AetherScanResult{}
	})
}

// AetherSaveScanResultAsConfig turns a persisted scan result into a new
// Aether profile. The result keeps the protocol/transport it was found
// with; one-hop results become address+port, two-hop results keep the
// outer/inner pair. [name] is the operator's typed name; when blank a
// label derived from the endpoint is used.
func (a *App) AetherSaveScanResultAsConfig(index int, name string) (AetherConfig, error) {
	results, err := a.AetherListScanResults()
	if err != nil {
		return AetherConfig{}, err
	}
	if index < 0 || index >= len(results) {
		return AetherConfig{}, fmt.Errorf("scan result %d not found (have %d)", index, len(results))
	}
	r := results[index]
	protocol := aetherEnumDefault(aetherProtocols, r.Protocol, "wg")
	label := strings.TrimSpace(r.Endpoint)
	if label == "" {
		label = aetherScanResultText(&r)
	}
	if label == "" {
		label = protocol
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Aether scan " + label
	}
	cfg := Profile{Name: name, Protocol: "aether", Core: CoreSingBox}
	cfg.Aether = aetherSettings{Protocol: protocol}
	if aetherIsOverMasque(protocol) {
		cfg.Aether.Transport = aetherEnumDefault(aetherTransports, r.Transport, "h3")
	}
	if aetherIsTwoHops(protocol) {
		outer := strings.TrimSpace(r.Outer)
		if outer == "" {
			outer = strings.TrimSpace(r.Endpoint)
		}
		cfg.Aether.WiwOuter = outer
		cfg.Aether.WiwInner = strings.TrimSpace(r.Inner)
	} else {
		host, port := r.Host, r.Port
		if host == "" || port <= 0 {
			if ep, ok := aetherEndpointParse(r.Endpoint); ok {
				host, port = ep.host, ep.port
			}
		}
		cfg.Address = host
		cfg.Port = port
	}
	saved, err := a.SaveProfile(cfg)
	if err != nil {
		return AetherConfig{}, err
	}
	return saved, nil
}

// AetherScanLogs returns the buffered Aether log lines newer than
// [sinceID]. The source prefix covers aether and aether-scan.
func (a *App) AetherScanLogs(sinceID uint64) ([]LogEntry, error) {
	a.logMu.RLock()
	defer a.logMu.RUnlock()
	out := make([]LogEntry, 0, 16)
	for _, e := range a.logBuf {
		if strings.HasPrefix(e.Source, "aether") && e.ID > sinceID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (a *App) aetherScanPersistResult(r AetherScanResult) error {
	return a.store.UpdateAetherScanResults(func(list []AetherScanResult) []AetherScanResult {
		return mergeAetherScanResult(list, r)
	})
}

func mergeAetherScanResult(list []AetherScanResult, r AetherScanResult) []AetherScanResult {
	key := aetherScanResultKey(r)
	for i := range list {
		if aetherScanResultKey(list[i]) == key {
			list[i] = r
			return list
		}
	}
	return append(list, r)
}

func aetherScanResultKey(r AetherScanResult) string {
	endpoint := strings.TrimSpace(r.Endpoint)
	if endpoint == "" {
		endpoint = aetherScanResultEndpoint(&r)
	}
	return endpoint + "\x00" + strings.ToLower(strings.TrimSpace(r.Protocol)) + "\x00" + strings.ToLower(strings.TrimSpace(r.Transport))
}

func aetherScanResultEndpoint(r *AetherScanResult) string {
	if r == nil {
		return ""
	}
	if r.Endpoint != "" {
		return r.Endpoint
	}
	if r.Outer != "" {
		return r.Outer
	}
	if r.Host != "" && r.Port > 0 {
		return aetherEndpointString(aetherEndpoint{host: r.Host, port: r.Port})
	}
	return r.Host
}

func aetherScanResultPrepared(p Profile, r *AetherScanResult, elapsed time.Duration) *AetherScanResult {
	if r == nil {
		return nil
	}
	protocol := aetherProtocolDefault(p.Aether.Protocol)
	if r.Protocol == "" {
		r.Protocol = protocol
	}
	if r.Transport == "" && aetherIsOverMasque(protocol) {
		r.Transport = aetherEnumDefault(aetherTransports, p.Aether.Transport, "h3")
	}
	if protocol == "wg-over-masque" && r.Inner == "" {
		r.Inner = strings.TrimSpace(p.Aether.WiwInner)
	}
	if r.Endpoint == "" {
		r.Endpoint = aetherScanResultEndpoint(r)
	}
	if r.LatencyMs == 0 {
		r.LatencyMs = int(elapsed.Milliseconds())
	}
	if r.LastScannedAt == nil {
		now := time.Now().UTC()
		r.LastScannedAt = &now
	}
	return r
}

func aetherScanResultText(r *AetherScanResult) string {
	if r == nil {
		return ""
	}
	if r.Outer != "" {
		if r.Inner != "" {
			return fmt.Sprintf("%s (outer), %s (inner)", r.Outer, r.Inner)
		}
		return r.Outer
	}
	if r.Endpoint != "" {
		return r.Endpoint
	}
	return aetherEndpointString(aetherEndpoint{host: r.Host, port: r.Port})
}
