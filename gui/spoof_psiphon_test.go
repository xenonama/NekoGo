package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// chainCfg renders spoofTestProfile with the chain ports published.
func chainCfg(t *testing.T, s Settings) map[string]any {
	t.Helper()
	setSpoofFrontPort(40555)
	setSpoofChainPorts(40556, 40557)
	t.Cleanup(func() {
		clearSpoofFrontPort()
		clearSpoofChainPorts()
	})
	raw, err := BuildSingBoxConfig(spoofTestProfile(), s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return cfg
}

func chainOutbound(cfg map[string]any, tag string) map[string]any {
	outs, _ := cfg["outbounds"].([]any)
	for _, o := range outs {
		m, _ := o.(map[string]any)
		if m["tag"] == tag {
			return m
		}
	}
	return nil
}

func chainInbound(cfg map[string]any, tag string) map[string]any {
	ins, _ := cfg["inbounds"].([]any)
	for _, in := range ins {
		m, _ := in.(map[string]any)
		if m["tag"] == tag {
			return m
		}
	}
	return nil
}

// TestSpoofPsiphonChainRendering: with the chain armed the rendered
// config exposes Psiphon through the TUN, routes user inbounds into it,
// carries Psiphon's own dials on the spoofed "proxy" outbound, and still
// renders the front rewrite for the profile itself.
func TestSpoofPsiphonChainRendering(t *testing.T) {
	s := DefaultSettings()
	s.SpoofPsiphon = true
	s.TunEnabled = false // the chain forces TUN on its own
	cfg := chainCfg(t, s)

	psiphon := chainOutbound(cfg, spoofPsiphonTag)
	if psiphon == nil {
		t.Fatal("no psiphon outbound rendered")
	}
	if psiphon["type"] != "socks" {
		t.Fatalf("psiphon outbound type = %v, want socks", psiphon["type"])
	}
	if psiphon["server"] != "127.0.0.1" || psiphon["server_port"] != float64(40556) {
		t.Fatalf("psiphon outbound = %v:%v, want 127.0.0.1:40556", psiphon["server"], psiphon["server_port"])
	}

	up := chainInbound(cfg, spoofPsiphonUpTag)
	if up == nil {
		t.Fatal("no psiphon upstream inbound rendered")
	}
	if up["type"] != "mixed" || up["listen"] != "127.0.0.1" || up["listen_port"] != float64(40557) {
		t.Fatalf("psiphon upstream inbound = %v", up)
	}

	// The chain is what routes the system's traffic, so TUN must render
	// even with settings.TunEnabled false.
	tun := chainInbound(cfg, "tun-in")
	if tun == nil {
		t.Fatal("chain rendered no tun inbound")
	}
	if tun["mtu"] != float64(1350) {
		t.Fatalf("tun mtu = %v, want 1350", tun["mtu"])
	}

	// "proxy" is the spoof front: the chain's upstream rule and DNS
	// final both ride it.
	proxy := chainOutbound(cfg, "proxy")
	if proxy == nil || proxy["server"] != "127.0.0.1" || proxy["server_port"] != float64(40555) {
		t.Fatalf("proxy outbound = %v, want the spoof front 127.0.0.1:40555", proxy)
	}
	route := routeBlock(t, cfg)
	if route["final"] != "proxy" {
		t.Fatalf("route final = %v, want proxy", route["final"])
	}
	var upRule, userRule bool
	for _, r := range route["rules"].([]any) {
		m, _ := r.(map[string]any)
		in, _ := m["inbound"].([]any)
		has := func(tag string) bool {
			for _, v := range in {
				if v == tag {
					return true
				}
			}
			return false
		}
		if has(spoofPsiphonUpTag) && m["outbound"] == "proxy" {
			upRule = true
		}
		if has("tun-in") && has("mixed-in") && m["outbound"] == spoofPsiphonTag {
			userRule = true
		}
	}
	if !upRule {
		t.Fatal("no route rule sending the psiphon upstream inbound to proxy")
	}
	if !userRule {
		t.Fatal("no route rule sending user inbounds to psiphon")
	}

	// TUN mode hijacks DNS into the module instead of leaking it.
	hijack := false
	for _, r := range route["rules"].([]any) {
		m, _ := r.(map[string]any)
		if m["protocol"] == "dns" && m["action"] == "hijack-dns" {
			hijack = true
		}
	}
	if !hijack {
		t.Fatalf("no hijack-dns rule in %v", route["rules"])
	}
	if _, ok := cfg["dns"]; !ok {
		t.Fatal("chain rendered no dns block")
	}
}

// TestSpoofPsiphonChainPassesCoreCheck feeds the rendered chain config
// to the real core's `check`, catching a route/inbound schema regression
// the JSON assertions above cannot see. Skips without the built core.
func TestSpoofPsiphonChainPassesCoreCheck(t *testing.T) {
	core := filepath.Join("build", "bin", "nekobox_core.exe")
	if _, err := os.Stat(core); err != nil {
		t.Skipf("missing %s", core)
	}
	s := DefaultSettings()
	s.SpoofPsiphon = true
	setSpoofFrontPort(40555)
	setSpoofChainPorts(40556, 40557)
	t.Cleanup(func() {
		clearSpoofFrontPort()
		clearSpoofChainPorts()
	})
	raw, err := BuildSingBoxConfig(spoofTestProfile(), s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "chain.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(core, "check", "-c", path).CombinedOutput()
	if err != nil {
		t.Fatalf("core check rejected the chain config: %v\n%s\nconfig: %s", err, out, raw)
	}
}

// TestSpoofPsiphonNeedsPublishedPorts: the chain is gated by the
// published ports, so previews and ephemeral probes never render it,
// and a non-spoof profile never chains even with the setting on.
func TestSpoofPsiphonNeedsPublishedPorts(t *testing.T) {
	s := DefaultSettings()
	s.SpoofPsiphon = true
	setSpoofFrontPort(40555)
	defer clearSpoofFrontPort()
	clearSpoofChainPorts()

	raw, err := BuildSingBoxConfig(spoofTestProfile(), s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	var cfg map[string]any
	_ = json.Unmarshal([]byte(raw), &cfg)
	if chainOutbound(cfg, spoofPsiphonTag) != nil {
		t.Fatal("chain rendered without published ports")
	}
	if chainInbound(cfg, "tun-in") != nil {
		t.Fatal("TUN rendered for a chain that is not armed")
	}

	if !spoofPsiphonChain(spoofTestProfile(), s) {
		t.Fatal("spoofPsiphonChain = false for a spoof profile with the setting on")
	}
	if spoofPsiphonChain(&Profile{Core: CoreSingBox}, s) {
		t.Fatal("spoofPsiphonChain = true for a non-spoof profile")
	}
	s.SpoofPsiphon = false
	if spoofPsiphonChain(spoofTestProfile(), s) {
		t.Fatal("spoofPsiphonChain = true with the setting off")
	}
}

func TestAllocateSpoofChainPorts(t *testing.T) {
	s := DefaultSettings()
	spoofPort := s.MixedPort + 10
	psiphonPort, upstreamPort, err := allocateSpoofChainPorts(s, spoofPort)
	if err != nil {
		t.Fatalf("allocateSpoofChainPorts: %v", err)
	}
	if psiphonPort == upstreamPort {
		t.Fatalf("ports collide: %d", psiphonPort)
	}
	for _, avoid := range []int{s.MixedPort, s.MixedPort + 1, spoofPort} {
		if psiphonPort == avoid || upstreamPort == avoid {
			t.Fatalf("allocated %d/%d collides with %d", psiphonPort, upstreamPort, avoid)
		}
	}
}

func TestPsiphonOnlyArgs(t *testing.T) {
	got := psiphonOnlyArgs(1234, 5678, "debug", "")
	want := []string{
		"--bind", "127.0.0.1:1234",
		"--psiphon-only",
		"--upstream", "socks5://127.0.0.1:5678",
		"--log-level", "debug",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("psiphonOnlyArgs = %v, want %v", got, want)
	}
	// A chain dir pins Psiphon's datastore away from the shared default.
	withDir := psiphonOnlyArgs(1234, 5678, "debug", `C:\chain-dir`)
	wantDir := append(append([]string{}, want...), "--psiphon-dir", `C:\chain-dir`)
	if !reflect.DeepEqual(withDir, wantDir) {
		t.Fatalf("psiphonOnlyArgs = %v, want %v", withDir, wantDir)
	}
}

// TestChainPsiphonDirs: every chain run gets its own dir under the work
// dir, so a leaked psiphon-tunnel-core can never hold its datastore
// lock; stale dirs are pruned, fresh and unrelated ones are kept.
func TestChainPsiphonDirs(t *testing.T) {
	base := t.TempDir()
	prev := aetherWorkDirHook
	aetherWorkDirHook = func() string { return base }
	t.Cleanup(func() { aetherWorkDirHook = prev })

	stale := filepath.Join(base, chainPsiphonDirPrefix+"stale")
	fresh := filepath.Join(base, chainPsiphonDirPrefix+"fresh")
	other := filepath.Join(base, "aether.toml-psiphon")
	for _, d := range []string{stale, fresh, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * chainPsiphonDirMaxAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	d1 := newChainPsiphonDir()
	d2 := newChainPsiphonDir()
	if d1 == d2 {
		t.Fatalf("two chain runs share a dir: %s", d1)
	}
	for _, d := range []string{d1, d2} {
		if filepath.Dir(d) != base {
			t.Fatalf("chain dir %s is not under the work dir %s", d, base)
		}
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Fatalf("chain dir %s not created: %v", d, err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale chain dir survived the prune: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh chain dir was pruned: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("non-chain dir was pruned: %v", err)
	}
}

func TestTunRequirementForChain(t *testing.T) {
	s := DefaultSettings()
	p := spoofTestProfile()
	if tunRequiredFor(p, s) {
		t.Fatal("tunRequiredFor = true for a plain spoof connect")
	}
	s.SpoofPsiphon = true
	if !tunRequiredFor(p, s) {
		t.Fatal("tunRequiredFor = false for an armed chain")
	}
	if tunRequiredFor(&Profile{Core: CoreSingBox}, s) {
		t.Fatal("tunRequiredFor = true for a non-spoof profile")
	}
	// The Xray transport renders no TUN, so it never requires one —
	// but an armed chain still runs on sing-box and does.
	xray := DefaultSettings()
	xray.SpoofXray = true
	xray.TunEnabled = true
	if tunRequiredFor(p, xray) {
		t.Fatal("tunRequiredFor = true for a spoof connect on xray")
	}
	if !spoofTransportXray(p, xray) {
		t.Fatal("spoofTransportXray = false with the setting on")
	}
	xray.SpoofPsiphon = true
	if spoofTransportXray(p, xray) {
		t.Fatal("the armed chain must stay on sing-box")
	}
	if !tunRequiredFor(p, xray) {
		t.Fatal("tunRequiredFor = false for an armed chain on xray")
	}
	// tunActiveFor is the render mirror: it needs the published ports.
	if tunActiveFor(p, s) {
		t.Fatal("tunActiveFor = true before the chain ports are published")
	}
	setSpoofChainPorts(40556, 40557)
	defer clearSpoofChainPorts()
	if !tunActiveFor(p, s) {
		t.Fatal("tunActiveFor = false with the chain armed")
	}
}

// TestStopLockedClearsChainState: a CoreSpoof group teardown drops the
// chain bookkeeping even when the Psiphon child is already gone.
func TestStopLockedClearsChainState(t *testing.T) {
	m := &CoreManager{
		activeCore:    CoreSpoof,
		running:       true,
		psiphonActive: true,
		psiphonPort:   40556,
		psiphonMgr:    NewPsiphonManager(nil),
	}
	setSpoofChainPorts(40556, 40557)
	setSpoofFrontPort(40555)
	defer clearSpoofFrontPort()
	_ = m.stopLocked() // singboxMgr nil, so an error is expected
	if m.psiphonActive || m.psiphonPort != 0 {
		t.Fatalf("chain state survived teardown: active=%v port=%d", m.psiphonActive, m.psiphonPort)
	}
	if _, _, ok := spoofChainPorts(); ok {
		t.Fatal("chain ports survived teardown")
	}
	if _, ok := currentSpoofFrontPort(); ok {
		t.Fatal("front port survived teardown")
	}
	active, port := m.PsiphonChain()
	if active || port != 0 {
		t.Fatalf("PsiphonChain = %v/%d after teardown", active, port)
	}
}

// TestStartSpoofXrayNeedsXrayManager: a plain spoof connect on the Xray
// transport fails fast when that manager is not wired, before any leg
// starts, instead of silently running sing-box.
func TestStartSpoofXrayNeedsXrayManager(t *testing.T) {
	m := &CoreManager{spoofMgr: NewSpoofManager(nil)}
	err := m.startSpoofLocked(spoofTestProfile(), Settings{SpoofXray: true})
	if err == nil || err.Error() != "xray manager is nil" {
		t.Fatalf("startSpoofLocked = %v, want xray manager is nil", err)
	}
	if m.spoofXray {
		t.Fatal("a failed start left spoofXray set")
	}
}
