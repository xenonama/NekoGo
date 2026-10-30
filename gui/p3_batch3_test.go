package main

// P3 Batch 3 regression tests (integration BUG-013..021, Go-testable
// subset). Frontend-only bugs (013 wiring, 014, 016, 018, 019, 020) are
// covered by `npm run build` (vue-tsc) plus grep verification; BUG-021
// end-to-end needs live cores, so its clamp helper is unit-tested here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// BUG-015: a transient "" version result must not poison the cache —
// the next call retries instead of serving "" forever.
func TestP3VersionCacheRetriesAfterEmpty(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	// Existing but un-runnable files: resolve succeeds, the version
	// query fails, exercising the cache-write lines.
	fakeCore := filepath.Join(t.TempDir(), "nekobox_core.exe")
	if err := os.WriteFile(fakeCore, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeXray := filepath.Join(t.TempDir(), "xray.exe")
	if err := os.WriteFile(fakeXray, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	app.singbox.setCorePath(fakeCore)
	app.xray.setXrayPath(fakeXray)
	if got := app.CoreVersion(); got != "" {
		t.Fatalf("CoreVersion(broken bin) = %q, want empty", got)
	}
	if app.coreVer.version != "" {
		t.Fatal("CoreVersion cached the transient empty result")
	}
	if got := app.CoreVersion(); got != "" {
		t.Fatalf("CoreVersion retry = %q, want empty (re-queried, not poisoned)", got)
	}
	if got := app.XrayVersion(); got != "" {
		t.Fatalf("XrayVersion(broken bin) = %q, want empty", got)
	}
	if app.xrayVer.version != "" {
		t.Fatal("XrayVersion cached the transient empty result")
	}
}

// BUG-017: unset timestamps must be omitted from profile JSON, never
// serialized as year-1; set ones must round-trip.
func TestP3ZeroTimesOmitted(t *testing.T) {
	raw, err := json.Marshal(Profile{ID: "x", Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lastTestedAt") || strings.Contains(string(raw), "countryAt") {
		t.Fatalf("zero times serialized: %s", raw)
	}
	stamp := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	p := Profile{ID: "x", Name: "n", LastTestedAt: &stamp, CountryAt: &stamp}
	raw, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "lastTestedAt") || !strings.Contains(string(raw), "countryAt") {
		t.Fatalf("set times omitted: %s", raw)
	}
	var back Profile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.LastTestedAt == nil || !back.LastTestedAt.Equal(stamp) {
		t.Fatalf("lastTestedAt did not round-trip: %+v", back.LastTestedAt)
	}
	if back.CountryAt == nil || !back.CountryAt.Equal(stamp) {
		t.Fatalf("countryAt did not round-trip: %+v", back.CountryAt)
	}
}

// BUG-021: sub-ms success clamps to a persistable 1 ms; real values pass
// through untouched.
func TestP3ClampLatencyMs(t *testing.T) {
	if got := clampLatencyMs(0); got != 1 {
		t.Fatalf("clampLatencyMs(0) = %d, want 1", got)
	}
	if got := clampLatencyMs(-3); got != 1 {
		t.Fatalf("clampLatencyMs(-3) = %d, want 1", got)
	}
	if got := clampLatencyMs(42); got != 42 {
		t.Fatalf("clampLatencyMs(42) = %d, want 42", got)
	}
}
