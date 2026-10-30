package main

// Race + lifecycle tests for BUG-004 (Start/EphemeralRun must not hold
// the manager mutex across the multi-second probe) and the epoch
// invalidation shared with integration BUG-001/002.
//
// The tests re-exec the test binary in linger-helper mode (see
// helper_process_test.go): the child ignores all core args and sleeps,
// so Start blocks in its readiness probe exactly like a hung core.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// TestStartDoesNotHoldLockDuringProbe (BUG-004): with Start blocked in
// its dial probe, Stop/GetStats/IsRunning must each complete within
// 200ms instead of queueing behind the manager mutex.
func TestStartDoesNotHoldLockDuringProbe(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	m := NewSingBoxManager(bin, make(chan LogEntry, 512))
	p := &Profile{ID: "x", Name: "hung", Core: CoreSingBox, Protocol: "direct",
		RawOutbound: `{"type":"direct","tag":"proxy"}`}
	startErr := make(chan error, 1)
	go func() { startErr <- m.Start(p, DefaultSettings()) }()
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
	go func() { startErr <- m.Start(p, DefaultSettings()) }()
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
	go func() { startErr <- m.Start(p, DefaultSettings()) }()
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
		if err == nil {
			t.Fatal("in-flight Start succeeded behind a Stop; want abort error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight Start did not abort after Stop")
	}
	if _, _, running := m.Status(); running {
		t.Fatal("CoreManager running after aborted Start")
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
