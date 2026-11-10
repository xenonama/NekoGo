//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"

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

// osSystemProxy reads the ACTUAL OS proxy switch plus the configured
// ProxyServer string, so callers can tell "off" apart from "on but
// pointing elsewhere" (Connect repairs both onto our ports), and so a
// toggle can tell its own proxy from a corporate one.
// A profile that never touched the proxy panel has no ProxyEnable value
// at all: that is "off", not a read failure — reporting it as an error
// left callers stuck on their settings fallback instead of the truth.
func osSystemProxy() (bool, string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE)
	if err != nil {
		return false, "", fmt.Errorf("open internet settings: %w", err)
	}
	defer key.Close()
	v, _, err := key.GetIntegerValue("ProxyEnable")
	if errors.Is(err, registry.ErrNotExist) || errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("read ProxyEnable: %w", err)
	}
	server, _, _ := key.GetStringValue("ProxyServer")
	return v != 0, server, nil
}
