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
	return NewApp(store, make(chan LogEntry, 64))
}

// TestListUpdatablesNoFiles — no DBs, no cores, no externals: 6 rows,
// empty Current, no panic, no network.
func TestListUpdatablesNoFiles(t *testing.T) {
	a := newUpdaterTestApp(t)
	rows, err := a.ListUpdatables()
	if err != nil {
		t.Fatalf("ListUpdatables: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6", len(rows))
	}
	want := map[string]bool{"geoip": true, "geosite": true, "singbox": true, "nekobox": true, "xray": true, "gui": true}
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
	for _, id := range []string{"singbox", "nekobox", "xray"} {
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

// TestUpdateComponentGeoIP — redirected download installs the file,
// Current tracks the new hash, geo detector reloads without panic.
func TestUpdateComponentGeoIP(t *testing.T) {
	saveUpdateBaseURL(t)
	payload := []byte("fake-mmdb-bytes-v1")
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
	payload := []byte("fake-mmdb-bytes-v2")
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
	payload := []byte("fake-mmdb-bytes-v1")
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
