package main

import (
	"testing"
)

// A TUN that connects but carries no name resolution loads nothing, and
// the core reports no error at all: the adapter is up, the outbound is
// up, every lookup simply dies. The rendered hijack rule matched only
// `protocol: dns`, which the TUN inbound sets for ONE destination — its
// own derived hijack address (interface address + 1, see
// protocol/tun/inbound.go NewConnectionEx / dnsHijackAddress). A query
// to the resolver the OS already had (the LAN router, an ISP resolver)
// is therefore not recognised as DNS, falls through the rules to
// final:proxy, and the proxy is asked to reach the user's own LAN
// address from the far side of the tunnel. It never answers.
//
// PattN hijacks on port 53 OR protocol dns
// (ServiceLib/Services/CoreConfig/Singbox/SingboxRoutingService.cs,
// the logical-or rule beside its `action: sniff`), so a query is caught
// whichever resolver the OS sends it to. That is the shape pinned here.

// hijackRuleCoversPort53 reports whether a hijack-dns rule catches port
// 53, either directly or through a logical-or branch. A protocol-only
// rule does not: protocol is populated by sniffing or by the TUN's own
// hijack-address match, never by the port a query was sent to.
func hijackRuleCoversPort53(r map[string]any) bool {
	if r["action"] != "hijack-dns" {
		return false
	}
	if hasPort53(r) {
		return true
	}
	if r["type"] == "logical" {
		for _, sub := range asList(r["rules"]) {
			if m, ok := sub.(map[string]any); ok && hasPort53(m) {
				return true
			}
		}
	}
	return false
}

func hasPort53(r map[string]any) bool {
	for _, p := range asList(r["port"]) {
		if f, ok := p.(float64); ok && f == 53 {
			return true
		}
	}
	return false
}

func TestTunHijacksDNSByPortNotOnlyProtocol(t *testing.T) {
	rules := routeRulesOf(t, tunRender(t, nil))
	hijacks := 0
	for _, r := range rules {
		if r["action"] == "hijack-dns" {
			hijacks++
		}
		if hijackRuleCoversPort53(r) {
			return
		}
	}
	if hijacks == 0 {
		t.Fatal("TUN mode renders no hijack-dns rule at all: every captured lookup goes to the proxy")
	}
	t.Errorf("the %d hijack-dns rule(s) match only the sniffed protocol, so a query to the OS resolver "+
		"(anything but the TUN's own hijack address) falls through to final:proxy and dies there; "+
		"hijack port 53 as PattN does", hijacks)
}

// The hijack must not be narrowed to one address family or one transport:
// DNS rides UDP and TCP, and a stub resolver may use either.
func TestTunHijackIsNotTransportScoped(t *testing.T) {
	for _, r := range routeRulesOf(t, tunRender(t, nil)) {
		if !hijackRuleCoversPort53(r) {
			continue
		}
		if net, ok := r["network"]; ok {
			t.Errorf("the port-53 hijack is scoped to network %v: DNS on the other transport is not hijacked", net)
		}
		return
	}
	t.Skip("no port-53 hijack rendered yet")
}
