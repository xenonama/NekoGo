//go:build !windows

package main

// readTunInterfaceCounters has no portable implementation: TUN mode
// (Wintun) is a Windows feature here, so other platforms keep using
// the engine's own counters.
func readTunInterfaceCounters(alias string) (in, out uint64, ok bool, err error) {
	return 0, 0, false, nil
}
