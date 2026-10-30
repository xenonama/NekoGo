package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/apache/thrift/lib/go/thrift"

	gen "nekogo/thriftgen"
)

// SingBoxManager starts nekobox_core.exe as a child process and talks to it
// over its Apache Thrift RPC interface (core/server/gen/libcore.thrift).
//
// The core is launched as:
//
//	nekobox_core core -address 127.0.0.1 -port <free-port>
//
// and serves LibcoreService on that TCP address.
type SingBoxManager struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	client  *gen.LibcoreServiceClient
	trans   thrift.TTransport
	addr    string
	running bool
	// stopRequested marks an in-flight Stop. The core can exit from the
	// Stop RPC before running is cleared, and that exit is a clean stop,
	// not an EngineExit.
	stopRequested bool
	// pathMu guards corePath alone (BUG-022): SaveSettings swaps it
	// while CoreVersion/resolveCore read it. A dedicated lock (not mu,
	// which Start holds across its multi-second probe) keeps settings
	// saves from blocking behind a hung connect.
	pathMu   sync.RWMutex
	corePath string
	logCh    chan LogEntry
	// recent keeps the child's last output lines for an EngineExit.
	recent      lineRing
	startedAt   time.Time
	exitHandler func(EngineExit)
	// Ephemeral test instance: fully separate process/transport so a
	// latency probe never disturbs the active connection above.
	ephCmd     *exec.Cmd
	ephClient  *gen.LibcoreServiceClient
	ephTrans   thrift.TTransport
	ephAddr    string
	ephRunning bool
	// lastProbeURL remembers the most recent Test-RPC probe target so
	// QueryURLTest rows (which carry no URL) can be stamped. Guarded
	// by mu like the rest.
	lastProbeURL string
	// startEpoch invalidates in-flight Start/EphemeralRun probes: Stop
	// (or a superseding Start) bumps it, and the probe commits its
	// running state only if the epoch is unchanged (BUG-004). Guarded
	// by mu.
	startEpoch uint64
	ephEpoch   uint64
	// traffic is NekoGo's cumulative traffic ledger. The core's own
	// counter drops whenever its closed-connection window slides (see
	// traffic_total.go), so the GUI keeps the authoritative total.
	traffic trafficTotals
}

var thriftConf = &thrift.TConfiguration{
	ConnectTimeout: 3 * time.Second,
	SocketTimeout:  30 * time.Second,
	MaxMessageSize: 1024 * 1024 * 50,
}

// thriftProtoFact builds request/response protocols for the shared client.
var thriftProtoFact = thrift.NewTBinaryProtocolFactoryConf(thriftConf)

// dialRPCAddr opens a fresh Thrift transport+client to addr.
func dialRPCAddr(addr string) (thrift.TTransport, *gen.LibcoreServiceClient, error) {
	trans := thrift.NewTSocketConf(addr, thriftConf)
	if trans == nil {
		return nil, nil, fmt.Errorf("create thrift socket for %s", addr)
	}
	if err := trans.Open(); err != nil {
		return nil, nil, fmt.Errorf("open thrift transport: %w", err)
	}
	return trans, gen.NewLibcoreServiceClientFactory(trans, thriftProtoFact), nil
}

// isTransportErr reports whether err looks like a broken transport (as
// opposed to an application-level RPC error, which must NOT trigger a
// reconnect).
func isTransportErr(err error) bool {
	if err == nil {
		return false
	}
	var texc thrift.TTransportException
	if errors.As(err, &texc) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// withClient runs fn against the current client, reopening the transport
// exactly once on transport-level failure. Application-level errors pass
// through untouched. No reconnect storm: one retry per call.
func (m *SingBoxManager) withClient(fn func(*gen.LibcoreServiceClient) error) error {
	m.mu.Lock()
	client, trans, addr := m.client, m.trans, m.addr
	m.mu.Unlock()
	if client == nil || trans == nil {
		return fmt.Errorf("transport not initialized")
	}
	callErr := fn(client)
	if callErr == nil {
		return nil
	} else if !isTransportErr(callErr) {
		return callErr
	}
	m.log("warn", "[rpc] transport error, reconnecting")
	newTrans, newClient, dialErr := dialRPCAddr(addr)
	if dialErr != nil {
		m.log("error", fmt.Sprintf("[rpc] reconnect failed: %v", dialErr))
		return callErr
	}
	m.mu.Lock()
	if m.trans == trans {
		// Still our generation: swap it in.
		if m.trans != nil {
			_ = m.trans.Close()
		}
		m.trans, m.client = newTrans, newClient
	} else {
		// A concurrent call already reconnected; drop ours.
		_ = newTrans.Close()
		newClient = m.client
	}
	m.mu.Unlock()
	m.log("info", "[rpc] reconnect ok")
	if newClient == nil {
		return callErr
	}
	return fn(newClient)
}

// NewSingBoxManager creates a manager. corePath may be empty (auto-detect:
// settings override, then executable directory, then PATH).
func NewSingBoxManager(corePath string, logCh chan LogEntry) *SingBoxManager {
	return &SingBoxManager{corePath: corePath, logCh: logCh}
}

// SetExitHandler registers the cross-engine death report callback
// (CoreManager.handleEngineExit). Called once at wiring time.
func (m *SingBoxManager) SetExitHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exitHandler = fn
}

func (m *SingBoxManager) log(level, msg string) {
	if m.logCh == nil {
		return
	}
	// Best-effort: the pump writes a file per entry, so a slow/dead
	// LogOutputPath fills the channel, and several call sites (Start's
	// commit, Stop) hold m.mu — a blocking send would freeze the manager
	// and every poll behind it.
	select {
	case m.logCh <- LogEntry{Level: level, Source: "sing-box", Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
	}
}

// getCorePath reads the settings corePath override under pathMu.
func (m *SingBoxManager) getCorePath() string {
	m.pathMu.RLock()
	defer m.pathMu.RUnlock()
	return m.corePath
}

// setCorePath swaps the settings corePath override under pathMu.
func (m *SingBoxManager) setCorePath(p string) {
	m.pathMu.Lock()
	defer m.pathMu.Unlock()
	m.corePath = p
}

// resolveCore finds nekobox_core.exe (the custom Thrift core): the
// settings override, then bin\ next to nekogo.exe (shipped layout), then
// the exe's own folder (legacy portable copies), then PATH. It never
// resolves the official sing-box CLI: that binary answers no Thrift RPC
// and belongs to Settings.UpstreamCorePath.
func (m *SingBoxManager) resolveCore() (string, error) {
	if p := m.getCorePath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			if isOfficialSingBoxName(p) {
				return "", fmt.Errorf("corePath %q is the official sing-box CLI; the custom engine needs nekobox_core.exe (use upstreamCorePath for the official binary)", p)
			}
			return p, nil
		}
		return "", fmt.Errorf("core not found at %q", p)
	}
	if c, ok := firstEngineFile("nekobox_core.exe", "nekobox_core"); ok {
		return c, nil
	}
	if p, err := exec.LookPath("nekobox_core"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("nekobox_core.exe"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("nekobox_core executable not found (place it in bin\\ next to nekogo.exe or set corePath in settings)")
}

// freePort implements bind-then-handover: bind 127.0.0.1:0, capture the
// kernel-assigned port, close immediately and hand the port number to the
// manager. Residual TOCTOU window: another process could bind the port
// between Close and the core's listen. Unavoidable without SO_REUSEPORT
// gymnastics or fd-passing; callers treat "address in use" startup
// failures as retryable errors.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// freePortTCPAndUDP picks a loopback port free for both protocols. The
// Aether engine can bind the same number for TCP and UDP (WARP listener,
// scan sockets), so a TCP-only probe is not enough (PattN PortsProblem
// probes both). The TOCTOU window after Close remains, as with freePort.
func freePortTCPAndUDP() (int, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		port, err := freePort()
		if err != nil {
			return 0, err
		}
		u, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = u.Close()
			return port, nil
		}
		lastErr = err
	}
	return 0, fmt.Errorf("no loopback port free for TCP and UDP after 5 attempts: %w", lastErr)
}

// startAborted reports whether the given Start epoch was invalidated by
// a concurrent Stop (or a superseding Start). Callers must NOT hold m.mu.
func (m *SingBoxManager) startAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startEpoch != epoch
}

// Start launches the core with the given profile and loads the generated
// sing-box config via RPC Start.
//
// Lifecycle (BUG-004): only fast setup runs under m.mu. The multi-second
// readiness probe and Start RPC run UNLOCKED so Stop, GetStats and
// IsRunning stay responsive; the running state commits under lock only
// when our epoch is still current, otherwise the child is torn down and
// Start reports an abort instead of a stale success.
func (m *SingBoxManager) Start(profile *Profile, settings Settings) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("sing-box already running")
	}
	m.startEpoch++
	epoch := m.startEpoch
	m.stopRequested = false
	core, err := m.resolveCore()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	port, err := freePort()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cfg, err := BuildSingBoxConfig(profile, settings)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	cmd := exec.Command(core, "core", "-address", "127.0.0.1", "-port", fmt.Sprint(port))
	setHideWindow(cmd)
	// Advanced Phase H: pass the GUI log level to the child core env
	// (best-effort; a child that ignores it gets a one-time warning).
	cmd.Env = childEnvWithLogLevel(os.Environ(), settings.LogLevel, func(msg string) {
		m.log("warn", msg)
	})
	cmd.Stdout = &logWriter{ch: m.logCh, source: "sing-box", ring: &m.recent}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "sing-box", ring: &m.recent}
	m.recent.reset()
	m.startedAt = time.Now()
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("start nekobox_core: %w", err)
	}
	// watchChild owns Wait() from here on; no other path may Wait on
	// this cmd. The exited channel turns an instant crash into a fast
	// error instead of a full 15s RPC spin.
	exited := make(chan error, 1)
	go m.watchChild(cmd, exited)
	m.mu.Unlock()

	// Wait for the RPC socket (no lock held: Stop bumps the epoch and
	// returns promptly; we notice below).
	deadline := time.Now().Add(15 * time.Second)
	for {
		if m.startAborted(epoch) {
			_ = cmd.Process.Kill()
			return fmt.Errorf("sing-box start aborted (concurrent stop)")
		}
		select {
		case err := <-exited:
			_ = cmd.Process.Kill()
			if m.startAborted(epoch) {
				return fmt.Errorf("sing-box start aborted (concurrent stop)")
			}
			return fmt.Errorf("nekobox_core exited during startup: %v", err)
		default:
		}
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			if m.startAborted(epoch) {
				return fmt.Errorf("sing-box start aborted (concurrent stop)")
			}
			return fmt.Errorf("nekobox_core RPC not ready at %s", addr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	trans, client, err := dialRPCAddr(addr)
	if err != nil {
		_ = cmd.Process.Kill()
		if m.startAborted(epoch) {
			return fmt.Errorf("sing-box start aborted (concurrent stop)")
		}
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	resp, err := client.Start(ctx, &gen.LoadConfigReq{CoreConfig: cfg})
	cancel()
	if err != nil {
		trans.Close()
		_ = cmd.Process.Kill()
		if m.startAborted(epoch) {
			return fmt.Errorf("sing-box start aborted (concurrent stop)")
		}
		return fmt.Errorf("core Start RPC: %w", err)
	}
	if resp.GetError() != "" {
		rpcErr := fmt.Errorf("core Start: %s", resp.GetError())
		trans.Close()
		_ = cmd.Process.Kill()
		if m.startAborted(epoch) {
			return fmt.Errorf("sing-box start aborted (concurrent stop)")
		}
		return rpcErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startEpoch != epoch {
		// Stop ran during our probe/RPC: tear down and refuse to
		// commit a stale running state.
		trans.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("sing-box start aborted (concurrent stop)")
	}
	m.cmd = cmd
	m.client = client
	m.trans = trans
	m.addr = addr
	m.running = true
	// A new core session starts a new traffic ledger: the counters of
	// the previous session are gone with the previous process.
	m.traffic.reset()
	m.log("info", fmt.Sprintf("nekobox_core started (profile %q)", profile.Name))
	return nil
}

// watchChild reaps the child and flips running=false on unexpected death.
// It owns cmd.Wait(): Start failure paths only Kill (never Wait), and
// Stop only signals. The (buffered) exited channel, when non-nil,
// receives the Wait result for the startup probe.
func (m *SingBoxManager) watchChild(cmd *exec.Cmd, exited chan<- error) {
	err := cmd.Wait()
	// Signal BEFORE taking the mutex: the startup probe holds m.mu
	// while waiting on this channel, so locking first would deadlock
	// the early-death report into the full 15s RPC spin.
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
	if exited != nil {
		select {
		case exited <- err:
		default:
		}
	}
	if !current {
		m.log("info", "nekobox_core stopped")
		return
	}
	ev := newEngineExit("sing-box", cmd, err, started, m.recent.tail(engineLastLines))
	m.log("warn", ev.Summary())
	if handler != nil {
		handler(ev)
	}
}

// Stop unloads the config and kills the core process.
// watchChild owns Wait(); Stop only signals and waits for the reaping.
// It bumps startEpoch first so an in-flight Start probe aborts instead
// of committing behind us (BUG-004); Stop itself never blocks on the
// probe because Start no longer holds the lock while probing.
func (m *SingBoxManager) Stop() error {
	m.mu.Lock()
	m.startEpoch++
	m.stopRequested = true
	cmd := m.cmd
	trans := m.trans
	addr := m.addr
	m.mu.Unlock()
	// The unload RPC goes over a THROWAWAY transport: m.client's socket is
	// shared with the 1s stats poll, and two concurrent Thrift calls on one
	// TSocket corrupt both streams (see nettools.go). A failed unload is
	// harmless — the Kill below ends the core anyway.
	if addr != "" {
		if t, c, err := dialRPCAddr(addr); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = c.Stop(ctx, &gen.EmptyReq{})
			cancel()
			_ = t.Close()
		}
	}
	if trans != nil {
		trans.Close()
		m.mu.Lock()
		m.client = nil
		m.trans = nil
		m.mu.Unlock()
	}
	if cmd == nil || cmd.Process == nil {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
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
	m.mu.Unlock()
	m.log("info", "nekobox_core stopped")
	return nil
}

// ephAborted reports whether the given ephemeral epoch was invalidated
// by a concurrent StopEphemeral (or a superseding EphemeralRun).
// Callers must NOT hold m.mu.
func (m *SingBoxManager) ephAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ephEpoch != epoch
}

// watchEphemeral reaps a crashed ephemeral core (BUG-005). It owns
// cmd.Wait(); explicit teardown paths only Kill. If the instance is
// still current, the ephemeral state is cleared under lock so neither
// a zombie handle nor a stale "running" flag survives the crash.
func (m *SingBoxManager) watchEphemeral(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	current := m.ephCmd == cmd && m.ephRunning
	if current {
		if m.ephTrans != nil {
			m.ephTrans.Close()
			m.ephClient = nil
			m.ephTrans = nil
		}
		m.ephCmd = nil
		m.ephRunning = false
	}
	m.mu.Unlock()
	if current {
		m.log("warn", fmt.Sprintf("ephemeral nekobox_core exited unexpectedly: %v", err))
	}
}

// EphemeralRun starts a temporary core with only the profile outbound and
// a mixed inbound on 127.0.0.1:mixedPort (0 = pick a free port, reported
// back as the return value). No TUN, no system proxy. The active
// connection is untouched: all state lives in the eph* fields. A second
// call stops the previous ephemeral instance first.
//
// Like Start, the probe runs UNLOCKED with an epoch guard (BUG-004), and
// a watcher owns Wait from cmd.Start on so a crash is always reaped
// (BUG-005).
func (m *SingBoxManager) EphemeralRun(profile *Profile, settings Settings, mixedPort int) (int, error) {
	// Tear down any previous instance outside the lock first. The fresh
	// epoch for THIS run is captured after setup below (under lock),
	// so a StopEphemeral racing the teardown cannot spuriously abort
	// the new probe.
	m.mu.Lock()
	oldCmd, oldTrans, oldClient := m.stopEphemeralLocked()
	m.mu.Unlock()
	teardownEphemeral(oldCmd, oldTrans, oldClient)

	m.mu.Lock()
	if mixedPort <= 0 {
		p, err := freePort()
		if err != nil {
			m.mu.Unlock()
			return 0, err
		}
		mixedPort = p
	}
	rpcPort, err := distinctPort(mixedPort)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	core, err := m.resolveCore()
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	es := settings
	es.MixedPort = mixedPort
	es.TunEnabled = false
	cfg, err := BuildSingBoxConfig(profile, es)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", rpcPort)
	cmd := exec.Command(core, "core", "-address", "127.0.0.1", "-port", fmt.Sprint(rpcPort))
	setHideWindow(cmd)
	cmd.Env = childEnvWithLogLevel(os.Environ(), settings.LogLevel, func(msg string) {
		m.log("warn", msg)
	})
	cmd.Stdout = &logWriter{ch: m.logCh, source: "sing-box"}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "sing-box"}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return 0, fmt.Errorf("start ephemeral nekobox_core: %w", err)
	}
	// epoch was bumped by stopEphemeralLocked at entry and the lock has
	// been held since setup, so capturing it here is both fresh and
	// unique; the watcher owns Wait from this point on.
	epoch := m.ephEpoch
	go m.watchEphemeral(cmd)
	m.mu.Unlock()

	kill := func() {
		_ = cmd.Process.Kill()
		// No Wait here: watchEphemeral owns it.
	}
	abortErr := fmt.Errorf("ephemeral sing-box start aborted (superseded)")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if m.ephAborted(epoch) {
			kill()
			return 0, abortErr
		}
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			kill()
			if m.ephAborted(epoch) {
				return 0, abortErr
			}
			return 0, fmt.Errorf("ephemeral nekobox_core RPC not ready at %s", addr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	trans := thrift.NewTSocketConf(addr, thriftConf)
	if trans == nil {
		kill()
		if m.ephAborted(epoch) {
			return 0, abortErr
		}
		return 0, fmt.Errorf("create ephemeral thrift socket for %s", addr)
	}
	if err := trans.Open(); err != nil {
		kill()
		if m.ephAborted(epoch) {
			return 0, abortErr
		}
		return 0, fmt.Errorf("open ephemeral thrift transport: %w", err)
	}
	protoFact := thrift.NewTBinaryProtocolFactoryConf(thriftConf)
	client := gen.NewLibcoreServiceClientFactory(trans, protoFact)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	resp, err := client.Start(ctx, &gen.LoadConfigReq{CoreConfig: cfg})
	cancel()
	if err != nil {
		trans.Close()
		kill()
		if m.ephAborted(epoch) {
			return 0, abortErr
		}
		return 0, fmt.Errorf("ephemeral core Start RPC: %w", err)
	}
	if resp.GetError() != "" {
		rpcErr := fmt.Errorf("ephemeral core Start: %s", resp.GetError())
		trans.Close()
		kill()
		if m.ephAborted(epoch) {
			return 0, abortErr
		}
		return 0, rpcErr
	}
	if err := waitForPort(fmt.Sprintf("127.0.0.1:%d", mixedPort), 10*time.Second); err != nil {
		_, _ = client.Stop(context.Background(), &gen.EmptyReq{})
		trans.Close()
		kill()
		if m.ephAborted(epoch) {
			return 0, abortErr
		}
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ephEpoch != epoch {
		trans.Close()
		_ = cmd.Process.Kill()
		return 0, abortErr
	}
	m.ephCmd = cmd
	m.ephClient = client
	m.ephTrans = trans
	m.ephAddr = addr
	m.ephRunning = true
	return mixedPort, nil
}

// StopEphemeral stops the ephemeral instance only; the active connection
// (if any) is unaffected. Teardown runs outside the lock; the watcher
// owns Wait.
func (m *SingBoxManager) StopEphemeral() {
	m.mu.Lock()
	cmd, trans, client := m.stopEphemeralLocked()
	m.mu.Unlock()
	teardownEphemeral(cmd, trans, client)
}

// stopEphemeralLocked detaches the ephemeral instance (epoch bump plus
// state clear) and returns it for teardown outside the lock. The epoch
// bump invalidates any in-flight EphemeralRun probe. Caller must hold
// m.mu; caller must NOT hold it while tearing down.
func (m *SingBoxManager) stopEphemeralLocked() (cmd *exec.Cmd, trans thrift.TTransport, client *gen.LibcoreServiceClient) {
	m.ephEpoch++
	if !m.ephRunning {
		return nil, nil, nil
	}
	cmd, trans, client = m.ephCmd, m.ephTrans, m.ephClient
	m.ephCmd, m.ephTrans, m.ephClient = nil, nil, nil
	m.ephRunning = false
	return cmd, trans, client
}

// teardownEphemeral runs the detached ephemeral teardown (Stop RPC,
// transport close, Kill) without holding m.mu. It never Waits: the
// watchEphemeral goroutine owns Wait and reaps in the background.
func teardownEphemeral(cmd *exec.Cmd, trans thrift.TTransport, client *gen.LibcoreServiceClient) {
	if client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = client.Stop(ctx, &gen.EmptyReq{})
		cancel()
	}
	if trans != nil {
		trans.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// IsRunning reports whether the core process is active.
func (m *SingBoxManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// GetStats queries QueryStats and aggregates totals.
//
// The core's counter is NOT monotonic: QueryStats sums the live
// connections plus the traffic manager's closed-connection window, and
// a connection evicted from that window disappears from the answer (its
// bytes stay in the core's private totals). The raw value therefore
// drops - the "cumulative" counter used to reset to ~0 mid-transfer.
// The GUI keeps its own cumulative ledger (traffic_total.go) and
// reports that instead, so the counter only ever grows.
func (m *SingBoxManager) GetStats() (*Stats, error) {
	m.mu.Lock()
	running := m.running
	hasClient := m.client != nil
	m.mu.Unlock()
	if !running || !hasClient {
		return &Stats{Running: false, Core: "sing-box"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var resp *gen.QueryStatsResp
	if err := m.withClient(func(client *gen.LibcoreServiceClient) error {
		r, err := client.QueryStats(ctx, &gen.EmptyReq{})
		if err != nil {
			return err
		}
		resp = r
		return nil
	}); err != nil {
		// RPC failed (withClient already retried once on transport
		// errors). When the child is still alive the API is down but
		// the connection may be fine: flag it instead of erroring so
		// the UI warns on stale counters rather than blanking them.
		m.mu.Lock()
		alive := m.running && m.cmd != nil && m.cmd.Process != nil
		m.mu.Unlock()
		if alive {
			return &Stats{Running: true, Core: "sing-box", ApiDown: true}, nil
		}
		return nil, err
	}
	rawUp := make(map[string]int64, len(resp.GetUps()))
	for tag, v := range resp.GetUps() {
		rawUp[tag] = v
	}
	rawDown := make(map[string]int64, len(resp.GetDowns()))
	for tag, v := range resp.GetDowns() {
		rawDown[tag] = v
	}
	up, down, upByTag, downByTag := m.traffic.observe(rawUp, rawDown)
	return &Stats{Running: true, Core: "sing-box", Uplink: up, Downlink: down,
		UplinkByTag: upByTag, DownlinkByTag: downByTag}, nil
}

// CheckConfig validates a generated config without starting.
func (m *SingBoxManager) CheckConfig(profile *Profile, settings Settings) error {
	cfg, err := BuildSingBoxConfig(profile, settings)
	if err != nil {
		return err
	}
	m.mu.Lock()
	hasClient := m.client != nil
	m.mu.Unlock()
	if !hasClient {
		// No running core: bare JSON sanity check only.
		var v any
		return json.Unmarshal([]byte(cfg), &v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var resp *gen.ErrorResp
	if err := m.withClient(func(client *gen.LibcoreServiceClient) error {
		r, err := client.CheckConfig(ctx, &gen.LoadConfigReq{CoreConfig: cfg})
		if err != nil {
			return err
		}
		resp = r
		return nil
	}); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return fmt.Errorf("invalid config: %s", resp.GetError())
	}
	return nil
}

// URLTest probes url through the RUNNING core's default outbound via the
// Test RPC. It requires a live connection; use EphemeralURLTest otherwise.
//
// Concurrency: uses a dedicated transport (sbTestDial), never the shared
// m.client/m.trans, so it cannot interleave Thrift frames with the 1s
// GetStats poll (BUG-001).
func (m *SingBoxManager) URLTest(url string, timeoutMs int32) ([]TestResult, error) {
	if url == "" {
		url = defaultTestURL
	}
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	// BUG-020: clamp before any arithmetic (int64 math + clamped value
	// on the wire, so a crafted huge timeout cannot wrap the context
	// nor arm a gigantic server-side timeout).
	timeoutMs = clampProbeTimeoutMs(timeoutMs)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(int64(timeoutMs)+5000)*time.Millisecond)
	defer cancel()
	tResp, err := client.Test(ctx, &gen.TestReq{
		TestCurrent:   true,
		URL:           url,
		TestTimeoutMs: timeoutMs,
	})
	if err != nil {
		return nil, err
	}
	var out []TestResult
	for _, r := range tResp.GetResults() {
		out = append(out, TestResult{OutboundTag: r.GetOutboundTag(), LatencyMs: r.GetLatencyMs(), Error: r.GetError()})
	}
	return out, nil
}

// ListConnections returns active connections (sing-box only) plus whether
// the server capped the list (P3-3: most recent 500, Truncated=true).
func (m *SingBoxManager) ListConnections() (*ConnectionList, error) {
	m.mu.Lock()
	client := m.client
	running := m.running
	m.mu.Unlock()
	if !running || client == nil {
		return &ConnectionList{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lcResp *gen.ListConnectionsResp
	if err := m.withClient(func(client *gen.LibcoreServiceClient) error {
		r, err := client.ListConnections(ctx, &gen.EmptyReq{})
		if err != nil {
			return err
		}
		lcResp = r
		return nil
	}); err != nil {
		return nil, err
	}
	out := &ConnectionList{}
	if lcResp != nil {
		out.Truncated = lcResp.GetTruncated()
		for _, c := range lcResp.GetConnections() {
			out.Connections = append(out.Connections, Connection{
				ID: c.GetID(), Outbound: c.GetOutbound(), Network: c.GetNetwork(),
				Dest: c.GetDest(), Protocol: c.GetProtocol(), Domain: c.GetDomain(),
				Process: c.GetProcess(), Upload: c.GetUpload(), Download: c.GetDownload(),
			})
		}
	}
	return out, nil
}

// BuildSingBoxConfig renders a minimal sing-box config for a profile.
// Profiles carrying RawOutbound JSON have it merged as the first outbound.
//
// External chaining: when auxiliary processes (see external.go) run
// with ChainTo="proxy", the "proxy" outbound resolved below is
// wrapped by chainSingBoxProxy: it is retagged "proxy-origin" and a
// new SOCKS "proxy" outbound pointing at 127.0.0.1:<external port>
// (detouring to the origin) takes its place, so traffic flows
// proxy -> external -> origin. Route final stays "proxy". This is a
// pure config-time substitution — no core manager is involved. A
// stored full raw override (Phase G) replaces the whole config and
// skips chaining, so ordering stays deterministic.
func BuildSingBoxConfig(profile *Profile, settings Settings) (string, error) {
	// Global transport overrides (settings fragment/fingerprint):
	// render-time copy, stored profile untouched.
	profile = applyGlobalTransportOverrides(profile, settings)
	outbound := map[string]any{"tag": "proxy", "type": "direct"}
	if profile.RawOutbound != "" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(profile.RawOutbound), &raw); err != nil {
			return "", fmt.Errorf("rawOutbound: %w", err)
		}
		// Always re-tag: route.final names "proxy", so a pasted outbound
		// carrying its own tag ("vless-out") made the engine abort with
		// "default outbound not found: proxy" — and the SNI front / chain
		// rewrites look the tag up by name too.
		raw["tag"] = "proxy"
		outbound = raw
	} else if profile.Protocol != "" && profile.Protocol != "direct" {
		mapped, err := mapProfileToSingBoxOutbound(profile)
		if err != nil {
			return "", err
		}
		outbound = mapped
	}
	// Phase G: a stored full raw override is authoritative — it
	// replaces the whole rendered config (section overrides and the
	// TUN append below are skipped) so ordering is deterministic.
	if full, ok, err := rawCoreOverride(settings.CoreOverrides); err != nil {
		return "", err
	} else if ok {
		data, err := json.Marshal(full)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	mixedPort := settings.MixedPort
	if mixedPort <= 0 {
		mixedPort = 2080
	}
	if mixedPort > 65534 {
		return "", fmt.Errorf("mixedPort %d leaves no room for the SOCKS port (must be <= 65534)", mixedPort)
	}
	listen := inboundListenAddr(settings)
	// DNS routing depends on the mode. Without TUN the only DNS packets
	// reaching the route are a proxied lookup, so the historical direct
	// path stays. With TUN every query captured on the tunnel matches
	// this first rule: sending it "direct" leaks it to the ISP resolver
	// outside the tunnel (the "search works, YouTube does not" symptom —
	// poisoned answers resolve to dead CDN IPs), so TUN hijacks DNS into
	// the sing-box DNS module instead.
	// The Psiphon chain always exposes itself through the TUN inbound:
	// that is what routes the system's traffic into Psiphon.
	tunOn := tunActiveFor(profile, settings)
	dnsRule := map[string]any{"protocol": "dns", "outbound": "direct"}
	if tunOn {
		dnsRule = map[string]any{"protocol": "dns", "action": "hijack-dns"}
	}
	rules := []any{dnsRule}
	if tunOn {
		// auto_route also routes packets addressed to the TUN's own
		// address back into the outbound, which can loop a packet and
		// pin a CPU core. Drop them before anything else (PattN
		// SingboxRoutingService's TUN self-address guard).
		rules = append([]any{map[string]any{
			"ip_cidr": []any{tunSelfAddress},
			"action":  "reject",
			"method":  "drop",
		}}, rules...)
	}
	route := map[string]any{
		"rules": rules,
		"final": "proxy",
	}
	if tunOn {
		// auto_route installs a default route pointing at the TUN;
		// without interface detection sing-box cannot tell which
		// interface to dial from and loops proxied traffic back into
		// the tunnel it just created.
		route["auto_detect_interface"] = true
	}
	cfg := map[string]any{
		"log": map[string]any{"level": singLogLevel(settings.LogLevel), "timestamp": true},
		"inbounds": []any{
			map[string]any{
				"type": "mixed", "tag": "mixed-in",
				"listen": listen, "listen_port": mixedPort,
			},
		},
		"outbounds": chainSingBoxProxy(outbound),
		"route":     route,
	}
	// SNI-spoof front (sni_front.go): CoreSpoof always rides the
	// local engine listener; other cores only when loopback-addressed
	// while an engine runs. server_name/Host are never touched.
	if spoofPort, ok := spoofPortForProfile(profile); ok {
		applySNIFrontSingBox(cfg, spoofPort)
	}
	// SNI spoof → Psiphon: Psiphon's outbound, the loopback inbound its
	// own dials ride, and the inbound-matched route rules.
	if spoofPsiphonRendering(profile, settings) {
		renderSpoofPsiphonChain(cfg, route)
	}
	// Optional upstream DNS (Throne parity): rendered only when the
	// user set at least one server, so default proxy-mode configs keep
	// the previous system-DNS behavior byte-for-byte.
	dns, dnsOK, err := singDNSBlock(settings)
	if err != nil {
		return "", err
	}
	if dnsOK {
		cfg["dns"] = dns
	} else if tunOn {
		// TUN leaves no system resolver to fall back on (every query
		// is hijacked into the module), so it needs a server of its
		// own. The user's explicit DnsRemote/DnsDirect wins above;
		// this default only fills the gap.
		cfg["dns"] = singTunDefaultDNSBlock(dnsDetourForOutbound(outbound))
	}
	if tunOn {
		// CPU audit #1: the stack is a setting, defaulting to "system".
		// gVisor keeps every packet in user space and costs 2-5x the
		// CPU per byte on Windows; it stays available for users who
		// need its NAT behaviour, but it is no longer the default.
		cfg["inbounds"] = append(cfg["inbounds"].([]any), map[string]any{
			"type": "tun", "tag": "tun-in", "stack": normalizeTunStack(settings.TunStack),
			"interface_name": tunInterfaceName,
			"address":        tunAddress, "mtu": tunMTU,
			"auto_route": true, "strict_route": false,
		})
	}
	// Phase E: merge stored section overrides (route/dns/inbound/
	// experimental) over the rendered defaults before writing.
	if err := mergeCoreOverrides(cfg, settings.CoreOverrides, false); err != nil {
		return "", err
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// tunAddress is the TUN interface address; tunSelfAddress is the same
// host as a /32 for the route guard that drops traffic addressed to the
// tunnel itself.
const (
	tunAddress     = "172.19.0.1/24"
	tunSelfAddress = "172.19.0.1/32"
)

// tunInterfaceName names the Wintun adapter explicitly (PattN parity).
// Without it the adapter gets a driver-generated name, which makes it
// unidentifiable from the OS side — and the TUN traffic readout
// (tun_traffic.go) finds the adapter by this name. "nekogo_tun" rather
// than PattN's "singbox_tun" so two sing-box-based apps can run at once.
const tunInterfaceName = "nekogo_tun"

// tunMTU is the MTU rendered into the TUN inbound. Left out, sing-box
// defaults to 9000 (protocol/tun/inbound.go), which is far above the
// real path MTU of most Windows uplinks (PPPoE/DSL ~1492, WireGuard
// and mobile less): oversized packets are dropped or fragmented, so
// heavy pages (YouTube) stall while tiny ones (search) still load.
// 1350 leaves room for the usual tunnel overhead; Advanced -> Config ->
// Inbound still overrides it for unusual links.
const tunMTU = 1350

// tunStackDefault is the TUN network stack used when settings.tunStack
// is empty or unrecognized. "system" hands packets to the OS stack;
// gVisor ("gvisor") parses every packet again in user space and costs
// roughly 2-5x the CPU per byte on Windows, so it is opt-in.
const tunStackDefault = "system"

// normalizeTunStack returns the canonical stack value: one of
// system | gvisor | mixed, or tunStackDefault for empty/unknown input.
// Defensive on purpose: SaveSettings validates, but hand-edited and
// legacy configs (and backups predating the setting) flow through here.
func normalizeTunStack(stack string) string {
	switch v := strings.ToLower(strings.TrimSpace(stack)); v {
	case "system", "gvisor", "mixed":
		return v
	}
	return tunStackDefault
}

// validTunStack reports whether stack is an accepted settings.tunStack
// value. Empty is accepted: it means "use the default".
func validTunStack(stack string) bool {
	switch strings.ToLower(strings.TrimSpace(stack)) {
	case "", "system", "gvisor", "mixed":
		return true
	}
	return false
}

// inboundListenAddr resolves the local listen address: the stored
// value when valid, loopback otherwise (defensive — SaveSettings
// validates, but hand-edited or legacy configs flow through here).
func inboundListenAddr(s Settings) string {
	if addr, err := normalizeInboundAddress(s.InboundAddress); err == nil {
		return addr
	}
	return "127.0.0.1"
}

// singDNSBlock renders an optional sing-box dns block from the
// user-configured upstreams. ok=false when neither is set (no block
// at all — system-DNS behavior unchanged). With only one side set,
// it becomes the final resolver. Unsupported values are an error: the
// old silent fallback to a public DoH resolver sent every lookup to a
// third party the user never chose.
func singDNSBlock(s Settings) (map[string]any, bool, error) {
	remote := strings.TrimSpace(s.DnsRemote)
	direct := strings.TrimSpace(s.DnsDirect)
	if remote == "" && direct == "" {
		return nil, false, nil
	}
	servers := []any{}
	final := ""
	if remote != "" {
		entry, err := parseDNSAddr(remote)
		if err != nil {
			return nil, false, fmt.Errorf("Upstream DNS: %w", err)
		}
		entry["tag"] = "dns-remote"
		servers = append(servers, entry)
		final = "dns-remote"
	}
	if direct != "" {
		entry, err := parseDNSAddr(direct)
		if err != nil {
			return nil, false, fmt.Errorf("Direct DNS: %w", err)
		}
		entry["tag"] = "dns-direct"
		// No detour: the engine rejects a detour to the bare "direct"
		// outbound ("detour to an empty direct outbound makes no sense"),
		// and a resolver without one dials directly — which is what this
		// setting means.
		servers = append(servers, entry)
		if final == "" {
			final = "dns-direct"
		}
	}
	return map[string]any{"servers": servers, "final": final}, true, nil
}

// dnsDetourForOutbound picks the detour for the TUN default resolver:
// the "proxy" outbound when it is a real proxy, none when it is a bare
// direct outbound (a detour to an empty direct is rejected by the
// engine, and a direct-only profile wants direct lookups anyway).
func dnsDetourForOutbound(outbound map[string]any) string {
	if t, _ := outbound["type"].(string); t == "" || t == "direct" {
		return ""
	}
	return "proxy"
}

// singTunDefaultDNSBlock is the fallback resolver for TUN mode when the
// user configured none. The address is an IP, so no hostname bootstrap
// is needed. A non-empty detour routes the lookup through the tunnel:
// without one the engine dials the resolver directly (DNS dialers only
// follow route.final when a detour names an outbound), which in TUN mode
// leaks every query outside the tunnel.
func singTunDefaultDNSBlock(detour string) map[string]any {
	entry := defaultDNSEntry("remote")
	if detour != "" {
		entry["detour"] = detour
	}
	return map[string]any{
		"servers": []any{entry},
		"final":   "remote",
	}
}

// parseDNSAddr converts a DNS server value (scheme URL, bare IP or
// "local") into the sing-box 1.14 typed server object (without a tag).
// "local" selects the system resolver.
func parseDNSAddr(value string) (map[string]any, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, fmt.Errorf("empty DNS server")
	}
	if strings.EqualFold(v, "local") || strings.EqualFold(v, "localhost") {
		return map[string]any{"type": "local"}, nil
	}
	if idx := strings.Index(v, "://"); idx >= 0 {
		scheme := strings.ToLower(v[:idx])
		rest := v[idx+3:]
		host := rest
		path := ""
		if slash := strings.Index(rest, "/"); slash >= 0 {
			host = rest[:slash]
			path = rest[slash:]
		}
		if strings.TrimSpace(host) == "" {
			return nil, fmt.Errorf("invalid DNS server %q: no host", v)
		}
		switch scheme {
		case "https":
			entry := map[string]any{"type": "https", "server": host}
			if path != "" {
				entry["path"] = path
			}
			return entry, nil
		case "tls", "tcp", "udp", "quic":
			return map[string]any{"type": scheme, "server": host}, nil
		default:
			return nil, fmt.Errorf("invalid DNS server %q: unsupported scheme %q (use https://, tls://, tcp://, udp://, quic://, an IP or \"local\")", v, scheme)
		}
	}
	if net.ParseIP(v) != nil {
		return map[string]any{"type": "udp", "server": v}, nil
	}
	return nil, fmt.Errorf("invalid DNS server %q (use https://, tls://, tcp://, udp://, quic://, an IP or \"local\")", v)
}

// defaultDNSEntry returns the hardcoded default DoH resolver (8.8.8.8)
// in the sing-box 1.14 typed format, tagged with the given tag.
func defaultDNSEntry(tag string) map[string]any {
	return map[string]any{"type": "https", "server": "8.8.8.8", "path": "/dns-query", "tag": tag}
}

// chainSingBoxProxy wraps the resolved "proxy" outbound with one SOCKS
// outbound per active external chain (config order, nested outward):
// the origin is retagged "proxy-origin" and each link detours to the
// next, so traffic flows proxy -> ext0 -> ext1 -> origin. With no
// chains the list is the plain [proxy, direct, block].
func chainSingBoxProxy(outbound map[string]any) []any {
	direct := map[string]any{"tag": "direct", "type": "direct"}
	block := map[string]any{"tag": "block", "type": "block"}
	chains := chainsForTag("proxy")
	if len(chains) == 0 {
		return []any{outbound, direct, block}
	}
	outbound["tag"] = "proxy-origin"
	out := make([]any, 0, len(chains)+3)
	for i, c := range chains {
		// The first link keeps the route-final "proxy" tag; later
		// links take numbered tags referenced by the previous detour.
		tag := "proxy"
		if i > 0 {
			tag = fmt.Sprintf("proxy-chain-%d", i)
		}
		next := "proxy-origin"
		if i+1 < len(chains) {
			next = fmt.Sprintf("proxy-chain-%d", i+1)
		}
		out = append(out, map[string]any{
			"tag": tag, "type": "socks",
			"server": "127.0.0.1", "server_port": c.port,
			"version": "5", "detour": next,
		})
	}
	out = append(out, outbound, direct, block)
	return out
}

// profileFingerprint resolves the effective uTLS fingerprint: the new
// TLSFingerprint preset wins; empty/"none" keeps the legacy
// Fingerprint field (default chrome). Normalized to lowercase — both
// cores match fingerprints case-sensitively (see fingerprint.go).
func profileFingerprint(p *Profile) string {
	return effectiveFingerprintValue(p)
}

// addProxyAuth renders the SOCKS/HTTP credential that parseSocks packs into
// IDOrPassword as "user:pass" (and buildSocksLink splits back out on
// export). A credential-less entry stores just the username. Without this
// the outbound dials unauthenticated and the server refuses the connection.
func addProxyAuth(out map[string]any, cred string) {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return
	}
	if user, pass, ok := strings.Cut(cred, ":"); ok {
		out["username"] = user
		out["password"] = pass
		return
	}
	out["username"] = cred
}

// mapProfileToSingBoxOutbound converts common protocols to sing-box
// outbound objects. Unknown protocols fall back to RawOutbound handling
// by the caller.
func mapProfileToSingBoxOutbound(p *Profile) (map[string]any, error) {
	if err := validateFragment(p); err != nil {
		return nil, err
	}
	base := map[string]any{
		"tag": "proxy", "server": p.Address, "server_port": p.Port,
	}
	switch p.Protocol {
	case "shadowsocks":
		base["type"] = "shadowsocks"
		base["method"] = valueOr(p.Method, "aes-256-gcm")
		base["password"] = p.IDOrPassword
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = p.IDOrPassword
		base["alter_id"] = p.AlterID
		base["security"] = valueOr(p.Security, "auto")
	case "vless":
		base["type"] = "vless"
		base["uuid"] = p.IDOrPassword
		if p.Flow != "" {
			base["flow"] = p.Flow
		}
	case "trojan":
		base["type"] = "trojan"
		base["password"] = p.IDOrPassword
	case "socks":
		base["type"] = "socks"
		base["version"] = "5"
		addProxyAuth(base, p.IDOrPassword)
	case "http":
		base["type"] = "http"
		addProxyAuth(base, p.IDOrPassword)
	case "aether":
		// Aether engine (PattNG parity): the profile's outbound is a
		// SOCKS connection to the engine's dialed listener. A throwaway
		// probe pins its own engine port on the profile; otherwise the
		// live published port is used (CoreManager.Start guarantees it
		// before this renders), and with no engine at all the default
		// engine port is rendered, like PattNG's config export dials the
		// settings' Aether listen port.
		port := aetherDefaultCommandPort
		if p.AetherEnginePort > 0 {
			port = p.AetherEnginePort
		} else if pCurr, ok := currentAetherActivePort(); ok {
			port = pCurr
		}
		base["type"] = "socks"
		base["server"] = aetherLoopback
		base["server_port"] = port
		base["version"] = "5"
	default:
		return nil, fmt.Errorf("protocol %q needs RawOutbound JSON for sing-box", p.Protocol)
	}
	if p.TLS || p.Protocol == "trojan" || p.Protocol == "vless" && p.Flow != "" {
		if err := validateFingerprint(p); err != nil {
			return nil, err
		}
		tls := map[string]any{
			"enabled": true, "server_name": valueOr(p.ServerName, p.Address),
			"insecure": p.AllowInsecure, "utls": map[string]any{
				"enabled": true, "fingerprint": profileFingerprint(p),
			},
		}
		// TLS hello fragmentation (sing-box OutboundTLSOptions
		// fragment). Length/interval knobs have no sing-box outbound
		// equivalent and are xray-only (see buildXrayFragment).
		if frag := strings.ToLower(strings.TrimSpace(p.TLSFragment)); frag != "" && frag != "off" {
			tls["fragment"] = true
		}
		if list := alpnList(p.ALPN); len(list) > 0 {
			tls["alpn"] = list
		}
		if p.RealityEnabled {
			tls["reality"] = map[string]any{
				"enabled": true, "public_key": p.RealityPubKey, "short_id": p.RealityShortID,
			}
		}
		base["tls"] = tls
	}
	if p.Network != "" && p.Network != "tcp" {
		t := map[string]any{"type": p.Network}
		switch p.Network {
		case "ws":
			t["path"] = valueOr(p.Path, "/")
			if p.Host != "" {
				t["headers"] = map[string]any{"Host": p.Host}
			}
		case "grpc":
			t["service_name"] = valueOr(p.Path, "TunService")
		case "xhttp":
			t["path"] = valueOr(p.Path, "/")
			if p.Host != "" {
				t["host"] = p.Host
			}
		case "http", "httpupgrade":
			// Both carry the vhost path/host; the renderer used to drop
			// them, so the handshake went to "/" without a Host header.
			t["path"] = valueOr(p.Path, "/")
			if p.Host != "" {
				t["host"] = p.Host
			}
		case "quic":
			// No knobs the profile carries; the engine defaults apply.
		default:
			return nil, fmt.Errorf("network %q is not supported by the sing-box core — use tcp, ws, grpc, xhttp, http, httpupgrade, quic or rawOutbound JSON", p.Network)
		}
		base["transport"] = t
	}
	// Mux (connection multiplexing): omitted when disabled; invalid
	// options fail here instead of at core startup.
	if mux, err := muxBlockSingBox(p); err != nil {
		return nil, err
	} else if mux != nil {
		base["multiplex"] = mux
	}
	return base, nil
}

func valueOr(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

// alpnList splits a link's comma-separated ALPN value into the protocol
// list both engines expect. Rendering the raw string as one element
// ("h2,http/1.1") meant nothing ever negotiated.
func alpnList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
