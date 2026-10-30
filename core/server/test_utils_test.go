//go:build !darwin

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countConn wraps a net.Conn and counts Close calls so the test can
// prove every dialed socket is released (fd-leak assertion).
type countConn struct {
	net.Conn
	closes *int32
}

func (c *countConn) Close() error {
	atomic.AddInt32(c.closes, 1)
	return c.Conn.Close()
}

// TestSimpleDownloadTestClosesBody — the BUG-011 regression test. The
// server advertises a 256MB Content-Length and then closes the
// connection without writing the body; every call must fail fast
// (truncated body / status path) and every dialed socket must be
// closed (closes == opens across N calls: no fd leak).
func TestSimpleDownloadTestClosesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(256<<20))
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Return without writing the body: the client must see a
		// truncated body, report an error, and still close it.
	}))
	defer srv.Close()

	var opens, closes int32
	base := &net.Dialer{Timeout: 5 * time.Second}
	dialer := func(ctx context.Context, network string, address string) (net.Conn, error) {
		c, err := base.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		atomic.AddInt32(&opens, 1)
		return &countConn{Conn: c, closes: &closes}, nil
	}

	const calls = 10
	for i := 0; i < calls; i++ {
		res := &SpeedTestResult{}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		start := time.Now()
		err := simpleDownloadTest(ctx, dialer, res, srv.URL, 15*time.Second)
		elapsed := time.Since(start)
		cancel()
		if err == nil && res.Error == nil {
			t.Fatalf("call %d: expected truncated-body error, got nil (elapsed %v)", i, elapsed)
		}
		if elapsed > 25*time.Second {
			t.Fatalf("call %d: took %v, expected a fast failure", i, elapsed)
		}
	}

	// The server sends Connection: close, so the transport cannot park
	// idle keep-alive sockets: every dial must have a matching close.
	if o, c := atomic.LoadInt32(&opens), atomic.LoadInt32(&closes); o != c {
		t.Fatalf("fd leak: dialed %d sockets but closed %d", o, c)
	}
	fmt.Println("socket open/close balanced")
}

// TestSimpleDownloadTestRejectsErrorStatus — a 404 page must be
// reported as an error without buffering the body.
func TestSimpleDownloadTestRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		http.Error(w, "no such file ://example", http.StatusNotFound)
	}))
	defer srv.Close()

	base := &net.Dialer{Timeout: 5 * time.Second}
	dialer := func(ctx context.Context, network string, address string) (net.Conn, error) {
		return base.DialContext(ctx, network, address)
	}
	res := &SpeedTestResult{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := simpleDownloadTest(ctx, dialer, res, srv.URL, 15*time.Second)
	if err == nil && res.Error == nil {
		t.Fatal("expected HTTP-status error, got nil")
	}
}

// TestSpeedTestRunningFlagRace — BUG-013: the running flag was written
// without the lock its readers take. Hammer set/Result/store from 8
// goroutines; run with -race.
func TestSpeedTestRunningFlagRace(t *testing.T) {
	var q SpeedTestResultQuerier
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				q.setIsRunning((i+g)%2 == 0)
				q.storeResult(&SpeedTestResult{Tag: "t"})
				// The race detector is the assertion: any
				// unsynchronized access fails the run.
				_, _ = q.Result()
			}
		}(g)
	}
	wg.Wait()
}

// TestOutboundTagsCapped — BUG-015: per-request tag lists above 256 are
// rejected before any box is created or goroutine fans out.
func TestOutboundTagsCapped(t *testing.T) {
	if err := validateOutboundTags(nil); err != nil {
		t.Fatalf("nil tags rejected: %v", err)
	}
	ok := make([]string, maxOutboundTags)
	if err := validateOutboundTags(ok); err != nil {
		t.Fatalf("%d tags rejected: %v", maxOutboundTags, err)
	}
	for _, n := range []int{maxOutboundTags + 1, 10000} {
		big := make([]string, n)
		err := validateOutboundTags(big)
		if err == nil {
			t.Fatalf("%d tags accepted, want rejection", n)
		}
		t.Logf("%d tags -> %v", n, err)
	}
}

// TestReporterBufferBounded — BUG-015: every reporter drops oldest past
// 10000 entries, keeping the newest.
func TestReporterBufferBounded(t *testing.T) {
	const extra = 500
	var u URLTestReporter
	for i := 0; i < maxReporterResults+extra; i++ {
		u.AddResult(&URLTestResult{Tag: fmt.Sprintf("t-%d", i)})
	}
	got := u.Results()
	if len(got) != maxReporterResults {
		t.Fatalf("URLReporter len = %d, want %d", len(got), maxReporterResults)
	}
	if got[0].Tag != fmt.Sprintf("t-%d", extra) || got[len(got)-1].Tag != fmt.Sprintf("t-%d", maxReporterResults+extra-1) {
		t.Fatalf("URLReporter window = [%s..%s], want [t-%d..t-%d]",
			got[0].Tag, got[len(got)-1].Tag, extra, maxReporterResults+extra-1)
	}

	var c CountryTestResults
	for i := 0; i < maxReporterResults+extra; i++ {
		c.AddResult(&SpeedTestResult{Tag: fmt.Sprintf("c-%d", i)})
	}
	gotC := c.Results()
	if len(gotC) != maxReporterResults || gotC[0].Tag != fmt.Sprintf("c-%d", extra) {
		t.Fatalf("CountryTestResults not bounded/newest-kept (len=%d)", len(gotC))
	}

	var ip IPTestReporter
	for i := 0; i < maxReporterResults+extra; i++ {
		ip.AddResult(&IPTestResult{Tag: fmt.Sprintf("i-%d", i)})
	}
	gotIP := ip.Results()
	if len(gotIP) != maxReporterResults || gotIP[0].Tag != fmt.Sprintf("i-%d", extra) {
		t.Fatalf("IPTestReporter not bounded/newest-kept (len=%d)", len(gotIP))
	}
}

// TestLimiterHonorsCancel — BUG-016: a full limiter plus a cancelled
// context must release immediately instead of hanging for a probe
// timeout.
func TestLimiterHonorsCancel(t *testing.T) {
	limiter := make(chan struct{}, 2)
	limiter <- struct{}{}
	limiter <- struct{}{} // fill every slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := acquireSlot(ctx, limiter)
	if err == nil {
		t.Fatal("acquireSlot on cancelled ctx returned nil, want ctx error")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("cancelled acquire took %v, want <200ms", d)
	}
	// Sanity: a free slot still acquires without error.
	<-limiter
	if err := acquireSlot(context.Background(), limiter); err != nil {
		t.Fatalf("free-slot acquire: %v", err)
	}
}
