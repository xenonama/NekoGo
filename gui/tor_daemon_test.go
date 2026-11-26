package main

// tor_daemon_test.go — the Tor Project's tor.exe carries the chain's Tor
// hop, as an engine of its own.
//
// NekoGo has only ever run Tor as aether.exe's linked arti client, and on
// the spoofed path that client stops at 15% of its bootstrap: it reaches
// the guards over the bridges but no directory ever comes back through
// them ("Retiring circuit because of directory failure: Partial
// response"). The same path with the standalone daemon — a Psiphon-fronted
// SOCKS, no bridges — went 0% to Bootstrapped 100% in 2m45s and carried
// traffic, measured on this machine on 2026-11-15. So the hop needs a
// driver of its own, and that driver has facts the arti one does not:
// tor.exe opens its --SocksPort at 0% (a listener answer says nothing
// about a circuit), it takes bridges as --Bridge/--UseBridges with a
// --ClientTransportPlugin naming the transport binaries, and one
// malformed or front-less bridge line is a hard config failure or an
// endless dial error rather than something the engine recovers from.
//
// The argv shapes here were checked against the shipped binary with
// `tor.exe --verify-config`, which rejects unknown options and bad
// bridge lines without starting anything.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Bridge lines in the shape tor.exe accepts, measured against
// `tor.exe --verify-config`: a fingerprint that is not 40 hex digits
// fails the whole run ("Key digest for Bridge is wrong length"), so the
// short fixtures aether's argv renderer tolerates ("AAAA") would kill a
// real tor.exe at spawn. The addresses are documentation ranges and the
// digest is invented — nothing here names a live bridge.
const (
	torTestFP   = "A1B2C3D4E5F60718293A4B5C6D7E8F9012345678"
	torTestCert = "kwVXS3ZmFF6eO9kwLMVzDSYodnplKGXacC9LZXALH5NPm0FeCsfjECSP3PbHzMEJgVLgY5CjLGjs"
)

func torTestObfs4(ip string, port int) string {
	return fmt.Sprintf("obfs4 %s:%d %s cert=%s kdf=sha256 iat-mode=0", ip, port, torTestFP, torTestCert)
}

func torTestWebtunnel(ip string, port int, front string) string {
	if front == "" {
		return fmt.Sprintf("webtunnel %s:%d %s cert=%s iat-mode=0", ip, port, torTestFP, torTestCert)
	}
	return fmt.Sprintf("webtunnel %s:%d %s cert=%s url=%s iat-mode=0", ip, port, torTestFP, torTestCert, front)
}

// torDaemonTestTree lays out a standalone-Tor folder: tor.exe with the
// pluggable transports beside it, as the expert bundle and NekoGo's own
// Tor updater both stage them. The binaries are inert files.
func torDaemonTestTree(t *testing.T) (exe, ptDir string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, torExeName)
	if err := os.WriteFile(exe, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	ptDir = filepath.Join(dir, "pluggable_transports")
	if err := os.MkdirAll(ptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lyrebird.exe", "conjure-client.exe"} {
		if err := os.WriteFile(filepath.Join(ptDir, name), []byte("not a real binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe, ptDir
}

// torDaemonTestSettings is what the Chain tab stores, run through the
// same normalization a save applies.
func torDaemonTestSettings(t *testing.T, bridges, lines string) Settings {
	t.Helper()
	s := DefaultSettings()
	s.ChainTorBridges = bridges
	s.ChainTorBridgeLines = lines
	if err := normalizeChainTor(&s); err != nil {
		t.Fatalf("normalizeChainTor: %v", err)
	}
	return s
}

// torArgValue is the value after [flag] in a rendered argv, and whether
// the flag is there at all.
func torArgValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// torArgValues collects every value of a repeated flag (--Bridge).
func torArgValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func torArgHasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// TestTorDaemonArgsRunsTheFrontedClient pins the plain shape the measured
// 100% bootstrap ran on: a loopback SOCKS listener, the chain's fronted
// inbound as its outbound proxy, a state directory that survives the
// connect, and logging on stdout so the bootstrap percentage the app
// waits for is actually visible.
func TestTorDaemonArgsRunsTheFrontedClient(t *testing.T) {
	exe, _ := torDaemonTestTree(t)
	state := filepath.Join(t.TempDir(), "tor-state")
	s := torDaemonTestSettings(t, "never", "")

	args, note, err := torDaemonArgs(exe, s, 58000, 59000, state)
	if err != nil {
		t.Fatalf("torDaemonArgs: %v", err)
	}
	if note != "" {
		t.Errorf("note = %q, want nothing to explain", note)
	}
	for _, want := range []struct{ flag, value string }{
		{"--SocksPort", "127.0.0.1:58000"},
		{"--Socks5Proxy", "127.0.0.1:59000"},
		{"--DataDirectory", state},
		{"--AvoidDiskWrites", "1"},
		{"--Log", "notice stdout"},
		{"--DormantCanceledByStartup", "1"},
	} {
		got, ok := torArgValue(args, want.flag)
		if !ok || got != want.value {
			t.Errorf("%s = %q (present=%v), want %q", want.flag, got, ok, want.value)
		}
	}
	// A relative --DataDirectory is resolved against the child's working
	// directory and tor.exe warns about it on every start; the app always
	// owns an absolute one.
	if v, ok := torArgValue(args, "--DataDirectory"); !ok || !filepath.IsAbs(v) {
		t.Errorf("--DataDirectory %q is not absolute", v)
	}
	// Plain relays: nothing bridge-shaped reaches the daemon.
	for _, flag := range []string{"--Bridge", "--UseBridges", "--ClientTransportPlugin"} {
		if torArgHasFlag(args, flag) {
			t.Errorf("%s reached a run with Bridges=never: %v", flag, args)
		}
	}

	// No front to ride (a run that dials on its own) names no proxy.
	args, _, err = torDaemonArgs(exe, s, 58001, 0, state)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := torArgValue(args, "--Socks5Proxy"); ok {
		t.Errorf("a run with no front took --Socks5Proxy %q", v)
	}
	if v, _ := torArgValue(args, "--SocksPort"); v != "127.0.0.1:58001" {
		t.Errorf("--SocksPort = %q, want the listener this run publishes", v)
	}

	// Ports outside the range are refused before a child is spawned.
	for _, bad := range [][2]int{{0, 59000}, {58000, 70000}} {
		if _, _, err := torDaemonArgs(exe, s, bad[0], bad[1], state); err == nil {
			t.Errorf("torDaemonArgs(%d, %d) accepted", bad[0], bad[1])
		}
	}
}

// TestTorDaemonArgsHandsOverOwnBridges: own means the saved lines go to
// the daemon and nothing else does, and a transport the lines name must
// also be handed to the pluggable transport binary that serves it — tor
// launches one binary per type list and the bridge dial dies silently
// without it.
func TestTorDaemonArgsHandsOverOwnBridges(t *testing.T) {
	exe, ptDir := torDaemonTestTree(t)
	state := filepath.Join(t.TempDir(), "tor-state")
	lines := strings.Join([]string{
		torTestObfs4("198.51.100.7", 443),
		"# a comment the line splitter keeps out",
		torTestObfs4("203.0.113.9", 80),
		torTestWebtunnel("192.0.2.2", 443, "https://front.invalid/"),
	}, "\n")
	s := torDaemonTestSettings(t, "own", lines)

	args, note, err := torDaemonArgs(exe, s, 58100, 59100, state)
	if err != nil {
		t.Fatalf("torDaemonArgs: %v", err)
	}
	if note != "" {
		t.Errorf("note = %q, want a run that got its bridges to say nothing", note)
	}
	if v, _ := torArgValue(args, "--UseBridges"); v != "1" {
		// Measured: Bridge lines without UseBridges validate fine and are
		// then ignored, which is how stored bridges silently did nothing.
		t.Errorf("--UseBridges = %q, want 1 next to the lines", v)
	}
	got := torArgValues(args, "--Bridge")
	want := []string{torTestObfs4("198.51.100.7", 443), torTestObfs4("203.0.113.9", 80),
		torTestWebtunnel("192.0.2.2", 443, "https://front.invalid/")}
	if len(got) != len(want) {
		t.Fatalf("%d --Bridge args, want %d: %v", len(got), len(want), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("--Bridge[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	plugins := torArgValues(args, "--ClientTransportPlugin")
	if len(plugins) != 1 {
		t.Fatalf("%d --ClientTransportPlugin args, want one per transport binary", len(plugins))
	}
	lyrebird := filepath.Join(ptDir, "lyrebird.exe")
	if plugins[0] != "obfs4,webtunnel exec "+lyrebird {
		t.Errorf("--ClientTransportPlugin = %q, want the types the lines name exec'd by %s",
			plugins[0], lyrebird)
	}
	if !filepath.IsAbs(lyrebird) {
		t.Fatal("fixture transport path is not absolute")
	}
}

// TestTorDaemonArgsRefusesLinesTheDaemonCannotDial: tor.exe takes every
// --Bridge it is handed at face value at config time and then fails — a
// line that does not parse stops the run before it starts, a webtunnel
// line with no front leaves the transport dialing an empty address for
// the whole readiness budget. Both are measured; dropping the line and
// saying so is the only shape that leaves the rest of the pool working.
func TestTorDaemonArgsRefusesLinesTheDaemonCannotDial(t *testing.T) {
	exe, _ := torDaemonTestTree(t)
	state := filepath.Join(t.TempDir(), "tor-state")
	good := torTestObfs4("198.51.100.7", 443)

	cases := []struct {
		name        string
		lines       string
		wantErr     bool
		wantArgs    bool
		wantInNote  []string
		wantNoInLog string
	}{
		{
			name:       "a webtunnel line with no front",
			lines:      good + "\n" + torTestWebtunnel("192.0.2.2", 443, "") + "\n",
			wantArgs:   true,
			wantInNote: []string{"1 of", "webtunnel", "url="},
		},
		{
			name:       "only a front-less webtunnel line",
			lines:      torTestWebtunnel("192.0.2.2", 443, "") + "\n",
			wantErr:    true,
			wantInNote: []string{"webtunnel", "url="},
		},
		{
			name:       "a fingerprint that is not a digest",
			lines:      good + "\nobfs4 203.0.113.9:443 AAAA iat-mode=0\n",
			wantArgs:   true,
			wantInNote: []string{"1 of", "fingerprint"},
		},
		{
			name:       "a transport the bundle does not carry",
			lines:      good + "\nsnowflake 192.0.2.5:443 " + torTestFP + " iat-mode=0\n",
			wantArgs:   true,
			wantInNote: []string{"1 of", "snowflake"},
		},
		{
			name:       "a line that is not a line",
			lines:      good + "\nbridges.torproject.org\n",
			wantArgs:   true,
			wantInNote: []string{"1 of"},
		},
		{
			name:       "every line is junk",
			lines:      "hello\nworld\n",
			wantErr:    true,
			wantInNote: []string{"2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, note, err := torDaemonArgs(exe, torDaemonTestSettings(t, "own", tc.lines), 58200, 59200, state)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("a run with no usable line started anyway: %v", args)
				}
				for _, want := range tc.wantInNote {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not say %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("one unusable line took the whole run down: %v", err)
			}
			if !tc.wantArgs {
				t.Fatal("case wants a surviving run")
			}
			if got := torArgValues(args, "--Bridge"); len(got) != 1 || got[0] != good {
				t.Errorf("--Bridge args = %q, want only the line that can be dialed", got)
			}
			for _, want := range tc.wantInNote {
				if !strings.Contains(note, want) {
					t.Errorf("note %q does not say %q", note, want)
				}
			}
			// The note describes the lines, it never repeats them: a
			// bridge's address and digest stay out of the app's log.
			if strings.Contains(note, "198.51.100.7") || strings.Contains(note, "192.0.2.") ||
				strings.Contains(note, torTestFP) || strings.Contains(note, torTestCert) {
				t.Errorf("note repeats bridge material: %q", note)
			}
			// The refused transport never reaches the plugin list.
			if strings.Contains(note, "webtunnel") || strings.Contains(note, "snowflake") {
				for _, p := range torArgValues(args, "--ClientTransportPlugin") {
					if strings.Contains(p, "webtunnel,") || strings.HasPrefix(p, "webtunnel") ||
						strings.Contains(p, "snowflake") {
						t.Errorf("the plugin list still names the dropped transport: %q", p)
					}
				}
			}
		})
	}
}

// TestTorDaemonArgsOwnWithNothingSavedDialsPlainly: measured against
// --verify-config, UseBridges with no bridge is a hard config failure —
// the daemon exits at once. The run must go without bridges and say so,
// because the user believes they pasted some.
func TestTorDaemonArgsOwnWithNothingSavedDialsPlainly(t *testing.T) {
	exe, _ := torDaemonTestTree(t)
	state := filepath.Join(t.TempDir(), "tor-state")
	for _, lines := range []string{"", "# only a comment\n", "   \n"} {
		args, note, err := torDaemonArgs(exe, torDaemonTestSettings(t, "own", lines), 58300, 59300, state)
		if err != nil {
			t.Fatalf("lines %q: %v", lines, err)
		}
		if torArgHasFlag(args, "--UseBridges") {
			t.Errorf("lines %q: --UseBridges with no bridge takes the run down at spawn", lines)
		}
		if !strings.Contains(note, "own") || !strings.Contains(note, "no bridge") {
			t.Errorf("lines %q: note %q does not say the run has nothing to hand over", lines, note)
		}
	}
}

// TestTorDaemonArgsNamesBridgesItLeavesUnused: the Chain tab keeps the
// lines whatever Bridges says, and only own hands them over. A run that
// waits five minutes on a bootstrap the user thinks is bridged has to
// say which shape it ran — the same note the aether leg gives, in the
// words of this one.
func TestTorDaemonArgsNamesBridgesItLeavesUnused(t *testing.T) {
	exe, _ := torDaemonTestTree(t)
	state := filepath.Join(t.TempDir(), "tor-state")
	lines := strings.Join([]string{
		torTestObfs4("198.51.100.7", 443), torTestObfs4("203.0.113.9", 80),
		torTestWebtunnel("192.0.2.2", 443, "https://front.invalid/"),
		torTestObfs4("192.0.2.9", 9001),
	}, "\n")

	for _, tc := range []struct{ mode, why string }{
		{"auto", "auto"},
		{"first", "first"},
		{"never", "never"},
	} {
		args, note, err := torDaemonArgs(exe, torDaemonTestSettings(t, tc.mode, lines), 58400, 59400, state)
		if err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		if torArgHasFlag(args, "--Bridge") || torArgHasFlag(args, "--UseBridges") {
			t.Errorf("%s: bridge args reached a run that should dial plain", tc.mode)
		}
		for _, want := range []string{"4 bridge", tc.why, "own"} {
			if !strings.Contains(note, want) {
				t.Errorf("%s: note %q does not say %q", tc.mode, note, want)
			}
		}
		if strings.Contains(note, "198.51.100.7") || strings.Contains(note, torTestFP) {
			t.Errorf("%s: note repeats bridge material: %q", tc.mode, note)
		}
	}

	// Nothing saved means nothing to explain.
	if _, note, err := torDaemonArgs(exe, torDaemonTestSettings(t, "auto", ""), 58500, 59500, state); err != nil || note != "" {
		t.Errorf("a plain run with no saved lines: note %q, err %v", note, err)
	}
}

// TestTorDaemonBootstrapGateWaitsForTheCircuit is the whole point of the
// driver: the real daemon opens its SOCKS listener at 0% of the
// bootstrap, so the answer the arti leg treated as readiness proves
// nothing here. The child under test listens from the start and only
// reaches 100% later; Start must not return before that.
func TestTorDaemonBootstrapGateWaitsForTheCircuit(t *testing.T) {
	bin := installFakeTor(t)
	work := withTorDaemonHooks(t, bin)
	state := filepath.Join(work, "tor-state")
	m := NewTorManager(make(chan LogEntry, 512))
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	front, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	s := torDaemonTestSettings(t, "never", "")

	// The fake listens at once and marks its state dir only when it
	// prints Bootstrapped 100%.
	if _, _, err := m.StartStandaloneTor(s, port, front, 30*time.Second, nil); err != nil {
		t.Fatalf("StartStandaloneTor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, fakeTorReadyMark)); err != nil {
		t.Errorf("Start returned before the daemon said 100%%: %v", err)
	}
	// The state directory is kept, not per-connect: what it caches
	// (consensus, guards) is why a second connect is not another
	// minutes-long fetch, and runProcess deletes what it is handed.
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st, err := os.Stat(state); err != nil || !st.IsDir() {
		t.Errorf("the state dir did not survive the disconnect: %v", err)
	}

	// What the daemon was told to do, read back out of its own state.
	raw, err := os.ReadFile(filepath.Join(state, fakeTorArgvMark))
	if err != nil {
		t.Fatalf("the daemon recorded no argv: %v", err)
	}
	var ran []string
	if err := json.Unmarshal(raw, &ran); err != nil {
		t.Fatalf("argv record: %v", err)
	}
	if got, _ := torArgValue(ran, "--SocksPort"); got != fmt.Sprintf("127.0.0.1:%d", port) {
		t.Errorf("the daemon listened on %q, want the port the chain publishes", got)
	}
	if got, _ := torArgValue(ran, "--Socks5Proxy"); got != fmt.Sprintf("127.0.0.1:%d", front) {
		t.Errorf("the daemon dialed through %q, want the fronted inbound", got)
	}
	if torArgHasFlag(ran, "--Bridge") {
		t.Errorf("a Bridges=never run was handed bridges: %v", ran)
	}
}

// TestTorDaemonBridgesReachTheTransportTheyName: the daemon resolves the
// path in --ClientTransportPlugin itself, in its own working directory,
// with the separators Windows gives it. A path that only looks right in
// the builder's output leaves the transport unfound and the bootstrap
// stopped at 2%, so the file is checked from the child's side.
func TestTorDaemonBridgesReachTheTransportTheyName(t *testing.T) {
	bin := installFakeTor(t)
	work := withTorDaemonHooks(t, bin)
	ptDir := filepath.Join(filepath.Dir(bin), "pluggable_transports")
	if err := os.MkdirAll(ptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lyrebird.exe", "conjure-client.exe"} {
		if err := os.WriteFile(filepath.Join(ptDir, name), []byte("not a real binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := NewTorManager(make(chan LogEntry, 512))
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	front, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	s := torDaemonTestSettings(t, "own",
		torTestObfs4("198.51.100.7", 443)+"\n"+torTestWebtunnel("192.0.2.2", 443, "https://front.invalid/"))

	if _, _, err := m.StartStandaloneTor(s, port, front, 30*time.Second, nil); err != nil {
		t.Fatalf("StartStandaloneTor: %v", err)
	}
	defer func() { _ = m.Stop() }()

	raw, err := os.ReadFile(filepath.Join(work, "tor-state", fakeTorPtMark))
	if err != nil {
		t.Fatalf("the daemon recorded no transport paths: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("transport records = %q, want one line for one binary", raw)
	}
	if !strings.HasPrefix(lines[0], "exists ") {
		t.Errorf("the daemon could not find the transport the argv named: %q", lines[0])
	}
	if !strings.EqualFold(strings.TrimPrefix(lines[0], "exists "), filepath.Join(ptDir, "lyrebird.exe")) {
		t.Errorf("the daemon ran %q, want %s", lines[0], filepath.Join(ptDir, "lyrebird.exe"))
	}
}

// TestTorDaemonBootstrapStallIsReportedAsOne: a daemon that answers on
// its listener and never finishes bootstrapping must fail as a stalled
// bootstrap, naming how far it got — not as a ready engine, and not as
// the bare last line of its output.
func TestTorDaemonBootstrapStallIsReportedAsOne(t *testing.T) {
	bin := installFakeTor(t)
	withTorDaemonHooks(t, bin)
	t.Setenv(fakeTorModeEnv, fakeTorModeStall)
	m := NewTorManager(make(chan LogEntry, 512))
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	front, err := freePort()
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = m.StartStandaloneTor(torDaemonTestSettings(t, "never", ""), port, front, 2*time.Second, nil)
	if err == nil {
		t.Fatal("a daemon that never finished bootstrapping was reported ready")
	}
	if !strings.Contains(err.Error(), "30%") {
		t.Errorf("error %q does not say how far the bootstrap got", err.Error())
	}
	_ = m.Stop()
}

// TestTorDaemonReadinessNeedsBootstrapAndListener pins the gate itself:
// 100% is the engine's own word for a usable circuit, and the listener
// still has to answer a SOCKS greeting. Lines from the arti leg name no
// percentage this regex reads, so one run's progress cannot open the
// other's gate.
func TestTorDaemonReadinessNeedsBootstrapAndListener(t *testing.T) {
	m := NewTorManager(nil)
	pipe := m.linePipe()

	write := func(line string) {
		if _, err := pipe.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
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
				_, _ = c.Write([]byte{5, 0})
			}(c)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port

	write("Nov 15 23:35:13.000 [notice] Bootstrapped 0% (starting): Starting")
	if m.torDaemonReady(port) {
		t.Fatal("ready at 0%: the listener alone is the false-ready this driver replaced")
	}
	write("Nov 15 23:35:13.000 [notice] Bootstrapped 30% (loading_status): Loading networkstatus consensus")
	if m.torDaemonReady(port) {
		t.Fatal("ready at 30%")
	}
	// The arti leg's own progress wording must not move this gate.
	write("[*] tor reaching the network: 100%: connecting successfully")
	if m.torDaemonReady(port) {
		t.Fatal("an arti progress line opened the standalone gate")
	}
	write("Nov 15 23:35:13.000 [notice] Bootstrapped 100% (done): Done")
	if !m.torDaemonReady(port) {
		t.Fatal("100% and a listener that answers SOCKS is a ready engine")
	}

	// 100% with no listener is not: a daemon that said Done and died
	// before its port stayed open is a failed connect, not a ready one.
	dead, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if m.torDaemonReady(dead) {
		t.Fatal("ready on a port nobody listens on")
	}

	// A fresh spawn starts from nothing.
	m2 := NewTorManager(nil)
	if m2.torDaemonReady(port) {
		t.Fatal("a manager that has run nothing reported ready")
	}
}

// TestTorDaemonStateDirIsUnderTheEngineWorkDir: the daemon's state is
// engine data, so it lives where the other engines' data lives and is
// created before the child runs — a --DataDirectory that does not exist
// is a start failure, and the folder is the same one the readiness test
// above reads its marks out of.
func TestTorDaemonStateDirIsUnderTheEngineWorkDir(t *testing.T) {
	work := withTorDaemonHooks(t, "")
	dir := torDaemonStateDir()
	// Separator-aware: the work dir hook may hand back forward slashes
	// and filepath.Join does not.
	if dir == "" || !isUnderDir(dir, work) {
		t.Errorf("torDaemonStateDir = %q, want it under the engine work dir %q", dir, work)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("torDaemonStateDir did not create %s: %v", dir, err)
	}
}

// TestStandaloneTorWarmupCoversAColdDirectoryFetch: the hop's first
// bootstrap on an install is not the second one. Measured on this
// machine with the daemon riding an aether MASQUE tunnel: 5m43s to
// Bootstrapped 100% against an empty state dir and 9s against the cache
// that run left behind — so the first connect gave up at 5m0s on a hop
// that finished 43 seconds later, and every connect after it worked.
// The budget follows the cache, and a caller that already asked for more
// is never cut down.
func TestStandaloneTorWarmupCoversAColdDirectoryFetch(t *testing.T) {
	if torColdChainWarmup <= torChainWarmup {
		t.Fatalf("cold budget %s does not outlast the plain one %s", torColdChainWarmup, torChainWarmup)
	}
	if torColdChainWarmup < 8*time.Minute {
		t.Errorf("cold budget %s is shorter than the measured 5m43s fetch plus room to fail and retry", torColdChainWarmup)
	}
	withTorDaemonHooks(t, "")
	state := torDaemonStateDir()
	ch := make(chan LogEntry, 64)
	m := NewTorManager(ch)

	// Cold: the directory exists — torDaemonStateDir makes it — but
	// holds nothing tor wrote there.
	if got := standaloneTorWarmup(m, torChainWarmup); got != torColdChainWarmup {
		t.Errorf("cold hop budget = %s, want %s", got, torColdChainWarmup)
	}
	var e LogEntry
	select {
	case e = <-ch:
	default:
		t.Fatal("the cold budget was spent without saying why")
	}
	if !strings.Contains(e.Message, torColdChainWarmup.String()) ||
		!strings.Contains(e.Message, torChainWarmup.String()) ||
		!strings.Contains(e.Message, "fetch") {
		t.Errorf("the cold budget was spent without saying why: %q", e.Message)
	}

	// Empty leftovers are still cold: a lock file is not a consensus.
	if err := os.WriteFile(filepath.Join(state, "lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := standaloneTorWarmup(m, torChainWarmup); got != torColdChainWarmup {
		t.Errorf("hop budget beside a zero-byte file = %s, want %s", got, torColdChainWarmup)
	}
	for len(ch) > 0 {
		<-ch
	}
	// A warm cache is what the base is worth: the fetch is done, and a
	// connect that fails now has no minutes of directory left in it.
	if err := os.WriteFile(filepath.Join(state, "state"), []byte("addr=127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := standaloneTorWarmup(m, torChainWarmup); got != torChainWarmup {
		t.Errorf("warm hop budget = %s, want the base %s", got, torChainWarmup)
	}
	select {
	case e := <-ch:
		t.Errorf("a hop that needs no extra room logged %q", e.Message)
	default:
	}

	// A caller that named a longer budget than the cold one keeps it —
	// and the check runs with the cache back out of the way, so it is
	// the floor and not the warm answer doing the work.
	if err := os.Remove(filepath.Join(state, "state")); err != nil {
		t.Fatal(err)
	}
	if got := standaloneTorWarmup(m, 20*time.Minute); got != 20*time.Minute {
		t.Errorf("budget = %s, want the longer base left alone", got)
	}
}

// TestStandaloneTorHopSpendsTheColdBudget: both production hops choose
// their budget from the state they are about to run in. A probe that
// reads the helper alone cannot tell a connect that uses it from one
// that still hands the daemon torChainWarmup.
func TestStandaloneTorHopSpendsTheColdBudget(t *testing.T) {
	src := readGuiFile(t, "core_manager.go")
	for _, sig := range []string{
		"func (m *CoreManager) startAetherTorHop(",
		"func (m *CoreManager) startTorChainLeg(",
	} {
		if body := goFuncBody(t, src, sig); !strings.Contains(body, "standaloneTorWarmup(") {
			t.Errorf("%s gives the daemon a budget that has no room for a cold fetch", sig)
		}
	}
}
