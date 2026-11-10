package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// skipUnlessStockGeoDB skips when path is absent or is not a stock MaxMind
// GeoLite2-Country DB. A hand-"Customized" DB carries unknown mappings
// (the one shipped here maps 8.8.8.8 -> "GOOGLE"), so it is not a valid
// fixture for asserting the canonical 8.8.8.8 -> US result. Against a
// genuine DB the assertion still runs, so this never masks a reader bug.
func skipUnlessStockGeoDB(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skip("no real GeoLite2 DB present")
	}
	db, err := maxminddb.Open(path)
	if err != nil {
		t.Skipf("GeoLite2 DB at %s is unreadable: %v", path, err)
	}
	defer db.Close()
	md := db.Metadata
	if md.DatabaseType != "GeoLite2-Country" || strings.Contains(strings.ToLower(md.Description["en"]), "custom") {
		t.Skipf("%s is not a stock GeoLite2-Country DB (type=%q desc=%q); replace it with a genuine MaxMind DB to exercise this test", path, md.DatabaseType, md.Description["en"])
	}
}

// All tests run WITHOUT a real .mmdb (the user places it later), except
// TestGeoIPRealDB which skips when absent.

// TestSplitRecordRejectsMalformedCode — a corrupt/customized DB can
// return a non-ISO string (the shipped one maps 8.8.8.8 -> "GOOGLE").
// splitRecord must accept only two-letter alpha-2 codes and degrade
// everything else to empty so garbage never reaches the UI.
func TestSplitRecordRejectsMalformedCode(t *testing.T) {
	cases := []struct {
		raw      string
		wantCode string
	}{
		{"us", "US"},
		{"  de ", "DE"},
		{"GB", "GB"},
		{"", ""},
		{"GOOGLE", ""},
		{"USA", ""},
		{"U", ""},
		{"U1", ""},
		{"1S", ""},
		{"ÜS", ""},
	}
	for _, c := range cases {
		var rec geoCountryRecord
		rec.Country.ISOCode = c.raw
		rec.Country.Names = map[string]string{"en": "Example"}
		code, name, err := splitRecord(rec)
		if err != nil {
			t.Fatalf("splitRecord(%q) err = %v", c.raw, err)
		}
		if code != c.wantCode {
			t.Fatalf("splitRecord(%q) code = %q, want %q", c.raw, code, c.wantCode)
		}
		if code == "" && name != "" {
			t.Fatalf("splitRecord(%q) dropped code but kept name %q", c.raw, name)
		}
	}
}

// TestGeoIPMissingDB — missing file degrades: not available, empty lookup.
func TestGeoIPMissingDB(t *testing.T) {
	// Isolate from any user-placed DB: resolveDBPath auto-discovers
	// ./assets/GeoLite2-Country.mmdb relative to CWD, so run from an
	// empty dir to prove the missing-DB path regardless of the asset.
	t.Chdir(t.TempDir())
	g := NewGeoIP("Z:\\definitely\\not\\here\\GeoLite2-Country.mmdb")
	if g.Available() {
		t.Fatal("Available() = true with a missing DB")
	}
	code, name, err := g.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup err = %v", err)
	}
	if code != "" || name != "" {
		t.Fatalf("Lookup = (%q, %q), want empty", code, name)
	}
	if g.DBPath() != "" {
		t.Fatalf("DBPath = %q, want empty", g.DBPath())
	}
}

// TestGeoIPPrivateIP — private/loopback space has no country.
func TestGeoIPPrivateIP(t *testing.T) {
	g := NewGeoIP("Z:\\definitely\\not\\here\\GeoLite2-Country.mmdb")
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "::1", ""} {
		code, name, err := g.Lookup(host)
		if err != nil {
			t.Fatalf("Lookup(%q) err = %v", host, err)
		}
		if code != "" || name != "" {
			t.Fatalf("Lookup(%q) = (%q, %q), want empty", host, code, name)
		}
	}
}

// TestGeoIPHostnameResolution — localhost resolves to loopback only.
func TestGeoIPHostnameResolution(t *testing.T) {
	g := NewGeoIP("Z:\\definitely\\not\\here\\GeoLite2-Country.mmdb")
	code, name, err := g.Lookup("localhost")
	if err != nil {
		t.Fatalf("Lookup(localhost) err = %v", err)
	}
	if code != "" || name != "" {
		t.Fatalf("Lookup(localhost) = (%q, %q), want empty", code, name)
	}
}

func newGeoTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewApp(store, make(chan LogEntry, 512))
}

// TestDetectCountryNoDB — no DB: profile comes back unchanged, no error.
func TestDetectCountryNoDB(t *testing.T) {
	t.Chdir(t.TempDir()) // no auto-discovered assets DB (see TestGeoIPMissingDB)
	a := newGeoTestApp(t)
	saved, err := a.store.SaveProfile(Profile{Name: "t", Address: "8.8.8.8", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.DetectCountry(saved.ID)
	if err != nil {
		t.Fatalf("DetectCountry err = %v", err)
	}
	if got.CountryCode != "" || got.CountryName != "" {
		t.Fatalf("CountryCode = %q, CountryName = %q, want empty", got.CountryCode, got.CountryName)
	}
	if _, err := a.DetectCountry("no-such-id"); err == nil {
		t.Fatal("DetectCountry(bad id) err = nil, want error")
	}
}

// TestRefreshAllCountriesNoDB — Updated=0, Skipped=N, Failed=0, no error.
func TestRefreshAllCountriesNoDB(t *testing.T) {
	t.Chdir(t.TempDir()) // no auto-discovered assets DB (see TestGeoIPMissingDB)
	a := newGeoTestApp(t)
	for _, p := range []Profile{
		{Name: "a", Address: "1.1.1.1", Port: 443},
		{Name: "b", Address: "9.9.9.9", Port: 443},
		{Name: "c", Address: "10.9.9.9", Port: 443}, // private -> skip
	} {
		if _, err := a.store.SaveProfile(p); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.RefreshAllCountries()
	if err != nil {
		t.Fatalf("RefreshAllCountries err = %v", err)
	}
	if res.Updated != 0 || res.Skipped != 3 || res.Failed != 0 {
		t.Fatalf("result = %+v, want {Updated:0 Skipped:3 Failed:0}", res)
	}
	// GeoIPStatus mirrors the degraded state.
	st, err := a.GeoIPStatus()
	if err != nil {
		t.Fatalf("GeoIPStatus err = %v", err)
	}
	if st.Available {
		t.Fatal("GeoIPStatus.Available = true, want false")
	}
	// ClearCountries on empty countries is a no-op success.
	if err := a.ClearCountries(); err != nil {
		t.Fatalf("ClearCountries err = %v", err)
	}
}

// TestGeoIPRealDB — optional: runs only when a stock user-placed DB exists.
func TestGeoIPRealDB(t *testing.T) {
	skipUnlessStockGeoDB(t, "assets/GeoLite2-Country.mmdb")
	g := NewGeoIP("assets/GeoLite2-Country.mmdb")
	if !g.Available() {
		t.Fatal("Available() = false with a real DB present")
	}
	code, _, err := g.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup err = %v", err)
	}
	if code != "US" {
		t.Fatalf("Lookup(8.8.8.8) = %q, want US", code)
	}
}

// TestGeoIPConcurrentReloadAndLookup — BUG-021: lookups must hold the
// RLock for their whole duration so a concurrent Configure/Reload
// cannot close the reader under them. Hammer both sides from 8
// goroutines; run with -race.
func TestGeoIPConcurrentReloadAndLookup(t *testing.T) {
	g := NewGeoIP("Z:\\definitely\\not\\here\\GeoLite2-Country.mmdb")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				switch (i + j) % 5 {
				case 0:
					_, _, _ = g.Lookup("8.8.8.8")
				case 1:
					_, _, _ = g.Lookup("example.com")
				case 2:
					g.Configure("")
				case 3:
					_ = g.Reload()
				default:
					_ = g.Available()
					_ = g.DBPath()
					_ = g.LastErr()
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestNewestExistingPrefersTheUpdatedCopy — the portable build ships the
// GeoLite2 DB twice (bin\ and next to the exe) while the updater only
// rewrites the latter, so resolving by folder order kept serving the
// stale bin\ copy after every "successful" GeoIP update.
func TestNewestExistingPrefersTheUpdatedCopy(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	inBin := filepath.Join(bin, "GeoLite2-Country.mmdb")
	nextToExe := filepath.Join(dir, "GeoLite2-Country.mmdb")
	for _, p := range []string{inBin, nextToExe} {
		if err := os.WriteFile(p, []byte("db"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	set := func(p string, ts time.Time) {
		t.Helper()
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	set(inBin, old)
	set(nextToExe, time.Now())
	if got := newestExisting([]string{inBin, nextToExe}); got != nextToExe {
		t.Fatalf("updated copy next to the exe: got %q, want %q", got, nextToExe)
	}

	set(inBin, time.Now())
	set(nextToExe, old)
	if got := newestExisting([]string{inBin, nextToExe}); got != inBin {
		t.Fatalf("updated copy in bin: got %q, want %q", got, inBin)
	}

	missing := filepath.Join(dir, "assets", "GeoLite2-Country.mmdb")
	if got := newestExisting([]string{missing, nextToExe}); got != nextToExe {
		t.Fatalf("missing candidates must be skipped: got %q, want %q", got, nextToExe)
	}
	if got := newestExisting([]string{missing}); got != "" {
		t.Fatalf("nothing exists: got %q, want empty", got)
	}
	if got := newestExisting([]string{bin}); got != "" {
		t.Fatalf("a directory must never be picked: got %q, want empty", got)
	}
}

// TestGeoIPUpdateWhileMapped — the updater renames a freshly downloaded DB
// over the one the detector has open. On Windows the reader memory-maps the
// file, so this asserts the install order really works there: the rename
// succeeds despite the live mapping, and the Reload that follows serves the
// new file.
func TestGeoIPUpdateWhileMapped(t *testing.T) {
	skipUnlessStockGeoDB(t, "assets/GeoLite2-Country.mmdb")
	raw, err := os.ReadFile("assets/GeoLite2-Country.mmdb")
	if err != nil {
		t.Skip("no real GeoLite2 DB present")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "GeoLite2-Country.mmdb")
	if err := os.WriteFile(dbPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGeoIP(dbPath)
	if !g.Available() {
		t.Fatalf("Available() = false with a real DB, lastErr = %v", g.LastErr())
	}

	// Mirrors installFile: temp file next to the target, then rename over it
	// while the reader still holds the old mapping.
	tmp := filepath.Join(dir, ".update-test")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		t.Fatalf("renaming over a mapped DB failed: %v", err)
	}
	if err := g.Reload(); err != nil {
		t.Fatalf("Reload err = %v", err)
	}
	if !g.Available() {
		t.Fatalf("reader not usable after the swap, lastErr = %v", g.LastErr())
	}
	if got := g.DBPath(); got != dbPath {
		t.Fatalf("DBPath after swap = %q, want %q", got, dbPath)
	}
	code, _, err := g.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup err = %v", err)
	}
	if code != "US" {
		t.Fatalf("Lookup(8.8.8.8) = %q, want US", code)
	}
}

// TestRefreshCountriesHonorsCancel — BUG-024: a lookup cancelled
// mid-flight must surface as an error and must NOT write a stale
// profile afterwards. The countryLookup seam injects a slow fake.
func TestRefreshCountriesHonorsCancel(t *testing.T) {
	old := countryLookup
	defer func() { countryLookup = old }()
	release := make(chan struct{})
	lookupDone := make(chan struct{})
	countryLookup = func(a *App, p *Profile) (bool, error) {
		defer close(lookupDone)
		<-release // blocks until the test lets go
		p.CountryCode = "DE"
		p.CountryName = "Germany"
		return true, nil
	}
	a := newGeoTestApp(t)
	saved, err := a.store.SaveProfile(Profile{Name: "slow", Address: "8.8.8.8", Port: 443})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type rr struct {
		upd bool
		err error
	}
	done := make(chan rr, 1)
	go func() {
		upd, _, err := a.refreshOneCountry(ctx, saved)
		done <- rr{upd, err}
	}()
	cancel() // abort while the lookup is still blocked
	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("cancelled refresh returned nil error, want ctx error")
		}
		if r.upd {
			t.Fatal("cancelled refresh counted as success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refreshOneCountry did not abort on cancel")
	}
	close(release) // let the stranded lookup finish; nobody receives (buffered)
	<-lookupDone   // gate: the stranded goroutine has read the seam var
	// and returned, so reassigning it below cannot race.
	cur, ok := a.store.GetProfile(saved.ID)
	if !ok {
		t.Fatal("profile vanished")
	}
	if cur.CountryCode != "" {
		t.Fatalf("stale save after cancel: CountryCode = %q, want empty", cur.CountryCode)
	}

	// Happy path still persists (no regression from the rework).
	countryLookup = func(a *App, p *Profile) (bool, error) {
		p.CountryCode = "DE"
		p.CountryName = "Germany"
		return true, nil
	}
	upd, code, err := a.refreshOneCountry(context.Background(), saved)
	if err != nil {
		t.Fatalf("happy-path refresh err = %v", err)
	}
	if !upd || code != "DE" {
		t.Fatalf("happy-path refresh = (%v, %q), want (true, DE)", upd, code)
	}
	cur2, _ := a.store.GetProfile(saved.ID)
	if cur2.CountryCode != "DE" {
		t.Fatalf("happy-path CountryCode = %q, want DE", cur2.CountryCode)
	}
}

// TestRefreshOneCountryKeepsConcurrentEdits — a country lookup can take
// seconds, and the refresh used to write back the copy it took BEFORE the
// lookup, silently reverting any edit the user saved in between (and
// RefreshAllCountries then persisted that revert to config.json).
func TestRefreshOneCountryKeepsConcurrentEdits(t *testing.T) {
	old := countryLookup
	defer func() { countryLookup = old }()
	a := newGeoTestApp(t)
	saved, err := a.store.SaveProfile(Profile{Name: "orig", Address: "8.8.8.8", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	countryLookup = func(a *App, p *Profile) (bool, error) {
		// A user edit lands while the lookup is in flight.
		edited := *p
		edited.Name = "renamed"
		edited.Address = "1.1.1.1"
		if _, err := a.store.SaveProfile(edited); err != nil {
			t.Errorf("concurrent save: %v", err)
		}
		p.CountryCode = "DE"
		p.CountryName = "Germany"
		now := time.Now()
		p.CountryAt = &now
		return true, nil
	}
	upd, code, err := a.refreshOneCountry(context.Background(), saved)
	if err != nil {
		t.Fatalf("refreshOneCountry: %v", err)
	}
	if !upd || code != "DE" {
		t.Fatalf("refresh = (%v, %q), want (true, DE)", upd, code)
	}
	cur, ok := a.store.GetProfile(saved.ID)
	if !ok {
		t.Fatal("profile vanished")
	}
	if cur.Name != "renamed" || cur.Address != "1.1.1.1" {
		t.Fatalf("concurrent edit reverted: name=%q address=%q", cur.Name, cur.Address)
	}
	if cur.CountryCode != "DE" || cur.CountryName != "Germany" || cur.CountryAt == nil {
		t.Fatalf("country not stamped: %+v", cur)
	}
}
