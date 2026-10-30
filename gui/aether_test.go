package main

// Tests for the PattNG Aether port: share-link round-trips (the exact
// shapes PattNG's AetherFmt writes), the PattNG-format endpoint and
// range readers, normalization rejections, the CLI argument builder and
// hand-written command reading.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseAetherLinkBasic: a one-hop WireGuard link in PattNG's shape.
func TestParseAetherLinkBasic(t *testing.T) {
	link := "aether://203.0.113.7:443?protocol=wg&scan=balanced&ip=v4#My%20Aether"
	p, err := parseAetherLink(link)
	if err != nil {
		t.Fatalf("parseAetherLink: %v", err)
	}
	if p.Protocol != "aether" || p.Core != CoreSingBox {
		t.Fatalf("protocol/core = %q/%v", p.Protocol, p.Core)
	}
	if p.Name != "My Aether" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Address != "203.0.113.7" || p.Port != 443 {
		t.Fatalf("endpoint = %s:%d", p.Address, p.Port)
	}
	if p.Aether.Protocol != "wg" {
		t.Fatalf("aether protocol = %q", p.Aether.Protocol)
	}
	if p.Aether.ScanMode != "balanced" || p.Aether.IPVersion != "v4" {
		t.Fatalf("scan/ip = %q/%q", p.Aether.ScanMode, p.Aether.IPVersion)
	}
}

// TestParseAetherLinkDefaults: fields PattNG leaves out follow the
// defaults (WireGuard, h3, balanced scan, automatic obfuscation).
func TestParseAetherLinkDefaults(t *testing.T) {
	link := "aether://[2001:db8:1:2::3]:443?protocol=masque#A"
	p, err := parseAetherLink(link)
	if err != nil {
		t.Fatalf("parseAetherLink: %v", err)
	}
	if p.Address != "2001:db8:1:2::3" || p.Port != 443 {
		t.Fatalf("endpoint = %s:%d", p.Address, p.Port)
	}
	if p.Aether.Transport != "h3" || p.Aether.Obfuscation != "auto" {
		t.Fatalf("transport/obfuscation = %q/%q", p.Aether.Transport, p.Aether.Obfuscation)
	}
}

// TestAetherLinkRoundTripMasque: a full MASQUE link round-trips
// through parse → link with values in place.
func TestAetherLinkRoundTripMasque(t *testing.T) {
	link := "aether://203.0.113.9:2053?protocol=masque&transport=h2&fragment=1&fragment_size=1-40" +
		"&fragment_delay=5-50&ech=1&ech_dns=https%3A%2F%2F1.1.1.1%2Fdns-query&ech_domain=example.net" +
		"&fingerprint=firefox&noize=light&ip=both&dns=1.1.1.1%2C8.8.8.8&exit_loc=DE%2C%21NL" +
		"&psiphon=chain&psiphon_mode=cdn&region=DE&psiphon_bundled=0#Masque%20Roundtrip"
	p, err := parseAetherLink(link)
	if err != nil {
		t.Fatalf("parseAetherLink: %v", err)
	}
	if p.Aether.ScanMode != "balanced" || p.Aether.Transport != "h2" || !p.Aether.Fragment || !p.Aether.Ech {
		t.Fatalf("masque settings = %+v", p.Aether)
	}
	if p.Aether.FragmentSz != "1-40" || p.Aether.FragmentDl != "5-50" {
		t.Fatalf("fragment = %q/%q", p.Aether.FragmentSz, p.Aether.FragmentDl)
	}
	if p.Aether.EchDNS != "https://1.1.1.1/dns-query" || p.Aether.EchDomain != "example.net" {
		t.Fatalf("ech = %q/%q", p.Aether.EchDNS, p.Aether.EchDomain)
	}
	if p.Aether.Fingerprint != "firefox" || p.Aether.Obfuscation != "light" || p.Aether.IPVersion != "both" {
		t.Fatalf("fp/noize/ip = %q/%q/%q", p.Aether.Fingerprint, p.Aether.Obfuscation, p.Aether.IPVersion)
	}
	if p.Aether.DNS != "1.1.1.1,8.8.8.8" || p.Aether.ExitLoc != "DE,!NL" {
		t.Fatalf("dns/exit = %q/%q", p.Aether.DNS, p.Aether.ExitLoc)
	}
	if p.Aether.Psiphon != "chain" || p.Aether.PsiphonMode != "cdn" || p.Aether.PsiphonRegn != "DE" {
		t.Fatalf("psiphon = %+v", p.Aether)
	}
	if p.Aether.PsiphonBundledList == nil || *p.Aether.PsiphonBundledList {
		t.Fatalf("psiphonBundled = %v, want false", p.Aether.PsiphonBundledList)
	}
	out, err := buildAetherLink(p)
	if err != nil {
		t.Fatalf("buildAetherLink: %v", err)
	}
	p2, err := parseAetherLink(out)
	if err != nil {
		t.Fatalf("re-parse %q: %v", out, err)
	}
	// PattNG's AetherFmt.toUri carries no obfuscation word on MASQUE
	// over HTTP/2 (the core takes none there), so the utterance drops
	// on the round trip and the profile follows the default again: the
	// remisslos settings compare except Obfuscation.
	want := p.Aether
	want.Obfuscation = "auto"
	if !aetherSettingsEqual(p2.Aether, want) {
		t.Fatalf("aether settings round-trip mismatch:\n want: %+v\n  got: %+v", want, p2.Aether)
	}
	if p2.Address != p.Address || p2.Port != p.Port {
		t.Fatalf("endpoint mismatch %s:%d vs %s:%d", p.Address, p.Port, p2.Address, p2.Port)
	}
	// Over HTTP/3 the obfuscation word survives the round trip.
	h3 := *p
	h3.Aether.Transport = "h3"
	outh3, err := buildAetherLink(&h3)
	if err != nil {
		t.Fatalf("buildAetherLink h3: %v", err)
	}
	p3, err := parseAetherLink(outh3)
	if err != nil {
		t.Fatalf("re-parse h3: %v", err)
	}
	if p3.Aether.Obfuscation != "light" || p3.Aether.Transport != "h3" {
		t.Fatalf("h3 obfuscation lost: %+v (%s)", p3.Aether, out)
	}
}

// TestAetherLinkRoundTripTwoHops: gool outer/inner hops round-trip and
// an inner hop sharing the outer's address is dropped (distinct hops).
func TestAetherLinkRoundTripTwoHops(t *testing.T) {
	link := "aether://?protocol=gool&outer=162.159.192.1%3A2408&inner=188.114.96.1%3A894#GOOL"
	p, err := parseAetherLink(link)
	if err != nil {
		t.Fatalf("parseAetherLink: %v", err)
	}
	if p.Address != "" || p.Port != 0 {
		t.Fatalf("two-hop profile carries an endpoint: %s:%d", p.Address, p.Port)
	}
	if p.Aether.WiwOuter != "162.159.192.1:2408" || p.Aether.WiwInner != "188.114.96.1:894" {
		t.Fatalf("hops = %q/%q", p.Aether.WiwOuter, p.Aether.WiwInner)
	}
	out, err := buildAetherLink(p)
	if err != nil {
		t.Fatalf("buildAetherLink: %v", err)
	}
	p2, err := parseAetherLink(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if p2.Aether.WiwOuter != p.Aether.WiwOuter || p2.Aether.WiwInner != p.Aether.WiwInner {
		t.Fatalf("hop round-trip: %q/%q -> %q/%q", p.Aether.WiwOuter, p.Aether.WiwInner, p2.Aether.WiwOuter, p2.Aether.WiwInner)
	}

	// Distinct-hops protocol: a same-address inner is refused on save.
	shared := *p
	if err := aetherSaveValidate(&shared); err != nil {
		// endpoints differ here, so this must pass
		t.Fatalf("normalizeAether: %v", err)
	}
	same := *p
	same.Aether.WiwInner = p.Aether.WiwOuter // same address now
	if err := aetherSaveValidate(&same); err == nil {
		t.Fatal("distinct-hops same-address accepted, want error")
	}
}

// TestAetherEndpointParse: canonicalization and rejections, the
// AetherEndpoint shapes.
func TestAetherEndpointParse(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"203.0.113.1:443", "203.0.113.1", 443, true},
		{"[2001:DB8::1]:8080", "2001:db8::1", 8080, true},
		{"203.0.113.01:443", "", 0, false}, // leading zero
		{"1.2.3:443", "", 0, false},
		{"1.2.3.4:99999", "", 0, false},
		{"example.invalid:443", "", 0, false}, // only addresses
		{"203.0.113.1", "", 0, false},         // no port separator
		{"[::1]:443", "::1", 443, true},
	}
	for _, c := range cases {
		ep, ok := aetherEndpointParse(c.in)
		if ok != c.ok || (ok && (ep.host != c.host || ep.port != c.port)) {
			t.Fatalf("aetherEndpointParse(%q) = %+v ok=%v, want host %q port %d ok=%v", c.in, ep, ok, c.host, c.port, c.ok)
		}
	}
}

// TestAetherRangeParse: the AetherRange reader.
func TestAetherRangeParse(t *testing.T) {
	if r, ok := aetherRangeParse("1-4096", aetherFragmentSizeBounds); !ok || r.String() != "1-4096" {
		t.Fatalf("range = %+v ok=%v", r, ok)
	}
	if r, ok := aetherRangeParse("7", aetherFragmentSizeBounds); !ok || r.String() != "7" {
		t.Fatalf("range = %+v ok=%v", r, ok)
	}
	if _, ok := aetherRangeParse("0", aetherFragmentSizeBounds); ok {
		t.Fatal("size 0 accepted")
	}
	if _, ok := aetherRangeParse("1-4097", aetherFragmentSizeBounds); ok {
		t.Fatal("size 4097 accepted")
	}
	if _, ok := aetherRangeParse("", aetherFragmentSizeBounds); ok {
		t.Fatal("empty accepted")
	}
	if _, ok := aetherRangeParse("10-100000", aetherFragmentDelayBounds); ok {
		t.Fatal("delay out of bounds accepted")
	}
}

// TestAetherValueChecks: isMasqueSNI/isECHDNS/isECHDomain.
func TestAetherValueChecks(t *testing.T) {
	if !aetherIsMasqueSNI("www.cloudflare.com") || !aetherIsMasqueSNI("example.com.") {
		t.Fatal("valid masque SNI refused")
	}
	if aetherIsMasqueSNI("203.0.113.1") || aetherIsMasqueSNI("[2001:db8::1]") {
		t.Fatal("IP accepted as masque SNI")
	}
	if aetherIsMasqueSNI("-bad-.example.com") {
		t.Fatal("label starting with '-' accepted")
	}
	if !aetherIsECHDNS("udp://1.1.1.1") || !aetherIsECHDNS("tcp://8.8.8.8:53") || !aetherIsECHDNS("udp://1.1.1.1") {
		t.Fatal("plain resolver refused")
	}
	if !aetherIsECHDNS("https://1.1.1.1/dns-query") || !aetherIsECHDNS("https://dns.example.com/dns-query@address=5.6.7.8") ||
		!aetherIsECHDNS("https://dns.example.com/dns-query@address=5.6.7.8@sni=dns.example.com") {
		t.Fatal("doh resolver refused")
	}
	if aetherIsECHDNS("https://dns.example.com/dns-query@address=5.6.7.8@address=9.9.9.9") {
		t.Fatal("duplicate @address accepted")
	}
	if aetherIsECHDNS("smtp://1.1.1.1") || aetherIsECHDNS("udp://example.com") || aetherIsECHDNS("--upstream") {
		t.Fatal("bad resolver accepted")
	}
	if !aetherIsECHDomain("cloudflare-ech.com") || aetherIsECHDomain("bad-.example.com") || aetherIsECHDomain("") {
		t.Fatal("ech domain check wrong")
	}
}

// TestNormalizeAetherRejections: every rejection AetherFmt.normalize
// gives, in the same chain order.
func TestNormalizeAetherRejections(t *testing.T) {
	mk := func(protocol string) *Profile {
		return &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Aether: aetherSettings{Protocol: protocol}}
	}
	// Tor reverse without MASQUE.
	p := mk("wg")
	p.Aether.Tor = "reverse"
	if err := aetherSaveValidate(p); err == nil || !strings.Contains(strings.ToLower(err.Error()), "masque") {
		t.Fatalf("tor-reverse wg: %v", err)
	}
	// Tor and Psiphon both around the tunnel.
	p = mk("masque")
	p.Aether.Tor, p.Aether.Psiphon = "reverse", "reverse"
	if err := aetherSaveValidate(p); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("tor+psiphon both reverse: %v", err)
	}
	// Nested pair is fine.
	p = mk("masque")
	p.Aether.Tor, p.Aether.Psiphon = "reverse", "chain"
	if err := aetherSaveValidate(p); err != nil {
		t.Fatalf("tor reverse + psiphon chain refused: %v", err)
	}
	// Psiphon only + tor only conflict (two insides).
	p = mk("masque")
	p.Aether.Tor, p.Aether.Psiphon = "only", "only"
	if err := aetherSaveValidate(p); err == nil {
		t.Fatal("tor-only + psiphon-only accepted")
	}
	// ECH DNS out of use is kept but not validated.
	p = mk("wg")
	p.Aether.Ech, p.Aether.EchDNS = true, "smtp://nope"
	if err := aetherSaveValidate(p); err != nil {
		t.Fatalf("ech over wg stored-but-unused refused: %v", err)
	}
	// ECH in use with a bad resolver is refused.
	p = mk("masque")
	p.Aether.Ech, p.Aether.EchDNS = true, "smtp://nope"
	if err := aetherSaveValidate(p); err == nil || !strings.Contains(err.Error(), "ech dns") {
		t.Fatalf("ech dns: %v", err)
	}
	// Own bridges need lines.
	p = mk("masque")
	p.Aether.Tor, p.Aether.TorBridges = "chain", "own"
	if err := aetherSaveValidate(p); err == nil || !strings.Contains(err.Error(), "bridge line") {
		t.Fatalf("own bridges empty: %v", err)
	}
	// Default values are cleaned to empty.
	p = mk("masque")
	p.Aether.MasqueSNI = aetherMasqueSNI
	p.Aether.EchDNS = aetherECHDNS
	p.Aether.EchDomain = aetherECHDomain
	if err := aetherSaveValidate(p); err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
	if p.Aether.MasqueSNI != "" || p.Aether.EchDNS != "" || p.Aether.EchDomain != "" {
		t.Fatalf("defaults not cleaned: %+v", p.Aether)
	}
}

// aetherSaveValidate runs save validation (SaveProfile's normalize).
func aetherSaveValidate(p *Profile) error { return normalizeAether(p) }

// TestValidateProfileForSaveAether: save-time gates.
func TestValidateProfileForSaveAether(t *testing.T) {
	// Two-hop aether needs no port.
	p := Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Aether: aetherSettings{Protocol: "gool"}}
	if err := validateProfileForSave(p); err != nil {
		t.Fatalf("two-hop aether port refused: %v", err)
	}
	// One-hop aether needs a port (thefallthrough of the skip).
	p.Port = 0
	p.Aether.Protocol = "wg"
	p.Address = "203.0.113.1"
	if err := validateProfileForSave(p); err == nil {
		t.Fatal("one-hop aether port 0 accepted")
	}
	// Built-in aether needs no raw outbound.
	p.Port = 443
	if err := validateProfileForSave(p); err != nil {
		t.Fatalf("aether without raw refused: %v", err)
	}
}

// TestBuildAetherArguments: argument shapes per profile class.
func TestBuildAetherArguments(t *testing.T) {
	// WireGuard one-hop.
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg", ScanMode: "balanced", IPVersion: "v4", Obfuscation: "auto"}}
	args := buildAetherArguments(p, 10819)
	argsjoin := strings.Join(args, " ")
	if !strings.Contains(argsjoin, "--bind 127.0.0.1:10819") ||
		!strings.Contains(argsjoin, "--protocol wg") ||
		!strings.Contains(argsjoin, "--scan balanced") ||
		!strings.Contains(argsjoin, "--peer 203.0.113.7:443") ||
		!strings.Contains(argsjoin, "--quick-reconnect") {
		t.Fatalf("wg args: %s", argsjoin)
	}
	if strings.Contains(argsjoin, "--noize") {
		t.Fatalf("automatic obfuscation named: %s", argsjoin)
	}

	// MASQUE over HTTP/2 with fragment+ECH+firefox fingerprint.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.9", Port: 2053,
		Aether: aetherSettings{
			Protocol: "masque", Transport: "h2", Fragment: true, FragmentSz: "1-40", FragmentDl: "5-50",
			Ech: true, EchDNS: "https://1.1.1.1/dns-query", EchDomain: "example.net",
			Fingerprint: "firefox", Obfuscation: "light", MasqueSNI: "stealth.example.com",
		}}
	args = buildAetherArguments(p, 10819)
	argsjoin = strings.Join(args, " ")
	for _, want := range []string{
		"--h2", "--fragment", "--fragment-size 1-40", "--fragment-delay 5-50",
		"--ech auto", "--ech-dns https://1.1.1.1/dns-query", "--ech-domain example.net",
		"--tls-ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:",
		"--disable-grease",
	} {
		if !strings.Contains(argsjoin, want) {
			t.Fatalf("masque args missing %q: %s", want, argsjoin)
		}
	}
	// MASQUE over HTTP/2 (this profile) takes no obfuscation word at
	// all; over HTTP/3 light keeps its place.
	if strings.Contains(argsjoin, "--noize") {
		t.Fatalf("h2 obfuscation named: %s", argsjoin)
	}
	p.Aether.Transport = "h3"
	argsjoin = strings.Join(buildAetherArguments(p, 10819), " ")
	if !strings.Contains(argsjoin, "--noize light") || strings.Contains(argsjoin, "--h2") {
		t.Fatalf("h3 obfuscation args: %s", argsjoin)
	}

	// MASQUE over HTTP/2 takes no obfuscation, and chrome names no
	// fingerprint option beyond the ciphers list (grease stays).
	p.Aether.Obfuscation = "auto"
	p.Aether.Fingerprint = "chrome"
	argsjoin = strings.Join(buildAetherArguments(p, 10819), " ")
	if strings.Contains(argsjoin, "--noize") || strings.Contains(argsjoin, "--disable-grease") {
		t.Fatalf("chrome/h2 args: %s", argsjoin)
	}

	// Tor inside the tunnel takes the next port; the app dials it.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg", Tor: "chain"}}
	args = buildAetherArguments(p, 10819)
	argsjoin = strings.Join(args, " ")
	if !strings.Contains(argsjoin, "--bind 127.0.0.1:10820") || !strings.Contains(argsjoin, "--tor-bind 127.0.0.1:10819") || !strings.Contains(argsjoin, "--tor") {
		t.Fatalf("tor chain args: %s", argsjoin)
	}
	if strings.Contains(argsjoin, "--tor-relays") || strings.Contains(argsjoin, "--tor-bridge") {
		t.Fatalf("auto bridging args: %s", argsjoin)
	}
	p.Aether.TorRelays = "only"
	argsjoin = strings.Join(buildAetherArguments(p, 10819), " ")
	if !strings.Contains(argsjoin, "--tor-relays only") {
		t.Fatalf("relay-only args: %s", argsjoin)
	}
	// Own bridges.
	p.Aether.TorBridges = "own"
	p.Aether.TorBridgeLines = "# comment\nBridge obfs4 a b c\n\nplain bridge\n"
	argsjoin = strings.Join(buildAetherArguments(p, 10819), " ")
	if strings.Contains(argsjoin, "--tor-relays") || !strings.Contains(argsjoin, "--tor-bridge obfs4 a b c") || !strings.Contains(argsjoin, "--tor-bridge plain bridge") {
		t.Fatalf("own bridges args: %s", argsjoin)
	}

	// Psiphon inside the tunnel: the app dials Psiphon on [port],
	// the tunnel takes the next one.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "masque", Psiphon: "chain", PsiphonMode: "auto"}}
	args = buildAetherArguments(p, 10819)
	argsjoin = strings.Join(args, " ")
	if !strings.Contains(argsjoin, "--bind 127.0.0.1:10820") || !strings.Contains(argsjoin, "--psiphon-bind 127.0.0.1:10819") {
		t.Fatalf("psiphon chain args: %s", argsjoin)
	}
	if strings.Contains(argsjoin, "--psiphon-server-entries") {
		t.Fatalf("bundled list sentinel emitted: %s", argsjoin)
	}

	// Psiphon reverse (around the tunnel): its own listener takes no
	// app port (the core reads the client's report) and forces HTTP/2
	// on MASQUE.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg-over-masque", Transport: "h3", Psiphon: "reverse", Obfuscation: "firewall", WiwOuter: "162.159.197.3:443"}}
	args = buildAetherArguments(p, 10819)
	argsjoin = strings.Join(args, " ")
	if !strings.Contains(argsjoin, "--psiphon-reverse") || strings.Contains(argsjoin, "--noize") ||
		!strings.Contains(argsjoin, "--psiphon-bind 127.0.0.1:0") {
		t.Fatalf("psiphon reverse args: %s", argsjoin)
	}
	// wg-over-masque names both hops, the outer as --peer the inner
	// as --gool-peer, and gool-classic is not used for it. The single
	// endpoint is unused for a two-hop protocol here.
	if !strings.Contains(argsjoin, "--peer 162.159.197.3:443") || strings.Contains(argsjoin, "--gool-classic") {
		t.Fatalf("wg-over-masque args: %s", argsjoin)
	}
	// Named hops only survive save-time normalization when they differ.
	p2x := *p
	p2x.Address = ""
	if err := aetherSaveValidate(&p2x); err != nil {
		t.Fatalf("wg-over-masque hops refused: %v", err)
	}

	// Classic gool keeps --gool-classic and the two-hop names.
	p = &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t",
		Aether: aetherSettings{Protocol: "gool", WiwOuter: "162.159.192.1:2408", WiwInner: "188.114.96.1:894"}}
	args = buildAetherArguments(p, 10819)
	argsjoin = strings.Join(args, " ")
	if !strings.Contains(argsjoin, "--gool-classic") || !strings.Contains(argsjoin, "--wiw-outer 162.159.192.1:2408") ||
		!strings.Contains(argsjoin, "--wiw-inner 188.114.96.1:894") || strings.Contains(argsjoin, "--peer") {
		t.Fatalf("gool args: %s", argsjoin)
	}

	// Tor-only and Psiphon-only strip the tunnel from the command.
	p.Aether.Tor = "only"
	argsjoin = strings.Join(buildAetherArguments(p, 10819), " ")
	if strings.Contains(argsjoin, "--protocol") || strings.Contains(argsjoin, "--tor-relays") {
		t.Fatalf("tor-only args: %s", argsjoin)
	}
}

// TestAetherCoreOf: the engine command the profile yields, incl. the
// hand-written command line.
func TestAetherCoreOf(t *testing.T) {
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg"}}
	args, listen, custom := aetherCoreOf(p, 10819)
	if custom || listen != 10819 || !strings.Contains(strings.Join(args, " "), "--bind 127.0.0.1:10819") {
		t.Fatalf("settings core: %v %d %v", args, listen, custom)
	}
	// Hand-written command: the program name is dropped, its listener
	// stands, quotes keep words together.
	p.Aether.Command = `aether --bind 10.0.0.1:2080 --verbose --dns "one two"`
	args, listen, custom = aetherCoreOf(p, 10819)
	if !custom || listen != 2080 {
		t.Fatalf("custom core: %v %d %v", args, listen, custom)
	}
	if !containsStr(args, "one two") {
		t.Fatalf("quoted word lost: %v", args)
	}
	// A command with no listener gets the engine port.
	p.Aether.Command = "aether --protocol mim"
	args, listen, custom = aetherCoreOf(p, 10819)
	if !custom || listen != 10819 || !strings.Contains(strings.Join(args, " "), "--bind 127.0.0.1:10819") {
		t.Fatalf("listenerless command: %v %d %v", args, listen, custom)
	}

	// Psiphon inside the tunnel: the dialed listener is Psiphon's,
	// which sits on the passed port.
	p.Aether.Command = ""
	p.Aether.Psiphon = "chain"
	args, listen, _ = aetherCoreOf(p, 10819)
	if listen != 10819 || !strings.Contains(strings.Join(args, " "), "--psiphon-bind 127.0.0.1:10819") {
		t.Fatalf("psiphon dial listen: %v %d", args, listen)
	}
}

// TestParseAetherLinkBridgeLines: bridges join on ';' in links.
func TestParseAetherLinkBridgeLines(t *testing.T) {
	link := "aether://?protocol=wg&tor=chain&tor_bridges=own&bridges=obfs4%20a%20b%3Bwebtunnel%20c%20d#B"
	p, err := parseAetherLink(link)
	if err != nil {
		t.Fatalf("parseAetherLink: %v", err)
	}
	if p.Aether.TorBridgeLines != "obfs4 a b\nwebtunnel c d" {
		t.Fatalf("bridge lines = %q", p.Aether.TorBridgeLines)
	}
}

// TestOutboundRendersAetherSocks: the builders render the engine SOCKS
// outbound (the live port, else the default engine port).
func TestOutboundRendersAetherSocks(t *testing.T) {
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg"}}
	defer clearAetherActivePort()
	clearAetherActivePort()
	out, err := mapProfileToSingBoxOutbound(p)
	if err != nil {
		t.Fatalf("singbox outbound: %v", err)
	}
	if out["type"] != "socks" || out["server"] != aetherLoopback || out["server_port"] != 10819 {
		t.Fatalf("singbox socks = %v", out)
	}
	setAetherActivePort(20001)
	out, err = mapProfileToSingBoxOutbound(p)
	if err != nil {
		t.Fatalf("singbox outbound: %v", err)
	}
	if out["server_port"] != 20001 {
		t.Fatalf("singbox socks live port = %v", out["server_port"])
	}
	xr, err := mapProfileToXrayOutbound(p)
	if err != nil {
		t.Fatalf("xray outbound: %v", err)
	}
	if xr["protocol"] != "socks" {
		t.Fatalf("xray protocol = %v", xr["protocol"])
	}
	servers, ok := xr["settings"].(map[string]any)["servers"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("xray socks servers = %v", xr["settings"])
	}
	s := servers[0].(map[string]any)
	if s["address"] != aetherLoopback || s["port"] != 20001 {
		t.Fatalf("xray socks server = %v", s)
	}
}

// TestAetherLinkNameDecoding: remarks round-trip.
func TestAetherLinkNameDecoding(t *testing.T) {
	link := "aether://203.0.113.1:443?protocol=wg#Give%20me%20a%E2%80%94name"
	p, err := parseAetherLink(link)
	if err != nil {
		t.Fatalf("parseAetherLink: %v", err)
	}
	if p.Name != "Give me a—name" {
		t.Fatalf("name = %q", p.Name)
	}
	out, err := buildAetherLink(p)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	p2, err := parseAetherLink(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if p2.Name != p.Name {
		t.Fatalf("name round-trip %q -> %q", p.Name, p2.Name)
	}
}

// containsArg reports whether [args] carries [want] as a whole element.
func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// TestAetherFragmentOffEmitsNoFragment: aether 2.3.0 defaults
// --fragment to ON, so an off profile must say --no-fragment.
func TestAetherFragmentOffEmitsNoFragment(t *testing.T) {
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.9", Port: 2053,
		Aether: aetherSettings{Protocol: "masque", Transport: "h2", Fragment: false}}
	args := buildAetherArguments(p, 10819)
	if !containsArg(args, "--no-fragment") {
		t.Fatalf("--no-fragment missing: %v", args)
	}
	if containsArg(args, "--fragment") {
		t.Fatalf("--fragment emitted while off: %v", args)
	}
	p.Aether.Fragment = true
	args = buildAetherArguments(p, 10819)
	if !containsArg(args, "--fragment") {
		t.Fatalf("--fragment missing while on: %v", args)
	}
	if containsArg(args, "--no-fragment") {
		t.Fatalf("--no-fragment emitted while on: %v", args)
	}
}

// TestAetherRejectsMasqueSni: aether 2.3.0 rejects --masque-sni, so
// the setting stays stored but is never forwarded.
func TestAetherRejectsMasqueSni(t *testing.T) {
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.9", Port: 2053,
		Aether: aetherSettings{Protocol: "masque", MasqueSNI: "stealth.example.com"}}
	args := buildAetherArguments(p, 10819)
	if containsArg(args, "--masque-sni") {
		t.Fatalf("--masque-sni emitted: %v", args)
	}
	for _, arg := range args {
		if arg == "stealth.example.com" {
			t.Fatalf("masque SNI value emitted: %v", args)
		}
	}
}

// TestAetherPsiphonServerEntriesSentinel: the PattNG "shipped-list"
// sentinel is never an aether.exe path, bundled list or fresh fetch.
func TestAetherPsiphonServerEntriesSentinel(t *testing.T) {
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "masque", Psiphon: "chain"}}
	args := buildAetherArguments(p, 10819)
	if containsArg(args, "--psiphon-server-entries") {
		t.Fatalf("bundled list sentinel emitted: %v", args)
	}
	fresh := false
	p.Aether.PsiphonBundledList = &fresh
	args = buildAetherArguments(p, 10819)
	if containsArg(args, "--psiphon-server-entries") {
		t.Fatalf("fetch fresh sentinel emitted: %v", args)
	}
}

// TestResolveAetherExeHonorsPath: the dedicated setting drives the
// manager, and a broken explicit path is an error rather than a silent
// fallback to the exe-adjacent binary.
func TestResolveAetherExeHonorsPath(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "aether.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &AetherManager{}
	got, err := m.resolveAetherExe(Settings{AetherPath: exe})
	if err != nil {
		t.Fatalf("resolveAetherExe: %v", err)
	}
	if got != exe {
		t.Fatalf("resolveAetherExe = %q, want %q", got, exe)
	}
	missing := filepath.Join(t.TempDir(), "missing.exe")
	if _, err := m.resolveAetherExe(Settings{AetherPath: missing}); err == nil {
		t.Fatal("resolveAetherExe with missing explicit path = nil, want error")
	}
}

// TestAetherPsiphonChainProfile: the Settings toggle arms psiphon=chain
// on a copy, so the engine dials Psiphon's servers through the tunnel
// while the app keeps dialling the Psiphon listener. The stored profile
// is never mutated, and an explicit mode (or a hand-written command)
// always wins.
func TestAetherPsiphonChainProfile(t *testing.T) {
	base := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "t", Address: "203.0.113.7", Port: 443,
		Aether: aetherSettings{Protocol: "wg"}}
	s := DefaultSettings()
	if got := aetherPsiphonChainProfile(base, s); got != base {
		t.Fatal("setting off must return the profile untouched")
	}
	if aetherProfilePsiphonChain(base) {
		t.Fatal("a plain profile reports a chain")
	}
	s.AetherPsiphon = true
	armed := aetherPsiphonChainProfile(base, s)
	if armed == base || !aetherProfilePsiphonChain(armed) {
		t.Fatal("setting on must arm psiphon=chain on a copy")
	}
	if base.Aether.Psiphon != "" {
		t.Fatalf("stored profile mutated: psiphon=%q", base.Aether.Psiphon)
	}
	args := buildAetherArguments(armed, 10819)
	argsjoin := strings.Join(args, " ")
	if !strings.Contains(argsjoin, "--psiphon") ||
		!strings.Contains(argsjoin, "--psiphon-bind 127.0.0.1:10819") {
		t.Fatalf("chain args: %s", argsjoin)
	}
	if !strings.Contains(argsjoin, "--bind 127.0.0.1:10820") {
		t.Fatalf("the tunnel keeps its own listener: %s", argsjoin)
	}
	if aetherListenerFlagOf(args) != "--psiphon-bind" {
		t.Fatalf("dialer flag = %q", aetherListenerFlagOf(args))
	}
	if port, ok := aetherPortAfter(args, "--psiphon-bind"); !ok || port != 10819 {
		t.Fatalf("dial port = %d ok=%v", port, ok)
	}

	own := *base
	own.Aether.Psiphon = "only"
	if got := aetherPsiphonChainProfile(&own, s); got != &own {
		t.Fatal("an explicit psiphon mode must win")
	}
	own = *base
	own.Aether.Command = "--bind 127.0.0.1:1"
	if got := aetherPsiphonChainProfile(&own, s); got != &own {
		t.Fatal("a hand-written command must win")
	}
	plain := &Profile{Protocol: "vless"}
	if got := aetherPsiphonChainProfile(plain, s); got != plain {
		t.Fatal("a non-aether profile must stay untouched")
	}
}

// TestBeginAetherProbeReusesLiveEngine: a ping of the connected profile
// probes through the live engine instead of spawning a second
// aether.exe, and the live port survives the restore.
func TestBeginAetherProbeReusesLiveEngine(t *testing.T) {
	p := &Profile{ID: "a1", Protocol: "aether", Aether: aetherSettings{Protocol: "wg"}}
	mgr := NewAetherManager(nil)
	mgr.mu.Lock()
	mgr.running = true
	mgr.mu.Unlock()
	m := &CoreManager{aetherMgr: mgr, running: true, aetherActive: true, activeID: "a1"}
	setAetherActivePort(55555)
	defer clearAetherActivePort()
	port, restore, err := m.beginAetherProbe(p, DefaultSettings())
	if err != nil {
		t.Fatalf("beginAetherProbe: %v", err)
	}
	if port != 55555 {
		t.Fatalf("probe port = %d, want the live engine port 55555", port)
	}
	if live, ok := currentAetherActivePort(); !ok || live != 55555 {
		t.Fatalf("live engine port replaced: port=%d ok=%v", live, ok)
	}
	restore()
	if live, ok := currentAetherActivePort(); !ok || live != 55555 {
		t.Fatalf("restore dropped the live port: port=%d ok=%v", live, ok)
	}
}

// TestBeginAetherProbeRefusesSecondTunnel: testing a different aether
// profile while a tunnel runs must not open a second one on the same
// WARP identity (the shared aether work-dir files) — the scan refuses
// for the same reason.
func TestBeginAetherProbeRefusesSecondTunnel(t *testing.T) {
	live := &Profile{ID: "a1", Protocol: "aether", Aether: aetherSettings{Protocol: "wg"}}
	other := &Profile{ID: "a2", Protocol: "aether", Aether: aetherSettings{Protocol: "wg"}}
	mgr := NewAetherManager(nil)
	mgr.mu.Lock()
	mgr.running = true
	mgr.mu.Unlock()
	m := &CoreManager{aetherMgr: mgr, running: true, aetherActive: true, activeID: live.ID}
	if _, _, err := m.beginAetherProbe(other, DefaultSettings()); err == nil || !strings.Contains(err.Error(), "disconnect the running Aether tunnel") {
		t.Fatalf("second-tunnel probe err = %v, want the disconnect refusal", err)
	}
}

// TestAetherProfilePinnedEnginePort: a probe's profile carries its own
// engine port, which both config builders render instead of the
// process-wide live port.
func TestAetherProfilePinnedEnginePort(t *testing.T) {
	setAetherActivePort(40123)
	defer clearAetherActivePort()
	p := &Profile{Protocol: "aether", Core: CoreSingBox, Name: "probe",
		Aether: aetherSettings{Protocol: "wg"}, AetherEnginePort: 40999}
	raw, err := BuildSingBoxConfig(p, DefaultSettings())
	if err != nil {
		t.Fatalf("BuildSingBoxConfig: %v", err)
	}
	if !strings.Contains(raw, "40999") || strings.Contains(raw, "40123") {
		t.Fatalf("sing-box outbound did not pin the probe port: %s", raw)
	}
	out, err := mapProfileToXrayOutbound(p)
	if err != nil {
		t.Fatalf("mapProfileToXrayOutbound: %v", err)
	}
	if view := fmt.Sprint(out); !strings.Contains(view, "40999") || strings.Contains(view, "40123") {
		t.Fatalf("xray outbound did not pin the probe port: %s", view)
	}
}

// TestWaitAetherPsiphonTunnelAborts: the chain readiness wait gives up
// at once on a dead engine or a racing Stop instead of burning the
// warmup budget.
func TestWaitAetherPsiphonTunnelAborts(t *testing.T) {
	mgr := NewAetherManager(nil)
	m := &CoreManager{epoch: 7}
	err := m.waitAetherPsiphonTunnel(1, time.Second, 7, mgr)
	if err == nil || !strings.Contains(err.Error(), "exited during connect") {
		t.Fatalf("dead engine = %v", err)
	}
	mgr.mu.Lock()
	mgr.running = true
	mgr.mu.Unlock()
	m.epoch = 8
	err = m.waitAetherPsiphonTunnel(1, time.Second, 7, mgr)
	if err == nil || !strings.Contains(err.Error(), "concurrent stop") {
		t.Fatalf("stale epoch = %v", err)
	}
}

// TestAetherReadinessFailureSparesNewerSpawn: a warmup failure stops
// only the child its own Start spawned. When a newer Start owns the
// manager by then, the failed probe must leave it alone — the rollback
// used to call Stop, which ignores the spawn token and killed the newer
// engine while clearing the port it published.
func TestAetherReadinessFailureSparesNewerSpawn(t *testing.T) {
	bin, pidFile := lingerHelperBinary(t)
	withAetherLingerExe(t, bin)
	m := NewPsiphonManager(make(chan LogEntry, 512))
	psiphonPort, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	upstreamPort, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	startErr := make(chan error, 1)
	go func() {
		_, serr := m.StartPsiphonOnly(DefaultSettings(), psiphonPort, upstreamPort, 1200*time.Millisecond)
		startErr <- serr
	}()
	// The helper never listens, so this Start sits in its warmup probe.
	pid := waitHelperPID(t, pidFile, 10*time.Second)
	t.Cleanup(func() { killProcessTree(pid) })

	// A newer spawn replaced this one while its probe was still running.
	newer := exec.Command(bin)
	newerJob := newProcJob()
	m.mu.Lock()
	m.token++
	m.cmd = newer
	m.job = newerJob
	m.running = true
	m.mu.Unlock()

	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("the warmup probe reported success for a non-listening engine")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the warmup failure did not return")
	}

	m.mu.Lock()
	alive, own := m.running, m.cmd == newer
	m.mu.Unlock()
	if !alive || !own {
		t.Fatal("the failed probe rolled back the newer spawn (Stop swallowed a stale token)")
	}
	newerJob.close()
}

// TestStopTokenSparesNewerSpawn: an aborted connect stops only the
// child it started; a token a newer Start replaced is a no-op.
func TestStopTokenSparesNewerSpawn(t *testing.T) {
	mgr := NewAetherManager(nil)
	mgr.mu.Lock()
	mgr.running = true
	mgr.token = 5
	mgr.mu.Unlock()
	if err := mgr.StopToken(4); err != nil {
		t.Fatalf("StopToken(stale): %v", err)
	}
	if !mgr.Running() {
		t.Fatal("a stale token stopped the newer spawn")
	}
	if err := mgr.StopToken(5); err != nil {
		t.Fatalf("StopToken(current): %v", err)
	}
	if mgr.Running() {
		t.Fatal("the current token did not stop the child")
	}
}
