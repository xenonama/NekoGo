package main

// Engine death reports are asynchronous. The report for the engine a newer
// Start replaced can land while that newer group is live — the spawn the
// spoof/aether managers reap has already committed its own state by then,
// and CoreManager's generation check only runs inside teardownFaultedGroup.
//
// handleEngineExit used to unpublished the shared loopback dial ports
// before any of that: it cleared aether/spoof front on sight. The new
// group then rendered its configs with no spoof front and no aether
// listener while both engines were healthy — "connected, no traffic", or a
// loopback profile dialling a port nobody owns.
//
// faultRaceHook is the seam that makes the interleaving deterministic: it
// runs in the exact window a concurrent Start lands in.

import (
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// The spoof half: a fault from the replaced engine must leave the front
// port the current generation published, and the current generation
// itself, untouched.
func TestEngineFaultRacingANewerStartKeepsTheLiveFrontPort(t *testing.T) {
	m := NewCoreManager(NewSingBoxManager("", nil), NewXrayManager("", "", nil))
	m.SetSpoofManager(NewSpoofManager(nil))
	t.Cleanup(clearSpoofFrontPort)
	clearSpoofFrontPort()

	m.mu.Lock()
	m.running = true
	m.activeCore = CoreSpoof
	m.activeID = "p1"
	staleEpoch := m.epoch
	m.mu.Unlock()
	setSpoofFrontPort(40999) // the engine that is about to be reported dead

	// A connect for another profile wins the race while this fault is in
	// flight: new generation, new published port.
	m.faultRaceHook = func() {
		m.mu.Lock()
		m.epoch++
		m.running = true
		m.activeCore = CoreSpoof
		m.activeID = "p2"
		m.mu.Unlock()
		setSpoofFrontPort(41001)
	}
	defer func() { m.faultRaceHook = nil }()

	m.handleEngineExit(newEngineExit("spoof", nil, errors.New("engine died"), time.Now().Add(-time.Minute), nil))

	if p, ok := currentSpoofFrontPort(); !ok || p != 41001 {
		t.Fatalf("the stale fault took down the live front port: %d/%v, want 41001 still published", p, ok)
	}
	m.mu.Lock()
	running, id, epoch := m.running, m.activeID, m.epoch
	m.mu.Unlock()
	if !running || id != "p2" {
		t.Fatalf("the stale fault tore down the newer group: running=%v id=%q", running, id)
	}
	if epoch == staleEpoch {
		t.Fatal("the fault's teardown was accepted although a newer generation is live")
	}
}

// The aether half, same shape.
func TestEngineFaultRacingANewerStartKeepsTheLiveAetherPort(t *testing.T) {
	m := NewCoreManager(NewSingBoxManager("", nil), NewXrayManager("", "", nil))
	m.SetAetherManager(NewAetherManager(nil))
	t.Cleanup(clearAetherActivePort)
	clearAetherActivePort()

	m.mu.Lock()
	m.running = true
	m.activeCore = CoreSingBox
	m.activeID = "p1"
	m.aetherActive = true
	m.mu.Unlock()
	setAetherActivePort(20000)

	m.faultRaceHook = func() {
		m.mu.Lock()
		m.epoch++
		m.running = true
		m.activeCore = CoreSingBox
		m.activeID = "p2"
		m.aetherActive = true
		m.mu.Unlock()
		setAetherActivePort(20001)
	}
	defer func() { m.faultRaceHook = nil }()

	m.handleEngineExit(newEngineExit("aether", nil, errors.New("engine died"), time.Now().Add(-time.Minute), nil))

	if p, ok := currentAetherActivePort(); !ok || p != 20001 {
		t.Fatalf("the stale fault took down the live aether port: %d/%v, want 20001 still published", p, ok)
	}
	m.mu.Lock()
	running, id := m.running, m.activeID
	m.mu.Unlock()
	if !running || id != "p2" {
		t.Fatalf("the stale fault tore down the newer group: running=%v id=%q", running, id)
	}
}

// The mirror-image requirement: a real death DOES unpublish its own port.
// The spoof reaper already does this value-scoped; the aether one relied on
// handleEngineExit's blind clear, so the fault path cannot simply stop
// clearing without it. A genuine crash still has to leave nothing
// published, otherwise every later probe is refused until the app restarts.
func TestAetherReaperUnpublishesItsOwnPortOnDeath(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "exit", "3")
	} else {
		cmd = exec.Command("false")
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn trivial child: %v", err)
	}
	m := &AetherManager{cmd: cmd, running: true, port: 20002, startedAt: time.Now()}
	t.Cleanup(clearAetherActivePort)
	setAetherActivePort(20002)

	// No exit handler wired: whatever unpublishes the port must be the
	// reaper that owns the dying instance.
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.watchChild(cmd, nil, make(chan struct{}))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watchChild did not return")
	}

	if p, ok := currentAetherActivePort(); ok {
		t.Fatalf("the dead aether engine left port %d published: its listener is gone, so builders still route traffic to it", p)
	}
}
