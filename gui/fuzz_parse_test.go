package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzParseOneLink feeds arbitrary text to the share-link parser. The
// parser runs on every import/preview/subscription path, so malformed
// input must return an error — never panic. Seeds cover each scheme plus
// a few truncated/hostile shapes.
func FuzzParseOneLink(f *testing.F) {
	seeds := []string{
		"",
		"not-a-link",
		"vmess://",
		"vless://",
		"trojan://",
		"ss://",
		"socks://",
		"aether://",
		"vless://uuid@example.com:443?security=tls&type=ws&path=/x#name",
		"trojan://pw@example.com:443?security=reality&pbk=k&sid=ab",
		"ss://YWVzLTI1Ni1nY206cGFzcw@example.com:8388#ss",
		"socks://user:pass@127.0.0.1:1080#local",
		"aether://?protocol=gool&outer=1.1.1.1:443&inner=2.2.2.2:443#GOOL",
		"vmess://eyJ2IjoiMiIsInBzIjoibiIsImFkZCI6ImEuYiIsInBvcnQiOiI0NDMiLCJpZCI6ImlkIn0=",
		"vless://example.com:443",
		"ss://@@@@",
		"trojan://@:99999999",
		"vless://u@h:443?fragment=x,y,z&mux=abc",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, link string) {
		p, err := parseOneLink(link)
		if err != nil {
			if p != nil {
				t.Fatalf("parseOneLink(%q) returned both a profile and an error", link)
			}
			return
		}
		if p == nil {
			t.Fatalf("parseOneLink(%q) returned nil profile and nil error", link)
		}
		if p.Port < 0 || p.Port > 65535 {
			t.Fatalf("parseOneLink(%q) produced out-of-range port %d", link, p.Port)
		}
	})
}

// FuzzParseFragmentParam exercises the fragment= decoder, which accepts
// two part orders and numeric ranges. It must reject malformed triples
// with an error rather than panicking on an index or conversion.
func FuzzParseFragmentParam(f *testing.F) {
	for _, s := range []string{
		"", "tlshello,10-20,10", "10-20,10,tlshello", "1-3,20-40,5-10",
		"tlshello,,", ",,", "a,b,c", "tlshello,10-20,-5", "1-2,3-4,999999999999999999999",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, val string) {
		mode, length, interval, err := parseFragmentParam(val)
		if err != nil {
			return
		}
		if interval < 0 {
			t.Fatalf("parseFragmentParam(%q) negative interval %d", val, interval)
		}
		if strings.TrimSpace(mode) == "" || strings.TrimSpace(length) == "" {
			t.Fatalf("parseFragmentParam(%q) accepted empty mode/length (%q,%q)", val, mode, length)
		}
	})
}

// FuzzParseDNSAddr exercises the upstream DNS parser used by both the
// save-time validator and the config renderer. A value that parses must
// carry a server type; anything else must error, never panic.
func FuzzParseDNSAddr(f *testing.F) {
	for _, s := range []string{
		"", "local", "1.1.1.1", "8.8.8.8:53", "https://dns.google/dns-query",
		"tls://dns.google", "udp://[::1]:53", "quic://dns.adguard.com",
		"[::1]", "2001:db8::1", "https://", "://x", "tcp://host:99999",
		`{"type":"https","server":"dns.google"}`, `{}`, `{"type":"bogus"}`,
		"https://a b/", "tls://[::1", "udp://host:0",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, val string) {
		entry, err := parseDNSAddr(val)
		if err != nil {
			if entry != nil {
				t.Fatalf("parseDNSAddr(%q) returned both an entry and an error", val)
			}
			return
		}
		if entry == nil {
			t.Fatalf("parseDNSAddr(%q) returned nil entry and nil error", val)
		}
		if typ, _ := entry["type"].(string); strings.TrimSpace(typ) == "" {
			t.Fatalf("parseDNSAddr(%q) accepted entry with no type: %#v", val, entry)
		}
	})
}

// FuzzRewriteLegacyPaths exercises the recursive JSON walk used by the
// one-time SNI-NekoBox migration. Valid JSON must stay valid; invalid
// JSON must be returned verbatim. Neither path may panic on deep or
// hostile nesting.
func FuzzRewriteLegacyPaths(f *testing.F) {
	for _, s := range []string{
		"", "not json", "{}", "[]", `{"settings":{"corePath":"C:\\SNI-NekoBox\\core.exe"}}`,
		`{"name":"SNI-NekoBox"}`, `{"a":["sni-nekobox/x",{"b":"y/SNI-NekoBox/z"}]}`,
		`{"p":"plain SNI-NekoBox name"}`, `null`, `123`, `"SNI-NekoBox"`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out := rewriteLegacyPaths(raw)
		if json.Valid(raw) && !json.Valid(out) {
			t.Fatalf("rewriteLegacyPaths corrupted valid JSON: in=%q out=%q", raw, out)
		}
		if !json.Valid(raw) && !bytes.Equal(out, raw) {
			t.Fatalf("rewriteLegacyPaths altered invalid JSON: in=%q out=%q", raw, out)
		}
	})
}
