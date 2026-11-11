package main

// Tests for the SNI tab's Plan A / Plan B mode switch: the config the
// engine gets (RESOLVE_SNI), Plan A's DoH-derived connect target
// (resolvePlanATarget) and Plan A's scan shape (resolve + handshake,
// stamped on the SNI entries only).
//
// Resolution is driven through dohServersOverride at a loopback fake
// dns-json server, so no test can reach 1.1.1.1; the handshake half reuses
// sni_scan_test.go's local TLS server hooks. Plan B's paths are covered by
// the pre-existing tests and must stay green.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDoH answers dns-json queries from a fixed name -> IPv4 map; an
// unknown name replies NXDOMAIN (Status 3), which the client treats as
// "answered, no record".
type fakeDoH struct {
	mu     sync.Mutex
	hits   []string
	answer map[string]string
	srv    *httptest.Server
}

func newFakeDoH(t *testing.T, answer map[string]string) *fakeDoH {
	t.Helper()
	f := &fakeDoH{answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		f.mu.Lock()
		f.hits = append(f.hits, name)
		ip, ok := f.answer[name]
		f.mu.Unlock()
		w.Header().Set("content-type", "application/dns-json")
		if ok {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Status": 0,
				"Answer": []any{map[string]any{"name": name, "type": 1, "data": ip}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Status": 3})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDoH) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

// useDoH points resolution at the fake (empty pin => dial the URL host,
// which is loopback) and restores the production servers afterwards.
func useDoH(t *testing.T, f *fakeDoH) {
	t.Helper()
	old := dohServersOverride
	dohServersOverride = []dohServer{{url: f.srv.URL + "/dns-query", pin: ""}}
	t.Cleanup(func() { dohServersOverride = old })
}

// forgetDoH makes sure a host starts each test unresolved (the cache is
// process-wide and would otherwise carry an answer between tests) and
// leaves nothing behind.
func forgetDoH(t *testing.T, hosts ...string) {
	t.Helper()
	drop := func() {
		dohCache.mu.Lock()
		for _, h := range hosts {
			delete(dohCache.ips, normalizeFakeSNI(h))
		}
		dohCache.mu.Unlock()
	}
	drop()
	t.Cleanup(drop)
}

// allowLoopbackSpoofAnswers opens the public-answer gate for one test. A
// hermetic scan can only ever dial loopback, so Plan A's scan tests answer
// the decoy SNI with the local TLS server's address; gate behaviour itself
// is covered in sni_doh_filter_test.go, which leaves this closed.
func allowLoopbackSpoofAnswers(t *testing.T) {
	t.Helper()
	old := spoofEndpointUsable
	spoofEndpointUsable = func(net.IP) bool { return true }
	t.Cleanup(func() { spoofEndpointUsable = old })
}

func planTestApp(t *testing.T, plan string) (*App, *ConfigStore) {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	if err := store.UpdateSNI(func(s *Settings) {
		s.SNIIPs = []SNIIP{{IP: "172.67.1.2", Source: "test"}}
		s.SNISNIs = []SNISNI{{Host: "example.com", Source: "test"}}
		s.SNIConfig = SNIConfig{Plan: plan, Method: "auto"}
	}); err != nil {
		t.Fatalf("seed SNI lists: %v", err)
	}
	return NewApp(store, make(chan LogEntry, 64)), store
}

func TestSpoofPlanARecognizesPlan(t *testing.T) {
	for _, tc := range []struct {
		plan string
		want bool
	}{
		{"", false}, {"B", false}, {"b", false},
		{"A", true}, {"a", true}, {" A ", true},
	} {
		s := DefaultSettings()
		s.SNIConfig.Plan = tc.plan
		if got := spoofPlanA(s); got != tc.want {
			t.Errorf("spoofPlanA(%q) = %v, want %v", tc.plan, got, tc.want)
		}
	}
}

// Plan A must add RESOLVE_SNI and nothing else; Plan B (including the
// empty value every pre-switch settings file carries) must not mention
// the key at all, so the engine keeps dialing the curated pool.
func TestBuildSpoofConfigJSONPlanSwitch(t *testing.T) {
	p := spoofTestProfile()
	p.Address = "www.calmlunch.com"
	p.Port = 8443

	for _, tc := range []struct {
		plan  string
		wants bool
	}{{"", false}, {"B", false}, {"A", true}} {
		s := DefaultSettings()
		s.SNIConfig = SNIConfig{Plan: tc.plan, Method: "wrong_seq"}
		raw, err := buildSpoofConfigJSON(p, s, 40443, "172.67.1.2", 8443, "example.com")
		if err != nil {
			t.Fatalf("buildSpoofConfigJSON(plan %q): %v", tc.plan, err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		got, ok := cfg["RESOLVE_SNI"]
		if ok != tc.wants {
			t.Fatalf("plan %q: RESOLVE_SNI present=%v value=%v, want present=%v", tc.plan, ok, got, tc.wants)
		}
		if tc.wants && got != true {
			t.Fatalf("plan %q: RESOLVE_SNI = %v, want true", tc.plan, got)
		}
		// ENDPOINTS stays in the document in both plans: in Plan A the core
		// still reads its port from there.
		eps, ok := cfg["ENDPOINTS"].([]any)
		if !ok || len(eps) == 0 {
			t.Fatalf("plan %q: ENDPOINTS missing: %v", tc.plan, cfg["ENDPOINTS"])
		}
		if ep := eps[0].(map[string]any); ep["ip"] != "172.67.1.2" || int(ep["port"].(float64)) != 8443 {
			t.Fatalf("plan %q: ENDPOINTS[0] = %v", tc.plan, ep)
		}
	}
}

// Plan A's Start-time target: the pinned decoy SNI resolved over DoH,
// with no Cloudflare-range gate (the answer is the endpoint by
// definition) and the loopback port rule preserved.
func TestResolvePlanATargetFromDoH(t *testing.T) {
	forgetDoH(t, "example.com", "other.example")
	f := newFakeDoH(t, map[string]string{"example.com": "203.0.113.42", "other.example": "198.51.100.7"})
	useDoH(t, f)

	s := DefaultSettings()
	s.SNIConfig.Plan = "A"
	s.SNISNIs = []SNISNI{{Host: "example.com", Source: "test"}}
	s.SNISelectedSNI = "example.com"
	// A non-Cloudflare answer must be used as-is: Plan B's range check
	// would have rejected it, Plan A's whole point is not to care.
	ip, sni, port, err := resolvePlanATarget(&Profile{Name: "p", Address: "www.calmlunch.com", Port: 8443}, s)
	if err != nil {
		t.Fatalf("resolvePlanATarget: %v", err)
	}
	if ip != "203.0.113.42" || sni != "example.com" || port != 8443 {
		t.Fatalf("got %q/%q/%d, want 203.0.113.42/example.com/8443", ip, sni, port)
	}

	// resolveSNITarget must route to Plan A on its own, and the curated IP
	// pool must not be consulted (the fake's other answer proves the SNI
	// list, not the IP list, drives it).
	s.SNIIPs = []SNIIP{{IP: "172.67.1.2", Source: "test"}}
	s.SNISNIs = []SNISNI{{Host: "other.example", Source: "test"}}
	s.SNISelectedSNI = "other.example"
	ip2, sni2, _, err := resolveSNITarget(&Profile{Name: "p", Address: "www.calmlunch.com", Port: 443}, s)
	if err != nil {
		t.Fatalf("resolveSNITarget(plan A): %v", err)
	}
	if ip2 != "198.51.100.7" || sni2 != "other.example" {
		t.Fatalf("routed target = %q/%q, want 198.51.100.7/other.example", ip2, sni2)
	}

	// A chained (loopback) profile still dials the remote 443, never the
	// stale local listen port from the link.
	s.SNISelectedSNI = "example.com"
	s.SNISNIs = []SNISNI{{Host: "example.com", Source: "test"}}
	if _, _, lp, err := resolvePlanATarget(&Profile{Name: "loop", Address: "127.0.0.1", Port: 40443}, s); err != nil {
		t.Fatalf("resolvePlanATarget(loopback): %v", err)
	} else if lp != 443 {
		t.Fatalf("loopback remote port = %d, want 443", lp)
	}
}

func TestResolvePlanAErrors(t *testing.T) {
	forgetDoH(t, "dead.example", "example.com")
	f := newFakeDoH(t, map[string]string{})
	useDoH(t, f)

	s := DefaultSettings()
	s.SNIConfig.Plan = "A"
	s.SNISNIs = nil
	s.SNISelectedSNI = ""
	// No pin and no SNI/Host on the profile: nothing to resolve.
	if _, _, _, err := resolvePlanATarget(&Profile{Name: "bare", Address: "1.1.1.1", Port: 443}, s); err == nil {
		t.Fatal("plan A with nothing to resolve succeeded, want error")
	} else if !strings.Contains(err.Error(), "nothing to resolve") {
		t.Fatalf("error %q does not mention the missing SNI", err)
	}

	// A pin that has no A record must fail with the DoH cause, not hang.
	s.SNISNIs = []SNISNI{{Host: "dead.example", Source: "test"}}
	s.SNISelectedSNI = "dead.example"
	if _, _, _, err := resolvePlanATarget(&Profile{Name: "p", Address: "www.calmlunch.com", Port: 443}, s); err == nil {
		t.Fatal("plan A with an unresolvable SNI succeeded, want error")
	} else if !strings.Contains(err.Error(), "no A record") || !strings.Contains(err.Error(), "plan A:") {
		t.Fatalf("error %q lost the DoH cause", err)
	}
}

// Plan A's scan is resolve-then-dial: one job per SNI, the IP argument
// ignored, the outcome stamped on the SNI entry (including the address it
// resolved to) while the Plan B IP pool is left byte-for-byte untouched.
func TestScanSNIPairsPlanAStampsResolvedIP(t *testing.T) {
	forgetDoH(t, "example.com")
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	withLocalTLS(t, ts)
	f := newFakeDoH(t, map[string]string{"example.com": "127.0.0.1"})
	useDoH(t, f)
	// The answer is loopback because the handshake target is the local TLS
	// server above; see allowLoopbackSpoofAnswers.
	allowLoopbackSpoofAnswers(t)

	a, store := planTestApp(t, "A")
	res, err := a.ScanSNIPairs(nil, []string{"example.com"})
	if err != nil {
		t.Fatalf("ScanSNIPairs(plan A): %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	r := res[0]
	if !r.OK || r.IP != "127.0.0.1" || r.SNI != "example.com" {
		t.Fatalf("result = %+v, want OK 127.0.0.1/example.com", r)
	}
	// Loopback is not a Cloudflare edge, so the row carries the hint the
	// tab shows next to the pass.
	if r.Warning == "" || !strings.Contains(r.Warning, "not a Cloudflare edge") {
		t.Fatalf("warning = %q, want the non-Cloudflare hint", r.Warning)
	}
	if q := f.queries(); len(q) != 1 || q[0] != "example.com" {
		t.Fatalf("DoH queries = %v, want one for example.com", q)
	}

	before := store.GetSettings().SNIIPs
	after := store.GetSettings()
	if after.SNISNIs[0].LastIP != "127.0.0.1" || !after.SNISNIs[0].LastOK || after.SNISNIs[0].LastAt == "" {
		t.Fatalf("SNI entry not stamped: %+v", after.SNISNIs[0])
	}
	if len(after.SNIIPs) != len(before) || after.SNIIPs[0].IP != before[0].IP || after.SNIIPs[0].LastAt != "" {
		t.Fatalf("plan A touched the Plan B IP pool: %+v (was %+v)", after.SNIIPs, before)
	}
}

// A Plan A scan that cannot resolve reports the failure on the SNI row and
// clears the stale address, so the derived "IPs" card stops showing an IP
// the host no longer has.
func TestScanSNIPairsPlanAResolveFailure(t *testing.T) {
	forgetDoH(t, "example.com", "dead.example")
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	withLocalTLS(t, ts)
	// "dead.example" is deliberately unanswered (NXDOMAIN); the passing
	// half has to be "example.com" because that is the name the local
	// TLS server's certificate covers.
	f := newFakeDoH(t, map[string]string{"example.com": "127.0.0.1"})
	useDoH(t, f)
	allowLoopbackSpoofAnswers(t)

	a, store := planTestApp(t, "A")
	if err := store.UpdateSNI(func(s *Settings) {
		s.SNISNIs = []SNISNI{
			{Host: "example.com", Source: "test"},
			{Host: "dead.example", Source: "test", LastIP: "198.51.100.1", LastOK: true, LastAt: "2020-01-01T00:00:00Z"},
		}
	}); err != nil {
		t.Fatalf("seed stale stamp: %v", err)
	}
	res, err := a.ScanSNIPairs([]string{"172.67.1.2"}, []string{"example.com", " example.com ", "dead.example"})
	if err != nil {
		t.Fatalf("ScanSNIPairs(plan A): %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2 (duplicate deduped): %+v", len(res), res)
	}
	var dead, live *SNIPairResult
	for i := range res {
		if res[i].SNI == "dead.example" {
			dead = &res[i]
		} else {
			live = &res[i]
		}
	}
	if dead == nil || dead.OK || !strings.Contains(dead.Error, "no A record") || dead.IP != "" {
		t.Fatalf("unresolvable row = %+v, want a resolve error", dead)
	}
	if live == nil || !live.OK || live.IP != "127.0.0.1" {
		t.Fatalf("resolvable row = %+v, want a handshake against the resolved address", live)
	}
	after := store.GetSettings()
	if len(after.SNISNIs) != 2 {
		t.Fatalf("scan changed the SNI list length: %+v", after.SNISNIs)
	}
	for _, e := range after.SNISNIs {
		switch e.Host {
		case "dead.example":
			if e.LastIP != "" || e.LastOK || e.LastAt == "2020-01-01T00:00:00Z" {
				t.Fatalf("stale stamp survived a failed resolve: %+v", e)
			}
		case "example.com":
			if e.LastIP != "127.0.0.1" || !e.LastOK || e.LastMs < 1 {
				t.Fatalf("passing row not stamped: %+v", e)
			}
		default:
			t.Fatalf("unexpected entry %+v", e)
		}
	}
}

// The mode itself is a persisted option: unknown values are refused and
// the empty legacy value normalizes to Plan B, so the tab always shows an
// active mode.
func TestSpoofSetConfigPlanNormalizes(t *testing.T) {
	a, store := planTestApp(t, "")
	if err := a.SpoofSetConfig(SNIConfig{Plan: "a", Method: "auto"}); err != nil {
		t.Fatalf("SpoofSetConfig(plan a): %v", err)
	}
	if got := store.GetSettings().SNIConfig.Plan; got != "A" {
		t.Fatalf("stored plan = %q, want A", got)
	}
	if err := a.SpoofSetConfig(SNIConfig{Method: "auto"}); err != nil {
		t.Fatalf("SpoofSetConfig(empty plan): %v", err)
	}
	if got := store.GetSettings().SNIConfig.Plan; got != "B" {
		t.Fatalf("stored plan = %q, want B", got)
	}
	if err := a.SpoofSetConfig(SNIConfig{Plan: "C", Method: "auto"}); err == nil {
		t.Fatal("SpoofSetConfig(plan C) succeeded, want rejection")
	} else if !strings.Contains(err.Error(), "plan must be") {
		t.Fatalf("error %q does not name the accepted plans", err)
	}
	// An invalid plan must not have been written.
	if got := store.GetSettings().SNIConfig.Plan; got != "B" {
		t.Fatalf("rejected plan persisted: %q", got)
	}
}

// sniTabOptions reads one of the SNI tab's option arrays (const methods =
// [...]) straight out of the component source. The dropdown and the save
// validator must know the same names, and they drifted: while the Go set
// still listed v2's methods and only the versioned fingerprints, the tab's
// own hello_split / chrome / firefox were refused — and a refused value
// discarded the whole card, not just the refused field. A test that only
// walked the Go set would have stayed green through exactly that.
func sniTabOptions(t *testing.T, decl string) []string {
	t.Helper()
	src, err := os.ReadFile("frontend/src/components/SNIPanel.vue")
	if err != nil {
		t.Skipf("SNI tab source unavailable: %v", err)
	}
	array := regexp.MustCompile(`const ` + decl + ` = \[([^\]]*)\]`)
	body := array.FindSubmatch(src)
	if body == nil {
		t.Fatalf("SNIPanel.vue declares no `const %s = [...]` array", decl)
	}
	item := regexp.MustCompile(`'([^']*)'`)
	var out []string
	for _, g := range item.FindAllSubmatch(body[1], -1) {
		out = append(out, string(g[1]))
	}
	if len(out) == 0 {
		t.Fatalf("const %s = [...] holds no values", decl)
	}
	return out
}

// Every value the tab can put in the dropdown has to be savable.
func TestSpoofSetConfigAcceptsEveryOptionTheTabOffers(t *testing.T) {
	a, store := planTestApp(t, "B")
	fields := map[string]func(cfg *SNIConfig, v string){
		"methods": func(cfg *SNIConfig, v string) { cfg.Method = v },
		"fps":     func(cfg *SNIConfig, v string) { cfg.Fingerprint = v },
		"quics":   func(cfg *SNIConfig, v string) { cfg.QUICMode = v },
		"ipmodes": func(cfg *SNIConfig, v string) { cfg.IPMode = v },
	}
	for decl, set := range fields {
		for _, v := range sniTabOptions(t, decl) {
			cfg := SNIConfig{Plan: "B"}
			set(&cfg, v)
			if err := a.SpoofSetConfig(cfg); err != nil {
				t.Fatalf("the tab offers %s = %q but saving it fails: %v", decl, v, err)
			}
		}
	}
	// Not merely accepted: the v3 shapes have to come back by name.
	if err := a.SpoofSetConfig(SNIConfig{Plan: "B", Method: "hello_split", Fingerprint: "chrome"}); err != nil {
		t.Fatalf("SpoofSetConfig(hello_split + chrome): %v", err)
	}
	if got := store.GetSettings().SNIConfig; got.Method != "hello_split" || got.Fingerprint != "chrome" {
		t.Fatalf("stored engine options = %+v, want hello_split + chrome", got)
	}
}

// The tab's promise is that a change is on disk, not merely in the running
// process: re-open the same directory and read the engine options back.
func TestSpoofSetConfigSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	a := NewApp(store, make(chan LogEntry, 64))
	if err := a.SpoofSetConfig(SNIConfig{
		Plan: "B", Method: "hello_split", Fingerprint: "firefox",
		HandshakeTimeout: 2, MaxConnections: 300,
	}); err != nil {
		t.Fatalf("SpoofSetConfig: %v", err)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	got := reopened.GetSettings().SNIConfig
	if got.Method != "hello_split" || got.Fingerprint != "firefox" {
		t.Fatalf("after restart = %+v, want hello_split + firefox", got)
	}
	if got.HandshakeTimeout != 2 || got.MaxConnections != 300 {
		t.Fatalf("after restart the knobs did not persist: %+v", got)
	}
}

// The card above proves the tab and the save agree; this proves the save
// and the ENGINE agree. SpoofSetConfig's comment claims it mirrors the Rust
// validator, and it had not for a whole release: the engine renamed the
// fingerprints and added hello_split while Go still listed v2's names.
// Reading the two const arrays out of the crate source keeps that claim
// checked instead of aspirational.
func TestSpoofValidatorMirrorsTheEngine(t *testing.T) {
	for _, tc := range []struct {
		file, decl string
		accepted   []string
	}{
		{"../sni spoofing/core/src/fake_tcp.rs", "SUPPORTED_METHODS", validSpoofMethods},
		{"../sni spoofing/core/src/config.rs", "SUPPORTED_FINGERPRINTS", validSpoofFPS},
		{"../sni spoofing/core/src/config.rs", "SUPPORTED_QUIC_MODES", validSpoofQUIC},
		{"../sni spoofing/core/src/config.rs", "SUPPORTED_IP_MODES", validSpoofIPMode},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Skipf("engine source unavailable: %v", err)
		}
		body := regexp.MustCompile(`(?s)pub const ` + tc.decl + `: &\[&str\] = &\[(.*?)\];`).FindSubmatch(src)
		if body == nil {
			t.Fatalf("%s declares no pub const %s", tc.file, tc.decl)
		}
		names := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(body[1], -1)
		if len(names) < 3 {
			t.Fatalf("%s: parsed %d names out of %s, the const was read wrong", tc.file, len(names), tc.decl)
		}
		have := map[string]bool{}
		for _, g := range names {
			name := string(g[1])
			have[name] = true
			if !spoofEnumOk(name, tc.accepted) {
				t.Errorf("the engine supports %q but the tab cannot save it (%s)", name, tc.file)
			}
		}
		// The other direction is the one that mattered: a value the tab can
		// save but the engine does not know is not an error, it is a silent
		// rewrite. "custom" did exactly that — config.rs canonicalizes it to
		// legacy through tls.rs, so the user picked a fingerprint, saw it
		// stick in the dropdown, and got legacy bytes.
		for _, v := range tc.accepted {
			if !have[v] {
				t.Errorf("the tab saves %q but %s never lists it in %s", v, tc.file, tc.decl)
			}
		}
	}
	// And nothing the engine has deleted stays savable by name alone: the
	// retired spellings are only ever reached through migration.
	for _, dead := range []string{"double_sni", "delayed_retry"} {
		if spoofEnumOk(dead, validSpoofMethods) {
			t.Errorf("validSpoofMethods still lists %q, which v3 deleted", dead)
		}
	}
}

// The [STATS] line is a wire contract with no schema, no RPC and no shared
// type, so the Go mirror keeps compiling happily when the engine adds a
// field. That is what happened to Ranked.latency_ms/score: `auto` had been
// ranking on them for a whole release (stats.rs sorts the rows by score),
// while the tab could only show ok/fail and therefore looked like it was
// demoting a 100% method for no reason.
//
// Reading the field names out of the Rust structs and comparing them with
// the Go struct tags turns that class of drift into a test failure.
func TestSpoofStatsMirrorTheEngineSnapshot(t *testing.T) {
	src, err := os.ReadFile("../sni spoofing/core/src/stats.rs")
	if err != nil {
		t.Skipf("engine source unavailable: %v", err)
	}
	renameRe := regexp.MustCompile(`#\[serde\(rename\s*=\s*"([^"]+)"\)\]`)
	fieldRe := regexp.MustCompile(`^\s*pub\s+(\w+)\s*:`)
	for _, tc := range []struct {
		rustStruct string
		goName     string
		goValue    any
		// unread names engine fields the GUI deliberately does not
		// consume. Growing this list is a decision, not a fix.
		unread []string
	}{
		{"Ranked", "SpoofRanked", SpoofRanked{}, nil},
		{"Snapshot", "SpoofStats", SpoofStats{}, []string{"type"}},
	} {
		body := regexp.MustCompile(`(?s)pub struct ` + tc.rustStruct + ` \{(.*?)\n\}`).FindSubmatch(src)
		if body == nil {
			t.Fatalf("stats.rs declares no pub struct %s", tc.rustStruct)
		}
		want := map[string]bool{}
		pending := ""
		for _, line := range strings.Split(string(body[1]), "\n") {
			if m := renameRe.FindStringSubmatch(line); m != nil {
				pending = m[1]
				continue
			}
			m := fieldRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := pending
			pending = ""
			if key == "" {
				key = m[1]
			}
			want[key] = true
		}
		if len(want) < 4 {
			t.Fatalf("%s: parsed %d fields, the struct was read wrong", tc.rustStruct, len(want))
		}
		skip := map[string]bool{}
		for _, u := range tc.unread {
			skip[u] = true
		}
		have := map[string]bool{}
		rt := reflect.TypeOf(tc.goValue)
		for i := 0; i < rt.NumField(); i++ {
			tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			have[tag] = true
		}
		for k := range want {
			if skip[k] {
				continue
			}
			if !have[k] {
				t.Errorf("%s emits %q, which %s never unmarshals", tc.rustStruct, k, tc.goName)
			}
		}
		for k := range have {
			if !want[k] {
				t.Errorf("%s reads %q, which stats.rs %s never emits", tc.goName, k, tc.rustStruct)
			}
		}
	}
}

// A settings file written before v3 names values the engine deleted or
// folded together; those migrate rather than blocking the save, while a
// genuinely unknown value still fails and names the set it accepts.
func TestSpoofSetConfigMigratesRetiredSpellings(t *testing.T) {
	a, store := planTestApp(t, "B")
	if err := a.SpoofSetConfig(SNIConfig{Plan: "B", Method: "double_sni", Fingerprint: "chrome_124"}); err != nil {
		t.Fatalf("SpoofSetConfig(retired spellings): %v", err)
	}
	got := store.GetSettings().SNIConfig
	if got.Method != "auto" {
		t.Fatalf("retired method stored as %q, want auto", got.Method)
	}
	if got.Fingerprint != "chrome" {
		t.Fatalf("versioned fingerprint stored as %q, want chrome", got.Fingerprint)
	}
	if err := a.SpoofSetConfig(SNIConfig{Plan: "B", Method: "teleport"}); err == nil {
		t.Fatal("SpoofSetConfig(method teleport) succeeded, want rejection")
	} else if !strings.Contains(err.Error(), "hello_split") {
		t.Fatalf("error %q does not name the current method set", err)
	}
	if got := store.GetSettings().SNIConfig.Method; got != "auto" {
		t.Fatalf("rejected method persisted: %q", got)
	}
	// "custom" is not a fingerprint the engine can produce: config.rs lists
	// three, and tls.rs maps custom onto Legacy. It has to land on the name
	// that describes the bytes actually sent — and the rest of the card must
	// survive, which is the whole §23 failure class.
	if err := a.SpoofSetConfig(SNIConfig{Plan: "B", Method: "hello_split", Fingerprint: "custom", HandshakeTimeout: 3, QUICMode: "spoof"}); err != nil {
		t.Fatalf("SpoofSetConfig(fingerprint custom): %v", err)
	}
	got = store.GetSettings().SNIConfig
	if got.Fingerprint != "legacy" {
		t.Fatalf(`fingerprint "custom" stored as %q, want legacy`, got.Fingerprint)
	}
	if got.Method != "hello_split" || got.HandshakeTimeout != 3 || got.QUICMode != "spoof" {
		t.Fatalf("the migrated card lost its other knobs: %+v", got)
	}
	// An unknown fingerprint still fails — failing must not rewrite the card.
	if err := a.SpoofSetConfig(SNIConfig{Plan: "B", Method: "hello_split", Fingerprint: "safari"}); err == nil {
		t.Fatal("SpoofSetConfig(fingerprint safari) succeeded, want rejection")
	}
	if got := store.GetSettings().SNIConfig; got.Fingerprint != "legacy" || got.Method != "hello_split" {
		t.Fatalf("a rejected fingerprint rewrote the card: %+v", got)
	}
}

// sanity: the fake really does serve IPv4 answers through the same code
// path Plan A uses, and successes are cached process-wide while nothing
// is ever dialed outside loopback.
func TestFakeDoHServesARecords(t *testing.T) {
	forgetDoH(t, "example.com")
	want := "203.0.113.42"
	f := newFakeDoH(t, map[string]string{"example.com": want})
	useDoH(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ip, err := resolveA(ctx, "Example.COM.")
	if err != nil {
		t.Fatalf("resolveA: %v", err)
	}
	if ip != want || net.ParseIP(ip) == nil {
		t.Fatalf("resolveA = %q, want %q", ip, want)
	}
	// Cached answers must not re-query the fake.
	before := len(f.queries())
	if _, err := resolveA(ctx, "example.com"); err != nil {
		t.Fatalf("resolveA(cached): %v", err)
	}
	if q := f.queries(); len(q) != before {
		t.Fatalf("cached lookup hit the network again: %v", q)
	}
}
