package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Subscription/feed import tests: what a feed is grouped as, what a
// repoint may destroy, and whether two imports may run at once.

func subFeedServer(t *testing.T, body func() string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func nodeLink(n int) string {
	return fmt.Sprintf("vless://%08d-0000-0000-0000-000000000000@node%d.example:443?security=tls#Node%02d", n, n, n)
}

// TestFeedGroupFollowsTheFeedName: a feed's profiles are grouped by the
// feed, and the Proxies tab shows one group per feed. Import stamped the
// group with the URL host while UpdateSubscriptions stamped it with the
// recorded name, so renaming a feed (which also regroups what it already
// imported) and then refreshing it split its nodes across two groups —
// "My VPN" and "127.0.0.1:54321" — and the collapsed/expanded state and
// the per-group filters no longer matched the feed.
func TestFeedGroupFollowsTheFeedName(t *testing.T) {
	body := ""
	srv := subFeedServer(t, func() string { return body })
	body = nodeLink(1)
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	if _, err := app.ImportLinks(srv.URL); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := app.EditSubscription(srv.URL, srv.URL, "My VPN"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// The provider adds a node; the user refreshes the same feed.
	body = nodeLink(1) + "\n" + nodeLink(2)
	if _, err := app.ImportLinks(srv.URL); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	groups := map[string]int{}
	for _, p := range store.ListProfiles() {
		groups[p.Group]++
	}
	if len(groups) != 1 || groups["My VPN"] != 2 {
		t.Fatalf("one renamed feed is split into %+v, want 2 profiles in \"My VPN\"", groups)
	}

	// The same must hold for the scheduled path.
	body = nodeLink(1) + "\n" + nodeLink(2) + "\n" + nodeLink(3)
	if _, err := app.UpdateSubscriptions(); err != nil {
		t.Fatalf("UpdateSubscriptions: %v", err)
	}
	for _, p := range store.ListProfiles() {
		if p.Group != "My VPN" {
			t.Fatalf("profile %q joined group %q, want the feed name", p.Name, p.Group)
		}
	}
}

// TestEditSubscriptionKeepsTheFeedWhenTheNewURLYieldsNothing: a repoint
// imports the new address first so a failure leaves the old feed alone,
// but err only covers transport/status/empty-body problems. A 200 that
// does contain "://" and still parses to zero usable links — a typo'd
// path, a rotated token page, a Clash-YAML answer — used to sail past
// that check, and the rest of the call deleted the working record and
// re-stamped every one of its profiles onto the dead URL.
func TestEditSubscriptionKeepsTheFeedWhenTheNewURLYieldsNothing(t *testing.T) {
	old := subFeedServer(t, func() string { return nodeLink(1) })
	junk := subFeedServer(t, func() string { return "<html>see vless://rotated-token.example for more</html>" })
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	if _, err := app.ImportLinks(old.URL); err != nil {
		t.Fatalf("seed import: %v", err)
	}
	if _, err := app.EditSubscription(old.URL, junk.URL, ""); err == nil {
		t.Fatal("repoint to a feed with no usable profiles reported success")
	}
	var subs []string
	for _, s := range store.GetSettings().Subscriptions {
		subs = append(subs, s.URL)
	}
	if len(subs) != 1 || subs[0] != old.URL {
		t.Fatalf("feeds after the refused repoint = %v, want only %s", subs, old.URL)
	}
	for _, p := range store.ListProfiles() {
		if p.SubURL != old.URL {
			t.Fatalf("profile %q was re-pointed at %q, want the working feed %q", p.Name, p.SubURL, old.URL)
		}
	}
}

// TestImportsAreSerialized: the dedupe set is built before the fetch, so
// two imports in flight both treat a shared node as new and store it
// twice — permanently, because SaveProfile matches by id. It is reachable
// without any unusual timing: per-feed refresh only marks that feed
// refreshing, so the auto-update timer (and a second feed sharing nodes)
// overlaps a manual refresh. One import at a time, refused honestly, like
// Connect.
func TestImportsAreSerialized(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	var slowHits, fastHits int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&slowHits, 1)
		close(inFlight)
		<-release
		_, _ = w.Write([]byte(nodeLink(1)))
	}))
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fastHits, 1)
		_, _ = w.Write([]byte(nodeLink(2)))
	}))
	// Registered last so it runs first (LIFO): a failure path in the
	// middle of the test must not leave the blocked handler wedged, or
	// the two Close calls below wait forever.
	defer slow.Close()
	defer fast.Close()
	defer releaseNow()

	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))
	first := make(chan error, 1)
	go func() {
		_, err := app.ImportLinks(slow.URL)
		first <- err
	}()
	<-inFlight
	_, err = app.ImportLinks(fast.URL)
	if err == nil {
		t.Fatal("a second import ran while the first was still fetching")
	}
	if !strings.Contains(fmt.Sprint(err), "import") {
		t.Fatalf("concurrent import error = %v, want it to name the import", err)
	}
	if got := atomic.LoadInt32(&fastHits); got != 0 {
		t.Fatalf("the refused import still fetched (%d hits), want the guard before the network", got)
	}
	releaseNow()
	if err := <-first; err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(store.ListProfiles()) != 1 {
		t.Fatalf("profiles = %d, want 1", len(store.ListProfiles()))
	}
	// The slot must be released: a later import works normally.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := app.ImportLinks(fast.URL); err != nil {
			t.Errorf("import after the first settled: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("import slot stuck after the first import")
	}
}
