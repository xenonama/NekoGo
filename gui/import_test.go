package main

import (
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func mustParse(t *testing.T, link string) *Profile {
	t.Helper()
	p, err := parseOneLink(link)
	if err != nil {
		t.Fatalf("parse %q: %v", link, err)
	}
	return p
}

func TestParseVmess(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(
		`{"v":"2","ps":"test-vm","add":"1.2.3.4","port":"8080","id":"11111111-2222-4333-8444-555555555555","aid":"0","net":"ws","path":"/p","tls":"tls"}`))
	p := mustParse(t, "vmess://"+payload)
	if p.Protocol != "vmess" || p.Address != "1.2.3.4" || p.Port != 8080 {
		t.Fatalf("bad vmess mapping: %+v", p)
	}
	if !p.TLS || p.Network != "ws" || p.Path != "/p" || p.Name != "test-vm" {
		t.Fatalf("bad vmess fields: %+v", p)
	}
}

func TestParseVlessReality(t *testing.T) {
	p := mustParse(t, "vless://aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@real.example:443?security=reality&sni=real.example&fp=chrome&pbk=PUBKEY&sid=abcd&flow=xtls-rprx-vision&type=tcp#RealNode")
	if p.Protocol != "vless" || !p.TLS || !p.RealityEnabled {
		t.Fatalf("bad vless mapping: %+v", p)
	}
	if !p.Core.IsXray() {
		t.Fatalf("reality profile should route to xray: %+v", p)
	}
	if p.RealityPubKey != "PUBKEY" || p.Flow != "xtls-rprx-vision" || p.Name != "RealNode" {
		t.Fatalf("bad vless fields: %+v", p)
	}
}

func TestParseTrojan(t *testing.T) {
	p := mustParse(t, "trojan://secret@tj.example:443?sni=tj.example#TJ")
	if p.Protocol != "trojan" || !p.TLS || p.Address != "tj.example" || p.Port != 443 || p.Name != "TJ" {
		t.Fatalf("bad trojan mapping: %+v", p)
	}
}

// TestParseTrojanSpoofPromptLinks pins the three prompt.txt configs:
// the normal config stays on its normal core (no spoof tag: an IPv6
// literal never auto-tags), while both spoof configs auto-detect
// CoreSpoof (spoof tag) — the chained loopback shape (APPXA, served
// only by the engine) and the direct domain shape (Test-CF, whose
// direct dial is DPI-blocked). Both omit type=ws but carry ws-only
// path/host. Flows through every sync path (preview/import/
// subscription share parseOneLink).
func TestParseTrojanSpoofPromptLinks(t *testing.T) {
	normal := mustParse(t, "trojan://5504eaa8-1395-402f-8e0e-75ecd762e627@[2a06:98c1:3101::6812:202f]:443?alpn=http%2F1.1&fp=chrome&host=nat3.xxai.dpdns.org&path=%2Fanyway&security=tls&sni=nat3.xxai.dpdns.org&type=ws#@DeltaKroneckerGithub%20%7C%201877b3")
	if normal.Core.IsSpoof() {
		t.Fatalf("normal config core = %v, want normal (IP literals never auto-tag)", normal.Core)
	}
	if normal.Network != "ws" {
		t.Fatalf("normal config network = %q, want ws", normal.Network)
	}

	direct := mustParse(t, "trojan://humanity@www.calmlunch.com:443?host=www.calmlunch.com&path=%2Fassignment&security=tls&sni=www.calmlunch.com#Test-CF")
	if direct.Protocol != "trojan" || !direct.TLS {
		t.Fatalf("direct spoof proto/tls = %q/%v, want trojan/true", direct.Protocol, direct.TLS)
	}
	if direct.Network != "ws" {
		t.Fatalf("direct spoof network = %q, want ws (inferred from path/host)", direct.Network)
	}
	if !direct.Core.IsSpoof() {
		t.Fatalf("direct config core = %v, want spoof (direct domain shape auto-tags like loopback)", direct.Core)
	}
	if direct.Address != "www.calmlunch.com" || direct.Port != 443 {
		t.Fatalf("direct spoof endpoint = %s:%d, want www.calmlunch.com:443", direct.Address, direct.Port)
	}
	if direct.ServerName != "www.calmlunch.com" || direct.Path != "/assignment" || direct.Host != "www.calmlunch.com" {
		t.Fatalf("direct spoof sni/path/host = %q/%q/%q", direct.ServerName, direct.Path, direct.Host)
	}
	if direct.Name != "Test-CF" {
		t.Fatalf("direct spoof name = %q, want Test-CF", direct.Name)
	}

	chained := mustParse(t, "trojan://humanity@127.0.0.1:40443?host=www.calmlunch.com&path=%2Fassignment&security=tls&sni=www.calmlunch.com&type=ws#APPXA")
	if chained.Network != "ws" || !chained.Core.IsSpoof() {
		t.Fatalf("chained spoof network/core = %q/%v, want ws/spoof", chained.Network, chained.Core)
	}
	if chained.Address != "127.0.0.1" || chained.Port != 40443 {
		t.Fatalf("chained spoof endpoint = %s:%d, want 127.0.0.1:40443", chained.Address, chained.Port)
	}
	if chained.Name != "APPXA" {
		t.Fatalf("chained spoof name = %q, want APPXA", chained.Name)
	}

	// Plain trojan TCP (no WS params) must stay sing-box, not spoof.
	plain := mustParse(t, "trojan://secret@tj.example:443?sni=tj.example#TJ")
	if plain.Core.IsSpoof() {
		t.Fatalf("plain trojan core = spoof, want sing-box")
	}
	if plain.Network != "tcp" {
		t.Fatalf("plain trojan network = %q, want tcp", plain.Network)
	}
}

func TestParseSS(t *testing.T) {
	userinfo := base64.URLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	p := mustParse(t, "ss://"+userinfo+"@ss.example:8388#SSNode")
	if p.Protocol != "shadowsocks" || p.Method != "aes-256-gcm" || p.IDOrPassword != "password" {
		t.Fatalf("bad ss mapping: %+v", p)
	}
	if p.Address != "ss.example" || p.Port != 8388 || p.Name != "SSNode" {
		t.Fatalf("bad ss endpoint: %+v", p)
	}
}

func TestParseSSQueryShape(t *testing.T) {
	// Generated subscriptions routinely carry an empty query before the
	// name, which used to leave "?" glued to the port: "ss: bad port 443?".
	userinfo := base64.URLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:ARgvGZywA+gacgGV26Bvmu05+wZmRW/j+AdU+Z8Bt44="))
	p := mustParse(t, "ss://"+userinfo+"@185.213.23.63:990?#%40DeltaKroneckerGithub")
	if p.Method != "chacha20-ietf-poly1305" {
		t.Fatalf("method = %q", p.Method)
	}
	// PathUnescape must not turn the base64 punctuation in the password
	// into spaces.
	if p.IDOrPassword != "ARgvGZywA+gacgGV26Bvmu05+wZmRW/j+AdU+Z8Bt44=" {
		t.Fatalf("password mangled: %q", p.IDOrPassword)
	}
	if p.Address != "185.213.23.63" || p.Port != 990 {
		t.Fatalf("endpoint = %s:%d", p.Address, p.Port)
	}
	if p.Name != "@DeltaKroneckerGithub" {
		t.Fatalf("name = %q, want the fragment un-escaped", p.Name)
	}

	// A parameter NekoGo does not use is still not part of the endpoint.
	p = mustParse(t, "ss://YWVzLTEyOC1nY206c2hhZG93c29ja3M@141.98.101.179:443?0rtt=true#Node")
	if p.Port != 443 || p.Method != "aes-128-gcm" || p.IDOrPassword != "shadowsocks" {
		t.Fatalf("bad mapping with a query: %+v", p)
	}

	// A SIP003 plugin cannot be honoured, so say so instead of storing a
	// profile that silently never connects.
	for _, ln := range []string{
		"ss://YWVzLTEyOC1nY206c2hhZG93c29ja3M@1.2.3.4:443?plugin=obfs-local%3Bobfs%3Dhttp#P",
		"ss://YWVzLTEyOC1nY206c2hhZG93c29ja3M@1.2.3.4:443?Plugin=simple-obfs#P",
	} {
		if _, err := parseOneLink(ln); err == nil {
			t.Errorf("plugin link accepted: %s", ln)
		} else if !strings.Contains(err.Error(), "plugin") {
			t.Errorf("error %q does not name the plugin", err)
		}
	}

	// The other two shapes keep working: plaintext userinfo, and the 2019
	// form whose base64 covers the whole link (no "?" in base64 to cut on).
	p = mustParse(t, "ss://aes-256-gcm:pw@ss.example:8388#Plain")
	if p.Method != "aes-256-gcm" || p.IDOrPassword != "pw" || p.Port != 8388 {
		t.Fatalf("plaintext userinfo broke: %+v", p)
	}
	whole := base64.URLEncoding.EncodeToString([]byte("aes-256-gcm:pw@ss.example:8388"))
	p = mustParse(t, "ss://"+whole+"#Whole")
	if p.Method != "aes-256-gcm" || p.IDOrPassword != "pw" ||
		p.Address != "ss.example" || p.Port != 8388 {
		t.Fatalf("2019 whole-link form broke: %+v", p)
	}
}

func TestParseSocks(t *testing.T) {
	p := mustParse(t, "socks://user:pass@so.example:1080#SO")
	if p.Protocol != "socks" || p.Address != "so.example" || p.Port != 1080 || p.IDOrPassword != "user:pass" {
		t.Fatalf("bad socks mapping: %+v", p)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, ln := range []string{"", "not-a-link", "http://x", "vless://", "ss://@@@", "vmess://%%invalid%%"} {
		if _, err := parseOneLink(ln); err == nil {
			t.Fatalf("expected error for %q", ln)
		}
	}
}

func TestPreviewTextDirect(t *testing.T) {
	text := "vless://aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@a.example:443# A\n\nbogus-link\ntrojan://pw@b.example:443# B"
	profiles, invalid := previewText(text, "")
	if len(profiles) != 2 || invalid != 1 {
		t.Fatalf("got %d profiles %d invalid", len(profiles), invalid)
	}
	if !strings.Contains(profiles[0].Name, "A") {
		t.Fatalf("bad first profile: %+v", profiles[0])
	}
}

func TestAppImportLinksEndToEnd(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	logCh := make(chan LogEntry, 16)
	app := NewApp(store, logCh)
	// ImportLinks kicks off a tracked background GeoIP refresh when
	// GeoIPAutoDetect is on (the default). Drain it before TempDir is
	// removed, or its late SaveFile races RemoveAll on Windows.
	t.Cleanup(app.waitCountryRefresh)
	payload := base64.StdEncoding.EncodeToString([]byte(
		`{"v":"2","ps":"e2e-vm","add":"9.9.9.9","port":1234,"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","aid":0,"net":"tcp","tls":""}`))
	res, err := app.ImportLinks("vmess://" + payload + "\nthis-is-not-a-link")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 1 || res.Invalid != 1 {
		t.Fatalf("got %+v", res)
	}
	got, ok := store.GetProfile(res.Profiles[0].ID)
	if !ok || got.Address != "9.9.9.9" || got.Port != 1234 || got.Name != "e2e-vm" {
		t.Fatalf("profile not persisted: %+v", got)
	}
	if len(store.ListProfiles()) != 1 {
		t.Fatalf("store has %d profiles", len(store.ListProfiles()))
	}
	// Re-importing the same link must not duplicate.
	res2, err := app.ImportLinks("vmess://" + payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Profiles) != 0 || len(store.ListProfiles()) != 1 {
		t.Fatalf("dedupe failed: %+v", res2)
	}
}

// TestImportKeepsAetherTwoHopLinks — a two-hop Aether profile (gool/mim/
// wg-over-masque) has no address of its own: its endpoints live in the
// outer/inner hops. Preview lists it, so the importer must store it too
// instead of counting it invalid and dropping it on the floor.
func TestImportKeepsAetherTwoHopLinks(t *testing.T) {
	const link = "aether://?protocol=gool&outer=162.159.192.1%3A2408&inner=188.114.96.1%3A894#GOOL"
	if profiles, invalid := previewText(link, ""); len(profiles) != 1 || invalid != 0 {
		t.Fatalf("preview = %d profiles / %d invalid, want 1 / 0", len(profiles), invalid)
	}

	a := newGeoTestApp(t)
	s := a.store.GetSettings()
	s.GeoIPAutoDetect = false // no background refresh goroutine in a test
	if err := a.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	res, err := a.ImportLinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 1 || res.Invalid != 0 {
		t.Fatalf("import = %d profiles / %d invalid, want 1 / 0", len(res.Profiles), res.Invalid)
	}
	got, ok := a.store.GetProfile(res.Profiles[0].ID)
	if !ok {
		t.Fatal("two-hop profile not persisted")
	}
	if got.Aether.WiwOuter != "162.159.192.1:2408" || got.Aether.WiwInner != "188.114.96.1:894" {
		t.Fatalf("hops = %q / %q", got.Aether.WiwOuter, got.Aether.WiwInner)
	}

	// A one-hop link with no endpoint is still rejected.
	if profiles, invalid := previewText("aether://?protocol=wg#NoEndpoint", ""); invalid != 1 || len(profiles) != 0 {
		t.Fatalf("address-less one-hop link: %d profiles / %d invalid, want 0 / 1", len(profiles), invalid)
	}
}

func newLatencyTestApp(t *testing.T) *App {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewApp(store, make(chan LogEntry, 16))
}

// TestProfileLatency asserts the real-probe contract end to end without
// internet: the probe URL is a local httptest origin fetched through the
// ephemeral core (direct outbound), and the measured latency is a real
// number that gets persisted to the store.
func TestProfileLatency(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("probe-ok"))
	}))
	defer origin.Close()
	app := newLatencyTestApp(t)
	coreBin := filepath.Join("build", "bin", "nekobox_core.exe")
	if _, err := os.Stat(coreBin); err != nil {
		t.Skipf("missing %s", coreBin)
	}
	absBin, err := filepath.Abs(coreBin)
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.CorePath = absBin
	if err := app.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	saved, err := app.store.SaveProfile(Profile{
		Name: "latency-local", Core: CoreSingBox, Protocol: "direct",
		TestURL:     origin.URL + "/",
		RawOutbound: `{"type":"direct","tag":"proxy"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.TestProfile(saved.ID, 15000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || res.LatencyMs < 0 || res.LatencyMs > 10000 {
		t.Fatalf("unexpected latency: %+v", res)
	}
	got, ok := app.store.GetProfile(saved.ID)
	if !ok || got.LastLatency != int(res.LatencyMs) || got.LastTestedAt == nil {
		t.Fatalf("latency not persisted: %+v", got)
	}
}

// TestProfileLatencyUnreachable points at a closed port and asserts -1.
func TestProfileLatencyUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	app := newLatencyTestApp(t)
	saved, err := app.store.SaveProfile(Profile{
		Name: "latency-closed", Core: CoreSingBox, Protocol: "vless",
		Address: "127.0.0.1", Port: port, IDOrPassword: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.TestProfile(saved.ID, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if res.LatencyMs != -1 {
		t.Fatalf("expected -1, got %+v", res)
	}
}

// TestDedupeDifferentMethod — same ss server+password but different
// methods are distinct profiles; both must be imported (P1-4).
func TestDedupeDifferentMethod(t *testing.T) {
	mk := func(method string) string {
		ui := base64.URLEncoding.EncodeToString([]byte(method + ":password"))
		return "ss://" + ui + "@ss.example:8388#SS"
	}
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	res, err := app.ImportLinks(mk("aes-256-gcm") + "\n" + mk("chacha20-poly1305"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 2 || res.Invalid != 0 {
		t.Fatalf("got %d profiles %d invalid, want 2/0", len(res.Profiles), res.Invalid)
	}
	if len(store.ListProfiles()) != 2 {
		t.Fatalf("store has %d profiles, want 2", len(store.ListProfiles()))
	}
}

// TestDedupeDifferentSNI — identical trojan endpoints with different SNI
// are distinct profiles; both must be imported (P1-4).
func TestDedupeDifferentSNI(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	res, err := app.ImportLinks("trojan://pw@tj.example:443?sni=a.example#A\ntrojan://pw@tj.example:443?sni=b.example#B")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 2 || res.Invalid != 0 {
		t.Fatalf("got %d profiles %d invalid, want 2/0", len(res.Profiles), res.Invalid)
	}
}

// TestDedupeIdenticalStillDedupes — byte-identical links collapse to one.
func TestDedupeIdenticalStillDedupes(t *testing.T) {
	ui := base64.URLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	ln := "ss://" + ui + "@ss.example:8388#SS"
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	res, err := app.ImportLinks(ln + "\n" + ln)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 1 || len(store.ListProfiles()) != 1 {
		t.Fatalf("identical links not deduped: %+v", res)
	}
}

// TestFetchSubscriptionRejectsErrorStatus — BUG-025: a 404 page
// containing "://" must be an error, not parsed profiles.
func TestFetchSubscriptionRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html>see vless://dead-link here</html>"))
	}))
	defer srv.Close()
	lines, _, err := fetchSubscription(srv.URL)
	if err == nil {
		t.Fatalf("404 subscription accepted with %d lines, want error", len(lines))
	}
}

// TestFetchSubscriptionUserInfo: the provider quota header is parsed
// and returned alongside the lines (zero value when absent).
func TestFetchSubscriptionUserInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("subscription-userinfo", "upload=100; download=200; total=1000; expire=1893456000")
		_, _ = w.Write([]byte("vless://u@h:1#n"))
	}))
	defer srv.Close()
	lines, info, err := fetchSubscription(srv.URL)
	if err != nil {
		t.Fatalf("fetchSubscription: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	if info.Upload != 100 || info.Download != 200 || info.Total != 1000 || info.Expire != "1893456000" {
		t.Fatalf("userinfo = %+v, want 100/200/1000/1893456000", info)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://u@h:1#n"))
	}))
	defer plain.Close()
	_, info2, err := fetchSubscription(plain.URL)
	if err != nil {
		t.Fatalf("fetchSubscription: %v", err)
	}
	if info2 != (SubUserInfo{}) {
		t.Fatalf("userinfo = %+v, want zero without the header", info2)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("subscription-userinfo", "upload=abc; total=-5; nope")
		_, _ = w.Write([]byte("vless://u@h:1#n"))
	}))
	defer broken.Close()
	_, info3, err := fetchSubscription(broken.URL)
	if err != nil {
		t.Fatalf("fetchSubscription: %v", err)
	}
	if info3.Upload != 0 || info3.Total != 0 {
		t.Fatalf("userinfo = %+v, want malformed pairs skipped", info3)
	}
}

// Subscription management (v2rayN-style edit/delete):
// DeleteSubscription drops the record but keeps imported profiles;
// EditSubscription renames a feed, migrates the profiles' group, and
// repoints the URL by importing the new feed before dropping the old
// — a failed fetch must leave the old record intact.
func TestSubscriptionEditDelete(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	if _, err := store.AddSubscription(Subscription{URL: "https://a.example/sub", Name: "a.example"}); err != nil {
		t.Fatal(err)
	}
	p, err := app.SaveProfile(Profile{Name: "p1", Core: CoreSingBox, Group: "a.example",
		Protocol: "direct", RawOutbound: `{"type":"direct","tag":"proxy"}`})
	if err != nil {
		t.Fatal(err)
	}
	// Pure rename: record renamed, imported profile regrouped.
	if _, err := app.EditSubscription("https://a.example/sub", "https://a.example/sub", "My Feed"); err != nil {
		t.Fatal(err)
	}
	subs := store.GetSettings().Subscriptions
	if len(subs) != 1 || subs[0].Name != "My Feed" || subs[0].URL != "https://a.example/sub" {
		t.Fatalf("rename: %+v", subs)
	}
	if got, _ := store.GetProfile(p.ID); got.Group != "My Feed" {
		t.Fatalf("profile group = %q, want My Feed", got.Group)
	}
	// URL change: new feed imported + recorded, old record dropped,
	// kept name preserved even though the new URL has a new host.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://u@h:443#new"))
	}))
	defer srv.Close()
	res, err := app.EditSubscription("https://a.example/sub", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Profiles) != 1 {
		t.Fatalf("edit URL imported %d profiles, want 1", len(res.Profiles))
	}
	if res.Profiles[0].Group != "My Feed" {
		t.Fatalf("new profile group = %q, want My Feed", res.Profiles[0].Group)
	}
	subs = store.GetSettings().Subscriptions
	if len(subs) != 1 || subs[0].URL != srv.URL || subs[0].Name != "My Feed" {
		t.Fatalf("edit URL: %+v", subs)
	}
	// A failed fetch keeps the old feed recorded.
	if _, err := app.EditSubscription(srv.URL, "http://127.0.0.1:1/nope", ""); err == nil {
		t.Fatal("edit to unreachable URL = nil, want error")
	}
	subs = store.GetSettings().Subscriptions
	if len(subs) != 1 || subs[0].URL != srv.URL {
		t.Fatalf("failed edit dropped the feed: %+v", subs)
	}
	// The repoint migrated the imported profiles' source URL.
	importedID := ""
	for _, e := range store.ListProfiles() {
		if e.SubURL == srv.URL {
			importedID = e.ID
		}
	}
	if importedID == "" {
		t.Fatal("edited feed's profile missing SubURL")
	}
	// Delete feed-only: record gone, every profile stays.
	if n, err := app.DeleteSubscription(srv.URL, false); err != nil || n != 0 {
		t.Fatalf("feed-only delete = %d/%v, want 0/nil", n, err)
	}
	if n := len(store.GetSettings().Subscriptions); n != 0 {
		t.Fatalf("%d feeds left after delete, want 0", n)
	}
	if len(store.ListProfiles()) != 2 {
		t.Fatalf("%d profiles left, want 2 (feed-only delete)", len(store.ListProfiles()))
	}
	// Delete with configs: SubURL match + group fallback for the
	// legacy manual row renamed to the feed's group.
	if _, err := store.AddSubscription(Subscription{URL: srv.URL, Name: "My Feed"}); err != nil {
		t.Fatal(err)
	}
	n, err := app.DeleteSubscription(srv.URL, true)
	if err != nil || n != 2 {
		t.Fatalf("delete with configs = %d/%v, want 2/nil", n, err)
	}
	if len(store.ListProfiles()) != 0 || len(store.GetSettings().Subscriptions) != 0 {
		t.Fatalf("delete with configs left %d profiles / %d feeds, want 0/0",
			len(store.ListProfiles()), len(store.GetSettings().Subscriptions))
	}
	// Store guards: unknown URL is a no-op, blank rename is rejected,
	// unknown enable-toggle reports not-found.
	if ok, err := store.DeleteSubscription("https://nope"); err != nil || ok {
		t.Fatalf("delete unknown = %v/%v, want false/nil", ok, err)
	}
	if _, err := store.RenameSubscription(srv.URL, "  "); err == nil {
		t.Fatal("rename to blank = nil, want error")
	}
	if ok, err := store.SetSubscriptionEnabled("https://nope", false); err != nil || ok {
		t.Fatalf("enable unknown = %v/%v, want false/nil", ok, err)
	}
}

// Disabled feeds stay listed but are skipped by UpdateSubscriptions
// (and thereby by the auto-update timer, which calls it); re-enabling
// resumes updates. Imported profiles record their source SubURL.
func TestUpdateSubscriptionsSkipsDisabled(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hitsA, hitsB := 0, 0
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitsA++
		mu.Unlock()
		_, _ = w.Write([]byte("vless://u@a.example:443#a"))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitsB++
		mu.Unlock()
		_, _ = w.Write([]byte("vless://u@b.example:443#b"))
	}))
	defer srvB.Close()
	if _, err := store.AddSubscription(Subscription{URL: srvA.URL, Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSubscription(Subscription{URL: srvB.URL, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range store.GetSettings().Subscriptions {
		if !s.IsEnabled() {
			t.Fatal("legacy record without the flag must default to enabled")
		}
	}
	if _, err := store.SetSubscriptionEnabled(srvB.URL, false); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 16))
	if _, err := app.UpdateSubscriptions(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotA, gotB := hitsA, hitsB
	mu.Unlock()
	if gotA != 1 || gotB != 0 {
		t.Fatalf("hits A/B = %d/%d after update, want 1/0 (disabled skipped)", gotA, gotB)
	}
	if _, err := store.SetSubscriptionEnabled(srvB.URL, true); err != nil {
		t.Fatal(err)
	}
	if _, err := app.UpdateSubscriptions(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotB = hitsB
	mu.Unlock()
	if gotB != 1 {
		t.Fatalf("hits B = %d after re-enable, want 1", gotB)
	}
	foundB := false
	for _, e := range store.ListProfiles() {
		if e.SubURL == srvB.URL {
			foundB = true
		}
	}
	if !foundB {
		t.Fatal("imported profile missing the source SubURL")
	}
}
