package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSaveSettingsKeepsCoreOverrides: CoreOverrides is backend-owned, so
// a settings snapshot taken before an Advanced edit must not drop it.
func TestSaveSettingsKeepsCoreOverrides(t *testing.T) {
	dir := t.TempDir()
	st, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m["route"] = json.RawMessage(`{"final":"block"}`)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s := st.GetSettings()
	s.MixedPort = 2345
	if err := st.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.GetSettings().CoreOverrides["route"]
	var compact bytes.Buffer
	if err := json.Compact(&compact, got); err != nil {
		t.Fatal(err)
	}
	if compact.String() != `{"final":"block"}` {
		t.Fatalf("override lost on save: %q", compact.String())
	}
	if reopened.GetSettings().MixedPort != 2345 {
		t.Fatalf("ordinary setting lost: %d", reopened.GetSettings().MixedPort)
	}
}

// TestGetSettingsDoesNotAliasOverrides: mutating the returned map must
// not touch store state (the Advanced editors used to do exactly that,
// off-lock).
func TestGetSettingsDoesNotAliasOverrides(t *testing.T) {
	dir := t.TempDir()
	st, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCoreOverrides(func(m map[string]json.RawMessage) bool {
		m["route"] = json.RawMessage(`{"final":"block"}`)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s := st.GetSettings()
	s.CoreOverrides["dns"] = json.RawMessage(`{"servers":[]}`)
	delete(s.CoreOverrides, "route")
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ov := reopened.GetSettings().CoreOverrides
	if _, ok := ov["dns"]; ok {
		t.Fatalf("GetSettings handed out the live map: %v", ov)
	}
	if _, ok := ov["route"]; !ok {
		t.Fatalf("live override disappeared: %v", ov)
	}
}

// TestSpoofAutoDetectMigrationRunsOnce: the marker must persist, or the
// rule re-tags a profile the user deliberately switched back on every
// launch.
func TestSpoofAutoDetectMigrationRunsOnce(t *testing.T) {
	dir := t.TempDir()
	// Model an install written BEFORE the rule existed: a config.json
	// without the marker. A fresh store (no file) skips the save, so the
	// marker would already be on disk and the migration could not run.
	old := fileConfig{
		Settings: DefaultSettings(),
		Profiles: []Profile{{
			ID: "p1", Name: "cf", Protocol: "trojan", Address: "example.com", Port: 443,
			IDOrPassword: "x", TLS: true, Network: "ws", Core: CoreSingBox,
		}},
	}
	data, err := json.MarshalIndent(old, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := NewConfigStore(dir) // migration runs and records the marker
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetProfile("p1")
	if !ok || got.Core != CoreSpoof {
		t.Fatalf("migration did not tag the profile: core=%v", got.Core)
	}
	// The user explicitly switches it back to the sing-box core.
	got.Core = CoreSingBox
	if _, err := st.SaveProfile(got); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := reopened.GetProfile("p1")
	if again.Core != CoreSingBox {
		t.Fatal("auto-detect migration re-ran and re-tagged the profile")
	}
}

// TestUpdateProfileFieldsKeepsConcurrentEdits: a field stamp must not
// write the whole snapshot taken before a multi-second probe back.
func TestUpdateProfileFieldsKeepsConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	st, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.SaveProfile(Profile{Name: "a", Protocol: "trojan", Address: "a.com", Port: 443, IDOrPassword: "x", TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := st.GetProfile(p.ID)
	edited := snapshot
	edited.Name = "renamed"
	if _, err := st.SaveProfile(edited); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.updateProfileFields(snapshot.ID, func(e *Profile) {
		e.LastLatency = 42
		now := time.Now()
		e.LastTestedAt = &now
	}); err != nil || !ok {
		t.Fatalf("updateProfileFields: ok=%v err=%v", ok, err)
	}
	got, _ := st.GetProfile(p.ID)
	if got.Name != "renamed" {
		t.Fatalf("concurrent edit reverted: name=%q", got.Name)
	}
	if got.LastLatency != 42 {
		t.Fatalf("latency not stamped: %d", got.LastLatency)
	}
}

// TestClearSpoofFrontPortIfScoped: a late report from an old engine must
// not drop a newer engine's published port.
func TestClearSpoofFrontPortIfScoped(t *testing.T) {
	t.Cleanup(clearSpoofFrontPort)
	setSpoofFrontPort(7777)
	clearSpoofFrontPortIf(8888)
	if p, ok := currentSpoofFrontPort(); !ok || p != 7777 {
		t.Fatalf("stale clear dropped a live port: port=%d ok=%v", p, ok)
	}
	clearSpoofFrontPortIf(7777)
	if _, ok := currentSpoofFrontPort(); ok {
		t.Fatal("own port was not cleared")
	}
}

// TestDedupeKeyIncludesAetherSettings: two-hop aether links leave Address
// empty, so without the settings two different links collapsed.
func TestDedupeKeyIncludesAetherSettings(t *testing.T) {
	a := Profile{Protocol: "aether", Aether: aetherSettings{Protocol: "gool", Transport: "h3", WiwOuter: "1.2.3.4:443"}}
	b := a
	b.Aether.Transport = "h2"
	if dedupeKey(&a) == dedupeKey(&b) {
		t.Fatal("aether links with different transports share a dedupe key")
	}
}

// TestLegacyMigrationRewritesOnlyPaths: the old whole-file replacement
// altered credentials or names containing the old app name.
func TestLegacyMigrationRewritesOnlyPaths(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "SNI-NekoBox")
	newDir := filepath.Join(root, "NekoGo")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"settings":{"geoIPDBPath":"C:\\Users\\me\\AppData\\Roaming\\SNI-NekoBox\\GeoLite2-Country.mmdb"},` +
		`"profiles":[{"id":"p1","name":"SNI-NekoBox","protocol":"trojan","address":"a.com","port":443,` +
		`"idOrPassword":"SNI-NekoBox-secret","tls":true}]}`
	if err := os.WriteFile(filepath.Join(oldDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateFromOldAppDataDir(newDir, oldDir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(newDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `NekoGo\\GeoLite2-Country.mmdb`) {
		t.Fatalf("path not rewritten: %s", s)
	}
	if !strings.Contains(s, "SNI-NekoBox-secret") {
		t.Fatalf("credential was rewritten: %s", s)
	}
	if !strings.Contains(s, `"name":"SNI-NekoBox"`) {
		t.Fatalf("profile name was rewritten: %s", s)
	}
}

// TestSingBoxTunDefaultDNSRoutesThroughProxy: the TUN fallback resolver
// used to dial directly (a leak); the entry that answers the hijacked
// queries must name the proxy outbound. The block's second server is the
// detour-free bootstrap and must not.
func TestSingBoxTunDefaultDNSRoutesThroughProxy(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	p := Profile{Name: "t", Protocol: "trojan", Address: "a.com", Port: 443, IDOrPassword: "x", TLS: true}
	raw, err := BuildSingBoxConfig(&p, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	dns, _ := cfg["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("dns servers = %v, want the proxied resolver and the bootstrap", dns["servers"])
	}
	for _, raw := range servers {
		entry, _ := raw.(map[string]any)
		switch entry["tag"] {
		case "remote":
			if entry["detour"] != "proxy" {
				t.Fatalf("tun default dns = %v, want detour proxy", entry)
			}
		case "dns-bootstrap":
			if _, leaked := entry["detour"]; leaked {
				t.Fatalf("the bootstrap rides the proxy it must resolve: %v", entry)
			}
		default:
			t.Fatalf("unexpected dns server tag %v in %v", entry["tag"], servers)
		}
	}
}

// TestSingBoxTunDefaultDNSSkipsDirectDetour: a detour to the bare direct
// outbound is rejected by the engine, and a direct profile wants direct
// lookups anyway.
func TestSingBoxTunDefaultDNSSkipsDirectDetour(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	cfg := singDNS(t, s) // helper builds a direct profile
	dns, _ := cfg["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	entry, _ := servers[0].(map[string]any)
	if _, has := entry["detour"]; has {
		t.Fatalf("direct profile's tun dns carries a detour: %v", entry)
	}
}

// TestSingBoxDNSLocalAndRejectsUnknown: "local" renders the typed resolver
// instead of silently becoming Google DoH, and unknown values fail loudly.
func TestSingBoxDNSLocalAndRejectsUnknown(t *testing.T) {
	p := Profile{Name: "t", Protocol: "direct", Address: "h", Port: 1}
	s := DefaultSettings()
	s.DnsRemote = "local"
	raw, err := BuildSingBoxConfig(&p, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	dns, _ := cfg["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	entry, _ := servers[0].(map[string]any)
	if entry["type"] != "local" {
		t.Fatalf("remote dns = %v, want the local resolver", entry)
	}

	s.DnsRemote = "ftp://1.1.1.1"
	if _, err := BuildSingBoxConfig(&p, s); err == nil {
		t.Fatal("unsupported DNS value rendered a config")
	}
	if err := validateDNSServer("ftp://1.1.1.1"); err == nil {
		t.Fatal("save accepted an unsupported DNS value")
	}
}

// TestAlpnSplitsCommaList: a link's "h2,http/1.1" used to become one
// bogus protocol ID in both engines.
func TestAlpnSplitsCommaList(t *testing.T) {
	p := Profile{Name: "t", Protocol: "vless", Address: "a.com", Port: 443,
		IDOrPassword: "uuid", TLS: true, ALPN: "h2, http/1.1"}
	raw, err := BuildSingBoxConfig(&p, DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	obs, _ := cfg["outbounds"].([]any)
	ob, _ := obs[0].(map[string]any)
	tls, _ := ob["tls"].(map[string]any)
	alpn, _ := tls["alpn"].([]any)
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("sing-box alpn = %v", tls["alpn"])
	}

	xraw, err := BuildXrayConfig(&p, DefaultSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	var xcfg map[string]any
	if err := json.Unmarshal([]byte(xraw), &xcfg); err != nil {
		t.Fatal(err)
	}
	xobs, _ := xcfg["outbounds"].([]any)
	for _, o := range xobs {
		ob, _ := o.(map[string]any)
		if ob["tag"] != "proxy" {
			continue
		}
		st, _ := ob["streamSettings"].(map[string]any)
		xtls, _ := st["tlsSettings"].(map[string]any)
		xalpn, _ := xtls["alpn"].([]any)
		if len(xalpn) != 2 {
			t.Fatalf("xray alpn = %v", xtls["alpn"])
		}
		return
	}
	t.Fatal("xray proxy outbound not found")
}

// TestRawOutboundTagForcedToProxy: route.final names "proxy", so a pasted
// tag made the engine abort with "default outbound not found".
func TestRawOutboundTagForcedToProxy(t *testing.T) {
	p := Profile{Name: "t", Protocol: "vless",
		RawOutbound: `{"tag":"vless-out","type":"socks","server":"1.2.3.4","server_port":1080}`}
	raw, err := BuildSingBoxConfig(&p, DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	obs, _ := cfg["outbounds"].([]any)
	ob, _ := obs[0].(map[string]any)
	if ob["tag"] != "proxy" {
		t.Fatalf("raw outbound tag = %v, want proxy", ob["tag"])
	}
	route, _ := cfg["route"].(map[string]any)
	if route["final"] != "proxy" {
		t.Fatalf("route final = %v", route["final"])
	}
}

// TestSingBoxTransportHostPathAndRejectsKCP: http/httpupgrade keep their
// vhost path/host; kcp is rejected by the engine, so fail here instead.
func TestSingBoxTransportHostPathAndRejectsKCP(t *testing.T) {
	p := Profile{Name: "t", Protocol: "vless", Address: "a.com", Port: 443,
		IDOrPassword: "uuid", TLS: true, Network: "httpupgrade", Path: "/ws", Host: "cdn.example"}
	raw, err := BuildSingBoxConfig(&p, DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	obs, _ := cfg["outbounds"].([]any)
	ob, _ := obs[0].(map[string]any)
	tr, _ := ob["transport"].(map[string]any)
	if tr["type"] != "httpupgrade" || tr["path"] != "/ws" || tr["host"] != "cdn.example" {
		t.Fatalf("transport = %v", tr)
	}

	p.Network = "kcp"
	if _, err := BuildSingBoxConfig(&p, DefaultSettings()); err == nil {
		t.Fatal("kcp rendered a config the engine rejects")
	}
}
