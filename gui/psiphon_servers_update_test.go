package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// psiphonEntriesText builds a signed-list body with n non-blank entries.
func psiphonEntriesText(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "0 0 0 0 {\"ipAddress\":\"203.0.113.%d\"}\n", i%255)
	}
	return b.String()
}

func psiphonTestSigningKey(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return base64.StdEncoding.EncodeToString(spki)
}

// psiphonUpdateEnv pins the aether work dir and the signing key tests use,
// so no test reads or writes the user's own list.
func psiphonUpdateEnv(t *testing.T) (work string, key *rsa.PrivateKey, signingKey string) {
	t.Helper()
	work = filepath.Join(t.TempDir(), "aether")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	prevWork := aetherWorkDirHook
	aetherWorkDirHook = func() string { return work }
	t.Cleanup(func() { aetherWorkDirHook = prevWork })

	key = psiphonTestKey(t)
	signingKey = psiphonTestSigningKey(t, key)
	prevKey := psiphonListKey
	psiphonListKey = signingKey
	t.Cleanup(func() { psiphonListKey = prevKey })
	return work, key, signingKey
}

func writePsiphonActiveList(t *testing.T, packed []byte, at time.Time) {
	t.Helper()
	if err := os.WriteFile(psiphonActiveListPath(), packed, 0o600); err != nil {
		t.Fatal(err)
	}
	if !at.IsZero() {
		if err := os.Chtimes(psiphonActiveListPath(), at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// psiphonListSource is an HTTP server of one signed list. A conditional
// request for a list no newer than what it serves gets 304.
type psiphonListSource struct {
	srv       *httptest.Server
	body      []byte
	published time.Time
	status    int

	mu              sync.Mutex
	headHits        int
	getHits         int
	ifModifiedSince string
}

func newPsiphonListSource(t *testing.T, body []byte, published time.Time) *psiphonListSource {
	t.Helper()
	s := &psiphonListSource{body: body, published: published}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *psiphonListSource) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if r.Method == http.MethodHead {
		s.headHits++
	} else {
		s.getHits++
	}
	status := s.status
	published := s.published
	s.mu.Unlock()

	if r.Method == http.MethodHead {
		if status != 0 {
			http.Error(w, "not this time", status)
			return
		}
		if !published.IsZero() {
			w.Header().Set("Last-Modified", published.UTC().Format(http.TimeFormat))
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if status != 0 {
		http.Error(w, "not this time", status)
		return
	}
	if since := r.Header.Get("If-Modified-Since"); since != "" {
		s.mu.Lock()
		s.ifModifiedSince = since
		s.mu.Unlock()
		if at, err := http.ParseTime(since); err == nil && !at.Before(published) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if !published.IsZero() {
		w.Header().Set("Last-Modified", published.UTC().Format(http.TimeFormat))
	}
	_, _ = w.Write(s.body)
}

func (s *psiphonListSource) hits() (head, get int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headHits, s.getHits
}

func (s *psiphonListSource) askedSince() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ifModifiedSince
}

func (s *psiphonListSource) setStatus(status int) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *psiphonListSource) setPublished(published time.Time) {
	s.mu.Lock()
	s.published = published
	s.mu.Unlock()
}

// The Updates panel needs a row of its own for the list the engine starts
// from, beside the engine that reads it.
func TestPsiphonServersRowReportsTheListInUse(t *testing.T) {
	_, key, _ := psiphonUpdateEnv(t)

	row := psiphonServersRow()
	if row.ID != "psiphon-servers" || row.Name != "Psiphon Servers" {
		t.Fatalf("row = %q/%q, want psiphon-servers/Psiphon Servers", row.ID, row.Name)
	}
	bundled, err := psiphonUnpack(psiphonBundledList, psiphonSigningKey)
	if err != nil {
		t.Fatalf("the shipped list: %v", err)
	}
	if want := fmt.Sprintf("Bundled list (%d entries)", psiphonEntryCount(bundled)); row.Current != want {
		t.Fatalf("Current = %q, want %q", row.Current, want)
	}
	if !strings.Contains(row.Notes, "No downloaded list") {
		t.Fatalf("Notes = %q, want the bundled-list explanation", row.Notes)
	}

	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packed, _ := psiphonTestList(t, key, psiphonEntriesText(60))
	writePsiphonActiveList(t, packed, at)

	row = psiphonServersRow()
	if want := "60 entries (2026-09-01)"; row.Current != want {
		t.Fatalf("Current = %q, want %q", row.Current, want)
	}
	if row.Notes != "" {
		t.Fatalf("Notes = %q, want none", row.Notes)
	}
}

// Check compares the publication time of the published list with the time
// of the downloaded list in use: newer is an update, not newer is current.
func TestPsiphonServersCheckOffersOnlyANewerList(t *testing.T) {
	_, key, _ := psiphonUpdateEnv(t)
	saveUpdateBaseURL(t)

	inUse := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packed, _ := psiphonTestList(t, key, psiphonEntriesText(55))
	writePsiphonActiveList(t, packed, inUse)

	src := newPsiphonListSource(t, packed, inUse.Add(24*time.Hour))
	updateBaseURL["psiphon-servers"] = src.srv.URL
	a := newUpdaterTestApp(t)

	row := psiphonServersRow()
	a.checkPsiphonServers(&row)
	if !row.UpdateAvail {
		t.Fatal("a newer published list did not offer the update")
	}
	if row.Latest != "2026-09-02" {
		t.Fatalf("Latest = %q, want 2026-09-02", row.Latest)
	}
	if head, _ := src.hits(); head != 1 {
		t.Fatalf("HEAD hits = %d, want 1", head)
	}

	src.setPublished(inUse)
	row = psiphonServersRow()
	a.checkPsiphonServers(&row)
	if row.UpdateAvail {
		t.Fatal("a list published at the same time offered an update")
	}
}

// The update asks for nothing older than the list in use, checks what
// arrives, and records the publication time it came with.
func TestPsiphonServersUpdateInstallsANewerSignedList(t *testing.T) {
	_, key, _ := psiphonUpdateEnv(t)
	saveUpdateBaseURL(t)

	inUse := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	oldText := psiphonEntriesText(55)
	oldPacked, _ := psiphonTestList(t, key, oldText)
	writePsiphonActiveList(t, oldPacked, inUse)

	newText := psiphonEntriesText(70)
	newPacked, _ := psiphonTestList(t, key, newText)
	published := inUse.Add(24 * time.Hour)
	src := newPsiphonListSource(t, newPacked, published)
	updateBaseURL["psiphon-servers"] = src.srv.URL
	a := newUpdaterTestApp(t)

	row, err := a.updatePsiphonServers()
	if err != nil {
		t.Fatalf("updatePsiphonServers: %v", err)
	}
	if row.ID != "psiphon-servers" || row.UpdateAvail {
		t.Fatalf("row = %+v, want a current psiphon-servers row", row)
	}
	if want := "70 entries (2026-09-02)"; row.Current != want {
		t.Fatalf("Current = %q, want %q", row.Current, want)
	}
	if !strings.Contains(row.Notes, "Installed 70 server entries") {
		t.Fatalf("Notes = %q, want the installed count", row.Notes)
	}
	got, err := os.ReadFile(psiphonActiveListPath())
	if err != nil || string(got) != string(newPacked) {
		t.Fatalf("the active list was not replaced: %v", err)
	}
	st, err := os.Stat(psiphonActiveListPath())
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(published) {
		t.Fatalf("active list time = %s, want %s", st.ModTime(), published)
	}
	if want := inUse.Format(http.TimeFormat); src.askedSince() != want {
		t.Fatalf("If-Modified-Since = %q, want %q", src.askedSince(), want)
	}

	// The next start unpacks the new list for the core.
	entries := defaultPsiphonEntriesPath()
	if entries == "" {
		t.Fatal("defaultPsiphonEntriesPath returned no path")
	}
	if text, err := os.ReadFile(entries); err != nil || string(text) != newText {
		t.Fatalf("entries = %q, %v; want the downloaded list", text, err)
	}
}

// A 304, or a published time no later than the one in use, leaves the list
// alone and does not write it again.
func TestPsiphonServersUpdateLeavesACurrentListAlone(t *testing.T) {
	_, key, _ := psiphonUpdateEnv(t)
	saveUpdateBaseURL(t)

	inUse := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packed, _ := psiphonTestList(t, key, psiphonEntriesText(55))
	writePsiphonActiveList(t, packed, inUse)

	src := newPsiphonListSource(t, packed, inUse)
	updateBaseURL["psiphon-servers"] = src.srv.URL
	a := newUpdaterTestApp(t)

	row, err := a.updatePsiphonServers()
	if err != nil {
		t.Fatalf("updatePsiphonServers: %v", err)
	}
	if row.Notes != "Already up to date" {
		t.Fatalf("Notes = %q, want Already up to date", row.Notes)
	}
	got, err := os.ReadFile(psiphonActiveListPath())
	if err != nil || string(got) != string(packed) {
		t.Fatalf("the active list changed: %v", err)
	}
	st, err := os.Stat(psiphonActiveListPath())
	if err != nil || !st.ModTime().Equal(inUse) {
		t.Fatalf("active list time = %v, %v; want %s", st, err, inUse)
	}
	if _, get := src.hits(); get != 1 {
		t.Fatalf("GET hits = %d, want 1", get)
	}
}

// A list that is not Psiphon's signed list, or has too few entries, must
// never take the place of the one in use.
func TestPsiphonServersUpdateKeepsTheListWhenTheDownloadIsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		body func(t *testing.T, key *rsa.PrivateKey) []byte
		want string
	}{
		{name: "unsigned", body: func(*testing.T, *rsa.PrivateKey) []byte {
			return []byte("<html>captive portal</html>")
		}, want: "not Psiphon's signed server list"},
		{name: "too few entries", body: func(t *testing.T, key *rsa.PrivateKey) []byte {
			packed, _ := psiphonTestList(t, key, psiphonEntriesText(49))
			return packed
		}, want: "only 49 server entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, key, _ := psiphonUpdateEnv(t)
			saveUpdateBaseURL(t)

			inUse := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			packed, _ := psiphonTestList(t, key, psiphonEntriesText(55))
			writePsiphonActiveList(t, packed, inUse)

			src := newPsiphonListSource(t, tc.body(t, key), inUse.Add(24*time.Hour))
			updateBaseURL["psiphon-servers"] = src.srv.URL
			a := newUpdaterTestApp(t)

			if _, err := a.updatePsiphonServers(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			got, err := os.ReadFile(psiphonActiveListPath())
			if err != nil || string(got) != string(packed) {
				t.Fatalf("an invalid download replaced the active list: %v", err)
			}
			st, err := os.Stat(psiphonActiveListPath())
			if err != nil || !st.ModTime().Equal(inUse) {
				t.Fatalf("active list time = %v, %v; want %s", st, err, inUse)
			}
		})
	}
}

// A failed download leaves the list in use where it was.
func TestPsiphonServersUpdateKeepsTheListWhenTheDownloadFails(t *testing.T) {
	_, key, _ := psiphonUpdateEnv(t)
	saveUpdateBaseURL(t)

	inUse := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	packed, _ := psiphonTestList(t, key, psiphonEntriesText(55))
	writePsiphonActiveList(t, packed, inUse)

	src := newPsiphonListSource(t, packed, inUse.Add(24*time.Hour))
	src.setStatus(http.StatusBadGateway)
	updateBaseURL["psiphon-servers"] = src.srv.URL
	a := newUpdaterTestApp(t)

	if _, err := a.updatePsiphonServers(); err == nil {
		t.Fatal("a failed download reported success")
	}
	got, err := os.ReadFile(psiphonActiveListPath())
	if err != nil || string(got) != string(packed) {
		t.Fatalf("a failed download replaced the active list: %v", err)
	}
}

// A downloaded list wins over the one shipped in the binary; a list that
// no longer checks out hands the core the bundled list again.
func TestPsiphonEntriesUseTheDownloadedListWhenItIsValid(t *testing.T) {
	_, key, _ := psiphonUpdateEnv(t)

	bundled, err := psiphonUnpack(psiphonBundledList, psiphonSigningKey)
	if err != nil {
		t.Fatalf("the shipped list: %v", err)
	}
	path := defaultPsiphonEntriesPath()
	if text, err := os.ReadFile(path); err != nil || string(text) != bundled {
		t.Fatalf("entries = %q, %v; want the bundled list", text, err)
	}

	activeText := psiphonEntriesText(60)
	packed, _ := psiphonTestList(t, key, activeText)
	writePsiphonActiveList(t, packed, time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))

	path = defaultPsiphonEntriesPath()
	if text, err := os.ReadFile(path); err != nil || string(text) != activeText {
		t.Fatalf("entries = %q, %v; want the downloaded list", text, err)
	}

	if err := os.WriteFile(psiphonActiveListPath(), []byte("not a list"), 0o600); err != nil {
		t.Fatal(err)
	}
	path = defaultPsiphonEntriesPath()
	if text, err := os.ReadFile(path); err != nil || string(text) != bundled {
		t.Fatalf("entries = %q, %v; want the bundled list after a broken download", text, err)
	}
}
