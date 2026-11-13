package main

// The Settings tab is frontend-only, so its contract is pinned the way
// about_versions_test.go pins the Version stats card: read the source and
// assert what the user actually sees. Behaviour that a source check cannot
// reach — the port field's parsing, the dialog's answer — is driven in the
// browser through the same store the component reads.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func settingsSrc(t *testing.T) string {
	t.Helper()
	return readFrontendSrc(t, filepath.Join("components", "SettingsPanel.vue"))
}

// readLocalSrc reads a file next to the test binary's working directory —
// the engine source, for the checks that compare a panel list against the
// renderer's own.
func readLocalSrc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// TestSettingsHeroNamesThisApp: the tab's own hero card said "Customize
// your NekoBox experience". NekoBox is the core this GUI drives, not the
// product — the About tab says so in its first line — and the sentence
// sits above every setting the user is about to change.
func TestSettingsHeroNamesThisApp(t *testing.T) {
	src := settingsSrc(t)
	for _, want := range []string{"your NekoBox experience", "NekoBox experience"} {
		if strings.Contains(src, want) {
			t.Errorf("Settings hero still says %q", want)
		}
	}
	line := regexp.MustCompile(`<p>([^<]*)</p>`).FindStringSubmatch(src)
	if line == nil || !strings.Contains(line[1], "NekoGo") {
		t.Errorf("Settings hero subtitle names neither the app nor what the tab holds: %v", line)
	}
}

// TestSettingsMixedPortRowMatchesWhatTheCoresBind: the Mixed port row
// promised "HTTP proxy on this port · SOCKS proxy on port + 1" for every
// core. That is the Xray shape — xray_manager.go renders http on the port
// and socks-in on port+1 — while BuildSingBoxConfig renders ONE inbound of
// type "mixed", which serves both protocols on the one port, and mihomo's
// mixed-port does the same. A sing-box user reading the row hunts for a
// SOCKS listener that never exists, which is exactly the port confusion
// that costs a connect.
func TestSettingsMixedPortRowMatchesWhatTheCoresBind(t *testing.T) {
	src := settingsSrc(t)
	m := regexp.MustCompile(`key: 'mixed',.*?sub: '([^']*)'`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("Settings has no Mixed port row to check")
	}
	sub := m[1]
	if strings.Contains(sub, "HTTP proxy on this port · SOCKS proxy on port + 1") {
		t.Errorf("the row still states the Xray port shape as if it were every core's: %q", sub)
	}
	if !strings.Contains(sub, "Xray") {
		t.Errorf("the row never names the one core that does use port+1: %q", sub)
	}
	if regexp.MustCompile(`(?i)share|same port|one port`).FindString(sub) == "" {
		t.Errorf("the row never says the sing-box cores serve both protocols on the port: %q", sub)
	}
	// The control refuses 65535 (its max, and the clamp); saying so is
	// cheaper than letting the field silently drop the digit.
	if !strings.Contains(sub, "65534") {
		t.Errorf("the row hides the range the field accepts: %q", sub)
	}
}

// TestSettingsConnectionHeadDoesNotContradictItsRow: the Connection card
// header asserted "the OS proxy is set on every connect" while its own
// System-proxy switch — and app.go, which only auto-ensures the OS proxy
// when the flag is set — says the opposite. Two sentences in one card
// disagreeing is how a user ends up unsure which one the app obeys.
func TestSettingsConnectionHeadDoesNotContradictItsRow(t *testing.T) {
	src := settingsSrc(t)
	m := regexp.MustCompile(`key: 'connection',[\s\S]*?headSub: '([^']*)'`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("Settings has no Connection card to check")
	}
	if strings.Contains(strings.ToLower(m[1]), "os proxy is set on every connect") {
		t.Errorf("the Connection header still promises a proxy on every connect: %q", m[1])
	}
	if strings.Contains(strings.ToLower(m[1]), "proxy") {
		t.Errorf("the proxy belongs to its row, not the header, which cannot state the condition honestly: %q", m[1])
	}
}

// TestSettingsTunAdviceNeedsTunMode: the MTU hint and the TUN stack select
// rendered on the Connection tab with TUN off. Both describe a virtual
// adapter that is not being created, so the panel spent its space on a
// mode the user had already turned off.
func TestSettingsTunAdviceNeedsTunMode(t *testing.T) {
	src := settingsSrc(t)

	i := strings.Index(src, "try lowering the MTU")
	if i < 0 {
		t.Fatal("the TUN MTU hint is gone from the Settings tab")
	}
	// The gate is the v-if nearest above the hint. It now lives on the
	// <template> wrapping the whole TUN group, so reading only the hint's
	// own <div> would report an ungated row that is not one.
	open := strings.LastIndex(src[:i], "v-if=")
	if open < 0 {
		t.Fatal("cannot find the element holding the MTU hint")
	}
	end := strings.Index(src[open:], ">")
	if end < 0 {
		t.Fatal("unterminated v-if above the MTU hint")
	}
	tag := src[open : open+end]
	if !strings.Contains(tag, "tunEnabled") {
		t.Errorf("the MTU hint renders with TUN off: %s", tag)
	}

	for _, key := range []string{"tunstack"} {
		line := regexp.MustCompile(`key: '` + key + `'[^\n]*`).FindString(src)
		if line == "" {
			t.Fatalf("Settings has no %q row", key)
		}
		if !strings.Contains(line, "show:") {
			t.Errorf("the %q row shows in every mode: %s", key, line)
		}
		if !strings.Contains(line, "tunEnabled") {
			t.Errorf("the %q row is not gated on TUN mode: %s", key, line)
		}
	}
}

// TestSettingsPortFieldKeepsItsPort: the number control parsed with
// parseInt(raw, 10) || 0 and then clamped, so clearing the field or typing
// "208oo" stored port 1 — a value validateMixedPort accepts, and a connect
// then dials a listener nobody configured. The row delegates to
// parsePortInput, which returns the previous port and a message.
func TestSettingsPortFieldKeepsItsPort(t *testing.T) {
	src := settingsSrc(t)
	if strings.Contains(src, "Math.min(65534, Math.max(1, parseInt") {
		t.Error("the number row still invents a port out of text it cannot parse")
	}
	if !strings.Contains(src, "parsePortInput(") {
		t.Error("the number row does not use parsePortInput")
	}
	ports := readFrontendSrc(t, "ports.ts")
	// The old shape was parseInt(raw, 10) || 0; it appears in this file
	// only as the bug being described, so the body is what gets checked.
	body := regexp.MustCompile(`(?s)export function parsePortInput\(.*?\n\}`).FindString(ports)
	if body == "" {
		t.Fatal("ports.ts no longer exports parsePortInput(...)")
	}
	if strings.Contains(body, "|| 0") || strings.Contains(body, "parseInt") {
		t.Error("parsePortInput still turns unparsable text into a port number")
	}
}

// TestSettingsClearsCountriesWithItsOwnDialog: the GeoIP "Clear" button
// opened window.confirm. WebView2 renders that as a browser box — the same
// defect that took the elevation prompt off window.confirm — and it is the
// only confirmation left in the tab, so the destructive action of all
// things arrived in the wrong chrome.
func TestSettingsClearsCountriesWithItsOwnDialog(t *testing.T) {
	src := settingsSrc(t)
	// Call syntax, not the name: the file explains in prose why the
	// browser box went away, and that sentence must not read as a bug.
	for _, native := range []string{"confirm('", "confirm(`", "window.confirm("} {
		if strings.Contains(src, native) {
			t.Errorf("the Settings tab still calls the browser dialog (%s)", native)
		}
	}
	if !strings.Contains(src, "useConfirmStore") {
		t.Error("the Settings tab does not use the app's confirm store")
	}
}

// TestSettingsAppearanceHeadMatchesItsRows: the Appearance card's header
// promised "Theme, window and sidebar". It has no window row — the stored
// windowStyle setting has no control anywhere — so the header advertises
// a knob the tab does not have, which is what sends a user looking for it.
func TestSettingsAppearanceHeadMatchesItsRows(t *testing.T) {
	src := settingsSrc(t)
	m := regexp.MustCompile(`key: 'appearance',[\s\S]*?headSub: '([^']*)'`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("Settings has no Appearance card to check")
	}
	if strings.Contains(strings.ToLower(m[1]), "window") {
		t.Errorf("the Appearance header offers a window control the card does not have: %q", m[1])
	}
}

// TestSettingsUpdatesCountsEveryCheckItRuns: backend.checkUpdates() is the
// whole list — a row's Check re-stamps every component's lastCheck — but
// only checkAll recorded the timestamp the panel's own header reads. So
// after pressing Check the rows said "checked just now" while the header
// insisted the last global check was "never", two answers to one question.
func TestSettingsUpdatesCountsEveryCheckItRuns(t *testing.T) {
	src := readFrontendSrc(t, filepath.Join("components", "UpdatesPanel.vue"))
	one := regexp.MustCompile(`(?s)async function checkOne\(.*?\n\}`).FindString(src)
	all := regexp.MustCompile(`(?s)async function checkAll\(.*?\n\}`).FindString(src)
	if one == "" || all == "" {
		t.Fatal("UpdatesPanel no longer has checkOne/checkAll to compare")
	}
	if strings.Contains(all, "lastGlobalCheck.value") && !strings.Contains(one, "lastGlobalCheck.value") {
		t.Error("checkOne runs the global check but never records it")
	}
}

// TestSettingsTrayToggleExplainsItself: the tray and start-on-boot toggles
// reach Go directly, and their failure paths diverged — launch-at-login
// reported why, the tray toggle wrote console.error, which shows the user
// nothing but a switch that flips back by itself.
func TestSettingsTrayToggleExplainsItself(t *testing.T) {
	src := settingsSrc(t)
	body := regexp.MustCompile(`(?s)if \(field === 'closeToTray'\) \{.*?\n[ ]{2}\}`).FindString(src)
	if body == "" {
		t.Fatal("the tray toggle no longer has its own branch in flip()")
	}
	if strings.Contains(body, "console.error") {
		t.Error("a refused tray toggle still only logs to the console")
	}
	if !strings.Contains(body, "saveErr.value") {
		t.Error("a refused tray toggle gives the user no message")
	}
}

// TestSettingsTunGroupReachesThePanel: the engine now reads a settings
// group (tun_config.go) instead of constants, so the panel has to offer
// it — an MTU you cannot lower on a link that needs it, or a TUN address
// you cannot move off a collision, is the same dead end as the setting
// that never existed. And the whole group is dead weight with TUN off.
func TestSettingsTunGroupReachesThePanel(t *testing.T) {
	src := settingsSrc(t)
	block := regexp.MustCompile(`(?s)<template v-if="tab === 'connection' && s\.tunEnabled">.*?</template>`).FindString(src)
	if block == "" {
		t.Fatal("Settings has no TUN group gated on tunEnabled")
	}
	for _, field := range []string{
		"s.tunMTU", "s.tunAutoRoute", "s.tunStrictRoute",
		"s.tunEnableIPv6", "s.tunAddress", "s.tunIcmp",
	} {
		if !strings.Contains(block, field) {
			t.Errorf("the TUN group does not render %s", field)
		}
	}
	// The excluded routes take a list, and a list needs its own control.
	if !strings.Contains(block, "s.tunRouteExclude") {
		t.Error("the TUN group offers no way to exclude a route")
	}
}

// TestSettingsTunHintNamesTheSetting: the old hint told the user the
// core renders 1400 (it rendered 1350) and sent them to Advanced →
// Config → Inbound to change it. Both halves were wrong, and only one
// of them is fixable by rewording — the MTU is a row now.
func TestSettingsTunHintNamesTheSetting(t *testing.T) {
	src := settingsSrc(t)
	if strings.Contains(src, "NekoGo renders 1400") {
		t.Error("the TUN hint still states an MTU the renderer does not use")
	}
	hint := regexp.MustCompile(`(?s)If heavy pages.*?</div>`).FindString(src)
	if hint == "" {
		t.Fatal("the TUN hint is gone: a stall in TUN mode has no advice next to it")
	}
	if !strings.Contains(hint, "MTU") {
		t.Errorf("the TUN hint does not point at the MTU setting: %q", hint)
	}
	// The hint used to send the user to the raw config editor because the
	// MTU was unchangeable anywhere else. It is a row now; an instruction
	// to hand-edit the core config would be a worse path to the same field.
	if strings.Contains(hint, "Inbound") {
		t.Errorf("the TUN hint still routes around the row it owns: %q", hint)
	}
}

// TestSettingsMtuIsNotCheckedAsAPort: the generic number setter validates
// a port (1-65534) and labels the error "Mixed port". Reusing it for an
// MTU would refuse 9000 as "must be 1-65534"? no — it would accept
// anything a port accepts and tell the user they got the port wrong.
// The MTU needs its own bound and its own name in the message.
func TestSettingsMtuIsNotCheckedAsAPort(t *testing.T) {
	src := settingsSrc(t)
	body := regexp.MustCompile(`(?s)async function setTunMTU\(.*?\n[ ]{2}\}`).FindString(src)
	if body == "" {
		t.Fatal("the MTU row has no setter of its own")
	}
	if strings.Contains(body, "parsePortInput") {
		t.Error("the MTU row validates through the port parser")
	}
	// The bounds are the engine's own (tun_config.go: 1280-65535), and
	// the default it shows is what singbox_manager.go renders.
	for _, def := range []string{`TUN_MTU_MIN = 1280`, `TUN_MTU_MAX = 65535`, `TUN_MTU_DEFAULT = 1350`} {
		if !strings.Contains(src, def) {
			t.Errorf("the panel does not define %s", def)
		}
	}
	for _, name := range []string{"TUN_MTU_MIN", "TUN_MTU_MAX"} {
		if !strings.Contains(body, name) {
			t.Errorf("setTunMTU does not enforce %s", name)
		}
	}
}

// TestSettingsTunPoolsMatchTheEngines: the dropdowns and
// gui/tun_config.go must offer the same addresses. A list that drifts
// shows the user a network the renderer would happily have accepted and
// hides the one that resolves their collision.
func TestSettingsTunPoolsMatchTheEngines(t *testing.T) {
	goSrc := readLocalSrc(t, "tun_config.go")
	fe := settingsSrc(t)
	for goVar, feConst := range map[string]string{
		"tunIPv4Options": "TUN_V4",
		"tunIPv6Options": "TUN_V6",
	} {
		want := quotedEntries(t, goSrc, goVar)
		got := quotedEntries(t, fe, feConst)
		if len(want) == 0 {
			t.Fatalf("%s not found in tun_config.go", goVar)
		}
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%s drift: engine %v, panel %v", goVar, want, got)
		}
	}
}

// quotedEntries reads the quoted strings of one list literal, from
// `var X = []string{` (Go) or `const X = [` (TS) down to the line holding
// only its closing brace. Reading to that line rather than to the first
// bracket keeps the helper usable on both sources; a list reformatted onto
// one line stops matching, which the callers report as "not found".
func quotedEntries(t *testing.T, src, name string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?s)(?:var|const) ` + regexp.QuoteMeta(name) + `.*?\n[\]}]`).FindString(src)
	if m == "" {
		return nil
	}
	var out []string
	for _, q := range regexp.MustCompile(`["']([^"'\n]+)["']`).FindAllStringSubmatch(m, -1) {
		out = append(out, q[1])
	}
	return out
}

// TestSettingsFrontendCarriesTheTunGroup: Wails hands the panel the
// generated type; a field the frontend type does not know is a row that
// cannot bind, and vue-tsc is the only thing that says so.
func TestSettingsFrontendCarriesTheTunGroup(t *testing.T) {
	src := readFrontendSrc(t, filepath.Join("types", "index.ts"))
	for _, field := range []string{
		"tunMTU", "tunAddress", "tunAddress6", "tunEnableIPv6",
		"tunAutoRoute", "tunStrictRoute", "tunRouteExclude", "tunIcmp",
	} {
		// Unset means "render what NekoGo always rendered", so every one
		// of them is optional in the type — and an optional field is
		// written `name?:`, which a search for `name:` misses.
		if !strings.Contains(src, field+"?:") {
			t.Errorf("the frontend Settings type has no %q field", field)
		}
	}
	// One of them is three-valued: unset and false are different answers
	// to the renderer, so the frontend mirror of DefaultSettings has to
	// state it instead of leaving it undefined.
	if !strings.Contains(src, "tunAutoRoute: true") {
		t.Error("the frontend defaults do not state auto-route")
	}
}
