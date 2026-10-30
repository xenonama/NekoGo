# Aether editor form (PattNG `ServerAetherActivity`)

Source: `PattNG/V2rayNG/app/src/main/java/com/v2ray/ang/ui/server/ServerAetherActivity.kt`
(lines 105-615), `ServerUiState.kt` (Aether fields), and `res/values/arrays.xml`
(Aether option values).

This is the field order and visibility logic to reproduce in the NekoGo editor.
Labels below are the English `values/strings.xml` names; the Android resource ID
is given in parentheses so the same key can be carried over.

## State defaults (new profile)

From `ServerUiState` constructor, lines 89-118:

| Field | Default |
|---|---|
| remarks | `""` (required) |
| address / port | `""` / `""` for Aether (not `10808`) |
| aetherProtocol | `wg` (`AetherProtocol.WIREGUARD`) |
| aetherTransport | `h3` |
| aetherMasqueSni | `www.cloudflare.com` |
| aetherScanMode | `balanced` |
| aetherObfuscation | `auto` |
| aetherFingerprint | `chrome` |
| aetherIpVersion | `v4` |
| aetherWiwOuter / aetherWiwInner | `""` / `""` |
| aetherFragment | `false` |
| aetherFragmentSize / aetherFragmentDelay | `""` / `""` |
| aetherEch | `false` |
| aetherEchDns | `udp://1.1.1.1` |
| aetherEchDomain | `cloudflare-ech.com` |
| aetherDns | `""` |
| aetherExitLoc | `""` |
| aetherExitNode | `""` (freedom) |
| finalMask / dialMode | `""` / `""` |
| aetherPsiphon | `off` |
| aetherPsiphonMode | `auto` |
| aetherPsiphonCdnIps / CdnSni / CdnSets / Region | `""` |
| aetherPsiphonBundledList | `true` |
| aetherTor | `off` |
| aetherTorBridges | `auto` |
| aetherTorBridgeLines | `""` |
| aetherTorRelays | `auto` |
| aetherCommand | `""` |

When an existing profile is loaded, enum strings are normalized with
`X.fromString(...).type`, the MASQUE SNI is shown as its default when null, and
ECH DNS/domain are shown filled in with their defaults
(`ServerUiState.fromProfileItem`, lines 433-464).

## Derived flags

```kotlin
val protocol   = AetherProtocol.fromString(aetherProtocol)
val psiphon    = AetherPsiphon.fromString(aetherPsiphon)
val psiphonMode= AetherPsiphonMode.fromString(aetherPsiphonMode)
val tor        = AetherTor.fromString(aetherTor)
val torBridges = AetherTorBridges.fromString(aetherTorBridges)

// No WARP tunnel at all when Psiphon or Tor is "only".
val warpUsed = psiphon != AetherPsiphon.ONLY && tor != AetherTor.ONLY

// MASQUE over HTTP/2: chosen, or forced by a carrier around the tunnel.
val overHttp2 = AetherArgs.masqueOverHttp2(
    protocol,
    AetherTransport.fromString(aetherTransport),
    tor,
    psiphon,
)

// The user explicitly chose HTTP/2 (gates Fragment ClientHello).
val usesHttp2 = protocol.overMasque &&
    AetherTransport.fromString(aetherTransport) == AetherTransport.HTTP2

// A scan must not open a second tunnel on the key of a live session.
val scanBlocked = liveSession?.disturbedByScanOf(protocol) == true
```

## Field order and visibility

1. **WARP keys button** — “Get new WARP keys…” (`aether_action_renew_key`);
   disabled while a scan runs. Opens the separate keys page.

2. **Remarks** — `server_lab_remarks`, required.

3. If `warpUsed`:
   1. **Protocol** dropdown (`aether_lab_protocol`), options/values:
      MASQUE/`masque`, WireGuard/`wg`, WARP-in-WARP/`gool`,
      MASQUE-in-MASQUE/`mim`, WireGuard over MASQUE/`wg-over-masque`.
   2. If `protocol.overMasque`:
      - **Transport** dropdown (`aether_lab_transport`): HTTP/3/`h3`,
        HTTP/2/`h2`.
      - **MASQUE SNI** text field (`aether_lab_masque_sni`), URI keyboard.
   3. If `usesHttp2`:
      - **Fragment ClientHello** switch (`aether_lab_fragment`).
      - If on: **Fragment size (bytes)** (`aether_lab_fragment_size`,
        placeholder `8-16`) and **Fragment delay (ms)**
        (`aether_lab_fragment_delay`, placeholder `2-10`).
   4. If `protocol.overMasque`:
      - **Encrypted Client Hello** switch (`aether_lab_ech`, summary
        `aether_hint_ech`).
      - If on: **ECH DNS** (`aether_lab_ech_dns`) and **ECH domain**
        (`aether_lab_ech_domain`) editable dropdowns. Options in
        `res/values/arrays.xml` (`aether_ech_dns_options`,
        `aether_ech_domain_options`); free text is accepted.
   5. **Scan mode** dropdown (`aether_lab_scan_mode`): `turbo`, `balanced`,
      `thorough`, `verified`, `ironclad`.
   6. If `!overHttp2`: **Obfuscation** dropdown (`aether_lab_obfuscation`):
      `auto`, `off`, `light`, `firewall`, `balanced`, `gfw`, `aggressive`.
      (Obfuscation shapes UDP; HTTP/2 takes none, so the field is hidden.)
   7. If `protocol.overMasque`: **Fingerprint** dropdown
      (`aether_lab_fingerprint`): `chrome`, `firefox`, `semi-python`, `go`.

4. **exit-node** dropdown (`aether_lab_exit_node`): `freedom (default)`
   (`aether_exit_node_freedom`) or one of the profiles that can be a chain hop
   (not an Aether profile). Hint `aether_hint_exit_node`.

5. **exit-node finalMask raw JSON** (`aether_lab_exit_final_mask`) and
   **exit-node dialMode** (`aether_lab_exit_dial_mode`). Both are enabled only
   while `aetherExitNode` is blank (freedom). The finalMask must be blank or a
   JSON object; a profile exit-node leaves both fields out of use.

6. **Psiphon** dropdown (`aether_lab_psiphon`): `off`, `chain`, `reverse`,
   `only`. If not `off`:
   - Warning if this build does not ship the Psiphon client
     (`aether_psiphon_unavailable`).
   - **Psiphon connection** dropdown (`aether_lab_psiphon_mode`):
     `auto`, `cdn`, `direct`.
   - If mode != `direct`:
     - **CDN fronting IPs** (`aether_lab_psiphon_cdn_ips`, placeholder
       `aether_hint_psiphon_list`).
     - If CDN IPs non-blank: **CDN fronting server names**
       (`aether_lab_psiphon_cdn_sni`).
     - **CDN edge lists** (`aether_lab_psiphon_cdn_sets`) in a collapsible
       group; none selected = all nine built-in lists. Values:
       `cloudflare`, `fastly`, `cloudfront`, `psiphon-akamai`,
       `psiphon-bunny`, `vercel`, `github`, `curated-fronting`,
       `legacy-android-overrides`.
   - **Exit country** dropdown (`aether_lab_psiphon_region`): “Any country” or
     an ISO code from the bundled/last-reported Psiphon server list.
   - **Start from the bundled server list** switch
     (`aether_lab_psiphon_bundled_list`, summary
     `aether_hint_psiphon_bundled_list`).
   - **Clear Psiphon data** button (`aether_action_clear_psiphon`), enabled only
     without a live Aether session.

7. **Tor** dropdown (`aether_lab_tor`): `off`, `chain`, `reverse`, `only`.
   If not `off`:
   - Warning if no pluggable transport is shipped
     (`aether_tor_transports_unavailable`) when
     `bridgesUsed = torBridges != never && !(tor == chain && torBridges == auto)`.
   - **Bridges** dropdown (`aether_lab_tor_bridges`): `auto`, `first`, `never`,
     `own`.
   - If `auto` or `first`: **Bridge sources** dropdown
     (`aether_lab_tor_relays`): `auto`, `only`, `off`.
   - If `own`: **Bridge lines** multi-line field (`aether_lab_tor_bridge_lines`,
     placeholder `aether_hint_tor_bridge_lines`, 6 lines).

8. If `warpUsed`:
   - If `protocol.twoHops`: **Outer hop** (`aether_lab_wiw_outer`) and
     **Inner hop** (`aether_lab_wiw_inner`) text fields. Both placeholders are
     `aether_hint_endpoint` (“Leave blank to scan”), except the inner field for
     `wg-over-masque` uses `aether_hint_gool_peer` (“Leave blank for the
     endpoint WARP assigns”).
   - Else: **Address** (`server_lab_address`, placeholder
     `aether_hint_endpoint`) and **Port** (`server_lab_port`, numeric).
   - **IP version** dropdown (`aether_lab_ip_version`): IPv4/`v4`, IPv6/`v6`,
     IPv4 + IPv6/`both`.

9. **Other settings** collapsible (`aether_lab_other_settings`), opened by
   default when DNS, exit-location or a non-default targetStrategy is set:
   - If `warpUsed`: **DNS inside the tunnel** (`aether_lab_dns`, placeholder
     `aether_hint_dns`) and **Exit location** (`aether_lab_exit_loc`,
     placeholder `aether_hint_exit_loc`).
   - The ordinary **targetStrategy** field (`CommonTargetStrategyField`).

10. If `warpUsed`: **Scan for an endpoint** button
    (`aether_action_scan` / `aether_action_scanning`) plus Cancel while
    scanning. Disabled when the core is unavailable, a scan runs, or
    `scanBlocked`. A successful scan writes:
    - two-hop: `aetherWiwOuter = endpoint`, and `aetherWiwInner = innerHop` if
      the result has one;
    - one-hop: `address = endpoint.host`, `port = endpoint.port`.

11. **Aether command** multi-line field (`aether_lab_command`, 8 lines). The
    field is pre-filled with the command built from the current settings:
    `AetherCore.of(uiState.toProfileItem(...).copy(aetherCommand = null),
    listenPort).command`. If the user edits it to something different:
    - show `aether_command_custom`;
    - show **Use the settings** (`aether_action_use_settings`) to clear it.
    The command runs verbatim; the settings above (except Xray-core settings)
    no longer apply.

12. **Log panel** (`aether_log_title`), 500 entries, newest last, copy button.

## Save / validation order

From `ServerAetherActivity.requestSave`, `validateBasicConfig`,
`validateCommonConfig`, `validateProtocolConfig` (lines 617-703) and
`ServerAetherViewModel.checkKeysBeforeSave`:

1. **Remarks** must not be blank.
2. **finalMask** must be blank or a JSON object (`AetherExit.takesFinalMask`).
3. `AetherFmt.normalize(config, takenPorts, listenPort)` runs. It normalizes
   fields in place and returns the first problem. Mapping from `AetherFmt.Problem`
   to strings:

   | Problem | String |
   |---|---|
   | `INVALID_MASQUE_SNI` | `aether_invalid_masque_sni` |
   | `INVALID_PEER` | `aether_invalid_endpoint` |
   | `INVALID_HOP` | `aether_invalid_hop` |
   | `SHARED_HOP` | `aether_same_hop` |
   | `INVALID_FRAGMENT` | `aether_invalid_fragment` |
   | `INVALID_DNS` | `aether_invalid_dns` |
   | `INVALID_EXIT_LOC` | `aether_invalid_exit_loc` |
   | `INVALID_ECH_DNS` | `aether_invalid_ech_dns` |
   | `INVALID_ECH_DOMAIN` | `aether_invalid_ech_domain` |
   | `LISTEN_PORT_TAKEN` | `aether_listen_port_taken` |
   | `PSIPHON_NEEDS_MASQUE` | `aether_psiphon_needs_masque` |
   | `NEXT_PORT_TAKEN` | `aether_next_port_taken` |
   | `TOR_NEEDS_MASQUE` | `aether_tor_needs_masque` |
   | `TOR_PSIPHON_CONFLICT` | `aether_tor_psiphon_conflict` |
   | `TOR_BRIDGES_MISSING` | `aether_tor_bridges_missing` |
   | `INVALID_COMMAND` | `aether_invalid_command` |

   `takenPorts` = local-proxy ports + the secondary-socks port
   (`AetherArgs.secondarySocksPort`, listen port + 3).
   `listenPort` = the Aether listen port from settings (default 10819).

4. **Exit-node lookup**: if `aetherExitNode` is non-blank, the named profile must
   still exist once, and must yield an outbound; otherwise refuse with
   `aether_exit_node_unusable` or `aether_exit_node_ech_unusable`.
5. **WARP keys check**: compute
   `AetherIdentityManager.filesNeededBy(AetherCore.of(profile, listenPort).arguments)`;
   if any are missing, ask “get keys first” or “save anyway”. A profile that
   uses Psiphon or Tor `only` needs no WARP key.

## Notes that matter for the Go port

- `warpUsed == false` (Psiphon-only or Tor-only) hides the whole WARP block,
  including protocol/scan/endpoint/IP version. The resulting command contains
  no `--protocol`, no `--scan`, no `--peer`; only the carrier flags and
  `--psiphon-mode`/bridge flags.
- `aetherMasqueSni`, `aetherEchDns`, `aetherEchDomain` are stored even when
  their feature is off; `normalize` only validates them while the feature is in
  use, and `AetherFmt.toUri` only exports them while in use.
- `aetherPsiphonBundledList` is tri-state: `true` is the default and is stored
  as null; only `false` is persisted.
- The editor never stores `aetherListenPort`; the legacy field is preserved on
  load only.
- The command preview is built without `--log-level`; the session adds the app's
  log level at start (`AetherArgs.withLogLevel`).
