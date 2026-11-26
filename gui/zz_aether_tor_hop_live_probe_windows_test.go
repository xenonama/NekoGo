//go:build windows

package main

// Aether-leg Tor hop live probe. Not part of the suite: it spawns real
// engines, so it stays skipped unless NEKOGO_TOR_HOP_PROBE=1.
//
//	NEKOGO_TOR_HOP_PROBE=1 go test -run TestZZAetherTorHopLiveProbe -v -count=1 -timeout 25m ./
//
// NEKOGO_TOR_HOP_ARM=1 arms the Tor hop on the scratch copy of the config,
// for a machine whose Chain tab currently holds the other exit chain.
//
// It drives the two-child shape of the Aether Tor hop: aether.exe with the
// tor mode taken out (the tunnel alone), then tor.exe behind that tunnel as
// its SOCKS front, on the bridge set the live config holds. The hop runs
// twice, cold and then warm, because the second start reuses the cached
// consensus in the scratch state dir and a user's later connects do too.
//
// The live config is copied into a scratch dir and the engines' work dir
// (identities, torrc, tor-state) points at the same place, so nothing here
// writes to %APPDATA%\NekoGo and nothing pre-seeds the bootstrap. What the
// probe prints cannot carry a bridge line: it reports ports, counts, flag
// names, versions, durations, percentages and the engines' own error text.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const hopProbeEnv = "NEKOGO_TOR_HOP_PROBE"

// hopProbeBudget is a minute over the readiness budget a connect gives the
// hop, so a run that would have made it on seconds to spare is not reported
// as a stall; the verdict against the real budget is printed alongside.
const hopProbeBudget = torChainWarmup + time.Minute

type hopProbeLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *hopProbeLog) drain(ch chan LogEntry) {
	for e := range ch {
		l.mu.Lock()
		l.lines = append(l.lines, fmt.Sprintf("%s %s %s: %s", e.Time, e.Source, e.Level, e.Message))
		l.mu.Unlock()
	}
}

func (l *hopProbeLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *hopProbeLog) tail(r *probeReport, n, since int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := l.lines
	if since > 0 && since < len(lines) {
		lines = lines[since:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, e := range lines {
		r.say("stage=engine-log %s", e)
	}
}

func TestZZAetherTorHopLiveProbe(t *testing.T) {
	if os.Getenv(hopProbeEnv) != "1" {
		t.Skip("set " + hopProbeEnv + "=1 to drive the real engines")
	}
	out := filepath.Join(os.TempDir(), probeEnv("NEKOGO_TOR_HOP_REPORT", "nekogo-tor-hop-probe.log"))
	sink, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	r := &probeReport{sink: sink}
	r.say("stage=report file=%s", out)

	binDir, err := filepath.Abs(filepath.Join("build", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	aetherExe := filepath.Join(binDir, "aether.exe")
	if _, err := os.Stat(aetherExe); err != nil {
		t.Skipf("no aether.exe at %s", aetherExe)
	}

	scratch := t.TempDir()
	liveDir, err := defaultConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(liveDir, "config.json"))
	if err != nil {
		t.Fatalf("read live config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(scratch)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.GetSettings()
	settings.AetherPath = aetherExe
	settings.LogLevel = probeEnv("NEKOGO_TOR_HOP_LEVEL", "info")
	// The hop is armed on the scratch copy, never on the stored config:
	// which exit chain the Chain tab currently holds is the user's choice
	// and has nothing to do with whether this hop can bootstrap.
	if os.Getenv("NEKOGO_TOR_HOP_ARM") == "1" {
		settings.AetherTor, settings.AetherPsiphon = true, false
	}
	// Under `go test` the running exe is a scratch binary in the build
	// cache, so anchoring the engine trees at build\bin is what makes this
	// the same resolution nekogo.exe in that folder does.
	prevTorDir := torExeDirOverride
	torExeDirOverride = binDir
	t.Cleanup(func() { torExeDirOverride = prevTorDir })

	var live fileConfig
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	profile := findProfileByID(live.Profiles, settings.ActiveProfileID)
	if profile == nil || profile.Protocol != "aether" {
		t.Skip("the active profile is not an aether node; connect one and rerun")
	}
	r.say("stage=profile name=%q aether.proto=%s aether.tor=%s aether.psiphon=%s own-command=%d",
		profile.Name, profile.Aether.Protocol, profile.Aether.Tor, profile.Aether.Psiphon,
		len(strings.TrimSpace(profile.Aether.Command)))

	hopProfile := aetherTorChainProfile(aetherPsiphonChainProfile(profile, settings), settings)
	tunnelProfile := aetherTunnelOnlyProfile(hopProfile)
	daemonHop, note := aetherTorHopDecision(hopProfile, settings, NewTorManager(make(chan LogEntry, 512)))
	r.say("stage=decision daemon=%v note=%q", daemonHop, note)
	if !daemonHop {
		t.Errorf("the live config does not pick the daemon shape: %s", note)
		return
	}
	r.say("stage=argv tunnel=%q hop=%q",
		strings.Join(hopProbeFlags(tunnelProfile), " "), strings.Join(hopProbeFlags(hopProfile), " "))

	work := filepath.Join(scratch, "aether")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	prevWork := aetherWorkDirHook
	aetherWorkDirHook = func() string { return work }

	logs := &hopProbeLog{}
	entries := make(chan LogEntry, 512)
	go logs.drain(entries)
	aetherMgr := NewAetherManager(entries)
	hopMgr := NewTorManager(entries)
	t.Cleanup(func() {
		_ = hopMgr.Stop()
		_ = aetherMgr.Stop()
		aetherWorkDirHook = prevWork
		close(entries)
	})

	dialPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	tunnelPort, token, err := aetherMgr.runProcess(aetherExe, buildAetherArguments(tunnelProfile, dialPort),
		dialPort, aetherWarmup, profile.ID, "", nil)
	r.say("stage=tunnel port=%d took=%s err=%v", tunnelPort, time.Since(start).Round(time.Millisecond), err)
	if err != nil {
		logs.tail(r, 10, 0)
		t.Errorf("the tunnel alone did not start: %v", err)
		return
	}

	hopSettings := aetherTorHopSettings(settings, hopProfile)
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		since := logs.count()
		// The budget is the one a connect would hand this hop, state
		// dir included: the cold round is what standaloneTorWarmup
		// exists for, and the verdict has to be read against it.
		budget := standaloneTorWarmup(hopMgr, hopProbeBudget)
		start = time.Now()
		daemonPort, hopToken, err := hopMgr.StartStandaloneTor(hopSettings, hopProbePort(t), tunnelPort,
			budget, nil)
		elapsed := time.Since(start).Round(time.Second)
		if err != nil {
			_ = hopMgr.StopToken(hopToken)
			r.say("stage=hop run=%s err=%v took=%s", name, err, elapsed)
			logs.tail(r, 14, since)
			t.Errorf("the %s hop run did not become ready: %v", name, err)
			return
		}
		r.say("stage=hop run=%s daemon=%d front=%d ready-in=%s budget=%s within-connect-budget=%v",
			name, daemonPort, tunnelPort, elapsed, budget, elapsed <= budget)
		logs.tail(r, 14, since)
		if !warm {
			if err := hopProbeTraffic(r, daemonPort); err != nil {
				r.say("stage=traffic aborted: %v", err)
			}
		}
		if err := hopMgr.Stop(); err != nil {
			r.say("stage=hop stop err=%v", err)
		}
	}

	if err := aetherMgr.StopToken(token); err != nil {
		r.say("stage=tunnel stop err=%v", err)
	}
	if exe, _ := standaloneTorFor(settings); exe != "" {
		r.say("stage=version tor=%q aether=%q",
			firstLine(runVersionQuery(exe, "--version")), firstLine(runVersionQuery(aetherExe, "--version")))
	}
}

func hopProbePort(t *testing.T) int {
	t.Helper()
	port, err := freePortTCPAndUDP()
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// hopProbeTraffic carries a real HTTPS request through the hop's listener:
// a bootstrapping log that never carries traffic is the failure being
// fixed, and only a request through the circuit tells the two apart.
func hopProbeTraffic(r *probeReport, port int) error {
	proxy, err := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return proxy, nil },
		},
		Timeout: 60 * time.Second,
	}
	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		r.say("stage=traffic target=generate_204 err=%v", err)
	} else {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		r.say("stage=traffic target=generate_204 status=%d", resp.StatusCode)
	}
	resp, err = client.Get("https://check.torproject.org/api/ip")
	if err != nil {
		r.say("stage=traffic target=check.torproject.org err=%v", err)
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	// The body names the exit address, so it is decoded and only the
	// verdict is reported. Three spellings have been measured on this
	// one endpoint: "IsTor": true as a JSON boolean (what
	// check.torproject.org/api/ip answers today), "tor": true, and
	// "is_tor": "true" as a string. Reading only the last two printed
	// is-tor=false for a hop that was carrying Tor traffic.
	var verdict struct {
		IsTor bool   `json:"IsTor"`
		Tor   *bool  `json:"tor"`
		IsUnd string `json:"is_tor"`
	}
	if err := json.Unmarshal(body, &verdict); err != nil {
		r.say("stage=traffic target=check.torproject.org status=%d answer=%d-byte-non-json",
			resp.StatusCode, len(body))
		return err
	}
	tor := verdict.IsTor || (verdict.Tor != nil && *verdict.Tor) || strings.EqualFold(verdict.IsUnd, "true")
	r.say("stage=traffic target=check.torproject.org status=%d is-tor=%v bytes=%d",
		resp.StatusCode, tor, len(body))
	return nil
}

// hopProbeFlags is a profile's engine argv reduced to its flag names, so a
// bridge line or an address can never reach the report through it.
func hopProbeFlags(p *Profile) []string {
	args := buildAetherArguments(p, 0)
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			out = append(out, a)
		}
	}
	return out
}
