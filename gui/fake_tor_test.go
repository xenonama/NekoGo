package main

// Test stand-in for the Tor Project's tor.exe.
//
// Copying the test binary to tor.exe and handing it the argv
// torDaemonArgs renders makes it behave like the daemon in the two ways
// the driver's readiness rule depends on: the SOCKS listener opens
// immediately — at 0% of the bootstrap, as measured against the real
// binary — and Bootstrapped lines arrive on stdout afterwards, reaching
// 100% only after a delay. A test can therefore tell "the listener
// answered" apart from "the circuit exists", which is the difference
// between this driver and the arti one, without a network.
//
// It also records the argv it was given and the transport paths those
// args name, in the state directory the app told it to use, so a test
// can read back what the daemon was really handed instead of trusting
// the builder's own output.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	// fakeTorModeEnv picks the shape the fake runs in: the default
	// reaches Bootstrapped 100% after a pause, fakeTorModeStall stops at
	// 30% with its listener already answering.
	fakeTorModeEnv   = "NEKOGO_FAKE_TOR_MODE"
	fakeTorModeStall = "stall"

	// The files the fake writes into the --DataDirectory the app gave it.
	fakeTorArgvMark  = "FAKE-ARGV.json"
	fakeTorPtMark    = "FAKE-PT.txt"
	fakeTorReadyMark = "FAKE-READY"
)

// fakeTorListeners keeps the fake's listener referenced, so the runtime
// cannot finalize it shut while the child sleeps.
var fakeTorListeners []net.Listener

// isFakeTorInvocation reports whether this process was started as the
// fake daemon (the test binary copied under tor.exe's name).
func isFakeTorInvocation() bool {
	base := filepath.Base(os.Args[0])
	if base != torExeName && base != "tor" {
		return false
	}
	for _, a := range os.Args[1:] {
		if a == "--DataDirectory" || a == "--version" {
			return true
		}
	}
	return false
}

// fakeTorMain is the child side. args is os.Args.
func fakeTorMain(args []string) {
	if hasFlag(args, "--version") {
		fmt.Println("Tor version 0.0.0-fake.")
		return
	}
	state := flagValueOf(args, "--DataDirectory")
	socks := flagValueOf(args, "--SocksPort")
	if state == "" || socks == "" {
		fmt.Fprintf(os.Stderr, "[err] fake tor: no --DataDirectory or --SocksPort\n")
		os.Exit(1)
	}
	_ = os.MkdirAll(state, 0o755)
	if data, err := json.Marshal(args); err == nil {
		_ = os.WriteFile(filepath.Join(state, fakeTorArgvMark), data, 0o644)
	}
	// What the pluggable transport argument points at, resolved the way
	// the daemon would: the app is expected to name a file that exists,
	// on this machine, as the child sees it.
	var pts strings.Builder
	for _, p := range allFlagValues(args, "--ClientTransportPlugin") {
		path := p
		if i := strings.LastIndex(p, "exec "); i >= 0 {
			path = strings.TrimSpace(p[i+len("exec "):])
		}
		if _, err := os.Stat(path); err != nil {
			pts.WriteString("missing " + path + "\n")
		} else {
			pts.WriteString("exists " + path + "\n")
		}
	}
	_ = os.WriteFile(filepath.Join(state, fakeTorPtMark), []byte(pts.String()), 0o644)

	ln, err := net.Listen("tcp", socks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[err] fake tor: socks listener: %v\n", err)
		os.Exit(1)
	}
	fakeTorListeners = append(fakeTorListeners, ln)
	go fakeTorSocks(ln)
	// Measured: the real daemon has its listener up before any of the
	// bootstrap lines land, so the greeting alone must never be taken as
	// a circuit.
	fakeTorBootstrap(0)

	switch os.Getenv(fakeTorModeEnv) {
	case fakeTorModeStall:
		fakeTorBootstrap(30)
		forever()
	default:
		time.Sleep(600 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(state, fakeTorReadyMark), []byte("done"), 0o644)
		fakeTorBootstrap(100)
		forever()
	}
}

func fakeTorBootstrap(pct int) {
	fmt.Printf("Nov 15 00:00:00.000 [notice] Bootstrapped %d%% (fake): fake progress\n", pct)
}

// fakeTorSocks answers a SOCKS5 greeting so a listener probe succeeds.
func fakeTorSocks(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			var g [3]byte
			if _, err := c.Read(g[:]); err != nil {
				return
			}
			_, _ = c.Write([]byte{5, 0})
			time.Sleep(time.Hour)
		}(c)
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func flagValueOf(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// allFlagValues collects every value of a repeated flag.
func allFlagValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func forever() {
	time.Sleep(time.Hour)
	os.Exit(0)
}

// installFakeTor copies the running test binary under tor.exe's name.
func installFakeTor(t *testing.T) string {
	t.Helper()
	name := torExeName
	if runtime.GOOS != "windows" {
		name = "tor"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	dest := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		t.Fatalf("write fake tor: %v", err)
	}
	return dest
}

// withTorDaemonHooks points the standalone-Tor resolver and the engine
// work dir at [bin] ("" leaves the resolver on the settings path) and a
// fresh work dir, restoring both afterwards. The work dir hook is the
// one the chain runs already share, so the daemon's state directory and
// its child's working directory are inside the test's temp tree.
func withTorDaemonHooks(t *testing.T, bin string) string {
	t.Helper()
	work := t.TempDir()
	prevExe, prevDir, prevWork := torExeOverride, torExeDirOverride, aetherWorkDirHook
	torExeOverride, torExeDirOverride = bin, ""
	aetherWorkDirHook = func() string { return work }
	t.Cleanup(func() {
		torExeOverride, torExeDirOverride, aetherWorkDirHook = prevExe, prevDir, prevWork
	})
	return work
}
