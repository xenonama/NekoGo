//go:build !windows

package main

import (
	"fmt"
)

// Elevated relaunch off Windows: there is nothing to elevate for.
// Stubbed so the shared Connect path compiles everywhere; the
// Connect gate also checks runtime.GOOS, so this is unreachable
// in practice.
var relaunchElevated = func(args ...string) error {
	return fmt.Errorf("elevated relaunch is only supported on Windows")
}

// guiElevated off Windows: never elevated (matches isGuiElevated).
var guiElevated = func() (bool, error) {
	return false, nil
}

// elevateExiter off Windows: same contract as the Windows one.
var elevateExiter = func(code int) {}
