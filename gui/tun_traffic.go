package main

import "sync"

// TUN traffic readout.
//
// WHY: with the TUN inbound active, the traffic tracker used to rely
// solely on the engine's own counters (Thrift QueryStats, the upstream
// Clash API, the spoof engine's [STATS] snapshots). Those counters
// describe what the *engine* proxied, and in TUN mode they can stay at
// zero — or stop moving — while the tunnel itself carries the system's
// traffic. The adapter's byte counters are ground truth for the tunnel
// and work identically for every core, so TUN sessions report those.
//
// The adapter is named tunInterfaceName by the renderer, which is what
// makes it findable from the OS side (see tun_traffic_windows.go).

// tunCountersFunc reads the TUN adapter's lifetime byte counters:
// received (tunnel → host = downlink) and sent (host → tunnel =
// uplink). A package variable so tests can drive the ledger.
var tunCountersFunc = readTunInterfaceCounters

// tunTrafficTag labels the per-tag breakdown of a TUN-mode reading.
const tunTrafficTag = "tun-in"

// tunTraffic turns the adapter's lifetime counters into per-session
// totals. The counters keep growing across connects because Windows
// reuses the adapter, so the session needs a baseline taken at connect
// (begin); only the growth after it is reported.
type tunTraffic struct {
	mu      sync.Mutex
	based   bool
	baseIn  uint64
	baseOut uint64
	lastIn  uint64
	lastOut uint64
	// seed is the byte count already reported for this session by the
	// engine's counters while the adapter was not readable/sampled yet.
	// The handoff continues the total instead of restarting it.
	seedUp   int64
	seedDown int64
	up       int64
	down     int64
}

// reset starts a new session: the next sample re-baselines.
func (t *tunTraffic) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.based = false
	t.baseIn, t.baseOut = 0, 0
	t.lastIn, t.lastOut = 0, 0
	t.seedUp, t.seedDown = 0, 0
	t.up, t.down = 0, 0
}

// begin pre-baselines a session on the adapter counters read at
// connect. Without it the baseline would be the first stats poll, which
// can land well after the connect: the lifetime counters do not know
// the session start, so every byte carried in between was dropped and
// the reported total fell when the adapter reading replaced the
// engine's fallback.
func (t *tunTraffic) begin(in, out uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.based = true
	t.baseIn, t.baseOut = in, out
	t.lastIn, t.lastOut = in, out
	t.seedUp, t.seedDown = 0, 0
	t.up, t.down = 0, 0
}

// sample folds one adapter reading into the session totals and returns
// the cumulative uplink/downlink bytes.
//
// An unbased session starts from [seedUp]/[seedDown] (what the engine
// already reported before the adapter became readable), so the total is
// continuous across the source handoff. A counter that went backwards
// means a recreated adapter: the session's own total is kept and the
// measurement restarts on the new counters.
func (t *tunTraffic) sample(in, out uint64, seedUp, seedDown int64) (up, down int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.based {
		t.based = true
		t.baseIn, t.baseOut = in, out
		t.seedUp, t.seedDown = seedUp, seedDown
	} else if in < t.lastIn || out < t.lastOut {
		t.baseIn, t.baseOut = in, out
		t.seedUp, t.seedDown = t.up, t.down
	}
	t.lastIn, t.lastOut = in, out
	t.up = t.seedUp + int64(out-t.baseOut)
	t.down = t.seedDown + int64(in-t.baseIn)
	return t.up, t.down
}
