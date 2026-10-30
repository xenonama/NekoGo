package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// xrayAPIPortDefault is the 127.0.0.1 port of our generated API inbound.
const xrayAPIDefaultPort = 10085

// XrayManager generates an Xray config.json from a Profile, runs xray.exe
// as a hidden child process, and polls its gRPC StatsService for traffic.
type XrayManager struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	profile *Profile
	apiAddr string
	// mixedPort is the http-in port bound by Start; the running-core
	// URL probe goes through it.
	mixedPort int
	running   bool
	// stopRequested marks an in-flight Stop: the exit it causes is a
	// clean stop, not an EngineExit.
	stopRequested bool
	// statsClient is the reused gRPC connection to the core's API
	// (the stats poll runs every second; one connection per session,
	// not one per poll). Closed on Stop.
	statsClient *XrayStatsClient

	// pathMu guards xrayPath alone (BUG-022): see SingBoxManager.pathMu.
	pathMu   sync.RWMutex
	xrayPath string
	workDir  string
	logCh    chan LogEntry
	// recent keeps the child's last output lines for an EngineExit.
	recent      lineRing
	startedAt   time.Time
	exitHandler func(EngineExit)
	// Ephemeral test instance: separate process/config so a latency
	// probe never disturbs the active connection above.
	ephCmd     *exec.Cmd
	ephDir     string
	ephRunning bool
	// startEpoch/ephEpoch invalidate in-flight Start/EphemeralRun probes
	// (BUG-004): Stop (or a superseding Start) bumps the counter, and
	// the probe commits only on an unchanged epoch. Guarded by mu.
	startEpoch uint64
	ephEpoch   uint64
	// Phase C client-side probe state (xray has no test RPCs): testMu
	// guards a cancel for the in-flight probe plus cached last
	// results backing LastNetwork (the reporter equivalent).
	testMu     sync.Mutex
	testCancel context.CancelFunc
	lastIP     *IPTestResult
	lastSpeed  *SpeedTestResult
	lastURL    *URLTestResult
}

// NewXrayManager creates a manager. xrayPath may be empty (auto-detect).
// workDir holds generated config.json; empty means OS temp dir.
func NewXrayManager(xrayPath, workDir string, logCh chan LogEntry) *XrayManager {
	return &XrayManager{xrayPath: xrayPath, workDir: workDir, logCh: logCh}
}

// SetExitHandler registers the cross-engine death report callback
// (CoreManager.handleEngineExit). Called once at wiring time.
func (m *XrayManager) SetExitHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exitHandler = fn
}

func (m *XrayManager) log(level, msg string) {
	if m.logCh == nil {
		return
	}
	// Best-effort (see SingBoxManager.log): call sites hold m.mu, so a
	// full channel must not wedge the manager.
	select {
	case m.logCh <- LogEntry{Level: level, Source: "xray", Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
	}
}

// getXrayPath reads the settings xrayPath override under pathMu.
func (m *XrayManager) getXrayPath() string {
	m.pathMu.RLock()
	defer m.pathMu.RUnlock()
	return m.xrayPath
}

// setXrayPath swaps the settings xrayPath override under pathMu.
func (m *XrayManager) setXrayPath(p string) {
	m.pathMu.Lock()
	defer m.pathMu.Unlock()
	m.xrayPath = p
}

func (m *XrayManager) resolveXray() (string, error) {
	if p := m.getXrayPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("xray not found at %q", p)
	}
	if c, ok := firstEngineFile("xray.exe", "xray"); ok {
		return c, nil
	}
	if p, err := exec.LookPath("xray"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("xray.exe"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("xray executable not found (place it in bin\\ next to nekogo.exe or set xrayPath in settings)")
}

// checkPortFree bind-tests one TCP address and releases it immediately.
// An occupied port yields the zombie-hint error (HOTFIX Bug 2); any other
// failure is returned verbatim. Residual TOCTOU: another process may grab
// the port between Close and xray's bind — the startup probe still catches
// that case honestly.
func checkPortFree(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if errors.Is(err, syscall.EADDRINUSE) ||
			strings.Contains(msg, "in use") ||
			strings.Contains(msg, "only one usage") ||
			strings.Contains(msg, "already in use") {
			_, port, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				port = addr
			}
			return fmt.Errorf("port %s in use — is another xray still running? Kill it or pick a different port", port)
		}
		return fmt.Errorf("pre-flight listen %s: %w", addr, err)
	}
	_ = l.Close()
	return nil
}

// startAborted reports whether the given Start epoch was invalidated by
// a concurrent Stop (or a superseding Start). Callers must NOT hold m.mu.
func (m *XrayManager) startAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startEpoch != epoch
}

// Start writes config.json and launches xray.exe -c config.json.
//
// Lifecycle (BUG-004): only fast setup runs under m.mu. The 5s API
// probe runs UNLOCKED so Stop/IsRunning stay responsive; the running
// state commits under lock only on an unchanged epoch, otherwise the
// child is torn down and Start reports an abort.
func (m *XrayManager) Start(profile *Profile, settings Settings) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("xray already running")
	}
	m.stopRequested = false
	apiAddr := settings.XrayAPIAddress
	if apiAddr == "" {
		apiAddr = fmt.Sprintf("127.0.0.1:%d", xrayAPIDefaultPort)
	}
	mixedPort := effectiveMixedPort(settings.MixedPort)
	// HOTFIX (zombie xray): a crashed session leaves xray.exe holding
	// these ports, so the next Connect would fail 5s later with a bare
	// bind error. Fail fast with a clear message instead. Never silently
	// pick another port: the UI documents these exact ports.
	preflight := []string{fmt.Sprintf("127.0.0.1:%d", mixedPort)}
	if mixedPort+1 <= 65535 {
		preflight = append(preflight, fmt.Sprintf("127.0.0.1:%d", mixedPort+1))
	}
	if apiHost, apiPort := splitAPIAddr(apiAddr); apiHost == "127.0.0.1" ||
		apiHost == "0.0.0.0" || apiHost == "::" || apiHost == "localhost" {
		preflight = append(preflight, fmt.Sprintf("127.0.0.1:%d", apiPort))
	}
	for _, ap := range preflight {
		if err := checkPortFree(ap); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	bin, err := m.resolveXray()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	cfg, err := BuildXrayConfig(profile, settings, apiAddr)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	dir := m.workDir
	createdDir := false
	if dir == "" {
		dir, err = os.MkdirTemp("", "nekobox-xray")
		if err != nil {
			m.mu.Unlock()
			return err
		}
		_ = os.Chmod(dir, 0o700)
		createdDir = true
		// NOTE: m.workDir is assigned only on commit below. An
		// in-flight Start must not publish its temp dir: a concurrent
		// Stop or a superseding Start would otherwise reuse/remove a
		// directory it does not own.
	}
	cfgPath, err := writeXrayConfig(dir, []byte(cfg))
	if err != nil {
		m.mu.Unlock()
		if createdDir {
			_ = os.RemoveAll(dir)
		}
		return err
	}
	cmd := exec.Command(bin, "-c", cfgPath)
	cmd.Dir = dir
	setHideWindow(cmd)
	cmd.Env = childEnvWithLogLevel(os.Environ(), settings.LogLevel, func(msg string) {
		m.log("warn", msg)
	})
	cmd.Stdout = &logWriter{ch: m.logCh, source: "xray", ring: &m.recent}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "xray", ring: &m.recent}
	m.recent.reset()
	m.startedAt = time.Now()
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		if createdDir {
			_ = os.RemoveAll(dir)
		}
		return fmt.Errorf("start xray: %w", err)
	}
	m.startEpoch++
	epoch := m.startEpoch
	// watchChild owns Wait() from here on; Start/Stop must never Wait
	// on this cmd again. The exited channel turns an instant crash
	// (bad config, port collision) into a fast, honest error instead
	// of a fake successful connect.
	exited := make(chan error, 1)
	go m.watchChild(cmd, exited)
	m.mu.Unlock()

	// Probe the API port for up to 5 seconds (no lock held): a live API
	// proves the child survived startup, the exited channel proves it
	// died, and an epoch change proves a Stop wants us gone.
	abortErr := fmt.Errorf("xray start aborted (concurrent stop)")
	abort := func() error {
		_ = cmd.Process.Kill()
		if createdDir {
			_ = os.RemoveAll(dir)
		}
		return abortErr
	}
	apiUp := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.startAborted(epoch) {
			return abort()
		}
		select {
		case err := <-exited:
			_ = cmd.Process.Kill()
			if createdDir {
				_ = os.RemoveAll(dir)
			}
			if m.startAborted(epoch) {
				return abortErr
			}
			return fmt.Errorf("xray exited during startup: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		conn, err := net.DialTimeout("tcp", apiAddr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			apiUp = true
			break
		}
	}
	if !apiUp {
		// The child may still be alive (e.g. xray retries a failed
		// inbound instead of exiting) — signal it; watchChild reaps.
		_ = cmd.Process.Kill()
		if createdDir {
			_ = os.RemoveAll(dir)
		}
		if m.startAborted(epoch) {
			return abortErr
		}
		return fmt.Errorf("xray API not reachable at %s (bad config?)", apiAddr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startEpoch != epoch {
		// Stop ran during our probe: tear down and refuse to commit a
		// stale running state. Only remove the temp dir we created.
		_ = cmd.Process.Kill()
		if createdDir {
			_ = os.RemoveAll(dir)
		}
		return abortErr
	}
	m.cmd = cmd
	m.profile = profile
	m.apiAddr = apiAddr
	m.workDir = dir
	m.mixedPort = effectiveMixedPort(settings.MixedPort)
	m.running = true
	// One gRPC connection for the whole session (the stats poll reuses
	// it instead of dialing a new one every second).
	m.statsClient = NewXrayStatsClient(apiAddr)
	m.log("info", fmt.Sprintf("xray started (profile %q, api %s)", profile.Name, apiAddr))
	return nil
}

// watchChild reaps the child and flips running=false on unexpected death
// so GetStats/IsRunning report truthfully. It owns cmd.Wait(): no other
// path may Wait on a watched cmd. The (buffered) exited channel, when
// non-nil, receives the Wait result for the startup probe.
func (m *XrayManager) watchChild(cmd *exec.Cmd, exited chan<- error) {
	err := cmd.Wait()
	// Signal BEFORE taking the mutex: the startup probe holds m.mu
	// while waiting on this channel, so locking first would deadlock
	// the early-death report into the full 5s timeout.
	if exited != nil {
		select {
		case exited <- err:
		default:
		}
	}
	m.mu.Lock()
	current := m.cmd == cmd && m.running && !m.stopRequested
	if m.cmd == cmd {
		m.cmd = nil
		m.running = false
	}
	m.stopRequested = false
	handler := m.exitHandler
	started := m.startedAt
	m.mu.Unlock()
	if !current {
		m.log("info", "xray stopped")
		return
	}
	ev := newEngineExit("xray", cmd, err, started, m.recent.tail(engineLastLines))
	m.log("warn", ev.Summary())
	if handler != nil {
		handler(ev)
	}
}

// Stop kills the xray process and deletes its temp config dir.
// watchChild owns Wait(); Stop only signals and waits for the reaping.
// It bumps startEpoch first so an in-flight Start probe aborts instead
// of committing behind us (BUG-004).
func (m *XrayManager) Stop() error {
	m.mu.Lock()
	m.startEpoch++
	m.stopRequested = true
	cmd := m.cmd
	statsClient := m.statsClient
	m.statsClient = nil
	m.mu.Unlock()
	if statsClient != nil {
		statsClient.Close()
	}
	if cmd == nil || cmd.Process == nil {
		m.mu.Lock()
		m.running = false
		workDir := m.workDir
		m.workDir = ""
		m.mu.Unlock()
		if workDir != "" {
			_ = os.RemoveAll(workDir)
		}
		return nil
	}
	_ = cmd.Process.Kill()
	// Give watchChild up to 2s to observe the exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	m.mu.Lock()
	m.running = false
	workDir := m.workDir
	m.workDir = ""
	m.mu.Unlock()
	if workDir != "" {
		_ = os.RemoveAll(workDir)
	}
	m.log("info", "xray stopped")
	return nil
}

// writeXrayConfig writes cfg to dir/config.json with 0600 permissions:
// the file embeds UUIDs, passwords and private keys.
func writeXrayConfig(dir string, cfg []byte) (string, error) {
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		return "", fmt.Errorf("write xray config: %w", err)
	}
	return cfgPath, nil
}

// URLTest probes url through the RUNNING xray's http-in listener. The
// probe runs under the shared xray probe scope (integration BUG-009):
// StopTest aborts it instead of letting it ride out its timeout, and a
// newer probe supersedes it (one test at a time, like IP/Speed).
func (m *XrayManager) URLTest(url string, timeoutMs int32) (*TestResult, error) {
	m.mu.Lock()
	running := m.running
	mixedPort := m.mixedPort
	m.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("xray not running")
	}
	if url == "" {
		url = defaultTestURL
	}
	ctx, cancel := m.xrayTestCtx()
	defer cancel()
	// BUG-020: clamp into range (int64 conversion below is wrap-safe,
	// but negatives would expire instantly and huge values linger).
	d, err := proxiedHeadOrGetCtx(ctx,
		fmt.Sprintf("127.0.0.1:%d", effectiveMixedPort(mixedPort)),
		url, time.Duration(int64(clampProbeTimeoutMs(timeoutMs)))*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &TestResult{OutboundTag: "proxy", LatencyMs: int32(d.Milliseconds())}, nil
}

// ephAborted reports whether the given ephemeral epoch was invalidated
// by a concurrent StopEphemeral (or a superseding EphemeralRun).
// Callers must NOT hold m.mu.
func (m *XrayManager) ephAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ephEpoch != epoch
}

// watchEphemeralX reaps a crashed ephemeral xray (BUG-005). It owns
// cmd.Wait(); explicit teardown paths only Kill. If the instance is
// still current, the state is cleared under lock and the temp config
// dir (holding secrets) is removed — no zombie handle, no secret
// leftovers.
func (m *XrayManager) watchEphemeralX(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	current := m.ephCmd == cmd && m.ephRunning
	var dir string
	if current {
		dir = m.ephDir
		m.ephCmd = nil
		m.ephDir = ""
		m.ephRunning = false
	}
	m.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	if current {
		m.log("warn", fmt.Sprintf("ephemeral xray exited unexpectedly: %v", err))
	}
}

// EphemeralRun starts a temporary xray with the profile outbound and the
// http-in/socks-in split on 127.0.0.1:mixedPort (0 = pick a free port,
// reported back as the return value). The api-in listener points at a
// throwaway port so it can never collide with an active core; no stats
// are needed for a latency probe. A second call stops the previous
// ephemeral instance first.
//
// Like Start, the probe runs UNLOCKED with an epoch guard (BUG-004), and
// a watcher owns Wait from cmd.Start on so a crash always reaps the
// handle and removes the secrets-bearing temp dir (BUG-005).
func (m *XrayManager) EphemeralRun(profile *Profile, settings Settings, mixedPort int) (int, error) {
	// Tear down any previous instance outside the lock first. The fresh
	// epoch for THIS run is captured after setup below (under lock).
	m.mu.Lock()
	oldCmd, oldDir := m.stopEphemeralLocked()
	m.mu.Unlock()
	teardownEphemeralX(oldCmd, oldDir)

	m.mu.Lock()
	if mixedPort <= 0 {
		p, err := freePort()
		if err != nil {
			m.mu.Unlock()
			return 0, err
		}
		mixedPort = p
	}
	bin, err := m.resolveXray()
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	// The socks-in listener sits on mixedPort+1, so the throwaway API
	// address must avoid both bound ports.
	apiPort, err := distinctPort(mixedPort, mixedPort+1)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	es := settings
	es.MixedPort = mixedPort
	es.XrayAPIAddress = fmt.Sprintf("127.0.0.1:%d", apiPort)
	cfg, err := BuildXrayConfig(profile, es, es.XrayAPIAddress)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	dir, err := os.MkdirTemp("", "nekobox-xray-test")
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	_ = os.Chmod(dir, 0o700)
	cfgPath, err := writeXrayConfig(dir, []byte(cfg))
	if err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(dir)
		return 0, err
	}
	cmd := exec.Command(bin, "-c", cfgPath)
	cmd.Dir = dir
	setHideWindow(cmd)
	cmd.Env = childEnvWithLogLevel(os.Environ(), settings.LogLevel, func(msg string) {
		m.log("warn", msg)
	})
	cmd.Stdout = &logWriter{ch: m.logCh, source: "xray"}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "xray"}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(dir)
		return 0, fmt.Errorf("start ephemeral xray: %w", err)
	}
	epoch := m.ephEpoch
	go m.watchEphemeralX(cmd)
	m.mu.Unlock()

	abortErr := fmt.Errorf("ephemeral xray start aborted (superseded)")
	if err := waitForPort(fmt.Sprintf("127.0.0.1:%d", mixedPort), 10*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(dir)
		if m.ephAborted(epoch) {
			return 0, abortErr
		}
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ephEpoch != epoch {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(dir)
		return 0, abortErr
	}
	m.ephCmd = cmd
	m.ephDir = dir
	m.ephRunning = true
	return mixedPort, nil
}

// StopEphemeral stops the ephemeral instance only (including its temp
// config dir); the active connection (if any) is unaffected. Teardown
// runs outside the lock; the watcher owns Wait.
func (m *XrayManager) StopEphemeral() {
	m.mu.Lock()
	cmd, dir := m.stopEphemeralLocked()
	m.mu.Unlock()
	teardownEphemeralX(cmd, dir)
}

// stopEphemeralLocked detaches the ephemeral instance (epoch bump, state
// clear) and returns it for teardown outside the lock. Caller must hold
// m.mu; caller must NOT hold it while tearing down.
func (m *XrayManager) stopEphemeralLocked() (cmd *exec.Cmd, dir string) {
	m.ephEpoch++
	if !m.ephRunning {
		return nil, ""
	}
	cmd, dir = m.ephCmd, m.ephDir
	m.ephCmd = nil
	m.ephDir = ""
	m.ephRunning = false
	return cmd, dir
}

// teardownEphemeralX runs the detached ephemeral teardown (Kill plus temp
// dir removal) without holding m.mu. It never Waits: the watchEphemeralX
// goroutine owns Wait and reaps in the background.
func teardownEphemeralX(cmd *exec.Cmd, dir string) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// IsRunning reports whether xray is active.
func (m *XrayManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// GetStats queries the StatsService API. When the API is unreachable but
// the process is alive, a running snapshot with ApiDown=true is returned
// (nil error) so the UI can warn instead of showing stale zeros as live
// traffic. watchChild flips running=false on real exits, so a dead child
// reports Running=false here.
func (m *XrayManager) GetStats() (*Stats, error) {
	m.mu.Lock()
	running := m.running
	apiAddr := m.apiAddr
	statsClient := m.statsClient
	m.mu.Unlock()
	if !running {
		return &Stats{Running: false, Core: "xray"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The session's client reuses one gRPC connection; without one
	// (a manager that was never started) fall back to a throwaway.
	if statsClient == nil {
		statsClient = NewXrayStatsClient(apiAddr)
		defer statsClient.Close()
	}
	resp, err := statsClient.QueryStats(ctx, "", false)
	if err != nil {
		return &Stats{Running: true, Core: "xray", ApiDown: true}, nil
	}
	up, down, upByTag, downByTag := parseXrayStats(resp)
	return &Stats{Running: true, Core: "xray", Uplink: up, Downlink: down,
		UplinkByTag: upByTag, DownlinkByTag: downByTag}, nil
}

// Test starts xray ephemerally with a probe config and measures TCP
// connect latency to the profile endpoint. (Full latency via proxy would
// require routing; this validates reachability + config validity.)
func (m *XrayManager) Test(profile *Profile, settings Settings, timeoutMs int) (*TestResult, error) {
	start := time.Now()
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	apiAddr := settings.XrayAPIAddress
	if apiAddr == "" {
		apiAddr = fmt.Sprintf("127.0.0.1:%d", xrayAPIDefaultPort)
	}
	if _, err := BuildXrayConfig(profile, settings, apiAddr); err != nil {
		return &TestResult{LatencyMs: -1, Error: err.Error()}, nil
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", profile.Address, profile.Port), timeout)
	if err != nil {
		return &TestResult{LatencyMs: -1, Error: err.Error()}, nil
	}
	conn.Close()
	return &TestResult{LatencyMs: int32(time.Since(start).Milliseconds())}, nil
}

// BuildXrayConfig renders a full Xray config.json for a profile, including
// local HTTP/SOCKS inbounds, the profile outbound, freedom/block outbounds,
// routing, and the stats/API inbounds used for traffic queries.
//
// External chaining: when auxiliary processes (see external.go) run
// with ChainTo="proxy", the "proxy" outbound resolved below gains a
// proxySettings tag pointing at a generated SOCKS "ext-<kind>"
// outbound per chain (nested in config order), so traffic flows
// proxy -> external -> origin. This is a pure config-time
// substitution — no core manager is involved. A stored full raw
// override (Phase G) replaces the whole config and skips chaining,
// so ordering stays deterministic.
func BuildXrayConfig(profile *Profile, settings Settings, apiAddr string) (string, error) {
	// Global transport overrides (settings fragment/fingerprint):
	// render-time copy, stored profile untouched.
	profile = applyGlobalTransportOverrides(profile, settings)
	// Phase G: a stored full raw override is authoritative — it
	// replaces the whole rendered config (profile mapping and section
	// overrides are skipped) so ordering is deterministic.
	if full, ok, err := rawCoreOverride(settings.CoreOverrides); err != nil {
		return "", err
	} else if ok {
		data, err := json.MarshalIndent(full, "", "  ")
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	outbound, err := mapProfileToXrayOutbound(profile)
	if err != nil {
		return "", err
	}
	mixedPort := settings.MixedPort
	if mixedPort <= 0 {
		mixedPort = 2080
	}
	if mixedPort > 65534 {
		return "", fmt.Errorf("mixedPort %d leaves no room for the SOCKS port (must be <= 65534)", mixedPort)
	}
	socksPort := mixedPort + 1

	// Refuse a collision with the apiAddr port.
	apiHost, apiPort := splitAPIAddr(apiAddr)
	_ = apiHost
	if apiPort == mixedPort || apiPort == socksPort {
		return "", fmt.Errorf("apiAddr port %d collides with mixed/SOCKS port", apiPort)
	}
	host, port := splitAPIAddr(apiAddr)
	listen := inboundListenAddr(settings)
	cfg := map[string]any{
		"log":   map[string]any{"loglevel": xrayLogLevel(settings.LogLevel)},
		"stats": map[string]any{},
		"api": map[string]any{
			"tag":      "api",
			"services": []string{"HandlerService", "StatsService", "LoggerService"},
		},
		// Without system stats policy, Xray serves the API but never
		// registers per-inbound/outbound traffic counters.
		"policy": map[string]any{
			"system": map[string]any{
				"statsInboundUplink": true, "statsInboundDownlink": true,
				"statsOutboundUplink": true, "statsOutboundDownlink": true,
			},
		},
		"inbounds": []any{
			map[string]any{
				"tag": "http-in", "listen": listen, "port": mixedPort,
				"protocol": "http",
				"settings": map[string]any{},
				"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "fakedns"}},
			},
			map[string]any{
				"tag": "socks-in", "listen": listen, "port": mixedPort + 1,
				"protocol": "socks",
				"settings": map[string]any{"auth": "noauth", "udp": true},
				"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "fakedns"}},
			},
			map[string]any{
				"tag": "api-in", "listen": host, "port": port,
				"protocol": "dokodemo-door",
				"settings": map[string]any{"address": host},
			},
		},
		"outbounds": chainXrayProxy(outbound),
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []string{"api-in"}, "outboundTag": "api"},
			},
		},
	}
	// SNI-spoof front (sni_front.go): same contract as the sing-box
	// builder — CoreSpoof or loopback-while-engine-runs rewrites the
	// "proxy" outbound at the local spoof listener.
	if spoofPort, ok := spoofPortForProfile(profile); ok {
		applySNIFrontXray(cfg, spoofPort)
	}
	// Phase E: merge stored section overrides (route->routing,
	// dns, inbound->inbounds; sing-box-only experimental is skipped)
	// over the rendered defaults before writing.
	if err := mergeCoreOverrides(cfg, settings.CoreOverrides, true); err != nil {
		return "", err
	}
	// Xray dokodemo api inbound shape: keep minimal but valid — the API
	// listener is a plain dokodemo-door bound to the api address.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// splitAPIAddr parses an Xray api-in address. Bad ports fall back to
// xrayAPIDefaultPort with a warning; an empty host defaults to
// 127.0.0.1; hosts containing ":" (bare IPv6 without brackets) or "/"
// (path) are rejected back to 127.0.0.1 so api-in is never rendered
// with an illegal address.
func splitAPIAddr(apiAddr string) (string, int) {
	h, p, err := net.SplitHostPort(apiAddr)
	if err != nil {
		return "127.0.0.1", xrayAPIDefaultPort
	}
	port, err := strconv.Atoi(strings.TrimSpace(p))
	if err != nil || port < 1 || port > 65535 {
		log.Printf("[xray] invalid api port %q, falling back to %d", p, xrayAPIDefaultPort)
		port = xrayAPIDefaultPort
	}
	h = strings.TrimSpace(h)
	if h == "" {
		h = "127.0.0.1"
	} else if strings.Contains(h, "/") {
		log.Printf("[xray] invalid api host %q, falling back to 127.0.0.1", h)
		h = "127.0.0.1"
	} else if strings.Contains(h, ":") && !strings.HasPrefix(strings.TrimSpace(apiAddr), "[") {
		// Bare IPv6 without brackets (e.g. "::1:10085" parses with a
		// colon-bearing host): reject so api-in never binds garbage.
		// Bracketed "[::1]:10085" is legitimate and kept.
		log.Printf("[xray] invalid api host %q, falling back to 127.0.0.1", h)
		h = "127.0.0.1"
	}
	return h, port
}

// chainXrayProxy adds one SOCKS outbound per active external chain
// (config order) and points the "proxy" outbound at the first via
// proxySettings, so traffic flows proxy -> ext -> origin. Each ext
// outbound in turn proxies to the next, the last exits directly.
// With no chains the list is the plain [proxy, direct, block, api].
func chainXrayProxy(outbound map[string]any) []any {
	direct := map[string]any{"tag": "direct", "protocol": "freedom"}
	block := map[string]any{"tag": "block", "protocol": "blackhole"}
	api := map[string]any{"tag": "api", "protocol": "freedom"}
	chains := chainsForTag("proxy")
	if len(chains) == 0 {
		return []any{outbound, direct, block, api}
	}
	out := make([]any, 0, len(chains)+4)
	out = append(out, outbound)
	prev := outbound
	for _, c := range chains {
		tag := fmt.Sprintf("ext-%s", string(c.kind))
		ext := map[string]any{
			"tag": tag, "protocol": "socks",
			"settings": map[string]any{"servers": []any{map[string]any{
				"address": "127.0.0.1", "port": c.port,
			}}},
			"streamSettings": map[string]any{"network": "tcp"},
		}
		prev["proxySettings"] = map[string]any{"tag": tag}
		out = append(out, ext)
		prev = ext
	}
	out = append(out, direct, block, api)
	return out
}

// mapProfileToXrayOutbound converts a Profile to an Xray outbound object.
// Supports vless, vmess, trojan, shadowsocks, aether (+ raw JSON passthrough).
func mapProfileToXrayOutbound(p *Profile) (map[string]any, error) {
	if p.Protocol == "aether" && strings.TrimSpace(p.RawOutbound) == "" {
		// Aether engine (PattNG parity): a SOCKS outbound dialing the
		// engine's listener — the port a throwaway probe pinned on the
		// profile, else the live published port, else the default engine
		// port when no engine runs (config export, like PattNG dials the
		// settings' Aether listen port).
		port := aetherDefaultCommandPort
		if p.AetherEnginePort > 0 {
			port = p.AetherEnginePort
		} else if live, ok := currentAetherActivePort(); ok {
			port = live
		}
		return map[string]any{
			"tag": "proxy", "protocol": "socks",
			"settings": map[string]any{
				"servers": []any{map[string]any{"address": aetherLoopback, "port": port}},
			},
		}, nil
	}
	if p.RawOutbound != "" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(p.RawOutbound), &raw); err != nil {
			return nil, fmt.Errorf("rawOutbound: %w", err)
		}
		// Always re-tag: the SNI front rewrite, external chaining and
		// spoof repair all look the outbound up as "proxy", so a pasted
		// tag silently disabled them.
		raw["tag"] = "proxy"
		return raw, nil
	}
	stream, err := buildXrayStreamSettings(p)
	if err != nil {
		return nil, err
	}
	if err := validateFragment(p); err != nil {
		return nil, err
	}
	// Mux renders as a top-level outbound object on xray (nil unless
	// enabled); invalid options fail here instead of at xray startup.
	mux, err := muxBlockXray(p)
	if err != nil {
		return nil, err
	}
	withMux := func(out map[string]any) map[string]any {
		if mux != nil {
			out["mux"] = mux
		}
		return out
	}
	switch p.Protocol {
	case "vless":
		vnext := map[string]any{"address": p.Address, "port": p.Port,
			"users": []any{map[string]any{
				"id": p.IDOrPassword, "encryption": "none",
				"flow": p.Flow,
			}}}
		if p.Flow == "" {
			users := vnext["users"].([]any)
			u := users[0].(map[string]any)
			delete(u, "flow")
		}
		return withMux(map[string]any{
			"tag": "proxy", "protocol": "vless",
			"settings":       map[string]any{"vnext": []any{vnext}},
			"streamSettings": stream,
		}), nil
	case "vmess":
		user := map[string]any{"id": p.IDOrPassword, "alterId": p.AlterID, "security": valueOr(p.Security, "auto")}
		return withMux(map[string]any{
			"tag": "proxy", "protocol": "vmess",
			"settings":       map[string]any{"vnext": []any{map[string]any{"address": p.Address, "port": p.Port, "users": []any{user}}}},
			"streamSettings": stream,
		}), nil
	case "trojan":
		return withMux(map[string]any{
			"tag": "proxy", "protocol": "trojan",
			"settings":       map[string]any{"servers": []any{map[string]any{"address": p.Address, "port": p.Port, "password": p.IDOrPassword}}},
			"streamSettings": stream,
		}), nil
	case "shadowsocks":
		return withMux(map[string]any{
			"tag": "proxy", "protocol": "shadowsocks",
			"settings": map[string]any{"servers": []any{map[string]any{
				"address": p.Address, "port": p.Port,
				"method": valueOr(p.Method, "aes-256-gcm"), "password": p.IDOrPassword,
			}}},
		}), nil
	default:
		return nil, fmt.Errorf("protocol %q not supported by xray manager (use rawOutbound JSON)", p.Protocol)
	}
}

// buildXrayStreamSettings renders network + TLS/REALITY settings,
// plus optional TLS hello fragmentation (Profile TLSFragment fields)
// as a stream finalmask TCP fragment mask.
func buildXrayStreamSettings(p *Profile) (map[string]any, error) {
	s := map[string]any{"network": valueOr(p.Network, "tcp")}
	if p.TLS || p.RealityEnabled || p.Protocol == "trojan" || p.Flow != "" {
		if err := validateFingerprint(p); err != nil {
			return nil, err
		}
		if p.RealityEnabled {
			s["security"] = "reality"
			s["realitySettings"] = map[string]any{
				"serverName":  valueOr(p.ServerName, p.Address),
				"fingerprint": profileFingerprint(p),
				"publicKey":   p.RealityPubKey,
				"shortId":     p.RealityShortID,
				"spiderX":     valueOr(p.RealitySpiderX, "/"),
				"show":        false,
			}
		} else {
			s["security"] = "tls"
			tls := map[string]any{
				"serverName":    valueOr(p.ServerName, p.Address),
				"allowInsecure": p.AllowInsecure,
				"fingerprint":   profileFingerprint(p),
			}
			if list := alpnList(p.ALPN); len(list) > 0 {
				tls["alpn"] = list
			}
			s["tlsSettings"] = tls
		}
	}
	frag, err := buildXrayFragment(p)
	if err != nil {
		return nil, err
	}
	if frag != nil {
		s["finalmask"] = frag
	}
	switch valueOr(p.Network, "tcp") {
	case "tcp", "raw":
		// Plain TCP: no stream settings.
	case "ws":
		ws := map[string]any{"path": valueOr(p.Path, "/")}
		if p.Host != "" {
			ws["headers"] = map[string]any{"Host": p.Host}
		}
		s["wsSettings"] = ws
	case "grpc":
		s["grpcSettings"] = map[string]any{"serviceName": valueOr(p.Path, "TunService")}
	case "xhttp":
		x := map[string]any{"path": valueOr(p.Path, "/")}
		if p.Host != "" {
			x["host"] = p.Host
		}
		s["xhttpSettings"] = x
	case "httpupgrade":
		hu := map[string]any{"path": valueOr(p.Path, "/")}
		if p.Host != "" {
			hu["host"] = p.Host
		}
		s["httpupgradeSettings"] = hu
	case "http", "h2", "h3":
		return nil, fmt.Errorf("network %q was removed from Xray — use xhttp, or the sing-box core", p.Network)
	case "kcp", "quic":
		return nil, fmt.Errorf("network %q needs transport settings the profile does not carry — use rawOutbound JSON, or the sing-box core", p.Network)
	default:
		return nil, fmt.Errorf("network %q is not supported by the Xray transport — use rawOutbound JSON", p.Network)
	}
	return s, nil
}

// buildXrayFragment renders Profile TLSFragment fields as an Xray
// stream finalmask TCP fragment mask (nil when disabled). Mode is
// "tlshello" or a packets range like "1-3"; length is "A-B" (empty
// defaults to "10-20"); interval is the delay upper bound in ms.
// Length/interval have no sing-box outbound equivalent — the
// sing-box builder only sets its boolean fragment flag.
func buildXrayFragment(p *Profile) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(p.TLSFragment))
	if mode == "" || mode == "off" {
		return nil, nil
	}
	// Shared range validation (same rules the sing-box builder
	// enforces, so bad values fail on either core).
	if err := validateFragment(p); err != nil {
		return nil, err
	}
	length := strings.TrimSpace(p.TLSFragmentLen)
	if length == "" {
		length = "10-20"
	}
	lfrom, lto, _ := parsePacketsRange(length)
	return map[string]any{"tcp": []any{map[string]any{
		"type": "fragment",
		"settings": map[string]any{
			"packets": mode,
			"length":  map[string]any{"from": lfrom, "to": lto},
			"delay":   map[string]any{"from": 0, "to": p.TLSFragmentInt},
		},
	}}}, nil
}

// parsePacketsRange parses "A-B" (or a bare "A" meaning A-A).
func parsePacketsRange(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		a, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		b, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		return a, b, true
	}
	a, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, false
	}
	return a, a, true
}
