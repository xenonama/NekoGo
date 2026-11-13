//go:build !windows

package main

// osIPv6DisabledByOS: no IPv6 is turned off by a Windows registry key
// here, and nothing else in NekoGo's supported surface (Wintun) needs the
// question. Linux's disable_ipv6 and macOS are PattN's problem to answer,
// not this renderer's to guess at.
func osIPv6DisabledByOS() bool { return false }
