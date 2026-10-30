# NekoGo

A fork of NekoBox with a completely new GUI (Go + Wails3 + Vue 3), built for Windows with advanced censorship circumvention in mind.

## Tech stack

- **Backend:** Go with a JSON config store (no database), using Wails3 to expose a bound `NekoGo` service to the frontend.
- **Frontend:** Vue 3 + TypeScript with Pinia stores and Vite builds.
- **Engines (Cores):**
  - **sing-box** (via `nekobox_core.exe` with Thrift RPC) — the default, custom-patched engine with extra protocols.
  - **Upstream sing-box** (optional CLI) — uses the official SagerNet binary via Clash API.
  - **Xray-core** (`xray.exe`) — used for specific protocols (e.g., REALITY).
  - **Aether** (`aether.exe`) — Rust-based WARP engine (MASQUE, WireGuard, Tor, Psiphon).
  - **SNI Spoofing** (`sni-spoof.exe`) — Rust-based DPI bypass engine.

## Supported protocols

- AmneziaWG
- AnyTLS
- Hysteria 1 / Hysteria 2
- Juicity
- Mieru
- Naive
- Reality (VLESS + REALITY)
- Shadowsocks
- ShadowTLS
- Snell
- SOCKS
- SSH
- Tailscale
- Tor
- Trojan
- TrustTunnel
- TUIC
- VLESS
- VMess
- WireGuard
- **Aether** (MASQUE, WireGuard, gool, mim)
- **SNI Spoofing** (Trojan / VLESS via DPI bypass)

## Key features

- **Custom DPI bypass:** First-class SNI Spoofing engine with built-in scanner, IP/SNI lists, and front-layer integration.
- **Aether integration:** Full Aether core support with scanning, SOCKS5 proxy, and Tor/Psiphon chaining.
- **Upstream core option:** Switch between the custom `nekobox_core` (full features) and the official sing-box CLI (via Clash API) in Advanced Settings.
- **TUN mode:** Full system-wide tunneling (requires Administrator privileges).
- **Modern UI:** Simple and Advanced modes, with a fully custom title bar.
- **GeoIP:** Country detection using the GeoLite2-Country database.
- **Subscription management:** Feed parsing, deduplication, per-feed enable/disable, and quota tracking.

## Build

Run `build.bat` on Windows 10/11 x64.

**Requirements:**
- Go 1.25+
- Node.js 20+
- Wails3
- Rust (for the SNI Spoofing engine)
- MinGW (for cgo)
- NSIS (optional, for the installer)

**Output:**
- `gui/build/bin/nekogo.exe`
- `gui/build/bin/nekobox_core.exe`
- `gui/build/bin/xray.exe`
- `gui/build/bin/sni-spoof.exe`
- `gui/build/bin/aether.exe` (optional)
- `dist/*.zip` (portable)
- `dist/*.exe` (installer)

## Credits

- [patterniha/PattNG](https://github.com/patterniha/PattNG) — Android Kotlin reference for Aether and advanced patterns.
- [patterniha/PattN](https://github.com/patterniha/PattN) — Windows C# reference for backend patterns.
- [CluvexStudio/Aether](https://github.com/CluvexStudio/Aether) — Rust WARP engine.
- [starifly/NekoBoxForAndroid](https://github.com/starifly/NekoBoxForAndroid)
- [Thrift](https://thrift.apache.org/)
- [enfein/mieru](https://github.com/enfein/mieru)
- [SagerNet/sing-box](https://github.com/SagerNet/sing-box)
- [XTLS/Xray-core](https://github.com/XTLS/Xray-core)
- [go](https://go.dev/)
- [Wails](https://wails.io/)
- [Vue](https://vuejs.org/)
- [protorpc](https://github.com/chai2010/protorpc)
- [simple-protobuf](https://github.com/tonda-kriz/simple-protobuf)
- [quickjs-ng](https://github.com/quickjs-ng/quickjs)
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