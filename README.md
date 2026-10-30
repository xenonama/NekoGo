# NekoGo

A fork of NekoBox with a completely new GUI (Go + Wails3 + Vue 3).

## Tech stack

- Go backend with a JSON config store (no database).
- Wails3 wrapper exposing a bound `NekoGo` service to the frontend.
- Vue 3 + TypeScript frontend with Pinia stores and Vite builds.
- Cores: sing-box (Thrift RPC via `nekobox_core`) + Xray (gRPC stats).

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

## Build

Run `build.bat` on Windows 10/11 x64.

Requirements: Go 1.25+, Node.js 20+, Wails3, MinGW, NSIS optional.

Output: `gui/build/bin/nekogo.exe` + `nekobox_core.exe` + `xray.exe`; `dist/*.zip` and `dist/*.exe`.

## Credits

- [starifly/NekoBoxForAndroid](https://github.com/starifly/NekoBoxForAndroid)
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
