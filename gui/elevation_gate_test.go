package main

// Elevation gates for the SNI spoof engine (WinDivert) and the TUN
// inbound (Wintun), including the case where one connect needs both:
// the SNI spoof → Psiphon chain.
//
// The engine loads a kernel driver, so an unelevated process gets
// "Access is denied (code 5)". Connect refuses that up front with a
// message naming the remedy; these tests pin that the same remedy
// reaches the paths that used to surface the raw driver error.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// spoofElevationFixture wires an app with one CoreSpoof profile, a spoof
// exe that does not exist (so a start that reaches the spawn fails
// without launching anything) and a chosen elevation state.
func spoofElevationFixture(t *testing.T, elevated bool) *App {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("WinDivert and Wintun are Windows paths")
	}
	withElevated(t, elevated)
	oldExe, oldDir := spoofExeOverride, spoofExeDirOverride
	spoofExeOverride = filepath.Join(t.TempDir(), "missing-sni-spoof.exe")
	t.Cleanup(func() { spoofExeOverride, spoofExeDirOverride = oldExe, oldDir })

	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := Profile{
		ID: "spoof1", Name: "spoof node", Core: CoreSpoof,
		Protocol: "vless", Address: "example.com", Port: 443,
		Network: "ws",
	}
	if _, err := store.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	return NewApp(store, make(chan LogEntry, 64))
}

// TestSpoofStartRefusesUnelevated: the SNI tab's Start went straight to
// the engine, so an unelevated user got the driver's own error and no
// route to the remedy the app already offers.
func TestSpoofStartRefusesUnelevated(t *testing.T) {
	app := spoofElevationFixture(t, false)
	err := app.SpoofStart("spoof1")
	if err == nil {
		t.Fatal("SpoofStart started the engine in an unelevated process")
	}
	if !strings.Contains(err.Error(), "requires administrator rights") {
		t.Fatalf("SpoofStart error = %q, want the admin remedy the frontend matches", err)
	}
	// The gate has to come before the exe lookup, or the driver error
	// still wins and the message the UI keys on never appears.
	if strings.Contains(err.Error(), "missing-sni-spoof.exe") {
		t.Fatalf("SpoofStart reached the spawn: %v", err)
	}
	if app.spoofMgr.IsRunning() {
		t.Fatal("the spoof engine is running after a refused start")
	}
}

// TestSpoofStartRunsWhenElevated: the gate must not become a wall. An
// elevated process goes on to the real start, which here fails on the
// deliberately missing mock exe.
func TestSpoofStartRunsWhenElevated(t *testing.T) {
	app := spoofElevationFixture(t, true)
	err := app.SpoofStart("spoof1")
	if err == nil {
		t.Fatal("SpoofStart succeeded with no engine exe")
	}
	if strings.Contains(err.Error(), "administrator") {
		t.Fatalf("an elevated process was refused with the admin message: %v", err)
	}
	if !strings.Contains(err.Error(), "missing-sni-spoof.exe") {
		t.Fatalf("SpoofStart error = %q, want it to reach the engine start", err)
	}
}

// TestSpoofStartDisabledSpoofIsNotAnElevationProblem: Settings → SNI
// spoofing off means no engine at all, so the tab must keep saying that
// rather than asking for administrator rights.
func TestSpoofStartDisabledSpoofIsNotAnElevationProblem(t *testing.T) {
	app := spoofElevationFixture(t, false)
	s := app.store.GetSettings()
	off := false
	s.SpoofEnabled = &off
	if err := app.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	err := app.SpoofStart("spoof1")
	if err == nil {
		t.Fatal("SpoofStart ran with SNI spoofing disabled")
	}
	if strings.Contains(err.Error(), "administrator") {
		t.Fatalf("disabled spoofing asked for elevation: %v", err)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("SpoofStart error = %q, want the disabled-setting reason", err)
	}
}

// TestElevationMessageNamesBothDriversForTheChain: the SNI spoof →
// Psiphon chain forces the TUN inbound, so it needs Wintun AND
// WinDivert. Reporting only TUN left the driver that carries the
// spoofing itself unnamed, on the one connect that needs both.
func TestElevationMessageNamesBothDriversForTheChain(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("WinDivert and Wintun are Windows paths")
	}
	withElevated(t, false)
	spoof := &Profile{ID: "s", Core: CoreSpoof, Protocol: "vless"}
	plain := &Profile{ID: "p", Core: CoreSingBox, Protocol: "vless"}

	spoofOnly := DefaultSettings()
	if got := elevationMessage(spoof, spoofOnly); got != spoofRequiresAdminMessage {
		t.Errorf("spoof connect = %q, want the spoof driver message", got)
	}

	tunOnly := DefaultSettings()
	tunOnly.TunEnabled = true
	if got := elevationMessage(plain, tunOnly); got != tunRequiresAdminMessage {
		t.Errorf("TUN connect = %q, want the TUN message", got)

	}

	chain := DefaultSettings()
	chain.SpoofPsiphon = true
	got := elevationMessage(spoof, chain)
	if got == "" {
		t.Fatal("the spoof → Psiphon chain reported no elevation need")
	}
	// The frontend keys on this phrase to offer RelaunchAsAdmin.
	if !strings.Contains(got, "requires administrator rights") {
		t.Errorf("chain message %q lost the phrase the frontend matches", got)
	}
	for _, driver := range []string{"WinDivert", "Wintun"} {
		if !strings.Contains(got, driver) {
			t.Errorf("chain message %q never names %s", got, driver)
		}
	}

	// Nothing to report when the process is already elevated, and no
	// gate at all for a profile that starts neither driver.
	withElevated(t, true)
	if got := elevationMessage(spoof, chain); got != "" {
		t.Errorf("elevated chain = %q, want no refusal", got)
	}
	withElevated(t, false)
	if got := elevationMessage(plain, DefaultSettings()); got != "" {
		t.Errorf("plain proxy-only connect = %q, want no refusal", got)
	}
}

// TestConnectRefusesTheChainBeforeSpawning: the refusal has to come from
// Connect itself, before any child process exists, and persist the
// profile the elevated relaunch resumes.
func TestConnectRefusesTheChainBeforeSpawning(t *testing.T) {
	app := spoofElevationFixture(t, false)
	s := app.store.GetSettings()
	s.SpoofPsiphon = true
	if err := app.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	started := 0
	app.coreStarter = func(*Profile, Settings) error {
		started++
		return nil
	}
	err := app.Connect("spoof1")
	if err == nil {
		t.Fatal("Connect accepted an unelevated spoof chain")
	}
	if !strings.Contains(err.Error(), "requires administrator rights") {
		t.Fatalf("Connect error = %q, want the admin remedy", err)
	}
	if started != 0 {
		t.Fatal("Connect spawned the core after refusing on elevation")
	}
	if got := app.store.GetSettings().ActiveProfileID; got != "spoof1" {
		t.Fatalf("activeProfileId = %q, want spoof1 for the elevated relaunch to resume", got)
	}
}

// TestElevationPromptIsNekoGoOwnDialog: the handoff was a browser
// confirm(), which renders outside the app's theme. Both prompts go
// through the elevation store now, and the shell mounts the dialog.
func TestElevationPromptIsNekoGoOwnDialog(t *testing.T) {
	read := func(parts ...string) string {
		raw, err := os.ReadFile(filepath.Join(parts...))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	front := filepath.Join("frontend", "src")

	store := read(front, "stores", "elevation.ts")
	if !strings.Contains(store, "export const useElevationStore") {
		t.Error("there is no elevation store to carry the prompt")
	}
	view := read(front, "components", "ElevationPrompt.vue")
	for _, cls := range []string{"nb-modal-overlay", "nb-modal-foot", "nb-act--primary"} {
		if !strings.Contains(view, cls) {
			t.Errorf("ElevationPrompt.vue does not use the app's .%s modal idiom", cls)
		}
	}
	if !strings.Contains(read(front, "App.vue"), "ElevationPrompt") {
		t.Error("App.vue never mounts the elevation dialog, so it cannot appear")
	}
	for _, site := range []struct{ rel, why string }{
		{filepath.Join("stores", "connection.ts"), "the connect-error handoff"},
		{filepath.Join("components", "SNIPanel.vue"), "the SNI tab's relaunch button"},
	} {
		src := read(front, site.rel)
		if strings.Contains(src, "confirm(") {
			t.Errorf("%s still uses the browser confirm() instead of the app dialog", site.why)
		}
		if !strings.Contains(src, "useElevationStore") {
			t.Errorf("%s does not ask through the elevation store", site.why)
		}
	}
}
