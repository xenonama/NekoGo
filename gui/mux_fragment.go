package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Shared mux (connection multiplexing) + fragment (TLS hello
// fragmentation) helpers for both core builders.
//
// Share-link wire format follows the Streisand/v2rayNG convention so
// links import across clients:
//	fragment=<packets>,<length>,<interval>  e.g. fragment=tlshello,10-20,10
//	mux=<concurrency>                       e.g. mux=8
// where <packets> is "tlshello" or an "N-M" range. Import also accepts
// the reversed Streisand order (length,interval,packets) by detecting
// which part names the packets mode.

// supportedMuxProtocols are the sing-box multiplex sub-protocols.
// xray's mux has no protocol knob (it is ignored there).
var supportedMuxProtocols = []string{"h2mux", "smux", "yamux"}

// defaultMuxConcurrency is rendered for xray when the user enables mux
// without picking a concurrency (xray's own default is 8).
const defaultMuxConcurrency = 8

// muxProtocolOrDefault resolves the sing-box multiplex protocol:
// empty means h2mux. Callers must run validateMux first.
func muxProtocolOrDefault(p *Profile) string {
	if s := strings.ToLower(strings.TrimSpace(p.MuxProtocol)); s != "" {
		return s
	}
	return "h2mux"
}

// validateMux rejects mux misuse before any render or save: unknown
// sub-protocol, out-of-range concurrency, and mux combined with an
// xtls flow (vision flows must not be multiplexed — both cores fail
// such configs at runtime, so fail fast here instead).
func validateMux(p *Profile) error {
	if p == nil || !p.MuxEnabled {
		return nil
	}
	if proto := strings.TrimSpace(p.MuxProtocol); proto != "" && !containsFold(supportedMuxProtocols, proto) {
		return fmt.Errorf("invalid mux protocol %q: want one of %v", p.MuxProtocol, supportedMuxProtocols)
	}
	if p.MuxConcurrency < 0 || p.MuxConcurrency > 1024 {
		return fmt.Errorf("invalid mux concurrency %d: want 0 (core default) or 1..1024", p.MuxConcurrency)
	}
	if strings.TrimSpace(p.Flow) != "" {
		return fmt.Errorf("mux cannot be combined with xtls flow %q (disable mux or clear the flow)", p.Flow)
	}
	return nil
}

// validateFragment rejects malformed fragment options before any
// render or save. ""/"off" disables (length/interval ignored);
// otherwise mode must be "tlshello" or an "N-M" packets range, length
// an "A-B" range, and interval non-negative. Both core builders call
// this so a bad length fails fast on either engine.
func validateFragment(p *Profile) error {
	if p == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(p.TLSFragment))
	if mode == "" || mode == "off" {
		return nil
	}
	if mode != "tlshello" {
		from, to, ok := parsePacketsRange(mode)
		if !ok || from <= 0 || to < from {
			return fmt.Errorf("invalid tlsFragment mode %q: want tlshello or N-M", p.TLSFragment)
		}
	}
	length := strings.TrimSpace(p.TLSFragmentLen)
	if length == "" {
		length = "10-20"
	}
	lfrom, lto, ok := parsePacketsRange(length)
	if !ok || lfrom < 0 || lto < lfrom || lto == 0 {
		return fmt.Errorf("invalid tlsFragmentLen %q: want A-B", p.TLSFragmentLen)
	}
	if p.TLSFragmentInt < 0 {
		return fmt.Errorf("invalid tlsFragmentInt %d: want >= 0", p.TLSFragmentInt)
	}
	return nil
}

// muxBlockSingBox renders the sing-box outbound "multiplex" object,
// or nil when mux is disabled. Error on invalid options (never render
// garbage the core would reject at startup).
func muxBlockSingBox(p *Profile) (map[string]any, error) {
	if err := validateMux(p); err != nil {
		return nil, err
	}
	if p == nil || !p.MuxEnabled {
		return nil, nil
	}
	m := map[string]any{"enabled": true, "protocol": muxProtocolOrDefault(p)}
	if p.MuxConcurrency > 0 {
		m["max_connections"] = p.MuxConcurrency
	}
	return m, nil
}

// muxBlockXray renders the xray outbound "mux" object, or nil when mux
// is disabled. Error on invalid options.
func muxBlockXray(p *Profile) (map[string]any, error) {
	if err := validateMux(p); err != nil {
		return nil, err
	}
	if p == nil || !p.MuxEnabled {
		return nil, nil
	}
	cc := p.MuxConcurrency
	if cc <= 0 {
		cc = defaultMuxConcurrency
	}
	return map[string]any{"enabled": true, "concurrency": cc}, nil
}

// isPacketsToken reports whether a fragment URI part names the packets
// mode ("tlshello" or an "N-M" range with from >= 1).
func isPacketsToken(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "tlshello" {
		return true
	}
	from, to, ok := parsePacketsRange(s)
	return ok && from > 0 && to >= from
}

// parseFragmentParam decodes a `fragment=` share-link value in either
// order: "packets,length,interval" (v2rayNG PR #2832) or
// "length,interval,packets" (Streisand export). Order detection keys
// on a trailing "tlshello": "length,interval,tlshello" reads
// packets-last, everything else with a packets-like head reads
// packets-first (an all-numeric triple like "20-40,5-10,1-3" is
// genuinely ambiguous and parses packets-first, per the v2rayNG
// convention). The interval may be a single int or an "A-B" range —
// ranges collapse to their upper bound because NekoGo models the xray
// delay as a single max (delay.to).
// Returns mode ("tlshello"/"N-M"), length ("A-B"), interval int.
func parseFragmentParam(val string) (mode, length string, interval int, err error) {
	fail := func() (string, string, int, error) {
		return "", "", 0, fmt.Errorf("invalid fragment %q: want packets,length,interval (e.g. tlshello,10-20,10)", val)
	}
	parts := strings.Split(val, ",")
	if len(parts) != 3 {
		return fail()
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	packets, length, iv := "", "", ""
	if strings.EqualFold(parts[2], "tlshello") {
		length, iv, packets = parts[0], parts[1], parts[2]
	} else if isPacketsToken(parts[0]) {
		packets, length, iv = parts[0], parts[1], parts[2]
	} else {
		return fail()
	}
	mode = strings.ToLower(packets)
	if mode != "tlshello" {
		from, to, ok := parsePacketsRange(mode)
		if !ok || from <= 0 || to < from {
			return fail()
		}
	}
	if length == "" {
		length = "10-20"
	}
	if lfrom, lto, ok := parsePacketsRange(length); !ok || lfrom < 0 || lto < lfrom || lto == 0 {
		return fail()
	}
	interval = 0
	if iv != "" {
		if n, convErr := strconv.Atoi(iv); convErr == nil && n >= 0 {
			interval = n
		} else if from, to, ok := parsePacketsRange(iv); ok && from >= 0 && to >= from {
			interval = to
		} else {
			return fail()
		}
	}
	return mode, length, interval, nil
}

// parseMuxParam decodes a `mux=` share-link value (Streisand/v2rayNG:
// concurrency only, e.g. mux=8). N > 0 enables mux with that
// concurrency; anything else is an error (absent param = disabled).
func parseMuxParam(val string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil || n <= 0 || n > 1024 {
		return 0, fmt.Errorf("invalid mux %q: want concurrency 1..1024", val)
	}
	return n, nil
}

// buildFragmentParam renders the canonical `fragment=` share-link value
// (packets-first order). Empty when fragment is disabled.
func buildFragmentParam(p *Profile) string {
	mode := strings.ToLower(strings.TrimSpace(p.TLSFragment))
	if mode == "" || mode == "off" {
		return ""
	}
	length := strings.TrimSpace(p.TLSFragmentLen)
	if length == "" {
		length = "10-20"
	}
	return fmt.Sprintf("%s,%s,%d", mode, length, p.TLSFragmentInt)
}

// buildMuxParam renders the canonical `mux=` share-link value.
// Empty when mux is disabled.
func buildMuxParam(p *Profile) string {
	if p == nil || !p.MuxEnabled {
		return ""
	}
	cc := p.MuxConcurrency
	if cc <= 0 {
		cc = defaultMuxConcurrency
	}
	return strconv.Itoa(cc)
}

// globalFragmentTriple resolves the effective global fragment triple
// (settings defaults applied: tlshello / 10-20). Check
// s.FragmentEnabled before calling — disabled globals never render.
func globalFragmentTriple(s Settings) (mode, length string, interval int) {
	mode = strings.ToLower(strings.TrimSpace(s.FragmentMode))
	if mode == "" {
		mode = "tlshello"
	}
	length = strings.TrimSpace(s.FragmentLength)
	if length == "" {
		length = "10-20"
	}
	return mode, length, s.FragmentInterval
}

// validateGlobalFragment rejects bad global fragment options when the
// override is enabled (a disabled override renders nothing, so it
// never fails a save). Rules are identical to per-profile
// validateFragment — the same values flow into the same builders.
func validateGlobalFragment(s Settings) error {
	if !s.FragmentEnabled {
		return nil
	}
	mode, length, interval := globalFragmentTriple(s)
	if err := validateFragment(&Profile{TLSFragment: mode, TLSFragmentLen: length, TLSFragmentInt: interval}); err != nil {
		return fmt.Errorf("global fragment: %w", err)
	}
	return nil
}

// applyGlobalTransportOverrides returns a copy of p with the global
// fragment/fingerprint overrides applied (render-time only — the
// stored profile is never mutated, so toggling the globals off
// restores every profile exactly). Raw-JSON profiles are exempt:
// raw bypasses the builders, so there is nothing honest to override.
// A nil profile stays nil.
func applyGlobalTransportOverrides(p *Profile, s Settings) *Profile {
	if p == nil {
		return nil
	}
	out := *p
	if p.RawOutbound == "" {
		if s.FragmentEnabled {
			out.TLSFragment, out.TLSFragmentLen, out.TLSFragmentInt = globalFragmentTriple(s)
		}
		if s.FingerprintEnabled {
			if fp := normalizeFingerprint(s.FingerprintValue); fp != "" {
				out.TLSFingerprint = fp
				out.Fingerprint = ""
			}
		}
	}
	return &out
}
