package main

// Tests for the curated SNI-tab defaults (sni_defaults.go): every
// default IP must be a real Cloudflare address, every decoy SNI must
// be normalized, the seed must never clobber user data, and the reset
// must preserve scanner stamps.

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cidrContains reports whether ip belongs to cidr (test-only helper).
func cidrContains(cidr, ip string) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return n.Contains(net.ParseIP(ip))
}

func TestDefaultSNIIPsAreCloudflare(t *testing.T) {
	ips := DefaultSNIIPs()
	if len(ips) == 0 {
		t.Fatal("DefaultSNIIPs is empty")
	}
	seen := map[string]bool{}
	for _, e := range ips {
		if e.IP == "" {
			t.Fatal("default SNIIP with empty IP")
		}
		if seen[e.IP] {
			t.Fatalf("duplicate default IP %q", e.IP)
		}
		seen[e.IP] = true
		if !isCloudflareIP(e.IP) {
			t.Errorf("default IP %q is not in the Cloudflare ranges", e.IP)
		}
		if e.Source == "" {
			t.Errorf("default IP %q has no source", e.IP)
		}
		if e.Selected == nil || !*e.Selected {
			t.Errorf("default IP %q does not arrive checked", e.IP)
		}
	}
	// All 15 official ranges must be represented.
	covered := map[string]bool{}
	for _, c := range cloudflareCIDRs {
		covered[c] = false
	}
	for _, e := range ips {
		for _, c := range cloudflareCIDRs {
			if cidrContains(c, e.IP) {
				covered[c] = true
			}
		}
	}
	for c, ok := range covered {
		if !ok {
			t.Errorf("official range %s has no curated IP", c)
		}
	}
}

func TestDefaultSNISNIsNormalized(t *testing.T) {
	snis := DefaultSNISNIs()
	if len(snis) == 0 {
		t.Fatal("DefaultSNISNIs is empty")
	}
	seen := map[string]bool{}
	for _, e := range snis {
		if e.Host == "" {
			t.Fatal("default SNISNI with empty host")
		}
		if e.Host != normalizeFakeSNI(e.Host) {
			t.Errorf("default SNI %q is not normalized", e.Host)
		}
		if strings.Contains(e.Host, " ") || strings.Contains(e.Host, "/") {
			t.Errorf("default SNI %q looks invalid", e.Host)
		}
		if seen[e.Host] {
			t.Fatalf("duplicate default SNI %q", e.Host)
		}
		seen[e.Host] = true
		if e.Selected == nil || !*e.Selected {
			t.Errorf("default SNI %q does not arrive checked", e.Host)
		}
	}
}

func TestDefaultPinsAreMembers(t *testing.T) {
	s := DefaultSettings()
	foundIP, foundSNI := false, false
	for _, e := range s.SNIIPs {
		if e.IP == s.SNISelectedIP {
			foundIP = true
		}
	}
	for _, e := range s.SNISNIs {
		if e.Host == s.SNISelectedSNI {
			foundSNI = true
		}
	}
	if !foundIP {
		t.Errorf("default pin %q is not in SNIIPs", s.SNISelectedIP)
	}
	if !foundSNI {
		t.Errorf("default pin %q is not in SNISNIs", s.SNISelectedSNI)
	}
}

func TestSeedSNIDefaultsFillsEmptyOnly(t *testing.T) {
	// Empty settings get everything.
	s := Settings{}
	if !seedSNIDefaults(&s) {
		t.Fatal("seedSNIDefaults(empty) reported no change")
	}
	if len(s.SNIIPs) == 0 || len(s.SNISNIs) == 0 {
		t.Fatalf("seed left lists empty: %+v", s)
	}
	if s.SNISelectedIP == "" || s.SNISelectedSNI == "" {
		t.Fatalf("seed left pins empty: %+v", s)
	}
	// Full settings are untouched.
	custom := Settings{
		SNISelectedIP:  "1.2.3.4",
		SNISelectedSNI: "custom.example.com",
		SNIIPs:         []SNIIP{{IP: "1.2.3.4", Source: "manual", LastOK: true}},
		SNISNIs:        []SNISNI{{Host: "custom.example.com", Source: "manual"}},
	}
	before := custom
	if seedSNIDefaults(&custom) {
		t.Fatal("seedSNIDefaults(full) reported a change")
	}
	if custom.SNISelectedIP != before.SNISelectedIP ||
		custom.SNISelectedSNI != before.SNISelectedSNI ||
		len(custom.SNIIPs) != 1 || len(custom.SNISNIs) != 1 ||
		!custom.SNIIPs[0].LastOK {
		t.Fatalf("seed clobbered user data: %+v", custom)
	}
	// Partial: only the gaps fill.
	partial := Settings{SNISNIs: []SNISNI{{Host: "mine.example.com"}}}
	seedSNIDefaults(&partial)
	if len(partial.SNIIPs) == 0 || partial.SNISelectedIP == "" {
		t.Fatalf("seed did not fill gaps: %+v", partial)
	}
	if len(partial.SNISNIs) != 1 || partial.SNISNIs[0].Host != "mine.example.com" {
		t.Fatalf("seed touched the user SNI list: %+v", partial.SNISNIs)
	}
}

// The engine pin follows the checkboxes: a checked pin wins, an
// unchecked pin yields to the first checked entry, a pin absent from
// the list (legacy loose pin) is kept, and nothing checked = empty.
func TestSpoofPinsFollowSelection(t *testing.T) {
	no := false
	s := Settings{
		SNISelectedIP:  "1.1.1.1",
		SNISelectedSNI: "pin.example.com",
		SNIIPs: []SNIIP{
			{IP: "1.1.1.1", Selected: &no},
			{IP: "2.2.2.2"},
			{IP: "3.3.3.3"},
		},
		SNISNIs: []SNISNI{
			{Host: "pin.example.com", Selected: &no},
			{Host: "next.example.com"},
		},
	}
	if got := spoofPinIP(&s); got != "2.2.2.2" {
		t.Fatalf("spoofPinIP = %q, want first checked 2.2.2.2", got)
	}
	if got := spoofPinSNI(&s); got != "next.example.com" {
		t.Fatalf("spoofPinSNI = %q, want first checked next.example.com", got)
	}
	// The pin wins while its entry stays checked.
	yes := true
	s.SNIIPs[0].Selected = &yes
	s.SNISNIs[0].Selected = &yes
	if got := spoofPinIP(&s); got != "1.1.1.1" {
		t.Fatalf("spoofPinIP = %q, want checked pin 1.1.1.1", got)
	}
	if got := spoofPinSNI(&s); got != "pin.example.com" {
		t.Fatalf("spoofPinSNI = %q, want checked pin pin.example.com", got)
	}
	// Legacy loose pins (absent from the list) are kept verbatim.
	loose := Settings{SNISelectedIP: "9.9.9.9", SNISelectedSNI: "Loose.Example.COM."}
	if got := spoofPinIP(&loose); got != "9.9.9.9" {
		t.Fatalf("loose spoofPinIP = %q, want 9.9.9.9", got)
	}
	if got := spoofPinSNI(&loose); got != "loose.example.com" {
		t.Fatalf("loose spoofPinSNI = %q, want normalized loose.example.com", got)
	}
	// Nothing checked at all: empty (callers fall back to the profile).
	empty := Settings{
		SNISelectedIP:  "1.1.1.1",
		SNISelectedSNI: "pin.example.com",
		SNIIPs:         []SNIIP{{IP: "1.1.1.1", Selected: &no}},
		SNISNIs:        []SNISNI{{Host: "pin.example.com", Selected: &no}},
	}
	if got := spoofPinIP(&empty); got != "" {
		t.Fatalf("spoofPinIP = %q, want empty", got)
	}
	if got := spoofPinSNI(&empty); got != "" {
		t.Fatalf("spoofPinSNI = %q, want empty", got)
	}
}

// Existing configs saved before the curated defaults shipped load
// with empty SNI-tab lists — the store must seed them on open while
// keeping any user entries verbatim.
func TestNewConfigStoreSeedsSNILists(t *testing.T) {
	dir := t.TempDir()
	raw := `{"profiles":[],"settings":{"mixedPort":2080}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	st, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	got := st.GetSettings()
	if len(got.SNIIPs) != len(defaultCloudflareIPs) {
		t.Fatalf("seeded SNIIPs has %d entries, want %d", len(got.SNIIPs), len(defaultCloudflareIPs))
	}
	if len(got.SNISNIs) != len(defaultDecoySNIs) {
		t.Fatalf("seeded SNISNIs has %d entries, want %d", len(got.SNISNIs), len(defaultDecoySNIs))
	}
	if got.SNISelectedIP == "" || got.SNISelectedSNI == "" {
		t.Fatalf("seeded pins are empty: %q / %q", got.SNISelectedIP, got.SNISelectedSNI)
	}

	// User entries survive the load untouched.
	dir2 := t.TempDir()
	raw2 := `{"profiles":[],"settings":{"mixedPort":2080,"sniIps":[{"ip":"9.9.9.9","source":"manual"}],"sniSnis":[{"host":"mine.example.com"}]}}`
	if err := os.WriteFile(filepath.Join(dir2, "config.json"), []byte(raw2), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	st2, err := NewConfigStore(dir2)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	got2 := st2.GetSettings()
	if len(got2.SNIIPs) != 1 || got2.SNIIPs[0].IP != "9.9.9.9" {
		t.Fatalf("user IP list clobbered: %+v", got2.SNIIPs)
	}
	if len(got2.SNISNIs) != 1 || got2.SNISNIs[0].Host != "mine.example.com" {
		t.Fatalf("user SNI list clobbered: %+v", got2.SNISNIs)
	}
}

// Reset replaces the lists with the curated defaults but keeps the
// scanner stamps of surviving entries.
func TestResetSNIDefaultsPreservesStamps(t *testing.T) {
	s := Settings{
		SNISelectedIP:  "9.9.9.9",
		SNISelectedSNI: "gone.example.com",
		SNIIPs: []SNIIP{
			{IP: "162.159.192.1", Source: "manual", LastOK: true, LastMs: 137, LastAt: "then", LastTLS: "1.3"},
			{IP: "9.9.9.9", Source: "manual"},
		},
		SNISNIs: []SNISNI{
			{Host: "www.cloudflare.com", LastOK: true, LastMs: 42, LastAt: "then", LastTLS: "1.3"},
		},
	}
	resetSNIDefaults(&s)
	if s.SNISelectedIP != defaultSNISelectedIP || s.SNISelectedSNI != defaultSNISelectedSNI {
		t.Fatalf("reset did not restore pins: %q / %q", s.SNISelectedIP, s.SNISelectedSNI)
	}
	if len(s.SNIIPs) != len(defaultCloudflareIPs) {
		t.Fatalf("reset SNIIPs has %d entries, want %d", len(s.SNIIPs), len(defaultCloudflareIPs))
	}
	if len(s.SNISNIs) != len(defaultDecoySNIs) {
		t.Fatalf("reset SNISNIs has %d entries, want %d", len(s.SNISNIs), len(defaultDecoySNIs))
	}
	for _, e := range s.SNIIPs {
		if e.IP == "9.9.9.9" {
			t.Fatal("reset kept a custom IP")
		}
		if e.IP == "162.159.192.1" {
			if !e.LastOK || e.LastMs != 137 || e.LastAt != "then" || e.LastTLS != "1.3" {
				t.Fatalf("reset dropped stamps: %+v", e)
			}
		}
	}
	for _, e := range s.SNISNIs {
		if e.Host == "www.cloudflare.com" && (!e.LastOK || e.LastMs != 42) {
			t.Fatalf("reset dropped SNI stamps: %+v", e)
		}
	}
}
