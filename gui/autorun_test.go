//go:build windows

package main

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// withScratchRunKey points the autorun helpers at an isolated subkey
// (never the real Run key) and cleans it up afterwards.
func withScratchRunKey(t *testing.T) {
	t.Helper()
	sub := `Software\NekoGoTest\` + strings.ReplaceAll(t.Name(), "/", "_")
	old := autoRunKeyPath
	autoRunKeyPath = sub
	t.Cleanup(func() {
		autoRunKeyPath = old
		k, err := registry.OpenKey(registry.CURRENT_USER, `Software\NekoGoTest`, registry.SET_VALUE)
		if err == nil {
			_ = k.DeleteValue("x")
			k.Close()
		}
		_ = registry.DeleteKey(registry.CURRENT_USER, sub)
	})
	// Ensure the scratch key exists (a missing key must read disabled,
	// but set needs a writable parent).
	k, _, err := registry.CreateKey(registry.CURRENT_USER, sub, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	k.Close()
}

// TestAutoRunRoundTrip: set -> enabled reads true; clear -> false;
// clearing twice and reading a missing value are clean.
func TestAutoRunRoundTrip(t *testing.T) {
	withScratchRunKey(t)
	if on, err := autorunEnabled(); err != nil || on {
		t.Fatalf("enabled = %v, %v; want false, nil (missing value)", on, err)
	}
	if err := autorunSet(); err != nil {
		t.Fatalf("autorunSet: %v", err)
	}
	if on, err := autorunEnabled(); err != nil || !on {
		t.Fatalf("enabled = %v, %v; want true, nil", on, err)
	}
	if err := autorunClear(); err != nil {
		t.Fatalf("autorunClear: %v", err)
	}
	if err := autorunClear(); err != nil {
		t.Fatalf("second clear: %v (want nil)", err)
	}
	if on, err := autorunEnabled(); err != nil || on {
		t.Fatalf("enabled = %v, %v; want false, nil", on, err)
	}
}

// TestSetStartOnBootAppliesAndPersists: the App path writes the OS
// value AND the stored setting together.
func TestSetStartOnBootAppliesAndPersists(t *testing.T) {
	withScratchRunKey(t)
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	if err := app.SetStartOnBoot(true); err != nil {
		t.Fatalf("SetStartOnBoot(true): %v", err)
	}
	if on, _ := autorunEnabled(); !on {
		t.Fatalf("OS value missing after enable")
	}
	if !store.GetSettings().StartOnBoot {
		t.Fatalf("stored StartOnBoot = false, want true")
	}
	if on, err := app.GetAutoRun(); err != nil || !on {
		t.Fatalf("GetAutoRun = %v, %v; want true, nil", on, err)
	}
	if err := app.SetStartOnBoot(false); err != nil {
		t.Fatalf("SetStartOnBoot(false): %v", err)
	}
	if on, _ := autorunEnabled(); on {
		t.Fatalf("OS value still present after disable")
	}
	if store.GetSettings().StartOnBoot {
		t.Fatalf("stored StartOnBoot = true, want false")
	}
}
