package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The file sets aether 2.3.0 writes per --register kind, measured against
// the shipped binary: no base aether.toml and no aether-masque-gool.toml
// exist on this core, so a list copied from a newer fork would wait for a
// file that never comes.
func TestAetherKeysFilesOf(t *testing.T) {
	cases := map[string][]string{
		"wg":     {"aether-wg.toml"},
		"masque": {"aether-masque.toml"},
		"gool":   {"aether-wg.toml", "aether-wg-secondary.toml"},
		"mim":    {"aether-masque.toml", "aether-masque-secondary.toml"},
		"all": {
			"aether-wg.toml", "aether-wg-secondary.toml",
			"aether-masque.toml", "aether-masque-secondary.toml",
		},
		"": {"aether-wg.toml", "aether-wg-secondary.toml", "aether-masque.toml", "aether-masque-secondary.toml"},
	}
	for kind, want := range cases {
		got := aetherKeysFilesOf(kind)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("kind %q files = %v, want %v", kind, got, want)
		}
	}
}

func TestAetherKeysArguments(t *testing.T) {
	args := buildAetherKeysArguments(aetherKeysSettings{})
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "--register all") {
		t.Fatalf("a blank setting did not register every kind: %s", joined)
	}
	// The core of this vintage has one API flag. PattN's --api-address,
	// --api-sni and --api-host belong to a newer fork and would make the
	// whole run fail on an unknown option.
	for _, absent := range []string{"--api-address", "--api-sni", "--api-host", "--fragment", "--ech", "--tor-reverse", "--psiphon-reverse"} {
		if containsStr(args, absent) {
			t.Fatalf("a blank setting named %q: %s", absent, joined)
		}
	}

	s := aetherKeysSettings{
		Kind: "gool", EnrollAddress: "188.114.97.6:443",
		Fragment: true, FragmentSize: "8-16", FragmentDelay: "2-10",
		Ech: true, Fingerprint: "chrome-120", Carrier: "psiphon",
	}
	args = buildAetherKeysArguments(s)
	joined = strings.Join(args, " ")
	for _, want := range []string{
		"--register gool", "--enroll-address 188.114.97.6:443",
		"--fragment", "--fragment-size 8-16", "--fragment-delay 2-10",
		"--ech auto", "--ech-dns " + aetherECHDNS, "--ech-domain " + aetherECHDomain,
		"--tls-groups X25519:P-256:P-384", "--psiphon-reverse",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q: %s", want, joined)
		}
	}
	// An out-of-range bound is dropped, not handed to the core.
	s.FragmentSize, s.FragmentDelay = "0-99999", "nope"
	args = buildAetherKeysArguments(s)
	if containsStr(args, "--fragment-size") || containsStr(args, "--fragment-delay") {
		t.Fatalf("an invalid range reached the core: %v", args)
	}
	// A value that reads as an option of its own never becomes one.
	s.EnrollAddress = "--tor-only"
	if containsStr(buildAetherKeysArguments(s), "--tor-only") {
		t.Fatalf("an option-shaped address was passed through: %v", buildAetherKeysArguments(s))
	}
}

func TestAetherKeysProblem(t *testing.T) {
	if err := aetherKeysProblem(aetherKeysSettings{}); err != nil {
		t.Fatalf("blank settings refused: %v", err)
	}
	for _, bad := range []aetherKeysSettings{
		{EnrollAddress: "not a host"},
		{EnrollAddress: "1.2.3.4:0"},
		{EnrollAddress: "1.2.3.4:99999"},
		{Fragment: true, FragmentSize: "0-99999"},
		{Fragment: true, FragmentDelay: "abc"},
		{Ech: true, EchDNS: "nonsense"},
		{Ech: true, EchDomain: "-bad"},
	} {
		if err := aetherKeysProblem(bad); err == nil {
			t.Fatalf("accepted an unusable setting: %+v", bad)
		}
	}
	for _, good := range []aetherKeysSettings{
		{EnrollAddress: "api.cloudflareclient.com"},
		{EnrollAddress: "2606:4700::1"},
		{EnrollAddress: "[2606:4700::1]:8443"},
		{Fragment: true, FragmentSize: "8-16", FragmentDelay: "2-10"},
		{Ech: true, EchDNS: "udp://1.1.1.1", EchDomain: "cloudflare-ech.com"},
	} {
		if err := aetherKeysProblem(good); err != nil {
			t.Fatalf("refused a usable setting %+v: %v", good, err)
		}
	}
}

func TestAetherKeysCustomCommandKind(t *testing.T) {
	for _, command := range []string{
		"aether --wg", "aether --register", "aether --register --fragment",
		"aether --register unknown", "aether --register wg --register",
	} {
		t.Run(command, func(t *testing.T) {
			if err := aetherKeysProblem(aetherKeysSettings{Command: command}); err == nil {
				t.Fatal("accepted a command without a supported registration kind")
			}
		})
	}
}

func TestAetherKeysRenewUsesCommandKind(t *testing.T) {
	_, state := installFakeAether(t)
	work := aetherWorkDir()
	cases := []struct {
		name    string
		setting aetherKeysSettings
		kind    string
		files   []string
	}{
		{"generated", aetherKeysSettings{Kind: "wg"}, "wg", []string{aetherKeyWgFile}},
		{"custom-wg", aetherKeysSettings{Kind: "all", Command: "aether --register wg"}, "wg", []string{aetherKeyWgFile}},
		{"custom-masque", aetherKeysSettings{Kind: "all", Command: "aether --register masque"}, "masque", []string{aetherKeyMasqueFile}},
		{"custom-gool", aetherKeysSettings{Kind: "masque", Command: "aether --register gool"}, "gool", []string{aetherKeyWgFile, aetherKeyWgInnerFile}},
		{"custom-mim", aetherKeysSettings{Kind: "wg", Command: "aether --register mim"}, "mim", []string{aetherKeyMasqueFile, aetherKeyMasqueInnerFile}},
		{"custom-all", aetherKeysSettings{Kind: "wg", Command: "aether --register all"}, "all", []string{aetherKeyWgFile, aetherKeyWgInnerFile, aetherKeyMasqueFile, aetherKeyMasqueInnerFile}},
		{"last-kind", aetherKeysSettings{Kind: "all", Command: "aether --register all --register wg"}, "wg", []string{aetherKeyWgFile}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAetherScanTestApp(t)
			for _, f := range aetherKeyFiles {
				seedKeyFile(t, work, f.name, "old-"+f.name)
			}
			infos, err := a.AetherRenewKeys(tc.setting)
			if err != nil {
				t.Fatalf("renew %s: %v", tc.kind, err)
			}
			if len(infos) != len(aetherKeyFiles) {
				t.Fatalf("got %d identity rows, want %d", len(infos), len(aetherKeyFiles))
			}
			status, err := a.AetherKeysProgress()
			if err != nil || status.Running || status.Kind != tc.kind || status.LastOKAt == "" || status.LastError != "" {
				t.Fatalf("unexpected renewal status: %+v, %v", status, err)
			}
			for _, f := range aetherKeyFiles {
				want := "old-" + f.name
				if containsStr(tc.files, f.name) {
					want = "00000000-1111-2222-3333-444444444444"
				}
				if got := keyDeviceOf(t, work, f.name); got != want {
					t.Errorf("wrong identity installed for %s", f.name)
				}
			}
		})
	}
	if runs := fakeAetherRuns(state); len(runs) != len(cases) {
		t.Fatalf("got %d child runs, want %d", len(runs), len(cases))
	}
}

func TestAetherKeysRenewRejectsIncompleteCommandKind(t *testing.T) {
	installFakeAether(t)
	t.Setenv(fakeAetherModeEnv, "register-incomplete")
	a, _ := newAetherScanTestApp(t)
	work := aetherWorkDir()
	for _, f := range aetherKeyFiles {
		seedKeyFile(t, work, f.name, "old-"+f.name)
	}
	_, err := a.AetherRenewKeys(aetherKeysSettings{Kind: "masque", Command: "aether --register mim"})
	if err == nil || !strings.Contains(err.Error(), aetherKeyMasqueInnerFile) {
		t.Fatalf("missing inner identity was not rejected: %v", err)
	}
	for _, f := range aetherKeyFiles {
		if got := keyDeviceOf(t, work, f.name); got != "old-"+f.name {
			t.Errorf("incomplete renewal replaced %s", f.name)
		}
	}
}

func TestAetherKeyCopyFailureLeavesNoDestination(t *testing.T) {
	work := t.TempDir()
	dst := filepath.Join(work, aetherKeyWgFile)
	// Reading a directory forces an I/O error after the destination is opened.
	if err := aetherKeyCopy(t.TempDir(), dst); err == nil {
		t.Fatal("copy unexpectedly succeeded")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("failed copy left a destination behind: %v", err)
	}
	src := seedKeyFile(t, t.TempDir(), aetherKeyWgFile, "retry")
	if err := aetherKeyCopy(src, dst); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := keyDeviceOf(t, work, aetherKeyWgFile); got != "retry" {
		t.Fatal("retry did not copy the identity")
	}
}

func TestAetherKeyCopyPreservesExistingDestination(t *testing.T) {
	work := t.TempDir()
	dst := seedKeyFile(t, work, aetherKeyWgFile, "old")
	src := seedKeyFile(t, t.TempDir(), aetherKeyWgFile, "new")
	if err := aetherKeyCopy(src, dst); err == nil {
		t.Fatal("copy replaced an existing destination")
	}
	if got := keyDeviceOf(t, work, aetherKeyWgFile); got != "old" {
		t.Fatal("failed copy touched the existing identity")
	}
}

func TestAetherKeysReadyLine(t *testing.T) {
	ready := "[2026-11-17T16:05:37.748Z INFO  aether] [+] identities ready: wireguard, wireguard inner, masque, masque inner"
	if !aetherKeysReadyLine(ready) {
		t.Fatalf("the core's own success line did not match: %s", ready)
	}
	for _, other := range []string{
		"[+] provisioned and saved new warp identity to aether-wg.toml",
		"[+] wireguard identity ready: device=x ipv4=192.0.2.2 ipv6=2001:db8::2",
		"identities ready: ",
		"Error: Other(\"--register takes masque, wg, gool, mim or all\")",
	} {
		if aetherKeysReadyLine(other) {
			t.Fatalf("matched a line that is not the success word: %s", other)
		}
	}
}

const aetherKeysFixture = `device_id = "00000000-1111-2222-3333-444444444444"
access_token = "secret-token"
cert_pem = """
-----BEGIN CERTIFICATE-----
secret
-----END CERTIFICATE-----
"""
ipv4 = "192.0.2.2"
ipv6 = "2001:db8::2"
wg_private_key = "secret-key"
`

func TestAetherKeyRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aether-wg.toml")
	if err := os.WriteFile(path, []byte(aetherKeysFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	info, ok := aetherKeyRead(path)
	if !ok {
		t.Fatalf("a readable identity was not read")
	}
	if info.DeviceID != "00000000-1111-2222-3333-444444444444" || info.IPv4 != "192.0.2.2" || info.IPv6 != "2001:db8::2" {
		t.Fatalf("identity fields = %+v", info)
	}
	if !info.Present {
		t.Fatalf("a present key reported absent")
	}
	// A file with no device id is not an identity: the settle must not
	// move it over a working key.
	bad := filepath.Join(dir, "empty.toml")
	if err := os.WriteFile(bad, []byte("access_token = \"x\"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, ok := aetherKeyRead(bad); ok {
		t.Fatalf("a file with no device id read as an identity")
	}
	if _, ok := aetherKeyRead(filepath.Join(dir, "missing.toml")); ok {
		t.Fatalf("a missing file read as an identity")
	}
}

func TestAetherKeysSettle(t *testing.T) {
	work, renewal := t.TempDir(), t.TempDir()
	old := filepath.Join(work, "aether-wg.toml")
	if err := os.WriteFile(old, []byte("device_id = \"old\"\nipv4 = \"192.0.2.1\"\n"), 0o600); err != nil {
		t.Fatalf("seed work key: %v", err)
	}
	untouched := filepath.Join(work, "aether-masque.toml")
	if err := os.WriteFile(untouched, []byte("device_id = \"masque\"\n"), 0o600); err != nil {
		t.Fatalf("seed other key: %v", err)
	}

	// A run that wrote only one of the two files it was asked for has not
	// finished: the keys in use must stay exactly as they were.
	if err := os.WriteFile(filepath.Join(renewal, "aether-wg.toml"), []byte(aetherKeysFixture), 0o600); err != nil {
		t.Fatalf("stage one key: %v", err)
	}
	if aetherKeysSettle(work, renewal) {
		t.Fatalf("an unmarked, partial renewal was settled")
	}
	if b, _ := os.ReadFile(old); !strings.Contains(string(b), `device_id = "old"`) {
		t.Fatalf("a partial renewal replaced the key in use: %s", b)
	}

	// The marker is written only once every file of the kind is there.
	if err := os.WriteFile(filepath.Join(renewal, "aether-wg-secondary.toml"), []byte(aetherKeysFixture), 0o600); err != nil {
		t.Fatalf("stage second key: %v", err)
	}
	if err := aetherKeysMarkReady(renewal); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if !aetherKeysSettle(work, renewal) {
		t.Fatalf("a complete renewal did not settle")
	}
	if b, _ := os.ReadFile(old); !strings.Contains(string(b), "00000000-1111-2222-3333-444444444444") {
		t.Fatalf("the new key did not take the old one's place: %s", b)
	}
	if b, _ := os.ReadFile(untouched); !strings.Contains(string(b), `device_id = "masque"`) {
		t.Fatalf("a key of another kind was touched: %s", b)
	}
	if _, err := os.Stat(filepath.Join(renewal, "aether-wg.toml")); !os.IsNotExist(err) {
		t.Fatalf("the staged key stayed behind after the move")
	}
	// Settling twice is a no-op, not an error: a status read may call it.
	if aetherKeysSettle(work, renewal) {
		t.Fatalf("an emptied renewal settled again")
	}
}

func TestAetherKeysSettleLeavesUnreadableStagedKey(t *testing.T) {
	work, renewal := t.TempDir(), t.TempDir()
	old := filepath.Join(work, "aether-wg.toml")
	if err := os.WriteFile(old, []byte("device_id = \"old\"\n"), 0o600); err != nil {
		t.Fatalf("seed work key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(renewal, "aether-wg.toml"), []byte("truncated"), 0o600); err != nil {
		t.Fatalf("stage a corrupt key: %v", err)
	}
	if err := aetherKeysMarkReady(renewal); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if aetherKeysSettle(work, renewal) {
		t.Fatalf("a staged file that is no identity was settled")
	}
	if b, _ := os.ReadFile(old); !strings.Contains(string(b), `device_id = "old"`) {
		t.Fatalf("a corrupt staged key replaced the working one: %s", b)
	}
}

// A renewal rewrites the identities a scan dials with, so the two share
// the core's one slot in both directions.
func TestAetherKeysAndScanShareOneSlot(t *testing.T) {
	a, _ := newAetherScanTestApp(t)

	_, keysCancel, err := a.aetherKeysBegin(time.Minute)
	if err != nil {
		t.Fatalf("keys begin: %v", err)
	}
	if _, _, err := a.aetherScanBegin(time.Minute); err == nil || !strings.Contains(err.Error(), "WARP key") {
		t.Fatalf("a scan started while a renewal held the slot: %v", err)
	}
	a.aetherKeysEnd(keysCancel)

	_, scanCancel, err := a.aetherScanBegin(time.Minute)
	if err != nil {
		t.Fatalf("scan begin: %v", err)
	}
	if _, err := a.AetherRenewKeys(aetherKeysSettings{}); err == nil || !strings.Contains(err.Error(), "scan is running") {
		t.Fatalf("a renewal started while a scan held the slot: %v", err)
	}
	a.aetherScanEnd(scanCancel)

	_, lastCancel, err := a.aetherKeysBegin(time.Minute)
	if err != nil {
		t.Fatalf("the slot was not freed: %v", err)
	}
	a.aetherKeysEnd(lastCancel)
}
