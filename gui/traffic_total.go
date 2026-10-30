package main

import "sync"

// trafficTotals is NekoGo's own cumulative traffic ledger.
//
// WHY: the number the core reports is NOT monotonic. The core's
// QueryStats (core/server/server.go) sums the live connections plus the
// traffic manager's closed-connection window
// (third_party/sing-box/common/trafficcontrol/manager.go). That window
// holds at most 1000 closed connections; when one is evicted its bytes
// move into the manager's private closedUploadTotal /
// closedDownloadTotal, which the Thrift API never reports. The value
// the core returns therefore DROPS - often straight back to ~0 once a
// long-lived transfer is pushed out of the window - and the UI showed
// that raw value, so the "cumulative" counter appeared to reset.
//
// HOW: the ledger keeps the authoritative total. Every poll adds the
// GROWTH since the previous poll (the delta). A reading SMALLER than
// the previous one (the window slid, or the core restarted its
// counters) is treated as a counter reset: the total is kept - the
// bytes the core reports now were already counted - and the new
// reading becomes the new baseline. The total never decreases.
//
// NekoGo never resets the core counter: the Thrift QueryStats request
// carries no reset flag at all, and the Xray StatsService call passes
// reset=false (see xray_stats_client.go).
type trafficTotals struct {
	mu   sync.Mutex
	up   trafficDirection
	down trafficDirection
}

// trafficDirection is one direction (uplink or downlink) of the ledger.
type trafficDirection struct {
	// byTag is the cumulative per-outbound-tag breakdown. A tag whose
	// connections all closed keeps its bytes here.
	byTag map[string]int64
	// last is the previous raw reading per tag - the baseline the next
	// delta is measured from.
	last map[string]int64
	// total is the cumulative byte count of this direction.
	total int64
}

// observe folds a raw per-tag reading into the ledger and returns the
// cumulative totals plus the cumulative per-tag breakdown.
func (t *trafficTotals) observe(upByTag, downByTag map[string]int64) (up, down int64, upCum, downCum map[string]int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.up.observe(upByTag)
	t.down.observe(downByTag)
	return t.up.total, t.down.total, copyTagTotals(t.up.byTag), copyTagTotals(t.down.byTag)
}

// reset drops the ledger: a new core session starts from zero.
func (t *trafficTotals) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.up = trafficDirection{byTag: map[string]int64{}, last: map[string]int64{}}
	t.down = trafficDirection{byTag: map[string]int64{}, last: map[string]int64{}}
}

// observe folds one raw reading into this direction.
//
// A tag that is missing from the reading keeps its cumulative bytes
// (its connections simply closed) and its baseline, so a later
// re-appearance is compared against the same baseline instead of being
// counted twice.
func (d *trafficDirection) observe(raw map[string]int64) {
	if d.byTag == nil {
		d.byTag = map[string]int64{}
	}
	if d.last == nil {
		d.last = map[string]int64{}
	}
	for tag, v := range raw {
		prev, seen := d.last[tag]
		switch {
		case !seen:
			// First reading of this tag: the core's counter is the
			// baseline, so it is the starting total.
			d.byTag[tag] += v
		case v >= prev:
			// Normal case: the core's counter grew - add the delta.
			d.byTag[tag] += v - prev
		default:
			// The core's counter went backwards: the closed-connection
			// window slid (or the core restarted its counters). Keep
			// the total - those bytes were already counted - and
			// re-baseline on the new value, so the traffic that comes
			// after the reset is still counted exactly once.
		}
		d.last[tag] = v
	}
	// The total is the sum of the per-tag breakdown, so a tag that
	// dropped out of the reading keeps its bytes.
	d.total = 0
	for _, v := range d.byTag {
		d.total += v
	}
}

// copyTagTotals returns a copy of the per-tag breakdown.
func copyTagTotals(in map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
