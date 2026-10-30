package main

import (
	"fmt"
	"strings"
)

// Shared uTLS fingerprint validation + normalization for both cores.
//
// Allowlist source (vendored in-repo, intersection of both engines):
//   - sing-box: third_party/sing-box/option/tls.go — Fingerprint enum
//     (chrome, firefox, edge, safari, 360, qq, ios, android, random,
//     randomized, plus chrome_psk/chrome_pq variants)
//   - xray: xray/transport/internet/tls/tls.go — PresetFingerprints
//     (chrome, firefox, safari, ios, android, edge, 360, qq, random,
//     randomized, randomizednoalpn, unsafe)
//
// Only the intersection is accepted: sing-box's chrome_* variants
// have no xray equivalent (and vice versa for randomizednoalpn/
// unsafe), and profiles can switch cores at any time. Anything
// outside the list needs rawOutbound JSON. "none"/empty on the preset
// keeps the legacy Fingerprint field (see profileFingerprint).
var supportedFingerprints = []string{
	"chrome", "firefox", "safari", "ios", "android",
	"edge", "360", "qq", "random", "randomized",
}

// normalizeFingerprint lowercases and trims one fingerprint value.
// Both cores match fingerprints case-sensitively against lowercase
// names, so "Chrome" must render as "chrome".
func normalizeFingerprint(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// profileRendersTLS reports whether the builders will render a TLS
// (or REALITY) block for this profile — the union of the sing-box
// condition (TLS/trojan/vless+flow) and the xray condition (which
// additionally counts REALITY and any flow). Fingerprint/mux-adjacent
// validation only applies when TLS renders; otherwise the fields are
// ignored by both cores and must not fail a save.
func profileRendersTLS(p *Profile) bool {
	if p == nil {
		return false
	}
	return p.TLS || p.RealityEnabled || p.Protocol == "trojan" ||
		(p.Protocol == "vless" && strings.TrimSpace(p.Flow) != "") ||
		strings.TrimSpace(p.Flow) != ""
}

// effectiveFingerprintValue resolves the fingerprint the builders
// will render (preset wins, empty/"none" keeps legacy, empty legacy
// falls back to chrome), normalized to lowercase.
func effectiveFingerprintValue(p *Profile) string {
	if p == nil {
		return "chrome"
	}
	if fp := normalizeFingerprint(p.TLSFingerprint); fp != "" && fp != "none" {
		return fp
	}
	if fp := normalizeFingerprint(p.Fingerprint); fp != "" {
		return fp
	}
	return "chrome"
}

// containsFold reports whether list contains s (case-insensitive).
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// validateFingerprint rejects fingerprints neither core accepts. Only
// enforced when TLS renders (see profileRendersTLS) — otherwise both
// builders ignore the fields.
func validateFingerprint(p *Profile) error {
	if !profileRendersTLS(p) {
		return nil
	}
	fp := effectiveFingerprintValue(p)
	if !containsFold(supportedFingerprints, fp) {
		return fmt.Errorf("invalid fingerprint %q: want one of %v (or rawOutbound JSON for engine-specific values)", fp, supportedFingerprints)
	}
	return nil
}

// validateGlobalFingerprint rejects bad global fingerprint overrides
// when enabled (a disabled override renders nothing, so it never
// fails a save). An enabled override with no value is a mis-set
// switch, not a default — fail instead of guessing.
func validateGlobalFingerprint(s Settings) error {
	if !s.FingerprintEnabled {
		return nil
	}
	fp := normalizeFingerprint(s.FingerprintValue)
	if fp == "" {
		return fmt.Errorf("global fingerprint is enabled but no value is set")
	}
	if !containsFold(supportedFingerprints, fp) {
		return fmt.Errorf("invalid global fingerprint %q: want one of %v", fp, supportedFingerprints)
	}
	return nil
}
