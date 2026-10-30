package process

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestStopBeforeStart — Stop on a fresh (never started) Process must
// not panic on the nil cmd (P3-12).
func TestStopBeforeStart(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop panicked: %v", r)
		}
	}()
	p := NewProcess("nonexistent-binary-xyz", nil, true)
	p.Stop()
	var nilP *Process
	nilP.Stop()
}

// TestStopAfterKillReaps — Stop after a real start kills without
// blocking and never panics.
func TestStopAfterKillReaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sleep helper")
	}
	// Prime cmd without going through Start: a live child we own.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep unavailable: %v", err)
	}
	p := &Process{cmd: cmd}
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Stop panicked: %v", r)
			}
			close(done)
		}()
		p.Stop()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked")
	}
	// Give the reap goroutine a moment; the child must be reaped
	// without blocking Stop (already proven above).
	time.Sleep(500 * time.Millisecond)
}
