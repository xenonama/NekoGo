//go:build !windows

package main

import (
	"os/exec"
)

// setHideWindow is a no-op off Windows (BUG-044): there is no console
// window to hide and SysProcAttr has no HideWindow field there.
func setHideWindow(cmd *exec.Cmd) {
}
