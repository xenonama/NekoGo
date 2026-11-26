package main

// The log console is frontend state, so — the way about_versions_test.go
// pins the Version stats card — the contract is asserted against the
// source that ships. The invariant is about WHERE the clear lives, which
// is exactly what the bug was.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestLogClearSurvivesRemount: the console is mounted with
// `v-else-if="tab === 'logs'"`, so leaving the tab unmounts the component
// and returning remounts it fresh. Clear used to record the dismissed
// lines in a Set declared in that component's own <script setup>, so the
// dismissals died with the instance while the log array they filtered
// lived in the Pinia store — every line came back on the way back, which
// is the reported bug ("it clears, but when I go to another tab I see the
// log hasn't been cleared").
//
// The store is therefore the only place the clear can live. Every entry
// carries a monotonic id (App.pumpLogs/emitLog assign it from one counter),
// so a watermark — "hide everything up to id N" — is enough, and unlike a
// Set of ids it cannot grow over a long session.
func TestLogClearSurvivesRemount(t *testing.T) {
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))
	viewer := readFrontendSrc(t, filepath.Join("components", "LogViewer.vue"))

	for _, want := range []string{"logsClearedThrough", "function clearLogs"} {
		if !strings.Contains(store, want) {
			t.Errorf("stores/connection.ts has no %s: the log clear must live in the store, "+
				"because LogViewer is unmounted every time the console tab closes", want)
		}
	}
	if regexp.MustCompile(`logsClearedThrough\s*,`).FindString(store) == "" {
		t.Error("the connection store does not return logsClearedThrough, so no consumer can read the watermark")
	}

	// The component must consult it...
	if !strings.Contains(viewer, "logsClearedThrough") {
		t.Error("LogViewer.vue filters nothing on the store's cleared-through watermark")
	}
	// ...and must not keep a per-instance copy of the dismissals: that is
	// the shape the bug had.
	if regexp.MustCompile(`(?m)^\s*(?:const|let)\s+cleared\s*=`).FindString(viewer) != "" {
		t.Error("LogViewer.vue keeps a component-local `cleared` again — it is lost on unmount")
	}
	if strings.Contains(viewer, "new Set<string>()") {
		t.Error("LogViewer.vue holds a local Set of dismissed log ids; the store watermark replaces it")
	}
}

// TestLogClearWatermarkIgnoresStaleBuffer: clearing must not hide lines
// produced later, and the backend buffer is deliberately left alone (a
// clear is a view action; the log file on disk keeps the history, and
// wiping App.logBuf would need a new bound method and would destroy the
// crash story Status reports). This pins the handler to the store call so
// a future edit cannot quietly turn it into a fetch of fresh logs, which
// is what made the old one look like it worked.
func TestSniLogClearSurvivesRemount(t *testing.T) {
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))
	panel := readFrontendSrc(t, filepath.Join("components", "SNIPanel.vue"))

	for _, want := range []string{"sniLogsClearedThrough", "function clearSniLogs"} {
		if !strings.Contains(store, want) {
			t.Errorf("stores/connection.ts has no %s: SNI Clear must outlive the tab instance", want)
		}
	}
	if regexp.MustCompile(`sniLogsClearedThrough\s*,`).FindString(store) == "" {
		t.Error("the connection store does not expose the SNI watermark")
	}
	if !strings.Contains(panel, "l.id > connection.sniLogsClearedThrough") {
		t.Error("SNIPanel.vue does not filter replayed entries through the store watermark")
	}
	clear := regexp.MustCompile(`(?s)async function clearLogs\(\).*?\n\}`).FindString(panel)
	if clear == "" {
		t.Fatal("SNIPanel.vue no longer defines its async clearLogs() handler")
	}
	if !strings.Contains(clear, "connection.clearSniLogs(") {
		t.Errorf("SNI Clear does not advance the store watermark:\n%s", clear)
	}
	if !strings.Contains(clear, "backend.recentLogs(1)") {
		t.Error("SNI Clear does not cover buffered entries the tab has not fetched yet")
	}
	if strings.Contains(clear, "connection.clearLogs()") {
		t.Error("SNI Clear also clears the main Console")
	}
}

func TestLogClearWatermarkIgnoresStaleBuffer(t *testing.T) {
	viewer := readFrontendSrc(t, filepath.Join("components", "LogViewer.vue"))

	fn := regexp.MustCompile(`(?s)function clearView\(\).*?\n\}`).FindString(viewer)
	if fn == "" {
		t.Fatal("LogViewer.vue no longer defines clearView(); update this test with the handler")
	}
	if !strings.Contains(fn, "clearLogs()") {
		t.Errorf("the Clear button does not delegate to the store:\n%s", fn)
	}
	store := readFrontendSrc(t, filepath.Join("stores", "connection.ts"))
	body := regexp.MustCompile(`(?s)function clearLogs\(\).*?\n  \}`).FindString(store)
	if body == "" {
		t.Fatal("connection.ts no longer defines clearLogs()")
	}
	if strings.Contains(body, "recentLogs") {
		t.Error("clearLogs() refetches the buffer, so the watermark it computes is already stale")
	}
	// A watermark, not a reassignment of the array: the poller owns logs,
	// and emptying it here would flash an empty console until the next
	// 2s tick and then restore every line.
	if regexp.MustCompile(`logs\.value\s*=\s*\[\]`).FindString(body) != "" {
		t.Error("clearLogs() empties the array instead of advancing a watermark")
	}
	if !strings.Contains(body, ".id") {
		t.Error("clearLogs() computes no watermark from the entry ids App assigns in pumpLogs/emitLog")
	}
}
