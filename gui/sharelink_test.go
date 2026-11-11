package main

import (
	"strings"
	"testing"
)

// shareTestApp builds an App on a temp store with one saved profile.
func shareTestApp(t *testing.T, p Profile) (*App, Profile) {
	t.Helper()
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.Core = CoreSingBox
	saved, err := store.SaveProfile(p)
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	return NewApp(store, make(chan LogEntry, 8)), saved
}

// roundTrip builds a link for the saved profile and re-parses it,
// asserting every stored field survives.
func roundTrip(t *testing.T, app *App, id string) *Profile {
	t.Helper()
	link, err := app.BuildShareLink(id)
	if err != nil {
		t.Fatalf("BuildShareLink: %v", err)
	}
	got, err := parseOneLink(link)
	if err != nil {
		t.Fatalf("re-parse %q: %v", link, err)
	}
	return got
}

func TestShareLinkVmess(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "my vmess", Address: "example.com", Port: 443,
		Protocol: "vmess", IDOrPassword: "7163b2d5-...", AlterID: 0,
		Security: "auto", Network: "ws", TLS: true,
		ServerName: "example.com", Fingerprint: "chrome",
		Path: "/path", Host: "example.com", ALPN: "h2",
	})
	got := roundTrip(t, app, saved.ID)
	for k, want := range map[string]string{
		"name": saved.Name, "addr": saved.Address, "id": saved.IDOrPassword,
		"net": "ws", "sni": "example.com", "fp": "chrome",
		"path": "/path", "host": "example.com", "alpn": "h2",
	} {
		var have string
		switch k {
		case "name":
			have = got.Name
		case "addr":
			have = got.Address
		case "id":
			have = got.IDOrPassword
		case "net":
			have = got.Network
		case "sni":
			have = got.ServerName
		case "fp":
			have = got.Fingerprint
		case "path":
			have = got.Path
		case "host":
			have = got.Host
		case "alpn":
			have = got.ALPN
		}
		if have != want {
			t.Fatalf("%s = %q, want %q", k, have, want)
		}
	}
	if !got.TLS || got.Port != 443 {
		t.Fatalf("tls/port = %v/%d, want true/443", got.TLS, got.Port)
	}
}

func TestShareLinkVlessReality(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "vless reality", Address: "104.16.0.1", Port: 443,
		Protocol: "vless", IDOrPassword: "uuid-1", Flow: "xtls-rprx-vision",
		TLS: true, RealityEnabled: true, RealityPubKey: "pbk1",
		RealityShortID: "sid1", ServerName: "example.com",
		TLSFingerprint: "firefox", Network: "tcp",
	})
	got := roundTrip(t, app, saved.ID)
	if !got.RealityEnabled || got.RealityPubKey != "pbk1" || got.RealityShortID != "sid1" {
		t.Fatalf("reality = %+v, want enabled/pbk1/sid1", got)
	}
	if got.Flow != "xtls-rprx-vision" || got.Fingerprint != "firefox" {
		t.Fatalf("flow/fp = %q/%q", got.Flow, got.Fingerprint)
	}
	if got.Core != CoreXray {
		t.Fatalf("core = %v, want xray (reality routes to xray)", got.Core)
	}
}

func TestShareLinkTrojanWS(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "trojan ws", Address: "example.org", Port: 443,
		Protocol: "trojan", IDOrPassword: "p@ss:word/with?s",
		Network: "ws", TLS: true, ServerName: "example.org",
		Path: "/wspath", Host: "example.org",
	})
	link, err := app.BuildShareLink(saved.ID)
	if err != nil {
		t.Fatalf("BuildShareLink: %v", err)
	}
	got, err := parseOneLink(link)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got.IDOrPassword != "p@ss:word/with?s" {
		t.Fatalf("password = %q, want the special-char original", got.IDOrPassword)
	}
	if got.Path != "/wspath" || got.Host != "example.org" || got.Network != "ws" {
		t.Fatalf("ws opts = %+v", got)
	}
}

func TestShareLinkSS(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "my ss", Address: "10.0.0.1", Port: 8388,
		Protocol: "shadowsocks", Method: "aes-256-gcm",
		IDOrPassword: "secret!",
	})
	got := roundTrip(t, app, saved.ID)
	if got.Method != "aes-256-gcm" || got.IDOrPassword != "secret!" {
		t.Fatalf("ss = %+v", got)
	}

	// buildSSLink base64s the password without escaping it, so a password
	// carrying "%" must survive the round trip byte-for-byte — the parser
	// used to percent-decode it into something else (or into "").
	app2, saved2 := shareTestApp(t, Profile{
		Name: "pct ss", Address: "10.0.0.2", Port: 8388,
		Protocol: "shadowsocks", Method: "chacha20-ietf-poly1305",
		IDOrPassword: "a%b+c/d=e%41",
	})
	if got := roundTrip(t, app2, saved2.ID); got.IDOrPassword != "a%b+c/d=e%41" {
		t.Fatalf("ss round trip mangled the password: %q", got.IDOrPassword)
	}
}

func TestShareLinkSocks(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "s 5", Address: "127.0.0.1", Port: 1080,
		Protocol: "socks", IDOrPassword: "user name:pw",
	})
	got := roundTrip(t, app, saved.ID)
	if got.IDOrPassword != "user name:pw" || got.Name != "s 5" {
		t.Fatalf("socks = %+v (space must survive both fields)", got)
	}
}

// TestShareLinkRoundTripAdversarial — the names, paths and credentials
// real subscriptions carry. URL syntax has opinions about "#", "?", "%",
// "&", "=" and "+", and an escape applied on one side but not undone (or
// not re-applied) on the other silently rewrites or empties the value:
// that is exactly how the ss:// password bug shipped, so every field that
// crosses the link format has to come back byte-for-byte.
func TestShareLinkRoundTripAdversarial(t *testing.T) {
	nasty := []string{
		"@DeltaKroneckerGithub",
		"100%",
		"a%41b",
		"a+b/c=d",
		"node & edge?x=1",
		"日本 01",
		"héllo#2",
		"trailing spaces  ",
		"emoji🚀",
	}
	protocols := []struct {
		name  string
		probe func(string) Profile
	}{
		{"vless", func(n string) Profile {
			return Profile{Protocol: "vless",
				Name:           n,
				Address:        "v.example",
				Port:           443,
				IDOrPassword:   "11111111-2222-4333-8444-555555555555",
				TLS:            true,
				Network:        "ws",
				Path:           "/ws?ed=2560&host=a%3Ab",
				Host:           "cdn." + n,
				ServerName:     "tls.example",
				Fingerprint:    "chrome",
				ALPN:           "h3",
				RealityEnabled: true, RealityPubKey: "pbkValue", RealityShortID: "00",
				RealitySpiderX: "/spx?x=1"}
		}},
		{"trojan", func(n string) Profile {
			return Profile{Protocol: "trojan", Name: n, Address: "t.example", Port: 443,
				IDOrPassword: "p@ss:?#%&=+/ word", TLS: true, Network: "ws",
				Path: "/rw?a=b", Host: "h." + n, ServerName: "t.example", Fingerprint: "firefox"}
		}},
		{"ss", func(n string) Profile {
			return Profile{Protocol: "shadowsocks", Name: n, Address: "s.example", Port: 8388,
				Method: "aes-256-gcm", IDOrPassword: "s3cr3t%p4ss+q/r=d?"}
		}},
		{"socks", func(n string) Profile {
			return Profile{Protocol: "socks", Name: n, Address: "127.0.0.1", Port: 1080,
				IDOrPassword: "user:name%41?pw"}
		}},
		{"vmess", func(n string) Profile {
			return Profile{Protocol: "vmess", Name: n, Address: "v2.example", Port: 443,
				IDOrPassword: "11111111-2222-4333-8444-555555555555", Security: "auto",
				AlterID: 16, Network: "ws", Path: "/p?a=b&c=%41", Host: "h." + n,
				TLS: true, ServerName: "sni.example", Fingerprint: "safari"}
		}},
	}
	for _, pr := range protocols {
		for _, n := range nasty {
			want := pr.probe(n)
			app, saved := shareTestApp(t, want)
			got := roundTrip(t, app, saved.ID)
			if got.Name != n {
				t.Errorf("%s %q: name arrived as %q", pr.name, n, got.Name)
			}
			if got.IDOrPassword != want.IDOrPassword {
				t.Errorf("%s %q: credential arrived as %q, want %q", pr.name, n, got.IDOrPassword, want.IDOrPassword)
			}
			if got.Address != want.Address || got.Port != want.Port {
				t.Errorf("%s %q: endpoint = %s:%d, want %s:%d", pr.name, n, got.Address, got.Port, want.Address, want.Port)
			}
			if got.Protocol != want.Protocol {
				t.Errorf("%s %q: protocol = %q", pr.name, n, got.Protocol)
			}
			for _, f := range []struct {
				label     string
				got, want string
			}{
				{"path", got.Path, want.Path},
				{"host", got.Host, want.Host},
				{"serverName", got.ServerName, want.ServerName},
				{"fingerprint", got.Fingerprint, want.Fingerprint},
				{"alpn", got.ALPN, want.ALPN},
				{"method", got.Method, want.Method},
				{"flow", got.Flow, want.Flow},
				{"network", got.Network, want.Network},
				{"realityPubKey", got.RealityPubKey, want.RealityPubKey},
				{"realitySpiderX", got.RealitySpiderX, want.RealitySpiderX},
				{"realityShortID", got.RealityShortID, want.RealityShortID},
			} {
				// Only fields the source profile actually carried: a
				// parser may fill a sensible default for something the
				// link format has no notion of (socks has no transport,
				// so it arrives as "tcp").
				if f.want != "" && f.got != f.want {
					t.Errorf("%s %q: %s = %q, want %q", pr.name, n, f.label, f.got, f.want)
				}
			}
			if got.RealityEnabled != want.RealityEnabled {
				t.Errorf("%s %q: reality = %v, want %v", pr.name, n, got.RealityEnabled, want.RealityEnabled)
			}
		}
	}
}

func TestShareLinkRefusals(t *testing.T) {
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 8))
	if _, err := app.BuildShareLink("missing"); err == nil {
		t.Fatalf("missing id: want error")
	}
	raw, err := store.SaveProfile(Profile{
		Name: "raw", Address: "h", Port: 1, Protocol: "vless",
		IDOrPassword: "x", RawOutbound: `{"type":"vless"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.BuildShareLink(raw.ID); err == nil ||
		!strings.Contains(err.Error(), "raw JSON") {
		t.Fatalf("raw profile: want raw-JSON error, got %v", err)
	}
	hyst, err := store.SaveProfile(Profile{
		Name: "h", Address: "h", Port: 1, Protocol: "hysteria2",
		IDOrPassword: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.BuildShareLink(hyst.ID); err == nil ||
		!strings.Contains(err.Error(), "no share-link format") {
		t.Fatalf("hysteria2: want no-format error, got %v", err)
	}
}
