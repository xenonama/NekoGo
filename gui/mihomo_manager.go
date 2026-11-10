package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mihomoCoreName is the Stats.Core label the mihomo backend reports.
const mihomoCoreName = "mihomo"

// mihomoGroupName names the single select group every connection is
// routed to. One MATCH rule sends all traffic there and the group holds
// exactly one member (the profile's own proxy), so the route is decided
// by NekoGo instead of relying on the GLOBAL group mihomo builds for
// "mode: global".
const mihomoGroupName = "NekoGo"

// mihomoProxyName is the name the rendered proxy carries. It is the tag
// the latency probe addresses and the name a pasted rawOutbound is
// re-tagged to, mirroring the "proxy" outbound tag on the other cores.
const mihomoProxyName = "proxy"

// mihomoConfigName is the file mihomo is pointed at with -f. mihomo
// parses its config with a YAML 1.2 reader, and YAML 1.2 is a superset
// of JSON, so the document is rendered with encoding/json — adding this
// core costs no new Go dependency.
const mihomoConfigName = "config.yaml"

// mihomoSupportedFingerprints are the uTLS presets mihomo's
// client-fingerprint accepts. It is narrower than supportedFingerprints
// (no "360"/"qq"), so a preset only the other cores know must not be
// rendered into a document mihomo would reject at startup.
var mihomoSupportedFingerprints = []string{
	"chrome", "firefox", "safari", "ios", "android",
	"edge", "random", "randomized",
}

// mihomoUnsupportedNetworks are transports NekoGo profiles can carry
// that mihomo has no proxy-side implementation of. They fail at render
// time with the core named, instead of rendering a document mihomo
// rejects (or, worse, one it accepts while silently dropping the knob).
var mihomoUnsupportedNetworks = map[string]bool{
	"quic":         true,
	"kcp":          true,
	"xhttp":        true,
	"meek":         true,
	"domainsocket": true,
}

// MihomoManager renders a Clash Meta document from a Profile, runs
// mihomo.exe as a hidden child process, and reads traffic, the
// connection table and latency probes from mihomo's own external
// controller — the same Clash REST API shape the upstream sing-box
// backend already speaks, so the two share the /connections ledger
// grammar (clashConnections, clashConnectionTag, trafficTotals).
//
// Unlike the custom core there is no Thrift RPC here, and unlike Xray
// there is no gRPC StatsService: every read is one authenticated GET.
type MihomoManager struct {
	mu            sync.Mutex
	cmd           *exec.Cmd
	running       bool
	startEpoch    uint64
	stopRequested bool
	logCh         chan LogEntry
	mixedPort     int
	// apiPort/apiSecret address the external controller Start renders
	// into the document; both are zero while no mihomo core runs.
	apiPort   int
	apiSecret string
	// traffic is the same monotonic ledger the other backends feed, so a
	// stats poll can never move a counter backwards.
	traffic trafficTotals
	// apiErrLogged throttles the controller failure log to one line per
	// outage instead of one per second (the frontend polls every second).
	apiErrLogged   bool
	tempConfigPath string
	// liveConfig is the exact document the running child was given. Its
	// controller port and secret are chosen per Start, so no idle
	// rendering can reproduce it — the read-only viewer reports this.
	liveConfig  string
	recent      lineRing
	startedAt   time.Time
	exitHandler func(EngineExit)
	// probes is the Phase C client-side state (mihomo has no test RPCs
	// either): the IP/speed/URL legs run against its mixed inbound and
	// cache their rows for LastNetwork. See nettools.go.
	probes probeState

	// Ephemeral test instance: a separate process/document so a latency
	// probe never disturbs the active connection.
	ephCmd     *exec.Cmd
	ephDir     string
	ephRunning bool
	ephEpoch   uint64
}

// NewMihomoManager creates a manager. The binary is resolved per Start
// from Settings.MihomoPath, so no path is captured here. logCh receives
// the child's stdout/stderr.
func NewMihomoManager(logCh chan LogEntry) *MihomoManager {
	return &MihomoManager{logCh: logCh}
}

// SetExitHandler registers the cross-engine death report callback
// (CoreManager.handleEngineExit). Called once at wiring time.
func (m *MihomoManager) SetExitHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exitHandler = fn
}

func (m *MihomoManager) log(level, msg string) {
	if m.logCh == nil {
		return
	}
	// Best-effort (see SingBoxManager.log): call sites hold m.mu, so a
	// full channel must not wedge the manager.
	select {
	case m.logCh <- LogEntry{Level: level, Source: "mihomo", Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
	}
}

// mihomoExeNames are the file names the resolver accepts. The official
// Windows release asset is mihomo-windows-amd64.exe, so an unpacked
// release dropped into bin\ resolves without a rename.
var mihomoExeNames = []string{"mihomo.exe", "mihomo", "mihomo-windows-amd64.exe"}

// resolveMihomo finds the engine: the dedicated MihomoPath override
// first (a directory hosting one of mihomoExeNames, or a file), then
// bin\ next to nekogo.exe, then the exe's own folder, then PATH.
// Settings.CorePath / XrayPath are other engines' overrides and are
// never consulted here.
func resolveMihomo(settings Settings) (string, error) {
	if p := strings.TrimSpace(settings.MihomoPath); p != "" {
		if info, err := os.Stat(p); err == nil {
			if info.IsDir() {
				for _, name := range mihomoExeNames {
					c := filepath.Join(p, name)
					if _, err := os.Stat(c); err == nil {
						return c, nil
					}
				}
				return "", fmt.Errorf("mihomo not found in %q (want mihomo.exe)", p)
			}
			return p, nil
		}
		return "", fmt.Errorf("mihomo not found at %q", p)
	}
	if c, ok := firstEngineFile(mihomoExeNames...); ok {
		return c, nil
	}
	// The official release names the binary after its asset, so a build
	// that was fetched but never renamed still resolves.
	if c, ok := firstEngineGlob("mihomo*.exe"); ok {
		return c, nil
	}
	for _, name := range []string{"mihomo", "mihomo.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("mihomo executable not found (place it in bin\\ next to nekogo.exe or set mihomoPath in settings)")
}

// Start renders the document, launches mihomo.exe -d <dir> -f <file> and
// waits for both the mixed inbound and the external controller.
//
// Lifecycle (BUG-004 grammar): only fast setup runs under m.mu; the
// readiness probe runs UNLOCKED so Stop/IsRunning stay responsive, and
// the running state commits under lock only on an unchanged epoch.
func (m *MihomoManager) Start(profile *Profile, settings Settings) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("mihomo already running")
	}
	m.stopRequested = false
	m.startEpoch++
	epoch := m.startEpoch
	mixedPort := effectiveMixedPort(settings.MixedPort)
	// A crashed session can leave mihomo.exe holding the mixed port, so
	// the next Connect would fail seconds later with a bare bind error.
	// Fail fast with the zombie hint instead, and never silently pick
	// another port: the UI documents this exact one.
	if err := checkPortFree(net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort))); err != nil {
		m.mu.Unlock()
		return err
	}
	bin, err := resolveMihomo(settings)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	apiPort, err := distinctPort(mixedPort)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("pick mihomo controller port: %w", err)
	}
	apiSecret, err := randomClashAPISecret()
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("generate mihomo controller secret: %w", err)
	}
	cfg, err := BuildMihomoConfig(profile, settings, apiPort, apiSecret)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	dir, err := os.MkdirTemp("", "nekogo-mihomo-*")
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("create mihomo config dir: %w", err)
	}
	_ = os.Chmod(dir, 0o700)
	cfgPath, err := writeMihomoConfig(dir, cfg)
	if err != nil {
		_ = os.RemoveAll(dir)
		m.mu.Unlock()
		return err
	}
	cmd := m.newChild(bin, dir, cfgPath, settings)
	m.recent.reset()
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		m.mu.Unlock()
		return fmt.Errorf("start mihomo: %w", err)
	}
	m.cmd = cmd
	m.startedAt = time.Now()
	m.tempConfigPath = dir
	m.mu.Unlock()

	exited := make(chan error, 1)
	go m.watchChild(cmd, epoch, exited)

	// Probe loopback whatever the document binds: a loopback setting binds
	// 127.0.0.1 itself, and any other setting renders bind-address "*" (the
	// IPv4 wildcard), which loopback still reaches.
	mixedAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort))
	apiAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort))
	deadline := time.Now().Add(15 * time.Second)
	abort := func(why error) error {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(dir)
		return why
	}
	mixedUp, apiUp := false, false
	for {
		if m.startAborted(epoch) {
			return abort(fmt.Errorf("mihomo start aborted (concurrent stop)"))
		}
		select {
		case werr := <-exited:
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(dir)
			if m.startAborted(epoch) {
				return fmt.Errorf("mihomo start aborted (concurrent stop)")
			}
			return fmt.Errorf("mihomo exited during startup: %v", werr)
		default:
		}
		if !mixedUp {
			if c, derr := net.DialTimeout("tcp", mixedAddr, 500*time.Millisecond); derr == nil {
				c.Close()
				mixedUp = true
			}
		}
		if !apiUp {
			if c, derr := net.DialTimeout("tcp", apiAddr, 500*time.Millisecond); derr == nil {
				c.Close()
				apiUp = true
			}
		}
		if mixedUp && apiUp {
			break
		}
		if time.Now().After(deadline) {
			if !mixedUp {
				return abort(fmt.Errorf("mihomo mixed inbound not ready at %s", mixedAddr))
			}
			return abort(fmt.Errorf("mihomo external controller not reachable at %s (bad config?)", apiAddr))
		}
		time.Sleep(200 * time.Millisecond)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startEpoch != epoch {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("mihomo start aborted (concurrent stop)")
	}
	m.running = true
	m.mixedPort = mixedPort
	m.apiPort = apiPort
	m.apiSecret = apiSecret
	m.liveConfig = cfg
	// A new core session starts a new traffic ledger: the counters of the
	// previous process are gone with the previous process.
	m.traffic.reset()
	m.apiErrLogged = false
	m.log("info", fmt.Sprintf("mihomo started (profile %q, controller 127.0.0.1:%d)", profile.Name, apiPort))
	return nil
}

// newChild builds the hidden mihomo child for one document. -d pins the
// home dir so the cache and any geodata mihomo writes land in the temp
// dir that Stop removes, never in bin\ next to nekogo.exe.
func (m *MihomoManager) newChild(bin, dir, cfgPath string, settings Settings) *exec.Cmd {
	cmd := exec.Command(bin, "-d", dir, "-f", cfgPath)
	cmd.Dir = dir
	setHideWindow(cmd)
	cmd.Env = childEnvWithLogLevel(os.Environ(), settings.LogLevel, func(msg string) {
		m.log("warn", msg)
	})
	cmd.Stdout = &logWriter{ch: m.logCh, source: "mihomo", ring: &m.recent}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "mihomo", ring: &m.recent}
	return cmd
}

func (m *MihomoManager) startAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startEpoch != epoch
}

// watchChild owns Wait() for the child. epoch is the Start generation
// that spawned it: Stop (or a superseding Start) bumps startEpoch, so a
// child that exits after a deliberate stop is reported as stopped rather
// than as a crash.
func (m *MihomoManager) watchChild(cmd *exec.Cmd, epoch uint64, exited chan<- error) {
	err := cmd.Wait()
	// Signal BEFORE taking the mutex: the startup probe reads this
	// channel, so locking first would delay the early-death report into
	// the full readiness timeout.
	if exited != nil {
		select {
		case exited <- err:
		default:
		}
	}
	m.mu.Lock()
	current := m.cmd == cmd && m.running && !m.stopRequested && m.startEpoch == epoch
	dir := ""
	if m.cmd == cmd {
		m.cmd = nil
		m.running = false
		m.apiPort = 0
		m.apiSecret = ""
		m.liveConfig = ""
		// The temp document carries the session's credentials; a crash
		// used to strand it because Stop's cleanup only runs while a cmd
		// is registered.
		dir = m.tempConfigPath
		m.tempConfigPath = ""
	}
	handler := m.exitHandler
	started := m.startedAt
	tail := m.recent.tail(engineLastLines)
	m.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	if !current {
		m.log("info", "mihomo stopped")
		return
	}
	ev := newEngineExit("mihomo", cmd, err, started, tail)
	m.log("warn", ev.Summary())
	if handler != nil {
		handler(ev)
	}
}

// Stop kills the child and removes its temp config dir. watchChild owns
// Wait(); Stop only signals and waits for the reaping. It bumps
// startEpoch first so an in-flight Start probe aborts instead of
// committing behind us (BUG-004).
func (m *MihomoManager) Stop() error {
	m.mu.Lock()
	m.startEpoch++
	m.stopRequested = true
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		m.mu.Lock()
		m.running = false
		m.apiPort = 0
		m.apiSecret = ""
		m.liveConfig = ""
		dir := m.tempConfigPath
		m.tempConfigPath = ""
		m.mu.Unlock()
		if dir != "" {
			_ = os.RemoveAll(dir)
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
	m.apiPort = 0
	m.apiSecret = ""
	m.liveConfig = ""
	m.cmd = nil
	dir := m.tempConfigPath
	m.tempConfigPath = ""
	m.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	m.stopRequested = false
	m.log("info", "mihomo stopped")
	return nil
}

func (m *MihomoManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// LiveConfig returns the exact document the running child was given, or
// "" when no mihomo core runs.
func (m *MihomoManager) LiveConfig() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveConfig
}

// GetStats reads GET /connections from the external controller and folds
// the byte counters into the shared monotonic ledger.
func (m *MihomoManager) GetStats() (*Stats, error) {
	if !m.IsRunning() {
		return &Stats{Running: false, Core: mihomoCoreName}, nil
	}
	var snap clashConnections
	if err := m.apiGet("/connections", 3*time.Second, &snap); err != nil {
		// The child is usually still fine (the controller comes up moments
		// after the mixed inbound): flag the counters as stale instead of
		// blanking them, exactly like the other backends do.
		m.logAPIError(err)
		return &Stats{Running: true, Core: mihomoCoreName, ApiDown: true}, nil
	}
	m.resetAPIError()
	upByTag := map[string]int64{}
	downByTag := map[string]int64{}
	var liveUp, liveDown int64
	for _, c := range snap.Connections {
		tag := clashConnectionTag(c)
		upByTag[tag] += c.Upload
		downByTag[tag] += c.Download
		liveUp += c.Upload
		liveDown += c.Download
	}
	// The session totals also cover connections that already closed;
	// attribute the difference to clashClosedTag so the ledger total equals
	// what the core reports.
	if rest := snap.UploadTotal - liveUp; rest > 0 {
		upByTag[clashClosedTag] += rest
	}
	if rest := snap.DownloadTotal - liveDown; rest > 0 {
		downByTag[clashClosedTag] += rest
	}
	up, down, upCum, downCum := m.traffic.observe(upByTag, downByTag)
	return &Stats{
		Running:       true,
		Core:          mihomoCoreName,
		Uplink:        up,
		Downlink:      down,
		UplinkByTag:   upCum,
		DownlinkByTag: downCum,
	}, nil
}

// ListConnections maps GET /connections rows onto the core-agnostic
// Connection struct. mihomo reports the route as chains, so chains[0] is
// the group name and the profile proxy follows it.
func (m *MihomoManager) ListConnections() (*ConnectionList, error) {
	if !m.IsRunning() {
		return &ConnectionList{}, nil
	}
	var snap clashConnections
	if err := m.apiGet("/connections", 3*time.Second, &snap); err != nil {
		return nil, err
	}
	out := &ConnectionList{Connections: make([]Connection, 0, len(snap.Connections))}
	for _, c := range snap.Connections {
		host := c.Metadata.Host
		if host == "" {
			host = c.Metadata.DestinationIP
		}
		dest := host
		if c.Metadata.DestinationPort != "" {
			dest = net.JoinHostPort(host, c.Metadata.DestinationPort)
		}
		out.Connections = append(out.Connections, Connection{
			ID:       c.ID,
			Outbound: clashConnectionTag(c),
			Network:  c.Metadata.Network,
			Dest:     dest,
			Domain:   c.Metadata.Host,
			Process:  c.Metadata.ProcessPath,
			Upload:   c.Upload,
			Download: c.Download,
		})
	}
	return out, nil
}

// URLTest measures url through the RUNNING core's mixed inbound — the
// same path live traffic takes, and the same grammar as Xray's URLTest.
// It runs under the shared probe scope (integration BUG-009) so StopTest
// aborts it instead of letting it ride out its timeout.
func (m *MihomoManager) URLTest(url string, timeoutMs int32) (*TestResult, error) {
	proxy, err := m.mihomoProxyAddr()
	if err != nil {
		return nil, err
	}
	if url == "" {
		url = defaultTestURL
	}
	ctx, cancel := m.probes.scope()
	defer cancel()
	d, err := proxiedHeadOrGetCtx(ctx, proxy, url,
		time.Duration(clampProbeTimeoutMs(timeoutMs))*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &TestResult{OutboundTag: mihomoProxyName, LatencyMs: int32(d.Milliseconds())}, nil
}

// apiGet performs one authenticated GET against the external controller
// and decodes its JSON body.
func (m *MihomoManager) apiGet(path string, timeout time.Duration, out any) error {
	m.mu.Lock()
	port, secret := m.apiPort, m.apiSecret
	m.mu.Unlock()
	if port <= 0 {
		return fmt.Errorf("mihomo controller is not running")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("mihomo api %s: %w", path, err)
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mihomo api %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("mihomo api %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("mihomo api %s: HTTP %d: %s", path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("mihomo api %s: %w", path, err)
	}
	return nil
}

// logAPIError logs one line per controller outage instead of one per
// stats poll (the frontend polls once a second).
func (m *MihomoManager) logAPIError(err error) {
	m.mu.Lock()
	already := m.apiErrLogged
	m.apiErrLogged = true
	m.mu.Unlock()
	if !already {
		m.log("warn", fmt.Sprintf("mihomo controller unavailable: %v", err))
	}
}

func (m *MihomoManager) resetAPIError() {
	m.mu.Lock()
	m.apiErrLogged = false
	m.mu.Unlock()
}

// CheckConfig validates a rendered document with mihomo's own -t flag,
// which is the only validator this backend has. It resolves the engine
// through the same settings Start does, so a MihomoPath override is
// validated by the binary that would actually run.
func (m *MihomoManager) CheckConfig(cfg string, settings Settings) error {
	bin, err := resolveMihomo(settings)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "nekogo-mihomo-check-*")
	if err != nil {
		return fmt.Errorf("create mihomo check dir: %w", err)
	}
	defer os.RemoveAll(dir)
	cfgPath, err := writeMihomoConfig(dir, cfg)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "-d", dir, "-f", cfgPath, "-t")
	cmd.Dir = dir
	setHideWindow(cmd)
	// mihomo logs -t diagnostics to STDOUT and leaves stderr empty, so
	// discarding stdout turned every rejection into a bare "exit status 1".
	// The two are separate builders because strings.Builder is not
	// comparable, and exec only serializes writes to an identical writer.
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stdout.String())
		if extra := strings.TrimSpace(stderr.String()); extra != "" {
			if msg != "" {
				msg += "\n"
			}
			msg += extra
		}
		if msg != "" {
			return fmt.Errorf("invalid mihomo config: %s", msg)
		}
		return fmt.Errorf("mihomo config test failed: %w", err)
	}
	return nil
}

// writeMihomoConfig writes cfg to dir/config.yaml with 0600 permissions:
// the document embeds UUIDs, passwords and private keys.
func writeMihomoConfig(dir, cfg string) (string, error) {
	cfgPath := filepath.Join(dir, mihomoConfigName)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return "", fmt.Errorf("write mihomo config: %w", err)
	}
	return cfgPath, nil
}

// ephAborted reports whether the given ephemeral epoch was invalidated
// by a concurrent StopEphemeral (or a superseding EphemeralRun).
func (m *MihomoManager) ephAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ephEpoch != epoch
}

// watchEphemeral reaps a crashed ephemeral child (BUG-005). It owns
// cmd.Wait(); explicit teardown paths only Kill. The temp document holds
// secrets, so it is removed here too.
func (m *MihomoManager) watchEphemeral(cmd *exec.Cmd) {
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
		m.log("warn", fmt.Sprintf("ephemeral mihomo exited unexpectedly: %v", err))
	}
}

// stopEphemeralLocked tears down the ephemeral instance's state and hands
// back the child + dir for the caller to kill and remove outside the lock.
func (m *MihomoManager) stopEphemeralLocked() (*exec.Cmd, string) {
	m.ephEpoch++
	cmd, dir := m.ephCmd, m.ephDir
	m.ephCmd = nil
	m.ephDir = ""
	m.ephRunning = false
	return cmd, dir
}

func teardownEphemeralMihomo(cmd *exec.Cmd, dir string) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// StopEphemeral ends a running ephemeral instance.
func (m *MihomoManager) StopEphemeral() {
	m.mu.Lock()
	cmd, dir := m.stopEphemeralLocked()
	m.mu.Unlock()
	teardownEphemeralMihomo(cmd, dir)
}

// EphemeralRun starts a temporary mihomo with the profile rendered behind
// the mixed inbound on 127.0.0.1:mixedPort (0 = pick a free port,
// reported back). Its controller gets its own throwaway port so it can
// never collide with an active core; no stats are needed for a latency
// probe. A second call stops the previous instance first.
func (m *MihomoManager) EphemeralRun(profile *Profile, settings Settings, mixedPort int) (int, error) {
	m.mu.Lock()
	oldCmd, oldDir := m.stopEphemeralLocked()
	m.mu.Unlock()
	teardownEphemeralMihomo(oldCmd, oldDir)

	m.mu.Lock()
	if mixedPort <= 0 {
		// mihomo's mixed inbound binds UDP on the same number as TCP
		// (measured on v1.19.33: "Start Mixed(http+socks) server error:
		// listen udp ..."). Windows reserves UDP ranges a TCP-only probe
		// reports as free, so picking one cost the probe its whole
		// readiness timeout.
		p, err := freePortTCPAndUDP()
		if err != nil {
			m.mu.Unlock()
			return 0, err
		}
		mixedPort = p
	}
	bin, err := resolveMihomo(settings)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	apiPort, err := distinctPort(mixedPort)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	apiSecret, err := randomClashAPISecret()
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	es := settings
	es.MixedPort = mixedPort
	es.InboundAddress = "127.0.0.1"
	cfg, err := BuildMihomoConfig(profile, es, apiPort, apiSecret)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	dir, err := os.MkdirTemp("", "nekogo-mihomo-test-*")
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	_ = os.Chmod(dir, 0o700)
	cfgPath, err := writeMihomoConfig(dir, cfg)
	if err != nil {
		_ = os.RemoveAll(dir)
		m.mu.Unlock()
		return 0, err
	}
	cmd := m.newChild(bin, dir, cfgPath, settings)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		m.mu.Unlock()
		return 0, fmt.Errorf("start ephemeral mihomo: %w", err)
	}
	m.ephCmd = cmd
	m.ephDir = dir
	m.ephRunning = true
	epoch := m.ephEpoch
	go m.watchEphemeral(cmd)
	m.mu.Unlock()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort))
	if err := waitForPort(addr, 10*time.Second); err != nil {
		if m.ephAborted(epoch) {
			m.StopEphemeral()
			return 0, fmt.Errorf("ephemeral mihomo start aborted (superseded)")
		}
		m.StopEphemeral()
		return 0, fmt.Errorf("ephemeral mihomo mixed inbound not ready: %w", err)
	}
	if m.ephAborted(epoch) {
		m.StopEphemeral()
		return 0, fmt.Errorf("ephemeral mihomo start aborted (superseded)")
	}
	return mixedPort, nil
}

// BuildMihomoConfig renders the Clash Meta document for one profile: the
// mixed inbound NekoGo's system proxy points at, the external controller
// every read goes through, and the profile as the single member of a
// select group that one MATCH rule routes all traffic to.
//
// Deliberately not rendered here, because mihomo has no equivalent and a
// silently-dropped knob would misrepresent what the engine does:
//   - Settings.DnsRemote/DnsDirect: mihomo resolves through the system
//     resolver when no dns block is present. Its dns block also demands a
//     bootstrap list of plain IPs for DoH upstreams, which the settings
//     do not carry, so guessing one would send lookups to a third party
//     the user never chose.
//   - Profile.TLSFragment*: TLS-hello fragmentation exists on Xray
//     (finalmask) and sing-box (tls.fragment) only.
//   - Settings.TunEnabled: the TUN inbound is rendered in the sing-box
//     config alone (see tunModeFor).
//   - Settings.CoreOverrides: those sections are shaped for sing-box and
//     Xray documents; merging them into a mihomo document would produce
//     keys mihomo rejects.
func BuildMihomoConfig(profile *Profile, settings Settings, apiPort int, apiSecret string) (string, error) {
	if profile == nil {
		return "", fmt.Errorf("nil profile")
	}
	// Global transport overrides (settings fingerprint): render-time copy,
	// the stored profile is untouched. The fragment half of that override
	// has no mihomo equivalent and is not applied.
	profile = applyGlobalFingerprintOverride(profile, settings)
	proxy, err := mapProfileToMihomoProxy(profile)
	if err != nil {
		return "", err
	}
	mixedPort := effectiveMixedPort(settings.MixedPort)
	if apiPort == mixedPort {
		return "", fmt.Errorf("mihomo controller port %d collides with the mixed inbound port", apiPort)
	}
	if apiPort <= 0 || apiPort > 65535 {
		return "", fmt.Errorf("mihomo controller port %d out of range", apiPort)
	}
	listen := inboundListenAddr(settings)
	lan := listen != "127.0.0.1" && listen != "::1"
	bind := "*"
	if !lan {
		bind = listen
	}
	cfg := map[string]any{
		"mixed-port":    mixedPort,
		"allow-lan":     lan,
		"bind-address":  bind,
		"mode":          "rule",
		"log-level":     mihomoLogLevel(settings.LogLevel),
		"ipv6":          true,
		"unified-delay": true,
		// Enumerating processes per connection needs privileges NekoGo may
		// not have and buys nothing for a MATCH-all route.
		"find-process-mode": "off",
		// No GEOIP/GEOSITE rule is rendered, so no geodata is loaded or
		// fetched: a first connect must not depend on a download.
		"geodata-mode":        false,
		"geo-auto-update":     false,
		"external-controller": fmt.Sprintf("127.0.0.1:%d", apiPort),
		"secret":              apiSecret,
		// Nothing to remember between runs: the temp dir is removed on stop,
		// and a stale selection would survive a profile edit.
		"profile": map[string]any{"store-selected": false, "store-fake-ip": false},
		"proxies": []any{proxy},
		"proxy-groups": []any{map[string]any{
			"name":    mihomoGroupName,
			"type":    "select",
			"proxies": []any{mihomoProxyName},
		}},
		"rules": []any{"MATCH," + mihomoGroupName},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// applyGlobalFingerprintOverride copies the global uTLS fingerprint onto
// a render-time profile clone when the override is enabled. It is the
// fingerprint half of applyGlobalTransportOverrides: the fragment half
// applies to Xray/sing-box only and mihomo cannot honor it.
func applyGlobalFingerprintOverride(p *Profile, s Settings) *Profile {
	if p == nil || !s.FingerprintEnabled || strings.TrimSpace(s.FingerprintValue) == "" {
		return p
	}
	clone := *p
	clone.TLSFingerprint = s.FingerprintValue
	return &clone
}

// mihomoLogLevel maps NekoGo's level names onto mihomo's, which spells
// the third one "warning".
func mihomoLogLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return "debug"
	case "error":
		return "error"
	case "warn", "warning":
		return "warning"
	case "silent", "off", "none":
		return "silent"
	default:
		return "info"
	}
}

// mihomoClientFingerprint returns the uTLS preset to render, or "" when
// mihomo has no such preset. An unsupported preset is dropped rather than
// passed through: mihomo rejects an unknown client-fingerprint at startup,
// which would fail every connect for a field only the other cores read.
// profileFingerprint already applies the "none"/empty → default rule the
// other builders use, so a profile that names no preset renders chrome
// here exactly as it does on sing-box and Xray.
func mihomoClientFingerprint(p *Profile) string {
	fp := profileFingerprint(p)
	if !containsFold(mihomoSupportedFingerprints, fp) {
		return ""
	}
	return fp
}

// mihomoTransport renders the network + its options block onto a proxy
// map. Unsupported transports are an error naming the core, so the user
// learns at Connect time instead of watching mihomo refuse to start.
func mihomoTransport(p *Profile, out map[string]any) error {
	network := strings.ToLower(strings.TrimSpace(p.Network))
	switch network {
	case "", "tcp":
		return nil
	case "ws":
		out["network"] = "ws"
		opts := map[string]any{"path": valueOr(p.Path, "/")}
		if p.Host != "" {
			opts["headers"] = map[string]any{"Host": p.Host}
		}
		out["ws-opts"] = opts
		return nil
	case "grpc":
		out["network"] = "grpc"
		out["grpc-opts"] = map[string]any{"grpc-service-name": valueOr(p.Path, "TunService")}
		return nil
	case "h2", "http", "httpupgrade":
		out["network"] = "h2"
		opts := map[string]any{"path": valueOr(p.Path, "/")}
		if p.Host != "" {
			opts["host"] = []any{p.Host}
		}
		out["h2-opts"] = opts
		return nil
	}
	if mihomoUnsupportedNetworks[network] {
		return fmt.Errorf("network %q is not supported by the mihomo core — use tcp, ws, grpc or h2, or switch this profile to another core", p.Network)
	}
	return fmt.Errorf("network %q is not supported by the mihomo core — use tcp, ws, grpc or h2, or switch this profile to another core", p.Network)
}

// mihomoTLS renders the TLS/REALITY half of a proxy map. mihomo spells
// the knobs servername / skip-cert-verify / alpn / client-fingerprint and
// reality-opts, so the sing-box and Xray field names do not carry over.
func mihomoTLS(p *Profile, out map[string]any) error {
	if err := validateFingerprint(p); err != nil {
		return err
	}
	out["tls"] = true
	if sni := valueOr(p.ServerName, p.Address); sni != "" {
		out["servername"] = sni
	}
	if p.AllowInsecure {
		out["skip-cert-verify"] = true
	}
	if list := alpnList(p.ALPN); len(list) > 0 {
		alpn := make([]any, 0, len(list))
		for _, v := range list {
			alpn = append(alpn, v)
		}
		out["alpn"] = alpn
	}
	if fp := mihomoClientFingerprint(p); fp != "" {
		out["client-fingerprint"] = fp
	}
	if p.RealityEnabled {
		if strings.TrimSpace(p.RealityPubKey) == "" {
			return fmt.Errorf("REALITY is enabled but no public key is set")
		}
		opts := map[string]any{"public-key": p.RealityPubKey}
		if p.RealityShortID != "" {
			opts["short-id"] = p.RealityShortID
		}
		out["reality-opts"] = opts
	}
	return nil
}

// mihomoSmux renders mihomo's multiplexing block, or nil when mux is off.
// Invalid options fail here instead of at mihomo startup.
func mihomoSmux(p *Profile) (map[string]any, error) {
	if err := validateMux(p); err != nil {
		return nil, err
	}
	if p == nil || !p.MuxEnabled {
		return nil, nil
	}
	opts := map[string]any{"enabled": true, "protocol": muxProtocolOrDefault(p)}
	if p.MuxConcurrency > 0 {
		opts["max-connections"] = p.MuxConcurrency
	}
	return opts, nil
}

// mapProfileToMihomoProxy converts a Profile to one mihomo proxy object.
// It supports the same protocol set the other builders do (vless, vmess,
// trojan, shadowsocks, socks, http, aether) plus rawOutbound passthrough;
// anything else needs rawOutbound, exactly as on sing-box and Xray.
func mapProfileToMihomoProxy(p *Profile) (map[string]any, error) {
	if p == nil {
		return nil, fmt.Errorf("nil profile")
	}
	if p.Protocol == "aether" && strings.TrimSpace(p.RawOutbound) == "" {
		// Aether engine (PattNG parity): the proxy is a SOCKS connection to
		// the engine's dialed listener. A throwaway probe pins its own
		// engine port on the profile; otherwise the live published port is
		// used, and with no engine at all the default port is rendered.
		port := aetherDefaultCommandPort
		if p.AetherEnginePort > 0 {
			port = p.AetherEnginePort
		} else if live, ok := currentAetherActivePort(); ok {
			port = live
		}
		return map[string]any{
			"name": mihomoProxyName, "type": "socks5",
			"server": aetherLoopback, "port": port, "udp": true,
		}, nil
	}
	if raw := strings.TrimSpace(p.RawOutbound); raw != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, fmt.Errorf("rawOutbound: %w", err)
		}
		// JSON null decodes WITHOUT an error and leaves obj nil, so the
		// re-name below would panic on a profile whose raw outbound was
		// cleared to null in a hand-edited config.json.
		if obj == nil {
			return nil, fmt.Errorf("rawOutbound: want a JSON object, got null")
		}
		// Always re-name: the group, the MATCH rule and the latency probe
		// all address the proxy by mihomoProxyName, so a pasted name would
		// silently disconnect them.
		obj["name"] = mihomoProxyName
		return obj, nil
	}
	if p.Address == "" || p.Port <= 0 || p.Port > 65535 {
		return nil, fmt.Errorf("profile %q has no usable server address or port", p.Name)
	}
	base := map[string]any{
		"name":   mihomoProxyName,
		"server": p.Address,
		"port":   p.Port,
	}
	switch p.Protocol {
	case "vless":
		base["type"] = "vless"
		base["uuid"] = p.IDOrPassword
		base["udp"] = true
		if p.Flow != "" {
			base["flow"] = p.Flow
		}
		if p.TLS || p.RealityEnabled || p.Flow != "" {
			if err := mihomoTLS(p, base); err != nil {
				return nil, err
			}
		}
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = p.IDOrPassword
		base["alterId"] = p.AlterID
		base["cipher"] = valueOr(p.Security, "auto")
		base["udp"] = true
		if p.TLS || p.RealityEnabled {
			if err := mihomoTLS(p, base); err != nil {
				return nil, err
			}
		}
	case "trojan":
		base["type"] = "trojan"
		base["password"] = p.IDOrPassword
		base["udp"] = true
		if err := mihomoTLS(p, base); err != nil {
			return nil, err
		}
	case "shadowsocks":
		base["type"] = "ss"
		base["cipher"] = valueOr(p.Method, "aes-256-gcm")
		base["password"] = p.IDOrPassword
		base["udp"] = true
	case "socks":
		base["type"] = "socks5"
		base["udp"] = true
		addProxyAuth(base, p.IDOrPassword)
		if p.TLS {
			base["tls"] = true
			if p.AllowInsecure {
				base["skip-cert-verify"] = true
			}
		}
	case "http":
		base["type"] = "http"
		addProxyAuth(base, p.IDOrPassword)
		if p.TLS {
			base["tls"] = true
			if sni := valueOr(p.ServerName, p.Address); sni != "" {
				base["sni"] = sni
			}
			if p.AllowInsecure {
				base["skip-cert-verify"] = true
			}
		}
	default:
		return nil, fmt.Errorf("protocol %q needs rawOutbound JSON for the mihomo core", p.Protocol)
	}
	if err := mihomoTransport(p, base); err != nil {
		return nil, err
	}
	if smux, err := mihomoSmux(p); err != nil {
		return nil, err
	} else if smux != nil {
		base["smux-opts"] = smux
	}
	return base, nil
}
