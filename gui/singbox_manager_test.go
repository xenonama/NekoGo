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
