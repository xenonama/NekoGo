package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tor_core.go — Tor as an engine of its own.
//
// NekoGo has always reached Tor through aether.exe, which links the arti
// client and answers to a different version number than the Tor Project's
// daemon. That is a Tor *implementation*, not the binary the Tor Project
// ships: the standalone tor.exe has its own name, its own torrc, its own
// pluggable-transport folder and its own release channel, and until now
// the app could neither find it nor report whether one was installed —
// the Updates row for "Tor" answered with aether's version.
//
// Resolving it is the first half: everything below locates the file,
// reads its version and names the transports a bridge line needs. The
// second half is tor_daemon.go, which runs it, and the chain's Tor leg
// now prefers this engine over aether's arti whenever the machine has one
// (CoreManager.startTorChainLeg).

const torExeName = "tor.exe"

// Test hooks, mirroring aetherExeOverride/aetherExeDirOverride: the hook
// pins the resolved binary itself, the dir hook the folder the shipped
// layout is searched in.
var (
	torExeOverride    string
	torExeDirOverride string
)

// torSubdirs are the folders a tor.exe can sit in below a base directory.
// All three are real layouts a user has on disk: a bare bin\tor.exe is one
// dropped beside the other engines; bin\tor\tor.exe is the expert bundle's
// own archive layout, and the shape NekoGo's Tor updater installs;
// TorBrowser\Tor\tor.exe is the installed Tor Browser, which is the most
// common way to have a tor.exe at all.
var torSubdirs = []string{"", "tor", filepath.Join("TorBrowser", "Tor")}

// firstTorIn returns the first tor.exe below [dir], "" when it holds
// none.
func firstTorIn(dir string) string {
	for _, sub := range torSubdirs {
		c := filepath.Join(dir, sub, torExeName)
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// resolveTorExe locates the standalone tor.exe: the Settings.TorPath
// override first (a file, or a directory hosting one of the layouts
// above), then the shipped folders next to nekogo.exe. No silent
// fallbacks — a machine without Tor gets an error that says where to put
// the file, because a chain that quietly ran something else is worse than
// one that refuses to start.
func resolveTorExe(settings Settings) (string, error) {
	if torExeOverride != "" {
		if st, err := os.Stat(torExeOverride); err == nil && !st.IsDir() {
			return torExeOverride, nil
		}
		return "", fmt.Errorf("tor test exe not found at %q", torExeOverride)
	}
	if p := strings.TrimSpace(settings.TorPath); p != "" {
		if st, err := os.Stat(p); err == nil {
			if !st.IsDir() {
				return p, nil
			}
			if c := firstTorIn(p); c != "" {
				return c, nil
			}
			return "", fmt.Errorf("no %s in %q (looked in the folder itself, tor\\ and TorBrowser\\Tor\\)", torExeName, p)
		}
		return "", fmt.Errorf("tor.exe not found at %q", p)
	}
	dir := torExeDirOverride
	if dir == "" {
		for _, sub := range torSubdirs {
			if c, ok := firstEngineFile(filepath.Join(sub, torExeName)); ok {
				return c, nil
			}
		}
		dir = exeDir()
	}
	if c := firstTorIn(dir); c != "" {
		return c, nil
	}
	return "", fmt.Errorf("no %s found: install it in bin\\ next to nekogo.exe (%s) or set the Tor binary path under Settings → Advanced → Core runtime (torPath)",
		torExeName, filepath.Join(dir, binDirName))
}

// torPtDir is where a standalone tor.exe finds its pluggable transports.
// The expert bundle ships them as tor\pluggable_transports\ beside
// tor\tor.exe, and Tor's own ClientTransportPlugin line takes an explicit
// path, so both that folder and the pt\ folder aether uses are searched —
// a tree staged for one engine then serves the other.
func torPtDir(exe string) string {
	return filepath.Join(filepath.Dir(exe), "pluggable_transports")
}

// torTransports are the transport binaries a bridge line needs. lyrebird
// carries obfs4 and webtunnel, which is what the Tor Project hands out on
// the bridge request page; meek and the rest are its own client
// transports, so one file covers the bridges a user can paste.
var torTransports = []string{"lyrebird.exe"}

// missingStandaloneTorTransports names the transport binaries [exe]
// cannot launch. Without them tor.exe connects to nothing for every
// bridge line and the bootstrap just stops, which reads as a network
// problem rather than a missing file.
func missingStandaloneTorTransports(exe string) []string {
	if exe == "" {
		return nil
	}
	var out []string
	for _, name := range torTransports {
		if exeExists(filepath.Join(torPtDir(exe), name)) || exeExists(filepath.Join(filepath.Dir(exe), "pt", name)) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// TorVersion answers "<exe> --version" for the standalone binary, cached
// per resolved path the way every other engine is. The flag matters:
// "tor -v" is a malformed option that drops the daemon into its startup
// path, while "--version" prints one line and exits.
func (a *App) TorVersion() string {
	if a.store == nil {
		return ""
	}
	bin, err := resolveTorExe(a.store.GetSettings())
	if err != nil {
		return ""
	}
	a.torVer.mu.Lock()
	if a.torVer.path != bin {
		a.torVer.version = ""
		a.torVer.path = bin
	}
	cached := a.torVer.version
	a.torVer.mu.Unlock()
	if cached != "" {
		return cached
	}
	v := exeVersionQuery(bin, "--version")
	a.torVer.mu.Lock()
	if v != "" {
		a.torVer.version = v
	}
	a.torVer.path = bin
	a.torVer.mu.Unlock()
	return v
}
