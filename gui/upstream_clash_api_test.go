package main

// Tests for the upstream (official sing-box CLI) backend: the Clash API
// stats/connections/delay paths, the injected clash_api block, the
// Connect-time refusal for NekoGo-only profiles, and the orphan list.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newClashTestManager points a manager at a fake Clash API. The returned
// server URL is the base the handler sees (127.0.0.1:<port>).
func newClashTestManager(t *testing.T, handler http.HandlerFunc) *UpstreamSingBoxManager {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server port: %v", err)
	}
	m := &UpstreamSingBoxManager{
		logCh:     make(chan LogEntry, 64),
		running:   true,
		apiPort:   port,
		apiSecret: "test-secret",
	}
	t.Cleanup(func() { m.running = false })
	return m
}

// clashFixture is the /connections body every test starts from.
const clashFixture = `{
  "downloadTotal": 3000,
  "uploadTotal": 1000,
  "connections": [
    {
      "id": "c1", "upload": 100, "download": 200, "chains": ["proxy"],
      "metadata": {"network": "tcp", "host": "example.com", "destinationIP": "93.184.216.34",
                   "destinationPort": "443", "processPath": "C:\\app\\nekogo.exe"}
    },
    {
      "id": "c2", "upload": 50, "download": 80, "chains": ["direct"],
      "metadata": {"network": "udp", "destinationIP": "8.8.8.8", "destinationPort": "53"}
    }
  ]
}`

func requireBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-secret")
	}
}

func TestUpstreamClashAPIStats(t *testing.T) {
	var fail atomic.Bool
	m := newClashTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Path != "/connections" {
			t.Errorf("path = %q, want /connections", r.URL.Path)
		}
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(clashFixture))
	})

	st, err := m.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if !st.Running || st.ApiDown {
		t.Fatalf("GetStats = %+v, want Running and not ApiDown", st)
	}
	// uploadTotal/downloadTotal are the API's session totals: the live
	// rows (150/280) plus the closed-connection remainder (850/2720).
	if st.Uplink != 1000 || st.Downlink != 3000 {
		t.Fatalf("uplink/downlink = %d/%d, want 1000/3000", st.Uplink, st.Downlink)
	}
	if got := st.UplinkByTag["proxy"]; got != 100 {
		t.Fatalf("uplinkByTag[proxy] = %d, want 100", got)
	}
	if got := st.DownlinkByTag["direct"]; got != 80 {
		t.Fatalf("downlinkByTag[direct] = %d, want 80", got)
	}
	if got := st.UplinkByTag[clashClosedTag]; got != 850 {
		t.Fatalf("uplinkByTag[%s] = %d, want 850", clashClosedTag, got)
	}
	// A second identical poll must not grow the ledger.
	again, err := m.GetStats()
	if err != nil {
		t.Fatalf("GetStats (second): %v", err)
	}
	if again.Uplink != st.Uplink || again.Downlink != st.Downlink {
		t.Fatalf("second poll changed totals: %+v vs %+v", again, st)
	}
	// A failing API keeps the core reported as running (counters stale).
	fail.Store(true)
	down, err := m.GetStats()
	if err != nil {
		t.Fatalf("GetStats (failing api): %v", err)
	}
	if !down.Running || !down.ApiDown {
		t.Fatalf("GetStats (failing api) = %+v, want Running with ApiDown", down)
	}
}

func TestUpstreamClashAPIConnections(t *testing.T) {
	m := newClashTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		_, _ = w.Write([]byte(clashFixture))
	})
	list, err := m.ListConnections()
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(list.Connections) != 2 {
		t.Fatalf("got %d connections, want 2: %+v", len(list.Connections), list.Connections)
	}
	first := list.Connections[0]
	if first.ID != "c1" || first.Outbound != "proxy" || first.Network != "tcp" {
		t.Fatalf("connection 0 = %+v", first)
	}
	if first.Domain != "example.com" || first.Dest != "example.com:443" {
		t.Fatalf("connection 0 domain/dest = %q/%q", first.Domain, first.Dest)
	}
	if first.Process != `C:\app\nekogo.exe` {
		t.Fatalf("connection 0 process = %q", first.Process)
	}
	if first.Upload != 100 || first.Download != 200 {
		t.Fatalf("connection 0 bytes = %d/%d", first.Upload, first.Download)
	}
	// No host: the destination IP stands in, tagged with the port.
	second := list.Connections[1]
	if second.Domain != "" || second.Dest != "8.8.8.8:53" || second.Outbound != "direct" {
		t.Fatalf("connection 1 = %+v", second)
	}
}

func TestUpstreamClashAPIURLTest(t *testing.T) {
	m := newClashTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		if r.URL.Path != "/proxies/proxy/delay" {
			t.Errorf("path = %q, want /proxies/proxy/delay", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("url"); got != "https://example.com/204" {
			t.Errorf("url = %q", got)
		}
		ms, err := strconv.Atoi(q.Get("timeout"))
		if err != nil || ms <= 0 || ms > maxClashDelayTimeoutMs {
			t.Errorf("timeout = %q, want 1..%d", q.Get("timeout"), maxClashDelayTimeoutMs)
		}
		_, _ = w.Write([]byte(`{"delay": 123}`))
	})
	latency, err := m.URLTest("https://example.com/204", 3000)
	if err != nil {
		t.Fatalf("URLTest: %v", err)
	}
	if latency != 123 {
		t.Fatalf("latency = %d, want 123", latency)
	}
	// A huge timeout is clamped instead of becoming a 400 (16-bit field).
	if _, err := m.URLTest("https://example.com/204", 600000); err != nil {
		t.Fatalf("URLTest (clamped timeout): %v", err)
	}

	fail := newClashTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no route", http.StatusServiceUnavailable)
	})
	if _, err := fail.URLTest("", 3000); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("URLTest on HTTP 503 = %v, want an error naming the status", err)
	}
}

func TestInjectClashAPIKeepsExistingExperimental(t *testing.T) {
	cfg := `{"log":{"level":"info"},"experimental":{"cache_file":{"enabled":true}}}`
	out, err := injectClashAPI(cfg, 4321, "deadbeef")
	if err != nil {
		t.Fatalf("injectClashAPI: %v", err)
	}
	var doc struct {
		Log struct {
			Level string `json:"level"`
		} `json:"log"`
		Experimental struct {
			CacheFile struct {
				Enabled bool `json:"enabled"`
			} `json:"cache_file"`
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("injected config is not JSON: %v\n%s", err, out)
	}
	if doc.Experimental.ClashAPI.ExternalController != "127.0.0.1:4321" {
		t.Fatalf("external_controller = %q", doc.Experimental.ClashAPI.ExternalController)
	}
	if doc.Experimental.ClashAPI.Secret != "deadbeef" {
		t.Fatalf("secret = %q", doc.Experimental.ClashAPI.Secret)
	}
	if !doc.Experimental.CacheFile.Enabled || doc.Log.Level != "info" {
		t.Fatalf("injection dropped existing keys: %s", out)
	}
}

func TestUpstreamRejectsUnsupportedProfile(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 512))
	settings := store.GetSettings()
	settings.CoreEngine = "upstream"
	if err := app.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	mieru, err := app.SaveProfile(Profile{
		Name: "mieru", Core: CoreSingBox, Protocol: "mieru",
		RawOutbound: `{"type":"mieru","tag":"proxy","server":"example.com","server_port":443,"username":"u","password":"p"}`,
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	started := false
	app.coreStarter = func(*Profile, Settings) error {
		started = true
		return nil
	}
	start := time.Now()
	err = app.Connect(mieru.ID)
	if err == nil || !strings.Contains(err.Error(), "upstream sing-box core cannot run") {
		t.Fatalf("Connect(mieru, upstream) = %v, want the upstream refusal", err)
	}
	if started {
		t.Fatal("Connect started a core for a profile the upstream backend cannot run")
	}
	// The refusal is a pre-flight check, not a 15s core decode failure.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("refusal took %v, want an immediate error", elapsed)
	}

	// Raw NekoGo-only outbounds and VLESS encryption are refused too; a
	// plain VLESS outbound is not.
	for _, tc := range []struct {
		name string
		p    Profile
		want bool
	}{
		{"raw mieru", Profile{Protocol: "vless", RawOutbound: `{"type":"mieru"}`}, true},
		{"raw trusttunnel", Profile{Protocol: "vless", RawOutbound: `{"type":"trusttunnel"}`}, true},
		{"vless encryption", Profile{Protocol: "vless", RawOutbound: `{"type":"vless","encryption":"mlkem768x25519plus"}`}, true},
		{"plain vless", Profile{Protocol: "vless", RawOutbound: `{"type":"vless","encryption":"none"}`}, false},
		{"plain trojan", Profile{Protocol: "trojan", RawOutbound: `{"type":"trojan"}`}, false},
		{"shadowsocks", Profile{Protocol: "shadowsocks"}, false},
	} {
		if got := upstreamUnsupportedReason(&tc.p) != ""; got != tc.want {
			t.Errorf("%s: refused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpstreamOrphanList(t *testing.T) {
	found := false
	for _, n := range orphanImageNames {
		if n == "sing-box.exe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphanImageNames = %v, want sing-box.exe", orphanImageNames)
	}
	if !isOrphanImage("sing-box.exe") {
		t.Fatal("isOrphanImage(sing-box.exe) = false")
	}
	dir := `C:\app\bin`
	got := selectOrphans([]orphanProc{
		{pid: 301, ppid: 1, name: "sing-box.exe", exePath: dir + `\sing-box.exe`},
	}, dir, 100, map[int]bool{100: true})
	if len(got) != 1 || got[0].pid != 301 {
		t.Fatalf("selectOrphans = %+v, want the stray sing-box.exe", got)
	}
}
