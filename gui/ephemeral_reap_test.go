package main

// TestEphemeralRunReapsOnCrash (BUG-005): killing an ephemeral probe
// core externally must clear the ephemeral state within 2s (no zombie
// handle, no stale running flag) and, for xray, remove the temp config
// dir holding secrets.

import (
	"os"
	"testing"
	"time"
)

func killEphemeralChild(t *testing.T, pid int) {
	t.Helper()
	// Re-resolve the PID instead of reusing the manager's handle: this
	// is an external kill, exactly like a crash from the OS side.
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill(%d): %v", pid, err)
	}
}

func TestEphemeralRunReapsOnCrash(t *testing.T) {
	coreBin, xrayBin := testCoreBins(t)
	logs := make(chan LogEntry, 512)
	settings := DefaultSettings()

	pollState := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s not settled within 2s", what)
	}

	t.Run("xray", func(t *testing.T) {
		mgr := NewXrayManager(xrayBin, "", logs)
		port, err := mgr.EphemeralRun(directProfile(CoreXray), settings, 0)
		if err != nil {
			t.Fatalf("EphemeralRun: %v", err)
		}
		if port <= 0 {
			t.Fatal("EphemeralRun reported no port")
		}
		mgr.mu.Lock()
		pid := mgr.ephCmd.Process.Pid
		dir := mgr.ephDir
		mgr.mu.Unlock()
		if dir == "" {
			t.Fatal("ephemeral xray has no temp dir")
		}
		killEphemeralChild(t, pid)
		pollState(func() bool {
			mgr.mu.Lock()
			defer mgr.mu.Unlock()
			return !mgr.ephRunning && mgr.ephCmd == nil
		}, "xray ephemeral state clear")
		pollState(func() bool {
			_, err := os.Stat(dir)
			return os.IsNotExist(err)
		}, "xray ephemeral temp dir removal")
	})

	t.Run("singbox", func(t *testing.T) {
		mgr := NewSingBoxManager(coreBin, logs)
		port, err := mgr.EphemeralRun(directProfile(CoreSingBox), settings, 0)
		if err != nil {
			t.Fatalf("EphemeralRun: %v", err)
		}
		if port <= 0 {
			t.Fatal("EphemeralRun reported no port")
		}
		mgr.mu.Lock()
		pid := mgr.ephCmd.Process.Pid
		mgr.mu.Unlock()
		killEphemeralChild(t, pid)
		pollState(func() bool {
			mgr.mu.Lock()
			defer mgr.mu.Unlock()
			return !mgr.ephRunning && mgr.ephCmd == nil
		}, "sing-box ephemeral state clear")
	})
}
