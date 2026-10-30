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
| `FAKE_SNIS` | array of strings | `[]` → `FAKE_SNI` fallback; lowercased, dot-stripped, deduped | 1–200 entries; each valid hostname, ≤219 chars (**required non-empty**) |
| `FAKE_SNI` | string (legacy) | none | same SNI rules |
| `BYPASS_METHOD` | string | `"auto"` | `auto, wrong_seq, wrong_seq_ttl, split_seq, fragmented, padding, delayed_retry, double_sni, hostfakesplit, fakedsplit` |
| `HANDSHAKE_TIMEOUT` | f64 | `2.0` | 0.5–10.0 s |
| `MAX_CONNECTIONS` | usize | `200` | 10–2000 |
| `FAKE_DELAY` | f64 | `0.001` | 0.0–5.0 s |
| `SEQ_OVERLAP` | u8 | `3` | 0–16 |
| `TLS_FINGERPRINT` | string (lowercased) | `"legacy"` | `legacy, chrome_120, chrome_124, firefox_122, firefox_124, custom` |
| `PADDING_SIZE` | u8 | `0` | 0–128 |
| `QUIC_MODE` | string (lowercased) | `"block"` | `block, spoof, passthrough` |
| `MODE` | string | `"SNI Only"` | `"SNI Only"` only (`"Trojan + Xray"` removed 2026-10-15: fails validation, see §10) |
| `IP_MODE` (accepts `ip_mode`) | string (lowercased) | `"ipv4"` | `ipv4, ipv6, both` |
| `TRAY_ENABLED` (accepts `tray_enabled`) | bool (tolerant: bool, `true/1/yes/y/on` strings, numbers) | `true` | bool |
| `PROBE_TRIES` | u32 | `2` | 1–10 |
| `PROBE_TIMEOUT` | f64 | `3.0` | 0.5–10.0 s |
| `SOCKS5_PORT` | u16 | `10808` | ≠ 0, ≠ `LISTEN_PORT`, ≠ `HTTP_PORT` |
| `HTTP_PORT` | u16 | `10809` | ≠ 0, ≠ `LISTEN_PORT`, ≠ `SOCKS5_PORT` |

No new config keys were added.

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

This is `sni_core::stats::Snapshot` (`core/src/stats.rs:54-69`) serialized with `serde_json::to_string`, produced by the existing shared `Arc<Stats>` passed to `EngineHandle::start` — the same struct the FFI layer polls. `methods` is the top-3 ranked methods (`{key,ok,fail,rate}`); `best_endpoint`/`best_method` are the top-ranked keys (empty when no data).

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
