package main

// Backend parity with PattN's Aether service (v2rayN/ServiceLib/Aether):
// the words the core ends a session on, and what counts as its listener
// answering. Every case here was measured against aether.exe 2.3.0 or
// read out of PattN's own classifier (AetherLogLine.cs).

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAetherChainEndWords: a core before 2.3.4 goes on running after Tor
// or Psiphon inside the tunnel has ended, so its listener answers while
// nothing carries traffic. The line that says so is the only word the
// session is dead, and it counts only for the carrier the app dials.
func TestAetherChainEndWords(t *testing.T) {
	const torLine = "[2026-11-16T21:29:56.879Z ERROR aether] [-] tor: other: circuit failed"
	cases := []struct {
		name    string
		line    string
		carrier string
		want    string
	}{
		{"tor ends", torLine, "tor", "tor: circuit failed"},
		{"psiphon ends", "[2026-11-16T21:29:56.879Z ERROR aether] [-] psiphon: io: tunnel closed", "psiphon", "psiphon: tunnel closed"},
		{"identity refused label", "[2026-11-16T21:29:56.879Z ERROR aether] [-] tor: identity refused: bad bridge", "tor", "tor: bad bridge"},
		{"own listener dials nothing", torLine, "", ""},
		{"other carrier", torLine, "psiphon", ""},
		{"warn is not an end", "[2026-11-16T21:29:56.879Z WARN aether] [-] tor: other: circuit failed", "tor", ""},
		{"psiphon module notices are not the end", "[2026-11-16T21:29:56.879Z ERROR aether::psiphon] [-] psiphon: io: notice", "psiphon", ""},
		{"progress marker", "[2026-11-16T21:29:56.879Z ERROR aether] [*] tor: building", "tor", ""},
		{"no header", "[-] tor: other: circuit failed", "tor", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := aetherChainEndWords(c.line, c.carrier); got != c.want {
				t.Fatalf("aetherChainEndWords(%q, %q) = %q, want %q", c.line, c.carrier, got, c.want)
			}
		})
	}
}

// TestAetherNoEchKeyWord: ECH on with no key to offer is a stop, not a
// retry — the core refuses to send the server name in the clear.
func TestAetherNoEchKeyWord(t *testing.T) {
	line := "[2026-11-16T21:29:56.879Z ERROR aether] ECH is on but there is no ECH key to offer"
	if !aetherNoEchKeyWord(line) {
		t.Fatalf("the core's no-ECH-key word was not read: %q", line)
	}
	if aetherNoEchKeyWord("[2026-11-16T21:29:56.879Z INFO aether] [+] ECH off; the server name goes out in cleartext") {
		t.Fatal("ECH off is not a stop")
	}
}

// TestAetherFatalErrorLine: the error the core's main writes last.
func TestAetherFatalErrorLine(t *testing.T) {
	if !aetherFatalErrorLine(`Error: Other("unknown option 'x'")`) {
		t.Fatal("the core's fatal error was not read")
	}
	if aetherFatalErrorLine("[2026-11-16T21:29:56.879Z ERROR aether] [-] MASQUE tunnel ended: other: timed out") {
		t.Fatal("a retryable tunnel end is not the fatal error the core stops on")
	}
}

// TestAetherDeadForcedPeer: measured on aether.exe 2.3.0 with a saved
// endpoint that had gone dead. The core says it is rescanning and then
// logs "using forced peer ... (probe skipped)" and dials the same dead
// gateway again, forever. The retry line before it is progress, not the
// give-up.
func TestAetherDeadForcedPeer(t *testing.T) {
	dead := "[2026-11-16T21:29:56.879Z WARN  aether] [-] last known-good gateway 203.0.113.9:443 no longer responds; rescanning"
	if !aetherDeadForcedPeer(dead) {
		t.Fatalf("the core's dead-gateway word was not read: %q", dead)
	}
	retry := "[2026-11-16T21:29:51.873Z INFO  aether] [*] retrying last known-good gateway 203.0.113.9:443 before rescanning"
	if aetherDeadForcedPeer(retry) {
		t.Fatal("the retry before the give-up is progress, not a dead endpoint")
	}
}

// socksReplyListener stands up a loopback listener that answers a SOCKS
// greeting with exactly [reply] and then holds the connection, as a
// core's listener does while it serves the session.
func socksReplyListener(t *testing.T, reply []byte) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var g [3]byte
				if _, err := c.Read(g[:]); err != nil {
					return
				}
				_, _ = c.Write(reply)
				time.Sleep(time.Hour)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestAetherSocksProbeNeedsWholeReply: PattN reads the whole method
// selection, not its first byte. Two probes of loopback ports nothing
// listens on can be joined by a TCP simultaneous open, and each then
// reads the other's greeting — which also begins with 5. A listener
// that says nothing after the version has not answered.
func TestAetherSocksProbeNeedsWholeReply(t *testing.T) {
	if answersSocksOnce(socksReplyListener(t, []byte{5})) {
		t.Fatal("a bare version byte was taken for a SOCKS method selection")
	}
	if !answersSocksOnce(socksReplyListener(t, []byte{5, 0})) {
		t.Fatal("a whole no-auth method selection was refused")
	}
	if answersSocksOnce(socksReplyListener(t, []byte{5, 255})) {
		t.Fatal("a method the greeting never offered was accepted")
	}
	if answersSocksOnce(socksReplyListener(t, []byte{4, 0})) {
		t.Fatal("a SOCKS4 reply was accepted")
	}
}

// TestAetherWarmupWaitsInRoundsWhileEngineLives: measured against
// aether.exe 2.3.0 — a MASQUE run with no saved endpoint scans for
// gateways and its SOCKS listener answers at 48.3s. A hard 30s deadline
// killed a core that was still working, before its own first retry.
// PattN waits in rounds of WarmUpRound "round after round while it
// lives" (AetherCoreManager.AwaitWarmUpAsync).
func TestAetherWarmupWaitsInRoundsWhileEngineLives(t *testing.T) {
	prev := aetherReadyPoll
	aetherReadyPoll = 2 * time.Millisecond
	t.Cleanup(func() { aetherReadyPoll = prev })

	m := NewAetherManager(nil)
	calls := 0
	ready := func(int) bool { calls++; return calls >= 20 }
	start := time.Now()
	err := m.waitSocksOrExit(1, 10*time.Millisecond, make(chan error), ready, func() bool { return true })
	if err != nil {
		t.Fatalf("a live core that answers after several rounds was given up on: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("answered within one round (%v); the test proves nothing", elapsed)
	}
}

// TestAetherListenerPortsCoversEveryListener: the core binds its own
// listener plus Tor's and Psiphon's on consecutive numbers, and none of
// them may be shared (PattN AetherCore.ports). Checking only the dialled
// one leaves the other two to whoever takes them first.
func TestAetherListenerPortsCoversEveryListener(t *testing.T) {
	p := fakeAetherProfile()
	p.Aether.Tor = "chain"
	p.Aether.Psiphon = "chain"
	args := buildAetherArguments(p, 10819)
	got := aetherListenerPorts(args)
	for _, want := range []int{10819, 10820, 10821} {
		if !containsInt(got, want) {
			t.Fatalf("aetherListenerPorts = %v, missing listener %d (args %v)", got, want, args)
		}
	}
	if len(got) != 3 {
		t.Fatalf("aetherListenerPorts = %v, want exactly the three listeners", got)
	}
}

func containsInt(list []int, want int) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// holdPortTCPAndUDP occupies [port] on both protocols, as an engine
// listener does: a TCP-only probe hands over a port whose UDP side is
// taken (PattN PortsProblem probes both).
func holdPortTCPAndUDP(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("hold tcp %d: %v", port, err)
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		_ = ln.Close()
		t.Fatalf("hold udp %d: %v", port, err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = pc.Close() })
}

// TestAetherPickPortRefusesTakenListenerRange: the dial port being free
// is not enough — the numbers after it are listeners too.
func TestAetherPickPortRefusesTakenListenerRange(t *testing.T) {
	p := fakeAetherProfile()
	p.Aether.Tor = "chain"
	first, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	// The tor-bind number of the first candidate is taken.
	args, _, _ := aetherCoreOf(p, first)
	var taken int
	for _, port := range aetherListenerPortsOf(args) {
		if port != first {
			taken = port
			break
		}
	}
	if taken == 0 {
		t.Fatalf("no second listener rendered: %v", args)
	}
	holdPortTCPAndUDP(t, taken)

	prev := freePortHook
	n := 0
	freePortHook = func() (int, error) {
		n++
		if n == 1 {
			return first, nil
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		return port, nil
	}
	t.Cleanup(func() { freePortHook = prev })

	port, args, _, _, err := pickAetherEnginePort(p)
	if err != nil {
		t.Fatalf("pickAetherEnginePort: %v", err)
	}
	if port == first {
		t.Fatalf("picked %d whose listener %d is held", port, taken)
	}
	for _, l := range aetherListenerPorts(args) {
		if l == taken {
			t.Fatalf("picked a range that still names the held port %d: %v", taken, args)
		}
	}
}

// aetherListenerPortsOf reads the three listener flags straight out of
// the argv, independently of the helper under test.
func aetherListenerPortsOf(args []string) []int {
	var out []int
	for _, flag := range []string{aetherFlagBind, aetherFlagTorBind, aetherFlagPsiphonBind} {
		if p, ok := aetherPortAfter(args, flag); ok && p > 0 {
			out = append(out, p)
		}
	}
	return out
}

// TestAetherDeadSavedEndpointRescans: measured on aether.exe 2.3.0 — a
// saved endpoint that has gone dead is pinned forever. The core says it
// is rescanning, then "using forced peer ... (probe skipped)", and dials
// the same dead gateway until it is killed (170s, never ready). Without
// the pin it scans and answers in 48s. The rescue must run the engine
// once more without --peer, as the app's own Scan does.
func TestAetherDeadSavedEndpointRescans(t *testing.T) {
	_, state := installFakeAether(t)
	t.Setenv(fakeAetherModeEnv, fakeAetherModeDeadPeer)
	prev := aetherReadyPoll
	aetherReadyPoll = 10 * time.Millisecond
	t.Cleanup(func() { aetherReadyPoll = prev })

	m := NewAetherManager(make(chan LogEntry, 4096))
	s := DefaultSettings()
	s.SystemProxy = false
	port, token, err := m.StartWithToken(fakeAetherProfile(), s)
	if err != nil {
		t.Fatalf("a dead saved endpoint was not rescued: %v", err)
	}
	defer func() { _ = m.StopToken(token) }()
	if port < 1 || port > 65535 {
		t.Fatalf("rescued run published port %d", port)
	}
	runs := fakeAetherRuns(state)
	if len(runs) != 2 {
		t.Fatalf("engine ran %d times, want the pinned attempt then a scan", len(runs))
	}
	if !containsStr(runs[0], "--peer") {
		t.Fatalf("first run did not use the saved endpoint: %v", runs[0])
	}
	if containsStr(runs[1], "--peer") {
		t.Fatalf("the rescue still pinned the dead endpoint: %v", runs[1])
	}
	if !containsStr(runs[1], "--no-quick-reconnect") {
		t.Fatalf("the rescue left the reconnect cache on, so it re-uses the dead gateway: %v", runs[1])
	}
}

// TestAetherGiveUpWordsEndTheRun: the core goes on living after it says
// it cannot carry traffic, so the readiness wait must end on its words
// rather than publish a listener nothing answers on.
func TestAetherGiveUpWordsEndTheRun(t *testing.T) {
	for _, mode := range []string{fakeAetherModeChainEnd, fakeAetherModeNoEchKey, fakeAetherModeFatal} {
		t.Run(mode, func(t *testing.T) {
			installFakeAether(t)
			t.Setenv(fakeAetherModeEnv, mode)
			prev := aetherReadyPoll
			aetherReadyPoll = 10 * time.Millisecond
			t.Cleanup(func() { aetherReadyPoll = prev })

			m := NewAetherManager(make(chan LogEntry, 4096))
			p := fakeAetherProfile()
			if mode == fakeAetherModeChainEnd {
				p.Aether.Tor = "chain"
			}
			s := DefaultSettings()
			s.SystemProxy = false
			_, token, err := m.StartWithToken(p, s)
			if err == nil {
				_ = m.StopToken(token)
				t.Fatal("the run was published after the core said it could not carry traffic")
			}
			want := map[string]string{
				fakeAetherModeChainEnd: "tor",
				fakeAetherModeNoEchKey: "ECH",
				fakeAetherModeFatal:    "Error:",
			}[mode]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %q, want the core's own words naming %q", err, want)
			}
		})
	}
}

// TestAetherChainEndEndsTheWait: the readiness wait must end on the
// core's own words rather than run its budget out against a listener
// that answers while its carrier is gone.
func TestAetherChainEndEndsTheWait(t *testing.T) {
	m := NewAetherManager(nil)
	m.noteGiveUp("[2026-11-16T21:29:56.879Z ERROR aether] [-] tor: other: circuit failed", "tor")
	exited := make(chan error)
	err := m.waitSocksOrExit(1, time.Minute, exited, func(int) bool { return false }, func() bool { return true })
	if err == nil {
		t.Fatal("the wait ran on after the core said its carrier had ended")
	}
	if !strings.Contains(err.Error(), "tor: circuit failed") {
		t.Fatalf("err = %q, want the core's own words", err)
	}
}
