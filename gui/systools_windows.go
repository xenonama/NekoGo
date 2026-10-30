//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// isGuiElevated reports whether THIS (GUI) process runs elevated,
// mirroring the server's TokenElevation check (core/server/
// server_windows.go isElevated) for the xray/offline legs.
func isGuiElevated() (bool, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false, err
	}
	defer token.Close()
	return token.IsElevated(), nil
}

// osProxyEnabled reads the ACTUAL OS proxy switch (integration
// BUG-010): HKCU Internet Settings ProxyEnable. This is ground truth
// independent of our toggle state — it catches a crash that left the
// proxy installed, an external OS change, or a plain restart. It says
// only ON/OFF, not whether the proxy points at our core (use
// osSystemProxy for the server comparison).
func osProxyEnabled() (bool, error) {
	on, _, err := osSystemProxy()
	return on, err
}

// osSystemProxy reads the ACTUAL OS proxy switch plus the configured
// ProxyServer string, so callers can tell "off" apart from "on but
// pointing elsewhere" (Connect repairs both onto our ports).
func osSystemProxy() (bool, string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE)
	if err != nil {
		return false, "", fmt.Errorf("open internet settings: %w", err)
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil {
		return false, "", fmt.Errorf("read ProxyEnable: %w", err)
	}
	server, _, _ := key.GetStringValue("ProxyServer")
	return v != 0, server, nil
}
