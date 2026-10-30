package main

import "strings"

// sni_defaults.go — curated Cloudflare IP + decoy-SNI defaults and the
// checked-entry selection policy for the SNI Spoofing tab.
//
// Every entry below was verified with a real TLS handshake (the same
// per-row probe the tab runs: TCP dial to ip:443 + handshake with
// ServerName=sni and full chain validation, TLS 1.2+). The IPs cover
// all 15 official Cloudflare IPv4 ranges
// (https://www.cloudflare.com/ips-v4); the SNIs are Cloudflare-fronted
// hostnames whose certificates validate against any generic CF edge
// IP. Verified 2026-10-16; re-scan if a region throttles a specific
// anycast address.
//
// NOTE: ICMP ping is NOT a health signal here — several CF edge IPs
// drop ping while serving TLS fine. The tab scans TLS, not ping.

var defaultCloudflareIPs = []string{
	"103.21.246.10", // 103.21.244.0/22
	"103.22.201.10", // 103.22.200.0/22
	"103.31.4.1",    // 103.31.4.0/22
	"104.19.0.1",    // 104.16.0.0/13
	"104.25.0.10",   // 104.24.0.0/14
	"108.162.192.1", // 108.162.192.0/18
	"131.0.73.5",    // 131.0.72.0/22
	"141.101.65.10", // 141.101.64.0/18
	"162.159.192.1", // 162.158.0.0/15
	"172.64.0.1",    // 172.64.0.0/13
	"172.67.130.1",  // 172.64.0.0/13 (second-half anycast)
	"173.245.48.1",  // 173.245.48.0/20
	"188.114.96.5",  // 188.114.96.0/20
	"190.93.240.1",  // 190.93.240.0/20
	"197.234.240.1", // 197.234.240.0/22
	"198.41.130.10", // 198.41.128.0/17
}

var defaultDecoySNIs = []string{
	"www.cloudflare.com",
	"cloudflare.com",
	"developers.cloudflare.com",
	"cdnjs.cloudflare.com",
	"one.one.one.one",
	"www.digitalocean.com",
	"www.speedtest.net",
	"www.shopify.com",
	"www.discord.com",
	"www.canva.com",
}

// defaultSNISelectedIP/SNI are the pre-pinned endpoint + fake SNI:
// the fastest IP in the probe round and the SNI with the best
// cross-IP hit rate. Both are members of the curated lists below.
const (
	defaultSNISelectedIP  = "162.159.192.1"
	defaultSNISelectedSNI = "developers.cloudflare.com"
)

// DefaultSNIIPs returns the curated Cloudflare endpoint list shown in
// the SNI Spoofing tab (fresh copy, safe to mutate; every entry
// arrives checked).
func DefaultSNIIPs() []SNIIP {
	out := make([]SNIIP, 0, len(defaultCloudflareIPs))
	for _, ip := range defaultCloudflareIPs {
		out = append(out, SNIIP{IP: ip, Source: "curated", Selected: selTrue()})
	}
	return out
}

// DefaultSNISNIs returns the curated decoy-SNI list shown in the SNI
// Spoofing tab (fresh copy, safe to mutate; every entry arrives
// checked).
func DefaultSNISNIs() []SNISNI {
	out := make([]SNISNI, 0, len(defaultDecoySNIs))
	for _, h := range defaultDecoySNIs {
		out = append(out, SNISNI{Host: h, Source: "curated", Selected: selTrue()})
	}
	return out
}

// selTrue returns a fresh *true for seeded entries. nil means "active"
// too; defaults write it explicitly so every default entry renders
// checked while the first uncheck stores an explicit, persisted false.
func selTrue() *bool {
	t := true
	return &t
}

// spoofPinIP resolves the endpoint pin the engine and front should
// use: settings.SNISelectedIP wins while its entry stays checked (or
// while no matching entry exists at all — legacy loose pins); a pin
// that IS in the list but was unchecked yields to the first checked
// entry, so unchecked entries never reach the engine. Empty when
// nothing is checked (callers fall back to the profile target).
func spoofPinIP(s *Settings) string {
	if s == nil {
		return ""
	}
	pin := strings.TrimSpace(s.SNISelectedIP)
	if pin != "" {
		inList := false
		for _, e := range s.SNIIPs {
			if strings.TrimSpace(e.IP) == pin {
				inList = true
				if e.IsSelected() {
					return pin
				}
			}
		}
		if !inList {
			return pin
		}
	}
	for _, e := range s.SNIIPs {
		if e.IsSelected() {
			if ip := strings.TrimSpace(e.IP); ip != "" {
				return ip
			}
		}
	}
	return ""
}

// spoofPinSNI is spoofPinIP for the decoy-SNI list (same selection
// policy; hosts are compared normalized).
func spoofPinSNI(s *Settings) string {
	if s == nil {
		return ""
	}
	pin := normalizeFakeSNI(s.SNISelectedSNI)
	if pin != "" {
		inList := false
		for _, e := range s.SNISNIs {
			if normalizeFakeSNI(e.Host) == pin {
				inList = true
				if e.IsSelected() {
					return pin
				}
			}
		}
		if !inList {
			return pin
		}
	}
	for _, e := range s.SNISNIs {
		if e.IsSelected() {
			if h := normalizeFakeSNI(e.Host); h != "" {
				return h
			}
		}
	}
	return ""
}

// seedSNIDefaults fills empty SNI-tab fields with the curated
// defaults. It NEVER touches user entries: non-empty lists and
// already-pinned selections are kept verbatim. Reports whether
// anything changed.
func seedSNIDefaults(s *Settings) bool {
	if s == nil {
		return false
	}
	changed := false
	if len(s.SNIIPs) == 0 {
		s.SNIIPs = DefaultSNIIPs()
		changed = true
	}
	if len(s.SNISNIs) == 0 {
		s.SNISNIs = DefaultSNISNIs()
		changed = true
	}
	if s.SNISelectedIP == "" {
		s.SNISelectedIP = defaultSNISelectedIP
		changed = true
	}
	if s.SNISelectedSNI == "" {
		s.SNISelectedSNI = defaultSNISelectedSNI
		changed = true
	}
	return changed
}

// resetSNIDefaults replaces the SNI-tab lists, selection state and
// pins with the curated defaults, preserving the scanner stamps
// (LastOK/LastMs/LastAt/LastTLS) of entries that survive the reset so
// a fresh scan is not mandatory afterwards.
func resetSNIDefaults(s *Settings) {
	if s == nil {
		return
	}
	keepIP := map[string]SNIIP{}
	for _, e := range s.SNIIPs {
		if _, ok := keepIP[e.IP]; !ok {
			keepIP[e.IP] = e
		}
	}
	freshIPs := DefaultSNIIPs()
	for i, e := range freshIPs {
		if old, ok := keepIP[e.IP]; ok {
			freshIPs[i].LastOK = old.LastOK
			freshIPs[i].LastMs = old.LastMs
			freshIPs[i].LastAt = old.LastAt
			freshIPs[i].LastTLS = old.LastTLS
		}
	}
	keepSNI := map[string]SNISNI{}
	for _, e := range s.SNISNIs {
		if _, ok := keepSNI[e.Host]; !ok {
			keepSNI[e.Host] = e
		}
	}
	freshSNIs := DefaultSNISNIs()
	for i, e := range freshSNIs {
		if old, ok := keepSNI[e.Host]; ok {
			freshSNIs[i].LastOK = old.LastOK
			freshSNIs[i].LastMs = old.LastMs
			freshSNIs[i].LastAt = old.LastAt
			freshSNIs[i].LastTLS = old.LastTLS
		}
	}
	s.SNIIPs = freshIPs
	s.SNISNIs = freshSNIs
	s.SNISelectedIP = defaultSNISelectedIP
	s.SNISelectedSNI = defaultSNISelectedSNI
}
