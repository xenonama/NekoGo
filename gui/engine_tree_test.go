package main

// Process-tree teardown tests: a Disconnect must end the whole engine
// tree, including the psiphon-tunnel-core.exe the supervisor spawns.
// The manager tests re-exec the test binary in linger-helper mode (see
// helper_process_test.go) and point the aether hooks at it, so the
// engine never opens its listener and Start sits in the readiness probe
// exactly like a live tunnel warming up.

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withAetherLingerExe points the aether exe/work-dir/port hooks at the
// linger helper and a fresh work dir, restoring them afterwards.
func withAetherLingerExe(t *testing.T, bin string) string {
	t.Helper()
	work := t.TempDir()
	prevExe, prevDir, prevPort := aetherExeOverride, aetherWorkDirHook, aetherFreePortHook
	aetherExeOverride = bin
	aetherWorkDirHook = func() string { return work }
	aetherFreePortHook = func() (int, error) { return freePort() }
	t.Cleanup(func() {
		aetherExeOverride, aetherWorkDirHook, aetherFreePortHook = prevExe, prevDir, prevPort
	})
	return work
}

// TestAetherStartAbortedByStop: a Stop racing the engine probe aborts
// the Start, and the child the aborted Start spawned is gone (not just
// forgotten by the manager).
func TestAetherStartAbortedByStop(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	withAetherLingerExe(t, bin)
	m := NewAetherManager(make(chan LogEntry, 512))
	p := &Profile{ID: "a1", Protocol: "aether", Aether: aetherSettings{Protocol: "wg"}}

	startErr := make(chan error, 1)
	go func() {
		_, err := m.Start(p, DefaultSettings())
		startErr <- err
	}()
	pid := waitHelperPID(t, pidFile, 10*time.Second)

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop during probe: %v", err)
	}
	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("in-flight Start succeeded behind a Stop; want abort error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight Start did not abort after Stop")
	}
	if m.Running() {
		t.Fatal("manager running after aborted Start")
	}
	m.mu.Lock()
	committed := m.cmd != nil
	job := m.job
	m.mu.Unlock()
	if committed {
		t.Fatal("aborted Start committed m.cmd (stale running state)")
	}
	if job != nil {
		t.Fatal("aborted Start left a job attached")
	}
	waitProcessGone(t, pid, 5*time.Second)
}

// TestPsiphonOnlyStartAbortedByStop is the reported leak: the SNI
// spoof chain's Psiphon leg used to survive a Disconnect that landed
// during its warmup. The child must be ended and its private data dir
// removed.
func TestPsiphonOnlyStartAbortedByStop(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	work := withAetherLingerExe(t, bin)
	m := NewPsiphonManager(make(chan LogEntry, 512))
	psiphonPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	upstreamPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}

	startErr := make(chan error, 1)
	go func() {
		_, err := m.StartPsiphonOnly(DefaultSettings(), psiphonPort, upstreamPort, 20*time.Second)
		startErr <- err
	}()
	pid := waitHelperPID(t, pidFile, 10*time.Second)

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop during chain probe: %v", err)
	}
	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("chain Start succeeded behind a Stop; want abort error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("chain Start did not abort after Stop")
	}
	if m.Running() {
		t.Fatal("psiphon manager running after aborted chain start")
	}
	waitProcessGone(t, pid, 5*time.Second)
	entries, _ := os.ReadDir(work)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), chainPsiphonDirPrefix) {
			t.Fatalf("chain data dir %s survived the aborted start", e.Name())
		}
	}
}

// TestSpawnCurrentInvalidatedByStop: the token check a spawn uses to
// learn it lost the race must fail once Stop cleared the state.
func TestSpawnCurrentInvalidatedByStop(t *testing.T) {
	m := NewAetherManager(nil)
	cmd := &exec.Cmd{}
	m.mu.Lock()
	m.cmd = cmd
	m.running = true
	m.token = 3
	m.mu.Unlock()
	if !m.spawnCurrent(3, cmd) {
		t.Fatal("live spawn not reported as current")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.spawnCurrent(3, cmd) {
		t.Fatal("a stopped spawn still reports current")
	}
}

// TestProcJobKillsAttachedChild: the job object alone (no taskkill) must
// end an attached process, which is what tears down a psiphon child whose
// supervisor already exited. The exit code is the job's 1; an unreaped
// child stays visible to OpenProcess, so the wait is the proof.
func TestProcJobKillsAttachedChild(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("job objects are Windows-only")
	}
	bin, pidFile := lingerHelperBinary(t)
	cmd := exec.Command(bin)
	setHideWindow(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pid := waitHelperPID(t, pidFile, 10*time.Second)

	job := newProcJob()
	if job == nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Skip("job objects unavailable in this environment")
	}
	job.attach(pid)
	job.kill()
	job.close()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("attached child survived job termination")
	}
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.ExitStatus() != 1 {
		t.Fatalf("child exit status = %d, want 1 (TerminateJobObject)", st.ExitStatus())
	}
	waitProcessGone(t, pid, 5*time.Second)
}

// TestWatchChildKillsJobOnSelfExit: an engine that exits on its own
// (crash or core shutdown) still owns its tree; reaping it must take the
// job down so no helper keeps the ports.
func TestWatchChildKillsJobOnSelfExit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("job objects are Windows-only")
	}
	m := NewAetherManager(make(chan LogEntry, 512))
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=", "GO_WANT_HELPER_PIDFILE=")
	setHideWindow(cmd)
	job := newProcJob()
	if job == nil {
		t.Skip("job objects unavailable in this environment")
	}
	m.mu.Lock()
	m.cmd = cmd
	m.running = true
	m.job = job
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	job.attach(cmd.Process.Pid)
	go m.watchChild(cmd, make(chan error, 1), done)
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watchChild did not reap the child")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		left := m.job
		m.mu.Unlock()
		if left == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watchChild left the job attached after a self-exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOrphanSweepIncludesEngineTree: the startup sweep must know the
// Aether engine and its Psiphon exit, and still refuse a same-named
// binary outside the app directory or one owned by a live parent.
func TestOrphanSweepIncludesEngineTree(t *testing.T) {
	dir := `C:\app\bin`
	self := 100
	alive := map[int]bool{100: true, 200: true}
	procs := []orphanProc{
		{pid: 301, ppid: 1, name: "aether.exe", exePath: dir + `\aether.exe`},
		{pid: 302, ppid: 1, name: "psiphon-tunnel-core.exe", exePath: dir + `\pt\psiphon-tunnel-core.exe`},
		{pid: 303, ppid: 200, name: "aether.exe", exePath: dir + `\aether.exe`},
		{pid: 304, ppid: 1, name: "aether.exe", exePath: `D:\apps\Other\aether.exe`},
	}
	got := selectOrphans(procs, dir, self, alive)
	if len(got) != 2 || got[0].pid != 301 || got[1].pid != 302 {
		t.Fatalf("selected %+v, want PIDs 301 and 302", got)
	}
	if !isOrphanImage("psiphon-tunnel-core.exe") || !isOrphanImage("AETHER.EXE") {
		t.Fatal("engine images not in the orphan allow-list")
	}
}

// TestOrphanFilterCoversEveryEngineImage — the process-enumeration filter and
// the kill allow-list must stay in sync: an image missing from the filter is
// never even listed, so a leaked sni-spoof.exe (a WinDivert engine faking TLS
// for the WHOLE machine) or lyrebird.exe would survive a GUI crash.
func TestOrphanFilterCoversEveryEngineImage(t *testing.T) {
	where := orphanProcFilter("name")
	ps := orphanProcFilter("Name")
	for _, name := range orphanImageNames {
		if !isOrphanImage(name) {
			t.Fatalf("isOrphanImage(%q) = false", name)
		}
		if !strings.Contains(where, "name='"+name+"'") {
			t.Fatalf("wmic filter %q is missing %q", where, name)
		}
		if !strings.Contains(ps, "Name='"+name+"'") {
			t.Fatalf("powershell filter %q is missing %q", ps, name)
		}
	}
	for _, want := range []string{"sni-spoof.exe", "lyrebird.exe", "psiphon-tunnel-core.exe", "nekobox_core.exe"} {
		if !isOrphanImage(want) {
			t.Fatalf("%s must be reaped after a GUI crash", want)
		}
	}
}
