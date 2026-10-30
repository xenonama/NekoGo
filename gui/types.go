package main

import (
	"encoding/json"
	"time"
)

// CoreType selects which proxy engine runs a profile.
type CoreType int

const (
	// CoreSingBox runs the profile with nekobox_core (sing-box engine).
	CoreSingBox CoreType = iota
	// CoreXray runs the profile with xray.exe (Xray-core).
	CoreXray
	// CoreSpoof routes trojan/vless through the sni-spoof core:
	// sing-box transports raw TLS to sni-spoof.exe, which injects
	// fake ClientHello via WinDivert. MODE is always "SNI Only".
	CoreSpoof
)

// String returns a human readable core name.
func (c CoreType) String() string {
	switch c {
	case CoreXray:
		return "xray"
	case CoreSpoof:
		return "spoof"
	default:
		return "sing-box"
	}
}

// IsXray reports whether the core is Xray.
func (c CoreType) IsXray() bool { return c == CoreXray }

// IsSpoof reports whether the core is the SNI-spoof engine.
func (c CoreType) IsSpoof() bool { return c == CoreSpoof }

// Profile is the GUI-side canonical profile format. It is deliberately a
// superset: sing-box profiles are translated to a sing-box outbound JSON
// snippet, Xray profiles are translated to an Xray config.json by
// xray_manager.go.
type Profile struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Core     CoreType `json:"core"`
	Protocol string   `json:"protocol"` // vless, vmess, trojan, shadowsocks, ...

	// Server endpoint.
	Address string `json:"address"`
	Port    int    `json:"port"`

	// Credentials. Meaning depends on Protocol:
	// vless/vmess: UUID, trojan: password, shadowsocks: password, etc.
	IDOrPassword string `json:"idOrPassword,omitempty"`
	// Shadowsocks method, e.g. aes-256-gcm.
	Method string `json:"method,omitempty"`
	// VMess alterId (legacy) and security (auto/aes-128-gcm/...).
	AlterID  int    `json:"alterId,omitempty"`
	Security string `json:"security,omitempty"`

	// Transport / TLS.
	Network        string `json:"network,omitempty"` // tcp, ws, grpc, h2, kcp, quic, xhttp
	TLS            bool   `json:"tls,omitempty"`
	ServerName     string `json:"serverName,omitempty"`     // SNI
	Fingerprint    string `json:"fingerprint,omitempty"`    // uTLS fingerprint
	RealityEnabled bool   `json:"realityEnabled,omitempty"` // Xray REALITY
	RealityPubKey  string `json:"realityPublicKey,omitempty"`
	RealityShortID string `json:"realityShortId,omitempty"`
	RealitySpiderX string `json:"realitySpiderX,omitempty"`
	Flow           string `json:"flow,omitempty"` // xtls-rprx-vision, etc.
	Path           string `json:"path,omitempty"` // ws/h2/grpc path or serviceName
	Host           string `json:"host,omitempty"` // ws/h2 host header
	ALPN           string `json:"alpn,omitempty"`
	AllowInsecure  bool   `json:"allowInsecure,omitempty"`
	// TLS fingerprint preset (chrome, firefox, safari, ios, android,
	// edge, randomized, or "none"). Takes precedence over the legacy
	// Fingerprint field; empty/"none" keeps legacy behavior.
	TLSFingerprint string `json:"tlsFingerprint,omitempty"`
	// TLS fragment mode: ""/"off" disables, "tlshello" fragments the
	// TLS hello, "1-3" fragments packets 1-3 (xray packets range).
	TLSFragment string `json:"tlsFragment,omitempty"`
	// Fragment length range, e.g. "10-20" (xray finalmask length).
	TLSFragmentLen string `json:"tlsFragmentLen,omitempty"`
	// Fragment interval in ms (xray finalmask delay upper bound).
	TLSFragmentInt int `json:"tlsFragmentInt,omitempty"`
	// Mux (connection multiplexing): disabled when false. sing-box
	// renders a multiplex block, xray a mux block (concurrency).
	MuxEnabled bool `json:"muxEnabled,omitempty"`
	// Mux sub-protocol for sing-box: h2mux (default), smux, yamux.
	// Empty means the default; xray ignores it.
	MuxProtocol string `json:"muxProtocol,omitempty"`
	// Mux concurrency: xray mux concurrency and sing-box
	// max_connections. 0 means the core default (xray renders 8).
	MuxConcurrency int `json:"muxConcurrency,omitempty"`

	// For sing-box profiles this may carry a raw outbound JSON object that
	// is merged verbatim into the generated sing-box config. For Xray
	// profiles it may carry a raw Xray outbound JSON object.
	RawOutbound string `json:"rawOutbound,omitempty"`

	// Test URL override.
	TestURL string `json:"testUrl,omitempty"`

	// Aether (PattNG port): the WARP tunnel engine settings, only used
	// when Protocol is "aether". One aether.exe process runs per
	// connect (see aether_manager.go); the main core dials its SOCKS
	// listener. Typed settings live in Aether, imported PattNG share
	// links carry the same fields (gui/aether.go).
	Aether aetherSettings `json:"aether,omitempty"`

	// Last measured latency in ms (0 = never tested).
	LastLatency int `json:"lastLatency,omitempty"`
	// When LastLatency was measured (nil = never tested). Pointer so
	// encoding/json omitempty actually omits the unset value (BUG-017):
	// a plain time.Time never omits and serialized as year-1.
	LastTestedAt *time.Time `json:"lastTestedAt,omitempty"`

	// GeoIP country detection (Stage 3): ISO-3166 alpha-2 uppercase
	// (e.g. "DE"), empty when unknown or the DB is missing.
	CountryCode string `json:"countryCode,omitempty"`
	// Localized English country name from the DB.
	CountryName string `json:"countryName,omitempty"`
	// When the lookup last ran (for staleness, nil = never).
	// Pointer for the same BUG-017 omitempty reason as LastTestedAt.
	CountryAt *time.Time `json:"countryAt,omitempty"`

	// Subscription group name (empty for manual profiles).
	Group string `json:"group,omitempty"`
	// Source subscription URL (empty for manual profiles or legacy
	// imports recorded before this field existed). Lets edits and
	// deletes match a feed's profiles precisely; the group name is the
	// fallback for legacy rows.
	SubURL string `json:"subUrl,omitempty"`
	// Last successful connect, RFC3339 (empty if never connected).
	LastConnectedAt string `json:"lastConnectedAt,omitempty"`
}

// Stats is a core-agnostic traffic snapshot.
type Stats struct {
	Uplink   int64 `json:"uplink"`
	Downlink int64 `json:"downlink"`
	// Per-tag breakdown when the core provides it.
	UplinkByTag   map[string]int64 `json:"uplinkByTag,omitempty"`
	DownlinkByTag map[string]int64 `json:"downlinkByTag,omitempty"`
	Running       bool             `json:"running"`
	Core          string           `json:"core"`
	// ApiDown flags that the core process is alive but its stats API
	// is unreachable, so uplink/downlink are stale (not real zeros).
	ApiDown bool `json:"apiDown,omitempty"`
}

// TestResult is the outcome of a latency/connectivity probe.
type TestResult struct {
	OutboundTag string `json:"outboundTag"`
	LatencyMs   int32  `json:"latencyMs"`
	Error       string `json:"error,omitempty"`
}

// IPTestResult is one outbound's egress IP + country (Advanced Phase C).
// The core reports the ISO-2 code; Country is the English name filled
// best-effort from the local GeoIP DB (empty when unavailable).
type IPTestResult struct {
	OutboundTag string `json:"outboundTag,omitempty"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CC          string `json:"cc,omitempty"`
	Error       string `json:"error,omitempty"`
}

// SpeedTestResult is a download/upload measurement (Advanced Phase C).
// Mbps fields parse the core's "12.34Mbps"/"5.67Kbps"/"1.23Gbps"
// strings; absent directions stay 0 and are omitted (xray measures
// download only).
type SpeedTestResult struct {
	DownloadMbps  float64 `json:"downloadMbps,omitempty"`
	UploadMbps    float64 `json:"uploadMbps,omitempty"`
	LatencyMs     int     `json:"latencyMs,omitempty"`
	ServerName    string  `json:"serverName,omitempty"`
	ServerCountry string  `json:"serverCountry,omitempty"`
	OutboundTag   string  `json:"outboundTag,omitempty"`
	Cancelled     bool    `json:"cancelled,omitempty"`
	Error         string  `json:"error,omitempty"`
}

// URLTestResult is a single URL probe through the active core.
type URLTestResult struct {
	URL       string `json:"url"`
	LatencyMs int    `json:"latencyMs"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// LastNetworkResults bundles the last-known tool results without
// running anything: sing-box reporter reads (draining) or the xray
// manager's in-memory cache. Nil sections mean "no data yet".
type LastNetworkResults struct {
	IP           *IPTestResult     `json:"ip,omitempty"`
	Speed        *SpeedTestResult  `json:"speed,omitempty"`
	SpeedRunning bool              `json:"speedRunning"`
	URL          *URLTestResult    `json:"url,omitempty"`
	Country      []SpeedTestResult `json:"country,omitempty"`
}

// WgKeyPair is a generated WireGuard keypair (Advanced Phase D).
// PrivateKey is secret material: the UI warns against sharing it.
type WgKeyPair struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

// Connection describes one active proxied connection (sing-box only;
// Xray exposes no connection table via its API).
type Connection struct {
	ID       string `json:"id"`
	Outbound string `json:"outbound"`
	Network  string `json:"network"`
	Dest     string `json:"dest"`
	Protocol string `json:"protocol"`
	Domain   string `json:"domain"`
	Process  string `json:"process"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

// ConnectionList is the ListConnections result. Truncated is true when
// the server capped the list at the newest 500 entries (P3-3).
type ConnectionList struct {
	Connections []Connection `json:"connections"`
	Truncated   bool         `json:"truncated"`
}

// Settings are global GUI settings persisted to config.json.
type Settings struct {
	Theme     string `json:"theme"`     // dark | light
	MixedPort int    `json:"mixedPort"` // HTTP inbound port (SOCKS listens on MixedPort+1)
	// InboundAddress is the listen address for local inbounds
	// ("127.0.0.1" default; "0.0.0.0" shares on the LAN). Empty
	// means loopback. Validated + normalized on save.
	InboundAddress string `json:"inboundAddress,omitempty"`
	// DnsRemote/DnsDirect are upstream DNS servers rendered into the
	// sing-box dns block (DoH URLs like https://8.8.8.8/dns-query,
	// plain IPs, or "local"). Empty (default) keeps the previous
	// system-DNS behavior: no dns block is rendered at all.
	DnsRemote string `json:"dnsRemote,omitempty"`
	DnsDirect string `json:"dnsDirect,omitempty"`
	// Global fragment (TLS hello fragmentation) override: when
	// FragmentEnabled, every mapped profile renders with these
	// fragment options instead of its own (raw-JSON profiles are
	// exempt — raw bypasses the builders). Applies on reconnect.
	FragmentEnabled bool `json:"fragmentEnabled,omitempty"`
	// Fragment mode: "tlshello" (default when empty) or an "N-M"
	// packets range.
	FragmentMode string `json:"fragmentMode,omitempty"`
	// Fragment length range, e.g. "10-20" (default when empty).
	FragmentLength string `json:"fragmentLength,omitempty"`
	// Fragment interval in ms (xray delay upper bound).
	FragmentInterval int `json:"fragmentInterval,omitempty"`
	// Global uTLS fingerprint override: when FingerprintEnabled,
	// every TLS-rendering profile uses FingerprintValue instead of
	// its own (raw-JSON profiles exempt). Applies on reconnect.
	FingerprintEnabled bool   `json:"fingerprintEnabled,omitempty"`
	FingerprintValue   string `json:"fingerprintValue,omitempty"`
	TProxyPort         int    `json:"tproxyPort"` // 0 = disabled
	TunEnabled         bool   `json:"tunEnabled"` // sing-box TUN
	// TunStack selects the sing-box TUN network stack.
	// system | gvisor | mixed; empty means the default (system).
	// "system" hands packets to the OS stack; "gvisor" runs a whole
	// TCP/IP stack in user space and costs roughly 2-5x the CPU per
	// byte on Windows, so it stays opt-in.
	TunStack        string `json:"tunStack"`
	SystemProxy     bool   `json:"systemProxy"`     // remembered OS-proxy switch: Connect auto-ensures the OS proxy only when true (see ensureSystemProxy)
	ActiveProfileID string `json:"activeProfileId"` // last used profile
	XrayAPIAddress  string `json:"xrayApiAddress"`  // e.g. 127.0.0.1:10085
	SingBoxRPCAddr  string `json:"singboxRpcAddr"`  // e.g. 127.0.0.1 (port auto)
	CorePath        string `json:"corePath"`        // custom core override (nekobox_core.exe only)
	// UpstreamCorePath is the dedicated override for the OFFICIAL
	// sing-box CLI (sing-box.exe). It is never shared with CorePath: the
	// two engines are separate binaries and must never resolve to each
	// other's file.
	UpstreamCorePath string `json:"upstreamCorePath,omitempty"`
	XrayPath         string `json:"xrayPath"` // xray.exe override
	// CoreEngine selects which sing-box binary runs sing-box profiles and
	// every sing-box transport (plain, SNI spoof, Aether).
	// "custom" = nekobox_core.exe (NekoBox fork with Thrift RPC).
	// "upstream" = official sing-box.exe (CLI only: stats/connections via
	// its Clash API).
	CoreEngine string `json:"coreEngine,omitempty"`
	AetherPath string `json:"aetherPath,omitempty"` // aether.exe override
	Language   string `json:"language"`             // en, zh, fa, ru...
	LogLevel   string `json:"logLevel"`             // debug | info | warn | error (legacy "warning" migrates to "warn" on save)
	// LogOutputPath, when set, makes the GUI append every log line to
	// that file in addition to the in-memory buffer (Advanced Phase H).
	// Empty keeps the current in-memory-only behavior.
	LogOutputPath    string `json:"logOutputPath,omitempty"`
	StartMinimized   bool   `json:"startMinimized"`
	CloseToTray      bool   `json:"closeToTray"`
	AutoConnect      bool   `json:"autoConnect"`
	URLTestIntervalS int    `json:"urlTestIntervalS"`
	TestUrl          string `json:"testUrl,omitempty"` // latency probe URL override
	// Auto-detect profile countries on import/save (Stage 3).
	GeoIPAutoDetect bool `json:"geoIPAutoDetect"`
	// GeoLite2-Country.mmdb override (empty = auto-resolve).
	GeoIPDBPath string `json:"geoIPDBPath,omitempty"`
	// StartOnBoot launches NekoGo at login (Windows Run key,
	// applied by SetStartOnBoot; GetAutoRun reads the OS truth).
	StartOnBoot   bool `json:"startOnBoot"`
	Notifications bool `json:"notifications"`
	// TODO: WindowStyle has no visual effect yet (Wails window style is fixed).
	WindowStyle   string         `json:"windowStyle"` // rounded | square
	SidebarLabels bool           `json:"sidebarLabels"`
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	// SubAutoUpdateM re-fetches every subscription every N minutes
	// (Throne parity: sub_auto_update). 0 (default) disables it.
	// Owned by the frontend scheduler; the backend stays on-demand.
	SubAutoUpdateM int `json:"subAutoUpdateM,omitempty"`
	// RawOverrideAck suppresses the Phase G one-time warning about
	// saving a raw config over per-section overrides. Plain bool
	// (not CoreOverrides) so it survives override resets.
	RawOverrideAck bool `json:"rawOverrideAck,omitempty"`
	// SpoofAutoDetectReverted marks the one-time revert of profiles
	// an older import auto-detect put on the spoof core (the detect
	// is now loopback-only, see import.go). Also set on fresh
	// installs so the revert window closes immediately; later
	// explicit conversions (SNI tab / profile form) are never
	// touched because the marker persists.
	SpoofAutoDetectReverted bool `json:"spoofAutoDetectReverted,omitempty"`
	AdvancedMode            bool `json:"advancedMode"`
	// CoreOverrides stores Advanced Mode Phase E per-section config
	// JSON, keyed by section name ("route", "dns", "inbound",
	// "experimental"). The managers merge the matching entry into the
	// rendered core config before writing it; absent keys render
	// defaults. Applies app-wide (per-profile overrides are out of
	// scope for Phase E).
	CoreOverrides map[string]json.RawMessage `json:"coreOverrides,omitempty"`
	// AdvancedLastTab remembers the last-visited Advanced panel tab
	// (Advanced Mode v2 hub). Empty = default ("overview").
	AdvancedLastTab string `json:"advancedLastTab,omitempty"`
	// CollapsedProxyGroups remembers which Proxies-tab remark groups
	// (subscription feeds) are collapsed, so a restart keeps the list
	// folded the way the user left it. Names are group labels; stale
	// entries for groups that no longer exist are harmless.
	CollapsedProxyGroups []string `json:"collapsedProxyGroups,omitempty"`
	// AdvancedPeekSimple shows the Simple shell while Advanced Mode
	// stays ON (peek mode). Reset when Advanced Mode is disabled.
	AdvancedPeekSimple bool `json:"advancedPeekSimple,omitempty"`
	// SpoofPsiphon chains Psiphon on top of a CoreSpoof connect: the
	// spoofed tunnel carries Psiphon's servers, traffic enters through
	// the TUN inbound and leaves through Psiphon. Only Connect runs the
	// chain (the SNI tab's standalone engine start never does).
	SpoofPsiphon bool `json:"spoofPsiphon,omitempty"`
	// SpoofXray runs a plain CoreSpoof connect on the Xray transport
	// instead of sing-box (nekobox_core). The spoof engine stays the
	// route; only the transport core changes. The SNI spoof + Psiphon
	// chain is exempt: its TUN inbound exists only on sing-box.
	SpoofXray bool `json:"spoofXray,omitempty"`
	// AetherPsiphon runs Psiphon on top of an Aether connect through the
	// Aether core's own chain mode: Psiphon's servers are dialled
	// through the tunnel, and the app keeps dialling the Psiphon
	// listener. A profile that names its own Psiphon mode keeps it.
	AetherPsiphon bool `json:"aetherPsiphon,omitempty"`
	// SNIConfig holds the sni-spoof engine options. Empty/zero values
	// mean "engine default" and are omitted from the generated
	// config JSON. Only used when a profile selects CoreSpoof.
	SNIConfig SNIConfig `json:"sniConfig,omitempty"`
	// SNISelectedIP/SNISelectedSNI are the endpoint and fake SNI used
	// for loopback (chained) profiles: the profile's own Address is
	// 127.0.0.1 and carries no usable endpoint of its own. While the
	// pinned entry stays checked in the SNI tab lists it wins; an
	// unchecked pin yields to the first checked entry (see spoofPinIP/
	// spoofPinSNI).
	SNISelectedIP  string `json:"sniSelectedIp,omitempty"`
	SNISelectedSNI string `json:"sniSelectedSni,omitempty"`
	// SNIIPs/SNISNIs are the user-curated Cloudflare IP and decoy-SNI
	// lists shown in the SNI Spoofing tab. Scanner results persist
	// here (LastOK/LastMs/LastAt/LastTLS); the scanner never adds or
	// removes entries. Selected (nil = active) marks the entries the
	// engine may use; unchecked entries stay in the list but are not
	// handed to the spoof pools.
	SNIIPs  []SNIIP  `json:"sniIps,omitempty"`
	SNISNIs []SNISNI `json:"sniSnis,omitempty"`
	// AetherScanResults are the endpoints the Aether tab found and
	// kept, newest-last. They survive tab switches and restarts; the
	// scan APIs update this list atomically.
	AetherScanResults []AetherScanResult `json:"aetherScanResults,omitempty"`
}

// SNIConfig mirrors the sni-spoof engine tuning knobs
// (CORE_REPORT.md section 3). Every field is optional:
// empty/zero means "omit, let the engine default apply".
type SNIConfig struct {
	Method           string  `json:"method,omitempty"`
	Fingerprint      string  `json:"fingerprint,omitempty"`
	QUICMode         string  `json:"quicMode,omitempty"`
	IPMode           string  `json:"ipMode,omitempty"`
	HandshakeTimeout float64 `json:"handshakeTimeout,omitempty"`
	MaxConnections   int     `json:"maxConnections,omitempty"`
	FakeDelay        float64 `json:"fakeDelay,omitempty"`
	SeqOverlap       int     `json:"seqOverlap,omitempty"`
	PaddingSize      int     `json:"paddingSize,omitempty"`
}

// SNIIP is one Cloudflare endpoint candidate in the SNI tab list.
type SNIIP struct {
	IP       string `json:"ip"`
	Source   string `json:"source,omitempty"`
	LastOK   bool   `json:"lastOk,omitempty"`
	LastMs   int    `json:"lastMs,omitempty"`
	LastAt   string `json:"lastAt,omitempty"`
	LastTLS  string `json:"lastTls,omitempty"`
	Selected *bool  `json:"selected,omitempty"`
}

// IsSelected reports whether the entry joins the active spoof pool.
// nil (legacy configs, fresh seeds) means active; the tab stores an
// explicit false when the user unchecks an entry.
func (e SNIIP) IsSelected() bool { return e.Selected == nil || *e.Selected }

// SNISNI is one decoy-SNI candidate in the SNI tab list.
type SNISNI struct {
	Host     string `json:"host"`
	Source   string `json:"source,omitempty"`
	LastOK   bool   `json:"lastOk,omitempty"`
	LastMs   int    `json:"lastMs,omitempty"`
	LastAt   string `json:"lastAt,omitempty"`
	LastTLS  string `json:"lastTls,omitempty"`
	Selected *bool  `json:"selected,omitempty"`
}

// IsSelected reports whether the entry joins the active spoof pool
// (see SNIIP.IsSelected).
func (e SNISNI) IsSelected() bool { return e.Selected == nil || *e.Selected }

// SpoofStats mirrors the sni-spoof [STATS] Snapshot line
// (CORE_REPORT.md section 5): scraped from stdout, no file, no RPC.
type SpoofStats struct {
	Active       uint64        `json:"active"`
	Total        uint64        `json:"total"`
	Success      uint64        `json:"success"`
	Failed       uint64        `json:"failed"`
	Uptime       float64       `json:"uptime"`
	SuccessRate  float64       `json:"success_rate"`
	BestEndpoint string        `json:"best_endpoint"`
	BestMethod   string        `json:"best_method"`
	Methods      []SpoofRanked `json:"methods"`
	UpBytes      uint64        `json:"up_bytes"`
	DownBytes    uint64        `json:"down_bytes"`
}

// SpoofRanked is one top-3 ranked bypass method entry.
type SpoofRanked struct {
	Key  string  `json:"key"`
	Ok   uint32  `json:"ok"`
	Fail uint32  `json:"fail"`
	Rate float64 `json:"rate"`
}

// DefaultSettings returns sane first-run defaults.
func DefaultSettings() Settings {
	return Settings{
		Theme:          "light",
		MixedPort:      2080,
		TunEnabled:     false,
		TunStack:       tunStackDefault,
		SystemProxy:    true,
		XrayAPIAddress: "127.0.0.1:10085",
		SingBoxRPCAddr: "127.0.0.1",
		// CPU audit #2: "info" emits a line per connection open/close;
		// "warn" keeps that churn off the default path. debug/info stay
		// valid values in config.json for troubleshooting.
		LogLevel:         defaultLogLevel,
		Language:         "en",
		CloseToTray:      true,
		URLTestIntervalS: 300,
		TestUrl:          "https://www.gstatic.com/generate_204",
		GeoIPAutoDetect:  true,
		GeoIPDBPath:      "", // auto-resolve
		StartOnBoot:      false,
		Notifications:    true,
		WindowStyle:      "rounded",
		SidebarLabels:    true,
		AdvancedMode:     false,
		SNIConfig:        SNIConfig{Method: "auto"},
		// The SNI Spoofing tab is curated-list driven: fresh installs
		// start with the TLS-verified Cloudflare IPs + decoy SNIs
		// (sni_defaults.go) and a working pin, so Start works before
		// any manual entry or scan.
		SNISelectedIP:  defaultSNISelectedIP,
		SNISelectedSNI: defaultSNISelectedSNI,
		SNIIPs:         DefaultSNIIPs(),
		SNISNIs:        DefaultSNISNIs(),
		CoreEngine:     "custom",
	}
}

// LogEntry is one line forwarded from a core process to the log viewer.
type LogEntry struct {
	ID      uint64 `json:"id"`
	Level   string `json:"level"`
	Source  string `json:"source"` // sing-box | xray | gui
	Message string `json:"message"`
	Time    string `json:"time"`
}

// CountryRefreshResult summarizes one RefreshAllCountries run.
type CountryRefreshResult struct {
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// GeoIPStatus reports DB availability + path for the UI.
type GeoIPStatus struct {
	Available bool   `json:"available"`
	Path      string `json:"path"`
	Error     string `json:"error,omitempty"`
}

// backupVersion is the only ExportAllData/ImportAllData format
// version (Advanced Mode Phase F).
const backupVersion = 1

// BackupBlob is the Phase F export document: whole Settings plus
// explicit profiles/subscriptions/overrides snapshots. Subscriptions
// and CoreOverrides duplicate Settings fields on purpose so an
// inspector sees them without digging into settings.
type BackupBlob struct {
	Version       int                        `json:"version"`
	ExportedAt    string                     `json:"exportedAt"`
	Settings      Settings                   `json:"settings"`
	Profiles      []Profile                  `json:"profiles"`
	Subscriptions []Subscription             `json:"subscriptions"`
	CoreOverrides map[string]json.RawMessage `json:"coreOverrides,omitempty"`
}
