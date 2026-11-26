package main

// Which Tor implementation carries the chain's hop, and what the connect
// says about it.
//
// Tor is now two engines: the Tor Project's tor.exe and the arti client
// linked into aether.exe. The chain has to pick deliberately — the
// standalone daemon is what bootstraps on the path where arti stops at
// 15%, and a machine without tor.exe still has to be able to chain — and
// it has to say which one it ran, because the two answer to different
// version numbers and a user comparing the two connects would otherwise
// see one word, "tor", for both.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chainLegSettings is a Tor chain with no bridges, pointed at [torPath]
// ("" leaves the shipped layout).
func chainLegSettings(torPath string) Settings {
	s := DefaultSettings()
	s.ChainTorBridges = "never"
	s.TorPath = torPath
	return s
}

// drainLog reads what the run has said so far, in order.
func drainLog(ch chan LogEntry) string {
	var out []string
	for {
		select {
		case e := <-ch:
			out = append(out, e.Message)
		default:
			return strings.Join(out, "\n")
		}
	}
}

// TestTorChainLegRunsTheStandaloneDaemon: with tor.exe present the hop
// runs on it. The aether hook is deliberately a child that never becomes
// ready, so the test fails if the chain still goes through the engine.
func TestTorChainLegRunsTheStandaloneDaemon(t *testing.T) {
	bin := installFakeTor(t)
	work := withTorDaemonHooks(t, bin)
	// No aether.exe at all: a leg that went through the engine would fail
	// at resolution, so a pass here can only mean the daemon ran.
	prevAether := aetherExeOverride
	aetherExeOverride = filepath.Join(t.TempDir(), "absent", "aether.exe")
	defer func() { aetherExeOverride = prevAether }()

	ch := make(chan LogEntry, 64)
	mgr := NewTorManager(ch)
	m := &CoreManager{torMgr: mgr}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	front, err := freePort()
	if err != nil {
		t.Fatal(err)
	}

	if err := m.startTorChainLeg(mgr, chainLegSettings(""), port, front, 30*time.Second); err != nil {
		t.Fatalf("the chain's Tor leg did not start on tor.exe: %v", err)
	}
	defer func() { _ = mgr.Stop() }()
	if _, err := os.Stat(filepath.Join(work, "tor-state", fakeTorArgvMark)); err != nil {
		t.Errorf("no standalone daemon ran: %v", err)
	}
	text := drainLog(ch)
	if !strings.Contains(text, "tor.exe") {
		t.Errorf("nothing in the log names the engine that carried the hop: %q", text)
	}
	if strings.Contains(text, "arti") {
		t.Errorf("the standalone run announced arti too: %q", text)
	}
}

// TestTorChainLegFallsBackToArtiWhenTorExeIsAbsent: a machine with only
// aether.exe still chains, and the log says both that tor.exe was missing
// and that the engine's arti runs instead — the shape the user had
// before, reported as such.
func TestTorChainLegFallsBackToArtiWhenTorExeIsAbsent(t *testing.T) {
	hung, _ := lingerHelperBinary(t)
	work := withTorDaemonHooks(t, "")
	prevAether, prevAetherDir := aetherExeOverride, aetherExeDirOverride
	aetherExeOverride, aetherExeDirOverride = hung, ""
	defer func() { aetherExeOverride, aetherExeDirOverride = prevAether, prevAetherDir }()

	ch := make(chan LogEntry, 64)
	mgr := NewTorManager(ch)
	m := &CoreManager{torMgr: mgr}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	front, err := freePort()
	if err != nil {
		t.Fatal(err)
	}

	err = m.startTorChainLeg(mgr, chainLegSettings(t.TempDir()), port, front, 2*time.Second)
	defer func() { _ = mgr.Stop() }()
	if err == nil {
		t.Fatal("a leg whose engine never became ready reported success")
	}
	text := drainLog(ch)
	if !strings.Contains(text, torExeName) || !strings.Contains(strings.ToLower(text), "no ") {
		t.Errorf("nothing says why tor.exe was not used: %q", text)
	}
	if !strings.Contains(text, "arti") {
		t.Errorf("nothing says the fallback is aether's arti: %q", text)
	}
	// The daemon was never spawned, so it never got a state directory.
	if st, err := os.Stat(filepath.Join(work, "tor-state")); err == nil && st.IsDir() {
		t.Error("the arti leg created the standalone daemon's state dir anyway")
	}
}

// TestStandaloneTorForNamesTheFallbackReason: the choice itself is a
// pure decision the leg tests above cannot cover from both sides.
func TestStandaloneTorForNamesTheFallbackReason(t *testing.T) {
	prevExe, prevDir := torExeOverride, torExeDirOverride
	defer func() { torExeOverride, torExeDirOverride = prevExe, prevDir }()

	dir := t.TempDir()
	hook := filepath.Join(dir, torExeName)
	if err := os.WriteFile(hook, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	torExeOverride, torExeDirOverride = hook, ""
	exe, why := standaloneTorFor(DefaultSettings())
	if exe != hook || why != "" {
		t.Errorf("with tor.exe: %q, %q; want the binary and no apology", exe, why)
	}

	torExeOverride = filepath.Join(dir, "absent", torExeName)
	exe, why = standaloneTorFor(DefaultSettings())
	if exe != "" {
		t.Errorf("a missing hook resolved to %q", exe)
	}
	if !strings.Contains(why, torExeName) {
		t.Errorf("the fallback reason %q does not name the binary that is missing", why)
	}
}
