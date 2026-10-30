package main

// P3 Batch 1 regression tests (BUG-033..044, testable subset).
// Network/process-dependent bugs (034 probe, 035 Close path is in
// third_party, 036 DNS init, 038 ephemeral cores, 040 unwritable dir,
// 044 cross-compile) are verified by build/vet/GOOS checks, not here.

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// BUG-033: sing-box builder must fail fast on mixedPort > 65534,
// mirroring the xray builder.
func TestP3SingBoxBuilderRejectsHugeMixedPort(t *testing.T) {
	p := &Profile{Protocol: "direct", Core: CoreSingBox}
	s := DefaultSettings()
	s.MixedPort = 70000
	if _, err := BuildSingBoxConfig(p, s); err == nil {
		t.Fatal("BuildSingBoxConfig(MixedPort=70000) = nil, want range error")
	}
	s.MixedPort = 65535
	if _, err := BuildSingBoxConfig(p, s); err == nil {
		t.Fatal("BuildSingBoxConfig(MixedPort=65535) = nil, want <=65534 error")
	}
	s.MixedPort = 2080
	if _, err := BuildSingBoxConfig(p, s); err != nil {
		t.Fatalf("BuildSingBoxConfig(MixedPort=2080) = %v, want nil", err)
	}
}

// BUG-037: nil Stat entries must be skipped, not dereferenced.
func TestP3XrayStatsNilSafe(t *testing.T) {
	c := NewXrayStatsClient("127.0.0.1:1")
	_ = c
	resp := &QueryStatsResponse{Stat: []*Stat{
		nil,
		{Name: "outbound>>>proxy>>>traffic>>>uplink", Value: 7},
		nil,
		{Name: "outbound>>>proxy>>>traffic>>>downlink", Value: 9},
		nil,
	}}
	up, down, upByTag, downByTag := parseXrayStats(resp)
	if up != 7 || down != 9 {
		t.Fatalf("parseXrayStats with nils = (%d,%d), want (7,9)", up, down)
	}
	if upByTag["proxy"] != 7 || downByTag["proxy"] != 9 {
		t.Fatalf("per-tag with nils = %v/%v, want proxy 7/9", upByTag, downByTag)
	}
}

// BUG-039: dedupeKey must distinguish AlterID.
func TestP3DedupeKeyIncludesAlterID(t *testing.T) {
	a := &Profile{Protocol: "vmess", Address: "example.com", Port: 443, IDOrPassword: "uuid"}
	b := &Profile{Protocol: "vmess", Address: "example.com", Port: 443, IDOrPassword: "uuid"}
	a.AlterID = 0
	b.AlterID = 2
	if dedupeKey(a) == dedupeKey(b) {
		t.Fatal("dedupeKey ignores AlterID: aid 0 vs 2 collide")
	}
}

// BUG-041: anchored GUI asset picker — notes .txt must never win,
// installer .exe must beat a leading generic asset.
func TestP3PickGUIAssetAnchored(t *testing.T) {
	assets := []ghAsset{
		{Name: "NekoGo-Setup-notes.txt", BrowserDownloadURL: "http://x/notes.txt"},
		{Name: "NekoGo-Setup-v1.exe", BrowserDownloadURL: "http://x/setup.exe"},
		{Name: "NekoGo-Portable-v1.zip", BrowserDownloadURL: "http://x/portable.zip"},
	}
	got := pickGUIAsset(assets)
	if got == nil || got.Name != "NekoGo-Setup-v1.exe" {
		t.Fatalf("pickGUIAsset = %v, want the .exe installer", got)
	}
	txtOnly := []ghAsset{
		{Name: "NekoGo-Setup-notes.txt", BrowserDownloadURL: "http://x/notes.txt"},
		{Name: "random-setup-blob", BrowserDownloadURL: "http://x/blob"},
	}
	if got := pickGUIAsset(txtOnly); got != nil {
		t.Fatalf("pickGUIAsset(txt-only) = %v, want nil", got.Name)
	}
	portable := []ghAsset{
		{Name: "NekoGo-Portable-v1.zip", BrowserDownloadURL: "http://x/p.zip"},
	}
	if got := pickGUIAsset(portable); got == nil || !strings.HasSuffix(strings.ToLower(got.Name), ".zip") {
		t.Fatalf("pickGUIAsset(portable) = %v, want the .zip", got)
	}
}

// BUG-042: TrayController.visible is mutex-guarded and read back.
func TestP3TrayVisibleGuarded(t *testing.T) {
	tc := &TrayController{}
	tc.setVisible(true)
	if !tc.IsVisible() {
		t.Fatal("IsVisible = false after setVisible(true)")
	}
	tc.setVisible(false)
	if tc.IsVisible() {
		t.Fatal("IsVisible = true after setVisible(false)")
	}
	// Concurrent Show/Hide on a nil-tray controller must not race
	// (run with -race in CI).
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); tc.Show() }()
		go func() { defer wg.Done(); tc.Hide() }()
	}
	wg.Wait()
	_ = tc.IsVisible()
}

// BUG-043: AddSubscription/TouchSubscription are atomic RMWs —
// concurrent adds of the same URL record it once, and a concurrent
// CloseToTray toggle survives.
func TestP3SubscriptionAtomicRMW(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{URL: "https://example.com/sub", Name: "ex", UpdatedAt: time.Now().Format("2006-01-02")}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.AddSubscription(sub)
		}()
	}
	wg.Wait()
	subs := store.GetSettings().Subscriptions
	if len(subs) != 1 {
		t.Fatalf("concurrent AddSubscription left %d rows, want 1", len(subs))
	}
	if _, err := store.TouchSubscription(sub.URL, "t2", SubUserInfo{Upload: 7, Total: 100}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetSettings().Subscriptions[0]; got.UpdatedAt != "t2" || got.UsageUpload != 7 || got.UsageTotal != 100 {
		t.Fatalf("TouchSubscription row = %+v, want t2 + usage 7/100", got)
	}
	// A settings toggle after the subscription add must not drop it.
	if err := store.SetCloseToTray(true); err != nil {
		t.Fatal(err)
	}
	s := store.GetSettings()
	if !s.CloseToTray {
		t.Fatal("SetCloseToTray(true) did not persist")
	}
	if len(s.Subscriptions) != 1 || s.Subscriptions[0].URL != sub.URL {
		t.Fatalf("toggle dropped subscription: %+v", s.Subscriptions)
	}
}

// BUG-040: Connect must surface persistence failures instead of
// reporting success while state was lost.
func TestP3ConnectReportsPersistenceFailure(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	saved, err := app.SaveProfile(Profile{Name: "p", Core: CoreSingBox,
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	// Break persistence: point the store at a path that cannot be
	// written (a directory), so SaveProfile/SetActiveProfileID fail.
	app.store.path = t.TempDir() // a directory: writes fail
	app.coreStarter = func(p *Profile, s Settings) error { return nil }
	if err := app.Connect(saved.ID); err == nil {
		t.Fatal("Connect with broken persistence = nil, want persistence error")
	}
}
