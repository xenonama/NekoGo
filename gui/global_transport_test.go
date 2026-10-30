package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// globalTestSettings returns settings with the global fragment +
// fingerprint overrides on (non-default values so an override is
// distinguishable from per-profile rendering).
func globalTestSettings() Settings {
	return Settings{
		MixedPort:          2080,
		FragmentEnabled:    true,
		FragmentMode:       "1-3",
		FragmentLength:     "30-40",
		FragmentInterval:   7,
		FingerprintEnabled: true,
		FingerprintValue:   "firefox",
	}
}

// TestGlobalFragmentRenders: a profile with no fragment of its own
// renders the global fragment on both cores when the switch is on.
func TestGlobalFragmentRenders(t *testing.T) {
	p := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", Network: "tcp", TLS: true, ServerName: "example.com"}
	sraw, err := BuildSingBoxConfig(p, globalTestSettings())
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	var sdoc struct {
		Outbounds []struct {
			TLS *struct {
				Fragment bool `json:"fragment"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(sraw), &sdoc); err != nil {
		t.Fatal(err)
	}
	if sdoc.Outbounds[0].TLS == nil || !sdoc.Outbounds[0].TLS.Fragment {
		t.Fatal("sing-box fragment flag not set by global override")
	}
	xraw, err := BuildXrayConfig(p, globalTestSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	var xdoc struct {
		Outbounds []struct {
			StreamSettings *struct {
				Finalmask map[string][]struct {
					Settings struct {
						Packets string `json:"packets"`
						Length  struct {
							From int `json:"from"`
							To   int `json:"to"`
						} `json:"length"`
						Delay struct {
							To int `json:"to"`
						} `json:"delay"`
					} `json:"settings"`
				} `json:"finalmask"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(xraw), &xdoc); err != nil {
		t.Fatal(err)
	}
	fm := xdoc.Outbounds[0].StreamSettings.Finalmask["tcp"]
	if len(fm) != 1 || fm[0].Settings.Packets != "1-3" ||
		fm[0].Settings.Length.From != 30 || fm[0].Settings.Length.To != 40 ||
		fm[0].Settings.Delay.To != 7 {
		t.Fatalf("finalmask = %+v, want global 1-3/30-40/7", fm)
	}
}

// TestGlobalFragmentOffKeepsPerProfile: with the switch off, a
// fragment-less profile renders no fragment anywhere.
func TestGlobalFragmentOffKeepsPerProfile(t *testing.T) {
	p := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", Network: "tcp", TLS: true, ServerName: "example.com"}
	s := Settings{MixedPort: 2080}
	sraw, err := BuildSingBoxConfig(p, s)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	if strings.Contains(sraw, "fragment") {
		t.Fatalf("sing-box config contains fragment with override off: %s", sraw)
	}
	xraw, err := BuildXrayConfig(p, s, "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	if strings.Contains(xraw, "finalmask") {
		t.Fatalf("xray config contains finalmask with override off: %s", xraw)
	}
}

// TestGlobalOverridesPerProfile: the global triple wins over the
// profile's own fragment fields (and the stored profile is untouched).
func TestGlobalOverridesPerProfile(t *testing.T) {
	p := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", Network: "tcp", TLS: true, ServerName: "example.com",
		TLSFragment: "tlshello", TLSFragmentLen: "10-20", TLSFragmentInt: 1}
	xraw, err := BuildXrayConfig(p, globalTestSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	if !strings.Contains(xraw, "1-3") || strings.Contains(xraw, "tlshello") {
		t.Fatalf("global did not override per-profile fragment: %s", xraw)
	}
	if p.TLSFragment != "tlshello" || p.TLSFragmentLen != "10-20" || p.TLSFragmentInt != 1 {
		t.Fatalf("stored profile mutated by render: %+v", p)
	}
}

// TestGlobalFingerprintOverrides: the global value wins over the
// profile fingerprint on both cores; off keeps per-profile.
func TestGlobalFingerprintOverrides(t *testing.T) {
	p := &Profile{Protocol: "trojan", Address: "example.com", Port: 443,
		IDOrPassword: "pw", Fingerprint: "chrome"}
	sraw, err := BuildSingBoxConfig(p, globalTestSettings())
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	var sdoc struct {
		Outbounds []struct {
			TLS *struct {
				Utls *struct {
					Fingerprint string `json:"fingerprint"`
				} `json:"utls"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(sraw), &sdoc); err != nil {
		t.Fatal(err)
	}
	if sdoc.Outbounds[0].TLS.Utls.Fingerprint != "firefox" {
		t.Fatalf("sing-box fp = %q, want global firefox", sdoc.Outbounds[0].TLS.Utls.Fingerprint)
	}
	xraw, err := BuildXrayConfig(p, globalTestSettings(), "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	if !strings.Contains(xraw, `"fingerprint": "firefox"`) {
		t.Fatalf("xray config missing global firefox: %s", xraw)
	}
	if p.Fingerprint != "chrome" || p.TLSFingerprint != "" {
		t.Fatalf("stored profile mutated by render: %+v", p)
	}
	// Switch off: per-profile chrome renders again.
	off := globalTestSettings()
	off.FingerprintEnabled = false
	sraw, err = BuildSingBoxConfig(p, off)
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	sdoc.Outbounds = nil
	if err := json.Unmarshal([]byte(sraw), &sdoc); err != nil {
		t.Fatal(err)
	}
	if sdoc.Outbounds[0].TLS.Utls.Fingerprint != "chrome" {
		t.Fatalf("sing-box fp = %q, want per-profile chrome with override off", sdoc.Outbounds[0].TLS.Utls.Fingerprint)
	}
}

// TestGlobalRawExempt: raw-JSON profiles bypass the builders, so the
// overrides must not touch (or break) them.
func TestGlobalRawExempt(t *testing.T) {
	p := &Profile{Name: "raw", RawOutbound: `{"tag":"proxy","type":"direct"}`}
	sraw, err := BuildSingBoxConfig(p, globalTestSettings())
	if err != nil {
		t.Fatalf("BuildSingBoxConfig raw: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(sraw), &doc); err != nil {
		t.Fatal(err)
	}
	outs, _ := doc["outbounds"].([]any)
	if len(outs) == 0 {
		t.Fatalf("raw config lost outbounds: %s", sraw)
	}
	first, _ := outs[0].(map[string]any)
	if first["type"] != "direct" {
		t.Fatalf("raw outbound altered by globals: %s", sraw)
	}
}

// TestSaveSettingsValidatesGlobals: bad global values fail the save
// honestly; good values persist normalized.
func TestSaveSettingsValidatesGlobals(t *testing.T) {
	app := discoveryTestApp(t)
	base := app.store.GetSettings()
	bad := base
	bad.FragmentEnabled = true
	bad.FragmentMode = "nope"
	if err := app.SaveSettings(bad); err == nil {
		t.Fatal("bad global fragment mode saved without error, want failure")
	} else if !strings.Contains(strings.ToLower(err.Error()), "fragment") {
		t.Fatalf("error = %q, want it to name fragment", err)
	}
	badfp := base
	badfp.FingerprintEnabled = true
	badfp.FingerprintValue = "nope"
	if err := app.SaveSettings(badfp); err == nil {
		t.Fatal("bad global fingerprint saved without error, want failure")
	}
	empty := base
	empty.FingerprintEnabled = true
	empty.FingerprintValue = ""
	if err := app.SaveSettings(empty); err == nil {
		t.Fatal("enabled global fingerprint with no value saved without error, want failure")
	}
	// Disabled globals with junk values still save (never rendered).
	off := base
	off.FragmentMode = "junk"
	off.FingerprintValue = "junk"
	if err := app.SaveSettings(off); err != nil {
		t.Fatalf("disabled globals with junk rejected: %v", err)
	}
	// Good values persist normalized to lowercase/trimmed.
	good := base
	good.FragmentEnabled = true
	good.FragmentMode = "TLSHELLO"
	good.FragmentLength = "10-20"
	good.FingerprintEnabled = true
	good.FingerprintValue = "Firefox"
	if err := app.SaveSettings(good); err != nil {
		t.Fatalf("good globals rejected: %v", err)
	}
	got := app.store.GetSettings()
	if got.FragmentMode != "tlshello" || got.FingerprintValue != "firefox" {
		t.Fatalf("globals not normalized: mode=%q fp=%q", got.FragmentMode, got.FingerprintValue)
	}
}
