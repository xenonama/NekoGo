// Shared types mirroring gui/types.go.
export type CoreType = 0 | 1 | 2 | 3 // 0 = sing-box, 1 = xray, 2 = sni-spoof, 3 = mihomo

/** Aether engine options (mirrors gui/aether.go aetherSettings). */
export interface AetherSettings {
  /** masque | wg | gool | mim | wg-over-masque. */
  protocol?: string
  /** h3 | h2. */
  transport?: string
  /** turbo | balanced | thorough | verified | ironclad. */
  scanMode?: string
  /** auto | off | light | firewall | balanced | gfw | aggressive. */
  obfuscation?: string
  /** v4 | v6 | both. */
  ipVersion?: string
  /** MASQUE SNI override (empty = core default). */
  masqueSni?: string
  /** chrome | firefox | semi-python | go (empty = chrome). */
  fingerprint?: string
  /** Comma-separated DNS resolvers. */
  dns?: string
  /** Exit-country rule, e.g. "DE,!NL". */
  exitLoc?: string
  /** Two-hop protocols: outer endpoint host:port. */
  wiwOuter?: string
  /** Two-hop protocols: inner endpoint host:port. */
  wiwInner?: string
  fragment?: boolean
  /** Range string like "10-20" (empty = core default). */
  fragmentSize?: string
  /** Range string like "0-100" (empty = core default). */
  fragmentDelay?: string
  ech?: boolean
  /** ECH DNS resolver (empty = use dns). */
  echDns?: string
  /** ECH outer domain tree, e.g. example.com. */
  echDomain?: string
  /** Hand-written core command line, overrides everything else. */
  command?: string
  /** off | chain | reverse | only. */
  psiphon?: string
  /** auto | cdn | direct. */
  psiphonMode?: string
  /** CDN IP set, comma-separated. */
  psiphonCdnIps?: string
  /** CDN TCP SNI override. */
  psiphonCdnSni?: string
  /** CDN protocol set override. */
  psiphonCdnSets?: string
  /** Preferred region code, e.g. US. */
  psiphonRegion?: string
  /** true = bundled list (default), false = fetch fresh. */
  psiphonBundledList?: boolean
  /** off | chain | reverse | only. */
  tor?: string
  /** auto | first | never | own. */
  torBridges?: string
  /** One bridge per line (own mode). */
  torBridgeLines?: string
  /** auto | only | off for Tor relay fingerprinting. */
  torRelays?: string
}

/** Endpoint found by aether.exe's scan (gui/aether_scan.go). */
export interface AetherScanResult {
  /** One-hop MASQUE/WireGuard endpoint. */
  host?: string
  port?: number
  /** Two-hop outer endpoint (also the MASQUE gateway of wg-over-masque). */
  outer?: string
  /** Two-hop inner endpoint; absent for wg-over-masque (profile's own). */
  inner?: string
  /** Persisted endpoint label, host:port (outer for two-hop scans). */
  endpoint?: string
  /** Protocol the endpoint was scanned with. */
  protocol?: string
  /** Transport for MASQUE-based protocols. */
  transport?: string
  /** Time from process start to the endpoint line. */
  latencyMs?: number
  /** TLS version when the core reported one. */
  tlsVersion?: string
  /** RFC3339 timestamp of the scan that found this endpoint. */
  lastScannedAt?: string | null
  /** Scan error attached to this result, if any. */
  error?: string
}

/** Live scan progress polled by the Aether tab. */
export interface AetherScanStatus {
  running: boolean
  done: number
  total: number
  lastError?: string
  lastErrorAt?: string
  startedAt?: string
}

export interface Profile {
  id: string
  name: string
  core: CoreType
  protocol: string
  address: string
  port: number
  idOrPassword?: string
  method?: string
  alterId?: number
  security?: string
  network?: string
  tls?: boolean
  serverName?: string
  fingerprint?: string
  realityEnabled?: boolean
  realityPublicKey?: string
  realityShortId?: string
  realitySpiderX?: string
  flow?: string
  path?: string
  host?: string
  alpn?: string
  allowInsecure?: boolean
  /** TLS fingerprint preset (mirrors gui/types.go; empty/"none" = legacy). */
  tlsFingerprint?: string
  /** TLS fragment mode: ""/"off" disables, "tlshello" or "1-3". */
  tlsFragment?: string
  /** Fragment length range, e.g. "10-20". */
  tlsFragmentLen?: string
  /** Fragment interval in ms. */
  tlsFragmentInt?: number
  /** Mux (connection multiplexing) enabled. */
  muxEnabled?: boolean
  /** Mux sub-protocol for sing-box: h2mux (default), smux, yamux. */
  muxProtocol?: string
  /** Mux concurrency (0 = core default; xray renders 8). */
  muxConcurrency?: number
  rawOutbound?: string
  testUrl?: string
  /** Aether engine settings; used when protocol === 'aether'. */
  aether?: AetherSettings
  group?: string
  /** Source subscription URL (absent for manual or legacy imports). */
  subUrl?: string
  lastConnectedAt?: string
  /** Last measured latency in ms (absent/0 = never tested). */
  lastLatency?: number
  /** RFC3339 timestamp of the last measurement. */
  lastTestedAt?: string
  /** ISO-3166 alpha-2 country code, uppercase (absent = unknown). */
  countryCode?: string
  /** English country name from the GeoIP DB. */
  countryName?: string
  /** RFC3339 timestamp of the last country lookup. */
  countryAt?: string
}

export interface Stats {
  uplink: number
  downlink: number
  uplinkByTag?: Record<string, number>
  downlinkByTag?: Record<string, number>
  running: boolean
  core: string
  /** Core alive but stats API unreachable: counters are stale, not live. */
  apiDown?: boolean
}

export interface TestResult {
  outboundTag: string
  latencyMs: number
  error?: string
}

export interface Connection {
  id: string
  outbound: string
  network: string
  dest: string
  protocol: string
  domain: string
  process: string
  upload: number
  download: number
}

export interface ConnectionList {
  connections: Connection[]
  truncated: boolean
}

export interface Settings {
  theme: string
  mixedPort: number
  /** Local listen address (empty = loopback; 0.0.0.0 shares on LAN). */
  inboundAddress?: string
  /** Remote DNS: https://host/path, tls://host, tcp://host, udp://host,
   *  quic://host, an IP or hostname (with an optional :port), or "local".
   *  A pasted sing-box server JSON object also works. Empty = system default. */
  dnsRemote?: string
  /** Direct-path DNS (never detoured through the proxy); same shapes,
   *  empty = system default. */
  dnsDirect?: string
  /** Global fragment override: when on, every mapped profile renders
   *  with these fragment options instead of its own (raw-JSON profiles
   *  exempt; applies on reconnect). */
  fragmentEnabled?: boolean
  /** Global fragment mode: "tlshello" (default) or "N-M". */
  fragmentMode?: string
  /** Global fragment length range, e.g. "10-20" (empty = 10-20). */
  fragmentLength?: string
  /** Global fragment interval in ms. */
  fragmentInterval?: number
  /** Global uTLS fingerprint override for every TLS profile. */
  fingerprintEnabled?: boolean
  /** Global fingerprint value (chrome, firefox, ...). */
  fingerprintValue?: string
  tproxyPort: number
  tunEnabled: boolean
  /** sing-box TUN network stack: system | gvisor | mixed. */
  tunStack: string
  /** Backend-owned, one-time: the stored stack was moved off the OS
   *  stack, which carries TCP only if the core is allowed through the
   *  firewall. Sent back untouched, so a later deliberate pick of
   *  System/Mixed is never rewritten. */
  tunOSStackMigrated?: boolean
  /** TUN interface MTU; unset = the 1350 NekoGo has always rendered. */
  tunMTU?: number
  /** TUN interface prefixes (CIDR, e.g. 172.19.0.1/24). Unset = default. */
  tunAddress?: string
  tunAddress6?: string
  /** Put an IPv6 prefix on the adapter at all. Off until asked: the
   *  backend still refuses it on a host that turns IPv6 off for new
   *  interfaces, unless IPv6 could reach the internet around the tunnel. */
  tunEnableIPv6?: boolean
  /** Install the default route into the TUN. null = yes (the historical
   *  behavior); a TUN that routes nothing captures nothing. */
  tunAutoRoute?: boolean | null
  /** Drop traffic that does not come from the TUN. */
  tunStrictRoute?: boolean
  /** Routes auto_route must leave alone (CIDR list). */
  tunRouteExclude?: string[]
  /** ICMP policy: rule | direct | unreachable | drop | reply. */
  tunIcmp?: string
  systemProxy: boolean
  activeProfileId: string
  xrayApiAddress: string
  singboxRpcAddr: string
  /** Custom-core override: nekobox_core.exe ONLY (the official CLI is
   *  upstreamCorePath — the two binaries are never interchangeable). */
  corePath: string
  /** Official sing-box.exe CLI override (upstream engine); never shared
   *  with corePath. Omitted by Go while empty. */
  upstreamCorePath?: string
  xrayPath: string
  /** mihomo.exe (Clash Meta) override; never shared with corePath or
   *  xrayPath. Omitted by Go while empty. */
  mihomoPath?: string
  /** Core backend: 'custom' (nekobox_core with Thrift RPC, default) or
   *  'upstream' (official sing-box.exe CLI, Clash API stats; no
   *  Mieru/Juicity/TrustTunnel/VLESS-encryption). The choice covers every
   *  sing-box transport: plain profiles, the SNI spoof transport and the
   *  Aether transport. Xray and mihomo profiles never run a sing-box
   *  binary, so the choice is inert for them. Omitted by Go when a legacy
   *  config file predates the setting — GetSettings reports 'custom' then. */
  coreEngine?: string
  /** aether.exe override (empty = bin\ next to the app; omitted by Go when empty). */
  aetherPath?: string
  language: string
  logLevel: string
  startMinimized: boolean
  closeToTray: boolean
  autoConnect: boolean
  urlTestIntervalS: number
  /** Latency probe URL override — omitted by Go (omitempty) when empty. */
  testUrl?: string
  /** Launch at OS login (stored only; no autostart registration yet). */
  startOnBoot: boolean
  /** Show desktop notifications. */
  notifications: boolean
  /** Wails window chrome: 'rounded' | 'square' (no visual effect yet). */
  windowStyle: string
  /** Show text labels next to sidebar icons. */
  sidebarLabels: boolean
  /** Auto-detect profile countries on import/save. */
  geoIPAutoDetect: boolean
  /** GeoLite2-Country.mmdb override (empty = auto-resolve; omitted by Go when empty). */
  geoIPDBPath?: string
  /** Show expert tools and core capabilities (Advanced Mode Phase A shell). */
  advancedMode: boolean
  /** Recorded subscription feeds (mirrors gui/types.go; omitted by Go when empty). */
  subscriptions?: Subscription[]
  /** Re-fetch every subscription every N minutes (0 = off). */
  subAutoUpdateM?: number
  /** Phase E per-section core config overrides (mirrors gui/types.go;
   *  omitted by Go when empty). */
  coreOverrides?: Record<string, unknown>
  /** Phase G: suppress the raw-config overrides warning (mirrors
   *  gui/types.go; omitted by Go when false). */
  rawOverrideAck?: boolean
  /** One-time marker for the spoof auto-detect revert (mirrors
   *  gui/types.go; omitted by Go when false). */
  spoofAutoDetectReverted?: boolean
  /** Phase H: extra GUI log file path (mirrors gui/types.go;
   *  omitted by Go when empty). */
  logOutputPath?: string
  /** Last-visited Advanced panel tab (Advanced Mode v2 hub;
   *  mirrors gui/types.go; empty = default "overview"). */
  advancedLastTab?: string
  /** Collapsed Proxies remark groups (subscription feeds), persisted so
   *  a restart keeps the list folded the same way. */
  collapsedProxyGroups?: string[]
  /** User-created Proxies remark groups (mirrors gui/types.go). Feed
   *  groups are implicit from Profile.group; these persist even while
   *  empty. Omitted by Go when empty. */
  proxyGroups?: string[]
  /** Peek into the Simple shell while Advanced Mode stays ON
   *  (mirrors gui/types.go; transient UI state, persisted). */
  advancedPeekSimple?: boolean
  /** sni-spoof engine options (mirrors gui SNIConfig; only used by
   *  CoreSpoof profiles; empty = engine default). */
  sniConfig?: SNIConfig
  /** Chain Psiphon on top of every CoreSpoof connect (mirrors gui
   *  Settings; omitted by Go when false). Psiphon rides the spoofed
   *  tunnel and is exposed to the system through the TUN inbound. */
  spoofPsiphon?: boolean
  /** Run plain CoreSpoof connects on the Xray transport instead of
   *  sing-box (mirrors gui Settings; omitted by Go when false). The
   *  Psiphon chain stays on sing-box: its TUN inbound is sing-box only. */
  spoofXray?: boolean
  /** Run the SNI spoof engine at all. Off = SNI Spoofing profiles dial
   *  their own address with the normal transport (e.g. a local
   *  127.0.0.1 tunnel). Go sends true/false (null only for a raw *bool
   *  null); unset means enabled. */
  spoofEnabled?: boolean | null
  /** Chain Psiphon on top of every Aether connect through the aether
   *  core's own chain mode (mirrors gui Settings; omitted by Go when
   *  false). Separate from spoofPsiphon: this rides the Aether tunnel. */
  aetherPsiphon?: boolean
  /** Chain-level Psiphon options (mirrors gui Settings; Advanced-only).
   *  They drive the two chain legs above — never a profile's own Aether
   *  Psiphon fields. Empty means aether's own default. */
  /** ISO alpha-2 egress country, '' = Psiphon chooses. */
  chainPsiphonRegion?: string
  /** Fronting shape: auto | cdn | direct. */
  chainPsiphonMode?: string
  /** Own CDN edge IPs / the SNI names used beside them / the built-in
   *  edge sets to try. */
  chainPsiphonCdnIps?: string
  chainPsiphonCdnSni?: string
  chainPsiphonCdnSets?: string
  /** Loopback-profile endpoint/SNI override (mirrors gui Settings). */
  sniSelectedIp?: string
  /** Loopback-profile decoy SNI override. */
  sniSelectedSni?: string
  /** Plan B endpoint pool (mirrors gui SNIIP). Under Plan A the tab shows
   *  the derived addresses instead, and this list stays saved untouched. */
  sniIps?: SNIIP[]
  /** Curated decoy-SNI list (mirrors gui SNISNI). Under Plan A these are
   *  the endpoints: each checked host is resolved over DoH and dialed. */
  sniSnis?: SNISNI[]
  /** Persisted Aether scan results (mirrors gui Settings). */
  aetherScanResults?: AetherScanResult[]
}

/** Phase F backup document (mirrors gui BackupBlob). */
export interface BackupBlob {
  version: number
  exportedAt: string
  settings: Settings
  profiles: Profile[]
  subscriptions: Subscription[]
  coreOverrides?: Record<string, unknown>
}

/** Local defaults mirroring the Go DefaultSettings (single source for
 *  the api.ts null fallback and the settings store initial state). */
export const defaultSettings: Settings = {
  theme: 'light',
  mixedPort: 2080,
  tproxyPort: 0,
  tunEnabled: false,
  tunStack: 'gvisor',
  // Unset and false mean different things to the renderer, so the mirror
  // states the default rather than leaving it undefined.
  tunAutoRoute: true,
  systemProxy: true,
  activeProfileId: '',
  xrayApiAddress: '127.0.0.1:10085',
  singboxRpcAddr: '127.0.0.1',
  corePath: '',
  upstreamCorePath: '',
  xrayPath: '',
  mihomoPath: '',
  coreEngine: 'custom',
  aetherPath: '',
  language: 'en',
  logLevel: 'info',
  startMinimized: false,
  closeToTray: true,
  autoConnect: false,
  urlTestIntervalS: 300,
  testUrl: 'https://www.gstatic.com/generate_204',
  startOnBoot: false,
  notifications: true,
  windowStyle: 'rounded',
  sidebarLabels: true,
  geoIPAutoDetect: true,
  fragmentEnabled: false,
  fragmentMode: 'tlshello',
  fragmentLength: '',
  fragmentInterval: 0,
  fingerprintEnabled: false,
  fingerprintValue: 'chrome',
  subAutoUpdateM: 0,
  geoIPDBPath: '',
  advancedMode: false,
  logOutputPath: '',
  sniConfig: { method: 'auto' },
  spoofPsiphon: false,
  spoofXray: false,
  spoofEnabled: true,
  aetherPsiphon: false,
  chainPsiphonRegion: '',
  chainPsiphonMode: 'auto',
  chainPsiphonCdnIps: '',
  chainPsiphonCdnSni: '',
  chainPsiphonCdnSets: '',
}

export interface LogEntry {
  id: number
  level: string
  source: string
  message: string
  time: string
}

export interface ConnStatus {
  core: string
  profileId: string
  running: boolean
  connectBusy?: boolean
  /** Last engine-death report (empty when none). */
  lastError?: string
}

export interface LinkImportResult {
  profiles: Profile[]
  invalid: number
}

export interface Subscription {
  url: string
  name: string
  updatedAt?: string
  /** Pause flag: null/undefined = enabled (legacy records), false =
   *  skipped by Update All and the auto-update timer. */
  enabled?: boolean | null
  /** Provider quota from the subscription-userinfo header (0 = unknown). */
  usageUpload?: number
  usageDownload?: number
  usageTotal?: number
  usageExpire?: string
}

export interface CountryRefreshResult {
  updated: number
  skipped: number
  failed: number
  errors?: string[]
}

export interface GeoIPStatus {
  available: boolean
  path: string
  error?: string
}

export interface UpdatableComponent {
  id: string
  name: string
  current: string
  latest: string
  updateAvail: boolean
  notes?: string
  lastCheck?: string
  lastError?: string
  installerPath?: string
}

// Advanced Mode Phase C: core network tools (Console -> Network).
export interface IPTestResult {
  outboundTag?: string
  ip?: string
  country?: string
  cc?: string
  error?: string
}

export interface SpeedTestResult {
  downloadMbps?: number
  uploadMbps?: number
  latencyMs?: number
  serverName?: string
  serverCountry?: string
  outboundTag?: string
  cancelled?: boolean
  error?: string
}

export interface URLTestResult {
  url: string
  latencyMs: number
  ok: boolean
  error?: string
}

export interface LastNetworkResults {
  ip?: IPTestResult
  speed?: SpeedTestResult
  speedRunning: boolean
  url?: URLTestResult
  country?: SpeedTestResult[]
}

// Advanced Mode Phase D: system tools (Console -> System).
export interface WgKeyPair {
  privateKey: string
  publicKey: string
}

// SNI Spoofing tab (mirrors gui/types.go + gui/app.go SpoofStatus).
export interface SNIConfig {
  method?: string
  fingerprint?: string
  quicMode?: string
  ipMode?: string
  handshakeTimeout?: number
  maxConnections?: number
  fakeDelay?: number
  seqOverlap?: number
  paddingSize?: number
  /** SNI tab mode: "A" resolves each decoy SNI over DoH and dials that
   *  (the IP list becomes a read-out); "B" (or absent) dials the curated
   *  IP pool — the historical behaviour. Backend normalizes "" to "B". */
  plan?: string
}

export interface SNIIP {
  ip: string
  source?: string
  lastOk?: boolean
  lastMs?: number
  lastAt?: string
  lastTls?: string
  /** Checked in the SNI tab = handed to the spoof engine. Absent/null
   *  = active (legacy configs); explicit false = the user unchecked
   *  it. null mirrors the Go *bool binding. */
  selected?: boolean | null
}

export interface SNISNI {
  host: string
  source?: string
  lastOk?: boolean
  lastMs?: number
  lastAt?: string
  lastTls?: string
  /** Plan A's read-out: the address the last DoH resolve of this host
   *  returned (empty until a Plan A scan runs; Plan B never writes it). */
  lastIp?: string
  /** Checked in the SNI tab = handed to the spoof engine. Absent/null
   *  = active (legacy configs); explicit false = the user unchecked
   *  it. null mirrors the Go *bool binding. */
  selected?: boolean | null
}

/** One row of the SNI tab's IP / decoy-SNI list card (SniListCard.vue).
 *  `index` is the position in the store array (removal target); `ok` /
 *  `ms` / `at` carry the scanner stamps for health sorting; `sub`/`cls`
 *  are the pre-rendered scan line + its status class; `checked` is the
 *  row's selection flag (feeds the engine); `primary` marks the first
 *  checked entry (the engine pin). */
export interface SniListRow {
  key: string
  index: number
  label: string
  sub: string
  cls: string
  ok: boolean
  ms: number
  at: string
  checked: boolean
  primary: boolean
}

export interface SpoofStatus {
  running: boolean
  listenPort: number
  startedAt: string
  uptime: number
  lastError: string
  /** The live connect runs the SNI spoof → Psiphon chain; chainPort is
   *  Psiphon's local SOCKS listener (0 when no chain). */
  chain?: boolean
  chainPort?: number
}

export interface SpoofRanked {
  key: string
  ok: number
  fail: number
  rate: number
  /** Engine EWMA handshake time in ms; null until this method has been timed. */
  latency_ms: number | null
  /** What auto ordered the row on: recent success rate minus latency drag. */
  score: number
}

export interface SpoofStats {
  active: number
  total: number
  success: number
  failed: number
  uptime: number
  success_rate: number
  best_endpoint: string
  best_method: string
  methods: SpoofRanked[]
  up_bytes: number
  down_bytes: number
}

export interface SNIPairResult {
  ip: string
  sni: string
  ok: boolean
  latencyMs: number
  tlsVersion?: string
  error?: string
  /** Plan A only: the handshake passed but the resolved address is not
   *  Cloudflare-fronted, so a Cloudflare-hosted profile cannot ride it. */
  warning?: string
}

export interface SNIScanStatus {
  running: boolean
  done: number
  total: number
  last?: string
}
