package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// tor_release_test.go — the Tor row reads the Tor Project's download
// site, not GitHub.
//
// The binary it installs is the tor.exe inside a Tor Browser
// expert-bundle archive, published in a versioned directory beside a
// signed sha256sums file. Nothing in the GitHub path of this updater
// reaches that site, so before this the Tor row could name a binary the
// app had no way to fetch — "add the tor binary to the project" was a
// request NekoGo could not carry out. These tests pin what makes it
// work: the newest release found by number rather than by string order,
// the bytes checked against the published digest before anything is
// unpacked, and the archive's own layout landing where the resolver
// looks for it.

// makeTarGz packs entries the way the dist site ships them. The
// timestamps are pinned: gzip writes an MTIME header, and a test that
// recomputes the archive to predict its digest needs the same bytes
// twice.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var gzb bytes.Buffer
	zw := gzip.NewWriter(&gzb)
	zw.ModTime = time.Unix(0, 0).UTC()
	tw := tar.NewWriter(zw)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)),
			Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC()}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return gzb.Bytes()
}

// torBundleFiles is a release archive laid out the way the 16.0.1 bundle
// measured on the dist site: the engine and its transports under tor\,
// the browser's data and docs beside them.
func torBundleFiles(exe string) map[string]string {
	return map[string]string{
		"tor/tor.exe":                             exe,
		"tor/tor-gencert.exe":                     "gencert-bytes",
		"tor/pluggable_transports/lyrebird.exe":   "lyrebird-bytes",
		"tor/pluggable_transports/pt_config.json": "{}",
		"data/geoip":                              "geoip-blob",
		"data/torrc-defaults":                     "Log notice stdout\n",
		"docs/tor.txt":                            "license",
	}
}

// fakeDist serves a dist.torproject.org-shaped tree: the torbrowser\
// autoindex over [versions], and in each version directory the sums file
// plus the expert bundle. One archive's bytes are shared by every
// version, so a test pins which directory the lookup picks.
func fakeDist(t *testing.T, versions []string, files map[string]string) *httptest.Server {
	t.Helper()
	saveUpdateBaseURL(t)
	bundle := makeTarGz(t, files)
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/"+torDistDir+"/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/"+torDistDir+"/"), "/")
		if rest == "" {
			var b strings.Builder
			b.WriteString("<html><title>Index of /torbrowser</title><table>\n")
			for _, v := range versions {
				b.WriteString(`<tr><td><img src="/icons/cool.png" alt="   "/></td><td><a href="` + v + `/">` + v + `/</a></td><td class="lr">Thu, 01-May-2025 12:00:00 GMT</td></tr>` + "\n")
			}
			b.WriteString("</table></html>\n")
			w.Header().Set("Content-Type", "text/html;charset=UTF-8")
			_, _ = w.Write([]byte(b.String()))
			return
		}
		ver := strings.SplitN(rest, "/", 2)[0]
		if strings.HasSuffix(rest, torDistSumsFile) || strings.HasSuffix(rest, torDistSumsFallback) {
			sums := sha256Hex(bundle) + "  " + torBundleAssetName(ver) + "\n" +
				"0000000000000000000000000000000000000000000000000000000000000000  " +
				"tor-browser-windows-i686-unsigned-" + ver + ".tar.gz\n"
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(sums))
			return
		}
		if strings.HasSuffix(rest, ".tar.gz") {
			_, _ = w.Write(bundle)
			return
		}
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	updateBaseURL["tor-dist"] = srv.URL
	return srv
}

func checkedTorRow(t *testing.T, a *App) UpdatableComponent {
	t.Helper()
	rows, err := a.CheckUpdates()
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	for _, r := range rows {
		if r.ID == "tor" {
			return r
		}
	}
	t.Fatalf("no tor row after CheckUpdates")
	return UpdatableComponent{}
}

// TestTorLatestBundlePicksTheNewestReleaseNumbers: the source is a
// directory listing, so a plain string sort puts "9.0.14" above "16.0.1"
// and would offer a downgrade as an update. Alphas, the platform
// directories and the site's own docs folders are not releases NekoGo
// can install.
func TestTorLatestBundlePicksTheNewestReleaseNumbers(t *testing.T) {
	fakeDist(t, []string{"9.0.14", "16.0a3", "15.5.6", "16.0.1", "docs"}, torBundleFiles("tor-bytes"))
	b, err := torLatestBundle()
	if err != nil {
		t.Fatalf("torLatestBundle: %v", err)
	}
	if b.Version != "16.0.1" {
		t.Errorf("Version = %q, want 16.0.1 (numeric newest stable)", b.Version)
	}
	if b.Asset != "tor-expert-bundle-windows-x86_64-16.0.1.tar.gz" {
		t.Errorf("Asset = %q", b.Asset)
	}
	if !strings.HasPrefix(b.URL, updateBaseURL["tor-dist"]+"/torbrowser/16.0.1/") {
		t.Errorf("URL = %q, want it under the release directory", b.URL)
	}
	if b.SHA256 != sha256Hex(makeTarGz(t, torBundleFiles("tor-bytes"))) {
		t.Errorf("SHA256 = %q, want the digest the sums file publishes for the bundle", b.SHA256)
	}
}

// TestCheckTorReleaseOffersTheBundleItCanInstall: the row has to answer
// the two questions that were asked of it — is Tor there, and can NekoGo
// get it.
func TestCheckTorReleaseOffersTheBundleItCanInstall(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the dist site's expert bundle is looked up per Windows asset")
	}
	fakeDist(t, []string{"16.0.1"}, torBundleFiles("bundle"))

	a := newUpdaterTestApp(t)
	dir := t.TempDir()

	// Nothing installed: the row says so and offers the download.
	stubTorResolver(t, "", "")
	row := checkedTorRow(t, a)
	if row.Latest != "16.0.1" {
		t.Errorf("Latest = %q, want 16.0.1", row.Latest)
	}
	if !row.UpdateAvail {
		t.Errorf("UpdateAvail = false with no tor.exe: %+v", row)
	}
	if row.LastError != "" {
		t.Errorf("LastError = %q", row.LastError)
	}

	// Installed by hand (a Tor Browser copy, no install record): the
	// bundle's number and tor's own are different series, so the honest
	// answer is "cannot compare", not a nagging update offer every time
	// the panel refreshes.
	setTorPath(t, a, writeTorExe(t, dir, "flat"))
	stubTorResolver(t, filepath.Join(dir, "tor.exe"), "Tor version 0.4.9.16 (git-c51547ca15ea6659).")
	row = checkedTorRow(t, a)
	if row.Current != "Tor version 0.4.9.16 (git-c51547ca15ea6659)." {
		t.Errorf("Current = %q", row.Current)
	}
	if row.UpdateAvail {
		t.Errorf("UpdateAvail = true with no install record to compare against")
	}
	if !strings.Contains(row.Notes, "compare") {
		t.Errorf("Notes = %q, want it to say the versions are not comparable", row.Notes)
	}

	// Installed by NekoGo from an older bundle: that is an update.
	if err := os.WriteFile(filepath.Join(dir, torBundleMarker), []byte("15.5.6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	row = checkedTorRow(t, a)
	if !row.UpdateAvail || row.Latest != "16.0.1" {
		t.Errorf("row = %+v, want the 15.5.6 install offered 16.0.1", row)
	}
	// Parity with what was installed is not.
	if err := os.WriteFile(filepath.Join(dir, torBundleMarker), []byte("16.0.1"), 0o644); err != nil {
		t.Fatal(err)
	}
	row = checkedTorRow(t, a)
	if row.UpdateAvail {
		t.Errorf("UpdateAvail = true on parity: %+v", row)
	}
}

// TestUpdateTorReleaseInstallsTheBundleLayout: the archive is a Tor
// Browser tree, and only the engine and its transports belong in
// NekoGo's bin folder. The layout it writes has to be the one
// resolveTorExe and the bridge launcher look for, and the smoke verify
// has to ask the question tor.exe answers — "tor -v" is a malformed
// option that drops the binary into its daemon startup path, while
// --version prints a line and exits.
func TestUpdateTorReleaseInstallsTheBundleLayout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the tor row installs the Windows expert bundle")
	}
	fakeDist(t, []string{"16.0.1"}, torBundleFiles("new-tor-bytes"))

	var verifyArgs []string
	prevExe, prevQuery := torExeOverride, exeVersionQuery
	torExeOverride = ""
	exeVersionQuery = func(path string, args ...string) string {
		verifyArgs = append(verifyArgs, args...)
		return "Tor version 0.4.9.16 (git-c51547ca15ea6659)."
	}
	t.Cleanup(func() { torExeOverride, exeVersionQuery = prevExe, prevQuery })

	a := newUpdaterTestApp(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, torExeName)
	setTorPath(t, a, dest)

	row, err := a.UpdateComponent("tor")
	if err != nil {
		t.Fatalf("UpdateComponent(tor): %v", err)
	}
	if len(verifyArgs) == 0 || verifyArgs[0] != "--version" {
		t.Errorf("smoke verify asked %v, want --version (tor -v starts the daemon)", verifyArgs)
	}
	for name, want := range map[string]string{
		torExeName: "new-tor-bytes",
		filepath.Join("pluggable_transports", "lyrebird.exe"): "lyrebird-bytes",
		"tor-gencert.exe": "gencert-bytes",
	} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s was not installed: %v", name, err)
		}
		if string(raw) != want {
			t.Errorf("%s holds %q, want %q", name, raw, want)
		}
	}
	// The browser's payload stays out of the engine folder.
	for _, name := range []string{"geoip", "torrc-defaults", "tor.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s from the bundle's data\\docs was written beside tor.exe", name)
		}
	}
	// The install records which bundle it came from, which is the only
	// comparison the next check can honestly make.
	mark, err := os.ReadFile(filepath.Join(dir, torBundleMarker))
	if err != nil || strings.TrimSpace(string(mark)) != "16.0.1" {
		t.Errorf("install record = %q, %v; want 16.0.1", mark, err)
	}
	if row.ID != "tor" || row.Latest != "16.0.1" || row.UpdateAvail {
		t.Errorf("row = %+v, want the tor row at parity with 16.0.1", *row)
	}
	// And the row reports the engine it installed, not aether's.
	if got := a.TorVersion(); got != "Tor version 0.4.9.16 (git-c51547ca15ea6659)." {
		t.Errorf("TorVersion after install = %q", got)
	}
	// The transport that arrived with it is no longer reported missing.
	if strings.Contains(row.Notes, "lyrebird") {
		t.Errorf("Notes = %q after installing the transports", row.Notes)
	}
}

// TestUpdateTorReleaseRefusesDigestMismatch: the sums file is the only
// integrity claim this site makes, and it covers the ARCHIVE. A bundle
// that does not match must never reach the extractor, the engine or a
// transport — and must leave no install record behind.
func TestUpdateTorReleaseRefusesDigestMismatch(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the tor row installs the Windows expert bundle")
	}
	files := torBundleFiles("tampered")
	saveUpdateBaseURL(t)
	bundle := makeTarGz(t, files)
	mux := http.NewServeMux()
	mux.HandleFunc("/"+torDistDir+"/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/"+torDistDir+"/"), "/")
		switch {
		case rest == "":
			_, _ = w.Write([]byte(`<a href="16.0.1/">16.0.1/</a>`))
		case strings.HasSuffix(rest, torDistSumsFile):
			_, _ = w.Write([]byte(sha256Hex([]byte("the-real-archive")) + "  " + torBundleAssetName("16.0.1") + "\n"))
		case strings.HasSuffix(rest, ".tar.gz"):
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	updateBaseURL["tor-dist"] = srv.URL

	a := newUpdaterTestApp(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, torExeName)
	before := []byte("working-tor")
	if err := os.WriteFile(dest, before, 0o644); err != nil {
		t.Fatal(err)
	}
	setTorPath(t, a, dest)

	if _, err := a.UpdateComponent("tor"); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want a sha256 mismatch refusal", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil || string(after) != string(before) {
		t.Errorf("tor.exe = %q, %v; want the working bytes kept", after, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pluggable_transports", "lyrebird.exe")); !os.IsNotExist(err) {
		t.Error("an unverified archive wrote a transport binary")
	}
	if _, err := os.Stat(filepath.Join(dir, torBundleMarker)); !os.IsNotExist(err) {
		t.Error("an unverified archive wrote the install record")
	}
}
