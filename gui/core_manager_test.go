package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSingBoxIsRunningFalseAfterWatchedChildExits — the watchChild
// reaper (Part 2 fix) must flip running=false so IsRunning reports
// truthfully after the child dies (P3-4).
func TestSingBoxIsRunningFalseAfterWatchedChildExits(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "exit", "0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn trivial child: %v", err)
	}
	m := &SingBoxManager{cmd: cmd, running: true}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.watchChild(cmd, nil)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchChild did not return")
	}
	if m.IsRunning() {
		t.Fatal("IsRunning true after watched child exited")
	}
}

func TestGetStatsNilManagers(t *testing.T) {
	m := &CoreManager{activeCore: CoreSingBox, running: true}
	if _, err := m.GetStats(); err == nil {
		t.Fatal("expected error for nil sing-box manager")
	}
	m2 := &CoreManager{activeCore: CoreXray, running: true}
	if _, err := m2.GetStats(); err == nil {
		t.Fatal("expected error for nil xray manager")
	}
	// Idle manager reports not-running without touching managers.
	m3 := &CoreManager{}
	st, err := m3.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatalf("got %+v, want idle", st)
	}
}

func TestStartReturnsStopError(t *testing.T) {
	// running with a nil xray manager: stopLocked fails, Start must
	// propagate the stop error instead of silently continuing.
	m := &CoreManager{activeCore: CoreXray, running: true, activeID: "old"}
	err := m.Start(&Profile{Name: "p", Core: CoreSingBox, Protocol: "vless"}, DefaultSettings())
	if err == nil {
		t.Fatal("expected stop error from Start")
	}
	if !strings.Contains(err.Error(), "stop previous core") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStoppedManagersReleaseCommandImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*CoreManager, *exec.Cmd) error
	}{
		{"sing-box", func(m *CoreManager, cmd *exec.Cmd) error {
			m.singboxMgr = &SingBoxManager{cmd: cmd, running: true}
			return m.singboxMgr.Stop()
		}},
		{"xray", func(m *CoreManager, cmd *exec.Cmd) error {
			m.xrayMgr = &XrayManager{cmd: cmd, running: true}
			return m.xrayMgr.Stop()
		}},
		{"upstream sing-box", func(m *CoreManager, cmd *exec.Cmd) error {
			m.upstreamMgr = &UpstreamSingBoxManager{cmd: cmd, running: true}
			return m.upstreamMgr.Stop()
		}},
		{"mihomo", func(m *CoreManager, cmd *exec.Cmd) error {
			m.mihomoMgr = &MihomoManager{cmd: cmd, running: true}
			return m.mihomoMgr.Stop()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("unused-core.exe")
			cores := &CoreManager{}
			if err := tc.stop(cores, cmd); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if cores.usesImage(cmd.Path) {
				t.Fatal("stopped manager still reports its executable image in use")
			}
		})
	}
}
