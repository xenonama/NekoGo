package main

// Aether (CluvexStudio/aether, via PattNG): the WARP-tunnel engine PattNG
// runs as its own process, dialed through a SOCKS outbound rendered into
// the main core's config. This file is the Go port of PattNG's
// com.v2ray.ang.fmt.AetherFmt — the share-link format (aether://), the
// option enums the core's CLI takes, and the profile shape — so PattNG
// share links import into NekoGo and round-trip through buildAetherLink.
// Process lifecycle lives in aether_manager.go, argument building in
// aether_core.go. Standard library only.

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Aether link and core-level defaults (AppConfig.kt).
const (
	// aetherURLPrefix is the share-link scheme PattNG emits.
	aetherURLPrefix = "aether://"
	// aetherLoopback is the loopback address the core always binds.
	aetherLoopback = "127.0.0.1"
	// Core defaults for ECH and the MASQUE SNI (AppConfig.kt 198-206).
	aetherECHDNS    = "udp://1.1.1.1"
	aetherECHDomain = "cloudflare-ech.com"
	aetherMasqueSNI = "www.cloudflare.com"
)

// aetherSettings is the NekoGo-side Aether profile config: every field a
// PattNG ProfileItem stores for an Aether profile, minus the ones with
// no NekoGo equivalent (PattNG's Xray-only finalMask/dialMode, chain
// exit-nodes and the dead aetherListenPort) which are dropped on import.
type aetherSettings struct {
	Protocol    string `json:"protocol,omitempty"`    // masque|wg|gool|mim|wg-over-masque
	Transport   string `json:"transport,omitempty"`   // h3|h2
	ScanMode    string `json:"scanMode,omitempty"`    // turbo|balanced|thorough|verified|ironclad
	Obfuscation string `json:"obfuscation,omitempty"` // auto|off|light|firewall|balanced|gfw|aggressive
	IPVersion   string `json:"ipVersion,omitempty"`   // v4|v6|both
	MasqueSNI   string `json:"masqueSni,omitempty"`   // empty = core default
	Fingerprint string `json:"fingerprint,omitempty"` // chrome|firefox|semi-python|go, empty = chrome
	DNS         string `json:"dns,omitempty"`         // comma-separated resolvers
	ExitLoc     string `json:"exitLoc,omitempty"`     // country allow/refuse rule e.g. DE,!NL
	WiwOuter    string `json:"wiwOuter,omitempty"`    // two-hop protocols: outer endpoint
	WiwInner    string `json:"wiwInner,omitempty"`    // two-hop protocols: inner endpoint
	Fragment    bool   `json:"fragment,omitempty"`
	FragmentSz  string `json:"fragmentSize,omitempty"`
	FragmentDl  string `json:"fragmentDelay,omitempty"`
	Ech         bool   `json:"ech,omitempty"`
	EchDNS      string `json:"echDns,omitempty"`
	EchDomain   string `json:"echDomain,omitempty"`
	Command     string `json:"command,omitempty"`     // hand-written core command line, overrides the rest
	Psiphon     string `json:"psiphon,omitempty"`     // off|chain|reverse|only
	PsiphonMode string `json:"psiphonMode,omitempty"` // auto|cdn|direct
	PsiphonCDN  string `json:"psiphonCdnIps,omitempty"`
	PsiphonSNI  string `json:"psiphonCdnSni,omitempty"`
	PsiphonSets string `json:"psiphonCdnSets,omitempty"`
	PsiphonRegn string `json:"psiphonRegion,omitempty"`
	// PsiphonBundledList: nil = bundled list (default), false = fetch fresh.
	PsiphonBundledList *bool  `json:"psiphonBundledList,omitempty"`
	Tor                string `json:"tor,omitempty"`        // off|chain|reverse|only
	TorBridges         string `json:"torBridges,omitempty"` // auto|first|never|own
	TorBridgeLines     string `json:"torBridgeLines,omitempty"`
	TorRelays          string `json:"torRelays,omitempty"` // auto|only|off
}

// --- option enums (com.v2ray.ang.enums.AetherOption) ---
//
// Every enum maps link/profile values to core words only where they
// differ; the mapping tables mirror PattNG exactly.

var aetherProtocols = []string{"masque", "wg", "gool", "mim", "wg-over-masque"}

// aetherProtocolDefault defaults an unknown link value to WireGuard,
// like AetherProtocol.fromString.
func aetherProtocolDefault(link string) string {
	for _, p := range aetherProtocols {
		if p == link {
			return p
		}
	}
	return "wg"
}

// aetherProtocolCore returns the word the core's --protocol takes.
func aetherProtocolCore(link string) string {
	if link == "wg-over-masque" {
		return "gool"
	}
	return link
}

// aetherIsOverMasque reports whether MASQUE carries the tunnel.
func aetherIsOverMasque(protocol string) bool {
	return protocol == "masque" || protocol == "mim" || protocol == "wg-over-masque"
}

// aetherIsTwoHops reports whether the tunnel is an outer+inner pair.
func aetherIsTwoHops(protocol string) bool {
	return protocol == "gool" || protocol == "mim" || protocol == "wg-over-masque"
}

// aetherNeedsDistinctHops reports whether the two hops must differ.
func aetherNeedsDistinctHops(protocol string) bool { return protocol == "gool" || protocol == "mim" }

// aetherEnumDefault looks [v] up in [opts], defaulting to [d].
func aetherEnumDefault(opts []string, v, d string) string {
	for _, o := range opts {
		if o == v {
			return o
		}
	}
	return d
}

// aetherEnumNonDefault returns [v] when it is a known value other than
// [off], else "" (PattNG stores the default as null).
func aetherEnumNonDefault(opts []string, v, off string) string {
	if v == off {
		return ""
	}
	r := aetherEnumDefault(opts, v, off)
	if r == off {
		return ""
	}
	return r
}

var (
	aetherTransports   = []string{"h3", "h2"}
	aetherScanModes    = []string{"turbo", "balanced", "thorough", "verified", "ironclad"}
	aetherObfuscations = []string{"auto", "off", "light", "firewall", "balanced", "gfw", "aggressive"}
	aetherIPVersions   = []string{"v4", "v6", "both"}
	aetherPsiphons     = []string{"off", "chain", "reverse", "only"}
	aetherPsiphonModes = []string{"auto", "cdn", "direct"}
	aetherTors         = []string{"off", "chain", "reverse", "only"}
	aetherTorBridges   = []string{"auto", "first", "never", "own"}
	aetherTorRelays    = []string{"auto", "only", "off"}
)

// aetherScanDefault resolves the link value: "stealth" is the name the
// verified mode had before aether 2.1.
func aetherScanDefault(link string) string {
	if link == "stealth" {
		return "verified"
	}
	return aetherEnumDefault(aetherScanModes, link, "balanced")
}

// aetherCdnSets are the built-in Psiphon CDN edge lists, in the order
// the scan tries them (AetherPsiphonCdnSet).
var aetherCdnSets = []string{
	"cloudflare", "fastly", "cloudfront", "psiphon-akamai", "psiphon-bunny",
	"vercel", "github", "curated-fronting", "legacy-android-overrides",
}

// aetherCdnSetParse returns the known set names in [text] in canonical order.
func aetherCdnSetParse(text string) []string {
	named := map[string]bool{}
	for _, part := range splitCommaSpacer(text) {
		named[part] = true
	}
	var out []string
	for _, s := range aetherCdnSets {
		if named[s] {
			out = append(out, s)
		}
	}
	return out
}

// aetherCdnSetJoin writes [sets] comma-separated in canonical order.
func aetherCdnSetJoin(sets string) string {
	return strings.Join(aetherCdnSetParse(sets), ",")
}

// splitCommaSpacer splits on commas and whitespace, dropping empties.
func splitCommaSpacer(text string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || isSpaceRune(r) }) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// aetherFingerprint is one TLS ClientHello fingerprint preset.
type aetherFingerprint struct {
	name    string
	ciphers string
	grease  bool
}

var aetherFingerprints = []aetherFingerprint{
	{"chrome", "ALL:!aPSK:!ECDSA+SHA1:!3DES", true},
	{"firefox",
		"ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:" +
			"ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-RSA-AES128-SHA:ECDHE-RSA-AES256-SHA:" +
			"AES128-GCM-SHA256:AES256-GCM-SHA384:AES128-SHA:AES256-SHA", false},
	{"semi-python",
		"ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:" +
			"ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:ECDHE-ECDSA-AES256-SHA:ECDHE-RSA-AES256-SHA:" +
			"ECDHE-ECDSA-AES128-SHA256:ECDHE-RSA-AES128-SHA256", false},
	{"go",
		"ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:" +
			"ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:ECDHE-ECDSA-AES128-SHA:ECDHE-RSA-AES128-SHA:" +
			"ECDHE-ECDSA-AES256-SHA:ECDHE-RSA-AES256-SHA", false},
}

// aetherFingerprintDefault maps an unknown name to chrome.
func aetherFingerprintDefault(name string) string {
	for _, f := range aetherFingerprints {
		if f.name == name {
			return f.name
		}
	}
	return "chrome"
}

// aetherFingerprintArguments returns the core's TLS options for the
// preset [name]: its TLS 1.2 suites, and GREASE left out where it
// sends none (AetherFingerprint.arguments).
func aetherFingerprintArguments(name string) []string {
	for _, f := range aetherFingerprints {
		if f.name == name {
			out := []string{"--tls-ciphers", f.ciphers}
			if !f.grease {
				out = append(out, "--disable-grease")
			}
			return out
		}
	}
	return []string{"--tls-ciphers", aetherFingerprints[0].ciphers}
}

// --- endpoint and range (com.v2ray.ang.dto.AetherEndpoint / AetherRange) ---

// aetherNormalizeAddress canonicalizes [address]: IPv4 octets are
// rewritten without leading zeros, IPv6 in canonical form; anything
// else fails. Mirrors AetherEndpoint.canonicalAddress.
func aetherNormalizeAddress(address string) (string, bool) {
	if strings.Contains(address, ":") {
		ip, err := netip.ParseAddr(strings.Trim(address, "[]"))
		if err != nil || ip.Is4() {
			return "", false
		}
		return ip.String(), true
	}
	return parseIPv4Canonical(address)
}

// parseIPv4Canonical reads four octets, each 0..255 without leading
// zeros beyond a lone zero, and returns the canonical "d.d.d.d".
func parseIPv4Canonical(address string) (string, bool) {
	parts := strings.Split(address, ".")
	if len(parts) != 4 {
		return "", false
	}
	out := make([]string, 0, 4)
	for _, p := range parts {
		if len(p) < 1 || len(p) > 3 {
			return "", false
		}
		if len(p) > 1 && p[0] == '0' {
			return "", false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n > 255 {
			return "", false
		}
		out = append(out, strconv.Itoa(n))
	}
	return strings.Join(out, "."), true
}

// aetherEndpoint is a host:port pair; aetherEndpointString brackets IPv6.
type aetherEndpoint struct {
	host string
	port int
}

func aetherEndpointString(e aetherEndpoint) string {
	host := e.host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d", host, e.port)
}

// aetherEndpointHost returns just the host of a parsed "host:port" pair.
func aetherEndpointHost(text string) string {
	if ep, ok := aetherEndpointParse(text); ok {
		return ep.host
	}
	return ""
}

// aetherEndpointOf builds an endpoint from separate host and port strings.
func aetherEndpointOf(host, port string) (aetherEndpoint, bool) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return aetherEndpoint{}, false
	}
	norm, ok := aetherNormalizeAddress(host)
	if !ok {
		return aetherEndpoint{}, false
	}
	n := parsePortInt(port)
	if n < 1 || n > 65535 {
		return aetherEndpoint{}, false
	}
	return aetherEndpoint{host: norm, port: n}, true
}

// aetherEndpointParse reads "host:port" (bracketed IPv6 allowed).
func aetherEndpointParse(value string) (aetherEndpoint, bool) {
	text := strings.TrimSpace(value)
	sep := strings.LastIndex(text, ":")
	if sep <= 0 {
		return aetherEndpoint{}, false
	}
	host := text[:sep]
	if strings.Contains(host, ":") && !(strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]")) {
		return aetherEndpoint{}, false
	}
	return aetherEndpointOf(host, text[sep+1:])
}

func parsePortInt(text string) int {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return n
}

// aetherRange is a fragment size/delay bound.
type aetherRange struct{ min, max int }

// bounds1to is the a min..a max value bound of one fragment setting.
type bounds1to struct{ min, max int }

var (
	aetherFragmentSizeBounds  = bounds1to{min: 1, max: 4096}
	aetherFragmentDelayBounds = bounds1to{min: 0, max: 1000}
)

func (r aetherRange) String() string {
	if r.min == r.max {
		return strconv.Itoa(r.min)
	}
	return fmt.Sprintf("%d-%d", r.min, r.max)
}

// aetherRangeParse reads "n" or "a-b", each within [b] bounds. Values
// are not reordered here (PattNG keeps the order written and sorts with
// min()/max() at parse time, which amounts to the same thing).
func aetherRangeParse(text string, b bounds1to) (aetherRange, bool) {
	if text == "" {
		return aetherRange{}, false
	}
	parts := strings.Split(text, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return aetherRange{}, false
	}
	var values []int
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < b.min || n > b.max {
			return aetherRange{}, false
		}
		values = append(values, n)
	}
	lo, hi := values[0], values[len(values)-1]
	if lo > hi {
		lo, hi = hi, lo
	}
	return aetherRange{min: lo, max: hi}, true
}

// aetherSettingsEqual compares two settings sets field-wise with the
// PsiphonBundledList pointer compared by value (json round both).
func aetherSettingsEqual(a, b aetherSettings) bool {
	aBytes, errA := json.Marshal(a)
	bBytes, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aBytes) == string(bBytes)
}

// --- share-link (AetherFmt.parse / toUri) ---

// parseAetherLink parses one aether:// share link exactly as PattNG's
// AetherFmt.parse does. Fields NekoGo cannot run (finalMask, dialMode,
// chain exit-nodes) are dropped; everything else round-trips through
// buildAetherLink.
func parseAetherLink(link string) (*Profile, error) {
	rest, rawFrag := splitLinkFragment(strings.TrimSpace(link))
	u, err := url.Parse(rest)
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	q := u.Query()
	pick := func(key string) string { return strings.TrimSpace(q.Get(key)) }

	protocol := aetherProtocolDefault(pick("protocol"))
	cfg := aetherSettings{
		Protocol:    protocol,
		Transport:   aetherEnumDefault(aetherTransports, pick("transport"), "h3"),
		ScanMode:    aetherScanDefault(pick("scan")),
		Obfuscation: aetherEnumDefault(aetherObfuscations, pick("noize"), "auto"),
		IPVersion:   aetherEnumDefault(aetherIPVersions, pick("ip"), "v4"),
		Fragment:    pick("fragment") == "1",
		Ech:         pick("ech") == "1",
		DNS:         pick("dns"),
		ExitLoc:     pick("exit_loc"),
		Psiphon:     aetherEnumNonDefault(aetherPsiphons, pick("psiphon"), "off"),
		PsiphonMode: aetherEnumDefault(aetherPsiphonModes, pick("psiphon_mode"), "auto"),
		PsiphonCDN:  pick("cdn_ips"),
		PsiphonSNI:  pick("cdn_sni"),
		PsiphonSets: pick("cdn_sets"),
		PsiphonRegn: pick("region"),
		Tor:         aetherEnumNonDefault(aetherTors, pick("tor"), "off"),
		TorBridges:  aetherEnumDefault(aetherTorBridges, pick("tor_bridges"), "auto"),
		TorRelays:   aetherEnumDefault(aetherTorRelays, pick("tor_relays"), "auto"),
	}
	if pick("psiphon_bundled") == "0" {
		f := false
		cfg.PsiphonBundledList = &f
	}
	if fp := pick("fingerprint"); fp != "" {
		// Only POST-CHROME preset names carry; an unknown one follows the
		// default (chrome), like "a name the core would not take".
		for _, f := range aetherFingerprints {
			if f.name == fp {
				cfg.Fingerprint = fp
				break
			}
		}
	}
	// over-MASQUE-only values; a name the core would not take is dropped.
	if aetherIsOverMasque(protocol) {
		if sn := pick("sni"); sn != "" && aetherIsMasqueSNI(sn) {
			cfg.MasqueSNI = sn
		}
		if pick("fragment_size") != "" || pick("fragment_delay") != "" {
			if r, ok := aetherRangeParse(pick("fragment_size"), aetherFragmentSizeBounds); ok {
				cfg.FragmentSz = r.String()
			}
			if r, ok := aetherRangeParse(pick("fragment_delay"), aetherFragmentDelayBounds); ok {
				cfg.FragmentDl = r.String()
			}
		}
		if cfg.Ech {
			if d := pick("ech_dns"); d != "" && aetherIsECHDNS(d) {
				cfg.EchDNS = d
			}
			if d := pick("ech_domain"); d != "" && aetherIsECHDomain(d) {
				cfg.EchDomain = d
			}
		}
	}
	// Bridge lines join on ';' in a link and '\n' in the profile.
	if pick("bridges") != "" {
		var parts []string
		for _, l := range strings.Split(pick("bridges"), ";") {
			if l = strings.TrimSpace(l); l != "" {
				parts = append(parts, l)
			}
		}
		if len(parts) > 0 {
			cfg.TorBridgeLines = strings.Join(parts, "\n")
		}
	}

	p := &Profile{
		Protocol: "aether",
		Core:     CoreSingBox,
		Name:     linkName(rawFrag, "Aether"),
		Aether:   cfg,
	}
	if aetherIsTwoHops(protocol) {
		if ep, ok := aetherEndpointParse(pick("outer")); ok {
			p.Aether.WiwOuter = aetherEndpointString(ep)
		}
		if ep, ok := aetherEndpointParse(pick("inner")); ok {
			// A named inner hop that shares the outer's address with a
			// distinct-hops protocol is left out (the profile then
			// follows the core's default), like PattNG.
			if !(aetherNeedsDistinctHops(protocol) && ep.host == aetherEndpointHost(p.Aether.WiwOuter)) {
				p.Aether.WiwInner = aetherEndpointString(ep)
			}
		}
	} else {
		if ep, ok := aetherEndpointOf(u.Hostname(), u.Port()); ok {
			p.Address = ep.host
			p.Port = ep.port
		}
	}
	return p, nil
}

// buildAetherLink renders the profile as an aether:// link in the exact
// field order PattNG's AetherFmt.toUri writes.
func buildAetherLink(p *Profile) (string, error) {
	if p == nil {
		return "", fmt.Errorf("no profile")
	}
	cfg := p.Aether
	protocol := aetherProtocolDefault(cfg.Protocol)
	type kv struct{ k, v string }
	q := []kv{}
	add := func(k, v string) { q = append(q, kv{k, v}) }
	add("protocol", protocol)
	add("scan", aetherScanDefault(cfg.ScanMode))
	transport := aetherEnumDefault(aetherTransports, cfg.Transport, "h3")
	tor := aetherEnumDefault(aetherTors, cfg.Tor, "off")
	psiphon := aetherEnumDefault(aetherPsiphons, cfg.Psiphon, "off")
	overHTTP2 := aetherIsOverMasque(protocol) &&
		(transport == "h2" || tor == "reverse" || psiphon == "reverse")
	// Automatic obfuscation is the core's choice per protocol; MASQUE
	// over HTTP/2 takes none at all.
	if ob := aetherEnumDefault(aetherObfuscations, cfg.Obfuscation, "auto"); ob != "auto" && !(aetherIsOverMasque(protocol) && overHTTP2) {
		add("noize", ob)
	}
	add("ip", aetherEnumDefault(aetherIPVersions, cfg.IPVersion, "v4"))
	if s := strings.TrimSpace(cfg.DNS); s != "" {
		add("dns", s)
	}
	if s := strings.TrimSpace(cfg.ExitLoc); s != "" {
		add("exit_loc", s)
	}
	if aetherIsOverMasque(protocol) {
		add("transport", transport)
		if sn := strings.TrimSpace(cfg.MasqueSNI); sn != "" && sn != aetherMasqueSNI && aetherIsMasqueSNI(sn) {
			add("sni", sn)
		}
		if cfg.Fragment {
			add("fragment", "1")
			if r, ok := aetherRangeParse(cfg.FragmentSz, aetherFragmentSizeBounds); ok {
				add("fragment_size", r.String())
			}
			if r, ok := aetherRangeParse(cfg.FragmentDl, aetherFragmentDelayBounds); ok {
				add("fragment_delay", r.String())
			}
		}
		// ECH only while a WARP tunnel is in use (not psiphon-only, not tor-only).
		warpUsed := psiphon != "only" && tor != "only"
		if cfg.Ech && warpUsed {
			add("ech", "1")
			if d := strings.TrimSpace(cfg.EchDNS); d != "" && aetherIsECHDNS(d) {
				add("ech_dns", d)
			}
			if d := strings.TrimSpace(cfg.EchDomain); d != "" && aetherIsECHDomain(d) {
				add("ech_domain", d)
			}
		}
		if fp := aetherFingerprintDefault(cfg.Fingerprint); fp != "chrome" {
			add("fingerprint", fp)
		}
	}
	if aetherIsTwoHops(protocol) {
		if ep, ok := aetherEndpointParse(cfg.WiwOuter); ok {
			add("outer", aetherEndpointString(ep))
		}
		if ep, ok := aetherEndpointParse(cfg.WiwInner); ok {
			add("inner", aetherEndpointString(ep))
		}
	}
	if tor != "off" {
		add("tor", tor)
		add("tor_bridges", aetherEnumDefault(aetherTorBridges, cfg.TorBridges, "auto"))
		add("tor_relays", aetherEnumDefault(aetherTorRelays, cfg.TorRelays, "auto"))
		if lines := aetherBridgeLines(cfg.TorBridgeLines); len(lines) > 0 {
			add("bridges", strings.Join(lines, ";"))
		}
	}
	if psiphon != "off" {
		add("psiphon", psiphon)
		add("psiphon_mode", aetherEnumDefault(aetherPsiphonModes, cfg.PsiphonMode, "auto"))
		if s := strings.TrimSpace(cfg.PsiphonCDN); s != "" {
			add("cdn_ips", s)
		}
		if s := strings.TrimSpace(cfg.PsiphonSNI); s != "" {
			add("cdn_sni", s)
		}
		if s := aetherCdnSetJoin(cfg.PsiphonSets); s != "" {
			add("cdn_sets", s)
		}
		if s := strings.TrimSpace(cfg.PsiphonRegn); s != "" {
			add("region", s)
		}
		if cfg.PsiphonBundledList != nil && !*cfg.PsiphonBundledList {
			add("psiphon_bundled", "0")
		}
	}

	var sb strings.Builder
	if !aetherIsTwoHops(protocol) {
		if ep, ok := aetherEndpointOf(p.Address, strconv.Itoa(p.Port)); ok {
			sb.WriteString(aetherEndpointString(ep))
		}
	}
	parts := make([]string, 0, len(q))
	for _, kv := range q {
		parts = append(parts, url.QueryEscape(kv.k)+"="+url.QueryEscape(kv.v))
	}
	sb.WriteString("?" + strings.Join(parts, "&"))
	sb.WriteString("#" + url.PathEscape(p.Name))
	return aetherURLPrefix + sb.String(), nil
}

// aetherBridgeLines reads bridge lines the way the core reads a bridge
// file (AetherFmt.bridgeLines): blanks and comments dropped, a leading
// "Bridge " keyword taken off.
func aetherBridgeLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "Bridge "), "bridge "))
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// --- value checks (isMasqueSni / isEchDns / isEchDomain / exit rule) ---

var aetherExitRule = regexp.MustCompile(`!?[A-Z]{2}(,[A-Z]{2})*`)
var aetherDomainLabel = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?$`)

// aetherIsECHDomain reports whether [value] is a domain whose HTTPS
// record can be asked for (AetherFmt.isEchDomain).
func aetherIsECHDomain(value string) bool {
	name := strings.TrimSuffix(value, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !aetherDomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// aetherIsMasqueSNI reports whether [value] is a server name the core's
// --masque-sni takes: a domain, never an IP address (AetherFmt.isMasqueSni).
func aetherIsMasqueSNI(value string) bool {
	name := strings.TrimSuffix(value, ".")
	return !strings.HasSuffix(name, ".") && !isNetipAddress(name) && aetherIsECHDomain(name)
}

// isNetipAddress mirrors CoreOutboundBuilder.isNetipAddress: a literal
// IPv4 or IPv6 address reads as one to Go's netip.
func isNetipAddress(value string) bool {
	ip, err := netip.ParseAddr(value)
	return err == nil && ip.IsValid()
}

// aetherIsECHDNS reports whether [value] names a resolver the core asks
// for the ECH key: udp:// or tcp:// IP[:53], or an https:// DoH URL
// with optional @address= and @sni= suffixes (AetherFmt.isEchDns).
func aetherIsECHDNS(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\"'") {
		return false
	}
	scheme, rest := "", value
	if i := strings.Index(value, "://"); i >= 0 {
		scheme = strings.ToLower(value[:i])
		rest = value[i+3:]
	}
	switch scheme {
	case "https":
		return aetherIsDohEndpoint(rest)
	case "udp", "tcp":
		rest = strings.TrimRight(rest, "/")
		if !isASCIIHost(rest) {
			return false
		}
		if strings.HasPrefix(rest, "[") && !strings.Contains(strings.SplitN(rest, "]", 2)[0], ":") {
			return false
		}
		_, ok1 := aetherEndpointParse(rest)
		_, ok2 := aetherEndpointOf(rest, "53")
		return ok1 || ok2
	}
	return false
}

// isASCIIHost checks the ASCII-only rule (Go/Kotlin also reject
// non-ASCII in command-line words).
func isASCIIHost(s string) bool {
	for _, r := range s {
		if r >= 0x80 {
			return false
		}
	}
	return true
}

// aetherIsDohEndpoint checks the URL plus @address=/@sni= suffixes,
// each at most once, in either order.
func aetherIsDohEndpoint(rest string) bool {
	pieces := strings.Split(rest, "@")
	// The URL part must name a host before any path/query/fragment.
	uhead := pieces[0]
	if i := strings.IndexAny(uhead, "/?#"); i >= 0 {
		uhead = uhead[:i]
	}
	if uhead == "" {
		return false
	}
	named := map[string]bool{}
	for _, piece := range pieces[1:] {
		eq := strings.Index(piece, "=")
		if eq < 0 {
			return false
		}
		name := strings.ToLower(piece[:eq])
		setting := piece[eq+1:]
		_, isAddress := aetherEndpointOf(setting, "443")
		var fits bool
		switch name {
		case "address":
			fits = isAddress || aetherIsECHDomain(setting)
		case "sni":
			fits = !isAddress && aetherIsECHDomain(setting)
		}
		if !fits || named[name] {
			return false
		}
		named[name] = true
	}
	return true
}

func isSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// --- command-line reading (AetherCore.ofCommand and the option
// helpers of AetherCoreManager) ---

// aetherWords splits a command line on whitespace, with single or
// double quotes keeping a word together (AetherCore.words).
func aetherWords(command string) []string {
	var words []string
	var word strings.Builder
	quote := '\x00'
	open := false
	for _, c := range command {
		switch {
		case quote != '\x00':
			if c == quote {
				quote = '\x00'
			} else {
				word.WriteRune(c)
			}
		case c == '"' || c == '\'':
			quote = c
			open = true
		case isSpaceRune(c):
			if open {
				words = append(words, word.String())
				word.Reset()
				open = false
			}
		default:
			word.WriteRune(c)
			open = true
		}
	}
	if open {
		words = append(words, word.String())
	}
	return words
}

var (
	aetherFlagBind        = "--bind"
	aetherFlagTorBind     = "--tor-bind"
	aetherFlagPsiphonBind = "--psiphon-bind"
)

// aetherPsiphonModeOf reads where Psiphon stands in [argv]; last flag wins.
func aetherPsiphonModeOf(argv []string) string {
	mode := "off"
	for _, w := range argv {
		switch w {
		case "--psiphon":
			mode = "chain"
		case "--psiphon-reverse":
			mode = "reverse"
		case "--psiphon-only":
			mode = "only"
		}
	}
	return mode
}

// aetherTorModeOf reads where Tor stands in [argv]; last flag wins.
func aetherTorModeOf(argv []string) string {
	mode := "off"
	for _, w := range argv {
		switch w {
		case "--tor":
			mode = "chain"
		case "--tor-reverse":
			mode = "reverse"
		case "--tor-only":
			mode = "only"
		}
	}
	return mode
}

// aetherListenerFlagOf names the listener the app dials: Psiphon's when
// it runs inside the tunnel, Tor's when Tor does, the core's else.
func aetherListenerFlagOf(argv []string) string {
	switch {
	case aetherPsiphonModeOf(argv) == "chain":
		return aetherFlagPsiphonBind
	case aetherTorModeOf(argv) == "chain":
		return aetherFlagTorBind
	}
	return aetherFlagBind
}

// aetherValueAfter returns the value after the LAST [flag] in [argv]
// (the last one is the one the core keeps).
func aetherValueAfter(argv []string, flag string) (string, bool) {
	for i := len(argv) - 2; i >= 0; i-- {
		if argv[i] == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// aetherPortAfter reads a port from the value after [flag].
func aetherPortAfter(argv []string, flag string) (int, bool) {
	v, ok := aetherValueAfter(argv, flag)
	if !ok {
		return 0, false
	}
	n := parsePortInt(v[strings.LastIndex(v, ":")+1:])
	if n == 0 {
		return 0, false
	}
	return n, true
}

// aetherWithoutOption drops [flag] and its value from [argv].
func aetherWithoutOption(argv []string, flag string) []string {
	var kept []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == flag && i+1 < len(argv) {
			i++
			continue
		}
		kept = append(kept, argv[i])
	}
	return kept
}

// aetherWithListener puts the listener [flag] names on loopback [port].
func aetherWithListener(argv []string, flag string, port int) []string {
	return append(aetherWithoutOption(argv, flag), flag, fmt.Sprintf("%s:%d", aetherLoopback, port))
}

// aetherDefaultCommandPort is the port a hand-written command that names
// no listener gets; at connect time the manager passes the engine port.
const aetherDefaultCommandPort = 10819

// aetherCoreOfCommand parses a hand-written command line
// (AetherCore.ofCommand): ok=false when it names nothing the app can
// run; [argv] carries the command without the program name, with a
// listener on aetherDefaultCommandPort added when none was named.
func aetherCoreOfCommand(command string) (argv []string, port int, ok bool) {
	words := aetherWords(command)
	if len(words) == 0 {
		return nil, 0, false
	}
	argv = words
	if !strings.HasPrefix(argv[0], "-") {
		// A program name in front is dropped.
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return nil, 0, false
	}
	listener := aetherListenerFlagOf(argv)
	if !containsStr(argv, listener) {
		fixed := aetherWithListener(argv, listener, aetherDefaultCommandPort)
		return fixed, aetherDefaultCommandPort, true
	}
	p, okp := aetherPortAfter(argv, listener)
	if !okp {
		return nil, 0, false
	}
	return argv, p, true
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
