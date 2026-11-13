package main

// The About "Version stats" card is frontend-only, so its wiring is
// pinned here the way TestBuildMetadataVersionMatchesVERSIONFile pins
// the frontend manifests: read the source, assert the contract.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readFrontendSrc(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("frontend", "src", rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(raw)
}

// TestAboutSingBoxRowReadsTheOfficialCore: two sing-box binaries ship,
// and only one of them reports a sing-box version. build.bat stamps the
// NekoBox Thrift fork's sing-box/constant.Version with the NekoGo build
// version so the updater can compare it against NekoGo release tags, so
// `nekobox_core sing-box version` prints "sing-box version 1.0.0-beta.3"
// — NekoGo's own version. The card fed that line to a row labelled
// "sing-box core", telling users their sing-box was the GUI version.
// The official sing-box.exe is the only binary whose version line means
// sing-box, and it already has its own query (UpstreamSingBoxVersion).
func TestAboutSingBoxRowReadsTheOfficialCore(t *testing.T) {
	diag := readFrontendSrc(t, "diag.ts")

	// collectVersions runs the queries in parallel and destructures them
	// positionally, so the name bound to each backend call is the only
	// thing that decides which binary a field reports.
	list := regexp.MustCompile(`const \[([^\]]+)\] = await Promise\.all\(\[([^\]]*)\]`).FindStringSubmatch(diag)
	if list == nil {
		t.Fatal("collectVersions no longer destructures a Promise.all; this test must be updated with it")
	}
	names := strings.Split(list[1], ",")
	calls := regexp.MustCompile(`backend\.(\w+)\(\)`).FindAllStringSubmatch(list[2], -1)
	if len(names) != len(calls) {
		t.Fatalf("Promise.all binds %d names to %d backend calls", len(names), len(calls))
	}
	bound := map[string]string{}
	for i, n := range names {
		bound[strings.TrimSpace(n)] = calls[i][1]
	}

	for field, want := range map[string]string{
		"singBox": "upstreamSingBoxVersion",
		"nekoBox": "coreVersion",
	} {
		m := regexp.MustCompile(field + `:\s*(\w+)\s*\|\|`).FindStringSubmatch(diag)
		if m == nil {
			t.Fatalf("DiagVersions.%s is no longer assigned in collectVersions", field)
		}
		if got := bound[m[1]]; got != want {
			t.Errorf("%s reports backend.%s(), want backend.%s()", field, got, want)
		}
	}

	about := readFrontendSrc(t, filepath.Join("components", "About.vue"))
	if regexp.MustCompile(`versionStatRows\(\s*[\w.]+\s*,\s*[\w.]*advancedMode\s*\)`).FindString(about) == "" {
		t.Error("About builds its rows without telling versionStatRows the mode")
	}
	for label, field := range map[string]string{
		"sing-box core":   "singBox",
		"NekoBox version": "nekoBox",
	} {
		row := regexp.MustCompile(`\{\s*label:\s*'` + regexp.QuoteMeta(label) + `',\s*value:\s*v\.(\w+)\s*\}`)
		m := row.FindStringSubmatch(diag)
		if m == nil {
			t.Errorf("versionStatRows has no %q row", label)
			continue
		}
		if m[1] != field {
			t.Errorf("row %q reads v.%s, want v.%s", label, m[1], field)
		}
	}
}

// TestVersionStatsDropsTheGoRuntime: the GUI's Go toolchain is not a
// version the user can act on — every core reports its own — and the
// row was asked to go. It must not come back through the copied
// diagnostics either: that text is the same facts, so a Go line there
// would keep publishing what the card no longer shows.
func TestVersionStatsDropsTheGoRuntime(t *testing.T) {
	for _, rel := range []string{"diag.ts", filepath.Join("components", "About.vue")} {
		src := readFrontendSrc(t, rel)
		if strings.Contains(src, "Go runtime") {
			t.Errorf("%s still renders a Go runtime row", rel)
		}
		if strings.Contains(src, "v.go") || strings.Contains(src, "info['go']") {
			t.Errorf("%s still reads the Go toolchain version", rel)
		}
	}
	if _, ok := (&App{}).RuntimeInfo()["go"]; ok {
		t.Error("RuntimeInfo still reports the Go toolchain version nothing displays")
	}
}

// TestVersionStatRowsGateEngineFactsToAdvancedMode: Simple mode chooses
// no core, installs no second engine and shows no core path, so the four
// binary versions and the idle "Engine" read-out described a screen its
// user cannot reach — they showed anyway, because the card rendered
// every field the collector returns. Placement inside the `if (advanced)`
// branch is what gates a row, so that is what gets checked, braces
// balanced: reordering the list must not quietly move a row into or out
// of Simple mode.
func TestVersionStatRowsGateEngineFactsToAdvancedMode(t *testing.T) {
	gated, always := versionStatRowPlacement(t, readFrontendSrc(t, "diag.ts"))

	if want := []string{"sing-box core", "NekoBox version", "Xray core", "mihomo core", "Engine"}; !sameLabels(gated, want) {
		t.Errorf("advanced-only rows = %v, want %v", gated, want)
	}
	if want := []string{"NekoGo", "OS"}; !sameLabels(always, want) {
		t.Errorf("rows Simple mode shows = %v, want %v", always, want)
	}
}

// TestVersionStatCopyKeepsEveryFact: hiding a row is a display decision,
// not a fact the user should lose when they paste diagnostics into a bug
// report. So the copy path stays ungated — and it is the same rename
// that took "NekoBox core" out of the card, which is why both live here.
func TestVersionStatCopyKeepsEveryFact(t *testing.T) {
	diag := readFrontendSrc(t, "diag.ts")

	for _, rel := range []string{"diag.ts", filepath.Join("components", "About.vue")} {
		if strings.Contains(readFrontendSrc(t, rel), "NekoBox core") {
			t.Errorf("%s still says \"NekoBox core\": the row reports a version, not a binary path", rel)
		}
	}

	body := regexp.MustCompile(`(?s)export function formatDiagText.*?\n\}`).FindString(diag)
	if body == "" {
		t.Fatal("diag.ts no longer exports formatDiagText")
	}
	for _, line := range []string{
		"NekoBox version ${v.nekoBox}", "sing-box ${v.singBox}",
		"Xray ${v.xray}", "mihomo ${v.mihomo}", "Core ${v.core}",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("Copy diagnostics dropped %q", line)
		}
	}
}

// versionStatRowPlacement splits versionStatRows' labels into the ones
// behind its `if (advanced)` branch and the ones Simple mode still sees.
func versionStatRowPlacement(t *testing.T, diag string) (gated, always []string) {
	t.Helper()
	fn := regexp.MustCompile(`(?s)export function versionStatRows\(.*?\n\}`).FindString(diag)
	if fn == "" {
		t.Fatal("diag.ts no longer exports versionStatRows(...)")
	}
	const head = "if (advanced) {"
	open := strings.Index(fn, head)
	if open < 0 {
		t.Fatal("versionStatRows gates no `if (advanced)` branch")
	}
	depth := 0
	for i := open + len(head) - 1; i < len(fn); i++ {
		switch fn[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		if depth == 0 {
			label := regexp.MustCompile(`label: '([^']+)'`)
			collect := func(src string) []string {
				var out []string
				for _, m := range label.FindAllStringSubmatch(src, -1) {
					out = append(out, m[1])
				}
				return out
			}
			return collect(fn[open : i+1]), collect(fn[:open] + fn[i+1:])
		}
	}
	t.Fatal("the `if (advanced)` branch never closes")
	return nil, nil
}

func sameLabels(a, b []string) bool {
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
