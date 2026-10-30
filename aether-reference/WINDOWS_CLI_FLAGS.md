# Aether 2.3.0 Windows CLI — flag table and PART 0 diff

Source of truth: `aether-reference/bin/aether.exe` (official `CluvexStudio/Aether`
v2.3.0 Windows x86_64 release, archive SHA256
`0cc32dd2d7573f91d2e55d4f427bfe4c6f6252342494e1b410d61b12d1e4dc79`).
Raw output: `aether-reference/WINDOWS_CLI.txt`.

Comparison targets:

- `aether-reference/args/AetherArgs.kt` (PattNG `buildArguments`)
- `aether-reference/args/AetherKeys.kt` (PattNG `--register` builder)
- `C:\Users\pc\Desktop\NekoGo\gui\aether_core.go` (`buildAetherArguments`, the
  current NekoGo port). The prompt names this file as part of NekoGo, but the
  file does not exist under `C:\Users\pc\Desktop\nekobox-main`; it exists only
  in `C:\Users\pc\Desktop\NekoGo`.

## 1. CLI flags (from `aether.exe --help`, v2.3.0)

| Flag | Value type | Default | Allowed values | Description |
| --- | --- | --- | --- | --- |
| `--bind` | addr | `127.0.0.1:1819` | `host:port` | local SOCKS5 listen address |
| `--http-proxy` | addr | off | `host:port` | also expose an HTTP CONNECT proxy |
| `--upstream` | URL | off | `socks5://…`, `http://user:pass@host:8080` | dial out through an existing proxy |
| `--mark` | int/hex | none | e.g. `0xff` | SO_MARK on internet sockets (Linux/Android, root) |
| `--exit-loc` | spec | off | `!CC,CC`, `CC,CC` (e.g. `!IR,AZ,RU`, `DE,SE`) | refuse/allow exit countries |
| `--exit-loc-secs` | int (s) | 60 | positive integer | exit-country recheck interval |
| `--stats` | bool flag | off | — | log traffic and uptime |
| `--stats-secs` | int (s) | 60 | positive integer | stats line interval |
| `--quick-reconnect` | bool flag | not stated by help | — | auto-accept the last known working gateway |
| `--no-quick-reconnect` | bool flag | not stated by help | — | always scan fresh |
| `-4` | bool flag | on | — | IPv4 only |
| `-6` | bool flag | off | — | IPv6 only |
| `--dual` | bool flag | off | — | both IPv4 and IPv6 |
| `--ip` | enum | `v4` | `v4`, `v6`, `both` | written form of `-4`/`-6`/`--dual` |
| `--peer` | endpoint | none | `ip:port` | force a MASQUE/WireGuard peer, skip scan |
| `--wg-peer` | endpoint | none | `ip:port` | force a WireGuard peer (warp-in-warp outer) |
| `--masque` | bool flag | on (default protocol) | — | MASQUE over QUIC/HTTP-3 |
| `--wg`, `--wireguard`, `--warp` | bool aliases | off | — | classic WireGuard |
| `--gool`, `--wiw` | bool aliases | off | — | gool (WireGuard inside MASQUE) |
| `--gool-peer` | endpoint | registration-named, port 2408 | `ip:port` | WireGuard endpoint gool dials inside the tunnel |
| `--gool-classic` | bool flag | off | — | older gool: WireGuard in WireGuard |
| `--api-fragment` | bool flag | off | — | reach the WARP API only over the fragmented route |
| `--mim`, `--masque-in-masque` | bool aliases | off | — | MASQUE-in-MASQUE |
| `--protocol` | enum | `masque` | `masque`, `wg`, `gool`, `mim` | written protocol selector |
| `--wiw-outer` | endpoint | scan finds it | `ip:port` | classic gool outer hop |
| `--wiw-inner` | endpoint | scan finds it | `ip:port` | classic gool inner hop |
| `--wiw-peers` | endpoint list | scan finds them | `out[,in]` | classic gool hops in one value |
| `--wiw-scan` | bool flag | off | — | scan both classic gool hops |
| `--mim-outer` | endpoint | scan finds it | `ip:port` | MIM outer hop |
| `--mim-inner` | endpoint | picked for you | `ip:port` | MIM inner hop |
| `--mim-peers` | endpoint list | scan finds them | `out[,in]` | MIM hops in one value |
| `--mim-scan` | bool flag | off | — | scan the MIM outer hop |
| `--scan` | enum | `balanced` | `turbo`, `balanced`, `thorough`, `verified`, `ironclad` | scan mode |
| `--turbo` | bool flag | off | — | stop at the first candidate that answers |
| `--balanced` | bool flag | on (default mode) | — | collect a few, keep the fastest |
| `--thorough` | bool flag | off | — | sweep whole ranges |
| `--verified` | bool flag | off | — | only edges measured to answer connect-ip |
| `--ironclad` | bool flag | off | — | real tunnel + real HTTP request per candidate |
| `--noize` | enum | `firewall` (MASQUE), `balanced` (WG/gool) | `off`, `light`, `firewall`, `balanced`, `gfw`, `aggressive` | obfuscation profile |
| `--h2`, `--http2` | bool aliases | off | — | HTTP/2 (TCP) instead of HTTP/3 |
| `--h3`, `--quic` | bool aliases | on | — | HTTP/3 (QUIC) |
| `--no-quic-v2` | bool flag | off | — | drop the QUIC v2 version-negotiation opener |
| `--h2-peer` | endpoint | none | `ip:port` | HTTP/2 transport peer override |
| `--no-data-check` | bool flag | off | — | skip end-to-end data-plane validation |
| `--validate-secs` | int (s) | 10 | positive integer | data-plane validation wait |
| `--startup-secs` | int (s) | 30 | positive integer | MASQUE startup deadline |
| `--reconnect-secs` | int (s) | 2 | positive integer | reconnect delay |
| `--dns` | list | `1.1.1.1,1.0.0.1` | comma/space separated resolvers | in-tunnel resolvers |
| `--fragment` | bool flag | on | — | fragment the HTTP/2 ClientHello |
| `--no-fragment` | bool flag | off | — | send the HTTP/2 ClientHello whole |
| `--fragment-size` | range | `16-32` | `n` or `a-b` (bytes) | fragment chunk size |
| `--fragment-delay` | range | `2-10` | `n` or `a-b` (ms) | delay between fragments |
| `--ech` | enum/base64 | off | `auto`, base64 key | Encrypted Client Hello |
| `--ech-dns` | URL | `udp://1.1.1.1` | `udp://ip[:port]`, `tcp://ip[:port]`, `https://…` (plus `@address=`, `@sni=`) | ECH key resolver |
| `--ech-domain` | domain | `cloudflare-ech.com` | domain name | domain whose HTTPS record holds the ECH key |
| `--tls-ciphers` | list | `ALL:!aPSK:!ECDSA+SHA1:!3DES` | colon-separated cipher names | TLS 1.2 cipher suites |
| `--tls-groups` | list | `P-256:X25519:P-384` | colon-separated group names | TLS groups in order |
| `--disable-grease` | bool flag | off | — | omit GREASE values |
| `--keepalive` | int (s) | 5 | positive integer | WireGuard persistent keepalive |
| `--no-profile-retry` | bool flag | off | — | do not retry other obfuscation profiles during scan |
| `--tor` | bool flag | off | — | carry Tor inside the tunnel |
| `--tor-reverse` | bool flag | off | — | dial the tunnel through Tor |
| `--tor-only` | bool flag | off | — | plain Tor, no WARP |
| `--tor-bind` | addr | `127.0.0.1:1820` | `host:port` | Tor proxy listener |
| `--tor-http` | addr | off | `host:port` | also serve Tor as HTTP CONNECT |
| `--tor-bridge-file` | path | none | file path | read Tor bridge lines from a file |
| `--tor-relays` | string | `auto` | `auto`, `only`, `off`, count (e.g. `80`) | onionoo relays as bridges |
| `--tor-relay-ports` | enum | `web` | `web`, `any` | relay ports to consider |
| `--tor-dir` | path | `<config>-tor` | directory | Tor data directory |
| `--tor-bridges` | bool flag | off | — | go to bridges at once |
| `--no-tor-bridges` | bool flag | off | — | never use bridges |
| `--tor-bridge` | repeatable string | none | torrc-style bridge line | own bridge line |
| `--tor-pt` | repeatable path | none | `[name=]<path>` | pluggable transport binary |
| `--tor-pt-dir` | path | none | directory | extra PT search directory |
| `--psiphon` | bool flag | off | — | carry Psiphon inside the tunnel |
| `--psiphon-reverse` | bool flag | off | — | dial the tunnel through Psiphon |
| `--psiphon-only` | bool flag | off | — | plain Psiphon, no WARP |
| `--psiphon-mode` | enum | `auto` | `auto`, `cdn`, `direct` | Psiphon shape |
| `--psiphon-config` | path | built-in | JSON file | own Psiphon config |
| `--psiphon-cdn-ips` | list | built-in list | comma/space separated addresses | CDN fronting edges |
| `--psiphon-cdn-sni` | list | built-in list | comma/space separated names | server names for those edges |
| `--psiphon-cdn-sets` | list | all built-ins | `cloudflare`, `fastly`, `cloudfront`, `psiphon-akamai`, `psiphon-bunny`, `vercel`, `github`, `curated-fronting`, `legacy-android-overrides` | built-in edge lists to try |
| `--psiphon-bind` | addr | `127.0.0.1:1821` | `host:port` | Psiphon proxy listener |
| `--psiphon-http` | addr | off | `host:port` | also serve Psiphon as HTTP CONNECT |
| `--psiphon-region` | country code | none | e.g. `DE` | requested Psiphon exit region |
| `--psiphon-dir` | path | `<config>-psiphon` | directory | Psiphon data directory |
| `--psiphon-bin` | path | next to aether, `./pt`, PATH | executable path | psiphon-tunnel-core binary |
| `--psiphon-server-entries` | path | none | file path | preloaded Psiphon server entries |
| `--team` | name | none | team name | Zero Trust organization |
| `--access-id` | id | none | service token client id | headless enrolment |
| `--access-secret` | secret | none | service token client secret | headless enrolment |
| `--access-email` | email | none | email address | one-time-code sign-in |
| `--access-token` | JWT | none | enrolment token | pre-obtained token |
| `--gateway` | bool flag | off | — | route HTTP/HTTPS via the Zero Trust gateway |
| `--route-block` | list | none | routing rules | never let these reach the network |
| `--route-direct` | list | none | routing rules | bypass the tunnel |
| `--routes` | path | none | `[block]`/`[direct]` file | load both route lists |
| `--config` | path | `aether.toml` | file path | base identity config |
| `--wg-config` | path | derived | file path | WireGuard identity config |
| `--masque-config` | path | derived | file path | MASQUE identity config |
| `--register` | enum | none | `masque`, `wg`, `gool`, `mim`, `all` (help text); `wiw`, `warp-in-warp`, `masque-in-masque` accepted at runtime; `gool-classic` rejected | register identities and exit |
| `--enroll-address` | host[:port] | `api.cloudflareclient.com` | IP, IPv6 in brackets, or domain | WARP API address |
| `--perf` | enum | `auto` | `low`, `medium`, `high` | forced resource profile |
| `--log-level` | enum | `info` | `error`, `warn`, `info`, `debug`, `trace` | log level |
| `--verbose` | bool flag | off | — | shortcut for `--log-level debug` |
| `-v`, `--version` | bool flag | — | — | print version and exit |
| `-h`, `--help`, `help` | bool flag | — | — | print help and exit |

### Environment-only knobs (no CLI flag in v2.3.0)

`AETHER_ROUTE_SNIFF`, `AETHER_ROUTE_SNIFF_MS`, `AETHER_WG_ENDPOINT_COOLDOWN_SECS`,
`AETHER_WG_STALE_SECS`, `AETHER_MASQUE_H2_KEEPALIVE_SECS`,
`AETHER_MASQUE_H2_KEEPALIVE_TIMEOUT_SECS`, `AETHER_IRONCLAD_PORT`,
`AETHER_MAX_CLIENTS`, `AETHER_HALF_CLOSE_SECS`, `AETHER_TCP_KEEPALIVE_SECS`,
`AETHER_TCP_CONNECT_SECS`, `AETHER_REPROVISION`, `AETHER_PSIPHON_READY_SECS`,
`AETHER_TOR_DIRECT_SECS`, `AETHER_TOR_STALL_SECS`, `AETHER_TOR_BRIDGE_SECS`,
`AETHER_TOR_COUNTRY`, `AETHER_TOR_CHECK`, `AETHER_TOR_LOG`, `RUST_LOG`.
Every other flag above also has its own `AETHER_*` environment variable.

## 2. PART 0 four-way diff

### (i) Accepted by `aether.exe` 2.3.0 but NOT emitted by PattNG or NekoGo

Connection / process: `--http-proxy`, `--upstream`, `--mark`,
`--exit-loc-secs`, `--stats`, `--stats-secs`, `-6`, `--dual`, `--wg-peer`.

Protocol / transport: the boolean protocol selectors `--masque`, `--wg`,
`--wireguard`, `--warp`, `--gool`, `--wiw`, `--mim`, `--masque-in-masque`
(both apps use `--protocol` instead), `--api-fragment`, `--h3`, `--quic`,
`--no-quic-v2`, `--h2-peer`, `--no-data-check`, `--validate-secs`,
`--startup-secs`, `--reconnect-secs`, `--no-fragment`.

Scan shortcuts: `--turbo`, `--balanced`, `--thorough`, `--verified`,
`--ironclad` (both apps use `--scan <mode>`).

TLS / WireGuard: `--tls-groups`, `--keepalive`, `--no-profile-retry`.

Tor: `--tor-http`, `--tor-bridge-file`, `--tor-relay-ports`, `--tor-dir`,
`--tor-pt`, `--tor-pt-dir`.

Psiphon: `--psiphon-config`, `--psiphon-http`, `--psiphon-dir`,
`--psiphon-bin`.

Zero Trust: `--team`, `--access-id`, `--access-secret`, `--access-email`,
`--access-token`, `--gateway`.

Routing / files: `--route-block`, `--route-direct`, `--routes`, `--config`,
`--wg-config`, `--masque-config`, `--enroll-address` (PattNG's keys page uses
the removed `--api-address` instead).

Advanced: `--perf`, `--verbose`.

Accepted but undocumented aliases: `--gool-inner`, `--gool-outer`,
`--gool-peers`, `--gool-scan`, `--inner-peer`, `--outer-peer` (clap accepts
them, `--help` does not list them).

### (ii) Emitted by PattNG/NekoGo but NOT accepted by `aether.exe` 2.3.0

1. `--masque-sni` — emitted by PattNG `buildArguments`
   (`AetherArgs.kt:240`) and by NekoGo `buildAetherArguments`
   (`aether_core.go:116`). Verified:
   `unknown option '--masque-sni'`.
2. `--api-address` — emitted by PattNG `AetherKeys.arguments`
   (`AetherKeys.kt:94`). Verified: `unknown option '--api-address'`. The
   2.3.0 flag is `--enroll-address`.
3. `--register gool-classic` — emitted by PattNG `AetherKeyKind.GOOL.register`
   (`AetherOption.kt:218`). Verified:
   `--register takes masque, wg, gool, mim or all, not 'gool-classic'`.
   `--register gool` is the 2.3.0 word for WireGuard-over-MASQUE;
   `--register wiw` / `warp-in-warp` / `masque-in-masque` are accepted aliases.

No other flag emitted by the two builders was rejected; `--gool-inner`,
`--inner-peer`, `--wiw`, `--gool-classic` and `--register wiw` were probed with
`--version` and survive clap parsing.

### (iii) Type or value-shape differences

1. `--psiphon-server-entries`: Windows expects `<path>`; PattNG substitutes a
   real extracted list file at launch; NekoGo passes the literal sentinel
   `shipped-list` (`aether_core.go:260`) with no substitution anywhere in the
   NekoGo tree.
2. `--fragment` default: ON in Windows 2.3.0; PattNG/NekoGo only pass
   `--fragment` when true and pass nothing when false, so a profile with
   fragmentation off still gets fragmentation.
3. `--register`: PattNG's `gool-classic` value vs Windows' `gool` (finding ii.3).
4. `--api-address` vs `--enroll-address` naming (finding ii.2).
5. `--masque-sni` removed (finding ii.1); there is no 2.3.0 replacement flag in
   `--help` and no `AETHER_MASQUE_SNI` environment variable.
6. Value shapes that do match: `--exit-loc` (uppercase comma spec), `--dns`
   (comma list), `--fragment-size`/`--fragment-delay` (`n` / `a-b`),
   `--ech` (`auto`), `--psiphon-cdn-sets` (comma list), `--tor-relays`
   (`auto`/`only`/`off`), `--ip` (`v4`/`v6`/`both`), `--scan` enum,
   `--noize` enum, boolean flags.

### (iv) Default differences (Windows wins)

1. `--fragment`: Windows ON, PattNG/NekoGo default false (finding iii.2).
2. `--masque-sni`: PattNG has a default WARP SNI in `AetherDefaults`; Windows
   2.3.0 has no such flag at all (finding ii.1).
3. `--noize`: Windows `firewall` for MASQUE and `balanced` for WireGuard/gool;
   PattNG/NekoGo pass `auto` and let the core choose. This matches the Windows
   defaults for the current core, so no argument change is required.
4. `--scan` = `balanced`, `--ip` = `v4`, `--dns` = `1.1.1.1,1.0.0.1`,
   `--ech-dns` = `udp://1.1.1.1`, `--ech-domain` = `cloudflare-ech.com`,
   `--tls-ciphers` = Chrome rule, `--tls-groups` = `P-256:X25519:P-384`,
   `--fragment-size` = `16-32`, `--fragment-delay` = `2-10`,
   `--keepalive` = 5, `--validate-secs` = 10, `--startup-secs` = 30,
   `--reconnect-secs` = 2, `--psiphon-mode` = `auto`, `--tor-relays` = `auto`,
   `--tor-relay-ports` = `web`, `--log-level` = `info`: all match what
   PattNG/NekoGo rely on (either emitted explicitly or left to the core).
5. Listener defaults (`--bind`, `--tor-bind`, `--psiphon-bind`) are overridden
   by explicit app-allocated ports in both apps; no mismatch in practice.
