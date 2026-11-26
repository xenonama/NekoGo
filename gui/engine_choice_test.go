package main

// Regression tests for the engine-choice contract: Settings.CoreEngine
// selects which sing-box binary runs EVERY sing-box transport — a plain
// profile, the SNI-spoof transport, and the Aether transport. Before
// this, a spoof profile always ran nekobox_core even with the official
// sing-box selected, which is exactly what users reported.
//
// The official CLI is stood in for by the test binary copied to
// sing-box.exe (fake_singbox_test.go); the spoof engine by
// testdata/spoofmock. No real binaries, drivers or admin rights needed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type engineRig struct {
	cores    *CoreManager
	upstream *UpstreamSingBoxManager
	custom   *SingBoxManager
	spoof    *SpoofManager
	settings Settings
}

func newEngineRig(t *testing.T, coreEngine string) *engineRig {
	t.Helper()
	buildSpoofMock(t)
	logCh := make(chan LogEntry, 8192)
	custom := NewSingBoxManager("", logCh)
	up := NewUpstreamSingBoxManager(logCh)
	cores := NewCoreManager(custom, NewXrayManager("", t.TempDir(), logCh))
	spoofMgr := NewSpoofManager(logCh)
	cores.SetSpoofManager(spoofMgr)
	cores.SetUpstreamSingBoxManager(up)

	s := spoofTestSettings()
	s.SystemProxy = false
	s.TunEnabled = false
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	s.MixedPort = port
	s.CoreEngine = coreEngine
	if coreEngine == "upstream" {
		s.UpstreamCorePath = installFakeSingBox(t)
	}
	return &engineRig{cores: cores, upstream: up, custom: custom, spoof: spoofMgr, settings: s}
}

// TestSpoofTransportHonorsCoreEngine: a CoreSpoof connect runs the
// selected engine as its transport, not always the custom core.
func TestSpoofTransportHonorsCoreEngine(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SNI spoof engine is Windows-only")
	}
	rig := newEngineRig(t, "upstream")
	if err := rig.cores.Start(spoofTestProfile(), rig.settings); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rig.cores.Stop()
	if !rig.upstream.IsRunning() {
		t.Fatal("spoof transport is not the official sing-box (UpstreamCorePath)")
	}
	if rig.custom.IsRunning() {
		t.Fatal("custom nekobox_core ran even though CoreEngine=upstream")
	}
	if !rig.cores.groupMemberAlive(true, false, false, false) {
		t.Fatal("group membership does not see the upstream transport")
	}
	if err := rig.cores.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if rig.upstream.IsRunning() {
		t.Fatal("upstream engine survived Stop")
	}
	if rig.spoof.IsRunning() {
		t.Fatal("spoof engine survived Stop")
	}
}

// TestPlainProfileHonorsCoreEngine: a plain profile with the upstream
// engine spawns the official CLI (the existing behaviour, pinned here).
func TestPlainProfileHonorsCoreEngine(t *testing.T) {
	rig := newEngineRig(t, "upstream")
	p := &Profile{
		ID: "plain-1", Name: "plain", Core: CoreSingBox, Protocol: "trojan",
		Address: "example.com", Port: 443, IDOrPassword: "pw", TLS: true,
	}
	if err := rig.cores.Start(p, rig.settings); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rig.cores.Stop()
	if !rig.upstream.IsRunning() {
		t.Fatal("plain profile is not running the official sing-box")
	}
	if !rig.cores.groupMemberAlive(false, false, true, false) {
		t.Fatal("group membership does not see the upstream core")
	}
}

// TestUpstreamTransportDeathTearsDownSpoofGroup: an unexpected death of
// the upstream transport faults the whole spoof group (engine included),
// exactly like a custom-core death.
func TestUpstreamTransportDeathTearsDownSpoofGroup(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SNI spoof engine is Windows-only")
	}
	rig := newEngineRig(t, "upstream")
	if err := rig.cores.Start(spoofTestProfile(), rig.settings); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rig.cores.Stop()

	rig.upstream.mu.Lock()
	cmd := rig.upstream.cmd
	rig.upstream.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("upstream child not published")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill upstream child: %v", err)
	}
	waitFor(t, "upstream death", func() bool { return !rig.upstream.IsRunning() })
	waitFor(t, "group teardown", func() bool {
		rig.cores.mu.RLock()
		defer rig.cores.mu.RUnlock()
		return !rig.cores.running
	})
	waitFor(t, "spoof engine teardown", func() bool { return !rig.spoof.IsRunning() })
}

// TestFirstFileInPrefersBinFolder pins the lookup order: bin\ first, the
// legacy exe folder second, name order within a folder.
func TestFirstFileInPrefersBinFolder(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "nekobox_core.exe")
	inBin := filepath.Join(bin, "nekobox_core.exe")
	for _, p := range []string{legacy, inBin} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := firstFileIn([]string{bin, root}, "nekobox_core.exe", "nekobox_core")
	if !ok || got != inBin {
		t.Fatalf("picked %q (ok=%v), want the bin\\ copy %q", got, ok, inBin)
	}
	if got, ok := firstFileIn([]string{bin, root}, "xray.exe"); ok {
		t.Fatalf("picked %q for a missing name", got)
	}
	if err := os.Remove(inBin); err != nil {
		t.Fatal(err)
	}
	if got, ok := firstFileIn([]string{bin, root}, "nekobox_core.exe"); !ok || got != legacy {
		t.Fatalf("legacy fallback picked %q (ok=%v), want %q", got, ok, legacy)
	}
}

// TestEngineInstallDirIsBinFolder: updater installs land in bin\.
func TestEngineInstallDirIsBinFolder(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	want := filepath.Join(filepath.Dir(exe), binDirName)
	if got := engineInstallDir(); got != want {
		t.Fatalf("engineInstallDir = %q, want %q", got, want)
	}
	if got := coreExeDir(); got != want {
		t.Fatalf("coreExeDir = %q, want %q", got, want)
	}
}

func TestCorePreferenceDecisionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name         string
		engine       string
		protocol     string
		storedCore   CoreType
		wantCore     CoreType
		wantEngine   string
		wantFallback bool
	}{
		{"nekobox vless", "custom", "vless", CoreXray, CoreSingBox, "custom", false},
		{"nekobox socks", "custom", "socks", CoreMihomo, CoreSingBox, "custom", false},
		{"nekobox direct", "custom", "direct", CoreXray, CoreSingBox, "custom", false},
		{"singbox vless", "upstream", "vless", CoreMihomo, CoreSingBox, "upstream", false},
		{"singbox http", "upstream", "http", CoreXray, CoreSingBox, "upstream", false},
		{"xray vless", "xray", "vless", CoreSingBox, CoreXray, "xray", false},
		{"xray aether", "xray", "aether", CoreMihomo, CoreXray, "xray", false},
		{"xray socks falls back", "xray", "socks", CoreXray, CoreSingBox, "custom", true},
		{"xray http falls back", "xray", "http", CoreSingBox, CoreSingBox, "custom", true},
		{"xray direct falls back", "xray", "direct", CoreMihomo, CoreSingBox, "custom", true},
		{"mihomo vless", "mihomo", "vless", CoreSingBox, CoreMihomo, "mihomo", false},
		{"mihomo socks", "mihomo", "socks", CoreXray, CoreMihomo, "mihomo", false},
		{"mihomo direct falls back", "mihomo", "direct", CoreMihomo, CoreSingBox, "custom", true},
		{"empty preference", "", "vless", CoreXray, CoreSingBox, "custom", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Profile{Name: tc.name, Core: tc.storedCore, Protocol: tc.protocol, Address: "example.com", Port: 443}
			choice, err := resolveProfileCore(p, Settings{CoreEngine: tc.engine})
			if err != nil {
				t.Fatalf("resolveProfileCore: %v", err)
			}
			if choice.Profile.Core != tc.wantCore || choice.Engine != tc.wantEngine || choice.Fallback != tc.wantFallback {
				t.Fatalf("choice = core %s, engine %q, fallback %v; want %s, %q, %v",
					choice.Profile.Core, choice.Engine, choice.Fallback, tc.wantCore, tc.wantEngine, tc.wantFallback)
			}
			if p.Core != tc.storedCore {
				t.Fatal("resolver mutated the stored profile")
			}
		})
	}
}

func TestCorePreferenceLeavesTunBehaviorUnchanged(t *testing.T) {
	for _, tc := range []struct {
		engine     string
		wantCore   CoreType
		wantEngine string
		wantTun    bool
	}{
		{"custom", CoreSingBox, "custom", true},
		{"upstream", CoreSingBox, "upstream", true},
		{"xray", CoreXray, "xray", false},
		{"mihomo", CoreMihomo, "mihomo", false},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			p := &Profile{Name: "tun", Core: CoreSingBox, Protocol: "vless", Address: "example.com", Port: 443}
			settings := Settings{CoreEngine: tc.engine, TunEnabled: true}
			choice, err := resolveProfileCore(p, settings)
			if err != nil {
				t.Fatalf("resolveProfileCore: %v", err)
			}
			if choice.Profile.Core != tc.wantCore || choice.Engine != tc.wantEngine || choice.Fallback {
				t.Fatalf("choice = core %s, engine %q, fallback %v; want %s, %q, false",
					choice.Profile.Core, choice.Engine, choice.Fallback, tc.wantCore, tc.wantEngine)
			}
			if got := tunModeFor(&choice.Profile, settings); got != tc.wantTun {
				t.Fatalf("tunModeFor = %v, want unchanged %v", got, tc.wantTun)
			}
			if settings.CoreEngine != tc.engine || p.Core != CoreSingBox {
				t.Fatal("resolver mutated the preference or stored profile")
			}
		})
	}
}

func TestCorePreferenceTunKeepsRawOutboundFormats(t *testing.T) {
	settings := Settings{CoreEngine: "xray", TunEnabled: true}
	for _, tc := range []struct {
		name       string
		core       CoreType
		raw        string
		wantCore   CoreType
		wantEngine string
	}{
		{"sing-box", CoreSingBox, `{"type":"vless"}`, CoreSingBox, "custom"},
		{"xray", CoreXray, `{"protocol":"vless"}`, CoreXray, "xray"},
		{"mihomo", CoreMihomo, `{"type":"vless"}`, CoreMihomo, "mihomo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Profile{Core: tc.core, Protocol: "vless", RawOutbound: tc.raw}
			choice, err := resolveProfileCore(p, settings)
			if err != nil {
				t.Fatalf("resolveProfileCore: %v", err)
			}
			if choice.Profile.Core != tc.wantCore || choice.Engine != tc.wantEngine {
				t.Fatalf("choice = core %s, engine %q; want %s, %q",
					choice.Profile.Core, choice.Engine, tc.wantCore, tc.wantEngine)
			}
		})
	}
}

func TestCorePreferencePreservesSpoofAndRawFormats(t *testing.T) {
	settings := Settings{CoreEngine: "xray"}
	spoof := &Profile{Core: CoreSpoof, Protocol: "vless", Address: "example.com", Port: 443}
	choice, err := resolveProfileCore(spoof, settings)
	if err != nil || choice.Profile.Core != CoreSpoof || choice.Engine != "xray" {
		t.Fatalf("enabled spoof choice = %+v, %v; want spoof with xray preference", choice, err)
	}
	off := settings
	disabled := false
	off.SpoofEnabled = &disabled
	choice, err = resolveProfileCore(spoof, off)
	if err != nil || choice.Profile.Core != CoreXray {
		t.Fatalf("disabled spoof choice = %+v, %v; want the global xray preference", choice, err)
	}

	for _, tc := range []struct {
		name       string
		engine     string
		core       CoreType
		raw        string
		wantCore   CoreType
		wantEngine string
		fallback   bool
	}{
		{"singbox raw on nekobox", "custom", CoreSingBox, `{"type":"vless"}`, CoreSingBox, "custom", false},
		{"singbox raw on singbox", "upstream", CoreSingBox, `{"type":"vless"}`, CoreSingBox, "upstream", false},
		{"singbox raw stays singbox", "xray", CoreSingBox, `{"type":"vless"}`, CoreSingBox, "custom", false},
		{"nekobox-only raw falls back", "upstream", CoreSingBox, `{"type":"mieru"}`, CoreSingBox, "custom", true},
		{"xray raw stays xray", "custom", CoreXray, `{"protocol":"vless"}`, CoreXray, "xray", false},
		{"mihomo raw stays mihomo", "upstream", CoreMihomo, `{"type":"vless"}`, CoreMihomo, "mihomo", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Profile{Core: tc.core, Protocol: "vless", RawOutbound: tc.raw}
			choice, err := resolveProfileCore(p, Settings{CoreEngine: tc.engine})
			if err != nil {
				t.Fatalf("resolveProfileCore: %v", err)
			}
			if choice.Profile.Core != tc.wantCore || choice.Engine != tc.wantEngine || choice.Fallback != tc.fallback {
				t.Fatalf("choice = core %s, engine %q, fallback %v; want %s, %q, %v",
					choice.Profile.Core, choice.Engine, choice.Fallback, tc.wantCore, tc.wantEngine, tc.fallback)
			}
		})
	}
}

func TestCorePreferenceDoesNotFallbackOnStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-core")
	logCh := make(chan LogEntry, 64)
	custom := NewSingBoxManager(missing, logCh)
	xray := NewXrayManager(missing, t.TempDir(), logCh)
	cores := NewCoreManager(custom, xray)
	settings := DefaultSettings()
	settings.CoreEngine = "xray"
	settings.XrayPath = missing
	settings.CorePath = missing
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	settings.MixedPort = port
	api, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	settings.XrayAPIAddress = "127.0.0.1:" + strconv.Itoa(api)
	err = cores.Start(portTestProfile(), settings)
	if err == nil || !strings.Contains(err.Error(), "xray not found") {
		t.Fatalf("Start with missing preferred Xray = %v, want the Xray error", err)
	}
	if custom.IsRunning() {
		t.Fatal("a runtime Xray failure silently started the fallback core")
	}
}

func TestPreferredCoreRuntimeDispatch(t *testing.T) {
	for _, tc := range []struct {
		engine   string
		wantCore CoreType
	}{
		{"xray", CoreXray},
		{"mihomo", CoreMihomo},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			logCh := make(chan LogEntry, 256)
			missing := filepath.Join(t.TempDir(), "missing-core")
			custom := NewSingBoxManager(missing, logCh)
			xray := NewXrayManager("", t.TempDir(), logCh)
			mihomo := NewMihomoManager(logCh)
			cores := NewCoreManager(custom, xray)
			cores.SetMihomoManager(mihomo)
			t.Cleanup(func() { _ = cores.Stop() })

			settings := DefaultSettings()
			settings.CoreEngine = tc.engine
			settings.CorePath = missing
			settings.UpstreamCorePath = missing
			settings.XrayPath = installFakeXray(t)
			settings.MihomoPath = installFakeMihomo(t)
			xray.setXrayPath(settings.XrayPath)
			port, err := freePort()
			if err != nil {
				t.Fatal(err)
			}
			settings.MixedPort = port
			api, err := distinctPort(port, port+1)
			if err != nil {
				t.Fatal(err)
			}
			settings.XrayAPIAddress = "127.0.0.1:" + strconv.Itoa(api)
			profile := &Profile{
				ID: "dispatch", Name: "dispatch", Core: CoreSingBox, Protocol: "vless",
				Address: "example.com", Port: 443, IDOrPassword: "pw",
			}
			if err := cores.Start(profile, settings); err != nil {
				t.Fatalf("Start: %v", err)
			}
			core, id, running := cores.Status()
			if !running || core != tc.wantCore.String() || id != profile.ID {
				t.Fatalf("Status = (%q, %q, %v), want (%q, %q, true)", core, id, running, tc.wantCore, profile.ID)
			}
			if xray.IsRunning() != (tc.engine == "xray") || mihomo.IsRunning() != (tc.engine == "mihomo") || custom.IsRunning() {
				t.Fatalf("running managers: custom=%v xray=%v mihomo=%v", custom.IsRunning(), xray.IsRunning(), mihomo.IsRunning())
			}
			if err := cores.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if xray.IsRunning() || mihomo.IsRunning() || custom.IsRunning() {
				t.Fatal("a selected manager survived Stop")
			}
		})
	}
}

func TestCompatibilityFallbackSelectsCustomBeforeStart(t *testing.T) {
	logCh := make(chan LogEntry, 64)
	missing := filepath.Join(t.TempDir(), "missing-core")
	custom := NewSingBoxManager(missing, logCh)
	xray := NewXrayManager(installFakeXray(t), t.TempDir(), logCh)
	cores := NewCoreManager(custom, xray)
	settings := DefaultSettings()
	settings.CoreEngine = "xray"
	settings.CorePath = missing
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	settings.MixedPort = port
	profile := &Profile{Name: "socks", Core: CoreXray, Protocol: "socks", Address: "example.com", Port: 1080}
	err = cores.Start(profile, settings)
	if err == nil || !strings.Contains(err.Error(), "core not found") {
		t.Fatalf("Start = %v, want the custom core's missing-binary error", err)
	}
	if xray.IsRunning() || custom.IsRunning() {
		t.Fatal("compatibility fallback spawned Xray or committed a dead custom core")
	}
}

func TestSaveSettingsAcceptsFourCoreEngines(t *testing.T) {
	app := newGeoTestApp(t)
	for _, engine := range []string{"custom", "upstream", "xray", "mihomo"} {
		settings := app.GetSettings()
		settings.CoreEngine = engine
		if err := app.SaveSettings(settings); err != nil {
			t.Errorf("SaveSettings(coreEngine=%q): %v", engine, err)
		}
		if got := app.GetSettings().CoreEngine; got != engine {
			t.Errorf("saved coreEngine = %q, want %q", got, engine)
		}
	}
	bad := app.GetSettings()
	bad.CoreEngine = "other"
	if err := app.SaveSettings(bad); err == nil {
		t.Fatal("SaveSettings accepted an unknown core engine")
	}
}

func TestConfigPreviewsFollowPreferredCore(t *testing.T) {
	for _, tc := range []struct {
		engine       string
		protocol     string
		wantOutbound string
		wantInbound  string
		wantMihomo   bool
	}{
		{"custom", "vless", `"type": "vless"`, `"type": "mixed"`, false},
		{"upstream", "vless", `"type": "vless"`, `"type": "mixed"`, false},
		{"xray", "vless", `"protocol": "vless"`, `"protocol": "http"`, false},
		{"mihomo", "vless", "", "", true},
		{"xray", "socks", `"type": "socks"`, `"type": "mixed"`, false},
	} {
		t.Run(tc.engine+"/"+tc.protocol, func(t *testing.T) {
			app := newGeoTestApp(t)
			if _, err := app.SaveProfile(Profile{
				Name: "preview", Core: CoreSingBox, Protocol: tc.protocol,
				Address: "example.com", Port: 443, IDOrPassword: "pw",
			}); err != nil {
				t.Fatal(err)
			}
			settings := app.GetSettings()
			settings.CoreEngine = tc.engine
			if err := app.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			raw, err := app.GetRawConfig()
			if tc.wantMihomo {
				if err == nil || !strings.Contains(err.Error(), mihomoConfigEditorMessage) {
					t.Fatalf("GetRawConfig = %v, want the mihomo editor message", err)
				}
			} else if err != nil {
				t.Fatalf("GetRawConfig: %v", err)
			} else if !strings.Contains(raw, tc.wantOutbound) {
				t.Fatalf("GetRawConfig missing %s: %s", tc.wantOutbound, raw)
			}
			section, err := app.GetCoreConfigSection("inbound")
			if tc.wantMihomo {
				if err == nil || !strings.Contains(err.Error(), mihomoConfigEditorMessage) {
					t.Fatalf("GetCoreConfigSection = %v, want the mihomo editor message", err)
				}
			} else if err != nil {
				t.Fatalf("GetCoreConfigSection: %v", err)
			} else if !strings.Contains(section, tc.wantInbound) {
				t.Fatalf("inbound section missing %s: %s", tc.wantInbound, section)
			}
		})
	}
}

func TestSpoofXrayConfigPreviewUsesXrayTransport(t *testing.T) {
	settings := DefaultSettings()
	settings.SpoofXray = true
	p := &Profile{Core: CoreSpoof, Protocol: "vless", Address: "example.com", Port: 443, IDOrPassword: "pw"}
	const spoofPort = 18080
	setSpoofFrontPort(spoofPort)
	t.Cleanup(func() { clearSpoofFrontPortIf(spoofPort) })

	raw, err := renderActiveConfig(p, settings)
	if err != nil {
		t.Fatalf("renderActiveConfig: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				VNext []struct {
					Address string `json:"address"`
					Port    int    `json:"port"`
				} `json:"vnext"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("preview is not JSON: %v", err)
	}
	for _, out := range cfg.Outbounds {
		if out.Protocol != "vless" {
			continue
		}
		if len(out.Settings.VNext) != 1 || out.Settings.VNext[0].Address != "127.0.0.1" || out.Settings.VNext[0].Port != spoofPort {
			t.Fatalf("spoof outbound = %+v, want the local spoof front", out.Settings.VNext)
		}
		return
	}
	t.Fatalf("preview has no Xray VLESS outbound: %s", raw)
}

func TestCoreEngineSelectorOffersFourChoices(t *testing.T) {
	src := readGuiFile(t, "frontend/src/components/SettingsPanel.vue")
	for _, option := range []string{
		`{ value: 'custom', label: 'NekoBox Core' }`,
		`{ value: 'upstream', label: 'sing-box' }`,
		`{ value: 'xray', label: 'Xray' }`,
		`{ value: 'mihomo', label: 'Mihomo' }`,
	} {
		if !strings.Contains(src, option) {
			t.Errorf("Core Engine selector is missing %s", option)
		}
	}
}
