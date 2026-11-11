package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Integration P2 batch tests (bugs-integration.txt BUG-003..012).
// Live-core probes stay in probe_test.go; everything here runs without
// spawning a core process.

// TestValidateProfileForSave — BUG-007 (port) + BUG-011 (protocol):
// non-raw profiles need 1..65535 and a per-core-routable protocol;
// rawOutbound passthrough bypasses both (a raw-only profile with empty
// address/port is backend-legal).
func TestValidateProfileForSave(t *testing.T) {
	ok := []Profile{
		{Name: "vless", Core: CoreSingBox, Protocol: "vless", Address: "h", Port: 443},
		{Name: "socks", Core: CoreSingBox, Protocol: "socks", Address: "h", Port: 1080},
		{Name: "direct", Core: CoreSingBox, Protocol: "direct", Address: "h", Port: 80},
		{Name: "ss", Core: CoreXray, Protocol: "shadowsocks", Address: "h", Port: 8388},
		{Name: "raw-wireguard", Core: CoreSingBox, Protocol: "wireguard", RawOutbound: `{"type":"wireguard"}`},
		{Name: "raw-only", Core: CoreXray, RawOutbound: `{"protocol":"freedom"}`},
	}
	for _, p := range ok {
		if err := validateProfileForSave(p); err != nil {
			t.Errorf("validateProfileForSave(%s) = %v, want nil", p.Name, err)
		}
	}
	bad := []Profile{
		{Name: "port-zero", Core: CoreSingBox, Protocol: "vless", Address: "h", Port: 0},
		{Name: "port-huge", Core: CoreSingBox, Protocol: "vless", Address: "h", Port: 99999},
		{Name: "port-neg", Core: CoreXray, Protocol: "vmess", Address: "h", Port: -1},
		{Name: "wg-no-raw", Core: CoreSingBox, Protocol: "wireguard", Address: "h", Port: 51820},
		{Name: "hysteria-no-raw", Core: CoreSingBox, Protocol: "hysteria2", Address: "h", Port: 443},
		{Name: "socks-on-xray", Core: CoreXray, Protocol: "socks", Address: "h", Port: 1080},
		{Name: "empty-proto", Core: CoreSingBox, Address: "h", Port: 443},
	}
	for _, p := range bad {
		if err := validateProfileForSave(p); err == nil {
			t.Errorf("validateProfileForSave(%s) = nil, want error", p.Name)
		}
	}
}

// TestSaveProfileValidatesPortAndProtocol — the bound path rejects and
// persists nothing.
func TestSaveProfileValidatesPortAndProtocol(t *testing.T) {
	a := newGeoTestApp(t)
	if _, err := a.SaveProfile(Profile{Name: "bad-port", Core: CoreSingBox,
		Protocol: "vless", Address: "h", Port: 99999}); err == nil {
		t.Fatal("SaveProfile(port 99999) = nil, want range error")
	}
	if _, err := a.SaveProfile(Profile{Name: "bad-proto", Core: CoreSingBox,
		Protocol: "wireguard", Address: "h", Port: 51820}); err == nil {
		t.Fatal("SaveProfile(wireguard, no raw) = nil, want protocol error")
	}
	if n := len(a.store.ListProfiles()); n != 0 {
		t.Fatalf("rejected saves persisted %d profiles, want 0", n)
	}
	if _, err := a.SaveProfile(Profile{Name: "good", Core: CoreSingBox,
		Protocol: "vless", Address: "h", Port: 443}); err != nil {
		t.Fatalf("SaveProfile(valid) = %v, want nil", err)
	}
}

// TestDeleteProfileClearsActiveID — BUG-012: deleting the selected (idle)
// profile must not leave it as the persisted auto-connect target.
func TestDeleteProfileClearsActiveID(t *testing.T) {
	a := newGeoTestApp(t)
	pa, err := a.SaveProfile(Profile{Name: "a", Core: CoreSingBox,
		Protocol: "vless", Address: "h", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := a.SaveProfile(Profile{Name: "b", Core: CoreSingBox,
		Protocol: "vless", Address: "h", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetActiveProfileID(pa.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteProfile(pa.ID); err != nil {
		t.Fatalf("DeleteProfile(selected) = %v, want nil", err)
	}
	if got := a.store.GetSettings().ActiveProfileID; got != "" {
		t.Fatalf("ActiveProfileID = %q after deleting it, want empty", got)
	}
	if err := a.store.SetActiveProfileID(pb.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteProfile(pa.ID); err == nil {
		t.Fatal("DeleteProfile(gone) = nil, want not-found")
	}
	if got := a.store.GetSettings().ActiveProfileID; got != pb.ID {
		t.Fatalf("ActiveProfileID = %q after unrelated delete, want %q", got, pb.ID)
	}
}

// TestResolveTestURLPrefersProfile — BUG-006: per-profile override wins,
// then global, then built-in default.
func TestResolveTestURLPrefersProfile(t *testing.T) {
	s := DefaultSettings()
	s.TestUrl = "https://global.example/"
	p := &Profile{TestURL: "https://profile.example/"}
	if got := resolveTestURL(p, s); got != "https://profile.example/" {
		t.Fatalf("profile override = %q, want profile URL", got)
	}
	if got := resolveTestURL(&Profile{}, s); got != "https://global.example/" {
		t.Fatalf("empty profile = %q, want global URL", got)
	}
	s.TestUrl = ""
	if got := resolveTestURL(nil, s); got != defaultTestURL {
		t.Fatalf("nil+empty = %q, want default", got)
	}
}

// TestStopEphemeralTestsOwnerScope — BUG-009: cancellation is per-owner.
// A network stop must not abort proxies-owned probes and vice versa.
// Concurrent probes are independent (Test All pings several rows at
// once): opening a new probe must never cancel an older sibling, and a
// stale forget must not wipe a newer registration.
func TestStopEphemeralTestsOwnerScope(t *testing.T) {
	m := NewCoreManager(nil, nil)
	ctx, cancel, gen := m.ephProbeCtx(probeOwnerProxies)
	defer cancel()
	ctx2, cancel2, gen2 := m.ephProbeCtx(probeOwnerProxies)
	defer cancel2()
	if ctx.Err() != nil || ctx2.Err() != nil {
		t.Fatal("a second concurrent probe superseded the first, want both alive")
	}
	if m.StopEphemeralTests(probeOwnerNetwork) {
		t.Fatal("network stop aborted proxies probes, want false (BUG-008)")
	}
	if ctx.Err() != nil || ctx2.Err() != nil {
		t.Fatal("proxies ctx cancelled by foreign-owner stop")
	}
	m.ephProbeForget(gen) // ends only the first probe's registration
	ctx3, cancel3, gen3 := m.ephProbeCtx(probeOwnerNetwork)
	defer cancel3()
	defer m.ephProbeForget(gen3)
	if !m.StopEphemeralTests(probeOwnerNetwork) {
		t.Fatal("network stop = false, want true")
	}
	select {
	case <-ctx3.Done():
	default:
		t.Fatal("network ctx not cancelled by own-owner stop")
	}
	if ctx2.Err() != nil {
		t.Fatal("proxies ctx cancelled by network stop")
	}
	m.ephProbeForget(gen2)
	if m.StopEphemeralTests("") {
		t.Fatal("stop with nothing in flight = true, want false")
	}
}

// TestRefreshAllCountriesTotalDeadline — BUG-003: the total cap bounds
// wall time even when every lookup hangs past its per-profile budget.
func TestRefreshAllCountriesTotalDeadline(t *testing.T) {
	oldTimeout := refreshTotalTimeout
	refreshTotalTimeout = 300 * time.Millisecond
	defer func() { refreshTotalTimeout = oldTimeout }()
	old := countryLookup
	defer func() { countryLookup = old }()
	var wg sync.WaitGroup
	countryLookup = func(a *App, p *Profile) (bool, error) {
		defer wg.Done()
		time.Sleep(2 * time.Second)
		p.CountryCode = "DE"
		return true, nil
	}
	a := newGeoTestApp(t)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		if _, err := a.store.SaveProfile(Profile{
			Name: fmt.Sprintf("slow%d", i), Address: "8.8.8.8", Port: 443}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	res, err := a.RefreshAllCountries()
	if err != nil {
		t.Fatalf("RefreshAllCountries = %v, want nil (failures counted, not raised)", err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("refresh took %v, want bounded by the 300ms total deadline", d)
	}
	if res.Failed != 3 {
		t.Fatalf("result = %+v, want 3 timeouts counted as Failed", res)
	}
	wg.Wait() // drain stranded lookups before restoring the seam
}

// TestCancelRefreshCountries — BUG-003: Cancel aborts the run, reports
// progress honestly, and a second cancel is a no-op false.
func TestCancelRefreshCountries(t *testing.T) {
	old := countryLookup
	defer func() { countryLookup = old }()
	release := make(chan struct{})
	countryLookup = func(a *App, p *Profile) (bool, error) {
		<-release // blocked until cancel (ctx fires first) or test end
		return false, nil
	}
	a := newGeoTestApp(t)
	for _, n := range []string{"c1", "c2"} {
		if _, err := a.store.SaveProfile(Profile{Name: n, Address: "8.8.8.8", Port: 443}); err != nil {
			t.Fatal(err)
		}
	}
	type rr struct {
		res *CountryRefreshResult
		err error
	}
	out := make(chan rr, 1)
	go func() {
		res, err := a.RefreshAllCountries()
		out <- rr{res, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if a.RefreshCountriesProgress().Running {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("refresh never registered as running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !a.CancelRefreshCountries() {
		close(release)
		t.Fatal("CancelRefreshCountries = false while running, want true")
	}
	select {
	case r := <-out:
		if r.err != nil {
			t.Fatalf("cancelled refresh err = %v, want nil with failures counted", r.err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("cancelled refresh did not return")
	}
	if a.CancelRefreshCountries() {
		t.Fatal("second CancelRefreshCountries = true, want false")
	}
	if p := a.RefreshCountriesProgress(); p.Running {
		t.Fatal("progress still Running after cancel")
	}
	close(release)
}

// TestCancelledRefreshKeepsNewRunSlot: CancelRefreshCountries frees the
// one-at-a-time slot right away (the UI's Cancel has to work while the
// run still drains), so a newer run can register before the cancelled
// one returns. The cancelled run's cleanup must not clear the NEWER
// registration — that made the progress readout report "not running"
// during a live refresh, turned Cancel into a no-op, and re-armed the
// "refresh already running" guard so repeated Cancel→Refresh clicks
// stack concurrent runs on one set of progress counters.
func TestCancelledRefreshKeepsNewRunSlot(t *testing.T) {
	old := countryLookup
	defer func() { countryLookup = old }()
	release := make(chan struct{})
	countryLookup = func(*App, *Profile) (bool, error) {
		<-release // blocked until the run is cancelled, then drained here
		return false, nil
	}
	a := newGeoTestApp(t)
	for _, n := range []string{"slot1", "slot2"} {
		if _, err := a.store.SaveProfile(Profile{Name: n, Address: "8.8.8.8", Port: 443}); err != nil {
			t.Fatal(err)
		}
	}
	closeOnce := func(ch chan struct{}) func() {
		var once sync.Once
		return func() { once.Do(func() { close(ch) }) }
	}
	hold := make(chan struct{})
	unpin := closeOnce(hold)
	unblock := closeOnce(release)
	defer unblock()
	defer unpin()

	// Pin the store lock: a run stops at its first store call
	// (ListProfiles), AFTER registering its slot. That holds the
	// cancelled run alive while the second one registers — an ordering
	// no amount of sleeping could sample reliably.
	holding := make(chan struct{})
	pinned := make(chan struct{})
	go func() {
		defer close(pinned)
		if err := a.store.UpdateCoreOverrides(func(map[string]json.RawMessage) bool {
			close(holding)
			<-hold
			return false
		}); err != nil {
			t.Errorf("pinning the store lock: %v", err)
		}
	}()
	select {
	case <-holding:
	case <-time.After(3 * time.Second):
		t.Fatal("store lock never pinned")
	}

	type rr struct {
		res *CountryRefreshResult
		err error
	}
	start := func() chan rr {
		out := make(chan rr, 1)
		go func() {
			res, err := a.RefreshAllCountries()
			out <- rr{res, err}
		}()
		return out
	}
	waitRegistered := func(out chan rr, why string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if a.RefreshCountriesProgress().Running {
				return
			}
			select {
			case r := <-out:
				t.Fatalf("refresh %s returned %v instead of running", why, r.err)
			default:
			}
			if time.Now().After(deadline) {
				unblock()
				unpin()
				t.Fatalf("refresh %s never registered as running", why)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitReturn := func(out chan rr, why string) rr {
		t.Helper()
		select {
		case r := <-out:
			return r
		case <-time.After(5 * time.Second):
			unblock()
			unpin()
			t.Fatalf("refresh %s did not return", why)
			return rr{}
		}
	}

	first := start()
	waitRegistered(first, "run A")
	if !a.CancelRefreshCountries() {
		t.Fatal("CancelRefreshCountries = false while run A is registered, want true")
	}
	// The cancelled run is still draining (pinned at ListProfiles); a
	// second run registers in its freed slot.
	second := start()
	waitRegistered(second, "run B")

	unpin() // both runs resume; A now finishes and runs its cleanup
	r := waitReturn(first, "run A after the store lock was released")
	if r.err != nil {
		t.Fatalf("cancelled run A err = %v, want nil with the lookups counted as failed", r.err)
	}
	if p := a.RefreshCountriesProgress(); !p.Running {
		t.Fatal("run B lost its slot to cancelled run A: progress reports not running during a live refresh")
	}
	if !a.CancelRefreshCountries() {
		t.Fatal("CancelRefreshCountries = false while run B is live: run A cleared B's registration")
	}
	unblock()
	if r := waitReturn(second, "run B"); r.err != nil {
		t.Fatalf("run B err = %v, want nil", r.err)
	}
	select {
	case <-pinned:
	case <-time.After(3 * time.Second):
		t.Error("store-lock pin goroutine never exited")
	}
}

// TestGetSystemProxyStateSmoke — BUG-010: the OS query answers without
// error on Windows (a missing ProxyEnable is "off", not a failure), and
// the bound method reports through the same read.
func TestGetSystemProxyStateSmoke(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("HKCU Internet Settings only exists on Windows")
	}
	on, server, err := osSystemProxy()
	if err != nil {
		t.Fatalf("osSystemProxy = %v, want nil error", err)
	}
	t.Logf("OS proxy enabled = %v server = %q", on, server)
	a := newGeoTestApp(t)
	if _, err := a.GetSystemProxyState(); err != nil {
		t.Fatalf("GetSystemProxyState = %v, want nil error", err)
	}
}
