package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAppLogosCannotBeDragged(t *testing.T) {
	imgTag := regexp.MustCompile(`(?s)<img\b[^>]*>`)
	found := 0
	err := filepath.WalkDir(filepath.Join("frontend", "src"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".vue" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, tag := range imgTag.FindAllString(string(raw), -1) {
			if !strings.Contains(tag, "appIcon") && !strings.Contains(tag, "aetherLogo") {
				continue
			}
			found++
			if !strings.Contains(tag, `draggable="false"`) {
				t.Errorf("%s renders a draggable app logo: %s", path, tag)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 7 {
		t.Fatalf("found %d app logo renders, want at least the seven known locations", found)
	}
}

func TestAetherNavLogoStaysVisibleInBothThemes(t *testing.T) {
	sidebar := readFrontendSrc(t, filepath.Join("components", "Sidebar.vue"))
	aetherImg := regexp.MustCompile(`(?s)<img\b[^>]*aetherLogo[^>]*>`).FindString(sidebar)
	if !strings.Contains(aetherImg, `class="nb-aether-logo"`) {
		t.Error("the Aether navigation logo has no theme-aware styling class")
	}

	css := withoutComments(readFrontendSrc(t, filepath.Join("styles", "theme.css")))
	light := cssRuleBody(t, css, ".nb-aether-logo")
	for _, want := range []string{"filter: brightness(0)", "object-fit: contain", "flex-shrink: 0"} {
		if !strings.Contains(light, want) {
			t.Errorf("the light-mode Aether logo rule does not declare %q", want)
		}
	}
	dark := cssRuleBody(t, css, ":root.dark .nb-aether-logo")
	if !strings.Contains(dark, "filter: none") {
		t.Error("the dark-mode Aether logo does not restore the original white artwork")
	}
}

func TestConsoleLogLimitDefaultsAndPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	got := store.GetSettings()
	if got.ConsoleLogLimit == nil || *got.ConsoleLogLimit != 300 {
		t.Fatalf("default ConsoleLogLimit = %v, want 300", got.ConsoleLogLimit)
	}
	*got.ConsoleLogLimit = 999
	if again := store.GetSettings(); again.ConsoleLogLimit == nil || *again.ConsoleLogLimit != 300 {
		t.Fatal("GetSettings returned the store's ConsoleLogLimit pointer")
	}

	for _, limit := range []int{0, 100, 750, 2000} {
		got = store.GetSettings()
		value := limit
		got.ConsoleLogLimit = &value
		if err := app.SaveSettings(got); err != nil {
			t.Fatalf("SaveSettings(limit=%d): %v", limit, err)
		}
		saved := store.GetSettings()
		if saved.ConsoleLogLimit == nil || *saved.ConsoleLogLimit != limit {
			t.Fatalf("saved ConsoleLogLimit = %v, want %d", saved.ConsoleLogLimit, limit)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var disk map[string]any
		if err := json.Unmarshal(raw, &disk); err != nil {
			t.Fatal(err)
		}
		settings, _ := disk["settings"].(map[string]any)
		if settings["consoleLogLimit"] != float64(limit) {
			t.Fatalf("config.json consoleLogLimit = %v, want %d", settings["consoleLogLimit"], limit)
		}
	}

	got = store.GetSettings()
	negative := -1
	got.ConsoleLogLimit = &negative
	if err := app.SaveSettings(got); err == nil {
		t.Fatal("SaveSettings accepted a negative Console log limit")
	}
	if saved := store.GetSettings(); saved.ConsoleLogLimit == nil || *saved.ConsoleLogLimit != 2000 {
		t.Fatal("a rejected limit changed the stored setting")
	}
}

func TestLegacyConfigGetsDefaultConsoleLogLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(store.GetSettings()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	settings, _ := disk["settings"].(map[string]any)
	delete(settings, "consoleLogLimit")
	raw, err = json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.GetSettings().ConsoleLogLimit; got == nil || *got != 300 {
		t.Fatalf("legacy ConsoleLogLimit = %v, want 300", got)
	}
}

func TestConsoleLimitControlUsesPersistedSetting(t *testing.T) {
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))
	viewer := readFrontendSrc(t, filepath.Join("components", "LogViewer.vue"))
	settings := readFrontendSrc(t, filepath.Join("stores", "settings.ts"))
	if !strings.Contains(store, "consoleLogLimit ?? 300") {
		t.Error("the Console poller does not use the persisted limit with a legacy-safe default")
	}
	if strings.Contains(store, "backend.recentLogs(300)") {
		t.Error("the Console poller still fetches a hard-coded 300 lines")
	}
	if strings.Contains(store, "consoleLogLimit || 300") {
		t.Error("the Console poller treats zero (No limit) as missing")
	}
	for _, want := range []string{"consoleLogLimit", "No limit", "setConsoleLogLimit", "refreshLogs(true)"} {
		if !strings.Contains(viewer, want) {
			t.Errorf("LogViewer.vue has no %q control wiring", want)
		}
	}
	if !strings.Contains(settings, "async function setConsoleLogLimit") || !strings.Contains(settings, "await save()") {
		t.Error("the settings store has no persisted Console-limit action")
	}
}

func TestScrollingCenterDoesNotCollapsePageSections(t *testing.T) {
	css := withoutComments(readFrontendSrc(t, filepath.Join("styles", "theme.css")))
	center := cssRuleBody(t, css, ".nb-center")
	if !strings.Contains(center, "overflow-x: hidden") {
		t.Error(".nb-center still computes overflow-x:auto from overflow-y:auto, allowing page-wide horizontal scrolling")
	}
	children := cssRuleBody(t, css, ".nb-center > *")
	for _, want := range []string{"flex: 0 0 auto", "min-width: 0"} {
		if !strings.Contains(children, want) {
			t.Errorf(".nb-center > %s does not declare %q", "*", want)
		}
	}
}

func TestConsoleContentKeepsNaturalHeight(t *testing.T) {
	css := withoutComments(readFrontendSrc(t, filepath.Join("styles", "theme.css")))
	for _, sel := range []string{".console-fill", ".console-fill > .panel"} {
		body := cssRuleBody(t, css, sel)
		if !strings.Contains(body, "flex: 0 0 auto") {
			t.Errorf("%s can shrink inside the scrolling center and clip the active Console view", sel)
		}
	}
	logs := cssRuleBody(t, css, ".console-fill > .panel.lv")
	if !regexp.MustCompile(`height:\s*clamp\(`).MatchString(logs) {
		t.Error("the Logs panel has no bounded height, so its log list cannot own a stable scroll area")
	}
}

func TestRoutingRulesContainsItsOwnWideContent(t *testing.T) {
	src := readFrontendSrc(t, filepath.Join("components", "RoutingRules.vue"))
	if !strings.Contains(src, `class="panel routing-rules"`) {
		t.Error("RoutingRules has no scoped layout class")
	}
	if !strings.Contains(src, `class="routing-rules-scroll"`) {
		t.Error("the connections table has no local horizontal scroll container")
	}
	css := withoutComments(readFrontendSrc(t, filepath.Join("styles", "theme.css")))
	root := cssRuleBody(t, css, ".routing-rules")
	if !strings.Contains(root, "overflow: hidden") {
		t.Error("RoutingRules can still establish page-wide horizontal overflow")
	}
	code := cssRuleBody(t, css, ".routing-rules code")
	if !strings.Contains(code, "overflow-wrap: anywhere") {
		t.Error("long config tokens cannot wrap inside the Routing Rules card")
	}
	scroll := cssRuleBody(t, css, ".routing-rules-scroll")
	for _, want := range []string{"max-width: 100%", "overflow-x: auto"} {
		if !strings.Contains(scroll, want) {
			t.Errorf(".routing-rules-scroll does not declare %q", want)
		}
	}
}

func TestProfileCoreControlMatchesBackendResolution(t *testing.T) {
	form := withoutComments(readFrontendSrc(t, filepath.Join("components", "ProfileForm.vue")))
	if !strings.Contains(form, `v-if="model.rawOutbound?.trim()"`) {
		t.Error("ordinary profiles still offer a per-profile Core selector even though Connect uses the global preference")
	}
	if !strings.Contains(form, "Uses Settings → Connection → Core engine") {
		t.Error("the profile form does not say where an ordinary profile's engine is selected")
	}
	table := withoutComments(readFrontendSrc(t, filepath.Join("components", "ProxiesTable.vue")))
	if !strings.Contains(table, "const spoof = p.core === 2") {
		t.Error("the meaningful SNI Spoofing badge was removed")
	}
	if !strings.Contains(table, "const mihomo = !!p.rawOutbound?.trim() && p.core === 3") {
		t.Error("the mihomo badge still trusts a stale per-profile Core on ordinary profiles")
	}
}

func TestRawOutboundCoreChoicesExcludeSpoof(t *testing.T) {
	form := withoutComments(readFrontendSrc(t, filepath.Join("components", "ProfileForm.vue")))
	options := regexp.MustCompile(`(?s)const coreOptions = \[(.*?)\]`).FindStringSubmatch(form)
	if len(options) != 2 {
		t.Fatal("ProfileForm has no raw-outbound coreOptions list")
	}
	values := regexp.MustCompile(`value: '([^']+)'`).FindAllStringSubmatch(options[1], -1)
	var got []string
	for _, value := range values {
		got = append(got, value[1])
	}
	if joined := strings.Join(got, ","); joined != "0,1,3" {
		t.Fatalf("raw-outbound core choices = %q, want only sing-box, Xray and Mihomo", joined)
	}
}

func TestConfigWorkspaceMihomoRequiresRawOutbound(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("components", "adv", "ConfigWorkspace.vue")))
	if !strings.Contains(src, "const mihomoProfile = computed(() => !!activeProfile.value?.rawOutbound?.trim() && activeProfile.value?.core === 3)") {
		t.Error("ConfigWorkspace still treats an ordinary stale core=3 profile as Mihomo")
	}
	if !strings.Contains(src, "p.core === 3 && p.rawOutbound?.trim() ? 'Mihomo'") {
		t.Error("ConfigWorkspace still labels an ordinary stale core=3 profile as Mihomo")
	}
}

func TestSettingsSavesSerializeAndRollbackOnlyOwnedFields(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("stores", "settings.ts")))
	for _, want := range []string{"let saveQueue", "saveQueue.then", "snapshot(settings.value)", "claimFields(", "fieldsUnchanged("} {
		if !strings.Contains(src, want) {
			t.Errorf("settings store lacks overlapping-save protection %q", want)
		}
	}
}

func TestSettingsRollbackUsesLastPersistedSnapshot(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("stores", "settings.ts")))
	for _, want := range []string{"let persistedSettings", "persistedSettings = snapshot(loadedSettings)", "persistedSettings = next", "function restoreField", "persistedSettings[field]"} {
		if !strings.Contains(src, want) {
			t.Errorf("settings store does not restore rejected fields from the last persisted snapshot: missing %q", want)
		}
	}
}

func TestSettingsProfileSelectionStaysLocalWithoutBackend(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("stores", "settings.ts")))
	start := strings.Index(src, "async function setActiveProfileId")
	if start < 0 {
		t.Fatal("settings store has no setActiveProfileId action")
	}
	rest := src[start:]
	end := strings.Index(rest, "async function setAdvancedLastTab")
	if end < 0 {
		t.Fatal("settings store has no action boundary after setActiveProfileId")
	}
	body := rest[:end]
	guard := strings.Index(body, "if (!backend.available()) return")
	if guard < 0 || strings.Index(body, "await save()") < guard {
		t.Error("offline profile selection still calls the unavailable SaveSettings bridge")
	}
}

func TestSettingsActionsRollbackRejectedPersistence(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("stores", "settings.ts")))
	section := func(start, end string) string {
		t.Helper()
		i := strings.Index(src, start)
		if i < 0 {
			t.Fatalf("settings.ts has no %s", start)
		}
		rest := src[i:]
		if end != "" {
			j := strings.Index(rest, end)
			if j < 0 {
				t.Fatalf("settings.ts has no %s after %s", end, start)
			}
			rest = rest[:j]
		}
		return rest
	}
	cases := []struct {
		name     string
		body     string
		rollback []string
	}{
		{"setTheme", section("async function setTheme", "async function setActiveProfileId"), []string{"restoreField('theme')", "syncThemeClass(settings.value.theme)"}},
		{"setAdvancedLastTab", section("async function setAdvancedLastTab", "async function setAdvancedMode"), []string{"restoreField('advancedLastTab')"}},
		{"setAdvancedMode", section("async function setAdvancedMode", "async function setAdvancedPeekSimple"), []string{"restoreField('advancedMode')", "restoreField('advancedPeekSimple')"}},
		{"setAdvancedPeekSimple", section("async function setAdvancedPeekSimple", "return {"), []string{"restoreField('advancedPeekSimple')"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range append([]string{"if (backend.available())", "throw e"}, tc.rollback...) {
				if !strings.Contains(tc.body, want) {
					t.Errorf("%s does not roll back and propagate a backend rejection: missing %q", tc.name, want)
				}
			}
		})
	}
}

func TestSettingsActionCallersSurfaceRejections(t *testing.T) {
	sidebar := withoutComments(readFrontendSrc(t, filepath.Join("components", "Sidebar.vue")))
	for _, action := range []string{"toggleTheme", "toggleMode"} {
		start := strings.Index(sidebar, "async function "+action)
		if start < 0 {
			t.Errorf("Sidebar has no awaited %s", action)
			continue
		}
		rest := sidebar[start:]
		end := strings.Index(rest, "\n}")
		if end < 0 {
			t.Errorf("Sidebar %s has no function end", action)
			continue
		}
		body := rest[:end]
		if !strings.Contains(body, "catch (e)") || !strings.Contains(body, "emit('settings-error', String(e))") {
			t.Errorf("Sidebar %s leaves a rejected settings save unreported", action)
		}
	}
	if !strings.Contains(sidebar, "(e: 'settings-error', message: string): void") {
		t.Error("Sidebar cannot pass a settings failure to the shell")
	}

	theme := withoutComments(readFrontendSrc(t, filepath.Join("components", "ThemeSelect.vue")))
	chooseStart := strings.Index(theme, "async function choose")
	chooseEnd := strings.Index(theme[chooseStart:], "\n}")
	choose := theme[chooseStart : chooseStart+chooseEnd]
	if !strings.Contains(choose, "catch (e)") || !strings.Contains(choose, "error.value = String(e)") {
		t.Error("ThemeSelect does not surface a rejected theme save")
	}
	if !strings.Contains(theme, `v-if="error"`) || !strings.Contains(theme, `role="alert"`) {
		t.Error("ThemeSelect has no visible settings-error message")
	}

	panel := withoutComments(readFrontendSrc(t, filepath.Join("components", "SettingsPanel.vue")))
	setStrStart := strings.Index(panel, "async function setStr")
	setStrEnd := strings.Index(panel[setStrStart:], "async function setNum")
	setStr := panel[setStrStart : setStrStart+setStrEnd]
	if !strings.Contains(setStr, "await store.setTheme") || !strings.Contains(setStr, "catch (e)") || !strings.Contains(setStr, "saveErr.value = String(e)") {
		t.Error("SettingsPanel does not report a rejected theme save in its existing error row")
	}

	shell := withoutComments(readFrontendSrc(t, filepath.Join("components", "SimpleShell.vue")))
	if !strings.Contains(shell, `@settings-error="settingsError = $event"`) || !strings.Contains(shell, `v-if="settingsError"`) {
		t.Error("the shell has no visible destination for sidebar settings failures")
	}
	exitStart := strings.Index(shell, "async function onExitRequest")
	exitEnd := strings.Index(shell[exitStart:], "\n}")
	exit := shell[exitStart : exitStart+exitEnd]
	if !strings.Contains(exit, "await settings.setAdvancedMode(false)") || !strings.Contains(exit, "catch (e)") || !strings.Contains(exit, "settingsError.value = String(e)") {
		t.Error("leaving Advanced mode ignores a rejected settings save")
	}
}
