package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// logLevelEnvName is the child-core env var carrying the GUI log level
// (Advanced Phase H). The child reads it at startup; when it ignores the
// value the GUI logs a one-time warning and continues.
const logLevelEnvName = "NEKOBOX_LOG_LEVEL"

// defaultLogLevel is the effective log level when settings.logLevel is
// empty (first run, or a config written before the field existed). CPU
// audit #2: "warn" — "info" emits a line per connection open/close.
const defaultLogLevel = "warn"

var logLevelWarnOnce sync.Once

// normalizeLogLevel lowercases s and migrates the legacy "warning" value
// to the spec "warn". Empty means "unset" (callers default to
// defaultLogLevel).
func normalizeLogLevel(s string) string {
	lvl := strings.ToLower(strings.TrimSpace(s))
	if lvl == "warning" {
		return "warn"
	}
	return lvl
}

// validLogLevel reports whether lvl is an accepted settings.logLevel
// value (empty = unset, defaults apply).
func validLogLevel(lvl string) bool {
	switch normalizeLogLevel(lvl) {
	case "", "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// singLogLevel maps a settings value to a sing-box log level
// ("warning" is not a sing-box level).
func singLogLevel(s string) string {
	lvl := normalizeLogLevel(s)
	if lvl == "" {
		return defaultLogLevel
	}
	return lvl
}

// xrayLogLevel maps a settings value to an Xray loglevel
// (Xray spells it "warning", not "warn").
func xrayLogLevel(s string) string {
	lvl := normalizeLogLevel(s)
	if lvl == "" {
		lvl = defaultLogLevel
	}
	if lvl == "warn" {
		return "warning"
	}
	return lvl
}

// coreGOGCName is the child-core GC target (CPU audit #4). The GUI
// already runs with SetGCPercent(200) (main.go); the cores inherit the
// Go default of 100 otherwise. A relaxed target trades a little RSS for
// fewer collections, which matters most under sustained throughput.
const (
	coreGOGCName  = "GOGC"
	coreGOGCValue = "200"
)

// withCoreGCEnv returns env plus GOGC=200 unless env already carries a
// GOGC entry (an explicit user value always wins). The result is a copy
// appended to, never a mutation of env, so callers keep ownership.
func withCoreGCEnv(env []string) []string {
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(name, coreGOGCName) {
			return env
		}
	}
	out := make([]string, 0, len(env)+1)
	out = append(out, env...)
	return append(out, coreGOGCName+"="+coreGOGCValue)
}

// childEnvWithLogLevel returns base (usually os.Environ()) plus
// NEKOBOX_LOG_LEVEL=<level> when level is set, and always plus
// GOGC=200 (unless base already sets GOGC). warnLog, when non-nil, is
// called exactly once process-wide to note that the child may ignore
// the variable — the GUI continues regardless.
func childEnvWithLogLevel(base []string, level string, warnLog func(string)) []string {
	lvl := normalizeLogLevel(level)
	if lvl == "" {
		return withCoreGCEnv(base)
	}
	if warnLog != nil {
		logLevelWarnOnce.Do(func() {
			warnLog(fmt.Sprintf("[env] %s=%s passed to child core (best-effort; the child may ignore it)", logLevelEnvName, lvl))
		})
	}
	out := make([]string, 0, len(base)+2)
	out = append(out, base...)
	out = append(out, logLevelEnvName+"="+lvl)
	return withCoreGCEnv(out)
}

// appendLogFile appends one log line to path (best-effort, never fails
// the caller). It opens/closes per call so rotation or deletion under us
// degrades to a fresh file instead of a stale handle.
func appendLogFile(path string, e LogEntry) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s [%s/%s] %s\n", e.Time, e.Source, e.Level, e.Message)
}

// logWriter forwards child-process output line by line to the log channel.
// Sends never block: when the channel is full the line is dropped and an
// atomic counter tracks the loss, so a log burst can never stall the
// child process on a pipe write.
type logWriter struct {
	ch     chan LogEntry
	source string
	buf    bytes.Buffer
	// ring, when set, keeps the newest lines for an EngineExit report
	// (engine_exit.go). Log-channel drops do not affect it.
	ring *lineRing
	// dropped counts lines discarded because ch was full.
	dropped atomic.Uint64
}

// maxLogLineBytes bounds one pending partial log line (BUG-026).
const maxLogLineBytes = 64 << 10

// emit delivers e best-effort. A "[log] N lines dropped" note is queued
// (also best-effort) every 100 drops; it goes through the same path, so
// it can itself be dropped rather than recurse.
func (w *logWriter) emit(e LogEntry) {
	if w.ch == nil {
		return
	}
	select {
	case w.ch <- e:
	default:
		n := w.dropped.Add(1)
		if n%100 == 0 {
			note := LogEntry{Level: "warn", Source: w.source,
				Message: fmt.Sprintf("[log] %d lines dropped (channel full)", n),
				Time:    time.Now().Format(time.RFC3339)}
			select {
			case w.ch <- note:
			default:
			}
		}
	}
}

func (w *logWriter) Write(p []byte) (int, error) {
	n := len(p)
	// BUG-026: bound the pending partial-line buffer. A child emitting
	// a huge line without newlines (or a binary dump) used to grow
	// w.buf without limit; now a full budget with no newline is dropped
	// as one overlong line (noted, counted) instead of accumulating.
	// Input is appended in budget-sized pieces so even a single giant
	// Write (e.g. 100MB with no newline) never spikes memory: at most
	// maxLogLineBytes is ever held.
	for len(p) > 0 {
		if w.buf.Len() >= maxLogLineBytes {
			w.buf.Reset()
			w.dropped.Add(1)
			w.emit(LogEntry{Level: "warn", Source: w.source,
				Message: "[log] overlong line dropped (exceeded 64KB without newline)",
				Time:    time.Now().Format(time.RFC3339)})
		}
		room := maxLogLineBytes - w.buf.Len()
		take := len(p)
		if take > room {
			take = room
		}
		w.buf.Write(p[:take])
		p = p[take:]
		for {
			// Scan the pending bytes in place: w.buf.String() copied
			// the WHOLE buffer on every iteration (O(n^2) copying per
			// log line). Only the line itself is materialized now.
			b := w.buf.Bytes()
			i := bytes.IndexByte(b, '\n')
			if i < 0 {
				break
			}
			line := strings.TrimSpace(string(b[:i]))
			w.buf.Next(i + 1)
			if line != "" {
				if w.ring != nil {
					w.ring.push(line)
				}
				w.emit(LogEntry{Level: "info", Source: w.source, Message: line, Time: time.Now().Format(time.RFC3339)})
			}
		}
	}
	return n, nil
}

// drainLines is a helper for tests: split s into lines.
func drainLines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}
