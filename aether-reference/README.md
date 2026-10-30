# Aether reference (PattNG → NekoGo)

This folder is a read-only reference extraction from **PattNG**, the v2rayNG fork
that added Aether support. It exists so the NekoGo Go/Wails port can copy the
exact profile, `aether://` link, CLI-argument, and editor logic without reading
the whole Android codebase every time.

Nothing here is a buildable Android or Kotlin module. The files are the raw
logic with Android plumbing removed; Kotlin package names are intentionally kept
(`com.v2ray.ang...`) so every declaration can be traced back to its original
file. The folder names (`core/`, `args/`, `ui/`) are only an index.

## Source repositories

| What | URL |
|---|---|
| PattNG (Android app source) | https://github.com/patterniha/PattNG |
| Aether core (the `aether` binary the arguments drive) | https://github.com/CluvexStudio/aether |
| PattNG's Aether submodule/fork | https://github.com/patterniha/Aether.git |
| Upstream v2rayNG (PattNG's base) | https://github.com/2dust/v2rayNG |

`AppConfig.AETHER_URL` in PattNG points at `https://github.com/CluvexStudio/aether`,
and the checkout's `.gitmodules` names `https://github.com/patterniha/Aether.git`
for the `aether/` submodule. The extracted argument builder targets the CLI of
those cores.

The Android source root used for all provenance paths below is:

```
PattNG/V2rayNG/app/src/main/java/com/v2ray/ang/
```

## Files in this folder

| Extracted file | Original source | What it contains |
|---|---|---|
| `core/AetherConfig.kt` | `dto/entities/ProfileItem.kt` lines 7–166 | Distilled struct of the Aether profile fields. See the note below: PattNG has no nested `AetherConfig` class; these are top-level `ProfileItem.aether*` properties, plus the shared `server`, `serverPort`, `finalMask`, `dialMode`, `targetStrategy` fields that also drive the core. |
| `core/AetherDefaults.kt` | `AppConfig.kt` lines 141, 155, 196, 199–200, 206, 209, 215, 282 | Constants the link parser, validator, and argument builder fall back to. |
| `core/AetherOption.kt` | `enums/AetherOption.kt` | Every Aether enum: `AetherProtocol`, `AetherTransport`, `AetherScanMode`, `AetherObfuscation`, `AetherIpVersion`, `AetherPsiphon`, `AetherPsiphonCdnSet`, `AetherPsiphonMode`, `AetherTor`, `AetherTorBridges`, `AetherTorRelays`, `AetherKeyKind`, `AetherFingerprint`, plus their `fromString`/`parse`/`join` helpers. |
| `core/AetherEndpoint.kt` | `dto/AetherEndpoint.kt` | `host:port` parser/canonicaliser used by `--peer`, hop fields, `--dns`, ECH resolvers, and link authority. Handles IPv4 (no leading zeros) and IPv6 (compressed at the longest zero run). |
| `core/AetherRange.kt` | `dto/AetherRange.kt` | `min-max` parser for fragment size/delay, with bounds `1..4096` and `0..1000`. |
| `core/AetherFmt.kt` | `fmt/AetherFmt.kt` | The complete `aether://` share-link parser (`parse`, line 85), exporter (`toUri`, line 145), and profile validator/normalizer (`normalize`, line 232), including all `Problem` values and the small `Utils`/`FmtBase`/`CoreOutboundBuilder` helpers it used. |
| `core/AetherCore.kt` | `core/AetherCore.kt` lines 20–175 | Profile ↔ command bridge: `of(profile)` (custom command or settings), `ofCommand(text)` (parse command back), `command` (render `aether <args>` for the editor), listener/port/protocol introspection. The Xray `AetherExit` half of the original data class is intentionally omitted. |
| `args/AetherArgs.kt` | `core/AetherCoreManager.kt` — `buildArguments` lines 280–429 plus helpers | The authoritative profile → `aether` CLI argument builder (`buildArguments` at extracted line 176), port allocation, log-level mapping, and argument introspection (`listenerPortOf`, `protocolOf`, `pathOf`, `torModeOf`, `psiphonModeOf`, …). |
| `args/AetherKeys.kt` | `core/AetherKeys.kt` | The `aether --register …` argument builder and validation used by the "Get new WARP keys" page. |
| `ui/AetherEditorFields.kt` | `ui/server/ServerUiState.kt` — constructor lines 89–118, Aether properties 173–214, mappings 328–372 and 433–464 | Editor field state with PattNG's exact defaults, CDN-set toggle, `hasOtherAetherSettings`, and `ProfileItem` round-trip mapping. Compose `mutableStateOf`/`Saver` removed. |
| `ui/AetherEditorLayout.md` | `ui/server/ServerAetherActivity.kt` lines 105–703 and `res/values/arrays.xml` | Field order, visibility rules, labels, option values, and the save/validation flow of the Aether editor. |

## Data model

`ProfileItem.create(EConfigType.AETHER)` (`EConfigType.AETHER(500, "aether://")`)
creates the profile. There is **no** `ProfileItem.aetherConfig` nested object in
PattNG. The Aether settings are top-level nullable properties; `null` almost
always means "use the default" or "feature off".

`core/AetherConfig.kt` lists them with the same names and nullability so the Go
struct can match one-to-one. The groups are:

- identity: `remarks`, `server`, `serverPort`
- protocol/transport/scan: `aetherProtocol`, `aetherTransport`,
  `aetherMasqueSni`, `aetherScanMode`, `aetherObfuscation`,
  `aetherFingerprint`, `aetherIpVersion`
- two-hop endpoints: `aetherWiwOuter`, `aetherWiwInner`
- HTTP/2 fragmentation: `aetherFragment`, `aetherFragmentSize`,
  `aetherFragmentDelay`
- ECH: `aetherEch`, `aetherEchDns`, `aetherEchDomain`
- tunnel DNS/exit rule: `aetherDns`, `aetherExitLoc`
- exit node: `aetherExitNode` (profile name, not in any link)
- legacy: `aetherListenPort` (stored but no longer read)
- Psiphon: `aetherPsiphon`, `aetherPsiphonMode`, `aetherPsiphonCdnIps`,
  `aetherPsiphonCdnSni`, `aetherPsiphonCdnSets`, `aetherPsiphonRegion`,
  `aetherPsiphonBundledList`
- Tor: `aetherTor`, `aetherTorBridges`, `aetherTorBridgeLines`,
  `aetherTorRelays`
- custom command: `aetherCommand`
- shared fields: `finalMask`, `dialMode`, `targetStrategy`

## Config → CLI arguments

`AetherArgs.buildArguments(profile, port, scan = false, logLevel = "info")`
(`args/AetherArgs.kt`, translated from `AetherCoreManager.kt:280`) is the exact
mapping. It always returns an even-sized `option value` list except for the
boolean flags (`--gool-classic`, `--h2`, `--fragment`, `--quick-reconnect`,
`--no-quick-reconnect`, `--tor`, `--tor-reverse`, `--tor-only`, `--tor-bridges`,
`--no-tor-bridges`, `--psiphon`, `--psiphon-reverse`, `--psiphon-only`).

### Fixed option order

`buildArguments` appends in this order. A porting implementation must keep this
order: the editor preview and PattNG's tests assert it.

| # | Argument | Emitted when | Value |
|---|---|---|---|
| 1 | `--bind` | always | `127.0.0.1:<own>`; see the port table below |
| 2 | `--protocol` | neither Psiphon nor Tor is `only` | `AetherProtocol.core`: `masque`, `wg`, `gool`, `mim`; `wg-over-masque` also becomes `gool` |
| 3 | `--gool-classic` | protocol is `GOOL` (`WARP-in-WARP`) | no value |
| 4 | `--scan` | WARP block | `AetherScanMode.type` (`turbo`, `balanced`, `thorough`, `verified`, `ironclad`) |
| 5 | `--noize` | WARP block, obfuscation is not `auto`, and not MASQUE-over-HTTP/2 | `AetherObfuscation.type` (`off`, `light`, `firewall`, `balanced`, `gfw`, `aggressive`) |
| 6 | `--ip` | WARP block | `AetherIpVersion.type` (`v4`, `v6`, `both`) |
| 7 | `--dns` | WARP block and `aetherDns` non-blank | first valid setting value |
| 8 | `--exit-loc` | WARP block and `aetherExitLoc` non-blank | first valid setting value (also kept for scans) |
| 9 | `--masque-sni` | WARP block and `protocol.overMasque` | `aetherMasqueSni` trimmed, or `www.cloudflare.com`; **always emitted for MASQUE protocols, including the default** |
| 10 | `--h2` | WARP block, `protocol.overMasque`, and transport is `h2` | no value |
| 11 | `--fragment` | as `--h2`, and `aetherFragment == true` | no value |
| 12 | `--fragment-size` | `--fragment` and `aetherFragmentSize` parses against `1..4096` | `min` or `min-max` |
| 13 | `--fragment-delay` | `--fragment` and `aetherFragmentDelay` parses against `0..1000` | `min` or `min-max` |
| 14 | `--ech` | WARP block, `protocol.overMasque`, `aetherEch == true` | literal `auto` |
| 15 | `--ech-dns` | as `--ech` | `aetherEchDns` trimmed, or `udp://1.1.1.1` |
| 16 | `--ech-domain` | as `--ech` | `aetherEchDomain` trimmed, or `cloudflare-ech.com` |
| 17 | `--tls-ciphers` | WARP block and `protocol.overMasque` | `AetherFingerprint.ciphers` |
| 18 | `--disable-grease` | same, only for fingerprints whose `grease == false` | no value |
| 19 | `--peer` | `wg-over-masque`, not a scan, `aetherWiwOuter` parses | outer MASQUE gateway |
| 20 | `--gool-peer` | `wg-over-masque`, `aetherWiwInner` parses | inner WireGuard endpoint (kept in scans) |
| 21 | `--wiw-outer` / `--mim-outer` | `gool`/`mim`, not a scan, outer hop parses | canonical `host:port` |
| 22 | `--wiw-inner` / `--mim-inner` | `gool`/`mim`, not a scan, inner hop parses | canonical `host:port` |
| 23 | `--wiw-scan` / `--mim-scan` | two-hop protocol, not a scan, and both hop fields are empty/invalid | no value |
| 24 | `--peer` | one-hop protocol, not a scan, and `server:serverPort` parses | canonical `host:port` |
| 25 | `--quick-reconnect` | WARP block, not a scan | no value |
| 26 | `--no-quick-reconnect` | WARP block and scan | no value |
| 27 | `--tor` / `--tor-reverse` / `--tor-only` | `aetherTor` is not `off` | mode flag |
| 28 | `--tor-bind` | Tor listener has a real port (see below) | `127.0.0.1:<port>` |
| 29 | `--tor-bridges` | Tor on and bridges mode is `first` | no value |
| 30 | `--no-tor-bridges` | Tor on and bridges mode is `never` | no value |
| 31 | `--tor-bridge` | Tor on and bridges mode is `own` | one argument per non-blank bridge line |
| 32 | `--tor-relays` | Tor on, bridges mode `auto` or `first`, relays not `auto` | `only` or `off` |
| 33 | `--psiphon` / `--psiphon-reverse` / `--psiphon-only` | `aetherPsiphon` is not `off` | mode flag |
| 34 | `--psiphon-bind` | Psiphon listener has a real port (see below) | `127.0.0.1:<port>` |
| 35 | `--psiphon-mode` | Psiphon on | `auto`, `cdn`, or `direct` |
| 36 | `--psiphon-cdn-ips` | Psiphon on, mode is not `direct`, `aetherPsiphonCdnIps` valid | comma/space list |
| 37 | `--psiphon-cdn-sni` | the previous line was emitted and `aetherPsiphonCdnSni` valid | comma/space list |
| 38 | `--psiphon-cdn-sets` | Psiphon on, mode is not `direct`, selected sets not empty | `cloudflare,fastly,…` in built-in order |
| 39 | `--psiphon-region` | Psiphon on and `aetherPsiphonRegion` valid | region code |
| 40 | `--psiphon-server-entries` | Psiphon on and `aetherPsiphonBundledList != false` | literal `shipped-list` (the real file path is substituted by the app at launch) |
| 41 | `--log-level` | always | `logLevel` (default `info`) |

Notes on the table:

- The whole WARP block (items 2–26) is skipped when `aetherPsiphon == "only"`
  or `aetherTor == "only"`. The carrier flags and their settings remain.
- `protocol.overMasque` is true for `masque`, `mim`, and `wg-over-masque`.
- `masqueOverHttp2(protocol, transport, tor, psiphon)` is true when the protocol
  is over MASQUE **and** (`transport == h2` **or** Tor/Psiphon is `reverse`).
  That value suppresses `--noize` and gates `--h2`/`--fragment` (only when the
  user explicitly chose `h2`).
- `settingValue()` trims and drops a value that is blank or starts with `-`.
  This is a security measure: `dns=--upstream` in a link must not become a new
  core option.
- `--masque-sni` is unconditionally emitted for any MASQUE protocol, so the
  default server name is visible in the editor's command preview.
- `GOOL` (classic WARP-in-WARP) is the only protocol that gets
  `--gool-classic`. `WG_OVER_MASQUE` uses `--protocol gool` without the flag.
- `--quick-reconnect` / `--no-quick-reconnect` sits inside the WARP block, so a
  Tor-only or Psiphon-only command never carries it.

### Port allocation

`buildArguments` first decides what the app dials:

```kotlin
val dialsPsiphon = psiphon == AetherPsiphon.CHAIN
val dialsTor     = tor == AetherTor.CHAIN && !dialsPsiphon
```

Then every listener gets a port from `port` upward. `port` is normally the
Aether listen port (`10819` by default, from the app settings), but a scan may
pass another value; `scanPort(profile)` uses a random free port only when Tor is
`reverse`, otherwise `0`.

| Profile state | App dials | `--bind` (core) | `--tor-bind` | `--psiphon-bind` |
|---|---|---|---|---|
| no carrier | `--bind` | `port` | — | — |
| Tor `chain` only | Tor | `port + 1` | `port` | — |
| Psiphon `chain` only | Psiphon | `port + 1` | — | `port` |
| Tor `reverse` only | `--bind` | `port` | `port + 1` | — |
| Psiphon `reverse` only | `--bind` | `port` | — | `0` (ephemeral) |
| Tor `chain` + Psiphon `reverse` | Tor | `port + 1` | `port` | `0` |
| Psiphon `chain` + Tor `reverse` | Psiphon | `port + 1` | `port + 2` | `port` |
| Tor `only` | `--bind` | `port` | — | — |
| Psiphon `only` | `--bind` | `port` | — | — |
| any, with `scan = true` | same rules | same rules | same rules | `0` for Psiphon `reverse` |

`--tor-bind` is emitted whenever Tor is `chain` or `reverse`; `--psiphon-bind`
is emitted whenever Psiphon is `chain` or `reverse`. Psiphon `reverse` always
gets `0`, because the core reports the port it ends up listening on and nothing
of the app dials it directly. Tor `reverse` gets a real port because the core
dials the address Tor was told to listen on.

`AetherArgs.socksPort` is normally port `10819`;
`AetherArgs.secondarySocksPort` is `socksPort + 3` (the two listeners after the
core's are Psiphon's and Tor's).

### Log level

- `buildArguments(..., logLevel)` defaults to `"info"`.
- `AetherCore.of(profile)` calls `withoutOption(buildArguments(...), "--log-level")`
  so the editor preview does **not** show a level. The session adds the app
  setting at start.
- `coreLogLevel(appLevel)`: `debug → debug`, `info → info`,
  `warning`/`warn → warn`, `error`/`none → error`, anything else `info`.
- `withLogLevel(args, level)` appends `--log-level level` unless the args
  already contain `--log-level` or `--verbose`.
- `withInfoLines(args)` is used by scans/key renewals. If `showsInfo(args)` is
  false, it removes the existing `--log-level` and appends `info`.
- `showsInfo(args)` is true with `--verbose`, or when the current level is
  `info`, `debug`, or `trace`, or when no level is named (the core default is
  treated as `info`).

### Argument introspection

PattNG reads live/custom commands back word by word. These helpers are in
`args/AetherArgs.kt` and may be needed by the NekoGo UI:

| Helper | Meaning |
|---|---|
| `tunnelArguments(args)` | args without `--log-level`, `--bind`, `--tor-bind`, `--psiphon-bind`, `--upstream`; used to tell one tunnel from another |
| `withoutOption(args, flag)` | args without every occurrence of `flag` and the value after it |
| `withListener(args, flag, port)` | args with that listener replaced by `127.0.0.1:port` |
| `listenerFlagOf(args)` | which listener the app dials: `--psiphon-bind`, else `--tor-bind`, else `--bind` |
| `torModeOf(args)` / `psiphonModeOf(args)` | last mode flag wins; absent means `off` |
| `pathOf(args, label)` | tunnel parts outside-in, e.g. `TOR → PSIPHON → WG_OVER_MASQUE → TOR`; a carrier alone is the whole path |
| `leavesThroughWarp(args)` | whether the last path part is WARP rather than Tor/Psiphon |
| `bindAddressOf` / `bindPortOf` | value and port after `--bind` |
| `listenerAddressOf` / `listenerPortOf` | address and port after the listener the app dials |
| `portAfter(args, flag)` | port part of the value after `flag` |
| `protocolOf(args)` | last of `--protocol` and the protocol/hop flags; no protocol at all reads as `masque` |
| `scanPort(profile)` | `0`, unless Tor `reverse` then a random free port |
| `masqueOverHttp2(...)` / `reachesWarpThroughCarrier(args)` | UI/session predicates |
| `listenPortOf(text)` / `listenPortProblem(text, localPorts)` | the Aether listen-port setting: 1024–65532, and checking the three ports after it |
| `secondarySocksPort` | `socksPort + 3` |

`protocolOf` needs care in Go: a hand-written command can select a protocol in
several ways. The last `--protocol` wins; `--gool-classic` or any `--wiw-*`
hop setting turns `wg-over-masque` back into the classic `gool`, wherever it
stands; a hop setting without `--protocol` selects `gool` before `mim`, and
nothing at all selects `masque`.

## `aether://` share links

`AetherFmt.parse` is the importer (`core/AetherFmt.kt`, original
`fmt/AetherFmt.kt:49`) and `AetherFmt.toUri` is the exporter (original line
104). `AngConfigManager` registers them for `EConfigType.AETHER.protocolScheme`
at `handler/AngConfigManager.kt:53` and dispatches exports at line 169.

### Shape

```
aether://<host>:<port>?protocol=…&scan=…&ip=…#<remarks>
aether://?protocol=…&outer=<host:port>&inner=<host:port>…#<remarks>
```

- The scheme comes from `EConfigType.AETHER`; **`toUri` returns everything
  after `aether://`** and the caller prepends the scheme.
- One-hop protocols (`wg`, `masque`) put the endpoint in the authority.
- Two-hop protocols (`gool`, `mim`, `wg-over-masque`) leave the authority empty
  and put the hops in `outer=` and `inner=`.
- The remarks are the URL fragment; `parse` uses `"Aether"` when it is empty.

Examples (`toUri` output plus the caller's scheme):

```
aether://162.159.198.1:443?protocol=masque&scan=verified&noize=aggressive&ip=both&transport=h2&fragment=1#My%20Node
aether://?protocol=gool&scan=balanced&ip=v4&outer=162.159.192.1:2408&inner=188.114.96.1:894#Two%20hops
aether://162.159.198.1:443?protocol=masque&scan=balanced&ip=v4&transport=h3&psiphon=chain&psiphon_mode=cdn&region=DE#Psiphon
```

### Parameters

`toUri` writes them in this order; `parse` accepts them in any order. A
parameter is only exported while its feature is in use (see the "Exported"
column for the exact rule).

| Key | Profile field | Parse default | Exported when |
|---|---|---|---|
| `protocol` | `aetherProtocol` | `wg` | always |
| `scan` | `aetherScanMode` | `balanced` | always |
| `noize` | `aetherObfuscation` | `auto` | non-`auto` and not MASQUE-over-HTTP/2 |
| `ip` | `aetherIpVersion` | `v4` | always |
| `dns` | `aetherDns` | absent | non-blank |
| `exit_loc` | `aetherExitLoc` | absent | non-blank |
| `transport` | `aetherTransport` | `h3` | only when `protocol.overMasque` |
| `sni` | `aetherMasqueSni` | absent | MASQUE protocol, non-default, valid server name |
| `fragment` | `aetherFragment` | `false` (`"1"` means true) | `true` |
| `fragment_size` | `aetherFragmentSize` | absent | `fragment=1` and the range parses |
| `fragment_delay` | `aetherFragmentDelay` | absent | `fragment=1` and the range parses |
| `ech` | `aetherEch` | `false` (`"1"` means true) | `true` with a WARP tunnel |
| `ech_dns` | `aetherEchDns` | absent | ECH on and the value is a valid resolver |
| `ech_domain` | `aetherEchDomain` | absent | ECH on and the value is a valid domain |
| `fingerprint` | `aetherFingerprint` | absent → stored null; effective `chrome` | non-`chrome` |
| `outer` | `aetherWiwOuter` | absent | two-hop protocol and the value parses |
| `inner` | `aetherWiwInner` | absent | two-hop protocol and the value parses |
| `psiphon` | `aetherPsiphon` | `off` (stored as null) | carrier active |
| `psiphon_mode` | `aetherPsiphonMode` | `auto` | carrier active |
| `cdn_ips` | `aetherPsiphonCdnIps` | absent | carrier active and non-blank |
| `cdn_sni` | `aetherPsiphonCdnSni` | absent | carrier active and non-blank |
| `cdn_sets` | `aetherPsiphonCdnSets` | absent (all) | carrier active and non-blank |
| `region` | `aetherPsiphonRegion` | absent | carrier active and non-blank |
| `psiphon_bundled` | `aetherPsiphonBundledList` | `true` (stored as null) | only `"0"` when `false` |
| `tor` | `aetherTor` | `off` (stored as null) | carrier active |
| `tor_bridges` | `aetherTorBridges` | `auto` | carrier active |
| `bridges` | `aetherTorBridgeLines` | absent | carrier active and lines exist; lines joined with `;` |
| `tor_relays` | `aetherTorRelays` | `auto` | carrier active |
| `fm` | `finalMask` | absent | non-blank |
| `dialMode` | `dialMode` | absent | non-blank |
| `#remarks` | `remarks` | `"Aether"` | always |

The "Parse default" column is the effective default after later
`fromString`/`normalize` lookups, not always a stored value. `parse` writes
`protocol`, `transport`, `scan`, `noize`, `ip`, `fragment`, and `ech`
unconditionally; values such as `fingerprint`, `psiphon_mode`, `tor_bridges`,
and `tor_relays` stay null when their parameter is absent.

Important asymmetries to preserve in Go:

- `parse` accepts `sni` and ECH values only when `protocol.overMasque` (for
  SNI) or `aetherEch == true` (for ECH values); invalid values are dropped and
  the profile follows the core default.
- `parse` normalizes `fragment_size` and `fragment_delay` through
  `AetherRange`, so an out-of-range value becomes absent.
- `parse` stores `aetherPsiphon`/`aetherTor` as null when the link says `off`,
  and `aetherPsiphonBundledList` only when `psiphon_bundled=0`.
- `toUri` includes `psiphon_mode`, `tor_bridges`, and `tor_relays` whenever the
  carrier is active, even when they hold the default `auto`.
- `toUri` includes `transport` for every MASQUE protocol, but no
  `transport` for `wg`.
- `aetherMasqueSni`, `aetherEchDns`, and `aetherEchDomain` can be stored while
  their feature is off; `toUri` exports them only while the feature is in use.
- `aetherExitNode`, `aetherListenPort`, `targetStrategy`, and `aetherCommand`
  are never carried by a link.
- URI parsing uses `fixIllegalUrl` (spaces → `%20`, `|` → `%7C`), keeps `+`
  literally in `decodeURIComponent`, and encodes with `%20` rather than `+`.
- The parser canonicalises the one-hop endpoint through `AetherEndpoint`, so
  e.g. IPv4 leading zeros and IPv6 spellings are normalized on import.

## Validation and normalization

`AetherFmt.normalize(config, takenPorts, listenPort)` returns the first
`Problem` or null, and modifies `config` in place. It runs in this order:

1. `normalizeMasqueSni` — trims; validates only while a MASQUE tunnel is in
   use (`overMasque` and neither carrier is `only`); stores null for empty or
   the default.
2. `normalizeFragment` — parses size/delay ranges; rejects a non-empty value
   that does not parse only while fragmenting is actually in use
   (`overMasque && transport == h2 && aetherFragment == true`).
3. `normalizeEndpoints` — for two-hop protocols validates `aetherWiwOuter` and
   `aetherWiwInner`, rejects two equal hosts for `gool`/`mim`, clears the
   unused fields on the other protocol branch.
4. `normalizeDns` — splits on comma/semicolon/whitespace, validates every
   resolver as `AetherEndpoint.parse(...) ?? AetherEndpoint.of(..., "53")`,
   and writes the list back comma-separated (or null).
5. `normalizeExitLoc` — strips whitespace, uppercases, validates
   `!?[A-Z]{2}(,[A-Z]{2})*`, writes back the rule or null.
6. `normalizeEch` — trims the resolver/domain; validates them only while ECH is
   in use (`aetherEch == true`, MASQUE protocol, neither carrier `only`);
   stores null for empty or the defaults.
7. `normalizePsiphon` — clears every Psiphon field when mode is `off`; rejects
   `reverse` with a non-MASQUE protocol (`PSIPHON_NEEDS_MASQUE`); normalizes
   lists/regions and stores `aetherPsiphonBundledList = false` only.
8. `normalizeTor` — clears every Tor field when mode is `off`; rejects
   `reverse` with a non-MASQUE protocol (`TOR_NEEDS_MASQUE`); only nested
   Tor/Psiphon combinations survive; `own` bridges require at least one line;
   normalizes bridge lines to one per line.
9. `normalizeListenPort` — skipped for a custom command. Rejects
   `LISTEN_PORT_TAKEN` when `listenPort` is in `takenPorts`, and
   `NEXT_PORT_TAKEN` when one of the extra carrier ports is taken:
   Psiphon `chain`, Tor `chain`, Tor `reverse` each take one port after the
   Aether listen port.
10. `normalizeCommand` — trims, stores null when empty, parses with
    `AetherCore.ofCommand`, rejects unreadable commands
    (`INVALID_COMMAND`) and any command listener port in `takenPorts`
    (`LISTEN_PORT_TAKEN`).

`Problem` values, in the same order as the enum in `core/AetherFmt.kt`:

`INVALID_MASQUE_SNI`, `INVALID_PEER`, `INVALID_HOP`, `SHARED_HOP`,
`INVALID_FRAGMENT`, `INVALID_DNS`, `INVALID_EXIT_LOC`, `INVALID_ECH_DNS`,
`INVALID_ECH_DOMAIN`, `LISTEN_PORT_TAKEN`, `PSIPHON_NEEDS_MASQUE`,
`NEXT_PORT_TAKEN`, `TOR_NEEDS_MASQUE`, `TOR_PSIPHON_CONFLICT`,
`TOR_BRIDGES_MISSING`, `INVALID_COMMAND`.

The editor adds checks that run outside `normalize` (see
`ui/AetherEditorLayout.md`): remarks required, `finalMask` blank or a JSON
object, exit-node profile lookup, and the WARP-keys-present check before save.

## Editor reference

`ui/AetherEditorLayout.md` is the field-by-field inventory: field order,
visibility conditions, label resource IDs, dropdown values, and the save
validation flow. `ui/AetherEditorFields.kt` is the same state as plain Kotlin:
defaults from `ServerUiState`, dropdown normalization on load, `toProfileItem`,
and `fromProfileItem`.

The most important editor rules for the Go port:

- `warpUsed = psiphon != only && tor != only`; when false, the whole WARP block
  (protocol, transport, MASQUE SNI, fragment, ECH, scan, endpoints, IP version,
  fingerprint, quick-reconnect) is hidden and never reaches the command.
- `overHttp2` and `usesHttp2` control fragment, obfuscation, and `--h2`
  visibility. Obfuscation is hidden for any MASQUE-over-HTTP/2 tunnel.
- The MASQUE SNI and ECH DNS/domain fields are shown filled with their defaults
  so the user sees what the core will be told; they are stored as null when
  they equal the default.
- The command preview is built with `AetherCore.of(profile, listenPort).command`
  and therefore has no `--log-level`. A different hand-written command is
  stored trimmed and shown with the "custom command" warning.
- A command equal to the settings-built command is not stored; the profile
  follows the settings.

## WARP keys page (`aether --register`)

`args/AetherKeys.kt` is the other argument constructor. Its run shape is:

```
aether --register <all|wg|masque|gool-classic|mim|gool>
       [--api-address <address>]
       [--fragment [--fragment-size N] [--fragment-delay N]]
       [--ech auto --ech-dns <resolver> --ech-domain <domain>]
       [--tls-ciphers <list>] [--disable-grease]
```

`AetherKeyKind.register` gives the `--register` word. `AetherKeys.problem`
validates the enroll address, fragment range, ECH resolver/domain, finalMask
JSON, and a custom command; `runArguments` prefers the custom command when one
is present. The WARP identity files themselves are managed by
`AetherIdentityManager`, which is not extracted (see below).

## Deliberately not extracted (runtime/Android)

These are Aether-related too, but they are lifecycle, process, network, or
Android-UI code rather than profile/link/argument logic. If NekoGo needs the
same behaviour, read the originals directly.

| Original file | Why it is not in `aether-reference/` |
|---|---|
| `core/AetherCoreManager.kt` | Process lifecycle: binary discovery, `ProcessBuilder`, environment variables, `/proc` owner/session reaping, readiness probes, log relay, Psiphon server-list unpacking, session tracking. Only the pure argument/port/introspection helpers are extracted. |
| `core/AetherScanner.kt` | Parses live core log lines into scan results and drives the "Scan for an endpoint" flow. Needs the process/scan session. |
| `core/AetherDelayTester.kt` | Latency tests for Aether profiles against a live core. |
| `core/AetherIdentityManager.kt` | WARP key/identity files, registration runs, and renewal ordering. `AetherKeys` builds the arguments; this manages the files. |
| `core/AetherDependency.kt` | Config-graph checks (two Aether hops, chained profiles). |
| `core/AetherCore.kt` (after line ~175) | `AetherExit`, Xray outbound construction, node lookup, digest keys. This is Xray integration. |
| `core/CoreOutboundBuilder.kt` Aether branches | Builds the ordinary Xray outbound for an Aether profile and its exit-node; includes `isNetipAddress` (copied into `AetherFmt.kt` because validation uses it). |
| `handler/AngConfigManager.kt` | Parser/exporter registration and subscription/config serialization hooks. Lines 53 and 169 are noted above. |
| `handler/SettingsManager.kt` | The Aether listen-port setting (`PREF_AETHER_LISTEN_PORT` → `AetherCoreManager.listenPortOf`). |
| `ui/server/AetherEditorRepository.kt`, `AetherKeysRepository.kt`, `AetherKeysViewModel.kt`, `ServerAetherViewModel.kt` | Android ViewModel/repository wiring; the pure state and validation are extracted instead. |
| `ui/server/ServerAetherActivity.kt`, `ServerAetherKeysActivity.kt` | Compose screens and string resources; the field layout is in `ui/AetherEditorLayout.md`. |
| `ui/main/MainServerRowModels.kt`, `MainViewModel.kt` | Row labels/paths using `AetherCore.path(...)`; the path helper is extracted. |
| `service/RealPingWorkerService.kt`, `service/CoreServiceManager.kt`, `core/LauncherManager.kt`, `core/CoreConfigManager.kt`, `core/CoreConfigContextBuilder.kt` | Service/chain integration that calls the Aether manager; not needed to generate arguments. |
| Aether-related `res/values/strings.xml` and translations, `res/values/arrays.xml` | Labels/options. The option values used by the layout doc; full translations belong to the Android app. |

Tests that document the expected behaviour and are useful to port as Go table
tests: `AetherFmtTest`, `AetherCoreManagerTest`, `AetherCoreTest`,
`AetherKeysTest`, `AetherEndpointTest`, `AetherRangeTest`, `AetherEchOptionsTest`,
`AetherScannerTest`, `ServerAetherViewModelTest`, `AetherEditorRepositoryTest`,
`AetherKeysViewModelTest`.

## Porting gotchas

- Keep the option order from `buildArguments`; the editor preview and the tests
  compare it.
- `--masque-sni` is never omitted for MASQUE protocols, even when it equals the
  default.
- Obfuscation is omitted for `auto` and for every MASQUE-over-HTTP/2 tunnel,
  including when `transport` is `h3` but Tor/Psiphon `reverse` forces HTTP/2.
- `--h2` and fragmentation appear only when the user explicitly chose `h2`.
- A scan drops the endpoint pin for one-hop and `gool`/`mim` protocols, keeps
  the `wg-over-masque` inner `--gool-peer`, drops inner carriers except
  `reverse`, and emits `--no-quick-reconnect`.
- Psiphon/Tor `only` suppresses the entire WARP block.
- `aetherPsiphonBundledList` is tri-state: `null` and `true` both mean "use the
  shipped list"; only `false` is persisted/exported.
- `aether://` export strips the scheme; the caller adds `aether://`.
- Two-hop authority stays empty; do not try to build `aether://host:port` for
  `gool`, `mim`, or `wg-over-masque`.
- IPv6 is bracketed on output and canonicalised on input by `AetherEndpoint`;
  `host:port` parsing uses the last colon, so `[::1]:443` rather than `::1:443`.
- `normalize()` mutates the profile and is the authority for what is stored;
  the editor's extra checks happen after it.
- `AetherCore.of` strips `--log-level` for the preview; the session appends the
  app setting with `withLogLevel`, and scan/key runs force an info-capable level
  with `withInfoLines`.
- The extracted Kotlin is not a standalone build. `args/AetherKeys.kt` still
  imports Gson's `JsonParser` (the original finalMask check); replace it with
  Go's `encoding/json` equivalent in NekoGo.
