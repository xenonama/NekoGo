package main

import (
	"fmt"
	"testing"
	"time"
)

// TestLogWriterNeverBlocks — 1000 lines into an undrained 1-slot
// channel must complete (drops instead of blocking the child).
func TestLogWriterNeverBlocks(t *testing.T) {
	ch := make(chan LogEntry, 1)
	w := &logWriter{ch: ch, source: "test"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
				t.Errorf("Write %d: %v", i, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Write blocked on a full channel (P2-4 regression)")
	}
	if got := w.dropped.Load(); got == 0 {
		t.Fatal("expected dropped lines with an undrained 1-slot channel")
	} else {
		t.Logf("dropped=%d (1 buffered, rest best-effort)", got)
	}
	// The single buffered line must be intact.
	select {
	case e := <-ch:
		if e.Message == "" {
			t.Fatal("buffered entry has empty message")
		}
	default:
		t.Fatal("expected one buffered entry")
	}
}

// TestLogWriterSplitsLinesInPlace — the line splitter must behave
// exactly as before the in-place scan change (P2): a multi-line write
// yields one entry per line, a line split across two writes is joined,
// blank lines are skipped and CRLF is trimmed.
func TestLogWriterSplitsLinesInPlace(t *testing.T) {
	ch := make(chan LogEntry, 32)
	w := &logWriter{ch: ch, source: "test"}
	drain := func() []string {
		var out []string
		for {
			select {
			case e := <-ch:
				out = append(out, e.Message)
			default:
				return out
			}
		}
	}
	// One write, three lines (the last one CRLF-terminated).
	if _, err := w.Write([]byte("alpha\nbeta\r\n\n")); err != nil {
		t.Fatal(err)
	}
	got := drain()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("lines = %q, want [alpha beta]", got)
	}
	// A line split across two writes is joined.
	if _, err := w.Write([]byte("gam")); err != nil {
		t.Fatal(err)
	}
	if got := drain(); len(got) != 0 {
		t.Fatalf("partial line emitted early: %q", got)
	}
	if _, err := w.Write([]byte("ma\n")); err != nil {
		t.Fatal(err)
	}
	if got := drain(); len(got) != 1 || got[0] != "gamma" {
		t.Fatalf("split line = %q, want [gamma]", got)
	}
}

// TestLogWriterNilChannel — a nil channel is a silent no-op.
func TestLogWriterNilChannel(t *testing.T) {
	w := &logWriter{ch: nil, source: "test"}
	if _, err := fmt.Fprintln(w, "hello"); err != nil {
		t.Fatal(err)
	}
	if got := w.dropped.Load(); got != 0 {
		t.Fatalf("dropped=%d, want 0 for nil channel", got)
	}
}

// TestLogWriterBoundsOverlongLine — BUG-026: 200KB without a newline
// (1KB chunks) must not grow the pending buffer without bound.
func TestLogWriterBoundsOverlongLine(t *testing.T) {
	ch := make(chan LogEntry, 4096)
	w := &logWriter{ch: ch, source: "test"}
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for i := 0; i < 200; i++ {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("Write %d = (%d, %v), want (%d, nil)", i, n, err, len(chunk))
		}
		if w.buf.Len() > maxLogLineBytes+len(chunk) {
			t.Fatalf("pending buffer = %d, want <= %d", w.buf.Len(), maxLogLineBytes+len(chunk))
		}
	}
	if got := w.dropped.Load(); got == 0 {
		t.Fatal("expected dropped overlong lines, got 0")
	}
	// Terminate the flood's trailing partial line first: correct
	// line-buffering merges a partial tail (200*1024 mod 64KB = 8KB
	// here) with whatever arrives next, so "after" would otherwise
	// arrive glued to 8KB of flood garbage instead of exact.
	if _, err := fmt.Fprintln(w); err != nil {
		t.Fatal(err)
	}
	// Newlines still flush normally after the flood.
	if _, err := fmt.Fprintln(w, "after"); err != nil {
		t.Fatal(err)
	}
	found := false
	for {
		select {
		case e := <-ch:
			if e.Message == "after" {
				found = true
			}
		default:
			goto drained
		}
	}
drained:
	if !found {
		t.Fatal("post-flood line never flushed")
	}
}
