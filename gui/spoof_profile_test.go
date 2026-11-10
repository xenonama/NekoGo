package main

// Tests for the sni-spoof profile eligibility rule
// (spoofEligibilityIssue: shared by the link-import auto-detect and
// the SNI tab's SpoofUseProfile) and the scan-health ordering of the
// engine failover pools (buildSpoofEndpoints / buildSpoofSNIs).

import (
	"net"
	"runtime"
	"strings"
	"testing"
)

func TestSpoofEligibilityIssue(t *testing.T) {
	base := func() *Profile {
		return &Profile{
			ID: "p1", Name: "P", Core: CoreSingBox, Protocol: "trojan",
			Address: "example.com", Port: 443, IDOrPassword: "pw",
			TLS: true, Network: "ws",
		}
	}
	tests := []struct {
		name string
		mod  func(*Profile)
		want string // error substring; empty = eligible
	}{
		{name: "trojan tls ws", mod: func(p *Profile) {}, want: ""},
		{name: "vless tls ws", mod: func(p *Profile) { p.Protocol = "vless" }, want: ""},
		{name: "loopback ws", mod: func(p *Profile) { p.Address = "127.0.0.1"; p.Port = 40443 }, want: ""},
		{name: "shadowsocks", mod: func(p *Profile) { p.Protocol = "ss" }, want: "trojan or vless"},
		{name: "no tls", mod: func(p *Profile) { p.TLS = false }, want: "needs TLS"},
		{name: "reality", mod: func(p *Profile) { p.RealityEnabled = true }, want: "REALITY"},
		{name: "xtls flow", mod: func(p *Profile) { p.Flow = "xtls-rprx-vision" }, want: "flow"},
		{name: "tcp transport", mod: func(p *Profile) { p.Network = "tcp" }, want: `transport "tcp"`},
		{name: "grpc transport", mod: func(p *Profile) { p.Network = "grpc" }, want: `transport "grpc"`},
		{name: "no address", mod: func(p *Profile) { p.Address = "  " }, want: "no address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base()
			tt.mod(p)
			err := spoofEligibilityIssue(p)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("spoofEligibilityIssue = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("spoofEligibilityIssue = %v, want containing %q", err, tt.want)
			}
		})
	}
	if err := spoofEligibilityIssue(nil); err == nil {
		t.Fatal("spoofEligibilityIssue(nil) = nil, want error")
	}
}

// TestIsDomainAddress pins the import auto-tag split: bare domains
// (prompt.txt Test-CF) auto-tag, while IP literals (prompt.txt
// normal), loopback, and empties never do.
func TestIsDomainAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"www.calmlunch.com":           true,
		"nat3.xxai.dpdns.org":         true,
		"example.com.":                true,
		"2a06:98c1:3101::6812:202f":   false,
		"[2a06:98c1:3101::6812:202f]": false,
		"104.18.9.83":                 false,
		"127.0.0.1":                   false,
		"localhost":                   false,
		"":                            false,
		"  ":                          false,
		"nodot":                       false,
	} {
		if got := isDomainAddress(addr); got != want {
			t.Errorf("isDomainAddress(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestSpoofUseProfile(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	loop := Profile{
		ID: "appxa", Name: "APPXA", Core: CoreSingBox, Protocol: "trojan",
		Address: "127.0.0.1", Port: 40443, IDOrPassword: "pw",
		TLS: true, Network: "ws", ServerName: "www.calmlunch.com", Path: "/assignment",
	}
	spoofed := Profile{
		ID: "test-cf", Name: "Test-CF", Core: CoreSpoof, Protocol: "trojan",
		Address: "www.calmlunch.com", Port: 443, IDOrPassword: "pw",
		TLS: true, Network: "ws",
	}
	reality := Profile{
		ID: "real-1", Name: "R", Core: CoreXray, Protocol: "vless",
		Address: "example.com", Port: 443, IDOrPassword: "pw",
		TLS: true, Network: "ws", RealityEnabled: true, Flow: "xtls-rprx-vision",
	}
	for _, p := range []Profile{loop, spoofed, reality} {
		if _, err := store.SaveProfile(p); err != nil {
			t.Fatalf("SaveProfile(%s): %v", p.Name, err)
		}
	}
	a := &App{store: store}

	got, err := a.SpoofUseProfile("appxa")
	if err != nil {
		t.Fatalf("SpoofUseProfile(loopback): %v", err)
	}
	if got.Core != CoreSpoof {
		t.Fatalf("returned core = %v, want CoreSpoof", got.Core)
	}
	if p, _ := store.GetProfile("appxa"); p.Core != CoreSpoof {
		t.Fatalf("persisted core = %v, want CoreSpoof (switch not saved)", p.Core)
	}

	// Already-spoof: no-op success.
	again, err := a.SpoofUseProfile("test-cf")
	if err != nil || again.Core != CoreSpoof {
		t.Fatalf("SpoofUseProfile(already spoof) = %v, %v; want spoof, nil", again.Core, err)
	}

	// Missing profile.
	if _, err := a.SpoofUseProfile("nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("SpoofUseProfile(missing) = %v, want not-found", err)
	}

	// Ineligible: named error, core untouched.
	if _, err := a.SpoofUseProfile("real-1"); err == nil || !strings.Contains(err.Error(), "REALITY") {
		t.Fatalf("SpoofUseProfile(reality) = %v, want REALITY error", err)
	}
	if p, _ := store.GetProfile("real-1"); p.Core != CoreXray {
		t.Fatalf("ineligible profile core = %v, want untouched CoreXray", p.Core)
	}

	// The switch is logged to the spoof source for the tab's log card.
	found := false
	a.logMu.RLock()
	for _, e := range a.logBuf {
		if e.Source == "spoof" && strings.Contains(e.Message, "APPXA") && strings.Contains(e.Message, "SNI spoof core") {
			found = true
		}
	}
	a.logMu.RUnlock()
	if !found {
		t.Fatal("no spoof log line recorded for the core switch")
	}
}

// TestShouldRescueLoopbackProfile — Connect's on-the-fly rescue
// decision: convert engine-eligible loopback profiles whose local
// port has NO listener (the legacy dead-end), never hijack a live
// listener (legitimate local chain), never touch other shapes.
func TestShouldRescueLoopbackProfile(t *testing.T) {
	base := func() Profile {
		return Profile{
			ID: "appxa", Name: "APPXA", Core: CoreSingBox, Protocol: "trojan",
			Address: "127.0.0.1", Port: 40443, IDOrPassword: "pw",
			TLS: true, Network: "ws", ServerName: "www.calmlunch.com", Path: "/assignment",
		}
	}
	// Reserve a port, release it: guaranteed dead.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	dead := base()
	dead.Port = deadPort
	if !shouldRescueLoopbackProfile(&dead, DefaultSettings()) {
		t.Fatal("dead eligible loopback: want rescue")
	}
	if shouldRescueLoopbackProfile(nil, DefaultSettings()) {
		t.Fatal("nil profile: want no rescue")
	}

	// Live listener on the profile port = legitimate local chain.
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen live: %v", err)
	}
	defer live.Close()
	alive := base()
	alive.Port = live.Addr().(*net.TCPAddr).Port
	if shouldRescueLoopbackProfile(&alive, DefaultSettings()) {
		t.Fatal("live listener: want no rescue (never hijack)")
	}

	spoofed := base()
	spoofed.Core = CoreSpoof
	if shouldRescueLoopbackProfile(&spoofed, DefaultSettings()) {
		t.Fatal("already spoof core: want no rescue")
	}
	direct := base()
	direct.Address = "www.calmlunch.com"
	if shouldRescueLoopbackProfile(&direct, DefaultSettings()) {
		t.Fatal("non-loopback address: want no rescue")
	}
	tcpT := base()
	tcpT.Network = "tcp"
	if shouldRescueLoopbackProfile(&tcpT, DefaultSettings()) {
		t.Fatal("ineligible transport: want no rescue")
	}
}

// TestShouldRescueDirectSpoofProfile pins the Test-CF rule: an
// eligible direct-domain profile on a normal core rescues (import
// would tag it spoof today), while spoof/IP-literal/ineligible/nil
// never do.
func TestShouldRescueDirectSpoofProfile(t *testing.T) {
	base := func() Profile {
		return Profile{
			ID: "test-cf", Name: "Test-CF", Core: CoreSingBox, Protocol: "trojan",
			Address: "www.calmlunch.com", Port: 443, IDOrPassword: "pw",
			TLS: true, Network: "ws", ServerName: "www.calmlunch.com", Path: "/assignment",
		}
	}
	if p := base(); !shouldRescueDirectSpoofProfile(&p, DefaultSettings()) {
		t.Fatal("eligible direct domain: want rescue")
	}
	if shouldRescueDirectSpoofProfile(nil, DefaultSettings()) {
		t.Fatal("nil profile: want no rescue")
	}
	spoofed := base()
	spoofed.Core = CoreSpoof
	if shouldRescueDirectSpoofProfile(&spoofed, DefaultSettings()) {
		t.Fatal("already spoof core: want no rescue")
	}
	lit := base()
	lit.Address = "104.18.9.83"
	if shouldRescueDirectSpoofProfile(&lit, DefaultSettings()) {
		t.Fatal("IP literal: want no rescue (never auto-tag)")
	}
	v6 := base()
	v6.Address = "2a06:98c1:3101::6812:202f"
	if shouldRescueDirectSpoofProfile(&v6, DefaultSettings()) {
		t.Fatal("IPv6 literal (normal config): want no rescue")
	}
	tcpT := base()
	tcpT.Network = "tcp"
	if shouldRescueDirectSpoofProfile(&tcpT, DefaultSettings()) {
		t.Fatal("ineligible transport: want no rescue")
	}
}

// TestConnectRescuesDeadLoopbackProfile — the end-to-end effect:
// connecting the legacy core-0 APPXA (nothing on 127.0.0.1:40443)
// converts + persists it to CoreSpoof BEFORE the core starts, so the
// engine + transport run instead of dialing a dead port.
func TestConnectRescuesDeadLoopbackProfile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("loopback rescue is Windows-only (sni-spoof.exe)")
	}
	// The rescue converts this profile to the spoof core, and a spoof connect
	// now needs an elevated process for WinDivert. This test is about the
	// conversion, not the driver, so declare the side explicitly (the
	// elevation gate has its own tests in elevate_test.go).
	withElevated(t, true)
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	s := DefaultSettings()
	s.SystemProxy = false // no OS-proxy side effects in tests
	if err := store.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	saved, err := store.SaveProfile(Profile{
		Name: "APPXA", Core: CoreSingBox, Protocol: "trojan",
		Address: "127.0.0.1", Port: deadPort, IDOrPassword: "pw",
		TLS: true, Network: "ws", ServerName: "www.calmlunch.com",
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	var started Profile
	app.coreStarter = func(p *Profile, _ Settings) error {
		started = *p
		return nil
	}
	if err := app.Connect(saved.ID); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if started.Core != CoreSpoof {
		t.Fatalf("connect started core %v, want CoreSpoof (rescue)", started.Core)
	}
	if got, _ := store.GetProfile(saved.ID); got.Core != CoreSpoof {
		t.Fatalf("persisted core = %v, want CoreSpoof (conversion not saved)", got.Core)
	}
	found := false
	app.logMu.RLock()
	for _, e := range app.logBuf {
		if e.Source == "spoof" && strings.Contains(e.Message, "SNI spoof core") {
			found = true
		}
	}
	app.logMu.RUnlock()
	if !found {
		t.Fatal("no spoof log line for the rescue conversion")
	}
}

func TestBuildSpoofEndpointsOrdersByHealth(t *testing.T) {
	s := Settings{}
	s.SNIIPs = []SNIIP{
		{IP: "203.0.113.9", LastAt: "2026-10-16T12:00:00Z"},                             // failed
		{IP: "203.0.113.2", LastOK: true, LastMs: 5000, LastAt: "2026-10-16T12:00:00Z"}, // ok, slow
		{IP: "203.0.113.3"}, // never scanned
		{IP: "203.0.113.1", LastOK: true, LastMs: 120, LastAt: "2026-10-16T12:00:00Z"}, // ok, fast
		{IP: "203.0.113.8", LastAt: "2026-10-16T12:00:00Z"},                            // failed
	}
	eps := buildSpoofEndpoints("198.41.128.1", 443, s)
	want := []string{"198.41.128.1", "203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.9", "203.0.113.8"}
	if len(eps) != len(want) {
		t.Fatalf("ENDPOINTS has %d entries, want %d", len(eps), len(want))
	}
	for i, e := range eps {
		if got := e.(map[string]any)["ip"]; got != want[i] {
			t.Fatalf("ENDPOINTS[%d] = %v, want %s", i, got, want[i])
		}
	}
	if s.SNIIPs[0].IP != "203.0.113.9" {
		t.Fatal("buildSpoofEndpoints mutated the input settings slice")
	}
}

func TestBuildSpoofSNIsOrdersByHealth(t *testing.T) {
	s := Settings{}
	s.SNISNIs = []SNISNI{
		{Host: "dead.example.com", LastAt: "2026-10-16T12:00:00Z"},
		{Host: "slow.example.com", LastOK: true, LastMs: 900, LastAt: "2026-10-16T12:00:00Z"},
		{Host: "fresh.example.com"},
		{Host: "fast.example.com", LastOK: true, LastMs: 80, LastAt: "2026-10-16T12:00:00Z"},
	}
	snis := buildSpoofSNIs("Primary.Example.COM.", s)
	want := []any{"primary.example.com", "fast.example.com", "slow.example.com", "fresh.example.com", "dead.example.com"}
	if len(snis) != len(want) {
		t.Fatalf("FAKE_SNIS has %d entries, want %d: %v", len(snis), len(want), snis)
	}
	for i, got := range snis {
		if got != want[i] {
			t.Fatalf("FAKE_SNIS[%d] = %v, want %v", i, got, want[i])
		}
	}
}

// Unchecked entries must never reach the engine pools: the checkboxes
// are the single on/off switch (nil Selected = legacy = active).
func TestBuildSpoofPoolsSkipUnchecked(t *testing.T) {
	no := false
	s := Settings{}
	s.SNIIPs = []SNIIP{
		{IP: "203.0.113.1"},
		{IP: "203.0.113.2", Selected: &no},
		{IP: "203.0.113.3", Selected: &no},
		{IP: "203.0.113.4"},
	}
	eps := buildSpoofEndpoints("198.41.128.1", 443, s)
	want := []string{"198.41.128.1", "203.0.113.1", "203.0.113.4"}
	if len(eps) != len(want) {
		t.Fatalf("ENDPOINTS has %d entries, want %d: %v", len(eps), len(want), eps)
	}
	for i, e := range eps {
		if got := e.(map[string]any)["ip"]; got != want[i] {
			t.Fatalf("ENDPOINTS[%d] = %v, want %s", i, got, want[i])
		}
	}
	s.SNISNIs = []SNISNI{
		{Host: "keep.example.com"},
		{Host: "drop.example.com", Selected: &no},
	}
	snis := buildSpoofSNIs("primary.example.com", s)
	if len(snis) != 2 || snis[0] != "primary.example.com" || snis[1] != "keep.example.com" {
		t.Fatalf("FAKE_SNIS = %v, want [primary.example.com keep.example.com]", snis)
	}
	// All unchecked: the pools fall back to the primary alone.
	s.SNIIPs = []SNIIP{{IP: "203.0.113.2", Selected: &no}}
	s.SNISNIs = []SNISNI{{Host: "drop.example.com", Selected: &no}}
	if eps := buildSpoofEndpoints("198.41.128.1", 443, s); len(eps) != 1 {
		t.Fatalf("all-unchecked ENDPOINTS has %d entries, want primary only", len(eps))
	}
	if snis := buildSpoofSNIs("primary.example.com", s); len(snis) != 1 {
		t.Fatalf("all-unchecked FAKE_SNIS = %v, want primary only", snis)
	}
}
