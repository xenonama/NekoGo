//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// killProcessTree ends [pid] and every process it spawned. aether.exe
// supervises psiphon-tunnel-core.exe (and Tor) as children; killing only
// the supervisor leaves the tunnel alive, holding its SOCKS port with no
// parent left to reap it. taskkill /T walks the child tree, /F because
// these children do not answer a console-close request.
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	setHideWindow(cmd)
	_ = cmd.Run()
}
