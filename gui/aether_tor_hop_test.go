package main

// aether_tor_hop_test.go — the Aether leg's Tor hop runs on tor.exe
// behind the tunnel, and on arti only where no daemon exists.
//
// Settings.AetherTor used to mean one thing: hand the engine a second
// --tor flag and let its linked arti client dial out through the tunnel
// it just built. That client is the one that stops at 15% here — it
// reaches its guards over the bridges and no directory ever comes back —
// while the same shape with the standalone daemon bootstrapped to 100%
// and carried traffic (measured on this machine, 2026-11-15/16). The SNI
// spoof leg learned that in startTorChainLeg; this is the other leg, and
// the shape is different: the hop is not a child in front of the
// transport but a client behind the engine, so the connect has to pick
// which listener the main core dials — the tunnel's (arti carrying the
// hop inside it) or the daemon's (the tunnel as the daemon's front).
//
// What the tests pin is the choice, the render each choice produces, the
// ports published afterwards, and the unwind order: a daemon left
// running behind a tunnel that failed to start reaches the internet
// through nothing and outlives the connect.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aetherHopProfile is an Aether node with the fields the hop decision
// reads: [tor] is Aether.Tor and [command] a hand-written engine line.
func aetherHopProfile(tor, command string) *Profile {
	return &Profile{
		ID: "a1", Name: "aether hop", Protocol: "aether",
		Aether: aetherSettings{Protocol: "wg", Tor: tor, Command: command},
	}
}

// aetherHopArmed is the Chain tab with the Aether Tor toggle on and the
// bridge options normalized, as SaveSettings leaves them.
func aetherHopArmed(t *testing.T, bridges, lines string) Settings {
	t.Helper()
	s := torDaemonTestSettings(t, bridges, lines)
	s.AetherTor = true
	return s
}

// TestAetherTorHopPicksTheDaemonBehindTheTunnel: the choice, per shape.
// tor.exe wins whenever the hop is the app's dialled one and the profile
// left the engine's arguments to NekoGo; every case that does not say so
// keeps today's arti run and says why in the words the log quotes.
func TestAetherTorHopPicksTheDaemonBehindTheTunnel(t *testing.T) {
	ch := make(chan LogEntry, 256)
	mgr := NewTorManager(ch)
	present := installFakeTor(t)
	absent := filepath.Join(t.TempDir(), "absent", torExeName)

	// The profile the connect renders when the toggle armed the hop.
	armed := aetherHopArmed(t, "never", "")
	chained := aetherTorChainProfile(aetherHopProfile("off", ""), armed)

	own := aetherHopProfile("chain", "")
	own.Aether.TorBridges = "own"
	own.Aether.TorBridgeLines = torTestObfs4("192.0.2.9", 443)

	both := aetherHopProfile("chain", "")
	both.Aether.Psiphon = "chain"

	handWritten := aetherHopProfile("chain", "--bind 127.0.0.1:41000 --tor")

	defer func() { torExeOverride = "" }()
	for _, tc := range []struct {
		name    string
		exe     string
		mgr     *AetherManager
		p       *Profile
		want    bool
		wantSub string
		wantNo  string
	}{
		{"a daemon on the machine carries it", present, mgr, chained, true, torExeName, "arti"},
		{"no daemon: arti keeps the hop", absent, mgr, chained, false, "arti", ""},
		{"a profile that named its own chain hop", present, mgr, own, true, torExeName, "arti"},
		{"the engine command line owns its exits", present, mgr, handWritten, false, "command", ""},
		{"psiphon wins the dialled listener", present, mgr, both, false, "", ""},
		{"reverse tor is not the dialled hop", present, mgr, aetherHopProfile("reverse", ""), false, "", ""},
		{"no hop wanted at all", present, mgr, aetherHopProfile("off", ""), false, "", ""},
		{"a plain profile runs no aether engine", present, mgr,
			&Profile{ID: "s1", Protocol: "ss"}, false, "", ""},
		{"no tor manager is wired", present, nil, chained, false, "manager", ""},
	} {
		torExeOverride = tc.exe
		got, note := aetherTorHopDecision(tc.p, armed, tc.mgr)
		if got != tc.want {
			t.Errorf("%s: daemon hop = %v, want %v (note %q)", tc.name, got, tc.want, note)
		}
		if tc.wantSub == "" && tc.wantNo == "" {
			if note != "" {
				t.Errorf("%s: a run with no tor hop to explain logged %q", tc.name, note)
			}
			continue
		}
		if tc.wantSub != "" && !strings.Contains(note, tc.wantSub) {
			t.Errorf("%s: note %q does not name %q", tc.name, note, tc.wantSub)
		}
		if tc.wantNo != "" && strings.Contains(strings.ToLower(note), strings.ToLower(tc.wantNo)) {
			t.Errorf("%s: note %q still says %q", tc.name, note, tc.wantNo)
		}
		if !strings.Contains(note, torExeName) && !strings.Contains(note, "tor") {
			t.Errorf("%s: note %q never mentions the hop", tc.name, note)
		}
	}
}

// TestAetherTunnelOnlyProfileLeavesTheTorModeOutOfTheEngine: the split
// is only real if the engine's own arguments stop carrying the hop. The
// arti render the fallback still uses is asserted alongside it, so a
// change to either shape is visible here.
func TestAetherTunnelOnlyProfileLeavesTheTorModeOutOfTheEngine(t *testing.T) {
	armed := aetherHopArmed(t, "own", torTestObfs4("198.51.100.7", 443))
	chained := aetherTorChainProfile(aetherHopProfile("off", ""), armed)
	only := aetherTunnelOnlyProfile(chained)
	if only == chained {
		t.Fatal("aetherTunnelOnlyProfile handed back the profile it was given: the engine would still run tor")
	}

	hopArgs := buildAetherArguments(chained, 41000)
	if !containsStr(hopArgs, "--tor") {
		t.Errorf("the arti render lost its hop: %v", hopArgs)
	}
	if p, ok := aetherPortAfter(hopArgs, aetherFlagTorBind); !ok || p != 41000 {
		t.Errorf("the arti render dials %d/%v, want the engine's tor listener on 41000", p, ok)
	}
	if aetherEngineWarmup(hopArgs) != torChainWarmup {
		t.Error("an engine that carries the hop no longer waits a full bootstrap")
	}

	onlyArgs := buildAetherArguments(only, 41000)
	if containsStr(onlyArgs, "--tor") || containsStr(onlyArgs, aetherFlagTorBind) {
		t.Errorf("the split render still runs tor inside the engine: %v", onlyArgs)
	}
	if p, ok := aetherPortAfter(onlyArgs, aetherFlagBind); !ok || p != 41000 {
		t.Errorf("the split render binds %d/%v, want the tunnel on the dialed port 41000", p, ok)
	}
	if aetherEngineWarmup(onlyArgs) != aetherWarmup {
		t.Error("a tunnel with no hop in it still waits a bootstrap's 5m0s for readiness")
	}
	// The stored profile is never rewritten by a decision made at connect
	// time — the same rule aetherTorChainProfile keeps.
	if !aetherProfileTorChain(chained) {
		t.Error("taking the hop out of the render took it out of the chained profile too")
	}
}

// TestAetherTorHopSettingsFollowTheProfileThatNamedTheHop: the daemon has
// to be handed the bridges the engine would have dialled — the Chain
// tab's when the toggle armed the hop, the profile's own when the
// profile named it. Reading the settings instead would silently dial
// different bridges than the ones the connect rendered.
func TestAetherTorHopSettingsFollowTheProfileThatNamedTheHop(t *testing.T) {
	own := aetherHopProfile("chain", "")
	own.Aether.TorBridges = "own"
	own.Aether.TorBridgeLines = torTestObfs4("192.0.2.9", 443)
	plain := DefaultSettings()
	s := aetherTorHopSettings(plain, own)
	if s.ChainTorBridges != "own" {
		t.Errorf("Bridges = %q, want the profile's own", s.ChainTorBridges)
	}
	if s.ChainTorBridgeLines != own.Aether.TorBridgeLines {
		t.Errorf("the daemon was handed %q, want the lines the profile names", s.ChainTorBridgeLines)
	}

	armed := aetherHopArmed(t, "own", torTestObfs4("198.51.100.7", 443))
	before := armed.ChainTorBridgeLines
	chained := aetherTorChainProfile(aetherHopProfile("off", ""), armed)
	s2 := aetherTorHopSettings(armed, chained)
	if s2.ChainTorBridgeLines != before {
		t.Errorf("the toggle's lines did not reach the daemon: %q", s2.ChainTorBridgeLines)
	}
	if armed.ChainTorBridgeLines != before {
		t.Errorf("the connect's own settings were rewritten while choosing the hop: %q", armed.ChainTorBridgeLines)
	}
}

// TestAetherTorHopRunsTheDaemonBehindTheTunnelAndPublishesIt is the whole
// shape: the daemon listens on a port of its own, dials through the
// tunnel, and the port the main core renders against is the daemon's.
// The arti leg published the engine's tor-bind listener instead; that is
// the number a user's connect must NOT get once the daemon carries it.
func TestAetherTorHopRunsTheDaemonBehindTheTunnelAndPublishesIt(t *testing.T) {
	bin := installFakeTor(t)
	work := withTorDaemonHooks(t, bin)
	// The published port is process state: a leftover from another test
	// file would answer currentAetherActivePort before this hop publishes.
	clearAetherActivePort()
	t.Cleanup(clearAetherActivePort)

	torMgr := NewTorManager(make(chan LogEntry, 512))
	t.Cleanup(func() { _ = torMgr.Stop() })
	m := NewCoreManager(nil, nil)

	tunnelPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	armed := aetherHopArmed(t, "never", "")
	chained := aetherTorChainProfile(aetherHopProfile("off", ""), armed)

	got, token, err := m.startAetherTorHop(torMgr, armed, chained, tunnelPort, func() bool { return true })
	if err != nil {
		t.Fatalf("startAetherTorHop: %v", err)
	}
	if got == tunnelPort {
		t.Errorf("the hop published the tunnel's own port %d: the daemon never got a listener", got)
	}
	if token == 0 {
		t.Error("the hop child names no token, so a rollback cannot stop exactly this daemon")
	}
	if pub, ok := currentAetherActivePort(); !ok || pub != got {
		t.Errorf("connect dials %d/%v, want the daemon's listener %d", pub, ok, got)
	}
	if !torMgr.Running() {
		t.Fatal("the daemon the hop started is not running")
	}

	raw, err := os.ReadFile(filepath.Join(work, torDaemonStateDirName, fakeTorArgvMark))
	if err != nil {
		t.Fatalf("the daemon recorded no argv: %v", err)
	}
	var ran []string
	if err := json.Unmarshal(raw, &ran); err != nil {
		t.Fatalf("argv record: %v", err)
	}
	if v, _ := torArgValue(ran, "--SocksPort"); v != fmt.Sprintf("127.0.0.1:%d", got) {
		t.Errorf("the daemon listens on %q, want the published %d", v, got)
	}
	if v, _ := torArgValue(ran, "--Socks5Proxy"); v != fmt.Sprintf("127.0.0.1:%d", tunnelPort) {
		t.Errorf("the daemon dials through %q, want the aether tunnel on %d", v, tunnelPort)
	}

	if err := torMgr.StopToken(token); err != nil {
		t.Fatalf("StopToken of the hop's own token: %v", err)
	}
	if torMgr.Running() {
		t.Error("the hop's daemon survived the rollback")
	}
}

// TestAetherTorHopRefusesADaemonWhoseTunnelIsGone: a daemon behind a
// tunnel that died mid-bootstrap can never finish, and waiting out its
// five minutes for that is the failure waitChainTunnel already refuses
// for the arti shape. Nothing is published on the way out.
func TestAetherTorHopRefusesADaemonWhoseTunnelIsGone(t *testing.T) {
	withTorDaemonHooks(t, installFakeTor(t))
	clearAetherActivePort()
	t.Cleanup(clearAetherActivePort)

	// The daemon is needed only to answer on its listener; the gate ends
	// the wait before it ever gets there.
	torMgr := NewTorManager(make(chan LogEntry, 512))
	t.Cleanup(func() { _ = torMgr.Stop() })
	m := NewCoreManager(nil, nil)

	tunnelPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	armed := aetherHopArmed(t, "never", "")
	chained := aetherTorChainProfile(aetherHopProfile("off", ""), armed)

	dead := false
	got, _, err := m.startAetherTorHop(torMgr, armed, chained, tunnelPort, func() bool { return dead })
	if err == nil {
		t.Fatalf("a hop behind a tunnel that went away published %d and called it ready", got)
	}
	if !strings.Contains(err.Error(), "tunnel") {
		t.Errorf("the failure is %q, want the tunnel's death named", err)
	}
	if torMgr.Running() {
		t.Error("the daemon outlived the hop that refused to publish it")
	}
	if pub, ok := currentAetherActivePort(); ok {
		t.Errorf("a refused hop left %d published", pub)
	}
}

// TestAetherTorHopDeathTakesDownTheCommittedGroup: the hop is a child of
// the committed aether group, so its own death report must be accepted
// for that group even though it is not a chainActive spoof leg. The
// report arrives after the manager already cleared its running flag, so
// the group has to carry the record itself.
func TestAetherTorHopDeathTakesDownTheCommittedGroup(t *testing.T) {
	m := NewCoreManager(nil, nil)
	m.mu.Lock()
	defer m.mu.Unlock()

	m.aetherActive = true
	m.aetherTorActive = true
	if !m.engineInGroupLocked("tor") {
		t.Error("an aether group running the standalone hop does not claim its death")
	}
	m.aetherTorActive = false
	if m.engineInGroupLocked("tor") {
		t.Error("an aether group whose hop runs inside the engine claims a tor child it never started")
	}
	m.chainActive = true
	m.chainKind = chainDriverTor
	m.aetherTorActive = true
	if !m.engineInGroupLocked("tor") {
		t.Error("the spoof chain's tor leg stopped being its own member")
	}
}

// TestAetherStartUnwindsTheHopBeforeTheTunnel: the two children of the
// split shape have to come down in dependency order, and the main core
// may not render against a port the hop has not published yet. The
// connect's engine start and its rollback live in one branch of Start,
// and that branch is the only place the order exists.
func TestAetherStartUnwindsTheHopBeforeTheTunnel(t *testing.T) {
	src := readGuiFile(t, "core_manager.go")
	i := strings.Index(src, "if wantAether {")
	if i < 0 {
		t.Fatal("Start's aether branch is gone")
	}
	j := strings.Index(src[i:], "if wantSpoof {")
	if j < 0 {
		t.Fatal("Start's aether branch no longer ends where the spoof branch begins")
	}
	body := src[i : i+j]

	hopStop := strings.Index(body, "StopToken(hopToken)")
	engineStop := strings.Index(body, "StopToken(token)")
	hopStart := strings.Index(body, "startAetherTorHop(")
	render := strings.Index(body, "startAetherMainCore(")
	if hopStop < 0 || engineStop < 0 || hopStart < 0 {
		t.Fatalf("the aether branch does not unwind a hop child (hop stop %d, engine stop %d, hop start %d)",
			hopStop, engineStop, hopStart)
	}
	if hopStop > engineStop {
		t.Error("the rollback took the tunnel down first, leaving the daemon dialling a dead port")
	}
	if hopStart > render {
		t.Error("the main core was rendered before the hop published its listener")
	}
}
