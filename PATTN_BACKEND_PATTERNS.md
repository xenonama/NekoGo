# PattN Backend Patterns — reference for NekoGo

(Read-only audit. No NekoGo file was changed. No build, test or core
binary was run; everything below comes from reading source. The audited
tree is PattN — the Windows C# fork of v2rayN at `PattN/v2rayN/` — not
the Android Kotlin PattNG. The two forks share the same backend
patterns, so this reference applies to both.)

## 0. Source locations

- **NekoGo root:** `C:\Users\pc\Desktop\nekobox-main`
  (Go module `gui/`, the libbox/Thrift core server in `core/server/`,
  Wails frontend in `gui/frontend/src/`).
- **PattN source root:** `C:\Users\pc\Desktop\nekobox-main\PattN\v2rayN`
  (the checkout starts at `...\PattN`, which holds only packaging
  scripts and the solution directory). The backend logic lives in
  `ServiceLib/`; there is also a headless HTTP backend in
  `v2rayN.WebAPI/` (the closest analogue of NekoGo's Go backend) and two
  desktop frontends (`v2rayN/` WPF, `v2rayN.Desktop/` Avalonia).
- **PattN variant:** **desktop only — C# / .NET 8 / WPF.**
  The brief expected an Android/Kotlin tree; it is not there. Under
  `PattN` there are **0** `AndroidManifest.xml`, **0** `build.gradle*`,
  **0** `*.kt` files and **544** `*.cs` files. The package
  `com.v2ray.ang` does not appear. Every "PattNG" reference you will see
  in the code is a comment describing the *upstream Android app the
  logic was ported from* (e.g. `AetherCoreManager.kt`,
  `AetherCoreManager.adopt`); the implementation being audited is the
  C# port in this tree. All line numbers below are for that C# code.
- NekoGo is itself already a partial port of that PattNG logic
  (`gui/aether*.go` cites PattNG design/critique identifiers), so several
  areas are comparisons between two ports of the same idea rather than
  independent designs.

Path prefix used below: `P = PattN/v2rayN/ServiceLib`.

---

## 1. Core process lifecycle

### PattN approach

- `P/Manager/CoreManager.cs:146` (`LoadCore`), `:169`
  (`LoadCoreInTurn`), `:284` (`CoreStop`), `:295` (`CoreStopInTurn`),
  `:333` (`CoreStart`), `:459` (`RunProcess`), `:495`
  (`RunProcessNormal`); serialization helper `P/Aether/AetherCoreLoad.cs:405`
  (`AetherCoreTurns`) with `RunAsync` `:414`, `NewGeneration` `:428`,
  `EndIfCurrentAsync` `:437`.
- Every load or stop runs **inside a turn** (one `SemaphoreSlim`) and a
  **generation counter** is bumped whenever the cores are replaced or
  stopped. The session core records the generation it was started with,
  so a death of an *old* core can never stop the *new* cores. Start is:
  generate config → pre-flight Aether (`BeforeStopAsync`) → stop old →
  pre-flight ports (`BeforeXrayAsync`) → start main core → wait for its
  inbound → start Aether session (`AfterXrayAsync`).
- "Core started" for the main core is a weak 100 ms liveness check
  (`CoreManager.cs:495-521`): process still alive after `Task.Delay(100)`,
  otherwise `ResUI.FailedToRunCore` (`:520`). Readiness that matters (proxy port,
  Aether SOCKS listener) is waited for separately
  (`CoreManager.cs:373` `WaitForProxyPort`, `AetherCoreManager.cs:596`
  `AwaitListeningAsync`).
- "Core crashed": only the Aether session is watched. `WatchAsync`
  (`AetherCoreManager.cs:1370`) awaits the exit code and classifies it
  via `CouldNotStart` (`:1040`, DLL-not-found exit code
  `AetherProcess.cs:90`, or no output within the 1 s `StartWindow`).
  A real crash raises `SessionEnded` (`:455`), which
  `CoreManager.OnAetherSessionEnded` (`:595`) handles through
  `EndIfCurrentAsync`: user message, then `CoreStopInTurn` — Xray and the
  system proxy die with the engine ("all or nothing"). The WebAPI
  backend subscribes to that event (`v2rayN.WebAPI/Services/V2rayRuntime.cs:290`)
  and turns it into published runtime state: `OnAetherCoresStoppedAsync`
  (`:1216`) faults the current core state with the core's own words and
  publishes `"status"`, so HTTP clients learn about the death immediately
  instead of polling dead processes (`CoreManager.cs:29`, `:603`).
- Cleanup on exit: `P/Manager/AppManager.cs:122` (`AppExitAsync`)
  reverts the system proxy, saves, calls `CoreManager.CoreStop()` and
  `AetherCoreManager.StopAllAsync()` (`AetherCoreManager.cs:891`).
  Belt-and-braces on Windows: every child handle is put in a job object
  with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`
  (`P/Services/WindowsJobService.cs:4`, `AddProcess` `:46`; called from
  `CoreManager.AddProcessJob` `:527` and `:541` for the Aether core).
  On Linux/macOS, leftover cores are reaped from pid files instead
  (see §5).

### NekoGo today

- `gui/core_manager.go:144` (`Start`), `:311` (`Stop`), `:389`
  (`stopLocked`), engine attach points `:249` / `:264`. NekoGo already has
  a generation mechanism — the `epoch` field and its
  "commit only if unchanged" pattern (`core_manager.go:15-30`,
  `:144`, `:311`) — which covers the "a stop raced a start" case that
  PattN's turns cover, but it does **not** serialise cross-engine
  start/stop: each manager locks only itself.
- Engine processes: `gui/singbox_manager.go:232` (`Start`, RPC socket
  probe with an `exited` channel so an instant crash fails fast), `:397`
  (`Stop`); `gui/xray_manager.go:154` / `:346`; `gui/aether_manager.go:147`
  (`Start`, spawn + `answersSocks` warm-up), `:243` (`Stop`).
- "Core crashed": `gui/singbox_manager.go:361` (`watchChild`) and
  `gui/aether_manager.go:231` (`watchChild`) only flip their own
  `running` flag and log a warning. Nothing tells the UI and nothing
  stops the other leg of a two-process connection.
- Cleanup on exit: `gui/app.go:2139` (`Shutdown`) → `cores.Stop()`;
  startup orphan sweep `gui/main.go:68` → `gui/orphans.go:47`
  (`cleanupOrphanCores`) with an ownership test in `:88`
  (`selectOrphans`: image name at `:43`, executable under the app dir,
  parent dead and not us). Windows only.

### Adopt?

**Yes (partially).** NekoGo's `epoch` already equals PattN's generation
for a single core, but NekoGo should adopt the "engine death ends the
connection group" contract (`CoreManager.OnAetherSessionEnded`
`:595` + `AetherCoreManager.WatchAsync` `:1370`), because today an
`aether.exe` crash leaves sing-box/Xray plus the system proxy pointing at
a dead SOCKS port with only a log line to show for it.

---

## 2. Config generation

### PattN approach

- Entry: `P/Handler/CoreConfigHandler.cs:10` (`GenerateClientConfig`)
  → either `P/Services/CoreConfig/Singbox/CoreConfigSingboxService.cs:13`
  (`GenerateClientConfigContent`) or the Xray equivalent.
  Custom profiles are copied verbatim (`CoreConfigHandler.cs:44`).
- Before any rendering, `P/Handler/Builder/CoreConfigContextBuilder.cs:41`
  (`Build`) freezes an **immutable snapshot** — node, run core type,
  routing item, DNS item, TUN flag, per-OS flags, IPv6 reachability,
  protected domains, resolved proxy map — into
  `P/Models/CoreConfigs/CoreConfigContext.cs:3`. Both the generator and
  the launcher read that same snapshot, so a settings change during a
  reload cannot desync the JSON from the ports/TUN mode it was built for.
  `Build` also refuses a context whose profile or routing targets cannot
  be built, and returns errors/warnings (`:41-207`).
  `BuildAll` (`:209`) additionally builds the optional pre-socks context
  used for TUN protection or chained proxies.
- sing-box rendering is a fixed pipeline (`CoreConfigSingboxService.cs:13-58`):
  `GenLog` → `GenInbounds` → `GenOutbounds` → `GenRouting` → `GenDns` →
  `GenExperimental` → `ConvertGeo2Ruleset`.
  - TUN: `P/Services/CoreConfig/Singbox/SingboxInboundService.cs:5`
    (`GenInbounds`), TUN fields set at `:60-83` (`interface_name`, `mtu`,
    `auto_route`, `strict_route`, `stack`, `route_exclude_address`), IPv6
    decision in `TunTakesIPv6` `:96`.
  - Routing: `P/Services/CoreConfig/Singbox/SingboxRoutingService.cs:5`
    (`GenRouting`). Loop guard for the TUN's own addresses (drop, not
    reject) `:41-72`; `hijack-dns` per process path `:74-86`; ICMP policy
    `:88-110`; sniffing `:112-121` (one `action: "sniff"` rule plus a
    logical `hijack-dns` rule for port 53 or `protocol: dns`); final TLS
    fragment `:121-130`.
  - DNS: `P/Services/CoreConfig/Singbox/SingboxDnsService.cs:5` (`GenDns`),
    servers `:32`, FakeIP server `:123`, DNS rules `:152`, FakeIP filter
    `:260`.
- Merging subscriptions and per-profile overrides is done **outside** the
  generator: `P/Handler/ConfigHandler.cs:2160` (`ApplySubOverrides`) rewrites
  address/port of imported profiles from the subscription's overrides;
  `CoreConfigContextBuilder.ResolveNodeAsync` (`:286`) wraps a profile in
  its subscription's prev/next chain (`BuildSubscriptionChainNodeAsync`,
  `:328`) and registers every node it may route to. A chain that names a
  hop no profile has is refused rather than silently shortened.
- Non-negotiable detail worth copying: `CanListen` / `PortsProblem`
  pre-flight (`P/Aether/AetherCoreLoad.cs:163`, `:215`) before anything is
  started — see §5.

### NekoGo today

- `gui/singbox_manager.go:844` (`BuildSingBoxConfig`) and
  `gui/xray_manager.go:672` (`BuildXrayConfig`). Both are single functions
  that read the **live** `*Profile` and `Settings` at call time
  (a copy is taken only for global transport overrides, `:844-847` and
  `:672-675`).
  - sing-box: one mixed inbound, one `proxy` outbound, `route.rules` is
    just the DNS rule (`:893-895`), optional `auto_detect_interface`
    (`:904-906`), optional TUN inbound appended at `:941-948`
    (`address 172.19.0.1/24`, `mtu 1350`, `auto_route true`,
    `strict_route false`, stack normalised at `:942`), optional DNS block
    (`:924-936`, `:1049-1075`), section/full JSON overrides merged at `:949`.
  - Xray: HTTP + SOCKS inbounds with    `sniffing` (`xray_manager.go:725-737`),
    `routing.rules` contains only the API rule (`:744-756`); no TUN.
- There is **no** instance of the "build a snapshot, then render from it"
  pattern; the manager renders, spawns, probes and publishes in one call.
- Profile/subscription overrides reach the renderer only through the
  profile record itself (`gui/import.go`, `gui/config.go:519`) and
  `Settings.CoreOverrides`; there is no routing-rule/chain resolution.

### Adopt?

**Yes, the snapshot — not the feature set.** Adopting
`CoreConfigContextBuilder.Build` as a small `RenderContext` value passed
to `BuildSingBoxConfig`/`BuildXrayConfig` and to the launcher would close
a real race NekoGo has (TUN toggled while a start is in flight renders a
config that disagrees with the mode the process was launched in); copying
the routing/DNS generator stack is a feature port, not a pattern, and is
out of scope for this reference.

---

## 3. Profile storage and migration

### PattN approach

- Settings: one JSON document (`Config`), loaded by
  `P/Handler/ConfigHandler.cs:19` (`LoadConfig`). Missing sections are
  repaired by a long series of null-coalescing defaults (`:33-210`), so a
  partial/older file always yields a complete object. Saved via
  `SaveConfig` (`:218`) which writes `<file>_temp` then
  `File.Move(temp, resPath, true)` — atomic replace.
- Profiles: **SQLite** (`P/Helper/SqliteHelper.cs`), one `ProfileItem` row
  per profile, with a `ConfigVersion` column.
- Migration: `P/Manager/AppManager.cs:350` (`MigrateProfileExtra`) is the
  entry point, called at startup from `InitComponents` (`:99`, call at
  `:109`, synchronously via `Task.Run(...).Wait()`).
  - `MigrateProfileExtraV2ToV3` (`:359`) pages the table 100 rows at a
    time with `WHERE ConfigVersion < 3`, and advances the offset by the
    number of **failed** rows so successful rows are skipped on the next
    page (`:375-385`).
  - `MigrateProfileTransportV3ToV4` (`:386`) moves legacy transport
    columns into a typed `TransportExtra`, guarded per row and logged
    (`:475`, `:488`).
  - New/edited profiles are stamped with the current version on write:
    `ConfigHandler.cs:1291` (`AddServerCommon`, `ConfigVersion = 4`).
- Result: migration is idempotent, restartable, version-driven, and never
  blocks on a single bad row.

### NekoGo today

- One JSON document for both (`gui/config.go:15` `fileConfig`,
  `:67` `NewConfigStore`), path `%APPDATA%/NekoGo/config.json`.
- Writes are already atomic: `saveLocked` (`:226`) writes `config.json.tmp`
  then `os.Rename`.
- Migrations are **ad-hoc and one-shot**, keyed on field emptiness, run
  inline at load: `migrateLegacyAetherPath` (`:36`), the SNI
  default seeding inside `NewConfigStore`, `migrateSpoofProfiles` (`:149`,
  guarded by the `SpoofAutoDetectReverted` settings flag `:120-146`), and
  the whole-file copy from the old app-data dir
  `migrateFromOldAppDataDir` (`:196`).
- There is no schema/record version field, so a future migration has no
  way to know whether it ran.

### Adopt?

**Yes, the versioning discipline; no, the storage engine.** A
`schemaVersion` on the document plus a small ordered migration list
(each step idempotent, each step logged) is a low-risk change to
`gui/config.go:67-146`, whereas SQLite would trade a 40 MB native
dependency for behaviour NekoGo does not need.

---

## 4. Subscription handling

### PattN approach

- Fetch/parse/merge: `P/Handler/SubscriptionHandler.cs:5`
  (`UpdateProcess`). It iterates every subscription (`IsValidSubscription`
  `:60`), skips disabled ones, builds a `DownloadService` with the
  subscription's request headers (`:83`), downloads, and falls back to a
  direct connection if the proxied fetch returned nothing
  (`:102-113`). `MoreUrl` feeds are concatenated and Base64-sniffed
  (`:158-194`). Success/failure is reported per subscription through
  `updateFunc` (`:5`, `:58`, `:70`).
- Import/replace: `ProcessDownloadResult` (`:197`) →
  `P/Handler/ConfigHandler.cs:2185` (`AddBatchServers`). For a subscription
  feed the  old rows of that subscription are **removed first**
  (`:2193-2197`, `RemoveServersViaSubid`), then parsing is attempted in
  several formats until one yields rows (Base64 → raw → SIP008 →
  WireGuard, `:2198-2224`). The previously active profile is remembered by
  `IndexId` and re-pointed afterwards, so a refresh does not drop the
  user's selection.
- Dedupe: `ConfigHandler.cs:1246` (`DedupServerList`) compares **content**
  via `CompareProfileItem` (`:1348`), with direction controlled by
  `GuiItem.KeepOlderDedupl` (keep first vs keep last).
- userinfo (upload/download/expiry): **not found.** No
  `subscription-userinfo` parsing exists anywhere in the ServiceLib tree;
  the quota header is ignored by this codebase.

### NekoGo today

- Fetch and header parsing already exist and are ahead of PattN:
  `gui/import.go:451` (`fetchSubscription`), `gui/import.go:60`
  (`parseSubUserInfo`), `gui/import.go:50` (`SubUserInfo`).
- `gui/app.go:1338` (`importSubscription`) and `:1388`
  (`UpdateSubscriptions`) parse links, tag `Group`/`SubURL`, and skip a
  link whose `dedupeKey` is already known (`:1398`, `:1419`).
  `recordSubscription`/`TouchSubscription` (`:1360`, `gui/config.go:446`)
  persist quota and expiry.
- The merge is **append-only**: profiles removed from the feed stay in
  `config.json` forever, and the dedupe key skips rather than replaces, so
  a changed node keeps its old settings.

### Adopt?

**No for fetch/parse (already equivalent or better), yes for
reconciliation.** PattN's delete-then-re-add per feed, with the active
profile preserved by id, is the pattern that fixes NekoGo's growing stale
profile list (`gui/app.go:1388-1437`); copying the multi-format parser
cascade is unnecessary because NekoGo's parser is deliberately narrower.

---

## 5. External process management (aether / sni-spoof)

### PattN approach

- Spawn: `P/Aether/AetherProcess.cs:138` (`StartInfo`) and `:183`
  (`Start`). Working directory is the core's work dir; stdin is closed
  immediately so the core cannot ask interactive questions (`:190-200`);
  every line of stdout/stderr is pumped with ANSI stripped
  (`:267` `PumpAsync`, guarded per line). **Environment is sanitised:**
  all inherited `AETHER_*` and `RUST_LOG` variables are removed before
  the app's own are set (`:155-181`, predicate `IsCoreSetting` `:241`), so
  a user's shell environment cannot silently override the arguments.
  Config is passed as CLI arguments (`P/Aether/AetherArguments.cs`, 1044
  lines, `--upstream` at `:42`) plus env vars naming the identity files
  (`AETHER_CONFIG`, `AETHER_MASQUE_CONFIG`, `AETHER_WG_CONFIG`) and the
  Psiphon data dir; no per-run config file is generated.
- Stop: `AetherProcess.cs:207` (`KillAsync`) kills the **entire process
  tree** and waits with a bound; exit code is captured before the output
  drain so a killed core still reports (`:247` `WaitForExitAsync`).
- Readiness: `AetherCoreManager.cs:485` (`StartSessionAsync`) spawns under
  a lifecycle semaphore, then callers wait either on the process
  (`AwaitListeningAsync` `:596`) or on the SOCKS greeting; failures are
  classified by `CouldNotStart` (`:1040`) into "did not start" vs "ended".
- Crash: `WatchAsync` `:1370` — see §1.
- Leftovers (Linux/macOS, where there is no job object):
  `AetherProcess.cs:307` (`AetherLeftovers`). Each core writes
  `"pid startTicks [bootTicks]"` (`Write` `:408`); the next start kills a
  matching process that is not the live one (`Reap` `:439`), using
  pid + start time + boot ticks so PID reuse cannot kill an innocent
  process. Deleted on a clean stop (`AetherCoreManager.cs:1318`).
- Ports: `P/Aether/AetherPorts.cs:12` fixes a base listen port (10819)
  with the next three reserved for the core, Tor and Psiphon
  (`SecondaryOffset` `:17`, `LastListenPort` `:23`), and keeps them clear
  of the app's own mixed/SOCKS/PAC/API window; `IsReserved` `:91`,
  `Unreserved` `:102`, `ScanPort` `:142` for scans. Before starting,
  `P/Aether/AetherCoreLoad.cs:128` (`BeforeXrayAsync`) probes every port
  with TCP **and** UDP for the secondary inbound (`PortsProblem` `:163`,
  `CanListen` `:215`), retrying for at most `PortReleaseWait` (1 s) while
  the old cores release them, and refuses the load with the offending
  port instead of letting the core fail to bind.
- Ordering: `BeforeStopAsync` `:102` (refuse early if the core binary is
  missing, cancel probes), `AfterXrayAsync` `:256` (start the session only
  after Xray's `--upstream` port answers; if the core fails, stop the
  cores), `UpstreamPort` `:296`.
- TUN interaction: `P/Aether/AetherUplink.cs:34` resolves which interface
  the running TUN actually uses (`TunInterfaceOf` `:60`, `IsTun` `:49`,
  `Find` `:118`/`:128`, Windows route tiers `:148`, Linux route parsing
  `:173`/`:200`), so a short-lived core started for a scan or latency test
  binds to the uplink instead of looping back into the TUN.
- Helper binaries: `P/Aether/AetherBinaries.cs` provisions
  `aether`, `psiphon-tunnel-core`, `lyrebird` in `bin/aether`, exposes
  availability checks (`IsCoreAvailable` `:83`, `IsPsiphonAvailable`
  `:86`, `IsLyrebirdAvailable` `:89`) and the execute-bit rule (`:108`).
- `sni-spoof`: no separate sni-spoof executable exists in this tree; the
  spoofing behaviour lives inside the Aether arguments/profiles. Nothing
  to compare against NekoGo's `sni-spoof.exe` beyond the process pattern
  above.

### NekoGo today

- `gui/aether_manager.go:147` (`Start`): `freePort()` per connect, argv
  from `gui/aether_core.go:266` (`aetherCoreOf`) / `:38`
  (`buildAetherArguments`), `--log-level` appended (`:168`), env from
  `aetherEnv` (`:132`), hidden window, stdout/stderr relayed by
  `aetherLogWriter` (`:286`), then `answersSocks` (`:343`) for up to 30 s
  (`aetherWarmup`, `:40`). `Stop` (`:243`) kills and waits 3 s;
  `watchChild` (`:231`) is the only crash observer.
- Env: `aetherEnv` (`:132-145`) appends to `os.Environ()` and never
  removes inherited `AETHER_*`/`RUST_LOG`-style variables.
- Leftovers: `gui/orphans.go:47` sweeps only
  `orphanImageNames = {xray.exe, nekobox_core.exe, sing-box.exe}`
  (`:43`) — **`aether.exe` is not in the list**, and its ownership test
  is parent-PID based (`selectOrphans` `:88`), so a crashed GUI leaves
  aether running with no pid file to prove identity.
- Ports: only `freePort` (`gui/singbox_manager.go:206`, bind-then-close
  on TCP) and a live-connect staleness check `aetherPortFree`
  (`gui/aether_manager.go:384`); no UDP probe, no reserved window, no
  pre-start refusal. Scans use the same single-port pick
  (`gui/aether_scan.go:304`).
- Probes/tests: `gui/core_manager.go:660` (`startEphemeralProbe`) and
  `:697` (`EphemeralURLTest`) spawn throwaway cores with owner-scoped
  cancellation (`:551`, `:628`) — this is NekoGo's equivalent of PattN's
  standalone/test runs, and it has no TUN-uplink awareness.

### Adopt?

**Yes — four concrete patterns, in descending value:**

1. **Engine death ends the connection** (`AetherCoreManager.WatchAsync`
   `:1370` + `CoreManager.OnAetherSessionEnded` `:595`) into
   `gui/aether_manager.go:231`.
2. **Identity-based reaping** (`AetherLeftovers` `:307`/`:408`/`:439`)
   into `gui/orphans.go:47` (or a new `gui/aether_leftovers.go`), because
   NekoGo's name+parent heuristic cannot see an `aether.exe` orphan.
3. **Env sanitisation** (`AetherProcess.cs:138-181`, `IsCoreSetting`
   `:241`) into `gui/aether_manager.go:132`.
4. **Port pre-flight** (`AetherCoreLoad.cs:128`/`:163`/`:215`,
   `AetherPorts.cs:12-140`) into `gui/aether_manager.go:147` /
   `gui/core_manager.go:660`, chiefly because duplicate or reserved-port
   binds are the current failure mode of parallel scans.

---

## 6. Elevation / admin on Windows

### PattN approach

- `P/Common/ProcUtils.cs:49` (`RebootAsAdmin`) relaunches the executable
  with `UseShellExecute = true` and `Verb = "runas"`, passing a single
  "reboot as admin" argument; `P/Manager/AppManager.cs:157`
  (`RebootAsAdmin`) then calls `AppExitAsync`. The elevated child starts
  from scratch — there is no pending-action hand-off.
- Non-Windows elevation is per-core: `CoreManager.cs:452`
  (`ShouldRunAsSudo`) elevates only TUN launches of mihomo/sing-box/Xray,
  through `P/Manager/CoreAdminManager.cs:32` (`RunProcessAsLinuxSudo`) and
  `:72` (`KillProcessAsLinuxSudo`), and remembers that it did
  (`_linuxSudo`, `CoreManager.cs:17`, cleaned up in `CoreStopInTurn`
  `:295-315`).
- Stale TUN devices are removed before a TUN start:
  `P/Common/WindowsUtils.cs:64` (`RemoveTunDevice`, `pnputil
  /remove-device SWD\Wintun\{guid}` for `wintunsingbox_tun` /
  `xray_tun`), called from `CoreManager.cs:200`.

### NekoGo today

- `gui/elevate_windows.go:42` (`relaunchElevated`, ShellExecuteW `runas`)
  and `:68` (`guiElevated`); `gui/systools_windows.go:15`
  (`isGuiElevated`).
- Pre-flight gate + actionable error: `gui/app.go:682`
  (`tunNeedsElevation`), `:700` (`tunStartHint` keeps `%w` and appends the
  admin remedy), `:743` (`RelaunchAsAdmin` relaunches with
  `--connect <profileID>` and shuts the parent down, so the elevated child
  **resumes the pending connect** — strictly more than PattN does).
- No stale-Wintun-device removal: no `pnputil` / `wintun` call exists in
  `gui/` outside the pre-flight comments (`gui/app.go:598-699`).

### Adopt?

**Already equivalent, and NekoGo is ahead on the resume-after-elevation
part.** The only piece worth taking is the stale adapter sweep
(`WindowsUtils.cs:64`) as an optional pre-TUN step in
`gui/singbox_manager.go` / `gui/app.go:682`, since NekoGo otherwise has no
remedy for a half-removed Wintun device.

---

## 7. Error handling and user-visible errors

### PattN approach

- One `Func<bool, string, Task>` "update" callback flows through every
  layer (`CoreManager.cs:18`, `ProcessService.cs:6`, `:22`). The `bool`
  means "this is for the user" — failures call it with `true`
  (`CoreManager.cs:161`, `:177`, `:180`; `ProcessService.cs:115`, `:174`),
  core output calls it with `false` (`ProcessService.cs:125`).
- Diagnostics go to `Logging.SaveLog` with a tag and are never shown
  raw; user text comes from a localised `ResUI` table (e.g.
  `ResUI.FailedToRunCore` at `CoreManager.cs:519`,
  `ResUI.MsgAetherCoreCannotStart`, `ResUI.MsgAetherConnecting` in
  `AetherCoreManager.cs:623-641`).
- Crash reporting composes a human sentence from the core's own last
  words: `AetherCoreManager.StoppedMessage` (`:1023`) uses
  `NoEchKey`, `LastError` and `ChainEnd`, and `CouldNotStart`
  (`:1040`) separates "never started" from "stopped".
- A failed start is always paired with cleanup: `AfterXrayAsync`
  (`AetherCoreLoad.cs:256`) reports then `StopCores()`; `LoadCoreInTurn`
  (`CoreManager.cs:169-231`) leaves the previously running cores alone if
  the new config cannot start.
- For non-GUI clients the same information becomes structured runtime
  state, not just a string: `V2rayRuntime.cs:1216`
  (`OnAetherCoresStoppedAsync`) faults the core state and publishes
  `"status"`; a start already in flight takes the words as its failure
  instead of the old state winning.

### NekoGo today

- `Connect` returns the error to the frontend (`gui/app.go:540`, failure
  at `:616-619`) **and** records it with `noteError`/`LastError`
  (`:130`, `:145`) so silent auto-connect failures are visible. Logs go
  through `emitLog` with `source` + `level` (`gui/app.go:609`,
  `gui/aether_manager.go:57`). Errors wrap causes (`%w`) so the reason is
  preserved (`singbox_manager.go:267`, `:303`, `:325`).
- TUN failures are annotated with the admin remedy (`app.go:700`), and
  the core start path refuses with an actionable message before spawning
  (`app.go:682`). There is no equivalent of faulting the *runtime* state:
  `Status()` (`gui/core_manager.go:838`) is derived from manager flags, so
  a dead engine is reported as "not running" only after a poll, with no
  reason attached.
- Gaps: a crash **after** a successful start produces only a log line
  (`gui/singbox_manager.go:361`, `gui/aether_manager.go:231`) — no error
  state, no UI notification, no automatic `Disconnect`. Aether warm-up
  timeout collapses every cause into
  `"aether engine listener on port %d did not come up in %s"`
  (`aether_manager.go:196`) even when the core printed a precise reason.

### Adopt?

**Yes, two things:** (a) the notify-vs-log split, implemented against
NekoGo's existing log levels plus `LastError`; (b) `CouldNotStart`-style
classification and last-words reporting (`AetherCoreManager.cs:1023`,
`:1040`) in `gui/aether_manager.go`, so a crashed core yields "did not
start" vs "ended after N s" with its own last error rather than a generic
timeout.

---

## 8. Patterns worth adopting (summary)

1. **Serialized turns + generation for core replacement** —
   `P/Aether/AetherCoreLoad.cs:405-453`, used at
   `P/Manager/CoreManager.cs:146`, `:284`, `:595`.
   Solves: overlapping reload/stop/engine-death sequences.
   Lands in: `gui/core_manager.go:144` (extend the existing `epoch`
   commit rule to a shared start/stop queue across the three managers).
   Risk: **medium** (deadlock surface; must not hold `m.mu` while
   queueing).
2. **Engine death tears down the connection group** —
   `P/Manager/CoreManager.cs:595` + `P/Aether/AetherCoreManager.cs:1370`.
   Solves: connected-but-dead tunnel after an `aether.exe` crash, and
   clients that keep believing they are connected.
   Lands in: `gui/aether_manager.go:231` → `gui/core_manager.go`
   (`Stop` + `noteError`), published through `Status()`
   (`gui/core_manager.go:838`) / `LastError` (`gui/app.go:145`); the
   WebAPI half of the pattern is `V2rayRuntime.cs:1216`.
   Risk: **low**.
3. **Crash-vs-stop classification** —
   `P/Aether/AetherCoreManager.cs:1040` (`CouldNotStart`) and `:1023`
   (`StoppedMessage`), `AetherProcess.cs:90` (DLL-not-found exit code).
   Solves: opaque "did not come up" errors.
   Lands in: `gui/aether_manager.go:147` / `:194`.
   Risk: **low**.
4. **Leftover reaping by pid + start time** —
   `P/Aether/AetherProcess.cs:307`, `:408`, `:439`.
   Solves: orphaned `aether.exe` after a GUI crash (NekoGo's orphan list
   at `gui/orphans.go:43` misses it).
   Lands in: `gui/orphans.go:47` plus a new `gui/aether_leftovers.go`.
   Risk: **medium** (must get process-identity comparison exactly right).
5. **Loopback port pre-flight including UDP, with a reserved window** —
   `P/Aether/AetherCoreLoad.cs:128`, `:163`, `:215`;
   `P/Aether/AetherPorts.cs:12-140`.
   Solves: "address in use" starts and parallel scans stealing each
   other's ports.
   Lands in: `gui/aether_manager.go:147`, `gui/core_manager.go:660`.
   Risk: **low–medium** (a UDP probe is a new failure mode on locked-down
   hosts).
6. **Child environment sanitisation** —
   `P/Aether/AetherProcess.cs:138-181`, `:241`.
   Solves: inherited `AETHER_*`/`RUST_LOG` silently overriding arguments
   (NekoGo appends at `gui/aether_manager.go:132`).
   Lands in: `gui/aether_manager.go:132`.
   Risk: **low**.
7. **Immutable render context** —
   `P/Handler/Builder/CoreConfigContextBuilder.cs:41`,
   `P/Models/CoreConfigs/CoreConfigContext.cs:3`.
   Solves: render/launch desync when settings change mid-start.
   Lands in: `gui/singbox_manager.go:844`, `gui/xray_manager.go:672`.
   Risk: **medium** (touches the hot start path).
8. **Subscription reconciliation (replace the feed, keep the active
   profile)** — `P/Handler/ConfigHandler.cs:2185-2200`.
   Solves: stale profiles accumulating in `config.json`.
   Lands in: `gui/app.go:1388`.
   Risk: **medium** (deleting rows is destructive if the fetch returned a
   truncated feed; PattN accepts that trade).
9. **Settings schema version + ordered migrations** —
   `P/Manager/AppManager.cs:350`, `:359`, `:386`.
   Solves: one-shot migrations that cannot be re-run or ordered
   (NekoGo's flag-based `migrateSpoofProfiles`,
   `gui/config.go:149`).
   Lands in: `gui/config.go:67`.
   Risk: **low**.
10. **Stale Wintun device removal before a TUN start** —
    `P/Common/WindowsUtils.cs:64`, caller `CoreManager.cs:200`.
    Solves: a half-removed adapter making the next TUN start fail.
    Lands in: `gui/singbox_manager.go` TUN path or
    `gui/app.go:682` pre-flight.
    Risk: **low** (best-effort, logged).
11. **Windows job object (kill children with the GUI)** —
    `P/Services/WindowsJobService.cs:4`, `:45`; wired at
    `CoreManager.cs:527`, `:541`.
    Solves: orphaned children on abnormal exit, without a `wmic`/`taskkill`
    sweep.
    Lands in: new `gui/job_windows.go`, wired from `gui/app.go` start.
    Risk: **medium** (Go needs `CreateJobObject`/`AssignProcessToJobObject`
    via `syscall`; job-wide kill must not catch unrelated children).
12. **TUN-uplink binding for short-lived cores** —
    `P/Aether/AetherUplink.cs:34-140` (interface discovery, `Find` `:118`),
    consumed by the standalone/test runs in `AetherCoreManager.cs:667`.
    Solves: a probe/test started while TUN runs either bypasses or loops
    through the tunnel.
    Lands in: `gui/core_manager.go:660` (`startEphemeralProbe`).
    Risk: **medium** (per-OS route introspection: `GetIpForwardTable2`,
    `/proc/net/route`, `scutil`).

---

## 9. Patterns NOT worth adopting

1. **SQLite profile store** (`P/Helper/SqliteHelper.cs`) — NekoGo's
   atomic JSON store (`gui/config.go:226`) needs no native dependency and
   its dataset is orders of magnitude smaller.
2. **Immutable record contexts cloned with `with` for every field**
   (`CoreConfigContext.cs:3`) — in Go a plain value snapshot passed by
   argument gives the same guarantee without the ceremony; only the
   snapshot *idea* (§8.7) is worth taking.
3. **A single fixed Aether listen port (10819) plus three reserved
   ports** (`P/Aether/AetherPorts.cs:12-23`) — NekoGo deliberately picks a
   port per connect, and a fixed port would collide between the session,
   scans and parallel tests.
4. **Linux sudo relaunch for privileged TUN cores**
   (`CoreManager.cs:452`, `CoreAdminManager.cs:32`) — NekoGo is a
   Windows-first desktop build with a `runas` path; there is no Linux
   elevation surface to hang this on today.
5. **The full v2rayN routing/DNS generator stack** (`SingboxRoutingService.cs`,
   616 lines; `SingboxDnsService.cs`, 715 lines; `SingboxOutboundService.cs`,
   866 lines) — this is feature breadth, not a pattern, and NekoGo's
   JSON section overrides already give advanced users an escape hatch.
6. **Psiphon/Tor helper provisioning** (`P/Aether/AetherBinaries.cs:12-60`,
   `PsiphonServerList.cs`, `PsiphonServersUpdate.cs`) — NekoGo distributes
   a single `aether.exe` with no helper downloader; adding one is a
   product decision, not a lifecycle pattern.
7. **Localised `ResUI` string table** (used throughout, e.g.
   `CoreManager.cs:161`) — NekoGo reports English errors and localises in
   the frontend, which keeps the Go backend free of a translation table.
8. **The 100 ms "still alive" start heuristic** (`CoreManager.cs:495-521`)
   — NekoGo's RPC-port and SOCKS-greeting probes
   (`gui/singbox_manager.go:232`, `gui/aether_manager.go:343`) are already
   strictly stronger.
9. **A separate sni-spoof executable pattern** — this tree has none
   (spoofing is Aether profile data); nothing to port that NekoGo's
   `gui/spoof_manager.go` does not already do.

---

## 10. Open questions

- Whether NekoGo's bundled `aether.exe` honours the same pid-file /
  `AETHER_*` environment contract as PattN's core. The binary is external;
  source alone cannot confirm it writes a pid file, and §8.4 depends on
  that or on an equivalent OS-level identity check.
- Whether `aether.exe` actually survives its parent's death on Windows in
  NekoGo's build (no job object today, `gui/orphans.go:43` misses it). The
  observed failure mode is inferred from source, not reproduced.
- Whether sing-box's TUN in NekoGo needs the `pnputil` sweep at all: the
  Wintun adapter may be recreated on demand, making §8.10 unnecessary.
  PattN's call site (`CoreManager.cs:200`) is unconditional on Windows
  with TUN, which hints at a real problem but does not prove NekoGo has it.
- How NekoGo's frontend would consume a faulted core state pushed from
  the backend: the WebAPI publishes an event (`V2rayRuntime.cs:1216`),
  while NekoGo's surfaces are polled (`Status`, `LastError`). Whether to
  add a push event or let the existing poll observe it could not be
  decided from source alone.
- PattN's subscription quota handling: no `subscription-userinfo` parser
  exists in `ServiceLib`, so it is unknown whether quota/expiry is
  surfaced elsewhere (views, WebAPI) or simply dropped in this fork.
- Whether `CoreManager.OnAetherSessionEnded` also reverts the OS proxy.
  It stops the cores (`CoreStopInTurn`) and calls `AetherCoresStopped`
  (`V2rayRuntime.cs:1216`), which only updates runtime state; the system
  proxy reversion in that path could not be traced from ServiceLib alone.
- NekoGo's exact UI behaviour on a post-connect `aether.exe` death
  (whether `Status()` polling notices `running == false` anywhere) was not
  traced end-to-end; `watchChild` (`gui/aether_manager.go:231`) only
  updates its own flags.
- The precise semantics of the SQLite paging loop when a migration row
  keeps failing (`AppManager.cs:375-385`) — the offset arithmetic is
  subtle and would need a run to confirm it terminates for a permanently
  bad row.
