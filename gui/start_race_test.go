package main

// Race + lifecycle tests for BUG-004 (Start/EphemeralRun must not hold
// the manager mutex across the multi-second probe) and the epoch
// invalidation shared with integration BUG-001/002.
//
// The tests re-exec the test binary in linger-helper mode (see
// helper_process_test.go): the child ignores all core args and sleeps,
// so Start blocks in its readiness probe exactly like a hung core.

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// lingerHelperBinary points a manager at the current test binary in
// linger-helper mode and returns a pidfile the child writes on startup.
// t.Setenv restores the environment automatically.
func lingerHelperBinary(t *testing.T) (bin, pidFile string) {
	t.Helper()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	pidFile = filepath.Join(t.TempDir(), "helper.pid")
	t.Setenv("GO_WANT_HELPER_PIDFILE", pidFile)
	return os.Args[0], pidFile
}

// waitHelperPID polls for the helper child's pidfile (proof Start passed
// cmd.Start and is sitting in its probe).
func waitHelperPID(t *testing.T, pidFile string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("helper child did not start within %v", timeout)
	return 0
}

// processGone reports whether pid no longer names a live process.
func processGone(pid int) bool {
	if runtime.GOOS == "windows" {
		h, err := syscall.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return true
		}
		_ = syscall.CloseHandle(h)
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return p.Signal(syscall.Signal(0)) != nil
}

func waitProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child pid %d still alive after %v (zombie/leak)", pid, timeout)
}

// raceSettings is DefaultSettings with a free mixed inbound. Start
// preflights the ports it is about to bind and refuses a held one before
// any child spawns, so the default 2080 made these tests fail whenever
// NekoGo itself was connected — the helper never ran, and the failure read
// as a lifecycle bug rather than a port collision.
func raceSettings(t *testing.T) Settings {
	t.Helper()
	s := DefaultSettings()
	for attempt := 0; attempt < 25; attempt++ {
		port, err := freePort()
		if err != nil {
			t.Fatalf("freePort: %v", err)
		}
		// The HTTP inbound takes MixedPort and SOCKS the next one.
		addrs := []string{
			net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1)),
		}
		if len(occupiedBindPorts(addrs)) == 0 {
			s.MixedPort = port
			return s
		}
	}
	t.Fatal("no free mixed inbound pair for the race tests")
	return s
}

// TestStartDoesNotHoldLockDuringProbe (BUG-004): with Start blocked in
// its dial probe, Stop/GetStats/IsRunning must each complete within
// 200ms instead of queueing behind the manager mutex.
func TestStartDoesNotHoldLockDuringProbe(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	m := NewSingBoxManager(bin, make(chan LogEntry, 512))
	p := &Profile{ID: "x", Name: "hung", Core: CoreSingBox, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
	startErr := make(chan error, 1)
	s := raceSettings(t)
	go func() { startErr <- m.Start(p, s) }()
	waitHelperPID(t, pidFile, 10*time.Second)

	t0 := time.Now()
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop during probe: %v", err)
	}
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Fatalf("Stop blocked %v behind the Start probe, want <200ms", d)
	}
	t0 = time.Now()
	if _, err := m.GetStats(); err != nil {
		t.Fatalf("GetStats during probe: %v", err)
	}
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Fatalf("GetStats blocked %v behind the Start probe, want <200ms", d)
	}
	t0 = time.Now()
	_ = m.IsRunning()
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Fatalf("IsRunning blocked %v behind the Start probe, want <200ms", d)
	}

	select {
	case err := <-startErr:
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("in-flight Start err = %v, want abort error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight Start did not abort after Stop")
	}
	if m.IsRunning() {
		t.Fatal("manager running after aborted Start")
	}
}

// TestStartAbortedByConcurrentStop (BUG-004): a Stop racing the probe
// aborts the pending Start — no stale commit, no zombie child.
func TestStartAbortedByConcurrentStop(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	m := NewSingBoxManager(bin, make(chan LogEntry, 512))
	p := &Profile{ID: "x", Name: "hung", Core: CoreSingBox, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
	startErr := make(chan error, 1)
	s := raceSettings(t)
	go func() { startErr <- m.Start(p, s) }()
	pid := waitHelperPID(t, pidFile, 10*time.Second)

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-startErr:
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("Start err = %v, want abort error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not abort after concurrent Stop")
	}
	if m.IsRunning() {
		t.Fatal("manager running after aborted Start")
	}
	m.mu.Lock()
	committed := m.cmd != nil
	m.mu.Unlock()
	if committed {
		t.Fatal("aborted Start committed m.cmd (stale running state)")
	}
	waitProcessGone(t, pid, 5*time.Second)
}

// TestStopAbortsInFlightStart (integration BUG-001/002, CoreManager
// level): Stop during an engine probe returns fast and the pending
// Start aborts instead of publishing a connection behind the Stop.
func TestStopAbortsInFlightStart(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	sb := NewSingBoxManager(bin, make(chan LogEntry, 512))
	xr := NewXrayManager("", "", make(chan LogEntry, 512))
	m := NewCoreManager(sb, xr)
	p := &Profile{ID: "x", Name: "hung", Core: CoreSingBox, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
	startErr := make(chan error, 1)
	s := raceSettings(t)
	go func() { startErr <- m.Start(p, s) }()
	waitHelperPID(t, pidFile, 10*time.Second)

	t0 := time.Now()
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop during probe: %v", err)
	}
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Fatalf("CoreManager.Stop blocked %v, want <200ms", d)
	}
	select {
	case err := <-startErr:
		if err == nil || err.Error() != "connect aborted (concurrent stop)" {
			t.Fatalf("in-flight Start err = %v, want the shared connect cancellation error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight Start did not abort after Stop")
	}
	if _, _, running := m.Status(); running {
		t.Fatal("CoreManager running after aborted Start")
	}
}

func TestStopBeforeCoreEpochAbortsStart(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	m := NewCoreManager(NewSingBoxManager(bin, make(chan LogEntry, 512)), NewXrayManager("", "", make(chan LogEntry, 512)))
	entered := make(chan struct{})
	release := make(chan struct{})
	m.startHook = func() {
		close(entered)
		<-release
	}
	p := &Profile{ID: "x", Name: "early cancel", Core: CoreSingBox, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
	startErr := make(chan error, 1)
	s := raceSettings(t)
	go func() { startErr <- m.Start(p, s) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Start never reached the pre-epoch test gate")
	}
	if err := m.Stop(); err != nil {
		close(release)
		t.Fatalf("Stop before core epoch: %v", err)
	}
	close(release)
	select {
	case err := <-startErr:
		if err == nil || err.Error() != "connect aborted (concurrent stop)" {
			t.Fatalf("Start after pre-epoch Stop = %v, want shared cancellation error", err)
		}
	case <-time.After(3 * time.Second):
		_ = m.Stop()
		t.Fatal("Start survived a Stop that completed before its core epoch was claimed")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("cancelled Start spawned a child: pid file stat = %v", err)
	}
}

// TestConnectCancelledBeforeCoreStart covers the window before CoreManager
// owns an epoch: Disconnect must cancel the App-level attempt, not merely a
// core probe that has not started yet.
func TestConnectCancelledBeforeCoreStart(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	app.sysProxyHook = func(bool) error { return nil }
	saved, err := app.SaveProfile(Profile{Name: "cancel", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	scan := make(chan struct{})
	defer func() {
		select {
		case <-scan:
		default:
			close(scan)
		}
	}()
	app.setOrphanScanDone(scan)
	started := make(chan struct{}, 1)
	app.coreStarter = func(*Profile, Settings) error {
		started <- struct{}{}
		return nil
	}
	connectErr := make(chan error, 1)
	go func() { connectErr <- app.Connect(saved.ID) }()
	deadline := time.Now().Add(5 * time.Second)
	for !app.IsConnectBusy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !app.IsConnectBusy() {
		t.Fatal("Connect never claimed the in-flight slot")
	}
	if err := app.Disconnect(); err != nil {
		t.Fatalf("Disconnect during orphan scan: %v", err)
	}
	close(scan)
	select {
	case err := <-connectErr:
		if err == nil || err.Error() != "connect aborted (concurrent stop)" {
			t.Fatalf("Connect after pre-start cancel = %v, want shared cancellation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Connect did not settle")
	}
	select {
	case <-started:
		t.Fatal("cancelled Connect still invoked the core starter")
	default:
	}
}

// TestCancelledConnectRevertsLateSystemProxy forces Disconnect into the
// SetSystemProxy call. The late enable must be undone before Connect settles.
func TestCancelledConnectRevertsLateSystemProxy(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.SystemProxy = true
	settings.MixedPort = 22980
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	saved, err := app.SaveProfile(Profile{Name: "proxy race", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	enabling := make(chan struct{})
	release := make(chan struct{})
	var enableOnce sync.Once
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	calls := make(chan bool, 4)
	app.sysStateHook = func() (bool, string, error) { return false, "", nil }
	app.sysProxyHook = func(enable bool) error {
		calls <- enable
		if enable {
			enableOnce.Do(func() { close(enabling) })
			<-release
		}
		return nil
	}
	app.coreStarter = func(p *Profile, _ Settings) error {
		app.cores.mu.Lock()
		app.cores.running = true
		app.cores.activeID = p.ID
		app.cores.activeCore = CoreSingBox
		app.cores.mu.Unlock()
		return nil
	}
	connectErr := make(chan error, 1)
	go func() { connectErr <- app.Connect(saved.ID) }()
	select {
	case <-enabling:
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not reach system-proxy setup")
	}
	if err := app.Disconnect(); err != nil {
		t.Fatalf("Disconnect during proxy setup: %v", err)
	}
	close(release)
	select {
	case err := <-connectErr:
		if err == nil || err.Error() != "connect aborted (concurrent stop)" {
			t.Fatalf("Connect after proxy-setup cancel = %v, want shared cancellation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not settle after proxy setup was released")
	}
	var got []bool
	for len(calls) > 0 {
		got = append(got, <-calls)
	}
	if len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("system-proxy calls = %v, want enable then revert", got)
	}
	if app.getAutoProxy() {
		t.Fatal("late proxy enable left autoProxyOn set after cancellation")
	}
	if _, _, running := app.cores.Status(); running {
		t.Fatal("core running after cancelled Connect settled")
	}
}

// TestConnectDoesNotOverwriteSelectionChangedDuringStart: the server rail
// stays usable while a profile is starting. A newer selection must survive
// the older Connect's final active-profile write.
func TestConnectDoesNotOverwriteSelectionChangedDuringStart(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	app.sysProxyHook = func(bool) error { return nil }
	defer app.Disconnect()
	first, err := app.SaveProfile(Profile{Name: "first", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.SaveProfile(Profile{Name: "second", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveProfileID("initial"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	app.coreStarter = func(p *Profile, _ Settings) error {
		close(started)
		<-release
		app.cores.mu.Lock()
		app.cores.running = true
		app.cores.activeID = p.ID
		app.cores.activeCore = CoreSingBox
		app.cores.mu.Unlock()
		return nil
	}
	connectErr := make(chan error, 1)
	go func() { connectErr <- app.Connect(first.ID) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not reach the core starter")
	}
	if err := store.SetActiveProfileID(second.ID); err != nil {
		t.Fatalf("select newer server: %v", err)
	}
	close(release)
	select {
	case err := <-connectErr:
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not settle")
	}
	if got := store.GetSettings().ActiveProfileID; got != second.ID {
		t.Fatalf("active profile after Connect = %q, want newer selection %q", got, second.ID)
	}
}

// TestConnectBusyFlag (integration BUG-001/002, App level): Connect sets
// the shared busy state (visible via IsConnectBusy and Status) until the
// probe settles; a racing Disconnect aborts promptly and clears it.
func TestConnectBusyFlag(t *testing.T) {
	bin, _ := lingerHelperBinary(t)
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	app.singbox.corePath = bin
	saved, err := app.SaveProfile(Profile{Name: "hung", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	connectErr := make(chan error, 1)
	go func() { connectErr <- app.Connect(saved.ID) }()
	deadline := time.Now().Add(5 * time.Second)
	for !app.IsConnectBusy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !app.IsConnectBusy() {
		t.Fatal("IsConnectBusy false during in-flight Connect")
	}
	if st := app.Status(); st["connectBusy"] != true {
		t.Fatalf("Status connectBusy = %v, want true", st["connectBusy"])
	}
	t0 := time.Now()
	if err := app.Disconnect(); err != nil {
		t.Fatalf("Disconnect during Connect: %v", err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Disconnect blocked %v, want abort within ~1s", d)
	}
	select {
	case err := <-connectErr:
		if err == nil {
			t.Fatal("Connect succeeded behind a Disconnect; want abort error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect did not abort after Disconnect")
	}
	if app.IsConnectBusy() {
		t.Fatal("IsConnectBusy stuck true after settled Connect")
	}
	if _, _, running := app.cores.Status(); running {
		t.Fatal("core running after aborted Connect")
	}
}
