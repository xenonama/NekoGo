package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// appVersion is overridden at link time:
// go build -ldflags="-X main.appVersion=1.2.3"
var appVersion = "5.11.28.3-fork.1-dev"

// App is the Wails bound service. Every public method is callable from the
// Vue frontend.
//
// NOTE (Part 6 P3-1): the following bound methods have no frontend
// callers today and are intentionally kept — Shutdown and
// ShouldHideOnClose serve main.go/tray logic, AttachWailsApp serves the
// Go runtime wiring (it takes a non-serializable *application.App and is
// Go-only in practice). The next audit should not flag them as dead.
type App struct {
	mu      sync.Mutex
	store   *ConfigStore
	cores   *CoreManager
	singbox *SingBoxManager
	xray    *XrayManager
	// spoofMgr owns the sni-spoof.exe child process (CoreSpoof).
	// Wired into cores via SetSpoofManager; the SNI tab drives it
	// directly through the Spoof* bound methods below.
	spoofMgr *SpoofManager
	// aetherMgr owns the aether.exe engine for aether profiles.
	// Wired into cores via SetAetherManager: a connect on an aether
	// profile starts the engine first, then the main core dials its
	// SOCKS listener.
	aetherMgr *AetherManager
	// psiphonMgr owns the second aether.exe child of the SNI spoof →
	// Psiphon chain. Wired into cores via SetPsiphonManager: it is a
	// member of the CoreSpoof group and stops with it.
	psiphonMgr *AetherManager
	// upstreamMgr owns the official sing-box.exe CLI child process when
	// Settings.CoreEngine == "upstream". Wired into cores via
	// SetUpstreamSingBoxManager.
	upstreamMgr *UpstreamSingBoxManager
	tray        trayController
	// trayBuilder rebuilds the tray with identical menu+callbacks;
	// set by main.go, used when closeToTray flips false->true after
	// a launch that skipped tray creation. Nil in tests unless set.
	trayBuilder func() trayController
	onShow      func()
	logCh       chan LogEntry
	logBuf      []LogEntry
	logMu       sync.RWMutex
	logSeq      uint64
	quitting    bool
	wailsApp    *application.App
	// GeoIP country detector (Stage 3; degrades to empty when the DB
	// is missing — never blocks, never fails saves).
	geo *GeoIP
	// Cached "<exe> version" first lines for the About diagnostics card.
	// Each cache is keyed on the configured binary path (P3-15): a
	// Settings corePath change invalidates the stale value.
	coreVer     versionCache
	upstreamVer versionCache
	xrayVer     versionCache
	// orphanScanDone is closed when the startup orphan scan finished.
	// The scan runs in the background (it spawns wmic + tasklist, ~420
	// ms measured) so the window appears immediately; Connect waits for
	// it so a stray core can never hold a port into the first Connect.
	orphanScanDone chan struct{}
	// lastErrMu guards lastErr: the most recent startup/tray-level
	// error (auto-connect, tray connect/disconnect). Empty when none.
	// Surfaced to the UI via LastError so a failed silent connect is
	// visible instead of looking like "no auto-connect configured".
	lastErrMu sync.Mutex
	lastErr   string
	// connMu guards connBusy/connEpoch: shared "a Connect is in flight"
	// state covering auto-connect and user/tray Connect alike
	// (integration BUG-001/002). The frontend polls it via Status and
	// IsConnectBusy to disable Connect/Disconnect while busy; tray
	// handlers skip while busy; Stop aborts the in-flight probe via the
	// CoreManager epoch.
	connMu    sync.Mutex
	connBusy  bool
	connEpoch uint64
	// autoProxyOn tracks whether THIS session ensured the OS proxy in
	// Connect (GUI-owned, any core). Disconnect/Shutdown only revert
	// what we ensured — never a proxy the user turned on manually via
	// Console -> System. Guarded by connMu.
	autoProxyOn bool
	// sysProxyHook overrides SetSystemProxy/ClearSystemProxy in tests
	// with a fake that records enable/disable calls (nil in
	// production). Unexported so the Wails bindings surface is
	// unchanged.
	sysProxyHook func(enable bool) error
	// sysStateHook overrides the OS proxy state read in tests with a
	// fake reporting (enabled, server) (nil in production, which
	// reads the real OS switch). Lets ensureSystemProxy prove its
	// skip-vs-repair branches without touching the registry.
	// Unexported, no bindings change.
	sysStateHook func() (bool, string, error)
	// coreStarter overrides CoreManager.Start in tests (nil in
	// production): Connect exercises the full auto-proxy block without
	// spawning a real core process. Unexported, no bindings change.
	coreStarter func(p *Profile, s Settings) error
	// refreshMu guards refreshCancel: at most one RefreshAllCountries
	// runs at a time, and CancelRefreshCountries aborts it
	// (integration BUG-003). refreshDone/refreshTotal are progress
	// counters read by RefreshCountriesProgress (atomics: workers bump
	// them without holding refreshMu).
	refreshMu     sync.Mutex
	refreshCancel context.CancelFunc
	refreshDone   atomic.Int64
	refreshTotal  atomic.Int64
	// aetherScanMu guards aetherScanCancel: at most one endpoint scan
	// runs at a time, and AetherScanCancel aborts it.
	aetherScanMu     sync.Mutex
	aetherScanCancel context.CancelFunc
	// aetherScanStatusMu guards aetherScanStatus: the live scan
	// progress polled by the Aether tab.
	aetherScanStatusMu sync.Mutex
	aetherScanStatus   AetherScanStatus
}

// noteError records a startup/tray-level error for LastError.
func (a *App) noteError(msg string) {
	a.lastErrMu.Lock()
	defer a.lastErrMu.Unlock()
	a.lastErr = msg
}

// clearError drops any recorded startup/tray-level error.
func (a *App) clearError() {
	a.lastErrMu.Lock()
	defer a.lastErrMu.Unlock()
	a.lastErr = ""
}

// noteFault reports an unexpected engine death (CoreManager's
// EngineExit callback): log it, surface it through LastError/Status, and
// revert the auto-enabled system proxy so traffic is not pointed at a
// closed port. The group teardown itself belongs to CoreManager.
func (a *App) noteFault(ev EngineExit) {
	msg := ev.Summary()
	a.emitLog("gui", "error", msg)
	a.noteError(msg)
	if a.getAutoProxy() {
		if err := a.ClearSystemProxy(); err != nil {
			a.emitLog("gui", "warn", fmt.Sprintf("auto system proxy revert after engine exit: %v", err))
		}
		a.setAutoProxy(false)
	}
	a.refreshTray()
}

// LastError returns the last startup/tray-level error message
// (empty when none). Bound for the frontend startup banner.
func (a *App) LastError() string {
	a.lastErrMu.Lock()
	defer a.lastErrMu.Unlock()
	return a.lastErr
}

// NewApp wires config store, both core managers, and the log bus.
func NewApp(store *ConfigStore, logCh chan LogEntry) *App {
	a := &App{store: store, logCh: logCh}
	a.singbox = NewSingBoxManager(store.GetSettings().CorePath, logCh)
	a.xray = NewXrayManager(store.GetSettings().XrayPath, "", logCh)
	a.cores = NewCoreManager(a.singbox, a.xray)
	a.spoofMgr = NewSpoofManager(logCh)
	a.cores.SetSpoofManager(a.spoofMgr)
	a.aetherMgr = NewAetherManager(logCh)
	a.cores.SetAetherManager(a.aetherMgr)
	a.psiphonMgr = NewPsiphonManager(logCh)
	a.cores.SetPsiphonManager(a.psiphonMgr)
	a.upstreamMgr = NewUpstreamSingBoxManager(logCh)
	a.cores.SetUpstreamSingBoxManager(a.upstreamMgr)
	a.cores.SetFaultHandler(a.noteFault)
	a.geo = NewGeoIP(store.GetSettings().GeoIPDBPath)
	go a.pumpLogs()
	return a
}

// ServiceName names the service in Wails logs.
func (a *App) ServiceName() string { return "NekoGo" }

// setOrphanScanDone hands the app the channel the startup orphan scan
// closes when it is done (nil = no scan pending).
func (a *App) setOrphanScanDone(ch chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.orphanScanDone = ch
}

// waitOrphanScan blocks until the startup orphan scan finished (or 3s
// pass, so a hung scan can never block a Connect). No-op when no scan
// is pending.
func (a *App) waitOrphanScan() {
	a.mu.Lock()
	ch := a.orphanScanDone
	a.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
	}
}

func (a *App) pumpLogs() {
	for e := range a.logCh {
		a.logMu.Lock()
		a.logSeq++
		e.ID = a.logSeq
		a.logBuf = append(a.logBuf, e)
		if len(a.logBuf) > 2000 {
			a.logBuf = a.logBuf[len(a.logBuf)-2000:]
		}
		a.logMu.Unlock()
		// Advanced Phase H: mirror to the log file when configured
		// (in-memory buffer behavior is unchanged).
		appendLogFile(a.store.GetSettings().LogOutputPath, e)
	}
}

func (a *App) emitLog(source, level, msg string) {
	e := LogEntry{Source: source, Level: level, Message: msg, Time: time.Now().Format(time.RFC3339)}
	a.logMu.Lock()
	a.logSeq++
	e.ID = a.logSeq
	a.logBuf = append(a.logBuf, e)
	if len(a.logBuf) > 2000 {
		a.logBuf = a.logBuf[len(a.logBuf)-2000:]
	}
	a.logMu.Unlock()
	appendLogFile(a.store.GetSettings().LogOutputPath, e)
}

// AttachWailsApp stores the Wails application handle so OpenURL can reach
// the runtime browser API. Called once from main.go after application.New.
func (a *App) AttachWailsApp(app *application.App) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.wailsApp = app
}

// attachTray links the tray controller created in main.go. A nil
// controller is valid (launch with closeToTray=false skips creation;
// the runtime toggle rebuilds it via trayBuilder). Unexported on
// purpose: a trayController interface parameter cannot be expressed
// in TS bindings, and the frontend never calls this (main.go does).
func (a *App) attachTray(t trayController) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tray = t
}

// setTrayBuilder stores the tray rebuild func under a.mu (BUG-023):
// SetCloseToTray reads it under a.mu, so a plain startup assignment
// races a toggle fired during launch.
func (a *App) setTrayBuilder(b func() trayController) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trayBuilder = b
}

// setupTrayIfEnabled builds the tray when closeToTray is on and logs
// the skip otherwise, so a fresh launch with false shows no icon at
// all (no create-then-hide flash).
func setupTrayIfEnabled(appSvc *App, build func() trayController) trayController {
	if appSvc.ShouldHideOnClose() {
		return build()
	}
	appSvc.emitLog("tray", "info", "[tray] skipped: closeToTray=false")
	return nil
}

func (a *App) refreshTray() {
	a.mu.Lock()
	t := a.tray
	a.mu.Unlock()
	if t == nil {
		return
	}
	core, id, running := a.cores.Status()
	name := ""
	if id != "" {
		if p, ok := a.store.GetProfile(id); ok {
			name = p.Name
		}
	}
	t.SetStatus(running, name, core)
	// Throne parity: the Servers submenu follows the store (active
	// checked, capped). The interface stays Show/Hide/SetStatus so
	// headless fakes keep working; only the real controller refreshes.
	if rc, ok := t.(*TrayController); ok {
		all := a.store.ListProfiles()
		entries := make([]TrayServer, 0, len(all))
		for _, p := range all {
			entries = append(entries, TrayServer{ID: p.ID, Name: p.Name})
		}
		active := ""
		if running {
			active = id
		} else {
			active = a.store.GetSettings().ActiveProfileID
		}
		rc.RefreshServers(entries, active)
	}
}

// tryBeginConnect claims the connect-in-flight slot atomically. A
// second Connect (double click, tray plus UI, auto-connect racing a
// click) must be refused, not run in parallel: both Starts bump the
// CoreManager epoch and the loser's abort path tears down whatever is
// current — including the winner's freshly started core.
func (a *App) tryBeginConnect() bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.connBusy {
		return false
	}
	a.connBusy = true
	a.connEpoch++
	return true
}

// setConnectBusy marks a Connect as in flight (true) or settled
// (false), returning the current connect epoch. Callers must clear the
// flag (defer) so the UI never sticks on busy.
func (a *App) setConnectBusy(b bool) uint64 {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.connBusy = b
	if b {
		a.connEpoch++
	}
	return a.connEpoch
}

// IsConnectBusy reports whether a Connect (user, tray, or auto-connect)
// is currently probing. Bound for the frontend and tray handlers.
func (a *App) IsConnectBusy() bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	return a.connBusy
}

// --- profiles ---

// ListProfiles returns all stored profiles.
func (a *App) ListProfiles() []Profile { return a.store.ListProfiles() }

// singBoxProtocols are routable without rawOutbound: they mirror the
// mapProfileToSingBoxOutbound switch plus "direct", which
// BuildSingBoxConfig handles by skipping the proxy outbound, and
// "aether", which runs its own engine process and dials it via SOCKS.
var singBoxProtocols = map[string]bool{
	"shadowsocks": true, "vmess": true, "vless": true, "trojan": true,
	"socks": true, "http": true, "direct": true, "aether": true,
}

// xrayProtocols mirror the mapProfileToXrayOutbound switch.
var xrayProtocols = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
	"aether": true,
}

// validateProfileForSave rejects profiles the builders cannot route,
// so bad input fails fast at save instead of slowly at Connect/Test
// (integration BUG-007 port, BUG-011 protocol). Profiles carrying a raw
// outbound bypass both checks: the port field is unused by raw
// passthrough and the protocol may be anything the raw JSON describes
// (a rawOutbound-only profile with empty address/port is backend-legal).
func validateProfileForSave(p Profile) error {
	if p.RawOutbound == "" {
		// Aether two-hop profiles (gool/mim/wg-over-masque) name outer
		// and inner hops and no single endpoint: port unused.
		if !(p.Protocol == "aether" && aetherIsTwoHops(aetherEnumDefault(aetherProtocols, p.Aether.Protocol, "wg"))) {
			if p.Port < 1 || p.Port > 65535 {
				return fmt.Errorf("port must be 1..65535 (got %d)", p.Port)
			}
		}
		// CoreSpoof passes any protocol through at save time: the
		// spoof engine only accepts trojan/vless, but that is
		// enforced at Start (clear error), not at save, so the JSON
		// editor never blocks the user's draft.
		if !p.Core.IsSpoof() {
			supported := singBoxProtocols
			coreName := "sing-box"
			if p.Core.IsXray() {
				supported, coreName = xrayProtocols, "xray"
			}
			if !supported[p.Protocol] {
				return fmt.Errorf("protocol %q needs rawOutbound JSON for %s", p.Protocol, coreName)
			}
		}
		// Mux/fragment/fingerprint ride the mapped builders (raw JSON
		// bypasses them, so raw profiles skip these checks — the fields
		// are ignored there, not rendered).
		if err := validateMux(&p); err != nil {
			return err
		}
		if err := validateFragment(&p); err != nil {
			return err
		}
		if err := validateFingerprint(&p); err != nil {
			return err
		}
	}
	return nil
}

// aetherProfileNeedsNoAddress reports whether [p] is an aether profile
// whose settings live in the outer/inner hops instead of an endpoint.
func aetherProfileNeedsNoAddress(p *Profile) bool {
	if p == nil || p.Protocol != "aether" || strings.TrimSpace(p.RawOutbound) != "" || p.Core.IsSpoof() {
		return false
	}
	protocol := aetherEnumDefault(aetherProtocols, p.Aether.Protocol, "wg")
	if aetherIsTwoHops(protocol) {
		_, outer := aetherEndpointParse(p.Aether.WiwOuter)
		_, inner := aetherEndpointParse(p.Aether.WiwInner)
		return outer || inner || (p.Aether.WiwOuter == "" && p.Aether.WiwInner == "")
	}
	return false
}

// SaveProfile creates or updates a profile.
func (a *App) SaveProfile(p Profile) (Profile, error) {
	if p.Name == "" {
		return Profile{}, fmt.Errorf("profile name is required")
	}
	if p.Address == "" && p.RawOutbound == "" && !aetherProfileNeedsNoAddress(&p) {
		return Profile{}, fmt.Errorf("address is required (or rawOutbound JSON)")
	}
	if err := validateProfileForSave(p); err != nil {
		return Profile{}, err
	}
	// PattNG parity: the Aether editor normalizes the settings and
	// rejects what the core would refuse, at save time (AetherFmt.normalize).
	if p.Protocol == "aether" && strings.TrimSpace(p.RawOutbound) == "" {
		if err := normalizeAether(&p); err != nil {
			return Profile{}, err
		}
	}
	// A changed server address invalidates the detected country: the
	// code derives from the address, so a stale flag would be wrong.
	if p.ID != "" {
		if old, ok := a.store.GetProfile(p.ID); ok && old.Address != p.Address {
			p.CountryCode = ""
			p.CountryName = ""
			p.CountryAt = nil
		}
	}
	saved, err := a.store.SaveProfile(p)
	if err != nil {
		return saved, err
	}
	// Fire-and-forget country detection: never block the save on DNS.
	if a.store.GetSettings().GeoIPAutoDetect && saved.CountryCode == "" && saved.Address != "" {
		go func(id string) {
			if _, err := a.DetectCountry(id); err != nil {
				a.emitLog("gui", "error", fmt.Sprintf("GeoIP detect failed: %v", err))
			}
		}(saved.ID)
	}
	return saved, nil
}

// DeleteProfile removes a profile; refuses while it is connected.
func (a *App) DeleteProfile(id string) error {
	_, activeID, running := a.cores.Status()
	if running && activeID == id {
		return fmt.Errorf("disconnect before deleting the active profile")
	}
	if err := a.store.DeleteProfile(id); err != nil {
		return err
	}
	// BUG-012: a deleted selected-but-idle profile must not linger as
	// the persisted auto-connect target.
	if a.store.GetSettings().ActiveProfileID == id {
		if err := a.store.SetActiveProfileID(""); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("DeleteProfile: clear active profile failed: %v", err))
		}
	}
	return nil
}

// --- settings ---

// GetSettings returns global settings.
func (a *App) GetSettings() Settings { return a.store.GetSettings() }

// SaveSettings persists global settings and refreshes manager paths.
func (a *App) SaveSettings(s Settings) error {
	// BUG-019: reject an out-of-range MixedPort before it is stored
	// (and later truncated to int32 for the proxy RPC).
	if err := validateMixedPort(s.MixedPort); err != nil {
		return err
	}
	// Advanced Phase H: the log level dropdown stores debug | info |
	// warn | error; legacy "warning" configs migrate to "warn" on save.
	if !validLogLevel(s.LogLevel) {
		return fmt.Errorf("logLevel must be one of debug, info, warn, error")
	}
	s.LogLevel = normalizeLogLevel(s.LogLevel)
	// TUN stack (CPU audit #1): fail fast on values sing-tun would
	// reject; empty normalizes to the default ("system") so configs
	// written before the setting existed converge on the cheap stack.
	if !validTunStack(s.TunStack) {
		return fmt.Errorf("tunStack must be one of system, gvisor, mixed")
	}
	s.TunStack = normalizeTunStack(s.TunStack)
	// Throne parity fields: fail fast on unbindable listen
	// addresses and malformed DNS upstreams (same BUG-019
	// discipline as MixedPort); the listen address normalizes
	// to loopback when empty.
	addr, err := normalizeInboundAddress(s.InboundAddress)
	if err != nil {
		return err
	}
	s.InboundAddress = addr
	if err := validateDNSServer(s.DnsRemote); err != nil {
		return err
	}
	if err := validateDNSServer(s.DnsDirect); err != nil {
		return err
	}
	// Global transport overrides: fail fast on values neither core
	// accepts (same BUG-019 discipline as the fields above); normalize
	// case/whitespace so the renderers see canonical values.
	if err := validateGlobalFragment(s); err != nil {
		return err
	}
	if err := validateGlobalFingerprint(s); err != nil {
		return err
	}
	s.FragmentMode = strings.ToLower(strings.TrimSpace(s.FragmentMode))
	s.FragmentLength = strings.TrimSpace(s.FragmentLength)
	s.FingerprintValue = normalizeFingerprint(s.FingerprintValue)
	// CoreEngine must be "custom" (default, Thrift RPC core) or
	// "upstream" (CLI-only official sing-box.exe). Empty normalizes to
	// "custom"; anything else rejects the save.
	switch s.CoreEngine {
	case "", "custom":
		s.CoreEngine = "custom"
	case "upstream":
		// valid
	default:
		return fmt.Errorf("coreEngine must be one of custom, upstream")
	}
	// The two sing-box engines are two binaries with two settings:
	// corePath is the custom nekobox_core.exe, upstreamCorePath the
	// official sing-box.exe. A setting naming the other engine's binary
	// is refused at save time instead of failing (or, worse, launching
	// the wrong file) at connect.
	if p := strings.TrimSpace(s.CorePath); p != "" && isOfficialSingBoxName(p) {
		return fmt.Errorf("corePath points at the official sing-box CLI; that binary belongs in upstreamCorePath — corePath is the custom nekobox_core.exe")
	}
	if p := strings.TrimSpace(s.UpstreamCorePath); p != "" && coreImageOwner(p) == "nekobox" {
		return fmt.Errorf("upstreamCorePath points at nekobox_core.exe; the official CLI belongs there — use corePath for the custom core")
	}
	old := a.store.GetSettings()
	if err := a.store.SaveSettings(s); err != nil {
		return err
	}
	a.singbox.setCorePath(s.CorePath)
	a.xray.setXrayPath(s.XrayPath)
	if s.GeoIPDBPath != old.GeoIPDBPath {
		a.geo.Configure(s.GeoIPDBPath)
	}
	return nil
}

// --- connection ---

// Connect starts the given profile (stops any active core first).
// The busy flag covers the whole probe so the UI/tray can disable
// competing actions; a racing Disconnect aborts the probe via the
// CoreManager epoch instead of deadlocking behind it.
func (a *App) Connect(id string) error {
	// One Connect at a time. A second one used to run a parallel Start:
	// both bump the CoreManager epoch, and the loser's abort path then
	// stopped whatever was current — including the winner's fresh core,
	// leaving the UI "connected" with nothing running.
	if !a.tryBeginConnect() {
		a.emitLog("gui", "warn", "Connect: another connect is already in progress")
		return fmt.Errorf("a connect is already in progress")
	}
	defer a.setConnectBusy(false)
	defer a.refreshTray()
	// A stray core from a crashed session can hold the mixed/API
	// ports. The startup orphan scan reaps those; wait for it (the
	// scan runs in the background so the window appears immediately)
	// so a port conflict can never reach the first Connect.
	a.waitOrphanScan()
	p, ok := a.store.GetProfile(id)
	if !ok {
		a.emitLog("gui", "error", fmt.Sprintf("Connect: profile %q not found", id))
		return fmt.Errorf("profile %q not found", id)
	}
	a.emitLog("gui", "info", fmt.Sprintf("Connect: starting %q via %s", p.Name, p.Core.String()))
	// Legacy loopback dead-end rescue (Windows: the engine ships as
	// sni-spoof.exe). A loopback config imported before the SNI
	// auto-detect (e.g. trojan://...@127.0.0.1:40443 ws) has no local
	// listener, so sing-box "connects" while every dial is refused.
	// Convert it here through the SAME path as the SNI tab's Start
	// (SpoofUseProfile: eligibility rule + persist + log). A profile
	// whose local port IS alive is a legitimate local chain — never
	// hijack it. This also covers tray + auto-connect, so connecting
	// the legacy profile from anywhere behaves like the tab Start.
	if runtime.GOOS == "windows" && shouldRescueLoopbackProfile(&p) {
		if saved, err := a.SpoofUseProfile(p.ID); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("Connect: loopback rescue failed: %v", err))
		} else {
			p = saved
		}
	}
	// Direct-domain rescue (prompt.txt Test-CF): an eligible
	// direct-domain profile imported before the domain auto-detect
	// sits on sing-box and DPI-blocks every direct dial. Convert it
	// here through the SAME path as the SNI tab's Start
	// (SpoofUseProfile), so Connect converges it to the spoof core
	// a fresh import would get. IP literals never rescue (same rule
	// as import: proving CF-fronting needs a live lookup).
	if runtime.GOOS == "windows" && shouldRescueDirectSpoofProfile(&p) {
		if saved, err := a.SpoofUseProfile(p.ID); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("Connect: direct-spoof rescue failed: %v", err))
		} else {
			p = saved
		}
	}
	settings := a.store.GetSettings()
	// Upstream pre-flight: the official sing-box binary is compiled
	// without the NekoGo-only transports, so a profile that needs one
	// cannot run there. Refuse now with an actionable message instead of
	// letting the core fail 15s later on a decode error. Xray profiles
	// never use a sing-box binary; every other core (plain, SNI spoof,
	// aether) runs the selected engine's sing-box as its transport.
	if settings.CoreEngine == "upstream" && !p.Core.IsXray() {
		if reason := upstreamUnsupportedReason(&p); reason != "" {
			a.emitLog("gui", "error", fmt.Sprintf("Connect: upstream core refused profile %q: %s", p.Name, reason))
			a.noteError(upstreamUnsupportedProfileMessage)
			return errors.New(upstreamUnsupportedProfileMessage)
		}
		a.emitLog("gui", "info", fmt.Sprintf("Connect: sing-box engine = official sing-box.exe (upstream, core %s)", p.Core.String()))
	}
	// TUN pre-flight (Windows): the TUN inbound creates a Wintun
	// adapter, which needs an elevated process, while nekogo.exe ships
	// asInvoker. Refuse BEFORE spawning the core with an actionable
	// message instead of a cryptic driver error. The frontend offers
	// the existing RelaunchAsAdmin handoff, so persist the requested
	// profile first: the elevated child resumes it through --connect.
	// Xray ignores tunEnabled, so proxy-only users are never gated.
	if tunNeedsElevation(&p, settings) {
		if err := a.store.SetActiveProfileID(id); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("Connect: persist TUN elevation target: %v", err))
		}
		a.emitLog("gui", "error", tunRequiresAdminMessage)
		a.noteError(tunRequiresAdminMessage)
		return errors.New(tunRequiresAdminMessage)
	}
	start := a.cores.Start
	if a.coreStarter != nil {
		start = a.coreStarter
	}
	if err := start(&p, settings); err != nil {
		a.emitLog("gui", "error", fmt.Sprintf("Connect: core start failed: %v", err))
		return tunStartHint(err, &p, settings)
	}
	ports := a.ActiveProxyPorts()
	// Mode sync, per connect: TUN and the OS proxy are two ways to feed
	// traffic into the tunnel, so the rendered mode decides which one is
	// used. When the config carries the TUN inbound (tunEnabled or the
	// SNI spoof → Psiphon chain, which always needs TUN) the OS proxy is
	// left off — a proxy pointing at our mixed port would duplicate every
	// connection and route the proxy's own traffic back through the
	// tunnel. An auto-set proxy from a previous connect is cleared; a
	// manually enabled one is the user's choice and stays (Disconnect's
	// contract). Otherwise the remembered SystemProxy setting gates the
	// auto path exactly as before. Do NOT fail Connect when only the OS
	// proxy could not be set — the core is running; the UI surfaces
	// LastError.
	if tunModeFor(&p, settings) {
		if a.getAutoProxy() {
			if err := a.ClearSystemProxy(); err != nil {
				a.emitLog("gui", "error", fmt.Sprintf("auto system proxy disable failed: %v", err))
			} else {
				a.emitLog("gui", "info", "auto system proxy disabled (TUN mode carries all traffic)")
			}
			a.setAutoProxy(false)
		} else {
			a.emitLog("gui", "info", "system proxy left off (TUN mode carries all traffic)")
		}
	} else if settings.SystemProxy {
		if err := a.ensureSystemProxy(ports); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("auto system proxy: %v", err))
		}
	} else {
		a.emitLog("gui", "info", "auto system proxy skipped (SystemProxy=false in settings)")
	}
	// BUG-040: persistence failures must not masquerade as success.
	// The core is running, but restart/auto-connect state would be
	// silently lost — aggregate the errors and return them.
	connectedAt := time.Now().Format(time.RFC3339)
	var persistErrs []error
	// Single-field stamp: the connect probe ran for seconds and the row
	// above predates it — writing the whole snapshot back would revert an
	// edit saved meanwhile (BUG-043 class).
	if _, _, err := a.store.updateProfileFields(id, func(e *Profile) {
		e.LastConnectedAt = connectedAt
	}); err != nil {
		msg := fmt.Sprintf("Connect: save last-connected failed: %v", err)
		a.emitLog("gui", "error", msg)
		persistErrs = append(persistErrs, err)
	}
	if err := a.store.SetActiveProfileID(id); err != nil {
		msg := fmt.Sprintf("Connect: save active profile failed: %v", err)
		a.emitLog("gui", "error", msg)
		persistErrs = append(persistErrs, err)
	}
	a.refreshTray()
	// Step 4: the spoof legs (engine first, then the sing-box transport
	// via CoreManager) are up — timestamp the completion in the spoof
	// log stream so a slow Connect shows exactly which leg lagged.
	if p.Core.IsSpoof() {
		port := 0
		if a.spoofMgr != nil {
			port = a.spoofMgr.GetListenPort()
		}
		a.emitLog("spoof", "info", fmt.Sprintf("[spoof] connect complete (profile %q, port %d)", p.Name, port))
		if chain, chainPort := a.cores.PsiphonChain(); chain {
			a.emitLog("psiphon", "info", fmt.Sprintf("[psiphon] chain up (socks 127.0.0.1:%d)", chainPort))
		}
	}
	a.clearError()
	if len(persistErrs) > 0 {
		return errors.Join(persistErrs...)
	}
	return nil
}

// tunRequiresAdminMessage is the Connect error shown when TUN is
// enabled but this process cannot create the Wintun adapter. The
// frontend matches the "TUN mode requires administrator rights"
// prefix to offer the existing RelaunchAsAdmin handoff.
const tunRequiresAdminMessage = "TUN mode requires administrator rights. Relaunch NekoGo as administrator?"

// tunNeedsElevation reports whether connecting p will start sing-box
// with the TUN inbound while this GUI process is not elevated. Only
// Windows needs it (Wintun); Xray never renders the TUN inbound, so
// a stale tunEnabled setting must not block a proxy-only profile. An
// elevation-probe failure is NOT treated as unelevated: the connect
// proceeds and a real permission failure is annotated by
// tunStartHint instead.
func tunNeedsElevation(p *Profile, settings Settings) bool {
	if runtime.GOOS != "windows" || p == nil || p.Core.IsXray() || !tunRequiredFor(p, settings) {
		return false
	}
	// A stored full raw config replaces the rendered config (and the
	// TUN append) entirely, so it never needs the Wintun adapter.
	if _, full, err := rawCoreOverride(settings.CoreOverrides); err == nil && full {
		return false
	}
	elevated, err := guiElevated()
	return err == nil && !elevated
}

// tunStartHint annotates a permission-shaped TUN start failure with
// the admin remedy while preserving the core's exact error (%w).
// sing-box/wintun report these as plain "Access is denied" /
// "CreateAdapter failed" strings; on an unelevated GUI that means the
// Wintun adapter, not a config bug. Config rendering is untouched.
func tunStartHint(err error, p *Profile, settings Settings) error {
	if err == nil || runtime.GOOS != "windows" || p == nil || p.Core.IsXray() || !tunRequiredFor(p, settings) {
		return err
	}
	if elevated, probeErr := guiElevated(); probeErr == nil && elevated {
		return err
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"access is denied",
		"requires elevation",
		"createadapter",
		"operation not permitted",
		"permission denied",
	} {
		if strings.Contains(lower, needle) {
			return fmt.Errorf("%w (TUN needs administrator rights: relaunch NekoGo as administrator and reconnect)", err)
		}
	}
	return err
}

// Disconnect stops the active core. When the OS proxy was enabled by
// the auto path in Connect (autoProxyOn), it is restored best-effort
// BEFORE stopping the core; a manually-enabled proxy is left
// untouched. The GUI owns the proxy, so no running core is needed to
// revert it.
func (a *App) Disconnect() error {
	if a.getAutoProxy() {
		if err := a.ClearSystemProxy(); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("auto system proxy disable failed: %v", err))
		}
		a.setAutoProxy(false)
	}
	err := a.cores.Stop()
	a.refreshTray()
	return err
}

// RelaunchAsAdmin restarts NekoGo elevated, resuming the active
// connection when there is one. Bound for the UI (offered when
// unelevated). Already-elevated is an honest error, not a
// silent no-op.
func (a *App) RelaunchAsAdmin() error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("elevated relaunch is only supported on Windows")
	}
	if elevated, err := guiElevated(); err == nil && elevated {
		return fmt.Errorf("already running as administrator")
	}
	id := a.store.GetSettings().ActiveProfileID
	if _, pid, running := a.cores.Status(); running && pid != "" {
		id = pid
	}
	_ = a.Disconnect()
	args := []string{}
	if id != "" {
		args = []string{"--connect", id}
	}
	if err := relaunchElevated(args...); err != nil {
		return err
	}
	a.emitLog("gui", "info", "relaunching as administrator (approve the UAC prompt)")
	a.Shutdown()
	elevateExiter(0)
	return nil
}

// parseConnectArg extracts a `--connect <profileID>` argv pair (used
// by the elevated child to resume the pending connect). Pure+testable:
// unknown args are ignored, a dangling flag yields "".
func parseConnectArg(argv []string) string {
	for i, arg := range argv {
		if arg == "--connect" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// Status reports core name, active profile id, running flag, and whether
// a Connect is currently in flight (integration BUG-001/002).
func (a *App) Status() map[string]any {
	core, id, running := a.cores.Status()
	return map[string]any{
		"core": core, "profileId": id, "running": running,
		"connectBusy": a.IsConnectBusy(), "lastError": a.LastError(),
	}
}

// GetStats returns live traffic for the active core.
func (a *App) GetStats() (*Stats, error) { return a.cores.GetStats() }

// --- SNI Spoofing core (sni-spoof.exe, CoreSpoof) ---
//
// The SNI tab drives the engine directly through these bound methods
// (all additive: no existing signature changed). A CoreSpoof Connect
// owns the engine via CoreManager; SpoofStart/SpoofStop control it
// standalone (e.g. probing endpoints before connecting).

// SpoofStatus is the SNI tab status card payload. It always reflects
// real manager state (IsRunning/listen port), never an optimistic
// UI flag.
type SpoofStatus struct {
	Running    bool    `json:"running"`
	ListenPort int     `json:"listenPort"`
	StartedAt  string  `json:"startedAt"`
	Uptime     float64 `json:"uptime"`
	LastError  string  `json:"lastError"`
	// Chain reports the SNI spoof → Psiphon chain on a live connect;
	// ChainPort is Psiphon's local SOCKS listener.
	Chain     bool `json:"chain"`
	ChainPort int  `json:"chainPort"`
}

// SpoofGetStatus reports the engine state. It NEVER returns an error
// for "simply not running": the SNI tab polls this every 2s and an
// error there would paint the status card red while idle. Only a
// missing manager (nil in tests) yields the safe default — still
// with a nil error. All reads are mutex-guarded and non-blocking.
func (a *App) SpoofGetStatus() (SpoofStatus, error) {
	if a == nil || a.spoofMgr == nil {
		return SpoofStatus{}, nil
	}
	running := a.spoofMgr.IsRunning()
	st := SpoofStatus{Running: running, LastError: a.spoofMgr.GetLastError()}
	if running {
		st.ListenPort = a.spoofMgr.GetListenPort()
		started := a.spoofMgr.StartedAt()
		if !started.IsZero() {
			st.StartedAt = started.Format(time.RFC3339)
			st.Uptime = time.Since(started).Seconds()
		}
	}
	if a.cores != nil {
		st.Chain, st.ChainPort = a.cores.PsiphonChain()
	}
	return st, nil
}

// SpoofGetStats returns the last scraped [STATS] snapshot.
func (a *App) SpoofGetStats() (*SpoofStats, error) {
	if a.spoofMgr == nil {
		return nil, fmt.Errorf("sni-spoof manager not wired")
	}
	return a.spoofMgr.GetStats()
}

// SpoofGetConfig returns the stored engine options.
func (a *App) SpoofGetConfig() (SNIConfig, error) {
	return a.store.GetSettings().SNIConfig, nil
}

// validSpoofEnums mirrors the Rust validator (CORE_REPORT.md section
// 3): unknown values fail here with the accepted set named, instead
// of dying inside the engine at Start.
var (
	validSpoofMethods = []string{"auto", "wrong_seq", "wrong_seq_ttl", "split_seq", "fragmented", "padding", "delayed_retry", "double_sni", "hostfakesplit", "fakedsplit"}
	validSpoofFPS     = []string{"legacy", "chrome_120", "chrome_124", "firefox_122", "firefox_124", "custom"}
	validSpoofQUIC    = []string{"block", "spoof", "passthrough"}
	validSpoofIPMode  = []string{"ipv4", "ipv6", "both"}
)

func spoofEnumOk(v string, valid []string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, e := range valid {
		if v == e {
			return true
		}
	}
	return false
}

// SpoofSetConfig validates and persists the engine options. Empty
// values mean "engine default" and are omitted from the generated
// config — except Method, which migrates to "auto" (the engine
// default) so a fresh tab never renders a blank method.
func (a *App) SpoofSetConfig(cfg SNIConfig) error {
	cfg.Method = strings.ToLower(strings.TrimSpace(cfg.Method))
	cfg.Fingerprint = strings.ToLower(strings.TrimSpace(cfg.Fingerprint))
	cfg.QUICMode = strings.ToLower(strings.TrimSpace(cfg.QUICMode))
	cfg.IPMode = strings.ToLower(strings.TrimSpace(cfg.IPMode))
	if cfg.Method == "" {
		cfg.Method = "auto"
	}
	if !spoofEnumOk(cfg.Method, validSpoofMethods) {
		return fmt.Errorf("method must be one of %s", strings.Join(validSpoofMethods, ", "))
	}
	if cfg.Fingerprint != "" && !spoofEnumOk(cfg.Fingerprint, validSpoofFPS) {
		return fmt.Errorf("fingerprint must be one of %s", strings.Join(validSpoofFPS, ", "))
	}
	if cfg.QUICMode != "" && !spoofEnumOk(cfg.QUICMode, validSpoofQUIC) {
		return fmt.Errorf("quicMode must be one of %s", strings.Join(validSpoofQUIC, ", "))
	}
	if cfg.IPMode != "" && !spoofEnumOk(cfg.IPMode, validSpoofIPMode) {
		return fmt.Errorf("ipMode must be one of %s", strings.Join(validSpoofIPMode, ", "))
	}
	if cfg.HandshakeTimeout != 0 && (cfg.HandshakeTimeout < 0.5 || cfg.HandshakeTimeout > 10.0) {
		return fmt.Errorf("handshakeTimeout must be 0.5..10.0 (0 = engine default)")
	}
	if cfg.MaxConnections != 0 && (cfg.MaxConnections < 10 || cfg.MaxConnections > 2000) {
		return fmt.Errorf("maxConnections must be 10..2000 (0 = engine default)")
	}
	if cfg.FakeDelay != 0 && (cfg.FakeDelay < 0.0 || cfg.FakeDelay > 5.0) {
		return fmt.Errorf("fakeDelay must be 0.0..5.0")
	}
	if cfg.SeqOverlap < 0 || cfg.SeqOverlap > 16 {
		return fmt.Errorf("seqOverlap must be 0..16")
	}
	if cfg.PaddingSize < 0 || cfg.PaddingSize > 128 {
		return fmt.Errorf("paddingSize must be 0..128")
	}
	return a.store.UpdateSNI(func(s *Settings) { s.SNIConfig = cfg })
}

// ResetSNIDefaults restores the SNI Spoofing tab's curated Cloudflare
// IP + decoy-SNI lists and the default pin (sni_defaults.go). Scanner
// stamps of surviving entries are preserved; custom entries are
// dropped by design — this is the explicit "give me the fresh list"
// action, unlike the scanner which never adds or removes entries.
func (a *App) ResetSNIDefaults() error {
	return a.store.UpdateSNI(func(s *Settings) { resetSNIDefaults(s) })
}

// SpoofStart launches the engine standalone for the given profile
// (SNI tab Start button). Refuses while it already runs — the live
// config belongs to whoever started it. The profile must be a
// CoreSpoof profile: the tab only offers those, and anything else
// fails here with the actual core named (never auto-starts with the
// active sing-box profile).
func (a *App) SpoofStart(profileID string) error {
	if a.spoofMgr == nil {
		return fmt.Errorf("sni-spoof manager not wired")
	}
	p, ok := a.store.GetProfile(profileID)
	if !ok {
		return fmt.Errorf("profile %q not found", profileID)
	}
	if !p.Core.IsSpoof() {
		return fmt.Errorf("profile %q is not a CoreSpoof profile (got %s)", p.Name, p.Core.String())
	}
	if err := a.spoofMgr.Start(&p, a.store.GetSettings()); err != nil {
		return err
	}
	setSpoofFrontPort(a.spoofMgr.GetListenPort())
	a.emitLog("spoof", "info", fmt.Sprintf("[spoof] standalone start complete (port %d)", a.spoofMgr.GetListenPort()))
	return nil
}

// SpoofStop terminates the standalone engine (SNI tab Stop button).
func (a *App) SpoofStop() error {
	if a.spoofMgr == nil {
		return fmt.Errorf("sni-spoof manager not wired")
	}
	// Refuse to kill the engine out from under a live spoof
	// connection: Disconnect owns that teardown.
	if _, id, running := a.cores.Status(); running {
		if p, ok := a.store.GetProfile(id); ok && p.Core.IsSpoof() {
			return fmt.Errorf("disconnect the spoof connection first")
		}
	}
	port := a.spoofMgr.GetListenPort()
	if err := a.spoofMgr.Stop(); err != nil {
		return err
	}
	// Port-scoped: a newer engine (connect racing the Stop) keeps its port.
	clearSpoofFrontPortIf(port)
	return nil
}

// SpoofUseProfile switches an eligible profile to the SNI spoof core
// (CoreSpoof) and persists it — the SNI tab's one-click fix for
// loopback/chained configs (e.g. trojan://...@127.0.0.1:40443 ws)
// imported before the auto-detect existed, which otherwise dead-end
// on a non-spoof core (nothing listens on the local port). Same
// eligibility rule as the import auto-detect (spoofEligibilityIssue);
// an ineligible profile fails here with the exact missing trait named.
// Already-spoof profiles are a no-op so the UI can call it
// unconditionally before SpoofStart.
func (a *App) SpoofUseProfile(profileID string) (Profile, error) {
	p, ok := a.store.GetProfile(profileID)
	if !ok {
		return Profile{}, fmt.Errorf("profile %q not found", profileID)
	}
	if p.Core.IsSpoof() {
		return p, nil
	}
	if err := spoofEligibilityIssue(&p); err != nil {
		return Profile{}, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	from := p.Core.String()
	p.Core = CoreSpoof
	saved, err := a.store.SaveProfile(p)
	if err != nil {
		return Profile{}, err
	}
	a.emitLog("spoof", "info", fmt.Sprintf("[spoof] profile %q switched from %s to the SNI spoof core", saved.Name, from))
	return saved, nil
}

// SpoofGetLogs returns buffered engine log lines (sources "spoof" and
// "psiphon") whose ID is newer than [sinceID], so the tab can poll
// incrementally. IDs, not offsets: the buffer is a 2000-entry ring that
// evicts from the head, so an index cursor drifts (skipped or duplicated
// lines) and then freezes for good once it passes the filtered length.
// Same contract as AetherScanLogs.
func (a *App) SpoofGetLogs(sinceID uint64) ([]LogEntry, error) {
	a.logMu.RLock()
	defer a.logMu.RUnlock()
	out := make([]LogEntry, 0, 16)
	for _, e := range a.logBuf {
		if (e.Source == "spoof" || e.Source == "psiphon") && e.ID > sinceID {
			out = append(out, e)
		}
	}
	return out, nil
}

// ListConnections returns active connections (sing-box only) plus whether
// the server capped the list at the newest 500 (P3-3: Truncated=true).
// QueryStats reports lifetime totals including closed connections; this is
// a snapshot of what is connected right now.
func (a *App) ListConnections() (*ConnectionList, error) {
	// CoreManager routes to the upstream CLI manager when that backend
	// is the active one (Clash API rows); the custom core keeps its own
	// Thrift ListConnections RPC.
	return a.cores.ListConnections()
}

// TestProfile measures real end-to-end latency for a profile by sending an
// HTTP probe through a throwaway ephemeral core that is always torn down,
// whether or not this profile is the active connection. The active core
// is never touched: routing the active-profile test through the shared
// server Test RPC meant a Network-tab StopTest cancelled the sibling
// Proxies-tab spinner through the one global test scope (integration
// BUG-008). There is no TCP-connect fallback; a failure returns
// LatencyMs -1 with Error set (nil error) so the UI shows "Failed"
// instead of failing the call. Successful measurements persist
// LastLatency/LastTestedAt to the store.
func (a *App) TestProfile(id string, timeoutMs int) (*TestResult, error) {
	p, ok := a.store.GetProfile(id)
	if !ok {
		return nil, fmt.Errorf("profile %q not found", id)
	}
	if timeoutMs <= 0 {
		timeoutMs = 8000
	}
	settings := a.store.GetSettings()
	url := resolveTestURL(&p, settings)
	res, err := a.cores.EphemeralURLTest(&p, settings, url, timeoutMs, probeOwnerProxies)
	if err != nil {
		return &TestResult{LatencyMs: -1, Error: err.Error()}, nil
	}
	if res != nil && res.Error == "" && res.LatencyMs >= 0 {
		// BUG-021: a sub-ms success measures 0 ms, which encoding/json
		// omitempty drops on the wire and the store reads back as
		// never-tested — clamp to 1 ms so the measurement round-trips.
		res.LatencyMs = clampLatencyMs(res.LatencyMs)
		// Stamp only the latency fields: the probe ran for seconds and the
		// snapshot above predates it — saving the whole row would revert an
		// edit saved meanwhile (BUG-043 class).
		if _, _, err := a.store.updateProfileFields(p.ID, func(e *Profile) {
			e.LastLatency = int(res.LatencyMs)
			now := time.Now()
			e.LastTestedAt = &now
		}); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("Test: save latency failed: %v", err))
		}
	}
	return res, nil
}

// clampLatencyMs floors a successful probe at 1 ms (BUG-021): 0 is a
// real sub-ms measurement, but LastLatency omitempty cannot persist it.
func clampLatencyMs(ms int32) int32 {
	if ms < 1 {
		return 1
	}
	return ms
}

// resolveTestURL picks the latency probe target: per-profile override,
// then the global settings override, then the built-in default.
func resolveTestURL(p *Profile, s Settings) string {
	if p != nil && p.TestURL != "" {
		return p.TestURL
	}
	if s.TestUrl != "" {
		return s.TestUrl
	}
	return defaultTestURL
}

// --- Advanced Mode Phase C: core network tools (Console -> Network) ---

// activeNetworkTarget resolves the running connection's profile and
// settings for tools that probe the ACTIVE core. All Phase C tools
// require a live connection.
func (a *App) activeNetworkTarget() (Profile, Settings, error) {
	_, id, running := a.cores.Status()
	if !running {
		return Profile{}, Settings{}, fmt.Errorf("no active connection")
	}
	p, ok := a.store.GetProfile(id)
	if !ok {
		return Profile{}, Settings{}, fmt.Errorf("active profile %q not found", id)
	}
	return p, a.store.GetSettings(), nil
}

// fillCountryName fills an IPTestResult's English country name from the
// local GeoIP DB (best-effort: empty when the DB is missing).
func (a *App) fillCountryName(r *IPTestResult) {
	if r == nil || r.IP == "" || r.Country != "" || a.geo == nil {
		return
	}
	if _, name, _ := a.geo.Lookup(r.IP); name != "" {
		r.Country = name
	}
}

// StartIPTest probes the egress IP through the active core (sing-box
// IPTest RPC; client-side proxy probe for xray).
func (a *App) StartIPTest() (*IPTestResult, error) {
	p, settings, err := a.activeNetworkTarget()
	if err != nil {
		return nil, err
	}
	r, err := a.cores.IPTest(&p, settings)
	if err != nil {
		return nil, err
	}
	a.fillCountryName(r)
	return r, nil
}

// StartSpeedTest measures throughput through the active core (sing-box
// SpeedTest RPC; client-side download probe for xray).
func (a *App) StartSpeedTest() (*SpeedTestResult, error) {
	if _, _, err := a.activeNetworkTarget(); err != nil {
		return nil, err
	}
	return a.cores.SpeedTest()
}

// StartURLTest probes url through the active core (sing-box Test RPC;
// running-proxy probe for xray). Empty url falls back to the
// profile/settings/default chain, resolved against the ACTIVE profile
// (integration BUG-006): passing nil bypassed a per-profile testUrl
// override set in the profile form.
func (a *App) StartURLTest(url string, timeoutMs int) (*URLTestResult, error) {
	p, settings, err := a.activeNetworkTarget()
	if err != nil {
		return nil, err
	}
	if url == "" {
		url = resolveTestURL(&p, settings)
	}
	return a.cores.URLTest(url, timeoutMs)
}

// StopTest cancels the in-flight Network-tab probe, if any: the engine
// probe (server testCtx for sing-box, local probe ctx for xray) plus
// any network-owned ephemeral probe. Proxies-owned ephemeral probes
// (TestProfile rows) are deliberately NOT cancelled here — a Network
// Stop must not kill the sibling tab's spinner (integration BUG-008).
func (a *App) StopTest() error {
	abortedEph := a.cores.StopEphemeralTests(probeOwnerNetwork)
	err := a.cores.StopTest()
	if err != nil && abortedEph {
		return nil
	}
	return err
}

// LastNetworkResults returns the last-known tool results without
// running anything (sing-box reporter reads; xray cache). Best-effort
// seeding call for the Network tab: idle yields an empty non-nil struct.
func (a *App) LastNetworkResults() (*LastNetworkResults, error) {
	if a.cores == nil {
		return &LastNetworkResults{}, nil
	}
	out, err := a.cores.LastNetwork()
	if err != nil {
		return nil, err
	}
	a.fillCountryName(out.IP)
	return out, nil
}

// --- Advanced Mode Phase D: system tools (Console -> System) ---

// IsPrivileged reports elevation: the core process via RPC when
// sing-box runs, otherwise this GUI process. Bound for the UI.
func (a *App) IsPrivileged() (bool, error) {
	return a.cores.IsPrivileged()
}

// GetSystemProxyState reports the ACTUAL OS proxy switch by reading it
// from the OS (integration BUG-010). The Console System tab queries
// this on mount so the toggle reflects reality — a crash leftover, an
// external change, or a restart — instead of its own stale local state.
func (a *App) GetSystemProxyState() (bool, error) {
	return osProxyEnabled()
}

// EnableSystemProxyGUI applies the OS proxy for the currently running
// core's ports (manual Console -> System toggle, any core). It also
// remembers SystemProxy=true so the next Connect keeps the OS proxy
// on instead of second-guessing the user.
func (a *App) EnableSystemProxyGUI() error {
	if err := a.SetSystemProxy(a.ActiveProxyPorts()); err != nil {
		return err
	}
	return a.rememberSystemProxy(true)
}

// DisableSystemProxyGUI reverts the OS proxy set via the GUI path. It
// also remembers SystemProxy=false so the next Connect leaves the OS
// proxy untouched instead of forcing it back on.
func (a *App) DisableSystemProxyGUI() error {
	if err := a.ClearSystemProxy(); err != nil {
		return err
	}
	return a.rememberSystemProxy(false)
}

// rememberSystemProxy persists the SystemProxy setting (best-effort
// sync of the in-memory copy on store failure, so the running session
// and the next launch agree). Failures are logged, never fatal to the
// toggle that just succeeded.
func (a *App) rememberSystemProxy(on bool) error {
	s := a.store.GetSettings()
	if s.SystemProxy == on {
		return nil
	}
	s.SystemProxy = on
	if err := a.store.SaveSettings(s); err != nil {
		a.emitLog("gui", "warn", fmt.Sprintf("remember system proxy failed: %v", err))
		return err
	}
	return nil
}

// setAutoProxy records whether this session enabled the OS proxy.
func (a *App) setAutoProxy(on bool) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.autoProxyOn = on
}

// getAutoProxy reports whether this session enabled the OS proxy.
func (a *App) getAutoProxy() bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	return a.autoProxyOn
}

// SetSystemDNS applies (clear=false) or restores (clear=true) the
// core's local DNS. NOTE: the Thrift request carries only the clear
// flag — no address field exists server-side — so this takes a bool,
// not the addr string the Phase D draft assumed.
func (a *App) SetSystemDNS(clear bool) error {
	return a.cores.SetSystemDNS(clear)
}

// IsSupported probes one capability name ("wireguard", "naive",
// "tailscale", ...): server-side when sing-box runs, otherwise the
// exact local mirror (only "naive" is a real check there).
func (a *App) IsSupported(kind string) (bool, error) {
	if kind == "" {
		return false, fmt.Errorf("empty capability")
	}
	return a.cores.IsSupported(kind)
}

// GenWgKeyPair generates a WireGuard keypair: server-side when
// sing-box runs, otherwise locally with identical semantics.
func (a *App) GenWgKeyPair() (*WgKeyPair, error) {
	return a.cores.GenWgKeyPair()
}

// CacheHTTP fetches url into the core ruleset cache, returning the
// local path. Sing-box only.
func (a *App) CacheHTTP(url string) (string, error) {
	return a.cores.CacheHTTP(url)
}

// --- link import (share links + subscriptions) ---

// PreviewLinks parses pasted text without persisting anything.
func (a *App) PreviewLinks(text string) (*LinkImportResult, error) {
	profiles, invalid := previewText(text, "")
	return &LinkImportResult{Profiles: profiles, Invalid: invalid}, nil
}

// saveImportedProfile persists one profile that came from a link or a
// subscription feed.
//
// Imports used to call the store directly, so a link the core builders
// cannot route (mux together with an xtls flow, an out-of-range mux
// concurrency, an Aether profile that never went through normalizeAether)
// was saved, listed in Proxies, and only failed at Connect with a message
// about a field the user never saw. Validate the same way the profile
// editor does and let the caller count the rejection as invalid.
func (a *App) saveImportedProfile(p Profile) (Profile, error) {
	if p.Protocol == "aether" && strings.TrimSpace(p.RawOutbound) == "" {
		if err := normalizeAether(&p); err != nil {
			return Profile{}, err
		}
	}
	if err := validateProfileForSave(p); err != nil {
		return Profile{}, err
	}
	return a.store.SaveProfile(p)
}

// ImportLinks parses pasted text, records subscription URLs, and saves all
// new profiles (deduplicated against the existing store).
func (a *App) ImportLinks(text string) (*LinkImportResult, error) {
	direct, subs := collectLinks(text)
	known := map[string]bool{}
	for _, e := range a.store.ListProfiles() {
		known[dedupeKey(&e)] = true
	}
	var out []Profile
	invalid := 0
	for _, ln := range direct {
		p, err := parseOneLink(ln)
		if !usableParsedLink(p, err) {
			invalid++
			continue
		}
		if known[dedupeKey(p)] {
			continue
		}
		saved, err := a.saveImportedProfile(*p)
		if err != nil {
			invalid++
			continue
		}
		known[dedupeKey(&saved)] = true
		out = append(out, saved)
	}
	for _, su := range subs {
		added, bad, err := a.importSubscription(su)
		if err != nil {
			invalid++
			continue
		}
		invalid += bad
		for _, p := range added {
			if known[dedupeKey(&p)] {
				continue
			}
			saved, err := a.saveImportedProfile(p)
			if err != nil {
				invalid++
				continue
			}
			known[dedupeKey(&saved)] = true
			out = append(out, saved)
		}
	}
	a.emitLog("gui", "info", fmt.Sprintf("Import: %d new profiles, %d invalid", len(out), invalid))
	if len(out) > 0 && a.store.GetSettings().GeoIPAutoDetect {
		go a.refreshCountriesBackground("import")
	}
	return &LinkImportResult{Profiles: out, Invalid: invalid}, nil
}

// importSubscription fetches one URL, parses it, and records the subscription.
func (a *App) importSubscription(rawURL string) ([]Profile, int, error) {
	lines, info, err := fetchSubscription(rawURL)
	if err != nil {
		return nil, 0, err
	}
	u := urlParseHost(rawURL)
	var out []Profile
	invalid := 0
	for _, ln := range lines {
		p, err := parseOneLink(ln)
		if !usableParsedLink(p, err) {
			invalid++
			continue
		}
		p.Group = u
		p.SubURL = rawURL
		out = append(out, *p)
	}
	a.recordSubscription(rawURL, u, info)
	return out, invalid, nil
}

func (a *App) recordSubscription(rawURL, name string, info SubUserInfo) {
	if name == "" {
		name = rawURL
	}
	// BUG-043: atomic locked RMW on the store — no copy-modify-
	// SaveSettings window for a concurrent settings edit to fall into.
	// A re-imported feed already exists (AddSubscription no-ops), so
	// the timestamp + usage refresh below keeps quota/expiry current.
	now := time.Now().Format(time.RFC3339)
	if _, err := a.store.AddSubscription(Subscription{
		URL:           rawURL,
		Name:          name,
		UpdatedAt:     now,
		UsageUpload:   info.Upload,
		UsageDownload: info.Download,
		UsageTotal:    info.Total,
		UsageExpire:   info.Expire,
	}); err != nil {
		a.emitLog("gui", "error", fmt.Sprintf("save settings: %v", err))
		return
	}
	if _, err := a.store.TouchSubscription(rawURL, now, info); err != nil {
		a.emitLog("gui", "error", fmt.Sprintf("save settings: %v", err))
	}
}

// UpdateSubscriptions re-fetches every recorded subscription and saves profiles
// not already in the store. It returns only the newly added profiles.
func (a *App) UpdateSubscriptions() (*LinkImportResult, error) {
	// Snapshot the subscription list; each timestamp touch below is an
	// atomic locked RMW (BUG-043) instead of a copy-modify-SaveSettings
	// of the whole Settings that would drop concurrent settings edits.
	subs := a.store.GetSettings().Subscriptions
	if len(subs) == 0 {
		return &LinkImportResult{}, nil
	}
	known := map[string]bool{}
	for _, e := range a.store.ListProfiles() {
		known[dedupeKey(&e)] = true
	}
	var out []Profile
	invalid := 0
	for _, sub := range subs {
		if !sub.IsEnabled() {
			continue
		}
		lines, info, err := fetchSubscription(sub.URL)
		if err != nil {
			invalid++
			continue
		}
		for _, ln := range lines {
			p, err := parseOneLink(ln)
			if !usableParsedLink(p, err) {
				invalid++
				continue
			}
			p.Group = sub.Name
			p.SubURL = sub.URL
			if known[dedupeKey(p)] {
				continue
			}
			saved, err := a.saveImportedProfile(*p)
			if err != nil {
				invalid++
				continue
			}
			known[dedupeKey(&saved)] = true
			out = append(out, saved)
		}
		if _, err := a.store.TouchSubscription(sub.URL, time.Now().Format(time.RFC3339), info); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("save settings: %v", err))
		}
	}
	a.emitLog("gui", "info", fmt.Sprintf("Subscriptions updated: %d new profiles", len(out)))
	if len(out) > 0 && a.store.GetSettings().GeoIPAutoDetect {
		go a.refreshCountriesBackground("subscriptions")
	}
	return &LinkImportResult{Profiles: out, Invalid: invalid}, nil
}

// ListSubscriptions returns the recorded subscription URLs.
func (a *App) ListSubscriptions() []Subscription {
	return a.store.GetSettings().Subscriptions
}

// EditSubscription renames a feed and/or repoints it at a new URL.
// Order matters: a URL change imports the new feed first, so a fetch
// failure leaves the old record intact; only then the name is applied,
// the old record dropped, and already-imported profiles regrouped
// (their Group was stamped at import time).
func (a *App) EditSubscription(oldURL, newURL, name string) (*LinkImportResult, error) {
	oldURL = strings.TrimSpace(oldURL)
	newURL = strings.TrimSpace(newURL)
	name = strings.TrimSpace(name)
	if oldURL == "" {
		return nil, fmt.Errorf("subscription not found")
	}
	if newURL == "" {
		return nil, fmt.Errorf("subscription URL required")
	}
	// The display name to keep: the user's new one, else the old one.
	oldName := ""
	for _, s := range a.store.GetSettings().Subscriptions {
		if s.URL == oldURL {
			oldName = s.Name
			break
		}
	}
	effName := name
	if effName == "" {
		effName = oldName
	}
	res := &LinkImportResult{}
	if newURL != oldURL {
		added, invalid, err := a.importSubscription(newURL)
		if err != nil {
			return nil, err
		}
		res.Invalid = invalid
		known := map[string]bool{}
		for _, e := range a.store.ListProfiles() {
			known[dedupeKey(&e)] = true
		}
		for _, p := range added {
			if effName != "" {
				p.Group = effName
			}
			if known[dedupeKey(&p)] {
				continue
			}
			saved, err := a.saveImportedProfile(p)
			if err != nil {
				res.Invalid++
				continue
			}
			known[dedupeKey(&saved)] = true
			res.Profiles = append(res.Profiles, saved)
		}
	}
	// A URL change records the feed under its host name, so keep the
	// kept/entered name even when it matches the old record's.
	if effName != "" && (newURL != oldURL || effName != oldName) {
		if _, err := a.store.RenameSubscription(newURL, effName); err != nil {
			return res, err
		}
	}
	if newURL != oldURL {
		if _, err := a.store.DeleteSubscription(oldURL); err != nil {
			return res, err
		}
		// Profiles imported from the old URL follow the repoint so a
		// later delete-with-configs still matches them precisely.
		for _, e := range a.store.ListProfiles() {
			if e.SubURL != oldURL {
				continue
			}
			e.SubURL = newURL
			if _, err := a.store.SaveProfile(e); err != nil {
				a.emitLog("gui", "error", fmt.Sprintf("repoint profiles: %v", err))
			}
		}
	}
	// Profiles imported before the rename still carry the old group
	// name — move them over so the Proxies tab shows one coherent
	// group per feed.
	if effName != "" && oldName != "" && effName != oldName {
		for _, e := range a.store.ListProfiles() {
			if e.Group != oldName {
				continue
			}
			e.Group = effName
			if _, err := a.store.SaveProfile(e); err != nil {
				a.emitLog("gui", "error", fmt.Sprintf("regroup profiles: %v", err))
			}
		}
	}
	label := effName
	if label == "" {
		label = newURL
	}
	a.emitLog("gui", "info", fmt.Sprintf("Subscription saved: %s", label))
	if len(res.Profiles) > 0 && a.store.GetSettings().GeoIPAutoDetect {
		go a.refreshCountriesBackground("subscriptions")
	}
	return res, nil
}

// DeleteSubscription removes a recorded feed. With alsoProfiles the
// profiles imported from it are deleted as well (matched by SubURL,
// plus the group-name fallback for legacy imports); the count of
// deleted profiles is returned. A connected profile refuses deletion
// (same contract as DeleteProfile).
func (a *App) DeleteSubscription(url string, alsoProfiles bool) (int, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return 0, fmt.Errorf("subscription URL required")
	}
	name := ""
	found := false
	for _, s := range a.store.GetSettings().Subscriptions {
		if s.URL == url {
			name, found = s.Name, true
			break
		}
	}
	if !found {
		return 0, fmt.Errorf("subscription %q not found", url)
	}
	n := 0
	if alsoProfiles {
		if _, activeID, running := a.cores.Status(); running && activeID != "" {
			for _, p := range a.store.ListProfiles() {
				if p.ID == activeID && profileFromSubscription(&p, url, name) {
					return 0, fmt.Errorf("disconnect before deleting the active profile")
				}
			}
		}
		var err error
		n, err = a.store.DeleteProfilesBySource(url, name)
		if err != nil {
			return n, err
		}
		// BUG-012: a deleted selected-but-idle profile must not linger
		// as the persisted auto-connect target.
		if active := a.store.GetSettings().ActiveProfileID; active != "" {
			if _, ok := a.store.GetProfile(active); !ok {
				if err := a.store.SetActiveProfileID(""); err != nil {
					a.emitLog("gui", "error", fmt.Sprintf("DeleteSubscription: clear active profile failed: %v", err))
				}
			}
		}
	}
	if _, err := a.store.DeleteSubscription(url); err != nil {
		return n, err
	}
	label := name
	if label == "" {
		label = url
	}
	a.emitLog("gui", "info", fmt.Sprintf("Subscription removed: %s (%d configs deleted)", label, n))
	return n, nil
}

// SetSubscriptionEnabled pauses/resumes a feed: disabled feeds stay
// listed and manually updatable, but Update All and the auto-update
// timer skip them.
func (a *App) SetSubscriptionEnabled(url string, enabled bool) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("subscription URL required")
	}
	ok, err := a.store.SetSubscriptionEnabled(url, enabled)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("subscription %q not found", url)
	}
	return nil
}

// --- GeoIP country detection (Stage 3) ---

// DetectCountry resolves and persists the country for one profile.
// Returns the updated profile. A missing DB or unresolvable address
// is not an error — the profile comes back unchanged.
func (a *App) DetectCountry(id string) (*Profile, error) {
	p, ok := a.store.GetProfile(id)
	if !ok {
		return nil, fmt.Errorf("profile %q not found", id)
	}
	updated, err := a.detectCountryForProfile(&p)
	if err != nil {
		return &p, err
	}
	if !updated {
		return &p, nil
	}
	saved, ok, err := a.store.updateProfileFields(p.ID, func(e *Profile) {
		e.CountryCode = p.CountryCode
		e.CountryName = p.CountryName
		e.CountryAt = p.CountryAt
	})
	if err != nil {
		return &p, err
	}
	if !ok {
		return &p, fmt.Errorf("profile %q is gone", id)
	}
	return &saved, nil
}

// detectCountryForProfile looks up p.Address and stamps the country
// fields on p. Returns true when anything changed (caller persists).
// Only hard DB read failures are errors; everything else degrades.
func (a *App) detectCountryForProfile(p *Profile) (bool, error) {
	if p.Address == "" {
		return false, nil
	}
	code, name, err := a.geo.Lookup(p.Address)
	if err != nil {
		return false, err
	}
	if code == "" {
		return false, nil
	}
	if p.CountryCode == code && p.CountryName == name {
		return false, nil
	}
	p.CountryCode = code
	p.CountryName = name
	now := time.Now()
	p.CountryAt = &now
	return true, nil
}

// refreshCountriesBackground runs a full refresh off the RPC path and
// logs the summary (fire-and-forget contract: never block a save/import).
func (a *App) refreshCountriesBackground(trigger string) {
	res, err := a.RefreshAllCountries()
	if err != nil {
		a.emitLog("gui", "error", fmt.Sprintf("GeoIP refresh (%s) failed: %v", trigger, err))
		return
	}
	a.emitLog("gui", "info", fmt.Sprintf("GeoIP refresh (%s): %d updated, %d skipped, %d failed",
		trigger, res.Updated, res.Skipped, res.Failed))
}

// countryLookup resolves and stamps one profile's country fields. It is
// a variable (not a direct method call) so tests can inject a slow fake
// to prove cancellation aborts the save (BUG-024).
var countryLookup = (*App).detectCountryForProfile

// refreshOneCountry resolves and persists one profile's country,
// honoring ctx (BUG-024): the resolution runs in a goroutine selected
// against ctx, and the SaveProfile is skipped when ctx ends first — a
// cancelled/timeout lookup is reported as an error, never counted as a
// success, and never written stale after the caller gave up.
func (a *App) refreshOneCountry(ctx context.Context, p Profile) (updated bool, code string, err error) {
	cur, ok := a.store.GetProfile(p.ID)
	if !ok {
		return false, "", fmt.Errorf("profile %q vanished", p.ID)
	}
	type res struct {
		updated bool
		err     error
	}
	rch := make(chan res, 1)
	// Capture the seam before spawning: the lookup goroutine may
	// outlive this call (cancel/timeout abandons it via ctx while it
	// still runs), and reading the package var inside it would race
	// a test restoring the seam after the run (BUG-RACE-001).
	lookup := countryLookup
	go func() {
		u, e := lookup(a, &cur)
		rch <- res{u, e}
	}()
	var upd bool
	select {
	case r := <-rch:
		if r.err != nil {
			return false, "", r.err
		}
		upd = r.updated
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
	if !upd {
		return false, "", nil
	}
	// Re-check before persisting: the lookup may have finished just as
	// ctx ended; saving now would resurrect a row the caller timed out.
	select {
	case <-ctx.Done():
		return false, "", ctx.Err()
	default:
	}
	// In-memory only: the batch writes the file ONCE when the whole
	// refresh is done (see RefreshAllCountries). Writing per profile
	// re-marshalled and rewrote the whole config for every profile.
	// Only the country fields are written back — [cur] was read before a
	// lookup that can take seconds, so a full-struct write here would
	// revert whatever the user saved in between (BUG-043).
	a.store.UpdateProfileCountry(cur.ID, cur.CountryCode, cur.CountryName, cur.CountryAt)
	return true, cur.CountryCode, nil
}

// refreshTotalTimeout caps one RefreshAllCountries run wall-clock
// (integration BUG-003): per-profile 5s over a 10-worker pool is
// N*5s/10 unbounded (minutes for hundreds of profiles). A variable
// (not a const) so tests can shrink it.
var refreshTotalTimeout = 60 * time.Second

// RefreshProgress is the live RefreshAllCountries state polled by the
// UI while a run is in flight (integration BUG-003).
type RefreshProgress struct {
	Done    int  `json:"done"`
	Total   int  `json:"total"`
	Running bool `json:"running"`
}

// RefreshAllCountries re-detects every profile with a worker pool capped
// at 10 and a 5s per-profile timeout, under a 60s TOTAL deadline and a
// run registered for CancelRefreshCountries (integration BUG-003). The
// caller waits; use refreshCountriesBackground from save/import paths.
// A second call while one runs fails fast instead of piling up.
func (a *App) RefreshAllCountries() (*CountryRefreshResult, error) {
	a.refreshMu.Lock()
	if a.refreshCancel != nil {
		a.refreshMu.Unlock()
		return nil, fmt.Errorf("refresh already running")
	}
	total, cancel := context.WithTimeout(context.Background(), refreshTotalTimeout)
	a.refreshCancel = cancel
	a.refreshMu.Unlock()
	defer func() {
		cancel()
		a.refreshMu.Lock()
		a.refreshCancel = nil
		a.refreshMu.Unlock()
	}()
	profiles := a.store.ListProfiles()
	a.refreshTotal.Store(int64(len(profiles)))
	a.refreshDone.Store(0)
	res := &CountryRefreshResult{}
	var mu sync.Mutex
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for _, p := range profiles {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer a.refreshDone.Add(1)
			sem <- struct{}{}
			defer func() { <-sem }()
			// Per-profile budget derives from the total scope, so the
			// 60s cap (or a Cancel) cascades into every worker instead
			// of each one lingering its own 5s afterwards.
			ctx, cancel := context.WithTimeout(total, 5*time.Second)
			defer cancel()
			updated, code, err := a.refreshOneCountry(ctx, p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					res.Failed++
					if len(res.Errors) < 20 {
						res.Errors = append(res.Errors, fmt.Sprintf("%s: lookup timed out", p.Name))
					}
					a.emitLog("gui", "warn", fmt.Sprintf("GeoIP: %q timed out", p.Name))
					return
				}
				res.Failed++
				if len(res.Errors) < 20 {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", p.Name, err))
				}
				a.emitLog("gui", "warn", fmt.Sprintf("GeoIP: %q failed: %v", p.Name, err))
				return
			}
			if updated {
				res.Updated++
				a.emitLog("gui", "info", fmt.Sprintf("GeoIP: %q -> %s", p.Name, code))
				return
			}
			res.Skipped++
		}()
	}
	wg.Wait()
	// One file write for the whole batch — and only when something
	// actually changed (an unchanged refresh must not touch the file:
	// this runs in the background, and a late write can outlive the
	// caller).
	if res.Updated > 0 {
		if err := a.store.SaveFile(); err != nil {
			a.emitLog("gui", "error", fmt.Sprintf("GeoIP: saving country results failed: %v", err))
		}
	}
	if total.Err() != nil && res.Updated+res.Skipped+res.Failed < len(profiles) {
		a.emitLog("gui", "warn", "GeoIP refresh hit the total deadline; rerun to cover the rest")
	}
	a.emitLog("gui", "info", fmt.Sprintf("GeoIP refresh done: %d updated, %d skipped, %d failed",
		res.Updated, res.Skipped, res.Failed))
	return res, nil
}

// RefreshCountriesProgress reports live RefreshAllCountries progress
// for the UI's "N/M" readout. Bound for both Refresh buttons.
func (a *App) RefreshCountriesProgress() RefreshProgress {
	a.refreshMu.Lock()
	running := a.refreshCancel != nil
	a.refreshMu.Unlock()
	return RefreshProgress{
		Done:    int(a.refreshDone.Load()),
		Total:   int(a.refreshTotal.Load()),
		Running: running,
	}
}

// CancelRefreshCountries aborts the in-flight RefreshAllCountries, if
// any. Bound for the Refresh buttons' Cancel action. Reports whether
// anything was actually cancelled.
func (a *App) CancelRefreshCountries() bool {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	if a.refreshCancel == nil {
		return false
	}
	a.refreshCancel()
	a.refreshCancel = nil
	return true
}

// ClearCountries wipes CountryCode/Name/At for all profiles.
func (a *App) ClearCountries() error {
	changed := 0
	for _, p := range a.store.ListProfiles() {
		if p.CountryCode == "" && p.CountryName == "" && p.CountryAt == nil {
			continue
		}
		// In-memory only; the file is written once below. Country fields
		// only, so a concurrent edit is not reverted (BUG-043).
		a.store.UpdateProfileCountry(p.ID, "", "", nil)
		changed++
	}
	// One file write for the whole batch (only when something changed).
	if changed > 0 {
		if err := a.store.SaveFile(); err != nil {
			return err
		}
	}
	a.emitLog("gui", "info", "GeoIP: cleared all countries")
	return nil
}

// GeoIPStatus returns DB availability + path for the UI.
func (a *App) GeoIPStatus() (*GeoIPStatus, error) {
	st := &GeoIPStatus{Available: a.geo.Available(), Path: a.geo.DBPath()}
	if err := a.geo.LastErr(); err != nil && !st.Available {
		st.Error = err.Error()
	}
	return st, nil
}

// RecentLogs returns the last n buffered log lines.
func (a *App) RecentLogs(n int) []LogEntry {
	a.logMu.RLock()
	defer a.logMu.RUnlock()
	if n <= 0 || n > len(a.logBuf) {
		n = len(a.logBuf)
	}
	out := make([]LogEntry, n)
	copy(out, a.logBuf[len(a.logBuf)-n:])
	return out
}

// AppVersion returns the GUI version.
func (a *App) AppVersion() string { return appVersion }

// OpenURL opens an http(s) URL in the OS default browser.
// The Wails3 runtime handles platform differences.
func (a *App) OpenURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing to open non-http(s) URL: %q", raw)
	}
	return a.browserOpen(raw)
}

// browserOpen opens a URL via the Wails3 BrowserManager
// (application.App.Browser.OpenURL -> internal/browser, which uses
// rundll32 url.dll on Windows, open on darwin, xdg-open elsewhere).
// Falls back to the equivalent OS command when the Wails handle is nil
// (e.g. unit tests that construct App without main.go wiring).
func (a *App) browserOpen(raw string) error {
	a.mu.Lock()
	w := a.wailsApp
	a.mu.Unlock()
	if w != nil && w.Browser != nil {
		return w.Browser.OpenURL(raw)
	}
	return openURLFallback(raw)
}

// openURLFallback launches the OS default browser without the Wails runtime.
// It is a variable (not a plain func) so tests can spy on it and prove
// the updater never auto-opens downloads (BUG-010).
var openURLFallback = func(raw string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	case "darwin":
		cmd = exec.Command("open", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck
	return nil
}

// versionCache is a single-entry version cache keyed on the binary path
// it was read from (P3-15).
type versionCache struct {
	mu      sync.Mutex
	path    string
	version string
}

// --- About diagnostics ---

// CoreVersion runs "<resolved nekobox_core path> sing-box version" and
// returns the first non-empty stdout line ("" on any error). Only
// non-empty results are cached per resolved core path (BUG-015: a
// transient failure retries on the next call); changing corePath in
// Settings invalidates it. The "sing-box" subcommand is required: a bare
// "version" arg would fall through to the core-server path when
// InTheNameOf is set.
func (a *App) CoreVersion() string {
	p := a.singbox.getCorePath()
	a.coreVer.mu.Lock()
	if a.coreVer.path != p {
		a.coreVer.version = ""
		a.coreVer.path = p
	}
	cached := a.coreVer.version
	a.coreVer.mu.Unlock()
	if cached != "" {
		return cached
	}
	bin, err := a.singbox.resolveCore()
	if err != nil {
		return ""
	}
	v := runVersionQuery(bin, "sing-box", "version")
	// BUG-015: only cache successful lookups — a transient "" (busy
	// binary, timeout) must not poison the cache; the next call retries.
	a.coreVer.mu.Lock()
	if v != "" {
		a.coreVer.version = v
	}
	a.coreVer.path = p
	a.coreVer.mu.Unlock()
	return v
}

// UpstreamSingBoxVersion runs the official sing-box.exe "version"
// command, cached per resolved path like CoreVersion (transient ""
// never cached, next call retries). Empty when no upstream binary is
// installed — the official CLI is optional, only CoreEngine=upstream
// needs it.
func (a *App) UpstreamSingBoxVersion() string {
	if a.upstreamMgr == nil || a.store == nil {
		return ""
	}
	bin, err := a.upstreamMgr.resolveUpstreamCore(a.store.GetSettings())
	if err != nil {
		return ""
	}
	a.upstreamVer.mu.Lock()
	if a.upstreamVer.path != bin {
		a.upstreamVer.version = ""
		a.upstreamVer.path = bin
	}
	cached := a.upstreamVer.version
	a.upstreamVer.mu.Unlock()
	if cached != "" {
		return cached
	}
	v := runVersionQuery(bin, "version")
	a.upstreamVer.mu.Lock()
	if v != "" {
		a.upstreamVer.version = v
	}
	a.upstreamVer.path = bin
	a.upstreamVer.mu.Unlock()
	return v
}

// XrayVersion runs "<resolved xray path> version" and returns the first
// non-empty stdout line, cached per resolved path like CoreVersion
// (transient "" never cached, next call retries).
func (a *App) XrayVersion() string {
	p := a.xray.getXrayPath()
	a.xrayVer.mu.Lock()
	if a.xrayVer.path != p {
		a.xrayVer.version = ""
		a.xrayVer.path = p
	}
	cached := a.xrayVer.version
	a.xrayVer.mu.Unlock()
	if cached != "" {
		return cached
	}
	bin, err := a.xray.resolveXray()
	if err != nil {
		return ""
	}
	v := runVersionQuery(bin, "version")
	// BUG-015: same no-poison rule as CoreVersion.
	a.xrayVer.mu.Lock()
	if v != "" {
		a.xrayVer.version = v
	}
	a.xrayVer.path = p
	a.xrayVer.mu.Unlock()
	return v
}

// RuntimeInfo reports the GUI process runtime for the About diagnostics
// card (OS + Go toolchain; the cores report their own versions above).
func (a *App) RuntimeInfo() map[string]string {
	return map[string]string{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "go": runtime.Version()}
}

// runVersionQuery runs bin with args, kills it after 3 seconds, and parses
// the first non-empty stdout line. Returns "" on any error (callers show
// "unknown"). Stdout only: nekobox_core logs to stderr on every launch.
func runVersionQuery(bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	setHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// OpenConfigFolder opens the directory holding config.json in Explorer
// (Windows) or the OS default file handler elsewhere.
func (a *App) OpenConfigFolder() error {
	dir := a.store.Dir()
	if dir == "" {
		return fmt.Errorf("config directory unknown")
	}
	if runtime.GOOS == "windows" {
		return exec.Command("explorer", dir).Start()
	}
	return openURLFallback(dir)
}

// ShouldHideOnClose is the single source of truth for the window-X
// decision: true means hide to tray instead of quitting. Read fresh on
// every close (ConfigStore is mutex-guarded) so a Settings toggle takes
// effect immediately without an app restart.
func (a *App) ShouldHideOnClose() bool {
	return a.store.GetSettings().CloseToTray
}

// DebugWindowClose is a DEBUG-ONLY test hook that runs the same close
// decision branch the OS close event runs, without sending WM_CLOSE.
// Returns "hide" or "quit".
func (a *App) DebugWindowClose() string {
	if a.ShouldHideOnClose() {
		return "hide"
	}
	return "quit"
}

// SetCloseToTray persists the setting and applies the tray visibility
// change immediately: Show on an existing tray, Hide likewise, or a
// full rebuild (identical menu+callbacks via trayBuilder) when the
// tray was never created because the app launched with false.
func (a *App) SetCloseToTray(enabled bool) error {
	// BUG-043: single-field atomic RMW on the store so a concurrent
	// subscription add/touch cannot be clobbered by a stale full-struct
	// copy-modify-SaveSettings.
	if err := a.store.SetCloseToTray(enabled); err != nil {
		return err
	}
	a.mu.Lock()
	t := a.tray
	builder := a.trayBuilder
	a.mu.Unlock()
	if enabled {
		if t == nil {
			if builder == nil {
				return nil
			}
			t = builder()
			a.mu.Lock()
			a.tray = t
			a.mu.Unlock()
			a.emitLog("tray", "info", "[tray] re-enabled after closeToTray=true")
		} else {
			t.Show()
		}
		a.emitLog("tray", "info", "[tray] SetCloseToTray(true) -> showing")
	} else {
		if t == nil {
			return nil
		}
		t.Hide()
		a.emitLog("tray", "info", "[tray] SetCloseToTray(false) -> hiding")
	}
	return nil
}

// Shutdown stops cores; called on app exit. A leftover system-DNS
// override is restored best-effort first (BUG-009: quitting with
// system DNS applied must not leave it installed); an auto-enabled
// system proxy is likewise restored best-effort, but only when this
// session enabled it (autoProxyOn) — a manually-enabled proxy is left
// untouched. Failures are ignored so shutdown never blocks. cores.Stop
// aborts any in-flight Start via the epoch instead of blocking on its
// probe.
func (a *App) Shutdown() {
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		return
	}
	a.quitting = true
	a.mu.Unlock()
	if a.getAutoProxy() {
		_ = a.ClearSystemProxy()
		a.setAutoProxy(false)
	}
	_ = a.cores.SetSystemDNS(true)
	_ = a.cores.Stop()
}

// --- Advanced Mode Phase E: core config section editors ---
//
// The core reads its config from the files the managers render.
// Phase E lets the Settings -> Advanced tab edit four sections of
// that rendered config (route rules, DNS rules, inbound, experimental)
// as JSON. Overrides live in Settings.CoreOverrides (app-wide, keyed
// by section name); the managers merge the matching entry into the
// rendered config before writing it, so the core server's own schema
// is never touched from here.

// coreConfigSections are the valid Phase E section names.
var coreConfigSections = []string{"route", "dns", "inbound", "experimental"}

// validCoreSection reports whether section names a Phase E section.
func validCoreSection(section string) bool {
	for _, s := range coreConfigSections {
		if s == section {
			return true
		}
	}
	return false
}

// sectionConfigKey maps a Phase E section to the rendered config key
// for the given core. Experimental is sing-box-only: on xray it
// reports ok=false and callers must refuse with a clear error.
func sectionConfigKey(section string, xray bool) (string, bool) {
	switch section {
	case "route":
		if xray {
			return "routing", true
		}
		return "route", true
	case "dns":
		return "dns", true
	case "inbound":
		return "inbounds", true
	case "experimental":
		if xray {
			return "", false
		}
		return "experimental", true
	}
	return "", false
}

// validateCoreSectionJSON parses text and applies the light schema
// check shared with the frontend Validate button: inbound must be a
// JSON array (it renders as "inbounds": [...]), every other section a
// JSON object.
func validateCoreSectionJSON(section, text string) (any, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, fmt.Errorf("empty JSON")
	}
	var v any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if section == "inbound" {
		if _, ok := v.([]any); !ok {
			return nil, fmt.Errorf("inbound must be a JSON array")
		}
		return v, nil
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("%s must be a JSON object", section)
	}
	return v, nil
}

// mergeCoreOverrides decodes stored overrides into the rendered config
// map. Unknown section names (hand-edited config only — Set validates)
// and sing-box-only sections on xray are skipped; corrupt entries fail
// fast so Connect never starts on half-merged config. The full raw
// override (CoreOverrides["*"], Phase G) is NOT merged here — the
// builders check it first and let it replace the whole config.
func mergeCoreOverrides(cfg map[string]any, overrides map[string]json.RawMessage, xray bool) error {
	for sec, raw := range overrides {
		if sec == rawOverrideKey {
			continue
		}
		key, ok := sectionConfigKey(sec, xray)
		if !ok {
			continue
		}
		t := strings.TrimSpace(string(raw))
		if t == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(t), &v); err != nil {
			return fmt.Errorf("coreOverrides[%q]: %w", sec, err)
		}
		cfg[key] = v
	}
	return nil
}

// rawOverrideKey is the CoreOverrides key holding the Phase G full raw
// config. Storage choice (documented per the Phase G prompt): the blob
// lives in CoreOverrides["*"] rather than a dedicated Profile field
// because Phase E overrides are app-wide — a per-profile field would
// create two override scopes with ambiguous merge order, while one map
// keeps a single scope and deterministic precedence (raw wins).
const rawOverrideKey = "*"

// rawCoreOverride decodes the full-config override when present.
// Returns ok=false when absent/blank; corrupt entries are an error.
func rawCoreOverride(overrides map[string]json.RawMessage) (map[string]any, bool, error) {
	raw, ok := overrides[rawOverrideKey]
	if !ok || strings.TrimSpace(string(raw)) == "" {
		return nil, false, nil
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, false, fmt.Errorf("coreOverrides[%q]: %w", rawOverrideKey, err)
	}
	return full, true, nil
}

// resolveConfigTarget picks the profile and core the section editors
// follow: the live connection when running, otherwise the stored
// active profile (else the first profile) so the editors still render
// defaults while idle.
func (a *App) resolveConfigTarget() (*Profile, Settings) {
	settings := a.store.GetSettings()
	if _, id, running := a.cores.Status(); running {
		if p, ok := a.store.GetProfile(id); ok {
			return &p, settings
		}
	}
	if id := settings.ActiveProfileID; id != "" {
		if p, ok := a.store.GetProfile(id); ok {
			return &p, settings
		}
	}
	if all := a.store.ListProfiles(); len(all) > 0 {
		return &all[0], settings
	}
	return nil, settings
}

// defaultConfigSection renders the manager default for one section
// (no override applied) and pretty-prints it. Absent keys (e.g. no
// "dns" block in the minimal config) yield "{}" as a starting point.
func defaultConfigSection(p *Profile, settings Settings, section string, xray bool) (string, error) {
	key, ok := sectionConfigKey(section, xray)
	if !ok {
		return "", fmt.Errorf("section %q requires the sing-box core", section)
	}
	var raw string
	var err error
	if xray {
		addr := settings.XrayAPIAddress
		if addr == "" {
			addr = "127.0.0.1:10085"
		}
		raw, err = BuildXrayConfig(p, settings, addr)
	} else {
		raw, err = BuildSingBoxConfig(p, settings)
	}
	if err != nil {
		return "", err
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", fmt.Errorf("render default: %w", err)
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return "{}", nil
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render default: %w", err)
	}
	return string(out), nil
}

// GetCoreConfigSection returns the current JSON for a named section
// ("route", "dns", "inbound", "experimental") of the active core's
// config: the stored override when present, else the manager default.
func (a *App) GetCoreConfigSection(section string) (string, error) {
	if !validCoreSection(section) {
		return "", fmt.Errorf("unknown section %q (want route|dns|inbound|experimental)", section)
	}
	p, settings := a.resolveConfigTarget()
	if p == nil {
		return "", fmt.Errorf("no profiles: add a profile first")
	}
	xray := p.Core.IsXray()
	key, ok := sectionConfigKey(section, xray)
	if !ok {
		return "", fmt.Errorf("section %q requires the sing-box core", section)
	}
	// Phase G precedence: a stored raw override is authoritative, so
	// section editors slice it out of the raw blob (a stale per-section
	// entry underneath is ignored, never merged on top).
	if full, ok, err := rawCoreOverride(settings.CoreOverrides); err != nil {
		return "", fmt.Errorf("stored raw override is invalid (reset it): %w", err)
	} else if ok {
		v, present := full[key]
		if !present || v == nil {
			return "{}", nil
		}
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return "", fmt.Errorf("stored raw override: %w", err)
		}
		return string(out), nil
	}
	if raw, ok := settings.CoreOverrides[section]; ok {
		t := strings.TrimSpace(string(raw))
		if t != "" {
			v, err := validateCoreSectionJSON(section, t)
			if err != nil {
				return "", fmt.Errorf("stored override for %q is invalid (reset it): %w", section, err)
			}
			out, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return "", fmt.Errorf("stored override for %q: %w", section, err)
			}
			return string(out), nil
		}
	}
	return defaultConfigSection(p, settings, section, xray)
}

// SetCoreConfigSection validates the JSON for a section, saves it into
// Settings.CoreOverrides, and returns the normalized section JSON.
// It does not restart anything: the frontend offers a Disconnect +
// Connect reload after a successful save.
func (a *App) SetCoreConfigSection(section, jsonText string) (string, error) {
	if !validCoreSection(section) {
		return "", fmt.Errorf("unknown section %q (want route|dns|inbound|experimental)", section)
	}
	v, err := validateCoreSectionJSON(section, jsonText)
	if err != nil {
		return "", err
	}
	norm, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("normalize: %w", err)
	}
	if err := a.store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m[section] = norm
		return true
	}); err != nil {
		return "", err
	}
	return string(norm), nil
}

// ResetCoreConfigSection clears the per-section override so the
// manager renders the default again. Missing entries are a no-op.
func (a *App) ResetCoreConfigSection(section string) error {
	if !validCoreSection(section) {
		return fmt.Errorf("unknown section %q (want route|dns|inbound|experimental)", section)
	}
	return a.store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		if _, ok := m[section]; !ok {
			return false
		}
		delete(m, section)
		return true
	})
}

// --- Advanced Mode Phase F: backup, restore, reset ---
//
// ExportAllData snapshots settings, profiles, subscriptions and
// CoreOverrides into one versioned JSON blob; ImportAllData validates
// a blob fully before replacing anything; ViewRawConfig renders the
// exact config the active core would receive; ResetToDefaults wipes
// back to DefaultSettings behind a confirm token.

// ExportAllData returns a single JSON blob with settings, profiles,
// subscriptions and every CoreOverrides section, for the user to save
// anywhere.
func (a *App) ExportAllData() (string, error) {
	s := a.store.GetSettings()
	blob := BackupBlob{
		Version:       backupVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Settings:      s,
		Profiles:      a.store.ListProfiles(),
		Subscriptions: s.Subscriptions,
		CoreOverrides: s.CoreOverrides,
	}
	data, err := json.MarshalIndent(blob, "", "  ")
	if err != nil {
		return "", fmt.Errorf("export: %w", err)
	}
	return string(data), nil
}

// backupFile is the validated shape of an import blob: every section
// stays raw until its own check passes, so a bad field aborts before
// the store is touched.
type backupFile struct {
	Version       int             `json:"version"`
	Settings      json.RawMessage `json:"settings"`
	Profiles      json.RawMessage `json:"profiles"`
	Subscriptions json.RawMessage `json:"subscriptions"`
	CoreOverrides json.RawMessage `json:"coreOverrides"`
}

// ImportAllData replaces settings, profiles, subscriptions and
// CoreOverrides with the given blob after validation. On any error
// nothing changes; each error names the bad field.
func (a *App) ImportAllData(blob string) error {
	var f backupFile
	if err := json.Unmarshal([]byte(blob), &f); err != nil {
		return fmt.Errorf("backup: invalid JSON: %w", err)
	}
	if f.Version != backupVersion {
		return fmt.Errorf("backup: bad \"version\" %d (want %d)", f.Version, backupVersion)
	}
	if len(f.Settings) == 0 {
		return fmt.Errorf("backup: missing \"settings\"")
	}
	var s Settings
	if err := json.Unmarshal(f.Settings, &s); err != nil {
		return fmt.Errorf("backup: bad \"settings\": %w", err)
	}
	if err := validateMixedPort(s.MixedPort); err != nil {
		return fmt.Errorf("backup: bad \"settings.mixedPort\": %w", err)
	}
	var profiles []Profile
	if len(f.Profiles) > 0 {
		if err := json.Unmarshal(f.Profiles, &profiles); err != nil {
			return fmt.Errorf("backup: bad \"profiles\": %w", err)
		}
	}
	for i := range profiles {
		p := &profiles[i]
		if p.ID == "" {
			id, err := newID()
			if err != nil {
				return fmt.Errorf("backup: bad \"profiles[%d]\": %w", i, err)
			}
			p.ID = id
		}
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("backup: bad \"profiles[%d].name\": empty", i)
		}
		if err := validateProfileForSave(*p); err != nil {
			return fmt.Errorf("backup: bad \"profiles[%d]\": %w", i, err)
		}
	}
	var subs []Subscription
	if len(f.Subscriptions) > 0 {
		if err := json.Unmarshal(f.Subscriptions, &subs); err != nil {
			return fmt.Errorf("backup: bad \"subscriptions\": %w", err)
		}
	}
	for i := range subs {
		if strings.TrimSpace(subs[i].URL) == "" {
			return fmt.Errorf("backup: bad \"subscriptions[%d].url\": empty", i)
		}
	}
	overrides := map[string]json.RawMessage{}
	if len(f.CoreOverrides) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(f.CoreOverrides, &raw); err != nil {
			return fmt.Errorf("backup: bad \"coreOverrides\": %w", err)
		}
		for sec, val := range raw {
			if sec != rawOverrideKey && !validCoreSection(sec) {
				return fmt.Errorf("backup: bad \"coreOverrides[%s]\": unknown section", sec)
			}
			v, err := validateCoreSectionJSON(sec, string(val))
			if err != nil {
				return fmt.Errorf("backup: bad \"coreOverrides[%s]\": %w", sec, err)
			}
			norm, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("backup: bad \"coreOverrides[%s]\": %w", sec, err)
			}
			overrides[sec] = norm
		}
	}
	// Top-level subscriptions are the source of truth.
	s.Subscriptions = subs
	s.CoreOverrides = overrides
	old := a.store.GetSettings()
	if err := a.store.restoreAll(s, profiles); err != nil {
		return err
	}
	a.singbox.setCorePath(s.CorePath)
	a.xray.setXrayPath(s.XrayPath)
	if s.GeoIPDBPath != old.GeoIPDBPath {
		a.geo.Configure(s.GeoIPDBPath)
	}
	return nil
}

// renderActiveConfig renders the exact config JSON the given profile
// would receive on the next Connect, overrides merged in.
func renderActiveConfig(p *Profile, settings Settings) (string, error) {
	if p.Core.IsXray() {
		addr := settings.XrayAPIAddress
		if addr == "" {
			addr = "127.0.0.1:10085"
		}
		return BuildXrayConfig(p, settings, addr)
	}
	return BuildSingBoxConfig(p, settings)
}

// ViewRawConfig returns the exact config JSON the active core would
// receive on the next Connect, CoreOverrides merged in. It is empty
// (no error) when no profile is connected.
func (a *App) ViewRawConfig() (string, error) {
	_, id, running := a.cores.Status()
	if !running {
		return "", nil
	}
	p, ok := a.store.GetProfile(id)
	if !ok {
		return "", nil
	}
	return renderActiveConfig(&p, a.store.GetSettings())
}

// validateRawConfig runs the Phase G light schema over a full config
// object: the same per-section rules Phase E enforces (inbounds must
// be an array; route/routing/dns/experimental must be objects when
// present), core-agnostic so one blob works for either engine.
func validateRawConfig(v map[string]any) error {
	if inb, ok := v["inbounds"]; ok && inb != nil {
		if _, ok := inb.([]any); !ok {
			return fmt.Errorf("\"inbounds\" must be a JSON array")
		}
	}
	for _, k := range []string{"route", "routing", "dns", "experimental"} {
		if sec, ok := v[k]; ok && sec != nil {
			if _, ok := sec.(map[string]any); !ok {
				return fmt.Errorf("%q must be a JSON object", k)
			}
		}
	}
	return nil
}

// GetRawConfig returns the complete rendered config for the active
// profile, every Phase E override (and the raw override itself)
// already merged in, as pretty-printed JSON.
func (a *App) GetRawConfig() (string, error) {
	p, settings := a.resolveConfigTarget()
	if p == nil {
		return "", fmt.Errorf("no profiles: add a profile first")
	}
	raw, err := renderActiveConfig(p, settings)
	if err != nil {
		return "", err
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	return string(out), nil
}

// SetRawConfig replaces the entire config JSON for the active
// profile: syntax plus the light schema are validated, the blob is
// stored as CoreOverrides["*"], and every Phase E section override
// is cleared first so the raw edit is authoritative. Returns the
// canonical pretty-printed form. An empty input clears the raw
// override (the Reset path — no extra bound method needed) and
// returns the re-rendered section-by-section default.
func (a *App) SetRawConfig(jsonText string) (string, error) {
	if strings.TrimSpace(jsonText) == "" {
		if err := a.store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
			if _, ok := m[rawOverrideKey]; !ok {
				return false
			}
			delete(m, rawOverrideKey)
			return true
		}); err != nil {
			return "", err
		}
		return a.GetRawConfig()
	}
	var full map[string]any
	if err := json.Unmarshal([]byte(jsonText), &full); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	if err := validateRawConfig(full); err != nil {
		return "", err
	}
	norm, err := json.Marshal(full)
	if err != nil {
		return "", fmt.Errorf("normalize: %w", err)
	}
	if err := a.store.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m[rawOverrideKey] = norm
		for _, sec := range coreConfigSections {
			delete(m, sec)
		}
		return true
	}); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return "", fmt.Errorf("normalize: %w", err)
	}
	return string(pretty), nil
}

// ReloadCoreNow stops the active core if running, then reconnects the
// active profile. Used after Phase E/Phase G saves. Test hook:
// coreStarter (normally nil) replaces the real Start like Connect.
func (a *App) ReloadCoreNow() error {
	_, id, running := a.cores.Status()
	if running {
		if err := a.cores.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	if id == "" {
		id = a.store.GetSettings().ActiveProfileID
	}
	if id == "" {
		return fmt.Errorf("no active profile to reload")
	}
	p, ok := a.store.GetProfile(id)
	if !ok {
		return fmt.Errorf("active profile %q not found", id)
	}
	start := a.coreStarter
	if start == nil {
		start = func(p *Profile, s Settings) error { return a.cores.Start(p, s) }
	}
	if err := start(&p, a.store.GetSettings()); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	a.refreshTray()
	return nil
}

// resetAllToken is the literal confirm token ResetToDefaults requires.
const resetAllToken = "RESET-ALL"

// ResetToDefaults clears settings, profiles, subscriptions and
// CoreOverrides back to DefaultSettings. Anything but the literal
// "RESET-ALL" token is refused so a stray call cannot wipe data.
func (a *App) ResetToDefaults(confirm string) error {
	if confirm != resetAllToken {
		return fmt.Errorf("refusing reset without the %q confirm token", resetAllToken)
	}
	if err := a.store.restoreAll(DefaultSettings(), nil); err != nil {
		return err
	}
	a.singbox.setCorePath("")
	a.xray.setXrayPath("")
	a.geo.Configure("")
	return nil
}
