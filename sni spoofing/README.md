<!-- ===== FILE: README.md ===== -->
# SNI Spoofer (Rust + Wails v3)

A fast, lightweight SNI-spoofing relay that bypasses DPI (Deep Packet
Inspection) by injecting a fake TLS ClientHello with a decoy SNI. This
is a full Rust rewrite of the original Python project by **Patterniha**,
with a modern React + Tailwind GUI running on Wails v3.

## About

The project relays TCP traffic to remote endpoints while spoofing the
SNI field in the TLS handshake.

Since v3 the engine does not pick a single "method" any more. It
composes two independent ideas per connection — which *decoy* ClientHello
is injected, and whether the client's *real* ClientHello is cut into
sub-label TCP segments — and lets a latency-aware scoreboard rank the
resulting 15 strategies and keep the best one. The GUI provides live
metrics, a console with filtering, and Smart Tools for probing endpoints
and SNIs.

Original project (Python): [patterniha/SNI-Spoofing](https://github.com/patterniha/SNI-Spoofing)

## Features

- 8 bypass primitives plus `auto`: `wrong_seq`, `wrong_seq_ttl`,
  `split_seq`, `fragmented`, `padding`, `hello_split`, `hostfakesplit`,
  `fakedsplit`
- `auto` = a 15-strategy pool (decoy shape x real-ClientHello splitting)
  ranked by recent success **and** measured connect latency; sticky once a
  strategy wins, rotated on 3 failures or when it turns out slow (>800 ms)
- TLS fingerprint profiles: `legacy`, `chrome`, `firefox` — three
  genuinely different hellos; the old versioned spellings still load
- QUIC handling: `block`, `spoof`, `passthrough`
- Live metrics: active / total / OK / fail, per-session traffic, per-method
  and per-endpoint latency
- Console with level filter, search, auto-scroll, export
- Smart Tools: rank endpoints, rank SNIs, pick fastest
- Config editor with atomic save
- Self-test suite (offline, no Admin required)
- Single-instance guard
- No coil whine, no Electron, ~12 MB binary

## Architecture

Data flow: React → Wails bindings → Go service → purego → Rust cdylib
→ WinDivert.

## Requirements

- Windows 10/11 (64-bit)
- [Rust](https://rustup.rs/) with the GNU toolchain
- [MinGW-w64](https://www.msys2.org/) (for linking)
- [Go 1.25+](https://go.dev/dl/)
- [Node.js 20+](https://nodejs.org/)
- [Wails v3 CLI](https://v3alpha.wails.io/): `go install github.com/wailsapp/wails/v3/cmd/wails3@latest`
- `WinDivert.dll` and `WinDivert64.sys` (see below)

## Building

```cmd
:: Rust engine
cd engine-ffi
cargo build --release

:: Frontend
cd ..\gui\frontend
npm install
npm run build

:: Wails app
cd ..
wails3 generate bindings -ts
go build -ldflags="-H windowsgui -s -w" -o build\bin\sni-gui.exe .

:: Copy the engine DLL next to the exe
copy ..\target\release\sni_engine.dll build\bin\