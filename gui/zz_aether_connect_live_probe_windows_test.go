//go:build windows

package main

// Aether connect live probe. Not part of the suite: it spawns the real
// engine, so it stays skipped unless NEKOGO_AETHER_CONNECT_PROBE=1.
//
//	NEKOGO_AETHER_CONNECT_PROBE=1 go test -run TestZZAetherConnectLiveProbe -v -count=1 -timeout 12m ./
//
// It drives StartWithToken against build\bin\aether.exe on the profile
// the live config holds, and reports what the readiness rules decided:
// how long the listener took, whether a dead saved endpoint was rescued
// by a fresh scan, and whether the listener really carries traffic.
//
// The live config is copied into a scratch dir and the engine work dir
// points there too, so nothing writes to %APPDATA%\NekoGo. The report
// carries ports, durations, flag names and verdicts only: no bridge line,
// no identity, and the engine's own log tail is redacted of both.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZZAetherConnectLiveProbe(t *testing.T) {
	if os.Getenv("NEKOGO_AETHER_CONNECT_PROBE") != "1" {
		t.Skip("set NEKOGO_AETHER_CONNECT_PROBE=1 to drive the real engine")
	}
	out := filepath.Join(os.TempDir(), "nekogo-aether-connect-probe.log")
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
	r.say("stage=version aether=%q", firstLine(runVersionQuery(aetherExe, "--version")))

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
	settings.LogLevel = "info"
	settings.SystemProxy = false

	var live fileConfig
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	profile := findProfileByID(live.Profiles, settings.ActiveProfileID)
	if profile == nil || profile.Protocol != "aether" {
		profile = nil
		for i := range live.Profiles {
			if live.Profiles[i].Protocol == "aether" {
				profile = &live.Profiles[i]
				break
			}
		}
	}
	if profile == nil {
		t.Skip("the live config holds no aether profile")
	}
	r.say("stage=profile proto=%s transport=%s tor=%s psiphon=%s ech=%v fragment=%v own-command=%d",
		profile.Aether.Protocol, profile.Aether.Transport, profile.Aether.Tor, profile.Aether.Psiphon,
		profile.Aether.Ech, profile.Aether.Fragment, len(strings.TrimSpace(profile.Aether.Command)))
	r.say("stage=argv flags=%q", strings.Join(hopProbeFlags(profile), " "))

	work := filepath.Join(scratch, "aether")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	prevWork := aetherWorkDirHook
	aetherWorkDirHook = func() string { return work }
	entries := make(chan LogEntry, 4096)
	logs := &hopProbeLog{}
	go logs.drain(entries)
	m := NewAetherManager(entries)
	t.Cleanup(func() {
		_ = m.Stop()
		aetherWorkDirHook = prevWork
		close(entries)
	})

	start := time.Now()
	port, token, err := m.StartWithToken(profile, settings)
	elapsed := time.Since(start).Round(time.Second)
	r.say("stage=connect port=%d ready-in=%s err=%v", port, elapsed, err)
	rescued := logs.hasLine("scanning for a gateway afresh")
	r.say("stage=rescue ran=%v", rescued)
	if err != nil {
		logs.redactedTail(r, profile, 24)
		t.Errorf("the real engine did not become ready: %v", err)
		return
	}
	if elapsed <= aetherWarmup {
		r.say("stage=note ready within the old %s deadline; this run does not exercise the rounds", aetherWarmup)
	}
	if trafficErr := hopProbeTraffic(r, port); trafficErr != nil {
		r.say("stage=traffic aborted: %v", trafficErr)
	}
	logs.redactedTail(r, profile, 12)
	if err := m.StopToken(token); err != nil {
		r.say("stage=stop err=%v", err)
	}
	r.say("stage=done running=%v", m.Running())
}

func (l *hopProbeLog) hasLine(want string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// redactedTail prints the engine's last lines with anything naming a
// bridge dropped whole: a bridge line is a credential for the front it
// reaches, and this report is a file a user may hand over.
func (l *hopProbeLog) redactedTail(r *probeReport, p *Profile, n int) {
	l.mu.Lock()
	lines := append([]string{}, l.lines...)
	l.mu.Unlock()
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, line := range lines {
		low := strings.ToLower(line)
		if strings.Contains(low, "bridge") || strings.Contains(low, "obfs4") ||
			strings.Contains(low, "webtunnel") || strings.Contains(low, "snowflake") {
			r.say("stage=engine-log [redacted: names a bridge]")
			continue
		}
		secret := false
		for _, b := range strings.Split(p.Aether.TorBridgeLines, "\n") {
			if b = strings.TrimSpace(b); b != "" && strings.Contains(line, b) {
				secret = true
				break
			}
		}
		if secret {
			r.say("stage=engine-log [redacted: carries a saved bridge line]")
			continue
		}
		r.say("stage=engine-log %s", line)
	}
}
