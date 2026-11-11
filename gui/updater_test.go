package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// All updater tests redirect sources via updateBaseURL — no real network.
// updateBaseURL is process-global: save/restore it around each test.

func saveUpdateBaseURL(t *testing.T) {
	t.Helper()
	old := map[string]string{}
	for k, v := range updateBaseURL {
		old[k] = v
	}
	t.Cleanup(func() {
		for k := range updateBaseURL {
			delete(updateBaseURL, k)
		}
		for k, v := range old {
			updateBaseURL[k] = v
		}
	})
}

func newUpdaterTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	// The GeoIP reader memory-maps the database, and Windows refuses to
	// rewrite a file with a live mapping. updaterDataDir() is the test
	// binary's own directory, shared by every test in this package, so a
	// fixture that writes the database with os.WriteFile would fail on
	// whatever the previous test left mapped. installFile is unaffected
	// (it renames, which works), only the fixtures.
	t.Cleanup(func() {
		if app.geo != nil {
			app.geo.Configure(`Z\definitely\not\here\GeoLite2-Country.mmdb`)
		}
	})
	return app
}

// TestListUpdatablesNoFiles — no DBs, no cores, no externals: 7 rows,
// empty Current, no panic, no network.
func TestListUpdatablesNoFiles(t *testing.T) {
	a := newUpdaterTestApp(t)
	rows, err := a.ListUpdatables()
	if err != nil {
		t.Fatalf("ListUpdatables: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("got %d rows, want 7", len(rows))
	}
	want := map[string]bool{"geoip": true, "geosite": true, "singbox": true, "nekobox": true, "xray": true, "mihomo": true, "gui": true}
	for _, r := range rows {
		if !want[r.ID] {
			t.Fatalf("unexpected component %q", r.ID)
		}
		delete(want, r.ID)
		if r.Name == "" {
			t.Fatalf("%s has no name", r.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing components: %v", want)
	}
}

// TestUpdateComponentUnknownID — unknown IDs error cleanly.
func TestUpdateComponentUnknownID(t *testing.T) {
	a := newUpdaterTestApp(t)
	if _, err := a.UpdateComponent("nope"); err == nil {
		t.Fatal("expected error for unknown component")
	}
}

// TestUpdateComponentBlockedWhenCoreRunning — core updates refuse while
// connected. The manager flag is flipped directly (same package).
func TestUpdateComponentBlockedWhenCoreRunning(t *testing.T) {
	a := newUpdaterTestApp(t)
	a.cores.mu.Lock()
	a.cores.running = true
	a.cores.activeCore = CoreSingBox
	a.cores.mu.Unlock()
	defer func() {
		a.cores.mu.Lock()
		a.cores.running = false
		a.cores.mu.Unlock()
	}()
	for _, id := range []string{"singbox", "nekobox", "xray", "mihomo"} {
		if _, err := a.UpdateComponent(id); err == nil {
			t.Fatalf("UpdateComponent(%q) succeeded with a running core", id)
		} else if !strings.Contains(err.Error(), "disconnect") {
			t.Fatalf("UpdateComponent(%q) err = %q, want disconnect notice", id, err)
		}
	}
}

// fakeMMDBServer serves a tiny fake .mmdb plus an ETag on HEAD.
func fakeMMDBServer(t *testing.T, payload []byte, etag string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/gh/Loyalsoldier/geoip@release/Country.mmdb", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("ETag", `"`+etag+`"`)
			return
		}
		w.Write(payload)
	})
	return httptest.NewServer(mux)
}

// geoDBPayload returns bytes the GeoIP installer accepts: the bundled
// database with a salt appended. The reader ignores anything after the
// metadata marker, so two salts give two distinct "revisions" without a
// second database — and a made-up string is no longer an option, because
// updateDataFile refuses bytes that are not a database.
func geoDBPayload(t *testing.T, salt string) []byte {
	t.Helper()
	raw, err := os.ReadFile("assets/GeoLite2-Country.mmdb")
	if err != nil {
		t.Skip("no real GeoLite2 DB present")
	}
	return append(append([]byte{}, raw...), salt...)
}

// TestUpdateComponentGeoIP — redirected download installs the file,
// Current tracks the new hash, geo detector reloads without panic.
func TestUpdateComponentGeoIP(t *testing.T) {
	saveUpdateBaseURL(t)
	payload := geoDBPayload(t, "rev-1")
	srv := fakeMMDBServer(t, payload, "etag-v1")
	defer srv.Close()
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	got, err := a.UpdateComponent("geoip")
	if err != nil {
		t.Fatalf("UpdateComponent: %v", err)
	}
	dest := filepath.Join(updaterDataDir(), "GeoLite2-Country.mmdb")
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("installed file unreadable: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatal("installed file content mismatch")
	}
	if !strings.HasPrefix(got.Current, "sha:") {
		t.Fatalf("Current = %q, want sha: prefix", got.Current)
	}
	if got.UpdateAvail {
		t.Fatal("UpdateAvail should be false right after install")
	}
}

// TestGeoIPUpdateWithAnOverrideReportsTheTruth — Settings → GeoIP can point the
// detector at its own copy while the download always lands in the updater's data
// dir, so the update cannot take effect. The row must say that (and name both
// files) instead of reporting a clean install; the user's own path is never
// written to.
func TestGeoIPUpdateWithAnOverrideReportsTheTruth(t *testing.T) {
	raw, err := os.ReadFile("assets/GeoLite2-Country.mmdb")
	if err != nil {
		t.Skip("no real GeoLite2 DB present")
	}
	override := filepath.Join(t.TempDir(), "GeoLite2-Country.mmdb")
	if err := os.WriteFile(override, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	saveUpdateBaseURL(t)
	payload := append(append([]byte{}, raw...), "rev-2"...)
	srv := fakeMMDBServer(t, payload, "etag-v2")
	defer srv.Close()
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	a.geo.Configure(override)
	got, err := a.UpdateComponent("geoip")
	if err != nil {
		t.Fatalf("UpdateComponent: %v", err)
	}
	if !strings.Contains(got.Notes, "Settings → GeoIP overrides") {
		t.Fatalf("Notes = %q, want the override warning", got.Notes)
	}
	if !strings.Contains(got.Notes, override) {
		t.Fatalf("Notes = %q, want it to name %s", got.Notes, override)
	}
	if a.geo.DBPath() != override {
		t.Fatalf("DBPath = %q, want %q", a.geo.DBPath(), override)
	}
	after, err := os.ReadFile(override)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, raw) {
		t.Fatal("the update overwrote the user's override file")
	}
}

// TestUpdateComponentGeoIPRejectsNonDatabase — the jsDelivr GeoIP copy
// publishes no sha256, so verifyDigest cannot help here: the only thing
// standing between a truncated download, an error page or an LFS pointer
// and the file every country lookup reads is this check. It has to refuse
// BEFORE writing — accepting junk put it on disk, moved the revision marker
// to the new revision and left nothing to retry, so lookups went dead
// silently at the next restart.
func TestUpdateComponentGeoIPRejectsNonDatabase(t *testing.T) {
	saveUpdateBaseURL(t)
	good := geoDBPayload(t, "rev-1")
	dest := filepath.Join(updaterDataDir(), "GeoLite2-Country.mmdb")
	if err := os.WriteFile(dest, good, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []struct {
		name    string
		payload []byte
	}{
		{"error page", []byte("<html>403 — see vless://looks-like-a-link</html>")},
		{"empty body", []byte{}},
		{"lfs pointer", []byte("#!GITLFS version https://git-lfs.github.com/spec/v1\noid sha256:0000\nsize 7649827\n")},
		{"truncated", good[:len(good)/2]},
	} {
		srv := fakeMMDBServer(t, junk.payload, "etag-junk")
		updateBaseURL["jsdelivr"] = srv.URL
		a := newUpdaterTestApp(t)
		if _, err := a.UpdateComponent("geoip"); err == nil {
			t.Errorf("%s: junk download accepted, want refusal", junk.name)
		} else if !strings.Contains(err.Error(), "database") {
			t.Errorf("%s: err = %v, want it to name the database", junk.name, err)
		}
		srv.Close()
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, good) {
		t.Fatal("a refused download replaced the installed database")
	}
	// A marker left by an earlier test in this package (same shared
	// directory) is fine; one moved to the junk revision is not — that is
	// what would make checkGeoIP stop offering a re-download.
	if m, err := os.ReadFile(geoipMarker()); err == nil && strings.Contains(string(m), "junk") {
		t.Fatalf("a refused download moved the revision marker to %q", m)
	}
}

// TestSamePathFoldsSpelling — the resolver returns relative candidates while the
// install target is absolute, and Windows paths are case-insensitive, so equality
// has to survive all three spellings (and never match an empty path).
func TestSamePathFoldsSpelling(t *testing.T) {
	rel := filepath.Join("assets", "GeoLite2-Country.mmdb")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(rel, abs) {
		t.Fatalf("samePath(%q, %q) = false, want true", rel, abs)
	}
	if !samePath(abs, strings.ToUpper(abs)) {
		t.Fatal("case folding failed")
	}
	if samePath(abs, filepath.Join(filepath.Dir(abs), "other.mmdb")) {
		t.Fatal("different files reported equal")
	}
	if samePath("", abs) || samePath(abs, "") {
		t.Fatal("an empty path matched a real one")
	}
}

// fakeForkServer serves a fork releases list (newest first) plus raw
// downloads for each asset name. Tests redirect github-api at it.
func fakeForkServer(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/xenonama/NekoGo/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var parts []string
		for name, payload := range assets {
			parts = append(parts, `{"name":`+strconv.Quote(name)+
				`,"browser_download_url":`+strconv.Quote(srv.URL+"/dl/"+name)+
				`,"size":`+strconv.Itoa(len(payload))+`}`)
		}
		_, _ = w.Write([]byte(`[{"tag_name":` + strconv.Quote(tag) + `,"name":"test","assets":[` + strings.Join(parts, ",") + `]}]`))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		if p, ok := assets[name]; ok {
			_, _ = w.Write(p)
			return
		}
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	return srv
}

// makeZipBytes packs one named file into a zip archive.
func makeZipBytes(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stubExeVersion replaces the exe smoke-verify probe (tests never ship
// a real executable). Restored automatically.
func stubExeVersion(t *testing.T, out string) {
	t.Helper()
	old := exeVersionQuery
	exeVersionQuery = func(path string, args ...string) string { return out }
	t.Cleanup(func() { exeVersionQuery = old })
}

// TestCheckCoreForkRelease — fork assets resolve Latest + UpdateAvail
// for singbox/xray without touching upstream or the real network.
func TestCheckCoreForkRelease(t *testing.T) {
	saveUpdateBaseURL(t)
	srv := fakeForkServer(t, "v9.9.9", map[string][]byte{
		"nekobox_core.exe": []byte("nk"),
		"xray.exe":         []byte("xr"),
	})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.ID != "nekobox" && r.ID != "xray" {
			continue
		}
		seen[r.ID] = true
		if r.Latest != "v9.9.9" {
			t.Fatalf("%s: Latest=%q, want v9.9.9", r.ID, r.Latest)
		}
		if !r.UpdateAvail {
			t.Fatalf("%s: UpdateAvail=false, want true (nothing installed)", r.ID)
		}
		if r.LastError != "" {
			t.Fatalf("%s: LastError=%q, want empty", r.ID, r.LastError)
		}
	}
	for _, id := range []string{"nekobox", "xray"} {
		if !seen[id] {
			t.Fatalf("missing %s row", id)
		}
	}
}

// TestCheckCoreNoAsset — a fork release without core assets keeps the
// rows manual-only (honest note, no Update button) instead of failing.
func TestCheckCoreNoAsset(t *testing.T) {
	saveUpdateBaseURL(t)
	srv := fakeForkServer(t, "v9.9.9", map[string][]byte{
		"NekoGo-Setup-notes.txt": []byte("notes"),
	})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	seen := 0
	for _, r := range rows {
		if r.ID != "nekobox" && r.ID != "xray" {
			continue
		}
		seen++
		if r.UpdateAvail {
			t.Fatalf("%s: UpdateAvail=true, want false (no asset published)", r.ID)
		}
		if !strings.Contains(strings.ToLower(r.Notes), "manual") {
			t.Fatalf("%s: Notes=%q, want manual-only note", r.ID, r.Notes)
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d core rows, want 2", seen)
	}
}

// TestCheckCoreNoReleases — no fork release published yet: honest
// note, no error, no Update button.
func TestCheckCoreNoReleases(t *testing.T) {
	saveUpdateBaseURL(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	for _, r := range rows {
		if r.ID != "nekobox" && r.ID != "xray" {
			continue
		}
		if r.UpdateAvail {
			t.Fatalf("%s: UpdateAvail=true, want false", r.ID)
		}
		if r.LastError != "" {
			t.Fatalf("%s: LastError=%q, want empty", r.ID, r.LastError)
		}
		if !strings.Contains(r.Notes, "No core release") {
			t.Fatalf("%s: Notes=%q, want no-release note", r.ID, r.Notes)
		}
	}
}

// TestCoreDisplayVersionUnknownFallback — a core binary reporting
// "unknown" (built without the -X version stamp) is identified by
// file hash instead of showing "sing-box version unknown", and a
// missing binary stays "Not installed". Real versions pass through.
func TestCoreDisplayVersionUnknownFallback(t *testing.T) {
	a := newUpdaterTestApp(t)
	exe := filepath.Join(t.TempDir(), "nekobox_core.exe")
	if err := os.WriteFile(exe, []byte("unstamped-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.singbox.setCorePath(exe)
	cur, note := a.coreDisplayVersion("nekobox", "sing-box version unknown")
	if !strings.HasPrefix(cur, "sha:") {
		t.Fatalf("Current=%q, want sha: fallback", cur)
	}
	if note == "" {
		t.Fatal("want a missing-stamp note")
	}
	cur, _ = a.coreDisplayVersion("nekobox", "sing-box version 1.13.19")
	if cur != "sing-box version 1.13.19" {
		t.Fatalf("Current=%q, want the real version line", cur)
	}
	a.singbox.setCorePath(filepath.Join(t.TempDir(), "missing.exe"))
	cur, note = a.coreDisplayVersion("nekobox", "")
	if cur != "" || note != "Not installed" {
		t.Fatalf("got (%q, %q), want empty + Not installed", cur, note)
	}
}

// TestUpdateCoreInstallsFromFork — a fork raw-exe asset replaces the
// configured core path; the row reports the new tag.
func TestUpdateCoreInstallsFromFork(t *testing.T) {
	saveUpdateBaseURL(t)
	stubExeVersion(t, "sing-box version 9.9.9")
	payload := []byte("fake-nekobox-core-v999")
	srv := fakeForkServer(t, "v9.9.9", map[string][]byte{"nekobox_core.exe": payload})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "nekobox_core.exe")
	if err := os.WriteFile(dest, []byte("old-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.singbox.setCorePath(dest)
	got, err := a.UpdateComponent("nekobox")
	if err != nil {
		t.Fatalf("UpdateComponent: %v", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("installed core unreadable: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatal("installed core content mismatch")
	}
	if _, err := os.Stat(dest + ".bak"); !os.IsNotExist(err) {
		t.Fatal("stale .bak left behind after a good install")
	}
	if got.Latest != "v9.9.9" || got.UpdateAvail {
		t.Fatalf("row = %+v, want Latest=v9.9.9 UpdateAvail=false", got)
	}
}

// TestUpdateCoreRollbackKeepsPrevious — a downloaded binary that fails
// the smoke probe must not replace the working one: the backup is
// restored and the error says so.
func TestUpdateCoreRollbackKeepsPrevious(t *testing.T) {
	saveUpdateBaseURL(t)
	// Real probe (no stub): junk bytes cannot execute, so verify fails.
	payload := []byte("not-an-executable")
	srv := fakeForkServer(t, "v9.9.9", map[string][]byte{"xray.exe": payload})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "xray.exe")
	before := []byte("working-xray")
	if err := os.WriteFile(dest, before, 0o644); err != nil {
		t.Fatal(err)
	}
	a.xray.setXrayPath(dest)
	_, err := a.UpdateComponent("xray")
	if err == nil || !strings.Contains(err.Error(), "failed to run") {
		t.Fatalf("err = %v, want failed-to-run rollback", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("previous binary was not restored")
	}
}

// TestUpdateGUIDoesNotAutoOpen (BUG-010) — a GUI release download must
// land on disk with a manual-open note and must NOT auto-open: the
// auto-open hook stays unset and is never invoked.
func TestUpdateGUIDoesNotAutoOpen(t *testing.T) {
	saveUpdateBaseURL(t)
	payload := []byte("fake-installer-bytes-v1")

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/xenonama/NekoGo/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"v9.9.9","name":"test","assets":[` +
			`{"name":"NekoGo-Setup-test.exe","browser_download_url":"` + srv.URL + `/dl/NekoGo-Setup-test.exe","size":` +
			strconv.Itoa(len(payload)) + `}]}]`))
	})
	mux.HandleFunc("/dl/NekoGo-Setup-test.exe", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	// Production default: no auto-open hook. updateGUI's only open path
	// is this hook, so nil here proves openURLFallback is unreachable
	// from the update flow.
	if installerAutoOpen != nil {
		t.Fatal("installerAutoOpen hook set; want nil (no auto-open in production)")
	}
	// Spy on the opener itself: the download must land on disk without
	// any open attempt.
	oldOpener := openURLFallback
	opened := false
	openURLFallback = func(path string) error {
		opened = true
		return nil
	}
	defer func() { openURLFallback = oldOpener }()

	a := newUpdaterTestApp(t)
	got, err := a.UpdateComponent("gui")
	if err != nil {
		t.Fatalf("UpdateComponent(gui): %v", err)
	}
	if opened {
		t.Fatal("updateGUI auto-opened the installer; want download-only")
	}
	if got.InstallerPath == "" {
		t.Fatal("InstallerPath empty; want the downloaded file path for the manual Open button")
	}
	raw, err := os.ReadFile(got.InstallerPath)
	if err != nil {
		t.Fatalf("downloaded installer unreadable: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatal("downloaded installer content mismatch")
	}
	if !strings.Contains(strings.ToLower(got.Notes), "manually") {
		t.Fatalf("Notes = %q, want a manual-open instruction", got.Notes)
	}
}

// TestUpdateComponentIdenticalIsNoop — same bytes locally and remotely:
// no error, an "already up to date" row, the file untouched, and the
// revision marker written so the next check stops offering the update.
func TestUpdateComponentIdenticalIsNoop(t *testing.T) {
	saveUpdateBaseURL(t)
	payload := geoDBPayload(t, "rev-1")
	srv := fakeMMDBServer(t, payload, "etag-v1")
	defer srv.Close()
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(updaterDataDir(), "GeoLite2-Country.mmdb")
	if err := os.WriteFile(dest, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.UpdateComponent("geoip")
	if err != nil {
		t.Fatalf("identical content must be a quiet no-op, got err = %v", err)
	}
	if got == nil || got.UpdateAvail {
		t.Fatalf("row = %+v, want UpdateAvail=false", got)
	}
	if !strings.Contains(strings.ToLower(got.Notes), "already up to date") {
		t.Fatalf("Notes = %q, want an up-to-date note", got.Notes)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("identical file was rewritten")
	}
	marker, err := os.ReadFile(geoipMarker())
	if err != nil {
		t.Fatalf("geoip marker not written: %v", err)
	}
	if strings.TrimSpace(string(marker)) != "etag-v1" {
		t.Fatalf("geoip marker = %q, want the served etag", marker)
	}
}

// --- official sing-box (SagerNet) row ---

// fakeOfficialServer serves SagerNet's /releases/latest plus raw asset
// downloads. Only the official repo path is answered; other component
// checks 404 and are ignored by the tests below.
func fakeOfficialServer(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/SagerNet/sing-box/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var parts []string
		for name, payload := range assets {
			parts = append(parts, `{"name":`+strconv.Quote(name)+
				`,"browser_download_url":`+strconv.Quote(srv.URL+"/dl/"+name)+
				`,"size":`+strconv.Itoa(len(payload))+`}`)
		}
		_, _ = w.Write([]byte(`{"tag_name":` + strconv.Quote(tag) + `,"name":"test","assets":[` + strings.Join(parts, ",") + `]}`))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		if p, ok := assets[name]; ok {
			_, _ = w.Write(p)
			return
		}
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	return srv
}

func officialAssetName(tag string) string {
	return "sing-box-" + normTag(tag) + "-windows-" + runtime.GOARCH + ".zip"
}

// TestCheckOfficialSingBoxRelease — the singbox row follows SagerNet's
// stable release and ignores the fork's assets entirely.
func TestCheckOfficialSingBoxRelease(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the official row installs Windows zips only")
	}
	srv := fakeOfficialServer(t, "v9.9.9", map[string][]byte{
		officialAssetName("v9.9.9"):                         makeZipBytes(t, "sing-box.exe", []byte("new")),
		"sing-box-9.9.9-windows-amd64-legacy-windows-7.zip": makeZipBytes(t, "sing-box.exe", []byte("old")),
	})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	var got *UpdatableComponent
	for i := range rows {
		if rows[i].ID == "singbox" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("missing singbox row")
	}
	if got.Latest != "v9.9.9" || !got.UpdateAvail {
		t.Fatalf("row = %+v, want Latest=v9.9.9 UpdateAvail=true", *got)
	}
	if got.LastError != "" {
		t.Fatalf("LastError = %q, want empty", got.LastError)
	}
	if !strings.Contains(got.Name, "official") {
		t.Fatalf("Name = %q, want it to identify the official CLI", got.Name)
	}
}

// TestUpdateOfficialSingBoxFromZip — the official zip's sing-box.exe is
// installed at the path resolveUpstreamCore reads (UpstreamCorePath).
func TestUpdateOfficialSingBoxFromZip(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the official row installs Windows zips only")
	}
	stubExeVersion(t, "sing-box version 9.9.9")
	payload := []byte("official-sing-box-bytes")
	srv := fakeOfficialServer(t, "v9.9.9", map[string][]byte{
		officialAssetName("v9.9.9"): makeZipBytes(t, "sing-box.exe", payload),
	})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "sing-box.exe")
	s := a.store.GetSettings()
	s.UpstreamCorePath = dest
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	got, err := a.UpdateComponent("singbox")
	if err != nil {
		t.Fatalf("UpdateComponent(singbox): %v", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("installed core unreadable: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatal("installed core content mismatch")
	}
	if _, err := os.Stat(dest + ".bak"); !os.IsNotExist(err) {
		t.Fatal("stale .bak left behind after a good install")
	}
	if got.Latest != "v9.9.9" || got.UpdateAvail {
		t.Fatalf("row = %+v, want Latest=v9.9.9 UpdateAvail=false", *got)
	}
}

// TestUpdateOfficialSingBoxRejectsForeignCorePath — an upstreamCorePath
// naming nekobox_core.exe cannot host the upstream CLI: fail with a
// pointer to the setting instead of installing somewhere the engine
// never reads (and never over the custom core's binary).
func TestUpdateOfficialSingBoxRejectsForeignCorePath(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the official row installs Windows zips only")
	}
	srv := fakeOfficialServer(t, "v9.9.9", map[string][]byte{
		officialAssetName("v9.9.9"): makeZipBytes(t, "sing-box.exe", []byte("x")),
	})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	s := a.store.GetSettings()
	s.UpstreamCorePath = filepath.Join(t.TempDir(), "nekobox_core.exe")
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateComponent("singbox"); err == nil {
		t.Fatal("expected an error for an upstreamCorePath naming the custom core")
	} else if !strings.Contains(err.Error(), "upstreamCorePath") {
		t.Fatalf("err = %v, want an upstreamCorePath hint", err)
	}
}

// TestOfficialSingBoxAssetSelection — exact platform zip only; legacy
// Windows 7 builds and other architectures are skipped.
func TestOfficialSingBoxAssetSelection(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the official row installs Windows zips only")
	}
	legacy := "sing-box-1.0.0-windows-amd64-legacy-windows-7.zip"
	other := "sing-box-1.0.0-windows-arm64.zip"
	if runtime.GOARCH == "arm64" {
		other = "sing-box-1.0.0-windows-amd64.zip"
	}
	want := "sing-box-1.0.0-windows-" + runtime.GOARCH + ".zip"
	got := officialSingBoxAsset([]ghAsset{
		{Name: legacy},
		{Name: other},
		{Name: want},
		{Name: "sing-box-1.0.0-linux-amd64.tar.gz"},
	})
	if got == nil || got.Name != want {
		t.Fatalf("officialSingBoxAsset = %+v, want %q", got, want)
	}
	if officialSingBoxAsset([]ghAsset{{Name: legacy}, {Name: other}}) != nil {
		t.Fatal("matched a non-platform asset")
	}
}

// TestUpdateNeededOrdersVersions — the ordering every Updates row depends
// on. The old test was strings.Contains(current, latest): a build stamped
// "1.0.0-beta.1" CONTAINS "1.0.0", so the day a stable v1.0.0 was published
// the row claimed parity and the release was never offered.
func TestUpdateNeededOrdersVersions(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0-beta.1", "v1.0.0", true}, // prerelease -> stable
		{"1.0.0", "v1.0.0", false},       // same release
		{"1.0.0", "v1.0.1", true},        // patch bump
		{"1.0.1", "v1.0.0", false},       // newer than published
		{"1.2", "v1.2.0", false},         // missing trailing part == 0
		{"1.0.0-beta.1", "v1.0.0-beta.2", true},
		{"1.0.0-beta.2", "v1.0.0-beta.1", false},
		{"1.0.0-beta.1", "v1.0.0-alpha.9", false}, // alpha < beta
		{"1.0.0-rc.1", "v1.0.0-beta.9", false},    // rc > beta
		{"1.0.0-beta.1", "v1.0.0-beta.1.1", true}, // more identifiers ranks higher
		{"", "v1.0.0", true},                      // nothing installed
		{"1.0.0", "", false},                      // nothing published
		{"unknown", "v1.2.3", true},               // unstamped core: offer it
		{"unknown", "unknown", false},
		{"5.11.28.3-fork.1", "v1.0.0", false}, // fork versioning is ahead
		{"1.0.0+build.5", "v1.0.0", false},    // build metadata is ignored
		// Core rows carry the raw "<exe> version X" banner: the token is
		// what must be compared, or every row claimed an update forever.
		{"sing-box version 1.13.19", "v1.13.19", false},
		{"sing-box version 1.13.19", "v1.13.20", true},
		{"Xray 25.9.11 (Xray, Penetrates Everything.) Custom (go1.24.7 windows/amd64)", "v25.9.12", true},
		{"Xray 25.9.11 (Xray, Penetrates Everything.) Custom (go1.24.7 windows/amd64)", "v25.9.11", false},
		{"sing-box version 1.0.0-beta.1", "v1.0.0", true},
		{"sing-box version 1.0.0-beta.1", "v1.0.0-beta.1", false},
	}
	for _, c := range cases {
		if got := updateNeeded(c.current, c.latest); got != c.want {
			t.Errorf("updateNeeded(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

// TestGeoIPUpdateTargetsTheLiveCopy: the updater must overwrite the
// database the detector reads (bin\ in the shipped layouts), instead of
// creating a second copy next to the exe that then wins by mtime and
// leaves bin\ stale. A Settings → GeoIP override is never written to.
func TestGeoIPUpdateTargetsTheLiveCopy(t *testing.T) {
	if _, err := os.Stat("assets/GeoLite2-Country.mmdb"); err != nil {
		t.Skip("gui/assets/GeoLite2-Country.mmdb not present")
	}
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	// Simulate the shipped layout next to the "exe": <base>\bin\...mmdb.
	// A real database is copied so the detector can open it (DBPath only
	// reports a successfully opened reader).
	db := filepath.Join(updaterDataDir(), binDirName, "GeoLite2-Country.mmdb")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("assets/GeoLite2-Country.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, seed, 0o644); err != nil {
		t.Fatal(err)
	}
	app.geo.Configure(db)
	if got := app.geoipDest("GeoLite2-Country.mmdb"); !samePath(got, db) {
		t.Fatalf("dest = %q, want the live bin copy %q", got, db)
	}
	// A dev-tree assets\ copy is a build input: never the target.
	dev, err := filepath.Abs("assets/GeoLite2-Country.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	app.geo.Configure(dev)
	if got := app.geoipDest("GeoLite2-Country.mmdb"); samePath(got, dev) {
		t.Fatalf("dev assets db was chosen as an update target: %q", got)
	}
	// A user-chosen override is never written either.
	s := store.GetSettings()
	s.GeoIPDBPath = filepath.Join(t.TempDir(), "custom.mmdb")
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := app.geoipDest("GeoLite2-Country.mmdb"); samePath(got, db) {
		t.Fatalf("override path was written: %q", got)
	}
}

// --- mihomo (MetaCubeX) row ---

// mihomoAssetName mirrors the archive build.bat fetches: the GOAMD64=v1
// baseline build on amd64, the plain arch build elsewhere.
func mihomoAssetName(tag string) string {
	level := ""
	if runtime.GOARCH == "amd64" {
		level = "-v1"
	}
	return "mihomo-windows-" + runtime.GOARCH + level + "-v" + normTag(tag) + ".zip"
}

func setMihomoPath(t *testing.T, a *App, p string) {
	t.Helper()
	s := a.store.GetSettings()
	s.MihomoPath = p
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
}

// fakeMihomoServer serves MetaCubeX's stable release at /releases/latest
// and the rolling "Prerelease-Alpha" at /releases, so a check that reads
// the wrong endpoint reports the alpha and fails. digests carries the
// published sha256 per asset name; a name absent from it publishes none.
func fakeMihomoServer(t *testing.T, tag string, assets map[string][]byte, digests map[string]string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	assetJSON := func() string {
		var parts []string
		for name, payload := range assets {
			part := `{"name":` + strconv.Quote(name) +
				`,"browser_download_url":` + strconv.Quote(srv.URL+"/dl/"+name) +
				`,"size":` + strconv.Itoa(len(payload))
			if d := digests[name]; d != "" {
				part += `,"digest":` + strconv.Quote(d)
			}
			parts = append(parts, part+`}`)
		}
		return strings.Join(parts, ",")
	}
	mux.HandleFunc("/repos/MetaCubeX/mihomo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":` + strconv.Quote(tag) + `,"name":"stable","assets":[` + assetJSON() + `]}`))
	})
	mux.HandleFunc("/repos/MetaCubeX/mihomo/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"Prerelease-Alpha","name":"alpha","assets":[]}]`))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		if p, ok := assets[name]; ok {
			_, _ = w.Write(p)
			return
		}
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	return srv
}

// TestMihomoAssetSelection — the real v1.19.33 release ships 31 Windows
// archives and every amd64 one of them contains "windows-amd64":
// -compatible (legacy CPU build), -go120..-go125 (old toolchains),
// -v2/-v3 (instruction sets that will not start on every machine NekoGo
// ships to) and a plain amd64 build. Only the GOAMD64=v1 baseline is
// installable, so the name is constructed rather than substring-matched.
func TestMihomoAssetSelection(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the mihomo row installs Windows zips only")
	}
	const tag = "v1.19.33"
	names := []string{
		"mihomo-windows-386-go120-v1.19.33.zip",
		"mihomo-windows-386-v1.19.33.zip",
		"mihomo-windows-amd64-compatible-v1.19.33.zip",
		"mihomo-windows-amd64-v1-go120-v1.19.33.zip",
		"mihomo-windows-amd64-v1-go125-v1.19.33.zip",
		"mihomo-windows-amd64-v1-v1.19.33.zip",
		"mihomo-windows-amd64-v1.19.33.zip",
		"mihomo-windows-amd64-v2-go124-v1.19.33.zip",
		"mihomo-windows-amd64-v2-v1.19.33.zip",
		"mihomo-windows-amd64-v3-v1.19.33.zip",
		"mihomo-windows-arm64-v1.19.33.zip",
		"mihomo-linux-amd64-v1.19.33.gz",
	}
	want := mihomoAssetName(tag)
	var assets, traps []ghAsset
	for _, n := range names {
		assets = append(assets, ghAsset{Name: n})
		if n != want {
			traps = append(traps, ghAsset{Name: n})
		}
	}
	got := mihomoAsset(assets, tag)
	if got == nil || got.Name != want {
		t.Fatalf("mihomoAsset = %+v, want %q", got, want)
	}
	if mihomoAsset(traps, tag) != nil {
		t.Fatal("matched a compatible/go12x/v2/v3/plain-arch trap instead of requiring the exact name")
	}
	if got := mihomoAsset(assets, normTag(tag)); got == nil || got.Name != want {
		t.Fatalf("an un-prefixed tag = %+v, want %q", got, want)
	}
	if mihomoAsset(assets, "v9.9.9") != nil {
		t.Fatal("matched an asset from a different release")
	}
}

// TestCheckMihomoUsesStableRelease — the row must follow /releases/latest.
// The newest entry of /releases is the rolling "Prerelease-Alpha" tag,
// which carries no version: comparing against it would offer the same
// alpha forever.
func TestCheckMihomoUsesStableRelease(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the mihomo row installs Windows zips only")
	}
	zipBytes := makeZipBytes(t, "mihomo-windows-amd64-v1.exe", []byte("mihomo"))
	name := mihomoAssetName("v9.9.9")
	srv := fakeMihomoServer(t, "v9.9.9",
		map[string][]byte{name: zipBytes},
		map[string]string{name: "sha256:" + sha256Hex(zipBytes)})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	// Force "not installed" so a mihomo on this machine's PATH cannot
	// decide the assertion.
	setMihomoPath(t, a, filepath.Join(t.TempDir(), "absent", "mihomo.exe"))
	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	var got *UpdatableComponent
	for i := range rows {
		if rows[i].ID == "mihomo" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("missing mihomo row")
	}
	if got.Latest != "v9.9.9" {
		t.Fatalf("Latest = %q, want the stable v9.9.9 (not Prerelease-Alpha)", got.Latest)
	}
	if !got.UpdateAvail {
		t.Fatalf("row = %+v, want UpdateAvail=true with nothing installed", *got)
	}
	if got.LastError != "" {
		t.Fatalf("LastError = %q, want empty", got.LastError)
	}
	if !strings.Contains(got.Name, "Clash Meta") {
		t.Fatalf("Name = %q, want it to identify the engine", got.Name)
	}
}

// TestUpdateMihomoFromZip — the archive's inner binary is named after the
// asset (mihomo-windows-amd64-v1.exe, measured in the real v1.19.33 zip),
// so it must be found by prefix and written to the path resolveMihomo
// reads. The published digest is checked before anything is extracted.
func TestUpdateMihomoFromZip(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the mihomo row installs Windows zips only")
	}
	stubExeVersion(t, "Mihomo Meta v9.9.9 windows amd64")
	payload := []byte("mihomo-engine-bytes")
	zipBytes := makeZipBytes(t, "mihomo-windows-amd64-v1.exe", payload)
	name := mihomoAssetName("v9.9.9")
	srv := fakeMihomoServer(t, "v9.9.9",
		map[string][]byte{name: zipBytes},
		map[string]string{name: "sha256:" + sha256Hex(zipBytes)})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "mihomo.exe")
	setMihomoPath(t, a, dest)
	got, err := a.UpdateComponent("mihomo")
	if err != nil {
		t.Fatalf("UpdateComponent(mihomo): %v", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("installed engine unreadable: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatal("installed engine content mismatch (the archive's inner name was not matched)")
	}
	if _, err := os.Stat(dest + ".bak"); !os.IsNotExist(err) {
		t.Fatal("stale .bak left behind after a good install")
	}
	if got.Latest != "v9.9.9" || got.UpdateAvail {
		t.Fatalf("row = %+v, want Latest=v9.9.9 UpdateAvail=false", *got)
	}
	// Same bytes again: a quiet no-op, not an error row.
	again, err := a.UpdateComponent("mihomo")
	if err != nil {
		t.Fatalf("re-installing identical bytes failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(again.Notes), "already up to date") {
		t.Fatalf("Notes = %q, want an up-to-date note", again.Notes)
	}
}

// TestUpdateMihomoRefusesDigestMismatch — a download whose bytes do not
// match the sha256 GitHub published must never reach the install path,
// and the working binary must survive untouched.
func TestUpdateMihomoRefusesDigestMismatch(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the mihomo row installs Windows zips only")
	}
	stubExeVersion(t, "Mihomo Meta v9.9.9 windows amd64")
	zipBytes := makeZipBytes(t, "mihomo-windows-amd64-v1.exe", []byte("tampered"))
	name := mihomoAssetName("v9.9.9")
	srv := fakeMihomoServer(t, "v9.9.9",
		map[string][]byte{name: zipBytes},
		map[string]string{name: "sha256:" + sha256Hex([]byte("the-real-archive"))})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "mihomo.exe")
	before := []byte("working-mihomo")
	if err := os.WriteFile(dest, before, 0o644); err != nil {
		t.Fatal(err)
	}
	setMihomoPath(t, a, dest)
	_, err := a.UpdateComponent("mihomo")
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want a sha256 mismatch refusal", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("an unverified download replaced the working binary")
	}
	if _, err := os.Stat(dest + ".bak"); !os.IsNotExist(err) {
		t.Fatal("a .bak was written before the digest was checked")
	}
}

// TestMihomoVersionBannerOrdersAgainstItsTag measures the real staged
// engine: -v must produce a banner the version checker can order against
// a release tag, and the per-path cache must serve it without re-running.
func TestMihomoVersionBannerOrdersAgainstItsTag(t *testing.T) {
	bin := testMihomoBin(t)
	a := newUpdaterTestApp(t)
	setMihomoPath(t, a, bin)
	v := a.MihomoVersion()
	if v == "" {
		t.Fatal("MihomoVersion() empty for the staged engine")
	}
	tok := versionToken(v)
	if tok == "" {
		t.Fatalf("banner %q carries no dotted version token", v)
	}
	if updateNeeded(v, "v"+tok) {
		t.Fatalf("the installed banner %q claims an update against its own tag v%s", v, tok)
	}
	if !updateNeeded(v, "v999.0.0") {
		t.Fatalf("banner %q did not offer a newer release", v)
	}
	if again := a.MihomoVersion(); again != v {
		t.Fatalf("second call = %q, want the cached %q", again, v)
	}
	a.invalidateCoreVersion("mihomo")
	a.mihomoVer.mu.Lock()
	cleared := a.mihomoVer.version
	a.mihomoVer.mu.Unlock()
	if cleared != "" {
		t.Fatal("invalidateCoreVersion(mihomo) left a stale version")
	}
	if got := a.MihomoVersion(); got != v {
		t.Fatalf("after invalidation = %q, want a fresh %q", got, v)
	}
}

// --- digest verification and hash-stamped rows ---

// TestVerifyDigest — a published sha256 is enforced, an absent or
// malformed one is not (the source simply publishes none, and refusing
// every such asset would break the fork's own cores and the jsDelivr
// GeoIP copy).
func TestVerifyDigest(t *testing.T) {
	a := newUpdaterTestApp(t)
	data := []byte("asset-bytes")
	good := "sha256:" + sha256Hex(data)
	if err := a.verifyDigest("xray", "xray.exe", good, data); err != nil {
		t.Fatalf("matching digest rejected: %v", err)
	}
	if err := a.verifyDigest("xray", "xray.exe", strings.ToUpper(good), data); err != nil {
		t.Fatalf("case-folded digest rejected: %v", err)
	}
	err := a.verifyDigest("xray", "xray.exe", "sha256:"+sha256Hex([]byte("other")), data)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want a sha256 mismatch refusal", err)
	}
	for _, absent := range []string{"", "sha512:abcd", "sha256:tooshort", "sha256:" + strings.Repeat("z", 64)} {
		if err := a.verifyDigest("xray", "xray.exe", absent, data); err != nil {
			t.Fatalf("unpublished/malformed digest %q rejected: %v", absent, err)
		}
	}
}

// TestCoreUpdateNeededHashStamp — an unstamped core is identified by
// "sha:<12 hex>", which no version ordering can compare against a tag:
// the old fallback answered "newer" forever, so the row offered an
// update after every check even when the installed bytes were the
// published asset. A raw binary asset's digest settles it exactly.
func TestCoreUpdateNeededHashStamp(t *testing.T) {
	payload := []byte("installed-core-bytes")
	sum := sha256Hex(payload)
	stamp := "sha:" + sum[:12]
	raw := &ghAsset{Name: "nekobox_core.exe", Digest: "sha256:" + sum}
	if coreUpdateNeeded(stamp, "v9.9.9", raw) {
		t.Fatal("an installed binary whose bytes ARE the published asset still offers an update")
	}
	other := &ghAsset{Name: "nekobox_core.exe", Digest: "sha256:" + sha256Hex([]byte("different-bytes"))}
	if !coreUpdateNeeded(stamp, "v9.9.9", other) {
		t.Fatal("a changed asset was not offered")
	}
	// An archive's digest hashes the CONTAINER, never the exe inside it,
	// so the exact comparison is unavailable and the fallback stands.
	archive := &ghAsset{Name: "mihomo-windows-amd64-v1-v9.9.9.zip", Digest: "sha256:" + sum}
	if !coreUpdateNeeded(stamp, "v9.9.9", archive) {
		t.Fatal("an archive digest was compared against the extracted exe's hash")
	}
	if !coreUpdateNeeded(stamp, "v9.9.9", &ghAsset{Name: "nekobox_core.exe"}) {
		t.Fatal("a digest-less asset stopped offering the update")
	}
	// Real version banners keep ordering normally, digest or not.
	if coreUpdateNeeded("sing-box version 9.9.9", "v9.9.9", raw) {
		t.Fatal("equal versions offered an update")
	}
	if !coreUpdateNeeded("sing-box version 1.0.0", "v9.9.9", raw) {
		t.Fatal("an older version did not offer the update")
	}
}

// TestCheckUpdatesFetchesTheForkOnce — nekobox, xray and gui all read the
// fork's newest release. GitHub allows 60 unauthenticated calls an hour
// and the panel's per-row Check button runs a whole pass, so one lookup
// per repo is the budget, not one per row.
func TestCheckUpdatesFetchesTheForkOnce(t *testing.T) {
	saveUpdateBaseURL(t)
	var hits int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/xenonama/NekoGo/releases", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"v9.9.9","name":"test","assets":[` +
			`{"name":"nekobox_core.exe","browser_download_url":"` + srv.URL + `/dl/nk","size":2},` +
			`{"name":"xray.exe","browser_download_url":"` + srv.URL + `/dl/xr","size":2}]}]`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv = httptest.NewServer(mux)
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	if _, err := a.CheckUpdates(); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("the fork release was fetched %d times in one pass, want 1", n)
	}
}

// TestGeositeIgnoresTheChecksumAsset — the release ships geosite.dat AND
// geosite.dat.sha256sum. GitHub orders assets by upload, not by name, so
// a substring picker can take the checksum and install a text file as
// the rule-set; its own published digest would even verify.
func TestGeositeIgnoresTheChecksumAsset(t *testing.T) {
	saveUpdateBaseURL(t)
	dat := []byte("real-geosite-rules")
	sum := []byte(sha256Hex(dat) + "  geosite.dat\n")
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Loyalsoldier/v2ray-rules-dat/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"202611062204","assets":[` +
			`{"name":"geosite.dat.sha256sum","browser_download_url":"` + srv.URL + `/dl/sum","size":` + strconv.Itoa(len(sum)) + `,"digest":"sha256:` + sha256Hex(sum) + `"},` +
			`{"name":"geosite.dat","browser_download_url":"` + srv.URL + `/dl/dat","size":` + strconv.Itoa(len(dat)) + `,"digest":"sha256:` + sha256Hex(dat) + `"}]}`))
	})
	mux.HandleFunc("/dl/dat", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(dat) })
	mux.HandleFunc("/dl/sum", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(sum) })
	srv = httptest.NewServer(mux)
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	got, err := a.UpdateComponent("geosite")
	if err != nil {
		t.Fatalf("UpdateComponent(geosite): %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(updaterDataDir(), "geosite.dat"))
	if err != nil {
		t.Fatalf("installed rule-set unreadable: %v", err)
	}
	if string(raw) != string(dat) {
		t.Fatalf("installed %q, want the rule-set bytes (the checksum asset was picked)", raw)
	}
	if got.Latest != "202611062204" {
		t.Fatalf("Latest = %q, want the release tag", got.Latest)
	}
}

// TestCheckGUINotesWhenTheBuildIsAhead — the fork's newest published
// release is 1.0.0-beta.1 (measured 2026-11-07) while VERSION has since
// moved on, so a local build is ahead of everything published. The
// disabled Update button must say which side is newer instead of leaving
// the row looking like a plain "up to date". checkGUI reads a lookup that
// CheckUpdates already fetched, so the release is handed to it directly
// and the test needs no server.
func TestCheckGUINotesWhenTheBuildIsAhead(t *testing.T) {
	old := appVersion
	t.Cleanup(func() { appVersion = old })
	a := newUpdaterTestApp(t)
	cases := []struct {
		build, published, notes string
		avail                   bool
	}{
		{"1.0.0-beta.2", "v1.0.0-beta.1", "This build is newer than the published release", false},
		{"1.0.0-beta.1", "v1.0.0-beta.2", "", true},
		{"1.0.0-beta.1", "v1.0.0-beta.1", "", false},
	}
	for _, tc := range cases {
		appVersion = tc.build
		c := UpdatableComponent{ID: "gui", Name: "NekoGo GUI", Current: appVersion}
		a.checkGUI(&c, releaseLookup{repo: forkOwnerRepo, rel: &ghRelease{
			TagName: tc.published,
			Assets: []ghAsset{{
				Name:               "NekoGo-Portable-" + tc.published + ".zip",
				BrowserDownloadURL: "https://example.invalid/nekogo.zip",
			}},
		}})
		if c.UpdateAvail != tc.avail {
			t.Errorf("build %s vs published %s: UpdateAvail = %v, want %v", tc.build, tc.published, c.UpdateAvail, tc.avail)
		}
		if c.Notes != tc.notes {
			t.Errorf("build %s vs published %s: Notes = %q, want %q", tc.build, tc.published, c.Notes, tc.notes)
		}
		if c.LastError != "" {
			t.Errorf("build %s vs published %s: LastError = %q, want empty", tc.build, tc.published, c.LastError)
		}
	}
}
