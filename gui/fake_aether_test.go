package main

// Test stand-in for CluvexStudio's aether.exe.
//
// Copying the test binary to aether.exe and handing it the argv
// buildAetherArguments renders makes it behave like the core in the ways
// the engine manager's readiness rules depend on, without a network:
//
//   - its SOCKS listener answers the greeting with a whole method
//     selection, and only once the tunnel is up;
//   - a pinned --peer that has gone dead produces the core's own
//     "last known-good gateway ... no longer responds; rescanning" line
//     and then nothing ever answers, because the forced peer makes the
//     rescan a no-op ("using forced peer ... (probe skipped)") — measured
//     on aether.exe 2.3.0 over 170s;
//   - the words that end a session (a carrier inside the tunnel dying,
//     no ECH key to offer, a fatal error) arrive while the process lives.
//
// It records the argv of every run so a test can read back what the
// engine was really handed instead of trusting the builder's output.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

const (
	// fakeAetherModeEnv picks the shape the fake runs in. The default
	// binds its listener at once; the others reproduce a measured
	// failure or a delayed success.
	fakeAetherModeEnv = "NEKOGO_FAKE_AETHER_MODE"
	// fakeAetherStateEnv names the directory the fake records its argv in.
	fakeAetherStateEnv = "NEKOGO_FAKE_AETHER_STATE"
	// fakeAetherDelayEnv delays the listener, in milliseconds.
	fakeAetherDelayEnv = "NEKOGO_FAKE_AETHER_DELAY_MS"

	fakeAetherModeDeadPeer = "deadpeer"
	fakeAetherModeChainEnd = "chainend"
	fakeAetherModeNoEchKey = "noech"
	fakeAetherModeFatal    = "fatal"

	fakeAetherArgvMark = "FAKE-ARGV"
)

// fakeAetherListeners keeps the fake's listeners referenced, so the
// runtime cannot finalize them shut while the child sleeps.
var fakeAetherListeners []net.Listener

// isFakeAetherInvocation reports whether this process was started as
// the fake engine (the test binary copied under aether.exe's name).
func isFakeAetherInvocation() bool {
	base := filepath.Base(os.Args[0])
	if base != aetherExeName && base != "aether" {
		return false
	}
	for _, a := range os.Args[1:] {
		if a == "--bind" || a == "--protocol" || a == "--version" || a == "--register" {
			return true
		}
	}
	return false
}

// fakeAetherMain is the child side. args is os.Args.
func fakeAetherMain(args []string) {
	if hasFlag(args, "--version") {
		fmt.Println("Aether v2.3.0")
		return
	}
	if state := os.Getenv(fakeAetherStateEnv); state != "" {
		_ = os.MkdirAll(state, 0o755)
		if data, err := json.Marshal(args); err == nil {
			seq := len(fakeAetherRuns(state))
			_ = os.WriteFile(filepath.Join(state, fakeAetherArgvMark+strconv.Itoa(seq)+".json"), data, 0o644)
		}
	}
	if kinds := allFlagValues(args, "--register"); len(kinds) > 0 {
		files := map[string][]string{
			"wg":     {"aether-wg.toml"},
			"masque": {"aether-masque.toml"},
			"gool":   {"aether-wg.toml", "aether-wg-secondary.toml"},
			"mim":    {"aether-masque.toml", "aether-masque-secondary.toml"},
			"all":    {"aether-wg.toml", "aether-wg-secondary.toml", "aether-masque.toml", "aether-masque-secondary.toml"},
		}[kinds[len(kinds)-1]]
		if len(files) == 0 {
			fmt.Fprintln(os.Stderr, "invalid registration kind")
			os.Exit(1)
		}
		if os.Getenv(fakeAetherModeEnv) == "register-incomplete" {
			files = files[:len(files)-1]
		}
		for _, name := range files {
			if err := os.WriteFile(name, []byte(aetherKeysFixture), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		fmt.Println("[+] identities ready: " + kinds[len(kinds)-1])
		return
	}
	bind := flagValueOf(args, "--bind")
	if bind == "" {
		fmt.Fprintf(os.Stderr, "Error: Other(\"fake aether: no --bind\")\n")
		os.Exit(1)
	}
	mode := os.Getenv(fakeAetherModeEnv)
	if mode == fakeAetherModeDeadPeer && hasFlag(args, "--peer") {
		// The core's own words, measured: it announces a rescan and then
		// re-uses the forced peer, so nothing ever answers.
		fmt.Printf("[2026-11-16T21:29:56.879Z WARN  aether] [-] last known-good gateway %s no longer responds; rescanning\n",
			flagValueOf(args, "--peer"))
		fmt.Println("[2026-11-16T21:29:56.879Z INFO  aether] [+] using forced peer " + flagValueOf(args, "--peer") + " (probe skipped)")
		forever()
	}
	switch mode {
	case fakeAetherModeChainEnd:
		fmt.Println("[2026-11-16T21:29:56.879Z ERROR aether] [-] tor: other: circuit failed")
		forever()
	case fakeAetherModeNoEchKey:
		fmt.Println("[2026-11-16T21:29:56.879Z ERROR aether] ECH is on but there is no ECH key to offer")
		forever()
	case fakeAetherModeFatal:
		fmt.Println(`Error: Other("unknown option 'x'")`)
		forever()
	}
	if ms := os.Getenv(fakeAetherDelayEnv); ms != "" {
		if d, err := strconv.Atoi(ms); err == nil {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Other(\"fake aether: bind %s: %v\")\n", bind, err)
		os.Exit(1)
	}
	fakeAetherListeners = append(fakeAetherListeners, ln)
	go fakeTorSocks(ln)
	fmt.Printf("[2026-11-16T21:24:24.573Z INFO  aether::socks] [+] socks5 server listening on %s\n", bind)
	forever()
}

// fakeAetherRuns reads back the argv of every run the fake recorded, in
// order, so a test can tell a rescue apart from a first attempt.
func fakeAetherRuns(state string) [][]string {
	var out [][]string
	for i := 0; ; i++ {
		data, err := os.ReadFile(filepath.Join(state, fakeAetherArgvMark+strconv.Itoa(i)+".json"))
		if err != nil {
			return out
		}
		var argv []string
		if json.Unmarshal(data, &argv) != nil {
			return out
		}
		out = append(out, argv)
	}
}

// installFakeAether copies the running test binary under aether.exe's
// name and points the engine resolver and work dir at it, restoring
// both afterwards. The state directory is inside the test's scratch
// tree, so nothing touches the user's own engine data.
func installFakeAether(t *testing.T) (bin, state string) {
	t.Helper()
	dir := scratchDir(t)
	name := aetherExeName
	if runtime.GOOS != "windows" {
		name = "aether"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	bin = filepath.Join(dir, name)
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatalf("write fake aether: %v", err)
	}
	state = filepath.Join(dir, "state")
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("make fake aether work dir: %v", err)
	}
	prevExe, prevDir, prevWork := aetherExeOverride, aetherExeDirOverride, aetherWorkDirHook
	aetherExeOverride, aetherExeDirOverride = bin, ""
	aetherWorkDirHook = func() string { return work }
	t.Cleanup(func() {
		aetherExeOverride, aetherExeDirOverride, aetherWorkDirHook = prevExe, prevDir, prevWork
	})
	t.Setenv(fakeAetherStateEnv, state)
	return bin, state
}

// fakeAetherProfile is a MASQUE over HTTP/2 profile with a saved
// endpoint, the shape the app's Scan leaves behind.
func fakeAetherProfile() *Profile {
	return &Profile{ID: "a1", Name: "fake", Core: CoreSingBox, Protocol: "aether",
		Address: "203.0.113.9", Port: 443,
		Aether: aetherSettings{Protocol: "masque", Transport: "h2"}}
}
