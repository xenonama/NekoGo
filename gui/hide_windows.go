//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// setHideWindow hides the console window of a child process (BUG-044:
// SysProcAttr.HideWindow exists only on Windows, so this lives in a
// windows-tagged file; the !windows twin is a no-op).
func setHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
