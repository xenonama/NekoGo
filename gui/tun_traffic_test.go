package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTunTrafficSalvagesFirstReading: the adapter's counters survive
// across connects (Windows reuses the adapter), so the first reading of
// a session is only the baseline and later readings report growth.
func TestTunTrafficSalvagesFirstReading(t *testing.T) {
	var tr tunTraffic
	if up, down := tr.sample(9_000, 7_000); up != 0 || down != 0 {
		t.Fatalf("first sample = (%d, %d), want (0, 0)", up, down)
	}
	up, down := tr.sample(9_400, 7_500)
	if down != 400 || up != 500 {
		t.Fatalf("growth = (up %d, down %d), want (500, 400)", up, down)
	}
	// Cumulative: the next reading keeps the earlier bytes.
	up, down = tr.sample(9_900, 7_600)
	if down != 900 || up != 600 {
		t.Fatalf("cumulative = (up %d, down %d), want (600, 900)", up, down)
	}
}

// TestTunTrafficCountersGoingBackwardsRebaseline: a recreated adapter
// starts at zero; the ledger must re-baseline instead of reporting a
// negative total, and count what comes after.
func TestTunTrafficCountersGoingBackwardsRebaseline(t *testing.T) {
	var tr tunTraffic
	tr.sample(1_000, 1_000)
	tr.sample(2_000, 3_000)
	if up, down := tr.sample(10, 20); up != 0 || down != 0 {
		t.Fatalf("re-baseline sample = (%d, %d), want (0, 0)", up, down)
	}
	up, down := tr.sample(110, 120)
	if down != 100 || up != 100 {
		t.Fatalf("after re-baseline = (up %d, down %d), want (100, 100)", up, down)
	}
}

// TestTunTrafficResetStartsNewSession: a new connect drops the total
// even though the adapter still reports its lifetime counters.
func TestTunTrafficResetStartsNewSession(t *testing.T) {
	var tr tunTraffic
	tr.sample(0, 0)
	tr.sample(5_000, 6_000)
	tr.reset()
	if up, down := tr.sample(5_100, 6_100); up != 0 || down != 0 {
		t.Fatalf("post-reset first sample = (%d, %d), want (0, 0)", up, down)
	}
	up, down := tr.sample(5_300, 6_400)
	if down != 200 || up != 300 {
		t.Fatalf("post-reset growth = (up %d, down %d), want (300, 200)", up, down)
	}
}

// TestTunStatsUsesAdapterCounters: a TUN session reports the adapter's
// bytes without consulting the engine at all — the case the tracker
// used to show as 0 B. The baseline reading returns nil so GetStats
// falls through until the adapter has carried something.
func TestTunStatsUsesAdapterCounters(t *testing.T) {
	prev := tunCountersFunc
	t.Cleanup(func() { tunCountersFunc = prev })
	var in, out uint64
	tunCountersFunc = func(alias string) (uint64, uint64, bool, error) {
		if alias != tunInterfaceName {
			t.Fatalf("adapter alias = %q, want %q", alias, tunInterfaceName)
		}
		return in, out, true, nil
	}
	m := &CoreManager{running: true, tunActive: true}
	in, out = 1_000, 2_000
	if _, err := m.GetStats(); err == nil {
		t.Fatal("baseline read should fall through to the (missing) engine")
	}
	in, out = 1_400, 2_500
	st, err := m.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if st.Uplink != 500 || st.Downlink != 400 {
		t.Fatalf("stats = (up %d, down %d), want (500, 400)", st.Uplink, st.Downlink)
	}
	if st.UplinkByTag[tunTrafficTag] != 500 || st.DownlinkByTag[tunTrafficTag] != 400 {
		t.Fatalf("per-tag = (%v, %v)", st.UplinkByTag, st.DownlinkByTag)
	}
	if st.Core != "sing-box" {
		t.Fatalf("core = %q, want sing-box", st.Core)
	}
	if st.ApiDown {
		t.Fatal("TUN reading marked ApiDown")
	}
}

// TestTunStatsFallsBackWithoutAdapter: no adapter (TUN not up yet) or a
// zero reading keeps the engine's counters in charge.
func TestTunStatsFallsBackWithoutAdapter(t *testing.T) {
	prev := tunCountersFunc
	t.Cleanup(func() { tunCountersFunc = prev })
	tunCountersFunc = func(string) (uint64, uint64, bool, error) { return 0, 0, false, nil }
	m := &CoreManager{running: true, tunActive: true}
	if _, err := m.GetStats(); err == nil {
		t.Fatal("a missing adapter must fall through to the engine")
	}
}

// TestTunSessionClearedOnStop: a stopped connect must not leave the
// session flagged as TUN, or a later proxy-mode connect would report
// adapter bytes it does not own.
func TestTunSessionClearedOnStop(t *testing.T) {
	m := &CoreManager{}
	m.setTunSessionLocked(true)
	if !m.tunActive {
		t.Fatal("TUN session not recorded")
	}
	m.setTunSessionLocked(false)
	if m.tunActive {
		t.Fatal("TUN session survived the clear")
	}
}

// TestBuildSingBoxTunInterfaceName: the renderer names the adapter, so
// the OS-side reader can find it (and two sing-box apps cannot collide
// on the default name).
func TestBuildSingBoxTunInterfaceName(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	p := &Profile{ID: "s", Name: "ss", Core: CoreSingBox, Protocol: "shadowsocks",
		Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
	raw, err := BuildSingBoxConfig(p, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Type          string `json:"type"`
			InterfaceName string `json:"interface_name"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.Inbounds {
		if in.Type == "tun" && in.InterfaceName == tunInterfaceName {
			return
		}
	}
	t.Fatalf("no tun inbound named %q in %s", tunInterfaceName, raw)
}

// TestTunConfigPassesOfficialCheck: both shipped binaries accept the
// rendered TUN config, including interface_name. Skips when the engines
// are not built into the tree.
func TestTunConfigPassesOfficialCheck(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	p := &Profile{ID: "s", Name: "ss", Core: CoreSingBox, Protocol: "shadowsocks",
		Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
	raw, err := BuildSingBoxConfig(p, s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tun.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, core := range []string{
		filepath.Join("build", "bin", "nekobox_core.exe"),
		filepath.Join("bin", "sing-box.exe"),
	} {
		if _, err := os.Stat(core); err != nil {
			continue
		}
		checked++
		out, err := exec.Command(core, "check", "-c", path).CombinedOutput()
		if err != nil {
			t.Fatalf("%s rejected the TUN config: %v\n%s\nconfig: %s", core, err, out, raw)
		}
	}
	if checked == 0 {
		t.Skip("no engine binaries built in the tree")
	}
}
