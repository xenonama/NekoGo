package main

// The Advanced workspace is frontend-only, so its contract is pinned the
// way settings_panel_test.go pins the Settings tab: read the source and
// assert what the user actually gets. Behaviour a source check cannot
// reach — which dialog opens, what ESC does — is driven in the browser
// through the same stores the components read.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// advancedSrc reads one component of the expert workspace.
func advancedSrc(t *testing.T, rel string) string {
	t.Helper()
	return readFrontendSrc(t, rel)
}

// withoutComments strips HTML comments, block comments and whole-line
// // comments, so a sentence explaining why a native box was removed
// cannot be mistaken for a call to one.
func withoutComments(src string) string {
	src = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(src, "")
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var keep []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// TestAdvancedWorkspaceAsksInAppChrome: every question the expert
// workspace asks went through WebView2's native confirm()/prompt() — a
// browser box that matches no part of the app, cannot be themed, ignores
// the app's keyboard shortcuts, and (for prompt) can be refused outright
// by the host, which turns "New group…" into a button that does nothing.
// The app already owns a themed dialog (ConfirmPrompt + the confirm
// store) and ProxiesTable already owns a themed group editor.
func TestAdvancedWorkspaceAsksInAppChrome(t *testing.T) {
	files := []string{
		filepath.Join("components", "SimpleShell.vue"),
		filepath.Join("components", "CoreConfigEditor.vue"),
		filepath.Join("components", "JsonEditorModal.vue"),
		filepath.Join("components", "adv", "ConfigWorkspace.vue"),
		filepath.Join("components", "adv", "ProfileToolsPanel.vue"),
		filepath.Join("components", "adv", "NetworkTools.vue"),
		filepath.Join("components", "adv", "SystemTools.vue"),
		filepath.Join("components", "adv", "BackupWorkspace.vue"),
		filepath.Join("components", "BackupPanel.vue"),
	}
	call := regexp.MustCompile(`\b(?:window\.)?(?:confirm|prompt|alert)\s*\(`)
	for _, rel := range files {
		src := withoutComments(advancedSrc(t, rel))
		if m := call.FindString(src); m != "" {
			t.Errorf("%s still asks through a native browser box: %s", rel, m)
		}
	}
}

// TestAdvancedOverrideSizeMeasuresJSON: the "Active overrides" card told
// the user how much config was stored. coreOverrides arrives as parsed
// JSON, so String(v) on an object is "[object Object]" — every section
// override reported the same 15 chars, and an array reported the length
// of its elements joined by commas. The number has to come from the JSON
// text, which is also what the editor loads and what the core is given.
func TestAdvancedOverrideSizeMeasuresJSON(t *testing.T) {
	cfg := withoutComments(advancedSrc(t, filepath.Join("components", "adv", "ConfigWorkspace.vue")))
	if strings.Contains(cfg, "String(v).length") {
		t.Error("the stored-override size is measured with String(value), which is [object Object] for every object")
	}
	if !strings.Contains(cfg, "JSON.stringify") {
		t.Error("the stored-override size does not measure the JSON text")
	}
	// The raw editor's one-time warning keys off the same values: an
	// empty {} override is not a section override worth warning about.
	ed := withoutComments(advancedSrc(t, filepath.Join("components", "CoreConfigEditor.vue")))
	if strings.Contains(ed, "String(v)") {
		t.Error("the raw-config override warning tests String(value) instead of the stored JSON")
	}
}

// TestAdvancedConfigTargetFollowsTheBackend: Go resolves the config
// target as running profile -> activeProfileId -> first profile
// (app.go resolveConfigTarget). The card resolved active -> first and
// the editor resolved running -> active, so with a mihomo core running
// and a sing-box profile selected the card offered editors the backend
// refuses, and the head named a profile the backend was not rendering.
// One rule, shared, in the backend's order.
func TestAdvancedConfigTargetFollowsTheBackend(t *testing.T) {
	helper := advancedSrc(t, "configTarget.ts")
	order := []string{"profileId", "activeProfileId", "[0]"}
	at := 0
	for _, want := range order {
		i := strings.Index(helper[at:], want)
		if i < 0 {
			t.Fatalf("configTarget.ts does not resolve %q after the earlier steps (want running -> active -> first)", want)
		}
		at += i + len(want)
	}
	for _, rel := range []string{
		filepath.Join("components", "adv", "ConfigWorkspace.vue"),
		filepath.Join("components", "CoreConfigEditor.vue"),
	} {
		src := advancedSrc(t, rel)
		if !strings.Contains(src, "configTarget") {
			t.Errorf("%s resolves the config target on its own instead of sharing the backend's rule", rel)
		}
	}
}

// TestJsonEditorEscCancelsTheDiscardPrompt: ConfirmPrompt and the JSON
// editor both listen for Escape on window, and ConfirmPrompt registers
// first (it is mounted with the app). One ESC therefore closed the
// confirm — clearing its request synchronously, resolving the ask in a
// microtask — and then reached the editor, which saw a dirty draft and no
// open request and asked again. The prompt reopened on the same keypress,
// so ESC could never dismiss it. A close request must be ignored while
// the editor's own ask is still pending.
func TestJsonEditorEscCancelsTheDiscardPrompt(t *testing.T) {
	src := withoutComments(advancedSrc(t, filepath.Join("components", "JsonEditorModal.vue")))
	fn := regexp.MustCompile(`(?s)async function requestClose\(\).*?\n\}`).FindString(src)
	if fn == "" {
		t.Fatal("JsonEditorModal has no async requestClose")
	}
	if !regexp.MustCompile(`if \(askingDiscard\.value\) return`).MatchString(fn) {
		t.Error("requestClose does not ignore a second close request while its discard ask is pending")
	}
	set := strings.Index(fn, "askingDiscard.value = true")
	ask := strings.Index(fn, "confirmBox.ask(")
	if set < 0 || ask < 0 || set > ask {
		t.Error("the pending-ask flag is not raised before the discard ask")
	}
	if !regexp.MustCompile(`(?s)finally \{\s*askingDiscard\.value = false`).MatchString(fn) {
		t.Error("the pending-ask flag is not cleared in a finally, so one answer would lock the editor")
	}
}

// TestAdvancedModeSwitchNamesItsMode: the footer control is a switch
// (role="switch", aria-checked = advanced on), but its label was the
// DESTINATION mode — "Simple Mode" while Advanced was on. A screen
// reader announced "Simple Mode, switch, on", the opposite of the state,
// and the visible text disagreed with the accessible name.
func TestAdvancedModeSwitchNamesItsMode(t *testing.T) {
	src := advancedSrc(t, filepath.Join("components", "Sidebar.vue"))
	block := regexp.MustCompile(`(?s)<button\s+class="nb-side-advrow".*?</button>`).FindString(src)
	if block == "" {
		t.Fatal("the sidebar has no mode switch")
	}
	if !strings.Contains(block, `role="switch"`) || !strings.Contains(block, `:aria-checked="isAdvancedMode"`) {
		t.Fatalf("the mode control is no longer a switch reporting the advanced state: %s", block)
	}
	if strings.Contains(block, `:aria-label="modeLabel"`) {
		t.Error("the mode switch is named after the mode it would switch to, not the mode it controls")
	}
	if !regexp.MustCompile(`aria-label="Advanced [Mm]ode"`).MatchString(block) {
		t.Errorf("the mode switch does not name Advanced mode: %s", block)
	}
	// The visible label has to carry the same words (label-in-name), so
	// the destination belongs in the tooltip, not in the switch's name.
	if !strings.Contains(src, `const modeLabel = 'Advanced Mode'`) &&
		!strings.Contains(src, `const modeLabel = "Advanced Mode"`) {
		t.Error("the visible mode label still changes with the mode instead of naming it")
	}
}
