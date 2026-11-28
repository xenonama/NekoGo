# NekoGo patterns — the PattN shape, translated to Go + Wails

PattN is the reference architecture: one process group per connection,
one owner per child process, honest failure reporting, and state that is
only published after a probe succeeds. NekoGo already has most of the
structure; this document fixes the pattern names and the file each one
belongs to so new code follows the same shape instead of inventing a
parallel one.

Sources: `PATTN_BACKEND_PATTERNS.md`, `PATTN_DEEP_DIVE_PATTERNS.md`,
`PATTN_VS_NEKOGO_GAP_ANALYSIS.md`. This is the Go/Wails projection of
those findings, not a feature port.

## 0. Translation rules

| PattN (C#) | NekoGo (Go + Wails) |
|---|---|
| `CoreManager` turn + generation | `CoreManager.epoch` + per-manager `startEpoch` |
| `SessionEnded` event | `EngineExit` callback (`SetExitHandler`) |
| `AetherCoreManager.WatchAsync` | manager `watchChild` (owns `cmd.Wait`) |
| Immutable `CoreConfigContext` | pure `Build*Config` + an explicit render input (later phase) |
| `CouldNotStart` vs `StoppedMessage` | `EngineExit.Reason` + last output lines |
| pid + start-ticks ledger | future `gui/proc_ledger.go` (not yet) |
| Job object kill-on-close | future Windows job object (not yet) |
| Localized `ResUI` keys | error category + code on the returned error (later) |

Rules that apply to every pattern below:

1. **One owner per child.** The goroutine that calls `cmd.Wait` is the
   only one allowed to reap it; everyone else signals (`Kill`) and
   waits on a `done` channel, never on `cmd.Wait`.
2. **Publish after proof.** `running = true` is committed under the lock
   only after the listener/RPC probe answered and the epoch is unchanged.
3. **Failure is a value, not a log line.** A child death is reported as a
   struct (`EngineExit`) that carries what died, why, and its last words.
4. **Group semantics.** A connection is a group: aether engine + main
   core (+ system proxy). A member dying takes the group down; the user
   never has to disconnect a corpse.
5. **No silent retry.** Surface the crash; the engine's own
   `--quick-reconnect` handles reconnects.
6. **Pure builders.** `BuildSingBoxConfig` / `BuildXrayConfig` stay pure
   and testable; side effects belong to the manager.

## 1. Engine exit contract (adopt — phase 1)

PattN: `AetherCoreManager.WatchAsync` → `SessionEnded` →
`CoreManager.OnAetherSessionEnded` → `EndIfCurrentAsync` (stop the
group, publish state).

NekoGo implementation:

- `gui/engine_exit.go` — `EngineExit{Engine, PID, Err, StartedAt,
  Uptime, LastLines}`, `lineRing` (bounded, mutex-guarded), `Summary()`.
- Each manager (`aether_manager.go`, `singbox_manager.go`,
  `xray_manager.go`) owns a `lineRing` and an `exitHandler`.
  `SetExitHandler(func(EngineExit))` is called by `CoreManager`.
- `watchChild` notifies only when the child was the **committed**
  generation (`m.cmd == cmd && m.running`); a `Stop` clears `running`
  first, so user stops and start rollbacks are never reported as crashes.
- `CoreManager.handleEngineExit` flips `running=false` immediately,
  publishes the fault to `App` (`noteError` + log + auto-proxy revert),
  then tears the surviving members down before returning. A concurrent
  `Start` waits on the same lock, so it cannot begin while a half-dead
  group still holds ports.
- `App.Status()` gains `lastError`; the connection store mirrors it and
  `ConnectionCard` renders it. Console already shows the same line.

The contract is one-way: managers never call `CoreManager` directly.

## 2. Crash vs failed start, with last words (adopt — phase 1)

PattN: `CouldNotStart` (1 s window / DLL-not-found) vs
`StoppedMessage` (ran, then ended), quoting the core's own output.

NekoGo implementation:

- Aether `Start` races the warmup probe against the child's exit channel
  instead of waiting the full 30 s: `waitSocksOrExit(listen, timeout,
  exited)`.
- A start-window death returns `aether engine exited during startup:
  <err> (last output: "...")` immediately.
- A committed death returns the same summary through `EngineExit`.
- The ring keeps the last 12 non-empty lines (bounded, trimmed), so the
  report is useful without dumping the whole stream.

## 3. Environment sanitisation + port pre-flight (adopt — phase 2)

PattN: `AetherProcess` strips inherited `AETHER_*` / `RUST_LOG` before
setting its own; `AetherCoreLoad.PortsProblem` probes TCP **and** UDP
for every reserved port before starting.

NekoGo implementation:

- `sanitizeChildEnv(base, prefixes, exact)` in `logx.go` (or a small
  `childenv.go`): drop every `AETHER_*` and `RUST_LOG` entry, then add
  the app's own. `aetherEnv` is the only user for now.
- `freePortTCPAndUDP()` in `singbox_manager.go`: bind TCP :0, derive the
  port, verify the same port is free for UDP, retry a bounded number of
  times. Aether uses it through `aetherFreePortHook` or directly.
- Pre-flight stays honest: a taken fixed port is still an error, never
  silently remapped.

## 4. TUN self-address drop (adopt — phase 3)

PattN: `SingboxRoutingService` inserts a `reject` / `method: drop` rule
for the TUN's own addresses so `auto_route` cannot loop a packet back
into the outbound and pin a CPU core.

NekoGo implementation:

- In `BuildSingBoxConfig`, when `settings.TunEnabled`, prepend
  `{"ip_cidr": ["172.19.0.1/32"], "action": "reject", "method": "drop"}`
  to `route.rules` (before the DNS rule).
- The TUN address stays the single constant used by the inbound, so the
  rule cannot drift from it.
- Unit-test both states (TUN on/off).

## 5. Process ownership and leftovers (adopt — later)

PattN: `pid + startTicks (+ bootTicks)` ledger; reaping cannot kill an
innocent PID-reused process.

NekoGo today: `gui/orphans.go` sweeps `xray.exe` / `nekobox_core.exe` /
`sing-box.exe` by image name + app dir + dead parent. It does not know
`aether.exe`, and a parent-PID check cannot prove ownership after PID
reuse.

Later shape:

- `gui/proc_ledger.go`: `<pid> <utc-start-ticks>` written beside the
  work dir at spawn, read at startup, deleted on clean stop.
- Add `aether.exe` to the sweep only through the ledger, not by name.
- Windows job object (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) is the
  belt-and-braces alternative; one mechanism, not both.

## 6. Immutable render context (adopt — later, narrow)

PattN freezes profile + settings + ports + TUN flag into one
`CoreConfigContext`. NekoGo's builders read live settings, so a TUN
toggle during a start can render a config that disagrees with the
spawned mode.

Later shape: a small `RenderContext` value built once in `Start` and
passed to both builders. Not a feature port; only the race fix.

## 7. Config storage and migrations (version, not SQLite)

PattN uses SQLite + per-row `ConfigVersion`. NekoGo's single atomic
`config.json` is the right storage for a desktop app.

Adopt only the discipline: a `schemaVersion` on the document plus an
ordered list of idempotent `migrate{Version}` steps run at load. Do not
adopt SQLite.

## 8. Subscriptions (later)

Adopt from PattN:

- per-feed `AutoUpdateInterval`;
- conditional GET (`If-Modified-Since` / `ETag`, handle 304);
- delete-then-re-add per feed with the active profile re-selected.

Keep NekoGo's quota parser, dedupe key and delete-with-profiles: they
are ahead of PattN.

## 9. Observability and routing defaults (later)

- Connection rows: add `chains`, `rule`, `rulePayload`, `start` from the
  same Clash payload the core already returns.
- Seed a baseline route (private/loopback direct, small ad/tracker block,
  rest proxy) merged before user overrides.
- Error surface: add a category (`binary | port | permission | config |
  network`) so the UI can offer "Reload as admin" without string
  matching.
- Logs: daily file name + startup sweep of files older than N days.

## 10. Explicitly not adopted

| PattN pattern | Why not |
|---|---|
| Automatic restart loop | hides the crash §1 exists to surface |
| SQLite for profiles | one JSON file is easier to back up (keep versioning only) |
| Derived fixed port window | NekoGo's MixedPort / MixedPort+1 is already correct |
| Config diffing / hot reload | wholesale restart is honest and simpler |
| Privileged service / privilege drop | both apps accept a permanently elevated GUI |
| FakeIP by default | changes what applications resolve; needs a real TUN test first |

## 11. Phase order (what lands now)

1. **Engine exit contract + crash classification** (sections 1–2):
   `engine_exit.go`, three managers, `core_manager.go`, `app.go`,
   connection store + `ConnectionCard`.
2. **Env sanitisation + TCP/UDP port pre-flight** (section 3).
3. **TUN self-address drop** (section 4).

Every phase ends with `go build ./...`, `go vet ./...`, `go test ./...`
in `gui/`, plus a frontend `npm run build` when a bound field changes.
