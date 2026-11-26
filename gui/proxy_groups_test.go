package main

import (
	"bytes"
	"os"
	"reflect"
	"testing"
	"time"
)

func newProxyGroupTestStore(t *testing.T, profiles []Profile, groups, collapsed []string) *ConfigStore {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if _, err := store.SaveProfile(p); err != nil {
			t.Fatal(err)
		}
	}
	off := false
	for _, sub := range []Subscription{
		{URL: "https://feed.example/one", Name: "Old", UpdatedAt: "2026-01-02T03:04:05Z", Enabled: &off,
			UsageUpload: 11, UsageDownload: 22, UsageTotal: 100, UsageExpire: "2027-01-01"},
		{URL: "https://feed.example/two", Name: "Other feed"},
	} {
		if _, err := store.AddSubscription(sub); err != nil {
			t.Fatal(err)
		}
	}
	s := store.GetSettings()
	s.ProxyGroups, s.CollapsedProxyGroups = groups, collapsed
	if len(profiles) > 0 {
		s.ActiveProfileID = profiles[0].ID
	}
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return store
}

func proxyGroupTestProfiles() []Profile {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []Profile{
		{ID: "active", Name: "Active node", Core: CoreXray, Protocol: "vless", Address: "192.0.2.1", Port: 443,
			IDOrPassword: "test-credential", Network: "ws", TLS: true, ServerName: "sni.example", Path: "/tunnel",
			Host: "host.example", ALPN: "h2", Fingerprint: "chrome", RealityPubKey: "test-public-key",
			RealityShortID: "abcd", Flow: "xtls-rprx-vision", MuxEnabled: true, MuxConcurrency: 4,
			RawOutbound: `{"type":"vless","server":"192.0.2.1"}`, LastLatency: 42, LastTestedAt: &stamp,
			CountryCode: "NL", CountryName: "Netherlands", CountryAt: &stamp, LastConnectedAt: stamp.Format(time.RFC3339),
			Group: "Old", SubURL: "https://feed.example/one"},
		{ID: "second-feed", Name: "Second feed node", Group: "Old", SubURL: "https://feed.example/two", IDOrPassword: "another-secret"},
		{ID: "manual", Name: "Manual member", Group: "Old", IDOrPassword: "manual-secret"},
		{ID: "refiled", Name: "Already refiled", Group: "Keep", SubURL: "https://feed.example/one"},
		{ID: "ungrouped", Name: "Ungrouped", SubURL: "https://feed.example/one"},
		{ID: "prefix", Name: "Similar name", Group: "Old extra"},
		{ID: "case", Name: "Different case", Group: "old"},
		{ID: "spaces", Name: "Stored whitespace", Group: " Old "},
	}
}

func proxyGroupSnapshot(store *ConfigStore) fileConfig {
	return fileConfig{Profiles: store.ListProfiles(), Settings: store.GetSettings()}
}

func assertProxyGroupSnapshot(t *testing.T, store *ConfigStore, want fileConfig) {
	t.Helper()
	got := proxyGroupSnapshot(store)
	if !reflect.DeepEqual(got.Profiles, want.Profiles) {
		t.Errorf("profiles changed beyond the expected group edits: got %+v, want %+v", got.Profiles, want.Profiles)
	}
	if !reflect.DeepEqual(got.Settings, want.Settings) {
		t.Error("settings changed beyond the expected group metadata edits")
	}
}

func assertProxyGroupReload(t *testing.T, store *ConfigStore, want fileConfig) {
	t.Helper()
	reloaded, err := NewConfigStore(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	assertProxyGroupSnapshot(t, reloaded, want)
}

func proxyGroupDiskBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertProxyGroupWrites(t *testing.T, before, want int64) {
	t.Helper()
	if got := saveCount.Load() - before; got != want {
		t.Errorf("config writes = %d, want %d", got, want)
	}
}

// A headless recorder; neither the App nor CoreManager starts any processes.
type proxyGroupTrayRecorder struct {
	calls   int
	running bool
	name    string
	core    string
}

func (*proxyGroupTrayRecorder) Show() {}
func (*proxyGroupTrayRecorder) Hide() {}
func (r *proxyGroupTrayRecorder) SetStatus(running bool, name, core string) {
	r.calls++
	r.running, r.name, r.core = running, name, core
}

func TestProxyGroupEmptyLifecycle(t *testing.T) {
	store := newProxyGroupTestStore(t, nil, []string{"Keep", "Old"}, []string{"Old", "Keep"})
	want := proxyGroupSnapshot(store)
	before := saveCount.Load()
	if err := store.RenameProxyGroup(" \tOld\n", " New \t"); err != nil {
		t.Fatal(err)
	}
	want.Settings.ProxyGroups = []string{"Keep", "New"}
	want.Settings.CollapsedProxyGroups = []string{"New", "Keep"}
	assertProxyGroupWrites(t, before, 1)
	assertProxyGroupSnapshot(t, store, want)
	assertProxyGroupReload(t, store, want)

	before = saveCount.Load()
	if err := store.RemoveProxyGroup(" New\n"); err != nil {
		t.Fatal(err)
	}
	want.Settings.ProxyGroups = []string{"Keep"}
	want.Settings.CollapsedProxyGroups = []string{"Keep"}
	assertProxyGroupWrites(t, before, 1)
	assertProxyGroupSnapshot(t, store, want)
	assertProxyGroupReload(t, store, want)
}

func TestProxyGroupPopulatedLifecycle(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "implicit"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			groups := []string{"Keep"}
			if explicit {
				groups = append(groups, "Old")
			}
			store := newProxyGroupTestStore(t, proxyGroupTestProfiles(), groups, []string{"Old", "Keep"})
			tray := &proxyGroupTrayRecorder{}
			cores := &CoreManager{activeCore: CoreXray, activeID: "active", running: true, epoch: 7}
			app := &App{store: store, cores: cores, tray: tray}
			want := proxyGroupSnapshot(store)
			before := saveCount.Load()
			if err := app.RenameProxyGroup("Old", "New"); err != nil {
				t.Fatal(err)
			}
			for i := range want.Profiles {
				if want.Profiles[i].Group == "Old" {
					want.Profiles[i].Group = "New"
				}
			}
			// Even a formerly implicit feed group must survive becoming empty.
			want.Settings.ProxyGroups = []string{"Keep", "New"}
			want.Settings.CollapsedProxyGroups = []string{"New", "Keep"}
			assertProxyGroupWrites(t, before, 1)
			assertProxyGroupSnapshot(t, store, want)
			assertProxyGroupReload(t, store, want)
			if tray.calls != 1 || !tray.running || tray.name != "Active node" || tray.core != CoreXray.String() {
				t.Fatalf("rename did not refresh the active tray: %+v", tray)
			}

			before = saveCount.Load()
			if err := app.RemoveProxyGroup("New"); err != nil {
				t.Fatal(err)
			}
			for i := range want.Profiles {
				if want.Profiles[i].Group == "New" {
					want.Profiles[i].Group = ""
				}
			}
			want.Settings.ProxyGroups = []string{"Keep"}
			want.Settings.CollapsedProxyGroups = []string{"Keep"}
			assertProxyGroupWrites(t, before, 1)
			assertProxyGroupSnapshot(t, store, want)
			assertProxyGroupReload(t, store, want)
			if tray.calls != 2 || !tray.running || tray.name != "Active node" || tray.core != CoreXray.String() {
				t.Fatalf("remove did not refresh the active tray: %+v", tray)
			}
			if core, id, running := cores.Status(); core != CoreXray.String() || id != "active" || !running || cores.epoch != 7 {
				t.Fatal("group-only edits changed the live core state")
			}
		})
	}
}

func TestProxyGroupRejectedEditsLeaveStateUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		old    string
		new    string
		remove bool
	}{
		{name: "empty old", new: "New"},
		{name: "blank old", old: " \t\n", new: "New"},
		{name: "empty new", old: "Old"},
		{name: "blank new", old: "Old", new: " \t\n"},
		{name: "profile collision", old: "Old", new: " Keep \t"},
		{name: "manual collision", old: "Old", new: " Manual "},
		{name: "missing old", old: "Missing", new: "New"},
		{name: "missing no-op", old: "Missing", new: " Missing "},
		{name: "collapsed only rename", old: "Folded only", new: "New"},
		{name: "empty remove", remove: true},
		{name: "blank remove", old: " \t\n", remove: true},
		{name: "missing remove", old: "Missing", remove: true},
		{name: "collapsed only remove", old: "Folded only", remove: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newProxyGroupTestStore(t, proxyGroupTestProfiles(), []string{"Old", "Manual"}, []string{"Old", "Folded only"})
			tray := &proxyGroupTrayRecorder{}
			app := &App{store: store, cores: &CoreManager{}, tray: tray}
			want := proxyGroupSnapshot(store)
			disk := proxyGroupDiskBytes(t, store.path)
			before := saveCount.Load()
			var err error
			if tc.remove {
				err = app.RemoveProxyGroup(tc.old)
			} else {
				err = app.RenameProxyGroup(tc.old, tc.new)
			}
			if err == nil {
				t.Fatal("invalid group edit succeeded")
			}
			assertProxyGroupWrites(t, before, 0)
			assertProxyGroupSnapshot(t, store, want)
			if !bytes.Equal(proxyGroupDiskBytes(t, store.path), disk) {
				t.Error("rejected edit changed config.json")
			}
			if tray.calls != 0 {
				t.Error("rejected edit refreshed the tray")
			}
		})
	}
}

func TestProxyGroupRenameNoOpDoesNotWrite(t *testing.T) {
	for _, groups := range [][]string{nil, {"Old"}} {
		store := newProxyGroupTestStore(t, proxyGroupTestProfiles(), groups, []string{"Old"})
		want := proxyGroupSnapshot(store)
		disk := proxyGroupDiskBytes(t, store.path)
		before := saveCount.Load()
		if err := store.RenameProxyGroup(" Old ", "Old\t"); err != nil {
			t.Fatal(err)
		}
		assertProxyGroupWrites(t, before, 0)
		assertProxyGroupSnapshot(t, store, want)
		if !bytes.Equal(proxyGroupDiskBytes(t, store.path), disk) {
			t.Error("no-op changed config.json")
		}
	}
}

func TestProxyGroupSaveFailureRollsBack(t *testing.T) {
	for _, populated := range []bool{false, true} {
		for _, operation := range []string{"rename", "remove"} {
			name := "empty/" + operation
			if populated {
				name = "populated/" + operation
			}
			t.Run(name, func(t *testing.T) {
				var profiles []Profile
				if populated {
					profiles = proxyGroupTestProfiles()
				}
				store := newProxyGroupTestStore(t, profiles, []string{"Keep", "Old", "Old", "Tail"}, []string{"Keep", "Old", "Old", "Tail"})
				tray := &proxyGroupTrayRecorder{}
				app := &App{store: store, cores: &CoreManager{}, tray: tray}
				edit := func() error { return app.RenameProxyGroup("Old", "New") }
				if operation == "remove" {
					edit = func() error { return app.RemoveProxyGroup("Old") }
				}
				want := proxyGroupSnapshot(store)
				disk := proxyGroupDiskBytes(t, store.path)
				backup := proxyGroupDiskBytes(t, store.path+".bak")
				// Occupy the atomic-save staging path inside this test's temp dir.
				if err := os.Mkdir(store.path+".tmp", 0o755); err != nil {
					t.Fatal(err)
				}
				before := saveCount.Load()
				if err := edit(); err == nil {
					t.Fatal("edit succeeded despite blocked config.json.tmp")
				}
				assertProxyGroupWrites(t, before, 0)
				assertProxyGroupSnapshot(t, store, want)
				if !bytes.Equal(proxyGroupDiskBytes(t, store.path), disk) || !bytes.Equal(proxyGroupDiskBytes(t, store.path+".bak"), backup) {
					t.Error("failed save changed persisted configuration")
				}
				assertProxyGroupReload(t, store, want)
				if tray.calls != 0 {
					t.Error("failed save refreshed the tray")
				}

				if err := os.Remove(store.path + ".tmp"); err != nil {
					t.Fatal(err)
				}
				if err := edit(); err != nil {
					t.Fatalf("retry after clearing staging path: %v", err)
				}
				destination := "New"
				want.Settings.ProxyGroups = []string{"Keep", "New", "New", "Tail"}
				want.Settings.CollapsedProxyGroups = []string{"Keep", "New", "New", "Tail"}
				if operation == "remove" {
					destination = ""
					want.Settings.ProxyGroups = []string{"Keep", "Tail"}
					want.Settings.CollapsedProxyGroups = []string{"Keep", "Tail"}
				}
				for i := range want.Profiles {
					if want.Profiles[i].Group == "Old" {
						want.Profiles[i].Group = destination
					}
				}
				assertProxyGroupWrites(t, before, 1)
				assertProxyGroupSnapshot(t, store, want)
				assertProxyGroupReload(t, store, want)
				if tray.calls != 1 {
					t.Error("successful retry did not refresh the tray once")
				}
			})
		}
	}
}
