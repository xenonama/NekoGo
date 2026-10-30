package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWriteXrayConfigPermissions — the helper must persist the exact bytes
// with owner-only access (0600). Exact-mode assertion is POSIX-only:
// Windows ACLs do not express Unix permission bits.
func TestWriteXrayConfigPermissions(t *testing.T) {
	dir := t.TempDir()
	cfg := []byte(`{"outbounds":[{"password":"s3cr3t-uuid"}]}`)
	p, err := writeXrayConfig(dir, cfg)
	if err != nil {
		t.Fatalf("writeXrayConfig: %v", err)
	}
	back, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(back) != string(cfg) {
		t.Fatal("config bytes changed by writeXrayConfig")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("config perm = %o, want 600", perm)
		}
	} else {
		t.Logf("config perm on Windows = %o (ACL-governed, best effort)", fi.Mode().Perm())
	}
}

// TestXrayStopCleansWorkDir — Stop must delete the temp config dir so
// secrets never linger in %TEMP% after Disconnect.
func TestXrayStopCleansWorkDir(t *testing.T) {
	dir, err := os.MkdirTemp("", "nekobox-xray")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeXrayConfig(dir, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	m := NewXrayManager("", "", nil)
	m.workDir = dir
	m.running = true // simulate a live instance with no process handle
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("work dir still exists after Stop: %s", dir)
	}
	if m.workDir != "" {
		t.Fatal("workDir not cleared after Stop")
	}
}

// leftovers lists nekobox-xray* temp dirs (both main and ephemeral prefixes).
func xrayLeftovers(t *testing.T) map[string]bool {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "nekobox-xray*"))
	set := map[string]bool{}
	for _, m := range matches {
		set[m] = true
	}
	return set
}

// TestXrayStartEarlyDeath — with the mixed port occupied, xray cannot bind
// http-in. Start must report that fast and honestly: the HOTFIX pre-flight
// check rejects the occupied port immediately with the "in use" zombie hint
// (no fake success, no 5s API spin), and no temp dir may leak.
func TestXrayStartEarlyDeath(t *testing.T) {
	bin, err := filepath.Abs(filepath.Join("build", "bin", "xray.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("xray binary not built: %s", bin)
	}
	// Occupy what will become the http-in port.
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	mixedPort := holder.Addr().(*net.TCPAddr).Port

	// The API listener must avoid both the occupied http-in port and the
	// SOCKS listener on mixedPort+1 (BuildXrayConfig now rejects those
	// collisions outright); distinctPort skips all of them.
	apiPort, err := distinctPort(mixedPort, mixedPort+1)
	if err != nil {
		t.Fatal(err)
	}
	m := NewXrayManager(bin, "", nil)
	profile := &Profile{
		Name: "early-death", Core: CoreXray, Protocol: "vless",
		Address: "127.0.0.1", Port: 19999,
		IDOrPassword: "11111111-2222-4333-8444-555555555555",
		Network:      "tcp",
	}
	settings := DefaultSettings()
	settings.MixedPort = mixedPort
	settings.XrayAPIAddress = "127.0.0.1:" + strconv.Itoa(apiPort)

	before := xrayLeftovers(t)
	start := time.Now()
	err = m.Start(profile, settings)
	elapsed := time.Since(start)
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded with an occupied mixed port, want early-death error")
	}
	if !strings.Contains(err.Error(), "in use") ||
		!strings.Contains(err.Error(), strconv.Itoa(mixedPort)) {
		t.Fatalf("error = %q, want the port-in-use fast failure naming %d", err, mixedPort)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("early death took %v, want a fast failure (no 5s API spin)", elapsed)
	}
	t.Logf("early death reported in %v: %v", elapsed.Round(time.Millisecond), err)
	if m.IsRunning() {
		_ = m.Stop()
		t.Fatal("manager reports running after a failed Start")
	}
	if m.workDir != "" {
		t.Fatalf("workDir not cleaned after failed Start: %s", m.workDir)
	}
	for dir := range xrayLeftovers(t) {
		if !before[dir] {
			t.Fatalf("temp config leaked after failed Start: %s", dir)
		}
	}
}
