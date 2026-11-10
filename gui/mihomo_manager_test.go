package main

// Tests for the mihomo (Clash Meta) core integration. The document
// renderer is pure, so these assert on the structure mihomo will read
// without needing the engine binary; the enum test pins the persisted
// numeric core values, because config.json stores them as integers.

import (
	"encoding/json"
	"strings"
	"testing"
)

// mihomoDoc parses a rendered document back into a map. It also proves
// the "JSON is valid YAML 1.2" premise the renderer relies on: a document
// that is not even valid JSON could not be parsed by mihomo either.
func mihomoDoc(t *testing.T, cfg string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
		t.Fatalf("rendered mihomo document is not valid JSON: %v\n%s", err, cfg)
	}
	return doc
}

func mihomoMap(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %#v", what, v)
	}
	return m
}

func mihomoList(t *testing.T, v any, what string) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is not an array: %#v", what, v)
	}
	return l
}

// firstMihomoProxy returns proxies[0] of a rendered document.
func firstMihomoProxy(t *testing.T, cfg string) map[string]any {
	t.Helper()
	doc := mihomoDoc(t, cfg)
	proxies := mihomoList(t, doc["proxies"], "proxies")
	if len(proxies) != 1 {
		t.Fatalf("want exactly 1 proxy, got %d", len(proxies))
	}
	return mihomoMap(t, proxies[0], "proxies[0]")
}

func renderMihomo(t *testing.T, p *Profile, s Settings) string {
	t.Helper()
	cfg, err := BuildMihomoConfig(p, s, 19090, "test-secret")
	if err != nil {
		t.Fatalf("BuildMihomoConfig: %v", err)
	}
	return cfg
}

// TestCoreTypeMihomoValues pins the numeric core values config.json
// persists. Adding a core must append, never reorder: a saved "core": 1
// has to keep meaning Xray.
func TestCoreTypeMihomoValues(t *testing.T) {
	if CoreSingBox != 0 || CoreXray != 1 || CoreSpoof != 2 || CoreMihomo != 3 {
		t.Fatalf("core enum values moved: sing-box=%d xray=%d spoof=%d mihomo=%d",
			CoreSingBox, CoreXray, CoreSpoof, CoreMihomo)
	}
	if got := CoreMihomo.String(); got != "mihomo" {
		t.Fatalf("CoreMihomo.String() = %q, want %q", got, "mihomo")
	}
	if !CoreMihomo.IsMihomo() || CoreMihomo.IsXray() || CoreMihomo.IsSpoof() {
		t.Fatal("CoreMihomo predicate is wrong")
	}
	if CoreSingBox.IsMihomo() || CoreXray.IsMihomo() || CoreSpoof.IsMihomo() {
		t.Fatal("IsMihomo is true for another core")
	}
}

// TestBuildMihomoConfigShape asserts the routing skeleton: one mixed
// inbound, one proxy behind one select group, and a single MATCH rule
// sending everything to that group.
func TestBuildMihomoConfigShape(t *testing.T) {
	s := DefaultSettings()
	s.MixedPort = 2080
	cfg := renderMihomo(t, &Profile{
		Name: "p", Core: CoreMihomo, Protocol: "trojan",
		Address: "example.com", Port: 443, IDOrPassword: "pw", TLS: true,
	}, s)
	doc := mihomoDoc(t, cfg)

	if got := doc["mixed-port"]; got != float64(2080) {
		t.Errorf("mixed-port = %v, want 2080", got)
	}
	if got := doc["mode"]; got != "rule" {
		t.Errorf("mode = %v, want rule", got)
	}
	if got := doc["external-controller"]; got != "127.0.0.1:19090" {
		t.Errorf("external-controller = %v, want 127.0.0.1:19090", got)
	}
	if got := doc["secret"]; got != "test-secret" {
		t.Errorf("secret = %v, want the generated token", got)
	}
	if got := doc["allow-lan"]; got != false {
		t.Errorf("allow-lan = %v, want false for a loopback inbound", got)
	}
	if got := doc["bind-address"]; got != "127.0.0.1" {
		t.Errorf("bind-address = %v, want 127.0.0.1", got)
	}
	// No geodata rule is rendered, so no geodata may be loaded or fetched:
	// a first connect must not depend on a download.
	if got := doc["geo-auto-update"]; got != false {
		t.Errorf("geo-auto-update = %v, want false", got)
	}
	if _, present := doc["tun"]; present {
		t.Error("mihomo document renders a tun block, which NekoGo renders in sing-box only")
	}

	rules := mihomoList(t, doc["rules"], "rules")
	if len(rules) != 1 || rules[0] != "MATCH,"+mihomoGroupName {
		t.Errorf("rules = %v, want a single MATCH,%s", rules, mihomoGroupName)
	}
	groups := mihomoList(t, doc["proxy-groups"], "proxy-groups")
	if len(groups) != 1 {
		t.Fatalf("want exactly 1 proxy group, got %d", len(groups))
	}
	group := mihomoMap(t, groups[0], "proxy-groups[0]")
	if group["name"] != mihomoGroupName {
		t.Errorf("group name = %v, want %s", group["name"], mihomoGroupName)
	}
	if group["type"] != "select" {
		t.Errorf("group type = %v, want select", group["type"])
	}
	members := mihomoList(t, group["proxies"], "group members")
	if len(members) != 1 || members[0] != mihomoProxyName {
		t.Errorf("group members = %v, want exactly [%s]", members, mihomoProxyName)
	}
	// The group must name a proxy that exists, or mihomo refuses to start.
	if got := firstMihomoProxy(t, cfg)["name"]; got != mihomoProxyName {
		t.Errorf("proxy name = %v, want %s (the group would dangle)", got, mihomoProxyName)
	}
}

// TestBuildMihomoConfigLANBind: a shared inbound renders the wildcard
// bind and turns allow-lan on, matching the other cores' InboundAddress
// contract.
func TestBuildMihomoConfigLANBind(t *testing.T) {
	s := DefaultSettings()
	s.InboundAddress = "0.0.0.0"
	doc := mihomoDoc(t, renderMihomo(t, &Profile{
		Name: "p", Core: CoreMihomo, Protocol: "shadowsocks",
		Address: "example.com", Port: 8388, IDOrPassword: "pw", Method: "aes-256-gcm",
	}, s))
	if got := doc["allow-lan"]; got != true {
		t.Errorf("allow-lan = %v, want true", got)
	}
	if got := doc["bind-address"]; got != "*" {
		t.Errorf("bind-address = %v, want *", got)
	}
}

// TestBuildMihomoConfigAPICollision: the controller must never share the
// mixed inbound's port.
func TestBuildMihomoConfigAPICollision(t *testing.T) {
	s := DefaultSettings()
	s.MixedPort = 19090
	_, err := BuildMihomoConfig(&Profile{
		Name: "p", Core: CoreMihomo, Protocol: "trojan",
		Address: "example.com", Port: 443, IDOrPassword: "pw",
	}, s, 19090, "s")
	if err == nil {
		t.Fatal("controller port colliding with the mixed port was accepted")
	}
}

// TestBuildMihomoConfigLogLevel: mihomo spells the third level
// "warning", so NekoGo's "warn" must be translated, not passed through.
func TestBuildMihomoConfigLogLevel(t *testing.T) {
	for level, want := range map[string]string{
		"warn": "warning", "warning": "warning", "debug": "debug",
		"error": "error", "info": "info", "": "info", "bogus": "info",
	} {
		s := DefaultSettings()
		s.LogLevel = level
		doc := mihomoDoc(t, renderMihomo(t, &Profile{
			Name: "p", Core: CoreMihomo, Protocol: "shadowsocks",
			Address: "example.com", Port: 8388, IDOrPassword: "pw",
		}, s))
		if got := doc["log-level"]; got != want {
			t.Errorf("LogLevel %q rendered log-level %v, want %s", level, got, want)
		}
	}
}

// TestMapProfileToMihomoProxy covers the protocol mapping: the same set
// the sing-box and Xray builders support, in mihomo's own key spelling.
func TestMapProfileToMihomoProxy(t *testing.T) {
	base := DefaultSettings()
	cases := []struct {
		name string
		p    Profile
		want map[string]any
	}{
		{
			name: "vless ws tls",
			p: Profile{
				Name: "v", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
				IDOrPassword: "uuid-1", Network: "ws", TLS: true, ServerName: "sni.example",
				Path: "/ws", Host: "host.example",
			},
			want: map[string]any{
				"type": "vless", "server": "example.com", "port": float64(443),
				"uuid": "uuid-1", "tls": true, "servername": "sni.example",
				"network": "ws", "udp": true, "client-fingerprint": "chrome",
			},
		},
		{
			name: "vless reality with flow",
			p: Profile{
				Name: "r", Core: CoreMihomo, Protocol: "vless", Address: "1.2.3.4", Port: 2053,
				IDOrPassword: "uuid-2", RealityEnabled: true, RealityPubKey: "pub", RealityShortID: "sid",
				Flow: "xtls-rprx-vision", TLSFingerprint: "firefox",
			},
			want: map[string]any{
				"type": "vless", "uuid": "uuid-2", "flow": "xtls-rprx-vision",
				"tls": true, "servername": "1.2.3.4", "client-fingerprint": "firefox",
			},
		},
		{
			name: "trojan grpc insecure",
			p: Profile{
				Name: "t", Core: CoreMihomo, Protocol: "trojan", Address: "example.com", Port: 443,
				IDOrPassword: "pw", Network: "grpc", Path: "Svc", AllowInsecure: true, ALPN: "h2, http/1.1",
			},
			want: map[string]any{
				"type": "trojan", "password": "pw", "tls": true, "servername": "example.com",
				"skip-cert-verify": true, "network": "grpc", "udp": true,
			},
		},
		{
			name: "vmess",
			p: Profile{
				Name: "m", Core: CoreMihomo, Protocol: "vmess", Address: "example.com", Port: 8443,
				IDOrPassword: "uuid-3", AlterID: 2, Security: "aes-128-gcm", TLS: true,
			},
			want: map[string]any{
				"type": "vmess", "uuid": "uuid-3", "alterId": float64(2),
				"cipher": "aes-128-gcm", "tls": true, "udp": true,
			},
		},
		{
			name: "shadowsocks default cipher",
			p: Profile{
				Name: "ss", Core: CoreMihomo, Protocol: "shadowsocks", Address: "example.com",
				Port: 8388, IDOrPassword: "pw",
			},
			want: map[string]any{
				"type": "ss", "cipher": "aes-256-gcm", "password": "pw", "udp": true,
			},
		},
		{
			name: "socks with credentials",
			p: Profile{
				Name: "socks", Core: CoreMihomo, Protocol: "socks", Address: "127.0.0.1",
				Port: 1080, IDOrPassword: "user:pass",
			},
			want: map[string]any{
				"type": "socks5", "username": "user", "password": "pass", "udp": true,
			},
		},
		{
			name: "http proxy",
			p: Profile{
				Name: "http", Core: CoreMihomo, Protocol: "http", Address: "127.0.0.1",
				Port: 8080, IDOrPassword: "user:pass",
			},
			want: map[string]any{"type": "http", "username": "user", "password": "pass"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			got := firstMihomoProxy(t, renderMihomo(t, &p, base))
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s = %#v, want %#v", k, got[k], want)
				}
			}
			if got["name"] != mihomoProxyName {
				t.Errorf("name = %v, want %s", got["name"], mihomoProxyName)
			}
		})
	}
}

// TestMihomoTransportBlocks: ws carries path+Host, grpc carries the
// service name, h2 carries a host LIST (mihomo's h2-opts shape).
func TestMihomoTransportBlocks(t *testing.T) {
	s := DefaultSettings()
	ws := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "ws", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", Network: "ws", Path: "/p", Host: "h.example",
	}, s))
	opts := mihomoMap(t, ws["ws-opts"], "ws-opts")
	if opts["path"] != "/p" {
		t.Errorf("ws path = %v, want /p", opts["path"])
	}
	headers := mihomoMap(t, opts["headers"], "ws headers")
	if headers["Host"] != "h.example" {
		t.Errorf("ws Host header = %v, want h.example", headers["Host"])
	}

	grpc := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "grpc", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", Network: "grpc", Path: "Svc",
	}, s))
	gopts := mihomoMap(t, grpc["grpc-opts"], "grpc-opts")
	if gopts["grpc-service-name"] != "Svc" {
		t.Errorf("grpc service name = %v, want Svc", gopts["grpc-service-name"])
	}

	h2 := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "h2", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", Network: "h2", Path: "/h2", Host: "h.example", TLS: true,
	}, s))
	hopts := mihomoMap(t, h2["h2-opts"], "h2-opts")
	hosts := mihomoList(t, hopts["host"], "h2 host")
	if len(hosts) != 1 || hosts[0] != "h.example" {
		t.Errorf("h2 host = %v, want [h.example]", hosts)
	}
}

// TestMihomoRealityOpts: REALITY renders under reality-opts with mihomo's
// key spelling, and a missing public key fails at render time instead of
// at engine startup.
func TestMihomoRealityOpts(t *testing.T) {
	s := DefaultSettings()
	p := &Profile{
		Name: "r", Core: CoreMihomo, Protocol: "vless", Address: "1.2.3.4", Port: 443,
		IDOrPassword: "u", RealityEnabled: true, RealityPubKey: "pub", RealityShortID: "sid",
	}
	got := firstMihomoProxy(t, renderMihomo(t, p, s))
	opts := mihomoMap(t, got["reality-opts"], "reality-opts")
	if opts["public-key"] != "pub" || opts["short-id"] != "sid" {
		t.Errorf("reality-opts = %#v, want public-key pub / short-id sid", opts)
	}
	noKey := &Profile{
		Name: "r2", Core: CoreMihomo, Protocol: "vless", Address: "1.2.3.4", Port: 443,
		IDOrPassword: "u", RealityEnabled: true,
	}
	if _, err := BuildMihomoConfig(noKey, s, 19090, "x"); err == nil {
		t.Fatal("REALITY without a public key was accepted")
	}
}

// TestMihomoSmux: mux renders as smux-opts, and an invalid mux protocol
// fails at render time like it does on the other cores.
func TestMihomoSmux(t *testing.T) {
	s := DefaultSettings()
	got := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "mux", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", MuxEnabled: true, MuxProtocol: "smux", MuxConcurrency: 8,
	}, s))
	opts := mihomoMap(t, got["smux-opts"], "smux-opts")
	if opts["enabled"] != true || opts["protocol"] != "smux" || opts["max-connections"] != float64(8) {
		t.Errorf("smux-opts = %#v", opts)
	}
	bad := &Profile{
		Name: "bad", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", MuxEnabled: true, MuxProtocol: "quicmux",
	}
	if _, err := BuildMihomoConfig(bad, s, 19090, "x"); err == nil {
		t.Fatal("an invalid mux protocol was accepted")
	}
}

// TestMihomoAetherProxy: an aether profile renders as a SOCKS proxy to
// the engine's listener, the same shape the other cores dial.
func TestMihomoAetherProxy(t *testing.T) {
	s := DefaultSettings()
	got := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "a", Core: CoreMihomo, Protocol: "aether", AetherEnginePort: 40000,
	}, s))
	if got["type"] != "socks5" {
		t.Errorf("type = %v, want socks5", got["type"])
	}
	if got["server"] != aetherLoopback {
		t.Errorf("server = %v, want %s", got["server"], aetherLoopback)
	}
	if got["port"] != float64(40000) {
		t.Errorf("port = %v, want the pinned engine port 40000", got["port"])
	}
}

// TestMihomoRawOutbound: a pasted raw proxy is passed through, re-named
// so the group, the MATCH rule and the latency probe still address it.
func TestMihomoRawOutbound(t *testing.T) {
	s := DefaultSettings()
	got := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "raw", Core: CoreMihomo, Protocol: "hysteria2",
		RawOutbound: `{"name":"pasted","type":"hysteria2","server":"example.com","port":443,"password":"pw"}`,
	}, s))
	if got["name"] != mihomoProxyName {
		t.Errorf("name = %v, want %s (a pasted name disconnects the group)", got["name"], mihomoProxyName)
	}
	if got["type"] != "hysteria2" || got["password"] != "pw" {
		t.Errorf("raw passthrough lost fields: %#v", got)
	}
}

// TestMihomoRejectsUnsupported: transports and protocols mihomo has no
// proxy-side implementation of fail at render time naming the core, so
// the user learns at Connect instead of watching mihomo refuse to start.
func TestMihomoRejectsUnsupported(t *testing.T) {
	s := DefaultSettings()
	cases := []struct {
		name string
		p    Profile
		want string
	}{
		{
			name: "quic transport",
			p: Profile{
				Name: "q", Core: CoreMihomo, Protocol: "vless", Address: "example.com",
				Port: 443, IDOrPassword: "u", Network: "quic",
			},
			want: "mihomo",
		},
		{
			name: "kcp transport",
			p: Profile{
				Name: "k", Core: CoreMihomo, Protocol: "vless", Address: "example.com",
				Port: 443, IDOrPassword: "u", Network: "kcp",
			},
			want: "mihomo",
		},
		{
			name: "wireguard protocol",
			p: Profile{
				Name: "wg", Core: CoreMihomo, Protocol: "wireguard", Address: "example.com",
				Port: 443, IDOrPassword: "u",
			},
			want: "rawOutbound",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			_, err := BuildMihomoConfig(&p, s, 19090, "x")
			if err == nil {
				t.Fatalf("accepted a profile mihomo cannot run")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestMihomoDropsUnknownFingerprint: a uTLS preset only the other cores
// know ("360", "qq") is dropped rather than rendered, because mihomo
// rejects an unknown client-fingerprint at startup.
func TestMihomoDropsUnknownFingerprint(t *testing.T) {
	s := DefaultSettings()
	got := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "f", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", TLS: true, TLSFingerprint: "360",
	}, s))
	if _, present := got["client-fingerprint"]; present {
		t.Errorf("client-fingerprint = %v, want it dropped for a preset mihomo does not know", got["client-fingerprint"])
	}
	if got["tls"] != true {
		t.Error("tls was dropped along with the unsupported fingerprint")
	}
	// "none" means "no explicit preset" in this codebase (types.go), so it
	// falls back to the same default the other cores render — not to no
	// fingerprint at all.
	none := firstMihomoProxy(t, renderMihomo(t, &Profile{
		Name: "n", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", TLS: true, TLSFingerprint: "none",
	}, s))
	if none["client-fingerprint"] != "chrome" {
		t.Errorf("client-fingerprint = %v, want the chrome default for the none preset", none["client-fingerprint"])
	}
}

// TestMihomoGlobalFingerprintOverride: the settings-wide uTLS override
// reaches the mihomo renderer, and the stored profile is not modified.
func TestMihomoGlobalFingerprintOverride(t *testing.T) {
	s := DefaultSettings()
	s.FingerprintEnabled = true
	s.FingerprintValue = "safari"
	p := &Profile{
		Name: "g", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", TLS: true, TLSFingerprint: "chrome",
	}
	got := firstMihomoProxy(t, renderMihomo(t, p, s))
	if got["client-fingerprint"] != "safari" {
		t.Errorf("client-fingerprint = %v, want the global override safari", got["client-fingerprint"])
	}
	if p.TLSFingerprint != "chrome" {
		t.Errorf("the stored profile was modified: TLSFingerprint = %q", p.TLSFingerprint)
	}
}

// TestMihomoIgnoresFragmentOverride pins the documented gap: TLS-hello
// fragmentation exists on Xray and sing-box only, so a global fragment
// override must not appear in a mihomo document (and must not fail one).
func TestMihomoIgnoresFragmentOverride(t *testing.T) {
	s := DefaultSettings()
	s.FragmentEnabled = true
	s.FragmentMode = "tlshello"
	s.FragmentLength = "10-20"
	cfg := renderMihomo(t, &Profile{
		Name: "frag", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", TLS: true,
	}, s)
	if strings.Contains(cfg, "fragment") {
		t.Errorf("mihomo document carries a fragment key it cannot honor:\n%s", cfg)
	}
}

// TestMihomoValidateProfileForSave: the save-time protocol gate knows the
// mihomo set, so a bad protocol fails at save instead of at Connect.
func TestMihomoValidateProfileForSave(t *testing.T) {
	ok := Profile{
		ID: "1", Name: "ok", Core: CoreMihomo, Protocol: "socks",
		Address: "127.0.0.1", Port: 1080,
	}
	if err := validateProfileForSave(ok); err != nil {
		t.Fatalf("a socks profile on mihomo was rejected: %v", err)
	}
	bad := Profile{
		ID: "2", Name: "bad", Core: CoreMihomo, Protocol: "direct",
		Address: "127.0.0.1", Port: 1080,
	}
	err := validateProfileForSave(bad)
	if err == nil {
		t.Fatal("a direct profile on mihomo was accepted")
	}
	if !strings.Contains(err.Error(), "mihomo") {
		t.Fatalf("error %q does not name the mihomo core", err)
	}
}

// TestMihomoNotRescuedToSpoof pins the deliberate exclusion: the two
// Connect-time rescues convert stale sing-box profiles to the spoof core,
// but a profile the user put on mihomo is a choice, not a stale import.
func TestMihomoNotRescuedToSpoof(t *testing.T) {
	s := DefaultSettings()
	s.MixedPort = 2080
	p := &Profile{
		ID: "m1", Name: "m", Core: CoreMihomo, Protocol: "trojan",
		Address: "cf.example.com", Port: 443, IDOrPassword: "pw", TLS: true, Network: "ws",
	}
	if spoofEligibilityIssue(p) != nil {
		t.Fatal("the fixture is not engine-eligible, so the test proves nothing")
	}
	if shouldRescueDirectSpoofProfile(p, s) {
		t.Error("a mihomo profile was rescued to the spoof core")
	}
	loop := &Profile{
		ID: "m2", Name: "l", Core: CoreMihomo, Protocol: "trojan",
		Address: "127.0.0.1", Port: 1, IDOrPassword: "pw", TLS: true, Network: "ws",
	}
	if shouldRescueLoopbackProfile(loop, s) {
		t.Error("a mihomo loopback profile was rescued to the spoof core")
	}
}

// TestMihomoTunNotRequired pins that a mihomo connect is never treated as
// a TUN session: mihomo renders no TUN inbound, so the adapter counters
// must not replace the engine's and admin must not be demanded.
func TestMihomoTunNotRequired(t *testing.T) {
	s := DefaultSettings()
	s.TunEnabled = true
	p := &Profile{ID: "t1", Name: "t", Core: CoreMihomo, Protocol: "vless", Address: "example.com", Port: 443, IDOrPassword: "u"}
	if tunModeFor(p, s) {
		t.Error("tunModeFor reports TUN for a mihomo profile")
	}
	if tunNeedsElevation(p, s) {
		t.Error("a mihomo connect demands elevation for a TUN adapter it never renders")
	}
}
