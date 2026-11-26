package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// tor_release.go — the Tor row of the Updates panel, read against the
// Tor Project's own download site.
//
// The tor-expert-bundle is the Windows archive that carries a bare
// tor.exe — no browser, no launcher — and dist.torproject.org publishes
// it in a per-release directory beside a signed sha256sums file. Neither
// is a GitHub release, so nothing in the GitHub path of this updater
// could reach it: the Tor row had no source to check against and no way
// to install the binary it named, which left "get me a tor.exe" a
// request the app could not carry out.
//
// Two things about this site shape the code. The listing is an Apache
// autoindex, so the newest release has to be picked by version NUMBER —
// a string sort puts 9.0.14 above 16.0.1 and offers a downgrade. And the
// archive is versioned by Tor Browser (16.0.1) while the binary inside
// reports Tor's own series (0.4.9.16), with no mapping published on the
// site, so an install records the bundle it came from and only that
// comparison is ever offered as an update.

const (
	torDistDefault = "https://dist.torproject.org"
	torDistDir     = "torbrowser"
	// torDistSumsFile lists "<sha256>  <asset>" for everything in a
	// release directory. The signed one is the site's own integrity
	// claim; the unsigned variant is the fallback some directories carry.
	torDistSumsFile     = "sha256sums-signed-build.txt"
	torDistSumsFallback = "sha256sums-unsigned-build.txt"
	// torBundleMarker records the bundle version an install came from.
	// Tor Browser versions its archive (16.0.1) while tor.exe reports
	// its own (0.4.9.16), and the dist site maps neither to the other,
	// so the only honest comparison is against what the app installed.
	torBundleMarker = "NEKOGO-BUNDLE.txt"
	// torBundlePrefix is the archive subtree NekoGo installs: the engine
	// and its pluggable transports. data\ and docs\ belong to the
	// browser, not to a core running from a config the app renders.
	torBundlePrefix = "tor/"
)

// torBundle is one Tor Browser release directory on the dist site.
type torBundle struct {
	Version string // "16.0.1"
	Asset   string // "tor-expert-bundle-windows-x86_64-16.0.1.tar.gz"
	URL     string
	SHA256  string // from the sums file, "" when the name is not listed
}

// torBundleAssetName is the archive the site publishes for one release on
// this platform. The expert bundle is the one that carries a bare
// tor.exe; the browser bundle carries a launcher and a whole Firefox,
// and neither the resolver nor the chain wants it. The arch spellings
// are the site's, not Go's (measured in the 16.0.1 directory).
func torBundleAssetName(version string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	arch := "x86_64"
	switch runtime.GOARCH {
	case "386":
		arch = "i686"
	case "arm64":
		arch = "aarch64"
	}
	return "tor-expert-bundle-windows-" + arch + "-" + version + ".tar.gz"
}

func torDistURL(parts ...string) string {
	return updateBase("tor-dist", torDistDefault) + "/" + torDistDir + "/" + strings.Join(parts, "/")
}

// torDistListing reads one small body from the site. A variable so a
// test server stands in for the whole of it, the way updateBaseURL's
// "github-api" key does for the GitHub API root.
var torDistListing = func(url string) ([]byte, error) {
	resp, err := updateGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tor dist %s: %s", filepath.Base(url), resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// torStableDirRe matches a release directory name: three dotted numbers
// and nothing else. Alphas ("16.0a3") are not releases NekoGo can
// install, and the site's own folders carry no version at all.
var torStableDirRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// hrefDirRe pulls the target out of an autoindex anchor: href="16.0.1/".
var hrefDirRe = regexp.MustCompile(`href="([^"]*?)/"`)

// torNewestVersion picks the numerically newest stable directory in an
// autoindex listing.
func torNewestVersion(html string) string {
	best, bestNums := "", [3]int{}
	for _, m := range hrefDirRe.FindAllStringSubmatch(html, -1) {
		g := torStableDirRe.FindStringSubmatch(m[1])
		if g == nil {
			continue
		}
		var nums [3]int
		bad := false
		for i := 1; i <= 3; i++ {
			n, err := strconv.Atoi(g[i])
			if err != nil {
				bad = true
				break
			}
			nums[i-1] = n
		}
		if bad {
			continue
		}
		if best == "" || numsGreaterThan(nums, bestNums) {
			best, bestNums = m[1], nums
		}
	}
	return best
}

func numsGreaterThan(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// torDigestFromSums finds the digest the sums file publishes for [asset].
func torDigestFromSums(body, asset string) string {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) != 64 {
			continue
		}
		if strings.TrimPrefix(fields[1], "./") == asset {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

// torLatestBundle finds the newest stable release directory and the
// Windows expert bundle inside it, with the digest the site publishes.
var torLatestBundle = func() (torBundle, error) {
	asset := torBundleAssetName("")
	if asset == "" {
		return torBundle{}, fmt.Errorf("no tor.exe bundle is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	body, err := torDistListing(torDistURL("") + "/")
	if err != nil {
		return torBundle{}, err
	}
	ver := torNewestVersion(string(body))
	if ver == "" {
		return torBundle{}, fmt.Errorf("no Tor Browser release directories found under %s", updateBase("tor-dist", torDistDefault))
	}
	name := torBundleAssetName(ver)
	b := torBundle{Version: ver, Asset: name, URL: torDistURL(ver, name)}
	for _, sums := range []string{torDistSumsFile, torDistSumsFallback} {
		sb, err := torDistListing(torDistURL(ver, sums))
		if err != nil {
			continue
		}
		if d := torDigestFromSums(string(sb), name); d != "" {
			b.SHA256 = d
			break
		}
	}
	return b, nil
}

// torBundleRecord reads the bundle version an earlier install recorded
// beside [exe] ("" when NekoGo installed nothing there).
func torBundleRecord(exe string) string {
	if exe == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), torBundleMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// checkTorRelease fills the Tor row from the dist site.
func (a *App) checkTorRelease(c *UpdatableComponent) {
	b, err := torLatestBundle()
	if err != nil {
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s/%s latest=%s", c.ID, torDistDefault, torDistDir, b.Version))
	c.Latest = b.Version
	if b.Asset == "" {
		c.UpdateAvail = false
		return
	}
	if c.Current == "" {
		c.UpdateAvail = true
		return
	}
	rec := torBundleRecord(a.currentCorePath("tor"))
	switch {
	case rec == "":
		// A tor.exe NekoGo did not install. 16.0.1 and 0.4.9.16 are
		// different series and the site maps neither to the other, so
		// offering an update would be a guess that nags on every
		// refresh; the row says what it cannot compare instead.
		c.UpdateAvail = false
		c.Notes = "Installed outside NekoGo: a Tor Browser release cannot be compared with tor.exe's own version, and updating replaces it with the newest bundle"
	case rec != b.Version:
		c.UpdateAvail = true
		c.Notes = "Installed from bundle " + rec
	default:
		c.UpdateAvail = false
	}
}

// torInstallPath is where the bundle's tor.exe belongs: the
// Settings.TorPath override (a directory gets tor.exe inside it, a file
// is replaced in place), otherwise bin\tor\tor.exe next to nekogo.exe —
// the layout resolveTorExe searches, with the transports beside it.
func (a *App) torInstallPath() (string, error) {
	if a.store != nil {
		if p := strings.TrimSpace(a.store.GetSettings().TorPath); p != "" {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return filepath.Join(p, torExeName), nil
			}
			return p, nil
		}
	}
	if cur := a.currentCorePath("tor"); cur != "" {
		return cur, nil
	}
	return filepath.Join(engineInstallDir(), "tor", torExeName), nil
}

// updateTorRelease installs the newest expert bundle: the archive is
// fetched and verified against the site's digest before anything is
// unpacked, then tor.exe lands with the usual backup, smoke verify and
// rollback, and the transports beside it — a tor.exe without
// lyrebird.exe starts, fails to launch a transport for every bridge
// line, and then sits in a bootstrap that reads as a network problem.
func (a *App) updateTorRelease() (*UpdatableComponent, error) {
	if a.coreBusy() {
		return nil, fmt.Errorf("disconnect before updating")
	}
	b, err := torLatestBundle()
	if err != nil {
		return nil, err
	}
	if b.Asset == "" {
		return nil, fmt.Errorf("no tor.exe bundle for %s/%s in %s", runtime.GOOS, runtime.GOARCH, b.Version)
	}
	dest, err := a.torInstallPath()
	if err != nil {
		return nil, err
	}
	if owner := coreImageOwner(dest); owner != "" && owner != "tor" {
		return nil, fmt.Errorf("tor update would overwrite %s, which belongs to the %s row; fix torPath in settings", filepath.Base(dest), owner)
	}
	a.updateLog("tor", "info", fmt.Sprintf("[update:tor] installing %s from %s", b.Asset, b.Version))
	data, err := a.downloadFile("tor", b.URL)
	if err != nil {
		a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
		return nil, err
	}
	digest := ""
	if b.SHA256 != "" {
		digest = "sha256:" + b.SHA256
	}
	if err := a.verifyDigest("tor", b.Asset, digest, data); err != nil {
		a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
		return nil, err
	}
	files, err := extractTarGzUnder(data, torBundlePrefix)
	if err != nil {
		a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
		return nil, err
	}
	exe, ok := files[torExeName]
	if !ok {
		err = fmt.Errorf("%s holds no %s", b.Asset, torBundlePrefix+torExeName)
		a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
		return nil, err
	}
	dir := filepath.Dir(dest)
	installed := true
	// "--version", not aether's "-v": tor reads -v as an option with no
	// value and falls through to starting the daemon, which would leave
	// a stray Tor running from inside the updater.
	if err := a.installExeWithBackup("tor", dest, exe, "--version"); err != nil {
		if !errors.Is(err, errAlreadyUpToDate) {
			a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
			return nil, err
		}
		installed = false
		a.updateLog("tor", "info", "[update:tor] already up to date (identical binary)")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == torExeName {
			continue
		}
		if err := a.installFile("tor", filepath.Join(dir, name), files[name]); err != nil && !errors.Is(err, errAlreadyUpToDate) {
			a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
			return nil, err
		}
	}
	if err := a.installFile("tor", filepath.Join(dir, torBundleMarker), []byte(b.Version+"\n")); err != nil && !errors.Is(err, errAlreadyUpToDate) {
		a.updateLog("tor", "error", "[update:tor] ERROR: "+err.Error())
		return nil, err
	}
	// The version cache is keyed on path and the path did not change.
	a.invalidateCoreVersion("tor")
	c := UpdatableComponent{ID: "tor", Latest: b.Version}
	for _, base := range a.localUpdatables() {
		if base.ID == "tor" {
			c = base
			break
		}
	}
	c.Latest = b.Version
	c.UpdateAvail = false
	if !installed {
		c.Notes = "Already up to date"
	}
	return &c, nil
}

// extractTarGzUnder unpacks every regular file under [prefix] in a
// .tar.gz, keyed by its path below that prefix. The site's archives are
// gzip'd tars, which no zip reader opens, and the prefix is what keeps
// the browser's data\ and docs\ out of the engine folder.
func extractTarGzUnder(data []byte, prefix string) (map[string][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasPrefix(h.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(h.Name, prefix)
		if rel == "" {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != h.Size {
			return nil, fmt.Errorf("%s: truncated (%d of %d bytes)", h.Name, len(body), h.Size)
		}
		out[filepath.FromSlash(rel)] = body
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no files under %s in the archive", prefix)
	}
	return out, nil
}
