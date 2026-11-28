package main

// Update policy (v2rayN-style manager, in-app):
//
// Core and data rows download through the selected proxy without stopping
// the active connection. Installation replaces only the target image; a
// connection using that image is stopped and reconnected afterwards.
// NekoGo itself is staged as raw nekogo.exe and applied by a post-exit helper.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manual update manager (no auto-check). Checks and downloads share the
// selected proxy-aware transport; tests redirect sources through updateBaseURL.

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

func (a *App) updaterCheckClient() *http.Client {
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: a.updaterDownloadClient().Transport,
	}
}

// The download client bounds connection phases while allowing the body to
// stream. Compression is off so Content-Length stays the real byte count.
func newUpdaterDownloadTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
}

func (a *App) updaterDownloadClient() *http.Client {
	a.updaterHTTPOnce.Do(func() {
		a.updaterHTTPClient = &http.Client{Transport: newUpdaterDownloadTransport(a.updaterProxy)}
	})
	return a.updaterHTTPClient
}

func (a *App) updaterProxy(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil || proxyBypassHost(req.URL.Hostname()) {
		return nil, nil
	}
	target := req.URL
	if !a.UpdaterProxyEnabled() {
		return nil, nil
	}
	if a.cores != nil {
		ports := a.ActiveProxyPorts()
		if ports.HTTP > 0 || ports.SOCKS > 0 {
			return &url.URL{Scheme: "http", Host: proxyServerValue(ports)}, nil
		}
	}
	if on, server, err := a.proxyStateRead()(); err == nil && on {
		if proxy, ok := parseSystemProxyServer(server, target.Scheme); ok {
			return proxy, nil
		}
	}
	return http.ProxyFromEnvironment(req)
}

func proxyBypassHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func parseSystemProxyServer(value, scheme string) (*url.URL, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	if strings.Contains(value, "=") {
		keys := []string{"https", "http", "socks"}
		if scheme == "http" {
			keys = []string{"http", "https", "socks"}
		}
		for _, key := range keys {
			for _, part := range strings.Split(value, ";") {
				name, address, ok := strings.Cut(strings.TrimSpace(part), "=")
				if ok && strings.EqualFold(strings.TrimSpace(name), key) {
					defaultScheme := "http"
					if key == "socks" {
						defaultScheme = "socks5"
					}
					return parseProxyURL(address, defaultScheme)
				}
			}
		}
		return nil, false
	}
	return parseProxyURL(value, "http")
}

func parseProxyURL(value, defaultScheme string) (*url.URL, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	if !strings.Contains(value, "://") {
		value = defaultScheme + "://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return nil, false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, true
	default:
		return nil, false
	}
}

// errAlreadyUpToDate marks a download whose bytes match the installed
// file. It is a successful no-op, not a failure: the rows used to log it
// as an ERROR, so "Update" on a current component looked broken.
var errAlreadyUpToDate = errors.New("already up to date")

var errUpdateCancelled = errors.New("download cancelled")

// maxDownloadBytes caps a single download (cores are tens of MB).
const maxDownloadBytes = 512 << 20

// Download resilience policy. A link that is slow, or that goes away and
// comes back (mobile data, filtered networks), must not turn an update
// into either an indefinite hang or a full restart of a 70 MB installer.
// Vars, not consts, so tests can shrink the waits to milliseconds.
var (
	// downloadIdleTimeout is the longest a body may produce zero bytes.
	// the download client deliberately has no whole-request Timeout (a slow link
	// needs minutes), so without this a connection that dies mid-transfer
	// — the common case when the network drops — blocks Read forever.
	downloadIdleTimeout = 30 * time.Second
	// downloadMaxAttempts counts the first try plus the retries.
	downloadMaxAttempts = 5
	// downloadRetryDelay doubles per attempt, capped at downloadRetryCap.
	downloadRetryDelay = 1 * time.Second
	// downloadProgressEvery throttles the progress log lines.
	downloadProgressEvery = 2 * time.Second
)

const downloadRetryCap = 20 * time.Second

// UpdatableComponent is one row of the Updates panel.
type UpdatableComponent struct {
	ID           string `json:"id"` // "geoip" | "geosite" | "singbox" | "xray" | "mihomo" | "psiphon" | "psiphon-servers" | "nekogo"
	Name         string `json:"name"`
	Current      string `json:"current"`
	Latest       string `json:"latest"`
	UpdateAvail  bool   `json:"updateAvail"`
	CheckEnabled bool   `json:"checkEnabled"`
	Notes        string `json:"notes,omitempty"`
	LastCheck    string `json:"lastCheck,omitempty"` // RFC3339
	LastError    string `json:"lastError,omitempty"`
	// CheckUnavailable marks an exceptional check failure that must not
	// block a manual update attempt (currently Psiphon Servers only).
	CheckUnavailable bool `json:"checkUnavailable,omitempty"`
	// DownloadedBytes and TotalBytes drive the live "10/20 MB" display.
	// A negative total means the source did not declare a length.
	DownloadedBytes int64 `json:"downloadedBytes,omitempty"`
	TotalBytes      int64 `json:"totalBytes,omitempty"`
	Downloading     bool  `json:"downloading,omitempty"`
	// UpdateFoundAt remembers the first check that found this update.
	// It stays visible until the update is actually installed.
	UpdateFoundAt string `json:"updateFoundAt,omitempty"`
}

type updaterRowState struct {
	CheckEnabled     *bool  `json:"checkEnabled,omitempty"`
	Latest           string `json:"latest,omitempty"`
	UpdateAvail      bool   `json:"updateAvail,omitempty"`
	Notes            string `json:"notes,omitempty"`
	LastCheck        string `json:"lastCheck,omitempty"`
	LastError        string `json:"lastError,omitempty"`
	CheckUnavailable bool   `json:"checkUnavailable,omitempty"`
	UpdateFoundAt    string `json:"updateFoundAt,omitempty"`
	CompletedLatest  string `json:"completedLatest,omitempty"`
}

type updaterState struct {
	Version      int                        `json:"version"`
	ProxyEnabled *bool                      `json:"proxyEnabled,omitempty"`
	Rows         map[string]updaterRowState `json:"rows"`
}

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
// folder next to the GUI exe (created on install). Existing resolved
// core paths are updated in place.
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
	xrCurrent, xrNote := a.coreDisplayVersion("xray", a.XrayVersion())
	mhCurrent, mhNote := a.coreDisplayVersion("mihomo", a.MihomoVersion())
	psCurrent, psNote := a.coreDisplayVersion("psiphon", a.AetherVersion())
	psNote = a.psiphonTransportNote(psNote)
	out := []UpdatableComponent{
		{ID: "geoip", Name: "GeoIP Database", Current: dataFileCurrent(geoPath)},
		{ID: "geosite", Name: "GeoSite Rule-set", Current: dataFileCurrent(geoSitePath)},
		{ID: "singbox", Name: "sing-box Core (official)", Current: sbCurrent, Notes: sbNote},
		{ID: "xray", Name: "Xray Core", Current: xrCurrent, Notes: xrNote},
		{ID: "mihomo", Name: "mihomo Core (Clash Meta)", Current: mhCurrent, Notes: mhNote},
		{ID: "psiphon", Name: "Aether", Current: psCurrent, Notes: psNote},
		psiphonServersRow(),
		{ID: "nekogo", Name: "NekoGo", Current: appVersionValue()},
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

// psiphonTransportNote is the same report for aether.exe, which carries
// Psiphon in its own pt\ folder and runs its Arti client from there too.
func (a *App) psiphonTransportNote(note string) string {
	bin := a.currentCorePath("psiphon")
	missing := missingTorTransports(bin)
	if len(missing) == 0 {
		return note
	}
	pt := fmt.Sprintf("pt\\%s is missing next to %s — Tor bridges (obfs4, webtunnel) cannot launch",
		strings.Join(missing, ", pt\\"), filepath.Base(bin))
	if note != "" {
		return note + "; " + pt
	}
	return pt
}

// exeVersionQuery runs "<exe> args..." and returns the first stdout
// line ("" on any error). A variable — not a direct runVersionQuery
// call — so tests can stub the smoke-verify step without shipping a
// real executable.
var exeVersionQuery = runVersionQuery

var (
	nekogoExecutable        = os.Executable
	startNekoGoUpdateHelper = startSelfUpdateHelper
	coreApplyReconnect      = func(a *App, id string) error { return a.connect(id, true) }
	coreApplyConnectWait    = 60 * time.Second
	coreApplyProbeWait      = 45 * time.Second
)

// psiphonServersLocal reports the list the Psiphon engine starts from: a
// downloaded list when the one in the aether work dir is Psiphon's signed
// list, the bundled list otherwise. The time is the downloaded list's
// publication time (its mtime), and zero when there is no usable
// downloaded list — an invalid one counts as none, so any signed list may
// take its place.
func psiphonServersLocal() (current string, published time.Time, note string) {
	active := psiphonActiveListPath()
	if data, err := os.ReadFile(active); err == nil {
		if n, err := psiphonVerifyList(data, psiphonListKey); err == nil {
			if st, err := os.Stat(active); err == nil {
				published = st.ModTime().UTC()
				return fmt.Sprintf("%d entries (%s)", n, published.Format("2006-01-02")), published, ""
			}
			return fmt.Sprintf("%d entries", n), time.Time{}, ""
		}
		return dataFileCurrent(active), time.Time{},
			"The list in use is not Psiphon's signed server list — any signed list takes its place"
	}
	if text, err := psiphonUnpack(psiphonBundledList, psiphonSigningKey); err == nil {
		current = fmt.Sprintf("Bundled list (%d entries)", psiphonEntryCount(text))
	}
	return current, time.Time{}, "No downloaded list — the list shipped with NekoGo is in use"
}

// psiphonServersRow is one Updates row built from local state only.
func psiphonServersRow() UpdatableComponent {
	current, _, note := psiphonServersLocal()
	return UpdatableComponent{ID: "psiphon-servers", Name: "Psiphon Servers", Current: current, Notes: note}
}

func (a *App) loadUpdaterStateLocked() {
	if a.updaterLoaded {
		return
	}
	a.updaterLoaded = true
	a.updaterRows = make(map[string]updaterRowState)
	a.updaterBytes = make(map[string][2]int64)
	a.updaterRunning = make(map[string]bool)
	a.updaterContexts = make(map[string]context.Context)
	a.updaterCancels = make(map[string]context.CancelFunc)
	a.updaterProxyEnabled = true
	if a.store == nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(a.store.Dir(), "updates.json"))
	if err != nil {
		return
	}
	var state updaterState
	if err := json.Unmarshal(data, &state); err != nil {
		updateLog := a.updateLog
		updateLog("gui", "warn", "[update:gui] ignoring unreadable updates.json")
		return
	}
	if state.ProxyEnabled != nil {
		a.updaterProxyEnabled = *state.ProxyEnabled
	}
	for id, row := range state.Rows {
		row.UpdateFoundAt = strings.TrimSpace(row.UpdateFoundAt)
		row.CompletedLatest = strings.TrimSpace(row.CompletedLatest)
		a.updaterRows[id] = row
	}
}

func (a *App) saveUpdaterStateLocked() {
	if a.store == nil {
		return
	}
	proxyEnabled := a.updaterProxyEnabled
	state := updaterState{Version: 1, Rows: a.updaterRows, ProxyEnabled: &proxyEnabled}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	path := filepath.Join(a.store.Dir(), "updates.json")
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		a.updateLog("gui", "warn", "[update:gui] could not save update reminders: "+err.Error())
		return
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		_ = os.Remove(path + ".tmp")
		a.updateLog("gui", "warn", "[update:gui] could not save update reminders: "+err.Error())
	}
}

func (a *App) overlayUpdaterStateLocked(rows []UpdatableComponent) {
	for i := range rows {
		row := &rows[i]
		row.CheckEnabled = true
		state, ok := a.updaterRows[row.ID]
		if ok {
			if state.CheckEnabled != nil {
				row.CheckEnabled = *state.CheckEnabled
			}
			row.Latest = state.Latest
			row.CheckUnavailable = state.CheckUnavailable
			if state.Latest != "" && state.CompletedLatest != state.Latest && updateNeeded(row.Current, state.Latest) {
				row.UpdateAvail = state.UpdateAvail
			} else if state.CheckUnavailable && row.ID == "psiphon-servers" {
				row.UpdateAvail = state.UpdateAvail
			}
			if row.UpdateAvail && state.UpdateFoundAt != "" {
				row.UpdateFoundAt = state.UpdateFoundAt
			}
			row.LastCheck = state.LastCheck
			row.LastError = state.LastError
			if state.Notes != "" {
				row.Notes = state.Notes
			}
		}
		if bytes, ok := a.updaterBytes[row.ID]; ok {
			row.DownloadedBytes, row.TotalBytes = bytes[0], bytes[1]
		}
		row.Downloading = a.updaterRunning[row.ID]
	}
}

// ListUpdatables returns the current local state of every updatable
// component (no network), including remembered checks and live progress.
func (a *App) ListUpdatables() ([]UpdatableComponent, error) {
	rows := a.localUpdatables()
	a.updaterMu.Lock()
	a.loadUpdaterStateLocked()
	a.overlayUpdaterStateLocked(rows)
	a.updaterMu.Unlock()
	return rows, nil
}

func (a *App) SetUpdatableEnabled(id string, enabled bool) error {
	known := false
	for _, row := range a.localUpdatables() {
		if row.ID == id {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("unknown component %q", id)
	}
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	state := a.updaterRows[id]
	state.CheckEnabled = &enabled
	a.updaterRows[id] = state
	a.saveUpdaterStateLocked()
	return nil
}

func (a *App) UpdaterProxyEnabled() bool {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	return a.updaterProxyEnabled
}

func (a *App) SetUpdaterProxyEnabled(enabled bool) error {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	a.updaterProxyEnabled = enabled
	a.saveUpdaterStateLocked()
	return nil
}

func (a *App) CancelUpdate(id string) bool {
	a.updaterMu.Lock()
	cancel := a.updaterCancels[id]
	if cancel != nil {
		delete(a.updaterCancels, id)
	}
	a.updaterMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (a *App) updaterDownloadContext(id string) context.Context {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	if ctx := a.updaterContexts[id]; ctx != nil {
		return ctx
	}
	return context.Background()
}

func (a *App) rememberUpdaterRows(rows []UpdatableComponent) {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	now := time.Now().Format(time.RFC3339)
	for _, row := range rows {
		state := a.updaterRows[row.ID]
		if row.CheckUnavailable {
			if row.Latest != "" {
				state.Latest = row.Latest
			}
			state.UpdateAvail = true
			state.Notes = row.Notes
			state.LastCheck = row.LastCheck
			state.LastError = ""
			state.CheckUnavailable = true
			a.updaterRows[row.ID] = state
			continue
		}
		if row.LastError != "" && state.Latest != "" {
			state.LastCheck = row.LastCheck
			state.LastError = row.LastError
			a.updaterRows[row.ID] = state
			continue
		}
		previousLatest := state.Latest
		state.Latest = row.Latest
		state.UpdateAvail = row.UpdateAvail
		state.Notes = row.Notes
		state.LastCheck = row.LastCheck
		state.LastError = row.LastError
		state.CheckUnavailable = false
		if row.UpdateAvail && row.Latest != "" && row.Latest != state.CompletedLatest {
			if state.UpdateFoundAt == "" || previousLatest != row.Latest {
				state.UpdateFoundAt = now
			}
		} else {
			state.UpdateFoundAt = ""
		}
		a.updaterRows[row.ID] = state
	}
	a.saveUpdaterStateLocked()
}

func (a *App) beginUpdaterDownload(id string) {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	if cancel := a.updaterCancels[id]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.updaterContexts[id] = ctx
	a.updaterCancels[id] = cancel
	a.updaterBytes[id] = [2]int64{0, -1}
	a.updaterRunning[id] = true
}

func (a *App) setUpdaterProgress(id string, downloaded, total int64) {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	a.updaterBytes[id] = [2]int64{downloaded, total}
}

func (a *App) setUpdaterTotal(id string, total int64) {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	bytes := a.updaterBytes[id]
	bytes[1] = total
	a.updaterBytes[id] = bytes
}

func (a *App) finishUpdaterDownload(id string, row *UpdatableComponent, err error) {
	a.updaterMu.Lock()
	defer a.updaterMu.Unlock()
	a.loadUpdaterStateLocked()
	delete(a.updaterBytes, id)
	delete(a.updaterRunning, id)
	delete(a.updaterContexts, id)
	if cancel := a.updaterCancels[id]; cancel != nil {
		cancel()
		delete(a.updaterCancels, id)
	}
	if err != nil || row == nil {
		return
	}
	state := a.updaterRows[id]
	state.Latest = row.Latest
	state.Notes = row.Notes
	state.LastError = ""
	state.CheckUnavailable = false
	if id == "nekogo" {
		state.UpdateAvail = row.UpdateAvail
		if state.UpdateAvail {
			if state.UpdateFoundAt == "" {
				state.UpdateFoundAt = time.Now().Format(time.RFC3339)
			}
		} else {
			state.UpdateFoundAt = ""
		}
		row.UpdateAvail = state.UpdateAvail
		row.UpdateFoundAt = state.UpdateFoundAt
	} else {
		state.CompletedLatest = row.Latest
		state.UpdateAvail = false
		state.UpdateFoundAt = ""
		row.UpdateAvail = false
		row.UpdateFoundAt = ""
	}
	a.updaterRows[id] = state
	a.saveUpdaterStateLocked()
}

// --- GitHub release resolution ---

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	// Digest is GitHub's published "sha256:<64 hex>" for the asset.
	// Measured present on SagerNet/sing-box, MetaCubeX/mihomo and
	// Loyalsoldier/v2ray-rules-dat; absent on older releases and on
	// assets GitHub never hashed, so every reader must tolerate "".
	Digest string `json:"digest"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Name    string    `json:"name"`
	Assets  []ghAsset `json:"assets"`
}

func (a *App) updateGet(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersionValue())
	req.Header.Set("Accept", "application/vnd.github+json")
	return a.updaterCheckClient().Do(req)
}

// downloadGet issues one attempt of a binary download through
// downloadHTTP (no whole-request deadline — see updateHTTP). offset > 0
// asks for the missing tail instead of the whole file; validator is the
// strong ETag or Last-Modified of the first response, sent as If-Range so
// an asset that changed since then comes back whole rather than letting
// two different releases be spliced together.
func (a *App) downloadGet(ctx context.Context, rawURL string, offset int64, validator string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersionValue())
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	return a.updaterDownloadClient().Do(req)
}

func (a *App) githubLatest(ownerRepo string) (*ghRelease, error) {
	url := updateBase("github-api", "https://api.github.com") + "/repos/" + ownerRepo + "/releases/latest"
	resp, err := a.updateGet(url)
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
//	| nekogo    | xenonama/NekoGo       | nekogo.exe                            | running executable, applied after exit |
//	| xray      | XTLS/Xray-core        | Xray-windows-64.zip                  | resolved xray path, else exe dir |
//	| mihomo    | MetaCubeX/mihomo      | mihomo-windows-<arch>[-v1]-v<ver>.zip | resolved mihomo path, else exe dir\bin\mihomo.exe |
//	| psiphon   | CluvexStudio/Aether   | aether-windows-x86_64.zip            | resolved aether path, else exe dir |
//	| geoip     | Loyalsoldier/geoip    | Country.mmdb (@release)              | gui/assets/GeoLite2-Country.mmdb |
//	| geosite   | Loyalsoldier/v2ray-rules-dat | geosite.dat                     | gui/assets/geosite.dat |
//	| psiphon-servers | Psiphon (signed list) | psiphon_servers.dat (the list Psiphon publishes) | embedded in the app (gui/psiphon_servers.dat), unpacked into the aether work dir as psiphon-servers.txt |
const forkOwnerRepo = "xenonama/NekoGo"

// officialSingBoxOwnerRepo is the upstream sing-box project. The
// official CLI is the only source for the singbox row; it is a drop-in
// for CoreEngine=upstream and needs none of the fork's patches.
const officialSingBoxOwnerRepo = "SagerNet/sing-box"

// mihomoOwnerRepo publishes the Clash Meta engine. Its STABLE release is
// the source: the newest entry in /releases is the rolling
// "Prerelease-Alpha" tag (measured 2026-11-07), which is not a version
// and would make updateNeeded offer the same alpha forever.
const mihomoOwnerRepo = "MetaCubeX/mihomo"

// xrayOwnerRepo publishes xray.exe itself. The row used to read the app
// repo (forkOwnerRepo), which compared an Xray banner against a NekoGo
// release tag and installed the xray.exe bundled with the app release.
const xrayOwnerRepo = "XTLS/Xray-core"

// aetherOwnerRepo publishes aether.exe — the engine that carries Tor
// (arti is linked into it) and Psiphon, and so the only binary either
// exit chain runs. build.bat fetches this same repo and asset when the
// reference tree has none, so a from-source build and an in-app update
// cannot drift onto different binaries.
const aetherOwnerRepo = "CluvexStudio/Aether"

// aetherAssetName is that release's Windows x86-64 archive. Measured
// contents (2.3.0): aether.exe at the root, pt/lyrebird.exe and
// pt/psiphon-tunnel-core.exe — the pluggable transports Tor's bridges and
// Psiphon need — plus run-aether.bat, which NekoGo does not use.
const aetherAssetName = "aether-windows-x86_64.zip"

// aetherZipTransports are the pt\ entries of that archive NekoGo installs
// beside the engine. A release that drops one is not a failed update: the
// engine still installs and the row then reports the transport missing.
var aetherZipTransports = []string{"lyrebird.exe", "psiphon-tunnel-core.exe"}

// mihomoAsset picks the Windows archive for this arch from a stable
// mihomo release, anchored to the exact name build.bat fetches. The
// v1.19.33 release ships 31 Windows archives whose names all contain
// "windows-amd64": -compatible (legacy CPU build), -go120..-go125 (old
// toolchains), -v2/-v3 (newer instruction sets that will not start on
// every machine NekoGo ships to) and a plain amd64 build. A substring
// match takes whichever sorts first, so the name is constructed
// instead. The GOAMD64=v1 asset is the baseline x86-64 build.
//
// Other platforms ship .gz, which the in-place installer does not
// unpack — those stay on the manual note like the official sing-box.
func mihomoAsset(assets []ghAsset, tag string) *ghAsset {
	if runtime.GOOS != "windows" {
		return nil
	}
	level := ""
	if runtime.GOARCH == "amd64" {
		level = "-v1"
	}
	want := strings.ToLower("mihomo-windows-" + runtime.GOARCH + level + "-v" + normTag(tag) + ".zip")
	for i := range assets {
		if strings.ToLower(assets[i].Name) == want {
			return &assets[i]
		}
	}
	return nil
}

// xrayAsset picks the Windows archive for this arch from an XTLS
// release. The names are fixed and arch-suffixed, not versioned:
// Xray-windows-32.zip, Xray-windows-64.zip and
// Xray-windows-arm64-v8a.zip (measured in v26.11.9). Every archive also
// ships as a "<name>.dgst" sidecar whose name contains it, so the match
// is anchored exactly; a substring match would hand back a checksum
// text file to unpack. XTLS versions nothing but the platform in the
// name, so the tag is not part of the match. Other platforms ship zips
// too, but the shipped layout is the Windows one, so they stay on the
// manual note like the sing-box and mihomo rows.
func xrayAsset(assets []ghAsset) *ghAsset {
	if runtime.GOOS != "windows" {
		return nil
	}
	suffix := ""
	switch runtime.GOARCH {
	case "amd64":
		suffix = "-64"
	case "arm64":
		suffix = "-arm64-v8a"
	case "386":
		suffix = "-32"
	default:
		return nil
	}
	want := strings.ToLower("xray-windows" + suffix + ".zip")
	for i := range assets {
		if strings.ToLower(assets[i].Name) == want {
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
func (a *App) githubFirstRelease(ownerRepo string) (*ghRelease, error) {
	url := updateBase("github-api", "https://api.github.com") + "/repos/" + ownerRepo + "/releases?per_page=1"
	resp, err := a.updateGet(url)
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

// pickAssetExact selects the asset whose name matches exactly (case
// folded). The geosite release ships geosite.dat AND
// geosite.dat.sha256sum: a substring match takes whichever GitHub lists
// first, and asset order is upload order, not a contract. Picking the
// .sha256sum would install a 64-byte text file as the rule-set — and
// its own published digest would verify, so nothing downstream would
// notice.
func pickAssetExact(assets []ghAsset, name string) *ghAsset {
	want := strings.ToLower(name)
	for i := range assets {
		if strings.ToLower(assets[i].Name) == want {
			return &assets[i]
		}
	}
	return nil
}

func pickNekoGoAsset(assets []ghAsset) *ghAsset {
	return pickAssetExact(assets, "nekogo.exe")
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

// assetSHA256 returns the lowercase hex sha256 GitHub published for an
// asset, "" when it published none (or used another algorithm).
func assetSHA256(asset *ghAsset) string {
	if asset == nil {
		return ""
	}
	d := strings.TrimSpace(asset.Digest)
	if !strings.HasPrefix(strings.ToLower(d), "sha256:") {
		return ""
	}
	hexPart := strings.ToLower(strings.TrimSpace(d[len("sha256:"):]))
	if len(hexPart) != sha256.Size*2 {
		return ""
	}
	for _, r := range hexPart {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	return hexPart
}

// verifyDigest checks downloaded bytes against a published sha256
// BEFORE anything is extracted or installed. An empty digest means the
// source publishes none (jsDelivr's GeoIP copy, the fork's own assets):
// the download is accepted over HTTPS alone and the log says so, rather
// than pretending it was verified.
func (a *App) verifyDigest(id, name, digest string, data []byte) error {
	want := assetSHA256(&ghAsset{Digest: digest})
	if want == "" {
		a.updateLog(id, "warn", "[update:"+id+"] "+name+" publishes no sha256 digest — trusted over HTTPS only")
		return nil
	}
	if got := sha256Hex(data); got != want {
		return fmt.Errorf("%s: sha256 mismatch (downloaded %s, source published %s) — refusing to install", name, got, want)
	}
	a.updateLog(id, "info", "[update:"+id+"] "+name+" sha256 verified against the published digest")
	return nil
}

// contentHashCurrent recognizes the "sha:<12 hex>" stamp
// coreDisplayVersion/dataFileCurrent give a binary whose own version
// output is missing.
func contentHashCurrent(current string) string {
	s := strings.TrimSpace(current)
	if !strings.HasPrefix(s, "sha:") {
		return ""
	}
	h := strings.ToLower(s[len("sha:"):])
	if len(h) != 12 {
		return ""
	}
	for _, r := range h {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	return h
}

// isArchiveAsset reports whether the asset is a container rather than
// the binary itself. GitHub's digest covers the asset as published, so
// for an archive it is the hash of the zip — never of the exe extracted
// from it.
func isArchiveAsset(asset *ghAsset) bool {
	if asset == nil {
		return false
	}
	n := strings.ToLower(asset.Name)
	return strings.HasSuffix(n, ".zip") || strings.HasSuffix(n, ".gz") || strings.HasSuffix(n, ".7z")
}

// coreUpdateNeeded compares a core row against a release asset. A row
// stamped with a content hash cannot be ordered against a version tag,
// and updateNeeded's unorderable fallback then answers "newer" forever:
// the row offered an update after every check even when the installed
// bytes were exactly the published asset. When the asset IS the binary,
// GitHub's digest makes that comparison exact without downloading
// anything. Archives are excluded — see isArchiveAsset — and so is a
// source that publishes no digest; there the old fallback stands and
// installFile's identical-content no-op keeps the re-download harmless.
func coreUpdateNeeded(current, tag string, asset *ghAsset) bool {
	if h := contentHashCurrent(current); h != "" && !isArchiveAsset(asset) {
		if d := assetSHA256(asset); d != "" {
			return h != d[:len(h)]
		}
	}
	return updateNeeded(current, tag)
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
	req.Header.Set("User-Agent", "NekoGo/"+appVersionValue())
	resp, err := a.updaterCheckClient().Do(req)
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
		// "Up to date" would be a lie if the copy on disk cannot be opened:
		// the detector has already degraded to "no country" and a re-download
		// is the only fix, so keep the row actionable.
		if a.geo != nil {
			if e := a.geo.LastErr(); e != nil && !errors.Is(e, errGeoIPNoDB) {
				c.UpdateAvail = true
				c.Notes = "The installed database cannot be opened (" + e.Error() + ") — re-download to replace it"
				return
			}
		}
		c.Notes = "Up to date"
		return
	}
	c.UpdateAvail = true
}

func (a *App) checkGeosite(c *UpdatableComponent) {
	rel, err := a.githubLatest("Loyalsoldier/v2ray-rules-dat")
	if err != nil {
		c.LastError = err.Error()
		return
	}
	asset := pickAssetExact(rel.Assets, "geosite.dat")
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
		c.Notes = "Up to date"
		return
	}
	c.UpdateAvail = true
	if _, err := os.Stat(marker); err != nil {
		c.Notes = "Unknown installed version"
	}
}

func geositeMarker() string {
	return filepath.Join(updaterDataDir(), "geosite.version")
}

// checkOfficialSingBox resolves the singbox row against SagerNet's
// stable releases. The official CLI is a separate binary from
// nekobox_core.exe; only the Windows zip is unpacked in-app, so other
// platforms stay on the manual note.
func (a *App) checkOfficialSingBox(c *UpdatableComponent) {
	rel, err := a.githubLatest(officialSingBoxOwnerRepo)
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
	if coreUpdateNeeded(c.Current, rel.TagName, asset) {
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

// checkMihomo resolves the mihomo row against MetaCubeX's STABLE
// releases. /releases/latest is required here, not the newest entry of
// /releases: that is the rolling "Prerelease-Alpha" tag, which carries
// no version to compare and would never stop offering itself.
func (a *App) checkMihomo(c *UpdatableComponent) {
	rel, err := a.githubLatest(mihomoOwnerRepo)
	if err != nil {
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=%s", c.ID, mihomoOwnerRepo, rel.TagName))
	c.Latest = rel.TagName
	asset := mihomoAsset(rel.Assets, rel.TagName)
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = fmt.Sprintf("No automatic install for %s/%s — download mihomo manually.", runtime.GOOS, runtime.GOARCH)
		return
	}
	if coreUpdateNeeded(c.Current, rel.TagName, asset) {
		c.UpdateAvail = true
	}
}

// aetherAsset picks the Windows x86-64 archive from an Aether release.
// The exact name is what build.bat fetches, so a from-source build and an
// in-app update read the same asset; a release that versions the name is
// still installable, and an archive for another platform is not (the
// in-place installer unpacks zips only, and NekoGo ships the Windows
// layout).
func aetherAsset(assets []ghAsset) *ghAsset {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return nil
	}
	if a := pickAssetExact(assets, aetherAssetName); a != nil {
		return a
	}
	for i := range assets {
		n := strings.ToLower(assets[i].Name)
		if strings.HasPrefix(n, "aether-windows-x86_64") && strings.HasSuffix(n, ".zip") {
			return &assets[i]
		}
	}
	return nil
}

// checkPsiphon resolves the Aether core row against the release that
// publishes aether.exe — the id stays "psiphon" because that is the
// binary's role in the tree, not its name. The row's Current is the
// engine's own "-v" line, so a folder with no aether.exe reads as "Not
// installed" and offers the update — which is the only way a user learns
// that the Aether core was never there.
func (a *App) checkPsiphon(c *UpdatableComponent) {
	rel, err := a.githubLatest(aetherOwnerRepo)
	if err != nil {
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=%s", c.ID, aetherOwnerRepo, rel.TagName))
	c.Latest = rel.TagName
	asset := aetherAsset(rel.Assets)
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = fmt.Sprintf("No automatic install for %s/%s — download aether manually.", runtime.GOOS, runtime.GOARCH)
		return
	}
	if coreUpdateNeeded(c.Current, rel.TagName, asset) {
		c.UpdateAvail = true
	}
}

// checkPsiphonServers asks Psiphon's own address when the list was
// published and compares it with the downloaded list in use. A list that
// is missing or not Psiphon's is offered an update whatever the time: that
// is the same rule the update applies, where any signed list may take the
// place of one that cannot be used.
func (a *App) checkPsiphonServers(c *UpdatableComponent) {
	current, published, note := psiphonServersLocal()
	c.Current = current
	c.Notes = note
	latest, err := a.psiphonServersPublished(psiphonServersSource())
	if err != nil {
		c.CheckUnavailable = true
		c.UpdateAvail = true
		c.LastError = ""
		c.Notes = "Update check unavailable — you can still try Update."
		a.updateLog(c.ID, "warn", "[update:"+c.ID+"] check unavailable: "+err.Error())
		return
	}
	c.CheckUnavailable = false
	if latest.IsZero() {
		c.CheckUnavailable = true
		c.UpdateAvail = true
		c.LastError = ""
		c.Notes = "Publication time unknown — you can still try Update."
		return
	}
	c.Latest = latest.Format("2006-01-02")
	if published.IsZero() || latest.After(published) {
		c.UpdateAvail = true
	}
}

// psiphonServersSource is the list's address, redirectable in tests.
func psiphonServersSource() string {
	return updateBase("psiphon-servers", psiphonServersURL)
}

// psiphonServersPublished returns the Last-Modified time of the list at
// url; zero when the server does not name one.
func (a *App) psiphonServersPublished(rawURL string) (time.Time, error) {
	req, err := http.NewRequest("HEAD", rawURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersionValue())
	resp, err := a.updaterCheckClient().Do(req)
	if err != nil {
		return time.Time{}, errors.New(downloadErrorText(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("psiphon server list: %s", resp.Status)
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, nil
}

// checkXray resolves the xray row against XTLS/Xray-core's own
// releases — the repo that publishes xray.exe — the same way the
// singbox row follows SagerNet and the mihomo row follows MetaCubeX.
// The app release (forkOwnerRepo) ships an xray.exe too, and reading
// that row compared an Xray banner against a NekoGo tag such as
// "1.0.0-beta.3" and installed the binary bundled with the app.
func (a *App) checkXray(c *UpdatableComponent) {
	rel, err := a.githubLatest(xrayOwnerRepo)
	if err != nil {
		c.LastError = err.Error()
		return
	}
	a.updateLog(c.ID, "info", fmt.Sprintf("[update:%s] source=%s latest=%s", c.ID, xrayOwnerRepo, rel.TagName))
	c.Latest = rel.TagName
	asset := xrayAsset(rel.Assets)
	if asset == nil {
		c.UpdateAvail = false
		c.Notes = fmt.Sprintf("No automatic install for %s/%s — download xray manually.", runtime.GOOS, runtime.GOARCH)
		return
	}
	if coreUpdateNeeded(c.Current, rel.TagName, asset) {
		c.UpdateAvail = true
	}
}

func (a *App) checkNekoGo(c *UpdatableComponent) {
	rel, err := a.githubFirstRelease(forkOwnerRepo)
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
	if pickNekoGoAsset(rel.Assets) == nil {
		c.UpdateAvail = false
		c.Notes = "nekogo.exe is not published in this release"
		return
	}
	if updateNeeded(appVersionValue(), rel.TagName) {
		c.UpdateAvail = true
		return
	}
	if updateNeeded(rel.TagName, appVersionValue()) {
		c.Notes = "This build is newer than the published release"
	}
}

// CheckUpdates queries only the selected components and returns the merged
// result. A nil selection checks every component for Go callers; an empty
// non-nil selection checks none. Partial failures land in LastError.
func (a *App) CheckUpdates(ids []string) ([]UpdatableComponent, error) {
	a.updaterOpMu.Lock()
	defer a.updaterOpMu.Unlock()
	rows := a.localUpdatables()
	known := make(map[string]bool, len(rows))
	for _, row := range rows {
		known[row.ID] = true
	}
	selected := make(map[string]bool, len(rows))
	if ids == nil {
		for id := range known {
			selected[id] = true
		}
	} else {
		for _, id := range ids {
			if !known[id] {
				return nil, fmt.Errorf("unknown component %q", id)
			}
			selected[id] = true
		}
	}
	if len(selected) == 0 {
		return a.ListUpdatables()
	}
	now := time.Now().Format(time.RFC3339)
	var wg sync.WaitGroup
	for i := range rows {
		if !selected[rows[i].ID] {
			continue
		}
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
			case "xray":
				a.checkXray(c)
			case "mihomo":
				a.checkMihomo(c)
			case "psiphon":
				a.checkPsiphon(c)
			case "psiphon-servers":
				a.checkPsiphonServers(c)
			case "nekogo":
				a.checkNekoGo(c)
			}
			c.LastCheck = now
		}(&rows[i])
	}
	wg.Wait()
	checked := make([]UpdatableComponent, 0, len(selected))
	for _, row := range rows {
		if selected[row.ID] {
			checked = append(checked, row)
		}
	}
	a.rememberUpdaterRows(checked)
	return a.ListUpdatables()
}

// --- download + install ---

// downloadState is what survives between attempts: the bytes already in
// hand, the full length the source declared (-1 when it declared none),
// and the validator that keeps a resume honest.
type downloadState struct {
	data      []byte
	total     int64
	validator string
}

// downloadFile fetches rawURL into memory (capped at maxDownloadBytes),
// logging progress. It is built for a link that is slow and intermittently
// dies: each attempt reads under an idle deadline, a failed attempt keeps
// its bytes and asks for the tail with a Range request, and transient
// failures are retried with backoff. A permanent answer (404, an oversized
// body) is returned at once instead of being retried.
func (a *App) downloadFile(id, rawURL string) ([]byte, error) {
	ctx := a.updaterDownloadContext(id)
	if ctx.Err() != nil {
		return nil, errUpdateCancelled
	}
	a.updateLog(id, "info", "[update:"+id+"] downloading from "+redactURLQuery(rawURL))
	st := downloadState{total: -1}
	for attempt := 1; ; attempt++ {
		complete, retryable, err := a.downloadAttempt(id, rawURL, &st)
		if ctx.Err() != nil {
			return nil, errUpdateCancelled
		}
		if err == nil && complete {
			a.updateLog(id, "info", fmt.Sprintf("[update:%s] downloaded %d bytes", id, len(st.data)))
			return st.data, nil
		}
		if err == nil {
			err = errors.New("download ended before the declared length")
		}
		if !retryable || attempt >= downloadMaxAttempts {
			return nil, err
		}
		delay := downloadRetryDelay << (attempt - 1)
		if delay <= 0 || delay > downloadRetryCap {
			delay = downloadRetryCap
		}
		a.updateLog(id, "warn", fmt.Sprintf("[update:%s] attempt %d/%d failed (%s); retrying in %s, %d bytes kept",
			id, attempt, downloadMaxAttempts, err, delay.Round(time.Millisecond), len(st.data)))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errUpdateCancelled
		case <-timer.C:
		}
	}
}

// psiphonListDownloadLimit bounds a whole list download, body included.
// PattN's fetch script uses curl --max-time 180 for the same reason: a
// body that stalls must not hold the Updates panel forever.
var psiphonListDownloadLimit = 3 * time.Minute

// downloadPsiphonList fetches the signed server list, asking the server
// for nothing older than [since] so a list already in use is not sent
// again. It returns the bytes, the publication time from Last-Modified
// (zero when the server names none), and whether the server answered 304.
func (a *App) downloadPsiphonList(id, rawURL string, since time.Time) (data []byte, published time.Time, notModified bool, err error) {
	parent := a.updaterDownloadContext(id)
	if parent.Err() != nil {
		return nil, time.Time{}, false, errUpdateCancelled
	}
	ctx, cancel := context.WithTimeout(parent, psiphonListDownloadLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, time.Time{}, false, err
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersionValue())
	if !since.IsZero() {
		req.Header.Set("If-Modified-Since", since.UTC().Format(http.TimeFormat))
	}
	resp, err := a.updaterDownloadClient().Do(req)
	if err != nil {
		if parent.Err() != nil {
			return nil, time.Time{}, false, errUpdateCancelled
		}
		return nil, time.Time{}, false, errors.New(downloadErrorText(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, time.Time{}, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, false, fmt.Errorf("download %s: %s", redactURLQuery(rawURL), resp.Status)
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			published = t.UTC()
		}
	}
	timer := time.AfterFunc(downloadIdleTimeout, cancel)
	defer timer.Stop()
	body := &idleBody{r: resp.Body, t: timer, d: downloadIdleTimeout}
	a.setUpdaterTotal(id, resp.ContentLength)
	data, err = io.ReadAll(io.LimitReader(&updaterProgressReader{app: a, id: id, r: body, total: resp.ContentLength}, maxDownloadBytes+1))
	if err != nil {
		if parent.Err() != nil {
			return nil, published, false, errUpdateCancelled
		}
		return nil, published, false, fmt.Errorf("download %s: %w", redactURLQuery(rawURL), err)
	}
	if len(data) > maxDownloadBytes {
		return nil, published, false, fmt.Errorf("download exceeds %d bytes", maxDownloadBytes)
	}
	if parent.Err() != nil {
		return nil, published, false, errUpdateCancelled
	}
	a.updateLog(id, "info", fmt.Sprintf("[update:%s] %s received", id, humanBytes(int64(len(data)))))
	return data, published, false, nil
}

// downloadAttempt makes one GET and folds the body into st. complete
// reports a finished body that matches the declared length; retryable
// says another attempt could plausibly succeed.
func (a *App) downloadAttempt(id, rawURL string, st *downloadState) (complete, retryable bool, err error) {
	parent := a.updaterDownloadContext(id)
	if parent.Err() != nil {
		return false, false, errUpdateCancelled
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	offset := int64(len(st.data))
	resp, err := a.downloadGet(ctx, rawURL, offset, st.validator)
	if err != nil {
		if parent.Err() != nil {
			return false, false, errUpdateCancelled
		}
		return false, true, errors.New(downloadErrorText(err))
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// A 200 to a Range request means the source ignored the range (or
		// If-Range decided the asset changed): this body is the whole file,
		// so appending it to the partial buffer would corrupt the install.
		st.data = st.data[:0]
		st.total = resp.ContentLength
		st.validator = rangeValidator(resp)
	case http.StatusPartialContent:
		start, total, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			st.data = st.data[:0]
			st.total, st.validator = -1, ""
			return false, true, fmt.Errorf("download %s: Content-Range %q does not start at the requested offset %d",
				redactURLQuery(rawURL), resp.Header.Get("Content-Range"), offset)
		}
		st.total = total
	case http.StatusRequestedRangeNotSatisfiable:
		// The offset no longer exists: the asset changed, or the buffer is
		// already the whole file. Either way the only way forward is a
		// fresh start.
		st.data = st.data[:0]
		st.total, st.validator = -1, ""
		return false, true, fmt.Errorf("download %s: %s", redactURLQuery(rawURL), resp.Status)
	default:
		retry := resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= 500
		return false, retry, fmt.Errorf("download %s: %s", redactURLQuery(rawURL), resp.Status)
	}

	a.setUpdaterTotal(id, st.total)

	// The network going away mid-transfer usually leaves the socket open
	// with no FIN, so Read blocks forever. The download client has no whole-request
	// timeout on purpose (a slow link legitimately needs minutes); this
	// watchdog bounds the silence instead, and cancels the request so the
	// blocked Read actually returns.
	timer := time.AfterFunc(downloadIdleTimeout, cancel)
	defer timer.Stop()
	body := &idleBody{r: resp.Body, t: timer, d: downloadIdleTimeout}

	lastProgress := time.Now()
	buf := make([]byte, 256*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			st.data = append(st.data, buf[:n]...)
			if len(st.data) > maxDownloadBytes {
				return false, false, fmt.Errorf("download exceeds %d bytes", maxDownloadBytes)
			}
			a.setUpdaterProgress(id, int64(len(st.data)), st.total)
			if time.Since(lastProgress) >= downloadProgressEvery {
				a.updateLog(id, "info", downloadProgressText(id, int64(len(st.data)), st.total))
				lastProgress = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if parent.Err() != nil {
				return false, false, errUpdateCancelled
			}
			return false, true, fmt.Errorf("download %s: %w", redactURLQuery(rawURL), readErr)
		}
	}
	if st.total >= 0 && int64(len(st.data)) < st.total {
		// A clean close short of the declared length: something cut the body
		// without signalling an error. Retrying beats installing a truncated
		// binary, which then fails its digest — or has none and runs.
		return false, true, fmt.Errorf("download %s: got %d of %d bytes", redactURLQuery(rawURL), len(st.data), st.total)
	}
	return true, false, nil
}

type updaterProgressReader struct {
	app   *App
	id    string
	r     io.Reader
	n     int64
	total int64
}

func (r *updaterProgressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.n += int64(n)
		r.app.setUpdaterProgress(r.id, r.n, r.total)
	}
	return n, err
}

// idleBody restarts the stall timer on every read that produced bytes.
type idleBody struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.t.Reset(b.d)
	}
	return n, err
}

// rangeValidator picks the strong validator to replay as If-Range. A weak
// ETag must not be used (RFC 7233 §3.3): it can match two different bodies.
func rangeValidator(resp *http.Response) string {
	if et := resp.Header.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		return et
	}
	return resp.Header.Get("Last-Modified")
}

// contentRangeStart parses "bytes START-END/TOTAL" (RFC 7233). TOTAL may be
// "*", reported as -1. Anything else — including the "bytes */N" form a 416
// carries — is not a usable partial answer.
func contentRangeStart(v string) (start, total int64, ok bool) {
	v = strings.TrimSpace(v)
	if len(v) < 6 || !strings.EqualFold(v[:6], "bytes ") {
		return 0, -1, false
	}
	span, totalStr, found := strings.Cut(strings.TrimSpace(v[6:]), "/")
	if !found {
		return 0, -1, false
	}
	from, to, found := strings.Cut(span, "-")
	if !found {
		return 0, -1, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(from), 10, 64)
	if err != nil || start < 0 {
		return 0, -1, false
	}
	end, err := strconv.ParseInt(strings.TrimSpace(to), 10, 64)
	if err != nil || end < start {
		return 0, -1, false
	}
	total = -1
	if s := strings.TrimSpace(totalStr); s != "*" {
		if total, err = strconv.ParseInt(s, 10, 64); err != nil || total <= end {
			return 0, -1, false
		}
	}
	return start, total, true
}

func downloadProgressText(id string, have, total int64) string {
	if total > 0 {
		return fmt.Sprintf("[update:%s] %s / %s (%d%%)", id, humanBytes(have), humanBytes(total), have*100/total)
	}
	return fmt.Sprintf("[update:%s] %s received", id, humanBytes(have))
}

func humanBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n), -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// redactURLQuery keeps the scheme, host and path of a download URL and
// drops the userinfo, query and fragment. GitHub asset links redirect to
// signed CDN URLs whose query is a bearer credential, and both the log
// lines and the error text are shown in the UI and written to
// LogOutputPath — the same reason subscription failures name the host only.
func redactURLQuery(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unreadable URL)"
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	return u.String()
}

// downloadErrorText renders a transport error without the signed query.
// net/http reports the URL it was actually talking to, which after a
// redirect is the CDN link rather than the public asset URL.
func downloadErrorText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Op + " " + redactURLQuery(ue.URL) + ": " + ue.Err.Error()
	}
	return err.Error()
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
	// Authenticity: GitHub-published assets are checked against their
	// sha256 digest by verifyDigest before reaching this point. Sources
	// that publish none (jsDelivr) are trusted over HTTPS alone, and
	// identical content is rejected above.
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
	want := strings.ToLower(exeName)
	return extractZipEntry(zipData, exeName, func(base string) bool {
		return strings.ToLower(base) == want
	})
}

// extractZipEntry returns the first archive entry whose base name
// satisfies match. mihomo needs a prefix match: its Windows archive
// holds mihomo-windows-amd64-v1.exe (measured in the v1.19.33 zip), not
// mihomo.exe, and the exact name varies with arch and microarch level.
// The size guards live here so both callers get them.
func extractZipEntry(zipData []byte, exeName string, match func(base string) bool) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if !match(filepath.Base(f.Name)) {
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

// updateDataFile handles the geoip/geosite plain-file components. digest
// is the source's published sha256 ("" when it publishes none, as with
// the jsDelivr GeoIP copy).
func (a *App) updateDataFile(id, fileURL, destName, digest string) (*UpdatableComponent, error) {
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
	if err := a.verifyDigest(id, destName, digest, data); err != nil {
		a.updateLog(id, "error", "[update:"+id+"] ERROR: "+err.Error())
		return nil, err
	}
	if id == "geoip" {
		// The jsDelivr copy publishes no sha256, so verifyDigest above is a
		// no-op for this row: the bytes have to be proven to be a database
		// before they replace the one every country lookup reads. Writing
		// them anyway would degrade lookups silently and, with the revision
		// marker updated, leave no retry path.
		if err := validateMMDBBytes(data); err != nil {
			err = fmt.Errorf("geoip download rejected: %w", err)
			a.updateLog(id, "error", "[update:"+id+"] ERROR: "+err.Error())
			return nil, err
		}
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
		if installed {
			a.reloadGeoDB()
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

// reloadGeoDB re-opens the database after it changed on disk. A copy that
// will not open is worth a log line: every country lookup goes quiet from
// then on with no retry path, and nothing else in the UI hints that the
// file is present-but-broken.
func (a *App) reloadGeoDB() {
	if a.geo == nil {
		return
	}
	if err := a.geo.Reload(); err != nil {
		a.emitLog("update:geoip", "warn", "GeoIP database re-open failed: "+err.Error())
	}
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
	etag := a.remoteETag(fileURL)
	if etag == "" {
		return
	}
	if err := os.WriteFile(geoipMarker(), []byte(etag), 0o644); err != nil {
		a.updateLog("geoip", "warn", "[update:geoip] cannot record the installed revision: "+err.Error())
	}
}

// remoteETag is the source's revision marker for [url] ("" when unknown).
func (a *App) remoteETag(rawURL string) string {
	req, err := http.NewRequest("HEAD", rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "NekoGo/"+appVersionValue())
	resp, err := a.updaterCheckClient().Do(req)
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

func (a *App) waitConnectSettled(timeout time.Duration) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for a.IsConnectBusy() {
		select {
		case <-ticker.C:
		case <-timer.C:
			return errors.New("timed out waiting for the current connection attempt")
		}
	}
	return nil
}

func (a *App) profileUsesCoreImage(id string, profile Profile, settings Settings, dest string) bool {
	choice, err := resolveProfileCore(&profile, settings)
	if err != nil {
		return false
	}
	var path string
	switch id {
	case "singbox":
		if choice.Engine != "upstream" {
			return false
		}
		if choice.Profile.Core.IsSpoof() && settings.SpoofXray {
			return false
		}
		path, err = a.upstreamCoreInstallPath()
	case "xray":
		if choice.Profile.Core.IsSpoof() {
			if !settings.SpoofXray {
				return false
			}
		} else if !choice.Profile.Core.IsXray() {
			return false
		}
		path, err = a.xrayInstallPath()
	case "mihomo":
		if !choice.Profile.Core.IsMihomo() {
			return false
		}
		path, err = a.mihomoInstallPath()
	case "psiphon":
		if !aetherDriverProfile(&choice.Profile) && !(choice.Profile.Core.IsSpoof() && spoofChain(&choice.Profile, settings)) {
			return false
		}
		path, err = a.aetherInstallPath()
	default:
		return false
	}
	return err == nil && samePath(path, dest)
}

func (a *App) applyCoreImage(id, dest string, data []byte, force bool, install func() error) (installed bool, installErr, reconnectErr error) {
	if !force {
		if old, err := fileSHA256Hex(dest); err == nil && old == sha256Hex(data) {
			return false, nil, nil
		}
	}
	if a.cores == nil {
		return false, errors.New("core manager is not wired"), nil
	}
	a.beginCoreApply()
	defer a.endCoreApply()
	a.cores.beginImageApply()
	defer a.cores.endImageApply()

	connectingID := a.connectingProfile()
	reconnectID := ""
	if connectingID != "" {
		if profile, ok := a.store.GetProfile(connectingID); ok && a.profileUsesCoreImage(id, profile, a.store.GetSettings(), dest) {
			reconnectID = connectingID
			a.invalidateConnect()
			_ = a.cores.Stop()
		}
		if err := a.waitConnectSettled(coreApplyConnectWait); err != nil {
			return false, err, nil
		}
	}
	if err := a.cores.waitImageApplyProbes(coreApplyProbeWait); err != nil {
		return false, err, nil
	}

	_, activeID, running := a.cores.Status()
	targetActive := running && a.cores.usesImage(dest)
	if targetActive {
		reconnectID = activeID
		if err := a.disconnectCores(false); err != nil {
			return false, fmt.Errorf("disconnect before updating %s: %w", id, err), nil
		}
	}
	if err := a.cores.stopImageUsers(dest); err != nil {
		return false, fmt.Errorf("stop processes using %s: %w", filepath.Base(dest), err), nil
	}
	if a.cores.usesImage(dest) {
		return false, fmt.Errorf("%s is still in use; close the process using it and retry", filepath.Base(dest)), nil
	}

	installErr = install()
	if errors.Is(installErr, errAlreadyUpToDate) {
		installErr = nil
		installed = false
	} else if installErr == nil {
		installed = true
	}
	if reconnectID != "" {
		reconnectErr = coreApplyReconnect(a, reconnectID)
		if reconnectErr != nil {
			a.updateLog(id, "warn", fmt.Sprintf("[update:%s] installed, but reconnect failed: %v", id, reconnectErr))
		}
	}
	return installed, installErr, reconnectErr
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
	// resolveMihomo also accepts the release's own asset name
	// (mihomo-windows-amd64-v1.exe), so every mihomo* file is that row's.
	if strings.HasPrefix(base, "mihomo") {
		return "mihomo"
	}
	switch base {
	case strings.ToLower(coreExeName("sing-box")):
		return "singbox"
	case strings.ToLower(coreExeName("nekobox_core")):
		return "nekobox"
	case strings.ToLower(coreExeName("xray")):
		return "xray"
	case strings.ToLower(aetherExeName):
		return "psiphon"
	case strings.ToLower(torExeName):
		return "tor"
	}
	return ""
}

// updateOfficialSingBox installs the official CLI from SagerNet's
// stable release into the path resolveUpstreamCore reads.
func (a *App) updateOfficialSingBox() (*UpdatableComponent, error) {
	rel, err := a.githubLatest(officialSingBoxOwnerRepo)
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
	if owner := coreImageOwner(dest); owner != "" && owner != "singbox" {
		return nil, fmt.Errorf("sing-box update would overwrite %s, which belongs to the %s row; fix upstreamCorePath in settings", filepath.Base(dest), owner)
	}
	a.updateLog("singbox", "info", fmt.Sprintf("[update:singbox] installing %s from %s", asset.Name, rel.TagName))
	a.setUpdaterTotal("singbox", asset.Size)
	data, err := a.downloadFile("singbox", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+err.Error())
		return nil, err
	}
	if err := a.verifyDigest("singbox", asset.Name, asset.Digest, data); err != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+err.Error())
		return nil, err
	}
	data, err = extractExeFromZip(data, coreExeName("sing-box"))
	if err != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+err.Error())
		return nil, err
	}
	installed, installErr, reconnectErr := a.applyCoreImage("singbox", dest, data, false, func() error {
		return a.installExeWithBackup("singbox", dest, data, "version")
	})
	if installErr != nil {
		a.updateLog("singbox", "error", "[update:singbox] ERROR: "+installErr.Error())
		return nil, installErr
	}
	if !installed {
		a.updateLog("singbox", "info", "[update:singbox] already up to date (identical binary)")
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
	if !installed {
		c.Notes = "Already up to date"
	}
	if reconnectErr != nil {
		c.Notes = "Updated, but reconnect failed: " + reconnectErr.Error()
	}
	return &c, nil
}

// mihomoInstallPath is where mihomo.exe belongs, mirroring
// resolveMihomo: the dedicated MihomoPath override (a directory gets
// mihomo.exe inside it, a file is updated in place), otherwise the
// binary already resolved from bin\ / the exe folder, and bin\ next to
// nekogo.exe as the default destination. Settings.CorePath and
// UpstreamCorePath belong to other engines and are never read here.
func (a *App) mihomoInstallPath() (string, error) {
	if a.store != nil {
		if p := strings.TrimSpace(a.store.GetSettings().MihomoPath); p != "" {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return filepath.Join(p, coreExeName("mihomo")), nil
			}
			return p, nil
		}
	}
	if cur := a.currentCorePath("mihomo"); cur != "" {
		return cur, nil
	}
	return filepath.Join(coreExeDir(), coreExeName("mihomo")), nil
}

// updateMihomo installs the Clash Meta engine from MetaCubeX's stable
// release. The archive holds mihomo-windows-amd64-v1.exe (measured in
// the v1.19.33 zip), so the entry is matched by prefix and written to
// the resolved path — mihomo.exe in the shipped layout, the name the
// resolver prefers and build.bat stages.
func (a *App) updateMihomo() (*UpdatableComponent, error) {
	rel, err := a.githubLatest(mihomoOwnerRepo)
	if err != nil {
		return nil, err
	}
	asset := mihomoAsset(rel.Assets, rel.TagName)
	if asset == nil {
		return nil, fmt.Errorf("no mihomo build for %s/%s in %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}
	dest, err := a.mihomoInstallPath()
	if err != nil {
		return nil, err
	}
	if owner := coreImageOwner(dest); owner != "" && owner != "mihomo" {
		return nil, fmt.Errorf("mihomo update would overwrite %s, which belongs to the %s row; fix mihomoPath in settings", filepath.Base(dest), owner)
	}
	a.updateLog("mihomo", "info", fmt.Sprintf("[update:mihomo] installing %s from %s", asset.Name, rel.TagName))
	a.setUpdaterTotal("mihomo", asset.Size)
	data, err := a.downloadFile("mihomo", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("mihomo", "error", "[update:mihomo] ERROR: "+err.Error())
		return nil, err
	}
	if err := a.verifyDigest("mihomo", asset.Name, asset.Digest, data); err != nil {
		a.updateLog("mihomo", "error", "[update:mihomo] ERROR: "+err.Error())
		return nil, err
	}
	data, err = extractZipEntry(data, coreExeName("mihomo"), func(base string) bool {
		n := strings.ToLower(base)
		return strings.HasPrefix(n, "mihomo") && strings.HasSuffix(n, ".exe")
	})
	if err != nil {
		a.updateLog("mihomo", "error", "[update:mihomo] ERROR: "+err.Error())
		return nil, err
	}
	installed, installErr, reconnectErr := a.applyCoreImage("mihomo", dest, data, false, func() error {
		return a.installExeWithBackup("mihomo", dest, data, "-v")
	})
	if installErr != nil {
		a.updateLog("mihomo", "error", "[update:mihomo] ERROR: "+installErr.Error())
		return nil, installErr
	}
	if !installed {
		a.updateLog("mihomo", "info", "[update:mihomo] already up to date (identical binary)")
	}
	a.invalidateCoreVersion("mihomo")
	c := UpdatableComponent{ID: "mihomo", Latest: rel.TagName, UpdateAvail: false}
	for _, base := range a.localUpdatables() {
		if base.ID == "mihomo" {
			c = base
			break
		}
	}
	c.Latest = rel.TagName
	c.UpdateAvail = false
	c.Notes = ""
	if !installed {
		c.Notes = "Already up to date"
	}
	if reconnectErr != nil {
		c.Notes = "Updated, but reconnect failed: " + reconnectErr.Error()
	}
	return &c, nil
}

// xrayInstallPath is where xray.exe belongs, mirroring resolveXray: the
// dedicated XrayPath override (a directory gets xray.exe inside it, a
// file is updated in place), otherwise the binary a.xray already
// resolved, and bin\ next to nekogo.exe as the default destination.
// resolveXray only accepts an override that EXISTS on disk (it errors
// on a missing one), so an install into a configured-but-unpopulated
// directory would land in bin\ instead of where the user pointed the
// setting — the destination is computed here rather than read from the
// manager. Settings.CorePath and UpstreamCorePath are other engines'
// overrides and are never read here.
func (a *App) xrayInstallPath() (string, error) {
	if a.store != nil {
		if p := strings.TrimSpace(a.store.GetSettings().XrayPath); p != "" {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return filepath.Join(p, coreExeName("xray")), nil
			}
			return p, nil
		}
	}
	if cur := a.currentCorePath("xray"); cur != "" {
		return cur, nil
	}
	return filepath.Join(coreExeDir(), coreExeName("xray")), nil
}

// updateXray installs the Xray engine from XTLS/Xray-core's own
// release. The archive holds wintun.dll, geoip.dat and README.md beside
// the binary (measured in v26.11.9), so only xray.exe is extracted, and
// it goes to the path resolveXray reads.
func (a *App) updateXray() (*UpdatableComponent, error) {
	rel, err := a.githubLatest(xrayOwnerRepo)
	if err != nil {
		return nil, err
	}
	asset := xrayAsset(rel.Assets)
	if asset == nil {
		return nil, fmt.Errorf("no xray build for %s/%s in %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}
	dest, err := a.xrayInstallPath()
	if err != nil {
		return nil, err
	}
	if owner := coreImageOwner(dest); owner != "" && owner != "xray" {
		return nil, fmt.Errorf("xray update would overwrite %s, which belongs to the %s row; fix xrayPath in settings", filepath.Base(dest), owner)
	}
	a.updateLog("xray", "info", fmt.Sprintf("[update:xray] installing %s from %s", asset.Name, rel.TagName))
	a.setUpdaterTotal("xray", asset.Size)
	data, err := a.downloadFile("xray", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("xray", "error", "[update:xray] ERROR: "+err.Error())
		return nil, err
	}
	if err := a.verifyDigest("xray", asset.Name, asset.Digest, data); err != nil {
		a.updateLog("xray", "error", "[update:xray] ERROR: "+err.Error())
		return nil, err
	}
	data, err = extractExeFromZip(data, coreExeName("xray"))
	if err != nil {
		a.updateLog("xray", "error", "[update:xray] ERROR: "+err.Error())
		return nil, err
	}
	installed, installErr, reconnectErr := a.applyCoreImage("xray", dest, data, false, func() error {
		return a.installExeWithBackup("xray", dest, data, "version")
	})
	if installErr != nil {
		a.updateLog("xray", "error", "[update:xray] ERROR: "+installErr.Error())
		return nil, installErr
	}
	if !installed {
		a.updateLog("xray", "info", "[update:xray] already up to date (identical binary)")
	}
	a.invalidateCoreVersion("xray")
	c := UpdatableComponent{ID: "xray", Latest: rel.TagName, UpdateAvail: false}
	for _, base := range a.localUpdatables() {
		if base.ID == "xray" {
			c = base
			break
		}
	}
	c.Latest = rel.TagName
	c.UpdateAvail = false
	c.Notes = ""
	if !installed {
		c.Notes = "Already up to date"
	}
	if reconnectErr != nil {
		c.Notes = "Updated, but reconnect failed: " + reconnectErr.Error()
	}
	return &c, nil
}

// aetherInstallPath is where aether.exe belongs, mirroring
// resolveAetherExe: the dedicated AetherPath override (a directory gets
// aether.exe inside it, a file is updated in place), otherwise the binary
// already resolved from bin\ / the exe folder, and bin\ next to
// nekogo.exe as the default destination. resolveAetherExe only accepts an
// override that EXISTS, so the destination is computed here rather than
// read back from the resolver: an install into a configured-but-empty
// folder has to land where the setting points, which is also the only way
// a machine with no engine yet gets one.
func (a *App) aetherInstallPath() (string, error) {
	if a.store != nil {
		if p := strings.TrimSpace(a.store.GetSettings().AetherPath); p != "" {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return filepath.Join(p, aetherExeName), nil
			}
			return p, nil
		}
	}
	if cur := a.currentCorePath("psiphon"); cur != "" {
		return cur, nil
	}
	return filepath.Join(coreExeDir(), aetherExeName), nil
}

// updatePsiphon installs the Aether core from the release that
// publishes aether.exe. The archive's aether.exe goes to the resolved
// path with the usual backup + smoke verify + rollback, and the pt\
// transports land beside it: an engine without them starts Tor and then
// fails to spawn a transport for every bridge line, which reads as a
// bootstrap stall with nothing on screen naming the missing file.
func (a *App) updatePsiphon() (*UpdatableComponent, error) {
	rel, err := a.githubLatest(aetherOwnerRepo)
	if err != nil {
		return nil, err
	}
	asset := aetherAsset(rel.Assets)
	if asset == nil {
		return nil, fmt.Errorf("no aether build for %s/%s in %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}
	dest, err := a.aetherInstallPath()
	if err != nil {
		return nil, err
	}
	if owner := coreImageOwner(dest); owner != "" && owner != "psiphon" {
		return nil, fmt.Errorf("psiphon update would overwrite %s, which belongs to the %s row; fix aetherPath in settings", filepath.Base(dest), owner)
	}
	a.updateLog("psiphon", "info", fmt.Sprintf("[update:psiphon] installing %s from %s", asset.Name, rel.TagName))
	a.setUpdaterTotal("psiphon", asset.Size)
	data, err := a.downloadFile("psiphon", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("psiphon", "error", "[update:psiphon] ERROR: "+err.Error())
		return nil, err
	}
	// The digest covers the ARCHIVE, so it is checked before anything is
	// unpacked or written: a tampered zip must never reach the extractor.
	if err := a.verifyDigest("psiphon", asset.Name, asset.Digest, data); err != nil {
		a.updateLog("psiphon", "error", "[update:psiphon] ERROR: "+err.Error())
		return nil, err
	}
	exe, err := extractExeFromZip(data, aetherExeName)
	if err != nil {
		a.updateLog("psiphon", "error", "[update:psiphon] ERROR: "+err.Error())
		return nil, err
	}
	transportsChanged := false
	for _, name := range aetherZipTransports {
		body, err := extractExeFromZip(data, name)
		if err != nil {
			continue
		}
		current, err := fileSHA256Hex(filepath.Join(aetherPtDir(dest), name))
		if err != nil || current != sha256Hex(body) {
			transportsChanged = true
			break
		}
	}
	installed, installErr, reconnectErr := a.applyCoreImage("psiphon", dest, exe, transportsChanged, func() error {
		if err := a.installExeWithBackup("psiphon", dest, exe, "-v"); err != nil &&
			!errors.Is(err, errAlreadyUpToDate) {
			return err
		}
		// The transports beside the engine, written only after it verified. A
		// release that ships fewer is not a failed install — the row reports
		// what is missing the next time it is read, which is where a user
		// looks, and the engine it did get is the one that runs.
		for _, name := range aetherZipTransports {
			body, err := extractExeFromZip(data, name)
			if err != nil {
				a.updateLog("psiphon", "warn", fmt.Sprintf("[update:psiphon] %s is not in %s — Tor/Psiphon transports need it", name, asset.Name))
				continue
			}
			if err := a.installFile("psiphon", filepath.Join(aetherPtDir(dest), name), body); err != nil &&
				!errors.Is(err, errAlreadyUpToDate) {
				return err
			}
		}
		return nil
	})
	if installErr != nil {
		a.updateLog("psiphon", "error", "[update:psiphon] ERROR: "+installErr.Error())
		return nil, installErr
	}
	if !installed {
		a.updateLog("psiphon", "info", "[update:psiphon] already up to date (identical binary)")
	}
	// The version cache is keyed on path, and an in-place replace keeps
	// the path: drop the stale line so Current refreshes at once.
	a.invalidateCoreVersion("psiphon")
	c := UpdatableComponent{ID: "psiphon", Latest: rel.TagName}
	for _, base := range a.localUpdatables() {
		if base.ID == "psiphon" {
			c = base
			break
		}
	}
	c.Latest = rel.TagName
	c.UpdateAvail = false
	if !installed {
		c.Notes = "Already up to date"
	}
	if reconnectErr != nil {
		c.Notes = "Updated, but reconnect failed: " + reconnectErr.Error()
	}
	return &c, nil
}

func selfUpdateUnusedPath(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	// The helper requires a nonexisting status path and repeats this check.
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) updateNekoGo() (*UpdatableComponent, error) {
	rel, err := a.githubFirstRelease(forkOwnerRepo)
	if err != nil {
		return nil, err
	}
	asset := pickNekoGoAsset(rel.Assets)
	if asset == nil {
		return nil, fmt.Errorf("nekogo.exe not found in %s", rel.TagName)
	}
	target, err := nekogoExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve NekoGo executable: %w", err)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("resolve NekoGo executable path: %w", err)
	}
	if !strings.EqualFold(filepath.Base(target), "nekogo.exe") {
		return nil, fmt.Errorf("self-update target must be named nekogo.exe, got %s", filepath.Base(target))
	}
	c := UpdatableComponent{ID: "nekogo", Name: "NekoGo", Current: appVersionValue(), Latest: rel.TagName}
	a.setUpdaterTotal("nekogo", asset.Size)
	a.updateLog("nekogo", "info", fmt.Sprintf("[update:nekogo] downloading %s from %s", asset.Name, rel.TagName))
	data, err := a.downloadFile("nekogo", asset.BrowserDownloadURL)
	if err != nil {
		a.updateLog("nekogo", "error", "[update:nekogo] ERROR: "+err.Error())
		return nil, err
	}
	if err := a.verifyDigest("nekogo", asset.Name, asset.Digest, data); err != nil {
		a.updateLog("nekogo", "error", "[update:nekogo] ERROR: "+err.Error())
		return nil, err
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("read running NekoGo executable: %w", err)
	}
	if bytes.Equal(current, data) {
		c.Notes = "Already up to date"
		return &c, nil
	}
	if err := a.updaterDownloadContext("nekogo").Err(); err != nil {
		return nil, err
	}

	dir := filepath.Dir(target)
	sourceFile, err := os.CreateTemp(dir, ".nekogo-update-*.exe")
	if err != nil {
		return nil, fmt.Errorf("stage NekoGo update: %w", err)
	}
	source := sourceFile.Name()
	readyPath, resultPath := "", ""
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(source)
			if readyPath != "" {
				_ = os.Remove(readyPath)
			}
			if resultPath != "" {
				_ = os.Remove(resultPath)
			}
		}
	}()
	if _, err := sourceFile.Write(data); err != nil {
		_ = sourceFile.Close()
		return nil, fmt.Errorf("stage NekoGo update: %w", err)
	}
	if err := sourceFile.Sync(); err != nil {
		_ = sourceFile.Close()
		return nil, fmt.Errorf("sync NekoGo update: %w", err)
	}
	if err := sourceFile.Close(); err != nil {
		return nil, fmt.Errorf("close NekoGo update: %w", err)
	}
	if err := os.Chmod(source, 0o755); err != nil {
		return nil, fmt.Errorf("prepare NekoGo update: %w", err)
	}
	readyPath, err = selfUpdateUnusedPath(dir, ".nekogo-update-ready-*.json")
	if err != nil {
		return nil, fmt.Errorf("reserve self-update readiness path: %w", err)
	}
	resultPath, err = selfUpdateUnusedPath(dir, ".nekogo-update-result-*.json")
	if err != nil {
		return nil, fmt.Errorf("reserve self-update result path: %w", err)
	}

	connectID := ""
	if a.cores != nil {
		_, id, running := a.cores.Status()
		if running {
			connectID = id
		}
	}
	if connectID == "" {
		connectID = a.connectingProfile()
	}
	plan := selfUpdatePlan{
		Version:      rel.TagName,
		Source:       source,
		SourceSize:   int64(len(data)),
		SourceSHA256: sha256Hex(data),
		Target:       target,
		ReadyPath:    readyPath,
		ResultPath:   resultPath,
		ParentPID:    os.Getpid(),
		ConnectID:    connectID,
	}
	cmd, err := startNekoGoUpdateHelper(plan)
	if err != nil {
		a.updateLog("nekogo", "error", "[update:nekogo] ERROR: "+err.Error())
		return nil, err
	}
	if err := a.updaterDownloadContext("nekogo").Err(); err != nil {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return nil, err
	}
	committed = true
	c.UpdateAvail = true
	c.Notes = "NekoGo will restart to install the update."
	// os.Exit bypasses UpdateComponent's deferred state flush.
	a.finishUpdaterDownload("nekogo", &c, nil)
	a.Shutdown()
	selfUpdateExit(0)
	return &c, nil
}

// updatePsiphonServers downloads the signed server list and puts it in
// place of the one in use only after Psiphon's signature and the minimum
// entry count check out. No core has to stop: the next start unpacks the
// new list beside the identity files. A failed or invalid download leaves
// the list in use untouched.
func (a *App) updatePsiphonServers() (*UpdatableComponent, error) {
	active := psiphonActiveListPath()
	_, inUse, _ := psiphonServersLocal()
	source := psiphonServersSource()
	a.updateLog("psiphon-servers", "info", "[update:psiphon-servers] checking "+redactURLQuery(source))

	data, published, notModified, err := a.downloadPsiphonList("psiphon-servers", source, inUse)
	if err != nil {
		a.updateLog("psiphon-servers", "error", "[update:psiphon-servers] ERROR: "+err.Error())
		return nil, err
	}
	if notModified || (!inUse.IsZero() && !published.IsZero() && !published.After(inUse)) {
		c := psiphonServersRow()
		c.Notes = "Already up to date"
		c.LastCheck = time.Now().Format(time.RFC3339)
		a.updateLog("psiphon-servers", "info", "[update:psiphon-servers] already up to date")
		return &c, nil
	}
	entries, err := psiphonVerifyList(data, psiphonListKey)
	if err != nil {
		err = fmt.Errorf("the download is not Psiphon's signed server list (%w); the list in use stays", err)
		a.updateLog("psiphon-servers", "error", "[update:psiphon-servers] ERROR: "+err.Error())
		return nil, err
	}
	if published.IsZero() {
		published = time.Now()
	}
	if err := a.installFile("psiphon-servers", active, data); err != nil && !errors.Is(err, errAlreadyUpToDate) {
		a.updateLog("psiphon-servers", "error", "[update:psiphon-servers] ERROR: "+err.Error())
		return nil, err
	}
	// The list in use is as new as its publication time, not as the moment
	// the bytes were written: the next check compares the same clock.
	if err := os.Chtimes(active, published, published); err != nil {
		a.updateLog("psiphon-servers", "warn", "[update:psiphon-servers] cannot record the list's publication time: "+err.Error())
	}
	c := psiphonServersRow()
	c.Latest = published.Format("2006-01-02")
	c.UpdateAvail = false
	c.Notes = fmt.Sprintf("Installed %d server entries published %s", entries, published.Format("2006-01-02"))
	c.LastCheck = time.Now().Format(time.RFC3339)
	a.updateLog("psiphon-servers", "info", fmt.Sprintf("[update:psiphon-servers] installed %d server entries published %s", entries, published.Format("2006-01-02")))
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
	case "mihomo":
		if a.store != nil {
			if p, err := resolveMihomo(a.store.GetSettings()); err == nil {
				return p
			}
		}
	case "tor":
		if a.store != nil {
			if p, err := resolveTorExe(a.store.GetSettings()); err == nil {
				return p
			}
		}
	case "psiphon":
		if a.aetherMgr != nil && a.store != nil {
			if p, err := a.aetherMgr.resolveAetherExe(a.store.GetSettings()); err == nil {
				return p
			}
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
	case "mihomo":
		a.mihomoVer.mu.Lock()
		a.mihomoVer.version = ""
		a.mihomoVer.mu.Unlock()
	case "tor":
		a.torVer.mu.Lock()
		a.torVer.version = ""
		a.torVer.mu.Unlock()
	case "psiphon":
		a.aetherVer.mu.Lock()
		a.aetherVer.version = ""
		a.aetherVer.mu.Unlock()
	}
}

// UpdateComponent downloads and installs one component by ID, returning
// the refreshed row.
func (a *App) UpdateComponent(id string) (row *UpdatableComponent, err error) {
	a.updaterOpMu.Lock()
	defer a.updaterOpMu.Unlock()
	a.beginUpdaterDownload(id)
	defer func() { a.finishUpdaterDownload(id, row, err) }()
	switch id {
	case "geoip":
		url := updateBase("jsdelivr", "https://cdn.jsdelivr.net") + "/gh/Loyalsoldier/geoip@release/Country.mmdb"
		return a.updateDataFile(id, url, "GeoLite2-Country.mmdb", "")
	case "geosite":
		rel, err := a.githubLatest("Loyalsoldier/v2ray-rules-dat")
		if err != nil {
			return nil, err
		}
		asset := pickAssetExact(rel.Assets, "geosite.dat")
		if asset == nil {
			return nil, fmt.Errorf("geosite.dat not found in %s", rel.TagName)
		}
		a.setUpdaterTotal(id, asset.Size)
		c, err := a.updateDataFile(id, asset.BrowserDownloadURL, "geosite.dat", asset.Digest)
		if err != nil {
			return nil, err
		}
		_ = os.WriteFile(geositeMarker(), []byte(rel.TagName), 0o644)
		c.Latest = rel.TagName
		return c, nil
	case "singbox":
		return a.updateOfficialSingBox()
	case "xray":
		return a.updateXray()
	case "mihomo":
		return a.updateMihomo()
	case "psiphon":
		return a.updatePsiphon()
	case "psiphon-servers":
		return a.updatePsiphonServers()
	case "nekogo":
		return a.updateNekoGo()
	default:
		return nil, fmt.Errorf("unknown component %q", id)
	}
}
