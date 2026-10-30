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
// reuses the adapter, so the first sample of a session becomes the
// baseline and only the growth after it is reported.
type tunTraffic struct {
	mu      sync.Mutex
	based   bool
	baseIn  uint64
	baseOut uint64
	lastIn  uint64
	lastOut uint64
	up      int64
	down    int64
}

// reset starts a new session: the next sample re-baselines.
func (t *tunTraffic) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.based = false
	t.baseIn, t.baseOut = 0, 0
	t.lastIn, t.lastOut = 0, 0
	t.up, t.down = 0, 0
}

// sample folds one adapter reading into the session totals and returns
// the cumulative uplink/downlink bytes. The first reading (and a
// counter that went backwards, i.e. a recreated adapter) only sets the
// baseline.
func (t *tunTraffic) sample(in, out uint64) (up, down int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.based || in < t.lastIn || out < t.lastOut {
		t.based = true
		t.baseIn, t.baseOut = in, out
		t.lastIn, t.lastOut = in, out
		t.up, t.down = 0, 0
		return 0, 0
	}
	t.lastIn, t.lastOut = in, out
	t.up = int64(out - t.baseOut)
	t.down = int64(in - t.baseIn)
	return t.up, t.down
}
