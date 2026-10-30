package main

// Regression tests for pinging CoreSpoof profiles (Proxies Test / Test
// All) and for the engine-readiness contract SpoofManager.Start now
// enforces.
//
// The chain under test is fully local (no admin rights, no WinDivert):
//
//	probe client -> ephemeral sing-box mixed inbound -> trojan
//	outbound -> spoof front rewrite -> relay mock ("sni-spoof.exe")
//	-> trojan-over-TLS stand-in -> httptest origin
//
// The relay mock's SPOOFRELAY_DELAY_MS reproduces the real engine's
// asynchronous startup: engine.rs opens its WinDivert handles and
// binds LISTEN_PORT only after process spawn, and prints
// "[INFO] engine RUNNING" after that. Before Start waited for that
// readiness line, the probe raced the bind and its first dial failed
// with a refused connection / "forcibly closed" — the exact error the
// Proxies rows showed for every spoof config.

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildSpoofRelayMock compiles testdata/spoofrelaymock and points the
// SpoofManager exe overrides at it (same pattern as buildSpoofMock).
func buildSpoofRelayMock(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	name := "spoofrelaymock"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	mock := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", mock, "./testdata/spoofrelaymock")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build spoofrelaymock: %v\n%s", err, out)
	}
	oldExe, oldDir := spoofExeOverride, spoofExeDirOverride
	spoofExeOverride = mock
	spoofExeDirOverride = dir
	t.Cleanup(func() {
		spoofExeOverride, spoofExeDirOverride = oldExe, oldDir
		clearSpoofFrontPort()
	})
}

// TestSpoofManagerStartWaitsForEngineReady: with a delayed listener
// (the real engine's WinDivert-open window), Start must not return
// until the engine's own readiness line lands, and a dial right after
// Start must succeed. That is the contract Connect and the latency
// probe rely on.
func TestSpoofManagerStartWaitsForEngineReady(t *testing.T) {
	t.Setenv("SPOOFRELAY_DELAY_MS", "900")
	buildSpoofRelayMock(t)
	m := NewSpoofManager(make(chan LogEntry, 256))
	start := time.Now()
	if err := m.Start(spoofTestProfile(), spoofTestSettings()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Fatalf("Start returned after %v, want >= ~900ms (engine not ready yet)", elapsed)
	}
	port := m.GetListenPort()
	if port <= 0 {
		t.Fatalf("GetListenPort = %d after Start", port)
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatalf("dial engine port right after Start: %v", err)
	}
	c.Close()
}

// TestSpoofManagerStartReportsDeadEngine: the engine process dying
// before its readiness line must fail Start with the engine's own
// [ERROR] text, not a silent success. The relay mock exits when the
// delay is negative (test hook: "-1" = die during startup).
func TestSpoofManagerStartReportsDeadEngine(t *testing.T) {
	t.Setenv("SPOOFRELAY_DELAY_MS", "-1")
	buildSpoofRelayMock(t)
	m := NewSpoofManager(make(chan LogEntry, 256))
	err := m.Start(spoofTestProfile(), spoofTestSettings())
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded although the engine died during startup")
	}
	if !strings.Contains(err.Error(), "mock engine cannot start") {
		t.Fatalf("Start error = %q, want the engine's own [ERROR] text in it", err)
	}
	if m.IsRunning() {
		t.Fatalf("IsRunning = true after failed Start; want false")
	}
}

// startTrojanTLSServer runs a minimal single-password trojan server:
// TLS accept, SHA224(password) hex check, SOCKS-style destination
// parse, then a raw relay to the requested target. The certificate is
// borrowed from a throwaway httptest TLS server; the probe profile
// sets AllowInsecure, so chain validation is skipped on the client.
func startTrojanTLSServer(t *testing.T, password string) (port int) {
	t.Helper()
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	cert := certSrv.TLS.Certificates[0]

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen trojan: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveTrojanConn(conn, cert, password)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func serveTrojanConn(conn net.Conn, cert tls.Certificate, password string) {
	defer conn.Close()
	tconn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err := tconn.Handshake(); err != nil {
		return
	}
	sum := sha256.Sum224([]byte(password))
	want := hex.EncodeToString(sum[:])
	got := make([]byte, 56)
	if _, err := io.ReadFull(tconn, got); err != nil || string(got) != want {
		return
	}
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(tconn, crlf); err != nil {
		return
	}
	// Trojan request: CMD (0x01 CONNECT) + ATYP + addr + port + CRLF.
	cmd := make([]byte, 1)
	if _, err := io.ReadFull(tconn, cmd); err != nil || cmd[0] != 0x01 {
		return
	}
	atyp := make([]byte, 1)
	if _, err := io.ReadFull(tconn, atyp); err != nil {
		return
	}
	var host string
	switch atyp[0] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(tconn, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(tconn, lb); err != nil {
			return
		}
		nb := make([]byte, int(lb[0]))
		if _, err := io.ReadFull(tconn, nb); err != nil {
			return
		}
		host = string(nb)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(tconn, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(tconn, pb); err != nil {
		return
	}
	if _, err := io.ReadFull(tconn, crlf); err != nil {
		return
	}
	target := net.JoinHostPort(host, fmt.Sprint(int(pb[0])<<8|int(pb[1])))
	out, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer out.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(out, tconn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(tconn, out); done <- struct{}{} }()
	<-done
}

// TestSpoofProbeEngineFailureIsReported: a dead engine must fail the
// probe with the engine's own [ERROR] text and must not leave the
// spoof front port published.
func TestSpoofProbeEngineFailureIsReported(t *testing.T) {
	t.Setenv("SPOOFRELAY_DELAY_MS", "-1")
	buildSpoofRelayMock(t)
	coreBin, xrayBin := testCoreBins(t)

	settings := DefaultSettings()
	settings.SNISelectedIP = "127.0.0.1"
	settings.SNISelectedSNI = "decoy.example.test"
	profile := &Profile{
		ID: "spoof-dead", Name: "spoof-dead", Core: CoreSpoof,
		Protocol: "trojan", Address: "decoy.example.test", Port: 443,
		IDOrPassword: "pw", TLS: true, ServerName: "decoy.example.test",
	}

	logs := make(chan LogEntry, 2048)
	cm := NewCoreManager(NewSingBoxManager(coreBin, logs), NewXrayManager(xrayBin, "", logs))
	cm.SetSpoofManager(NewSpoofManager(logs))

	_, err := cm.EphemeralURLTest(profile, settings, "http://127.0.0.1:1/", 3000, probeOwnerProxies)
	if err == nil {
		t.Fatal("probe succeeded although the engine died during startup")
	}
	if !strings.Contains(err.Error(), "mock engine cannot start") {
		t.Fatalf("probe error = %q, want the engine's own [ERROR] text", err)
	}
	if _, live := currentSpoofFrontPort(); live {
		t.Fatal("spoof front port still published after failed engine start")
	}
}

func TestSpoofProfileEphemeralPing(t *testing.T) {
	// Engine bring-up slower than the ephemeral core's dial: the old
	// code failed here, the readiness wait absorbs it.
	t.Setenv("SPOOFRELAY_DELAY_MS", "1500")
	buildSpoofRelayMock(t)
	coreBin, xrayBin := testCoreBins(t)

	var hits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, "spoof-probe-ok")
	}))
	defer origin.Close()

	trojanPort := startTrojanTLSServer(t, "pw")

	settings := DefaultSettings()
	settings.SNISelectedIP = "127.0.0.1"
	settings.SNISelectedSNI = "decoy.example.test"

	profile := &Profile{
		ID: "spoof-eph", Name: "spoof-eph", Core: CoreSpoof,
		Protocol: "trojan", Address: "decoy.example.test", Port: trojanPort,
		IDOrPassword: "pw", TLS: true, ServerName: "decoy.example.test",
		AllowInsecure: true,
	}

	logs := make(chan LogEntry, 2048)
	cm := NewCoreManager(NewSingBoxManager(coreBin, logs), NewXrayManager(xrayBin, "", logs))
	cm.SetSpoofManager(NewSpoofManager(logs))

	start := time.Now()
	res, err := cm.EphemeralURLTest(profile, settings, origin.URL+"/", 15000, probeOwnerProxies)
	if err != nil {
		drainLogs(t, logs)
		t.Fatalf("EphemeralURLTest: %v", err)
	}
	if res == nil || res.Error != "" || res.LatencyMs < 0 {
		drainLogs(t, logs)
		t.Fatalf("bad probe result: %+v", res)
	}
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Fatalf("probe returned after %v, want >= engine delay", elapsed)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		drainLogs(t, logs)
		t.Fatalf("origin hits = %d, want 1 (probe must measure a real round trip, not a proxy error page)", got)
	}
	t.Logf("spoof probe latency: %d ms", res.LatencyMs)
}
