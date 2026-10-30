//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Windows autostart: HKCU\...\Run value "NekoGo" pointing at this
// exe. Only stdlib + golang.org/x/sys/windows (already used by the
// GUI system-proxy path) — no new modules.

// autoRunValueName is the Run-key value owned by NekoGo. Never touch
// other values: foreign autostart entries are left alone.
const autoRunValueName = "NekoGo"

// autoRunKeyPath is a variable (not a const) so tests can point it
// at a scratch subkey without touching the real Run key.
var autoRunKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// autoRunCommand returns the quoted command stored in the Run value
// (quotes tolerate install paths with spaces).
func autoRunCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return `"` + exe + `"`, nil
}

// autorunEnabled reports whether our Run value points at this exe
// (ground truth from the OS, like GetSystemProxyState).
func autorunEnabled() (bool, error) {
	want, err := autoRunCommand()
	if err != nil {
		return false, err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, autoRunKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, nil
	}
	defer key.Close()
	got, _, err := key.GetStringValue(autoRunValueName)
	if err != nil {
		return false, nil
	}
	return strings.Trim(got, `"`) == strings.Trim(want, `"`), nil
}

// autorunSet creates/updates our Run value.
func autorunSet() error {
	want, err := autoRunCommand()
	if err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, autoRunKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(autoRunValueName, want); err != nil {
		return fmt.Errorf("write Run value: %w", err)
	}
	return nil
}

// autorunClear removes our Run value. Missing value is success.
func autorunClear() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, autoRunKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(autoRunValueName); err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return fmt.Errorf("delete Run value: %w", err)
	}
	return nil
}
