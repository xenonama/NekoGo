# NekoGo

A fork of NekoBox with a completely new GUI (Go + Wails3 + Vue 3).

Current version: **1.0.0-beta.4** (see the [changelog](#changelog)).

## Tech stack

- Go backend with a JSON config store (no database).
- Wails3 wrapper (`v3.0.0-beta.20`) exposing a bound `NekoGo` service to the frontend.
- Vue 3 + TypeScript frontend with Pinia stores and Vite builds.
- Every engine runs out of process. Settings → Connection → Core engine
  selects the preferred engine for normal profiles: NekoBox Core, sing-box,
  Xray or Mihomo. If that engine cannot render the profile's protocol,
  NekoBox Core handles that connection without changing the saved preference.
  Missing binaries and startup failures never trigger this fallback. Raw
  outbound formats and SNI spoof keep their required engine; TUN remains
  sing-box-only.

| Engine | Binary | Notes |
| --- | --- | --- |
| sing-box (fork) | `nekobox_core.exe` | Default and compatibility fallback. Carries the NekoBox Thrift RPC server. |
| sing-box (upstream) | `sing-box.exe` | Preferred sing-box option. Cannot run Mieru, Juicity or TrustTunnel profiles. |
| Xray | `xray.exe` | Preferred engine option with gRPC stats; also carries REALITY/flow under the spoof core. |
| Clash Meta | `mihomo.exe` | Preferred engine option. Renders the profile as a single-member select group. |
| Aether | `aether.exe` | Optional. MASQUE-family transports, plus Psiphon. |
| SNI spoof | `sni-spoof.exe` | Rust decoy-ClientHello engine, driven over WinDivert. |

A missing `xray.exe`, `sing-box.exe`, `mihomo.exe` or `aether.exe` is
downloaded from its official release by `build.bat` (SHA256-checked where
the release publishes a digest).

## Protocols

Mapped directly by the profile form, and rendered for every engine:

- VLESS (including REALITY), VMess, Trojan, Shadowsocks, SOCKS, HTTP
- Aether: `masque`, `wg`, `gool`, `mim`, `wg-over-masque`, with optional
  Psiphon and Tor exit chains (`off` / `chain` / `reverse` / `only`) and ECH

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

A fresh clone has everything the build needs. The `.gitignore` inherited
from the Qt NekoBox tree used to hide it: a blanket `*.json` swallowed
`gui/frontend/package.json`, `package-lock.json`, `tsconfig.json` and
`gui/wails.json`, so `npm install` had nothing to resolve and
`wails3 generate bindings` could not run, and a blanket `*.dll` swallowed
`runtime/WinDivert.dll`, which `build.bat` copies with `if exist` — the
build printed BUILD OK and the SNI engine shipped without its driver.
Those names are re-included now. `gui/frontend/dist` stays a build output:
`main.go` embeds it, and `build.bat` runs `npm install` and `npm run build`
before `go build`, so a clone still builds in one command.

## Updating and releasing

In-app **Updates** takes each binary from the repo that publishes it: this
fork's releases supply `nekobox_core.exe`, `xray.exe` and the GUI, while
the upstream sing-box, mihomo, GeoIP and GeoSite rows resolve from their
own projects. See [docs/PUBLISHING.md](docs/PUBLISHING.md) for the release
procedure and the asset-name rules the updater matches on.

`export-source.bat` writes the publishable tree to `source\`, and its last
step refuses to call that tree GitHub-ready while any file inside it is a
list of proxy links one per line — a subscription dump, which is data and
not source. A public repo cannot be un-published, so that check is the
final line. Pass a second argument (`export-source.bat "" D:\tmp\check`) to
test the include/exclude rules against a scratch output instead of
replacing `source\`.

## Changelog

### 1.0.0-beta.4

- The Simple/Advanced switch now uses the same on/off control as Settings.
  Dark mode is the default for new and missing theme values; an explicitly
  saved light theme is still respected.

- Settings → Updates shows downloaded and total bytes while an asset is being
  fetched, with a progress bar and live polling. A discovered update leaves a
  badge and reminder beside its row in `updates.json` until that component is
  actually installed; downloading the NekoGo installer alone does not dismiss
  the reminder, and a failed update check keeps the last known release notice.
  Checks and downloads are serialized so an older completion cannot overwrite
  newer per-component state.

- Every explicit SNI bypass method now combines its decoy behaviour with a
  split of the real ClientHello, rather than relying on the decoy alone.
  `auto` keeps its previous compatibility strategy. New engine defaults are
  `hello_split`, `legacy`, `block` and `ipv4` across the GUI, backend and
  standalone engine.

- SNI Spoofing Plan A accepts a manually entered IPv4 address. Manual mode
  sends that endpoint through `CONNECT_OVERRIDE` and omits `RESOLVE_SNI`, so
  NekoGo does not perform its automatic SNI DoH lookup; Plan B keeps its IP
  pool and scans use the configured manual endpoint.

- SNI Spoofing Stats has a Clear button. It sends a reset command to the Rust
  engine and clears the cached snapshot, resetting counters, transferred-byte
  totals, rankings and hot-path statistics rather than only hiding the panel.
  Reset generations reject queued pre-reset snapshots, and delayed output from
  a stopped child cannot mutate a restarted engine's statistics or error state.
  Engine-option saves are serialized so a rapid plan/method edit cannot restore
  an older value.

- Durable configuration is now compact `config.json`; latency, country,
  connection and scan history move to a compact, versioned `state.json`
  sidecar. Runtime writes use a 500 ms trailing debounce with a two-second
  maximum delay, flush on shutdown, and retry failed writes automatically for
  up to four bounded backoffs. A failed legacy migration blocks the durable
  rewrite until the runtime snapshot is safe, and config backups are replaced
  atomically. Direct imports and subscription refreshes save new profiles in
  batches, so a 5,000-line import no longer rewrites the whole profile file
  once per node; subscription repointing updates only live matches under one
  lock.

- Settings → Connection → Core engine now offers NekoBox Core, sing-box,
  Xray and Mihomo. The selection is a preference: a normal profile whose
  protocol the selected engine cannot render uses NekoBox Core for that
  connection only. Missing binaries and startup failures never switch cores
  or overwrite the preference; raw outbound formats and SNI spoof keep their
  required engine, and TUN remains sing-box-only.

- Windowed Console and Settings layouts no longer collapse their content.
  Traffic, Logs and Connections keep usable heights in Advanced mode, Routing
  Rules stays fully readable in both modes, and wide table content scrolls
  inside its own card instead of widening the page.

- The NekoGo and Aether logos could be dragged out of the window and dropped
  on the Desktop as image files. Every app-logo render now opts out of native
  image dragging; profile rows keep their intentional drag-and-drop.

- The white Aether navigation logo was invisible in light mode. It now renders
  black in light mode and keeps its original white artwork in dark mode.

- Clearing SNI Spoofing logs worked only until the tab was left: the panel was
  unmounted, and its next visit fetched the same engine lines again. The clear
  is now a separate id watermark in the connection store, so it survives tab
  switches without clearing the main Console or the backend's log history.

- The Console silently showed only the newest 300 lines. Its toolbar now has a
  persisted line-limit picker (100, 300, 500, 1,000 or 2,000 lines), plus
  **No limit** to show the whole in-memory buffer. No limit removes the display
  cap; it does not make the bounded backend buffer or log-file history
  unlimited.

- Tor joined Psiphon as the chain's exit hop, on both legs (Settings →
  Chain, Advanced mode). The SNI-spoof leg runs `aether.exe --tor-only` with
  its upstream set to the spoofed transport; the Aether leg runs it as the
  engine's own `tor="chain"`, the same shape the Psiphon mode already used —
  one binary ships both, so nothing new had to be installed. A leg publishes
  one listener and readiness is probed on one child, so it carries one
  driver: a config that arms both keeps Psiphon, and the save clears the
  losing switch with a line in the log instead of leaving a toggle on screen
  that does nothing. Tor's chain-level options (bridges
  `auto`/`first`/`never`/`own`, the directory relay pool, the bridge lines)
  join the Psiphon ones above and follow the same rule — a profile that sets
  its own in the Aether editor keeps them. Both drivers' SOCKS listeners
  answer before a tunnel or circuit exists, so the connect waits for a real
  payload round trip through the chain: 120s for Psiphon, 300s for a cold Tor
  that must fetch a directory consensus first. There is no exit country to
  pick — Tor chooses its own.

- A Tor-carrying Aether connect is no longer aborted while it is bootstrapping
  normally. The engine opens Tor's listener only at the end of a bootstrap and
  `tor="chain"` dials that listener, so readiness waited on a cold consensus
  fetch with the budget of a WARP handshake: the log paired
  `aether engine on port 57xxx did not become ready in 30s` with
  `tor reaching the network: 15%: directory is fetching a consensus`, and the
  connect failed three times in a row while Tor was making progress. A run
  with Tor in the dialled path now gets the same 300s the Tor chain leg gets,
  and the log says so up front; a plain tunnel still reports in 30s.

- Pasting bridge lines into Settings → Chain no longer deletes them. The free
  text boxes there (the Tor bridge lines, the three Psiphon CDN fields) showed
  the stored value and saved only on blur, while the Chain tab re-renders once
  a second off the connection poll — so a re-render reset the box under the
  cursor and the paste was gone before it could be saved. The text now enters
  the settings as it changes with the disk write trailing it (one save per
  paste, not one per keystroke), and a blur or a tab switch flushes whatever
  is pending. Nothing reached `--tor-bridge` while this was open, which is why
  `bridges=own` appeared to be ignored.

- A Tor chain that cannot fetch its directory now says so, instead of going
  quiet for five minutes and quoting an escape code. Probes on a filtered
  network — through the tunnel and outside it, with hand-picked bridges, with
  the engine's own bridge list, with no bridges at all — all stalled in the
  same place: the bridges handshake, guards report themselves usable, and the
  consensus download comes back cut short
  (`tor_dirclient: Retiring circuit because of directory failure: Partial
  response`), after which the engine restarts the same download forever. The
  console could not show that: arti colourises every line it writes, so the
  log filled with `\x1b[2m…`, and the failure quoted one of those coloured
  lines as its "last output". Colour codes are now stripped wherever a core's
  output enters the log (viewer, last-lines ring, failure reports), the
  core's own give-up status (`[-] tor … Stuck at 15%: Can't bootstrap a Tor
  directory`) is logged as a warning the moment it appears, and the wait ends
  with that reason and the setting to change instead of `did not become ready
  in 5m0s`. The truncated download itself is the core's to fix — NekoGo can
  now at least tell you which of its settings still have a say.

- Bridge lines you saved but are not using are now named, on the Chain tab and
  in the log. Only `bridges=own` hands the lines to the engine, and the editor
  renders only under `own`, so moving the picker away after a paste left the
  lines stored, invisible and unused — the connect then waited the full 300s on
  the fetch that `auto` makes instead. The tab now counts them
  ("4 bridge lines saved, and auto does not hand them to the engine"), and both
  Tor legs log that difference as a warning the moment they start, from the
  arguments they are about to run, so a run cannot claim a shape it does not
  have. A hand-written engine command is left alone: it brings its own bridges.

- Psiphon's engine says whether it is here, and can be installed. It is
  `aether.exe` and its bridge transports live in `bin\pt\`, but nothing in the
  app named either: the Updates tab listed every
  other core and had no row for it, so "is the Psiphon engine installed?" had
  no answer anywhere — and a missing `pt\lyrebird.exe` read as a bootstrap stall
  rather than as a missing file. Settings → Updates gained an "Aether" row
  (previously misnamed "Psiphon engine (aether)"; the engine is the Aether
  core) that reads the `CluvexStudio/Aether` release: it reports the version,
  says "Not installed" when there is no engine, and adds "pt\lyrebird.exe is
  missing next to aether.exe — Tor bridges (obfs4, webtunnel) cannot launch"
  when the engine is present but its transports are not. The same number now
  appears in About → Version stats and in Copy diagnostics.

- That row installs the whole archive, not just the engine: `aether.exe` plus
  `pt\lyrebird.exe` and `pt\psiphon-tunnel-core.exe` from
  `aether-windows-x86_64.zip`, with the digest GitHub published checked before
  anything reaches the disk, a `.bak` and a `-v` smoke test around the engine
  swap, and a refusal while the Tor, Psiphon or Aether driver is running. The
  destination follows the `aetherPath` setting — and that setting used to
  accept a folder and hand the folder itself back, so the version query ran
  against a directory and the row reported the transports missing beside a
  binary named `003`. A folder now resolves to the `aether.exe` inside it, the
  way `mihomoPath` already did.

- The Psiphon server list has its own Settings → Updates row, PattN's
  check-update shape. "Psiphon Servers" reports how many entries the list in
  use carries and when it was published (the bundled list until a download
  lands), asks Psiphon's own `server_list_compressed` for its `Last-Modified`,
  and offers an update only when a newer list was published. Update asks with
  `If-Modified-Since`, accepts the download only when Psiphon's signature
  checks out and the list has at least 50 entries (PattN's floor), and writes
  it to `psiphon_servers.dat` in the Aether work dir with the published time.
  A failed or invalid download leaves the list in use alone, and the next core
  start unpacks the new list for `--psiphon-server-entries`.

- Tor and Psiphon are two engines and now have two Update rows. "Tor" was the
  aether row's other name, so the app could not say whether the Tor Project's
  own `tor.exe` was on the machine, had no way to download it, and reported
  Arti's version under Tor's name. The new "Tor (tor.exe, standalone)" row
  reads the Tor Project's download site rather than GitHub —
  `dist.torproject.org/torbrowser/<release>/tor-expert-bundle-windows-<arch>.tar.gz`
  — and installs what it finds: the published sha256 is checked before the
  archive is unpacked, `tor.exe` is swapped with a `.bak` and a `--version`
  smoke test, and `pluggable_transports\lyrebird.exe` comes with it so obfs4
  and webtunnel bridges still launch. The site versions that archive by Tor
  Browser (`16.0.1`) while `tor.exe` reports its own number (`0.4.9.16`) and
  publishes no mapping between them, so an install records the bundle it came
  from beside the binary and "update available" compares only that; a Tor
  installed outside NekoGo is reported as not comparable instead of nagging.
  `torPath` (Settings → Advanced → Core runtime) points at a different copy —
  a folder, or a Tor Browser tree, resolves to the `tor.exe` inside it.
  `bin\tor\` is staged into the portable zip and the installer the way
  `bin\pt\` already was when that directory is present in `build\bin`; the
  build does not download Tor automatically.

- The chain's Tor hop now runs on that `tor.exe` whenever the machine has
  one. Before, the hop was always aether's linked Arti, which stops at 15% of
  its bootstrap on the spoofed path — the bridges carry the handshake, no
  directory comes back through them — where the standalone daemon behind the
  same fronted SOCKS reaches `Bootstrapped 100%` and carries traffic (the two
  behaviours the Arti driver never had to model: `tor.exe` opens its SOCKS
  port at 0%, so readiness now waits for the daemon's own 100% line before it
  probes the listener, and its consensus and guard set are cached in a
  persistent `tor-state\` instead of a per-connect directory that makes every
  start pay the whole fetch again). Saved bridge lines go over as
  `--UseBridges 1` + `--Bridge` + a `--ClientTransportPlugin` naming the
  transport binary that serves each type, and a line this daemon cannot dial
  — a fingerprint that is not 40 hex digits, a webtunnel line with no `url=`
  front, a transport nothing beside `tor.exe` serves — is left out with a
  warning that describes its shape and never repeats the line, because a
  malformed line is a config failure that stops the run before it starts. A
  machine with no `tor.exe` still chains: the leg falls back to Arti, and the
  log names which engine carried the hop. A stray `tor.exe` left by a crashed
  GUI is reaped at startup with the other engines.

- The Aether leg's Tor hop takes the same split. `Aether → Tor` used to mean
  one flag more on the engine — `--tor`, so its linked Arti dialled out over
  the MASQUE tunnel it had just built — and that client stops at 15% here too:
  the tunnel is up in a couple of seconds, the directory never comes back
  through it, and the connect dies on
  `aether engine on port 49457 did not become ready in 5m0s`. With a `tor.exe`
  on the machine the engine now carries the tunnel alone and the daemon runs
  behind it as a second child, dialling the tunnel as its own SOCKS front,
  while the transport's aether outbound is rendered against the daemon's
  listener instead of the engine's. Connect logs which of the two it chose,
  and refuses a daemon whose tunnel died mid-bootstrap instead of waiting out
  its five minutes for a front that is gone. It stays Arti where the split
  would take something away: no `tor.exe` on the machine, a profile whose
  hand-written `Aether.Command` names its own exits, and a profile that chains
  Psiphon too — one driver per leg, and there Psiphon owns the dialled
  listener. The hop is a member of the committed group, so its death takes the
  connect down, a rollback stops the daemon before the tunnel it dials, and a
  sticky reconnect never hands one child of a two-child leg to the next
  connect.

- Tor's readiness budget now follows the cache the run can ride. Measured with
  the daemon behind a MASQUE tunnel on the bridges this user pasted: the first
  bootstrap took `5m43s`, and the one after it — reading the consensus out of
  the state directory the first left behind — took `9s`. Five minutes was
  therefore enough to fail the very first connect of an install and to succeed
  on every one after it, which is how "aether connects but tor does not" comes
  to read as intermittent. A hop whose state directory is empty now gets ten
  minutes and logs `no Tor directory cached under … yet, so this bootstrap gets
  10m0s for its first fetch instead of 5m0s`; a base a caller already thinks is
  longer is never cut. The Arti `--tor-only` leg gets the same budget every
  time, because it runs in a `--tor-dir` created for the connect and removed by
  the next one, so it is never the second run. The bridge lines themselves are
  worth a look too: `tor.exe` dials each of them, so an unreachable one — a
  retired bridge, or a line a test fixture pasted in — is minutes of retries
  before the fetch finishes.

- A chain this connect does not run is now named instead of quietly unused.
  The Chain tab's switches belong to a leg: `SNI spoof → Tor/Psiphon` needs a
  profile that goes through the spoofing engine, `Aether → Tor/Psiphon` needs
  one that IS an Aether tunnel naming no mode of its own. Arming Tor and
  connecting an ordinary node produced a normal "connected" state with no Tor
  anywhere in the path and nothing saying so. Connect now logs the leg it
  skipped and why — `the Aether Tor chain is armed, but "My SS" is not an
  Aether node: the engine runs no Tor on this connect` — and stays quiet when
  the chain really does run, including a profile that brings its own
  `tor=chain` rather than the switch.

- That check is now the whole connect's, not one engine's. Every fixed/known
  port the connect will bind is collected first — the mixed inbound, Xray's
  second port and API listener, the listeners a hand-written Aether command
  names, the inbounds a raw config override binds — and all of them are waited
  out and refused before the first child exists. Mihomo's external-controller
  port is picked later and is not part of this fixed-port preflight. Xray and
  mihomo used to refuse a held port instantly, so a socket the previous connect was
  still releasing cost a click; they get the same two-second window now.
  Ports the app picks for itself while the connect runs (a chain hop's
  listener, a Tor daemon's SOCKS port) are claimed as they are chosen, so a
  later picker cannot hand the same number to a second leg — the Arti hop
  binds its listener only at the end of its bootstrap, minutes during which
  the port looked free to everyone else. An engine whose own listener is
  taken now says so by port number before its readiness budget starts,
  instead of spending five minutes and reporting a timeout; and because the
  app numbers that listener, its remedy says to close whatever holds it
  rather than to change a setting that cannot move it.

- That check now covers every listener an Aether run binds, not only the
  one the app dials. With Tor or Psiphon inside the tunnel the engine
  listens on three consecutive numbers (`--bind`, `--tor-bind`,
  `--psiphon-bind`), and only the dialled one was looked at: a port taken
  two numbers up was handed to the engine, which then failed to bind it
  and spent its whole readiness budget saying nothing. A candidate range
  is now refused unless every number in it is free on TCP *and* UDP (the
  engine binds both) and unclaimed by another leg of the same connect,
  and the whole range is claimed as it is chosen.

- A connect could fail with `no loopback port free for TCP and UDP` on a
  machine with thousands of free ports. Windows reserves contiguous UDP
  bands inside the ephemeral range (WinNAT/Hyper-V — nine of them here,
  including 64826-65025), and the kernel answers a `:0` bind with the
  *next* number in sequence, so all five retries landed in the band the
  first one was refused in: 64844, 64850, 64855, 64869, 64878. The Aether
  engine, the Tor daemon hop and mihomo all pick their listener through
  that one function, so the whole connect died on it. After a refusal the
  picker now jumps to a random port in the range instead of asking for the
  next one, and still refuses a number another leg of the connect claimed.

- An Aether connect is no longer killed at 30 seconds while it is still
  working. A MASQUE scan on a filtered network was measured answering
  SOCKS at 48s, and the hard deadline took the engine down at 30 — so the
  connect failed and the retry that finally worked was luck, not a fix.
  Readiness now waits in 30-second rounds for as long as the child lives
  and keeps talking, the way the reference client does ("a scan may take
  minutes"), and each round that passes says so in the log instead of
  ending the run. The bounds are the engine's own: it exits, it says it
  cannot go on, it goes silent for a whole round after naming a stall (or
  after a Tor bootstrap that stopped short of a circuit), or a Disconnect
  stops this spawn.

- The engine's own words now end that wait, with the reason it gave.
  Three of them were logged and then ignored while the probe kept asking
  a listener that would never carry traffic: the carrier inside the
  tunnel reporting its end (`[-] tor: … circuit failed`, the Psiphon
  equivalent), `ECH is on but there is no ECH key to offer` (the core
  stops rather than send the server name in the clear — the message now
  says to turn ECH off or name a resolver that can fetch the key), and a
  bare `Error:` line. Each is reported in the core's words instead of a
  timeout minutes later.

- A saved Aether endpoint that has gone dead is no longer pinned forever.
  The engine announces `last known-good gateway … no longer responds;
  rescanning` and then re-uses the forced peer anyway (`probe skipped`),
  so it dials the same dead gateway until it is killed — measured at 170s
  with no listener, while the same profile without the pin scanned and
  answered in 48s. The connect now runs the engine once more without the
  pin, as the app's own Scan does, and says which endpoint it gave up on.
  The profile keeps what the user saved; only that run looks elsewhere.

- Aether profiles that never touched the fragment setting were sent
  `--no-fragment`, switching off a defence the engine enables by default:
  Iran's firewall resets a whole ClientHello whose SNI ends in
  `cloudflareclient.com`, which is both the MASQUE handshake and the WARP
  API calls. The flag is now only sent when fragment is actually asked
  for, and the size/delay bounds are still checked when it is.

- The SOCKS readiness probe read one byte of the reply and called any
  `5` a listening tunnel. Two probes of dead loopback ports can be joined
  by a TCP simultaneous open, and each then reads the *other's* greeting
  — which also begins with the version byte — so a connect could be
  declared up against a port nothing serves. It now reads the whole
  method selection and requires a method the greeting offered.

- About → Version stats labelled the NekoBox core row "NekoBox core" while
  displaying a version; it reads "NekoBox version" now. Its engine versions
  read as banners — `Xray 25.9.11 (v2fly.org) go1.25.0 windows amd64` in a
  card two lines from a row that said `windows/amd64` — and now show the
  number a bug report asks for (`v25.9.11`), while Copy diagnostics keeps the
  full lines. The idle "Engine" core path stays an Advanced-mode row: Simple
  mode chooses no core and shows no core path, so that one row described a
  screen its user cannot reach. Copy diagnostics still carries every one of
  them — a bug report needs the engine facts whether or not the card shows
  them.

- The native system tray menu was a text-only list. Show, Connect, Reconnect,
  Disconnect/Cancel, Profiles and Quit now carry embedded 16×16 Windows 10-style
  icons, including the busy-state cancel row. This is a visual update: the
  tray's Reconnect and Open Profiles actions still lack production callbacks
  and can be no-ops; the frontend/backend Reconnect button is separate.

- The system tray menu follows the app's theme. It is native Win32 UI, so the
  WebView theme could not reach it; NekoGo now sets the process's preferred
  app mode through uxtheme when Settings saves a theme (and at startup), and
  flushes the menu so a switch needs no restart. The calls are best-effort:
  a Windows build without those exports keeps its usual menu colour instead of
  failing the save or the start.

- Publish hygiene for the source tree, checked against what a fresh clone of
  it can actually do. `.gitignore` still carried the Qt NekoBox rules, where
  a blanket `*.json` and `*.dll` cost nothing: they had hidden
  `gui/frontend/package.json`, `package-lock.json`, `tsconfig.json`,
  `gui/wails.json` and `runtime/WinDivert.dll` from the repository, so a
  clone could not `npm install` — and so could not produce the
  `frontend/dist` that `main.go` embeds — while `build.bat`, which copies
  that DLL with `if exist`, reported BUILD OK and packaged an install whose
  SNI engine had no driver. Those names are re-included, and the reference
  and scratch trees (`PattN`, the `source*` exports, backups, the upload
  output, the `xray` clone) are now ignored, so `git add -A` stages this
  project instead of several hundred megabytes of other people's trees.
  `export-source.bat` no longer exports `sni spoofing\spoof_config.txt` — a
  subscription dump of thousands of live links with real passwords — or the
  MaxMind-licensed `*.mmdb`, and its last step refuses to call the export
  GitHub-ready while any file in it is a list of proxy links one per line,
  which is the shape a dump has and no source file has (6,645 matching lines
  in the dump, 0 across the exported tree). The node host, its password and
  the public free-node links that test fixtures had copied out of real
  configs are replaced with reserved placeholders (`node.example`,
  `2001:db8::`, `198.51.100.7`), and the comments that sent a reader to a
  private scratch file now name the fixture instead. Anything pasted into
  this tree from a live subscription should be treated as published and
  rotated: deleting a file does not remove it from a repo's history.

- A connect that goes slow could only be watched. The button reads
  "Connecting…" and disables itself while the core starts — tens of seconds
  on a chained profile, longer still when an engine has to be downloaded and
  unfrozen first — and the only way out was the Disconnect control on the
  other side of the screen, or waiting. While a connect is in flight the
  button is now a Cancel, and it asks for the stop the Disconnect does: the
  core manager bumps an epoch that a starting connect checks, so the half
  built legs get reaped rather than orphaned. That abort path already existed
  for a disconnect racing a connect; nothing in the UI could reach it. The
  message it returns is no longer treated as a failure, either — a cancelled
  connect clears back to Disconnected without the error line or the
  elevation prompt that keys off it, and Cancel itself disables while the
  stop is still running.

- A connected tunnel gained a Reconnect button: stop the profile that is up
  and start that same one again, which is what a node that has quietly gone
  stale needs. It refuses while any other action is in flight, and it does
  not pretend a failed second half is a success.

- That button now has a mode (Settings → Chain → Reconnect, Advanced
  mode), because "restart the tunnel" has two very different costs.
  `restart` (the default, and what the button did before) takes every leg
  down and raises them again — the exit chain included, so a cold Psiphon
  handshake or Tor consensus fetch is paid once more, minutes on a bad
  network. `keep the last chain hop` hands the SNI-spoof chain's `aether.exe`
  child to the connect that follows, on the same two ports it was already
  listening and dialing on, so only the spoof engine and its transport are
  remade; the Aether leg is never in that offer, because its chain runs
  inside the engine process and dies with it. This mode also keeps the
  button below Connect whether or not a tunnel is up — pressing it then is
  a plain connect — and `off` removes it entirely. The keep is armed and
  spent inside one teardown, and only a spoof-chain connect asking for the
  same driver adopts it: a plain Disconnect can never inherit it, and a
  connect that does not take the leg reaps it rather than leaving a child
  serving a port no config names. A Stop that reports a problem is logged
  and the connect goes ahead anyway; this control exists to recover a stuck
  tunnel. The stop-then-start order the frontend used to run moved into
  `App.Reconnect`, because a leg cannot survive a teardown the client makes
  in a call of its own.

- The custom caption buttons did not fit their bar. The global button rule
  asks every button on screen for a 36px touch target, `min-height` beats the
  caption rule's `height: 100%`, and the title bar is 34px — so all three
  buttons stood 2px proud, their hover painted over the edge of the page
  below, and Close's red band reached into the content. `button:active`'s
  `translateY(1px)` rode along too, which is not what a Windows caption does.
  They are square, flush and immobile now; the reset sits in the caption rule
  because the global one is what the rest of the app sizes against.

- Clearing the console did not survive a tab switch: the entries were gone,
  and the next time the log was opened every one of them was back. The
  component that rendered the log kept the cleared ids, and the panel lives
  behind a `v-else-if`, so switching tabs unmounted it and took the memory
  with it. The clear is a watermark over the log's ids in the connection
  store now, and the view filters on it, so it lasts as long as the session
  that asked for it.

- Renaming a subscription moved more profiles than the subscription owned.
  Profiles carry the feed's display name as their group, and the rename
  updated every row matching the *old* name — so a second link from the same
  provider, given the same name, left with it, as did the hand-added profiles
  in a remark group that shared the name. The next refresh of the abandoned
  feed stamped its own nodes back under the old name and the group split in
  two. A rename now re-files the feed's own profiles by their subscription
  URL, and only those still wearing the old name — a profile the user moved
  somewhere else stays where they put it. Profiles imported before the URL
  pointer existed still follow the rename, because the name is the only
  evidence they have, unless that name is also a group the user created.

- A collapsed feed could disappear from the Proxies tab. The list renders in
  bounded batches, and the walk over groups left the loop as soon as the row
  budget was spent — so every group after that point lost its header, folded
  ones and empty remark groups included, and the "showing N of M" count only
  ever adds rows, so there was nothing left to scroll to. A header costs no
  rows and now always renders. Grouping also became something you can do in
  bulk, which it never was: each group header carries a checkbox that selects
  the whole group — folded and off-screen members included, matching the count
  beside it — and the bar that already deletes, tests and exports a selection
  has Move to group next to them.

- The Aether tab is PattN's editor now: its field order, its wording and,
  above all, its gates. Every row used to be on screen at once, so a profile
  could carry a fragment size for a protocol that never fragments and an
  obfuscation mode the core ignores. Rows now appear only where the engine
  can use them — the WARP rows (protocol, scan mode, IP version, endpoint)
  go when Psiphon or Tor runs alone; transport, MASQUE SNI, ECH and
  fingerprint only where the protocol rides MASQUE; fragment only over
  HTTP/2; obfuscation not at all over MASQUE/h2 or a reverse chain, which
  carry TCP the shaper does not touch; and the endpoint is one host:port or
  an outer/inner pair, whichever the protocol dials. The address may be left
  blank to let the engine scan for one, which the tab refused outright
  before; the port is still checked, and a profile now needs a name.

- The same tab gained the inputs PattN offers instead of bare text boxes:
  ECH resolver and domain presets, the nine Psiphon CDN edge lists as
  checkboxes (none checked means all of them) under a summary naming the
  chosen ones, the bundled-server-list switch as a checkbox that says what
  each side costs, and fragment bounds suggesting 8-16 / 2-10. DNS resolvers
  and the exit-country rule moved into an "Other settings" fold; it and the
  CDN fold open by themselves for a profile that already holds a value in
  them, so switching profiles never hides a setting that is set. The command
  box shows the line these settings build, rendered by the backend that will
  run it, and editing it runs that line as written — with a note saying the
  settings above are then not applied, and a "Use the settings" button that
  returns to the generated line. MASQUE SNI is kept for parity and says
  plainly that aether 2.3.0 has no flag for it, so the value is saved but not
  sent.

- Aether's fingerprint list gained `chrome-120`: Chrome's TLS 1.2 suites
  with `--tls-groups X25519:P-256:P-384`, so the ClientHello offers no
  post-quantum key share. That share is the one part of a modern Chrome
  hello a core of this vintage can leave out, and it is what PattN's
  chrome-120 preset exists for; asking for it here used to mean writing the
  whole command by hand.

- The Aether tab gained PattN's WARP keys window as a card: new WARP
  identities can be registered without leaving the tab. It offers the kinds
  aether 2.3.0 actually accepts (`all`, `wg`, `masque`, `gool`, `mim`), the
  API address, ClientHello fragment and ECH for the calls to the WARP API,
  the TLS fingerprint, and an optional Tor or Psiphon carrier for networks
  that block the API outright. The identities in use are listed with their
  device id and assigned addresses. A run stages the new keys beside them and
  moves them into place only once every file of that kind reads as an
  identity, so a failed or cancelled registration keeps the keys that work;
  new keys are used from the next connection. The card refuses to start while
  a tunnel is up or a scan is running, and says which. PattN's API server
  name, API host and exit-node rows are left out — they belong to a newer
  fork, and this core refuses them.

- The "Bundled server list" switch on a Psiphon hop now does what it says. It
  was saved and rendered but never reached the engine, so every first
  connection still had to download Psiphon's server list — the one fetch a
  filtered network blocks before the tunnel can start. NekoGo now ships that
  list (Psiphon's own signed bundle, 426 entries, checked against the real
  signing key) and hands it to the core with `--psiphon-server-entries`,
  unpacked into the profile directory. The core merges anything it fetches on
  top, so the shipped list is a bootstrap rather than a ceiling; Settings →
  Updates → "Psiphon Servers" can replace it with a newer signed list without
  waiting for the next NekoGo build.

- An Aether profile with a Psiphon hop is no longer killed while it is still
  connecting. Readiness is measured in rounds of silence and the plain engine
  round is 30s, but Psiphon says nothing at all while it fetches tactics: a
  live run on a filtered network printed nothing for 54s and then came up
  ready with a working exit. The hop now gets the same 120s round the Psiphon
  chain leg already had, and Tor's longer round still wins when both carriers
  are on. The log line naming the carrier now names the one actually in the
  dialled path instead of always saying Tor.

- The Proxies editor can save a two-hop Aether profile again. `gool`, `mim`
  and `wg-over-masque` dial an outer/inner pair rather than one endpoint, so
  the editor hides the address and port rows — but the save path still
  demanded a port in 1..65535 and refused the profile with `Port must be
  1..65535 (got 0)`, including one the Aether tab had just written. A profile
  whose endpoint row is hidden is now exempt, as a raw-outbound profile
  already was. A single-hop Aether profile with an unusable port is still
  refused.

- The server list no longer prints an endpoint an Aether profile does not
  dial. A two-hop or carrier-only profile carries no address and port 0, and
  every list showed `:0` — the Home card and the row's screen-reader label
  included — as though that were where it connects. Those rows now say
  `scan`, or `via psiphon` / `via tor` when a carrier runs alone, and a pinned
  outer endpoint is shown as written. A single-hop profile with a blank
  address said `:443` for the same reason and now says `scan` too, matching
  the editor's own "blank = scan".

- Saving any setting no longer throws away SNI scan results found since the
  app started. The save path carried feeds, Aether scan results and core
  overrides over from disk, but the SNI address and name lists came from the
  snapshot the frontend loaded once at startup — so a scan that finished, or
  was cancelled (which also stores what it found), was overwritten by the next
  unrelated settings save. Both lists are carried over the same way now.

- A latency test on a connected Aether profile with a Tor chain hop measures
  the chain. It dialled the tunnel engine's own SOCKS listener instead of the
  published port, which for a real tor.exe hop is the daemon's listener — so
  the test bypassed Tor and reported WARP-only latency as the chain's, and a
  hop that routes through Tor looked no slower than WARP alone.

- A disconnect or an engine fault that raced a newer connect no longer hides
  the new engine's listener. Stopping an Aether engine can take three seconds
  to reap its process tree, and a connect that started inside that window
  published its dial port only to have the older stop clear it: the core then
  rendered "no engine" while aether.exe was healthy and serving — connected,
  no traffic. A stop now unpublishes only the port it owned.

- A failed WARP key installation could replace some existing identities
  while reporting that none had changed. Existing keys now go aside before
  replacement so a failed installation can roll back. Staged keys are copied
  rather than moved, preserving the renewal for a later attempt. This is
  rollback on reported file errors, not a crash-atomic multi-file transaction.

- An edit in the WARP keys card was lost if you left the tab within half a
  second of typing it. The card saves once typing settles, and leaving the
  Aether tab destroys it — which cancelled the pending save instead of
  writing it, so the field was blank when you came back. The pending edit is
  written on the way out now.

- A custom WARP registration command now determines the expected key files
  and progress kind. Previously the dropdown still controlled those checks:
  `--register wg` could fail after succeeding, while an incomplete `mim`
  registration could pass when the dropdown requested only `masque`.
  Missing or unsupported registration kinds are rejected before starting.

- A failed WARP identity copy now removes the destination it created, rather
  than leaving an empty or partial file that blocks retry. Existing files
  remain untouched when a copy is refused.

- Cancelling Connect is now atomic across preflight, engine startup and system-
  proxy setup. A cancel invalidates the app-level attempt and the core
  manager's startup generation, including a Stop that lands before core
  resolution, drains the stale attempt before allowing another one, reverts a
  proxy installed by the cancelled attempt, and reports no red failure for the
  user's own Cancel. The tray advertises Cancel connection as soon as startup
  begins and clears it when the attempt drains.

- A Connect that finishes after the user selected another server no longer
  writes the old selection back to disk. Active-profile updates now compare
  the value seen before startup and persist only while it is still current,
  including the elevation-relaunch path.

- Replacing a subscription address when every parsed link fails validation no
  longer records the unusable address or leaves the working feed pointing at
  it. The new address is rolled back and the rejection count is reported;
  duplicate-only replacements remain valid. A profile whose disk write fails
  is also removed from memory, so it cannot reappear under a later save;
  rejected whole-settings and active-profile writes restore their previous
  in-memory values as well.

- Legacy config migration rewrites only known filesystem settings (core and
  engine paths, output/log paths and GeoIP). Profile names, passwords,
  subscription addresses, test URLs and raw outbound JSON are no longer
  changed merely because their text contains `NekoBox`.

- The profile editor's Core picker now appears only for raw outbound JSON,
  where it selects the JSON schema and offers only sing-box, Xray and Mihomo.
  Ordinary profiles say that their engine comes from Settings → Connection,
  and neither the Proxies list nor Advanced Config labels or blocks an ordinary
  profile as Mihomo just because its old core field says so.

- Rejected theme, Simple/Advanced-mode, last-tab and peek saves now roll back
  the affected setting instead of leaving a control that claims an unsaved
  value; a rejected theme also restores the DOM class. Whole-settings writes
  are serialized, and a failed write rolls back only fields that no newer action
  has changed, using the last successfully persisted value so even repeated
  failed switches cannot leave an
  unsaved intermediate state. Sidebar, theme and mode controls surface failures
  in the shell, server selection failures are no longer silent, and offline
  selection stays local.

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

### patterniha
- **[PattN](https://github.com/patterniha/PattN) / [PattNG](https://github.com/patterniha/PattNG)**:
  the reference for NekoGo's Aether integration. The engine lifecycle,
  profile fields and their order, WARP keys card, endpoint scan and
  TUN options are modeled on PattN's design.
- **[SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing)**:
  the original idea behind the SNI-spoofing engine, which NekoGo
  rewrote in Rust.

### Engines
- [CluvexStudio/Aether](https://github.com/CluvexStudio/Aether)
- [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo)

### Other projects and libraries
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
