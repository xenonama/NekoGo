package main

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The group PattN keeps in TunModeItem has to reach the keys
// third_party/sing-box/option/tun.go reads, and an unset field must
// reach nothing: a config that changed shape on upgrade would move
// someone's adapter or raise an MTU their link cannot carry.

func tunInboundOf(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	ins, _ := cfg["inbounds"].([]any)
	for _, in := range ins {
		m, _ := in.(map[string]any)
		if m["type"] == "tun" {
			return m
		}
	}
	t.Fatalf("no tun inbound in %v", ins)
	return nil
}

// TestTunLegacyConfigRendersAsBefore: every field of the group is
// optional, and unset means the value NekoGo used when they were
// constants.
func TestTunLegacyConfigRendersAsBefore(t *testing.T) {
	tun := tunInboundOf(t, tunRender(t, nil))
	if got := tun["mtu"]; got != float64(tunMTU) {
		t.Errorf("mtu = %v, want the historical %d", got, tunMTU)
	}
	if got := tun["auto_route"]; got != true {
		t.Errorf("auto_route = %v, want true (a TUN with no route captures nothing)", got)
	}
	if got := tun["strict_route"]; got != false {
		t.Errorf("strict_route = %v, want false", got)
	}
	if _, ok := tun["route_exclude_address"]; ok {
		t.Error("route_exclude_address rendered for a config that never set one")
	}
	if got := tun["interface_name"]; got != tunInterfaceName {
		t.Errorf("interface_name = %v, want %v (the traffic readout finds the adapter by it)", got, tunInterfaceName)
	}
}

// TestTunSettingsDriveTheInbound: each field lands on the key the core
// reads it from.
func TestTunSettingsDriveTheInbound(t *testing.T) {
	tun := tunInboundOf(t, tunRender(t, func(s *Settings) {
		s.TunMTU = 1420
		s.TunAddress = "10.7.0.1/24"
		s.TunStrictRoute = true
		off := false
		s.TunAutoRoute = &off
		s.TunRouteExclude = []string{"192.168.1.0/24", "10.0.0.0/8"}
		s.TunStack = "mixed"
	}))
	if tun["mtu"] != float64(1420) {
		t.Errorf("mtu = %v, want the configured 1420", tun["mtu"])
	}
	if tun["strict_route"] != true {
		t.Errorf("strict_route = %v, want the configured true", tun["strict_route"])
	}
	if tun["auto_route"] != false {
		t.Errorf("auto_route = %v, want the configured false", tun["auto_route"])
	}
	if tun["stack"] != "mixed" {
		t.Errorf("stack = %v, want mixed", tun["stack"])
	}
	addrs, _ := tun["address"].([]any)
	if len(addrs) != 1 || addrs[0] != "10.7.0.1/24" {
		t.Errorf("address = %v, want the configured prefix only", tun["address"])
	}
	ex, _ := tun["route_exclude_address"].([]any)
	if len(ex) != 2 || ex[0] != "192.168.1.0/24" || ex[1] != "10.0.0.0/8" {
		t.Errorf("route_exclude_address = %v, want both excluded routes", tun["route_exclude_address"])
	}
}

// TestTunAddressMovesTheSelfGuard: the guard used to be a second
// constant restating the first, so moving the interface left it dropping
// a host nobody used while the address the tunnel actually owns looped.
func TestTunAddressMovesTheSelfGuard(t *testing.T) {
	rules := routeRulesOf(t, tunRender(t, func(s *Settings) { s.TunAddress = "10.7.0.1/24" }))
	cidrs, _ := rules[0]["ip_cidr"].([]any)
	if len(cidrs) != 1 || cidrs[0] != "10.7.0.1/32" {
		t.Fatalf("guard = %v, want the /32 of the address actually configured", rules[0]["ip_cidr"])
	}
	for _, c := range cidrs {
		if c == "172.19.0.1/32" {
			t.Errorf("guard still drops the default address %v after the interface moved", c)
		}
	}
}

// TestTunIcmpPolicyReachesTheRules: PattN offers five policies. "rule",
// the unset default, renders nothing; the rest answer ICMP wherever it
// comes from.
func TestTunIcmpPolicyReachesTheRules(t *testing.T) {
	cases := []struct {
		policy string
		want   map[string]any
	}{
		{"rule", nil},
		{"", nil},
		{"direct", map[string]any{"network": []any{"icmp"}, "outbound": "direct"}},
		{"unreachable", map[string]any{"network": []any{"icmp"}, "action": "reject", "method": "default"}},
		{"drop", map[string]any{"network": []any{"icmp"}, "action": "reject", "method": "drop"}},
		{"reply", map[string]any{"network": []any{"icmp"}, "action": "reject", "method": "reply"}},
	}
	for _, tc := range cases {
		rules := routeRulesOf(t, tunRender(t, func(s *Settings) { s.TunIcmp = tc.policy }))
		var got map[string]any
		for _, r := range rules {
			if n := asList(r["network"]); len(n) == 1 && n[0] == "icmp" {
				got = r
			}
		}
		if tc.want == nil {
			if got != nil {
				t.Errorf("policy %q rendered %v, want no ICMP rule", tc.policy, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("policy %q rendered no ICMP rule", tc.policy)
			continue
		}
		for k, v := range tc.want {
			if fmt.Sprint(got[k]) != fmt.Sprint(v) {
				t.Errorf("policy %q: rule[%q] = %v, want %v", tc.policy, k, got[k], v)
			}
		}
	}
}

// --- the IPv6 gate (PattN's TunTakesIPv6) ---

// stubIPv6Gate answers the OS questions for one test.
func stubIPv6Gate(t *testing.T, disabledByOS, hasGlobal bool) {
	t.Helper()
	origD, origG := ipv6DisabledByOS, globalIPv6Present
	ipv6DisabledByOS = func() bool { return disabledByOS }
	globalIPv6Present = func() bool { return hasGlobal }
	t.Cleanup(func() { ipv6DisabledByOS, globalIPv6Present = origD, origG })
}

func TestTunIPv6AddressFollowsTheOSGate(t *testing.T) {
	cases := []struct {
		name        string
		enable      bool
		disabledOS  bool
		hasGlobalV6 bool
		wantV6      bool
	}{
		{"off by default", false, false, false, false},
		{"os accepts it", true, false, false, true},
		{"os refuses, nothing that could leak", true, true, false, false},
		{"os refuses, but global ipv6 would go around", true, true, true, true},
	}
	for _, tc := range cases {
		stubIPv6Gate(t, tc.disabledOS, tc.hasGlobalV6)
		s := DefaultSettings()
		s.TunEnableIPv6 = tc.enable
		if got := tunTakesIPv6(s); got != tc.wantV6 {
			t.Errorf("%s: tunTakesIPv6 = %v, want %v", tc.name, got, tc.wantV6)
		}
		want := 1
		if tc.wantV6 {
			want = 2
		}
		if n := len(tunAddressList(s)); n != want {
			t.Errorf("%s: address list has %d entries, want %d", tc.name, n, want)
		}
	}
}

// TestTunIPv6PrefixShape: PattN's pool uses /126 because the system
// stack derives its gateway from the first prefix and rejects a prefix
// naming a single address — while the guard that drops traffic to the
// tunnel itself must still name only the host.
func TestTunIPv6PrefixShape(t *testing.T) {
	stubIPv6Gate(t, false, false)
	s := DefaultSettings()
	s.TunEnableIPv6 = true
	addrs := tunAddressList(s)
	self := tunSelfHostPrefixes(addrs)
	if len(self) != 2 {
		t.Fatalf("self guard = %v, want one host per interface address", self)
	}
	if !strings.HasSuffix(self[1], "/128") {
		t.Errorf("IPv6 self guard = %q, want a /128 host", self[1])
	}
	for _, p := range addrs[1:] {
		pref, err := netip.ParsePrefix(p)
		if err != nil || pref.Bits() > 126 {
			t.Errorf("rendered IPv6 prefix %q leaves no room for the peer address", p)
		}
	}
}

func TestIPv6DisabledByComponentsBit(t *testing.T) {
	// 0x10 (prefer IPv4) and anything that includes it, like 0xFF, leave
	// IPv6 off on the interface class Wintun reports as.
	for v, want := range map[uint64]bool{0x00: false, 0x01: false, 0x10: true, 0xFF: true} {
		if got := ipv6DisabledByComponents(v); got != want {
			t.Errorf("DisabledComponents %#x: got %v, want %v", v, got, want)
		}
	}
}

// --- validation: refuse what the core cannot start ---

func TestTunValidationRefusesUnrenderableValues(t *testing.T) {
	cases := []struct {
		name  string
		tune  func(*Settings)
		wants string
	}{
		{"bare v4 address", func(s *Settings) { s.TunAddress = "172.19.0.1" }, "tunAddress"},
		{"bare v6 address", func(s *Settings) { s.TunAddress6 = "fc00::1" }, "tunAddress6"},
		{"v6 prefix with no peer", func(s *Settings) { s.TunAddress6 = "fc00::1/128" }, "peer"},
		{"mtu below the floor", func(s *Settings) { s.TunMTU = 576 }, "tunMTU"},
		{"mtu above the cap", func(s *Settings) { s.TunMTU = 70000 }, "tunMTU"},
		{"unknown icmp policy", func(s *Settings) { s.TunIcmp = "flood" }, "tunIcmp"},
		{"bare exclude address", func(s *Settings) { s.TunRouteExclude = []string{"192.168.1.1"} }, "tunRouteExclude"},
	}
	for _, tc := range cases {
		s := DefaultSettings()
		tc.tune(&s)
		err := validateTunSettings(&s)
		if err == nil {
			t.Errorf("%s: saved, and the core would refuse the config at start", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%s: error %q does not name %q", tc.name, err, tc.wants)
		}
	}
}

func TestTunValidationAcceptsThePools(t *testing.T) {
	for _, v4 := range tunIPv4Options {
		s := DefaultSettings()
		s.TunAddress = v4
		if err := validateTunSettings(&s); err != nil {
			t.Errorf("tunIPv4Options %q refused: %v", v4, err)
		}
	}
	for _, v6 := range tunIPv6Options {
		s := DefaultSettings()
		s.TunAddress6 = v6
		if err := validateTunSettings(&s); err != nil {
			t.Errorf("tunIPv6Options %q refused: %v", v6, err)
		}
	}
	for _, mtu := range tunMTUOptions {
		s := DefaultSettings()
		s.TunMTU = mtu
		if err := validateTunSettings(&s); err != nil {
			t.Errorf("tunMTUOptions %d refused: %v", mtu, err)
		}
	}
	for _, pol := range tunIcmpOptions {
		s := DefaultSettings()
		s.TunIcmp = pol
		if err := validateTunSettings(&s); err != nil {
			t.Errorf("tunIcmpOptions %q refused: %v", pol, err)
		}
	}
}

// --- the whole group against the engine that has to read it ---

// TestFullTunConfigPassesOfficialCheck: the shipped core must accept a
// config using every field at once. The default-shape check does not
// cover an address list, excluded routes or an IPv6 prefix, and those
// are exactly the keys a PattN-shaped template can get wrong — inbound
// `sniff`, for instance, is a legacy field this sing-box refuses to
// start an inbound with at all.
func TestFullTunConfigPassesOfficialCheck(t *testing.T) {
	stubIPv6Gate(t, false, false)
	s := DefaultSettings()
	s.TunEnabled = true
	s.TunMTU = 1420
	s.TunAddress = "10.7.0.1/24"
	s.TunAddress6 = "fc00::172:19:0:1/126"
	s.TunEnableIPv6 = true
	s.TunStrictRoute = true
	s.TunStack = "mixed"
	s.TunIcmp = "reply"
	s.TunRouteExclude = []string{"192.168.1.0/24"}
	raw, err := BuildSingBoxConfig(&Profile{ID: "s", Name: "ss", Core: CoreSingBox,
		Protocol: "shadowsocks", Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}, s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tun-full.json")
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
		if out, err := exec.Command(core, "check", "-c", path).CombinedOutput(); err != nil {
			t.Fatalf("%s rejected the full TUN config: %v\n%s\nconfig: %s", core, err, out, raw)
		}
	}
	if checked == 0 {
		t.Skip("no engine binaries built in the tree")
	}
}

// TestTunGroupRoundTripsThroughDisk: the settings panel edits every
// field, so each needs a json tag and a place in the file. A field that
// silently does not survive the save is worse than one that has no row:
// the panel would show the value the user typed until the next launch.
func TestTunGroupRoundTripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(store, make(chan LogEntry, 8))

	s := store.GetSettings()
	s.TunEnabled = true
	s.TunStack = "gvisor"
	s.TunMTU = 1420
	s.TunAddress = "10.7.0.1/24"
	s.TunAddress6 = "fc00::172:19:0:1/126"
	s.TunEnableIPv6 = true
	on := false
	s.TunAutoRoute = &on
	s.TunStrictRoute = true
	s.TunIcmp = "drop"
	s.TunRouteExclude = []string{"192.168.1.0/24"}
	if err := a.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.GetSettings()
	if !got.TunEnabled || got.TunStack != "gvisor" || got.TunMTU != 1420 {
		t.Errorf("switch/stack/mtu did not survive: %+v", got)
	}
	if got.TunAddress != "10.7.0.1/24" || got.TunAddress6 != "fc00::172:19:0:1/126" || !got.TunEnableIPv6 {
		t.Errorf("addresses did not survive: %+v", got)
	}
	if got.TunAutoRoute == nil || *got.TunAutoRoute != false {
		t.Errorf("auto-route off did not survive: %v", got.TunAutoRoute)
	}
	if !got.TunStrictRoute || got.TunIcmp != "drop" {
		t.Errorf("strict-route/icmp did not survive: %+v", got)
	}
	if len(got.TunRouteExclude) != 1 || got.TunRouteExclude[0] != "192.168.1.0/24" {
		t.Errorf("excluded routes did not survive: %v", got.TunRouteExclude)
	}
}

// TestTunGroupSurvivesAnAbsentSave: a config written before the group
// existed carries none of it, and must not be re-defaulted on read —
// the renderer's unset-value path is what keeps such a host's routes
// where they were.
func TestTunGroupAbsentFromOldConfigKeepsHistoricalShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"profiles":[],"settings":{"mixedPort":2080,"tunEnabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := store.GetSettings()
	if s.TunMTU != 0 || s.TunAddress != "" || s.TunStrictRoute || s.TunEnableIPv6 || len(s.TunRouteExclude) != 0 {
		t.Errorf("an old config gained TUN choices it never made: %+v", s)
	}
	tun := tunInboundOf(t, tunRender(t, func(x *Settings) { *x = s }))
	if tun["mtu"] != float64(tunMTU) || tun["strict_route"] != false {
		t.Errorf("legacy config rendered %v/%v, want the historical shape", tun["mtu"], tun["strict_route"])
	}
}

// TestTunNormalizeLeavesAnUnsetGroupUnset: GetSettings canonicalizes the
// group on the way out, and the panel hands that object straight back to
// SaveSettings. Canonicalizing an unset field into a default makes a
// value the user never chose look like one they picked, and it breaks
// the round-trip ResetToDefaults is tested on.
func TestTunNormalizeLeavesAnUnsetGroupUnset(t *testing.T) {
	s := DefaultSettings()
	s.TunMTU, s.TunAddress, s.TunAddress6, s.TunIcmp = 0, "", "", ""
	s.TunRouteExclude, s.TunEnableIPv6, s.TunStrictRoute = nil, false, false
	normalizeTunSettings(&s)
	if s.TunIcmp != "" || s.TunAddress != "" || s.TunAddress6 != "" || s.TunRouteExclude != nil {
		t.Fatalf("normalizing an unset group filled it: icmp=%q v4=%q v6=%q exclude=%v",
			s.TunIcmp, s.TunAddress, s.TunAddress6, s.TunRouteExclude)
	}
	// Unset still renders the historical values, so leaving them unset
	// costs the config nothing.
	if got := tunEffectiveAddress(s); got != tunAddress {
		t.Errorf("unset address rendered %s, want the historical %s", got, tunAddress)
	}
	if got := tunEffectiveMTU(s); got != tunMTU {
		t.Errorf("unset MTU rendered %d, want the historical %d", got, tunMTU)
	}
	for _, r := range tunRouteRules(s) {
		if m, _ := r.(map[string]any); m["network"] != nil {
			if fmt.Sprint(m["network"]) == "[icmp]" {
				t.Errorf("an unset ICMP policy rendered a rule: %v", m)
			}
		}
	}
}
