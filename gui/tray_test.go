package main

import (
	"bytes"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// TestTrayIconEmbedded verifies the tray/window icon embed: non-empty PNG
// bytes that actually decode (a corrupt or missing file would leave the
// tray with no icon or a silent fallback).
func TestTrayIconEmbedded(t *testing.T) {
	if len(appIconPNG) == 0 {
		t.Fatal("appIconPNG is empty: gui/frontend/src/assets/app.png missing or embed broken")
	}
	img, err := png.Decode(bytes.NewReader(appIconPNG))
	if err != nil {
		t.Fatalf("appIconPNG does not decode as PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 16 || b.Dy() < 16 {
		t.Fatalf("appIconPNG too small for a tray icon: %dx%d", b.Dx(), b.Dy())
	}
	t.Logf("tray icon: %d bytes, %dx%d", len(appIconPNG), b.Dx(), b.Dy())
}

func TestTrayMenuItemsHaveWindows10Icons(t *testing.T) {
	app := application.New(application.Options{Name: "NekoGo-test"})
	tray := SetupTray(app, TrayCallbacks{}, nil)
	check := func(label string) {
		t.Helper()
		item := tray.menu.FindByLabel(label)
		if item == nil {
			t.Fatalf("tray menu has no %q item", label)
		}
		bitmap := reflect.ValueOf(item).Elem().FieldByName("bitmap").Bytes()
		img, err := png.Decode(bytes.NewReader(bitmap))
		if err != nil {
			t.Fatalf("%q has no valid menu PNG: %v", label, err)
		}
		if b := img.Bounds(); b.Dx() != 16 || b.Dy() != 16 {
			t.Fatalf("%q menu icon is %dx%d, want 16x16", label, b.Dx(), b.Dy())
		}
	}
	for _, label := range []string{"Show NekoGo", "Connect selected", "Reconnect", "Disconnect", "Profiles", "Quit NekoGo"} {
		check(label)
	}
	tray.SetConnectionState(false, true, "", "", false, false)
	check("Cancel connection")
}

func TestTrayCancelIsNotSkippedWhileConnectBusy(t *testing.T) {
	app := newTrayTestApp(t, true)
	app.connMu.Lock()
	app.connBusy = true
	before := app.connEpoch
	app.connMu.Unlock()
	runTrayDisconnect(app)
	app.connMu.Lock()
	after := app.connEpoch
	app.connMu.Unlock()
	if after != before+1 {
		t.Fatalf("tray Cancel left the Connect epoch at %d, want %d", after, before+1)
	}
	for _, entry := range app.RecentLogs(20) {
		if strings.Contains(entry.Message, "disconnect skipped") {
			t.Fatalf("tray Cancel was ignored: %s", entry.Message)
		}
	}
}

func TestTrayCancelIsPublishedDuringConnectStartup(t *testing.T) {
	app, profile := testProxyApp(t, DefaultSettings(), CoreSingBox)
	defer func() { _ = app.Disconnect() }()
	entered := make(chan struct{})
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	defer release()
	app.coreStarter = func(*Profile, Settings) error {
		close(entered)
		<-gate
		return nil
	}
	disconnected := make(chan struct{})
	tray := SetupTray(application.New(application.Options{Name: "NekoGo-test"}), TrayCallbacks{
		OnDisconnect: func() {
			runTrayDisconnect(app)
			close(disconnected)
		},
	}, app.logCh)
	app.attachTray(tray)
	connectErr := make(chan error, 1)
	go func() { connectErr <- app.Connect(profile.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not reach the core starter")
	}

	tray.mu.RLock()
	busy := tray.state.busy
	label := tray.disconnectItem.Label()
	tray.mu.RUnlock()
	if !busy {
		t.Fatal("tray did not publish busy=true while Connect was starting")
	}
	if label != "Cancel connection" {
		t.Fatalf("tray action = %q, want Cancel connection", label)
	}
	if !tray.invoke(trayDisconnect, "") {
		t.Fatal("tray rejected Cancel connection during startup")
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("tray Cancel did not reach App.Disconnect")
	}
	release()
	select {
	case err := <-connectErr:
		if err == nil || err.Error() != "connect aborted (concurrent stop)" {
			t.Fatalf("Connect after tray Cancel = %v, want shared cancellation error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not settle after tray Cancel")
	}
	tray.mu.RLock()
	busy = tray.state.busy
	label = tray.disconnectItem.Label()
	tray.mu.RUnlock()
	if busy || label != "Disconnect" {
		t.Fatalf("settled tray state: busy=%v action=%q, want stopped Disconnect", busy, label)
	}
}

// TestTrayIconConvertsToHICON runs the exact Windows conversion the tray
// uses (PNG bytes -> HICON via CreateIconFromResourceEx). A failure here
// means a blank/missing icon in the notification area.
func TestTrayIconConvertsToHICON(t *testing.T) {
	if len(appIconPNG) == 0 {
		t.Fatal("appIconPNG is empty")
	}
	h, err := w32.CreateSmallHIconFromImage(appIconPNG)
	if err != nil {
		t.Fatalf("CreateSmallHIconFromImage: %v", err)
	}
	if h == 0 {
		t.Fatal("CreateSmallHIconFromImage returned a null handle")
	}
	defer w32.DestroyIcon(h)
}

// TestTrayIconICO is the Windows tray source: a valid multi-image ICO
// that converts to a real HICON (native 16x16 entry, no PNG scaling).
func TestTrayIconICO(t *testing.T) {
	if len(appIconICO) == 0 {
		t.Fatal("appIconICO is empty: gui/app.ico missing or embed broken (rebuild it: go run ./script/png2ico.go -in gui/frontend/src/assets/app.png -out gui/app.ico)")
	}
	// ICO header: reserved(0,0), type(1=ICO), image count.
	if len(appIconICO) < 6 || appIconICO[0] != 0 || appIconICO[1] != 0 || appIconICO[2] != 1 || appIconICO[3] != 0 {
		t.Fatal("appIconICO is not a valid ICO file")
	}
	if n := int(appIconICO[4]) | int(appIconICO[5])<<8; n < 1 {
		t.Fatal("appIconICO contains no images")
	}
	h, err := w32.CreateSmallHIconFromImage(appIconICO)
	if err != nil {
		t.Fatalf("CreateSmallHIconFromImage(ico): %v", err)
	}
	if h == 0 {
		t.Fatal("CreateSmallHIconFromImage(ico) returned a null handle")
	}
	defer w32.DestroyIcon(h)
	t.Logf("tray ico: %d bytes", len(appIconICO))
}

// TestSetupTrayLogs builds the tray pre-Run (headless-safe: creation is
// deferred by the framework) and asserts every diagnostic line lands on
// logCh with no ERROR.
func TestSetupTrayLogs(t *testing.T) {
	app := application.New(application.Options{Name: "NekoGo-test"})
	ch := make(chan LogEntry, 64)
	tray := SetupTray(app, TrayCallbacks{}, ch)
	tray.SetStatus(false, "", "")
	tray.SetStatus(true, "p", "xray")
	var got []string
	for len(ch) > 0 {
		e := <-ch
		got = append(got, e.Message)
		if e.Source != "tray" || e.Level != "info" {
			t.Errorf("unexpected tray diagnostic: %+v", e)
		}
		if _, err := time.Parse(time.RFC3339, e.Time); err != nil {
			t.Errorf("invalid log timestamp: %v", err)
		}
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"[tray] creating system tray",
		"[tray] set label ok",
		"[tray] set icon (",
		"[tray] set menu (10 items)",
		"[tray] profiles menu rebuilt (0 rows, 0 hidden)",
		"[tray] system tray created",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing log line %q; got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "ERROR") {
		t.Errorf("unexpected tray error lines:\n%s", joined)
	}
	// Initial render and running transition log; unchanged stopped state skips.
	if n := strings.Count(joined, "[tray] set label ok"); n != 2 {
		t.Errorf("got %d label updates, want 2; logs:\n%s", n, joined)
	}
	if tray.statusItem.Label() != "Status: Connected — p (xray)" {
		t.Errorf("SetStatus did not update menu status: %q", tray.statusItem.Label())
	}
}

// TestTrayServersMenu: the Servers submenu lists profiles (capped,
// active checked), truncates long remarks, and shows a disabled
// placeholder when empty. Headless-safe: menu ops never need Run.
func TestTrayServersMenu(t *testing.T) {
	app := application.New(application.Options{Name: "NekoGo-test"})
	ch := make(chan LogEntry, 64)
	tray := SetupTray(app, TrayCallbacks{}, ch)

	many := make([]TrayServer, 0, 31)
	for i := 0; i < 30; i++ {
		many = append(many, TrayServer{ID: "id-" + string(rune('a'+i/10)) + string(rune('0'+i%10)), Name: "server"})
	}
	longName := "this remark is deliberately far too long for any sane tray menu row"
	many[2].Name = longName
	tray.RefreshServers(many, many[3].ID)
	if got := tray.servers.FindByLabel("server"); got == nil {
		t.Fatalf("servers submenu lost its rows")
	}
	if got := tray.servers.FindByLabel(longName); got != nil {
		t.Fatalf("long remark was not truncated")
	}
	// Cap: rows past trayServerCap never render.
	extra := make([]TrayServer, 0, trayServerCap+5)
	for i := 0; i < trayServerCap+5; i++ {
		extra = append(extra, TrayServer{ID: "x", Name: "capped-row"})
	}
	extra[trayServerCap].Name = "past-the-cap"
	tray.RefreshServers(extra, "")
	if got := tray.servers.FindByLabel("past-the-cap"); got != nil {
		t.Fatalf("row past the cap rendered")
	}
	if got := tray.servers.FindByLabel("capped-row"); got == nil {
		t.Fatalf("rows within the cap missing")
	}

	tray.RefreshServers(nil, "")
	if got := tray.servers.FindByLabel("No profiles"); got == nil {
		t.Fatalf("empty store: want the disabled placeholder row")
	}
}

// Regression: the selected profile must remain visible even past the cap.
// Uses only the original SetupTray/RefreshServers APIs, without app.Run.
func TestTrayServersRetainsSelectedBeyondCap(t *testing.T) {
	app := application.New(application.Options{Name: "NekoGo-test"})
	tray := SetupTray(app, TrayCallbacks{}, nil)
	entries := make([]TrayServer, trayServerCap+5)
	for i := range entries {
		id := "profile-" + string(rune('A'+i))
		entries[i] = TrayServer{ID: id, Name: id}
	}
	selected := entries[len(entries)-1]
	tray.RefreshServers(entries, selected.ID)
	row := tray.servers.FindByLabel(selected.Name)
	if row == nil {
		t.Fatalf("selected profile %q past cap %d is missing", selected.ID, trayServerCap)
	}
	if !row.IsRadio() || !row.Checked() {
		t.Fatal("selected profile must be a checked radio row")
	}
	radios := 0
	for i := 0; tray.servers.ItemAt(i) != nil; i++ {
		if tray.servers.ItemAt(i).IsRadio() {
			radios++
		}
	}
	if radios != trayServerCap {
		t.Fatalf("got %d profile rows, want cap %d including selection", radios, trayServerCap)
	}
}

// TestShouldHideOnCloseTrue — closeToTray=true hides instead of quitting.
func TestShouldHideOnCloseTrue(t *testing.T) {
	a := newTrayTestApp(t, true)
	if !a.ShouldHideOnClose() {
		t.Fatal("ShouldHideOnClose() = false with closeToTray=true")
	}
	if got := a.DebugWindowClose(); got != "hide" {
		t.Fatalf("DebugWindowClose() = %q, want hide", got)
	}
}

// TestShouldHideOnCloseFalse — closeToTray=false really quits.
func TestShouldHideOnCloseFalse(t *testing.T) {
	a := newTrayTestApp(t, false)
	if a.ShouldHideOnClose() {
		t.Fatal("ShouldHideOnClose() = true with closeToTray=false")
	}
	if got := a.DebugWindowClose(); got != "quit" {
		t.Fatalf("DebugWindowClose() = %q, want quit", got)
	}
}

func newTrayTestApp(t *testing.T, closeToTray bool) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := store.GetSettings()
	s.CloseToTray = closeToTray
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return NewApp(store, make(chan LogEntry, 512))
}

// fakeTray records Show/Hide calls for headless visibility tests.
// Calls are mutex-guarded: race tests drive SetCloseToTray from
// several goroutines at once.
type fakeTray struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeTray) Show() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "show")
}
func (f *fakeTray) Hide() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "hide")
}
func (f *fakeTray) SetStatus(bool, string, string) {}

func (f *fakeTray) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// TestTrayVisibilityStartupTrue — closeToTray=true builds the tray.
func TestTrayVisibilityStartupTrue(t *testing.T) {
	a := newTrayTestApp(t, true)
	var built int
	got := setupTrayIfEnabled(a, func() trayController {
		built++
		return &fakeTray{}
	})
	if built != 1 || got == nil {
		t.Fatalf("built=%d got=%v, want exactly one tray", built, got)
	}
}

// TestTrayVisibilityStartupFalse — closeToTray=false skips creation and
// logs the skip line instead of any creating line.
func TestTrayVisibilityStartupFalse(t *testing.T) {
	a := newTrayTestApp(t, false)
	var built int
	got := setupTrayIfEnabled(a, func() trayController {
		built++
		return &fakeTray{}
	})
	if built != 0 || got != nil {
		t.Fatalf("built=%d got=%v, want no tray", built, got)
	}
	found := false
	for _, e := range a.RecentLogs(50) {
		if e.Message == "[tray] skipped: closeToTray=false" {
			found = true
		}
		if e.Message == "[tray] creating system tray" {
			t.Fatal("tray was created despite closeToTray=false")
		}
	}
	if !found {
		t.Fatal("skip line missing from logs")
	}
}

// TestSetCloseToTrayTogglesVisibility — true then false on an attached
// fake tray calls show/hide in order and persists each step; with no
// tray attached, true rebuilds via trayBuilder and logs the re-enable.
func TestSetCloseToTrayTogglesVisibility(t *testing.T) {
	a := newTrayTestApp(t, true)
	rec := &fakeTray{}
	a.attachTray(rec)
	if err := a.SetCloseToTray(true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetCloseToTray(false); err != nil {
		t.Fatal(err)
	}
	if got := rec.recorded(); len(got) != 2 || got[0] != "show" || got[1] != "hide" {
		t.Fatalf("calls=%v, want [show hide]", got)
	}
	if a.store.GetSettings().CloseToTray {
		t.Fatal("persisted closeToTray=true after SetCloseToTray(false)")
	}

	b := newTrayTestApp(t, false)
	rec2 := &fakeTray{}
	built := false
	b.setTrayBuilder(func() trayController {
		built = true
		return rec2
	})
	if err := b.SetCloseToTray(true); err != nil {
		t.Fatal(err)
	}
	if !built || b.tray == nil {
		t.Fatal("re-enable did not rebuild and attach the tray")
	}
	found := false
	for _, e := range b.RecentLogs(50) {
		if e.Message == "[tray] re-enabled after closeToTray=true" {
			found = true
		}
	}
	if !found {
		t.Fatal("re-enable line missing from logs")
	}
}

// TestSetCloseToTrayPersistsSettings — the on-disk config.json carries
// the flipped value.
func TestSetCloseToTrayPersistsSettings(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(store, make(chan LogEntry, 512))
	if err := a.SetCloseToTray(false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fc fileConfig
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}
	if fc.Settings.CloseToTray {
		t.Fatalf("config.json closeToTray = true, want false:\n%s", raw)
	}
}

// TestTrayBuilderRace — BUG-023: the startup builder assignment vs
// SetCloseToTray reads must be race-clean. Drives the locked setter
// against the locked reader from 4 goroutines; run with -race.
func TestTrayBuilderRace(t *testing.T) {
	a := newTrayTestApp(t, false)
	a.attachTray(&fakeTray{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				a.setTrayBuilder(func() trayController { return &fakeTray{} })
				_ = a.SetCloseToTray((i+j)%2 == 0)
			}
		}(i)
	}
	wg.Wait()
}
