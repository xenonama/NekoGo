package main

// connect_ports.go — the ports one connect needs, before it starts any.
//
// A connect names some ports ahead of time (Settings' mixed inbound, the
// xray API listener, the exits a hand-written engine command line names)
// and picks the rest while it runs. The two halves have to agree: the
// fixed ones are checked before a child exists, and the picked ones are
// chosen around them, so no leg of a connect binds a port another leg of
// the same connect is about to need.

import (
	"net"
	"strconv"
	"strings"
	"sync"
)

// planConnectPorts returns the addresses this connect will bind that are
// fixed before it starts, and the port numbers behind them. Ports a leg
// of the app already owns are left out: a sticky reconnect adopts a chain
// child that is listening on its two ports, and the rendered config names
// them, so checking them as "must be free" would refuse every reconnect
// that keeps its leg.
func planConnectPorts(p *Profile, s Settings) (addrs []string, ports []int) {
	if p == nil {
		return nil, nil
	}
	owned := publishedConnectPorts()
	seen := map[int]bool{}
	add := func(host string, port int) {
		if port < 1 || port > 65535 || owned[port] || seen[port] {
			return
		}
		seen[port] = true
		ports = append(ports, port)
		addrs = append(addrs, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	addPort := func(port int) { add(aetherLoopback, port) }

	mixed := effectiveMixedPort(s.MixedPort)
	// The transport is the profile's core, except for a spoof connect:
	// Settings.SpoofXray moves that one onto xray, and the chain never
	// follows (its TUN inbound is rendered by sing-box alone).
	asXray := p.Core.IsXray()
	if spoofConnectEnabled(p, s) && s.SpoofXray && !spoofChain(p, s) {
		asXray = true
	}
	switch {
	case p.Core.IsMihomo():
		addPort(mixed)
	case asXray:
		addPort(mixed)
		addPort(mixed + 1)
		apiAddr := s.XrayAPIAddress
		if apiAddr == "" {
			apiAddr = net.JoinHostPort(aetherLoopback, strconv.Itoa(xrayAPIDefaultPort))
		}
		host, port := splitAPIAddr(apiAddr)
		switch host {
		case "127.0.0.1", "0.0.0.0", "::", "localhost":
			addPort(port)
		}
	default:
		// The rendered config is authoritative: a full raw override names
		// its own inbounds and a TUN inbound names no port at all.
		if rendered, err := singBoxBindPorts(p, s); err == nil {
			for _, a := range rendered {
				host, portStr, err := net.SplitHostPort(a)
				if err != nil {
					continue
				}
				port, err := strconv.Atoi(portStr)
				if err != nil {
					continue
				}
				add(host, port)
			}
		}
	}
	// A hand-written engine command line owns its exits, so the numbers
	// in it are fixed ports this connect binds — the tunnel's and the
	// hop's. A rendered command gets its listener picked at connect time.
	if aetherDriverProfile(p) && strings.TrimSpace(p.Aether.Command) != "" {
		if argv, _, ok := aetherCoreOfCommand(p.Aether.Command); ok {
			for _, flag := range []string{aetherFlagBind, aetherFlagTorBind, aetherFlagPsiphonBind} {
				if port, ok := aetherPortAfter(argv, flag); ok {
					addPort(port)
				}
			}
		}
	}
	return addrs, ports
}

// publishedConnectPorts collects the ports a leg of the app has already
// published: the engine the transport dials, the spoof front, and the
// chain's listener and upstream inbound. The chain's pair matters most —
// it is published before the transport starts, so a picker running inside
// that window must not hand one of them to the core's RPC listener.
func publishedConnectPorts() map[int]bool {
	owned := map[int]bool{}
	if p, ok := currentAetherActivePort(); ok {
		owned[p] = true
	}
	if p := int(spoofFrontPort.Load()); p > 0 {
		owned[p] = true
	}
	if _, chain, upstream, ok := spoofChainPorts(); ok {
		owned[chain] = true
		owned[upstream] = true
	}
	return owned
}

// The claim is the connect in flight's fixed ports, held from before its
// first child until Start returns. A generation names one claim so a
// release cannot drop the ports a newer connect claimed meanwhile.
var (
	connectPortMu    sync.Mutex
	connectPortClaim map[int]bool
	connectPortGen   uint64
)

// claimConnectPorts marks [ports] as needed by the connect in flight and
// returns the release, which only clears its own claim.
func claimConnectPorts(ports []int) func() {
	connectPortMu.Lock()
	connectPortGen++
	gen := connectPortGen
	claim := make(map[int]bool, len(ports))
	for _, p := range ports {
		if p > 0 && p <= 65535 {
			claim[p] = true
		}
	}
	connectPortClaim = claim
	connectPortMu.Unlock()
	return func() {
		connectPortMu.Lock()
		if connectPortGen == gen {
			connectPortClaim = nil
		}
		connectPortMu.Unlock()
	}
}

// claimPickedConnectPorts adds ports picked while the connect runs to the
// claim it already holds. A leg's listener can stay unbound for minutes
// after its number is chosen — the arti Tor hop opens its listener only
// at the end of a bootstrap — and the published chain ports are cleared
// as soon as the transport accepts the config, so without this a latency
// probe picking a free port in that window takes the number the leg still
// has to bind. With no connect in flight there is nothing to add to, and
// reserving a port for the rest of the process's life would be a leak.
func claimPickedConnectPorts(ports ...int) {
	connectPortMu.Lock()
	defer connectPortMu.Unlock()
	if connectPortClaim == nil {
		return
	}
	for _, p := range ports {
		if p > 0 && p <= 65535 {
			connectPortClaim[p] = true
		}
	}
}

// connectPortReserved reports whether [port] is already spoken for by
// this connect or by a leg that is running: a picker may not hand it out.
func connectPortReserved(port int) bool {
	if port < 1 || port > 65535 {
		return true
	}
	if publishedConnectPorts()[port] {
		return true
	}
	connectPortMu.Lock()
	defer connectPortMu.Unlock()
	return connectPortClaim[port]
}
