package main

// spoof_psiphon.go — the SNI spoof → Psiphon chain.
//
// Shape: traffic enters the TUN inbound, sing-box hands it to Psiphon's
// local SOCKS listener, and Psiphon dials its own servers through the
// SNI-spoofed tunnel (a dedicated upstream inbound → the spoof front →
// the profile's VPN server). Psiphon therefore leaves from its own
// network while every one of its connections is carried by the spoofed
// tunnel underneath.
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

// Chain outbound/inbound tags as rendered in the sing-box config.
const (
	spoofPsiphonTag   = "psiphon"
	spoofPsiphonUpTag = "psiphon-up-in"
)

// psiphonChainWarmup is how long the chain waits for Psiphon's listener.
// A cold start runs Psiphon's own server handshake through the spoofed
// tunnel, so it gets a longer budget than a plain aether profile.
var psiphonChainWarmup = 120 * time.Second

// spoofChainPsiphonPort is Psiphon's local SOCKS listener;
// spoofChainUpstreamPort is the loopback-only inbound Psiphon dials its
// servers through (it routes to "proxy", the spoof front).
var (
	spoofChainPsiphonPort  atomic.Int32
	spoofChainUpstreamPort atomic.Int32
)

func setSpoofChainPorts(psiphonPort, upstreamPort int) {
	spoofChainPsiphonPort.Store(int32(psiphonPort))
	spoofChainUpstreamPort.Store(int32(upstreamPort))
}

func clearSpoofChainPorts() {
	spoofChainPsiphonPort.Store(0)
	spoofChainUpstreamPort.Store(0)
}

func spoofChainPorts() (psiphonPort, upstreamPort int, ok bool) {
	p := int(spoofChainPsiphonPort.Load())
	u := int(spoofChainUpstreamPort.Load())
	if p < 1 || p > 65535 || u < 1 || u > 65535 || p == u {
		return 0, 0, false
	}
	return p, u, true
}

// spoofPsiphonChain reports whether [p]'s connect must run the chain.
func spoofPsiphonChain(p *Profile, settings Settings) bool {
	return p != nil && p.Core.IsSpoof() && settings.SpoofPsiphon
}

// spoofPsiphonRendering reports whether the chain ports are published
// for the config currently being rendered.
func spoofPsiphonRendering(p *Profile, settings Settings) bool {
	if !spoofPsiphonChain(p, settings) {
		return false
	}
	_, _, ok := spoofChainPorts()
	return ok
}

// spoofTransportXray reports whether a plain CoreSpoof connect runs on
// the Xray transport (Settings.SpoofXray) rather than sing-box. The
// chain is exempt: its TUN inbound is rendered by the sing-box
// transport alone, so an armed chain always runs on sing-box.
func spoofTransportXray(p *Profile, settings Settings) bool {
	return p != nil && p.Core.IsSpoof() && settings.SpoofXray && !spoofPsiphonChain(p, settings)
}

// tunActiveFor reports whether the rendered config carries the TUN
// inbound. The chain always does: its purpose is to expose Psiphon to
// the whole system. The Xray transport renders none.
func tunActiveFor(p *Profile, settings Settings) bool {
	if spoofTransportXray(p, settings) {
		return false
	}
	return settings.TunEnabled || spoofPsiphonRendering(p, settings)
}

// tunRequiredFor is the pre-flight mirror of tunActiveFor: the chain is
// known before its ports exist, so Connect/elevation checks use this.
func tunRequiredFor(p *Profile, settings Settings) bool {
	if spoofTransportXray(p, settings) {
		return false
	}
	return settings.TunEnabled || spoofPsiphonChain(p, settings)
}

// tunModeFor reports whether the config Connect is about to run actually
// carries the TUN inbound — i.e. whether TUN, not the OS proxy, is what
// feeds system traffic into the tunnel. It mirrors the renderer for the
// two cases where the tunEnabled setting is inert: Xray never renders
// TUN, and a full raw core override replaces the rendered config (with
// its TUN append). The chain always runs on sing-box and always forces
// TUN, which is why spoofPsiphonChain (not the port-dependent
// spoofPsiphonRendering) is the right test here.
func tunModeFor(p *Profile, settings Settings) bool {
	if p == nil || p.Core.IsXray() || spoofTransportXray(p, settings) {
		return false
	}
	if _, full, err := rawCoreOverride(settings.CoreOverrides); err == nil && full {
		return false
	}
	return tunRequiredFor(p, settings)
}

// allocateSpoofChainPorts picks the two loopback ports the chain binds:
// Psiphon's SOCKS listener and the transport inbound its own dials ride.
// Both must avoid the app's mixed listener (and its companion port) and
// the spoof engine's front port, or the chain would dial itself.
func allocateSpoofChainPorts(settings Settings, spoofPort int) (psiphonPort, upstreamPort int, err error) {
	mixed := settings.MixedPort
	if mixed <= 0 {
		mixed = 2080 // BuildSingBoxConfig's default listener
	}
	avoid := []int{mixed, mixed + 1, spoofPort}
	psiphonPort, err = distinctPort(avoid...)
	if err != nil {
		return 0, 0, fmt.Errorf("psiphon chain listener: %w", err)
	}
	upstreamPort, err = distinctPort(append(avoid, psiphonPort)...)
	if err != nil {
		return 0, 0, fmt.Errorf("psiphon chain upstream: %w", err)
	}
	return psiphonPort, upstreamPort, nil
}

// renderSpoofPsiphonChain adds Psiphon's outbound, the loopback
// upstream inbound and the inbound-matched route rules to a rendered
// config. "proxy" stays the final outbound, so Psiphon's own dials
// (matched by their inbound tag) and the hijacked DNS queries keep
// riding the spoofed tunnel.
func renderSpoofPsiphonChain(cfg, route map[string]any) {
	psiphonPort, upstreamPort, ok := spoofChainPorts()
	if !ok {
		return
	}
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok {
		return
	}
	cfg["outbounds"] = append(outbounds, map[string]any{
		"type": "socks", "tag": spoofPsiphonTag,
		"server": aetherLoopback, "server_port": psiphonPort,
	})
	inbounds, ok := cfg["inbounds"].([]any)
	if !ok {
		return
	}
	cfg["inbounds"] = append(inbounds, map[string]any{
		"type": "mixed", "tag": spoofPsiphonUpTag,
		"listen": aetherLoopback, "listen_port": upstreamPort,
	})
	rules, _ := route["rules"].([]any)
	route["rules"] = append(rules,
		map[string]any{"inbound": []any{spoofPsiphonUpTag}, "outbound": "proxy"},
		map[string]any{"inbound": []any{"mixed-in", "tun-in"}, "outbound": spoofPsiphonTag},
	)
}
