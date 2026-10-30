//go:build !darwin

package main

import (
	"nekobox_core/internal"
	"testing"
	"time"
)

// The SIGTERM/waitpid exit paths in main.go end in os.Exit and cannot be
// unit-tested directly. These tests pin the helper paths they rely on so
// the cleanup sequence (reset proxy, close instance, stop extra process)
// stays panic-free when nothing is running.
func TestExitCleanupHelpersNoPanic(t *testing.T) {
	if err := internal.ResetSystemProxy(); err != nil {
		t.Fatalf("ResetSystemProxy with no proxy enabled: %v", err)
	}
	// Mirrors the nil guards in the signal/waitpid handlers: with no
	// instance and no extra process there is nothing to close.
	if internal.BoxInstance != nil {
		t.Fatal("expected no BoxInstance in unit test")
	}
	if internal.ExtraProcess != nil {
		t.Fatal("expected no ExtraProcess in unit test")
	}
}

func TestWaitForProcessExitInvalidPID(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		// 1<<30 is not a real PID on test machines; both
		// implementations return fast (nil on unix ESRCH, error on
		// windows OpenProcess failure) instead of blocking.
		done <- WaitForProcessExit(1 << 30)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForProcessExit did not return for invalid PID")
	}
}
