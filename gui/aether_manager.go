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
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// aetherListenerFreeHook stands in for the pre-spawn listener check
	// when a test owns the engine's listener itself.
	aetherListenerFreeHook func(port int) error
)

// aetherWarmup is how long Start waits for the SOCKS listener to
// answer (PattNG warms up to its test budget; WARP tunnels take a few
// seconds to connect, much of it scan time on a first run).
const aetherWarmup = 30 * time.Second

// aetherReadyPoll is how often the readiness probe asks the listener
// (PattN AetherCoreManager.ReadyPoll).
var aetherReadyPoll = 500 * time.Millisecond

// chainPsiphonDirPrefix names the per-connect private Psiphon data
// directories under the work dir. Each chain run gets its own so a
// psiphon-tunnel-core leaked by an earlier session (or by another copy
// of the app) can never hold the shared bbolt datastore and make the
// new run exit during init ("psiphon stopped before it was ready").
const chainPsiphonDirPrefix = "chain-psiphon-"

// chainTorDirPrefix names the per-connect private Tor data directories
// (--tor-dir). Tor keeps its directory cache and state there, and two
// runs sharing one contend for it the way two Psiphon runs contend for
// the bbolt datastore.
const chainTorDirPrefix = "chain-tor-"

// chainDataDirMaxAge bounds how long an orphaned per-connect chain dir is
// kept before the next chain run prunes it.
const chainDataDirMaxAge = time.Hour

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
	// stalled quotes the core's own give-up line, kept apart from the
	// ring because a run that goes quiet after saying it is stuck keeps
	// the only useful line for the whole readiness wait.
	stalled atomic.Pointer[string]
	// giveUp is the reason this run cannot carry traffic, read out of the
	// core's own words: its carrier inside the tunnel ended, it had no
	// ECH key to offer, or it stopped on a fatal error. rescan marks the
	// one reason a restart can still save — the gateway --peer forces is
	// dead, so the rescan the core announces selects nothing.
	giveUp atomic.Pointer[aetherGiveUp]
	// carrier is the name of the listener the app dials when a carrier
	// inside the tunnel owns it ("tor", "psiphon"), empty otherwise.
	carrier atomic.Pointer[string]
	// forcesPeer is whether this run's command pins an endpoint, which
	// is what makes the core's own rescan a no-op when it goes dead.
	forcesPeer atomic.Bool
	// lastLine is when the core last printed, the bound on a run that
	// goes quiet: a working core keeps saying what it is doing.
	lastLine atomic.Int64
	// bootPct is how far a standalone tor.exe bootstrap got, read out of
	// its "Bootstrapped N%" lines. It gates readiness (the daemon opens
	// its SOCKS port at 0%) and it is the only progress a run that stops
	// printing has to report. Zero for every other engine.
	bootPct atomic.Int32
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

// NewTorManager builds the manager instance that runs aether.exe in
// Tor-only mode for the SNI spoof chain. Its own instance for the same
// reason as the Psiphon one, and so its log lines and exit reports say
// "tor" while a Tor chain is what is running.
func NewTorManager(logCh chan LogEntry) *AetherManager {
	return &AetherManager{logCh: logCh, engine: "tor", source: "tor"}
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

// noteIdleBridgeLines warns when the run about to start carries none of
// the bridge lines that are saved. Both Tor legs call it with the argv
// they are handing the engine and the lines that were in reach, so the
// warning describes that run and not a setting the user cannot see.
func (m *AetherManager) noteIdleBridgeLines(argv []string, lines string) {
	if note := bridgeLinesIdleNote(argv, lines); note != "" {
		m.log("warn", note)
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
// override first (a directory hosting aether.exe, or a file), then bin\
// next to nekogo.exe (shipped layout), then the exe's own folder (legacy
// portable copies). No silent fallbacks.
func (m *AetherManager) resolveAetherExe(settings Settings) (string, error) {
	if aetherExeOverride != "" {
		if _, err := os.Stat(aetherExeOverride); err == nil {
			return aetherExeOverride, nil
		}
		return "", fmt.Errorf("aether test exe not found at %q", aetherExeOverride)
	}
	if p := strings.TrimSpace(settings.AetherPath); p != "" {
		if info, err := os.Stat(p); err == nil {
			if info.IsDir() {
				c := filepath.Join(p, aetherExeName)
				if _, err := os.Stat(c); err == nil {
					return c, nil
				}
				return "", fmt.Errorf("aether exe not found in %q (want %s)", p, aetherExeName)
			}
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

// aetherTorTransports are the pluggable-transport binaries aether.exe
// launches for Tor bridges. arti is linked into the engine, so Tor
// itself needs nothing beside it — but every bridge the Tor Project
// hands out today is an obfs4 or a webtunnel line, and both are
// lyrebird's. Without it aether still starts Tor and then fails to spawn
// a transport for each bridge, which reads as a bootstrap stall rather
// than as a missing file.
var aetherTorTransports = []string{"lyrebird.exe"}

// aetherPtDir is where aether.exe looks for those transports: pt\ beside
// the binary, which is the layout build.bat stages and the release zip
// ships (bin\aether.exe with bin\pt\lyrebird.exe next to it).
func aetherPtDir(exe string) string { return filepath.Join(filepath.Dir(exe), "pt") }

// missingTorTransports names the pt\ binaries [exe] cannot find.
func missingTorTransports(exe string) []string {
	if exe == "" {
		return nil
	}
	var out []string
	for _, name := range aetherTorTransports {
		if !exeExists(filepath.Join(aetherPtDir(exe), name)) {
			out = append(out, name)
		}
	}
	return out
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
	port, args, listen, custom, err := pickAetherEnginePort(profile)
	if err != nil {
		return 0, 0, err
	}
	if aetherProfilePsiphonChain(profile) {
		m.log("info", fmt.Sprintf("psiphon chain armed: psiphon listener on %s:%d, its dials ride the tunnel",
			aetherLoopback, listen))
	}
	// A hand-written command owns its own bridges, so only a run
	// rendered from settings can be told it ignored the ones it saved.
	if !custom {
		m.noteIdleBridgeLines(args, profile.Aether.TorBridgeLines)
	}
	warmup := aetherEngineWarmup(args)
	if warmup > aetherWarmup {
		carrier := "psiphon"
		if aetherTorModeOf(args) != "off" {
			carrier = "tor"
		}
		m.log("info", fmt.Sprintf("%s is in the dialled path: a round of %s without a word from it ends the wait", carrier, warmup))
	}
	// A quieter level than info would hide the tunnel-up word
	// (readiness then rides the listener probe alone).
	level := aetherLogLevel(settings.LogLevel)
	args = append(append([]string{}, args...), "--log-level", level)
	listen, token, err := m.runProcess(exe, args, listen, warmup, profile.ID, "", nil)
	if errors.Is(err, errAetherForcedPeerDead) && !custom {
		// The core announces a rescan and then re-uses the forced peer
		// ("probe skipped"), so a dead saved endpoint repeats until the
		// run is killed. Run once more without the pin, as the app's own
		// Scan does: the gateway is selected afresh. The profile keeps
		// what the user saved; only this run looks elsewhere.
		m.log("warn", fmt.Sprintf("the saved endpoint %s:%d no longer responds; scanning for a gateway afresh",
			profile.Address, profile.Port))
		scanArgs := append(buildAetherScanArguments(profile, port), "--log-level", level)
		return m.runProcess(exe, scanArgs, dialPortOf(scanArgs, port), aetherEngineWarmup(scanArgs), profile.ID, "", nil)
	}
	return listen, token, err
}

// aetherEngineWarmup is the readiness budget of one engine run. The core
// opens Tor's listener only at the end of a bootstrap, and a reverse run
// exposes the tunnel only once circuits exist, so either waits on the
// same cold consensus fetch the Tor chain leg does — minutes, where the
// plain budget ends a connect that is still going fine. Psiphon's wait is
// shorter but of the same kind: it says nothing at all while it fetches
// tactics, which a filtered network stretches past the plain round. The
// engine's own bootstrap percentage streams while it waits.
func aetherEngineWarmup(argv []string) time.Duration {
	if aetherTorModeOf(argv) != "off" {
		return torChainWarmup
	}
	if aetherPsiphonModeOf(argv) != "off" {
		return psiphonChainWarmup
	}
	return aetherWarmup
}

// chainDataDirSeq makes two dirs created inside the same clock tick
// distinct (Windows clock resolution is coarser than UnixNano).
var chainDataDirSeq atomic.Uint64

// newChainDataDir creates a fresh private data directory for one chain
// run under [prefix], pruning stale ones from crashed sessions first.
func newChainDataDir(prefix string) string {
	base := aetherWorkDir()
	pruneChainDataDirs(base, prefix)
	name := prefix + strconv.FormatInt(time.Now().UnixNano(), 36) +
		"-" + strconv.FormatUint(chainDataDirSeq.Add(1), 36)
	dir := filepath.Join(base, name)
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// newChainPsiphonDir creates the Psiphon chain run's own data directory
// (--psiphon-dir), so no other process can hold its lock.
func newChainPsiphonDir() string { return newChainDataDir(chainPsiphonDirPrefix) }

// newChainTorDir creates the Tor chain run's own data directory
// (--tor-dir), for the same reason.
func newChainTorDir() string { return newChainDataDir(chainTorDirPrefix) }

// pruneChainDataDirs removes per-connect dirs a crashed or killed
// session left behind. Age-gated and best-effort, so a live run's dir
// (files locked on Windows) is never disturbed.
func pruneChainDataDirs(base, prefix string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-chainDataDirMaxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
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
// The Chain tab's Psiphon options are rendered through the same
// psiphonOptionArgs an Aether profile uses, so a country or fronting
// list picked on that tab applies to this leg too.
func psiphonOnlyArgs(port, upstreamPort int, logLevel, dataDir string, settings Settings) []string {
	args := []string{
		"--bind", fmt.Sprintf("%s:%d", aetherLoopback, port),
		"--psiphon-only",
		"--upstream", fmt.Sprintf("socks5://%s:%d", aetherLoopback, upstreamPort),
		"--log-level", logLevel,
	}
	args = append(args, psiphonOptionArgs(settings.ChainPsiphonMode, settings.ChainPsiphonCDN,
		settings.ChainPsiphonSNI, settings.ChainPsiphonSets, settings.ChainPsiphonRegion)...)
	args = append(args, psiphonBundledArgs(nil)...)
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
	args := psiphonOnlyArgs(port, upstreamPort, aetherLogLevel(settings.LogLevel), dataDir, settings)
	m.log("info", fmt.Sprintf("psiphon chain: plain psiphon on %s:%d, dialling through %s:%d (data dir %s)",
		aetherLoopback, port, aetherLoopback, upstreamPort, dataDir))
	listen, _, err := m.runProcess(exe, args, port, warmup, "", dataDir, probePsiphonTunnel)
	return listen, err
}

// torOnlyArgs builds aether's argv for the Tor chain: --bind is the
// plain Tor SOCKS listener (--tor-only runs no tunnel of its own), and
// --upstream makes Tor dial its relays through the loopback inbound that
// routes into the spoof front — aether's --upstream applies to tor and
// psiphon alike when no tunnel carries them, and tor takes a socks5
// address only, which is what the chain publishes.
// The Chain tab's Tor options are rendered through the same
// torOptionArgs an Aether profile uses, so bridges picked on that tab
// apply to this leg too.
func torOnlyArgs(port, upstreamPort int, logLevel, dataDir string, settings Settings) []string {
	args := []string{
		"--bind", fmt.Sprintf("%s:%d", aetherLoopback, port),
		"--tor-only",
		"--upstream", fmt.Sprintf("socks5://%s:%d", aetherLoopback, upstreamPort),
		"--log-level", logLevel,
	}
	args = append(args, torOptionArgs(settings.ChainTorBridges, settings.ChainTorBridgeLines,
		settings.ChainTorRelays)...)
	if dataDir != "" {
		args = append(args, "--tor-dir", dataDir)
	}
	return args
}

// StartTorOnly starts aether.exe as a plain Tor SOCKS proxy on [port]
// for the SNI spoof chain. Readiness is data-level for the same reason
// it is for Psiphon: Tor's listener answers a CONNECT while the circuit
// is still being built, so the probe drives a TLS handshake through it
// (probePsiphonTunnel) instead of trusting the listener alone.
func (m *AetherManager) StartTorOnly(settings Settings, port, upstreamPort int, warmup time.Duration) (int, error) {
	if port < 1 || port > 65535 || upstreamPort < 1 || upstreamPort > 65535 {
		return 0, fmt.Errorf("tor chain ports must be 1..65535 (got %d, %d)", port, upstreamPort)
	}
	exe, err := m.resolveAetherExe(settings)
	if err != nil {
		return 0, err
	}
	dataDir := newChainTorDir()
	args := torOnlyArgs(port, upstreamPort, aetherLogLevel(settings.LogLevel), dataDir, settings)
	m.log("info", fmt.Sprintf("tor chain: plain tor on %s:%d, dialling through %s:%d (data dir %s)",
		aetherLoopback, port, aetherLoopback, upstreamPort, dataDir))
	if budget := chainTorOnlyWarmup(warmup); budget != warmup {
		m.log("info", fmt.Sprintf("tor chain: this run's --tor-dir is new, so its bootstrap has no cached consensus to ride and waits %s instead of %s",
			budget, warmup))
		warmup = budget
	}
	m.noteIdleBridgeLines(args, settings.ChainTorBridgeLines)
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
	cmd.Stdout = m.linePipe()
	cmd.Stderr = m.linePipe()

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
	m.clearStall()
	m.bootPct.Store(0)
	m.giveUp.Store(nil)
	carrier := ""
	switch aetherListenerFlagOf(args) {
	case aetherFlagTorBind:
		carrier = "tor"
	case aetherFlagPsiphonBind:
		carrier = "psiphon"
	}
	m.carrier.Store(&carrier)
	m.forcesPeer.Store(containsStr(args, "--peer"))
	m.lastLine.Store(time.Now().UnixNano())
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

	// The listener is the one thing this run cannot start without. A port
	// taken here — by the child just reaped, still releasing its socket,
	// or by another program — would otherwise cost the whole readiness
	// budget (minutes for a Tor hop) before the connect said so, in words
	// naming a timeout instead of the port the user has to act on.
	if listen > 0 && listen <= 65535 {
		check := waitEngineListenerFree
		if aetherListenerFreeHook != nil {
			check = aetherListenerFreeHook
		}
		if err := check(listen); err != nil {
			m.mu.Lock()
			if m.cmd == cmd {
				m.cmd, m.running, m.done, m.dataDir = nil, false, nil, ""
			}
			m.mu.Unlock()
			if dataDir != "" {
				_ = os.RemoveAll(dataDir)
			}
			return 0, 0, err
		}
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
	if err := m.waitSocksOrExit(listen, warmup, exited, ready, func() bool { return m.spawnCurrent(token, cmd) }); err != nil {
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

// aetherGiveUp is why a run cannot carry traffic, in the core's words.
type aetherGiveUp struct {
	reason string
	rescan bool
}

// errAetherForcedPeerDead reports that the gateway --peer forces no
// longer responds. The core announces a rescan and then re-uses the
// forced peer ("probe skipped"), so only a run without it can recover.
var errAetherForcedPeerDead = errors.New("aether forced peer no longer responds")

// carrierName is the carrier inside the tunnel whose listener this run
// dials, empty when the app dials the core's own.
func (m *AetherManager) carrierName() string {
	if p := m.carrier.Load(); p != nil {
		return *p
	}
	return ""
}

// forcedPeer reports whether this run's command pins an endpoint.
func (m *AetherManager) forcedPeer() bool { return m.forcesPeer.Load() }

// waitSocksOrExit waits for [ready] to report a usable child, in rounds
// of [round] for as long as [live] holds: a scan may take minutes, and a
// core that is still connecting must not be killed on a wall clock
// (PattN AetherCoreManager.AwaitWarmUpAsync waits "round after round
// while it lives"). The bounds are the child itself — it exits, it says
// it cannot go on, or this spawn stops being the current run.
func (m *AetherManager) waitSocksOrExit(port int, round time.Duration, exited <-chan error, ready func(int) bool, live func() bool) error {
	if ready == nil {
		ready = answersSocksOnce
	}
	if live == nil {
		live = func() bool { return true }
	}
	exitedErr := func(err error) error {
		return fmt.Errorf("%s engine exited during startup: %v%s", m.engineName(), err, lastWordsSuffix(m.recent.tail(1)))
	}
	start := time.Now()
	next := start.Add(round)
	for live() {
		select {
		case err := <-exited:
			return exitedErr(err)
		default:
		}
		if g := m.giveUp.Load(); g != nil {
			if g.rescan {
				return errAetherForcedPeerDead
			}
			return fmt.Errorf("%s engine on port %d gave up: %s", m.engineName(), port, g.reason)
		}
		// The core's own give-up word ends the wait at once. A run that
		// has gone quiet for a whole round is the other kind of stall: a
		// standalone tor.exe stops printing while its circuit stays
		// unbuilt, and its last bootstrap percentage is all it said.
		if m.stalled.Load() != nil || time.Since(m.lastLineAt()) >= round {
			if notice := m.stallNotice(); notice != "" {
				return fmt.Errorf("%s engine on port %d cannot make progress: %s", m.engineName(), port, notice)
			}
		}
		if ready(port) {
			return nil
		}
		if time.Now().After(next) {
			m.log("info", fmt.Sprintf("%s still connecting on %s:%d (%s elapsed)",
				m.engineName(), aetherLoopback, port, time.Since(start).Round(time.Second)))
			next = time.Now().Add(round)
		}
		select {
		case err := <-exited:
			return exitedErr(err)
		case <-time.After(aetherReadyPoll):
		}
	}
	return fmt.Errorf("%s engine on port %d is no longer the current run", m.engineName(), port)
}

// lastLineAt is when the core last printed (the spawn itself counts).
func (m *AetherManager) lastLineAt() time.Time {
	return time.Unix(0, m.lastLine.Load())
}

// noteGiveUp reads one core line for the words that end this run
// (PattN AetherLogLine.ChainEndWords / IsNoEchKeyWord / IsFatalError).
func (m *AetherManager) noteGiveUp(line, carrier string) {
	if words := aetherChainEndWords(line, carrier); words != "" {
		m.giveUp.Store(&aetherGiveUp{reason: fmt.Sprintf(
			"%s inside the tunnel ended (%s); the listener the app dials no longer carries traffic", carrier, words)})
		return
	}
	if aetherNoEchKeyWord(line) {
		m.giveUp.Store(&aetherGiveUp{reason: "ECH is on but the core had no ECH key to offer, so it stopped " +
			"rather than send the server name in the clear; turn ECH off, or name an ECH resolver that can fetch the key"})
		return
	}
	if aetherFatalErrorLine(line) {
		m.giveUp.Store(&aetherGiveUp{reason: line})
		return
	}
	if aetherDeadForcedPeer(line) && m.forcedPeer() {
		m.giveUp.Store(&aetherGiveUp{reason: "the saved endpoint no longer responds", rescan: true})
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

// aetherListenerPorts returns every port [args] tells the engine to
// listen on (PattN AetherCore.ports: "an inbound of the configuration
// cannot share one").
func aetherListenerPorts(args []string) []int {
	var out []int
	seen := map[int]bool{}
	for _, flag := range []string{aetherFlagBind, aetherFlagTorBind, aetherFlagPsiphonBind} {
		p, ok := aetherPortAfter(args, flag)
		if !ok || p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// aetherPortPickAttempts bounds the search for a whole free listener
// range: the numbers after the dial port are listeners too, so a
// candidate is only usable when every one of them can be bound.
const aetherPortPickAttempts = 8

// pickAetherEnginePort picks the engine's dial port and renders the
// command for it, refusing a candidate whose other listeners are taken.
func pickAetherEnginePort(p *Profile) (port int, args []string, listen int, custom bool, err error) {
	held := 0
	for attempt := 0; attempt < aetherPortPickAttempts; attempt++ {
		if aetherFreePortHook != nil {
			port, err = aetherFreePortHook()
		} else {
			port, err = freePortTCPAndUDP()
		}
		if err != nil {
			return 0, nil, 0, false, err
		}
		args, listen, custom = aetherCoreOf(p, port)
		// A hand-written command owns its exits: they were preflighted
		// before any child, another pick renders the same numbers, and the
		// pre-spawn check gives a socket still releasing its grace window.
		if custom {
			return port, args, listen, custom, nil
		}
		ports := aetherListenerPorts(args)
		held = 0
		for _, l := range ports {
			if connectPortReserved(l) || !aetherListenerBindable(l) {
				held = l
				break
			}
		}
		if held == 0 {
			claimPickedConnectPorts(ports...)
			return port, args, listen, custom, nil
		}
	}
	return 0, nil, 0, false, bindPortHeldError(
		net.JoinHostPort(aetherLoopback, strconv.Itoa(held)), enginePortRemedy)
}

// aetherListenerBindable reports whether the engine can still bind
// [port] on both protocols: it listens on TCP for SOCKS and on UDP for
// the tunnel, so a free TCP side alone is not a free listener.
func aetherListenerBindable(port int) bool {
	if aetherListenerFreeHook != nil {
		return aetherListenerFreeHook(port) == nil
	}
	return portFreeTCPAndUDP(port)
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
	port := 0
	var job *procJob
	if m.cmd == cmd {
		port = m.port
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
	// This process' listener died with it, so the port it published goes
	// away here — by value, because a connect that raced the crash may
	// already have published a newer one, which CoreManager's fault path
	// used to clear on sight. Only the aether-profile instance publishes:
	// the chain's Psiphon listener is private and named in its config.
	if m.engineName() != "psiphon" {
		clearAetherActivePortIf(port)
	}
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
	port := m.port
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
	// By value: the reap above can take three seconds, and a connect that
	// raced it may already have published a newer engine's port.
	if m.engineName() != "psiphon" {
		clearAetherActivePortIf(port)
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
	// m, when set, records the core's give-up lines (noteStall) and its
	// bootstrap progress (noteBootstrap) on it.
	m *AetherManager
}

// linePipe builds the writer both of the child's streams go through.
func (m *AetherManager) linePipe() *aetherLogWriter {
	return &aetherLogWriter{ch: m.logCh, ring: &m.recent, source: m.source, m: m}
}

func (w *aetherLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\r\n"), "\n") {
		text := strings.TrimSpace(stripANSI(line))
		if text == "" {
			continue
		}
		if w.ring != nil {
			w.ring.push(text)
		}
		if w.m != nil {
			w.m.lastLine.Store(time.Now().UnixNano())
			w.m.noteStall(text)
			w.m.noteBootstrap(text)
			w.m.noteGiveUp(text, w.m.carrierName())
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

// aetherLogLevels are the levels a core log header may name.
var aetherLogLevels = map[string]bool{"ERROR": true, "WARN": true, "INFO": true, "DEBUG": true, "TRACE": true}

// aetherLogHeader returns the words of a core line's "[ts LEVEL target]"
// header, nil when it has none (PattN AetherLogLine.LogHeader).
func aetherLogHeader(line string) []string {
	if !strings.HasPrefix(line, "[") {
		return nil
	}
	end := strings.Index(line, "]")
	if end <= 0 {
		return nil
	}
	words := strings.Fields(line[1:end])
	if len(words) < 3 || !aetherLogLevels[words[1]] {
		return nil
	}
	return words
}

// aetherLogMessage is the line trimmed and without its header (PattN
// AetherLogLine.OutputMessage).
func aetherLogMessage(line string) string {
	if aetherLogHeader(line) == nil {
		return strings.TrimSpace(line)
	}
	return strings.TrimSpace(line[strings.Index(line, "]")+1:])
}

// aetherLinePriority reads a core log line's "[ts LEVEL aether] msg"
// header (AetherCoreManager.logHeader) for the viewer's level, and
// strips the header when it is one.
func aetherLinePriority(line string) (level, msg string) {
	if header := aetherLogHeader(line); header != nil {
		lvl := header[1]
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
		body := aetherLogMessage(line)
		return aetherMarkerLevel(lvl, body), body
	}
	if aetherFatalErrorLine(line) {
		return "error", line
	}
	return aetherMarkerLevel("info", line), line
}

// aetherMarkerLevel raises the core's give-up marker to a warn. Its
// status lines say [+] worked, [*] is progress and [-] it gave up, and
// the last of those reached the console at info — indistinguishable from
// progress while a connect sat waiting for a listener that never came.
func aetherMarkerLevel(level, msg string) string {
	if level == "info" && strings.HasPrefix(msg, "[-]") {
		return "warn"
	}
	return level
}

// torStuckRe matches the core's directory-bootstrap give-up status line,
// which reports the percentage it could not get past:
// "[-] tor over your bridges: Stuck at 15%: Can't bootstrap a Tor
// directory. (Can't make progress.)"
var torStuckRe = regexp.MustCompile(`(?i)\btor\b.*Stuck at (\d+)%: (.+)`)

// noteStall remembers a give-up line for the failure report. Tor keeps
// its own counsel while it retries the directory download, so the line
// that says why is often the last thing the core prints before NekoGo
// gives up on the run — and with a chatty log level it can scroll out of
// the ring's twelve-line tail, which is why it is kept on its own.
func (m *AetherManager) noteStall(line string) {
	if notice := torStallNotice(line); notice != "" {
		m.stalled.Store(&notice)
	}
}

func (m *AetherManager) stallNotice() string {
	if p := m.stalled.Load(); p != nil {
		return *p
	}
	// A standalone tor.exe that gives up says nothing: it stops printing
	// while its circuit stays unbuilt. Its last Bootstrapped line is then
	// the only thing that says how far the run got, and quoting the tail
	// of the log instead would report a stalled bootstrap as an engine
	// that merely never answered.
	if pct := m.bootPct.Load(); pct > 0 && pct < 100 {
		return fmt.Sprintf("%s reached %d%% of its bootstrap and stopped there: no circuit was built, "+
			"so nothing reaches its listener. Check the front's reachability, or the bridge lines "+
			"under Chain → Tor.", torExeName, pct)
	}
	return ""
}

func (m *AetherManager) clearStall() { m.stalled.Store(nil) }

// torStallNotice puts the core's own words into a sentence that names
// what the user can still change. Empty for every other line.
func torStallNotice(line string) string {
	groups := torStuckRe.FindStringSubmatch(line)
	if groups == nil {
		return ""
	}
	return fmt.Sprintf("tor stalled at %s%%: %s — its bridges carry the handshake but no "+
		"directory comes back through them; try other bridge lines, or let the engine fetch "+
		"its own (https://bridges.torproject.org).",
		groups[1], strings.TrimSpace(groups[2]))
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
	// The whole method selection, not its first byte: two probes of
	// loopback ports nothing listens on can be joined by a TCP
	// simultaneous open, and each then reads the other's greeting, which
	// begins with the version as well (PattN AnswersSocksAsync).
	buf := make([]byte, 2)
	if _, err := io.ReadFull(c, buf); err != nil {
		return false
	}
	if buf[0] != 5 {
		return false
	}
	for _, method := range greeting[2:] {
		if buf[1] == method {
			return true
		}
	}
	return false
}

// aetherChainEndWords returns the core's words as "tor: ..." or
// "psiphon: ..." when [line] says the carrier whose listener this run
// dials has ended or could not start, and "" for every other line
// (PattN AetherLogLine.ChainEndWords). [carrier] is that carrier's name,
// empty when the app dials the core's own listener. A core before 2.3.4
// goes on running without that listener, so the line is the only word
// that the session is dead.
func aetherChainEndWords(line, carrier string) string {
	if carrier == "" {
		return ""
	}
	header := aetherLogHeader(line)
	if header == nil || header[1] != "ERROR" || header[2] != "aether" {
		return ""
	}
	prefix := "[-] " + carrier + ": "
	msg := aetherLogMessage(line)
	if !strings.HasPrefix(msg, prefix) {
		return ""
	}
	return carrier + ": " + aetherErrorLabelRe.ReplaceAllString(msg[len(prefix):], "")
}

// aetherErrorLabelRe is the kind of error the core labels its words with
// (aether error.rs, AetherError); the label is not part of the reason.
var aetherErrorLabelRe = regexp.MustCompile(`^(?:io|quic|h3|tls|ech|masque|capsule|api|other|identity refused): `)

// aetherNoEchKeyWord reports the core's word that it did not start for
// want of an ECH key: ECH is on and it had no key to offer, so it
// stopped rather than send the server name in the clear (PattN
// AetherLogLine.IsNoEchKeyWord).
func aetherNoEchKeyWord(line string) bool {
	return strings.Contains(line, "ECH is on but there is no ECH key to offer")
}

// aetherFatalErrorLine reports the error the core stopped on, which its
// main writes last as `Error: Variant("...")` (PattN
// AetherLogLine.IsFatalError).
func aetherFatalErrorLine(line string) bool {
	return strings.HasPrefix(line, "Error:")
}

// aetherDeadForcedPeerRe matches the core's word that the gateway it was
// told to dial is dead. Measured on 2.3.0: it says this and then logs
// "using forced peer ... (probe skipped)" and dials the same gateway
// again, so the rescan it announces selects nothing.
var aetherDeadForcedPeerRe = regexp.MustCompile(`last known-good gateway \S+ no longer responds; rescanning`)

// aetherDeadForcedPeer reports the core's word that the gateway it was
// told to dial no longer responds and it is rescanning. While --peer
// forces that gateway the rescan selects nothing ("probe skipped"), so
// the run repeats the same dead endpoint until it is killed.
func aetherDeadForcedPeer(line string) bool {
	return aetherDeadForcedPeerRe.MatchString(line)
}
