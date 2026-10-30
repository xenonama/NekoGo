//go:build !windows

package main

import (
	"fmt"
)

// Non-Windows autostart: not implemented. Never panics, never fails
// the build; the Settings toggle surfaces this error and snaps back.

func autorunEnabled() (bool, error) {
	return false, fmt.Errorf("autostart is not implemented on this platform")
}

func autorunSet() error {
	return fmt.Errorf("autostart is not implemented on this platform")
}

func autorunClear() error {
	return fmt.Errorf("autostart is not implemented on this platform")
}
