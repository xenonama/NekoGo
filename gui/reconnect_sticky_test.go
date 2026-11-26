package main

// The Reconnect setting: off / restart / sticky.
//
// Reconnect itself is one shape — take the group down, raise it again on
// the same profile — but what happens to the exit chain in between is a
// choice. "restart" (option 1 in the UI) cold-starts everything, which is
// what the button has always done. "sticky" (option 2) hands the SNI-spoof
// chain's aether child to the next connect instead of reaping it, because a
// cold Psiphon or Tor session costs minutes and the reason a person presses
// Reconnect is the tunnel underneath, not the hop above it. "off" removes
// the button entirely.
//
// The Aether leg is not part of the offer: its chain runs inside the
// aether.exe process, so restarting the engine restarts the chain with it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalizeReconnectMode: the mode is a closed set, a legacy config
// that never carried the key keeps the behaviour it shipped with, and
// anything else in the file is canonicalised rather than passed through
// to a switch statement that would have to guess.
func TestNormalizeReconnectMode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", reconnectRestart},
		{"  ", reconnectRestart},
		{"STICKY", reconnectSticky},
		{" sticky ", reconnectSticky},
		{"off", reconnectOff},
		{"Off", reconnectOff},
		{"restart", reconnectRestart},
		{"whatever", reconnectRestart},
	} {
		s := DefaultSettings()
		s.ReconnectMode = tc.in
		if err := normalizeReconnectMode(&s); err != nil {
			t.Fatalf("normalizeReconnectMode(%q): %v", tc.in, err)
		}
		if s.ReconnectMode != tc.want {
			t.Errorf("ReconnectMode(%q) = %q, want %q", tc.in, s.ReconnectMode, tc.want)
		}
	}
	// A fresh install must land on the shape the button has always had.
	if got := DefaultSettings().ReconnectMode; got != reconnectRestart {
		t.Errorf("DefaultSettings ReconnectMode = %q, want %q", got, reconnectRestart)
	}
}

// TestReconnectKeepsChainOnlyInStickyMode: the one-shot handoff is armed
// from the stored mode alone, so "off" and "restart" can never leave a
// chain child standing after a Disconnect.
func TestReconnectKeepsChainOnlyInStickyMode(t *testing.T) {
	for _, mode := range []string{reconnectOff, reconnectRestart, "", "sideways"} {
		if reconnectKeepsChain(mode) {
			t.Errorf("reconnectKeepsChain(%q) = true", mode)
		}
	}
	if !reconnectKeepsChain(reconnectSticky) {
		t.Error("reconnectKeepsChain(sticky) = false")
	}
}

// chainGroup builds the committed CoreSpoof group a sticky reconnect
// would hand over: a running chain manager and the ports it listens on.
func chainGroup(driver string) (*CoreManager, *AetherManager) {
	mgr := NewPsiphonManager(nil)
	if driver == chainDriverTor {
		mgr = NewTorManager(nil)
	}
	mgr.mu.Lock()
	mgr.running = true
	mgr.mu.Unlock()
	m := &CoreManager{
		activeCore: CoreSpoof, running: true,
		chainActive: true, chainKind: driver,
		chainPort: 40556, chainUpstreamPort: 40557,
		psiphonMgr: NewPsiphonManager(nil),
		torMgr:     NewTorManager(nil),
	}
	if driver == chainDriverTor {
		m.torMgr = mgr
	} else {
		m.psiphonMgr = mgr
	}
	return m, mgr
}

// TestStopWithChainKeepHandsTheLegToTheNextStart: the keep is exactly
// one Stop wide — the chain leg and its bookkeeping survive it, the flag
// is spent inside the same call, and the next teardown takes the leg
// down like any other. Leaving the flag armed past its own Stop is how a
// later plain Disconnect would orphan a Psiphon child.
func TestStopWithChainKeepHandsTheLegToTheNextStart(t *testing.T) {
	m, mgr := chainGroup(chainDriverPsiphon)
	_ = m.StopWithChainKeep() // the nil transport errors; the state is the point
	if !m.chainActive || m.chainKind != chainDriverPsiphon || m.chainPort != 40556 ||
		m.chainUpstreamPort != 40557 {
		t.Fatalf("the kept leg lost its bookkeeping: %+v", m)
	}
	if !mgr.Running() {
		t.Fatal("StopWithChainKeep reaped the chain child it was told to hand over")
	}
	m.mu.RLock()
	armed := m.chainKeep
	m.mu.RUnlock()
	if armed {
		t.Fatal("the keep survived the Stop it was armed for")
	}
	// The next ordinary Disconnect is a plain one.
	_ = m.Stop()
	if m.chainActive || m.chainKind != "" || m.chainPort != 0 {
		t.Fatalf("chain state survived a plain Stop: active=%v kind=%q port=%d",
			m.chainActive, m.chainKind, m.chainPort)
	}
	if mgr.Running() {
		t.Fatal("a plain Stop left the chain child running")
	}
}

// TestStopWithoutKeepTakesTheChainDown is the control: nothing about a
// non-sticky reconnect or a Disconnect may leave the leg standing.
func TestStopWithoutKeepTakesTheChainDown(t *testing.T) {
	m, mgr := chainGroup(chainDriverTor)
	_ = m.Stop()
	if m.chainActive {
		t.Fatal("Stop kept a chain leg it was never asked to keep")
	}
	if mgr.Running() {
		t.Fatal("Stop left the Tor child running")
	}
}

// TestChainHandoffOffersOnlyALiveSameDriverLeg: adoption is what makes
// the keep worth having, and it is only ever offered for the driver this
// connect actually wants, with a child that is really there.
func TestChainHandoffOffersOnlyALiveSameDriverLeg(t *testing.T) {
	m, _ := chainGroup(chainDriverPsiphon)
	if port, up, ok := m.chainHandoff(chainDriverPsiphon); !ok || port != 40556 || up != 40557 {
		t.Fatalf("handoff for the same driver = %d/%d ok=%v", port, up, ok)
	}
	if _, _, ok := m.chainHandoff(chainDriverTor); ok {
		t.Fatal("the Psiphon leg was offered to a Tor connect")
	}
	// The child died on its own (a crash the UI has not noticed yet).
	mgr := m.psiphonMgr
	mgr.mu.Lock()
	mgr.running = false
	mgr.mu.Unlock()
	if _, _, ok := m.chainHandoff(chainDriverPsiphon); ok {
		t.Fatal("a dead child was offered as a live leg")
	}
	// No chain at all in the previous group.
	bare := &CoreManager{activeCore: CoreSpoof}
	if _, _, ok := bare.chainHandoff(chainDriverPsiphon); ok {
		t.Fatal("an empty group offered a chain leg")
	}
}

// TestRetireChainLegClearsBothStateAndChild: a connect that does not take
// the offered leg must not leave it serving on a port its config never
// names.
func TestRetireChainLegClearsBothStateAndChild(t *testing.T) {
	m, mgr := chainGroup(chainDriverPsiphon)
	m.retireChainLeg()
	if m.chainActive || m.chainKind != "" || m.chainPort != 0 || m.chainUpstreamPort != 0 {
		t.Fatalf("retire left the bookkeeping: %+v", m)
	}
	if mgr.Running() {
		t.Fatal("retire left the child running")
	}
	// Idle managers and an empty group are no-ops, not panics.
	(&CoreManager{}).retireChainLeg()
}

// TestFailedReconnectLeavesNoOrphanChain: the handoff is a promise to the
// next connect, and a reconnect whose connect never reached the cores (an
// unknown profile, a pre-flight refusal) never made it. The child goes
// down with the failed call rather than holding two loopback ports and a
// chainActive the GUI would keep reporting as a chain that is up.
func TestFailedReconnectLeavesNoOrphanChain(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 256))
	m, mgr := chainGroup(chainDriverPsiphon)
	app.cores = m
	s := store.GetSettings()
	s.ReconnectMode = reconnectSticky
	if err := app.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := app.Reconnect("no-such-profile"); err == nil {
		t.Fatal("Reconnect accepted a profile that does not exist")
	}
	if mgr.Running() {
		t.Error("the failed reconnect left the chain child holding its ports")
	}
	if active, _, _ := m.ChainRun(); active {
		t.Error("the GUI still reports a chain nothing runs")
	}
}

// TestSaveSettingsStoresTheReconnectMode: the button's visibility is a
// persisted choice, so the mode has to reach the file — including the
// legacy config that carries no key at all.
func TestSaveSettingsStoresTheReconnectMode(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	s := store.GetSettings()
	s.ReconnectMode = " sticky "
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if got := store.GetSettings().ReconnectMode; got != reconnectSticky {
		t.Fatalf("stored ReconnectMode = %q, want %q", got, reconnectSticky)
	}
	s = store.GetSettings()
	s.ReconnectMode = "nonsense"
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings rejected a bad mode instead of canonicalising it: %v", err)
	}
	if got := store.GetSettings().ReconnectMode; got != reconnectRestart {
		t.Fatalf("stored ReconnectMode = %q, want the %q fallback", got, reconnectRestart)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reconnectMode"`) {
		t.Fatal("config.json carries no reconnectMode")
	}
}

// TestReconnectRPCAndButtonWiring: the mode drives two things the user can
// see — which call the button makes and whether it shows at all — and the
// Chain tab is where it is set. The frontend has no test runner, so this
// pins the shipped source the way the other panel tests do.
func TestReconnectRPCAndButtonWiring(t *testing.T) {
	app := readFrontendSrc(t, "api.ts")
	if !strings.Contains(app, "App.Reconnect(") {
		t.Fatal("api.ts does not expose the Reconnect call")
	}
	store := readFrontendSrc(t, "stores/connection.ts")
	if !strings.Contains(store, "backend.reconnect") {
		t.Fatal("the connection store never reaches the backend's sticky Reconnect")
	}
	// off hides the button; sticky shows it whether or not a tunnel runs.
	card := readFrontendSrc(t, "components/ConnectionCard.vue")
	if !strings.Contains(card, "reconnectVisible") {
		t.Fatal("ConnectionCard.vue has no reconnect visibility rule")
	}
	body := fnBody(t, card, "reconnectVisible")
	for _, want := range []string{"off", "sticky"} {
		if !strings.Contains(body, want) {
			t.Errorf("the visibility rule does not handle the %q mode: %s", want, body)
		}
	}
	panel := readFrontendSrc(t, "components/ChainPanel.vue")
	if !strings.Contains(panel, "reconnectMode") {
		t.Fatal("ChainPanel.vue has no control for reconnectMode")
	}
	types := readFrontendSrc(t, "types/index.ts")
	if !strings.Contains(types, "reconnectMode") {
		t.Fatal("the Settings mirror has no reconnectMode field")
	}
	model, err := os.ReadFile(filepath.Join("frontend", "bindings", "nekogo", "models.ts"))
	if err != nil {
		t.Fatalf("reading bindings/nekogo/models.ts: %v", err)
	}
	if !strings.Contains(string(model), `"reconnectMode"`) {
		t.Fatal("bindings/nekogo/models.ts does not declare reconnectMode; regenerate the bindings")
	}
	svc, err := os.ReadFile(filepath.Join("frontend", "bindings", "nekogo", "app.ts"))
	if err != nil {
		t.Fatalf("reading bindings/nekogo/app.ts: %v", err)
	}
	if !strings.Contains(string(svc), "export function Reconnect(") {
		t.Fatal("the generated App binding has no Reconnect method; regenerate the bindings")
	}
}
