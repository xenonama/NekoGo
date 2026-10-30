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
