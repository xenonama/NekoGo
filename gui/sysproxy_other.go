//go:build !windows

package main

import (
	"fmt"
)

// Non-Windows GUI-owned system proxy: not implemented. Never panics,
// never fails the build.

func sysproxyApply(p ProxyPorts) error {
	return fmt.Errorf("GUI system proxy is not implemented on this platform")
}

func sysproxyRestore() error {
	return fmt.Errorf("GUI system proxy is not implemented on this platform")
}

func cleanupStaleSysproxy() {}
