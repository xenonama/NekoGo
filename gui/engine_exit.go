package main

// Cross-engine death reporting (PattN's SessionEnded contract).
//
// Every manager that spawns a child owns the single cmd.Wait for it. When
// that child was the committed generation and the exit was not requested
// by Stop, the manager reports an EngineExit upward instead of only
// flipping its own flag. CoreManager turns that report into group
// teardown, so a dead engine can never leave its siblings and the system
// proxy pointing at a closed port.

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// engineLastLines is how much child output a crash report carries.
const engineLastLines = 12

// EngineExit describes one unexpected engine death.
type EngineExit struct {
	Engine    string // "aether" | "sing-box" | "xray"
	PID       int
	Err       error
	StartedAt time.Time
	Uptime    time.Duration
	LastLines []string
}

// newEngineExit snapshots a reaped child. A zero startedAt (tests, a
// manager assembled by hand) leaves Uptime at 0 instead of a huge value.
func newEngineExit(engine string, cmd *exec.Cmd, err error, started time.Time, lines []string) EngineExit {
	ev := EngineExit{Engine: engine, Err: err, StartedAt: started, LastLines: lines}
	if cmd != nil && cmd.Process != nil {
		ev.PID = cmd.Process.Pid
	}
	if !started.IsZero() {
		ev.Uptime = time.Since(started)
	}
	return ev
}

// Summary is the user-facing sentence: what died, how long it ran, why,
// and its last words.
func (e EngineExit) Summary() string {
	name := strings.TrimSpace(e.Engine)
	if name == "" {
		name = "core"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s engine exited unexpectedly", name)
	if e.Uptime > 0 {
		fmt.Fprintf(&b, " after %s", e.Uptime.Round(time.Second))
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	if len(e.LastLines) > 0 {
		fmt.Fprintf(&b, " (last output: %q)", e.LastLines[len(e.LastLines)-1])
	}
	return b.String()
}

// lastWordsSuffix formats the newest ring line for a start-failure error.
func lastWordsSuffix(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	line := strings.TrimSpace(lines[len(lines)-1])
	if line == "" {
		return ""
	}
	if len(line) > 400 {
		line = line[:400] + "…"
	}
	return fmt.Sprintf(" (last output: %q)", line)
}

// lineRing keeps the tail of a child's output for crash reports. The zero
// value is usable (default capacity engineLastLines).
type lineRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (r *lineRing) push(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.max
	if n <= 0 {
		n = engineLastLines
	}
	r.lines = append(r.lines, line)
	if len(r.lines) > n {
		r.lines = append([]string(nil), r.lines[len(r.lines)-n:]...)
	}
}

func (r *lineRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

func (r *lineRing) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = nil
}
