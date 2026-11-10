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
- Dead code removal, including a second Xray traffic total that counted
  loopback API polling as transferred bytes.

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
