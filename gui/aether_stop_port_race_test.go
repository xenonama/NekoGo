package main

// stopChild reaps the child it captured under the lock, then spends up to
// three seconds killing the tree and waiting for it. CoreManager.Start
// drops its own lock before the aether engine starts, and the fault
// handler's teardownFaultedGroup runs on the reaper's goroutine — so a
// newer connect can spawn, become ready and publish its dial port inside
// that window. watchChild already unpublishes by value for exactly this
// reason; stopChild cleared on sight, and the live engine's port went with
// it: the outbound builders then render "no engine" while aether.exe is
// healthy and serving.

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// trivialChild is a process that exits at once, so the reap is quick and
// the test never waits on a real engine.
func trivialChild(t *testing.T) *exec.Cmd {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "exit", "3")
	} else {
		cmd = exec.Command("false")
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn trivial child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	return cmd
}

func TestAetherStopRacingANewerConnectKeepsTheLivePort(t *testing.T) {
	old := trivialChild(t)
	// held keeps stopChild inside its reap window: it waits on this as the
	// child's done channel, so the interleaving is deterministic rather
	// than a sleep the scheduler can win.
	held := make(chan struct{})
	m := NewAetherManager(nil)
	m.mu.Lock()
	m.cmd, m.running, m.port, m.done, m.startedAt = old, true, 20010, held, time.Now()
	m.mu.Unlock()
	t.Cleanup(clearAetherActivePort)
	setAetherActivePort(20010)

	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop() }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		swapped := !m.running
		m.mu.Unlock()
		if swapped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Stop never committed its state swap")
		}
		time.Sleep(time.Millisecond)
	}

	// The newer connect: its own spawn, its own token, its own port.
	fresh := trivialChild(t)
	m.mu.Lock()
	m.token++
	m.cmd, m.running, m.port, m.done = fresh, true, 20011, make(chan struct{})
	m.mu.Unlock()
	setAetherActivePort(20011)

	close(held)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}

	if p, ok := currentAetherActivePort(); !ok || p != 20011 {
		t.Fatalf("the stale stop unpublished the live aether port: %d/%v, want 20011 still published", p, ok)
	}
}

// The mirror image: stopping the engine that owns the published port must
// still unpublish it, or every later probe is refused until the app
// restarts. The fix cannot simply drop the clear.
func TestAetherStopUnpublishesItsOwnPort(t *testing.T) {
	cmd := trivialChild(t)
	m := NewAetherManager(nil)
	m.mu.Lock()
	m.cmd, m.running, m.port, m.startedAt = cmd, true, 20012, time.Now()
	m.mu.Unlock()
	t.Cleanup(clearAetherActivePort)
	setAetherActivePort(20012)

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p, ok := currentAetherActivePort(); ok {
		t.Fatalf("the stopped engine left port %d published: its listener is gone, so builders still route traffic to it", p)
	}
}
