package main

// The Settings tab is shared by both modes; Advanced appends the Backup
// and Core config workspaces under it. These pin the defects that only
// appear once those cards are on screen, so Simple mode never sees them.
// Source contracts here, behaviour driven in the browser.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAdvancedBackupBarsAreNotSticky: the Proxies batch bar is sticky so
// bulk actions stay reachable at the end of a long list. Its rule was
// written as a bare .nb-px-bar, a class BackupPanel reuses for three bars
// that only exist in Advanced Settings. Those bars inherited
// position:sticky + bottom:0 (and lost 8px of bottom padding), so while
// the page scrolled they were pulled up over their own card's head —
// measured 32px of overlap on the Import card.
func TestAdvancedBackupBarsAreNotSticky(t *testing.T) {
	css := withoutComments(readFrontendSrc(t, filepath.Join("styles", "theme.css")))
	rule := regexp.MustCompile(`([^{}]*\.nb-px-bar[^{}]*)\{([^{}]*)\}`)
	found := false
	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		sel, body := strings.TrimSpace(m[1]), m[2]
		if !strings.Contains(body, "position: sticky") {
			continue
		}
		found = true
		if sel == ".nb-px-bar" {
			t.Errorf("the sticky rule is unscoped (%q), so every Backup bar inherits it", sel)
		}
	}
	if !found {
		t.Error("the Proxies batch bar lost its sticky rule instead of scoping it")
	}
	px := readFrontendSrc(t, filepath.Join("components", "ProxiesTable.vue"))
	if !strings.Contains(px, "nb-px-bar--sticky") {
		t.Error("ProxiesTable does not carry the sticky modifier the rule now requires")
	}
	backup := readFrontendSrc(t, filepath.Join("components", "BackupPanel.vue"))
	if strings.Contains(backup, "nb-px-bar--sticky") {
		t.Error("a Backup bar carries the Proxies sticky modifier")
	}
}

// TestAdvancedBackupReportsInsideItsOwnCard: one toast sat after the last
// card. Export is the first card on a ~1900px page, so its result rendered
// 510px below the button and outside the viewport: pressing "Export to
// file" or "Show raw config" looked like nothing happened, success or
// failure alike. Each card reports its own result.
func TestAdvancedBackupReportsInsideItsOwnCard(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("components", "BackupPanel.vue")))
	if regexp.MustCompile(`(?s)<div v-if="toast" class="muted">\{\{ toast \}\}</div>\s*</div>\s*</template>`).MatchString(src) {
		t.Error("the only result message still sits after the last card, off-screen from the button that produced it")
	}
	for _, card := range []string{"export", "import", "raw", "reset"} {
		if !strings.Contains(src, "toastCard === '"+card+"'") {
			t.Errorf("the %s card shows no result of its own", card)
		}
	}
}

// TestAdvancedRawViewerFollowsTheConfigTarget: the card promises the exact
// config the active core receives, overrides merged in, but it fetched
// once and never again. Changing an override in the Core config card
// below, or selecting another profile, left the old document on screen
// under that promise while the Active overrides card already reported the
// new one. It follows the same target rule as the editors.
func TestAdvancedRawViewerFollowsTheConfigTarget(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("components", "BackupPanel.vue")))
	if !strings.Contains(src, "configTargetProfile") {
		t.Fatal("the raw viewer does not resolve the config target the backend renders")
	}
	watch := regexp.MustCompile(`(?s)watch\(\s*\[.*?\]\s*,.*?\n\}?\)`).FindString(src)
	if watch == "" {
		t.Fatal("the raw viewer never watches for a target or override change")
	}
	if !strings.Contains(watch, "coreOverrides") {
		t.Error("the raw viewer ignores a stored-override change")
	}
	if !strings.Contains(watch, "configTarget") && !strings.Contains(watch, "targetId") {
		t.Error("the raw viewer ignores a config-target change")
	}
}

// TestAdvancedBackupGuardsDestructiveActions: "Replace current data" runs
// ImportAllData, which restoreAll's settings and profiles wholesale, and
// asked nothing — while the Reset card beside it demands a typed
// RESET-ALL. Disarming that card also kept the typed token, so re-arming
// it later left "Confirm reset" already enabled: the two-step guard
// silently became one step.
func TestAdvancedBackupGuardsDestructiveActions(t *testing.T) {
	src := withoutComments(readFrontendSrc(t, filepath.Join("components", "BackupPanel.vue")))
	imp := regexp.MustCompile(`(?s)async function doImport\(\).*?\n\}`).FindString(src)
	if imp == "" {
		t.Fatal("BackupPanel has no doImport")
	}
	ask := strings.Index(imp, "confirmBox.ask(")
	call := strings.Index(imp, "backend.importAllData(")
	if ask < 0 {
		t.Error("replacing every profile and setting asks for no confirmation")
	} else if call >= 0 && ask > call {
		t.Error("the import confirmation comes after the data is already replaced")
	}
	toggle := regexp.MustCompile(`(?s)function toggleReset\(\).*?\n\}`).FindString(src)
	if toggle == "" {
		t.Fatal("the reset card arms and disarms without a handler")
	}
	if !strings.Contains(toggle, "resetToken.value = ''") {
		t.Error("disarming the reset card keeps the typed RESET-ALL, so re-arming skips the guard")
	}
}
