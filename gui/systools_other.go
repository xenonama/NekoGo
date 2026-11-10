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

// osSystemProxy off Windows: the HKCU proxy switch does not exist, so
// there is no state to report and no proxy the GUI could have owned.
func osSystemProxy() (bool, string, error) {
	return false, "", fmt.Errorf("system proxy state is only available on Windows")
}
