package main

import (
	"strings"
	"testing"
)

// The bridge lines the Tor Project hands out, as one Windows paste
// delivers them: CRLF between the lines, webtunnel carrying url= and
// ver=, obfs4 carrying cert= and iat-mode=. Every address and digest is
// synthetic — documentation ranges (RFC 5737 / RFC 3849) and a cert that
// decodes to its own disclaimer — because a real bridge line pasted here
// is a real user's bridge list published, and one that also lands in a
// test run against a live config is a dead line the next connect waits
// on.
const pastedBridgeLines = "webtunnel [2001:db8:12ff:2d55:9130:36a7:c49b:d1f4]:443 0123456789ABCDEF0123456789ABCDEF01234567 url=https://bridge.invalid/qzxrtfmu/ ver=0.0.3\r\n" +
	"webtunnel [2001:db8:a1f3:36af:54ca:cb71:b350:5c8a]:443 0123456789ABCDEF0123456789ABCDEF01234567 url=https://bridge.invalid/qzxrtfmu/ ver=0.0.7\r\n" +
	"\r\n" +
	"# a comment and a blank line must not become a bridge\r\n" +
	"obfs4 198.51.100.7:443 ABCDEF0123456789ABCDEF0123456789ABCDEF01 cert=" + testOnlyBridgeCert + " iat-mode=0\r\n" +
	"obfs4 203.0.113.9:80 ABCDEF0123456789ABCDEF0123456789ABCDEF01 cert=" + testOnlyBridgeCert + " iat-mode=2\r\n"

// testOnlyBridgeCert stands in for an obfs4 cert= argument: valid
// base64, so a parser that checks the shape is still exercised, and it
// decodes to "test-only-obfs4-cert-not-a-real-one".
const testOnlyBridgeCert = "dGVzdC1vbmx5LW9iZnM0LWNlcnQtbm90LWEtcmVhbC1vbmU="

// bridgeSettings is a Chain tab set to the user's own lines.
func bridgeSettings(t *testing.T) Settings {
	t.Helper()
	s := DefaultSettings()
	s.AetherTor = true
	s.ChainTorBridges = "own"
	s.ChainTorBridgeLines = pastedBridgeLines
	if err := normalizeChainTor(&s); err != nil {
		t.Fatalf("normalizeChainTor: %v", err)
	}
	return s
}

// wantBridges collects the lines a --tor-bridge run of [args] carries.
func wantBridges(t *testing.T, args []string) []string {
	t.Helper()
	var got []string
	for i, w := range args {
		if w == "--tor-bridge" {
			if i+1 >= len(args) {
				t.Fatalf("--tor-bridge with no line: %q", args)
			}
			got = append(got, args[i+1])
		}
	}
	return got
}

// TestPastedBridgeLinesReachBothTorLegs: the lines of the Chain tab must
// arrive at aether.exe as --tor-bridge arguments on both the Aether leg
// (tor=chain on the profile) and the SNI-spoof leg (--tor-only). A bridge
// that does not reach the core is a bridge the user does not have, and
// the connect then waits on a relay fetch the network blocks.
func TestPastedBridgeLinesReachBothTorLegs(t *testing.T) {
	s := bridgeSettings(t)
	want := []string{
		"webtunnel [2001:db8:12ff:2d55:9130:36a7:c49b:d1f4]:443 0123456789ABCDEF0123456789ABCDEF01234567 url=https://bridge.invalid/qzxrtfmu/ ver=0.0.3",
		"webtunnel [2001:db8:a1f3:36af:54ca:cb71:b350:5c8a]:443 0123456789ABCDEF0123456789ABCDEF01234567 url=https://bridge.invalid/qzxrtfmu/ ver=0.0.7",
		"obfs4 198.51.100.7:443 ABCDEF0123456789ABCDEF0123456789ABCDEF01 cert=" + testOnlyBridgeCert + " iat-mode=0",
		"obfs4 203.0.113.9:80 ABCDEF0123456789ABCDEF0123456789ABCDEF01 cert=" + testOnlyBridgeCert + " iat-mode=2",
	}

	p := &Profile{ID: "0", Name: "WARP", Protocol: "aether"}
	cp := aetherTorChainProfile(p, s)
	if aetherEnumDefault(aetherTors, cp.Aether.Tor, "off") != "chain" {
		t.Fatalf("Aether leg tor = %q, want chain", cp.Aether.Tor)
	}
	args, listen, custom := aetherCoreOf(cp, 57203)
	if custom {
		t.Fatal("a profile with no hand-written command built a custom command line")
	}
	if listen != 57203 {
		t.Errorf("dial port = %d, want the Tor bind 57203 (the tunnel proxy takes the next one)", listen)
	}
	if got := wantBridges(t, args); !equalStrings(got, want) {
		t.Errorf("Aether leg bridges =\n%q\nwant\n%q", got, want)
	}
	if containsStr(args, "--tor-bridges") || containsStr(args, "--no-tor-bridges") {
		t.Errorf("own lines also asked for fetched bridges: %q", args)
	}

	chain := torOnlyArgs(58000, 59000, "info", "", s)
	if got := wantBridges(t, chain); !equalStrings(got, want) {
		t.Errorf("SNI-spoof leg bridges =\n%q\nwant\n%q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAetherEngineWarmupWaitsForATorBootstrap: the engine's own listener
// is Tor's when Tor runs inside the tunnel, and aether opens it only at
// the end of a bootstrap. 30s is the budget for a WARP handshake; a cold
// consensus fetch runs for minutes, so the connect of a healthy
// bootstrap was being reported as a failure.
func TestAetherEngineWarmupWaitsForATorBootstrap(t *testing.T) {
	s := bridgeSettings(t)
	p := &Profile{ID: "0", Name: "WARP", Protocol: "aether"}
	torArgs := aetherArgsWithTor(t, s, p, "chain")
	if got := aetherEngineWarmup(torArgs); got <= aetherWarmup {
		t.Errorf("warmup for tor=chain = %s, want more than the plain engine budget %s", got, aetherWarmup)
	}
	for _, mode := range []string{"only", "reverse"} {
		args := aetherArgsWithTor(t, s, p, mode)
		if got := aetherEngineWarmup(args); got <= aetherWarmup {
			t.Errorf("warmup for tor=%s = %s, want more than %s", mode, got, aetherWarmup)
		}
	}

	plain := &Profile{ID: "1", Name: "WARP", Protocol: "aether"}
	args, _, _ := aetherCoreOf(plain, 57203)
	if got := aetherEngineWarmup(args); got != aetherWarmup {
		t.Errorf("warmup with no tor = %s, want %s", got, aetherWarmup)
	}

	// Start must spend the budget it computes.
	src := readGuiFile(t, "aether_manager.go")
	body := goFuncBody(t, src, "func (m *AetherManager) StartWithToken(")
	if !strings.Contains(body, "aetherEngineWarmup(args)") {
		t.Error("StartWithToken never asks what a run of these arguments is worth")
	}
	if !strings.Contains(body, "runProcess(exe, args, listen, warmup,") {
		t.Error("StartWithToken still probes readiness on a fixed budget")
	}
}

// aetherArgsWithTor builds the engine's argv for a profile running Tor in
// mode [mode] with the Chain tab's options.
func aetherArgsWithTor(t *testing.T, s Settings, p *Profile, mode string) []string {
	t.Helper()
	cp := *p
	cp.Aether.Command = ""
	cp.Aether.Tor = mode
	cp.Aether.TorBridges = s.ChainTorBridges
	cp.Aether.TorBridgeLines = s.ChainTorBridgeLines
	cp.Aether.TorRelays = s.ChainTorRelays
	args, _, _ := aetherCoreOf(&cp, 57203)
	if aetherTorModeOf(args) != mode {
		t.Fatalf("args do not run tor=%s: %q", mode, args)
	}
	return args
}

// goFuncBody returns the block of one top-level func, found by its
// [sig] substring and cut at the next func.
func goFuncBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%s is gone from the source", sig)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// TestChainPanelKeepsPastedTextThroughARender: the Chain tab re-renders
// once a second because its banner reads the connection poll, and a
// :value binding alone resets the control to the stored text under the
// cursor. Pasting bridge lines therefore deleted them before @change
// (blur) could save — the text never reached disk, and so never reached
// --tor-bridge. Every free-text control here must write the store as the
// text changes, and let the save trail it.
func TestChainPanelKeepsPastedTextThroughARender(t *testing.T) {
	src := readFrontendSrc(t, "components/ChainPanel.vue")
	for _, field := range []string{
		"chainTorBridgeLines", "chainPsiphonCdnIps", "chainPsiphonCdnSni", "chainPsiphonCdnSets",
	} {
		if !strings.Contains(src, `@input="setChainLive('`+field+`',`) {
			t.Errorf("%s is not written to the store as it is typed or pasted", field)
		}
		if !strings.Contains(src, `@change="flushChainStr('`+field+`',`) {
			t.Errorf("%s does not save on blur", field)
		}
	}
	live := fnBody(t, src, "setChainLive")
	if !strings.Contains(live, "settings.settings[field] = value") {
		t.Error("setChainLive does not write the store it renders from")
	}
	if !strings.Contains(live, "setTimeout") {
		t.Error("setChainLive saves on every keystroke instead of trailing the typing")
	}
	flush := fnBody(t, src, "flushChainStr")
	for _, want := range []string{"clearTimeout", "saveChainText"} {
		if !strings.Contains(flush, want) {
			t.Errorf("flushChainStr does not mention %s", want)
		}
	}
	save := fnBody(t, src, "saveChainText")
	if !strings.Contains(save, "settings.save()") {
		t.Error("saveChainText does not persist the settings")
	}
	// Leaving the tab with a save still pending must not drop the text.
	if !strings.Contains(src, "onBeforeUnmount") {
		t.Error("a pending chain save is never flushed when the panel unmounts")
	}
}

// TestBridgeLinesSurviveASettingsRoundTrip is the disk half of the same
// bug: what the panel saved must come back out of GetSettings intact, so
// a connect after a restart still has the bridges.
func TestBridgeLinesSurviveASettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := bridgeSettings(t)
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	back := store.GetSettings()
	if !strings.Contains(back.ChainTorBridgeLines, "webtunnel [2001:db8:12ff") {
		t.Errorf("saved bridge lines lost the webtunnel entry: %q", back.ChainTorBridgeLines)
	}
	if strings.Contains(back.ChainTorBridgeLines, "\r") {
		t.Errorf("saved bridge lines kept a carriage return: %q", back.ChainTorBridgeLines)
	}
}

// autoChainState is what leaving the tab after a paste actually stores:
// the lines kept, the shape back on auto.
func autoChainState(t *testing.T) Settings {
	t.Helper()
	s := bridgeSettings(t)
	s.ChainTorBridges = "auto"
	if err := normalizeChainTor(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBridgeLinesIdleNoteReadsTheArgvItWasGiven: the reason a run gives
// for not using the saved lines has to come out of the argv it was
// handed, so it cannot drift from what torOptionArgs actually rendered.
func TestBridgeLinesIdleNoteReadsTheArgvItWasGiven(t *testing.T) {
	s := autoChainState(t)
	if got := wantBridges(t, torOptionArgs(s.ChainTorBridges, s.ChainTorBridgeLines, s.ChainTorRelays)); len(got) != 0 {
		t.Fatalf("auto with stored lines now hands them to the core (%q): this test's premise is stale", got)
	}

	for _, tc := range []struct {
		shape string
		why   string
	}{
		{"auto", "auto"},
		{"first", "first"},
		{"never", "never"},
	} {
		args := append([]string{"--tor"}, torOptionArgs(tc.shape, pastedBridgeLines, "auto")...)
		note := bridgeLinesIdleNote(args, pastedBridgeLines)
		for _, want := range []string{"4 bridge line", tc.why, "own"} {
			if !strings.Contains(note, want) {
				t.Errorf("Bridges=%s note %q does not say %q", tc.shape, note, want)
			}
		}
	}

	// The three runs with nothing to explain: the lines went through,
	// there were no lines, or there is no Tor to feed.
	for _, tc := range []struct {
		name  string
		args  []string
		lines string
	}{
		{"own passes them",
			append([]string{"--tor"}, torOptionArgs("own", pastedBridgeLines, "auto")...), pastedBridgeLines},
		{"nothing saved",
			append([]string{"--tor"}, torOptionArgs("auto", "# only a comment\n", "auto")...), "# only a comment\n"},
		{"no tor", []string{"--bind", "127.0.0.1:58000"}, pastedBridgeLines},
	} {
		if note := bridgeLinesIdleNote(tc.args, tc.lines); note != "" {
			t.Errorf("%s: note = %q, want empty", tc.name, note)
		}
	}
}

// TestIdleBridgeLinesAreWarnedAtConnect: the notice has to reach the log
// bus of the run that dropped the lines, on both legs — the engine's
// (the profile's own Tor options) and the spoof chain's (the Chain tab
// straight). A warning is what the user reads after a five-minute wait.
func TestIdleBridgeLinesAreWarnedAtConnect(t *testing.T) {
	ch := make(chan LogEntry, 8)
	m := NewTorManager(ch)
	s := autoChainState(t)

	m.noteIdleBridgeLines(torOnlyArgs(58000, 59000, "info", "", s), s.ChainTorBridgeLines)
	e := <-ch
	if e.Level != "warn" {
		t.Errorf("idle bridges logged at %q, want warn", e.Level)
	}
	if !strings.Contains(e.Message, "4 bridge line") || !strings.Contains(e.Message, "auto") {
		t.Errorf("idle bridges notice = %q", e.Message)
	}

	// A run that got its bridges, that saved none, or that runs no Tor
	// stays quiet.
	own := bridgeSettings(t)
	noTor := []string{"--bind", "127.0.0.1:58002"}
	for _, tc := range []struct {
		name  string
		args  []string
		lines string
	}{
		{"own", torOnlyArgs(58001, 59001, "info", "", own), own.ChainTorBridgeLines},
		{"no lines saved", torOnlyArgs(58002, 59002, "info", "", s), ""},
		{"no tor", noTor, pastedBridgeLines},
	} {
		m.noteIdleBridgeLines(tc.args, tc.lines)
		select {
		case e := <-ch:
			t.Errorf("%s logged %q, want silence", tc.name, e.Message)
		default:
		}
	}

	// Both legs ask, from what they are about to run.
	src := readGuiFile(t, "aether_manager.go")
	for _, sig := range []string{
		"func (m *AetherManager) StartWithToken(",
		"func (m *AetherManager) StartTorOnly(",
	} {
		body := goFuncBody(t, src, sig)
		if !strings.Contains(body, "noteIdleBridgeLines(") {
			t.Errorf("%s drops stored bridge lines without saying so", sig)
		}
	}
}

// TestChainPanelNamesABridgePasteItIsNotUsing: the lines editor renders
// only under own, so a paste saved under another shape is both invisible
// and unused — which reads exactly like the bug a paste had before it
// was fixed, and the connect then waits the whole bootstrap budget on a
// directory fetch this network truncates. The tab has to say how many
// lines are waiting instead of leaving the warning to a five-minute
// failure.
func TestChainPanelNamesABridgePasteItIsNotUsing(t *testing.T) {
	src := readFrontendSrc(t, "components/ChainPanel.vue")
	i := strings.Index(src, "const savedBridges = computed(")
	if i < 0 {
		t.Fatal("ChainPanel.vue never counts the bridge lines it is holding")
	}
	block := src[i:]
	if j := strings.Index(block, "\n)"); j >= 0 {
		block = block[:j]
	}
	for _, want := range []string{"chainTorBridgeLines", "startsWith('#')", ".length"} {
		if !strings.Contains(block, want) {
			t.Errorf("the saved-bridge count does not use %s: %s", want, block)
		}
	}
	if !strings.Contains(src, "bridgesChoice !== 'own' && savedBridges") {
		t.Error("no hint names the stored lines the current Bridges shape will not use")
	}
}
