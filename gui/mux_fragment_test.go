package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- fragment param parsing (Streisand/v2rayNG convention) ---

func TestParseFragmentParamOrders(t *testing.T) {
	// Packets-first (v2rayNG PR #2832).
	mode, length, iv, err := parseFragmentParam("tlshello,10-20,10")
	if err != nil || mode != "tlshello" || length != "10-20" || iv != 10 {
		t.Fatalf("packets-first = %q,%q,%d,%v; want tlshello,10-20,10,nil", mode, length, iv, err)
	}
	// Reversed (Streisand export): length,interval,packets.
	mode, length, iv, err = parseFragmentParam("20-40,5-10,tlshello")
	if err != nil || mode != "tlshello" || length != "20-40" || iv != 10 {
		t.Fatalf("reversed = %q,%q,%d,%v; want tlshello,20-40,10,nil", mode, length, iv, err)
	}
	// N-M packets mode with a single-int interval.
	mode, length, iv, err = parseFragmentParam("1-3,10-20,5")
	if err != nil || mode != "1-3" || length != "10-20" || iv != 5 {
		t.Fatalf("packets-range = %q,%q,%d,%v; want 1-3,10-20,5,nil", mode, length, iv, err)
	}
	for _, bad := range []string{"", "tlshello", "a,b", "nope,10-20,10", "tlshello,zzz,10", "tlshello,10-20,-1", "tlshello,10-20,1-3-5"} {
		if _, _, _, err := parseFragmentParam(bad); err == nil {
			t.Fatalf("parseFragmentParam(%q) = nil, want error", bad)
		}
	}
}

func TestParseMuxParam(t *testing.T) {
	if n, err := parseMuxParam("8"); err != nil || n != 8 {
		t.Fatalf("parseMuxParam(8) = %d,%v; want 8,nil", n, err)
	}
	for _, bad := range []string{"", "0", "-1", "x", "99999"} {
		if _, err := parseMuxParam(bad); err == nil {
			t.Fatalf("parseMuxParam(%q) = nil, want error", bad)
		}
	}
}

// --- builders ---

func TestSingBoxMultiplexBlock(t *testing.T) {
	p := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "uuid", Network: "tcp", MuxEnabled: true, MuxConcurrency: 16}
	raw, err := BuildSingBoxConfig(p, Settings{MixedPort: 2080})
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	var doc struct {
		Outbounds []struct {
			Multiplex *struct {
				Enabled        bool   `json:"enabled"`
				Protocol       string `json:"protocol"`
				MaxConnections int    `json:"max_connections"`
			} `json:"multiplex"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	m := doc.Outbounds[0].Multiplex
	if m == nil || !m.Enabled || m.Protocol != "h2mux" || m.MaxConnections != 16 {
		t.Fatalf("multiplex = %+v, want enabled/h2mux/16", m)
	}

	// Disabled mux renders no block.
	p.MuxEnabled = false
	raw, err = BuildSingBoxConfig(p, Settings{MixedPort: 2080})
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	doc.Outbounds = nil
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Outbounds[0].Multiplex != nil {
		t.Fatalf("multiplex = %+v, want absent when disabled", doc.Outbounds[0].Multiplex)
	}

	// Invalid protocol and mux+flow fail honestly.
	bad := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "uuid", MuxEnabled: true, MuxProtocol: "nope"}
	if _, err := BuildSingBoxConfig(bad, Settings{MixedPort: 2080}); err == nil {
		t.Fatal("bad mux protocol: want error, got nil")
	}
	flow := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "uuid", Flow: "xtls-rprx-vision", MuxEnabled: true}
	if _, err := BuildSingBoxConfig(flow, Settings{MixedPort: 2080}); err == nil {
		t.Fatal("mux+flow: want error, got nil")
	}
	// Bad fragment length fails on sing-box too (shared validator).
	frag := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "uuid", TLSFragment: "tlshello", TLSFragmentLen: "zzz"}
	if _, err := BuildSingBoxConfig(frag, Settings{MixedPort: 2080}); err == nil {
		t.Fatal("bad fragment length on sing-box: want error, got nil")
	}
}

func TestXrayMuxBlock(t *testing.T) {
	p := &Profile{Protocol: "trojan", Address: "example.com", Port: 443,
		IDOrPassword: "pw", MuxEnabled: true}
	raw, err := BuildXrayConfig(p, Settings{MixedPort: 2080}, "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	var doc struct {
		Outbounds []struct {
			Mux *struct {
				Enabled     bool `json:"enabled"`
				Concurrency int  `json:"concurrency"`
			} `json:"mux"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	m := doc.Outbounds[0].Mux
	if m == nil || !m.Enabled || m.Concurrency != defaultMuxConcurrency {
		t.Fatalf("mux = %+v, want enabled/%d", m, defaultMuxConcurrency)
	}
	// mux+flow fails on xray as well.
	flow := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "uuid", Flow: "xtls-rprx-vision", MuxEnabled: true}
	if _, err := BuildXrayConfig(flow, Settings{MixedPort: 2080}, "127.0.0.1:10085"); err == nil {
		t.Fatal("mux+flow on xray: want error, got nil")
	}
}

// --- share-link round-trip ---

func TestShareLinkFragmentMuxRoundTrip(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "fragmux", Address: "example.com", Port: 443,
		Protocol: "vless", IDOrPassword: "7163b2d5-xxxx",
		Network: "ws", TLS: true, ServerName: "example.com",
		Path: "/ws", Host: "example.com",
		TLSFragment: "tlshello", TLSFragmentLen: "10-20", TLSFragmentInt: 10,
		MuxEnabled: true, MuxProtocol: "smux", MuxConcurrency: 8,
	})
	got := roundTrip(t, app, saved.ID)
	if got.TLSFragment != "tlshello" || got.TLSFragmentLen != "10-20" || got.TLSFragmentInt != 10 {
		t.Fatalf("fragment = %q/%q/%d, want tlshello/10-20/10",
			got.TLSFragment, got.TLSFragmentLen, got.TLSFragmentInt)
	}
	if !got.MuxEnabled || got.MuxProtocol != "smux" || got.MuxConcurrency != 8 {
		t.Fatalf("mux = %v/%q/%d, want true/smux/8",
			got.MuxEnabled, got.MuxProtocol, got.MuxConcurrency)
	}
}

// TestShareLinkFragmentStreisandImport: a Streisand-order link
// (length,interval,packets + mux) imports into the canonical fields.
func TestShareLinkFragmentStreisandImport(t *testing.T) {
	link := "vless://7163b2d5-xxxx@example.com:443?encryption=none&security=tls&sni=example.com&type=ws&path=%2Fws&host=example.com&fragment=20-40%2C5-10%2Ctlshello&mux=8#streisand"
	got, err := parseOneLink(link)
	if err != nil {
		t.Fatalf("parseOneLink: %v", err)
	}
	if got.TLSFragment != "tlshello" || got.TLSFragmentLen != "20-40" || got.TLSFragmentInt != 10 {
		t.Fatalf("fragment = %q/%q/%d, want tlshello/20-40/10",
			got.TLSFragment, got.TLSFragmentLen, got.TLSFragmentInt)
	}
	if !got.MuxEnabled || got.MuxConcurrency != 8 {
		t.Fatalf("mux = %v/%d, want true/8", got.MuxEnabled, got.MuxConcurrency)
	}
}

// TestShareLinkRejectsBadFragmentMux: malformed values fail honestly,
// and muxproto-only links enable mux at the core default.
func TestShareLinkRejectsBadFragmentMux(t *testing.T) {
	for _, link := range []string{
		"vless://u@example.com:443?encryption=none&fragment=nope#x",
		"vless://u@example.com:443?encryption=none&mux=abc#x",
		"vless://u@example.com:443?encryption=none&muxproto=nope#x",
	} {
		if _, err := parseOneLink(link); err == nil {
			t.Fatalf("parseOneLink(%q) = nil, want error", link)
		}
	}
	got, err := parseOneLink("vless://u@example.com:443?encryption=none&muxproto=yamux#x")
	if err != nil {
		t.Fatalf("muxproto-only: %v", err)
	}
	if !got.MuxEnabled || got.MuxProtocol != "yamux" {
		t.Fatalf("muxproto-only = %v/%q, want true/yamux", got.MuxEnabled, got.MuxProtocol)
	}
}

// TestDedupeIncludesMuxFragment: identical links differing only in
// fragment/mux must not dedupe to the same key.
func TestDedupeIncludesMuxFragment(t *testing.T) {
	base := &Profile{Protocol: "vless", Address: "example.com", Port: 443, IDOrPassword: "u"}
	alt := *base
	alt.TLSFragment, alt.TLSFragmentLen, alt.TLSFragmentInt = "tlshello", "10-20", 5
	if dedupeKey(base) == dedupeKey(&alt) {
		t.Fatal("dedupe keys equal despite different fragment, want distinct")
	}
	alt2 := *base
	alt2.MuxEnabled, alt2.MuxConcurrency = true, 8
	if dedupeKey(base) == dedupeKey(&alt2) {
		t.Fatal("dedupe keys equal despite different mux, want distinct")
	}
}

// TestValidateProfileForSaveMuxFragment: save-time validation fails
// fast on bad mux/fragment (non-raw profiles only).
func TestValidateProfileForSaveMuxFragment(t *testing.T) {
	bad := Profile{Name: "x", Core: CoreSingBox, Protocol: "vless",
		Address: "example.com", Port: 443, MuxEnabled: true, MuxProtocol: "nope"}
	if err := validateProfileForSave(bad); err == nil {
		t.Fatal("bad mux protocol saved without error, want failure")
	} else if !strings.Contains(err.Error(), "mux") {
		t.Fatalf("error = %q, want it to name mux", err)
	}
	badf := Profile{Name: "x", Core: CoreSingBox, Protocol: "vless",
		Address: "example.com", Port: 443, TLSFragment: "tlshello", TLSFragmentLen: "zzz"}
	if err := validateProfileForSave(badf); err == nil {
		t.Fatal("bad fragment saved without error, want failure")
	}
	// Raw passthrough skips both checks (fields are ignored there).
	raw := Profile{Name: "x", RawOutbound: `{"tag":"proxy","type":"direct"}`,
		MuxEnabled: true, MuxProtocol: "nope"}
	if err := validateProfileForSave(raw); err != nil {
		t.Fatalf("raw profile with mux fields rejected: %v", err)
	}
}
