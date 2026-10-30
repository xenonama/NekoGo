package main

// AetherManager owns the aether.exe child process — the pattern of
// SpoofManager: spawn + stdout relay + readiness probe, no FFI.
//
// Engine shape (PattNG port): the core binds SOCKS listeners on the
// loopback address (its own listener, plus Psiphon's and Tor's when a
// profile runs them). NekoGo picks a free port per connect, renders a
// SOCKS outbound into the main core's config dialing the listener the
// profile selects, and stops the engine with the connection.

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// aetherExeName is the Aether core binary. It resolves from the
// dedicated Settings.AetherPath override when set, and otherwise
// sits next to nekogo.exe.
const aetherExeName = "aether.exe"

// Test hooks (nil/empty in production), the SpoofManager pattern.
var (
	aetherExeOverride    string
	aetherExeDirOverride string
	aetherWorkDirHook    func() string
	aetherFreePortHook   func() (int, error)
)

// aetherWarmup is how long Start waits for the SOCKS listener to
// answer (PattNG warms up to its test budget; WARP tunnels take a few
// seconds to connect, much of it scan time on a first run).
const aetherWarmup = 30 * time.Second

// chainPsiphonDirPrefix names the per-connect private Psiphon data
// directories under the work dir. Each chain run gets its own so a
// psiphon-tunnel-core leaked by an earlier session (or by another copy
// of the app) can never hold the shared bbolt datastore and make the
// new run exit during init ("psiphon stopped before it was ready").
const chainPsiphonDirPrefix = "chain-psiphon-"

// chainPsiphonDirMaxAge bounds how long an orphaned per-connect dir is
// kept before the next chain run prunes it.
const chainPsiphonDirMaxAge = time.Hour

// AetherManager spawns aether.exe and reports readiness.
type AetherManager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	profileID string
	port      int // the port the app dials (the listener the profile selects)
	running   bool
	// token names the current spawn (incremented per Start). StopToken
	// uses it so a rollback racing a newer Start cannot kill the newer
	// engine.
	token uint64
	// dataDir is the chain Psiphon-only run's private data directory
	// (removed on stop); empty for the aether-profile instance.
	dataDir string
	logCh   chan LogEntry
	// engine names this child in exit reports and log lines ("aether",
	// or "psiphon" for the SNI-spoof chain's Psiphon-only instance).
	engine string
	// source tags this child's log lines.
	source string
	// done closes when the current child has been reaped. Stop waits on
	// it instead of calling cmd.Wait a second time (watchChild owns the
	// only Wait).
	done chan struct{}
	// job owns this spawn's process tree (Windows job object with
	// kill-on-close). taskkill /T can only walk live parent links, so a
	// supervisor that exited on its own left psiphon-tunnel-core.exe
	// holding its listener; terminating/closing the job takes every
	// descendant with it.
	job *procJob
	// recent keeps the engine's last output lines for an EngineExit.
	recent      lineRing
	startedAt   time.Time
	exitHandler func(EngineExit)
}

// NewAetherManager wires the engine manager to the shared log bus.
func NewAetherManager(logCh chan LogEntry) *AetherManager {
	return &AetherManager{logCh: logCh, engine: "aether", source: "aether"}
}

// NewPsiphonManager builds the manager instance that runs aether.exe in
// Psiphon-only mode for the SNI spoof chain. It is a separate instance
// so an aether profile and the chain can never share process state.
func NewPsiphonManager(logCh chan LogEntry) *AetherManager {
	return &AetherManager{logCh: logCh, engine: "psiphon", source: "psiphon"}
}

// SetExitHandler registers the cross-engine death report callback
// (CoreManager.handleEngineExit). Called once at wiring time.
func (m *AetherManager) SetExitHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exitHandler = fn
}

func (m *AetherManager) log(level, msg string) {
	if m.logCh != nil {
		source := m.source
		if source == "" {
			source = "aether"
		}
		select {
		case m.logCh <- LogEntry{Level: level, Source: source, Message: msg, Time: time.Now().Format(time.RFC3339)}:
		default:
		}
	}
}

// Port returns the loopback port the running engine's dialed listener
// takes, 0 when it does not run.
func (m *AetherManager) Port() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return 0
	}
	return m.port
}

// Running reports whether the engine process is up.
func (m *AetherManager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// resolveAetherExe locates aether.exe: the dedicated Settings.AetherPath
// override first, then bin\ next to nekogo.exe (shipped layout), then the
// exe's own folder (legacy portable copies). No silent fallbacks.
func (m *AetherManager) resolveAetherExe(settings Settings) (string, error) {
	if aetherExeOverride != "" {
		if _, err := os.Stat(aetherExeOverride); err == nil {
			return aetherExeOverride, nil
		}
		return "", fmt.Errorf("aether test exe not found at %q", aetherExeOverride)
	}
	if p := strings.TrimSpace(settings.AetherPath); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("aether exe not found at %q", p)
	}
	dir := aetherExeDirOverride
	if dir == "" {
		if c, ok := firstEngineFile(aetherExeName); ok {
			return c, nil
		}
		dir = exeDir()
	}
	candidate := filepath.Join(dir, aetherExeName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("aether.exe not found in bin\\ next to nekogo.exe (%s): set the Aether binary path under Advanced → Core runtime (or get it from %s)", candidate, "github.com/CluvexStudio/aether")
}

// aetherWorkDir is where the keeps its WARP identities (aether.toml and
// friends, named by the AETER*_CONFIG envs the core reads) and its
// runtime state: the GUI profile directory, subdir "aether".
func aetherWorkDir() string {
	if aetherWorkDirHook != nil {
		return aetherWorkDirHook()
	}
	base, err := os.UserConfigDir()
	if err != nil {
		// Match config.go's legacy fallback shape.
		base = "."
	}
	dir := filepath.Join(base, "NekoGo", "aether")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// aetherEnv builds the child environment (AetherCoreManager.startProcess):
// identity file paths in the work dir, HOME inside it, and Go's cert
// directory hook is not needed on Windows (the runtime reads the
// system store).
func aetherEnv(workDir string) []string {
	// Strip inherited AETHER_* / RUST_LOG / HOME / TMPDIR first: a
	// user's shell must not override the identity files or verbosity
	// this app decides (PattN IsCoreSetting).
	env := sanitizeChildEnv(os.Environ(),
		[]string{"AETHER_"},
		[]string{"RUST_LOG", "HOME", "TMPDIR"},
	)
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("AETHER_CONFIG", filepath.Join(workDir, "aether.toml"))
	set("AETHER_MASQUE_CONFIG", filepath.Join(workDir, "aether-masque.toml"))
	set("AETHER_WG_CONFIG", filepath.Join(workDir, "aether-wg.toml"))
	set("HOME", workDir)
	set("TMPDIR", workDir)
	return env
}

// Start spawns the engine for [profile] and waits until its dialed
// listener answers SOCKS. The caller renders the main core's config
// after this (aetherActivePort), and stops the engine before or with
// the main core. Any failure leaves nothing behind.
func (m *AetherManager) Start(profile *Profile, settings Settings) (int, error) {
	port, _, err := m.StartWithToken(profile, settings)
	return port, err
}

// StartWithToken is Start plus the token naming the child it spawned,
// so a connect that is aborted later can stop exactly that child
// (StopToken) instead of whatever engine the manager holds by then.
func (m *AetherManager) StartWithToken(profile *Profile, settings Settings) (int, uint64, error) {
	exe, err := m.resolveAetherExe(settings)
	if err != nil {
		return 0, 0, err
	}
	port := 0
	if aetherFreePortHook != nil {
		port, err = aetherFreePortHook()
	} else {
		// The engine may bind the same number for TCP and UDP (WARP /
		// scan listeners): a TCP-only probe can hand over a port whose
		// UDP side is taken (PattN PortsProblem probes both).
		port, err = freePortTCPAndUDP()
	}
	if err != nil {
		return 0, 0, err
	}
	args, _, _ := aetherCoreOf(profile, port)
	// The dialer is authoritative from the arguments: a hand-written
	// command keeps its own listener, settings put Psiphon's or Tor's
	// where the profile dials them.
	listen := dialPortOf(args, port)
	if aetherProfilePsiphonChain(profile) {
		m.log("info", fmt.Sprintf("psiphon chain armed: psiphon listener on %s:%d, its dials ride the tunnel",
			aetherLoopback, listen))
	}
	// A quieter level than info would hide the tunnel-up word
	// (readiness then rides the listener probe alone).
	args = append(args, "--log-level", aetherLogLevel(settings.LogLevel))
	return m.runProcess(exe, args, listen, aetherWarmup, profile.ID, "", nil)
}

// chainPsiphonSeq makes two dirs created inside the same clock tick
// distinct (Windows clock resolution is coarser than UnixNano).
var chainPsiphonSeq atomic.Uint64

// newChainPsiphonDir creates a fresh private data directory for one
// chain Psiphon run, pruning stale ones from crashed sessions first.
func newChainPsiphonDir() string {
	base := aetherWorkDir()
	pruneChainPsiphonDirs(base)
	name := chainPsiphonDirPrefix + strconv.FormatInt(time.Now().UnixNano(), 36) +
		"-" + strconv.FormatUint(chainPsiphonSeq.Add(1), 36)
	dir := filepath.Join(base, name)
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// pruneChainPsiphonDirs removes per-connect dirs a crashed or killed
// session left behind. Age-gated and best-effort, so a live run's dir
// (files locked on Windows) is never disturbed.
func pruneChainPsiphonDirs(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-chainPsiphonDirMaxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), chainPsiphonDirPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, e.Name()))
	}
}

// psiphonOnlyArgs builds aether's argv for the chain: --bind is the
// plain Psiphon SOCKS listener (psiphon-only mode has no tunnel of its
// own), and --upstream makes Psiphon dial its servers through the
// loopback inbound that routes into the spoof front. A non-empty
// [dataDir] pins Psiphon's datastore there instead of the shared
// default, so no other process can hold its lock.
func psiphonOnlyArgs(port, upstreamPort int, logLevel, dataDir string) []string {
	args := []string{
		"--bind", fmt.Sprintf("%s:%d", aetherLoopback, port),
		"--psiphon-only",
		"--upstream", fmt.Sprintf("socks5://%s:%d", aetherLoopback, upstreamPort),
		"--log-level", logLevel,
	}
	if dataDir != "" {
		args = append(args, "--psiphon-dir", dataDir)
	}
	return args
}

// StartPsiphonOnly starts aether.exe as a plain Psiphon SOCKS proxy on
// [port] for the SNI spoof chain. Readiness is data-level: Psiphon opens
// --bind and answers CONNECT before a tunnel exists, so the probe drives
// a TLS handshake through it (probePsiphonTunnel) instead of trusting the
// listener alone.
func (m *AetherManager) StartPsiphonOnly(settings Settings, port, upstreamPort int, warmup time.Duration) (int, error) {
	if port < 1 || port > 65535 || upstreamPort < 1 || upstreamPort > 65535 {
		return 0, fmt.Errorf("psiphon chain ports must be 1..65535 (got %d, %d)", port, upstreamPort)
	}
	exe, err := m.resolveAetherExe(settings)
	if err != nil {
		return 0, err
	}
	dataDir := newChainPsiphonDir()
	args := psiphonOnlyArgs(port, upstreamPort, aetherLogLevel(settings.LogLevel), dataDir)
	m.log("info", fmt.Sprintf("psiphon chain: plain psiphon on %s:%d, dialling through %s:%d (data dir %s)",
		aetherLoopback, port, aetherLoopback, upstreamPort, dataDir))
	listen, _, err := m.runProcess(exe, args, port, warmup, "", dataDir, probePsiphonTunnel)
	return listen, err
}

// runProcess owns the child from spawn to readiness for both engine
// shapes (aether profile, Psiphon-only chain). [dataDir] is the
// chain run's private Psiphon directory (empty for an aether profile);
// it is removed when the child is stopped or replaced. [ready] decides
// what "reachable" means; nil keeps the plain SOCKS-listener probe.
// The token identifies this spawn for StopToken.
func (m *AetherManager) runProcess(exe string, args []string, listen int, warmup time.Duration, profileID, dataDir string, ready func(int) bool) (int, uint64, error) {
	workDir := aetherWorkDir()
	cmd := exec.Command(exe, args...)
	cmd.Dir = workDir
	cmd.Env = aetherEnv(workDir)
	setHideWindow(cmd)
	cmd.Stdout = &aetherLogWriter{ch: m.logCh, ring: &m.recent, source: m.source}
	cmd.Stderr = &aetherLogWriter{ch: m.logCh, ring: &m.recent, source: m.source}

	m.mu.Lock()
	// A previous engine from an un-cleaned stop is reaped first so the
	// new start cannot race it. The pid is captured under the lock and
	// the tree killed after it: taskkill blocks briefly and must not
	// hold m.mu.
	oldPid := 0
	if m.running && m.cmd != nil && m.cmd.Process != nil {
		oldPid = m.cmd.Process.Pid
	}
	oldJob := m.job
	oldDir := m.dataDir
	done := make(chan struct{})
	m.cmd, m.profileID, m.port, m.running = cmd, profileID, listen, true
	m.token++
	token := m.token
	m.job = nil
	m.dataDir = dataDir
	m.done = done
	m.recent.reset()
	m.startedAt = time.Now()
	m.mu.Unlock()
	oldJob.kill()
	oldJob.close()
	if oldPid > 0 {
		killProcessTree(oldPid)
	}
	if oldDir != "" && oldDir != dataDir {
		_ = os.RemoveAll(oldDir)
	}

	job := newProcJob()
	if err := cmd.Start(); err != nil {
		job.close()
		m.mu.Lock()
		// Only clear OUR spawn: a concurrent newer Start may already own
		// the manager, and wiping its state would publish a dead engine.
		if m.cmd == cmd {
			m.cmd, m.running, m.done, m.dataDir = nil, false, nil, ""
		}
		m.mu.Unlock()
		if dataDir != "" {
			_ = os.RemoveAll(dataDir)
		}
		return 0, 0, fmt.Errorf("start %s engine: %w", m.engineName(), err)
	}
	// Join before anything else: the child starts spawning its own
	// helpers (psiphon-tunnel-core) immediately.
	job.attach(cmd.Process.Pid)
	// A Stop that landed between the state swap and cmd.Start found no
	// process to kill; it invalidated the token instead. This spawn is
	// then not the live one and must take its child down itself — nobody
	// else can (m.cmd already belongs to the stop).
	if !m.spawnCurrent(token, cmd) {
		job.kill()
		killProcessTree(cmd.Process.Pid)
		_ = cmd.Process.Kill()
		job.close()
		// watchChild (the only closer of done) starts further down, so
		// waiting on it here blocked the full 3s AND leaked the child
		// handle: nobody ever called Wait for this spawn. We own it.
		_ = cmd.Wait()
		if dataDir != "" {
			_ = os.RemoveAll(dataDir)
		}
		return 0, token, fmt.Errorf("%s engine start aborted (stop raced the spawn)", m.engineName())
	}
	m.mu.Lock()
	m.job = job
	m.mu.Unlock()
	exited := make(chan error, 1)
	go m.watchChild(cmd, exited, done)

	// Race the readiness probe against the child: an engine that dies
	// during warmup is reported at once with its own last words instead
	// of after the full timeout (PattN CouldNotStart).
	if err := m.waitSocksOrExit(listen, warmup, exited, ready); err != nil {
		// Stop only this spawn: a newer Start may own the manager by now
		// (its own spawn reaped ours), and a plain Stop would kill it and
		// clear the state it published.
		_ = m.StopToken(token)
		return 0, 0, err
	}
	if !m.spawnCurrent(token, cmd) {
		// A Stop overtook the probe result while stopChild was already
		// tearing the child down; never report this spawn as ready.
		return 0, token, fmt.Errorf("%s engine start aborted (stop raced the probe)", m.engineName())
	}
	m.log("info", fmt.Sprintf("%s listening on %s:%d", m.engineName(), aetherLoopback, listen))
	return listen, token, nil
}

// spawnCurrent reports whether [token] and [cmd] still name the live
// spawn. stopChild bumps the token under the same lock it clears the
// state, so a spawn that lost the race can tell it must clean up after
// itself even though the manager no longer references its child.
func (m *AetherManager) spawnCurrent(token uint64, cmd *exec.Cmd) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token == token && m.cmd == cmd && m.running
}

// engineName is the label this instance's child carries in logs and
// exit reports.
func (m *AetherManager) engineName() string {
	if m.engine == "" {
		return "aether"
	}
	return m.engine
}

// waitSocksOrExit waits for [ready] to report a usable child, aborting
// immediately when the child exits and quoting its last output line.
func (m *AetherManager) waitSocksOrExit(port int, timeout time.Duration, exited <-chan error, ready func(int) bool) error {
	if ready == nil {
		ready = answersSocksOnce
	}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("%s engine exited during startup: %v%s", m.engineName(), err, lastWordsSuffix(m.recent.tail(1)))
		default:
		}
		if ready(port) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s engine on port %d did not become ready in %s%s",
				m.engineName(), port, timeout, lastWordsSuffix(m.recent.tail(1)))
		}
		select {
		case err := <-exited:
			return fmt.Errorf("%s engine exited during startup: %v%s", m.engineName(), err, lastWordsSuffix(m.recent.tail(1)))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// dialPortOf returns the port of the listener [args] tell the app to
// dial ([listen] when it names none).
func dialPortOf(args []string, listen int) int {
	if l, ok := aetherPortAfter(args, aetherListenerFlagOf(args)); ok {
		return l
	}
	return listen
}

// aetherActivePort is the live engine dial port published by
// CoreManager before the main core starts, the spoofFrontPort
// pattern: the outbound builders read it to render the SOCKS outbound
// that dials the engine; 0 means "no engine".
var aetherActivePort atomic.Int32

func setAetherActivePort(p int) { aetherActivePort.Store(int32(p)) }

func clearAetherActivePort() { aetherActivePort.Store(0) }

// clearAetherActivePortIf unpublishes [p] only while it is still the
// published port, so a rollback cannot hide a newer engine's port.
func clearAetherActivePortIf(p int) { aetherActivePort.CompareAndSwap(int32(p), 0) }

// currentAetherActivePort returns the published engine dial port.
func currentAetherActivePort() (port int, ok bool) {
	p := int(aetherActivePort.Load())
	if p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

// watchChild owns Wait so the engine is always reaped (BUG-005 pattern).
// A committed generation that exits without a Stop becomes an EngineExit;
// Start/Stop races clear running first and are reported as a clean stop.
func (m *AetherManager) watchChild(cmd *exec.Cmd, exited chan<- error, done chan<- struct{}) {
	err := cmd.Wait()
	close(done)
	if exited != nil {
		select {
		case exited <- err:
		default:
		}
	}
	m.mu.Lock()
	current := m.cmd == cmd && m.running
	dataDir := ""
	var job *procJob
	if m.cmd == cmd {
		m.running = false
		m.port = 0
		// Only the chain instance publishes one, and the child is reaped
		// here: an exit without Stop must not leave the dir behind.
		dataDir = m.dataDir
		m.dataDir = ""
		// A supervisor that exited on its own still owns a live tree:
		// psiphon-tunnel-core.exe survives it and keeps serving. Take the
		// job down here — a later Stop sees a dead pid and cannot.
		job = m.job
		m.job = nil
	}
	handler := m.exitHandler
	started := m.startedAt
	m.mu.Unlock()
	job.kill()
	job.close()
	if dataDir != "" {
		_ = os.RemoveAll(dataDir)
	}
	if !current {
		m.log("info", fmt.Sprintf("%s engine stopped", m.engineName()))
		return
	}
	ev := newEngineExit(m.engineName(), cmd, err, started, m.recent.tail(engineLastLines))
	m.log("warn", ev.Summary())
	if handler != nil {
		handler(ev)
	}
}

// Stop ends the engine, SIGTERM-style first (Windows: Kill) and waits.
// It clears running before signalling, so watchChild reports a clean stop
// rather than a crash.
func (m *AetherManager) Stop() error {
	return m.stopChild(0)
}

// StopToken stops the child only while [token] still names the current
// spawn (StartWithToken's token). A connect aborted after a newer Start
// replaced the engine must not kill the newer one; a stale token is a
// no-op.
func (m *AetherManager) StopToken(token uint64) error {
	if token == 0 {
		return nil
	}
	return m.stopChild(token)
}

// stopChild reaps the current child (any child when expected is 0). The
// token check and the state swap happen under one lock, so a rollback
// can never kill a spawn that replaced its own.
func (m *AetherManager) stopChild(expected uint64) error {
	m.mu.Lock()
	if expected != 0 && m.token != expected {
		m.mu.Unlock()
		return nil
	}
	cmd := m.cmd
	done := m.done
	job := m.job
	m.running = false
	m.port = 0
	m.profileID = ""
	m.cmd = nil
	m.done = nil
	m.job = nil
	m.token++
	dataDir := m.dataDir
	m.dataDir = ""
	m.mu.Unlock()
	// The chain's private dir goes only after the child is reaped below:
	// deleting it while psiphon-tunnel-core still writes fails anyway.
	defer func() {
		if dataDir != "" {
			_ = os.RemoveAll(dataDir)
		}
	}()
	// The job first: it ends the whole tree in one call, including a
	// psiphon-tunnel-core whose supervisor link taskkill could not walk.
	job.kill()
	if cmd == nil || cmd.Process == nil {
		job.close()
		return nil
	}
	// Then the taskkill tree (defence in depth on Windows; the only
	// tree kill elsewhere) and the supervisor as a last resort.
	killProcessTree(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	if done == nil {
		// Defensive: a manager assembled without Start has no reaper.
		done = make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		m.log("warn", fmt.Sprintf("%s engine did not end on request; left to notice its pipe gone", m.engineName()))
	}
	job.close()
	// Only the aether-profile instance publishes that port; the Psiphon
	// chain's listener is private (the chain config names it directly).
	if m.engineName() != "psiphon" {
		clearAetherActivePort()
	}
	return nil
}

// aetherLevel maps the GUI log level onto the levels the core accepts
// (AetherCoreManager.coreLogLevel); info is kept so readiness words
// reach the relay.
func aetherLogLevel(appLevel string) string {
	switch strings.ToLower(strings.TrimSpace(appLevel)) {
	case "debug":
		return "debug"
	case "info":
		return "info"
	case "warning", "warn":
		return "warn"
	case "error", "none":
		return "error"
	default:
		return "info"
	}
}

// aetherLogWriter relays the engine's stdout into the log viewer at the
// priority the core's own header carries (AetherCoreManager.outputPriority).
type aetherLogWriter struct {
	ch     chan LogEntry
	ring   *lineRing
	source string
}

func (w *aetherLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\r\n"), "\n") {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if w.ring != nil {
			w.ring.push(text)
		}
		w.emit(text)
	}
	return len(p), nil
}

func (w *aetherLogWriter) emit(line string) {
	level, msg := aetherLinePriority(line)
	if w.ch == nil {
		return
	}
	source := w.source
	if source == "" {
		source = "aether"
	}
	select {
	case w.ch <- LogEntry{Level: level, Source: source, Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
	}
}

// aetherLinePriority reads a core log line's "[ts LEVEL aether] msg"
// header (AetherCoreManager.logHeader) for the viewer's level, and
// strips the header when it is one.
func aetherLinePriority(line string) (level, msg string) {
	levels := map[string]bool{"ERROR": true, "WARN": true, "INFO": true, "DEBUG": true, "TRACE": true}
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end > 0 {
			parts := strings.Fields(line[1:end])
			if len(parts) >= 3 && levels[parts[1]] {
				lvl := parts[1]
				switch lvl {
				case "ERROR":
					lvl = "error"
				case "WARN":
					lvl = "warn"
				case "DEBUG", "TRACE":
					lvl = "debug"
				default:
					lvl = "info"
				}
				return lvl, strings.TrimSpace(line[end+1:])
			}
		}
	}
	if strings.HasPrefix(line, "Error:") {
		return "error", line
	}
	return "info", line
}

// answersSocks probes a SOCKS server on loopback [port] the way PattNG
// does: the greeting is taken and answered with the SOCKS version byte.
// Retries every poll interval up to [timeout].
func answersSocks(port int, timeout time.Duration) bool {
	greeting := []byte{5, 1, 0}
	deadline := time.Now().Add(timeout)
	for {
		ok := probeSocksOnce(port, greeting)
		if ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// answersSocksOnce is the single-shot probe (PattNG answersSocks).
func answersSocksOnce(port int) bool {
	return probeSocksOnce(port, []byte{5, 1, 0})
}

func probeSocksOnce(port int, greeting []byte) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", aetherLoopback, port), time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.Write(greeting); err != nil {
		return false
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil {
		return false
	}
	return buf[0] == 5
}

// aetherPortFree reports whether loopback [port] might still be bound
// by nothing — a stale engine check used at start (PattNG reaps stale
// processes; on Windows we read what a fresh bind would find instead).
func aetherPortFree(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", aetherLoopback, port), 300*time.Millisecond)
	if err != nil {
		return true
	}
	_ = c.Close()
	// Something answered: bound.
	return false
}
