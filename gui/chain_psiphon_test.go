package main

// Chain-level Psiphon options (Settings → Chain tab, advanced mode).
//
// Two legs run Psiphon: the SNI-spoof chain (spoofPsiphon, a plain
// psiphon-only aether run) and the Aether chain (aetherPsiphon, psiphon
// carried inside the tunnel). Both read the same Settings fields, and
// both must hand aether.exe the same flags the profile editor hands it —
// otherwise the Chain tab silently changes what one leg does and not the
// other.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// chainSettings returns what the Chain tab stores after a save: the
// values as typed, run through the save-path normalization, so the args
// tests also cover the canonicalization (space list -> comma list).
func chainSettings(t *testing.T) Settings {
	t.Helper()
	s := DefaultSettings()
	s.ChainPsiphonRegion = " de "
	s.ChainPsiphonMode = "cdn"
	s.ChainPsiphonCDN = "1.1.1.1 8.8.8.8"
	s.ChainPsiphonSNI = "example.com"
	s.ChainPsiphonSets = "fastly,cloudflare"
	if err := normalizeChainPsiphon(&s); err != nil {
		t.Fatalf("normalizeChainPsiphon: %v", err)
	}
	return s
}

// TestPsiphonOnlyArgsCarriesChainOptions: the SNI-spoof leg used to get
// no psiphon options at all — it always ran with aether's defaults, so
// the country the user picked in the Chain tab was ignored.
func TestPsiphonOnlyArgsCarriesChainOptions(t *testing.T) {
	base := []string{
		"--bind", "127.0.0.1:1234",
		"--psiphon-only",
		"--upstream", "socks5://127.0.0.1:5678",
		"--log-level", "debug",
	}
	got := psiphonOnlyArgs(1234, 5678, "debug", "", DefaultSettings())
	if want := append(append([]string{}, base...), "--psiphon-mode", "auto"); !reflect.DeepEqual(got, want) {
		t.Fatalf("psiphonOnlyArgs(defaults) = %v, want %v", got, want)
	}

	got = psiphonOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, chainSettings(t))
	want := append(append([]string{}, base...),
		"--psiphon-mode", "cdn",
		"--psiphon-cdn-ips", "1.1.1.1,8.8.8.8",
		"--psiphon-cdn-sni", "example.com",
		"--psiphon-cdn-sets", "cloudflare,fastly",
		"--psiphon-region", "DE",
		"--psiphon-dir", `C:\chain-dir`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("psiphonOnlyArgs(chain options) = %v, want %v", got, want)
	}
}

// TestPsiphonOptionArgsShape pins the rules both legs share, taken from
// aether's own --help: the sni names count only beside an ip list of
// one's own, and the built-in edge sets mean nothing to a direct run
// that does no fronting.
func TestPsiphonOptionArgsShape(t *testing.T) {
	cases := []struct {
		name                         string
		mode, ips, sni, sets, region string
		want                         []string
	}{
		{"empty mode is auto", "", "", "", "", "", []string{"--psiphon-mode", "auto"}},
		{"bad mode is auto", "sideways", "", "", "", "", []string{"--psiphon-mode", "auto"}},
		{"sni needs ips", "cdn", "", "example.com", "", "", []string{"--psiphon-mode", "cdn"}},
		{"direct drops fronting", "direct", "1.1.1.1", "example.com", "cloudflare", "DE",
			[]string{"--psiphon-mode", "direct", "--psiphon-region", "DE"}},
		{"sets drop unknown names", "cdn", "1.1.1.1", "", "cloudflare,not-a-set", "",
			[]string{"--psiphon-mode", "cdn", "--psiphon-cdn-ips", "1.1.1.1", "--psiphon-cdn-sets", "cloudflare"}},
		{"no region no flag", "auto", "", "", "", "   ", []string{"--psiphon-mode", "auto"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := psiphonOptionArgs(tc.mode, tc.ips, tc.sni, tc.sets, tc.region)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("psiphonOptionArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAetherChainProfileCarriesChainOptions: the toggle builds a
// synthetic psiphon=chain profile, and that copy must carry the Chain
// tab's options — while the stored profile is never touched, and a
// profile that names its own Psiphon mode keeps its own settings
// (chain options drive the chain legs only).
func TestAetherChainProfileCarriesChainOptions(t *testing.T) {
	p := &Profile{Protocol: "aether", Name: "warp"}
	s := chainSettings(t)

	cp := aetherPsiphonChainProfile(p, s)
	if cp != p {
		t.Fatal("AetherPsiphon off produced a chain profile")
	}
	s.AetherPsiphon = true
	cp = aetherPsiphonChainProfile(p, s)
	if cp.Aether.Psiphon != "chain" {
		t.Fatalf("chain profile Psiphon = %q, want chain", cp.Aether.Psiphon)
	}
	for _, f := range []struct {
		name, got, want string
	}{
		{"mode", cp.Aether.PsiphonMode, "cdn"},
		{"cdn ips", cp.Aether.PsiphonCDN, "1.1.1.1,8.8.8.8"},
		{"cdn sni", cp.Aether.PsiphonSNI, "example.com"},
		{"cdn sets", cp.Aether.PsiphonSets, "cloudflare,fastly"},
		{"region", cp.Aether.PsiphonRegn, "DE"},
	} {
		if f.got != f.want {
			t.Errorf("chain profile %s = %q, want %q", f.name, f.got, f.want)
		}
	}
	if p.Aether.Psiphon != "" || p.Aether.PsiphonRegn != "" {
		t.Fatalf("the stored profile was mutated: %+v", p.Aether)
	}

	// A profile that chose its own Psiphon shape is not the chain's to
	// reconfigure.
	own := &Profile{Protocol: "aether", Name: "own"}
	own.Aether.Psiphon = "only"
	own.Aether.PsiphonRegn = "US"
	if got := aetherPsiphonChainProfile(own, s); got.Aether.PsiphonRegn != "US" || got.Aether.Psiphon != "only" {
		t.Fatalf("chain options overwrote the profile's own psiphon settings: %+v", got.Aether)
	}
}

func TestNormalizeChainPsiphon(t *testing.T) {
	s := DefaultSettings()
	s.ChainPsiphonRegion = " de "
	s.ChainPsiphonMode = "CDN"
	s.ChainPsiphonCDN = "1.1.1.1 8.8.8.8"
	s.ChainPsiphonSets = "cloudflare, bogus"
	if err := normalizeChainPsiphon(&s); err != nil {
		t.Fatal(err)
	}
	if s.ChainPsiphonRegion != "DE" {
		t.Errorf("region = %q, want DE", s.ChainPsiphonRegion)
	}
	// Mode is aether's own enum, matched case-sensitively: an unknown
	// spelling falls back to the default rather than reaching the core.
	if s.ChainPsiphonMode != "auto" {
		t.Errorf("mode = %q, want auto", s.ChainPsiphonMode)
	}
	if s.ChainPsiphonCDN != "1.1.1.1,8.8.8.8" {
		t.Errorf("cdn ips = %q", s.ChainPsiphonCDN)
	}
	if s.ChainPsiphonSets != "cloudflare" {
		t.Errorf("sets = %q, want the known name only", s.ChainPsiphonSets)
	}

	for _, bad := range []string{"germany", "D", "DEU", "1", "D E"} {
		s := DefaultSettings()
		s.ChainPsiphonRegion = bad
		if err := normalizeChainPsiphon(&s); err == nil {
			t.Errorf("region %q accepted, want an ISO alpha-2 rejection", bad)
		}
	}
	// Blank means "Psiphon chooses" and must stay legal.
	s = DefaultSettings()
	s.ChainPsiphonRegion = ""
	if err := normalizeChainPsiphon(&s); err != nil {
		t.Fatalf("empty region rejected: %v", err)
	}
}

// TestSaveSettingsRejectsABadChainRegion: the rejection has to happen on
// the save path, or the UI accepts a country aether silently ignores.
func TestSaveSettingsRejectsABadChainRegion(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	s := store.GetSettings()
	s.ChainPsiphonRegion = "Germany"
	if err := app.SaveSettings(s); err == nil {
		t.Fatal("SaveSettings accepted a non-alpha-2 chain region")
	}
	s.ChainPsiphonRegion = "de"
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if got := store.GetSettings().ChainPsiphonRegion; got != "DE" {
		t.Fatalf("stored region = %q, want the canonical DE", got)
	}
}

// TestChainPsiphonOptionsSurviveAReload: a Chain-tab pick that never
// reaches the file is gone at the next start, and both legs quietly fall
// back to aether's own defaults. The store is re-opened from the same
// directory so this reads what is on disk rather than the copy in memory,
// then hands it to the same renderer the legs run.
func TestChainPsiphonOptionsSurviveAReload(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	s := store.GetSettings()
	s.ChainPsiphonRegion = " de "
	s.ChainPsiphonMode = "cdn"
	s.ChainPsiphonCDN = "1.1.1.1 8.8.8.8"
	s.ChainPsiphonSNI = "example.com"
	s.ChainPsiphonSets = "fastly,cloudflare"
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("reading the saved config: %v", err)
	}
	for _, key := range []string{
		"chainPsiphonRegion", "chainPsiphonMode",
		"chainPsiphonCdnIps", "chainPsiphonCdnSni", "chainPsiphonCdnSets",
	} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("config.json carries no %s", key)
		}
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := psiphonOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, reopened.GetSettings())
	if want := psiphonOnlyArgs(1234, 5678, "debug", `C:\chain-dir`, chainSettings(t)); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv from the reloaded config = %v, want %v", got, want)
	}
}

// TestChainPanelWiresEveryChainOption: the Chain tab is frontend-only,
// so its wiring is pinned the way the About card's is — read the source
// and assert every option the backend honours has a control. A field
// that exists in Go but not on the panel is an option nobody can set.
func TestChainPanelWiresEveryChainOption(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("frontend", "src", "components", "ChainPanel.vue"))
	if err != nil {
		t.Fatalf("reading ChainPanel.vue: %v", err)
	}
	src := string(raw)
	for _, field := range []string{
		"chainPsiphonRegion", "chainPsiphonMode",
		"chainPsiphonCdnIps", "chainPsiphonCdnSni", "chainPsiphonCdnSets",
	} {
		if !strings.Contains(src, field) {
			t.Errorf("ChainPanel.vue has no control for %s", field)
		}
	}
	types, err := os.ReadFile(filepath.Join("frontend", "src", "types", "index.ts"))
	if err != nil {
		t.Fatalf("reading types/index.ts: %v", err)
	}
	for _, field := range []string{"chainPsiphonRegion", "chainPsiphonMode"} {
		if !strings.Contains(string(types), field) {
			t.Errorf("the Settings mirror has no %s field", field)
		}
	}
	// The generated model is the frontend's type for Settings. A field it
	// does not declare still reaches Go at runtime, but only because
	// api.ts casts the store object through `as unknown as` — so vue-tsc
	// cannot catch a renamed or misspelled key, and the panel silently
	// edits a field nothing reads. Regenerate bindings
	// (wails3 generate bindings -ts) whenever a Settings field is added.
	model, err := os.ReadFile(filepath.Join("frontend", "bindings", "nekogo", "models.ts"))
	if err != nil {
		t.Fatalf("reading bindings/nekogo/models.ts: %v", err)
	}
	for _, field := range []string{
		"chainPsiphonRegion", "chainPsiphonMode",
		"chainPsiphonCdnIps", "chainPsiphonCdnSni", "chainPsiphonCdnSets",
	} {
		if !strings.Contains(string(model), `"`+field+`"`) {
			t.Errorf("bindings/nekogo/models.ts does not declare %s; regenerate the bindings", field)
		}
	}
}
