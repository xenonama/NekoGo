# PattN vs NekoGo — Deep Dive (patterns the first report missed)

(Read-only audit. No NekoGo file was changed. This is a follow-up to
PATTN_VS_NEKOGO_GAP_ANALYSIS.md. The audited tree is PattN — the
Windows C# fork of v2rayN at `PattN/v2rayN/` — not the Android Kotlin
PattNG. PattN does NOT have SNI Spoofing; that is NekoGo-only and out
of scope here.)

Path prefixes: `P = PattN/v2rayN/ServiceLib`,
`W = PattN/v2rayN/v2rayN.WebAPI`, `G = gui` (NekoGo).

## 0. Source locations

- **NekoGo root:** `C:\Users\pc\Desktop\nekobox-main` — Go module in
  `gui/`, libbox/Thrift core server in `core/server/`.
- **PattN root:** `C:\Users\pc\Desktop\nekobox-main\PattN\v2rayN` —
  `ServiceLib/`, `v2rayN.WebAPI/`, WPF/Avalonia frontends, tests.
- **First report:** `aether-reference/PATTN_VS_NEKOGO_GAP_ANALYSIS.md`.

## 1. Process supervision and restart policy

### PattN approach

- There is **no automatic restart of a crashed core**. `WatchAsync`
  (`P/Aether/AetherCoreManager.cs:1370-1419`) awaits the exit and
  raises `SessionEnded`; `P/Manager/CoreManager.cs:595-604` reacts by
  stopping the group and reporting. Nothing schedules a retry — no
  backoff, attempt counter or retry timer appears in
  `ServiceLib/Manager` or `ServiceLib/Aether`.
- What PattN does instead is *engine-level* resilience: it passes
  `--quick-reconnect` to the core on normal runs and never on scans
  (`P/Aether/AetherArguments.cs:250`), so reconnection is the engine's
  job, not the supervisor's.
- Host-restart recovery is separate and real: the WebAPI persists a
  runtime intent and, on start, reconnects the profile that was
  running (`W/Services/V2rayRuntime.cs:305-331`,
  `RuntimeRestartRecovery.ShouldRecoverCore` / `RecoverAsync` at
  `:307-323`), and a 1-minute scheduler keeps state flushed
  (`P/Manager/TaskManager.cs:23-56`).

### NekoGo today

- Same policy: no restart. `G/singbox_manager.go:361-391`,
  `G/xray_manager.go:317-343` and `G/aether_manager.go:231-241` each
  reap the child, flip their own flag and log; the startup probe is
  the only place a death is turned into an error
  (`G/singbox_manager.go:285-290`).
- Engine-level reconnect is already at parity:
  `G/aether_core.go:177` adds `--quick-reconnect`, and scans add
  `--no-quick-reconnect` (`G/aether_scan.go:75`, `:97-98`).
- A persisted auto-connect target exists and is cleaned when the
  profile is deleted (`G/app.go:450-459`), so a relaunch can resume the
  last connection, but there is no "was running when the GUI died"
  record beyond it.

### Status

- **DIFFERENT** — identical crash policy (none), different recovery
  after a host restart: PattN records a runtime intent, NekoGo only a
  target profile.

### Adopt?

- no — adding a restart loop would hide exactly the crash the first
  report wants surfaced; the intent record is only useful if NekoGo
  ever becomes a service or tray-resident.

## 2. Port allocation and conflict resolution

### PattN approach

- Ports are **derived**, not probed: one base port plus a fixed enum
  offset produces mixed, SOCKS, PAC, API and the second API
  (`P/Manager/AppManager.cs:167-171`), so the app's own inbounds can
  never collide with each other; the window `L..L+6` is spelled out in
  `P/Aether/AetherPorts.cs:49-55`.
- When the preferred base port is taken, nothing remaps it: there is no
  "address already in use" handler in `ServiceLib` (the only retry
  loops are the proxy-readiness wait, `P/Manager/CoreManager.cs:373-435`,
  and the downloader's proxy retry, `P/Services/DownloadService.cs:405`).
  The core fails to bind and the user is told.
- Probed free ports are used only where a *random* port is acceptable:
  the API ports (`P/Manager/AppManager.cs:19`, `:28`) through
  `Utils.GetFreePort` (`P/Common/Utils.cs:816-835`, which falls back to
  `59090` when even the wildcard bind fails), and Aether scan ports
  (`P/Aether/AetherPorts.cs:130-160`).
- Aether's listen port is validated against that window *before* it is
  saved (`P/Aether/AetherPorts.cs:70-83`, called from
  `P/Aether/AetherEditorSource.cs:68`).

### NekoGo today

- The mixed port is a stored setting (`G/types.go:237`), refused above
  65534 so the SOCKS `+1` still fits (`G/singbox_manager.go:877-880`),
  and validated again on save (`G/app.go:463-467`). It is not remapped
  when taken; the core's bind error is the result
  (`G/singbox_manager.go:285-290`).
- Random ports are used for the Aether engine (`G/aether_manager.go:158-166`
  via `freePort`, `G/singbox_manager.go:206-215`), the upstream clash
  API (`G/upstream_singbox_manager.go:114`), and ephemeral probes.
- The Aether reserved window has no equivalent: nothing keeps the
  engine out of the app's own port range.

### Status

- **EQUAL** — both fail honestly on a taken fixed port; PattN's
  derived scheme is a tidiness win, not a missing capability.

### Adopt?

- no — NekoGo already derives SOCKS as `MixedPort+1`; the Aether
  window arithmetic is a safety net for a case NekoGo has not hit.

## 3. Config diffing and hot-reload

### PattN approach

- No diffing and no hot reload: every apply is a full stop/start inside
  the turn, guarded by a generation (`P/Manager/CoreManager.cs:146-203`,
  `P/Aether/AetherCoreLoad.cs:405-455`). A changed section is never
  isolated; the whole core is replaced.
- Ordering is deliberate: the engine is stopped first so a reload's old
  core ends before Xray restarts (`P/Manager/CoreManager.cs:302`), and a
  TUN toggle forces a reload because "launch mode must always agree,
  even if TUN is toggled while a reload is in flight"
  (`P/Manager/CoreManager.cs:450`).
- A reload that fails after Xray started is rolled back rather than
  left half-up (`P/Manager/CoreManager.cs:211`,
  `AetherCoreLoadOutcome.AetherFailed`), and settings that only take
  effect at start are reported as such instead of being silently
  ignored (`W/Contracts/ApiModels.cs:451`, `settings.savedRestartRequired`).

### NekoGo today

- `ReloadCoreNow` stops the active core and reconnects the active
  profile (`G/app.go:2679-2695`), behind the same epoch guard the start
  path uses (`G/core_manager.go:15-30`, `:218-230`); the Aether engine
  is started before and stopped with the group
  (`G/core_manager.go:166-200`, `:314-338`).
- No diff, no section hot-apply; the raw-config and section overrides
  are re-rendered from scratch on the next start
  (`G/app.go:2232-2284`).

### Status

- **EQUAL** — both restart wholesale; PattN's extra value is the
  explicit "restart required" answer and the post-Xray rollback.

### Adopt?

- no for diffing; the "saved, restart required" distinction is worth
  copying into NekoGo's settings result if users report silent no-ops.

## 4. Connection-level observability

### PattN approach

- The desktop side talks to sing-box's clash API through
  `P/Manager/ClashApiManager.cs`: proxies (`:11`), delay test (`:61`),
  switch active proxy (`:95`), clash mode (`:110`), connection list
  (`:166`) and close-connection (`:184`). The API block is rendered by
  `P/Services/CoreConfig/Singbox/SingboxStatisticService.cs:10`
  (`experimental.clash_api` in `P/Models/CoreConfigs/SingboxConfig.cs:290`).
- Depth comes from the DTO: `P/Models/Dto/ClashConnections.cs:3-30`
  carries per-connection `upload`, `download`, `start`, `chains`,
  `rule`, `rulePayload`, and metadata including `processPath` and
  `sniffHost` (`:12-30`).
- The desktop view polls it (`P/ViewModels/ClashConnectionsViewModel.cs:48`,
  `:119`, `:146`). The headless backend does **not** expose it: the
  WebAPI maps only `/api/status` (`W/Api/WebApiEndpoints.cs:23`), so a
  remote client gets totals, not the connection table.

### NekoGo today

- Connection listing exists end to end: `G/app.go:1013` →
  `G/core_manager.go:484` → `G/singbox_manager.go:797`, with a
  500-entry cap flagged as `Truncated` (`G/types.go:227-231`,
  `G/singbox_manager.go:820`), plus an upstream-manager variant
  (`G/upstream_singbox_manager.go:337`).
- The model is shallow: `G/types.go:221-231` has id, outbound, network,
  dest, protocol, domain, process, upload, download — no `chains`, no
  matched `rule`/`rulePayload`, no `start`, no `processPath`/`sniffHost`.
  The inspector UI therefore shows only outbound and byte counters
  (`G/frontend/src/components/ConnectionInspector.vue:16-27`).

### Status

- **BEHIND** — the rule that routed a connection and the chain it took
  are exactly what a user needs to debug a "wrong node" report, and both
  are available from the same clash payload PattN reads.

### Gap (if BEHIND)

- What is missing: `chains`, `rule`, `rulePayload`, `start` in the
  connection model and the inspector.
- Where it would land: `G/types.go:221` (model),
  `G/singbox_manager.go:797` (mapping) and
  `G/frontend/src/components/ConnectionInspector.vue:16` (display).
- Risk: low.

### Adopt?

- yes — property mapping only, no new subsystem.

## 5. Subscription refresh strategy

### PattN approach

- Refresh is scheduled per feed, in minutes: a 1-minute timer
  (`P/Manager/TaskManager.cs:23-31`) selects every feed whose
  `AutoUpdateInterval > 0` and whose `UpdateTime` is old enough
  (`:88-104`), updates it, then stamps `UpdateTime` and saves
  (`:104-112`) with a 1 s gap between feeds.
- Transfers are unconditional: `P/Handler/SubscriptionHandler.cs:102-113`
  tries through the proxy and falls back to direct, sending only a
  `UserAgent` (`:155`); no ETag or `If-Modified-Since` is sent for a
  feed, so a 304 is never in play.
- A working conditional GET exists elsewhere in the same tree for the
  Psiphon server list: `P/Aether/PsiphonServersUpdate.cs:34`
  (`NotModified` in the result), `:130-131` (`If-Modified-Since`), `:173`
  (`HttpStatusCode.NotModified` keeps the current list).
- Merge is delete-then-re-add per feed with the active profile
  re-selected by id (`P/Handler/ConfigHandler.cs:2189-2197`), so there
  is no duplicate problem and every local per-profile edit is lost.

### NekoGo today

- One global interval only (`G/types.go:307-310`), consumed by
  `UpdateSubscriptions` (`G/app.go:1388-1441`); a per-feed flag pauses a
  feed (`G/import.go:24-41`, `:45`) but not its own schedule.
- Fetching is unconditional (`G/import.go:451-482`), with the quota
  header parsed on the same response (`:470`).
- Reconcile is dedupe-based rather than delete-first
  (`G/import.go:552-572`), with `SubURL` stamping
  (`G/app.go:1353`, `:1418`, `:1514-1517`) and delete-with-profiles
  (`G/app.go:1553-1606`).

### Status

- **DIFFERENT** — NekoGo is ahead on reconciliation and quota, PattN on
  scheduling and on having a conditional-GET implementation at all.

### Gap (if BEHIND)

- Not BEHIND overall, but two pieces are missing: a per-feed interval
  (PattN `P/Models/Entities/SubItem.cs:25`, consumed at
  `P/Manager/TaskManager.cs:90-92`) and a conditional GET with 304
  handling (`P/Aether/PsiphonServersUpdate.cs:130-173`).
- Where it would land: `G/types.go` (`Subscription`), `G/import.go:451`
  (request headers) and `G/app.go:1388` (selection).
- Risk: low.

### Adopt?

- yes — a feed that has not changed costs one header-only round trip
  instead of a full body, and per-feed intervals are what providers
  actually expect.

## 6. DNS handling beyond the basic block

### PattN approach

- FakeIP is **on by default** (`P/Handler/ConfigHandler.cs:131-132`,
  and again when the simple DNS item is seeded at `:3108-3109`), with
  the pool from `P/Global.cs:124-125` (`198.18.0.0/15`, `2001:2::/48`),
  a bundled filter list (`P/Global.cs:47`) and the sing-box switch
  `store_fakeip` (`P/Models/CoreConfigs/SingboxConfig.cs:318`).
- The fakeip server object and its rules are generated with the rest:
  server at `P/Services/CoreConfig/Singbox/SingboxDnsService.cs:138`,
  fakeip rules at `:281` and `:432`, alongside the normal rule set at
  `:152-300`.
- Split DNS is rule-driven, not two fixed servers: routing-aware rules
  choose remote vs direct per domain/rule-set
  (`P/Services/CoreConfig/Singbox/SingboxDnsService.cs:294`, `:695`),
  and the final tag follows the routing item (`:24`, `:537`).

### NekoGo today

- No fakeip anywhere in `gui/`: the DNS block is two typed servers with
  `detour: direct` on the direct one (`G/singbox_manager.go:1010-1043`),
  a TUN fallback block (`:929-935`, `:1049-1056`) and a typed parser
  (`:1061-1094`). Split behaviour comes only from the two server
  entries, not from domain rules.

### Status

- **BEHIND** — fakeip, the fakeip filter and domain-split DNS are all
  absent; with TUN on, every query goes to the same remote resolver.

### Gap (if BEHIND)

- What is missing: a fakeip server + rules, the filter list, and
  domain/rule-set split between the remote and direct resolvers.
- Where it would land: `G/singbox_manager.go:929-935` (DNS block) or a
  new `gui/dns.go`; the TUN hijack at `:893-895` is already the right
  insertion point.
- Risk: medium — fakeip changes what the core hands to applications and
  interacts with the TUN route rules and NekoGo's own country detection
  (`G/geoip.go:134`), so it needs a real TUN test, not a unit test.

### Adopt?

- yes for split DNS (low risk, immediate benefit on TUN); fakeip only
  if TUN users report DNS-based leaks.

## 7. Routing rule generation

### PattN approach

- Routing is **seeded**, not left to the user: `InitBuiltinRouting`
  (`P/Handler/ConfigHandler.cs:2875-2955`) inserts four templates from
  embedded JSON — whitelist ("bypass the mainland"), blacklist, global
  and an Iran-direct set — plus the import path for advanced rules
  (`:2669`). A restore re-seeds them (`W/Services/V2rayRuntime.cs:265-269`).
- Rules travel as a JSON string on the routing item
  (`P/Models/Entities/RoutingItem.cs:11`), are parsed in the render
  context (`P/Handler/Builder/CoreConfigContextBuilder.cs:78-80`) and
  geo entries are converted to sing-box rule sets
  (`P/Services/CoreConfig/Singbox/SingboxRulesetService.cs:5`).
- There is no rule-set *updater*: only the routing template is seeded,
  and only geo files are refreshed, on the hourly tick
  (`P/Manager/TaskManager.cs:118-127`).

### NekoGo today

- There are no built-in routing templates: the route is the DNS rule,
  the optional TUN rules and `final: proxy`
  (`G/singbox_manager.go:896-899`), and anything else must come from a
  user section override (`G/app.go:2232-2252`) or a raw config
  (`:2264-2284`). The UI edits that override
  (`G/frontend/src/components/RoutingRules.vue`).

### Status

- **BEHIND** — a user who never opens the advanced editor has no
  "private IPs direct, ad domains blocked, everything else proxied"
  baseline.

### Gap (if BEHIND)

- What is missing: seeded rule sets (direct for RFC1918/loopback/
  link-local, a small ad/tracker block list, proxy for the rest) and a
  template picker.
- Where it would land: new `gui/routing_defaults.go` merged at
  `G/singbox_manager.go:896-899`; a picker in `RoutingRules.vue`.
- Risk: low — rules are additive and user overrides still win.

### Adopt?

- yes — highest value per line for new users.

## 8. Error classification for the UI

### PattN approach

- Errors are localized `ResUI` strings raised through a snackbar/message
  bus (`P/Manager/NoticeManager.cs:8-23`), and the Aether path
  classifies before it speaks (`P/Aether/AetherCoreManager.cs:1040`
  crash-vs-start, `:1023` stopped-with-last-error).
- Machine-readable classification exists only at the WebAPI boundary:
  `W/Contracts/ApiModels.cs:440-455` defines stable keys
  (`core.startFailed`, `settings.savedRestartRequired`,
  `errors.invalidInput`, …) that clients can branch on.
- There is no retry button logic anywhere: nothing in `ServiceLib`
  offers a retry for a transient error, because nothing retries at all
  (see §1).

### NekoGo today

- Errors are free-form sentences built at the failure site
  (`G/aether_manager.go:186-190`, `:196`) and exposed as one string
  through `LastError` (`G/app.go:145-150`) and the `Status` tuple
  (`G/core_manager.go:838-843`). There is no category, no code, and no
  distinction between permanent (missing binary) and transient (port
  busy) beyond the text itself.

### Status

- **BEHIND** — the frontend cannot branch on a category, so it can only
  print the sentence.

### Gap (if BEHIND)

- What is missing: a small category (binary / port / permission /
  config / network) and a code on the error surface, so a UI can offer
  "Relaunch as admin" for permission and "Open settings" for a port
  clash without string matching.
- Where it would land: `G/aether_manager.go:196` and
  `G/app.go:145` (return shape), plus the call sites that render it.
- Risk: low.

### Adopt?

- yes, minimally — a category field is enough; the WebAPI key catalog is
  more than a local Wails app needs.

## 9. Elevation model beyond UAC

### PattN approach

- No Windows service and no privilege drop: the whole GUI is relaunched
  elevated (`P/Common/ProcUtils.cs:49-66`) and stays elevated for its
  lifetime; the TUN toggle refuses to turn on when unelevated and
  starts that relaunch (`P/ViewModels/StatusBarViewModel.cs:441-460`).
  A search for `ServiceController`, `sc create` or `CreateService`
  across `ServiceLib` finds nothing.
- Outside Windows the core runs through a sudo password held by
  `P/Manager/CoreAdminManager.cs:32-69`, which is the closest thing to
  a privileged helper in the tree — and it is a password pipe to
  `sudo`, not a separate service.

### NekoGo today

- Same shape: whole-GUI relaunch via `ShellExecuteW` `runas`
  (`G/elevate_windows.go:36-64`), an elevation probe
  (`G/systools_windows.go:15-28`), and a connect-time refusal with the
  admin remedy (`G/app.go:682-693`, `:700-724`). No service, no drop.

### Status

- **EQUAL** — both accept a permanently elevated GUI as the model.

### Adopt?

- no — there is nothing in PattN to copy here; a split-privilege helper
  would be new work in both.

## 10. Log handling and rotation

### PattN approach

- NLog writes one file per day into `guiLogs`, named from the local
  date (`P/Common/Logging.cs:12-21`, path built by
  `P/Common/Utils.cs:1314-1324`). A second logger exists only for
  exceptions (`P/Common/Logging.cs:9-10`, `:48-52`) and lands in the
  same target, so there is no separate core log file.
- Retention is a scheduled sweep, not NLog configuration: the hourly
  tick deletes log files older than 7 days, plus temp files and old
  test configs (`P/Manager/TaskManager.cs:58-65`). There is no size
  cap, no compression and no per-core separation.
- Core output is not written to that file directly; it is forwarded to
  the UI through the process callback (`P/Services/ProcessService.cs:56-71`).

### NekoGo today

- Logs are appended line by line to an optional file
  (`G/app.go:207`, `:221` → `G/logx.go:116-132`), which opens and closes
  per call so external rotation degrades safely, and are also kept in a
  2000-entry in-memory ring (`G/app.go:211-219`).
- Backpressure is handled: a full channel drops lines and counts them
  (`G/logx.go:149-160`), and one pending line is capped at 64 KiB
  (`:145`). There is no daily split and no retention sweep — the file
  grows until the user deletes it.

### Status

- **DIFFERENT** — NekoGo is ahead on backpressure and in-memory
  browsing; PattN is ahead on retention and on never letting one file
  grow unbounded.

### Gap (if BEHIND)

- Not BEHIND overall, but the retention piece is missing: a daily file
  name and a startup sweep of files older than N days (PattN does both,
  `P/Common/Logging.cs:18` and `P/Manager/TaskManager.cs:58-65`).
- Where it would land: `G/logx.go:116` (path/naming) and a sweep at
  startup near `G/app.go:207`.
- Risk: low.

### Adopt?

- yes — a single unconditional append file is the one log design that
  reliably ends in a multi-gigabyte `nekogo.log`.

## 11. Other patterns found

1. **Aether identity and key management.** PattN has a full
   register/renew subsystem: `P/Aether/AetherIdentityManager.cs:17-40`
   (protocol identity records, a renewal lock, 4-minute and 10-minute
   carrier timeouts), `:42` (`StatusAsync`), `:70`/`:110` (`RenewAsync`),
   `:129` (`Register`), `:159` (`RegisterTimeout` derived from the
   argument list), `:271` (`MissingAsync`, which files a launch needs)
  and `:327` (`SharesIdentity`). NekoGo only points the core at config
  paths (`G/aether_manager.go:132-145`) and never inspects, renews or
  reports a missing key; its argument builder emits run arguments only
  (`G/aether_core.go:38-264`), so there is no register/renew entry point
  at all. This is the largest uncovered capability in the audit.
2. **Host-restart recovery.** `W/Services/V2rayRuntime.cs:305-331`
   decides from a persisted `RuntimeRestartIntent` whether the core
   should be recomposed after the host restarts, and suppresses
   autostart until a first-run setup completes (`:307-313`). NekoGo
   has only the auto-connect target (`G/app.go:450-459`).
3. **Conditional GET with 304 handling.** `P/Aether/PsiphonServersUpdate.cs:130-173`
   (see §5) is a reusable implementation sitting in the tree.
4. **Scheduled maintenance with expiry sweeps.**
   `P/Manager/TaskManager.cs:23-78`: save every 20 minutes, delete
   expired files hourly (test configs 1 h, logs and temp 7 days), check
   for updates daily. NekoGo has targeted hooks instead
   (`G/app.go:207` for logs, `G/updater.go:505`/`:870` for components)
   and no expiry policy for temp artifacts.
5. **Machine-readable result keys.** `W/Contracts/ApiModels.cs:440-455`
   gives clients stable identifiers instead of prose
   (`core.startFailed`, `settings.savedRestartRequired`), which is the
   same idea as §8's error category but already enumerated.
6. **Connection close and proxy-group control.** PattN can close a
   connection and switch a proxy group from the desktop UI
   (`P/Manager/ClashApiManager.cs:95`, `:184`); NekoGo lists connections
   (`G/core_manager.go:484`) but exposes no close or group switch in the
   same surface.

## 12. Summary table

| Area | NekoGo status | Risk if adopted | Where it lands |
|------|---------------|-----------------|----------------|
| 1. Process supervision | DIFFERENT | low | `G/app.go:450` |
| 2. Port allocation | EQUAL | low | `G/singbox_manager.go:877` |
| 3. Config diffing / hot-reload | EQUAL | low | `G/app.go:2679` |
| 4. Connection observability | BEHIND | low | `G/types.go:221` |
| 5. Subscription refresh | DIFFERENT | low | `G/app.go:1388` |
| 6. DNS beyond the block | BEHIND | medium | `G/singbox_manager.go:929` |
| 7. Routing rule generation | BEHIND | low | `G/singbox_manager.go:896` |
| 8. Error classification | BEHIND | low | `G/aether_manager.go:196` |
| 9. Elevation model | EQUAL | n/a | — |
| 10. Log rotation | DIFFERENT | low | `G/logx.go:116` |
| 11. Other patterns | DIFFERENT | medium | see §11 |

## 13. Top 5 NEW patterns to adopt (not in the first report's top 5)

1. **Seeded routing templates.** `P/Handler/ConfigHandler.cs:2875-2955`
   (four templates from embedded JSON, re-seeded on restore at
   `W/Services/V2rayRuntime.cs:265-269`). Why it matters: NekoGo's route
   is `dns`, optional TUN rules and `final: proxy`
   (`G/singbox_manager.go:896-899`), so a new user gets no private-IP
   bypass and no ad blocking without hand-editing overrides. Landing:
   new `gui/routing_defaults.go`, merged at
   `G/singbox_manager.go:896-899`. Risk: low.
2. **Per-feed refresh intervals plus conditional GET.**
   `P/Manager/TaskManager.cs:88-104` and
   `P/Aether/PsiphonServersUpdate.cs:130-173`. Why it matters: NekoGo
   re-downloads every feed on one global timer
   (`G/types.go:307-310`, `G/app.go:1388`) and never sends
   `If-Modified-Since`, so a 1 MB feed is refetched whether or not it
   changed. Landing: `G/import.go:451` and `G/app.go:1388`. Risk: low.
3. **Connection depth: matched rule and chain.**
   `P/Models/Dto/ClashConnections.cs:3-30` versus NekoGo's
   `G/types.go:221-231`. Why it matters: "which rule sent this where"
   is the first question in a routing bug, and the data is already in
   the payload NekoGo reads. Landing: `G/types.go:221`,
   `G/singbox_manager.go:797`,
   `G/frontend/src/components/ConnectionInspector.vue:16`. Risk: low.
4. **Log retention (daily file + 7-day sweep).**
   `P/Common/Logging.cs:12-21` with `P/Manager/TaskManager.cs:58-65`.
   Why it matters: NekoGo's optional `LogOutputPath`
   (`G/types.go:286-289`) is a single append-only file with no cap
   (`G/logx.go:116-132`); it grows without bound. Landing:
   `G/logx.go:116` plus a startup sweep. Risk: low.
5. **Aether identity and key management.**
   `P/Aether/AetherIdentityManager.cs:42`, `:70`, `:129`, `:271`. Why it
   matters: NekoGo can start an engine whose key files are missing or
   expired and will only report the engine's own failure, while PattN
   knows which files a launch needs and can renew them before starting.
   Landing: new `gui/aether_identity.go`, called before
   `G/aether_manager.go:147`. Risk: medium-high — it writes credential
   files and depends on the external core's key format, which cannot be
   proven from this tree.

## 14. Patterns NOT worth adopting

1. Restart-on-crash with backoff — PattN does not have it either, and it
   would hide the crash the first report wants surfaced.
2. Config diffing / partial hot-reload — sing-box and Xray both need a
   restart for most sections; the generation-guarded stop/start already
   covers the race.
3. A Windows service for TUN — no such code exists in PattN, so it is
   not a "pattern to copy", it is new work in both products.
4. The 1-minute always-on `TaskManager` loop
   (`P/Manager/TaskManager.cs:23-31`) — NekoGo's targeted hooks
   (`G/app.go:207`, `G/updater.go:505`) do the same work without a
   permanent timer.
5. The `NoticeManager` snackbar bus (`P/Manager/NoticeManager.cs:8-23`)
   — Wails already carries events to the frontend; a second bus would
   duplicate the channel NekoGo uses.
6. NLog as a dependency — a date-stamped path plus a sweep covers the
   whole requirement in a few lines of Go.
7. A permanently elevated GUI — both products do it, but copying it
   deliberately is not an improvement over what NekoGo already has.
8. FakeIP for every profile — PattN applies it globally by default
   (`P/Handler/ConfigHandler.cs:131-132`), which changes what every
   application sees; adopt only for TUN, if at all.

## 15. Open questions

- Whether the bundled `aether.exe` in NekoGo accepts PattN's identity
  key-file layout and the engine mode PattN drives
  (`P/Aether/AetherIdentityManager.cs:129`, `:271`). PattN writes and
  renews key files through the core; NekoGo only builds run arguments
  (`G/aether_core.go:38-264`), and the engine is an external binary, so
  the contract cannot be proven from either tree.
- Whether `If-Modified-Since` is honoured by typical subscription
  providers, or whether ETag is the only header that works; PattN's
  implementation proves the mechanism, not provider behaviour.
- Whether NekoGo's clash payload (sing-box 1.14) actually carries
  `rulePayload` and `chains` for every connection type, or only for
  rule-matched ones; PattN's DTO assumes they are always present
  (`P/Models/Dto/ClashConnections.cs:12-30`).
- Whether fakeip is compatible with NekoGo's country detection
  (`G/geoip.go:134`) and its own DNS-based latency tests, which would
  see fake addresses instead of real ones.
- Whether a per-feed interval needs a scheduler goroutine at all, or
  whether the existing update call can hand out "due" feeds on demand.
- What NekoGo's retention policy should be (7 days is PattN's choice,
  `P/Manager/TaskManager.cs:60-65`; the file is single-user and mostly
  diagnostic).

## 16. Cross-reference to the first report

- **§1 Process supervision** — first report covered death handling in
  §5 and §10; this deep dive adds that **neither** product auto-restarts,
  that PattN pushes reconnection into the engine
  (`P/Aether/AetherArguments.cs:250`) and that it has host-restart
  recovery (`W/Services/V2rayRuntime.cs:305-331`).
- **§2 Port allocation** — first report covered pre-flight in §11; this
  deep dive adds how the ports are *derived* rather than probed
  (`P/Manager/AppManager.cs:167-171`) and that nothing remaps a taken
  base port in either product. NekoGo's save path does validate the
  stored port before use (`G/app.go:468`).
- **§3 Config diffing** — first report covered generation in §6; this
  deep dive adds that both products always stop/start, the reload
  ordering rule (`P/Manager/CoreManager.cs:302`, `:450`) and the
  "restart required" result (`W/Contracts/ApiModels.cs:451`).
- **§4 Connection observability** — not in the first report at all;
  entirely new, including the WebAPI gap (no `/connections`).
- **§5 Subscription refresh** — first report covered fetch/parse in §8;
  this deep dive adds per-feed scheduling (`P/Manager/TaskManager.cs:88-104`)
  and the conditional-GET precedent (`P/Aether/PsiphonServersUpdate.cs:130-173`).
- **§6 DNS** — first report mentioned fakeip only in passing under §2;
  this deep dive adds the default-on switch, the pool/filter, the
  generated fakeip rules and the split-DNS rules.
- **§7 Routing** — first report covered rule rendering in §6; this deep
  dive adds the seeded templates and the geo→rule-set conversion
  (`P/Handler/ConfigHandler.cs:2875-2955`,
  `P/Services/CoreConfig/Singbox/SingboxRulesetService.cs:5`).
- **§8 Error classification** — first report covered classification in
  §10; this deep dive adds the message-key catalog
  (`W/Contracts/ApiModels.cs:440-455`) and the absence of any retry
  affordance in both.
- **§9 Elevation** — first report covered the UAC flow in §9; this deep
  dive adds the negative findings: no Windows service, no privilege
  drop, and the sudo path as the only privileged helper.
- **§10 Logs** — not in the first report at all; entirely new.
- **§11 Other** — first report's §13 covered the uplink binding and the
  job object; this deep dive adds identity/key management,
  host-restart recovery, expiry sweeps and the machine-readable keys.
