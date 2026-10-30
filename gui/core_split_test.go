package main

// Two-engine split tests: the custom Thrift core (nekobox_core.exe) and
// the official sing-box CLI (sing-box.exe) are separate binaries with
// separate settings, and neither resolver may ever pick the other's
// file — that conflation is what made an installed official binary
// break the custom engine.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestResolveCoreRejectsOfficialBinary: a corePath aimed at sing-box.exe
// must fail with a pointer to the right setting instead of launching the
// official CLI with the custom engine's Thrift flags.
func TestResolveCoreRejectsOfficialBinary(t *testing.T) {
	for _, name := range []string{"sing-box.exe", "sing-box"} {
		dir := t.TempDir()
		p := writeFakeExe(t, dir, name)
		m := NewSingBoxManager("", nil)
		m.setCorePath(p)
		got, err := m.resolveCore()
		if err == nil {
			t.Fatalf("%s: resolveCore = %q, want an error", name, got)
		}
		if !strings.Contains(err.Error(), "upstreamCorePath") {
			t.Fatalf("%s: err = %v, want an upstreamCorePath hint", name, err)
		}
	}
}

// TestResolveUpstreamIgnoresCustomCorePath: the official CLI resolves
// from UpstreamCorePath / the exe dir only — never from the custom
// core's CorePath.
func TestResolveUpstreamIgnoresCustomCorePath(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeExe(t, dir, "sing-box.exe")
	m := NewUpstreamSingBoxManager(nil)
	got, err := m.resolveUpstreamCore(Settings{CorePath: fake})
	if err == nil && got == fake {
		t.Fatal("resolveUpstreamCore resolved CorePath (custom-core override)")
	}
	if err != nil && !strings.Contains(err.Error(), "upstreamCorePath") {
		t.Fatalf("err = %v, want an upstreamCorePath hint", err)
	}
	// The dedicated override does resolve, including a directory form.
	got, err = m.resolveUpstreamCore(Settings{UpstreamCorePath: fake})
	if err != nil || got != fake {
		t.Fatalf("resolveUpstreamCore(UpstreamCorePath) = %q, %v; want %q", got, err, fake)
	}
	got, err = m.resolveUpstreamCore(Settings{UpstreamCorePath: dir})
	if err != nil || got != fake {
		t.Fatalf("resolveUpstreamCore(dir) = %q, %v; want %q", got, err, fake)
	}
}

// TestSaveSettingsRejectsCrossWiredCorePaths: saving either path aimed at
// the other engine's binary fails up front with a message naming the
// right setting.
func TestSaveSettingsRejectsCrossWiredCorePaths(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	dir := t.TempDir()

	s := DefaultSettings()
	s.CorePath = writeFakeExe(t, dir, "sing-box.exe")
	if err := app.SaveSettings(s); err == nil || !strings.Contains(err.Error(), "upstreamCorePath") {
		t.Fatalf("SaveSettings(corePath=sing-box.exe) = %v, want upstreamCorePath refusal", err)
	}
	s = DefaultSettings()
	s.UpstreamCorePath = writeFakeExe(t, dir, "nekobox_core.exe")
	if err := app.SaveSettings(s); err == nil || !strings.Contains(err.Error(), "corePath") {
		t.Fatalf("SaveSettings(upstreamCorePath=nekobox_core.exe) = %v, want corePath refusal", err)
	}
	// The correct wiring saves.
	s = DefaultSettings()
	s.CorePath = writeFakeExe(t, dir, "nekobox_core.exe")
	s.UpstreamCorePath = writeFakeExe(t, dir, "sing-box.exe")
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings with the two engines wired correctly: %v", err)
	}
}

// TestCoreImageOwner: each updater row owns exactly its own binary name,
// so an install can never land on another row's file.
func TestCoreImageOwner(t *testing.T) {
	cases := map[string]string{
		`C:\app\sing-box.exe`:       "singbox",
		`C:\app\SING-BOX.EXE`:       "singbox",
		`C:\app\nekobox_core.exe`:   "nekobox",
		`C:\app\xray.exe`:           "xray",
		`C:\app\aether.exe`:         "",
		`C:\app\nekogo.exe`:         "",
		`c:/app/pt/lyrebird.exe`:    "",
		`C:\app\sing-box.json`:      "",
		`C:\cores\nekobox_core.bak`: "",
	}
	for in, want := range cases {
		if got := coreImageOwner(in); got != want {
			t.Errorf("coreImageOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMigrateCorePathSplit: a pre-split config that stored the official
// CLI in corePath keeps working — the value moves to upstreamCorePath
// and corePath is cleared (otherwise the next settings save would be
// refused by the cross-wired-path validation).
func TestMigrateCorePathSplit(t *testing.T) {
	s := DefaultSettings()
	s.CorePath = `C:\cores\sing-box.exe`
	migrateCorePathSplit(&s)
	if s.CorePath != "" {
		t.Fatalf("corePath = %q, want cleared", s.CorePath)
	}
	if s.UpstreamCorePath != `C:\cores\sing-box.exe` {
		t.Fatalf("upstreamCorePath = %q, want the migrated path", s.UpstreamCorePath)
	}
	// An explicit upstreamCorePath wins; the stray corePath is dropped.
	s = DefaultSettings()
	s.CorePath = `C:\a\sing-box.exe`
	s.UpstreamCorePath = `C:\b\sing-box.exe`
	migrateCorePathSplit(&s)
	if s.CorePath != "" || s.UpstreamCorePath != `C:\b\sing-box.exe` {
		t.Fatalf("corePath/upstreamCorePath = %q/%q", s.CorePath, s.UpstreamCorePath)
	}
	// A legitimate custom-core path is untouched.
	s = DefaultSettings()
	s.CorePath = `C:\cores\nekobox_core.exe`
	migrateCorePathSplit(&s)
	if s.CorePath != `C:\cores\nekobox_core.exe` || s.UpstreamCorePath != "" {
		t.Fatalf("custom core path was migrated: %q/%q", s.CorePath, s.UpstreamCorePath)
	}
}
