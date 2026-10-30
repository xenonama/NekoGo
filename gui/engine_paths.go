package main

import (
	"os"
	"path/filepath"
)

// binDirName is the PattN-style folder the engine binaries live in,
// next to nekogo.exe: nekobox_core.exe, sing-box.exe, xray.exe,
// sni-spoof.exe, aether.exe (+ its pt\ payload) and the WinDivert
// runtime. Keeping them in bin/ leaves the portable root readable and
// treats every engine the same way.
const binDirName = "bin"

// exeDir is the folder holding nekogo.exe, "" when unknown.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	return filepath.Dir(exe)
}

// engineDirs lists the folders engine binaries are looked up in,
// preferred first: <exeDir>\bin (the shipped layout), then the exe's own
// folder (portable trees and installed copies from before the bin
// folder existed).
func engineDirs() []string {
	dir := exeDir()
	if dir == "" {
		return nil
	}
	return []string{filepath.Join(dir, binDirName), dir}
}

// firstEngineFile returns the first existing candidate among names in
// engineDirs (directory-major: bin/ before the legacy exe folder).
func firstEngineFile(names ...string) (string, bool) {
	return firstFileIn(engineDirs(), names...)
}

// firstFileIn returns the first existing regular file among names in
// dirs, directory-major.
func firstFileIn(dirs []string, names ...string) (string, bool) {
	for _, dir := range dirs {
		for _, name := range names {
			c := filepath.Join(dir, name)
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c, true
			}
		}
	}
	return "", false
}

// engineInstallDir is where updater-installed engine binaries go:
// <exeDir>\bin (created on install). It is the folder the resolvers
// search first, so an updated core is always the one that runs.
func engineInstallDir() string {
	dir := exeDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, binDirName)
}
