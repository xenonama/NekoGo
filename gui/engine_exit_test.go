package main

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLineRingKeepsNewest(t *testing.T) {
	var r lineRing
	r.max = 3
	for _, s := range []string{"one", "two", "", "three", "four"} {
		r.push(s)
	}
	got := r.tail(3)
	want := []string{"two", "three", "four"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tail = %v, want %v", got, want)
	}
	if one := r.tail(1); len(one) != 1 || one[0] != "four" {
		t.Fatalf("tail(1) = %v, want [four]", one)
	}
	r.reset()
	if len(r.tail(5)) != 0 {
		t.Fatal("reset kept lines")
	}
}

func TestEngineExitSummaryCarriesLastWords(t *testing.T) {
	ev := EngineExit{
		Engine:    "aether",
		Err:       errors.New("exit status 1"),
		Uptime:    2*time.Minute + 5*time.Second,
		LastLines: []string{"first", "wireguard: no endpoint"},
	}
	got := ev.Summary()
	for _, want := range []string{"aether engine exited unexpectedly", "2m5s", "exit status 1", "wireguard: no endpoint"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Summary() = %q, missing %q", got, want)
		}
	}
}

// A child that dies during warmup must fail at once and quote its last
// output, not wait out aetherWarmup (PattN CouldNotStart).
func TestAetherStartupExitFailsFastWithLastWords(t *testing.T) {
	m := NewAetherManager(nil)
	m.recent.push("wireguard: no endpoint")
	exited := make(chan error, 1)
	exited <- errors.New("exit status 1")
	start := time.Now()
	err := m.waitSocksOrExit(1, 30*time.Second, exited, nil)
	if err == nil {
		t.Fatal("expected start error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %v, want fail-fast", elapsed)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("error = %v, want an exit-during-startup classification", err)
	}
	if !strings.Contains(err.Error(), "wireguard: no endpoint") {
		t.Fatalf("error = %v, want the last output line", err)
	}
}

// A committed engine that exits on its own reports an EngineExit.
func TestAetherWatchChildReportsCommittedExit(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "exit", "3")
	} else {
		cmd = exec.Command("false")
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn trivial child: %v", err)
	}
	m := &AetherManager{cmd: cmd, running: true, startedAt: time.Now()}
	m.recent.push("last words")
	events := make(chan EngineExit, 1)
	m.SetExitHandler(func(ev EngineExit) { events <- ev })

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.watchChild(cmd, nil, make(chan struct{}))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchChild did not return")
	}
	select {
	case ev := <-events:
		if ev.Engine != "aether" {
			t.Fatalf("engine = %q, want aether", ev.Engine)
		}
		if ev.Err == nil {
			t.Fatal("missing exit error")
		}
		if len(ev.LastLines) == 0 || ev.LastLines[len(ev.LastLines)-1] != "last words" {
			t.Fatalf("last lines = %v", ev.LastLines)
		}
		if ev.Uptime <= 0 {
			t.Fatalf("uptime = %v, want > 0", ev.Uptime)
		}
	default:
		t.Fatal("no EngineExit reported for a committed death")
	}
	if m.Running() {
		t.Fatal("still running after the child exited")
	}
}

// A death report for a group that is not running is ignored.
func TestCoreManagerIgnoresIdleEngineExit(t *testing.T) {
	m := &CoreManager{}
	called := false
	m.SetFaultHandler(func(EngineExit) { called = true })
	m.handleEngineExit(EngineExit{Engine: "aether"})
	if called {
		t.Fatal("fault handler ran with no active group")
	}
}

func TestEngineInGroupLocked(t *testing.T) {
	m := &CoreManager{activeCore: CoreSingBox}
	if !m.engineInGroupLocked("sing-box") {
		t.Fatal("sing-box should belong to a sing-box group")
	}
	if m.engineInGroupLocked("aether") {
		t.Fatal("aether should not belong to a plain sing-box group")
	}
	m.aetherActive = true
	if !m.engineInGroupLocked("aether") || !m.engineInGroupLocked("xray") {
		t.Fatal("aether group should include the engine and the main core")
	}
	m.aetherActive = false
	m.activeCore = CoreSpoof
	if !m.engineInGroupLocked("sing-box") {
		t.Fatal("spoof group runs the sing-box transport")
	}
	m.spoofXray = true
	if m.engineInGroupLocked("sing-box") || !m.engineInGroupLocked("xray") {
		t.Fatal("a spoof group on xray must claim the xray transport, not sing-box")
	}
}
