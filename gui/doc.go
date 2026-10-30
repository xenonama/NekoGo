// Package main implements the NekoBox Wails3 GUI backend.
//
// It manages two proxy cores and exposes them to the Vue 3 frontend
// through Wails bound services:
//
//   - nekobox_core (sing-box engine) via Apache Thrift RPC
//     (see core/server/gen/libcore.thrift).
//   - xray.exe (Xray-core) via generated config.json + process
//     management; traffic stats via Xray gRPC StatsService.
package main
