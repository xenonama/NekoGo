# NekoGo

A fork of NekoBox with a completely new GUI (Go + Wails3 + Vue 3).

Current version: **1.0.0-beta.3** (see the [changelog](#changelog)).

## Tech stack

- Go backend with a JSON config store (no database).
- Wails3 wrapper (`v3.0.0-beta.20`) exposing a bound `NekoGo` service to the frontend.
- Vue 3 + TypeScript frontend with Pinia stores and Vite builds.
- Every engine runs out of process, either per profile (the Core selector)
  or globally in Settings → Connection:

| Engine | Binary | Notes |
| --- | --- | --- |
| sing-box (fork) | `nekobox_core.exe` | Default. Carries the NekoBox Thrift RPC server. |
| sing-box (upstream) | `sing-box.exe` | Opt-in. Cannot run Mieru, Juicity or TrustTunnel profiles. |
| Xray | `xray.exe` | gRPC stats; the engine for REALITY/flow profiles under the spoof core. |
| Clash Meta | `mihomo.exe` | Optional. Renders the profile as a single-member select group. |
| Aether | `aether.exe` | Optional. MASQUE-family transports, plus Psiphon. |
| SNI spoof | `sni-spoof.exe` | Rust decoy-ClientHello engine, driven over WinDivert. |

A missing `xray.exe`, `sing-box.exe`, `mihomo.exe` or `aether.exe` is
downloaded from its official release by `build.bat` (SHA256-checked where
the release publishes a digest).

## Protocols

Mapped directly by the profile form, and rendered for every engine:

- VLESS (including REALITY), VMess, Trojan, Shadowsocks, SOCKS, HTTP
- Aether: `masque`, `wg`, `gool`, `mim`, `wg-over-masque`, with optional
  Psiphon (`off` / `chain` / `reverse` / `only`) and ECH

Everything else reaches the core through the profile's **Raw outbound
JSON** field:

- WireGuard, Hysteria 2 and TUIC — in the protocol dropdown, but the form
  carries no parameters for them
- Mieru, Juicity and TrustTunnel — not in the dropdown at all, and the
  upstream sing-box build cannot run them (custom core only)

The core is compiled with more than the form exposes: `build.bat` builds
`nekobox_core` with the ShadowTLS, Naive, AmneziaWG, Tailscale and gRPC
sing-box tags, so those protocols work the same way — through raw outbound
JSON.

## Import

Paste text: each line is either a link or an `http(s)://` subscription
URL, which is fetched and expanded (up to 5000 lines). Link schemes:
`vless://`, `vmess://`, `trojan://`, `ss://` (SIP002 with parameters,
plaintext `method:password@host:port`, and the 2019 whole-link base64
form), `socks://` and `aether://`. Options the fork cannot honour — such
as SIP003 `?plugin=` on `ss://` — are reported as errors, never dropped
silently.

## Build

Run `build.bat` on Windows 10/11 x64.

Requirements: Go 1.25+, Node.js 20+, Wails3
(`go install github.com/wailsapp/wails/v3/cmd/wails3@latest`), MinGW-w64,
Rust/cargo (the `sni spoofing` workspace is a hard build step), and NSIS
(optional — without it no installer is produced).

The `VERSION` file at the repo root is the only place the version is
written; `build.bat` reads it and stamps the GUI, the core, the installer
metadata and the artifact names.

Output: `gui/build/bin/nekogo.exe` with `nekobox_core.exe`, `xray.exe`,
`sni-spoof.exe` and the optional engines beside it; `dist/*.zip` and
`dist/*.exe`.

## Updating and releasing

In-app **Updates** takes each binary from the repo that publishes it: this
fork's releases supply `nekobox_core.exe`, `xray.exe` and the GUI, while
the upstream sing-box, mihomo, GeoIP and GeoSite rows resolve from their
own projects. See [docs/PUBLISHING.md](docs/PUBLISHING.md) for the release
procedure and the asset-name rules the updater matches on.

## Changelog

### 1.0.0-beta.3

- `ss://` links carrying a query string (`ss://…@1.2.3.4:443?#name`, the
  shape link generators emit) now import instead of failing with
  `ss: bad port "443?"`. The base64 userinfo keeps its `+` and `=`
  punctuation, and a SIP003 `?plugin=` link is refused by name rather
  than silently imported as a profile that cannot connect.
- A profile whose raw outbound is the JSON literal `null` used to panic
  the sing-box, Xray and mihomo config builders on Connect; each now
  returns a config error instead.
- The raw config editor refuses an empty or `null` document. It used to be
  accepted, wipe every section override, and hand the engine a config that
  could not start. Profiles already saved that way now fall back to the
  rendered default on their own.
- Test suite hermeticity: import and config tests write only to their own
  temporary directory. They previously loaded — and could rewrite — the
  live `%APPDATA%\NekoGo\config.json`.
- A node labelled with a bare `%` (`…#100%`, the way plenty of generators
  write a bandwidth label) used to fail the whole import with
  `invalid URL escape "%"`, and a label containing `%XX` was decoded twice,
  renaming `a%41b` to `aAb`. The label is now split off the link before
  parsing and decoded exactly once, for every `url.Parse`-based protocol.
  A bare `%` inside a `trojan`/`vless`/`socks` password rejected the link in
  exactly the same way — Go refuses the whole URL over one invalid escape —
  so credentials are now escaped for parsing and still decode once.
- `vless`/`trojan`/`ss`/`socks` share links round-trip adversarial labels,
  credentials and WebSocket paths byte-for-byte (`@Delta`, `100%`,
  `p@ss:word/with?s`, `/ws?ed=2560`, non-ASCII).
- Subscription downloads no longer follow a redirect to a loopback,
  RFC1918, link-local/metadata, CGNAT or `localhost` target. The feed URL
  is re-fetched on a timer, so a redirect was a server-chosen request
  against the user's own network that nobody looked at.
- A feed that will not download now says why in the log (a 403, a refused
  redirect, an empty body all used to look like `0 new profiles,
  1 invalid`). The message is unwrapped from net/http's `*url.Error` first:
  its text repeats the request URL, and a feed URL carries the access token
  in its query — and this log file is what users hand over when asking for
  help.
- A GeoIP download that is not an openable MaxMind database is refused
  before it replaces the installed one. That source publishes no sha256,
  so a truncated body or an HTML error page used to land on disk, update
  the revision marker and silently degrade every country lookup with no
  retry path left.
- Cancelling a country refresh no longer unregisters the refresh started
  after it. `CancelRefreshCountries` frees the one-at-a-time slot so the UI
  can retry straight away, but the cancelled run used to clear whatever was
  registered by the time it finished draining: the newer refresh vanished
  from the "N/M" progress readout, its Cancel button became a no-op, and the
  already-running guard let stacked refreshes run at once on one set of
  counters. Registrations are now numbered and a run only clears its own.
- `ss://aes-256-gcm:pa=ss@host:port` and other plaintext-shadowsocks links
  whose password contains base64 punctuation (`=`, `%`) now import. The
  userinfo was only taken as plaintext when it held neither, so such links
  went to the base64 decoder and were reported as corrupt — although `:`
  is outside every base64 alphabet and already proves the plaintext shape.
- `vmess://<base64>#Node 01` now imports: the trailing label made the
  payload read as invalid base64, so the node was rejected outright. The
  JSON `ps` field still wins, the fragment is the fallback before the
  `host:port` default.
- A `vmess` payload with explicit JSON nulls (`"sni":null`, `"host":null`,
  `"ps":null`, as converters write them) no longer turns into the literal
  text `<nil>` in the profile. That placeholder reached the TLS SNI and the
  Host header, breaking connections from a perfectly readable link.
- `allowInsecure=true` is honoured, not only the `allowInsecure=1` this app
  itself writes. Dropping it meant verifying a certificate the link said
  not to, which fails the handshake with no clue where it came from.
- Dead code removal, including a second Xray traffic total that counted
  loopback API polling as transferred bytes.
- Updater downloads now survive a slow or intermittently dead link (mobile
  data, a filtered network). A transfer that stalls with no bytes for 30s
  is aborted instead of blocking forever — the binary client deliberately
  has no whole-request timeout, so a dropped network used to hang the
  update with no way out. Failed attempts keep their bytes and resume with
  an HTTP `Range` request, retried with backoff; a source that ignores the
  range, answers a misaligned `Content-Range`, or changes the asset
  mid-download (caught by `If-Range`) restarts cleanly rather than
  installing spliced or truncated bytes. Permanent answers (404) are not
  retried. Progress and each retry are logged, so a slow download no longer
  looks identical to a hung one.
- Download errors and log lines name the host and path, not the query:
  GitHub asset links redirect to signed CDN URLs whose query string is a
  bearer credential, and that text is what users paste into bug reports.
- The version shown in the bottom-right app card, the About tab and the
  packaged app metadata now all come from one place. The frontend's
  hardcoded `1.0.0` fallback and the Go default's `-dev` suffix claimed
  versions no release ever had; `wails.json` and the frontend manifest
  still carried the pre-rename `5.11.28.3-fork.1`. Tests now fail the build
  when any of these drift from `VERSION`.
- The About tab gained a "Version stats" card (app, sing-box, Xray, mihomo,
  active engine, OS/arch, Go runtime) with a Copy diagnostics button, so a
  bug report carries the whole picture instead of just the app version.
- Aether profiles with `tor="reverse"`, and `tor="chain"` alongside
  `psiphon="chain"`, were started with a `--tor-bind` one port higher than
  the one the app dials: the port allocator handed out a pointer to its
  moving cursor instead of a value, so every listener that took one read
  the *last* number allocated rather than its own. The tunnel answered on
  a port nobody connected to and the connect timed out. The allocation now
  matches the reference client's post-increment behaviour, and a table
  test pins all eight tor/psiphon mode combinations.
- A Disconnect that lands while a connect is still probing its engines no
  longer leaves the official sing-box CLI running. `CoreManager`
  publishes the flag that says "this group's transport is the upstream
  binary" only when it commits the group, so for the whole probe window
  the disconnect stopped the custom core that never ran and left
  `sing-box.exe` alive behind a "disconnected" GUI — and because that
  manager still reported itself running, every later Connect failed with
  "upstream sing-box already running" until the app was restarted. Both
  sing-box binaries are now taken down unconditionally on that route, the
  way the Psiphon and Aether legs already were (stopping an idle manager
  is a no-op). An aborted Aether connect also unpublishes its tunnel port
  now: that branch returned a fresh error instead of the named one, so
  the deferred rollback never ran.
- VLESS profiles with REALITY ticked but the TLS box left alone connect
  on the sing-box engine instead of dialling plaintext to a REALITY port.
  sing-box nests `reality` inside `tls`, and the gate that decides
  whether to render `tls` at all left `realityEnabled` out — the Xray,
  mihomo and fingerprint paths all list it, so the same profile worked on
  those engines and failed silently on this one.
- `vmess` ciphers are folded onto the values the sing-box core accepts.
  Its `security` field is a case-sensitive enum with no fallback, and a
  link's `scy=` was stored verbatim, so the spellings legacy panels still
  publish (`chacha20-ietf-poly1305`, `AES-128-GCM`, `id-aes128-gcm`) made
  the whole config fail to load — the profile could not connect at all,
  while Xray lowercases the same field and mihomo warns and defaults.
- The loopback-profile auto-detect no longer overwrites a core the user
  picked. Legacy dead-end imports (`trojan://…@127.0.0.1:40443` with
  nothing listening there) are still converted to the SNI spoof engine on
  the fly, but it fired on Xray and mihomo profiles too: on any machine
  where `sni-spoof.exe` cannot start (no binary, no administrator for the
  WinDivert driver) the profile that used to connect began failing with
  "SNI spoofing requires administrator rights". A stale import can only
  ever arrive on the default engine, so only that one is rescued now.
- The system proxy is now adopted, not merely claimed. A `ProxyEnable=1`
  already reading NekoGo's own `127.0.0.1:2080` — a NekoBox leftover, a
  crashed session whose marker did not survive, or a hand-typed address —
  used to satisfy the "already ours, skip the churn" check without
  recording ownership, so Disconnect's `ClearSystemProxy` saw nothing it
  owned, declined to act and returned success: the machine kept pointing
  at a port that had just closed, which is total internet loss rather
  than a broken tunnel. An un-owned match is now taken over (state
  captured, then cleared on teardown), and a captured previous state that
  already *is* our own address is dropped instead of handed back.
- The previous OS proxy is re-read on every take-over. It used to be
  captured once per process and never invalidated, so the second
  connect/disconnect cycle restored — or deleted — whatever was there
  when the *first* connect happened, quietly destroying a proxy the user
  configured while NekoGo was off. The snapshot is now cleared again by
  the restore that consumes it.
- `GetSettings` hands out detached slices. The settings snapshot shared
  its subscription, Aether-scan, SNI and proxy-group arrays with the
  store, and the locked writers (`AddSubscription`, `TouchSubscription`,
  the scan-result and SNI updaters) mutate those arrays in place, so a
  snapshot taken for the frontend — or iterated across a multi-second
  subscription fetch — could change under the reader and be re-saved as
  if it had been authored. Lists are now copied on the way out.
- Subscription nodes are grouped by the feed, not by the host. A renamed
  feed used to file its refresh under the old name: the group was derived
  from the URL host while the record's display name is what the Proxies
  tab shows, so one subscription split into two groups after a rename.
- A repoint to an address that yields no usable profiles now refuses
  instead of eating the working feed. The old flow deleted the subscription
  record and re-stamped its existing profiles onto the dead URL, so a
  rotated token in the wrong box meant losing every node from the panel.
  The feed keeps its old address and the rejection count is reported.
- Link and subscription imports are serialised. Overlapping imports built
  their dedupe set *before* the fetch, `SaveProfile` matches only by ID,
  and a per-feed refresh never sets the busy flag the frontend auto-timer
  checks, so a double-paste or a refresh landing mid-import appended the
  same nodes twice — permanently. A second import is now refused before
  anything is fetched.
- Plan A no longer dials an intercepted DNS answer. The decoy SNI's
  resolved address IS the connect endpoint, and the DoH path accepted any
  parseable IPv4 — so a poisoned resolver or a captive portal that answered
  `127.0.0.1`, an RFC1918 or a link-local address handed the tunnel to the
  interceptor (or to a closed local port) instead of failing, and the
  last-resort OS-resolver fallback did the same on exactly the network where
  it is guaranteed to be lying. Answers are now gated by the predicate
  Plan B's endpoint picker has always used: unusable records are skipped,
  the next provider is tried, and an answer set that is entirely unusable is
  reported with the addresses it contained.
- A death report from a superseded engine no longer tears down the live
  connection. Engine crashes are reported asynchronously, so the report for
  the process a newer connect replaced can land after that connect has
  committed. `handleEngineExit` used to unpublished the shared loopback dial
  ports on sight, before the generation check that runs inside the teardown:
  the new group kept running with no SNI front and no Aether listener
  published, so its configs rendered direct — "connected, no traffic", or a
  loopback profile dialling a port nobody owns. Un-publication now happens
  only in the two places that know which process owned the port (each
  manager's reaper, value-scoped, and the generation-checked group
  teardown). Its App-side observer made the same mistake from the other
  side: a death report that lands after a newer connect committed reverted
  the Windows system proxy and raised the disconnect banner anyway, so the
  browser's traffic quietly stopped using the tunnel while the panel still
  showed it connected and counted bytes. That half now runs only when the
  report really did leave nothing running.
- The same race survived inside a probe. Spoof-profile "Test" spins a
  throwaway engine when none is connected and shares the process-wide front
  port with a live one when it is — but its teardown cleared that port on
  sight. A connect that committed during the probe published its own engine
  port, and the probe's teardown took the live session's port down with it,
  so the just-connected group rendered its configs direct. Teardown now
  un-publishes by value: only the port it installed.
- The system-proxy revert is now recorded before it is installed, and
  written atomically. The crash record of the user's own proxy values used
  to be written *after* `ProxyServer`/`ProxyEnable` were committed, and its
  error was ignored — a session killed in that window (or one that hit a
  full disk, which this app's own config directory does routinely) left
  Windows dialing NekoGo's `127.0.0.1:<port>` with nothing marking that
  address as ours, so the next launch saw somebody else's setting and the
  dead pointer survived until the user found the proxy dialog by hand. An
  interrupted record write was worse: the marker was rewritten in place, so
  half of it replaced the only copy of the previous values, and recovery
  could then only disable the proxy — silently switching a corporate proxy
  off and deleting its address. The record now lands first, through a
  temporary file plus rename, so a torn write costs the new value and never
  the old one, and it is only rolled back when the apply changed nothing at
  all. A partially committed apply keeps the record, which also fixes the
  `<local>` `ProxyOverride` the old code leaked permanently into the user's
  exception list whenever its `ProxyServer` write failed.
- The window now draws its own title bar. The native Windows caption is
  disabled (`Frameless`) and a Vue bar carries the icon, the title and the
  minimise / maximise / close buttons. Dragging is handed to the runtime's
  native caption loop rather than reimplemented with mouse deltas, so Aero
  Snap, snap layouts and dragging across monitors keep working, and the
  frameless window keeps its resize borders, shadow and rounded corners.
  Close still goes through the same window-closing event the tray setting
  hooks, so "close to tray" behaves exactly as it did from the native
  caption. The maximise glyph is read back from the window instead of
  toggled locally, so Win+Up and snap layouts update it too.
- About → Version stats told users their sing-box was the GUI version.
  Two sing-box binaries ship, and the card read the wrong one: the NekoBox
  Thrift fork is built with its sing-box version constant stamped to the
  NekoGo build version (the updater compares that stamp against NekoGo
  release tags), so `nekobox_core sing-box version` really does print
  `sing-box version 1.0.0-beta.3`. The "sing-box core" row now queries the
  official `sing-box.exe`, and the fork gets its own "NekoBox core" row —
  the same split the updater already makes. The "Go runtime" row is gone:
  the GUI's Go toolchain is not a version a user can act on, and every
  core reports its own. Copy diagnostics emits the same facts, so the
  paste matches the card.
- Settings → Updates downloaded the wrong Xray. The Xray row read NekoGo's
  own app releases, so it compared an Xray banner (`Xray 25.9.11`) against a
  NekoGo tag (`1.0.0-beta.3`) — an ordering nobody can win, which meant the
  row never offered an update and installed the `xray.exe` bundled with the
  app. It now follows [XTLS/Xray-core](https://github.com/XTLS/Xray-core)
  like the sing-box row follows SagerNet and the mihomo row follows
  MetaCubeX: the arch's archive is matched by its exact release name (so the
  `.dgst` sidecars cannot be picked), the published digest is verified, and
  `xray.exe` is unpacked from it into the path the Xray engine actually
  resolves — the `xrayPath` override, or `bin\xray.exe` beside the GUI — with
  the same backup, smoke-verify and rollback as the other cores. An install
  that would land on another row's binary is refused and says which setting
  to fix.
- Settings → Chain grew the Psiphon options the two chain legs always
  ignored (Advanced mode only): the egress country, the fronting mode, and
  the CDN edge IPs, SNI names and built-in edge sets. A country picked there
  reached neither leg — the SNI-spoof→Psiphon run passed no Psiphon flags at
  all, and the Aether→Psiphon run used the profile's own. Both legs now
  render their flags through one shared builder, so a Chain-tab choice
  cannot apply to one leg and silently not the other. The country is a
  curated list plus a free two-letter code, validated on save (a value
  aether cannot read is refused there rather than ignored at Connect), and
  it drives the chain legs only: a profile that sets its own Psiphon options
  keeps running its own tunnel exactly as before.
- The "relaunch NekoGo as administrator" prompt is now one of NekoGo's own
  dialogs. It used WebView2's browser `confirm()` box — a window that matches
  no part of the app, cannot be themed and ignores the app's keyboard
  shortcuts. Escape and Cancel both leave the current instance running
  untouched; Relaunch restarts elevated and reconnects to the profile the
  refused connect had already remembered.
- The SNI spoof → Psiphon chain refused an unelevated connect by naming only
  TUN. That chain needs both drivers — WinDivert for the spoofing engine and
  Wintun for the forced TUN inbound — and the message named one, so a user
  reading it had no way to tell why the spoofing itself had not started. It
  now names both. A connect that needs only one still gets only that one, and
  every refusal still arrives before any driver is loaded or core spawned.
- `SpoofStart` (the standalone engine start, reachable through the same API
  the SNI tab uses) went straight to WinDivert and returned the driver's own
  `Access is denied (code 5)`, with no admin offer attached. It now refuses
  with the same message Connect uses. Disabling SNI spoofing in
  Settings → Connection keeps saying "disabled" rather than asking for
  elevation, and an already-elevated process is never refused on this ground.
- Connect no longer needs to be clicked repeatedly. Both sing-box engines
  bind a fixed inbound port, so a port already held made every attempt fail
  the same way — the clicks were never fixing anything, they were the waiting
  for whatever held the port to let go. The two engines now pre-flight their
  ports the way the Xray and mihomo paths always did, and they check the ports
  the config is about to render (a raw-config override included, a TUN inbound
  skipped, since it binds no TCP port). A collision is waited out for two
  seconds — the gap a previous connect's teardown can still leave — and then
  refused by port number, naming what typically owns it. The check runs before
  any config is written or process spawned, so a refused connect leaves no
  orphan behind, and it never shows the "port in use" line for a port the
  config does not bind.
- About → Version stats labelled the NekoBox core row "NekoBox core" while
  displaying a version; it reads "NekoBox version" now. Its four engine
  versions and the idle "Engine" read-out are Advanced-mode rows: Simple mode
  chooses no core, installs no second engine and shows no core path, so those
  rows described a screen its user cannot reach. Copy diagnostics still
  carries every one of them — a bug report needs the engine facts whether or
  not the card shows them.
- The Settings tab said things the app does not do, and one of them could
  cost a connect. The Mixed port row promised "HTTP proxy on this port ·
  SOCKS proxy on port + 1" for every core; that is the Xray shape, while the
  sing-box cores serve both protocols from the one port — so the row now says
  which core does what, and states the 1-65534 range the field enforces.
  Clearing or mistyping that field used to parse to nothing, fall through
  `|| 0` and get clamped to **port 1**, which the backend accepts as a valid
  port: the app saved a listener nobody asked for. A refused edit now keeps
  the previous port, redraws it in the box, and says which number is in
  force.
- Minimize-to-tray was the one control that failed silently — a refused
  `SetCloseToTray` went to the console while the switch flipped back on its
  own, which reads as the app ignoring the click. It reports like every other
  control now. The TUN stack row and the MTU hint stayed on screen with TUN
  off, describing an adapter that is not created; both appear in TUN mode.
  The Connection and Appearance card headers advertised controls the cards do
  not contain ("the OS proxy is set on every connect", next to the switch
  that decides whether it is, and a "window" setting nothing edits), and the
  hero line still invited the user to "customize your NekoBox experience" —
  NekoBox is the core this GUI drives, not the app. A row's **Check** in
  Settings → Updates runs the whole list, so the header's "Last global check"
  now moves with it instead of insisting "never".
- "Clear all countries" asked through `window.confirm`, a browser box in a
  window that has none — the same defect that took the relaunch prompt onto
  the app's own dialog. Destructive actions in Settings now use a themed
  confirmation card with the accept button styled as the danger it is;
  Cancel and ESC leave every profile untouched. The native boxes still in
  use for deleting profiles are unchanged.
- TUN mode is a settings group, after PattN's. It had a switch and a stack
  and hardcoded the rest — interface address (`172.19.0.1/24`), MTU (1350),
  auto/strict route, the excluded routes, the ICMP answer — so a range
  already routed on the machine had no way out except hand-editing
  `config.json`, and a link that needed a smaller packet could not have one.
  Settings → Connection, with TUN on, now offers every one of them, plus an
  IPv6 adapter address behind PattN's OS check (Windows can have IPv6 off
  for new interfaces, and asking a TUN for an address it cannot have kills
  the whole connect; a global IPv6 address elsewhere could leave the tunnel
  around it). Each field is optional in `config.json` and unset renders
  exactly what NekoGo rendered before, so upgrading changes nobody's tunnel.
- The TUN route rules the core is handed grew the guards that were missing:
  the drop rule for the interface's own address is now derived from whatever
  address is actually on it (it was a second constant restating the first,
  so it went stale the moment the address became a setting), UDP
  135/137/138/139/5353 and the `224.0.0.0/3` + `ff00::/8` multicast ranges
  are rejected instead of dialled at the proxy or looped back into the
  interface `auto_route` just pointed at the tunnel, and the ICMP policy can
  answer, drop or forward a ping.
- A TUN value the core would refuse at start time is refused at save time,
  naming the field: a bare IP where a CIDR prefix is required (sing-box
  decodes these into `netip.Prefix`, which has no address-only form), an
  IPv6 prefix longer than /126 (the system stack derives its gateway from a
  peer address), an MTU outside 1280-65535, an unknown ICMP policy. The MTU
  box in the panel is checked as an MTU, not as a port — 576 is a legal port
  and a size no interface will carry, and 65535 is a legal MTU the port field
  refuses.
- Not ported from PattN: its "legacy protect" mode runs a second core under
  the SOCKS listener, and NekoGo's Xray and mihomo paths render no TUN
  inbound at all, so there is nothing for it to protect here. PattN's TUN
  template also sets inbound `sniff`, deprecated in sing-box 1.11 and removed
  in 1.13 — with it the shipped core rejects the entire config at startup
  (`legacy inbound fields are deprecated`), so NekoGo does not set it.
- Advanced mode asks its questions in NekoGo's own dialog instead of the
  webview's native `confirm`/`prompt` boxes: discarding a config draft
  (section switch, leaving Settings, leaving the mode), closing the raw
  profile editor with unsaved JSON, applying a saved config with a
  reconnect, and naming a new remark group in Profile tools. The group
  name in particular used to go through `prompt()`, which WebView2 can
  refuse outright — "New group…" then did nothing at all. Escape cancels
  every one of them, including the JSON editor's, which previously
  reopened its own discard prompt on the same keypress.
- The "Active overrides" card reports the real size of a stored override.
  Overrides arrive as parsed JSON, so the old character count printed
  `[object Object]`'s length — 15 chars for every section, whatever it
  held. The raw-config warning follows the same rule as the core: an
  override holding `{}` or `[]` carries no setting, so it is not something
  a raw save would destroy.
- The config editors follow the profile the backend actually renders —
  the live connection first, then the stored selection, then the first
  profile — and name it in the card head. They used to resolve the stored
  selection while the backend resolved the running core, so with a mihomo
  profile connected and a sing-box profile selected the card offered
  editors the backend refuses, under the wrong profile's name.
- The sidebar mode switch is named "Advanced Mode" in both states, with
  the destination in its tooltip. It is a switch reporting whether
  Advanced is on, and it used to announce the mode it would switch *to*:
  a screen reader read "Simple Mode, switch, on" while Advanced was
  running, and the visible text disagreed with the accessible name.
- The Backup & reset cards no longer inherit the Proxies table's sticky
  action bar. Both use `.nb-px-bar`, and the rule pinning the bulk-action
  bar to the bottom of the proxy list was unscoped, so every Backup card
  bar stuck to the viewport and rode up over its own card head — 32px of
  the title hidden beneath the buttons that belong to it. Stickiness is
  now `.nb-px-bar--sticky`, worn only by the Proxies table.
- Each Backup card reports its own result. A single message sat after the
  last card, so Export printed "Exported to …" 510px below the button that
  caused it, off screen: the click looked like it did nothing, and a
  failure read as though Reset had produced it. Export, Import, View raw
  config and Reset each carry their own line now.
- "View raw config" re-reads when the config it shows changes. It fetched
  once per click and then froze, so saving an override or connecting a
  different profile left the old document on screen under a heading
  promising the current one. It follows the same target rule as the
  editors and refreshes on a target or override change — quietly, without
  disabling the other three cards while it reads.
- Importing a backup asks first. It replaces settings and profiles
  wholesale, destroying as much as the Reset card beside it — which
  demands a typed `RESET-ALL` — yet one click on "Replace current data"
  did it unconfirmed. It now uses the themed danger dialog; Cancel and
  ESC leave the data untouched and the chosen file still loaded.
  Disarming Reset also clears the typed token, which used to survive the
  disarm: re-arming offered a filled-in box and a live Confirm button.
- TUN mode connected but loaded nothing. Two defects in what the tunnel
  asks the DNS module to do, both fixed by porting PattN's TUN config.
  First, the hijack matched only the sniffed `protocol: dns`, and the TUN
  inbound sets that protocol for one destination — the hijack address it
  derives beside the interface address. A query sent to the resolver the
  OS already had was not recognised as DNS and fell through to the proxy.
  The hijack now matches port 53 as well, so a query is caught whichever
  resolver it was sent to.
  Second, and the reason nothing loaded: the TUN DNS block was a single
  server, DNS-over-HTTPS with `detour: proxy`. When the profile's address
  is a hostname, sing-box must resolve it before the proxy can be dialed,
  and with no `route.default_domain_resolver` it asks the DNS block's
  default transport — the same proxied DoH server. The lookup waits on
  the connection it exists to make, so no name ever resolves while the
  adapter is up and the core logs nothing wrong. TUN now keeps a
  detour-free bootstrap server beside the proxied resolver and names it
  in `default_domain_resolver`, exactly PattN's `local-local` /
  `direct-dns-1` shape, for the fallback block and for a user-configured
  block that holds only one proxied server. The hijacked queries still
  ride the proxy, so nothing about the leak protection changed, and proxy
  mode renders byte-for-byte what it rendered before.
- TUN mode still connected and still loaded nothing, and this time the
  stack was the reason. NekoGo defaulted it to `system`, deliberately, to
  save CPU; PattN defaults to `gvisor`. The OS stack terminates TCP
  inside Windows: the tunnel rewrites an app's SYN into a new connection
  addressed to the core's own endpoint on the adapter, so the handshake is
  completed by the OS and handed up afterwards — which makes it an inbound
  connection to the core binary, and Windows filters inbound connections.
  sing-tun tries to allow that itself and throws the error away, so the
  missing permission is silent. An elevated connect on the traced machine
  showed no `sing-tun` firewall rule before or after the start, and the
  same run answered every DNS query — UDP never reaches the OS listener —
  in about 200 ms while a TCP dial to the host those answers named timed
  out after 15 s. That is the exact shape of "connected, nothing loads".
  A/B runs over both stacks and both `strict_route` values settle it:
  `system` failed the TCP probe with strict routing on and off, `gvisor`
  passed both, so strict routing was never the suspect and `strict_route`
  is unchanged. The default is now `gvisor`, which finishes TCP inside the
  core and needs nothing from the firewall; `system` and `mixed` stay
  selectable for their CPU and NAT behaviour, and the Settings row now
  says what they cost — `mixed` embeds the same OS listener for TCP, so it
  inherits the same requirement. Changing the default, though, did not
  fix the install it was traced on, and the reason is worth recording:
  NekoGo had hardcoded the stack before the setting existed, so its first
  save wrote `"tunStack": "system"` into `config.json` as if the user had
  picked it. A stored value beats a default, and checking for one with a
  pattern that required `"tunStack":"system"` — no space, while Go writes
  `"tunStack": "system"` — reported the key as absent. So the config
  carried the broken stack all along and the default change never landed.
  Loading now migrates a stored `system`/`mixed` to the working stack
  once, logs what it did and why, and marks the file so a later deliberate
  pick of System or Mixed is honored from then on instead of being
  quietly reverted at the next start. Verified here by driving the real
  engine: under `gvisor` the TCP probe reaches the peer (`read: EOF` — a
  plaintext request on port 443 closed by Google, i.e. the handshake
  completed) where under `system` it had timed out.

### Earlier releases

`1.0.0-beta.1` and `1.0.0-beta.2` are documented per session in
[AGENT_LOG.md](AGENT_LOG.md), the project's append-only work log.

## Credits

- [qr243vbi/nekobox](https://github.com/qr243vbi/nekobox)
- [Thrift](https://thrift.apache.org/)
- [enfein/mieru](https://github.com/enfein/mieru)
- [SagerNet/sing-box](https://github.com/SagerNet/sing-box)
- [Xray-core](https://github.com/XTLS/Xray-core)
- [go](https://go.dev/)
- [Wails](https://wails.io/)
- [Vue](https://vuejs.org/)
- [protorpc](https://github.com/chai2010/protorpc)
- [simple-protobuf](https://github.com/tonda-kriz/simple-protobuf)
- [quickjs-ng](https://quickjs-ng.github.io/quickjs)
- [Open Build Service](https://openbuildservice.org/)
- [Github](https://github.com)
- [go-legacy-win7](https://github.com/thongtech/go-legacy-win7)
- [codeclysm/extract](https://github.com/codeclysm/extract)
- [shlex](https://github.com/google/shlex)
- [go-winio](https://github.com/Microsoft/go-winio)
- [taskmaster](https://github.com/giert/taskmaster)
- [lmdb](https://www.symas.com/mdb)
- [leveldb](https://github.com/google/leveldb)
- [amnezia-box](https://github.com/hoaxisr/amnezia-box)
- [reF1nd/sing-box](https://github.com/reF1nd/sing-box)
- [npipe](https://github.com/NullYing/npipe)
- [OpenSSL](https://github.com/openssl/openssl)
- [Aur](https://aur.archlinux.org/)
- [Protobuf](https://protobuf.dev/)
- [gRPC](https://grpc.io/)
- [shellescape](https://al.essio.dev/pkg/shellescape)
- [MatsuriDayo/nekoray](https://github.com/MatsuriDayo/nekoray)

## License

Use of this software is subject to the GPL-3 license.
