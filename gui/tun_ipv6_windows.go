//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// tcpip6ParamsKeyPath holds DisabledComponents. A variable, so a test can
// point it at a scratch subkey instead of the machine's real TCP/IP
// settings (the sysproxyKeyPath convention).
var tcpip6ParamsKeyPath = `SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters`

// osIPv6DisabledByOS reports whether this Windows install turns IPv6 off
// on newly created interfaces. A value that is not there at all means
// IPv6 is on; so does a read that fails — the gate then asks for the
// address, and the tunnel either has IPv6 or reports its own error.
func osIPv6DisabledByOS() bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, tcpip6ParamsKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("DisabledComponents")
	if err != nil {
		return false
	}
	return ipv6DisabledByComponents(v)
}
