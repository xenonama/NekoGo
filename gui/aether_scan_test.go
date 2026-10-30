package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestAetherScanRefusesWhileEngineRuns: a scan opens a second tunnel on
// the same WARP identity, so the live engine blocks it (PattNG's
// AetherSession.disturbedByScanOf).
func TestAetherScanRefusesWhileEngineRuns(t *testing.T) {
	a := &App{aetherMgr: &AetherManager{running: true}}
	if _, err := a.AetherScan(Profile{Protocol: "aether"}); err == nil || !strings.Contains(err.Error(), "before scanning") {
		t.Fatalf("scan while running: %v", err)
	}
}

func TestAetherScanRequiresAetherProfile(t *testing.T) {
	a := &App{}
	if _, err := a.AetherScan(Profile{Protocol: "vless"}); err == nil {
		t.Fatal("scan accepted a non-Aether profile")
	}
}

// TestBuildAetherScanArguments: a scan looks for the endpoints afresh,
// so the saved peer is dropped (AetherCoreManager.buildArguments'
// scan=true) and the quick-reconnect cache is off.
func TestBuildAetherScanArguments(t *testing.T) {
	// One-hop with a saved endpoint.
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg"}}
	args := buildAetherScanArguments(p, 10819)
	if containsArg(args, "--peer") {
		t.Fatalf("one-hop scan kept --peer: %v", args)
	}
	if !containsArg(args, "--no-quick-reconnect") || containsArg(args, "--quick-reconnect") {
		t.Fatalf("one-hop scan reconnect flags: %v", args)
	}
	if !containsArg(args, "--scan") || !containsArg(args, "balanced") {
		t.Fatalf("one-hop scan mode: %v", args)
	}

	// Two-hop with one saved hop still scans both hops.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t",
		Aether: aetherSettings{Protocol: "gool", WiwOuter: "162.159.192.3:443"}}
	args = buildAetherScanArguments(p, 10819)
	if containsArg(args, "--wiw-outer") || containsArg(args, "--wiw-inner") {
		t.Fatalf("two-hop scan kept saved hops: %v", args)
	}
	if !containsArg(args, "--wiw-scan") {
		t.Fatalf("two-hop scan flag missing: %v", args)
	}
	if !containsArg(args, "--no-quick-reconnect") || containsArg(args, "--quick-reconnect") {
		t.Fatalf("two-hop scan reconnect flags: %v", args)
	}

	p.Aether = aetherSettings{Protocol: "mim", WiwInner: "162.159.192.4:443"}
	args = buildAetherScanArguments(p, 10819)
	if containsArg(args, "--mim-outer") || containsArg(args, "--mim-inner") || !containsArg(args, "--mim-scan") {
		t.Fatalf("mim scan args: %v", args)
	}

	// WireGuard over MASQUE scans only the MASQUE gateway; the inner
	// WireGuard endpoint stays the profile's.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t",
		Aether: aetherSettings{Protocol: "wg-over-masque", WiwOuter: "162.159.197.3:443", WiwInner: "162.159.197.4:2408"}}
	args = buildAetherScanArguments(p, 10819)
	if containsArg(args, "--peer") {
		t.Fatalf("wg-over-masque scan kept outer peer: %v", args)
	}
	if !containsArg(args, "--gool-peer") {
		t.Fatalf("wg-over-masque scan dropped inner peer: %v", args)
	}
	for i, arg := range args {
		if arg == "--gool-peer" && (i+1 >= len(args) || args[i+1] != "162.159.197.4:2408") {
			t.Fatalf("wg-over-masque inner peer value: %v", args)
		}
	}
	if strings.Contains(strings.Join(args, " "), "--wiw-scan") {
		t.Fatalf("wg-over-masque scan used --wiw-scan: %v", args)
	}
}

// TestAetherScanParse: the core's endpoint lines, protocol by protocol
// (PattNG's AetherScanner.parse).
func TestAetherScanParse(t *testing.T) {
	one := func(protocol, line, host string, port int) {
		t.Helper()
		r := aetherScanParse(protocol, line)
		if r == nil || r.Host != host || r.Port != port {
			t.Fatalf("%s parse %q: %+v", protocol, line, r)
		}
	}
	one("masque", "selected MASQUE gateway 162.159.192.1:443", "162.159.192.1", 443)
	one("wg", "selected WireGuard endpoint 162.159.192.2:2408", "162.159.192.2", 2408)
	one("wg-over-masque", "gool ready: masque 162.159.192.7:443 carries wireguard 162.159.192.8:2408", "162.159.192.7", 443)
	if r := aetherScanParse("wg-over-masque", "gool ready: masque 162.159.192.7:443 carries wireguard 162.159.192.8:2408"); r == nil || r.Outer != "162.159.192.7:443" || r.Inner != "" {
		t.Fatalf("wg-over-masque outer hop: %+v", r)
	}

	hops := func(protocol, line string) {
		t.Helper()
		r := aetherScanParse(protocol, line)
		if r == nil || r.Outer != "162.159.192.3:443" || r.Inner != "162.159.192.4:443" ||
			r.Host != "162.159.192.3" || r.Port != 443 {
			t.Fatalf("%s hops parse %q: %+v", protocol, line, r)
		}
	}
	hops("gool", "using cloudflare edge 162.159.192.3:443 (outer) and 162.159.192.4:443 (inner)")
	hops("mim", "masque-in-masque ready: 162.159.192.3:443 (outer) and 162.159.192.4:443 (inner)")

	if r := aetherScanParse("wg", "selected WireGuard endpoint not-an-endpoint"); r != nil {
		t.Fatalf("invalid endpoint parsed: %+v", r)
	}
	if r := aetherScanParse("masque", "unrelated log line"); r != nil {
		t.Fatalf("unrelated line parsed: %+v", r)
	}
}

// TestAetherScanMatcherExitRule: with an exit rule the endpoint counts
// only after the core accepts its exit; a rejected exit forgets it. The
// two-hop ready lines stand alone.
func TestAetherScanMatcherExitRule(t *testing.T) {
	m := aetherScanMatcher("masque", true)
	if r := m("exit location DE accepted"); r != nil {
		t.Fatalf("accepted without named endpoint: %+v", r)
	}
	if r := m("selected MASQUE gateway 162.159.192.1:443"); r != nil {
		t.Fatalf("endpoint before exit check: %+v", r)
	}
	if r := m("exit location DE rejected"); r != nil {
		t.Fatalf("rejected exit returned endpoint: %+v", r)
	}
	if r := m("exit location DE accepted"); r != nil {
		t.Fatalf("stale endpoint survived rejection: %+v", r)
	}
	if r := m("selected MASQUE gateway 162.159.192.3:443"); r != nil {
		t.Fatalf("second endpoint before exit check: %+v", r)
	}
	r := m("exit location DE accepted")
	if r == nil || r.Host != "162.159.192.3" || r.Port != 443 {
		t.Fatalf("accepted endpoint: %+v", r)
	}

	// No rule: the named endpoint ends the scan.
	if r := aetherScanMatcher("wg", false)("selected WireGuard endpoint 162.159.192.2:2408"); r == nil || r.Port != 2408 {
		t.Fatalf("unruled wireguard endpoint: %+v", r)
	}
	// Ready lines follow the exit check by design.
	if r := aetherScanMatcher("mim", true)("masque-in-masque ready: 162.159.192.3:443 (outer) and 162.159.192.4:443 (inner)"); r == nil || r.Inner == "" {
		t.Fatalf("mim ready line: %+v", r)
	}
	if r := aetherScanMatcher("wg-over-masque", true)("gool ready: masque 162.159.192.7:443 carries wireguard 162.159.192.8:2408"); r == nil || r.Port != 443 {
		t.Fatalf("wg-over-masque ready line: %+v", r)
	}
}

func newAetherScanTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("open config store: %v", err)
	}
	settings := store.GetSettings()
	settings.GeoIPAutoDetect = false
	if err := store.SaveSettings(settings); err != nil {
		t.Fatalf("disable GeoIP: %v", err)
	}
	return &App{store: store, aetherMgr: &AetherManager{}}, dir
}

func withAetherScanRunner(t *testing.T, fn func(*App, context.Context, Profile) (*AetherScanResult, error)) {
	t.Helper()
	old := aetherScanRunner
	aetherScanRunner = fn
	t.Cleanup(func() { aetherScanRunner = old })
}

func TestAetherScanEndpointsDedupe(t *testing.T) {
	a, _ := newAetherScanTestApp(t)
	withAetherScanRunner(t, func(*App, context.Context, Profile) (*AetherScanResult, error) {
		return &AetherScanResult{Host: "162.159.192.1", Port: 443}, nil
	})

	got, err := a.AetherScanEndpoints(2, 5000)
	if err != nil {
		t.Fatalf("scan endpoints: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("dedupe kept %d results, want 1: %+v", len(got), got)
	}
	if got[0].Endpoint != "162.159.192.1:443" || got[0].Protocol != "wg" || got[0].LastScannedAt == nil {
		t.Fatalf("persisted result incomplete: %+v", got[0])
	}
	if persisted := a.store.GetSettings().AetherScanResults; len(persisted) != 1 {
		t.Fatalf("settings kept %d results, want 1: %+v", len(persisted), persisted)
	}
	status, err := a.AetherScanStatus()
	if err != nil {
		t.Fatalf("scan status: %v", err)
	}
	if status.Running || status.Done != 2 || status.Total != 2 || status.LastError != "" {
		t.Fatalf("scan status after completion: %+v", status)
	}
}

func TestAetherScanEndpointsReturnsFullList(t *testing.T) {
	a, _ := newAetherScanTestApp(t)
	old := AetherScanResult{Endpoint: "162.159.192.9:443", Protocol: "wg"}
	if err := a.store.UpdateAetherScanResults(func([]AetherScanResult) []AetherScanResult {
		return []AetherScanResult{old}
	}); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	withAetherScanRunner(t, func(*App, context.Context, Profile) (*AetherScanResult, error) {
		return &AetherScanResult{Host: "162.159.192.1", Port: 443}, nil
	})

	got, err := a.AetherScanEndpoints(1, 5000)
	if err != nil {
		t.Fatalf("scan endpoints: %v", err)
	}
	if len(got) != 2 || got[0].Endpoint != old.Endpoint || got[1].Endpoint != "162.159.192.1:443" {
		t.Fatalf("full list after scan: %+v", got)
	}
}

func TestAetherScanEndpointsPersistsPartialError(t *testing.T) {
	a, _ := newAetherScanTestApp(t)
	withAetherScanRunner(t, func(*App, context.Context, Profile) (*AetherScanResult, error) {
		return &AetherScanResult{Host: "162.159.192.2", Port: 2408}, fmt.Errorf("scan failed")
	})

	got, err := a.AetherScanEndpoints(1, 5000)
	if err == nil || !strings.Contains(err.Error(), "scan failed") {
		t.Fatalf("scan error: %v", err)
	}
	if len(got) != 1 || got[0].Endpoint != "162.159.192.2:2408" || got[0].Error != "scan failed" {
		t.Fatalf("partial results: %+v", got)
	}
	status, serr := a.AetherScanStatus()
	if serr != nil {
		t.Fatalf("scan status: %v", serr)
	}
	if status.Running || status.StartedAt == "" || status.LastErrorAt == "" || status.LastError != "scan failed" {
		t.Fatalf("error status: %+v", status)
	}
}

func TestAetherScanStatusConcurrentReject(t *testing.T) {
	a, _ := newAetherScanTestApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	withAetherScanRunner(t, func(*App, context.Context, Profile) (*AetherScanResult, error) {
		close(started)
		<-release
		return &AetherScanResult{Host: "162.159.192.2", Port: 2408}, nil
	})

	first := make(chan error, 1)
	go func() {
		_, err := a.AetherScanEndpoints(1, 5000)
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first scan did not start")
	}

	if _, err := a.AetherScanEndpoints(1, 1000); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second scan: %v", err)
	}
	status, err := a.AetherScanStatus()
	if err != nil || !status.Running {
		t.Fatalf("status while scanning: %+v, %v", status, err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first scan: %v", err)
	}
	status, _ = a.AetherScanStatus()
	if status.Running || status.Done != 1 {
		t.Fatalf("status after scan: %+v", status)
	}
}

func TestAetherSaveScanResultAsConfig(t *testing.T) {
	a, _ := newAetherScanTestApp(t)
	err := a.store.UpdateAetherScanResults(func([]AetherScanResult) []AetherScanResult {
		return []AetherScanResult{
			{Host: "162.159.192.3", Port: 2408, Endpoint: "162.159.192.3:2408", Protocol: "wg"},
			{Outer: "162.159.192.4:443", Inner: "162.159.192.5:443", Endpoint: "162.159.192.4:443", Protocol: "gool"},
		}
	})
	if err != nil {
		t.Fatalf("seed results: %v", err)
	}

	one, err := a.AetherSaveScanResultAsConfig(0, "My WARP")
	if err != nil {
		t.Fatalf("save one-hop: %v", err)
	}
	if one.ID == "" || one.Protocol != "aether" || one.Address != "162.159.192.3" || one.Port != 2408 || one.Aether.Protocol != "wg" {
		t.Fatalf("one-hop config: %+v", one)
	}
	if one.Name != "My WARP" {
		t.Fatalf("typed name ignored: got %q, want %q", one.Name, "My WARP")
	}

	two, err := a.AetherSaveScanResultAsConfig(1, "  ")
	if err != nil {
		t.Fatalf("save two-hop: %v", err)
	}
	if two.ID == "" || two.Aether.Protocol != "gool" || two.Aether.WiwOuter != "162.159.192.4:443" || two.Aether.WiwInner != "162.159.192.5:443" {
		t.Fatalf("two-hop config: %+v", two)
	}
	if two.Name != "Aether scan 162.159.192.4:443" {
		t.Fatalf("blank name did not fall back to endpoint label: %q", two.Name)
	}

	if _, err := a.AetherSaveScanResultAsConfig(9, "x"); err == nil {
		t.Fatal("out-of-range result was accepted")
	}
}

func TestAetherScanLogsFiltersBySource(t *testing.T) {
	a, _ := newAetherScanTestApp(t)
	a.emitLog("aether-scan", "info", "one")
	a.emitLog("gui", "info", "two")
	a.emitLog("aether", "info", "three")

	logs, err := a.AetherScanLogs(0)
	if err != nil {
		t.Fatalf("aether logs: %v", err)
	}
	if len(logs) != 2 || logs[0].Message != "one" || logs[1].Message != "three" {
		t.Fatalf("aether logs: %+v", logs)
	}
	if logs[0].ID == 0 || logs[1].ID <= logs[0].ID {
		t.Fatalf("log ids not increasing: %+v", logs)
	}
	later, err := a.AetherScanLogs(logs[0].ID)
	if err != nil {
		t.Fatalf("logs after id: %v", err)
	}
	if len(later) != 1 || later[0].Message != "three" {
		t.Fatalf("logs after id %d: %+v", logs[0].ID, later)
	}
}

func TestAetherScanResultsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	scannedAt := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	want := AetherScanResult{
		Host: "162.159.192.6", Port: 443, Endpoint: "162.159.192.6:443",
		Protocol: "masque", Transport: "h3", LatencyMs: 42, TLSVersion: "TLS 1.3",
		LastScannedAt: &scannedAt, Error: "test",
	}
	if err := store.UpdateAetherScanResults(func([]AetherScanResult) []AetherScanResult {
		return []AetherScanResult{want}
	}); err != nil {
		t.Fatalf("persist results: %v", err)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got := reopened.GetSettings().AetherScanResults
	if len(got) != 1 {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}
	g := got[0]
	if g.Host != want.Host || g.Port != want.Port || g.Endpoint != want.Endpoint ||
		g.Protocol != want.Protocol || g.Transport != want.Transport ||
		g.LatencyMs != want.LatencyMs || g.TLSVersion != want.TLSVersion || g.Error != want.Error ||
		g.LastScannedAt == nil || !g.LastScannedAt.Equal(scannedAt) {
		t.Fatalf("round trip: got %+v, want %+v", g, want)
	}
}
