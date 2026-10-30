package main

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestSanitizeChildEnvDropsInheritedCoreSettings(t *testing.T) {
	base := []string{
		"PATH=C:\\Windows",
		"aether_config=C:\\user\\override.toml",
		"AETHER_WG_CONFIG=C:\\user\\wg.toml",
		"RUST_LOG=trace",
		"HOME=C:\\Users\\other",
		"TMPDIR=C:\\Temp",
		"KEEP=1",
		"not-an-entry",
	}
	got := sanitizeChildEnv(base, []string{"AETHER_"}, []string{"RUST_LOG", "HOME", "TMPDIR"})
	joined := strings.Join(got, "|")
	for _, banned := range []string{"aether_config", "AETHER_WG_CONFIG", "RUST_LOG", "HOME=", "TMPDIR=", "not-an-entry"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("sanitized env still contains %q: %v", banned, got)
		}
	}
	for _, kept := range []string{"PATH=", "KEEP=1"} {
		if !strings.Contains(joined, kept) {
			t.Fatalf("sanitized env lost %q: %v", kept, got)
		}
	}
}

func TestFreePortTCPAndUDPIsFreeForBoth(t *testing.T) {
	for i := 0; i < 5; i++ {
		port, err := freePortTCPAndUDP()
		if err != nil {
			t.Fatal(err)
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		u, err := net.ListenPacket("udp", addr)
		if err != nil {
			t.Fatalf("UDP %s not free: %v", addr, err)
		}
		_ = u.Close()
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("TCP %s not free: %v", addr, err)
		}
		_ = l.Close()
	}
}
