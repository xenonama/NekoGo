package main

import "testing"

// TestAetherEngineWarmupWaitsForAPsiphonHop: waitSocksOrExit ends a run
// that has said nothing for a whole round, and Psiphon goes quiet while
// it fetches tactics. A live psiphon-only run on a filtered network
// printed nothing for 54s between "starts with the server entries" and
// "can leave from", then came up ready — so the plain engine round
// killed a connect that was still going fine. Tor's hop already gets a
// longer round; Psiphon's did not.
func TestAetherEngineWarmupWaitsForAPsiphonHop(t *testing.T) {
	for _, mode := range []string{"chain", "reverse", "only"} {
		p := &Profile{ID: "0", Name: "WARP", Protocol: "aether"}
		p.Aether.Psiphon = mode
		args, _, _ := aetherCoreOf(p, 57203)
		if aetherPsiphonModeOf(args) != mode {
			t.Fatalf("args do not run psiphon=%s: %q", mode, args)
		}
		got := aetherEngineWarmup(args)
		if got <= aetherWarmup {
			t.Errorf("warmup for psiphon=%s = %s, want more than the plain engine round %s", mode, got, aetherWarmup)
		}
		// Both Psiphon paths wait on the same handshake, so they must not
		// drift apart: the chain leg's budget is this one's.
		if got != psiphonChainWarmup {
			t.Errorf("warmup for psiphon=%s = %s, want the chain leg's %s", mode, got, psiphonChainWarmup)
		}
	}

	// A cold consensus fetch is the longer wait of the two.
	both := &Profile{ID: "1", Name: "WARP", Protocol: "aether"}
	both.Aether.Psiphon = "chain"
	both.Aether.Tor = "chain"
	args, _, _ := aetherCoreOf(both, 57203)
	if got := aetherEngineWarmup(args); got != torChainWarmup {
		t.Errorf("warmup for tor+psiphon = %s, want tor's %s", got, torChainWarmup)
	}

	plain := &Profile{ID: "2", Name: "WARP", Protocol: "aether"}
	args, _, _ = aetherCoreOf(plain, 57203)
	if got := aetherEngineWarmup(args); got != aetherWarmup {
		t.Errorf("warmup with no carrier = %s, want %s", got, aetherWarmup)
	}
}
