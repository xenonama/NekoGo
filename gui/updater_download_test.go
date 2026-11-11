package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Hermetic tests for the updater's download path. Every server here is an
// httptest server on loopback; nothing reaches GitHub or a CDN.
//
// The scenario these model is a link that is slow and intermittently dies
// (mobile data, a filtered network): bytes arrive, the connection drops,
// the network comes back. An update must resume rather than restart, must
// notice a dead connection instead of blocking forever, and must not retry
// a permanent failure.

// fastDownloadPolicy shrinks the resilience waits so a test finishes in
// milliseconds instead of the production minutes.
func fastDownloadPolicy(t *testing.T) {
	t.Helper()
	idle, attempts, delay, progress := downloadIdleTimeout, downloadMaxAttempts, downloadRetryDelay, downloadProgressEvery
	downloadIdleTimeout = 300 * time.Millisecond
	downloadMaxAttempts = 4
	downloadRetryDelay = time.Millisecond
	downloadProgressEvery = 2 * time.Second
	t.Cleanup(func() {
		downloadIdleTimeout, downloadMaxAttempts = idle, attempts
		downloadRetryDelay, downloadProgressEvery = delay, progress
	})
}

// downloadPayload is deterministic and larger than a single socket buffer
// so a server can cut it at a byte offset the client can resume from.
func downloadPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// abortDrop writes the first cut bytes of payload and then kills the
// connection without finishing the declared Content-Length, which is what
// a network drop looks like to a client.
func abortDrop(w http.ResponseWriter, payload []byte, cut int) {
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload[:cut])
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	panic(http.ErrAbortHandler)
}

func serveWhole(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func servePartial(w http.ResponseWriter, payload []byte, start int) {
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)-start))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(payload[start:])
}

// TestDownloadFileResumesAfterConnectionDrop: the link dies partway
// through. The retry must ask for the missing tail with a Range header
// instead of re-downloading the bytes it already has — on a slow link
// restarting a 70 MB installer at 90% is the difference between an update
// that finishes and one that never does.
func TestDownloadFileResumesAfterConnectionDrop(t *testing.T) {
	fastDownloadPolicy(t)
	payload := downloadPayload(200_000)
	const cut = 60_000
	var attempts int32
	var gotRange atomic.Value
	gotRange.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			abortDrop(w, payload, cut)
			return
		}
		gotRange.Store(r.Header.Get("Range"))
		start := 0
		if r.Header.Get("Range") == fmt.Sprintf("bytes=%d-", cut) {
			start = cut
		}
		if start > 0 {
			servePartial(w, payload, start)
			return
		}
		serveWhole(w, payload)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("gui", srv.URL+"/asset")
	if err != nil {
		t.Fatalf("downloadFile after a mid-body drop: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want the %d-byte payload intact", len(got), len(payload))
	}
	if want := fmt.Sprintf("bytes=%d-", cut); gotRange.Load().(string) != want {
		t.Errorf("retry sent Range %q, want %q (no resume: the whole file was fetched again)", gotRange.Load(), want)
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("attempts = %d, want 2", n)
	}
}

// TestDownloadFileRetriesTransientHTTPStatus: a 5xx is the source or a
// proxy hiccuping, not a permanent answer. One retry must succeed.
func TestDownloadFileRetriesTransientHTTPStatus(t *testing.T) {
	fastDownloadPolicy(t)
	payload := downloadPayload(4096)
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		serveWhole(w, payload)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("nekobox", srv.URL+"/core.exe")
	if err != nil {
		t.Fatalf("downloadFile after a 503: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(payload))
	}
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("attempts = %d, want 2", n)
	}
}

// TestDownloadFileAbortsAStalledBody: the network goes away without a FIN
// — the socket stays open and no byte ever arrives again. downloadHTTP has
// no whole-request timeout on purpose (a slow link needs minutes), so the
// body read needs its own idle deadline or the update hangs forever.
func TestDownloadFileAbortsAStalledBody(t *testing.T) {
	fastDownloadPolicy(t)
	downloadMaxAttempts = 1
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	a := newUpdaterTestApp(t)
	done := make(chan error, 1)
	go func() {
		_, err := a.downloadFile("gui", srv.URL+"/asset")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled body returned success with 5 of 1000000 bytes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downloadFile still blocked 5s after the body stalled: no idle timeout, so a dropped network hangs the update forever")
	}
}

// TestDownloadFileRestartsWhenRangeIsIgnored: not every source supports
// Range (a CDN or a proxy may answer 200 with the whole file). Appending
// that to the partial buffer would install a corrupted binary whose own
// published digest then fails — or worse, a source with no digest.
func TestDownloadFileRestartsWhenRangeIsIgnored(t *testing.T) {
	fastDownloadPolicy(t)
	payload := downloadPayload(150_000)
	const cut = 50_000
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			abortDrop(w, payload, cut)
			return
		}
		// Ignores Range entirely: 200 + the complete file.
		serveWhole(w, payload)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("gui", srv.URL+"/asset")
	if err != nil {
		t.Fatalf("downloadFile against a source that ignores Range: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want exactly %d (a 200 answer must replace the partial buffer, not extend it)", len(got), len(payload))
	}
}

// TestDownloadFileRejectsMisalignedContentRange: a 206 whose start is not
// the offset we asked for must never be spliced onto the partial buffer.
// The downloader has to notice and start over.
func TestDownloadFileRejectsMisalignedContentRange(t *testing.T) {
	fastDownloadPolicy(t)
	payload := downloadPayload(120_000)
	const cut = 40_000
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&attempts, 1) {
		case 1:
			abortDrop(w, payload, cut)
		case 2:
			// Lies about the offset: claims 0 while we asked for cut.
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload)
		default:
			serveWhole(w, payload)
		}
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("gui", srv.URL+"/asset")
	if err != nil {
		t.Fatalf("downloadFile after a misaligned 206: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want the intact %d-byte payload (a misaligned Content-Range was spliced in)", len(got), len(payload))
	}
}

// TestDownloadFileDoesNotRetryPermanentStatus: a 404 is an answer, not a
// hiccup. Retrying it five times only delays the error the user needs to
// see (and hammers the source).
func TestDownloadFileDoesNotRetryPermanentStatus(t *testing.T) {
	fastDownloadPolicy(t)
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	_, err := a.downloadFile("gui", srv.URL+"/missing")
	if err == nil {
		t.Fatal("a 404 returned success")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not mention the status", err)
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("attempts = %d, want 1 (a permanent status must not be retried)", n)
	}
}

// TestDownloadFileLogsProgress: on a slow link the Updates row shows only
// a spinner, so the log is the one place a user can tell "still moving"
// from "stuck". Intermediate progress must be reported, not just the final
// byte count.
func TestDownloadFileLogsProgress(t *testing.T) {
	fastDownloadPolicy(t)
	downloadProgressEvery = 0
	payload := downloadPayload(300_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		for off := 0; off < len(payload); off += 30_000 {
			end := off + 30_000
			if end > len(payload) {
				end = len(payload)
			}
			_, _ = w.Write(payload[off:end])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	if _, err := a.downloadFile("gui", srv.URL+"/asset"); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	progress := 0
	for _, e := range a.RecentLogs(500) {
		if e.Source == "update:gui" && strings.Contains(e.Message, "%") {
			progress++
		}
	}
	if progress == 0 {
		t.Error("no intermediate progress line was logged: a slow download looks identical to a hung one")
	}
}

// TestDownloadFileErrorRedactsSignedURL: GitHub asset downloads redirect to
// signed CDN URLs whose query string is a bearer credential. The error text
// reaches the log buffer, the on-disk log and the Updates row, so it must
// name the host and path, not the signature.
func TestDownloadFileErrorRedactsSignedURL(t *testing.T) {
	fastDownloadPolicy(t)
	downloadMaxAttempts = 1
	const secret = "SECRETSIG9df87"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Port 1 is not listening: the redirect target fails at dial time,
		// which is where net/http puts the target URL into the error.
		http.Redirect(w, r, "http://127.0.0.1:1/asset?X-Amz-Signature="+secret, http.StatusFound)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	_, err := a.downloadFile("gui", srv.URL+"/asset")
	if err == nil {
		t.Fatal("a download to a dead redirect target returned success")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("download error leaks the signed URL: %v", err)
	}
	for _, e := range a.RecentLogs(200) {
		if strings.Contains(e.Message, secret) {
			t.Errorf("log line leaks the signed URL: %q", e.Message)
		}
	}
}

// TestDownloadFileResumesACleanShortBody: a proxy can satisfy the part's
// Content-Length and still stop short of the total its Content-Range
// promised. No read error is raised, so only the length check catches it —
// installing those bytes would put a truncated core on disk.
func TestDownloadFileResumesACleanShortBody(t *testing.T) {
	fastDownloadPolicy(t)
	payload := downloadPayload(1000)
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			// 100 bytes delivered cleanly, but the range claims 1000 exist.
			w.Header().Set("Content-Range", "bytes 0-99/1000")
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:100])
			return
		}
		if r.Header.Get("Range") != "bytes=100-" {
			t.Errorf("retry sent Range %q, want bytes=100-", r.Header.Get("Range"))
		}
		servePartial(w, payload, 100)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("gui", srv.URL+"/asset")
	if err != nil {
		t.Fatalf("downloadFile after a clean short body: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want the full %d", len(got), len(payload))
	}
}

// TestDownloadFileIfRangeGuardsAChangedAsset: the point of If-Range. If the
// asset changed between attempts the source answers 200 with the new file,
// and the partial bytes from the old one must be thrown away — splicing them
// would produce a binary that is neither release.
func TestDownloadFileIfRangeGuardsAChangedAsset(t *testing.T) {
	fastDownloadPolicy(t)
	oldPayload := downloadPayload(800)
	newPayload := make([]byte, 800)
	for i := range newPayload {
		newPayload[i] = byte('A' + i%26)
	}
	var attempts int32
	var sawIfRange atomic.Value
	sawIfRange.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("ETag", `"release-one"`)
			abortDrop(w, oldPayload, 300)
			return
		}
		sawIfRange.Store(r.Header.Get("If-Range"))
		// The asset changed: If-Range fails, so the whole new file comes back.
		w.Header().Set("ETag", `"release-two"`)
		serveWhole(w, newPayload)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("gui", srv.URL+"/asset")
	if err != nil {
		t.Fatalf("downloadFile across an asset change: %v", err)
	}
	if string(got) != string(newPayload) {
		t.Fatalf("downloaded %d bytes that are not the new release (old partial bytes were spliced in)", len(got))
	}
	if want := `"release-one"`; sawIfRange.Load().(string) != want {
		t.Errorf("retry sent If-Range %q, want %q", sawIfRange.Load(), want)
	}
}

// TestDownloadFileSkipsWeakValidator: RFC 7233 forbids a weak ETag as an
// If-Range validator — it can match two different bodies, which is exactly
// the splice the header exists to prevent.
func TestDownloadFileSkipsWeakValidator(t *testing.T) {
	fastDownloadPolicy(t)
	payload := downloadPayload(600)
	var attempts int32
	var sawIfRange atomic.Value
	sawIfRange.Store("unset")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("ETag", `W/"weak"`)
			abortDrop(w, payload, 200)
			return
		}
		sawIfRange.Store(r.Header.Get("If-Range"))
		servePartial(w, payload, 200)
	}))
	defer srv.Close()

	a := newUpdaterTestApp(t)
	got, err := a.downloadFile("gui", srv.URL+"/asset")
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(payload))
	}
	if v := sawIfRange.Load().(string); v != "" {
		t.Errorf("a weak ETag was replayed as If-Range: %q", v)
	}
}

func TestContentRangeStart(t *testing.T) {
	cases := []struct {
		in    string
		start int64
		total int64
		ok    bool
	}{
		{"bytes 0-499/1234", 0, 1234, true},
		{"bytes 500-999/1234", 500, 1234, true},
		{"bytes 60000-199999/200000", 60000, 200000, true},
		{"bytes 0-0/*", 0, -1, true},
		{"BYTES 42-42/43", 42, 43, true},
		{"bytes  42-42/43 ", 42, 43, true},
		// A 416 carries this form: it is not a partial answer.
		{"bytes */1234", 0, -1, false},
		{"", 0, -1, false},
		{"bytes 500-499/1234", 0, -1, false},
		{"bytes -1-499/1234", 0, -1, false},
		{"bytes 0-499/499", 0, -1, false},
		{"bytes 0-499/abc", 0, -1, false},
		{"bytes 0-499", 0, -1, false},
		{"bytes abc-499/1234", 0, -1, false},
		{"items 0-499/1234", 0, -1, false},
		{"byte", 0, -1, false},
	}
	for _, tc := range cases {
		start, total, ok := contentRangeStart(tc.in)
		if ok != tc.ok || (ok && (start != tc.start || total != tc.total)) {
			t.Errorf("contentRangeStart(%q) = (%d, %d, %v), want (%d, %d, %v)",
				tc.in, start, total, ok, tc.start, tc.total, tc.ok)
		}
	}
}

func TestRedactURLQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://cdn.example.com/a/b.exe?sig=SECRET&x=1", "https://cdn.example.com/a/b.exe"},
		{"https://user:pass@example.com/x", "https://example.com/x"},
		{"https://example.com/x#frag", "https://example.com/x"},
		{"http://127.0.0.1:8080/asset", "http://127.0.0.1:8080/asset"},
		{"://bad", "(unreadable URL)"},
	}
	for _, tc := range cases {
		if got := redactURLQuery(tc.in); got != tc.want {
			t.Errorf("redactURLQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
