package main

import (
	"testing"
	"time"
)

// TestWaitOrphanScan — the startup orphan scan runs in the background
// (so the window appears immediately); Connect waits for it so a stray
// core can never hold a port into the first Connect. The wait must be
// a no-op when no scan is pending and must return at once when the
// scan already finished.
func TestWaitOrphanScan(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))

	// No scan pending: returns immediately.
	done := make(chan struct{})
	go func() {
		app.waitOrphanScan()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitOrphanScan blocked with no scan pending")
	}

	// A finished scan: returns immediately.
	ch := make(chan struct{})
	close(ch)
	app.setOrphanScanDone(ch)
	done = make(chan struct{})
	go func() {
		app.waitOrphanScan()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitOrphanScan blocked on a finished scan")
	}
}
