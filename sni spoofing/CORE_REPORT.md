# sni-spoof Core Report (input for NekoGo integration chat)

## 1. Exact exe path

- Build: `cargo build --release` in workspace root → `target\release\sni-spoof.exe`
- Verified: `C:\Users\pc\Desktop\sni spoofing core\target\release\sni-spoof.exe` (5,628,347 bytes, 2026-10-15 post-audit build)
- Binary name comes from `cli/Cargo.toml`: `[[bin]] name = "sni-spoof"`.

## 2. Exact CLI surface

Only these three forms exist. Anything else prints usage to **stderr** and exits **2**.

```
sni-spoof.exe version
sni-spoof.exe run -c <config.json>
sni-spoof.exe help
```

| Command | stdout | Exit |
|---|---|---|
| `version` | `sni-spoof 2.0.0` | 0 |
| `help` | `usage:` + the 3 lines above | 0 |
| `run -c <file>` | engine logs (see §4), blocks until signal | 0 on clean signal shutdown; 1 on config/driver/bind failure |
| unknown / missing `-c` value | (stderr) usage | 2 |

### C ABI inventory (`sni_engine` cdylib, `engine-ffi/src/lib.rs`)

All `sni_*` exports, exact signatures. `*mut c_char` returns are NUL-terminated UTF-8 JSON allocated with `CString::into_raw` — the caller MUST free each with `sni_free_string` (documented in `lib.rs` header). `c_uchar` returns are 1/0 ints. Nothing else is exported.

| Symbol | Signature | Return encoding |
|---|---|---|
| `sni_start_engine` | `(config_json: *const c_char) -> *mut c_char` | JSON `{"ok":bool,…}` |
| `sni_stop_engine` | `() -> *mut c_char` | JSON |
| `sni_reset_stats` | `() -> *mut c_char` | JSON |
| `sni_get_stats` | `() -> *mut c_char` | JSON (`Snapshot`) |
| `sni_get_logs` | `() -> *mut c_char` | JSON array of strings |
| `sni_clear_logs` | `() -> *mut c_char` | JSON |
| `sni_export_logs` | `() -> *mut c_char` | JSON `{"ok":true,"path":…}` |
| `sni_run_probe_endpoints` | `(endpoints_json: *const c_char) -> *mut c_char` | JSON `{"ok":true,"count":n}` |
| `sni_run_probe_snis` | `(snis_json: *const c_char, endpoint: *const c_char) -> *mut c_char` | JSON `{"ok":true,"count":n}` |
| `sni_get_probe_results` | `() -> *mut c_char` | JSON array |
| `sni_get_sni_results` | `() -> *mut c_char` | JSON array |
| `sni_is_admin` | `() -> c_uchar` | int 1/0 |
| `sni_is_running` | `() -> c_uchar` | int 1/0 |
| `sni_free_string` | `(s: *mut c_char)` | void (null-safe) |
| `sni_self_test` | `(config_path: *const c_char) -> *mut c_char` | JSON report (`{"checks":{…},"ok":bool}`) |
| `sni_save_config` | `(config_json: *const c_char) -> *mut c_char` | JSON `{"ok":true,"path":…}` |
| `sni_load_config` | `() -> *mut c_char` | JSON config + `"ok":true` |
| `sni_config_path` | `() -> *mut c_char` | JSON `{"ok":true,"path":…}` |
| `sni_set_config_path` | `(config_path: *const c_char)` | void (null/empty resets) |
| `sni_get_default_config` | `() -> *mut c_char` | JSON config + `"ok":true` |
| `sni_cancel_probe` | `()` | void |
| `sni_get_active_connections` | `() -> *mut c_char` | JSON array (empty when stopped, never errors) |
| `sni_snapshot_all` | `(log_since: u32) -> *mut c_char` | JSON `{"ok":true,"stats":{…},"logs":[…],"log_offset":n,"active":[…]}` |

No symbols added or removed in any pass (the `pub mod worker` change is Rust-rlib visibility for the loopback harness, not a C export).

Observed outputs (byte-exact from smoke test):

- `version` → `sni-spoof 2.0.0`
- `help` →
  ```
  usage:
    sni-spoof.exe version
    sni-spoof.exe run -c <config.json>
    sni-spoof.exe help
  ```
- `run -c <missing>` → `[ERROR] cannot load config <path>: cannot read config <path>: ...` exit 1
- `run -c <valid>` without DLL next to exe → `[ERROR] start failed: WinDivert open failed: ... LoadLibrary WinDivert.dll failed ...` exit 1
- `run -c <valid>` with DLLs next to exe, non-Admin machine → `[ERROR] start failed: WinDivert open failed: ... Access denied (code 5) ...` exit 1

## 3. Exact JSON config schema (`sni_core::config`, `core/src/config.rs`)

Loaded by `config::load(path)`: size-capped (256 KiB), root must be object; `<path>.full.json` fills **missing** keys only (`config.json` wins); legacy keys migrate; then strict `validate()`.

| Key | Type | Default | Valid |
|---|---|---|---|
| `LISTEN_HOST` | string | `"127.0.0.1"` | `"0.0.0.0"` or valid IPv4 |
| `LISTEN_PORT` | u16 (number or numeric string) | `40443` | 1–65535 |
| `ENDPOINTS` | array of `{ip,port}` or `"ip:port"` strings | `[]` → `CONNECT_IP`/`CONNECT_PORT` fallback | 1–64 entries; IPv4 + port ≠ 0 (**required non-empty**) |
| `CONNECT_IP` / `CONNECT_PORT` | string / u16 (legacy) | none / `443` | IPv4 / 1–65535 |
| `RESOLVE_SNI` (accepts `resolve_sni`) | bool (tolerant: same accepted set as `TRAY_ENABLED`) | `false` | Plan A (§17): resolve `FAKE_SNIS` through DoH and use those IPs as the endpoints for this run |
| `CONNECT_OVERRIDE` (accepts `connect_override`) | string | `""` (unset) | IPv4 literal only; wins over `ENDPOINTS` and over DoH (§17) |
| `FAKE_SNIS` | array of strings | `[]` → `FAKE_SNI` fallback; lowercased, dot-stripped, deduped | 1–200 entries; each valid hostname, ≤219 chars (**required non-empty**) |
| `FAKE_SNI` | string (legacy) | none | same SNI rules |
| `BYPASS_METHOD` | string | `"auto"` | v3 (§19): `auto, wrong_seq, wrong_seq_ttl, split_seq, fragmented, padding, hello_split, hostfakesplit, fakedsplit`. `double_sni` and `delayed_retry` were deleted; saved configs using them migrate to `auto` with a `[WARN]` |
| `HANDSHAKE_TIMEOUT` | f64 | `2.0` | 0.5–10.0 s |
| `MAX_CONNECTIONS` | usize | `200` | 10–2000 |
| `FAKE_DELAY` | f64 | `0.001` | 0.0–5.0 s |
| `SEQ_OVERLAP` | u8 | `3` | 0–16 |
| `TLS_FINGERPRINT` | string (lowercased) | `"legacy"` | v3 (§19): `legacy, chrome, firefox`. Retired spellings still load and migrate — `chrome_120`/`chrome_124`→`chrome`, `firefox_122`/`firefox_124`→`firefox`, `custom`→`legacy` |
| `PADDING_SIZE` | u8 | `0` | 0–128 |
| `QUIC_MODE` | string (lowercased) | `"block"` | `block, spoof, passthrough` |
| `MODE` | string | `"SNI Only"` | `"SNI Only"` only (`"Trojan + Xray"` removed 2026-10-15: fails validation, see §10) |
| `IP_MODE` (accepts `ip_mode`) | string (lowercased) | `"ipv4"` | `ipv4, ipv6, both` |
| `TRAY_ENABLED` (accepts `tray_enabled`) | bool (tolerant: bool, `true/1/yes/y/on` strings, numbers) | `true` | bool |
| `PROBE_TRIES` | u32 | `2` | 1–10 |
| `PROBE_TIMEOUT` | f64 | `3.0` | 0.5–10.0 s |
| `SOCKS5_PORT` | u16 | `10808` | ≠ 0, ≠ `LISTEN_PORT`, ≠ `HTTP_PORT` |
| `HTTP_PORT` | u16 | `10809` | ≠ 0, ≠ `LISTEN_PORT`, ≠ `SOCKS5_PORT` |

Keys through §16 were unchanged; the Plan A pass (2026-11-01) added exactly two — `RESOLVE_SNI` and `CONNECT_OVERRIDE`, documented in §17. The v3 pass (2026-11-04) added **no** keys and removed none; it changed the accepted *values* of `BYPASS_METHOD` and `TLS_FINGERPRINT` (see §19) plus the shape of `Snapshot.methods`.

## 4. Exact stdout line format

Plain text, one line per event, flushed after every line. Prefixes the host greps:

- `[INFO] ...` — normal lifecycle (example: `[INFO] engine RUNNING`; engine also logs `[INFO] listening on 127.0.0.1:40443 (method=..., N endpoint(s), M fake SNI(s))`)
- `[WARN] ...` — shutdown path (examples: `[WARN] shutdown signal received`, `[WARN] engine stopped`; engine warnings such as `all endpoints failed` also surface with this prefix)
- `[ERROR] ...` — failures (examples in §2; note the FFI layer's `[ERR ]` tag is normalized to `[ERROR]` here)
- `[STATS] ...` — stats line (see §5)
- Engine `debug!/trace!` events are filtered out (`EnvFilter("info")`), so stdout stays grep-friendly.

## 5. Exact stats mechanism

**Stdout line, no file, no RPC.** Every 5 s the CLI prints:

```
[STATS] {"type":"stats","active":0,"total":0,"success":0,"failed":0,"uptime":5.0,"success_rate":0.0,"best_endpoint":"","best_method":"","methods":[],"up_bytes":0,"down_bytes":0}
```

This is `sni_core::stats::Snapshot` (`core/src/stats.rs`) serialized with `serde_json::to_string`, produced by the existing shared `Arc<Stats>` passed to `EngineHandle::start` — the same struct the FFI layer polls. `methods` is the top-3 ranked rows, each `{key, ok, fail, rate, latency_ms, score}` since §19 (`latency_ms` is `null` until something has been timed); `best_endpoint`/`best_method` are the top-ranked keys (empty when no data). Since §19 `best_method` is a *strategy* key — a decoy name, or `<decoy>+split` when the real ClientHello is segmented too — not the configured `BYPASS_METHOD` string, and ranking uses `score`, not `rate`.

## 6. Exact shutdown behaviour (std implementation, no async runtime)

- **SIGINT**: unix via `signal-hook`, Windows via `SetConsoleCtrlHandler` (`CTRL_C_EVENT`).
- **SIGTERM and console events**: unix `SIGTERM`; Windows `ctrl_close` + `ctrl_shutdown` + `ctrl_break` in the same console handler. All four set one process-wide `AtomicBool` stop flag; `run()` polls it every 100 ms.
- If no handler could be armed at all, the CLI refuses to run: `[ERROR] no shutdown signal handler armed; refusing to run`, exit 1 (a missed signal would otherwise park forever).
- On fire: prints `[WARN] shutdown signal received`, joins the `[STATS]` ticker thread (exits in <100 ms via the same flag), calls `EngineHandle::shutdown()` — stops TCP + QUIC + IPv6 WinDivert handles (`WinDivertShutdown(RECV)` unblocks Recv), signals the Tokio accept loop, resets stats, reaps capture/runtime threads off-thread — then prints `[WARN] engine stopped`, exit 0.
- Live Ctrl+C test was **not possible on the build machine** (no Admin/driver, so the engine exits 1 before any signal is relevant; see §2). The signal path is compiled in and `shutdown()` is the same routine the FFI `sni_stop_engine` uses. Re-run `run -c` on a Windows + Admin + driver machine, wait 3 s, press Ctrl+C: expect the two `[WARN]` lines and exit 0, no panic.

## 7. WinDivert loading path

Dynamic `LoadLibraryW("WinDivert.dll")` (`windivert/src/ffi.rs:134-146`) — no link-time `.lib` needed, EXE-dir/PATH search. Expected layout (same as pydivert `windivert_dll`):

```
sni-spoof.exe
WinDivert.dll
WinDivert64.sys
```

(Copies already exist in the repo under `runtime/`.) Engine open order: TCP handle **fail-fast** → QUIC handle **fail-closed** when `QUIC_MODE` is `block`/`spoof` → IPv6-drop handle best-effort (warn + continue). Actionable hints reuse `ffi::open_error_hint`: code 2 = DLL/sys missing, 5 = Admin/driver-blocked, 1058 = service disabled. Non-Windows builds compile via the stub and fail at open with `UnsupportedPlatform`.

## 8. Deviations from the original Rust source (with reasons)

1. **New crate `cli/`** (`cli/Cargo.toml`, `cli/src/main.rs`; `cli` added to workspace `members`): no executable existed before (only `sni-sniff` example + Go frontend). Contains only arg parsing, stdout-logging setup, `EngineHandle::start`, `[STATS]` ticker, signal wait, shutdown. No engine logic duplicated.
2. **`engine-ffi/Cargo.toml`: `crate-type = ["cdylib", "rlib"]`** (was `["cdylib"]`): one-line build-forced fix so the CLI can link `sni_engine::engine::EngineHandle` instead of duplicating the engine. The produced `.dll` ABI is unchanged; no engine logic touched.
3. **`[ERROR]` instead of `[ERR ]`**: FFI log tag normalized per the STEP 2 plan so the host greps one stable error prefix.
4. **No port-mutex in v1**: the FFI single-instance mutex (`SNI-Spoofer-Backend-<port>`) was not ported; a second instance fails on `LISTEN_PORT` bind with `[ERROR] start failed: cannot bind ...` instead. Documented limitation, avoids dragging OS-handle code into the CLI.
5. **Nested-runtime crash fix (Option A, `cli/` only, 2026-10-15)**: `#[tokio::main]` in `cli/src/main.rs:157` nested `EngineHandle::start`'s internal build + `block_on` (`engine-ffi/src/engine.rs:120/130`) inside a second runtime → panic `Cannot start a runtime from within a runtime` right after the WinDivert setup lines. Fix: the CLI owns **no** async runtime (`grep -i tokio cli/` = zero hits). `run()` is a plain sync fn; the [STATS] ticker is a `std::thread` (5 s cadence kept via 100 ms sleep slices, so shutdown stays <100 ms late); shutdown waits on one process-wide `AtomicBool` stop flag. New dependencies: `signal-hook = "0.3"` (resolved 0.3.18, unix-only target dep: SIGINT + SIGTERM) and the already-workspace-pinned `windows` crate (windows-only target dep, `Win32_Foundation` + `Win32_System_Console`: one `SetConsoleCtrlHandler` covering Ctrl+C/Break/Close/Shutdown — console Close/Shutdown have no POSIX signal numbers, so the `windows` crate expresses them and no new Windows-only crate was added). `tokio` removed from `cli/Cargo.toml`; workspace `tokio` for other crates untouched. `engine-ffi/`, `core/`, `windivert/` untouched. **No externally visible behaviour difference**: same CLI surface, same `[INFO]/[WARN]/[ERROR]/[STATS]` lines and cadence, same exit codes; only the panic is gone.
6. **`core/` untouched; WinDivert binding untouched; no RPC; no new config keys** (signal handling: see 5 — the original `tokio(full)`-based wait was removed by the nested-runtime fix).
7. **Known limitation (deferred P2 #3): `FAKE_DELAY` sleep runs on the single capture thread** (`engine.rs`). Default 0.001 s is harmless; an explicit multi-second value stalls all DPI processing per handshake. Left unchanged to avoid touching injection timing.
8. **Known limitation (deferred P2 #8): SNI board tags use FNV64, not SHA-256** (`core/src/stats.rs:hash_sni`). Sufficient for scoreboard ranking; not a secret store. No `sha2` dependency added.

## 9. Smoke-test record (2026-10-15)

- `cargo build --release` → `Finished release profile [optimized] target(s) in 2m 36s`, no warnings-as-errors, no code fixes needed.
- Temp config used (NOT committed, at `C:\Users\pc\AppData\Local\Temp\opencode\sni-smoke.json`): `{"LISTEN_HOST":"127.0.0.1","LISTEN_PORT":40443,"ENDPOINTS":[{"ip":"1.1.1.1","port":443}],"FAKE_SNIS":["example.com"]}`.
- `run -c` + 3 s + Ctrl+C: engine cannot stay up on this non-Admin review machine (exits 1 with the `[ERROR]` lines in §2), so the interactive Ctrl+C leg is deferred to an Admin + driver machine per §6.

## 10. "Make the core correct" pass (2026-10-15): bugs found and fixes

Bugs from STEP 1 diagnosis (user's Admin-machine log: RUNNING + 16/16 SUCCESS but no traffic):
1. **Dead `MODE = "Trojan + Xray"`** — accepted by validation, zero Xray/Trojan/SOCKS code behind it. Fix: removed from `SUPPORTED_MODES` (`core/src/config.rs:38`); `validate()` fails it fast with exactly `MODE "Trojan + Xray" is not implemented in this build (only "SNI Only" is supported)` (`config.rs:463-471`); dead `trojan` branch removed from the QUIC thread (`engine-ffi/src/engine.rs`, now unconditional drop); `check_mode_quic` in `core/src/selftest.rs` updated. `grep -i "Trojan + Xray" core/ engine-ffi/ cli/` hits ONLY that message + 2 doc comments. Reason: never ship a mode that silently does nothing.
2. **Silent `try_connect` failover** (`worker.rs:618-700`): every skipped/failed endpoint attempt now logs `[WARN] connect <ip:port> failed: <reason>` (bad IP, port 0, socket create, egress bind, connect error, 5 s timeout); aggregate `all endpoints failed` kept. Reason: dead endpoint lists were undiagnosable.
3. **Silent `handle()` early returns** (`worker.rs:400-454`): empty SNI / empty endpoints / `local_addr` fail / IPv6 egress now log `[ERROR]` with reason. Reason: drops previously invisible except as counters.
4. **No client-side completion line** (`worker.rs:585-611`): one `[INFO] relay done key=(<lip>, <lport>, <rip>, <rport>) up=<n> down=<n> result=ok` per completed connection; on fail `[WARN]` + `reason=only N bytes relayed (< 100 minimum)`. Counters are now TRUE per-connection `AtomicU64`s fed by the relay tasks (the old global-snapshot diff misattributed concurrent traffic). Reason: the only end-to-end proof a client flow completed.
5. **Silent reinject/send failures** (`engine.rs` capture path): `reinject`, `fake send`, `delayed retry send` promoted debug → `[ERROR]` via a 1-per-second-per-tag throttle (`throttled_error`, drops counted + noted). Reason: a dead driver / never-sent fakes previously hid at debug while stats looked fine.
6. **Q4 INFO flood** (`engine.rs` inbound arm): the `no syn sent` line (capture race vs own egress socket) moved INFO → DEBUG with identical text; other UNEXPECTED lines stay INFO. Reason: [INFO] reserved for lifecycle + per-connection completion.
7. **Relay direction errors** (`worker.rs` `relay_bidirectional`): read/write errors and the 300 s idle timeout log `[WARN] relay <client->endpoint|endpoint->client> <read|write|read idle timeout> ...`; clean EOF stays silent. Reason: direction of death was unknowable.
8. **`pub mod worker`** (`engine-ffi/src/lib.rs:24`): visibility only, so the loopback harness can drive `Worker` without a driver. No logic touched. Reason: testability without Admin.
9. **STEP 4 extras**: empty fake-burst build now `[WARN] fake burst build failed ... (bypass off)` instead of silent forward (`engine.rs`); delayed-retry thread spawn failure now `[WARN]` instead of `.ok()` drop. Re-read verdict on `tcp.rs`/`fake_tcp.rs` (pure, total functions) and bounds (DpiMap 4096-clear, boards capped 200, conns evicted + reaper, relay guard, 2000-line log cap, 5 s connect / 0.5–10 s handshake / 300 s relay-idle timeouts): nothing else on the network path swallows failures. QUIC drops stay silent by design (fail-closed mechanism, item 7).

## 11. [STATS] semantics (exact, post-fix)

- `total`: accepted client connections (incremented once per `handle()` after upstream connect).
- `success`: relays that moved ≥ 100 bytes THIS connection (`result=ok`, always paired with the `[INFO] relay done` line).
- `failed`: everything else (connect failover exhaustion, early `handle()` drops, relays < 100 bytes — always paired with a `[WARN]`/`[ERROR]` naming the cause).
- `active`: currently open relays (guard-decremented on every exit path).
- `success_rate` = success / (success + failed); `up/down_bytes` = global lifetime totals; `methods` = top-3 real methods (never the `"auto"` placeholder); boards capped at 200 entries.

## 12. Stdout contract (unchanged, cadence unchanged)

`[INFO]` lifecycle + one line per completed connection; `[WARN]` recoverable (per-endpoint fails, relay fail verdict, relay direction errors); `[ERROR]` real failures (config, driver/bind, client drops, throttled send failures); `[STATS]` full `Snapshot` JSON every 5 s. Verified live: `T4 STATS: {"type":"stats","active":0,"total":1,"success":1,"failed":0,...,"best_endpoint":"127.0.0.1:45556","best_method":"wrong_seq","methods":[{"key":"wrong_seq","ok":1,"fail":0,"rate":1.0}],"up_bytes":256,"down_bytes":256}`.

## 13. Client contract

The engine speaks RAW TCP on LISTEN_PORT. It does not implement SOCKS, HTTP, Trojan, or WS. A client that wants to be relayed must open a raw TCP connection to LISTEN_PORT and send TLS bytes; the engine will transparently fake-inject the ClientHello on the endpoint leg and relay the rest. Consequences: `curl -x socks5h://…:40443` and browser-as-proxy setups cannot work (proxy handshakes get forwarded raw to the endpoint's TLS stack); ICMP ping can never traverse a TCP-only divert. MODE `"Trojan + Xray"` status: REMOVED — validation rejects it with byte-exact `MODE "Trojan + Xray" is not implemented in this build (only "SNI Only" is supported)`, CLI surfaces it as `[ERROR] cannot load config …: config validation failed: …`, exit 1 (verified live).

## 14. T4–T6 results (temporary harness `cli/tests/`, loopback, no Admin/driver/net)

- T4 (`t4_loopback.rs`): 256 B client→mock→client round-tripped; `total=1 success=1 failed=0`; `[INFO] relay done … result=ok` present. PASS (0.11 s).
- T5 (`t5_broken.rs`, nothing on mock port): `failed=1 success=0`; `[WARN] connect 127.0.0.1:45558 failed: …` + `all endpoints failed` present; no success counted. PASS (2.02 s). Note: verdict/failover lines are `[WARN]` per the approved item 4/1 wording (the STEP 3 brief also mentions an `[ERROR]` line; no `[ERROR]` fires on this path by design — every failure names its cause at `[WARN]`).
- T6 (`t6_mode.rs`): Trojan config rejected with the byte-exact message; `"SNI Only"` validates. PASS (2 tests).
- Run: `cargo test -p sni-spoof` → all ok. Harness lives in `cli/tests/` = integration tests, never in the release build.

## 15. For the NekoGo integration chat (not implemented here)

1. NekoGo must speak RAW TLS bytes into LISTEN_PORT (transparent-dial, like xray's freedom/dokodemo → local port). No SOCKS/HTTP handshake: the first client bytes are relayed verbatim to the endpoint.
2. Suggested NekoGo-side smoke: raw `TCP dial 127.0.0.1:40443 → write ClientHello → read ServerHello`, then the `relay done … result=ok` line + `success >= 1` (replaces the unworkable `curl -x socks5h` T2).
3. NekoGo should scrape `[WARN] connect <ip:port> failed: <reason>` for endpoint health (per-endpoint cause is now logged) and `relay done … result=fail` for client-visible failures.
4. `MODE` accepts only `"SNI Only"` now; any stored `"Trojan + Xray"` configs will fail fast at startup — migrate them on the NekoGo side.
5. Shutdown: close client conns, then Ctrl+C/SIGTERM → `[WARN] engine stopped`, exit 0; no port lock after exit (handles closed, threads reaped off-thread).

## 16. Final pre-integration audit (2026-10-15)

- Findings: 13 (P0: 0, P1: 1, P2: 12). P1 = stale §6 signal description. P2 = stale QUIC/Trojan docs; `snapshot_all` log-offset race; save dropping SOCKS/HTTP ports; silent probe-thread drops; 20 `extern "C"` fns without `catch_unwind`; FNV64-vs-SHA-256 note; signal-install-total-failure hang; dead `sni-windivert` dep in cli; fixed test ports; report hygiene + missing C ABI inventory; 3 test-coverage gaps (checksum vector, AutoState budgets, throttle window).
- Fixed: 11 (P1 + all P2 except the 2 deferred). Includes: §6 rewritten; QUIC docs corrected; single-lock log offset; ports preserved on save; probe panic WARNs; signature-preserving `catch_unwind` on every export (no symbol signature changed — verified by unchanged C ABI inventory in §2); fail-fast when no signal handler arms; dep removed; reuseaddr test binds; checksum/AutoState/throttle unit tests. Plus V3-driven mechanical cleanups with zero behavior change (clippy-suggested rewrites: range patterns, `repeat_n`, `clamp`, struct-update init, `Default` impl; `not_unsafe_ptr_arg_deref` allows on the 7 pointer-taking exports with contract comments intact).
- Deferred (with reason): 2 — `FAKE_DELAY` capture-thread sleep (default 0.001 s harmless; changing injection timing needs NekoGo-side call, see §8.7); FNV64 SNI tags (sufficient for ranking; `sha2` dep not justified, see §8.8).
- Behavior changes visible to the host: (a) `sni_save_config` now preserves custom `SOCKS5_PORT`/`HTTP_PORT` (previously reset to defaults on save); (b) new `[ERROR]` lines on reinject/fake-send failure (1/sec/tag throttled) and new `[WARN]`s (per-endpoint connect causes, relay verdict + direction, empty-burst, retry-spawn) — all additive, no line removed except the Q4 INFO flood (now DEBUG); (c) CLI refuses to run (exit 1) if no shutdown handler arms. Nothing else.
- New exported symbols: none.
- Verify matrix result: V1 release build clean, zero warnings (28.24 s full rebuild); V2 `cargo test -p sni-spoof -p sni-core -p sni-engine` all ok (unit 2 + 5, T4/T5/T6 ok); V3 `cargo clippy --all-targets -- -D warnings` clean; V4 `cargo fmt --check` clean for every audit-touched file, FAILS ONLY on 44 pre-existing drift hunks in untouched regions (`core/src/{config,fake_tcp,picker,selftest,tls}.rs`, `windivert/{examples/sniff.rs,src/ffi.rs,src/handle.rs}` — each verified outside any audit edit; `engine-ffi/*`, `cli/*`, `core/src/tcp.rs` and `core/src/quic.rs` are fully clean; a tree-wide `cargo fmt` would rewrite those unrelated lines, deliberately not done); (tls/selftest/probe/state/ffi/sniff/handle/picker/config-validate/fake_tcp-handshake/tcp is clean except none — full pre-existing list in chat log; a tree-wide `cargo fmt` would rewrite ~100 unrelated lines, deliberately not done); V5 `grep -i tokio cli/` zero in shipped source (`main.rs`, `Cargo.toml` deps) — tokio appears ONLY in `cli/tests/` dev-dependency (test-only, never in the release binary); V6 Trojan grep hits only the validation message + 2 doc comments + T6 test; V7 all 23 `sni_*` symbols present with unchanged signatures (inventory in §2).

## 17. Plan A: `RESOLVE_SNI` / `CONNECT_OVERRIDE` (2026-11-01)

Two ways to give the engine a connect address now exist:

| mode | config | behavior |
|---|---|---|
| **Plan B** (default, unchanged) | `ENDPOINTS` / `CONNECT_IP` + `FAKE_SNI` | the configured IPv4 is dialed; no name resolution of any kind happens |
| **Plan A** (opt-in) | `RESOLVE_SNI: true` + `FAKE_SNIS`, no IP needed | each SNI is resolved to an IPv4 over DNS-over-HTTPS and that becomes the endpoint |

Precedence, top to bottom (`apply_plan_a_with_servers`, `core/src/config.rs:454`):

1. `CONNECT_OVERRIDE` (IPv4 literal) → endpoints become `[(override, port)]`; DoH is skipped entirely, even when `RESOLVE_SNI` is true.
2. `RESOLVE_SNI: true` → the configured endpoints are **deactivated, not deleted**: the in-memory list is replaced for this run while the file keeps `ENDPOINTS`, so clearing the flag restores Plan B exactly.
3. Neither key set → today's code path; the function returns at the `!resolve_sni` guard (`config.rs:464`) without doing any new work.

Mechanics:

- Hook: `config::load()` is now migrate → `apply_plan_a` → validate (`config.rs:166-172`). `migrate()`/`validate()` perform no I/O, so callers that only validate (GUI save) never touch the network.
- Query: `GET https://api.cloudflare-dns.com/dns-query?name=<sni>&type=A` with `accept: application/dns-json`; the first `Answer[].type == 1` whose `data` is a valid IPv4 wins (a single-object `Answer` is handled too). `Status != 0` (NXDOMAIN/SERVFAIL) or no A record → next server.
- Servers: **1.1.1.1 then 1.0.0.1**, dialed *by address* through a custom ureq `Resolver` (`core/src/net.rs:144` `PinnedResolver`). The URL hostname still drives SNI and certificate verification, so nothing is downgraded to plaintext or to `https://1.1.1.1/` (whose certificate would not verify), and the local resolver is never consulted — which is the point when local DNS is poisoned.
- Timeouts: 2 s connect + 3 s response per server, one pass each (~10 s worst case per name on a black-holing network).
- Cache: `static DOH_CACHE` (`core/src/net.rs:78`), process-wide `parking_lot::Mutex<HashMap<host, ipv4>>`, no TTL, successes only — a transient outage cannot poison later connections.
- Cap: at most `MAX_RESOLVED_ENDPOINTS = 8` SNIs are resolved per load (`config.rs:16`); beyond that `RESOLVE_SNI: resolving the first 8 of N SNIs` is logged. Derived endpoints take the port of the first configured endpoint (which already carries `CONNECT_PORT`) else 443.
- Fail fast: `config validation failed: RESOLVE_SNI: no A record for <sni> via DoH (1.1.1.1: <cause>, 1.0.0.1: <cause>)`, surfaced by the CLI as `[ERROR] cannot load config …`, exit 1. Observed verbatim on this machine: `[ERROR] cannot load config C:\Users\pc\AppData\Local\Temp\plan-a-smoke.json: config validation failed: RESOLVE_SNI: no A record for hcaptcha.com via DoH (1.1.1.1: native-tls: The token supplied to the function is invalid (os error -2146893048), 1.0.0.1: timeout: connect)`.
- Dependency: one new direct dep, `ureq = { version = "3.5", default-features = false, features = ["native-tls"] }` (workspace `Cargo.toml` + `core/Cargo.toml`). `Cargo.lock` 80 → 116 packages, all pure Rust: `native-tls` → `schannel` (Windows system TLS), so no C compiler is required — deliberate on a `x86_64-pc-windows-gnu` box with no MSVC installed, where ureq's default rustls path would need `ring`'s C sources. The response body is parsed with the workspace's existing `serde_json`; ureq's `json` feature is not enabled.
- Files touched: `core/src/config.rs`, `core/src/net.rs`, `core/Cargo.toml`, `Cargo.toml`, `cli/tests/t7_plan_a.rs` (new), `CORE_REPORT.md`. `cli/src/main.rs` needed no edit (the existing `[ERROR] cannot load config` path already prints `ConfigError`). `engine-ffi/`, `windivert/`, `fake_tcp.rs`, `tcp.rs`, `tls.rs`, `quic.rs` and the existing tests are untouched; CLI argv unchanged; no `sni_*` symbol added.
- Tests (`cli/tests/t7_plan_a.rs`, 6, all offline): Plan B passes through `apply_plan_a_with_servers(&[], …)` untouched, proving the DoH path is unreachable; Plan A resolves against an in-process fake DoH server on `127.0.0.1:0` → `203.0.113.10:443`; `CONNECT_OVERRIDE` wins with **0** hits on the fake; NXDOMAIN fails fast naming the SNI; `resolve_sni: true` without `FAKE_SNI` is rejected by `validate`; `"RESOLVE_SNI": "yes"` migrates and a non-IPv4 `CONNECT_OVERRIDE` is rejected. Addresses come from RFC 5737/5727 documentation ranges, so no live host can mask a broken test.

Scope limit — the GUI/FFI path does not run Plan A yet:

- `sni_start_engine` (`engine-ffi/src/lib.rs:331`) builds its config with `parse_and_validate` (`:287`) → `migrate` + `validate`; it never calls `load()`, so nothing resolves.
- `config_to_json` (`:257`) writes a canonical schema and drops unknown keys by design (FIX D3), so a GUI save erases `RESOLVE_SNI`/`CONNECT_OVERRIDE`; and `sni_load_config` (`:919` → `config::load`) hands the UI the *resolved* endpoints, meaning a later GUI save would freeze those IPs into the file as Plan B.
- Closing this needs a 2-line exception in `engine-ffi/src/lib.rs`: add both keys to `config_to_json` and call `config::apply_plan_a(&mut cfg)?` inside `parse_and_validate`. Out of the agreed file list for this pass, so Plan A is currently a config-file / `sni-spoof.exe run -c` feature.

Still needs a desktop test: DoH egress is intercepted on the review machine, so the live TLS handshake to 1.1.1.1 is unverified — and the system Schannel client fails identically, which shows it is the network rather than the port: `curl --resolve api.cloudflare-dns.com:443:1.1.1.1 https://api.cloudflare-dns.com/dns-query?name=hcaptcha.com&type=A` → `SEC_E_INVALID_TOKEN (0x80090308)`; `--resolve dns.google:443:8.8.8.8` → `Connection was reset`; `https://example.com` → 200. `api.cloudflare-dns.com` itself does not resolve here. Re-run on a network with working DoH using `{"FAKE_SNIS":["hcaptcha.com"],"RESOLVE_SNI":true}` and expect `[INFO] engine RUNNING` over hcaptcha's real IPs.

## 18. Plan A rewrite: multi-provider DoH + IP scan + rank (2026-11-02)

§17 shipped Plan A as a single-provider (Cloudflare `1.1.1.1`/`1.0.0.1`) resolver that failed fast if any one SNI had no A record. On networks that intercept Cloudflare DoH this made every `RESOLVE_SNI:true` config unusable, and the derived endpoint was whatever a single name answered — reachable or not. Plan A is now **resolve → scan → rank**, best-effort.

**Resolution (`core/src/net.rs`).** `DEFAULT_DOH_SERVERS` is now six pinned endpoints across three providers, tried in order: Cloudflare (`api.cloudflare-dns.com/dns-query` @ `1.1.1.1`, `1.0.0.1`), Google (`dns.google/resolve` @ `8.8.8.8`, `8.8.4.4`), Quad9 (`dns.quad9.net/dns-query` @ `9.9.9.9`, `149.112.112.9`). All three serve the same `application/dns-json` shape, so `first_a_record` is unchanged; each is still dialed *by address* through `PinnedResolver` with the URL hostname driving SNI + certificate verification. New `resolve_a_os(host)` is a last-resort OS-resolver lookup (first IPv4 via `ToSocketAddrs`); Plan A exists to dodge a poisoned local resolver, so it is only reached when every DoH provider fails, and the caller logs a `[WARN]` when it is used.

**Scan + rank (new `core/src/scan.rs`).** `sni-core` cannot depend on `sni-engine` (where the richer TLS-handshake `probe.rs` lives), so this is a deliberately lean, std-only, self-contained TCP scanner: `scan_candidates` dials every `(ip, port)` concurrently (one thread each) with `TcpStream::connect_timeout`, measures connect RTT, and returns `ScanHit`s ordered reachable-first then lowest-latency-first; `rank_reachable` keeps only the reachable band. No new dependency.

**Orchestration (`core/src/config.rs`).** `apply_plan_a_inner` replaces the old single-pass loop. Precedence is unchanged (`CONNECT_OVERRIDE` wins outright → DoH and scan skipped; `!resolve_sni` returns at the Plan B guard). Under `RESOLVE_SNI` it now: (1) resolves each SNI **best-effort** — a dead name is logged and skipped rather than aborting the run, with the OS fallback behind it on the production path; (2) fails fast only when *nothing* resolves, naming every cause (`RESOLVE_SNI: no A record for any SNI via DoH (<sni>: <cause>; …)`); (3) scans + ranks the survivors and uses the reachable set as the endpoints; (4) if the scan finds nothing reachable (the same network that blocks DoH can black-hole the scan), falls back to the resolved IPs unranked rather than failing. Scan budget is `PLAN_A_SCAN_TIMEOUT = 1500 ms` per candidate, run concurrently, so wall cost ≈ one timeout, not N×.

**Seams.** `apply_plan_a` (production) = `apply_plan_a_inner(cfg, DEFAULT_DOH_SERVERS, os_fallback=true, scanner=Some(scan::rank_reachable))`. `apply_plan_a_with_servers(cfg, servers)` is unchanged in behaviour — resolution only, `os_fallback=false`, `scanner=None` — so the existing T7/T8 offline tests stay deterministic and instant. New `apply_plan_a_full(cfg, servers, os_fallback, scanner)` (with `pub type PlanAScanner`) is the full seam the new tests drive.

**Go parity (`gui/sni_doh.go`, NekoGo).** NekoGo does not use the C ABI — `gui/spoof_manager.go` spawns `sni-spoof.exe run -c` and the core resolves at `config::load`. The Go resolver mirrors the core anyway because `resolvePlanATarget`/`ScanSNIPairs` pre-flight the same answer: `dohDefaultServers` is now the same six Cloudflare/Google/Quad9 endpoints, and `resolveA` adds a `resolveAOS` (`net.DefaultResolver.LookupIP(ctx,"ip4",…)`) fallback that runs **only when `dohServersOverride` is empty** (production), so the offline `sni_plan_test.go` fakes stay deterministic and `dead.example` still fails with `no A record`.

**Scope note (supersedes §17's "Scope limit").** The FFI path *is* wired for Plan A: `parse_and_validate` (`engine-ffi/src/lib.rs:313`) calls `config::apply_plan_a`, and `config_to_json` (`:288-298`) re-emits `RESOLVE_SNI`/`CONNECT_OVERRIDE` when set. So the standalone Wails GUI (FFI) and NekoGo (CLI spawn) both run the rewritten Plan A; only direct `validate()`-only callers (a GUI save) skip it, by design.

**Caveat (unchanged by this rewrite).** The engine still dials whatever endpoint Plan A/B derives and relies on the inner TLS SNI/Host to reach the real origin. For NekoGo that endpoint is chosen pin-first (`gui/sni_front.go:227`, defaults seeded to a generic Cloudflare IP), so a server that is *not* Cloudflare-fronted cannot be served by a CF edge regardless of how robustly Plan A resolves and ranks. This rewrite fixes dead/blocked resolution and dead-endpoint selection; it does not change the CF-front bypass model.

**Tests.** `cli/tests/t7_plan_a.rs` (8, all offline): the six §17 tests plus `t7_plan_a_multi_provider_failover` (first provider = a closed loopback port → refused; second = the fake, which answers) and `t7_plan_a_scans_and_ranks_reachable_first` (fake DoH answers a documentation address and `127.0.0.1`; a live loopback listener makes only the latter reachable, and the scan drops the former). `engine-ffi/tests/t8_ffi_plan_a.rs` (3) unchanged and passing. Verify on this machine: `cargo clippy -p sni-core -- -D warnings` clean; `cargo test -p sni-spoof --test t7_plan_a` 8/8; `cargo test -p sni-engine --test t8_ffi_plan_a` 3/3; `rustfmt --check` clean for `net.rs`/`scan.rs`/`t7_plan_a.rs` (`config.rs` shows only the pre-existing §16 drift at lines 616+, untouched); `gofmt -l gui/sni_doh.go` empty, `go vet ./gui` clean, `go test ./gui -run <sni_plan_test funcs>` 8/8. Not run here: a full `cargo build --release`, the T4–T6 engine loopback tests (unaffected — Plan B path is guarded), and the Wails/Go app build (disk at ~100% and no Admin/WinDivert driver, so the live bypass path is still unverified on this box).

## 19. v3: strategy model, real-ClientHello splitting, latency-aware `auto` (2026-11-04)

Trigger for this pass was three complaints: every known SNI-spoof method (this project's and other apps') fails against the DPI in use; there are too many knobs; and two saved configs connect in ~900 ms / ~1000 ms. All three turned out to be the same underlying defect — the core optimised *what it showed the DPI instead of the real hello* and ranked *success only*, never speed.

**Diagnosis (the part that mattered).** Every v2 method is a *decoy*: the engine injects a fake ClientHello with a bogus SNI (wrong seq, clamped TTL, retransmitted overlap, padded, hostname-swapped, MD5-signed out-of-window) so the DPI records a permitted-looking handshake. But the client's **real** ClientHello still left the host as one intact, parseable segment: `0x16 0x03 0x01 …` with a full SNI extension and a contiguous hostname. A DPI that matches on the real stream — or simply ignores injected packets it never saw the ACK for — gets the true hostname for free. Adding more decoy shapes could not fix that, which is the honest reading of "DPI is stronger than SNI spoofing".

### Strategy = decoy × split, not method

`core/src/fake_tcp.rs` now separates the two decisions:

```rust
pub struct Strategy { pub decoy: BypassMethod, pub split_real: bool }
```

- `decoy` — which fake burst to inject (7 builders, unchanged in spirit).
- `split_real` — whether the **client's own** hello is segmented on its way out.

`Strategy::pool()` is the Cartesian product: 7 decoys × {plain, split} = 14, plus one decoyless entry (`hello_split`, `split_real` only) = **15 strategies**. `auto` is no longer "pick a method per connection"; it is a search over those 15 with a scoreboard. `board_key()` is the identity used everywhere (stats rows, `[STATS].best_method`, console): a decoy name, or `"<decoy>+split"`, or `hello_split`.

The user-facing vocabulary shrank while the internal search grew: 10 `BYPASS_METHOD` names → **9** (`auto, wrong_seq, wrong_seq_ttl, split_seq, fragmented, padding, hello_split, hostfakesplit, fakedsplit`), and 6 `TLS_FINGERPRINT` names → **3** (`legacy, chrome, firefox`).

### The real-hello splitter (`hello_split`)

`split_hello(seq, data)` cuts the *outbound payload the client actually produced*, so no TCP segment carries a whole hostname label:

1. `find_sni_host` / `hostname_labels` parse the ClientHello and return the byte spans of each DNS label inside the SNI hostname.
2. Labels shorter than `MIN_SPLIT_LABEL` (5) are skipped — cutting inside `www` buys nothing.
3. The **longest** `MAX_HELLO_CUTS` (3) labels are chosen and each is cut at its midpoint, so `youtube` becomes `you|tube` across a boundary rather than being shoved whole into the next segment. (The first draft cut one byte after the label start, which left `w.youtube.com` contiguous in segment 2 — self-caught and replaced.)
4. If the hello does not parse, the fallback cut is at `TLS_RECORD_HEADER` (5), i.e. the record type is separated from its length/version — never "no split at all", because an unparsed hello is exactly the case where leaking is most likely.
5. `split_hello` emits N+1 segments with **contiguous, in-window sequence numbers** (PSH only on the tail) so the stack reassembles byte-identically; the total is `data.len()` bytes with no overlap, no gap, and no retransmit. Non-handshake payloads are never split (`HandshakeState` gates it on `sch_fake_sent` + `strategy.split_real`), so steady-state traffic keeps its normal segmentation.

`engine-ffi/src/engine.rs` drives this from `OutboundAction::MaybeSplitHello` → `OutNext::NeedSplit(payload, seq, strategy)`, rebuilding each segment with `tcp::build_fake_tcp`; if any segment fails to build the packet forwards untouched rather than being dropped. The split path does **not** reinject the original packet (that would duplicate bytes on the wire).

### Latency is now a first-class number

- **Scoreboard** (`core/src/stats.rs`). Each row (`Cell`) keeps lifetime `ok`/`fail` for display plus two EWMAs that ranking actually uses: `rate_ewma` (`RATE_ALPHA 0.3`) so a strategy that worked yesterday and fails today stops winning today, and `lat_ms` (`LAT_ALPHA 0.4`, unset until measured). `score = rate_ewma − min(lat_ms / 1500, 1) × 0.45` — the cap at `MAX_LAT_PENALTY 0.45` is deliberate: **latency demotes, failure eliminates**, so a 1200 ms working strategy still outranks a broken one but loses to a 60 ms one. `record_latency(endpoint, method, ms)` is fed from the connect path; non-finite/negative samples are ignored and absurd ones clamped. `ranked()` rows gained `latency_ms` + `score` and sort by score, then sample count, then key — so the GUI table shows the same order the engine prefers. `strategy_score`/`endpoint_score` return `None` for never-tried rows, which is what makes `auto` explore instead of converging on the first success.
- **`AutoState`** (`engine-ffi/src/worker.rs`). Sticky, with four rotation triggers: 3 consecutive failures (`MAX_FAILURES`); a *working but slow* strategy (EWMA > `SLOW_SWITCH_MS 800` after ≥ `MIN_LATENCY_SAMPLES 2` samples — this is the 900/1000 ms case, "it connects and it feels broken"); the exploration queue still non-empty after `EXPLORE_EVERY 4` connections; otherwise keep the current strategy forever. Rotation pops the shuffled 15-entry queue while it has entries, then asks the board for the best-scoring strategy that is not the one just dropped, falling back to a random pool entry only when the board is empty. A success resets the consecutive-failure count, so a flaky network cannot thrash.
- **`FAKE_DELAY` off the capture thread** (`engine.rs`). The v2 TCP thread did `Recv → sleep(delay) → inject`, so *every* packet on every connection waited behind a decoy timer — with `FAKE_DELAY 0.05` that is a 50 ms stall per burst, on the single thread that also forwards real traffic. Bursts now go through a bounded `sync_channel(64)` to a dedicated `sni-fake-delay` feeder thread (spawned only when `fake_delay > 0`); if the queue is full the capture thread sends inline rather than dropping the decoy, so the worst case is the old behaviour, not a leak.
- **Retired `delayed_retry`** (a fixed ~1.5 s injected burst — literally a config that adds ping) and **`double_sni`** (two hellos where the first is byte-identical to `wrong_seq`, so it was decoration, and it doubled the injected surface). Both migrate to `auto`.
- **Failover budget from measured RTT.** `try_connect` used a flat 5 s per endpoint, so with 4 dead endpoints the first byte of a live connection arrived ~20 s after the click. The per-attempt budget is now `clamp(4 × best_endpoint_latency, 800 ms, 5 s)` (`connect_budget()`), using the board's fastest measured endpoint, and `order_endpoints()` tries board-ranked endpoints before unmeasured ones (neutral 0.5 — ahead of a failing row, behind a working one). A network whose fastest path is 40 ms no longer waits 5 s on a black-holed IP.

### Config surface: fewer names, no breakage

`config::canonical_method()` maps `double_sni` / `delayed_retry` → `auto` and logs a `[WARN]`, so existing `config.json` files load and run instead of failing validation; `TLS_FINGERPRINT` canonicalises through `TlsProfile::parse` (`chrome_120`/`chrome_124`→`chrome`, `firefox_122`/`firefox_124`→`firefox`, `custom`→`legacy`) and `migrate` rewrites the saved value. Unknown names are still hard errors — the point is to migrate spellings that meant something, not to accept typos. `core/src/config.rs::SUPPORTED_METHODS` is an alias of `fake_tcp::SUPPORTED_METHODS`, and `cli/tests/t9_v3_methods.rs` pins both to the exact 9 names so the Rust and the `gui/frontend/src/types.ts` lists cannot drift silently. The three surviving TLS profiles are now genuinely distinct builders (the §16-era note "only legacy differs" is obsolete — `chrome` emits GREASE/ALPS, `firefox` no GREASE).

Frontend: `types.ts` is the single source for `METHODS` / `FINGERPRINTS` / `QUIC_MODES`; `BypassPage.tsx` had hardcoded copies of the fingerprint and QUIC lists and now maps over the constants; `MethodPicker` help text describes the strategy pool. `npx tsc --noEmit` clean.

### What is proven, and what is not

Proven offline: `core::selftest` gained `hello_split` (3 profiles — no segment parses as a hello, no `"youtube"` substring anywhere, seqs contiguous, reassembly byte-identical, app data untouched), `strategy_pool` (15 distinct keys, `hello_split` present, retired names rejected, `Strategy::random()` ⊆ pool) and `latency_ranking` (fast-ok > slow-ok > broken, untried → `None`, `best_method` = the fast row, snapshot exposes latency). `fake_tcp` unit tests add `split_never_leaks_a_whole_hostname_label`, `split_is_contiguous_and_lossless`, `every_long_label_gets_its_own_cut`, `non_handshake_payload_is_not_split`, `unparseable_hello_falls_back_to_record_header_cut`. `worker.rs` `auto_state_tests` covers exploration, stickiness, 3-failure rotation, success-reset, slow-replacement, not-enough-samples, board-decides-after-exploration, empty-board fallback and config→strategy mapping. `t9_v3_methods.rs` covers migration, validation and the end-to-end selftest.

**Not proven here, and stated plainly:** this box has no Admin token and no WinDivert driver, and the local network intercepts DoH *and* outbound TLS (a Schannel handshake to `1.1.1.1:443` fails here for every client, including `curl`), so **no on-wire bypass claim in this section is measured**. The gains are at the packet-planner and state-machine level. Mechanically, splitting the real hello defeats DPI rules that match a hostname (or a hello shape) inside a *single* segment and rules that act before reassembly completes — which is what most deployed TLS-SNI signatures are. It does **not** defeat a DPI that fully reassembles the TCP stream and then parses the hello: after reassembly the hostname is present, whole, and correct, because the server needs it to be. Against that device the only thing that survives is not putting the hostname on the path at all (encrypted ClientHello/ECH, or a transport whose SNI is not the routing key), and no client-side TCP trick in this or any other app substitutes for that. That is the honest ceiling of what this pass can do, and it is also why the `auto` pool is scored on latency as well as success: when the DPI is doing reassembly, several strategies "work" only because they are being let through slowly.

**Tests + verification record (2026-11-04).** `cargo test --workspace` 44/44 green: `sni-core` 10 (unit) + `sni-engine` lib 13 + `t4_loopback` 1 + `t5_broken` 1 + `t6_mode` 2 + `t7_plan_a` 8 + `t8_ffi_plan_a` 3 + `t9_v3_methods` 6. `cargo clippy --workspace --all-targets -- -D warnings` clean; `rustfmt --check` clean for all eight touched Rust files (`core/src/{stats,fake_tcp,selftest,config,tls}.rs`, `engine-ffi/src/{worker,engine}.rs`, `cli/tests/t9_v3_methods.rs`); `npx tsc --noEmit` clean for the frontend. `cargo build --release --workspace` succeeded and `sni-spoof.exe` / `sni_engine.dll` / `libsni_engine.dll.a` were copied back into `sni spoofing/target/release/`; the release exe was run against a config naming the retired `double_sni` and printed `[WARN] BYPASS_METHOD "double_sni" was removed from this build; using "auto" (which now ranks real-hello splitting too)` before failing cleanly on the missing WinDivert driver, which is the expected outcome on this box.

Two flakes were fixed on the way. The `engine.rs` throttle tests asserted against wall time (one slept 1.1 s), so a loaded machine could stretch the gap between two calls past the 1 s window and flip the assertion — `throttled_error` now delegates to a clock-injecting `note_error`, the tests use a local `ErrThrottle` plus synthetic timestamps, and the lib suite dropped from 1.10 s to 0.00 s. `AutoState::best_scored` also used to fall back to `Strategy::random()`, which could hand a rotation back the strategy it had just dropped (~1 in 15, and the same nondeterminism was leaking into `auto_state_tests`); it now steps to the next pool entry, so a rotation always changes the strategy, and the affected tests pin `current` instead of trusting the shuffled constructor. `cargo test -p sni-engine --lib` run five times consecutively: 13/13 each.

**Not refreshed by this pass:** the previously shipped copies at `sni spoofing/gui/build/bin/` and `sni spoofing/dist/portable/` (both Oct 15) and `gui/build/bin/sni-spoof.exe` at the repo root still hold older binaries — say the word and they get the v3 pair.

## 20. Live-network test of `spoof_config.txt` against YouTube / Instagram / Google (2026-11-05)

The question put to this pass was concrete: do the links in `sni spoofing/spoof_config.txt` work, and can the core carry YouTube, Instagram and Google search on the network this box is actually on. Everything below is measured here, on this gateway. The scratch harness used to get it (`cli/tests/zz_scratch_live.rs`, `cli/tests/zz_scratch_proxy.rs` and a temporary `native-tls` dev-dependency) has been deleted; the tree is back to the shipped file set.

### What is in the file

`spoof_config.txt` is 1,169,697 bytes and 6,645 subscription links.

| kind | links | meaning |
|---|---|---|
| `…@127.0.0.1:40443` | 5,837 | front-name-only links (`sni=…` / `host=…` carried in the fragment) — no server address at all |
| `…@0.0.0.0:40443` | 1 | same idea, wildcard bind |
| real host:port | 807, over **377 unique endpoints** | directly reachable if the server is running |

Top real endpoints by link count: `185.5.38.111:55988` ×89, `mx2.drawnrisha.one:55988` ×26, `71.19.148.74:443` ×10, `195.93.253.245:49137` ×9, `173.244.56.6:443` ×7.

The 5,838 loopback links are the bulk of the file and they are the part that cannot work as written: `127.0.0.1:40443` is *this core's own listen port* (§13), not a server. Importing them gives a client that dials itself. They only become usable once `ENDPOINTS` (Plan B) or `RESOLVE_SNI` (Plan A) supplies the real address — and on this network Plan A is dead, because DoH is intercepted, so Plan B with literal IPv4s is the only working path.

Of the 377 real endpoints, 42 were sampled (two connect attempts each, 2 s timeout): **15 alive, 27 dead**, TCP RTT of the living ones in the 97–514 ms band. **No sampled endpoint had an RTT over 900 ms**, which matters for the original "two configs gave me 900 ms and 1000 ms" complaint: those figures cannot be plain TCP round-trip to these servers. They are consistent with what this gateway does — a delay before the RST, or the decoy-terminator handshake (§13) plus failover on top of a real RTT — i.e. latency introduced by *blocked TLS*, not by a distant server. That is exactly the case `AutoState`'s `SLOW_SWITCH_MS` path is meant to stop rewarding, but it also means the two slow configs were not diagnosable as slow-server links. Coverage is 42/377, so this is a sample, not a census.

### How this gateway blocks

TCP `connect()` to port 443 on any test host always succeeds. The drop happens one layer up, after the ClientHello leaves, in one of two forms:

- **RST** — `os10054` (`WSAECONNRESET`), no server response at all.
- **Injected plaintext reply** — a non-TLS `HTTP/1.1 302 found\r\nLocation: http://10.10.34.35…`, 415 bytes, delivered as if it came from the server. A real TLS peer never sends that record type on a handshake, so this is a gateway writing into the stream.

That is why "DPI is stronger than SNI spoofing" felt universally true: a client-side proxy that forwards an intact ClientHello gets either reset or a 302 to a local intercept address, no matter which decoy is also on the wire.

### The three tasks

| task | verdict | evidence |
|---|---|---|
| **Google search** | works, and needs no bypass | direct HTTPS from this box returns `HTTP/1.1 200 OK`, 1526 bytes, 712 ms. `www.google.com` is not in the filtered SNI set. |
| **YouTube** | **works only with the real hello segmented**, and non-deterministically | same bytes, `whole` → `KILLED` / handshake failure. Segmented with the v3 `hello_split` path, the raw probes returned `SERVERHELLO` ×4 at roughly one RTT (`cuts=[130] sizes=[130, 387]`). At application level over the relay, one run produced `HTTP/1.1 200 OK`, 2048 bytes, 1120 ms **with certificate verification enabled**; a later re-run of the identical bytes failed (`HANDSHAKE-FAIL`, `seg_sizes=[108, 69]`). So: split traverses this gateway, but the gateway does not grant it every time — treat it as working-sometimes, not as reliable. |
| **Instagram** | does not work | `whole` and `split` both `KILLED` / handshake failure; the relay's upstream `connect()` to the host even failed outright in one run. Instagram is filtered harder than YouTube and no client-side segmentation in this build defeats it. |

The split-vs-whole separation on YouTube is the load-bearing result: the same bytes that are reset when sent in one segment reach a real ServerHello when cut mid-label, which is the §19 prediction holding on live traffic — this gateway matches inside a single packet and acts before reassembly. The `[108, 69]` non-determinism was investigated and is **not** a splitter defect: `split_hello` cuts at the midpoint of the longest hostname labels (`youtube` → `you|ube`), the unit tests `split_never_leaks_a_whole_hostname_label` and `every_long_label_gets_its_own_cut` assert that for all three profiles, and the segment pair still reassembles byte-identically. The variance is the gateway's own statefulness.

### TLS fingerprint: two real wire bugs found and fixed, and one that is not

`chrome` was being rejected by real servers, and the cause turned out to be genuine malformed extensions in `core/src/tls.rs::build_chrome_hello`, not the gateway:

- `compress_certificate` (0x001b): `preferred_algorithms` is `<0..2^16-1>` of 2-byte IDs, so the vector needs a **u16** length prefix. It was emitted with a u8 → server-side decode failure. Now `00 06 00 02 00 01 00 03` (zstd = 0x0003).
- `application_settings`/ALPS (0x4469): the per-protocol entry length was written before the entry existed, so the u16 counted the wrong bytes. The entry is now built first (`h2` + empty config) and its real length pushed.
- `status_request` SCT (0x0012): response extensions were emitted with an empty body where the responder-ID/response-bytes structure requires a 2-byte zero length; now `00 00`.

`core/src/selftest.rs` was rewritten around these so the class of bug cannot come back: `check_tlsfp()` now walks **every** extension of **all three** profiles through `walk_extensions()` + `check_ext_inner()`, which re-parses each vector of the handshake grammar (cipher list, compression methods, supported groups, signature algorithms, ALPN, supported_versions, psk key exchange modes, key_share entries, cookie, pre_shared_key, ALPS, and the extensions list as a whole) rather than pattern-matching raw bytes. It is this walker that caught the psk-kem-mode defect in its own new code (modes are 1-byte enums: `0x002d` tiles at width 1, `0x002b` at width 2). A pre-existing flake went at the same time: the Firefox no-GREASE rule used to scan raw hello bytes, so it failed roughly one build in fifteen when a random field happened to contain a `?A?A` pair; it now inspects only the places GREASE can legally appear — the cipher list and the two id-vector extensions.

**Unresolved, stated plainly:** even with those fixes, and even reduced to a minimal 286-byte hello, `chrome` still draws `ALERT-50` (`fatal decode_error`) from google, github and cloudflare. `legacy` and `firefox` are accepted. The remaining cause is not identified, so the shipping default stays `legacy` and `gui/config.example.json` was corrected from `"chrome"` to `"legacy"` — it previously advertised the one profile this build cannot negotiate successfully.

### Verification after this pass

`cargo test -p sni-core -p sni-spoof` with the scratch files and the `native-tls` dev-dependency removed: exit 0, eight suites all `test result: ok`, 0 failures — including `t9_v3_methods` (6/6), which runs the strengthened `check_tlsfp()`. `native-tls` remains in `Cargo.lock` only as a transitive dependency of `ureq`, which is how it was before the scratch harness.

## 21. "SNI spoofing stopped working" — three startup blockers (2026-11-05)

Reported symptom: the bypass no longer works at all. Nothing here is a tuning question — three separate things each stopped the engine from doing its job, and two of them stopped it *before a packet was built*. All three are fixed and deployed.

### 1. `MODE "Trojan + Xray"` made every pre-audit config unloadable

§16 turned that mode into a hard validation error. Any `config.json` written before the audit therefore refuses to load, which is a startup failure, not a slow bypass:

```
[ERROR] cannot load config …\config.json: config validation failed: MODE "Trojan + Xray" is not implemented in this build (only "SNI Only" is supported)
```

`core/src/config.rs` now has `canonical_mode()` beside `canonical_method()`: `migrate()` rewrites the retired spelling to `"SNI Only"` with a `[WARN]`, exactly the pattern already used for `double_sni`/`delayed_retry`. `validate()` keeps rejecting the raw spelling, so a `Config` that was never migrated still fails fast with the same byte-exact message. After the change the same command prints the `[WARN]` and proceeds to the driver:

```
[WARN] MODE "Trojan + Xray" was removed from this build; using "SNI Only" (no Xray/Trojan transport is implemented here)
[ERROR] start failed: WinDivert open failed: driver open failed: Access denied (code 5) — relaunch as Administrator. …
```

`cli/tests/t6_mode.rs` was rewritten to pin both halves (legacy mode migrates; an unmigrated `Config` still fails with the exact message). The standalone GUI's picker was still *offering* the removed mode — `gui/frontend/src/types.ts` `PROXY_MODES` — so it could write a config the core rejects; it is now `["SNI Only"]`.

### 2. The endpoint resolver refused every site that is not on Cloudflare

`gui/spoof_manager.go::resolveDomainToCFIP` returned an error unless the DNS answer contained a Cloudflare edge:

```
resolve "www.youtube.com": 216.239.38.119 is not Cloudflare-fronted (want 104./172.67./141.101./108.162.)
```

That ran inside `SpoofManager.Start`, so YouTube, Instagram and Google — none of them Cloudflare-fronted — could never start the engine at all. The "any CF edge serves any CF site" trick is real but it is a *preference*, not a precondition; a non-CF site simply has to be dialed at its own address, where the decoy injection and hello splitting then apply.

`resolveSpoofEndpointIP` now takes a Cloudflare edge when the answer set has one and otherwise the first public IPv4. The choice was split out into `pickSpoofEndpointIP(domain, ips)` precisely because the fused version could only be exercised with live DNS — which is why it shipped broken. `TestPickSpoofEndpointIP` covers: non-CF site returns its own IP, a CF edge wins in a mixed set, IPv6 answers are skipped, a private-only answer set fails with the interceptor hint, an empty set fails. `sni_scan.go`'s Plan A warning was reworded, since "a profile hosted behind Cloudflare cannot ride it" is no longer true.

### 3. Stored settings overrode every profile with one Cloudflare edge

`%APPDATA%\NekoGo\config.json` held `sniConfig.method = "wrong_seq"` plus `sniSelectedIp = 162.159.192.1` and `sniSelectedSni = developers.cloudflare.com`. `spoofPinIP`/`spoofPinSNI` return the pin whenever it is non-empty and not a member of the curated lists — and both lists were empty (`sniIPs: 0`, `sniSNIs: 0`), so the pin always won. Every profile, whatever its own address or `sni=`, was therefore rendered as *dial 162.159.192.1:443, present `developers.cloudflare.com`*. Combined with `wrong_seq` — a decoy-only method that §20 measured as not traversing this gateway — the engine was pointed at one Cloudflare edge using the one strategy class that cannot work here.

Changed in place (app closed, original kept as `config.json.pre-hello-split.bak`): `method` → `hello_split`, both pins cleared so each profile's own address/SNI applies. `hello_split` is the strategy §20 measured reaching a real ServerHello; `auto` remains the adaptive choice and includes it.

### The NekoGo picker was stale

`gui/frontend/src/components/SNIPanel.vue` still listed the removed `delayed_retry` and `double_sni`, did **not** list `hello_split` (so the one working strategy was unselectable), and offered only retired fingerprint spellings (`chrome_120`/`chrome_124`/`firefox_122`/`firefox_124`). Both lists now mirror the core's accepted sets — 9 methods, 3 fingerprints — with a comment saying so. Frontend rebuilt and re-embedded.

### What still cannot work here, and why

`www.instagram.com` resolves to **10.10.34.36** on this network — the interceptor's own private range, the same address the injected `HTTP/1.1 302` in §20 pointed at. The poisoning is in DNS, upstream of any TLS trick, so no client-side SNI method can reach Instagram from this box; `pickSpoofEndpointIP` now says exactly that (`only private answers (10.10.34.36) — local resolver/DPI intercepted it`) instead of blaming Cloudflare. The escapes are Plan A (DoH — blocked here, §17) or a real proxy profile. `www.youtube.com` (216.239.38.119) and `www.google.com` (216.239.38.120) both resolve to public IPv4 and now start.

### Verification and deployment

`cargo test -p sni-core -p sni-spoof` exit 0, 8 suites ok. `cargo clippy --workspace --all-targets -- -D warnings` clean after fixing 5 `manual_is_multiple_of` in the new `selftest.rs` helpers and 1 `field_reassign_with_default` in `t6_mode.rs`. `rustfmt` clean for `core/src/selftest.rs`, `core/src/config.rs` and `cli/tests/t6_mode.rs`; the pre-existing drift in `core/src/picker.rs` and `windivert/*` is still deliberately untouched (§16). `go vet` and `gofmt -l` clean for the five touched Go files; `go test .` fails only on the 5 known-pre-existing `geoip_test.go` DB cases.

Staged: `gui/build/bin/nekogo.exe` (rebuilt with the new frontend) and `gui/build/bin/sni-spoof.exe` — the pair NekoGo actually spawns — plus `sni spoofing/target/release/`, `sni spoofing/gui/build/bin/` and `sni spoofing/dist/portable/`. `ip_list.txt`/`sni_list.txt` were also copied into `sni spoofing/gui/build/bin/`, which had neither, so a first launch of the standalone GUI there no longer seeds an empty `ENDPOINTS`/`FAKE_SNIS` config.

**Not measured:** this shell has no Admin token, so the engine still stops at `WinDivert … Access denied (code 5)`. What is proven is that the config now loads and reaches the driver; the on-wire behaviour is §20's, unchanged. Running NekoGo elevated is still required.

## 22. "sni spoofing 2026": driverless measurement, and the scoreboard was lying (2026-11-05)

§20 and §21 could only say *what the engine did* by reading its log, because measuring
on-wire behaviour required the WinDivert driver and therefore Admin. This section
replaces that with a measurement instrument that needs neither, runs it against the live
gateway, and then fixes the two things the measurements exposed — one of which
invalidates every strategy ranking the engine has ever produced.

### 22.1 `probe` mode: measure the DPI without the driver (`cli/src/probe.rs`)

`sni-spoof.exe probe --host=<name> [--ip=<edge>] [--vector=a,b] [--reps=n] [--delay=ms]
[--timeout=ms] [--profile=legacy|chrome|firefox]` builds a ClientHello with
`build_fake_client_hello`, cuts it into segments with the **engine's own** `split_hello`,
and sends the pieces over a plain blocking TCP socket — no driver, no injection, no
Admin. One `[PROBE]` JSON line per attempt, one `[SUMMARY]` per vector. Outcomes are
classified from the first response byte: `SERVERHELLO` (0x16 + type 0x02), `ALERT`
(0x15), `INJECTED-HTTP` (anything that is not a TLS record), `RST`, `CLOSED`, `TIMEOUT`,
plus connect/write failures. `ok` counts only `SERVERHELLO`; the other outcomes are
reported but never inflate it. `--ip` is mandatory when the resolver is being lied to
(§22.5), and a private/CGNAT answer is refused with a message instead of being dialled.
Seven unit tests cover shape construction, classification, the intercept-address filter
and the pacing rule.

Vectors: `whole` (the intact hello — the control), `split` (the engine's randomised label
cuts), `split_paced` (same cuts plus `--delay` between writes), `lead5` (only the 5-byte
record header in the first segment).

### 22.2 The measured gateway answers in two layers

Round 1, `--reps=5`, legacy profile:

| target | edge dialled | whole | split | split_paced | lead5 |
|---|---|---|---|---|---|
| `www.youtube.com` | 216.239.38.119 | **0/5** (1 injected-302, 4 RST) | **5/5** @165 ms | **5/5** @186 ms | **5/5** @192 ms |
| `www.google.com` (control) | 216.239.38.120 | 5/5 @161 ms | 5/5 @341 ms | 5/5 @165 ms | 5/5 @335 ms |
| `www.instagram.com` | 57.144.244.34 (real Meta edge) | **0/5** | **0/5** (5 injected-302) | **0/5** | **0/5** |

The kill for `whole` is the interceptor's own plaintext on port 443:
`HTTP/1.1 302 found\r\nLocation: http://10.10.34.35:80 ...` (415 bytes). Google passes
every vector, which is the control that proves the probe measures the gateway and not
itself.

Instagram is the interesting failure: it dies **with the split that works on YouTube**,
so the block there is not the hostname. Two cross tests settle it, same gateway,
`--reps=5`:

| SNI presented | edge dialled | whole | split | split_paced | lead5 |
|---|---|---|---|---|---|
| `www.instagram.com` | 216.239.38.120 (Google) | 0/5 | **5/5** @396 ms | **5/5** @360 ms | **5/5** @258 ms |
| `www.youtube.com` | 57.144.244.34 (Meta) | 0/5 | **0/5** | 0/5 | 0/5 |

So the gateway has two independent gates:

1. **SNI-string gate.** Present a blocked hostname intact and it is killed; hide it by
   cutting each long label and the same name traverses — *including
   `www.instagram.com`, which traverses 5/5 when the socket is connected to a Google
   edge.* This gate is beaten by segmentation, and the fact that `lead5` (record header
   alone in packet 1, whole body in packet 2) also passes proves the matcher looks
   inside a single segment: **this DPI does not reassemble the TCP stream before
   deciding.** That is the direct answer to §19's stated ceiling ("splitting defeats
   single-segment matchers but not a DPI that reassembles") — on this network the
   reassembling case does not obtain.
2. **Destination gate.** Anything dialled into the Meta range is killed after the
   ClientHello regardless of shape and regardless of which name it carries: YouTube's
   name, which traverses on 216.239.38.x, dies on 57.144.244.34 under all four vectors.
   **No client-side TLS trick beats an IP-prefix block**, so §20's Instagram verdict
   stands, and the honest escape remains a real proxy profile, not a better hello.

### 22.3 The scoreboard was scoring kills as wins — fixed

Two independent bugs, both visible only once the probe established what a *real*
traversal looks like:

* `HandshakeState::on_inbound` treated **any** inbound payload after the fake burst as
  `Success` (`core/src/fake_tcp.rs`; the comment above that branch still reads "FIX P0:
  previously only a pure ACK counted, which never happens in a real flow"). The
  interceptor's kill page *is* inbound payload, so every destroyed connection was
  reported as bypass complete.
* The relay's final verdict was `bytes_moved >= 100` (`engine-ffi/src/worker.rs`). A
  415-byte injected 302 is 415 bytes moved toward the client, so the same flows also
  passed that rule. Together they meant `auto` ranked never-traversing decoy strategies
  (`wrong_seq`, §20 measured it at 0/5) alongside the splitting ones — which is exactly
  why §21 found a stored config sitting on `wrong_seq` while its own statistics claimed
  success.

Now the DPI state machine receives the inbound TCP payload (`tcp_payload`,
`core/src/tcp.rs:133`; call site `engine-ffi/src/engine.rs:808`) and classifies it: a
payload that does not start with a TLS record content type (20..23) is the interceptor
speaking, not the server, and yields `InboundAction::Intercept`
(`core/src/fake_tcp.rs:830`, `:961`) carrying a printable preview of the injected text.
The engine turns that into `NextIn::Intercept` (`engine-ffi/src/engine.rs:817`, `:871`),
which marks the connection (`engine-ffi/src/worker.rs:434`) *and* still wakes the
handshake the old way. The mark exists because `ConnEntry` is evicted before the relay
starts, so a late intercept cannot reach the verdict through the entry; `ActiveGuard`
clears it on every exit path. The verdict is now one rule with a name and a test —
`connection_verdict(bytes_moved, interceptor_answered)` (`core/src/stats.rs:415`,
`MIN_RELAY_BYTES` at `:405`) — and an interceptor answer overrides the byte count
(`engine-ffi/src/worker.rs:693`). The fail log distinguishes the two reasons.

Deliberately unchanged: a TLS **alert** still counts as traversal (0x15 is in the record
set), because an alert proves the DPI let the hello through and a real endpoint answered
— that is a bypass success even when the session then dies, and re-scoring it as a
failure would punish strategies for the server's behaviour. A caller that has only
headers (selftest, or any future view without payload bytes) keeps the historical
verdict: the classifier refuses to guess when it cannot see (`&[]` → `Success`).

### 22.4 Shapes now vary per connection, and `lead5` was *not* promoted

`hello_split_points` (`core/src/fake_tcp.rs:668`) no longer produces one deterministic
cut pattern. The longest label always gets a cut, the remaining cut budget is sampled
randomly instead of always dropping the same tail, each cut lands at a random offset
*strictly inside* its label, and a spare slot sometimes buys an extra cut in the
random/session-id area. The invariant that matters is preserved — no segment ever holds
a whole label long enough to be a blocklist key — while the sizes a matcher could learn
are not stable. Visible on the wire, not just in tests: one run produced `[22,110,385]`,
`[136,381]` and `[43,92,382]` for the same host, and the Google control run produced
four different shapes in five attempts.
`split_shape_varies_across_connections` draws 200 splits and requires ≥3 distinct shapes
while asserting contiguity, PSH-on-tail and byte-identical reassembly every time.

`selftest::check_hello_split` had to change: it asserted exactly **2** segments per
profile, which is itself a shape signature, and it would have failed the moment the
randomiser started working. It now fails if the split *stops* randomising into a valid
shape (fewer than 2 segments, or more than `MAX_HELLO_CUTS + 1`) while checking
contiguity, PSH placement, losslessness and the no-whole-label rule over 8 draws per
profile instead of 1.

**`lead5` measured 5/5 on every traversable target and is still not a production shape.**
Cutting at the record header alone leaves the entire hostname intact inside the second
segment; it passes here only because this DPI inspects one segment — precisely the
assumption §19 refused to rely on — and a device that ever starts reassembling, or
simply matches "5-byte TLS record header followed by 512 bytes", turns it into a
signature for the whole fleet. The label-cut rule holds under both single-segment and
reassembling inspection, so production keeps it, and `lead5`'s real value is as the
fallback for hellos that cannot be parsed at all (unparseable → cut at 5 → *something*
is separated, and this gateway measurably accepts that).

### 22.5 DNS: the poison is probabilistic, so one lookup is not enough

§20 recorded that `www.instagram.com` resolves to a captive address. The probe work shows
it **alternates** between the real Meta edge (57.144.244.34) and the poison (10.10.34.36)
for the same name on the same resolver, while UDP/53 to 8.8.8.8, 1.1.1.1 and 9.9.9.9 is
transparently redirected and always answers the intercept address (§17's DoH result
holds: pinned DoH is not an escape here). `resolveSpoofEndpointIP` therefore stopped
trusting a single answer (`gui/spoof_manager.go:289`): it accumulates answer sets across
at most `spoofResolveAttempts = 3` lookups (`:323`), deduplicated, and hands each
accumulated set to the existing `pickSpoofEndpointIP`, so the first usable public IPv4
wins and a Cloudflare edge still outranks it. The first lookup keeps the old 3 s budget
and the retries get 1.5 s each, because this runs synchronously while the manager mutex
is held; the retries are only ever paid on the path that used to fail outright. A lookup
error is retried too. When every answer is poison the error keeps the interceptor hint
and now says how many lookups ran, listing each distinct address once. The resolver sits
behind `lookupSpoofEndpointAddr` (`:329`) precisely so this is testable offline — the
positive paths of §17/§18 could never be exercised against live DNS here. Five new tests:
poisoned-then-real, stop at the first usable answer (exactly 1 call — the success path
does not pay for the budget), bounded failure after 3 lookups with the deduped message,
error-then-retry with accumulation, and the dedupe helper itself.

### 22.6 Fingerprint profiles measured: `chrome`'s decoy hello does not parse

`--profile` matrix, live, YouTube edge:

| profile | whole | split | what the server said |
|---|---|---|---|
| legacy | 0/5 killed | 5/5 `SERVERHELLO` | accepted |
| firefox | 0/5 (3 RST) | 3/3 `SERVERHELLO` @639 ms | accepted |
| chrome | 0/5 (2 injected, 3 RST) | **0/5, `ALERT`×5** (also `lead5` ×5) | `15 03 03 00 02 02 32` = **fatal, decode_error (50)** |

§20 saw `ALERT-50` and could not say which side produced it. The probe can: the DPI
**passed** these flows (a TLS alert is an endpoint answer, not a middlebox kill), and
Google's stack replied `decode_error` — it could not parse our synthetic Chrome
ClientHello, while the firefox and legacy shapes built by the same file parse and get a
ServerHello. A second stack agrees: against an unrelated Cloudflare edge
(104.16.132.229) the chrome hello drew `TIMEOUT`, while firefox and legacy both drew a
well-formed `alert 40 (handshake_failure)` — the normal answer for an SNI that edge does
not serve, i.e. those two parsed and chrome's did not.

Scope of the damage, stated precisely: `build_fake_client_hello` is used for the **decoy**
only (`engine-ffi/src/engine.rs:636`, random SNI from the pool); the client's real hello
is the application's own bytes, which this engine only segments. So a non-parseable
chrome decoy breaks no session. What it costs is the decoy's purpose: a hello no TLS
stack can parse absorbs no attention, and if a device ever starts rewarding "hello that
fails to decode" it is a liability rather than a disguise. Not fixed here, because a real
fix needs a known-good Chrome capture to diff against and §22 has no way to obtain one on
this network; the honest state is documented and one command measures it. **`legacy` and
`firefox` are now the two profiles measured end-to-end on this gateway.**

### 22.7 Verification and deployment

`cargo test --workspace` exit 0: 58 tests, 0 failures — `sni-core` 16 (11 previous plus
the 3 intercept-classification tests and 2 verdict-rule tests), `sni-spoof` bins 7 (the
probe suite), 13 config/mode/method integration cases, `t9` 6, `sni-engine` 3, rest as
before. The `t9` selftest gate is what caught the shape-signature assertion described in
§22.4, which is how that change was found to be incomplete rather than assumed correct.
`cargo clippy --workspace --all-targets -- -D warnings` clean. `rustfmt --check` clean for
every touched file (`core/src/fake_tcp.rs`, `core/src/tcp.rs`, `core/src/stats.rs`,
`core/src/selftest.rs`, `engine-ffi/src/engine.rs`, `engine-ffi/src/worker.rs`,
`cli/src/probe.rs`, `cli/src/main.rs`); the pre-existing drift in `core/src/picker.rs` and
`windivert/*` is still deliberately untouched (§16).

Go: `go vet` clean, `gofmt -l` empty for the touched files, `go test ./` fails only on the
six known pre-existing cases (five `geoip_test.go` no-database assumptions plus
`TestAppImportLinksEndToEnd`'s Windows TempDir cleanup). No bound method signature
changed; `sni_front.go`'s two `resolveSpoofEndpointIP` call sites are untouched and get
the retry for free.

Staged from the release build of this source: `sni-spoof.exe` (7 343 382 B) into all five
locations that carry it (`gui/build/bin`, `dist/portable/bin`,
`sni spoofing/target/release`, `sni spoofing/gui/build/bin`,
`sni spoofing/dist/portable`) and `sni_engine.dll` (7 120 792 B) into the four that carry
it. `nekogo.exe` (19 902 464 B) rebuilt with the project's own flags
(`-trimpath -H windowsgui -s -w -X main.appVersion=<VERSION>`); the frontend did not
change, so no re-embed was needed. The staged `gui/build/bin/sni-spoof.exe` was re-run
against the live gateway afterwards and reproduced the table: `whole` 0/2 injected-302,
`split` 2/2 `SERVERHELLO`.

**Still not measured:** this shell has no Admin token, so the engine still cannot load
WinDivert (`Access denied (code 5)`) and §22.3's verdict change has been proven at unit
level only, not on a live injected flow. The instrument that found the bug does not need
the driver, which is the point: the ranking fix is now testable without elevating
anything, and running NekoGo elevated remains the one step only the user can take. Also
unmeasured: whether `auto` converges to the splitting class in production — it cannot be
observed from here, but after this section it is at least measuring something true.

## 23. The SNI tab's Save button was refusing every save (2026-11-05)

Reported: "any changes I make in the SNI Spoofing tab are not saved, even though I
press the save button", with the request to drop the button and save every change
immediately. Both halves turned out to matter: the button was broken, and the card
around it was the only part of the tab still needing it.

### 23.1 Cause: three-way name drift, and a refusal that cost the whole card

The engine is the source of truth for these names
(`core/src/fake_tcp.rs` `SUPPORTED_METHODS`, `core/src/config.rs`
`SUPPORTED_FINGERPRINTS`). The Vue dropdown followed the engine; the Go validator
did not. `validSpoofMethods` still listed v2's `double_sni`/`delayed_retry` and had
never learned v3's `hello_split`, and `validSpoofFPS` knew only the versioned
fingerprints the engine had folded together (`chrome_120`/`chrome_124` → `chrome`,
`firefox_122`/`firefox_124` → `firefox`).

So choosing `Method = hello_split` — or either of the two fingerprints the dropdown
actually offers — made `App.SpoofSetConfig` return
`method must be one of auto, wrong_seq, …` *before* writing, and because the whole
`SNIConfig` is persisted in one `UpdateSNI`, that refusal discarded **every** knob in
the card, not just the refused one. The message went into `err`, which renders in the
banner at the top of the page (`SNIPanel.vue:19`) while the user was looking at the
button. Hand-typed numbers survived (they were the only values both sides agreed on),
which is how the persisted settings looked half-saved:
`"sniConfig":{"method":"auto","handshakeTimeout":2,"fakeDelay":0.5,"seqOverlap":1,"plan":"B"}`.

### 23.2 Fix

`gui/app.go:959` now mirrors the engine, and retired spellings migrate instead of
blocking a save written by an older build: `double_sni`/`delayed_retry` → `auto`
(mirroring `config::migrate`), `chrome_124` → `chrome`, `firefox_122` → `firefox`.
`custom` stays accepted — `tls.rs` maps it onto the legacy profile.

### 23.3 Autosave, which is what the rest of the tab already did

The Mode buttons, the chain toggle and both lists already saved on the spot; the
Engine options card was the outlier. Its Save button is gone and each control
persists when its value settles:

* Dropdowns save on selection. The five number boxes debounce 500 ms and gate
  client-side on the ranges `app.go` enforces — a half-typed `1` on the way to `12`
  must not round-trip, because it would be refused and the revert would fight the
  typing. Out of range means *wait*, not *fail*.
* Every write merges onto the last **saved** snapshot plus the stored plan, never
  onto the draft, so an unrelated half-typed box cannot ride along.
* On failure the field reverts to what is stored and the reason appears inside the
  card, next to the control that caused it.
* Mount is silent by construction: the equality guard compares the wire spelling of
  the value against the snapshot, so `loadConfig()` writing stored values back into
  the draft is not a change and cannot start a save loop.
* Leaving the tab flushes pending debounces instead of dropping the edit with the
  component.
* The store's `sniConfig` copy is updated on success, because the lists' save writes
  the whole settings struct and a stale copy there would undo the knob.

### 23.4 Tests, each negative-controlled against the bug it guards

Four new cases in `gui/sni_plan_test.go`:
`TestSpoofSetConfigAcceptsEveryOptionTheTabOffers` reads the option arrays out of
`SNIPanel.vue` and requires every value the tab can show to be savable — with the old
Go lists it fails with exactly the message the user was getting
(`the tab offers methods = "hello_split" but saving it fails: method must be one of …`).
`TestSpoofValidatorMirrorsTheEngine` reads `SUPPORTED_METHODS` and
`SUPPORTED_FINGERPRINTS` out of the Rust crate, so the "mirrors the Rust validator"
comment is now checked rather than aspirational (also negative-controlled), and
refuses to read a const it parsed fewer than 3 names from.
`TestSpoofSetConfigSurvivesRestart` re-opens the store directory and reads the options
back — the actual promise the tab makes, rather than only the in-memory copy.
`TestSpoofSetConfigMigratesRetiredSpellings` covers the migration and that a genuinely
unknown value still fails while naming what it accepts.

`go test ./` : the same six documented pre-existing failures, nothing new. `go vet`
and `gofmt` clean. `vue-tsc --noEmit && vite build` exit 0.

### 23.5 Browser-verified, and what that cannot show

Run against `vite dev` (no Wails runtime there, so writes fail by construction):
zero `Save` buttons in the tab; an out-of-range box reports
`maxConnections must be 10–2000 (empty = engine default) — not saved`; a valid value
clears that message and triggers exactly one save attempt; the status stays put across
repeated 2 s polls, so the watchers do not loop; nothing is written at mount.
A *successful* write is the one thing this cannot show without the running app — the
Go side of that path is covered by the restart test, and the user's next launch closes
it end to end.

Staged: `nekogo.exe` 19 904 512 B (md5 `ea6df04e…`) in `gui/build/bin` and
`dist/portable`, built over the `index-BW5XHvvS.js` bundle; a second build produced the
same hash, so the artifact is reproducible from this source. `gui/dist/portable/nekogo.exe`
(2026-10-18) is a stale tree and was left alone rather than overwritten.

## 24. "Google and YouTube don't work": the hello was never the problem (2026-11-05)

### 24.1 Measured before touching any code

The probe ran first, on the stored config's own fingerprint (`legacy`), `--reps=3`:

| target | edge dialled | whole | split | split_paced | lead5 |
|---|---|---|---|---|---|
| `www.youtube.com` | 216.239.38.119 | **0/3** (2 injected-302, 1 RST) | **3/3** @537 ms | 3/3 @644 ms | 3/3 @608 ms |
| `www.google.com` | 216.239.38.120 | 3/3 @395 ms | 3/3 @650 ms | 3/3 @460 ms | 3/3 @615 ms |

And the endpoint the active profile actually tunnels through — `y6-3cj.pages.dev`, a
Cloudflare Pages host, so the CF-fronting is real — dialed at the pinned edge
104.19.0.1: `whole` **0/2** (1 RST, 1 injected-302), `split` **2/2** @310 ms.

Two conclusions before any edit. Google traverses on *every* vector, so Google is not
blocked at the TLS layer at all. YouTube and the VPN endpoint die only on a whole
hello, and `split` — which is what the configured `hello_split` does — carries both.
The stored `sniConfig.method` was already `hello_split` (§23's autosave is what let it
be stored). So the ClientHello was not what broke Google and YouTube.

QUIC was eliminated the same way, by reading rather than guessing: `quic_mode`
defaults to `block` (`core/src/config.rs:117`), so YouTube's UDP/443 path is already
closed and the browser falls back to the TCP hello measured above.

### 24.2 The cause: an empty `{}` override deleted the tunnel's only resolver

The stored config carried `coreOverrides: {"dns": {}}` and no `dnsRemote`/`dnsDirect`.
`mergeCoreOverrides` skipped only *blank* text and then assigned whatever decoded, so
`{}` replaced the rendered DNS block wholesale. In TUN mode — which the Psiphon chain
forces — that block is the only resolver there is: `singbox_manager.go` renders
`singTunDefaultDNSBlock(dnsDetour)` exactly because "TUN leaves no system resolver to
fall back on (every query is hijacked into the module)". The override removed it, so
lookups went to the local resolver: the one §22.5 measured alternating real and
poisoned answers for the same name, and §17 showed redirecting UDP/53 to 8.8.8.8,
1.1.1.1 and 9.9.9.9. Blocked names came back as the interceptor's address, so Google
and YouTube died at DNS while the tunnel stayed up and unblocked sites kept working —
precisely the reported symptom, and precisely why better hello shaping could not fix it.

Reproduced as a failing test first: with TUN on the rendered block has 1 server, and
with `{"dns":{}}` merged it has 0 (`TestEmptySectionOverrideKeepsTheRenderedBlock`).

Fixed at three layers, because one is not enough: the merge now skips an override that
decodes to `{}` or `[]` (`isEmptyOverride`) since it cannot express an intent;
`SetCoreConfigSection` treats an empty submission as "no override" and clears the key
instead of storing a section-deleter; and `migrateEmptyCoreOverrides` drops them on
load, so the file already on the user's disk heals and the section editor shows the
rendered default again. A *real* override still replaces its section — asserted in the
same test. A corrupt entry is deliberately kept, so `mergeCoreOverrides` can still fail
fast instead of silently starting on half-merged config.

### 24.3 The second gap: a spoof connect had no elevation gate

`tunNeedsElevation` only fires when the rendered config carries a TUN inbound. A plain
spoof connect (no TUN, no chain) renders none, so the gate never ran and the engine
reached WinDivert unelevated: `Access is denied (code 5)`, every dial dead behind a UI
that still reported a connection. `tunStartHint` could not annotate it either — it is
gated on the same `tunRequiredFor`. Added `spoofNeedsElevation` plus a Connect
pre-flight that persists the requested profile and returns `spoofRequiresAdminMessage`,
and generalized the frontend matcher from the TUN-only sentence to the phrase both
messages share (`requires administrator rights`), so the existing RelaunchAsAdmin
handoff now covers a spoof connect too. Three tests; negative-controlled — deleting the
gate makes `TestConnectSpoofWithoutAdminRefusesAndPersistsTarget` fail with
`Connect = nil`. It also caught a regression in the loopback-rescue test, which connects
through a converted spoof profile and now declares its elevation side explicitly.

### 24.4 New method `ext_split` — RETRACTED the same day, see §25.1

`hello_split` cuts inside hostname *labels*, which defeats a matcher that greps a
segment for a hostname string. `ext_split` adds one cut strictly inside the SNI
extension's own nine-byte header — type, two length fields, name type — so the leading
segment stops in the middle of a length and **no segment holds a parseable
`server_name` extension at all**. That is the stronger guarantee: a DPI that walks TLS
structure inside a segment, rather than searching for a string, finds nothing to read.

`find_sni_ext` / `sni_extension_span` locate the header (and `extract_sni_from_hello`
now delegates to the same walk, so the two cannot disagree); `hello_split_points_ext`
adds the cut to the randomised label cuts; `split_hello_ext` segments through a shared
`segment_at`, so the seq / PSH / ident rules cannot drift between the two splitters.
`BypassMethod::ExtSplit` is decoyless like `SplitHello` (`plan_decoy` false,
`plan_fake` returns no segments), joins `SUPPORTED_METHODS` / `REAL_METHODS`, gets
`Strategy::ext_split_only()` and enters `auto`'s pool — now 16 strategies, the two
contract tests in `cli/tests/t9_v3_methods.rs` updated deliberately rather than
silently. The engine picks the planner from the strategy at the `NeedSplit` arm. Cost:
one extra segment (up to `MAX_HELLO_CUTS + 1` points). No pacing, no new thread, and
nothing added to the capture path that could stall it.

Measured live, `--reps=3`, `legacy`:

| target | whole | split | **ext_split** |
|---|---|---|---|
| `www.youtube.com` @216.239.38.119 | 0/3 | 3/3 @243 ms | **3/3** @552 ms |
| `y6-3cj.pages.dev` @104.19.0.1 | 0/3 (2 RST, 1 injected) | 3/3 @252 ms | **3/3** @260 ms |
| `www.google.com` @216.239.38.120 | 3/3 @153 ms | 3/3 @175 ms | **3/3** @275 ms |

`ext_split` is **not** made the default, and the latency column is the reason: the
extra segment cost YouTube 552 ms against `split`'s 243 ms in the same run, while on
the VPN endpoint it cost 8 ms. Both shapes traverse 100% here, so the choice is about
which DPI class you expect, not about whether it works — and this network's own noise
is larger than the gap (YouTube's `split` measured 537 ms in §24.1 and 243 ms an hour
later). Pick `ext_split` against a structure-parsing matcher or when a stable byte
shape is a concern; leave `hello_split` or `auto` otherwise. `auto` now ranks both on
outcomes that §22.3 made honest.

The limit, stated plainly: segmentation of any kind only defeats a DPI that does not
reassemble the stream. §22.2 proved this one does not (that is why `lead5` traverses).
Against a true reassembler no client-side cut helps — that needs a real proxy (the
chain) or ECH. `ext_split` raises the bar *within* the non-reassembling class; it does
not change the class, and no method here beats the destination-prefix block §22.2
measured on the Meta range.

### 24.5 Verification and staging

Rust: `cargo test --workspace` exit 0, **62 tests** (was 58; +4 `ext_split`), 0
failures; `cargo clippy --workspace --all-targets -- -D warnings` clean; `cargo fmt`
clean. The engine's own self-test gained `check_ext_split` — 8 draws per profile
asserting a cut inside the header, no parseable extension in any segment, no hostname
leak, contiguity, PSH on the tail and lossless reassembly — and `t9`'s end-to-end
selftest gate runs it green. Go: `go vet` clean, `gofmt` clean, `go test ./` back to
exactly the six documented pre-existing failures. Frontend: `vue-tsc --noEmit && vite
build` exit 0 (`index-CsW_b0kB.js`).

Staged from this source: `sni-spoof.exe` 7 354 210 B into all five locations that
carry it, `sni_engine.dll` 7 129 406 B into the three that carry it (one hash each,
verified), `nekogo.exe` 19 906 560 B into `gui/build/bin` and `dist/portable`. The
staged `gui/build/bin/sni-spoof.exe` was re-run live afterwards: `whole` 0/2
(injected + RST), `ext_split` 2/2 `SERVERHELLO`, and its `help` lists the new vector.

Still unmeasured, unchanged from §22.7: this shell has no Admin token, so the engine
cannot load WinDivert and nothing here has been observed on a live *injected* flow.
The DNS fix is the one that makes Google and YouTube resolve correctly inside the
tunnel; it is proven by test at render level, and the confirmation that only the
user's next elevated launch can give is that the sites load.



## 25. `ext_split` retracted, the pin went bad, and the scanner was measuring the DPI (2026-11-05, later)

### 25.1 `ext_split`: 0/8 on the user's own edge, removed

§24.4 reported 3/3 at 104.19.0.1 and 216.239.38.119 and recommended the method.
Re-measured against the endpoint the user actually tunnels through
(`y6-3cj.pages.dev`), `--reps=4`, `legacy`, with the segment sizes recorded:

| edge | `split` (= hello_split) | `ext_split` |
|---|---|---|
| 162.159.192.1 (their pin) | **4/4** SERVERHELLO, segs `[132,4,381]` | **0/4** (2 injected-302, 2 RST), segs `[18,108,3,6,382]` |
| 104.19.0.1 | 3/4 SERVERHELLO, segs `[130,7,380]` | **0/4 TIMEOUT**, segs `[44,78,6,9,380]` |

0/8 against 7/8, and the discriminator is in the sizes: the extra structure cut
takes the hello from 3 segments to 5 with 3- and 6-byte fragments, and this
gateway kills that. §24.4's 3/3 was one shape that happened to land well. A
method that fails where `hello_split` succeeds is worse than no method, so it was
removed completely — `BypassMethod::ExtSplit`, `hello_split_points_ext`,
`split_hello_ext`, `sni_extension_span`, `find_sni_ext`, `segment_at`,
`Strategy::ext_split_only`, the pool entry (16 → 15), the selftest check, the
probe vector, the Go validator entry and the Vue option. `cargo test --workspace`
is back to 58 passed / 0 failed and grep finds no trace of it in `core/`,
`engine-ffi/` or `cli/`.

The lesson is worth more than the code: **on this gateway, more fragmentation is
not more evasion.** Three segments with one small middle piece traverses; five
does not. Any future shape gets measured against a *blocked* endpoint at more
than one edge before it is offered in a dropdown.

### 25.2 The pinned edge went bad on its own

Same IP, same SNI, same shape, thirty minutes apart: 162.159.192.1 went from
4/4 SERVERHELLO @257–457 ms to **0/2** (injected-302 + RST). At the moment it was
dead, 104.19.0.1 answered 2/2 @680 ms, 104.25.0.10 2/2 @993 ms and 188.114.96.5
2/2 @752 ms — while 172.64.0.1 was CONNECT-FAIL 2/2, i.e. never reachable at all.
So "it stopped working" needs no code change to explain: one pinned Cloudflare
edge is a single point of failure, and the remedy is to re-scan and move the pin —
which is only useful if the scanner tells the truth.

### 25.3 The scanner was measuring the DPI, not the endpoint

`probeOnePair` did `tls.Client(raw, cfg)`, and Go writes the ClientHello as ONE
TCP segment — the `whole` vector, which this gateway kills for every blocked name
(0/2, 0/3, 0/5 across §22, §24 and §25). Every scan of a CF-fronted VPN domain
therefore reported failure and persisted `lastOk=false`: the DPI's verdict, stored
as the edge's. A live edge scanned dead, so nothing the scan recommended could be
trusted — that is the "too many bugs" experience.

Fixed in `gui/sni_scan.go`: `splitHelloConn` wraps the socket and writes the first
ClientHello record as several TCP writes, cut at label midpoints by
`splitClientHello` (`minSplitLabel` / `maxHelloCuts` mirror the engine's
MIN_SPLIT_LABEL / MAX_HELLO_CUTS), so a scan now measures the shape `hello_split`
actually sends. `sniHostSpan` is the same extension walk the engine uses. Only a
*copy* of the hello is retained: crypto/tls hands `Write` a buffer it reuses, and
the first version of this kept views into it — the full suite caught that as
"pieces do not reassemble" once other tests had run more handshakes through the
same code. A mid-length cut is used instead of the engine's random offset because
this is a measurement tool, and a stable shape makes two scans comparable.

`TestSplitHelloConnCutsARealClientHello` handshakes against a local TLS server
through the wrapper and asserts the captured real Go hello went out as ≥2 pieces,
that they reassemble byte-for-byte, and that no piece holds a whole 5+ byte label;
`TestSplitClientHelloOnlyCutsTheHello` proves application data and an SNI-less
hello pass through untouched. The pre-existing scan tests now run through the
splitter, so a splitter that broke handshakes would fail them.

### 25.4 What to actually run

Measured today on this gateway, not guessed:

* **Method `hello_split`** — 4/4 at a good edge. `auto` is usable now that §22.3
  made its scoreboard honest, but it spends attempts on decoy shapes that do not
  traverse here.
* **Fingerprint: irrelevant** for `hello_split`. It is decoyless (`plan_decoy`
  false), so no fake hello is ever built and the profile is never read. Leave
  `legacy`.
* **QUIC `block`** (the engine default) — keeps YouTube on TCP, where the split
  applies. **IP mode `ipv4`**, every number at engine default: none of them are
  read by a decoyless method.
* **Plan B with a pinned edge.** Plan A resolves over DoH, which §17 measured as
  blocked on this network.
* **Pin 104.19.0.1 or 188.114.96.5** — not 162.159.192.1 (dead at the second
  measurement) and not 172.64.0.1 (unreachable). Re-scan after §25.3 and move the
  pin when a connect fails.
* **Decoy SNI:** the stored pin is `www.cloudflre.com`, a typo of
  `www.cloudflare.com`. Harmless for `hello_split` (no decoy is sent, and
  Cloudflare answers a ServerHello even for a name it does not serve — measured),
  but it poisons every scan row and any decoy method, so fix it.
* **Profile:** the active `🇩🇪 DE @hamvex` → `y6-3cj.pages.dev` is a Cloudflare
  Pages host and measured 2/2 with `split` through a good edge, so it is a correct
  CF-fronting target; `NetAccount` (`subb.nrshop198.workers.dev`) is the same
  shape. The `www.ignitelimit.com` / `www.creationlong.org` /
  `www.multiplydose.com` profiles are not obviously CF-hosted — if an edge answers
  them `alert 40 (handshake_failure)` they are not behind Cloudflare and no
  shaping will front them.
* **Psiphon chain off** unless it is needed: it forces TUN, which forces elevation
  and adds a hop. A plain spoof connect is fewer things to fail.
* **Run elevated.** WinDivert needs it, and §24.3 now says so instead of failing
  cryptically behind a "connected" UI.

### 25.5 Proxies tab: the Aether tag is back

`ProxiesTable.vue` rendered an `active` pill and a `spoof` pill (`core === 2`) but
nothing for Aether, which is a *protocol*, not a core (`p.protocol === 'aether'`,
`gui/aether.go:482`). Added the matching pill and the `aether` row flag. One of the
45 stored profiles is an Aether profile, so it was invisible in the list.

### 25.6 Verification and staging

Rust: `cargo test --workspace` 58 passed / 0 failed, clippy `-D warnings` clean,
fmt clean. Go: `go vet` clean, `gofmt` clean, `go test ./` at exactly the six
documented pre-existing failures — it was seven while the §25.3 aliasing bug was
in, which is how that bug was found. Frontend: `vue-tsc --noEmit && vite build`
exit 0 (`index-Bw3muQ3o.js`).

Staged from this source: `sni-spoof.exe` 7 344 018 B (`08a87f33…`) into all four
carrying locations, `sni_engine.dll` 7 120 868 B (`c645991e…`) into the three that
carry it, `nekogo.exe` 19 915 264 B (`9c8ed813…`) into `gui/build/bin` and
`dist/portable` — one hash per group, and both staged exes' `help` list
`--vector=whole,split,split_paced,lead5` with no `ext_split`. The two subproject
staging dirs had kept the earlier verify-target build (7 343 458 B, `95d5b4df…`,
from `CARGO_TARGET_DIR=D:/tmp/sni-verify-target`) — same reverted source, different
build tree; reconciled to the in-tree `--release` output so a package step cannot
pick up a stale exe. `nekogo.exe` (23:16:44) is newer than every `.go` under `gui/`
and than the embedded `index-Bw3muQ3o.js` (23:16:41), so it carries §25.3 and §25.5. Unchanged from §22.7: no Admin
token here, so nothing has been observed on a live injected flow.

---

## 26. The stop path never ran, and two more GUI↔engine contracts were stale (2026-11-06)

Task: "debug some bugs in sni spoofing, sync nekogo with sni spoofing". Three contract
drifts found by reading both sides, each with a red test first.

### 26.1 `Stop()` could never ask the engine to shut down

`spoof_manager.go` called `proc.Signal(os.Interrupt)`, waited, then `proc.Kill()`. The
comment said Windows cannot deliver it. It is worse than "cannot": Go's own
`src/os/exec_windows.go` answers every non-`Kill` signal with
`syscall.Errno(syscall.EWINDOWS)` under `// TODO(rsc): Handle Interrupt too?`, so the call
failed instantly, `Kill` won every time, and `EngineHandle::shutdown` plus its
"[WARN] engine stopped" line have never run in any build. The console handler in
`cli/src/main.rs` is real but only fires for a human at a terminal, because NekoGo owns no
console to share.

Fix is the one channel a Windows parent always controls: stdin.

* engine: `spawn_stop_listener()` (`cli/src/main.rs`) reads lines and flips `STOP` on
  `stop` (`is_stop_request`: trim + case-insensitive, everything else ignored). Armed after
  "[INFO] engine RUNNING", so a stdin already at EOF at exec time cannot cut a startup short.
* GUI: `Start` wires `cmd.StdinPipe()` into `m.stdin`; `Stop` writes `stop\n`, closes it,
  then polls for the exit and only escalates to `Kill` after 3 s — and logs
  "did not answer the stop request; terminated" when it does, instead of pretending.

Deliberately **not** implemented: treating bare EOF as a stop order. It would also reap an
orphaned engine when the GUI is killed, but it turns `run -c cfg < NUL` into "the engine
exits immediately", which is a worse surprise than the problem it solves.

Proof: `TestSpoofManagerStopIsGraceful` waits for the mock's `mock graceful stop` line,
which the mock prints only after reading `stop` on stdin. Negative control: disabling the
write fails it (`no graceful-stop line`, 9.07 s — the grace window plus fallback). Rust side
covered by `stop_request_accepts_the_hosts_word_only`.

Also measured, and it changed nothing: whether `cmd.Wait()` can close the stdout pipe before
the scraper reads the child's last line. A standalone harness (drain-then-wait and
wait-first, both modes) saw the final line in both orderings, so the scrape/reaper split
stays as it is.

### 26.2 The stats rows the engine sorts are not the rows the GUI reads

`core/src/stats.rs` `Ranked` gained `latency_ms` (nullable EWMA) and `score`, and `ranked()`
sorts rows by `score` — "Same comparator `auto` uses, so the GUI shows exactly what the
engine is preferring". The Go mirror (`gui/types.go` `SpoofRanked`) still had only
key/ok/fail/rate, so the tab sorted by a number it threw away: a method at 100 % sitting
below a slower one was inexplicable.

Added both fields to `SpoofRanked`, `latency_ms: number | null` + `score: number` to the
hand-written TS types, a latency column and a score tooltip in `SNIPanel.vue`, and a latency
track in `theme.css`. The mock engine now emits two rows (one timed, one `null`) and
`TestSpoofManagerStartWithMockExe` asserts both survive the stdout scrape.

New guard so this class cannot recur silently:
`TestSpoofStatsMirrorTheEngineSnapshot` reads the field names out of `Ranked` and `Snapshot`
in stats.rs (honouring `#[serde(rename = "…")]`) and compares them with the Go struct tags
by reflection, in both directions. It went red on exactly `latency_ms` and `score`. `type`
is the one allowed exception, listed in the test with a reason: the scraper already knows a
line is stats by its `[STATS]` prefix.

### 26.3 The GUI accepted a fingerprint the engine does not have

`validSpoofFPS` listed `custom`; `SUPPORTED_FINGERPRINTS` lists three names and `tls.rs`
maps `custom` onto `Legacy`. So `custom` validated, got rendered into `TLS_FINGERPRINT`,
was canonicalised to `legacy` by the engine, and left the tab showing a fingerprint that
never existed. The Vue dropdown was already right — the drift was one-sided in Go.

`custom` is now a retired spelling (`retiredSpoofFPS["custom"] = "legacy"`), which matters
because the alternative — rejecting it — discards the whole card, the §23 failure.
`TestSpoofSetConfigMigratesRetiredSpellings` now proves the migration keeps method,
`handshakeTimeout` and `quicMode` intact, and that a genuinely unknown fingerprint
(`safari`) still fails without rewriting the card.

The mirror test only ever checked engine→GUI ("every engine name is savable"), which is why
`custom` survived it. It now checks GUI→engine too, and covers `SUPPORTED_QUIC_MODES` and
`SUPPORTED_IP_MODES`, not just methods and fingerprints.

### 26.4 Checked and rejected: the stats cadence

The engine emits `[STATS]` every 5 s while the tab polls every 2 s, which reads like a bug.
It is not one: `SpoofManager.GetStats` returns a pointer to the zero `SpoofStats`, never
null, so before the first snapshot the card shows real zeros rather than "No stats yet", and
an immediate first snapshot would show the same zeros. Left alone.

### 26.5 One test was sensitive to a forward-slash TMPDIR

`TestChainPsiphonDirs` compared `filepath.Dir(d)` with `base` as raw strings. Under
`GOTMPDIR=D:/tmp/gotmp` (this shell), `t.TempDir()` keeps `/` where `MkdirTemp` returns
`\`, so it reported a chain dir as outside the work dir created in it — a seventh failure
next to the six documented ones. Both sides are `filepath.Clean`ed now; the failure
disappears with the fix and nothing else changed.

### 26.6 Verification and staging

Rust: `cargo test --workspace` all targets green (cli 8 unit incl. the new one, 16 core, 13
engine, plus the integration files), `cargo clippy -p sni-spoof -p sni-core -p sni-engine
--all-targets -- -D warnings` clean, `cargo fmt` clean for those three packages — note the
pre-existing drift in `windivert/examples/sniff.rs` (untouched since 2026-09-15, §16
policy) means `cargo fmt --all --check` is not clean workspace-wide.

Go: `go vet` exit 0, `gofmt -l` empty, `go test ./` back to exactly the six documented
pre-existing failures (five geoip no-DB + `TestAppImportLinksEndToEnd` TempDir).
Frontend: `npm run build` (`vue-tsc --noEmit && vite build`) exit 0 → `index-D3pYNPUF.js`,
`index-Qqa0qbGW.css`. `wails3 generate bindings -ts` re-run and byte-identical to what was
already committed, so the generated models match the Go structs.

Staged: `sni-spoof.exe` 7 351 732 B `edd043b5…` in all five locations, `sni_engine.dll`
unchanged at `c645991e…` (no engine-crate source moved), `nekogo.exe` 19 916 800 B
`2a58e3a6…` in `gui/build/bin` + `dist/portable`. The new exe embeds `index-D3pYNPUF.js` /
`index-Qqa0qbGW.css` and no trace of the previous `index-Bw3muQ3o.js`.

**Still unmeasured:** as in §22.7, no Admin token here, so WinDivert cannot load and
`run -c` cannot start. The stop path is therefore proven end-to-end against the mock
engine, not against the real one: what is verified is that the request travels the pipe and
the child acts on it, and that the engine's own word matcher is exactly `stop`. The last
mile — `[WARN] engine stopped` appearing in a live run's log — is one Admin relaunch away
and belongs to the user.
