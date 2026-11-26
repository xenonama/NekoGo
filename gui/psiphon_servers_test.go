package main

import (
	"bytes"
	"compress/zlib"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// psiphonTestEntriesPath is the scratch entries path TestMain pins for the
// package, so no argv builder unpacks the shipped list beside the user's
// WARP identities.
const psiphonTestEntriesPath = `C:\NekoGo\aether\psiphon-servers.txt`

// withPsiphonEntries pins the entries path the bundled-list flag hands
// over, so a test needs no shipped list and no unpacking.
func withPsiphonEntries(t *testing.T, path string) {
	t.Helper()
	old := psiphonEntriesPath
	psiphonEntriesPath = func() string { return path }
	t.Cleanup(func() { psiphonEntriesPath = old })
}

func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// The checkbox promises On = dial the servers NekoGo ships at once, Off =
// let the core fetch a fresh list first. Both states used to emit the
// same argv, so On could not rescue a connection whose list download is
// blocked — the very case the row exists for.
func TestPsiphonBundledListReachesTheCore(t *testing.T) {
	const entries = `C:\NekoGo\aether\psiphon-servers.txt`
	withPsiphonEntries(t, entries)
	fresh := false

	for _, mode := range []string{"chain", "reverse", "only"} {
		p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
			Aether: aetherSettings{Protocol: "masque", Psiphon: mode}}
		if mode == "reverse" {
			p.Aether.Transport = "h2"
		}
		args := buildAetherArguments(p, 10819)
		if !hasFlagValue(args, "--psiphon-server-entries", entries) {
			t.Fatalf("psiphon %s with the default bundled list did not hand over the entries: %v", mode, args)
		}
		p.Aether.PsiphonBundledList = &fresh
		args = buildAetherArguments(p, 10819)
		if containsStr(args, "--psiphon-server-entries") {
			t.Fatalf("psiphon %s with the bundled list off still handed over entries: %v", mode, args)
		}
	}

	// Off means off even with no list to hand over, and a missing list
	// degrades to the core's own fetch rather than a broken flag.
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "masque", Psiphon: "chain"}}
	withPsiphonEntries(t, "")
	if containsStr(buildAetherArguments(p, 10819), "--psiphon-server-entries") {
		t.Fatal("a missing list emitted an empty --psiphon-server-entries")
	}
	// Psiphon off takes no Psiphon flag at all.
	p.Aether.Psiphon = "off"
	withPsiphonEntries(t, entries)
	if containsStr(buildAetherArguments(p, 10819), "--psiphon-server-entries") {
		t.Fatal("psiphon off emitted the entries flag")
	}
}

// The Chain tab's psiphon-only leg runs the same core and must start from
// the same list, or one leg would dial servers the other cannot reach.
func TestPsiphonOnlyLegCarriesTheBundledList(t *testing.T) {
	const entries = `C:\NekoGo\aether\psiphon-servers.txt`
	withPsiphonEntries(t, entries)
	args := psiphonOnlyArgs(10820, 10819, "info", "", DefaultSettings())
	if !hasFlagValue(args, "--psiphon-server-entries", entries) {
		t.Fatalf("the psiphon-only leg did not hand over the entries: %v", args)
	}
}

// The flag takes a real path. PattN's "shipped-list" placeholder is not
// one aether 2.3.0 accepts, and the core runs with the aether work dir as
// its working directory, so a relative path would name the wrong file.
func TestPsiphonBundledArgsNeedsAnAbsolutePath(t *testing.T) {
	withPsiphonEntries(t, "shipped-list")
	if args := psiphonBundledArgs(nil); len(args) != 0 {
		t.Fatalf("bundled args = %v, want none for a placeholder path", args)
	}
}

// psiphonTestList signs data the way Psiphon does and compresses the
// package, returning it with the key text it was signed under.
func psiphonTestList(t *testing.T, key *rsa.PrivateKey, data string) ([]byte, string) {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	signingKey := base64.StdEncoding.EncodeToString(spki)
	digest := sha256.Sum256([]byte(signingKey))
	sum := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("SignPKCS1v15: %v", err)
	}
	pkg, err := json.Marshal(map[string]string{
		"data":                   data,
		"signingPublicKeyDigest": base64.StdEncoding.EncodeToString(digest[:]),
		"signature":              base64.StdEncoding.EncodeToString(sig),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(pkg); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes(), signingKey
}

func psiphonTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return key
}

// The core trusts the entries file it is handed, and the shipped list can
// be replaced by hand, so an unsigned or altered list must never reach it.
func TestPsiphonUnpackChecksTheSignature(t *testing.T) {
	key := psiphonTestKey(t)
	const data = "0102aabb\n0304ccdd\n"
	packed, signingKey := psiphonTestList(t, key, data)

	got, err := psiphonUnpack(packed, signingKey)
	if err != nil || got != data {
		t.Fatalf("Unpack = %q, %v; want the signed entries", got, err)
	}

	other := psiphonTestKey(t)
	otherPacked, otherKey := psiphonTestList(t, other, data)
	if _, err := psiphonUnpack(otherPacked, signingKey); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("a list signed under another key: err = %v, want the key refusal", err)
	}
	if _, err := psiphonUnpack(packed, otherKey); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("the wrong key for this list: err = %v, want the key refusal", err)
	}

	// Re-sign the same package with altered entries: the signature is over
	// the entry text, so a swapped line must not check out.
	var pkg map[string]string
	raw, err := psiphonInflate(packed)
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	pkg["data"] = "0102aabb\n0304eeff\n"
	altered, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(altered)
	zw.Close()
	if _, err := psiphonUnpack(buf.Bytes(), signingKey); err == nil || !strings.Contains(err.Error(), "does not check out") {
		t.Fatalf("altered entries: err = %v, want the signature refusal", err)
	}

	if _, err := psiphonUnpack([]byte("not a zlib stream"), signingKey); err == nil {
		t.Fatal("an uncompressed list was accepted")
	}
	for _, bad := range []string{`{}`, `{"data":"x"}`, `{"data":"x","signature":"x"}`, `[]`, `null`} {
		var b bytes.Buffer
		w := zlib.NewWriter(&b)
		w.Write([]byte(bad))
		w.Close()
		if _, err := psiphonUnpack(b.Bytes(), signingKey); err == nil {
			t.Fatalf("package %s was accepted", bad)
		}
	}
	blank, blankKey := psiphonTestList(t, key, " \n\t\n")
	if _, err := psiphonUnpack(blank, blankKey); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("a blank list: err = %v, want the empty refusal", err)
	}
}

// Unpacking is once per version of the shipped list: a start must not
// rewrite the entries the running core reads, a new list must take their
// place, and a list that cannot be used must leave the old entries alone.
func TestPsiphonEntriesFileUnpacksOncePerListVersion(t *testing.T) {
	key := psiphonTestKey(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "aether")
	const first = "0102aabb\n0304ccdd\n"
	packed, signingKey := psiphonTestList(t, key, first)

	entries, err := psiphonEntriesFileFor(packed, work, signingKey)
	if err != nil {
		t.Fatalf("EntriesFile: %v", err)
	}
	if entries != filepath.Join(work, psiphonEntriesFile) {
		t.Fatalf("entries path = %q, want it beside the keys", entries)
	}
	if text, err := os.ReadFile(entries); err != nil || string(text) != first {
		t.Fatalf("entries = %q, %v; want the unpacked list", text, err)
	}
	stamp, err := os.ReadFile(filepath.Join(work, psiphonEntriesStampFile))
	if err != nil || len(stamp) == 0 {
		t.Fatalf("stamp = %q, %v; want the list mark", stamp, err)
	}
	before, err := os.Stat(entries)
	if err != nil {
		t.Fatalf("stat entries: %v", err)
	}
	again, err := psiphonEntriesFileFor(packed, work, signingKey)
	if err != nil || again != entries {
		t.Fatalf("second EntriesFile = %q, %v", again, err)
	}
	after, err := os.Stat(entries)
	if err != nil {
		t.Fatalf("stat entries: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("an unchanged list was unpacked again, rewriting the file the core reads")
	}

	const second = "0506eeff\n0708aabb\n0900ccdd\n"
	next, _ := psiphonTestList(t, key, second)
	if _, err := psiphonEntriesFileFor(next, work, signingKey); err != nil {
		t.Fatalf("EntriesFile after a new list: %v", err)
	}
	if text, _ := os.ReadFile(entries); string(text) != second {
		t.Fatalf("entries = %q, want the new list %q", text, second)
	}

	// A list that is not Psiphon's leaves the entries already in place.
	kept, err := psiphonEntriesFileFor([]byte("junk"), work, signingKey)
	if err != nil || kept != entries {
		t.Fatalf("EntriesFile with a broken list = %q, %v; want the entries kept", kept, err)
	}
	if text, _ := os.ReadFile(entries); string(text) != second {
		t.Fatalf("a broken list replaced the entries: %q", text)
	}
	if _, err := psiphonEntriesFileFor([]byte("junk"), filepath.Join(dir, "empty"), signingKey); err == nil {
		t.Fatal("a broken list with no entries to keep was accepted")
	}
	if _, err := psiphonEntriesFileFor(packed, "", signingKey); err == nil {
		t.Fatal("no work directory was accepted")
	}
}

// The list NekoGo ships must be Psiphon's own, signed with the key the
// core checks: an embedded asset nobody verifies is a list of servers the
// core would refuse, or worse, one somebody else chose.
func TestPsiphonBundledListIsPsiphonsOwn(t *testing.T) {
	text, err := psiphonUnpack(psiphonBundledList, psiphonSigningKey)
	if err != nil {
		t.Fatalf("the shipped list did not verify: %v", err)
	}
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	// PattN's MinimumEntries: fewer is not the list.
	if n < 50 {
		t.Fatalf("the shipped list carries %d entries, too few to be Psiphon's", n)
	}
}
