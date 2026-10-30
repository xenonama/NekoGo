//go:build !windows

package main

import (
	"fmt"
)

// isGuiElevated off Windows: no elevation concept for the GUI legs;
// report non-elevated so callers degrade gracefully.
func isGuiElevated() (bool, error) {
	return false, nil
}

// osProxyEnabled off Windows: the HKCU proxy switch does not exist.
func osProxyEnabled() (bool, error) {
	return false, fmt.Errorf("system proxy state is only available on Windows")
}

// osSystemProxy off Windows: same platform limitation as osProxyEnabled.
func osSystemProxy() (bool, string, error) {
	return false, "", fmt.Errorf("system proxy state is only available on Windows")
}
