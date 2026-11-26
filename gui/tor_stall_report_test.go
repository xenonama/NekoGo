package main

import (
	"strings"
	"testing"
	"time"
)

// arti's tracing subscriber colourises every line it writes, and aether
// passes those escapes through untouched. They reached both the log
// console and the readiness failure, which quoted a Tor chain's last
// output as `"\x1b[2m2026-11-14T14:39:19.839161Z\x1b[0m \x1b[32m INFO\x1b[0m …"`
// — escape noise where the reason should have been.
const ansiInfoLine = "\x1b[2m2026-11-14T14:39:16.402504Z\x1b[0m \x1b[32m INFO\x1b[0m " +
	"\x1b[2mtor_dirmgr\x1b[0m\x1b[2m:\x1b[0m Didn't get usable directory from cache.\n"

// The core's own status lines carry a marker: [+] worked, [*] progress,
// [-] gave up. A stalled directory bootstrap is the [-] line below, and
// it arrived at level info — indistinguishable from progress in a console
// the user reads while connecting.
const ansiStallLine = "[2026-11-14T15:05:15.286Z \x1b[32mINFO\x1b[0m  aether::tor::with_tor] " +
	"[-] tor over your bridges: Stuck at 15%: Can't bootstrap a Tor directory. (Can't make progress.)\n"

const plainStallLine = "[-] tor over your bridges: Stuck at 15%: Can't bootstrap a Tor directory. (Can't make progress.)"

func TestEngineOutputReachesTheLogWithoutAnsiEscapes(t *testing.T) {
	m := NewTorManager(make(chan LogEntry, 8))
	w := m.linePipe()
	if _, err := w.Write([]byte(ansiInfoLine + ansiStallLine)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := []string{(<-m.logCh).Message, (<-m.logCh).Message}
	tail := m.recent.tail(2)
	for i, line := range append(got, tail...) {
		if strings.ContainsRune(line, '\x1b') {
			t.Fatalf("line %d kept escapes: %q", i, line)
		}
	}
	if !strings.Contains(got[0], "Didn't get usable directory from cache.") {
		t.Fatalf("stripping lost the message: %q", got[0])
	}
	if !strings.Contains(got[1], "Stuck at 15%") {
		t.Fatalf("stripping lost the status line: %q", got[1])
	}
	// The notice the stall line leaves behind is readable as well.
	if strings.ContainsRune(m.stallNotice(), '\x1b') {
		t.Fatalf("notice carries escapes: %q", m.stallNotice())
	}

	// The generic child writer (sing-box, xray, mihomo, the spoof tool)
	// feeds the same ring and the same viewer, so it strips too.
	gch := make(chan LogEntry, 4)
	var gring lineRing
	gw := &logWriter{ch: gch, source: "sing-box", ring: &gring}
	if _, err := gw.Write([]byte(ansiInfoLine)); err != nil {
		t.Fatalf("generic write: %v", err)
	}
	if line := (<-gch).Message; strings.ContainsRune(line, '\x1b') {
		t.Fatalf("logWriter kept escapes: %q", line)
	}
	if line := gring.tail(1)[0]; strings.ContainsRune(line, '\x1b') {
		t.Fatalf("ring kept escapes: %q", line)
	}
}

// The priority reader sees two shapes: a bare core line ("[-] …") and one
// with aether's "[ts LEVEL target] " header in front of it. The marker
// must win in both, or the give-up line hides among progress lines.
func TestGiveUpStatusLineLogsAsWarn(t *testing.T) {
	cases := []struct {
		line      string
		wantLevel string
		wantMsg   string
	}{
		{plainStallLine, "warn", "[-] tor over your bridges: Stuck at 15%"},
		{"[2026-11-14T15:05:15.286Z INFO  aether::tor::with_tor] [-] tor over your bridges: Stuck at 15%",
			"warn", "[-] tor over your bridges: Stuck at 15%"},
		{"[*] bootstrapping tor through the tunnel at 127.0.0.1:61197", "info", "[*] bootstrapping"},
		{"[+] socks5 server listening on 127.0.0.1:61197", "info", "[+] socks5 server listening"},
		{"[+] loaded existing masque identity", "info", "[+] loaded existing"},
		// A header's own level still wins for a core error line, and a
		// bracketed tag that is not a marker keeps its priority.
		{"[2026-11-14T15:05:15.286Z ERROR aether::tor] wireguard: no endpoint", "error", "wireguard: no endpoint"},
		{"[h2] tls established; alpn=h2", "info", "[h2] tls established"},
	}
	for _, tc := range cases {
		level, msg := aetherLinePriority(tc.line)
		if level != tc.wantLevel {
			t.Errorf("level for %q = %q, want %q", tc.line, level, tc.wantLevel)
		}
		if !strings.HasPrefix(msg, tc.wantMsg) {
			t.Errorf("message for %q = %q, want prefix %q", tc.line, msg, tc.wantMsg)
		}
	}
}

// A Tor run that gives up never opens its listener and aether retries its
// directory download forever, so the only end was the 5m0s readiness
// timeout — reported as "did not become ready" with a random last line.
// The engine had already said why; the failure must say it too.
func TestReadinessFailureNamesATorStall(t *testing.T) {
	m := NewTorManager(make(chan LogEntry, 8))
	w := m.linePipe()
	if _, err := w.Write([]byte(ansiStallLine)); err != nil {
		t.Fatalf("write: %v", err)
	}
	notice := m.stallNotice()
	if notice == "" {
		t.Fatal("stall line left no notice")
	}
	low := strings.ToLower(notice)
	for _, want := range []string{"15%", "directory", "bridge"} {
		if !strings.Contains(low, want) {
			t.Fatalf("notice %q missing %q", notice, want)
		}
	}

	// The wait then quotes the notice instead of an escape-filled tail.
	err := m.waitSocksOrExit(1, 20*time.Millisecond, make(chan error, 1), func(int) bool { return false }, func() bool { return true })
	if err == nil {
		t.Fatal("expected a readiness error")
	}
	if !strings.Contains(err.Error(), "15%") {
		t.Fatalf("error does not name the stall: %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("error carries escapes: %v", err)
	}

	// A run that never said it was stuck is not refused for a stall. The
	// wait is bounded by the run itself, not by a wall clock: a core that
	// is still connecting keeps its rounds (PattN AwaitWarmUpAsync), and
	// the wait ends when this spawn is no longer the current one.
	plain := NewAetherManager(nil)
	if got := plain.stallNotice(); got != "" {
		t.Fatalf("fresh manager reports a stall: %q", got)
	}
	rounds := 0
	perr := plain.waitSocksOrExit(1, 20*time.Millisecond, make(chan error, 1), func(int) bool { return false },
		func() bool { rounds++; return rounds < 4 })
	if perr == nil || strings.Contains(perr.Error(), "15%") {
		t.Fatalf("plain manager invented a stall: %v", perr)
	}
	if !strings.Contains(perr.Error(), "no longer the current run") {
		t.Fatalf("plain manager did not report the bound that ended its wait: %v", perr)
	}
}

// One stalled run must not leave its notice behind for the next: the
// manager is reused, and a Tor chain retried after a stalled attempt must
// not be refused with the previous attempt's reason.
func TestStallNoticeIsClearedPerRun(t *testing.T) {
	m := NewTorManager(make(chan LogEntry, 8))
	w := m.linePipe()
	if _, err := w.Write([]byte(plainStallLine + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if m.stallNotice() == "" {
		t.Fatal("notice missing before the reset")
	}
	m.clearStall()
	if got := m.stallNotice(); got != "" {
		t.Fatalf("reset left %q", got)
	}
	// Lines that are not a give-up must not set one either.
	if _, err := w.Write([]byte("[*] tor over your bridges: 15%: connecting successfully\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := m.stallNotice(); got != "" {
		t.Fatalf("a progress line set a stall: %q", got)
	}
}
