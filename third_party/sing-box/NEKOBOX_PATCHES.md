# NekoBox local sing-box patches

Base: `github.com/qr243vbi/sing-box v1.14.1-mod1`
(`replace github.com/sagernet/sing-box => ../../third_party/sing-box`
in `core/server/go.mod`).

The fork's module set does not compile as pinned; these minimal compat
patches adapt sing-box mod1 to the newer libraries it is paired with.
Upstream behavior is preserved in all three cases.

1. `common/process/searcher_windows.go`
   `winiphlpapi.FindPid(network, source)` gained a third parameter
   `destination` in `sagernet/sing` dev (`v0.9.6-0.20260922013359`).
   The call site already had `destination` in scope; it is now passed.

2. `transport/v2rayxhttp/client.go`
   `sing-quic` v0.7.0 changed `DialEarly(ctx, packetConn, addr, tls, quic)`
   to `DialEarly(ctx, net.Conn, tls, quic)`. `N.Dialer.DialContext` already
   returns `net.Conn`, so the `bufio.NewUnbindPacketConn` wrapper (and the
   `udpConn.RemoteAddr()` argument, and the now-unused `bufio` import) were
   removed and `udpConn` is passed directly. Same pattern as the sibling
   `quic.DialEarlyConn` call sites in this tree.

3. `protocol/tun/inbound.go`
   `sing-tun` mod1 changed `AutoRedirectOptions` from static
   `RouteAddressSet *[]*netipx.IPSet` / `RouteExcludeAddressSet *[]*netipx.IPSet`
   fields to a dynamic `RouteAddressSet func() (include, exclude []netip.Prefix, err error)`
   callback. The struct literal now passes a closure that converts the live
   `inbound.routeAddressSet` / `inbound.routeExcludeAddressSet` IPSets via
   `IPSet.Prefixes()`, preserving dynamic rule-set update behavior
   (sing-tun calls the func from `UpdateRouteAddressSet`).

## Wireguard endpoint drift patches (for sing-tun v0.9.5-mod1)

The vendored sing-box is newer than the `sing-tun v0.9.5-mod1` it is paired
with, so the Wireguard / AmneziaWG code reads fields and APIs that the pinned
libraries do not have. The pinned
`github.com/sagernet/wireguard-go v0.0.7` device implements **none** of the
UAPI keys the AmneziaWG extensions need, so the affected features cannot work
against this dependency set and the call sites are disabled rather than wired
through. Every site below is commented out (not deleted) and marked with a
`NekoBox patch` comment, so the drift is easy to re-audit when the pinned
versions move up.

### 0. `go.mod` language version

The vendored code uses a promoted field in a struct literal
(`option.DialerOptions` / `AbstractDialerOptions.BindInterface`), which
requires the `go1.27` language version, but the `go` directive still said
`go 1.25.5`.

- `go.mod`: `go 1.25.5` -> `go 1.27`
- `core/server/go.mod`: `go 1.26.3` -> `go 1.27.1`, because a dependency's
  `go` directive may not exceed the main module's.

### 1. `transport/wireguard/endpoint.go` — AmneziaWG UAPI keys (9 fields)

`option.WireGuardEndpointOptions` accepts these options, but
`wireguard.EndpointOptions` does not carry them, and `wireguard-go v0.0.7`
rejects the corresponding UAPI keys. The `ipcConf` emissions in
`NewEndpoint` are disabled:

| Option field | UAPI key | Lines (pre-patch) |
| --- | --- | --- |
| `JunkPacketCount` | `jc` | 56-58 |
| `JunkPacketMinSize` | `jmin` | 59-61 |
| `JunkPacketMaxSize` | `jmax` | 62-64 |
| `InitPacketJunkSize` | `s1` | 65-67 |
| `ResponsePacketJunkSize` | `s2` | 68-70 |
| `InitPacketMagicHeader` | `h1` | 71-73 |
| `ResponsePacketMagicHeader` | `h2` | 74-76 |
| `UnderloadPacketMagicHeader` | `h3` | 77-79 |
| `TransportPacketMagicHeader` | `h4` | 80-82 |

The `h1`-`h4` magic-header blocks are **not** in the original Stage 2.5 task
list, but they reference the same absent `EndpointOptions` fields; the
compiler stops after 10 errors, so they surface only once the first five are
fixed. Both groups are disabled together.

`listen_port` and the peer loop below are untouched.

### 2. `transport/wireguard/endpoint_options.go` — unused import

`github.com/sagernet/sing-box/option` was left imported after the advanced
security fields were dropped from `EndpointOptions`. Unused imports are only
an error in packages that are actually compiled, and this file is behind the
`with_wireguard` build tag, so `go build ./...` does not catch it. The
`wireguard` build tag does.

### 3. `transport/wireguard/device_system_stack.go` — `UDPNatOptions.ExcludeInterface`

`tun.UDPNatOptions` has no `ExcludeInterface` in `sing-tun v0.9.5-mod1`
(line 80, pre-patch), so the outbound's own interface is no longer excluded
from the system-stack UDP NAT. `InterfaceFinder` is kept.

### 4. `protocol/wireguard/endpoint.go` — two more sites

- Line 96 (pre-patch): `tun.UDPEgressPoolOptions.ExcludeInterface` does not
  exist in `sing-tun v0.9.5-mod1`, so the outbound is no longer excluded from
  the egress pool. `IsExempt` is kept.
- Line 129 (pre-patch):
  `wireguard.EndpointOptions.WireGuardAdvancedSecurityOptions` does not exist,
  and neither does `option.WireGuardEndpointOptions.WireGuardAdvancedSecurityOptions`,
  even though the `option.WireGuardAdvancedSecurityOptions` type itself is
  declared in `option/wireguard.go`. The whole forwarding line is disabled.

### Behavioral impact

Wireguard and AmneziaWG still connect and pass traffic. What is lost is only
the traffic-shaping/obfuscation layer:

- `junk_packet_count`, `junk_packet_min_size`, `junk_packet_max_size`
- `init_packet_junk_size`, `response_packet_junk_size`
- `init_packet_magic_header`, `response_packet_magic_header`,
  `underload_packet_magic_header`, `transport_packet_magic_header`
- the per-outbound interface exclusion for the system-stack UDP NAT and the
  UDP egress pool

The options remain accepted in JSON and silently ignored. Restoring them
requires bumping `sing-tun` and `wireguard-go` together, not just uncommenting.

## P2 BUG-032 hardening: `transport/v2rayxhttp/dialer.go` pool read

The reflection read of x/net's unexported H2 pool (`connPoolOrDef`,
fallback `connPool`) ignored the type-assertion result and cast the
eface word to `*clientConnPool` blindly, so the next x/net field
rename/retype would leak sockets or crash teardown with a wild-pointer
deref (`field.IsNil()` also panics on a non-nillable kind).

- `transportConnPool` now returns `(http2.ClientConnPool, bool)`:
  nil-transport guard, `CanAddr` check, `IsNil` only on nillable
  kinds, comma-ok assertion, typed-nil rejection.
- New `asLocalPool` verifies the dynamic type structurally (pointer to
  a struct named `clientConnPool` with `mu sync.Mutex` + map `conns`
  fields) before the eface cast; anything else is rejected.
- `Close` falls back to the public `CloseIdleConnections()` on any
  rejection instead of leaking or crashing.

Verified against the pinned `golang.org/x/net v0.57.0`
(`clientConnPool{t, mu, conns, ...}` — the mirror matches the first
three fields; extra trailing fields don't affect the read).

## P3-9 audit note: `protocol/tun/inbound.go` `updateRouteAddressSet`

The prompt asked to capture and log the `t.autoRedirect.UpdateRouteAddressSet()`
return at `inbound.go:510`. Against the pinned `sing-tun v0.9.5`
(`tun.AutoRedirect` interface in `redirect.go`), that method returns `void`
— the inner `nftablesUpdateRouteAddressSet() error` is already consumed
internally (`redirect_linux.go:251`, result ignored by upstream too). No
patch is applicable; a comment at the call site records the finding. When
`sing-tun` is next upgraded, re-check the interface: if it gains an error
return, log it via `t.logger` without changing control flow (the callback
is invoked from sing-tun and cannot abort).

