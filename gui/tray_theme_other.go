//go:build !windows

package main

// applyTrayTheme is Windows-only: macOS and the Linux trays draw their
// menus with the desktop's own theme, and neither has a per-process
// light/dark switch to set.
func applyTrayTheme(string) {}
