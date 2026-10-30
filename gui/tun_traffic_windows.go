//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// adapterFlags keeps the enumeration to what the lookup needs: names
// and LUIDs. DNS servers, anycast and multicast lists are skipped.
const adapterFlags = windows.GAA_FLAG_SKIP_ANYCAST |
	windows.GAA_FLAG_SKIP_MULTICAST |
	windows.GAA_FLAG_SKIP_DNS_SERVER

const adapterBufInitial = 16 * 1024

// readTunInterfaceCounters returns the lifetime byte counters of the
// adapter named alias: received (tunnel → host, downlink) and sent
// (host → tunnel, uplink). ok is false when no adapter carries that
// name — the TUN is not up yet or already torn down — which leaves the
// caller on the engine's own counters.
func readTunInterfaceCounters(alias string) (in, out uint64, ok bool, err error) {
	luid, found, err := adapterLUID(alias)
	if !found {
		return 0, 0, false, err
	}
	var row windows.MibIfRow2
	row.InterfaceLuid = luid
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row); err != nil {
		return 0, 0, false, fmt.Errorf("read adapter %q counters: %w", alias, err)
	}
	return row.InOctets, row.OutOctets, true, nil
}

// adapterLUID resolves an adapter's friendly name to its LUID. The
// buffer grows on ERROR_BUFFER_OVERFLOW: adapters can appear between
// the size query and the fill.
func adapterLUID(alias string) (uint64, bool, error) {
	size := uint32(adapterBufInitial)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, adapterFlags, 0, first, &size)
		if err == nil {
			for a := first; a != nil; a = a.Next {
				if windows.UTF16PtrToString(a.FriendlyName) == alias {
					return a.Luid, true, nil
				}
			}
			return 0, false, nil
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return 0, false, fmt.Errorf("enumerate network adapters: %w", err)
		}
	}
	return 0, false, nil
}
