package main

// The xray row must resolve against XTLS/Xray-core, the repo that
// publishes xray.exe, exactly like the singbox row follows SagerNet and
// the mihomo row follows MetaCubeX. It used to read the NekoGo app
// releases (forkOwnerRepo), which compared "Xray 25.9.11" against a
// NekoGo tag such as "1.0.0-beta.3" — and installed the xray.exe shipped
// inside the app's own release.

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

// xrayAssetName mirrors the archive XTLS publishes for this platform
// (measured in the v26.11.9 release: Xray-windows-32/-64/
// -arm64-v8a.zip).
func xrayAssetName() string {
	suffix := ""
	switch runtime.GOARCH {
	case "amd64":
		suffix = "-64"
	case "arm64":
		suffix = "-arm64-v8a"
	case "386":
		suffix = "-32"
	default:
		return ""
	}
	return "Xray-windows" + suffix + ".zip"
}

// fakeRelease is one GitHub repo's release as served by
// fakeXrayServer. digests publishes a sha256 per asset name; a name
// absent from it publishes none.
type fakeRelease struct {
	tag     string
	assets  map[string][]byte
	digests map[string]string
}

// fakeXrayServer answers BOTH repos the updater reads from one server
// (updateBaseURL holds a single API root):
//   - /repos/XTLS/Xray-core/releases/latest -> the xray release
//   - /repos/xenonama/NekoGo/releases -> the NekoGo app release, which
//     carries its own xray.exe
//
// So a row that resolves the wrong repo reports the wrong tag and
// downloads the wrong bytes. Assets are served under /dl/<name>.
func fakeXrayServer(t *testing.T, xray, fork fakeRelease) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	payloads := map[string][]byte{}
	for name, p := range xray.assets {
		payloads[name] = p
	}
	for name, p := range fork.assets {
		payloads[name] = p
	}
	assetJSON := func(rel fakeRelease) string {
		var parts []string
		for name, payload := range rel.assets {
			part := `{"name":` + strconv.Quote(name) +
				`,"browser_download_url":` + strconv.Quote(srv.URL+"/dl/"+name) +
				`,"size":` + strconv.Itoa(len(payload))
			if d := rel.digests[name]; d != "" {
				part += `,"digest":` + strconv.Quote(d)
			}
			parts = append(parts, part+`}`)
		}
		return strings.Join(parts, ",")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/XTLS/Xray-core/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":` + strconv.Quote(xray.tag) +
			`,"name":"xray","assets":[` + assetJSON(xray) + `]}`))
	})
	mux.HandleFunc("/repos/xenonama/NekoGo/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":` + strconv.Quote(fork.tag) +
			`,"name":"app","assets":[` + assetJSON(fork) + `]}]`))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/dl/")
		if p, ok := payloads[name]; ok {
			_, _ = w.Write(p)
			return
		}
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	return srv
}

// makeXrayZipBytes packs an archive shaped like the real Xray Windows
// zip (measured in v26.11.9): the binary plus wintun.dll and README.md
// beside it, so a picker that takes "the first file" is caught.
func makeXrayZipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"README.md", "wintun.dll", "xray.exe"} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		body := payload
		if name != "xray.exe" {
			body = []byte("not-the-binary-" + name)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// setXrayPath points both the settings override and the manager at p,
// the way a real save does: xrayInstallPath reads the setting,
// resolveXray reads the manager.
func setXrayPath(t *testing.T, a *App, p string) {
	t.Helper()
	s := a.store.GetSettings()
	s.XrayPath = p
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	a.xray.setXrayPath(p)
}

// installedXrayVersion seeds the per-path version cache so the row's
// Current line is a real Xray banner without shipping an executable:
// XrayVersion serves the cache while the resolved path is unchanged.
func installedXrayVersion(t *testing.T, a *App, path, line string) {
	t.Helper()
	a.xrayVer.mu.Lock()
	a.xrayVer.path = path
	a.xrayVer.version = line
	a.xrayVer.mu.Unlock()
}

// TestCheckXrayResolvesTheUpstreamRelease — the xray row follows
// XTLS/Xray-core. The local banner (25.9.11) is OLDER than the Xray
// release served here (26.11.9) and NEWER than the NekoGo app tag
// (1.0.0-beta.3), so the row can only report an update by reading the
// right repo. The nekobox row keeps following the app repo, which is
// the point of the shared lookups map.
func TestCheckXrayResolvesTheUpstreamRelease(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the xray row installs Windows zips only")
	}
	name := xrayAssetName()
	if name == "" {
		t.Skip("no Xray Windows asset for " + runtime.GOARCH)
	}
	xrayZip := makeXrayZipBytes(t, []byte("new-xray"))
	srv := fakeXrayServer(t,
		fakeRelease{tag: "v26.11.9", assets: map[string][]byte{
			name: xrayZip,
			// Every archive ships a .dgst sidecar whose name
			// contains the zip's: a substring match takes it.
			name + ".dgst": []byte("sha256-just-a-text-file"),
		}, digests: map[string]string{name: "sha256:" + sha256Hex(xrayZip)}},
		fakeRelease{tag: "1.0.0-beta.3", assets: map[string][]byte{
			"nekobox_core.exe": []byte("nk"),
			"xray.exe":         []byte("app-bundled-xray"),
		}})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL
	updateBaseURL["jsdelivr"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "xray.exe")
	if err := os.WriteFile(dest, []byte("old-xray"), 0o644); err != nil {
		t.Fatal(err)
	}
	setXrayPath(t, a, dest)
	installedXrayVersion(t, a, dest, "Xray 25.9.11 (v2fly.org) go1.25.0 windows amd64")

	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	var xr, nb *UpdatableComponent
	for i := range rows {
		switch rows[i].ID {
		case "xray":
			xr = &rows[i]
		case "nekobox":
			nb = &rows[i]
		}
	}
	if xr == nil {
		t.Fatal("missing xray row")
	}
	if nb == nil {
		t.Fatal("missing nekobox row")
	}
	if xr.Current != "Xray 25.9.11 (v2fly.org) go1.25.0 windows amd64" {
		t.Fatalf("Current = %q, want the installed banner", xr.Current)
	}
	if xr.Latest != "v26.11.9" {
		t.Fatalf("Latest = %q, want the XTLS tag; %q is the NekoGo app tag", xr.Latest, "1.0.0-beta.3")
	}
	if !xr.UpdateAvail {
		t.Fatalf("row = %+v, want UpdateAvail=true (25.9.11 is older than 26.11.9)", *xr)
	}
	if xr.LastError != "" {
		t.Fatalf("LastError = %q, want empty", xr.LastError)
	}
	// The fork-sourced rows must not drift with this change.
	if nb.Latest != "1.0.0-beta.3" || !nb.UpdateAvail || nb.LastError != "" {
		t.Fatalf("nekobox row = %+v, want Latest=1.0.0-beta.3 UpdateAvail=true", *nb)
	}
}

// TestXrayAssetSelection — the release names the platform, not the
// version, and every archive is shadowed by a "<name>.dgst" sidecar
// that contains its name (measured in v26.11.9). Only this arch's exact
// zip is installable, so the match is anchored and case-folded.
func TestXrayAssetSelection(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the xray row installs Windows archives only")
	}
	want := xrayAssetName()
	if want == "" {
		t.Skip("no Xray Windows asset for " + runtime.GOARCH)
	}
	names := []string{
		"Xray-linux-32.zip",
		"Xray-linux-64.zip",
		"Xray-linux-arm64-v8a.zip",
		"Xray-macos-64.zip",
		"Xray-windows-32.zip",
		"Xray-windows-32.zip.dgst",
		"Xray-windows-64.zip",
		"Xray-windows-64.zip.dgst",
		"Xray-windows-arm64-v8a.zip",
		"Xray-windows-arm64-v8a.zip.dgst",
	}
	var assets []ghAsset
	for _, n := range names {
		assets = append(assets, ghAsset{Name: n})
	}
	got := xrayAsset(assets)
	if got == nil || got.Name != want {
		t.Fatalf("xrayAsset = %+v, want %q", got, want)
	}
	// The same archive's .dgst sidecar must never win, and no other
	// platform's archive is a substitute for this one.
	traps := []ghAsset{{Name: want + ".dgst"}}
	for i := range assets {
		if assets[i].Name != want {
			traps = append(traps, assets[i])
		}
	}
	if got := xrayAsset(traps); got != nil {
		t.Fatalf("xrayAsset picked %q from a release without the archive", got.Name)
	}
	// The name is matched case-insensitively.
	if got := xrayAsset([]ghAsset{{Name: strings.ToUpper(want)}}); got == nil {
		t.Fatal("an uppercase asset name was not matched")
	}
}

// TestUpdateXrayRollbackKeepsPrevious — the archive unpacks, but a
// binary that fails the smoke probe must not survive the install: the
// previous xray.exe is restored and the error says so.
func TestUpdateXrayRollbackKeepsPrevious(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the xray row installs Windows zips only")
	}
	name := xrayAssetName()
	if name == "" {
		t.Skip("no Xray Windows asset for " + runtime.GOARCH)
	}
	// Real probe (no stub): junk bytes cannot execute, so verify fails.
	// No digest published: XTLS ships .dgst files, and the installer
	// must still run on the bytes it got.
	srv := fakeXrayServer(t,
		fakeRelease{tag: "v26.11.9", assets: map[string][]byte{
			name: makeXrayZipBytes(t, []byte("not-an-executable")),
		}},
		fakeRelease{tag: "1.0.0-beta.3"})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "xray.exe")
	before := []byte("working-xray")
	if err := os.WriteFile(dest, before, 0o644); err != nil {
		t.Fatal(err)
	}
	setXrayPath(t, a, dest)
	_, err := a.UpdateComponent("xray")
	if err == nil || !strings.Contains(err.Error(), "failed to run") {
		t.Fatalf("err = %v, want failed-to-run rollback", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("installed %q, want the previous binary restored", after)
	}
}

// TestXrayInstallPathHonoursTheDirectoryOverride — a directory
// override gets xray.exe inside it. resolveXray only answers for an
// override that already exists, so the destination cannot come from the
// manager: an install into a configured-but-unpopulated directory must
// still land there, not in bin\.
func TestXrayInstallPathHonoursTheDirectoryOverride(t *testing.T) {
	a := newUpdaterTestApp(t)
	dir := t.TempDir()
	setXrayPath(t, a, dir)
	got, err := a.xrayInstallPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, coreExeName("xray")); got != want {
		t.Fatalf("xrayInstallPath = %q, want %q", got, want)
	}
}

// TestUpdateXrayRefusesAnotherRowsBinary — rows install by name into
// the directory the override points at, so an xrayPath naming another
// row's binary would clobber it: refuse and say which row owns it.
func TestUpdateXrayRefusesAnotherRowsBinary(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the xray row installs Windows zips only")
	}
	name := xrayAssetName()
	if name == "" {
		t.Skip("no Xray Windows asset for " + runtime.GOARCH)
	}
	srv := fakeXrayServer(t,
		fakeRelease{tag: "v26.11.9", assets: map[string][]byte{
			name: makeXrayZipBytes(t, []byte("upstream-xray-engine-bytes")),
		}},
		fakeRelease{tag: "1.0.0-beta.3"})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), coreExeName("nekobox_core"))
	if err := os.WriteFile(dest, []byte("working-thrift-core"), 0o644); err != nil {
		t.Fatal(err)
	}
	setXrayPath(t, a, dest)
	_, err := a.UpdateComponent("xray")
	if err == nil || !strings.Contains(err.Error(), "belongs to the nekobox row") {
		t.Fatalf("err = %v, want the cross-row overwrite refusal", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "working-thrift-core" {
		t.Fatal("the nekobox core was overwritten by an xray update")
	}
}

// TestUpdateXrayFromZip — updating xray downloads the Xray Windows
// archive from XTLS/Xray-core and installs its xray.exe at the resolved
// path (the XrayPath override, updated in place). The app's own release
// also publishes an xray.exe here: installing those bytes is the bug.
func TestUpdateXrayFromZip(t *testing.T) {
	saveUpdateBaseURL(t)
	if runtime.GOOS != "windows" {
		t.Skip("the xray row installs Windows zips only")
	}
	stubExeVersion(t, "Xray 26.11.9 (v2fly.org) go1.25.0 windows amd64")
	payload := []byte("upstream-xray-engine-bytes")
	name := xrayAssetName()
	if name == "" {
		t.Skip("no Xray Windows asset for " + runtime.GOARCH)
	}
	xrayZip := makeXrayZipBytes(t, payload)
	srv := fakeXrayServer(t,
		fakeRelease{tag: "v26.11.9", assets: map[string][]byte{
			name:           xrayZip,
			name + ".dgst": []byte("sha256-just-a-text-file"),
		}, digests: map[string]string{name: "sha256:" + sha256Hex(xrayZip)}},
		fakeRelease{tag: "1.0.0-beta.3", assets: map[string][]byte{
			"xray.exe": []byte("app-bundled-xray"),
		}})
	defer srv.Close()
	updateBaseURL["github-api"] = srv.URL

	a := newUpdaterTestApp(t)
	dest := filepath.Join(t.TempDir(), "xray.exe")
	setXrayPath(t, a, dest)
	got, err := a.UpdateComponent("xray")
	if err != nil {
		t.Fatalf("UpdateComponent(xray): %v", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("installed xray unreadable: %v", err)
	}
	if string(raw) != string(payload) {
		t.Fatalf("installed %q, want the xray.exe from the Xray archive", raw)
	}
	if _, err := os.Stat(dest + ".bak"); !os.IsNotExist(err) {
		t.Fatal("stale .bak left behind after a good install")
	}
	if got.Latest != "v26.11.9" || got.UpdateAvail {
		t.Fatalf("row = %+v, want Latest=v26.11.9 UpdateAvail=false", *got)
	}
	// Same bytes again: a quiet no-op, not an error row.
	again, err := a.UpdateComponent("xray")
	if err != nil {
		t.Fatalf("re-installing identical bytes failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(again.Notes), "already up to date") {
		t.Fatalf("Notes = %q, want an up-to-date note", again.Notes)
	}
}
