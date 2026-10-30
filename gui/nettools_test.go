package main

import (
	"sync"
	"testing"
)

// Pure-helper tests for Phase C (no core process needed).
func TestParseBrateMbps(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"12.34Mbps", 12.34},
		{"5.67Kbps", 0.00567},
		{"1.23Gbps", 1230},
		{"0.00Mbps", 0},
		{"", 0},
		{"N/A", 0},
		{"12.34 Mbps", 12.34}, // surrounding space tolerated by design
		{"-1.5Mbps", 0},
		{"abcMbps", 0},
	}
	for _, c := range cases {
		if got := parseBrateMbps(c.in); got != c.want {
			t.Errorf("parseBrateMbps(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// HOTFIX repro (Bug 1): LastNetworkResults with no core running must
// return a non-nil, empty struct — never panic, never nil.
func TestLastNetworkResultsNoCore(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	out, err := app.LastNetworkResults()
	if err != nil {
		t.Fatalf("LastNetworkResults() err = %v, want nil (idle = empty, not error)", err)
	}
	if out == nil {
		t.Fatal("LastNetworkResults() = nil, want non-nil empty struct")
	}
	if out.IP != nil || out.Speed != nil || out.URL != nil || len(out.Country) != 0 {
		t.Fatalf("LastNetworkResults() = %+v, want empty", out)
	}
}

// Nil managers must not panic either (still non-nil struct back).
// Idle wins over misconfiguration: empty + nil error. Only a running
// core with missing managers reports an error (still with a struct).
func TestLastNetworkResultsNilManagers(t *testing.T) {
	m := NewCoreManager(nil, nil)
	if out, err := m.LastNetwork(); out == nil || err != nil {
		t.Fatalf("idle LastNetwork() = %v, %v; want non-nil, nil", out, err)
	}
	m.running = true
	out, err := m.LastNetwork()
	if out == nil {
		t.Fatal("LastNetwork() = nil, want non-nil struct")
	}
	if err == nil {
		t.Fatal("LastNetwork() err = nil, want managers-not-initialized")
	}
}

// Concurrent mount + stats-poll shape: LastNetwork and GetStats side by
// side must neither panic nor deadlock (run with -race in CI).
func TestLastNetworkResultsConcurrent(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := app.LastNetworkResults(); err != nil || out == nil {
				t.Errorf("LastNetworkResults() = %v, %v; want non-nil, nil", out, err)
			}
			if _, err := app.GetStats(); err != nil {
				t.Errorf("GetStats() err = %v", err)
			}
		}()
	}
	wg.Wait()
}
