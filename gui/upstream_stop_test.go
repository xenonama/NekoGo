package main

// Disconnect teardown while a connect is still in flight. CoreManager
// publishes upstreamActive — the flag that says "this group's transport
// is the official sing-box CLI" — only when it commits the group, at the
// very end of Start. Every engine it starts before that commit is
// therefore invisible to Stop's routing, which is how a Disconnect
// landing mid-connect could stop a core that never ran and leave the
// live sing-box.exe (or aether.exe) behind.
//
// Two stand-ins for the CLI, both the test binary re-executed under the
// official name: the linger helper (helper_process_test.go) sleeps
// instead of binding, so a readiness probe sits in its loop and Stop can
// interrupt it; the fake core (fake_singbox_test.go) binds the config's
// inbound, so the probe succeeds and the engine is left running with the
// group still uncommitted. No real binaries, drivers or admin rights
// needed — and nothing here pre-binds a port, which the connect-time port
// pre-flight would (correctly) refuse.

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCLIInScratchDir copies the running test binary into its own
// directory under the official CLI's name. installFakeSingBox puts it in
// t.TempDir, and a child killed moments earlier can still hold the image
// mapped — Windows then refuses the cleanup delete and the real failure
// gets buried. This copy is removed with retries instead.
func fakeCLIInScratchDir(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(scratchDir(t), "sing-box.exe")
	current, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(exe, data, 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	return exe
}

func upstreamStopRig(t *testing.T) (*CoreManager, *UpstreamSingBoxManager, *SingBoxManager, Settings) {
	t.Helper()
	logCh := make(chan LogEntry, 4096)
	custom := NewSingBoxManager("", logCh)
	up := NewUpstreamSingBoxManager(logCh)
	cores := NewCoreManager(custom, NewXrayManager("", t.TempDir(), logCh))
	cores.SetUpstreamSingBoxManager(up)

	s := DefaultSettings()
	s.CoreEngine = "upstream"
	s.SystemProxy = false
	s.TunEnabled = false
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	s.MixedPort = port
	return cores, up, custom, s
}

func hungUpstreamProfile() *Profile {
	return &Profile{ID: "u1", Name: "hung", Core: CoreSingBox, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
}

// TestStopDuringUpstreamProbeTakesDownEngine: a Disconnect landing while
// the official CLI is still warming up must end the child it spawned.
// The helper never binds the inbound, so without teardown the process
// sits there for the whole readiness timeout and the in-flight Start
// then commits an engine nobody owns.
func TestStopDuringUpstreamProbeTakesDownEngine(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("upstream CLI stand-in relies on Windows process handles")
	}
	bin := fakeCLIInScratchDir(t)
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("GO_WANT_HELPER_PIDFILE", pidFile)

	cores, up, _, s := upstreamStopRig(t)
	s.UpstreamCorePath = bin
	p := hungUpstreamProfile()

	startErr := make(chan error, 1)
	go func() { startErr <- cores.Start(p, s) }()
	pid := waitHelperPID(t, pidFile, 10*time.Second)

	if err := cores.Stop(); err != nil {
		t.Fatalf("Stop during upstream probe: %v", err)
	}
	// Stop on an idle manager is a no-op, so the only way this child dies
	// is Stop reaching the upstream one.
	waitProcessGone(t, pid, 5*time.Second)

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("in-flight upstream Start succeeded behind a Stop")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("in-flight upstream Start never returned")
	}
	if up.IsRunning() {
		t.Fatal("upstream manager reports running after its Start was aborted")
	}
	_ = cores.Stop()
}

// TestStopTakesDownUpstreamEngineWithUncommittedFlags reproduces the
// state a Disconnect sees between the engine's commit and the group's:
// the CLI is up and its manager says running, while CoreManager has not
// published upstreamActive yet. Stop has to take that engine down
// anyway — otherwise it stays up, the GUI says "disconnected", and every
// later Connect fails "upstream sing-box already running" until the app
// is restarted.
func TestStopTakesDownUpstreamEngineWithUncommittedFlags(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("upstream CLI stand-in relies on Windows process handles")
	}
	bin := fakeCLIInScratchDir(t)

	cores, up, _, s := upstreamStopRig(t)
	s.UpstreamCorePath = bin
	p := hungUpstreamProfile()

	// No helper env here: the child runs the fake-CLI branch
	// (fake_singbox_test.go), so it binds the config's inbound itself,
	// exactly like the official binary. The readiness dial is answered by
	// the core's own listener, and upstream Start completes — while
	// CoreManager, never told about it because Start is what would publish
	// the flags, still reports the pre-connect state.
	if err := up.Start(p, s); err != nil {
		t.Fatalf("upstream engine Start: %v", err)
	}
	if !up.IsRunning() {
		t.Fatal("rig is wrong: the upstream engine did not come up")
	}
	if _, _, running := cores.Status(); running {
		t.Fatal("rig is wrong: CoreManager already reports a running group")
	}

	if err := cores.Stop(); err != nil {
		t.Fatalf("Stop with the transport uncommitted: %v", err)
	}
	if up.IsRunning() {
		t.Fatal("Disconnect left the upstream sing-box running: its manager " +
			"keeps reporting \"already running\" to every later Connect")
	}

	if err := cores.Start(p, s); err != nil {
		t.Fatalf("Connect after the disconnect: %v", err)
	}
	if err := cores.Stop(); err != nil {
		t.Fatalf("cleanup Stop: %v", err)
	}
}

// TestAetherConnectAbortedByStopRollsBackItsLegs: a Disconnect landing
// during an aether connect must leave nothing of that connect behind.
// The commit at the end of the aether path returned a fresh error instead
// of the named one, so the deferred rollback — the only thing that
// unpublishes the engine's port — never ran; and because the transport
// flag is still unwritten, Stop's routing never saw the official CLI the
// same connect had already started.
//
// The interleaving is pinned by the two readiness probes: the engine's is
// answered by a SOCKS listener the test owns, the transport's only after
// the Disconnect has already landed. So the connect is provably inside
// its transport probe when Stop runs, and reaches its commit with the
// group taken over.
func TestAetherConnectAbortedByStopRollsBackItsLegs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("aether and upstream stand-ins rely on Windows process handles")
	}
	bin := fakeCLIInScratchDir(t)
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("GO_WANT_HELPER_PIDFILE", filepath.Join(scratchDir(t), "helper.pid"))

	cores, up, _, s := upstreamStopRig(t)
	s.UpstreamCorePath = bin

	aether := NewAetherManager(make(chan LogEntry, 4096))
	cores.SetAetherManager(aether)
	work := scratchDir(t)
	prevExe, prevDir, prevPort := aetherExeOverride, aetherWorkDirHook, aetherFreePortHook
	sink := newSocksSink(t)
	aetherExeOverride = bin
	aetherWorkDirHook = func() string { return work }
	aetherFreePortHook = func() (int, error) { return sink.port, nil }
	defer func() {
		aetherExeOverride, aetherWorkDirHook, aetherFreePortHook = prevExe, prevDir, prevPort
		sink.close()
	}()

	p := &Profile{ID: "ae1", Name: "aether", Protocol: "aether", Port: 443,
		Address: "203.0.113.7", Core: CoreSingBox, Aether: aetherSettings{Protocol: "wg"}}

	startErr := make(chan error, 1)
	go func() { startErr <- cores.Start(p, s) }()
	waitUpstreamSpawned(t, up, 30*time.Second)
	if _, ok := currentAetherActivePort(); !ok {
		t.Fatal("rig is wrong: the connect passed its engine without publishing the port")
	}

	if err := cores.Stop(); err != nil {
		t.Fatalf("Disconnect during the aether connect: %v", err)
	}
	// Let the transport's probe succeed now: the connect continues to its
	// commit and finds the epoch moved under it.
	transport := newLoopbackSink(t, s.MixedPort)
	defer transport.close()

	var err error
	select {
	case err = <-startErr:
	case <-time.After(30 * time.Second):
		t.Fatal("aether connect never returned")
	}
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("connect err = %v, want the concurrent-stop abort", err)
	}
	if port, ok := currentAetherActivePort(); ok {
		t.Fatalf("the aborted connect left aether port %d published: the tunnel is gone, "+
			"but every later probe is refused until the app restarts", port)
	}
	if up.IsRunning() {
		t.Fatal("the aborted connect left its upstream sing-box transport running")
	}
	if aether.Running() {
		t.Fatal("the aborted connect left the aether engine running")
	}
	_ = cores.Stop()
}

// waitUpstreamSpawned blocks until the official CLI has a child, which is
// after its manager's Start committed the process and while its readiness
// probe still runs — the window the test pins.
func waitUpstreamSpawned(t *testing.T, m *UpstreamSingBoxManager, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		spawned := m.cmd != nil
		m.mu.Unlock()
		if spawned {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("upstream sing-box never spawned (the connect died before its transport)")
}

// scratchDir is a temp dir removed with retries: a child killed moments
// earlier can still hold its image mapped, and Windows then refuses the
// delete. t.TempDir reports that as a test failure unrelated to the bug
// under test.
func scratchDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nekogo-scratch-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 60; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("could not remove %s", dir)
	})
	return dir
}

// loopbackSink is a listener that accepts and immediately closes, which
// is all a TCP-connect readiness probe needs to succeed.
type loopbackSink struct {
	ln net.Listener
}

func newLoopbackSink(t *testing.T, port int) *loopbackSink {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("listen on %d: %v", port, err)
	}
	s := &loopbackSink{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return s
}

func (s *loopbackSink) close() { _ = s.ln.Close() }

// socksSink answers the SOCKS5 greeting forever on an ephemeral port,
// which is what AetherManager.waitSocksOrExit probes for.
type socksSink struct {
	ln   net.Listener
	port int
}

func newSocksSink(t *testing.T) *socksSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks sink: %v", err)
	}
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("socks sink addr: %v", err)
	}
	s := &socksSink{ln: ln, port: parsePortInt(portStr)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				head := make([]byte, 3)
				if _, err := io.ReadFull(c, head); err != nil || head[0] != 5 {
					return
				}
				_, _ = c.Write([]byte{5, 0})
				time.Sleep(2 * time.Second)
			}(c)
		}
	}()
	return s
}

func (s *socksSink) close() { _ = s.ln.Close() }
