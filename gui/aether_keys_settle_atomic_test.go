package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func seedKeyFile(t *testing.T, dir, name, device string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("device_id = \""+device+"\"\nipv4 = \"192.0.2.1\"\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return path
}

func keyDeviceOf(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m := aetherKeyField.FindStringSubmatch(strings.TrimSpace(line)); m != nil && m[1] == "device_id" {
			return m[2]
		}
	}
	return ""
}

func TestAetherKeysSettleIsAllOrNothing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("an open handle only blocks a rename on Windows, which is where the core runs")
	}
	work, renewal := t.TempDir(), t.TempDir()
	for _, f := range aetherKeyFiles {
		seedKeyFile(t, work, f.name, "old-"+f.name)
		seedKeyFile(t, renewal, f.name, "new-"+f.name)
	}
	if err := aetherKeysMarkReady(renewal); err != nil {
		t.Fatalf("mark ready: %v", err)
	}

	// The third file of four cannot be moved. Whatever the cause, the two
	// before it must not stay moved.
	held, err := os.Open(filepath.Join(work, aetherKeyMasqueFile))
	if err != nil {
		t.Fatalf("hold a key open: %v", err)
	}
	// A failure below must still leave the temp dir removable.
	t.Cleanup(func() { _ = held.Close() })
	if aetherKeysSettle(work, renewal) {
		t.Fatalf("a renewal whose move failed reported success")
	}
	for _, f := range aetherKeyFiles {
		if got := keyDeviceOf(t, work, f.name); got != "old-"+f.name {
			t.Fatalf("%s is %q after a failed settle: the keys in use were replaced part way", f.name, got)
		}
	}
	// Nothing was consumed, so the renewal is still there to finish later.
	for _, f := range aetherKeyFiles {
		if got := keyDeviceOf(t, renewal, f.name); got != "new-"+f.name {
			t.Fatalf("staged %s is %q: a failed settle consumed the new keys", f.name, got)
		}
	}

	// Once the file is free the same renewal settles, and every key is new.
	if err := held.Close(); err != nil {
		t.Fatalf("release the key: %v", err)
	}
	if !aetherKeysSettle(work, renewal) {
		t.Fatalf("the renewal did not settle once the file was free")
	}
	for _, f := range aetherKeyFiles {
		if got := keyDeviceOf(t, work, f.name); got != "new-"+f.name {
			t.Fatalf("%s is %q after a successful settle", f.name, got)
		}
	}
	left, _ := filepath.Glob(filepath.Join(work, "*.previous"))
	if len(left) != 0 {
		t.Fatalf("a settled renewal left the keys it replaced behind: %v", left)
	}
}
