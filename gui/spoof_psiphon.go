package main

// spoof_psiphon.go — the SNI spoof → exit-chain leg (Psiphon or Tor).
//
// Shape: traffic enters the TUN inbound, sing-box hands it to the
// chain's local SOCKS listener, and that hop dials its own servers
// through the SNI-spoofed tunnel (a dedicated upstream inbound → the
// spoof front → the profile's VPN server). The chain therefore leaves
// from its own network while every one of its connections is carried by
// the spoofed tunnel underneath.
//
// The two chain ports are published around the sing-box config render
// only (startSpoofLocked sets them, clears them once the core accepted
// the config): ephemeral probes and config previews must never render
// the chain.

import (
	"fmt"
	"sync/atomic"
	"time"
)

// Chain outbound/inbound tags as rendered in the sing-box config. Each
// driver gets its own pair: the tag is what the config dump, the route
// rules and every log line about a stuck chain call the hop, so routing
// a Tor chain through an outbound named "psiphon" would work and lie.
const (
	spoofPsiphonTag   = "psiphon"
	spoofPsiphonUpTag = "psiphon-up-in"
	spoofTorTag       = "tor"
	spoofTorUpTag     = "tor-up-in"
)

// spoofChainTags returns the outbound and upstream inbound tags the
// rendered config uses for [driver].
func spoofChainTags(driver string) (tag, upTag string) {
	if driver == chainDriverTor {
		return spoofTorTag, spoofTorUpTag
	}
	return spoofPsiphonTag, spoofPsiphonUpTag
}

// psiphonChainWarmup is how long the chain waits for Psiphon's listener.
// A cold start runs Psiphon's own server handshake through the spoofed
// tunnel, so it gets a longer budget than a plain aether profile. Tor's
// budget is its own and longer still (chain_driver.go).
var psiphonChainWarmup = 120 * time.Second

// spoofChainPort is the exit chain's local SOCKS listener;
// spoofChainUpstreamPort is the loopback-only inbound the chain dials
// its own servers through (it routes to "proxy", the spoof front).
// spoofChainKind names the driver those two ports belong to, which is
// what the renderer needs to name the hop it adds.
var (
	spoofChainKind         atomic.Int32
	spoofChainPort         atomic.Int32
	spoofChainUpstreamPort atomic.Int32
)

// The published chain kind, as a small int so it can live in an atomic
// beside the ports it belongs to.
const (
	chainKindNone = iota
	chainKindPsiphon
	chainKindTor
)

func chainKindCode(driver string) int32 {
	switch driver {
	case chainDriverPsiphon:
		return chainKindPsiphon
	case chainDriverTor:
		return chainKindTor
	}
	return chainKindNone
}

func chainKindName(code int32) string {
	switch code {
	case chainKindPsiphon:
		return chainDriverPsiphon
	case chainKindTor:
		return chainDriverTor
	}
	return ""
}

func setSpoofChainPorts(driver string, chainPort, upstreamPort int) {
	spoofChainKind.Store(chainKindCode(driver))
	spoofChainPort.Store(int32(chainPort))
	spoofChainUpstreamPort.Store(int32(upstreamPort))
}

func clearSpoofChainPorts() {
	spoofChainKind.Store(chainKindNone)
	spoofChainPort.Store(0)
	spoofChainUpstreamPort.Store(0)
}

// spoofChainPorts returns the published chain ports and the driver they
// were published for. ok is false unless both ports are usable and
// distinct, and no ports are published without a driver.
func spoofChainPorts() (driver string, chainPort, upstreamPort int, ok bool) {
	driver = chainKindName(spoofChainKind.Load())
	p := int(spoofChainPort.Load())
	u := int(spoofChainUpstreamPort.Load())
	if driver == "" || p < 1 || p > 65535 || u < 1 || u > 65535 || p == u {
		return "", 0, 0, false
	}
	return driver, p, u, true
}

// spoofChain reports whether [p]'s connect must run an exit chain.
// Turning SNI spoofing off also disarms the chain: there is no engine to
// carry its upstream.
func spoofChain(p *Profile, settings Settings) bool {
	return spoofConnectEnabled(p, settings) && spoofChainDriver(settings) != ""
}

// spoofPsiphonChain reports whether that chain is the Psiphon one.
func spoofPsiphonChain(p *Profile, settings Settings) bool {
	return spoofChain(p, settings) && spoofChainDriver(settings) == chainDriverPsiphon
}

// spoofChainRendering reports whether the chain ports are published
// for the config currently being rendered.
func spoofChainRendering(p *Profile, settings Settings) bool {
	if !spoofChain(p, settings) {
		return false
	}
	_, _, _, ok := spoofChainPorts()
	return ok
}

// spoofTransportXray reports whether a plain CoreSpoof connect runs on
// the Xray transport (Settings.SpoofXray) rather than sing-box. The
// chain is exempt: its TUN inbound is rendered by the sing-box
// transport alone, so an armed chain always runs on sing-box.
func spoofTransportXray(p *Profile, settings Settings) bool {
	return spoofConnectEnabled(p, settings) && settings.SpoofXray && !spoofChain(p, settings)
}

// tunActiveFor reports whether the rendered config carries the TUN
// inbound. The chain always does: its purpose is to expose the exit hop
// to the whole system. The Xray transport renders none.
func tunActiveFor(p *Profile, settings Settings) bool {
	if spoofTransportXray(p, settings) {
		return false
	}
	return settings.TunEnabled || spoofChainRendering(p, settings)
}

// tunRequiredFor is the pre-flight mirror of tunActiveFor: the chain is
// known before its ports exist, so Connect/elevation checks use this.
func tunRequiredFor(p *Profile, settings Settings) bool {
	if spoofTransportXray(p, settings) {
		return false
	}
	return settings.TunEnabled || spoofChain(p, settings)
}

// tunModeFor reports whether the config Connect is about to run actually
// carries the TUN inbound — i.e. whether TUN, not the OS proxy, is what
// feeds system traffic into the tunnel. It mirrors the renderer for the
// two cases where the tunEnabled setting is inert: Xray never renders
// TUN, and a full raw core override replaces the rendered config (with
// its TUN append). The chain always runs on sing-box and always forces
// TUN, which is why spoofChain (not the port-dependent
// spoofChainRendering) is the right test here.
func tunModeFor(p *Profile, settings Settings) bool {
	// Xray and mihomo render no TUN inbound, so neither is ever in TUN
	// mode however the tunEnabled setting reads.
	if p == nil || p.Core.IsXray() || p.Core.IsMihomo() || spoofTransportXray(p, settings) {
		return false
	}
	if _, full, err := rawCoreOverride(settings.CoreOverrides); err == nil && full {
		return false
	}
	return tunRequiredFor(p, settings)
}

// allocateSpoofChainPorts picks the two loopback ports the chain binds:
// the exit hop's SOCKS listener and the transport inbound its own dials
// ride. Both must avoid the app's mixed listener (and its companion
// port) and the spoof engine's front port, or the chain would dial
// itself.
func allocateSpoofChainPorts(settings Settings, spoofPort int) (chainPort, upstreamPort int, err error) {
	mixed := settings.MixedPort
	if mixed <= 0 {
		mixed = 2080 // BuildSingBoxConfig's default listener
	}
	avoid := []int{mixed, mixed + 1, spoofPort}
	chainPort, err = distinctPort(avoid...)
	if err != nil {
		return 0, 0, fmt.Errorf("chain listener: %w", err)
	}
	upstreamPort, err = distinctPort(append(avoid, chainPort)...)
	if err != nil {
		return 0, 0, fmt.Errorf("chain upstream: %w", err)
	}
	return chainPort, upstreamPort, nil
}

// renderSpoofChain adds the armed chain's outbound, the loopback
// upstream inbound and the inbound-matched route rules to a rendered
// config. "proxy" stays the final outbound, so the chain's own dials
// (matched by their inbound tag) and the hijacked DNS queries keep
// riding the spoofed tunnel.
func renderSpoofChain(cfg, route map[string]any) {
	driver, chainPort, upstreamPort, ok := spoofChainPorts()
	if !ok {
		return
	}
	tag, upTag := spoofChainTags(driver)
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok {
		return
	}
	cfg["outbounds"] = append(outbounds, map[string]any{
		"type": "socks", "tag": tag,
		"server": aetherLoopback, "server_port": chainPort,
	})
	inbounds, ok := cfg["inbounds"].([]any)
	if !ok {
		return
	}
	cfg["inbounds"] = append(inbounds, map[string]any{
		"type": "mixed", "tag": upTag,
		"listen": aetherLoopback, "listen_port": upstreamPort,
	})
	rules, _ := route["rules"].([]any)
	route["rules"] = append(rules,
		map[string]any{"inbound": []any{upTag}, "outbound": "proxy"},
		map[string]any{"inbound": []any{"mixed-in", "tun-in"}, "outbound": tag},
	)
}
