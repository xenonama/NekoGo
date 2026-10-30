//go:build !darwin

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nekobox_core/gen"
	"nekobox_core/internal"
	"nekobox_core/internal/boxbox"

	"github.com/apache/thrift/lib/go/thrift"
)

// trafficcontrol.Manager.join is unexported, so the handler's connection
// source cannot be seeded directly. These tests drive capConnections —
// the exact function ListConnections applies — with mock entries, plus
// a wire round-trip proving the hand-applied Truncated field (P3-3;
// thrift compiler absent, see AI log) serializes correctly.
func TestCapConnectionsAt500(t *testing.T) {
	var res []*gen.ConnectionMetaData
	for i := int64(0); i < 600; i++ {
		res = append(res, &gen.ConnectionMetaData{
			ID:        fmt.Sprintf("conn-%d", i),
			CreatedAt: i,
		})
	}
	capped, truncated := capConnections(res, maxListConnections)
	if !truncated {
		t.Fatal("expected truncated=true for 600 entries")
	}
	if len(capped) != 500 {
		t.Fatalf("got %d entries, want 500", len(capped))
	}
	// Newest win: CreatedAt 599 down to 100; the oldest 100 dropped.
	if capped[0].GetCreatedAt() != 599 || capped[499].GetCreatedAt() != 100 {
		t.Fatalf("newest-first window broken: first=%d last=%d",
			capped[0].GetCreatedAt(), capped[499].GetCreatedAt())
	}
}

func TestCapConnectionsUnderLimit(t *testing.T) {
	res := []*gen.ConnectionMetaData{{ID: "a", CreatedAt: 3}, {ID: "b", CreatedAt: 1}}
	capped, truncated := capConnections(res, maxListConnections)
	if truncated {
		t.Fatal("expected truncated=false under the limit")
	}
	if len(capped) != 2 {
		t.Fatalf("got %d entries, want 2", len(capped))
	}
}

func TestListConnectionsRespTruncatedRoundTrip(t *testing.T) {
	ctx := context.Background()
	trans := thrift.NewTMemoryBuffer()
	proto := thrift.NewTBinaryProtocolFactoryDefault().GetProtocol(trans)
	want := &gen.ListConnectionsResp{
		Connections: []*gen.ConnectionMetaData{{ID: "x", CreatedAt: 7}},
		Truncated:   true,
	}
	if err := want.Write(ctx, proto); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := gen.NewListConnectionsResp()
	proto2 := thrift.NewTBinaryProtocolFactoryDefault().GetProtocol(trans)
	if err := got.Read(ctx, proto2); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !got.GetTruncated() {
		t.Fatal("Truncated lost in round-trip")
	}
	if len(got.GetConnections()) != 1 || got.GetConnections()[0].GetID() != "x" {
		t.Fatalf("connections mangled: %+v", got.GetConnections())
	}
	if !want.Equals(got) {
		t.Fatal("Equals reports mismatch after round-trip")
	}
}

// TestStartRejectsArbitraryBinary (BUG-002): a Start request naming an
// arbitrary binary must be rejected with an error and must not spawn a
// process or write any file.
func TestStartRejectsArbitraryBinary(t *testing.T) {
	s := &server{}
	dir := t.TempDir()
	for _, bin := range []string{
		`C:\Windows\System32\cmd.exe`,
		"/bin/sh",
		"evil.exe",
		`..\evil.exe`,
	} {
		in := &gen.LoadConfigReq{
			NeedExtraProcess: true,
			ExtraProcessPath: bin,
			ExtraProcessConf: "secret",
			// A contained dir, so only the binary gate is under test.
			ExtraProcessConfDir: dir,
		}
		out, err := s.Start(context.Background(), in)
		if err != nil {
			t.Fatalf("Start(%q) Go err = %v, want nil Go error with payload Error set", bin, err)
		}
		if out == nil || out.GetError() == "" {
			t.Fatalf("Start(%q) accepted an arbitrary binary; want rejection", bin)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "extra.conf")); !os.IsNotExist(statErr) {
		t.Fatal("rejected Start wrote extra.conf; want no file write")
	}
}

// TestStartRejectsConfDirEscape (BUG-002): a Start request whose conf dir
// escapes its base via ".." must be rejected with no file write and no
// process spawn — even when the binary itself is allowlisted.
func TestStartRejectsConfDirEscape(t *testing.T) {
	s := &server{}
	// Work inside a temp cwd so relative ".." escapes are observable
	// without touching the real filesystem.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	in := &gen.LoadConfigReq{
		NeedExtraProcess: true,
		ExtraProcessPath: "nekobox_core",
		ExtraProcessConf: "secret",
		// Relative traversal escaping the cwd.
		ExtraProcessConfDir: filepath.Join("..", "escape-target"),
	}
	out, err := s.Start(context.Background(), in)
	if err != nil {
		t.Fatalf("Start Go err = %v, want nil Go error with payload Error set", err)
	}
	if out == nil || out.GetError() == "" {
		t.Fatal("Start accepted a conf-dir escape; want rejection")
	}
	for _, p := range []string{
		filepath.Join(base, "extra.conf"),
		filepath.Join(filepath.Dir(base), "escape-target", "extra.conf"),
	} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Fatalf("rejected Start wrote %q; want no file write", p)
		}
	}
}

// TestBatchTestNegativeMaxConcurrency (BUG-006): a negative
// max_concurrency must be clamped to the default, never panic the core
// via make(chan struct{}, negative). Drives the real Test handler with
// a minimal direct config against a local HTTP server.
func TestBatchTestNegativeMaxConcurrency(t *testing.T) {
	if got := clampMaxConcurrency(-1); got != MaxConcurrentTests {
		t.Fatalf("clampMaxConcurrency(-1) = %d, want %d", got, MaxConcurrentTests)
	}
	if got := clampMaxConcurrency(0); got != MaxConcurrentTests {
		t.Fatalf("clampMaxConcurrency(0) = %d, want %d", got, MaxConcurrentTests)
	}
	if got := clampMaxConcurrency(MaxConcurrentTests + 1); got != MaxConcurrentTests {
		t.Fatalf("clampMaxConcurrency(over) = %d, want %d", got, MaxConcurrentTests)
	}
	if got := clampMaxConcurrency(7); got != 7 {
		t.Fatalf("clampMaxConcurrency(7) = %d, want 7", got)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// The handler probes run under the global testCtx, which main()
	// initializes at startup; unit tests must establish it themselves.
	oldCtx, oldCancel := testCtx, cancelTests
	testCtx, cancelTests = context.WithCancel(context.Background())
	defer func() { testCtx, cancelTests = oldCtx, oldCancel }()
	defer cancelTests()

	s := &server{}
	in := &gen.TestReq{
		Config:             `{"outbounds":[{"type":"direct","tag":"proxy"}]}`,
		UseDefaultOutbound: true,
		URL:                srv.URL,
		TestTimeoutMs:      5000,
		MaxConcurrency:     -1,
	}
	done := make(chan struct{})
	var out *gen.TestResp
	var herr error
	go func() {
		defer close(done)
		out, herr = s.Test(context.Background(), in)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("Test handler with MaxConcurrency=-1 hung; want a result, not a panic/hang")
	}
	if herr != nil {
		t.Fatalf("Test handler err = %v, want result without panic", herr)
	}
	if out == nil || len(out.GetResults()) == 0 {
		t.Fatal("Test handler returned no results; want one row for the default outbound")
	}
}

// TestStopRestoresSystemDns (BUG-009): server Stop must attempt the
// system-DNS restore even when no instance is running (quit-with-DNS
// path). The OS-touching call is stubbed with a mock.
func TestStopRestoresSystemDns(t *testing.T) {
	oldRestore := restoreSystemDNSOnStop
	restored := false
	restoreSystemDNSOnStop = func() { restored = true }
	defer func() { restoreSystemDNSOnStop = oldRestore }()

	s := &server{}
	out, err := s.Stop(context.Background(), &gen.EmptyReq{})
	if err != nil {
		t.Fatalf("Stop Go err = %v", err)
	}
	if out == nil {
		t.Fatal("Stop returned nil response")
	}
	if !restored {
		t.Fatal("Stop did not attempt the system-DNS restore; want restore on shutdown")
	}
}

// TestInstanceGlobalsRace — BUG-017: the Start/Stop check-and-set paths
// must be race-clean. This hammers the exact helpers Start/Stop and the
// exit watchers use (publish/take/running/get) from 8 goroutines; the
// race detector is the assertion. Real Start/Stop RPCs are NOT invoked:
// Start would build live boxes and Stop would touch OS proxy/DNS state.
//
// Limitation: internal-package self-reads (SetSystemProxy, DownloadFile)
// cannot use this package-main mutex and stay as-is; only the
// Start/Stop/watcher/TestCurrent paths from the bug report are covered.
func TestInstanceGlobalsRace(t *testing.T) {
	instanceMu.Lock()
	oldBI, oldCancel := internal.BoxInstance, internal.InstanceCancel
	oldEP := internal.ExtraProcess
	internal.BoxInstance, internal.InstanceCancel, internal.ExtraProcess = nil, nil, nil
	instanceMu.Unlock()
	defer func() {
		_, _ = takeInstance() // drain anything published
		instanceMu.Lock()
		internal.BoxInstance, internal.InstanceCancel = oldBI, oldCancel
		internal.ExtraProcess = oldEP
		instanceMu.Unlock()
	}()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				switch (i + g) % 4 {
				case 0:
					// Start-shaped: claim then publish or back off.
					if !instanceRunning() {
						_, cancel := context.WithCancel(context.Background())
						if err := publishInstance(&boxbox.Box{}, cancel); err != nil {
							cancel()
						}
					}
				case 1:
					// Stop-shaped: take (nil-safe) and release.
					if bi, cancel := takeInstance(); bi != nil {
						if cancel != nil {
							cancel()
						}
					}
				case 2:
					_ = getInstance()
				default:
					_ = instanceRunning()
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestTestScopeResetAbortsPrevious — BUG-014: StopTest (resetTestScope)
// cancels the previous shared scope, so in-flight batch loops selecting
// on it abort promptly.
func TestTestScopeResetAbortsPrevious(t *testing.T) {
	prev := currentTestCtx()
	resetTestScope()
	select {
	case <-prev.Done():
	default:
		t.Fatal("previous test scope not cancelled by resetTestScope")
	}
	// The fresh scope is live.
	select {
	case <-currentTestCtx().Done():
		t.Fatal("fresh test scope already cancelled")
	default:
	}
}

// TestTestScopeConcurrentHammer — BUG-014: concurrent snapshot/reset
// must be race-clean (run with -race where the toolchain supports it;
// the race detector is the assertion).
func TestTestScopeConcurrentHammer(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = currentTestCtx()
				if i%7 == 0 {
					resetTestScope()
				}
			}
		}()
	}
	wg.Wait()
}

// TestEnableSystemProxyRejectsBadPort — BUG-018: ports outside 1..65535
// are rejected before the uint16 cast (70000 must not wrap to 4464).
func TestEnableSystemProxyRejectsBadPort(t *testing.T) {
	s := &server{}
	for _, port := range []int32{0, -5, 65536, 70000, 1 << 30} {
		out, err := s.EnableSystemProxy(context.Background(), &gen.SystemProxy{
			Address: "127.0.0.1", Port: port, SupportSocks: true,
		})
		if err != nil {
			t.Fatalf("port %d: Go err = %v, want payload error", port, err)
		}
		if out.GetError() == "" {
			t.Fatalf("port %d accepted, want invalid-port error", port)
		}
	}
	// A valid port passes validation (it then fails on the missing live
	// instance — proving the request got PAST the range check).
	out, err := s.EnableSystemProxy(context.Background(), &gen.SystemProxy{
		Address: "127.0.0.1", Port: 2080, SupportSocks: true,
	})
	if err != nil {
		t.Fatalf("Go err = %v", err)
	}
	if got := out.GetError(); got == "" || strings.Contains(got, "invalid proxy port") {
		t.Fatalf("port 2080: error = %q, want pass-through to instance check", got)
	}
}

// TestExtraConfClosedOnWriteError — BUG-030 (fixed in Batch 1, verified
// here): the extra.conf handle is deferred-closed right after OpenFile,
// so the write-error return cannot leak the fd. A live repro needs a
// failing disk, so this pins the ordering statically.
func TestExtraConfClosedOnWriteError(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (s *server) Start(")
	if start < 0 {
		t.Fatal("server Start not found")
	}
	fn := body[start:]
	deferIdx := strings.Index(fn, "defer f.Close()")
	writeIdx := strings.Index(fn, "Failed to write extra.conf")
	if deferIdx < 0 || writeIdx < 0 || deferIdx > writeIdx {
		t.Fatal("extra.conf handle is not deferred-closed before the write-error return (BUG-030)")
	}
}
