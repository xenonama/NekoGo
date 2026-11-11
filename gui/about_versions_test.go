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
	for label, field := range map[string]string{
		"sing-box core": "singBox",
		"NekoBox core":  "nekoBox",
	} {
		row := regexp.MustCompile(`\{\s*label:\s*'` + regexp.QuoteMeta(label) + `',\s*value:\s*v\.(\w+)\s*\}`)
		m := row.FindStringSubmatch(about)
		if m == nil {
			t.Errorf("About has no %q row", label)
			continue
		}
		if m[1] != field {
			t.Errorf("About row %q reads v.%s, want v.%s", label, m[1], field)
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
