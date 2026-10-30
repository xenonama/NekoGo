package main

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// TestMain short-circuits into a linger-helper child when the manager
// race tests re-exec the test binary to simulate a hung core: the helper
// ignores all core args, records its PID, and sleeps. Its RPC port never
// opens, so Start sits in its readiness probe until Stop aborts it.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		if pidFile := os.Getenv("GO_WANT_HELPER_PIDFILE"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	// The test binary copied to sing-box.exe stands in for the official
	// CLI (see fake_singbox_test.go): it binds the config's inbound port
	// and blocks until killed.
	if isFakeSingBoxInvocation() {
		fakeSingBoxMain(os.Args)
		return
	}
	os.Exit(m.Run())
}
