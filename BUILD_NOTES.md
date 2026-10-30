# NekoGo Build Notes

## sing-box build tags
nekobox_core must be compiled with these tags (matching build.bat step 1):
with_clash_api, with_gvisor, with_quic, with_wireguard, with_utls, with_dhcp,
with_shadowtls, with_grpc, with_acme, with_awg, with_tailscale, with_naive,
with_naive_outbound, with_purego

## Required ldflags for sing-box
-checklinkname=0 -X internal/godebug.defaultGODEBUG=multipathtcp=0

## CGO
CGO_ENABLED=1

## Regenerating Thrift stubs
The core RPC interface is defined in core/server/gen/libcore.thrift.
When it changes:
  1. Install apache thrift compiler 0.24+.
  2. From core/server/gen/, run: bash update_libs.sh
  3. Copy the three generated Go files into gui/thriftgen/:
       libcore.go
       libcore-consts.go
       GoUnusedProtection__.go
  4. In each of those three files, change the package name from "gen" to "thriftgen".
  5. Run: cd gui && go build ./...   (must succeed).

## Cores
- nekobox_core.exe — sing-box engine, built from core/server/, talks Thrift on a random localhost port.
- xray.exe — Xray-core, cloned into xray/ by build.bat, talks gRPC StatsService on 127.0.0.1:10085.

## Original Qt build (removed)
The original NekoBox Qt/C++ GUI was removed. Its CMake build tags and ldflags
were migrated to build.bat (see above). The original Qt-specific directories
(src/, res/, cmake/, changelogs/) no longer exist.
