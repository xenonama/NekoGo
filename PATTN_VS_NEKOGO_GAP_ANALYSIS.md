# PattN vs NekoGo — Gap Analysis (backend)

(Read-only audit. No NekoGo file was changed. The audited tree is
PattN — the Windows C# fork of v2rayN at `PattN/v2rayN/` — not the
Android Kotlin PattNG. The two forks share the same backend patterns,
so this reference applies to both. PattN does NOT have SNI Spoofing;
that is a NekoGo-only feature and is out of scope here.)

Path prefixes used below: `P = PattN/v2rayN/ServiceLib`,
`W = PattN/v2rayN/v2rayN.WebAPI`, `G = gui` (NekoGo).

## 0. Source locations

- **NekoGo root:** `C:\Users\pc\Desktop\nekobox-main` — Go module in
  `gui/` (95 `.go` files including tests), libbox/Thrift core server
  in `core/server/`, Wails frontend in `gui/frontend/src/`.
- **PattN root:** `C:\Users\pc\Desktop\nekobox-main\PattN\v2rayN` —
  the directory holding `ServiceLib/`, `v2rayN/`, `v2rayN.Desktop/`,
  `v2rayN.WebAPI/` and `ServiceLib.Tests/`.
- **PattN variant:** Windows .NET 8. C# only: 544 `.cs` files, and
  0 `*.kt` / `AndroidManifest.xml` / `build.gradle*` under `PattN`.
  "PattNG" in the C# comments names the Android app the logic was
  ported from, not a tree in this checkout.

## 1. Fragment (TLS ClientHello fragmentation)

### PattN approach

- `P/Models/Configs/Config.cs:32` holds one app-wide
  `Fragment4RayItem`; the presets live in `P/Global.cs:461-476`
  (`tlshello`/`1-1` packet selectors, lengths, delays, `maxSplit`),
  and `P/Handler/ConfigHandler.cs:181-199` fills defaults (`packets`
  `tlshello`, lengths `50-100`, delays `10-20`, `maxSplit` `0`).
- Rendering is an **inserted outbound**, not a profile field:
  `P/Services/CoreConfig/V2ray/V2rayOutboundService.cs:898-930` builds
  a `fragment` freedom mask and `:885-897` clones it in front of every
  outbound, re-tagging the real one as `fragment-<tag>` and pointing
  its `dialerProxy` back. It is Xray-only; the sing-box path has no
  fragment mask (`P/Services/CoreConfig/Singbox/` has no
  `fragment` reference).
- Per-profile? Only for Aether, which is the exception: Aether keys
  carry their own fragment switch and ranges
  (`P/Aether/AetherKeys.cs:30-32`, `:126-137`), validated at
  `:217-220`, bounded by `P/Aether/AetherRange.cs:9-13`, emitted as
  `--fragment`/`--fragment-size`/`--fragment-delay`
  (`P/Aether/AetherArguments.cs:200-204`).
- Survives save/load because the switch is a JSON setting
  (`P/Handler/ConfigHandler.cs:218-236`) and is exposed in the settings
  dialog: `P/ViewModels/OptionSettingViewModel.cs:34-39`, `:170-175`,
  validated at `:318-324`.

### NekoGo today

- Fragment is **per profile**: `Profile.TLSFragment`,
  `TLSFragmentLen`, `TLSFragmentInt`, validated once in
  `G/mux_fragment.go:62-87` and shared by both builders so bad values
  fail on either core.
- Xray gets a real mask: `G/xray_manager.go:991-1014` emits
  `type: fragment` with `packets` (mode), one `length` range and one
  `delay` range; sing-box gets its boolean
  (`G/singbox_manager.go:1205-1210`), because sing-box has no
  length/interval knobs.
- A global override sits on top of the per-profile value:
  `G/mux_fragment.go:204-212` (triple as text), `:252` (validated),
  `:271` (applied when the profile names none). The UI exposes both
  (`G/frontend/src/components/ProfileForm.vue:44-52`,
  `.../JsonEditorModal.vue:202-226`).
- Persisted with the profile in `config.json`
  (`G/config.go:226-244`) and reproducible from a share link
  (`G/import.go:318`, `:564`).

### Status

- **AHEAD** — NekoGo applies fragmentation per profile and on both
  cores; PattN has one app-wide switch and only the Xray path.

### Adopt?

- no — the per-profile model is the stronger design. The only piece
  NekoGo does not mirror is PattN's `maxSplit` and its multi-entry
  `lengths`/`delays` lists (`P/Services/CoreConfig/V2ray/V2rayOutboundService.cs:900-915`),
  which Xray accepts; that is an optional polish, not a gap.

## 2. TUN / tunnel

### PattN approach

- The TUN inbound exists only for sing-box and only when the render
  context says so (`P/Handler/Builder/CoreConfigContextBuilder.cs:54`).
  `P/Services/CoreConfig/Singbox/SingboxInboundService.cs:53-82`
  builds it: MTU and stack defaulted from
  `P/Global.cs:706-720` (1280 / `gvisor`), `interface_name`
  `singbox_tun`, `auto_route`/`strict_route`/`stack` from
  `TunModeItem` (`P/Models/Configs/ConfigItems.cs:146-161`), IPv4 from
  a list of `/30` pairs (`P/Global.cs:894-903`), `route_exclude_address`.
- IPv6 is conditional on the OS: `TunTakesIPv6()`
  (`P/Services/CoreConfig/Singbox/SingboxInboundService.cs:84-93`)
  skips the IPv6 address when Windows has IPv6 disabled on new
  interfaces, unless a global IPv6 address could bypass the tunnel.
- Routing carries the TUN-specific safety rules:
  `P/Services/CoreConfig/Singbox/SingboxRoutingService.cs:40-45`
  appends the embedded TUN rule set; `:52-68` adds a
  `reject`/`method: drop` rule for the TUN's **own addresses** so
  `auto_route` cannot loop a packet back into the outbound and pin a
  CPU core; `:88-116` implements the ICMP policy; `:120-150` adds
  `action: sniff` plus `hijack-dns` for 53/`dns`.
- DNS is typed throughout: `P/Services/CoreConfig/Singbox/SingboxDnsService.cs:5`
  (`GenDns`), `:32` (`GenDnsServers`), `:117-138`
  (remote/direct/hosts/fakeip servers), `:152` (`GenDnsRules`),
  `:527` (`GenMinimizedDns` for latency tests).

### NekoGo today

- The TUN inbound is appended only when `settings.TunEnabled`
  (`G/singbox_manager.go:936-945`): fixed address `172.19.0.1/24`,
  `mtu` from the const `tunMTU = 1350` (`:966`), `auto_route: true`,
  `strict_route: false`, stack via `normalizeTunStack`
  (`:978-984`, `validTunStack` `:986-991`). No `interface_name`, no
  IPv6 address, no `route_exclude_address`.
- DNS is handled at the route level: `:893-895` swaps the default
  `protocol: dns` rule from `direct` to `hijack-dns` when TUN is on,
  `:900-905` turns on `auto_detect_interface` (otherwise sing-box
  dials back into its own tunnel), `:929-935` installs a default DNS
  block when the user set none (`singTunDefaultDNSBlock` `:1049-1056`).
  Servers are typed objects (`G/singbox_manager.go:1010-1043`,
  `:1061-1094`).
- TUN start failures are annotated, not rewritten: `G/app.go:682-693`
  (`tunNeedsElevation`) pre-flights elevation, `G/app.go:700-724`
  (`tunStartHint`) appends the admin remedy while preserving the
  core's own error.

### Status

- **BEHIND** — NekoGo renders no rule for the TUN's own address, so
  the loop PattN documents and drops is reachable here; it also has no
  ICMP policy, no IPv6 TUN address and no `route_exclude_address`.
  (NekoGo is ahead on two smaller points: a deliberate 1350 MTU versus
  PattN's 1280 default, and hijacking DNS even before the
  `sniff`/`hijack-dns` pair exists.)

### Gap (if BEHIND)

- What is missing: the drop rule for the TUN's own addresses
  (`P/Services/CoreConfig/Singbox/SingboxRoutingService.cs:52-68`) and
  the optional ICMP policy (`:88-116`); `route_exclude_address` and
  IPv6 addressing (`P/Services/CoreConfig/Singbox/SingboxInboundService.cs:71-82`).
- Where it would land: `G/singbox_manager.go:896-899` (route rules,
  built before the TUN inbound at `:936`), plus the TUN map itself.
- Risk: low for the drop rule (it only rejects traffic addressed to
  the TUN's own /24 that would otherwise loop), low for ICMP,
  medium for adding IPv6 to a Wintun adapter.

### Adopt?

- yes — the self-address drop is a two-line safety rule against a
  CPU-pinning loop; the rest is optional.

## 3. Aether

### PattN approach

- One session core is started **after** Xray and stopped **before** it
  (`P/Aether/AetherCoreLoad.cs:128-156`, `P/Manager/CoreManager.cs:169-203`),
  so the engine can dial Xray's secondary-socks inbound and dies with
  the group.
- Spawn is bespoke and argument-safe:  `P/Aether/AetherProcess.cs:138-176`
  uses `ArgumentList` (no quoting), closes stdin at once (the core only
  prompts on a terminal), pumps stdout/stderr line by line and sets
  `NO_COLOR`. Readiness is a listener await
  (`P/Aether/AetherCoreManager.cs:596`, `:1457`), not a sleep.
- Crash detection is classified: `WatchAsync`
  (`P/Aether/AetherCoreManager.cs:1370`) awaits the exit code and asks
  `CouldNotStart` (`:1040`: 1 s start window, no output, or the
  DLL-not-found code `0xC0000135` from `P/Aether/AetherProcess.cs:90`)
  versus `StoppedMessage` (`:1023`) for a core that ran. The verdict
  travels as `SessionEnded` (`:455`).
- Ports are a reserved window: the listen port plus three
  (`P/Aether/AetherPorts.cs:10-23`), kept clear of the app's own
  `L..L+6` local ports (`:55-83`), with scan ports avoided (`:130-160`).
- Leftovers are reaped by identity, not by name:
  `P/Aether/AetherProcess.cs:307-345` (pid + UTC start ticks + Linux
  boot ticks), `:394-407` (`IsLeftover`), `:467` (enumeration), driven
  by `P/Aether/AetherCoreLoad.cs:134` and
  `P/Aether/AetherCoreManager.cs:1258`.
- Binaries are validated before use: `P/Aether/AetherBinaries.cs:24-36`
  (names/folder), `:108` (`IsRunnable`).

### NekoGo today

- `G/aether_manager.go:147-202` spawns the engine with a free port, its
  own env (`:132-145`), a log relay, and waits up to `aetherWarmup`
  (30 s, `:40`) for the SOCKS listener to answer (`:196`, `:343-365`).
- `G/aether_manager.go:231-241` (`watchChild`) only clears
  `running`/`port` and logs "aether engine exited"; nothing tells the
  main core or the proxy. `Stop` (`:243-263`) kills and waits 3 s.
- Port handling is a single `freePort()` bind-and-hand-over
  (`G/singbox_manager.go:206-215`) with no notion of the Aether
  reserved window.
- Leftovers are not handled for this engine: `G/orphans.go:43` knows
  only `xray.exe`, `nekobox_core.exe`, `sing-box.exe`.
- The wiring exists but is one-way: `G/core_manager.go:166-200` starts
  the engine before the main core and publishes its port
  (`:217-229`), and `:311-338` stops it with the group — but a death
  after startup is never reported back.

### Status

- **BEHIND** — the engine outlives its own crash: sing-box/Xray and
  the system proxy keep pointing at a dead SOCKS port until the user
  disconnects.

### Gap (if BEHIND)

- What is missing: death notification and teardown; crash-vs-stop
  classification with the core's last words; pid-file ownership for
  leftovers.
- Where it would land: `G/aether_manager.go:231` (report instead of
  only logging) and `G/core_manager.go:172-200` / `:311` (teardown in
  the group); new file for a pid ledger.
- Risk: medium.

### Adopt?

- yes — this is the single most valuable pattern in the audit.

## 4. SNI Spoofing — NekoGo-only, out of scope

NekoGo has a first-class SNI Spoofing engine (`G/spoof_manager.go`,
`G/sni_front.go`, `G/sni_scan.go`); PattN does not; this is a
NekoGo-only feature and out of scope for this comparison.

## 5. Core lifecycle (spawn / stop / crash / orphan)

### PattN approach

- Every load or stop runs in a turn with a generation counter
  (`P/Aether/AetherCoreLoad.cs:405-455`: `RunAsync`, `NewGeneration`,
  `EndIfCurrentAsync`), so a core dying after a reload cannot stop the
  new cores. `LoadCore` (`P/Manager/CoreManager.cs:146`) enters that
  turn at `:159`/`:169`; `CoreStop` (`:284`) and `CoreStopInTurn`
  (`:295`) use the same one.
- "Started" is only a liveness check (`P/Manager/CoreManager.cs:495-521`:
  alive after 100 ms, otherwise `ResUI.FailedToRunCore`); real
  readiness is waited for separately (proxy port `:373`, Aether
  listener `P/Aether/AetherCoreManager.cs:596`).
- Cleanup on exit reverts the proxy, saves, stops both cores
  (`P/Manager/AppManager.cs:122`, `P/Aether/AetherCoreManager.cs:891`)
  and, on Windows, relies on a job object with
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`
  (`P/Services/WindowsJobService.cs:4-56`, attached via
  `P/Manager/CoreManager.cs:522`, `:527`, `:541`).

### NekoGo today

- `G/core_manager.go:144-207` (`Start`) starts the Aether engine first
  when the profile needs it, then the main core, with an `epoch` guard
  (`:15-30`, `:218-230`) so a stop that races a start wins. `Stop`
  (`:311-400`) bumps the epoch, tears the group down in reverse order
  and clears the published Aether port.
- Each manager owns its own `Wait`
  (`G/singbox_manager.go:361-391`, `G/xray_manager.go:317-343`,
  `G/aether_manager.go:231-241`) and logs an unexpected exit, but only
  the manager's own flag changes.
- Startup death is caught honestly for the two main cores: the
  `exited` channel turns an instant crash into a fast failure
  (`G/singbox_manager.go:272-290`).
- Leftover cores are swept by image name plus "is under our exe dir"
  (`G/orphans.go:43`, `:47-108`); ownership is a parent-pid check, not
  a start-time identity.

### Status

- **BEHIND** — no cross-engine death handling, no job object, and the
  sweep cannot prove ownership of an `aether.exe` a crashed GUI left
  behind.

### Gap (if BEHIND)

- What is missing: an engine-death callback into `CoreManager`; a
  kill-on-close job object (or equivalent) for every child; start-time
  ownership in the sweep.
- Where it would land: `G/aether_manager.go:231`,
  `G/core_manager.go:311`, `G/orphans.go:43`/`:88`.
- Risk: medium.

### Adopt?

- yes — the generation guard is already equivalent; the missing half
  is the death path.

## 6. Config generation and merging

### PattN approach

- Rendering starts from an immutable record:
  `P/Models/CoreConfigs/CoreConfigContext.cs:3-51` (init-only node,
  routing, DNS, TUN flags, IPv6 facts, ECH/Aether carry-overs), built
  by `P/Handler/Builder/CoreConfigContextBuilder.cs:41-70`.
- Generation is staged per section: log, inbounds, outbounds, routing,
  DNS, experimental, ruleset conversion
  (`P/Services/CoreConfig/Singbox/CoreConfigSingboxService.cs:40-64`),
  each section in its own file (`SingboxInboundService.cs`,
  `SingboxOutboundService.cs`, `SingboxRoutingService.cs`,
  `SingboxDnsService.cs`, `SingboxRulesetService.cs`).
- User overrides are a full-config template pulled from storage and
  merged last (`P/Services/CoreConfig/Singbox/SingboxConfigTemplateService.cs:5-12`,
  `:86`), plus a custom-outbound replacement pass (`:15`).
- Sniffing is rendered for sing-box, not only Xray:
  `P/Services/CoreConfig/Singbox/SingboxRoutingService.cs:120-150`
  adds `action: sniff`, the 53/dns hijack pair, and the
  `tls_record_fragment` route option when final fragment is on.

### NekoGo today

- Two pure builders: `G/singbox_manager.go:844-957`
  (`BuildSingBoxConfig`) and `G/xray_manager.go:672-776`
  (`BuildXrayConfig`, with the stream/mask work at `:930-1014`).
  Chaining is explicit (`G/singbox_manager.go:1105-1137`,
  `G/xray_manager.go:808-836`).
- Overrides are per-section JSON keys stored in settings and merged
  over the rendered map (`G/app.go:2232-2252`), with a full raw config
  taking the whole file (`:2264-2284`, consumed at `:2354-2370`),
  persisted and validated per section (`:2385-2425`) and exported with
  the rest of the data (`:2437-2447`).
- Inbounds are one `mixed` inbound (`G/singbox_manager.go:913-918`)
  plus, for Xray, the two sniffing-enabled inbounds
  (`G/xray_manager.go:729-737`).
- The sing-box path has **no** sniff rule: the route is the DNS rule,
  the optional TUN rules and `final: proxy` (`G/singbox_manager.go:896-899`).

### Status

- **DIFFERENT** — PattN's template merge and NekoGo's section
  overrides solve the same problem with different storage; NekoGo's
  builders are pure and easier to test, PattN's section services scale
  better.

### Gap (if BEHIND)

- Not BEHIND overall, but one piece is missing: the sing-box
  `action: sniff` rule (`P/Services/CoreConfig/Singbox/SingboxRoutingService.cs:120-137`).
  It would land in `G/singbox_manager.go:896-899`. Risk: low.

### Adopt?

- no for the architecture; yes for the sniff rule if domain-based
  rules are ever added to the sing-box path.

## 7. Profile storage and migration

### PattN approach

- Profiles, subscriptions, DNS items, routings and config templates are
  SQLite rows (`P/Helper/SqliteHelper.cs`), addressed by entity type
  (`P/Manager/AppManager.cs:176-195`); only the app settings are a JSON
  file, written temp-then-move (`P/Handler/ConfigHandler.cs:218-236`).
- Schema migration is versioned **per row** and paged:
  `P/Manager/AppManager.cs:350-357` (`MigrateProfileExtra`), `:359-385`
  (`MigrateProfileExtraV2ToV3`, 100-row pages, offset advanced by the
  failures that stay), `:386-405` (`MigrateProfileTransportV3ToV4`).
- Built-in rows are re-seeded on every start so a restored database
  still has a default routing
  (`W/Services/V2rayRuntime.cs:265-269` in the WebAPI headless path).

### NekoGo today

- One `config.json` holding profiles and settings
  (`G/config.go:67-144`: `NewConfigStore`), written atomically through
  a temp file and `os.Rename` (`:226-244`), which also keeps a single
  file to back up or hand to another machine.
- Migrations are in-code, idempotent and run at load: legacy Aether
  path (`G/config.go:36-62`, applied `:103`), pre-rename data directory
  (`:79-90`, `:196-217`), SNI-spoof method and curated-list migrations
  (`:107-119`), the one-time spoof auto-detect revert
  (`:121-141`, `SpoofAutoDetectReverted`), and setting normalization at
  save (`:345` for `TunStack`).
- Backup/restore is the whole store in one blob
  (`G/app.go:2439-2490`).

### Status

- **DIFFERENT** — PattN scales to thousands of rows and can migrate
  per record; NekoGo is simpler, portable and has no schema to
  reconcile across files.

### Adopt?

- no — SQLite would be a regression for a single-user desktop app with
  one file to back up; the one idea worth keeping is a stored schema
  version instead of flag-per-migration (see §15 risk note).

## 8. Subscription handling

### PattN approach

- `P/Handler/SubscriptionHandler.cs:5` (`UpdateProcess`) downloads each
  enabled feed with a proxy attempt and a direct fallback (`:102-113`),
  supports a second URL per feed (`:158-196`, `SubItem.MoreUrl`) and
  then hands the text to the importer.
- A feed carries `Enabled`, `AutoUpdateInterval`, `Filter`,
  `ConvertTarget`, `PrevProfile`/`NextProfile` (chain), `UserAgent`,
  `RequestHeaders`, `OverrideAddress`/`OverridePort`
  (`P/Models/Entities/SubItem.cs:4-46`); overrides are applied on
  import (`P/Handler/ConfigHandler.cs:2160-2173`).
- Refresh reconciles by **delete-then-re-add**: the subscription's
  rows are removed and rebuilt, with the active profile re-selected by
  id (`P/Handler/ConfigHandler.cs:2189-2197`). Deleting a feed deletes
  its profiles and any custom config files
  (`P/Handler/ConfigHandler.cs:2396-2412`, `:2428-2444`) — via
  interpolated SQL (`:2401`, `:2405`), see §16.
- There is no `subscription-userinfo` parser anywhere in `ServiceLib`.

### NekoGo today

- Fetch and quota parsing exist and are ahead: `G/import.go:451-482`
  (`fetchSubscription`, reads the `subscription-userinfo` header at
  `:470`), `:57-95` (`parseSubUserInfo`, malformed pairs skipped),
  with the parsed usage stored on the feed (`G/import.go:24-41`) and
  surfaced per feed.
- Feed control: record (`G/app.go:1360-1385`), update all enabled
  (`:1388-1441`), edit URL/name and re-stamp owned profiles
  (`:1451-1530`, stamping at `:1514-1517`), delete with or without its
  profiles (`:1553-1606`, matching by `SubURL` with a group fallback in
  `G/config.go:516-523`), enable/disable one feed
  (`G/app.go:1607-1620`).
- Imported profiles record their source (`G/app.go:1353`, `:1418`) and
  are deduplicated by a full-config key
  (`G/import.go:552-572`).
- Auto-update exists only as one global interval
  (`G/types.go:307-310`), not per feed.

### Status

- **DIFFERENT** — NekoGo is ahead on quota, delete-with-profiles,
  re-stamping and dedupe; PattN is ahead on per-feed intervals,
  multi-URL feeds, filters/conversion and address/port overrides.

### Gap (if BEHIND)

- Not BEHIND overall. Two PattN-only capabilities are worth taking:
  per-feed `AutoUpdateInterval` (`P/Models/Entities/SubItem.cs:25`)
  and `OverrideAddress`/`OverridePort` (`:41-43`, applied at
  `P/Handler/ConfigHandler.cs:2160-2173`). Landing: `G/import.go:24-41`
  (feed fields) and `G/app.go:1388` (update loop). Risk: low.

### Adopt?

- yes, selectively — the two fields above; not the delete-then-re-add
  refresh, which NekoGo's dedupe already handles better.

## 9. Elevation / admin on Windows

### PattN approach

- Elevation is a UI-level gate on the TUN toggle:
  `P/ViewModels/StatusBarViewModel.cs:441-480` refuses to enable TUN
  without admin, reverts the flag and relaunches elevated (Windows) or
  asks for the sudo password (Linux/macOS).
- The relaunch is `ShellExecute` with `Verb = "runas"`
  (`P/Common/ProcUtils.cs:49-66`, called from
  `P/Manager/AppManager.cs:157`); the admin test is a
  `WindowsPrincipal` role check (`P/Common/Utils.cs:1401-1407`).
- Non-Windows core elevation uses a sudo password held by
  `P/Manager/CoreAdminManager.cs:32-69` (`RunProcessAsLinuxSudo`,
  `KillProcessAsLinuxSudo`). There is no Windows service and no
  elevated helper process.
- Autostart decides launch mode from the same admin check
  (`P/Handler/AutoStartupHandler.cs:49-66`).

### NekoGo today

- The gate is at connect time, not in a settings toggle:
  `G/app.go:682-693` reports that a TUN connect needs elevation, with
  the raw-config and Xray exceptions handled explicitly.
- The relaunch resumes the connection: `G/app.go:743-765`
  (`RelaunchAsAdmin`) passes `--connect <profileID>`, shuts the parent
  down, and `G/app.go:771-780` (`parseConnectArg`) restores it in the
  child. `G/elevate_windows.go:36-64` is the `ShellExecuteW` `runas`
  call; `G/systools_windows.go:15-28` is the elevation probe.
- Failure annotation is separate and preserves the core's wording:
  `G/app.go:700-724`.
- Outside Windows elevation is a no-op (`G/elevate_other.go`), with no
  sudo-password path for the core.

### Status

- **DIFFERENT** — on Windows NekoGo's connect-time pre-flight plus
  resumed connection is the stronger flow; on Linux/macOS PattN has a
  capability NekoGo lacks.

### Adopt?

- no on Windows; the Linux sudo path is out of scope for a
  Windows-first port.

## 10. Error handling

### PattN approach

- A failed start is classified, not flattened:
  `P/Aether/AetherCoreManager.cs:1040` (`CouldNotStart`) covers "died
  inside the 1 s window", "no output at all" and the DLL-not-found
  exit code; `:1023` (`StoppedMessage`) covers a core that ran and then
  ended, quoting its last error line.
- The verdict is pushed, not polled: `SessionEnded` (`:455`) reaches
  `P/Manager/CoreManager.cs:595-604`, which stops the group through the
  generation guard and then calls the host callback
  (`AetherCoresStopped`, `:29`). The WebAPI turns that into published
  runtime state — `W/Services/V2rayRuntime.cs:1216-1228`
  (`OnAetherCoresStoppedAsync` faults the current state and publishes
  `status`) — wired at `:290`, with a start-in-flight notice in
  `_aetherStopNotice`.

### NekoGo today

- Aether failures are one sentence: `G/aether_manager.go:196` returns
  `"aether engine listener on port %d did not come up in %s"` whether
  the engine never started, died at once, or is simply slow. The
  process exit itself is only logged (`:231-241`).
- State is polled: `G/core_manager.go:838-843` (`Status`) and
  `G/app.go:145-150` (`LastError`) are what the UI reads; nothing
  faults them when a child dies.
- The two main cores at least fail fast on an instant exit
  (`G/singbox_manager.go:272-290`).

### Status

- **BEHIND** — the user sees a timeout, not the core's own last words,
  and the runtime state keeps claiming "running" after the engine died.

### Gap (if BEHIND)

- What is missing: crash-vs-stop classification, the child's last
  error lines, and a faulted state that the UI can poll (or a push
  event).
- Where it would land: `G/aether_manager.go:196` (message) and
  `G/aether_manager.go:231` (death), surfaced through
  `G/core_manager.go:838` / `G/app.go:145`.
- Risk: low.

### Adopt?

- yes — cheap, and it removes the most misleading failure mode.

## 11. Port pre-flight and env sanitisation

### PattN approach

- Every Aether port is bind-tested before Xray starts, by TCP **and**
  UDP for the secondary-socks port:
  `P/Aether/AetherCoreLoad.cs:128-156` (`BeforeXrayAsync`, retried for
  `PortReleaseWait` = 1 s, `:92`), `:163-190` (`PortsProblem` naming the
  first bad port), `:215-240` (`CanListen`; a Windows excluded port
  range answers "access denied" and is reported as such).
- Invalid or contradictory listener settings are named before any
  spawn (`:163` via `InvalidPort`, unreachable listener and shared
  port checks), and a missing core is refused earlier still
  (`:102-121`, `BeforeStopAsync`).
- The child environment is sanitised: inherited `AETHER_*` and
  `RUST_LOG` are removed (`P/Aether/AetherProcess.cs:158-161`,
  `:241-245`) before PattN's own paths, Psiphon dir/binary and
  `NO_COLOR` are set (`:162-174`), so the core runs exactly as its
  arguments say.
- Missing DLL is treated as a first-class start failure
  (`P/Aether/AetherProcess.cs:90`, `P/Aether/AetherCoreManager.cs:1040`).

### NekoGo today

- A free port is obtained by bind-and-hand-over
  (`G/singbox_manager.go:206-215`) and the engine is trusted to take
  it; there is no UDP check and no excluded-range check
  (`G/aether_manager.go:158-166`).
- Xray has a single-address TCP pre-flight with a zombie-core hint
  (`G/xray_manager.go:120-137`).
- Readiness is a real SOCKS handshake probe rather than a port check
  (`G/aether_manager.go:343-365`).
- `G/aether_manager.go:132-145` builds the child env by appending to
  `os.Environ()` and removes nothing, so an inherited `AETHER_*` or
  `RUST_LOG` in the user's session silently overrides the arguments.

### Status

- **BEHIND** — the env leak is a concrete correctness bug (a user who
  also runs the engine by hand can change what NekoGo starts), and the
  port pre-flight is weaker.

### Gap (if BEHIND)

- What is missing: removing `AETHER_*`/`RUST_LOG` from the inherited
  env before appending; a bind test of the chosen port (TCP, plus UDP
  for the secondary port) with a clear message.
- Where it would land: `G/aether_manager.go:132` (env) and `:147-166`
  (pre-flight), or a new `aether_ports.go`.
- Risk: low.

### Adopt?

- yes — the env strip is a few lines and fixes a real
  argument-vs-environment conflict.

## 12. Wintun / stale adapter sweep

### PattN approach

- `P/Common/WindowsUtils.cs:64-84` derives the adapter GUID from
  `MD5("wintunsingbox_tun")` and `MD5("xray_tun")` and removes each
  device with `pnputil /remove-device SWD\Wintun\{guid}`, swallowing
  errors.
- It is called unconditionally on the Windows TUN path right before a
  start (`P/Manager/CoreManager.cs:200`), which hints at real
  "adapter still held after a crash" reports but does not prove the
  cause. It needs admin, so it silently no-ops when unelevated.

### NekoGo today

- Nothing: `G/orphans.go` reaps processes only (`:40-108`), and no
  `pnputil`/`wintun` call exists in `gui/` outside the TUN pre-flight
  comments (`G/app.go:598`, `:687`). NekoGo also does not set
  `interface_name` on its TUN inbound (`G/singbox_manager.go:936-945`),
  so the adapter name is whatever sing-box derives.

### Status

- **BEHIND** — but the value is unproven; see §17.

### Gap (if BEHIND)

- What is missing: a sweep of a stale Wintun device before a TUN start.
- Where it would land: new file (`gui/wintun_windows.go`) called from
  `G/singbox_manager.go:936`.
- Risk: medium — `pnputil` is destructive if the GUID derivation is
  wrong, and it fails quietly when unelevated.

### Adopt?

- later — only after reproducing a stale-adapter failure; the correct
  device name cannot be asserted from source alone.

## 13. Other patterns found

1. **Bind short-lived cores to the physical uplink.** PattN finds the
   default route with the lowest metric that is up and not a TUN and
   binds the outbounds of its throwaway cores to it
   (`P/Aether/AetherUplink.cs:33-40`, `:5-19`), because under a TUN
   their DNS/ECH lookups would otherwise be captured by the tunnel
   rule. NekoGo has no equivalent, but its Xray never renders a TUN
   (`G/app.go:677-680`) and its ephemeral cores run on a mixed port, so
   the exposure is narrower. Where: `G/xray_manager.go:672` if TUN
   ever reaches Xray. Risk: low today, medium later.
2. **Kill-on-close job object for every child.**
   `P/Services/WindowsJobService.cs:4-56` guarantees no core outlives
   the GUI even on a hard kill; NekoGo relies on its startup sweep
   (`G/orphans.go:47`). Where: `G/core_manager.go:144` (attach at
   spawn). Risk: low.
3. **Validate the engine binary before spawning.**
   `P/Aether/AetherBinaries.cs:108` refuses a non-runnable file and
   `:24-36` fixes the folder layout; NekoGo resolves the path
   (`G/aether_manager.go:86-112`) and lets `exec` fail. Risk: low.
4. **Reserved-port arithmetic.** `P/Aether/AetherPorts.cs:10-23`
   documents the listen port + 3 window and `:55-83` keeps it clear of
   the app's `L..L+6` local ports; NekoGo asks the kernel for a free
   port instead (`G/singbox_manager.go:206`), which is simpler but
   cannot guarantee the engine's whole window is free. Risk: low.
5. **NekoGo-only strengths worth keeping:** the SNI front
   (`G/sni_front.go`), the global transport override that layers over
   per-profile values (`G/mux_fragment.go:252`, `:271`), and the
   `epoch` start/stop race guard (`G/core_manager.go:15-30`).

## 14. Summary table

| Area | NekoGo status | Risk if adopted | Where it lands |
|------|---------------|-----------------|----------------|
| 1. Fragment | AHEAD | n/a | — |
| 2. TUN / tunnel | BEHIND | low (drop rule) / medium (IPv6) | `G/singbox_manager.go:896` |
| 3. Aether | BEHIND | medium | `G/aether_manager.go:231` |
| 4. SNI Spoofing | NekoGo-only, N/A | N/A | N/A |
| 5. Core lifecycle | BEHIND | medium | `G/core_manager.go:311` |
| 6. Config generation | DIFFERENT | low | `G/singbox_manager.go:896` (sniff only) |
| 7. Profile storage | DIFFERENT | low | `G/config.go:226` |
| 8. Subscription handling | DIFFERENT | low | `G/app.go:1607` |
| 9. Elevation / admin | DIFFERENT | low | `G/app.go:743` |
| 10. Error handling | BEHIND | low | `G/aether_manager.go:196` |
| 11. Port pre-flight / env | BEHIND | low | `G/aether_manager.go:132` |
| 12. Wintun sweep | BEHIND | medium | new file |
| 13. Other patterns | DIFFERENT | low–medium | see §13 |

## 15. Top 5 patterns to adopt first

1. **Engine death tears down the connection group.**
   `P/Aether/AetherCoreManager.cs:1370` + `P/Manager/CoreManager.cs:595`
   via the generation guard (`P/Aether/AetherCoreLoad.cs:405-455`).
   Why it matters: an `aether.exe` crash today leaves sing-box/Xray and
   the system proxy dialling a dead SOCKS port until the user
   disconnects (`G/aether_manager.go:231`). Landing:
   `G/core_manager.go:172-200` / `:311`. Risk: medium.
2. **Classify a failed start and quote the core's last words.**
   `P/Aether/AetherCoreManager.cs:1040` (`CouldNotStart`) and `:1023`
   (`StoppedMessage`). Why it matters: replaces the single
   `"listener ... did not come up in 30s"` sentence
   (`G/aether_manager.go:196`) with "DLL missing", "died at once" or
   "ended after N s, last error: …". Landing:
   `G/aether_manager.go:196`. Risk: low.
3. **Own leftovers by pid + start time (+ boot ticks).**
   `P/Aether/AetherProcess.cs:307-345`, `:394-407`, `:467`, reaped at
   `P/Aether/AetherCoreLoad.cs:134`. Why it matters: NekoGo's sweep
   knows only the two core image names and decides ownership by parent
   pid (`G/orphans.go:43`, `:88`), so a crashed GUI leaves an
   `aether.exe` holding its listener with nothing able to prove it is
   ours. Landing: `G/orphans.go:43` and a pid ledger around
   `G/aether_manager.go:147`. Risk: medium.
4. **Drop traffic addressed to the TUN's own addresses.**
   `P/Services/CoreConfig/Singbox/SingboxRoutingService.cs:52-68`.
   Why it matters: with `auto_route` on (`G/singbox_manager.go:943`),
   a `direct` packet to `172.19.0.1/24` re-enters the inbound and can
   pin a CPU core. Landing: `G/singbox_manager.go:896-899`. Risk: low.
5. **Sanitise the engine environment and pre-flight its ports.**
   `P/Aether/AetherProcess.cs:158-161`/`:241-245` and
   `P/Aether/AetherCoreLoad.cs:128-156`/`:215`. Why it matters: an
   inherited `AETHER_*`/`RUST_LOG` silently overrides NekoGo's
   arguments (`G/aether_manager.go:132-145`), and a port taken between
   the bind test and the engine's listen reports as a 30 s timeout
   rather than an occupied port. Landing: `G/aether_manager.go:132` and
   `:147`. Risk: low.

## 16. Patterns NOT worth adopting

1. Interpolated SQL for deletes (`P/Handler/ConfigHandler.cs:2401`,
   `:2405`) — the subscription id goes straight into the statement.
2. A global-only fragment switch (`P/Models/Configs/Config.cs:32`) —
   NekoGo's per-profile plus global override is strictly more useful.
3. SQLite entity tables for a single-user desktop app
   (`P/Helper/SqliteHelper.cs`) — NekoGo's one atomic JSON file is
   easier to back up and inspect.
4. A generic `ProcessService` that takes one argument string
   (`P/Services/ProcessService.cs:13-24`, `:56-63`) — NekoGo's
   per-engine managers avoid the quoting class of bugs PattN had to
   escape by writing `AetherProcess` separately
   (`P/Aether/AetherProcess.cs:76-83`).
5. Treating "alive after 100 ms" as "core started"
   (`P/Manager/CoreManager.cs:495-521`) — NekoGo already probes real
   readiness (`G/singbox_manager.go:272-290`,
   `G/aether_manager.go:196`).
6. Storing the full config template in the database
   (`P/Manager/AppManager.cs:340-346`, merged at
   `P/Services/CoreConfig/Singbox/SingboxConfigTemplateService.cs:86`) —
   NekoGo's section overrides plus one raw-config key
   (`G/app.go:2232`, `:2264`) are easier to validate per section.
7. The Linux sudo-password core path
   (`P/Manager/CoreAdminManager.cs:32-69`) — no Windows equivalent and
   out of scope for this port.
8. Delete-then-re-add subscription refresh
   (`P/Handler/ConfigHandler.cs:2189-2197`) — it loses per-profile
   edits; NekoGo's dedupe key (`G/import.go:552-572`) and
   `SubURL` stamping (`G/app.go:1514-1517`) do better.

## 17. Open questions

- Whether the bundled `aether.exe` honours PattN's pid-file contract
  (`P/Aether/AetherProcess.cs:307-345`). It is an external binary; the
  C# side proves only what PattN writes and reads.
- Whether `aether.exe` survives parent death on Windows in this build.
  If it does not, §15.3 is moot; the crash-teardown in §15.1 is needed
  either way.
- Whether NekoGo needs the Wintun sweep at all: sing-box may recreate
  the adapter on demand, and NekoGo does not pin `interface_name`
  (`G/singbox_manager.go:936-945`), so the
  `MD5("wintunsingbox_tun")` GUID PattN removes
  (`P/Common/WindowsUtils.cs:66-74`) may never be NekoGo's adapter.
- Whether the TUN self-address loop is actually reachable in NekoGo:
  it needs a `direct` outbound for an address inside `172.19.0.1/24`,
  and NekoGo's default route has no such rule — reachable through a
  user section override (`G/app.go:2232`), not by default.
- How NekoGo's frontend should learn about a faulted engine state: the
  WebAPI pushes an event (`W/Services/V2rayRuntime.cs:1216-1228`),
  while NekoGo's surfaces poll (`G/core_manager.go:838`,
  `G/app.go:145`); a push event versus a polled flag could not be
  decided from source alone.
- Whether the per-feed `AutoUpdateInterval` and address/port overrides
  from PattN fit NekoGo's update model, which currently has one global
  interval (`G/types.go:307-310`) and no override concept.
- Whether `pnputil /remove-device` on a device another VPN owns can
  disrupt an unrelated tunnel; the GUID derivation makes that
  unlikely but not impossible.
