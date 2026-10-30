package main

import "testing"

const mb = 1 << 20

// TestTrafficTotalsSurviveCoreReset: two consecutive reads where the
// second one is SMALLER (the core's closed-connection window slid, or
// the core restarted its counters) must not make the cumulative total
// go backwards - and the traffic that comes after the reset must still
// be counted exactly once.
func TestTrafficTotalsSurviveCoreReset(t *testing.T) {
	var tt trafficTotals

	up, down, upByTag, downByTag := tt.observe(
		map[string]int64{"proxy": 80 * mb},
		map[string]int64{"proxy": 90 * mb},
	)
	if up != 80*mb || down != 90*mb {
		t.Fatalf("first read = (%d, %d), want (%d, %d)", up, down, 80*mb, 90*mb)
	}
	if upByTag["proxy"] != 80*mb || downByTag["proxy"] != 90*mb {
		t.Fatalf("first read per-tag = (%v, %v), want proxy=80MB/90MB", upByTag, downByTag)
	}

	// The core's counter dropped (evicted connections): the reading is
	// now smaller than the previous one.
	up, down, upByTag, downByTag = tt.observe(
		map[string]int64{"proxy": 1 * mb},
		map[string]int64{"proxy": 2 * mb},
	)
	if up != 80*mb {
		t.Fatalf("cumulative uplink after a smaller read = %d, want %d (must not go backwards)", up, 80*mb)
	}
	if down != 90*mb {
		t.Fatalf("cumulative downlink after a smaller read = %d, want %d (must not go backwards)", down, 90*mb)
	}
	if upByTag["proxy"] != 80*mb || downByTag["proxy"] != 90*mb {
		t.Fatalf("per-tag totals after a smaller read = (%v, %v), want proxy=80MB/90MB", upByTag, downByTag)
	}

	// New traffic after the reset is added on top of the kept total:
	// 1MB -> 3MB is 2MB of growth, so the total is 80MB + 2MB.
	up, down, _, _ = tt.observe(
		map[string]int64{"proxy": 3 * mb},
		map[string]int64{"proxy": 5 * mb},
	)
	if up != 82*mb {
		t.Fatalf("cumulative uplink after the reset = %d, want %d", up, 82*mb)
	}
	if down != 93*mb {
		t.Fatalf("cumulative downlink after the reset = %d, want %d", down, 93*mb)
	}
}

// TestTrafficTotalsNeverDecrease: a long random walk of readings (with
// drops in the middle) must produce a monotonic cumulative total.
func TestTrafficTotalsNeverDecrease(t *testing.T) {
	var tt trafficTotals
	readings := []int64{10, 25, 40, 5, 7, 60, 2, 3, 90, 0, 12}
	var prev int64
	for i, r := range readings {
		up, _, _, _ := tt.observe(map[string]int64{"proxy": r * mb}, nil)
		if up < prev {
			t.Fatalf("read %d: cumulative total went backwards: %d < %d", i, up, prev)
		}
		prev = up
	}
	// 10 + 15 + 15 (the drop to 5 keeps the total) + 2 + 53 (60-7) +
	// 1 (3-2, the drop to 2 keeps it) + 87 (90-3) + 12 (12-0) = 195MB.
	if prev != 195*mb {
		t.Fatalf("cumulative uplink = %d, want %d", prev, 195*mb)
	}
}

// TestTrafficTotalsKeepVanishedTags: a tag whose connections all closed
// disappears from the core's answer; its bytes must stay in the
// cumulative total instead of shrinking it.
func TestTrafficTotalsKeepVanishedTags(t *testing.T) {
	var tt trafficTotals
	up, _, upByTag, _ := tt.observe(map[string]int64{"proxy": 50 * mb}, nil)
	if up != 50*mb {
		t.Fatalf("first read = %d, want %d", up, 50*mb)
	}
	// The "proxy" tag is gone from the reading (all its connections
	// closed and were evicted).
	up, _, upByTag, _ = tt.observe(map[string]int64{}, nil)
	if up != 50*mb {
		t.Fatalf("total after the tag vanished = %d, want %d", up, 50*mb)
	}
	if upByTag["proxy"] != 50*mb {
		t.Fatalf("per-tag total after the tag vanished = %v, want proxy=50MB", upByTag)
	}
}

// TestTrafficTotalsResetStartsNewSession: a new core session (Start)
// starts a fresh ledger.
func TestTrafficTotalsResetStartsNewSession(t *testing.T) {
	var tt trafficTotals
	tt.observe(map[string]int64{"proxy": 80 * mb}, map[string]int64{"proxy": 80 * mb})
	tt.reset()
	up, down, _, _ := tt.observe(map[string]int64{"proxy": 7 * mb}, map[string]int64{"proxy": 8 * mb})
	if up != 7*mb || down != 8*mb {
		t.Fatalf("first read after reset = (%d, %d), want (%d, %d)", up, down, 7*mb, 8*mb)
	}
}
