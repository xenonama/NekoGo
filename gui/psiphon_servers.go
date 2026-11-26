package main

// The Psiphon server list NekoGo ships, so the core's first connection
// does not depend on downloading the list itself — the step a filtered
// network blocks. Ported from PattN's PsiphonServerList.

import (
	"bytes"
	"compress/zlib"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// psiphonBundledList is the signed, compressed list as Psiphon publishes
// it. It rides in the binary rather than bin\ so every build ships it:
// build.bat stages each engine asset by name, and a list left out of that
// staging would silently leave the checkbox below doing nothing again.
//
//go:embed psiphon_servers.dat
var psiphonBundledList []byte

const (
	psiphonEntriesFile      = "psiphon-servers.txt"
	psiphonEntriesStampFile = "psiphon-servers.stamp"

	// psiphonServersFile is the downloaded, signed list kept in the aether
	// work dir. The core is handed the unpacked entries, never this file.
	psiphonServersFile = "psiphon_servers.dat"
	// psiphonServersURL is Psiphon's own server_list_compressed, the
	// address its client (and PattN) reads.
	psiphonServersURL = "https://s3.amazonaws.com//psiphon/web/mjr4-p23r-puwl/server_list_compressed"
	// psiphonMinimumEntries is PattN's floor: fewer lines is not the list.
	psiphonMinimumEntries = 50
)

// psiphonSigningKey is Psiphon's public key for its server list, the one
// its client checks downloads with; the core carries the same key.
const psiphonSigningKey = "MIICIDANBgkqhkiG9w0BAQEFAAOCAg0AMIICCAKCAgEAt7Ls+/39r+T6zNW7GiVpJfzq/xvL9SBH" +
	"5rIFnk0RXYEYavax3WS6HOD35eTAqn8AniOwiH+DOkvgSKF2caqk/y1dfq47Pdymtwzp9ikpB1C5" +
	"OfAysXzBiwVJlCdajBKvBZDerV1cMvRzCKvKwRmvDmHgphQQ7WfXIGbRbmmk6opMBh3roE42Kcot" +
	"LFtqp0RRwLtcBRNtCdsrVsjiI1Lqz/lH+T61sGjSjQ3CHMuZYSQJZo/KrvzgQXpkaCTdbObxHqb6" +
	"/+i1qaVOfEsvjoiyzTxJADvSytVtcTjijhPEV6XskJVHE1Zgl+7rATr/pDQkw6DPCNBS1+Y6fy7G" +
	"stZALQXwEDN/qhQI9kWkHijT8ns+i1vGg00Mk/6J75arLhqcodWsdeG/M/moWgqQAnlZAGVtJI1O" +
	"geF5fsPpXu4kctOfuZlGjVZXQNW34aOzm8r8S0eVZitPlbhcPiR4gT/aSMz/wd8lZlzZYsje/Jr8" +
	"u/YtlwjjreZrGRmG8KMOzukV3lLmMppXFMvl4bxv6YFEmIuTsOhbLTwFgh7KYNjodLj/LsqRVfwz" +
	"31PgWQFTEPICV7GCvgVlPRxnofqKSjgTWI4mxDhBpVcATvaoBl1L/6WLbFvBsoAUBItWwctO2xal" +
	"KxF5szhGm8lccoc5MZr8kfE0uxMgsxz4er68iCID+rsCAQM="

// psiphonListKey is the key every list NekoGo unpacks or accepts is checked
// against. A variable so tests can sign with a key of their own.
var psiphonListKey = psiphonSigningKey

// psiphonEntriesPath resolves the plain entries file the core is handed,
// "" when no usable list is available. A variable so a test needs no
// unpacking of the shipped asset.
var psiphonEntriesPath = defaultPsiphonEntriesPath

func defaultPsiphonEntriesPath() string {
	work := aetherWorkDir()
	// A downloaded list is the list in use: the update row writes it here
	// only after Psiphon's signature checks out, and a hand-replaced file
	// has to pass the same check before the core is handed it.
	if packed, err := os.ReadFile(filepath.Join(work, psiphonServersFile)); err == nil {
		if _, err := psiphonUnpack(packed, psiphonListKey); err == nil {
			if path, err := psiphonEntriesFileFor(packed, work, psiphonListKey); err == nil {
				return path
			}
		}
	}
	path, err := psiphonEntriesFileFor(psiphonBundledList, work, psiphonSigningKey)
	if err != nil {
		return ""
	}
	return path
}

// psiphonActiveListPath is where a downloaded list is kept, beside the
// identity files the core reads.
func psiphonActiveListPath() string {
	return filepath.Join(aetherWorkDir(), psiphonServersFile)
}

// psiphonEntryCount counts the entries of an unpacked list, blank lines
// included as none — the way PattN's build counts them.
func psiphonEntryCount(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// psiphonVerifyList checks a downloaded list before it takes the place of
// the one in use: Psiphon's signature, and at least psiphonMinimumEntries
// entries. It returns the entry count; a list that is not Psiphon's is
// refused with the reason.
func psiphonVerifyList(packed []byte, signingKey string) (int, error) {
	text, err := psiphonUnpack(packed, signingKey)
	if err != nil {
		return 0, err
	}
	n := psiphonEntryCount(text)
	if n < psiphonMinimumEntries {
		return n, fmt.Errorf("only %d server entries in the list; that is not the list", n)
	}
	return n, nil
}

// psiphonBundledArgs renders --psiphon-server-entries. nil means the
// default, which is the bundled list; only an explicit no fetches fresh.
// With no list to hand over the flag is left out and the core fetches,
// so a list that cannot be unpacked degrades to today's behaviour.
func psiphonBundledArgs(want *bool) []string {
	if want != nil && !*want {
		return nil
	}
	path := psiphonEntriesPath()
	// The core runs with the aether work dir as its working directory, so
	// only an absolute path names the file this resolved.
	if path == "" || !filepath.IsAbs(path) {
		return nil
	}
	return []string{"--psiphon-server-entries", filepath.Clean(path)}
}

// psiphonEntriesFileFor unpacks the signed list into workDir, once per
// version of it. The entries kept from before are returned when the list
// cannot be used: an older list still dials, no list at all does not.
func psiphonEntriesFileFor(packed []byte, workDir, signingKey string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("no aether work directory to unpack the server list into")
	}
	entries := filepath.Join(workDir, psiphonEntriesFile)
	mark := filepath.Join(workDir, psiphonEntriesStampFile)
	sum := sha256.Sum256(packed)
	stamp := fmt.Sprintf("%d:%x", len(packed), sum)
	if old, err := os.ReadFile(mark); err == nil && string(old) == stamp {
		if _, err := os.Stat(entries); err == nil {
			return entries, nil
		}
	}
	text, err := psiphonUnpack(packed, signingKey)
	if err != nil {
		return psiphonEntriesFallback(entries, err)
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return psiphonEntriesFallback(entries, err)
	}
	// The session and the editor may both unpack a new list; each writes
	// under a name of its own.
	fresh := filepath.Join(workDir, fmt.Sprintf("%s.%d.new", psiphonEntriesFile, time.Now().UnixNano()))
	if err := os.WriteFile(fresh, []byte(text), 0o600); err != nil {
		return psiphonEntriesFallback(entries, err)
	}
	if err := os.Rename(fresh, entries); err != nil {
		// Windows refuses a rename over a file another process holds.
		os.Remove(entries)
		if err := os.Rename(fresh, entries); err != nil {
			os.Remove(fresh)
			return psiphonEntriesFallback(entries, err)
		}
	}
	// A stamp that cannot be written only costs a re-unpack next start.
	_ = os.WriteFile(mark, []byte(stamp), 0o600)
	return entries, nil
}

func psiphonEntriesFallback(entries string, err error) (string, error) {
	if _, statErr := os.Stat(entries); statErr == nil {
		return entries, nil
	}
	return "", err
}

// psiphonUnpack returns the server entries inside packed, a signed,
// compressed list as Psiphon writes it. Psiphon names the key by the
// digest of its text as written, not of the key's bytes, and signs the
// entry text with it.
func psiphonUnpack(packed []byte, signingKey string) (string, error) {
	text, err := psiphonInflate(packed)
	if err != nil {
		return "", err
	}
	var pkg struct {
		Data      string `json:"data"`
		KeyDigest string `json:"signingPublicKeyDigest"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(text, &pkg); err != nil {
		return "", fmt.Errorf("the list is not the package Psiphon writes: %w", err)
	}
	if pkg.Data == "" || pkg.KeyDigest == "" || pkg.Signature == "" {
		return "", fmt.Errorf("the list is not the package Psiphon writes")
	}
	want := sha256.Sum256([]byte(signingKey))
	digest, err := base64.StdEncoding.DecodeString(pkg.KeyDigest)
	if err != nil || !bytes.Equal(digest, want[:]) {
		return "", fmt.Errorf("the list is signed with another key")
	}
	sig, err := base64.StdEncoding.DecodeString(pkg.Signature)
	if err != nil {
		return "", fmt.Errorf("the list is not encoded the way Psiphon writes it: %w", err)
	}
	der, err := base64.StdEncoding.DecodeString(signingKey)
	if err != nil {
		return "", fmt.Errorf("the signing key is not encoded the way Psiphon writes it: %w", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", fmt.Errorf("the signing key cannot be read: %w", err)
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("the signing key is not an RSA key")
	}
	sum := sha256.Sum256([]byte(pkg.Data))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return "", fmt.Errorf("the signature of the list does not check out: %w", err)
	}
	if strings.TrimSpace(pkg.Data) == "" {
		return "", fmt.Errorf("the list is empty")
	}
	return pkg.Data, nil
}

func psiphonInflate(packed []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("the list is not compressed the way Psiphon writes it: %w", err)
	}
	defer zr.Close()
	text, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("the list is not compressed the way Psiphon writes it: %w", err)
	}
	return text, nil
}
