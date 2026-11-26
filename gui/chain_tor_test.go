package main

// Chain-level Tor options and the Tor leg of both chains.
//
// Tor is the second exit chain, and it has to behave like the first one
// in every place that matters: both legs (the SNI-spoof chain and the
// Aether chain) read the same Settings fields, both hand aether.exe the
// flags its own --help documents, and the Chain tab is the only place
// those fields are set. aether.exe carries arti, so Tor needs no binary
// NekoGo does not already ship — but it does need its own data directory,
// its own listener tag in the rendered config and a longer readiness
// budget, because a cold Tor bootstrap fetches a consensus before the
// first byte can move.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// torChainSettings returns what the Chain tab stores for Tor after a
// save, run through the same normalization the save path applies.
func torChainSettings(t *testing.T) Settings {
	t.Helper()
	s := DefaultSettings()
	s.ChainTorBridges = " first "
	s.ChainTorRelays = "only"
	s.ChainTorBridgeLines = "obfs4 198.51.100.7:443 AAAA iat-mode=0\r\n# kept out of the way\r\n\r\nobfs4 203.0.113.9:80 BBBB\r\n"
	if err := normalizeChainTor(&s); err != nil {
		t.Fatalf("normalizeChainTor: %v", err)
	}
	return s
}

// TestTorOptionArgsShape pins the Tor flags both legs share, taken from
// aether's own --help: bridges are tried plainly first unless the user
// says otherwise, own lines replace the fetched ones, and the relay pool
// only means something while bridges are being fetched.
func TestTorOptionArgsShape(t *testing.T) {
	lines := "obfs4 198.51.100.7:443 AAAA iat-mode=0\nobfs4 203.0.113.9:80 BBBB"
	cases := []struct {
		name                        string
		bridges, bridgeLines, relay string
		want                        []string
	}{
		{"defaults fetch nothing", "", "", "", nil},
		{"auto is the default", "auto", "", "auto", nil},
		{"unknown spellings are the default", "sideways", "", "sideways", nil},
		{"straight to bridges", "first", "", "auto", []string{"--tor-bridges"}},
		{"never bridges", "never", "", "auto", []string{"--no-tor-bridges"}},
		{"own lines", "own", lines, "auto", []string{
			"--tor-bridge", "obfs4 198.51.100.7:443 AAAA iat-mode=0",
			"--tor-bridge", "obfs4 203.0.113.9:80 BBBB",
		}},
		{"own lines ignore a comment", "own", "# nothing\n" + lines, "auto", []string{
			"--tor-bridge", "obfs4 198.51.100.7:443 AAAA iat-mode=0",
			"--tor-bridge", "obfs4 203.0.113.9:80 BBBB",
		}},
		{"relay pool while fetching", "first", "", "only", []string{"--tor-bridges", "--tor-relays", "only"}},
		{"relay pool off", "auto", "", "off", []string{"--tor-relays", "off"}},
		{"own lines fetch no relays", "own", lines, "only", []string{
			"--tor-bridge", "obfs4 198.51.100.7:443 AAAA iat-mode=0",
			"--tor-bridge", "obfs4 203.0.113.9:80 BBBB",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := torOptionArgs(tc.bridges, tc.bridgeLines, tc.relay)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("torOptionArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTorOnlyArgsCarriesChainOptions: the SNI-spoof leg runs aether with
// no profile to read, so the Chain tab's Tor options have to reach argv
// here or the tab changes nothing. --tor-only publishes plain Tor on
// --bind and dials out through --upstream, exactly the Psiphon shape.
func TestTorOnlyArgsCarriesChainOptions(t *testing.T) {
	base := []string{
		"--bind", "127.0.0.1:1234",
		"--tor-only",
		"--upstream", "socks5://127.0.0.1:5678",
		"--log-level", "debug",
	}
	got := torOnlyArgs(1234, 5678, "debug", "", DefaultSettings())
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("torOnlyArgs(defaults) = %v, want %v", got, base)
	}

	got = torOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, torChainSettings(t))
	want := append(append([]string{}, base...),
		"--tor-bridges",
		"--tor-relays", "only",
		"--tor-dir", `C:\chain-dir`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("torOnlyArgs(chain options) = %v, want %v", got, want)
	}
}

// TestAetherTorChainProfileCarriesChainOptions mirrors the Psiphon leg:
// the toggle builds a tor=chain copy, the Chain tab's options ride along,
// the stored profile is untouched, and a profile that chose its own Tor
// shape keeps it.
func TestAetherTorChainProfileCarriesChainOptions(t *testing.T) {
	p := &Profile{Protocol: "aether", Name: "warp"}
	s := torChainSettings(t)

	if cp := aetherTorChainProfile(p, s); cp != p {
		t.Fatal("AetherTor off produced a chain profile")
	}
	s.AetherTor = true
	cp := aetherTorChainProfile(p, s)
	if cp.Aether.Tor != "chain" {
		t.Fatalf("chain profile Tor = %q, want chain", cp.Aether.Tor)
	}
	for _, f := range []struct{ name, got, want string }{
		{"bridges", cp.Aether.TorBridges, "first"},
		{"relays", cp.Aether.TorRelays, "only"},
	} {
		if f.got != f.want {
			t.Errorf("chain profile %s = %q, want %q", f.name, f.got, f.want)
		}
	}
	if !strings.Contains(cp.Aether.TorBridgeLines, "198.51.100.7") {
		t.Errorf("chain profile dropped the bridge lines: %q", cp.Aether.TorBridgeLines)
	}
	if p.Aether.Tor != "" || p.Aether.TorBridges != "" {
		t.Fatalf("the stored profile was mutated: %+v", p.Aether)
	}

	own := &Profile{Protocol: "aether", Name: "own"}
	own.Aether.Tor = "only"
	own.Aether.TorBridges = "never"
	if got := aetherTorChainProfile(own, s); got.Aether.Tor != "only" || got.Aether.TorBridges != "never" {
		t.Fatalf("chain options overwrote the profile's own tor settings: %+v", got.Aether)
	}
}

// TestOneExitChainPerLeg: two chains on one leg is not a shape aether
// runs — the leg publishes one listener and the rendered config routes
// into one — so a config carrying both switches must resolve to one
// driver deterministically instead of arming whichever gate is read
// first. Psiphon wins: it is the flag that was already there, so a
// hand-edited config keeps doing what it did.
func TestOneExitChainPerLeg(t *testing.T) {
	s := DefaultSettings()
	s.SpoofPsiphon = true
	s.SpoofTor = true
	s.AetherPsiphon = true
	s.AetherTor = true
	notes := resolveChainDrivers(&s)
	if len(notes) != 2 {
		t.Fatalf("resolveChainDrivers returned %d notes, want one per leg: %v", len(notes), notes)
	}
	if s.SpoofTor || s.AetherTor {
		t.Fatalf("both drivers stayed armed: spoof psiphon=%v tor=%v, aether psiphon=%v tor=%v",
			s.SpoofPsiphon, s.SpoofTor, s.AetherPsiphon, s.AetherTor)
	}
	if got := spoofChainDriver(s); got != chainDriverPsiphon {
		t.Errorf("spoof leg driver = %q, want %q", got, chainDriverPsiphon)
	}
	if got := aetherChainDriver(s); got != chainDriverPsiphon {
		t.Errorf("aether leg driver = %q, want %q", got, chainDriverPsiphon)
	}

	s = DefaultSettings()
	s.SpoofTor = true
	s.AetherTor = true
	if notes := resolveChainDrivers(&s); len(notes) != 0 {
		t.Errorf("a Tor-only config was rewritten: %v", notes)
	}
	if got := spoofChainDriver(s); got != chainDriverTor {
		t.Errorf("spoof leg driver = %q, want %q", got, chainDriverTor)
	}
	if got := aetherChainDriver(s); got != chainDriverTor {
		t.Errorf("aether leg driver = %q, want %q", got, chainDriverTor)
	}
	if got := spoofChainDriver(DefaultSettings()); got != "" {
		t.Errorf("no chain armed, driver = %q", got)
	}

	// The legs are independent: Tor on the spoof leg says nothing about
	// the Aether one.
	s = DefaultSettings()
	s.SpoofTor = true
	s.AetherPsiphon = true
	if notes := resolveChainDrivers(&s); len(notes) != 0 {
		t.Errorf("one driver per leg was read as one driver per app: %v", notes)
	}
	if got := spoofChainDriver(s); got != chainDriverTor {
		t.Errorf("spoof leg driver = %q, want tor", got)
	}
	if got := aetherChainDriver(s); got != chainDriverPsiphon {
		t.Errorf("aether leg driver = %q, want psiphon", got)
	}
}

// TestTorChainGatesTheSameWayPsiphonDoes: everything that keys off an
// armed chain — the transport choice, the forced TUN inbound, the
// elevation check — has to see a Tor chain too, or Connect renders a
// config with no TUN inbound for a chain that needs one.
func TestTorChainGatesTheSameWayPsiphonDoes(t *testing.T) {
	p := spoofTestProfile()
	s := DefaultSettings()
	s.SpoofTor = true
	if !spoofChain(p, s) {
		t.Fatal("spoofChain = false for a spoof profile with Tor armed")
	}
	if spoofPsiphonChain(p, s) {
		t.Error("spoofPsiphonChain = true for a Tor chain")
	}
	if !tunRequiredFor(p, s) {
		t.Error("tunRequiredFor = false for an armed Tor chain")
	}
	xray := s
	xray.SpoofXray = true
	if spoofTransportXray(p, xray) {
		t.Error("a Tor chain followed the Xray transport, which renders no TUN inbound")
	}
	s.SpoofTor = false
	if spoofChain(p, s) {
		t.Error("spoofChain = true with nothing armed")
	}
}

// TestTorChainRendersItsOwnOutbound: the rendered sing-box config names
// the chain it runs. Routing the Tor leg into an outbound tagged
// "psiphon" would work and lie in every log and config dump the user is
// asked to hand over.
func TestTorChainRendersItsOwnOutbound(t *testing.T) {
	setSpoofChainPorts(chainDriverTor, 40556, 40557)
	defer clearSpoofChainPorts()
	cfg := map[string]any{
		"outbounds": []any{map[string]any{"type": "direct", "tag": "proxy"}},
		"inbounds":  []any{map[string]any{"type": "tun", "tag": "tun-in"}},
	}
	route := map[string]any{"rules": []any{}}
	renderSpoofChain(cfg, route)
	if chainOutbound(cfg, "tor") == nil {
		t.Fatal("no tor outbound in the rendered config")
	}
	if chainOutbound(cfg, spoofPsiphonTag) != nil {
		t.Error("a Tor chain rendered the psiphon outbound too")
	}
	out := chainOutbound(cfg, "tor")
	if out["server_port"] != 40556 {
		t.Errorf("tor outbound port = %v, want the published chain port", out["server_port"])
	}
	rules, _ := route["rules"].([]any)
	var up, in string
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		tags, _ := m["inbound"].([]any)
		has := func(want string) bool {
			for _, t := range tags {
				if t == want {
					return true
				}
			}
			return false
		}
		o, _ := m["outbound"].(string)
		if has("tor-up-in") {
			up = o
		}
		if has("tun-in") && has("mixed-in") {
			in = o
		}
	}
	if up != "proxy" {
		t.Errorf("the tor upstream inbound routes to %q, want proxy", up)
	}
	if in != "tor" {
		t.Errorf("system traffic routes to %q, want the tor outbound", in)
	}
}

// TestTorChainWaitsForAColdBootstrap: Psiphon's 120s budget covers a
// client that fetches a server list and tunnels. Tor fetches a directory
// consensus first and, on a network that blocks it, walks a bridge pool
// whose own per-bridge budget is 360s — so the chain that inherits
// Psiphon's number would be killed mid-bootstrap and reported as a
// failure to connect.
func TestTorChainWaitsForAColdBootstrap(t *testing.T) {
	if torChainWarmup <= psiphonChainWarmup {
		t.Fatalf("torChainWarmup = %s, want longer than Psiphon's %s", torChainWarmup, psiphonChainWarmup)
	}
	if torChainWarmup < 4*time.Minute {
		t.Errorf("torChainWarmup = %s, want at least 4m for a cold bootstrap", torChainWarmup)
	}
}

// TestTorChainGetsItsOwnDataDir: Tor keeps a directory cache and state
// under --tor-dir, and two runs sharing it contend for the same lock the
// way two Psiphon runs contend for the bbolt datastore.
func TestTorChainGetsItsOwnDataDir(t *testing.T) {
	d1 := newChainTorDir()
	d2 := newChainTorDir()
	if d1 == d2 {
		t.Fatalf("two Tor chain runs share a data dir: %s", d1)
	}
	for _, d := range []string{d1, d2} {
		if !strings.Contains(filepath.Base(d), chainTorDirPrefix) {
			t.Errorf("data dir %q is not under the %q prefix, so the pruner never finds it", d, chainTorDirPrefix)
		}
		if _, err := os.Stat(d); err != nil {
			t.Errorf("data dir %q was not created: %v", d, err)
		}
	}
	t.Cleanup(func() {
		for _, d := range []string{d1, d2} {
			_ = os.RemoveAll(d)
		}
	})
}

func TestNormalizeChainTor(t *testing.T) {
	s := torChainSettings(t)
	if s.ChainTorBridges != "first" {
		t.Errorf("bridges = %q, want first", s.ChainTorBridges)
	}
	if s.ChainTorRelays != "only" {
		t.Errorf("relays = %q, want only", s.ChainTorRelays)
	}
	if strings.Contains(s.ChainTorBridgeLines, "\r") {
		t.Errorf("bridge lines kept a carriage return: %q", s.ChainTorBridgeLines)
	}
	if strings.HasSuffix(s.ChainTorBridgeLines, "\n") {
		t.Errorf("bridge lines kept trailing whitespace: %q", s.ChainTorBridgeLines)
	}

	s = DefaultSettings()
	s.ChainTorBridges = "SIDEWAYS"
	s.ChainTorRelays = "lots"
	if err := normalizeChainTor(&s); err != nil {
		t.Fatal(err)
	}
	// aether's enums are matched case-sensitively: an unknown spelling
	// falls back to the default rather than reaching the core.
	if s.ChainTorBridges != "auto" || s.ChainTorRelays != "auto" {
		t.Errorf("unknown enums normalized to %q/%q, want auto/auto", s.ChainTorBridges, s.ChainTorRelays)
	}
}

// TestChainTorOptionsSurviveAReload: a Chain-tab pick that never reaches
// the file is gone at the next start and the leg quietly falls back to
// aether's defaults. The store is reopened from the same directory, so
// this reads what is on disk and then hands it to the renderer the leg
// runs.
func TestChainTorOptionsSurviveAReload(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	s := store.GetSettings()
	s.SpoofTor = true
	s.ChainTorBridges = " first "
	s.ChainTorRelays = "only"
	s.ChainTorBridgeLines = "obfs4 198.51.100.7:443 AAAA iat-mode=0"
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("reading the saved config: %v", err)
	}
	for _, key := range []string{"spoofTor", "chainTorBridges", "chainTorRelays", "chainTorBridgeLines"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("config.json carries no %s", key)
		}
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.GetSettings()
	if !got.SpoofTor {
		t.Error("the Tor switch did not survive the reload")
	}
	if want := torChainSettings(t); !reflect.DeepEqual(
		torOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, got),
		torOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, want),
	) {
		t.Fatalf("argv from the reloaded config = %v, want %v",
			torOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, got),
			torOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, want))
	}
}

// TestChainPanelWiresEveryTorOption: the Chain tab is frontend-only, so
// its wiring is pinned the way the Psiphon one is — a field that exists
// in Go but has no control on the panel is an option nobody can set. The
// generated model is checked too: api.ts casts the store object, so
// vue-tsc cannot catch a key the bindings do not declare.
func TestChainPanelWiresEveryTorOption(t *testing.T) {
	fields := []string{"chainTorBridges", "chainTorRelays", "chainTorBridgeLines"}
	panel := readFrontendSrc(t, "components/ChainPanel.vue")
	for _, field := range append([]string{"spoofTor", "aetherTor"}, fields...) {
		if !strings.Contains(panel, field) {
			t.Errorf("ChainPanel.vue has no control for %s", field)
		}
	}
	types := readFrontendSrc(t, "types/index.ts")
	for _, field := range append([]string{"spoofTor", "aetherTor"}, fields...) {
		if !strings.Contains(types, field) {
			t.Errorf("the Settings mirror has no %s field", field)
		}
	}
	model, err := os.ReadFile(filepath.Join("frontend", "bindings", "nekogo", "models.ts"))
	if err != nil {
		t.Fatalf("reading bindings/nekogo/models.ts: %v", err)
	}
	for _, field := range append([]string{"spoofTor", "aetherTor"}, fields...) {
		if !strings.Contains(string(model), `"`+field+`"`) {
			t.Errorf("bindings/nekogo/models.ts does not declare %s; regenerate the bindings", field)
		}
	}
}

// TestEveryChainWriterSetsBothDrivers: a leg runs one exit chain, and
// three places switch it — the Chain tab plus the SNI and Aether tabs'
// Psiphon checkboxes. The store's Settings copy is never re-read after
// a save, so a writer that touches only its own driver leaves the panel
// showing a chain the backend has already cleared (the log says Tor was
// turned off; the UI still claims it is armed). Every writer therefore
// writes both flags of its leg.
func TestEveryChainWriterSetsBothDrivers(t *testing.T) {
	for _, tc := range []struct {
		file, fn, psiphon, tor string
	}{
		{"components/SNIPanel.vue", "saveChain", "spoofPsiphon", "spoofTor"},
		{"components/AetherPanel.vue", "saveAetherChain", "aetherPsiphon", "aetherTor"},
	} {
		src := readFrontendSrc(t, tc.file)
		body := fnBody(t, src, tc.fn)
		if !strings.Contains(src, tc.psiphon) {
			t.Errorf("%s no longer writes %s", tc.file, tc.psiphon)
		}
		if !strings.Contains(body, tc.tor) {
			t.Errorf("%s does not clear %s on its save path, so the store keeps a driver the backend dropped",
				tc.file, tc.tor)
		}
	}
	panel := readFrontendSrc(t, "components/ChainPanel.vue")
	if body := fnBody(t, panel, "setLeg"); !strings.Contains(body, "prev") {
		t.Error("ChainPanel.setLeg does not roll the pair back when the save fails")
	}
	// The leg → field map is the single place the four booleans are
	// named; a Tor flag missing from it is a driver no picker can set.
	for _, field := range []string{"spoofPsiphon", "spoofTor", "aetherPsiphon", "aetherTor"} {
		if !strings.Contains(panel, field) {
			t.Errorf("ChainPanel.vue never mentions %s", field)
		}
	}
}

// TestTorOnlyLegPaysForAFirstFetchEveryConnect: the arti leg's budget is
// not the daemon's. --tor-only runs in a --tor-dir newChainTorDir made
// for this connect, and the next connect removes the previous one, so
// there is never a cached consensus to ride and every bootstrap walks
// the whole directory fetch — 5m43s measured for that fetch on this
// machine. A base of torChainWarmup therefore reported a hop that was
// still fetching as a connect that had failed, on every connect, not
// only the first one.
func TestTorOnlyLegPaysForAFirstFetchEveryConnect(t *testing.T) {
	if got := chainTorOnlyWarmup(torChainWarmup); got != torColdChainWarmup {
		t.Errorf("--tor-only budget = %s, want the cold-fetch one %s", got, torColdChainWarmup)
	}
	if got := chainTorOnlyWarmup(20 * time.Minute); got != 20*time.Minute {
		t.Errorf("budget = %s, want a longer base left alone", got)
	}

	// And StartTorOnly spends it, in a run that says what it waits for.
	src := readGuiFile(t, "aether_manager.go")
	body := goFuncBody(t, src, "func (m *AetherManager) StartTorOnly(")
	for _, want := range []string{
		"newChainTorDir()",                    // the premise: the dir is per-connect
		"chainTorOnlyWarmup(warmup)",          // the budget is raised for it
		"runProcess(exe, args, port, warmup,", // and what it raised is what is spent
	} {
		if !strings.Contains(body, want) {
			t.Errorf("StartTorOnly does not mention %q: the arti leg's wait is unaccounted for", want)
		}
	}
	if !strings.Contains(body, "m.log(") {
		t.Error("StartTorOnly raises its own budget without telling the log which one it waits on")
	}
}
