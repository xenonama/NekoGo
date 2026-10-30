//go:build !windows

package main

import "os"

// killProcessTree ends [pid]. No child tree is spawned here the way
// Windows' tunnel helpers are, so the direct kill matches previous
// behavior; callers still keep their own Process.Kill fallback.
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
