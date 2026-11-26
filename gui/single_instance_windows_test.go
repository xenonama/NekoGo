//go:build windows

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func singleInstanceSource(t *testing.T, name string) string {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// This source contract deliberately avoids run(): even a regression must not
// initialise the real config, change the system proxy, or reap real cores.
func TestSingleInstanceStartupContract(t *testing.T) {
	s := singleInstanceSource(t, "main.go")
	guard := strings.Index(s, "acquireSingleInstance(singleInstanceScope, os.Args[1:])")
	if guard < 0 {
		t.Fatal("run has no early single-instance guard; duplicates reach config and cleanup")
	}
	for _, operation := range []string{"logCh :=", "NewConfigStore(", "NewApp(", "cleanupOrphanCores(", "cleanupStaleSysproxy(", "application.New(", "parseConnectArg("} {
		if at := strings.Index(s, operation); at < guard {
			t.Errorf("%s must follow the single-instance guard", operation)
		}
	}
	startup := s[guard:strings.Index(s, "logCh :=")]
	for _, required := range []string{"if err != nil", "if instance == nil", "return nil", "defer instance.Close()"} {
		if !strings.Contains(startup, required) {
			t.Errorf("early guard missing %q", required)
		}
	}
	if !strings.Contains(s, "instance.watchWindow(window)") {
		t.Error("main window is not registered for queued activation")
	}
}

func TestSingleInstanceActivationAndElevationContracts(t *testing.T) {
	s := singleInstanceSource(t, "single_instance_windows.go")
	start := strings.Index(s, "func (g *singleInstance) watchWindow(")
	end := strings.Index(s[start:], "func (g *singleInstance) Close(") + start
	window := s[start:end]
	previous := -1
	for _, required := range []string{"WindowRuntimeReady", "g.onWindowReady(", "application.InvokeAsync(", "window.Show()", "window.UnMinimise()", "window.Focus()"} {
		at := strings.Index(window, required)
		if at <= previous {
			t.Fatalf("missing or misordered UI activation operation: %s", required)
		}
		previous = at
	}
	elevate := singleInstanceSource(t, "elevate_windows.go")
	launcher := strings.Index(elevate, "var relaunchElevated = func(args ...string) error {")
	appendPID := strings.Index(elevate, `args = append(append([]string(nil), args...), "--wait-for-parent", strconv.Itoa(os.Getpid()))`)
	quote := strings.Index(elevate, "quoteElevateArgs(args))")
	if launcher < 0 || appendPID < launcher || appendPID > quote {
		t.Fatal("real relaunch must append its PID inside the mockable launcher, before quoting")
	}
}

// init short-circuits before the package's existing TestMain. Helpers only run
// the IPC guard, never run(), application.Run, config, or cleanup. The scope
// check prevents even accidental use of the real application's kernel objects.
func init() {
	if mode := os.Getenv("NEKOGO_SINGLE_INSTANCE_HELPER"); mode != "" {
		scope := os.Getenv("NEKOGO_SINGLE_INSTANCE_TEST_SCOPE")
		if !strings.HasPrefix(scope, singleInstanceScope+".test.") {
			os.Exit(90)
		}
		os.Exit(runSingleInstanceHelper(scope, mode))
	}
}

func runSingleInstanceHelper(scope, mode string) int {
	fmt.Println("STARTING")
	var guard *singleInstance
	if mode == "guard" {
		var args []string
		for i, arg := range os.Args {
			if arg == "--" {
				args = os.Args[i+1:]
				break
			}
		}
		var err error
		guard, err = acquireSingleInstance(scope, args)
		if err != nil {
			fmt.Println("ERROR", err)
			return 2
		}
		if guard == nil {
			fmt.Println("DUPLICATE")
			return 0
		}
		defer guard.Close()
		fmt.Println("OWNER")
	} else if mode == "linger" {
		fmt.Println("LINGER")
	} else {
		return 91
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "ready":
			guard.onWindowReady(func() { fmt.Println("ACTIVATE") })
		case "release":
			if err := guard.Close(); err != nil {
				fmt.Println("ERROR", err)
				return 3
			}
			fmt.Println("RELEASED")
		case "exit":
			return 0
		case "crash":
			os.Exit(17) // deliberately bypass Close to abandon the owned mutex
		default:
			return 92
		}
	}
	return 0
}

type singleInstanceHelperProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	done  chan struct{}
	err   error // published by closing done
}

func singleInstanceTestScope(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s.test.%s.%d.%d", singleInstanceScope, t.Name(), os.Getpid(), time.Now().UnixNano())
}

func startSingleInstanceHelper(t *testing.T, scope, mode string, args ...string) *singleInstanceHelperProcess {
	t.Helper()
	exe, err := os.Executable() // always the test binary; never the normal GUI
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	h := &singleInstanceHelperProcess{
		cmd:   exec.CommandContext(ctx, exe, append([]string{"-test.run=^$", "--"}, args...)...),
		lines: make(chan string, 64),
		done:  make(chan struct{}),
	}
	h.cmd.Env = append(os.Environ(), "NEKOGO_SINGLE_INSTANCE_HELPER="+mode, "NEKOGO_SINGLE_INSTANCE_TEST_SCOPE="+scope)
	h.cmd.Stderr = os.Stderr
	h.stdin, err = h.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		cancel()
		h.stdin.Close()
		t.Fatal(err)
	}
	if err := h.cmd.Start(); err != nil {
		cancel()
		h.stdin.Close()
		stdout.Close()
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			h.lines <- scanner.Text()
		}
		h.err = h.cmd.Wait()
		close(h.lines)
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel() // only this test-owned helper can be killed
		h.stdin.Close()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("helper did not stop")
		}
	})
	h.expect(t, "STARTING")
	return h
}

func (h *singleInstanceHelperProcess) expect(t *testing.T, want string) {
	t.Helper()
	select {
	case got, ok := <-h.lines:
		if !ok || got != want {
			t.Fatalf("helper output = %q (open=%v), want %q", got, ok, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("helper never reported %q", want)
	}
}

func (h *singleInstanceHelperProcess) quiet(t *testing.T) {
	t.Helper()
	select {
	case got := <-h.lines:
		t.Fatalf("unexpected helper output before next activation: %q", got)
	case <-time.After(80 * time.Millisecond):
	}
}

func (h *singleInstanceHelperProcess) send(t *testing.T, command string) {
	t.Helper()
	if _, err := fmt.Fprintln(h.stdin, command); err != nil {
		t.Fatal(err)
	}
}

func (h *singleInstanceHelperProcess) wait(t *testing.T, code int) {
	t.Helper()
	select {
	case <-h.done:
		if h.cmd.ProcessState.ExitCode() != code {
			t.Fatalf("helper exit = %v, want code %d", h.err, code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper failed to exit")
	}
}

func TestSingleInstanceExclusionAndStartupActivation(t *testing.T) {
	scope := singleInstanceTestScope(t)
	owner := startSingleInstanceHelper(t, scope, "guard")
	owner.expect(t, "OWNER")
	// Multiple launches before runtime readiness queue only one notification.
	// In particular, arbitrary --connect is not an elevation/ownership bypass.
	for _, args := range [][]string{nil, {"--connect", "untrusted-profile"}, nil} {
		duplicate := startSingleInstanceHelper(t, scope, "guard", args...)
		duplicate.expect(t, "DUPLICATE")
		duplicate.wait(t, 0)
	}
	owner.quiet(t)
	owner.send(t, "ready")
	owner.expect(t, "ACTIVATE")
	owner.quiet(t)
	duplicate := startSingleInstanceHelper(t, scope, "guard")
	duplicate.expect(t, "DUPLICATE")
	duplicate.wait(t, 0)
	owner.expect(t, "ACTIVATE")
	// This exercises the stop event while the monitor is in its native wait.
	owner.send(t, "release")
	owner.expect(t, "RELEASED")
	owner.send(t, "exit")
	owner.wait(t, 0)
	fresh := startSingleInstanceHelper(t, scope, "guard")
	fresh.expect(t, "OWNER")
	// Closing before readiness must not leave a goroutine/thread behind either.
	fresh.send(t, "release")
	fresh.expect(t, "RELEASED")
	fresh.send(t, "exit")
	fresh.wait(t, 0)
}

func singleInstanceKeepMutex(t *testing.T, scope string) windows.Handle {
	t.Helper()
	name, _, _, err := singleInstanceObjects(scope)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE, false, windows.StringToUTF16Ptr(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(handle) })
	return handle
}

func singleInstanceAssertReleased(t *testing.T, handle windows.Handle) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	status, err := windows.WaitForSingleObject(handle, 0)
	if status == windows.WAIT_OBJECT_0 || status == windows.WAIT_ABANDONED {
		defer windows.ReleaseMutex(handle)
	}
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("mutex was not released by its owning thread: status=%#x err=%v", status, err)
	}
}

func TestSingleInstanceOwnedThreadAndCrashRelease(t *testing.T) {
	scope := singleInstanceTestScope(t)
	owner := startSingleInstanceHelper(t, scope, "guard")
	owner.expect(t, "OWNER")
	// Keep the object alive across the crash, so the next admission must
	// handle WAIT_ABANDONED rather than merely creating a fresh mutex.
	keeper := singleInstanceKeepMutex(t, scope)
	owner.send(t, "crash")
	owner.wait(t, 17)
	for i := 0; i < 3; i++ {
		guard, err := acquireSingleInstance(scope, nil)
		if err != nil || guard == nil {
			t.Fatalf("acquire after crash/release: guard=%v err=%v", guard, err)
		}
		t.Cleanup(func() { guard.Close() })
		// Two goroutines in one process must not exploit recursive native
		// mutex ownership by being scheduled on the same OS thread.
		duplicate, err := acquireSingleInstance(scope, []string{"--connect", "anything"})
		if err != nil || duplicate != nil {
			if duplicate != nil {
				duplicate.Close()
			}
			t.Fatalf("same-process duplicate admitted: err=%v", err)
		}
		if err := guard.Close(); err != nil {
			t.Fatal(err)
		}
		if err := guard.Close(); err != nil {
			t.Fatal("idempotent close:", err)
		}
		singleInstanceAssertReleased(t, keeper)
	}
}

func TestSingleInstanceIdentityAndObjectSecurity(t *testing.T) {
	scope := singleInstanceTestScope(t)
	mutex, event, sa, err := singleInstanceObjects(scope)
	if err != nil {
		t.Fatal(err)
	}
	mutexAgain, eventAgain, _, err := singleInstanceObjects(scope)
	if err != nil || mutex != mutexAgain || event != eventAgain || mutex == event || !strings.HasPrefix(mutex, `Local\NekoGo.`) {
		t.Fatalf("unstable or overlapping object identities: %q %q (%v)", mutex, event, err)
	}
	other, _, _, err := singleInstanceObjects(scope + ".other")
	if err != nil || mutex == other {
		t.Fatal("test scope does not isolate kernel objects")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sddl := sa.SecurityDescriptor.String()
	if !strings.Contains(sddl, "(A;;GA;;;"+user.User.Sid.String()+")") || !strings.Contains(sddl, "S:(ML;;NW;;;ME)") {
		t.Fatalf("IPC objects lack same-user DACL/medium integrity: %s", sddl)
	}
	if sa.InheritHandle != 0 || strings.Contains(sddl, ";;;WD)") || strings.Contains(sddl, ";;;BA)") {
		t.Fatalf("IPC security is too broad: %s", sddl)
	}
}

func TestSingleInstanceFailsClosedOnObjectErrors(t *testing.T) {
	for _, kind := range []string{"mutex", "event"} {
		t.Run(kind, func(t *testing.T) {
			scope := singleInstanceTestScope(t)
			mutex, event, sa, err := singleInstanceObjects(scope)
			if err != nil {
				t.Fatal(err)
			}
			var wrongType windows.Handle
			if kind == "mutex" {
				wrongType, err = windows.CreateEventEx(sa, windows.StringToUTF16Ptr(mutex), 0, windows.SYNCHRONIZE)
			} else {
				wrongType, err = windows.CreateMutexEx(sa, windows.StringToUTF16Ptr(event), 0, windows.SYNCHRONIZE)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(wrongType)
			guard, err := acquireSingleInstance(scope, nil)
			if guard != nil {
				guard.Close()
				t.Fatal("admitted an instance after a native object error")
			}
			if err == nil {
				t.Fatal("native error was incorrectly treated as a duplicate")
			}
		})
	}
}

func TestSingleInstanceWaitParentParser(t *testing.T) {
	other := strconv.Itoa(os.Getpid() + 4)
	for _, tc := range []struct {
		name string
		args []string
		pid  uint32
		bad  bool
	}{
		{"none", nil, 0, false},
		{"connect", []string{"--connect", "arbitrary"}, 0, false},
		{"pair", []string{"--connect", "id", "--wait-for-parent", other}, uint32(os.Getpid() + 4), false},
		{"equals", []string{"--wait-for-parent=" + other}, uint32(os.Getpid() + 4), false},
		{"missing", []string{"--wait-for-parent"}, 0, true},
		{"empty", []string{"--wait-for-parent="}, 0, true},
		{"zero", []string{"--wait-for-parent", "0"}, 0, true},
		{"negative", []string{"--wait-for-parent", "-4"}, 0, true},
		{"overflow", []string{"--wait-for-parent", "4294967296"}, 0, true},
		{"text", []string{"--wait-for-parent", "abc"}, 0, true},
		{"self", []string{"--wait-for-parent", strconv.Itoa(os.Getpid())}, 0, true},
		{"duplicate", []string{"--wait-for-parent", other, "--wait-for-parent=" + other}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pid, err := parseWaitForParentArg(tc.args)
			if (err != nil) != tc.bad || pid != tc.pid {
				t.Fatalf("parse = %d, %v; want pid=%d error=%v", pid, err, tc.pid, tc.bad)
			}
			if tc.bad {
				guard, err := acquireSingleInstance(singleInstanceTestScope(t), tc.args)
				if guard != nil {
					guard.Close()
					t.Fatal("invalid wait argument granted ownership")
				}
				if err == nil {
					t.Fatal("invalid wait argument did not fail closed")
				}
			}
		})
	}
}

func TestSingleInstanceWaitParentTimeoutAndExit(t *testing.T) {
	helper := startSingleInstanceHelper(t, singleInstanceTestScope(t), "linger")
	helper.expect(t, "LINGER")
	pid := uint32(helper.cmd.Process.Pid)
	start := time.Now()
	if err := waitForSingleInstanceParent(pid, 40*time.Millisecond); !errors.Is(err, errSingleInstanceParentTimeout) {
		t.Fatalf("live test-owned parent wait = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("unbounded or premature wait: %v", elapsed)
	}
	for _, timeout := range []time.Duration{0, -time.Second, 2 * time.Hour} {
		if err := waitForSingleInstanceParent(pid, timeout); err == nil {
			t.Fatal("invalid timeout accepted")
		}
	}
	helper.send(t, "exit")
	helper.wait(t, 0)
	if err := waitForSingleInstanceParent(pid, 200*time.Millisecond); err != nil {
		t.Fatalf("already exited test-owned parent: %v", err)
	}
}

func TestSingleInstanceElevationWaitStillAcquiresMutex(t *testing.T) {
	for _, thirdWins := range []bool{false, true} {
		t.Run(fmt.Sprintf("thirdWins=%v", thirdWins), func(t *testing.T) {
			scope := singleInstanceTestScope(t)
			parent := startSingleInstanceHelper(t, scope, "guard")
			parent.expect(t, "OWNER")
			child := startSingleInstanceHelper(t, scope, "guard", "--connect", "test-profile", "--wait-for-parent", strconv.Itoa(parent.cmd.Process.Pid))
			child.quiet(t) // must not signal-and-exit while its parent owns
			var third *singleInstanceHelperProcess
			if thirdWins {
				parent.send(t, "release")
				parent.expect(t, "RELEASED")
				child.quiet(t) // even a free mutex does not skip the process wait
				third = startSingleInstanceHelper(t, scope, "guard")
				third.expect(t, "OWNER")
			}
			parent.send(t, "exit")
			parent.wait(t, 0)
			if thirdWins {
				child.expect(t, "DUPLICATE")
				child.wait(t, 0)
				third.send(t, "ready")
				third.expect(t, "ACTIVATE")
				third.send(t, "release")
				third.expect(t, "RELEASED")
				third.send(t, "exit")
				third.wait(t, 0)
			} else {
				child.expect(t, "OWNER")
				duplicate := startSingleInstanceHelper(t, scope, "guard")
				duplicate.expect(t, "DUPLICATE")
				duplicate.wait(t, 0)
				child.send(t, "ready")
				child.expect(t, "ACTIVATE")
				child.send(t, "release")
				child.expect(t, "RELEASED")
				child.send(t, "exit")
				child.wait(t, 0)
			}
		})
	}
}
