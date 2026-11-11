package main

import (
	"fmt"
	"testing"
)

// TestGetSettingsSnapshotIsDetached: GetSettings returns a struct copy,
// but a struct copy shares every slice's backing array — and the
// subscription, SNI and Aether writers update rows IN PLACE under the
// store lock (TouchSubscription, RenameSubscription,
// SetSubscriptionEnabled, sni_scan's UpdateSNI,
// UpdateAetherScanResults). A snapshot therefore kept mutating outside
// the lock, which is a data race the moment two goroutines are involved
// and silent corruption when they are not: ListSubscriptions hands the
// alias to the Wails marshaller, and the update loop iterates one
// snapshot across whole network fetches.
func TestGetSettingsSnapshotIsDetached(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	url := "https://feed.example/sub"
	if _, err := store.AddSubscription(Subscription{URL: url, Name: "feed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAetherScanResults(func(r []AetherScanResult) []AetherScanResult {
		return append(r, AetherScanResult{Endpoint: "1.2.3.4:8443", LatencyMs: 10})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSNI(func(s *Settings) {
		s.SNIIPs = []SNIIP{{IP: "162.159.192.1"}}
		s.SNISNIs = []SNISNI{{Host: "developers.cloudflare.com"}}
		s.ProxyGroups = []string{"auto", "manual"}
		s.CollapsedProxyGroups = []string{"manual"}
	}); err != nil {
		t.Fatal(err)
	}

	snap := store.GetSettings()
	before := snapshotString(snap)

	if _, err := store.TouchSubscription(url, "2026-01-01T00:00:00Z",
		SubUserInfo{Upload: 1, Download: 2, Total: 3, Expire: "2026"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenameSubscription(url, "renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSubscriptionEnabled(url, false); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAetherScanResults(func(r []AetherScanResult) []AetherScanResult {
		if len(r) > 0 {
			r[0].LatencyMs = 999
		}
		return r
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSNI(func(s *Settings) {
		s.SNIIPs[0].LastOK = true
		s.SNISNIs[0].LastMs = 5
		s.ProxyGroups[0] = "changed"
		s.CollapsedProxyGroups[0] = "changed"
	}); err != nil {
		t.Fatal(err)
	}

	if after := snapshotString(snap); after != before {
		t.Fatalf("GetSettings snapshot mutated along with the live store:\n before: %s\n after:  %s", before, after)
	}
	// The live store really did change (else the test above is vacuous).
	cur := store.GetSettings()
	if cur.Subscriptions[0].Name != "renamed" || cur.Subscriptions[0].IsEnabled() {
		t.Fatalf("live store did not take the writes: %+v", cur.Subscriptions[0])
	}
	if !cur.SNIIPs[0].LastOK || cur.ProxyGroups[0] != "changed" {
		t.Fatalf("live store did not take the SNI writes: %+v", cur)
	}
}

func snapshotString(s Settings) string {
	return fmt.Sprintf("%v|%v|%v|%v|%v|%v",
		s.Subscriptions, s.AetherScanResults, s.SNIIPs, s.SNISNIs,
		s.ProxyGroups, s.CollapsedProxyGroups)
}
