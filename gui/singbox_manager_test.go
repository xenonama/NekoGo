package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeDeathBinary returns a system executable that exits (almost)
// immediately regardless of arguments, so Start's early-death probe
// can be exercised without a real core.
func fakeDeathBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		p, err := exec.LookPath("whoami.exe")
		if err != nil {
			t.Skip("whoami.exe not found")
		}
		return p
	}
	for _, c := range []string{"/bin/false", "/usr/bin/false"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("no instant-exit binary found")
	return ""
}

// TestSingboxStartDetectsImmediateDeath — a binary that dies on launch
// must fail Start fast (well under the 15s RPC deadline), not spin the
// full timeout and not leak the handle (watchChild owns Wait).
func TestSingboxStartDetectsImmediateDeath(t *testing.T) {
	m := NewSingBoxManager(fakeDeathBinary(t), make(chan LogEntry, 64))
	p := &Profile{ID: "x", Name: "dead", Core: CoreSingBox, Protocol: "shadowsocks",
		Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
	start := time.Now()
	err := m.Start(p, DefaultSettings())
	elapsed := time.Since(start)
	if err == nil {
		_ = m.Stop()
		t.Fatal("expected error for an instantly-dying core binary")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Start took %v, want fail-fast well under the 15s RPC deadline", elapsed)
	}
	if m.IsRunning() {
		_ = m.Stop()
		t.Fatal("manager still running after failed Start")
	}
	t.Logf("fail-fast after %v: %v", elapsed, err)
}

// coreBinaryForTest locates a real nekobox_core for the Stop test;
// empty when absent (the test then skips).
func coreBinaryForTest() string {
	for _, c := range []string{
		filepath.Join("build", "bin", "nekobox_core.exe"),
		filepath.Join("build", "bin", "nekobox_core"),
	} {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
			return c
		}
	}
	if p, err := exec.LookPath("nekobox_core"); err == nil {
		return p
	}
	if p, err := exec.LookPath("nekobox_core.exe"); err == nil {
		return p
	}
	return ""
}

// TestSingboxStopWaitsForChild — with a real core, Stop kills and the
// watchChild reaping flips running=false promptly.
func TestSingboxStopWaitsForChild(t *testing.T) {
	bin := coreBinaryForTest()
	if bin == "" {
		t.Skip("nekobox_core binary absent")
	}
	mixed, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.MixedPort = mixed
	m := NewSingBoxManager(bin, make(chan LogEntry, 512))
	p := &Profile{ID: "x", Name: "direct", Core: CoreSingBox}
	if err := m.Start(p, s); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !m.IsRunning() {
		t.Fatal("not running after Start")
	}
	start := time.Now()
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if m.IsRunning() {
		t.Fatal("still running after Stop")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Stop took %v, want prompt reaping (<=2s watch)", elapsed)
	}
}

// TestSingboxGetStatsNotRunning — idle manager reports not-running
// without ApiDown and without error.
func TestSingboxGetStatsNotRunning(t *testing.T) {
	m := NewSingBoxManager("/nonexistent-core-xyz", make(chan LogEntry, 8))
	st, err := m.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.ApiDown {
		t.Fatalf("got %+v, want idle snapshot", st)
	}
}

// TestURLTestUsesDedicatedTransport (BUG-001): URLTest must dial a fresh
// transport via sbTestDial and never touch the shared m.client. With
// running=true, a closed-loopback addr, and m.client == nil, the old
// shared-client path returned "core not running" immediately; the fixed
// path attempts a fresh dial and returns a transport open error instead.
func TestURLTestUsesDedicatedTransport(t *testing.T) {
	m := NewSingBoxManager("/nonexistent-core-xyz", make(chan LogEntry, 8))
	m.mu.Lock()
	m.running = true
	m.addr = closedLoopbackPort(t)
	m.client = nil
	m.trans = nil
	m.mu.Unlock()
	_, err := m.URLTest("https://example.com/", 1000)
	if err == nil {
		t.Fatal("expected dial error against closed port, got nil")
	}
	if err.Error() == "core not running" {
		t.Fatalf("URLTest used the shared-client gate (m.client==nil -> %q); want a dedicated-dial attempt", err)
	}
}

// TestSingBoxRealityWithoutTLSToggle: the profile form keeps TLS and
// REALITY as independent checkboxes, and a hand-made or hand-edited
// profile can therefore carry RealityEnabled with TLS off. sing-box
// nests reality INSIDE the tls object, so the gate that decides whether
// to render tls at all had to include RealityEnabled; the xray, mihomo
// and fingerprint paths all do, and sing-box's left it out — the profile
// then went out as plaintext vless to a REALITY port and every
// connection failed with nothing to show for it. The same profile
// connects on the other engines.
func TestSingBoxRealityWithoutTLSToggle(t *testing.T) {
	p := &Profile{Protocol: "vless", Address: "r.example.com", Port: 443,
		IDOrPassword:   "11111111-2222-4333-8444-555555555555",
		RealityEnabled: true, RealityPubKey: "pubkey", RealityShortID: "shortid"}
	if p.TLS || p.Flow != "" {
		t.Fatal("rig is wrong: the case under test is REALITY without TLS or a flow")
	}
	out, err := mapProfileToSingBoxOutbound(p)
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	tlsObj, ok := out["tls"].(map[string]any)
	if !ok {
		t.Fatalf("REALITY profile rendered no tls object, so it dials plaintext: %v", out)
	}
	if enabled, _ := tlsObj["enabled"].(bool); !enabled {
		t.Fatalf("tls rendered but not enabled: %v", tlsObj)
	}
	reality, ok := tlsObj["reality"].(map[string]any)
	if !ok {
		t.Fatalf("no reality block inside tls: %v", tlsObj)
	}
	if reality["public_key"] != "pubkey" || reality["short_id"] != "shortid" {
		t.Fatalf("reality keys = %v", reality)
	}
	if _, ok := out["flow"]; ok {
		t.Fatalf("flow rendered from an empty profile field: %v", out["flow"])
	}

	// Ticking TLS alongside REALITY took this branch already; the render
	// must not change.
	ticked := *p
	ticked.TLS = true
	out2, err := mapProfileToSingBoxOutbound(&ticked)
	if err != nil {
		t.Fatalf("outbound with TLS ticked: %v", err)
	}
	tls2, _ := out2["tls"].(map[string]any)
	if _, ok := tls2["reality"].(map[string]any); !ok {
		t.Fatalf("TLS+REALITY lost its reality block: %v", tls2)
	}

	// A profile with neither stays plaintext: the gate must not start
	// emitting tls for everything.
	plain := &Profile{Protocol: "vless", Address: "p.example.com", Port: 443,
		IDOrPassword: "11111111-2222-4333-8444-555555555555"}
	out3, err := mapProfileToSingBoxOutbound(plain)
	if err != nil {
		t.Fatalf("plain outbound: %v", err)
	}
	if _, ok := out3["tls"]; ok {
		t.Fatalf("plain vless profile rendered tls: %v", out3["tls"])
	}
}

// TestSingBoxVMessSecurityIsAnEnumValue: sing-box declares the vmess
// cipher as an enum (option/vmess.go:21 —
// auto,none,zero,aes-128-cfb,aes-128-gcm,chacha20-poly1305) and matches
// it case-sensitively, so anything else fails at config load and the
// profile cannot connect at all. import.go stores a link's scy= verbatim
// and legacy panels keep writing the old spellings, so the renderer
// folds the aliases instead of passing the text through. Xray lowercases
// the same field and defaults an unknown name to auto, which is the
// behaviour this matches for anything not in the table.
func TestSingBoxVMessSecurityIsAnEnumValue(t *testing.T) {
	// The enum, spelled exactly as the engine declares it.
	valid := []string{"auto", "none", "zero", "aes-128-cfb", "aes-128-gcm", "chacha20-poly1305"}
	for _, v := range valid {
		out := vmessSecuritySingBox(v)
		if out != v {
			t.Errorf("security %q rendered %q, want it unchanged (an enum value)", v, out)
		}
	}
	legacy := []struct{ got, want string }{
		{"", "auto"},
		{"chacha20-ietf-poly1305", "chacha20-poly1305"}, // the v2rayN spelling
		{"ChaCha20-Poly1305", "chacha20-poly1305"},
		{"CHACHA20-POLY1305", "chacha20-poly1305"},
		{"AES-128-GCM", "aes-128-gcm"},
		{"aes128-gcm", "aes-128-gcm"},
		{"id-aes128-gcm", "aes-128-gcm"},
		{"id-aes128-cfb", "aes-128-cfb"},
		{"aes-128-cfb", "aes-128-cfb"},
		{"auto ", "auto"},
		{"Zero", "zero"},
		{"none", "none"},
		{"banana", "auto"},      // unknown: xray's own default, not a dead profile
		{"aes-256-gcm", "auto"}, // no such vmess cipher in the enum
	}
	for _, c := range legacy {
		if out := vmessSecuritySingBox(c.got); out != c.want {
			t.Errorf("security %q rendered %q, want %q", c.got, out, c.want)
		}
	}

	// The rendered outbound must carry the folded value, and an empty
	// field must still default to auto the way the builder always did.
	p := &Profile{Protocol: "vmess", Address: "1.2.3.4", Port: 443,
		IDOrPassword: "11111111-2222-4333-8444-555555555555", Security: "chacha20-ietf-poly1305"}
	out, err := mapProfileToSingBoxOutbound(p)
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	if out["security"] != "chacha20-poly1305" {
		t.Fatalf("outbound security = %v, want chacha20-poly1305", out["security"])
	}
	p.Security = ""
	out, err = mapProfileToSingBoxOutbound(p)
	if err != nil {
		t.Fatalf("empty outbound: %v", err)
	}
	if out["security"] != "auto" {
		t.Fatalf("empty security = %v, want auto", out["security"])
	}
}

// TestSingBoxSocksHTTPAuth — parseSocks stores the credential as
// "user:pass" in IDOrPassword (and buildSocksLink splits it back out on
// export), so the rendered outbound has to carry username/password:
// without them the core dials unauthenticated and the server refuses the
// connection while the UI shows a saved password.
func TestSingBoxSocksHTTPAuth(t *testing.T) {
	cases := []struct{ proto, cred, user, pass string }{
		{"socks", "user:pass", "user", "pass"},
		{"http", "user:pass", "user", "pass"},
		{"socks", "user:p:a:ss", "user", "p:a:ss"},
		{"socks", "solo", "solo", ""},
		{"http", "  ", "", ""},
		{"socks", "", "", ""},
	}
	for _, c := range cases {
		p := &Profile{Protocol: c.proto, Address: "1.2.3.4", Port: 1080, IDOrPassword: c.cred}
		out, err := mapProfileToSingBoxOutbound(p)
		if err != nil {
			t.Fatalf("%s: %v", c.proto, err)
		}
		user, _ := out["username"].(string)
		pass, _ := out["password"].(string)
		if user != c.user || pass != c.pass {
			t.Fatalf("%s cred %q: username=%q password=%q, want %q/%q",
				c.proto, c.cred, user, pass, c.user, c.pass)
		}
		if c.user == "" {
			if _, ok := out["username"]; ok {
				t.Fatalf("%s: username rendered for credential %q", c.proto, c.cred)
			}
		}
	}
}
