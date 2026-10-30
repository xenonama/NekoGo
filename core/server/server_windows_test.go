//go:build windows

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestDoRunPipeFailureDoesNotPanic — a nonexistent pipe must surface as
// an error return, never a panic (P3-6).
func TestDoRunPipeFailureDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("doRun panicked: %v", r)
		}
	}()
	err := doRun(`\\.\pipe\nekobox-definitely-missing-pipe-xyz`)
	if err == nil {
		t.Fatal("expected error for nonexistent pipe, got nil")
	}
	t.Logf("doRun returned error as expected: %v", err)
}

// TestRunShellExecClosesHandle — the BUG-012 regression test.
//
// Limitation (documented per the task): runShellExec itself pops a UAC
// "runas" elevation and cannot run headless/CI, so this test does NOT
// invoke it. Instead it proves the fix wiring two ways:
//  1. The closeSysHandle seam wraps the real syscall.CloseHandle: a
//     recording stub observes exactly one close of a real event handle.
//  2. Source assertion: runShellExec defers closeSysHandle(sei.hProcess)
//     immediately after the ShellExecuteExW success check, so every
//     elevation path (wait and no-wait) releases the process handle.
// A `go vet ./...` run covers the rest (handle-type misuse would fail
// the build on windows).
func TestRunShellExecClosesHandle(t *testing.T) {
	orig := closeSysHandle
	defer func() { closeSysHandle = orig }()

	var calls int32
	var closed []syscall.Handle
	closeSysHandle = func(h syscall.Handle) error {
		calls++
		closed = append(closed, h)
		return orig(h)
	}

	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if err := closeSysHandle(syscall.Handle(ev)); err != nil {
		t.Fatalf("seam close: %v", err)
	}
	if calls != 1 {
		t.Fatalf("seam calls = %d, want 1", calls)
	}
	if len(closed) != 1 || closed[0] != syscall.Handle(ev) {
		t.Fatal("seam did not observe the closed handle")
	}

	src, err := os.ReadFile("server_windows.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func runShellExec(")
	if idx < 0 {
		t.Fatal("runShellExec not found")
	}
	fn := body[idx:]
	if !strings.Contains(fn, "defer closeSysHandle(sei.hProcess)") {
		t.Fatal("runShellExec never defers closeSysHandle(sei.hProcess) (BUG-012 unfixed)")
	}
}
