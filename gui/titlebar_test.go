package main

// The caption bar is CSS the frontend ships as text, so its contract is
// pinned the way the other GUI contracts here are: read the stylesheet and
// assert the rule that keeps the buttons inside their 34px bar.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// cssRuleBody returns the declaration block of the rule whose selector is
// exactly sel. Comments go first, so a note that mentions the selector
// cannot be mistaken for the rule.
func cssRuleBody(t *testing.T, css, sel string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^([^{}/]*)\{([^{}]*)\}`)
	for _, m := range re.FindAllStringSubmatch(css, -1) {
		if strings.TrimSpace(m[1]) == sel {
			return m[2]
		}
	}
	t.Fatalf("theme.css declares no %s rule", sel)
	return ""
}

// TestCaptionButtonsFitTheTitleBar: the global button rule asks every
// button on screen for a 36px touch target, and min-height beats the
// caption rule's height:100% — so each of the three caption buttons stood
// 36px in a 34px bar. Its hover block painted 2px past the caption into the
// page below, and the close button's red reached over the content edge,
// which is the "the close button's GUI has a bug" report. The reset has to
// be in the .nb-tb-btn rule itself: the global rule is what the rest of the
// app sizes against and taking min-height out of it would shrink every
// other button by the 2px the caption cannot afford.
//
// button:active's translateY(1px) ride-along is pinned in the same place:
// a caption button that dips when pressed is not the Windows behaviour the
// bar is imitating, and the press already has its own darker background.
func TestCaptionButtonsFitTheTitleBar(t *testing.T) {
	base := withoutComments(readFrontendSrc(t, filepath.Join("styles", "v2rayn-like.css")))
	if regexp.MustCompile(`(?m)^button\s*\{[^}]*min-height:\s*36px`).FindString(base) == "" {
		t.Fatal("v2rayn-like.css no longer asks for a 36px button box; this test pins the caption reset against that rule")
	}
	if !strings.Contains(base, "button:active { transform: translateY(1px)") {
		t.Fatal("v2rayn-like.css no longer shifts a pressed button; the caption reset below is pinned against that rule")
	}

	theme := withoutComments(readFrontendSrc(t, filepath.Join("styles", "theme.css")))
	btn := cssRuleBody(t, theme, ".nb-tb-btn")
	if regexp.MustCompile(`min-height:\s*0`).FindString(btn) == "" {
		t.Error(".nb-tb-btn keeps the global 36px min-height, so a caption button is taller than the 34px bar")
	}

	active := cssRuleBody(t, theme, ".nb-tb-btn:active")
	for _, decl := range []string{"transform: none", "filter: none"} {
		if !strings.Contains(active, decl) {
			t.Errorf(".nb-tb-btn:active does not reset %s, so pressing a caption button moves or dims it", decl)
		}
	}
}
