package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// TUN settings, after PattN's TunModeItem
// (PattN/v2rayN/ServiceLib/Models/Configs/ConfigItems.cs). PattN keeps a
// settings group here while NekoGo had a switch and a stack and hardcoded
// the rest, which meant an MTU that could not be raised for a fragile
// link, one TUN address that could not be moved off a collision, and no
// way to say "leave these routes alone".
//
// Every field is optional in the stored config: an absent (zero) value
// renders exactly what NekoGo rendered before the group existed, so
// turning the settings on is the user's decision, not an upgrade's.

// tunMTUOptions is PattN's Global.TunMtus list, offered as a dropdown.
// The value is not limited to it — a path that needs 1420 should not
// have to round up to 1500 and re-fragment.
var tunMTUOptions = []int{1280, 1408, 1500, 4064, 9000, 65535}

// tunStackOptions is PattN's Global.TunStacks, in PattN's order, which
// puts tunStackDefault first in the dropdown.
var tunStackOptions = []string{"gvisor", "system", "mixed"}

// tunIPv4Options is PattN's Global.TunIPv4Address. NekoGo's own
// 172.19.0.1/24 leads: it is what existing installs already have routes
// and firewall rules about, and PattN's /30 pools are the alternatives
// when one is taken.
var tunIPv4Options = []string{
	"172.19.0.1/24",
	"172.18.0.1/30",
	"172.31.0.1/30",
	"172.20.0.1/30",
	"172.16.0.1/30",
	"192.168.100.1/30",
	"10.10.14.1/30",
	"10.1.0.1/30",
}

// tunIPv6Options is PattN's Global.TunIPv6Address, whose prefixes leave
// room for a peer address: the sing-box system stack derives a gateway
// from the first prefix and refuses a prefix that names a single
// address. PattN's own comment says it; /126 is the longest that works.
var tunIPv6Options = []string{
	"fc00::172:19:0:1/126",
	"fc00::172:18:0:1/126",
	"fc00::172:31:0:1/126",
	"fc00::172:20:0:1/126",
	"fc00::172:16:0:1/126",
	"fc00::192:168:100:1/126",
	"fc00::10:10:14:1/126",
	"fc00::10:1:0:1/126",
}

// tunIcmpOptions is PattN's Global.TunIcmpRoutingPolicies. "rule" lets
// the routing rules already rendered decide, which is what NekoGo did
// before the setting existed and so stays the unset value.
var tunIcmpOptions = []string{"rule", "direct", "unreachable", "drop", "reply"}

// validTunMTU reports whether mtu is renderable as a TUN MTU. Empty/zero
// means "use the default" and is the caller's business, not an error.
func validTunMTU(mtu int) bool {
	return mtu >= 1280 && mtu <= 65535
}

// validTunIcmp reports whether v is one of the ICMP policies PattN
// offers. Empty means "rule" (no policy rendered).
func validTunIcmp(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", tunIcmpOptions[0]:
		return true
	}
	for _, o := range tunIcmpOptions[1:] {
		if strings.EqualFold(strings.TrimSpace(v), o) {
			return true
		}
	}
	return false
}

// normalizeTunIcmp returns the canonical ICMP policy for a stored value.
// Unset stays unset rather than becoming "rule": "rule" is what the
// renderer already does with nothing (tunRouteRules has no case for it),
// and writing the word into a config turns "never chosen" into a choice
// the user did not make.
func normalizeTunIcmp(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// validTunPrefix reports whether raw is a CIDR prefix sing-box can put on
// the interface or in a route. sing-box decodes these into
// badoption.Listable[netip.Prefix] (third_party/sing-box/option/tun.go),
// which has no bare-address form: "172.19.0.1" saved here would be a
// config the core refuses to start, so it is refused at the boundary
// that still has a human watching.
func validTunPrefix(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	_, err := netip.ParsePrefix(strings.TrimSpace(raw))
	return err == nil
}

// tunPrefix normalizes a stored prefix, or returns "" when it is not one.
func tunPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if !validTunPrefix(raw) {
		return ""
	}
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		return ""
	}
	return p.String()
}

// normalizeTunPrefixes drops blanks, canonicalizes the rest and reports
// the entries that are not prefixes at all, so a saved list keeps
// working and the user is told which line did not make it (PattN
// CoreConfigContextBuilder warns per address and drops it).
func normalizeTunPrefixes(in []string) (ok []string, bad []string) {
	for _, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if p := tunPrefix(raw); p != "" {
			ok = append(ok, p)
		} else {
			bad = append(bad, strings.TrimSpace(raw))
		}
	}
	return ok, bad
}

// validateTunSettings checks the TUN group the way SaveSettings checks
// every other field that a core would reject at start time. It reports
// the first problem with the name of the setting and the value, because
// the GUI shows this line and nothing else.
func validateTunSettings(s *Settings) error {
	if s.TunMTU != 0 && !validTunMTU(s.TunMTU) {
		return fmt.Errorf("tunMTU must be 1280-65535 (got %d)", s.TunMTU)
	}
	if !validTunIcmp(s.TunIcmp) {
		return fmt.Errorf("tunIcmp must be one of %s", strings.Join(tunIcmpOptions, ", "))
	}
	if v := strings.TrimSpace(s.TunAddress); v != "" && !validTunPrefix(v) {
		return fmt.Errorf("tunAddress %q is not a CIDR prefix like 172.19.0.1/24", v)
	}
	if v := strings.TrimSpace(s.TunAddress6); v != "" && !validTunPrefix(v) {
		return fmt.Errorf("tunAddress6 %q is not a CIDR prefix like fc00::172:19:0:1/126", v)
	}
	// A /127 or /128 IPv6 prefix names one address, and the system stack
	// needs a peer to derive a gateway from — sing-box's own error, not a
	// style rule, so refuse it here rather than at connect.
	if v := tunPrefix(s.TunAddress6); v != "" {
		p, _ := netip.ParsePrefix(v)
		if p.Addr().Is6() && p.Bits() > 126 {
			return fmt.Errorf("tunAddress6 %s leaves no room for the peer address (must be /126 or shorter)", v)
		}
	}
	_, bad := normalizeTunPrefixes(s.TunRouteExclude)
	if len(bad) > 0 {
		return fmt.Errorf("tunRouteExclude: %q is not a CIDR prefix like 192.168.1.0/24", bad[0])
	}
	return nil
}

// normalizeTunSettings rewrites the group in place to canonical values:
// trimmed prefixes, a canonical ICMP policy. It does not fill in
// defaults — those are the renderer's business, so that a stored zero
// keeps meaning "the user never chose" (and renders as before) instead
// of being silently pinned to whatever the current default is.
func normalizeTunSettings(s *Settings) {
	s.TunAddress = tunPrefix(s.TunAddress)
	s.TunAddress6 = tunPrefix(s.TunAddress6)
	s.TunIcmp = normalizeTunIcmp(s.TunIcmp)
	ex, _ := normalizeTunPrefixes(s.TunRouteExclude)
	if len(ex) == 0 {
		s.TunRouteExclude = nil
	} else {
		s.TunRouteExclude = ex
	}
}

// --- effective values: what the renderer puts on the interface ---

// tunEffectiveMTU is the MTU rendered into the inbound. 0 (never chosen)
// keeps NekoGo's historical 1350; an out-of-range stored value — from a
// hand-edited config, which save-time validation never saw — also falls
// back rather than writing an interface the core refuses to bring up.
func tunEffectiveMTU(s Settings) int {
	if s.TunMTU >= 1280 && s.TunMTU <= 65535 {
		return s.TunMTU
	}
	return tunMTU
}

// tunEffectiveAddress is the interface's IPv4 prefix.
func tunEffectiveAddress(s Settings) string {
	if p := tunPrefix(s.TunAddress); p != "" {
		return p
	}
	return tunAddress
}

// tunTakesIPv6 is PattN's TunTakesIPv6: an IPv6 prefix goes on the
// interface when the user asked for one AND either the OS still gives
// new interfaces IPv6, or a global IPv6 address somewhere else could
// reach the internet outside the tunnel. Asking an OS that has turned
// IPv6 off for a TUN address is how the whole connect dies at startup;
// letting an existing global address out around the tunnel is worse.
func tunTakesIPv6(s Settings) bool {
	if !s.TunEnableIPv6 {
		return false
	}
	return !ipv6DisabledByOS() || globalIPv6Present()
}

// tunAddressList is the inbound's "address": the IPv4 prefix, and the
// IPv6 one when the gate allows. A stored prefix that is not one is
// dropped, not guessed at.
func tunAddressList(s Settings) []string {
	addrs := []string{tunEffectiveAddress(s)}
	if tunTakesIPv6(s) {
		v6 := tunPrefix(s.TunAddress6)
		if v6 == "" {
			v6 = tunIPv6Options[0]
		}
		// A /127 or /128 names no peer for the system stack's gateway;
		// validateTunSettings refuses it on save, and here it is simply
		// left off a hand-edited config.
		if p, err := netip.ParsePrefix(v6); err == nil && p.Bits() <= 126 {
			addrs = append(addrs, v6)
		}
	}
	return addrs
}

// tunSelfHostPrefixes turns each interface prefix into the single host
// route PattN guards (ToSingleAddressPrefix in SingboxRoutingService):
// traffic addressed *to* the TUN's own address must be dropped, because
// auto_route sends it back into the tunnel and straight to the outbound
// again — a loop that pins a CPU core. Matched per address, never by the
// prefix it came in on: PattN's comment is that a whole /30 also covers
// the resolver address systemd-resolved registers beside it, which would
// drop every system lookup along with the loop.
func tunSelfHostPrefixes(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, raw := range addrs {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			continue
		}
		bits := 32
		if p.Addr().Is6() {
			bits = 128
		}
		out = append(out, netip.PrefixFrom(p.Addr(), bits).String())
	}
	return out
}

// tunRouteRules are the route rules a TUN config needs and a proxy-mode
// config must not have. They come first, ahead of the DNS hijack: each
// one exists to stop a packet reaching an outbound that would feed it
// back into the interface.
func tunRouteRules(s Settings) []any {
	rules := []any{}
	if self := tunSelfHostPrefixes(tunAddressList(s)); len(self) > 0 {
		rules = append(rules, map[string]any{
			"ip_cidr": self,
			"action":  "reject",
			"method":  "drop",
		})
	}
	// NetBIOS (137-139), RPC (135) and LLMNR (5353) are answered on the
	// local link; nothing out there will ever reply, so the tunnel's job
	// is to not pretend it will try.
	rules = append(rules, map[string]any{
		"network": []any{"udp"},
		"port":    []any{135, 137, 138, 139, 5353},
		"action":  "reject",
	})
	// 224.0.0.0/3 is IPv4 multicast and mDNS, ff00::/8 its IPv6 twin.
	rules = append(rules, map[string]any{
		"ip_cidr": []any{"224.0.0.0/3", "ff00::/8"},
		"action":  "reject",
	})
	// ICMP: "rule" (the default) renders nothing and leaves the choice
	// to the rules above and the routing below. Anything else is a
	// blanket answer, and only "reply" answers politely.
	switch normalizeTunIcmp(s.TunIcmp) {
	case "direct":
		rules = append(rules, map[string]any{"network": []any{"icmp"}, "outbound": "direct"})
	case "unreachable", "drop", "reply":
		method := map[string]string{"unreachable": "default", "drop": "drop", "reply": "reply"}[normalizeTunIcmp(s.TunIcmp)]
		rules = append(rules, map[string]any{"network": []any{"icmp"}, "action": "reject", "method": method})
	}
	return rules
}

// The OS questions behind the IPv6 gate, as variables so the decision
// table is tested without a host that happens to be configured one way.
var (
	ipv6DisabledByOS  = osIPv6DisabledByOS
	globalIPv6Present = osHasGlobalIPv6Address
)

// osHasGlobalIPv6Address reports whether any live interface holds a
// globally routable IPv6 address (2000::/3). Link-local and unique-local
// answers are excluded: they never reach the IPv6 internet, so a host
// holding only those has no IPv6 traffic that could go around the tunnel.
// On a probe failure it reports true, which is the answer that keeps
// asking for the TUN's IPv6 address — leaking IPv6 out is the worse
// mistake (PattN Utils.HasGlobalIPv6Address).
func osHasGlobalIPv6Address() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return true
		}
		for _, a := range addrs {
			p, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			if isGlobalUnicastIPv6(p.Addr()) {
				return true
			}
		}
	}
	return false
}

// isGlobalUnicastIPv6 is the 2000::/3 test: the top three bits are 001,
// which in one comparison leaves out ::1, fe80::/10, fc00::/7 and
// ff00::/8.
func isGlobalUnicastIPv6(a netip.Addr) bool {
	return a.Is6() && a.AsSlice()[0]&0xE0 == 0x20
}

// ipv6DisabledByComponents is the Windows DisabledComponents bit test:
// 0x10 ("prefer IPv4 over IPv6") turns IPv6 off on the non-tunnel
// interfaces, and Wintun's adapter is one of those (IF_TYPE_PROP_VIRTUAL)
// — so with the bit set, a TUN asked for an IPv6 address is a TUN that
// does not come up. 0xFF includes it. Read as a function of the value so
// the decision is testable without a registry (PattN
// Utils.IsIPv6DisabledByComponents).
func ipv6DisabledByComponents(disabled uint64) bool {
	return disabled&0x10 != 0
}

// tunAutoRouteEnabled reads the auto-route switch with its historical
// default: an unset value means the interface takes the default route,
// because that is what a TUN without routes does for a living.
func tunAutoRouteEnabled(s Settings) bool {
	return s.TunAutoRoute == nil || *s.TunAutoRoute
}
