package main

// Plan A's trust boundary: the decoy SNI's DNS answer IS the address the
// engine dials (resolvePlanATarget -> resolveA -> the config endpoint).
// An answer that is loopback, RFC1918, link-local or multicast is not a
// decoy host — it is a poisoned resolver or a captive-portal redirect,
// which is precisely the thing Plan A exists to bypass. Plan B already
// refuses those (pickSpoofEndpointIP); the DoH path accepted them and
// handed the engine a local address, so the tunnel connected to whatever
// listens on the interceptor's port instead of failing.
//
// All resolution is driven at loopback fakes, so no test reaches a real
// resolver, and the OS fallback runs through the osLookupIP seam.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dohRecordsServer answers dns-json with an arbitrary A-record set per
// name (the shared fake only serves one address per name, which cannot
// express "private answer plus a good one" or "private answer only").
func dohRecordsServer(t *testing.T, answer map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		ips, ok := answer[name]
		w.Header().Set("content-type", "application/dns-json")
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"Status": 3})
			return
		}
		recs := make([]any, 0, len(ips))
		for _, ip := range ips {
			recs = append(recs, map[string]any{"name": name, "type": 1, "data": ip})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Status": 0, "Answer": recs})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useDoHRecords(t *testing.T, srvs ...*httptest.Server) {
	t.Helper()
	var servers []dohServer
	for _, s := range srvs {
		servers = append(servers, dohServer{url: s.URL + "/dns-query"})
	}
	old := dohServersOverride
	dohServersOverride = servers
	t.Cleanup(func() { dohServersOverride = old })
}

func resolveCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The user-visible failure: Start succeeds, the engine dials a local
// address, and every connection hangs or lands on the interceptor.
func TestPlanARejectsAPoisonedSNIAnswer(t *testing.T) {
	forgetDoH(t, "decoy.example")
	useDoHRecords(t, dohRecordsServer(t, map[string][]string{"decoy.example": {"127.0.0.1"}}))

	s := DefaultSettings()
	s.SNIConfig.Plan = "A"
	s.SNISNIs = []SNISNI{{Host: "decoy.example", Source: "test"}}
	s.SNISelectedSNI = "decoy.example"
	ip, _, _, err := resolvePlanATarget(&Profile{Name: "p", Address: "www.calmlunch.com", Port: 443}, s)
	if err == nil {
		t.Fatalf("plan A handed the engine the answer %q, want a rejection", ip)
	}
	if ip != "" {
		t.Fatalf("rejected target still returned an endpoint: %q", ip)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error %q does not name the intercepted answer", err)
	}
}

// A public answer mixed in with poisoned ones is the normal round-robin
// shape: the good address must win rather than the first record.
func TestResolveASkipsPoisonAndKeepsThePublicAnswer(t *testing.T) {
	forgetDoH(t, "mixed.example")
	useDoHRecords(t, dohRecordsServer(t, map[string][]string{
		"mixed.example": {"10.10.34.35", "169.254.169.254", "203.0.113.42"},
	}))

	ip, err := resolveAVia(resolveCtx(t), dohServers(), "mixed.example")
	if err != nil {
		t.Fatalf("resolveAVia: %v", err)
	}
	if ip != "203.0.113.42" {
		t.Fatalf("endpoint = %q, want the only usable answer 203.0.113.42", ip)
	}
}

// One provider intercepted is a reason to ask the next one, not to give
// up: that is the whole point of the provider list.
func TestResolveAFallsToTheNextProviderOnAPoisonedAnswer(t *testing.T) {
	forgetDoH(t, "split.example")
	bad := dohRecordsServer(t, map[string][]string{"split.example": {"127.0.0.1"}})
	good := dohRecordsServer(t, map[string][]string{"split.example": {"198.51.100.9"}})
	useDoHRecords(t, bad, good)

	ip, err := resolveAVia(resolveCtx(t), dohServers(), "split.example")
	if err != nil {
		t.Fatalf("resolveAVia: %v", err)
	}
	if ip != "198.51.100.9" {
		t.Fatalf("endpoint = %q, want the clean provider's answer", ip)
	}
}

// Every provider intercepted must read as an interception, with the
// addresses shown — "no A record" would send the user hunting for a typo
// in the SNI list.
func TestFirstARecordReportsTheInterceptedAnswers(t *testing.T) {
	body := `{"Status":0,"Answer":[{"name":"x.example","type":1,"data":"10.0.0.5"},{"name":"x.example","type":1,"data":"192.168.1.1"}]}`
	ip, err := firstARecord([]byte(body))
	if err == nil {
		t.Fatalf("firstARecord accepted private answers as %q, want rejection", ip)
	}
	if ip != "" {
		t.Fatalf("rejected answer returned endpoint %q", ip)
	}
	for _, want := range []string{"10.0.0.5", "192.168.1.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q omits the intercepted answer %s", err, want)
		}
	}

	// A non-A record is still "answered, no usable A record" (nil error),
	// so the caller tries the next provider without a scary message.
	onlyAAAA := `{"Status":0,"Answer":[{"name":"x.example","type":28,"data":"2001:4860::1"}]}`
	if ip, err := firstARecord([]byte(onlyAAAA)); err != nil || ip != "" {
		t.Fatalf("AAAA-only = %q/%v, want \"\"/nil", ip, err)
	}
}

// The last-resort OS resolver is the MOST likely source of a poisoned
// answer, so it needs the same gate.
func TestResolveAOSRejectsPrivateAnswers(t *testing.T) {
	old := osLookupIP
	defer func() { osLookupIP = old }()

	ctx := context.Background()
	osLookupIP = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0")}, nil
	}
	if ip, err := resolveAOS(ctx, "example.com"); err == nil {
		t.Fatalf("OS fallback returned the unusable endpoint %q, want rejection", ip)
	}

	osLookupIP = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("172.16.0.9"), net.ParseIP("203.0.113.7")}, nil
	}
	ip, err := resolveAOS(ctx, "example.com")
	if err != nil {
		t.Fatalf("resolveAOS with a public answer: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Fatalf("OS endpoint = %q, want the public answer", ip)
	}
}
