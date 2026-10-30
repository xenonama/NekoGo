package main

// Update policy (v2rayN-style manager, in-app):
//
// NekoGo runs two sing-box binaries, and each has its own updater:
//   - sing-box.exe: the official SagerNet CLI, used when Settings →
//     Connection selects CoreEngine=upstream. It has no Thrift RPC, so
//     traffic stats/connections go through the Clash API.
//   - nekobox_core.exe: the NekoBox fork with the Thrift server, used
//     by CoreEngine=custom; it also carries the NekoGo-only protocols.
//
// The official binary comes straight from SagerNet/sing-box (stable
// releases). The NekoBox core still ships in the fork's own releases
// (xenonama/NekoGo) until a dedicated core repo exists; pickForkAsset
// only matches fork-published assets, so the two sources can never be
// mixed up. Upstream xray.exe lacks the NekoGo-compatible config
// format — installing it would break Connect entirely (see AI_ACT_LOG
// 2026-09-30 19:38:46 and 2026-10-01 13:45:00), so xray stays on the
// fork source.
//
// Covered components (see updatableIDs):
//   - geoip/geosite: plain data files, updated in place.
//   - singbox (official CLI), nekobox (Thrift core), xray: replaced in
//     place with .bak backup + smoke-verify + rollback. Refused while
//     a core is running.
//   - gui (NekoGo.exe): the installer is downloaded and opened
//     manually, never applied silently (a running exe cannot replace
//     itself, and there is no signature check yet).

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manual update manager (no auto-check, no GPG yet — see TODOs below).
// All network access uses a NekoGo User-Agent: updateHTTP (15s whole-request
// timeout) for the small API/HEAD calls, downloadHTTP for binaries. Tests
// redirect every source via updateBaseURL so the suite never touches the
// real GitHub API.

// updateBaseURL redirects updater source roots in tests (httptest
// servers). Production uses the defaults. Keys:
//   - "github-api": GitHub REST API root (default https://api.github.com)
//   - "jsdelivr":    jsDelivr CDN root (default https://cdn.jsdelivr.net)
//
// Never exposed to the frontend.
var updateBaseURL = map[string]string{}

func updateBase(key, def string) string {
	if v, ok := updateBaseURL[key]; ok && v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return def
}

// updateHTTP bounds the small API/HEAD calls. Its Timeout covers the WHOLE
// request including the body, so it must never be used for a binary
// download: 15s aborts a 30-80 MB core (or the ~70 MB setup) on any link
// slower than a few MB/s, which made every core update fail with
// "context deadline exceeded" and read like a dead source.
var updateHTTP = &http.Client{Timeout: 15 * time.Second}

// downloadHTTP serves binaries: the connection phases are bounded, the
// body is allowed to stream.
var downloadHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// errAlreadyUpToDate marks a download whose bytes match the installed
// file. It is a successful no-op, not a failure: the rows used to log it
// as an ERROR, so "Update" on a current component looked broken.
var errAlreadyUpToDate = errors.New("already up to date")

// maxDownloadBytes caps a single download (cores are tens of MB).
const maxDownloadBytes = 512 << 20

// UpdatableComponent is one row of the Updates panel.
type UpdatableComponent struct {
	ID          string `json:"id"` // "geoip" | "geosite" | "singbox" | "nekobox" | "xray" | "gui"
	Name        string `json:"name"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	UpdateAvail bool   `json:"updateAvail"`
	Notes       string `json:"notes,omitempty"`
	LastCheck   string `json:"lastCheck,omitempty"` // RFC3339
	LastError   string `json:"lastError,omitempty"`
	// InstallerPath is set by updateGUI after a successful download:
	// the UI offers an "Open installer" button the user must click.
	// The backend never opens it on its own (BUG-010).
	InstallerPath string `json:"installerPath,omitempty"`
}

// updatableIDs are the fixed rows. "singbox" is the official CLI
// (upstream engine); "nekobox" is the Thrift core (custom engine).
var updatableIDs = []string{"geoip", "geosite", "singbox", "nekobox", "xray", "gui"}

// --- local state (no network) ---

// updaterDataDir holds updater-managed data files (geoip/geosite):
// <exeDir> in production, the repo tree when running from it.
func updaterDataDir() string {
	exe, _ := os.Executable()
	candidates := []string{}
	if exe != "" {
		candidates = append(candidates, filepath.Dir(exe))
	}
	candidates = append(candidates,
		filepath.Join("gui", "assets"), // repo-root cwd
		"assets",                       // gui/ cwd (only if it exists)
	)
	for _, d := range candidates {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	// Nothing usable: fall back to the exe dir (may not exist in odd
	// setups; installers create it with MkdirAll).
	if exe != "" {
		return filepath.Dir(exe)
	}
	return ""
}

// coreExeDir is where replacement core exes go: the PattN-style bin\
// folder next to the GUI exe (created on install). An existing core
// installed elsewhere is updated in place instead (updateCoreExe uses
// the resolved path).
func coreExeDir() string {
	return engineInstallDir()
}

func coreExeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// exeExists reports whether path names an existing regular file.
func exeExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// fileSHA12 returns the first 12 hex chars of a file's SHA-256.
func fileSHA12(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// dataFileCurrent versions a data file by content hash, mtime fallback.
func dataFileCurrent(path string) string {
	if sha, err := fileSHA12(path); err == nil {
		return "sha:" + sha
	}
	if st, err := os.Stat(path); err == nil {
		return "mtime:" + st.ModTime().Format("2006-01-02 15:04")
	}
	return ""
}

func (a *App) updateLog(id, level, msg string) {
	a.emitLog("update:"+id, level, msg)
}

// localUpdatables builds the rows from local state only.
func (a *App) localUpdatables() []UpdatableComponent {
	dataDir := updaterDataDir()
	geoPath := filepath.Join(dataDir, "GeoLite2-Country.mmdb")
	// Report the copy the detector actually opened: the portable layout
	// ships two, and the row must not hash the wrong file (or claim "Not
	// installed") while country detection works from the other one.
	if a.geo != nil {
		if p := a.geo.DBPath(); p != "" {
			geoPath = p
		}
	}
	geoSitePath := filepath.Join(dataDir, "geosite.dat")
	sbCurrent := a.UpstreamSingBoxVersion()
	sbNote := ""
	if sbCurrent == "" {
		sbNote = "Not installed — only the Upstream core engine needs it"
	}
	nbCurrent, nbNote := a.coreDisplayVersion("nekobox", a.CoreVersion())
	xrCurrent, xrNote := a.coreDisplayVersion("xray", a.XrayVersion())
	out := []UpdatableComponent{
		{ID: "geoip", Name: "GeoIP Database", Current: dataFileCurrent(geoPath)},
		{ID: "geosite", Name: "GeoSite Rule-set", Current: dataFileCurrent(geoSitePath)},
		{ID: "singbox", Name: "sing-box Core (official)", Current: sbCurrent, Notes: sbNote},
		{ID: "nekobox", Name: "NekoBox Core (nekobox_core)", Current: nbCurrent, Notes: nbNote},
		{ID: "xray", Name: "Xray Core", Current: xrCurrent, Notes: xrNote},
		{ID: "gui", Name: "NekoGo GUI", Current: appVersion},
	}
	if _, err := os.Stat(geoPath); err != nil {
		out[0].Notes = "Not installed"
	}
	if _, err := os.Stat(geoSitePath); err != nil {
		out[1].Notes = "Not installed"
	}
	return out
}

// coreDisplayVersion turns a raw "<exe> version" line into the row's
// Current cell. Real versions pass through; a missing binary stays
// empty ("Not installed"); a binary that reports "unknown" (built
// without the -X version stamp — build.bat stamps it since
// 5.11.28.3, older builds don't) falls back to a content hash so the
// row still identifies the file and the check still offers fork
// updates instead of comparing a release tag against "unknown".
func (a *App) coreDisplayVersion(id, line string) (current, note string) {
	if line != "" && !strings.Contains(strings.ToLower(line), "unknown") {
		return line, ""
	}
	if p := a.currentCorePath(id); p != "" {
		return dataFileCurrent(p), "Installed, but its version stamp is missing — updates compare by file content"
	}
	return "", "Not installed"
}

// exeVersionQuery runs "<exe> args..." and returns the first stdout
// line ("" on any error). A variable — not a direct runVersionQuery
// call — so tests can stub the smoke-verify step without shipping a
// real executable.
var exeVersionQuery = runVersionQuery

// ListUpdatables returns the current local state of every updatable
// component (no network).
func (a *App) ListUpdatables() ([]UpdatableComponent, error) {
	return a.localUpdatables(), nil
}

// --- GitHub release resolution ---

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Name    string    `json:"name"`
	Assets  []ghAsset `json:"assets"`
}

func updateGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	return updateHTTP.Do(req)
}

// downloadGet fetches a binary asset through downloadHTTP (no
// whole-request deadline — see updateHTTP).
func downloadGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersion)
	return downloadHTTP.Do(req)
}

func githubLatest(ownerRepo string) (*ghRelease, error) {
	url := updateBase("github-api", "https://api.github.com") + "/repos/" + ownerRepo + "/releases/latest"
	resp, err := updateGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github api %s: %s", ownerRepo, resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("github api %s: empty tag", ownerRepo)
	}
	return &rel, nil
}

// Update source table (v2rayN-style: each binary comes from the repo
// that publishes it).
//
//	| Component | Source repo           | Asset pattern                        | Install path |
//	| singbox   | SagerNet/sing-box     | sing-box-<ver>-windows-<arch>.zip    | resolveUpstreamCore path, else exe dir |
//	| nekobox   | xenonama/NekoGo       | nekobox_core.exe (or .zip holding it) | resolved core path, else exe dir |
//	| xray      | xenonama/NekoGo       | xray.exe (or .zip holding it)         | resolved xray path, else exe dir |
//	| gui       | xenonama/NekoGo       | NekoGo-Setup-*.exe / NekoGo-Portable-*.zip | %APPDATA%/NekoGo/updates/ (manual open via Open installer button; never auto-opened) |
//	| geoip     | Loyalsoldier/geoip    | Country.mmdb (@release)              | gui/assets/GeoLite2-Country.mmdb |
//	| geosite   | Loyalsoldier/v2ray-rules-dat | geosite.dat                     | gui/assets/geosite.dat |
//
// The NekoBox core row points at the app repo today; a dedicated core
// repo is planned (nekoCoreOwnerRepo), and only that constant changes.
const forkOwnerRepo = "xenonama/NekoGo"

// officialSingBoxOwnerRepo is the upstream sing-box project. The
// official CLI is the only source for the singbox row; it is a drop-in
// for CoreEngine=upstream and needs none of the fork's patches.
const officialSingBoxOwnerRepo = "SagerNet/sing-box"

// nekoCoreOwnerRepo hosts the NekoBox Thrift core (nekobox_core.exe).
// A dedicated core repo is planned; until it exists the fork's app
// releases carry the binaries, so this points at the same repo.
const nekoCoreOwnerRepo = forkOwnerRepo

// pickForkAsset selects a fork-published core binary asset: either the
// raw "<exeBase>.exe" (or bare "<exeBase>" on non-Windows), or a
// *.zip containing it (matched by name).
func pickForkAsset(assets []ghAsset, exeBase string) *ghAsset {
	want := strings.ToLower(exeBase + ".exe")
	for i := range assets {
		if strings.ToLower(assets[i].Name) == want {
			return &assets[i]
		}
	}
	// Bare name (linux/macOS assets ship without .exe).
	wantBare := strings.ToLower(exeBase)
	for i := range assets {
		if strings.ToLower(assets[i].Name) == wantBare {
			return &assets[i]
		}
	}
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		if strings.HasSuffix(n, ".zip") && strings.Contains(n, strings.ToLower(exeBase)) {
			return &assets[i]
		}
	}
	return nil
}

// isNotFoundErr reports a GitHub 404 (no release published yet) as
// opposed to other network/API failures.
func isNotFoundErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "404")
}

// githubFirstRelease returns the newest release INCLUDING prereleases
// (GET /releases, first entry). Unlike githubLatest (/releases/latest),
// which GitHub restricts to full releases, this keeps NekoGo
// pre-releases visible to the GUI updater. An empty list means the
// fork has not published anything yet.
func githubFirstRelease(ownerRepo string) (*ghRelease, error) {
	url := updateBase("github-api", "https://api.github.com") + "/repos/" + ownerRepo + "/releases?per_page=1"
	resp, err := updateGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github api %s: %s", ownerRepo, resp.Status)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err
	}
	if len(rels) == 0 || rels[0].TagName == "" {
		return nil, fmt.Errorf("github api %s: no releases published yet", ownerRepo)
	}
	return &rels[0], nil
}

func pickNamed(assets []ghAsset, substrs ...string) *ghAsset {
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		for _, s := range substrs {
			if strings.Contains(n, strings.ToLower(s)) {
				return &assets[i]
			}
		}
	}
	return nil
}

// pickGUIAsset selects the GUI installer asset with anchored suffixes
// (BUG-041): exact "NekoGo-Setup-*.exe" first, then
// "NekoGo-Portable-*.zip", then any *nekogo* name still anchored to
// .exe/.zip. Bare generic substrings ("setup"/"nekogo") never match
// non-installer files (e.g. NekoGo-Setup-notes.txt), which the old
// pickNamed call matched and then downloaded/auto-opened.
func pickGUIAsset(assets []ghAsset) *ghAsset {
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		if strings.HasPrefix(n, "nekogo-setup-") && strings.HasSuffix(n, ".exe") {
			return &assets[i]
		}
	}
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		if strings.HasPrefix(n, "nekogo-portable-") && strings.HasSuffix(n, ".zip") {
			return &assets[i]
		}
	}
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		if strings.Contains(n, "nekogo") && (strings.HasSuffix(n, ".exe") || strings.HasSuffix(n, ".zip")) {
			return &assets[i]
		}
	}
	return nil
}

// normTag strips a leading "v" for comparison ("v1.14.1" vs "1.14.1").
func normTag(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "v")
}

// updateNeeded reports whether [latest] is newer than [current].
//
// The previous test was strings.Contains(current, latest): a build stamped
// "1.0.0-beta.1" CONTAINS "1.0.0", so the day a stable v1.0.0 was published
// the row silently claimed parity and the release was never offered.
func updateNeeded(current, latest string) bool {
	c, l := strings.TrimSpace(normTag(current)), strings.TrimSpace(normTag(latest))
	if l == "" {
		return false
	}
	if c == "" {
		return true
	}
	cv, cpre, cok := splitVersion(c)
	if !cok {
		// Core rows carry the raw "<exe> version 1.2.3" banner, which is
		// not a bare version; without this the fallback below always said
		// "update available" and every Update was a no-op re-download.
		if tok := versionToken(c); tok != "" {
			cv, cpre, cok = splitVersion(tok)
		}
	}
	lv, lpre, lok := splitVersion(l)
	if !lok {
		if tok := versionToken(l); tok != "" {
			lv, lpre, lok = splitVersion(tok)
		}
	}
	if !cok || !lok {
		// Not an orderable version ("unknown" from an unstamped core, a
		// date, a hash): offer the update unless the strings match.
		return !strings.EqualFold(c, l)
	}
	if n := compareDotted(cv, lv); n != 0 {
		return n < 0
	}
	// Same numeric version: a prerelease sorts before the plain release
	// (1.0.0-beta.1 < 1.0.0), two prereleases compare identifier-wise.
	return comparePrerelease(cpre, lpre) < 0
}

// versionLineRe matches the first dotted version token, prerelease
// suffix included ("sing-box version 1.13.19", "Xray 25.9.11 (…)").
var versionLineRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)*(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?`)

// versionToken extracts that token from a raw version line ("" when the
// text carries none).
func versionToken(s string) string {
	return versionLineRe.FindString(s)
}

// splitVersion separates the dotted numeric part from a "-prerelease"
// suffix, dropping "+build" metadata. ok is false when any numeric part is
// not a plain non-negative integer.
func splitVersion(v string) (nums []int, pre string, ok bool) {
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	main := v
	if i := strings.Index(v, "-"); i >= 0 {
		main, pre = v[:i], v[i+1:]
	}
	for _, part := range strings.Split(main, ".") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return nil, "", false
		}
		nums = append(nums, n)
	}
	return nums, pre, true
}

// compareDotted orders two dotted numeric versions, treating missing
// trailing parts as zero (1.2 == 1.2.0).
func compareDotted(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePrerelease orders two prerelease suffixes (semver §11): no
// suffix is a RELEASE and outranks any suffix, fewer identifiers rank
// lower when all shared ones are equal.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if n := comparePreIdent(as[i], bs[i]); n != 0 {
			return n
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

// comparePreIdent orders one dot-separated identifier: numeric identifiers
// compare numerically and rank below alphanumeric ones.
func comparePreIdent(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// --- per-component checks (each fills Latest/UpdateAvail/Notes) ---

func (a *App) checkGeoIP(c *UpdatableComponent) {
	url := updateBase("jsdelivr", "https://cdn.jsdelivr.net") + "/gh/Loyalsoldier/geoip@release/Country.mmdb"
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		c.LastError = err.Error()
		return
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersion)
	resp, err := updateHTTP.Do(req)
	if err != nil {
		c.LastError = err.Error()
		return
	}
	resp.Body.Close()
	etag := normRemoteRev(resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
	c.Latest = etag
	a.updateLog(c.ID, "info", "[update:geoip] source=Loyalsoldier/geoip latest="+etag)
	if c.Current == "" {
		c.UpdateAvail = true
		c.Notes = "Not installed"
		return
	}
	if etag == "" {
		c.Notes = "Remote version unknown"
		return
	}
	// The marker is the revision the updater installed. Local Current is a
	// content hash, so comparing it with the remote ETag can never match —
	// that mismatch made this row claim an update forever.
	if m, err := os.ReadFile(geoipMarker()); err == nil && strings.TrimSpace(string(m)) == etag {
		c.Notes = "Up to date"
		return
	}
	c.UpdateAvail = true
}

func (a *App) checkGeosite(c *UpdatableComponent) {
	rel, err := githubLatest("Loyalsoldier/v2ray-rules-dat")
	if err != nil {
		c.LastError = err.Error()
		return
	}
	asset := pickNamed(rel.Assets, "geosite.dat")
	if asset == nil {
		c.LastError = "geosite.dat not found in latest release"
		return
	}
	c.Latest = rel.TagName
	a.updateLog(c.ID, "info", "[update:geosite] source=Loyalsoldier/v2ray-rules-dat latest="+rel.TagName)
	if c.Current == "" {
		c.UpdateAvail = true
		c.Notes = "Not installed"
		return
	}
	marker := geositeMarker()
	if tag, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(tag)) == rel.TagName {
		return // installed tag matches
	}
	c.UpdateAvail = true
	if _, err := os.Stat(marker); err != nil {
		c.Notes = "Unknown installed version"
	}
}

func geositeMarker() string {
	return filepath.Join(updaterDataDir(), "geosite.version")
}

// checkCore resolves a singbox/xray row against a fork release list
// (prereleases included): fork-published patched cores are the only
// installable source — upstream binaries stay rejected by
// construction (pickForkAsset never looks at upstream repos).
func (a *App) checkCore(c *UpdatableComponent, repo, exeBase string) {
	rel, err := githubFirstRelease(repo)
	if err != nil {
		if isNotFoundErr(err) || strings.Contains(err.Error(), "no releases") {
			c.UpdateAvail = false
			c.Notes = "No core release published yet — manual update only (run build.bat)."
			a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=(none)", c.ID, repo))
			return
		}
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=%s", c.ID, repo, rel.TagName))
	asset := pickForkAsset(rel.Assets, exeBase)
	c.Latest = rel.TagName
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = fmt.Sprintf("%s is not published in %s — manual update only (run build.bat).", coreExeName(exeBase), rel.TagName)
		return
	}
	if updateNeeded(c.Current, rel.TagName) {
		c.UpdateAvail = true
	}
}

// checkOfficialSingBox resolves the singbox row against SagerNet's
// stable releases. The official CLI is a separate binary from
// nekobox_core.exe; only the Windows zip is unpacked in-app, so other
// platforms stay on the manual note.
func (a *App) checkOfficialSingBox(c *UpdatableComponent) {
	rel, err := githubLatest(officialSingBoxOwnerRepo)
	if err != nil {
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=%s", c.ID, officialSingBoxOwnerRepo, rel.TagName))
	asset := officialSingBoxAsset(rel.Assets)
	c.Latest = rel.TagName
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = fmt.Sprintf("No automatic install for %s/%s — download sing-box manually.", runtime.GOOS, runtime.GOARCH)
		return
	}
	if updateNeeded(c.Current, rel.TagName) {
		c.UpdateAvail = true
	}
}

// officialSingBoxAsset picks the archive for this OS/arch from a
// SagerNet release. Windows ships sing-box-<ver>-windows-<arch>.zip;
// legacy-windows-7 builds are skipped on purpose. Other OSes ship
// .tar.gz archives, which the in-place installer does not unpack.
func officialSingBoxAsset(assets []ghAsset) *ghAsset {
	if runtime.GOOS != "windows" {
		return nil
	}
	suffix := "-windows-" + runtime.GOARCH + ".zip"
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		if strings.HasPrefix(n, "sing-box-") && strings.HasSuffix(n, suffix) && !strings.Contains(n, "legacy-windows-7") {
			return &assets[i]
		}
	}
	return nil
}

func (a *App) checkGUI(c *UpdatableComponent) {
	// AUDIT (hotfix Task 2): queries xenonama/NekoGo/releases (list,
	// newest first, prereleases included) — NOT /releases/latest, so
	// pre-releases are visible.
	rel, err := githubFirstRelease(forkOwnerRepo)
	if err != nil {
		if isNotFoundErr(err) || strings.Contains(err.Error(), "no releases published yet") {
			c.UpdateAvail = false
			c.Notes = "No NekoGo release published yet."
			a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=(none)", c.ID, forkOwnerRepo))
			return
		}
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=%s", c.ID, forkOwnerRepo, rel.TagName))
	c.Latest = rel.TagName
	asset := pickGUIAsset(rel.Assets)
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = "Manual update only"
		return
	}
	if updateNeeded(appVersion, rel.TagName) {
		c.UpdateAvail = true
	}
}

// CheckUpdates queries every component (own goroutine each) and returns
// the merged result. It never downloads. Partial failures land in
// LastError; the call itself only fails if nothing could be checked... in
// practice it always returns the rows with nil error so the UI can
// render per-row state.
func (a *App) CheckUpdates() ([]UpdatableComponent, error) {
	rows := a.localUpdatables()
	now := time.Now().Format(time.RFC3339)
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(c *UpdatableComponent) {
			defer wg.Done()
			switch c.ID {
			case "geoip":
				a.checkGeoIP(c)
			case "geosite":
				a.checkGeosite(c)
			case "singbox":
				a.checkOfficialSingBox(c)
			case "nekobox":
				a.checkCore(c, nekoCoreOwnerRepo, "nekobox_core")
			case "xray":
				a.checkCore(c, forkOwnerRepo, "xray")
			case "gui":
				a.checkGUI(c)
			}
			c.LastCheck = now
		}(&rows[i])
	}
	wg.Wait()
	return rows, nil
}

// --- download + install ---

// downloadFile fetches url to memory (capped), logging progress lines.
func (a *App) downloadFile(id, url string) ([]byte, error) {
	a.updateLog(id, "info", "[update:"+id+"] downloading from "+url)
	resp, err := downloadGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("download exceeds %d bytes", maxDownloadBytes)
	}
	a.updateLog(id, "info", fmt.Sprintf("[update:%s] downloaded %d bytes", id, len(data)))
	return data, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileSHA256Hex(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

// installFile atomically replaces (or creates) path with data, ensuring
// the parent directory exists. Identical content returns
// errAlreadyUpToDate, which callers treat as a successful no-op.
func (a *App) installFile(id, path string, data []byte) error {
	if old, err := fileSHA256Hex(path); err == nil && old == sha256Hex(data) {
		return errAlreadyUpToDate
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	// TODO: GPG/Sigstore verification of release artifacts (Part 3
	// ships hash-compare only: identical content is rejected above,
	// but authenticity beyond HTTPS is not established).
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	a.updateLog(id, "info", "[update:"+id+"] installed to "+path)
	return nil
}

// extractExeFromZip returns the bytes of the "<exeName>" entry (matched
// by base name) inside a zip archive.
func extractExeFromZip(zipData []byte, exeName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(exeName)
	for _, f := range zr.File {
		if strings.ToLower(filepath.Base(f.Name)) != want {
			continue
		}
		// Checked before reading (a zip bomb would otherwise be spooled
		// into memory) and again after (a forged header can lie).
		if f.UncompressedSize64 > maxDownloadBytes {
			return nil, fmt.Errorf("%s in archive is too large (%d bytes)", exeName, f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxDownloadBytes+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > maxDownloadBytes {
			return nil, fmt.Errorf("%s in archive exceeds the %d-byte limit", exeName, maxDownloadBytes)
		}
		return data, nil
	}
	return nil, fmt.Errorf("%s not found in archive", exeName)
}

// coreBusy reports whether any core is currently running (replacing an
// exe out from under it is unsafe on Windows).
func (a *App) coreBusy() bool {
	_, _, running := a.cores.Status()
	return running
}

// geoipDest decides where a GeoIP download lands. The shipped database
// lives in bin\ next to the engines and the detector reads that copy;
// writing next to the exe (the old target) created a second file and
// left the shipped one stale. Only the shipped copies are rewritten —
// a Settings → GeoIP override and a dev-tree assets\ database are build
// inputs and are never written behind their owner's back.
func (a *App) geoipDest(name string) string {
	fallback := filepath.Join(updaterDataDir(), name)
	if a.geo == nil || a.store == nil {
		return fallback
	}
	if strings.TrimSpace(a.store.GetSettings().GeoIPDBPath) != "" {
		return fallback
	}
	live := a.geo.DBPath()
	if live == "" || !strings.EqualFold(filepath.Base(live), name) {
		return fallback
	}
	base := updaterDataDir()
	if samePath(live, filepath.Join(base, binDirName, name)) ||
		samePath(live, filepath.Join(base, name)) {
		return live
	}
	return fallback
}

// updateDataFile handles the geoip/geosite plain-file components.
func (a *App) updateDataFile(id, fileURL, destName string) (*UpdatableComponent, error) {
	c := UpdatableComponent{ID: id}
	for _, base := range a.localUpdatables() {
		if base.ID == id {
			c = base
			break
		}
	}
	data, err := a.downloadFile(id, fileURL)
	if err != nil {
		a.updateLog(id, "error", "[update:"+id+"] ERROR: "+err.Error())
		return nil, err
	}
	dest := filepath.Join(updaterDataDir(), destName)
	if id == "geoip" {
		// Overwrite the database the detector actually reads (bin\ in the
		// shipped layouts) instead of creating a second copy next to the
		// exe that then wins by mtime and leaves bin\ stale.
		dest = a.geoipDest(destName)
	}
	installed := true
	if err := a.installFile(id, dest, data); err != nil {
		if !errors.Is(err, errAlreadyUpToDate) {
			a.updateLog(id, "error", "[update:"+id+"] ERROR: "+err.Error())
			return nil, err
		}
		// The published bytes are already on disk: a quiet no-op, not the
		// ERROR row it used to produce.
		installed = false
		a.updateLog(id, "info", "[update:"+id+"] already up to date (identical content)")
	}
	if id == "geoip" {
		// Record the revision now on disk (even for a no-op) so checkGeoIP
		// can tell "up to date" from "changed".
		a.writeGeoIPMarker(fileURL)
		if installed && a.geo != nil {
			_ = a.geo.Reload()
		}
	}
	c.Current = "sha:" + sha256Hex(data)[:12]
	c.UpdateAvail = false
	c.Notes = ""
	if !installed {
		c.Notes = "Already up to date"
	}
	if id == "geoip" && a.geo != nil {
		// The download always lands in the updater's data dir, but a
		// Settings → GeoIP override makes the detector read a different file,
		// so Reload keeps the old DB and the row would claim an update that
		// country detection never sees. Say so instead of staying silent —
		// the override is a user-chosen path, so it is never written to.
		if live := a.geo.DBPath(); live != "" && !samePath(live, dest) {
			c.Notes = fmt.Sprintf("Installed to %s, but Settings → GeoIP overrides the database with %s — clear that override to use this copy", dest, live)
		}
	}
	return &c, nil
}

// geoipMarker records the published revision (the source ETag) of the
// GeoLite2 copy the updater installed. It lives in the updater's data
// dir; the database itself is bin\GeoLite2-Country.mmdb in the shipped
// layouts.
func geoipMarker() string {
	return filepath.Join(updaterDataDir(), "geoip.version")
}

// writeGeoIPMarker stores the current revision marker of [fileURL].
// Best-effort: a missing marker only makes the row offer the update again.
func (a *App) writeGeoIPMarker(fileURL string) {
	etag := remoteETag(fileURL)
	if etag == "" {
		return
	}
	if err := os.WriteFile(geoipMarker(), []byte(etag), 0o644); err != nil {
		a.updateLog("geoip", "warn", "[update:geoip] cannot record the installed revision: "+err.Error())
	}
}

// remoteETag is the source's revision marker for [url] ("" when unknown).
func remoteETag(url string) string {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersion)
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return normRemoteRev(resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
}

// normRemoteRev normalizes an ETag (weak prefix and quotes stripped) and
// falls back to Last-Modified, so a stored marker and a fresh check always
// compare the same kind of value.
func normRemoteRev(etag, lastModified string) string {
	e := strings.Trim(strings.TrimSpace(etag), `"`)
	e = strings.TrimPrefix(e, "W/")
	e = strings.Trim(e, `"`)
	if e != "" {
		return e
	}
	return strings.TrimSpace(lastModified)
}

// samePath reports whether two spellings name one file. Abs+Clean so a
// relative resolver candidate ("assets\GeoLite2-Country.mmdb") matches the
// absolute install target, folding case because Windows paths are
// case-insensitive. Empty paths never match.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err := filepath.Abs(a)
	if err != nil {
		aa = filepath.Clean(a)
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		bb = filepath.Clean(b)
	}
	return strings.EqualFold(aa, bb)
}

// forkCoreAsset resolves the newest fork release (prereleases
// included) and selects the asset for exeBase. Shared by the core
// install paths so check and install can never disagree on
// the source.
func (a *App) forkCoreAsset(id, repo, exeBase string) (*ghRelease, *ghAsset, error) {
	rel, err := githubFirstRelease(repo)
	if err != nil {
		return nil, nil, err
	}
	asset := pickForkAsset(rel.Assets, exeBase)
	if asset == nil {
		return nil, nil, fmt.Errorf("%s is not published in %s — manual update only (run build.bat)", coreExeName(exeBase), rel.TagName)
	}
	a.updateLog(id, "info", fmt.Sprintf("[update:%s] installing %s from %s", id, asset.Name, rel.TagName))
	return rel, asset, nil
}

// forkAssetBytes downloads a fork asset, unpacking the exe when the
// asset is a zip archive.
func (a *App) forkAssetBytes(id string, asset *ghAsset, exeBase string) ([]byte, error) {
	data, err := a.downloadFile(id, asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog(id, "error", "[update:"+id+"] ERROR: "+err.Error())
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(asset.Name), ".zip") {
		data, err = extractExeFromZip(data, coreExeName(exeBase))
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// installExeWithBackup replaces dest with data: the previous binary
// (when any) is kept as dest+".bak", then the new binary must answer
// the smoke-verify probe (verifyArgs) or the backup is restored and
// the install fails. Refuses when dest has no parent directory.
func (a *App) installExeWithBackup(id, dest string, data []byte, verifyArgs ...string) error {
	bak := dest + ".bak"
	hadPrev := exeExists(dest)
	if hadPrev {
		old, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		if err := os.WriteFile(bak, old, 0o755); err != nil {
			return err
		}
	}
	if err := a.installFile(id, dest, data); err != nil {
		os.Remove(bak)
		return err
	}
	_ = os.Chmod(dest, 0o755)
	if v := exeVersionQuery(dest, verifyArgs...); v == "" {
		if hadPrev {
			if err := os.Rename(bak, dest); err != nil {
				return fmt.Errorf("downloaded binary failed to run and rollback failed (%v) — reinstall manually", err)
			}
			return fmt.Errorf("downloaded binary failed to run; kept the previous binary")
		}
		os.Remove(dest)
		return fmt.Errorf("downloaded binary failed to run; nothing was installed")
	}
	os.Remove(bak)
	return nil
}

// updateCoreExe installs a nekobox/xray row from a fork release's
// assets. Refused while a core is running (Windows cannot replace a
// mapped exe, and the version caches must stay trustworthy).
func (a *App) updateCoreExe(id, repo, exeBase string, verifyArgs ...string) (*UpdatableComponent, error) {
	if a.coreBusy() {
		return nil, fmt.Errorf("disconnect before updating")
	}
	rel, asset, err := a.forkCoreAsset(id, repo, exeBase)
	if err != nil {
		return nil, err
	}
	data, err := a.forkAssetBytes(id, asset, exeBase)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(coreExeDir(), coreExeName(exeBase))
	if cur := a.currentCorePath(id); cur != "" {
		dest = cur
	}
	if owner := coreImageOwner(dest); owner != "" && owner != id {
		return nil, fmt.Errorf("%s update would overwrite %s, which belongs to the %s row; fix corePath in settings", id, filepath.Base(dest), owner)
	}
	if err := a.installExeWithBackup(id, dest, data, verifyArgs...); err != nil {
		if !errors.Is(err, errAlreadyUpToDate) {
			a.updateLog(id, "error", "[update:"+id+"] ERROR: "+err.Error())
			return nil, err
		}
		a.updateLog(id, "info", "[update:"+id+"] already up to date (identical binary)")
	}
	// The version caches are keyed on path: same path, new bytes —
	// drop the stale value so Current refreshes immediately.
	a.invalidateCoreVersion(id)
	c := UpdatableComponent{ID: id, Latest: rel.TagName, UpdateAvail: false}
	for _, base := range a.localUpdatables() {
		if base.ID == id {
			c = base
			break
		}
	}
	c.Latest = rel.TagName
	c.UpdateAvail = false
	c.Notes = ""
	return &c, nil
}

// upstreamCoreInstallPath is where the official sing-box.exe belongs,
// mirroring resolveUpstreamCore: the dedicated UpstreamCorePath (a
// directory or a sing-box-named file) when set, otherwise the sing-box
// already resolved from bin\ / the exe folder (updated in place), and
// bin\ next to nekogo.exe as the default destination. Settings.CorePath
// belongs to the custom core and is never read here. An
// UpstreamCorePath naming a different binary is an error — the upstream
// engine would not find the install there.
func (a *App) upstreamCoreInstallPath() (string, error) {
	if a.store != nil {
		if p := strings.TrimSpace(a.store.GetSettings().UpstreamCorePath); p != "" {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return filepath.Join(p, coreExeName("sing-box")), nil
			}
			if isOfficialSingBoxName(p) {
				return p, nil
			}
			return "", fmt.Errorf("upstreamCorePath %q is not the official sing-box; clear it or point it at sing-box.exe", p)
		}
	}
	if cur, ok := firstEngineFile(coreExeName("sing-box"), "sing-box"); ok {
		return cur, nil
	}
	return filepath.Join(coreExeDir(), coreExeName("sing-box")), nil
}

// coreImageOwner reports which updater row owns a binary file name, "" when
// none does. Rows install by name into the same directory, so a
// destination that names another row's binary would clobber it (the
// sing-box / nekobox_core conflation this split exists to prevent).
func coreImageOwner(path string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	switch base {
	case strings.ToLower(coreExeName("sing-box")):
		return "singbox"
	case strings.ToLower(coreExeName("nekobox_core")):
		return "nekobox"
	case strings.ToLower(coreExeName("xray")):
		return "xray"
	}
	return ""
}

// updateOfficialSingBox installs the official CLI from SagerNet's
// stable release into the path resolveUpstreamCore reads.
func (a *App) updateOfficialSingBox() (*UpdatableComponent, error) {
	if a.coreBusy() {
		return nil, fmt.Errorf("disconnect before updating")
	}
	rel, err := githubLatest(officialSingBoxOwnerRepo)
	if err != nil {
		return nil, err
	}
	asset := officialSingBoxAsset(rel.Assets)
	if asset == nil {
		return nil, fmt.Errorf("no sing-box build for %s/%s in %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}
	dest, err := a.upstreamCoreInstallPath()
	if err != nil {
		return nil, err
	}
	a.updateLog("singbox", "info", fmt.Sprintf("[update:singbox] installing %s from %s", asset.Name, rel.TagName))
	data, err := a.downloadFile("singbox", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+err.Error())
		return nil, err
	}
	data, err = extractExeFromZip(data, coreExeName("sing-box"))
	if err != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+err.Error())
		return nil, err
	}
	if err := a.installExeWithBackup("singbox", dest, data, "version"); err != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+err.Error())
		return nil, err
	}
	a.invalidateCoreVersion("singbox")
	c := UpdatableComponent{ID: "singbox", Latest: rel.TagName, UpdateAvail: false}
	for _, base := range a.localUpdatables() {
		if base.ID == "singbox" {
			c = base
			break
		}
	}
	c.Latest = rel.TagName
	c.UpdateAvail = false
	c.Notes = ""
	return &c, nil
}

// currentCorePath returns the resolved exe path a component runs
// today ("" when none resolves — fresh install to the exe dir).
func (a *App) currentCorePath(id string) string {
	switch id {
	case "singbox":
		if a.upstreamMgr != nil && a.store != nil {
			if p, err := a.upstreamMgr.resolveUpstreamCore(a.store.GetSettings()); err == nil {
				return p
			}
		}
	case "nekobox":
		if p, err := a.singbox.resolveCore(); err == nil {
			return p
		}
	case "xray":
		if p, err := a.xray.resolveXray(); err == nil {
			return p
		}
	}
	return ""
}

// invalidateCoreVersion drops the cached version line for a component
// whose binary was just replaced (same path, new content).
func (a *App) invalidateCoreVersion(id string) {
	switch id {
	case "singbox":
		a.upstreamVer.mu.Lock()
		a.upstreamVer.version = ""
		a.upstreamVer.mu.Unlock()
	case "nekobox":
		a.coreVer.mu.Lock()
		a.coreVer.version = ""
		a.coreVer.mu.Unlock()
	case "xray":
		a.xrayVer.mu.Lock()
		a.xrayVer.version = ""
		a.xrayVer.mu.Unlock()
	}
}

// installerAutoOpen is a test seam for updateGUI. It stays nil in
// production: a downloaded GUI installer must NEVER auto-open — there
// is no publisher signature/hash check yet, so an unverified asset
// would mean remote code execution on update (BUG-010). The UI offers
// an explicit "Open installer" button instead (which surfaces open
// errors verbatim via OpenURL). Tests set this hook to a spy to prove
// updateGUI makes no auto-open attempt.
// TODO: GPG/Sigstore verification of the downloaded asset before any
// auto-open behavior may be re-enabled.
var installerAutoOpen func(path string) error

// updateGUI downloads the installer to %APPDATA%/NekoGo/updates (via
// ConfigStore.Dir) and reports its path for a manual, user-initiated
// open (the GUI cannot replace its own running exe, so nothing is
// applied silently).
// Source: xenonama/NekoGo/releases (list endpoint, prereleases
// included — same audit as checkGUI).
func (a *App) updateGUI() (*UpdatableComponent, error) {
	c := UpdatableComponent{ID: "gui", Name: "NekoGo GUI", Current: appVersion}
	rel, err := githubFirstRelease(forkOwnerRepo)
	if err != nil {
		return nil, err
	}
	asset := pickGUIAsset(rel.Assets)
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = "Manual update only"
		return &c, nil
	}
	data, err := a.downloadFile("gui", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("gui", "error", "[update:gui] ERROR: "+err.Error())
		return nil, err
	}
	dir := filepath.Join(a.store.Dir(), "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := filepath.Base(asset.BrowserDownloadURL)
	if name == "" || name == "/" {
		name = "NekoGo-setup.exe"
	}
	dest := filepath.Join(dir, name)
	if err := a.installFile("gui", dest, data); err != nil {
		// The same installer is already downloaded: report its path for a
		// manual open instead of failing the row.
		if !errors.Is(err, errAlreadyUpToDate) {
			a.updateLog("gui", "error", "[update:gui] ERROR: "+err.Error())
			return nil, err
		}
	}
	a.updateLog("gui", "info", "[update:gui] installer ready: "+dest)
	if installerAutoOpen != nil {
		if err := installerAutoOpen(dest); err != nil {
			return nil, err
		}
	}
	c.Latest = rel.TagName
	c.InstallerPath = dest
	c.Notes = "downloaded to " + dest + " — open it manually to install"
	return &c, nil
}

// UpdateComponent downloads and installs one component by ID, returning
// the refreshed row.
func (a *App) UpdateComponent(id string) (*UpdatableComponent, error) {
	switch id {
	case "geoip":
		url := updateBase("jsdelivr", "https://cdn.jsdelivr.net") + "/gh/Loyalsoldier/geoip@release/Country.mmdb"
		return a.updateDataFile(id, url, "GeoLite2-Country.mmdb")
	case "geosite":
		rel, err := githubLatest("Loyalsoldier/v2ray-rules-dat")
		if err != nil {
			return nil, err
		}
		asset := pickNamed(rel.Assets, "geosite.dat")
		if asset == nil {
			return nil, fmt.Errorf("geosite.dat not found in %s", rel.TagName)
		}
		c, err := a.updateDataFile(id, asset.BrowserDownloadURL, "geosite.dat")
		if err != nil {
			return nil, err
		}
		_ = os.WriteFile(geositeMarker(), []byte(rel.TagName), 0o644)
		c.Latest = rel.TagName
		return c, nil
	case "singbox":
		return a.updateOfficialSingBox()
	case "nekobox":
		if a.coreBusy() {
			return nil, fmt.Errorf("disconnect before updating")
		}
		return a.updateCoreExe(id, nekoCoreOwnerRepo, "nekobox_core", "sing-box", "version")
	case "xray":
		if a.coreBusy() {
			return nil, fmt.Errorf("disconnect before updating")
		}
		return a.updateCoreExe(id, forkOwnerRepo, "xray", "version")
	case "gui":
		return a.updateGUI()
	default:
		return nil, fmt.Errorf("unknown component %q", id)
	}
}
