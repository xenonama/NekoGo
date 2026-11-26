package main

// chain_idle_test.go — an exit chain belongs to the leg that arms it, and
// a connect that runs the other leg has to say so.
//
// The Chain tab's switches are per leg: SpoofPsiphon/SpoofTor need a
// profile that goes through the spoofing engine, AetherPsiphon/AetherTor
// need one that IS an Aether tunnel naming no mode of its own. A user who
// armed Tor and connected the node sitting in the list saw a normal
// connected state and traffic that never met Tor, with nothing in the log
// explaining the difference — the complaint this closes.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func idlePlainProfile() *Profile {
	return &Profile{ID: "ss-1", Name: "Plain SS", Core: CoreSingBox,
		Protocol: "shadowsocks", Address: "198.51.100.20", Port: 8443,
		IDOrPassword: "pw"}
}

func idleAetherProfile() *Profile {
	return &Profile{ID: "ae-1", Name: "Aether hop", Core: CoreSingBox,
		Protocol: "aether", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "masque"}}
}

// TestIdleChainNotesNameTheLegThisConnectSkips pins the whole rule: every
// armed switch that this connect leaves unused is named, with the reason,
// and a chain that does run is never reported.
func TestIdleChainNotesNameTheLegThisConnectSkips(t *testing.T) {
	off := false
	cases := []struct {
		name    string
		profile *Profile
		mutate  func(*Settings)
		want    []string // substrings one note must carry; nil wants silence
		wantN   int
	}{
		{
			name:    "aether tor on a plain node",
			profile: idlePlainProfile(),
			mutate:  func(s *Settings) { s.AetherTor = true },
			want:    []string{"Tor", "Plain SS", "not an Aether"},
			wantN:   1,
		},
		{
			name:    "aether tor on its own engine",
			profile: idleAetherProfile(),
			mutate:  func(s *Settings) { s.AetherTor = true },
			wantN:   0,
		},
		{
			name:    "aether tor bypassed by a raw outbound",
			profile: func() *Profile { p := idleAetherProfile(); p.RawOutbound = `{"type":"direct"}`; return p }(),
			mutate:  func(s *Settings) { s.AetherTor = true },
			want:    []string{"Tor", "raw outbound"},
			wantN:   1,
		},
		{
			name:    "aether tor under a hand-written command",
			profile: func() *Profile { p := idleAetherProfile(); p.Aether.Command = "--tor off"; return p }(),
			mutate:  func(s *Settings) { s.AetherTor = true },
			want:    []string{"Tor", "command"},
			wantN:   1,
		},
		{
			name:    "aether tor under a profile that names its own mode",
			profile: func() *Profile { p := idleAetherProfile(); p.Aether.Tor = "only"; return p }(),
			mutate:  func(s *Settings) { s.AetherTor = true },
			want:    []string{"Tor", "own"},
			wantN:   1,
		},
		{
			name:    "the profile's own tor chain is not an idle switch",
			profile: func() *Profile { p := idleAetherProfile(); p.Aether.Tor = "chain"; return p }(),
			mutate:  func(s *Settings) { s.AetherTor = true },
			wantN:   0,
		},
		{
			name:    "spoof tor on a spoofed node",
			profile: spoofTestProfile(),
			mutate:  func(s *Settings) { s.SpoofTor = true },
			wantN:   0,
		},
		{
			name:    "spoof tor with the engine switched off",
			profile: spoofTestProfile(),
			mutate: func(s *Settings) {
				s.SpoofTor = true
				s.SpoofEnabled = &off
			},
			want:  []string{"Tor", "spoofing"},
			wantN: 1,
		},
		{
			name:    "spoof tor on a node that never spoofs",
			profile: idlePlainProfile(),
			mutate:  func(s *Settings) { s.SpoofTor = true },
			want:    []string{"Tor", "Plain SS"},
			wantN:   1,
		},
		{
			name:    "aether psiphon on a plain node",
			profile: idlePlainProfile(),
			mutate:  func(s *Settings) { s.AetherPsiphon = true },
			want:    []string{"Psiphon", "Plain SS"},
			wantN:   1,
		},
		{
			name:    "nothing armed",
			profile: idlePlainProfile(),
			mutate:  func(s *Settings) {},
			wantN:   0,
		},
		{
			name:    "both legs armed, neither runs",
			profile: idlePlainProfile(),
			mutate: func(s *Settings) {
				s.SpoofTor = true
				s.AetherPsiphon = true
			},
			wantN: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultSettings()
			tc.mutate(&s)
			got := idleChainNotes(tc.profile, s)
			if len(got) != tc.wantN {
				t.Fatalf("idleChainNotes = %d notes %q, want %d", len(got), got, tc.wantN)
			}
			if tc.wantN == 0 {
				return
			}
			joined := strings.Join(got, " | ")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("note %q does not mention %q", joined, want)
				}
			}
		})
	}
}

// TestChainSwitchSurvivesASave: the two booleans per leg ARE the switch,
// and they are omitempty — a config that never wrote them reads back as
// None. This pins the whole round trip through the API the Chain tab uses,
// so "Tor is not in my path" cannot be a toggle that never reached disk.
func TestChainSwitchSurvivesASave(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.AetherTor = true
	s.SpoofTor = true
	app := NewApp(store, make(chan LogEntry, 256))
	if err := app.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Decoded, not grepped: the file is indented and nests the settings
	// under "settings", and what matters is that disk carries the keys
	// under the names the app reads back.
	var onDisk fileConfig
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("config.json unreadable: %v", err)
	}
	if !onDisk.Settings.AetherTor || !onDisk.Settings.SpoofTor {
		t.Fatalf("disk aetherTor %v, spoofTor %v; both must be written",
			onDisk.Settings.AetherTor, onDisk.Settings.SpoofTor)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.GetSettings()
	if !got.AetherTor || !got.SpoofTor {
		t.Fatalf("reloaded leg drivers = aetherTor %v, spoofTor %v; both must stay armed",
			got.AetherTor, got.SpoofTor)
	}
}

// idleConnectLogs runs the real Connect with the core start stubbed out,
// and hands back everything the app logged on the way.
func idleConnectLogs(t *testing.T, mutate func(*Settings), p Profile) []LogEntry {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	mutate(&s)
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 256))
	app.coreStarter = func(*Profile, Settings) error { return nil }
	if err := app.Connect(saved.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return app.RecentLogs(0)
}

func idleChainWarns(logs []LogEntry) []string {
	var out []string
	for _, e := range logs {
		if e.Source == "chain" && e.Level == "warn" {
			out = append(out, e.Message)
		}
	}
	return out
}

func TestConnectWarnsAboutAChainItDoesNotRun(t *testing.T) {
	arm := func(s *Settings) { s.AetherTor = true }

	warns := idleChainWarns(idleConnectLogs(t, arm, *idlePlainProfile()))
	if len(warns) != 1 {
		t.Fatalf("chain warnings on a plain connect = %q, want exactly one", warns)
	}
	for _, want := range []string{"Tor", "Plain SS"} {
		if !strings.Contains(warns[0], want) {
			t.Errorf("warning %q does not mention %q", warns[0], want)
		}
	}

	// The same switch on an Aether node runs Tor through the engine, so
	// there is nothing to warn about.
	if warns := idleChainWarns(idleConnectLogs(t, arm, *idleAetherProfile())); len(warns) != 0 {
		t.Fatalf("an Aether connect that runs its chain logged %q", warns)
	}
}
