package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateFingerprintValues: every fingerprint both vendored cores
// accept passes (case-insensitively); engine-specific or garbage
// values fail with an actionable error.
func TestValidateFingerprintValues(t *testing.T) {
	mk := func(fp, preset string) Profile {
		return Profile{Name: "x", Core: CoreSingBox, Protocol: "vless",
			Address: "example.com", Port: 443, TLS: true,
			Fingerprint: fp, TLSFingerprint: preset}
	}
	for _, fp := range []string{
		"chrome", "firefox", "safari", "ios", "android",
		"edge", "360", "qq", "random", "randomized", "Chrome", "FIREFOX",
	} {
		if err := validateProfileForSave(mk(fp, "")); err != nil {
			t.Fatalf("fingerprint %q rejected: %v", fp, err)
		}
		if err := validateProfileForSave(mk("", fp)); err != nil {
			t.Fatalf("preset %q rejected: %v", fp, err)
		}
	}
	for _, fp := range []string{"nope", "chrome_psk", "unsafe", "randomizednoalpn", "helloChrome_120"} {
		if err := validateProfileForSave(mk(fp, "")); err == nil {
			t.Fatalf("fingerprint %q saved without error, want failure", fp)
		} else if !strings.Contains(strings.ToLower(err.Error()), "fingerprint") {
			t.Fatalf("fingerprint %q error = %q, want it to name the fingerprint", fp, err)
		}
	}
}

// TestValidateFingerprintSkipsNonTLS: the fields are ignored by both
// builders without TLS, so they must not fail a save.
func TestValidateFingerprintSkipsNonTLS(t *testing.T) {
	p := Profile{Name: "x", Core: CoreSingBox, Protocol: "shadowsocks",
		Address: "example.com", Port: 8388, IDOrPassword: "pw",
		Method: "aes-256-gcm", Fingerprint: "garbage-value"}
	if err := validateProfileForSave(p); err != nil {
		t.Fatalf("non-TLS profile with junk fingerprint rejected: %v", err)
	}
}

// TestProfileFingerprintNormalization: mixed-case values render
// lowercase (both cores match case-sensitively); preset wins.
func TestProfileFingerprintNormalization(t *testing.T) {
	if got := profileFingerprint(&Profile{Fingerprint: "Chrome"}); got != "chrome" {
		t.Fatalf("legacy = %q, want chrome", got)
	}
	if got := profileFingerprint(&Profile{Fingerprint: "chrome", TLSFingerprint: "Firefox"}); got != "firefox" {
		t.Fatalf("preset = %q, want firefox", got)
	}
	if got := effectiveShareFP(&Profile{TLSFingerprint: "QQ"}); got != "qq" {
		t.Fatalf("share fp = %q, want qq", got)
	}
	if got := effectiveShareFP(&Profile{}); got != "" {
		t.Fatalf("unset share fp = %q, want omitted (empty)", got)
	}
}

// TestDedupeTLSFingerprint: profiles differing only in the preset must
// not share a dedupe key (the rendered TLS differs).
func TestDedupeTLSFingerprint(t *testing.T) {
	a := &Profile{Protocol: "vless", Address: "example.com", Port: 443, IDOrPassword: "u", Fingerprint: "chrome"}
	b := *a
	b.Fingerprint, b.TLSFingerprint = "", "firefox"
	if dedupeKey(a) == dedupeKey(&b) {
		t.Fatal("dedupe keys equal despite different fingerprint preset, want distinct")
	}
}

// TestBuildersRenderLowercaseFP: a mixed-case stored fingerprint
// renders lowercase on both cores.
func TestBuildersRenderLowercaseFP(t *testing.T) {
	p := &Profile{Protocol: "trojan", Address: "example.com", Port: 443,
		IDOrPassword: "pw", Fingerprint: "FIREFOX"}
	raw, err := BuildSingBoxConfig(p, Settings{MixedPort: 2080})
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
	if err := json.Unmarshal([]byte(raw), &sdoc); err != nil {
		t.Fatal(err)
	}
	if sdoc.Outbounds[0].TLS == nil || sdoc.Outbounds[0].TLS.Utls == nil ||
		sdoc.Outbounds[0].TLS.Utls.Fingerprint != "firefox" {
		t.Fatalf("sing-box fp = %+v, want firefox", sdoc.Outbounds[0].TLS)
	}
	xraw, err := BuildXrayConfig(p, Settings{MixedPort: 2080}, "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	var xdoc struct {
		Outbounds []struct {
			StreamSettings *struct {
				TLSSettings *struct {
					Fingerprint string `json:"fingerprint"`
				} `json:"tlsSettings"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(xraw), &xdoc); err != nil {
		t.Fatal(err)
	}
	if xdoc.Outbounds[0].StreamSettings == nil || xdoc.Outbounds[0].StreamSettings.TLSSettings == nil ||
		xdoc.Outbounds[0].StreamSettings.TLSSettings.Fingerprint != "firefox" {
		t.Fatalf("xray fp = %+v, want firefox", xdoc.Outbounds[0].StreamSettings)
	}
}

// TestBuildShareLinkRejectsBadFingerprint: links must never carry a
// fingerprint the cores reject, and vmess links must not silently
// drop mux/fragment.
func TestBuildShareLinkRejectsBadFingerprint(t *testing.T) {
	app, saved := shareTestApp(t, Profile{
		Name: "badfp", Address: "example.com", Port: 443,
		Protocol: "vless", IDOrPassword: "u", TLS: true, Fingerprint: "nope",
	})
	if _, err := app.BuildShareLink(saved.ID); err == nil {
		t.Fatal("BuildShareLink with bad fingerprint = nil, want error")
	}
	app2, vmux := shareTestApp(t, Profile{
		Name: "vmux", Address: "example.com", Port: 443,
		Protocol: "vmess", IDOrPassword: "u", MuxEnabled: true,
	})
	if _, err := app2.BuildShareLink(vmux.ID); err == nil {
		t.Fatal("BuildShareLink vmess+mux = nil, want error")
	}
	app3, vfrag := shareTestApp(t, Profile{
		Name: "vfrag", Address: "example.com", Port: 443,
		Protocol: "vmess", IDOrPassword: "u", TLSFragment: "tlshello",
	})
	if _, err := app3.BuildShareLink(vfrag.ID); err == nil {
		t.Fatal("BuildShareLink vmess+fragment = nil, want error")
	}
}

// TestFragmentPacketsRangeRenders: an N-M packets mode renders the
// xray finalmask packets verbatim and enables the sing-box flag.
func TestFragmentPacketsRangeRenders(t *testing.T) {
	p := &Profile{Protocol: "vless", Address: "example.com", Port: 443,
		IDOrPassword: "u", TLS: true, TLSFragment: "1-5",
		TLSFragmentLen: "100-200", TLSFragmentInt: 15}
	xraw, err := BuildXrayConfig(p, Settings{MixedPort: 2080}, "127.0.0.1:10085")
	if err != nil {
		t.Fatalf("BuildXrayConfig: %v", err)
	}
	var xdoc struct {
		Outbounds []struct {
			StreamSettings *struct {
				Finalmask map[string][]struct {
					Type     string `json:"type"`
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
	if len(fm) != 1 || fm[0].Settings.Packets != "1-5" ||
		fm[0].Settings.Length.From != 100 || fm[0].Settings.Length.To != 200 ||
		fm[0].Settings.Delay.To != 15 {
		t.Fatalf("finalmask = %+v, want 1-5/100-200/delay-to-15", fm)
	}
	sraw, err := BuildSingBoxConfig(p, Settings{MixedPort: 2080})
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
		t.Fatal("sing-box fragment flag not set for 1-5 mode")
	}
}
