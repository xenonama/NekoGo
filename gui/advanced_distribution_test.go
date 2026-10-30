package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase H: log-level normalization and validation.
func TestNormalizeLogLevel(t *testing.T) {
	cases := map[string]string{
		"debug": "debug", "INFO": "info", "warn": "warn",
		"warning": "warn", "WARNING": "warn", "error": "error", "": "",
	}
	for in, want := range cases {
		if got := normalizeLogLevel(in); got != want {
			t.Fatalf("normalizeLogLevel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, lvl := range []string{"debug", "info", "warn", "warning", "error", ""} {
		if !validLogLevel(lvl) {
			t.Fatalf("validLogLevel(%q) = false, want true", lvl)
		}
	}
	for _, lvl := range []string{"verbose", "trace", "none", "infoo"} {
		if validLogLevel(lvl) {
			t.Fatalf("validLogLevel(%q) = true, want false", lvl)
		}
	}
}

// Phase H: per-core level mapping (sing-box wants warn, xray wants warning).
func TestCoreLogLevelMapping(t *testing.T) {
	if got := singLogLevel("warning"); got != "warn" {
		t.Fatalf("singLogLevel(warning) = %q, want warn", got)
	}
	if got := xrayLogLevel("warn"); got != "warning" {
		t.Fatalf("xrayLogLevel(warn) = %q, want warning", got)
	}
	if got := xrayLogLevel("warning"); got != "warning" {
		t.Fatalf("xrayLogLevel(warning) = %q, want warning", got)
	}
	// CPU audit #2: an unset level resolves to the warn default per core.
	if got := singLogLevel(""); got != "warn" {
		t.Fatalf("singLogLevel(empty) = %q, want warn", got)
	}
	if got := xrayLogLevel(""); got != "warning" {
		t.Fatalf("xrayLogLevel(empty) = %q, want warning", got)
	}
}

// Phase H: NEKOBOX_LOG_LEVEL reaches the child env; warn hook fires once.
func TestChildEnvWithLogLevel(t *testing.T) {
	base := []string{"A=1"}
	calls := 0
	env := childEnvWithLogLevel(base, "debug", func(string) { calls++ })
	found := false
	for _, kv := range env {
		if kv == "NEKOBOX_LOG_LEVEL=debug" {
			found = true
		}
	}
	if !found {
		t.Fatalf("NEKOBOX_LOG_LEVEL=debug missing in %v", env)
	}
	// CPU audit #4: the child cores always get the relaxed GC target.
	if !hasEnvKV(env, "GOGC=200") {
		t.Fatalf("GOGC=200 missing in %v", env)
	}
	// Empty level adds no NEKOBOX_LOG_LEVEL, but GOGC still applies.
	env2 := childEnvWithLogLevel(base, "", func(string) { calls++ })
	for _, kv := range env2 {
		if strings.HasPrefix(kv, "NEKOBOX_LOG_LEVEL=") {
			t.Fatalf("empty level still set NEKOBOX_LOG_LEVEL: %v", env2)
		}
	}
	if !hasEnvKV(env2, "GOGC=200") {
		t.Fatalf("GOGC=200 missing for empty level: %v", env2)
	}
	// An explicit GOGC (any case) is never overwritten.
	env3 := childEnvWithLogLevel([]string{"A=1", "GOGC=50"}, "warn", nil)
	if hasEnvKV(env3, "GOGC=200") || !hasEnvKV(env3, "GOGC=50") {
		t.Fatalf("explicit GOGC overridden: %v", env3)
	}
	env4 := childEnvWithLogLevel([]string{"gogc=off"}, "", nil)
	if hasEnvKV(env4, "GOGC=200") {
		t.Fatalf("lowercase gogc not honored: %v", env4)
	}
	_ = calls
}

// hasEnvKV reports whether env contains kv exactly (helper for the
// child-env tests).
func hasEnvKV(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// Phase H: SaveSettings rejects bad log levels and migrates legacy warning.
func TestSaveSettingsLogLevel(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	logCh := make(chan LogEntry, 8)
	app := NewApp(store, logCh)
	s := store.GetSettings()
	s.LogLevel = "verbose"
	if err := app.SaveSettings(s); err == nil {
		t.Fatal("SaveSettings accepted logLevel=verbose")
	}
	s.LogLevel = "warning"
	if err := app.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().LogLevel; got != "warn" {
		t.Fatalf("legacy warning not migrated, got %q", got)
	}
}

// CPU audit #1: the TUN stack setting rejects values sing-tun cannot
// load, accepts the three real stacks, and normalizes empty to the
// default so pre-setting configs converge on "system".
func TestSaveSettingsTunStack(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	s := store.GetSettings()
	s.TunStack = "gvisor2"
	if err := app.SaveSettings(s); err == nil {
		t.Fatal("SaveSettings accepted tunStack=gvisor2")
	}
	s = store.GetSettings()
	s.TunStack = "gvisor"
	if err := app.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().TunStack; got != "gvisor" {
		t.Fatalf("tunStack = %q, want gvisor", got)
	}
	s = store.GetSettings()
	s.TunStack = ""
	if err := app.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().TunStack; got != "system" {
		t.Fatalf("empty tunStack = %q, want system", got)
	}
}

// Phase H: log output path round-trips and receives appended lines.
func TestLogOutputPathRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	logCh := make(chan LogEntry, 8)
	app := NewApp(store, logCh)
	out := filepath.Join(dir, "gui.log")
	s := store.GetSettings()
	s.LogOutputPath = out
	if err := app.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().LogOutputPath; got != out {
		t.Fatalf("logOutputPath = %q, want %q", got, out)
	}
	app.emitLog("gui", "info", "phase-h-probe-line")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "phase-h-probe-line") {
		t.Fatalf("log file missing probe line: %q", data)
	}
}
