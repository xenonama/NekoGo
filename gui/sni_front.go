package main

// sni_front.go — the loopback-only substitution layer between NekoGo
// profiles and the sni-spoof engine.
//
// Rule (hard): the front applies ONLY to loopback addresses. A
// profile whose Address is a domain keeps its own server_name/Host
// and is routed by its own core untouched. Rewriting a direct
// Cloudflare profile would silently double-route it — that was bug B2
// and must not come back.
//
// Two shapes exist:
//   - CoreSpoof profiles: the spoof engine IS the route. After
//     SpoofManager.Start binds 127.0.0.1:<port>, the sing-box
//     transport config is rewritten so the "proxy" outbound dials
//     the local spoof listener instead of the remote endpoint.
//     server_name / Host headers are never touched.
//   - Other cores with a loopback Address while a spoof engine runs:
//     same rewrite (chained setup). Domain addresses never rewrite.

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// spoofFrontPort is the live engine LISTEN_PORT published by
// CoreManager after SpoofManager.Start. Builders read it to decide
// the rewrite; 0 means "no engine" (render direct, no fallback
// lies in the connect path because CoreManager guarantees the port
// before starting the transport).
var spoofFrontPort atomic.Int32

// setSpoofFrontPort publishes the engine listen port.
func setSpoofFrontPort(p int) { spoofFrontPort.Store(int32(p)) }

// clearSpoofFrontPort drops the published port (engine stopped).
func clearSpoofFrontPort() { spoofFrontPort.Store(0) }

// clearSpoofFrontPortIf drops the published port only when it still
// names [port]. A crashed engine must not leave its port published,
// while a newer engine's port has to survive a late report from the one
// it replaced.
func clearSpoofFrontPortIf(port int) {
	if port < 1 || port > 65535 {
		return
	}
	for {
		cur := spoofFrontPort.Load()
		if int(cur) != port {
			return
		}
		if spoofFrontPort.CompareAndSwap(cur, 0) {
			return
		}
	}
}

// currentSpoofFrontPort returns the published port, ok=false when no
// engine is live.
func currentSpoofFrontPort() (port int, ok bool) {
	p := int(spoofFrontPort.Load())
	if p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

// spoofEnabled reports whether the SNI spoof engine may run. Absent
// (nil) means enabled, so configs written before the toggle existed keep
// their behaviour.
func spoofEnabled(s Settings) bool {
	return s.SpoofEnabled == nil || *s.SpoofEnabled
}

// spoofConnectEnabled reports whether a CoreSpoof profile actually runs
// the spoof engine. Settings → SNI spoofing can turn it off so the
// profile dials its own address with the normal transport (e.g. a local
// 127.0.0.1 tunnel the user runs themselves).
func spoofConnectEnabled(p *Profile, s Settings) bool {
	return p != nil && p.Core.IsSpoof() && spoofEnabled(s)
}

// isLoopbackAddress reports whether addr is a loopback endpoint:
// 127.0.0.1, any 127.x.x.x, ::1, or localhost (case-insensitive).
func isLoopbackAddress(addr string) bool {
	s := strings.TrimSpace(strings.ToLower(addr))
	if s == "" {
		return false
	}
	if s == "localhost" {
		return true
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.IsLoopback()
	}
	// Bare "127.x" hostnames without full IP syntax still count.
	return s == "127.0.0.1" || strings.HasPrefix(s, "127.")
}

// shouldApplySNIFront reports whether the profile's Address is a
// loopback address. Only then does NekoGo substitute
// profile.Address/Port with the local spoof port for non-spoof cores.
func shouldApplySNIFront(p *Profile) bool {
	if p == nil {
		return false
	}
	return isLoopbackAddress(p.Address)
}

// loopbackListenerAlive reports whether something accepts TCP on the
// given endpoint. A short 300ms budget: a localhost refusal is
// instant, and a timeout means filtered/unusable — both "not
// serving". Used to tell a dead chained config apart from a
// legitimate local relay chain on the same port.
func loopbackListenerAlive(addr string, port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	host := strings.TrimSpace(addr)
	if host == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// shouldRescueLoopbackProfile reports whether Connect should convert
// this profile to the SNI spoof core on the fly. True when the
// profile is engine-eligible, its Address is loopback, and NOTHING
// listens on that loopback port — the classic legacy dead-end
// (trojan://...@127.0.0.1:40443 ws imported before the auto-detect):
// sing-box "connects" while every dial is refused. A live listener
// means a legitimate local relay chain on that port — never hijack
// it. Callers gate on GOOS (the engine ships as sni-spoof.exe). mihomo is
// excluded like an already-spoof profile: the rescue converges stale
// imports, and a core the user explicitly picked is not one — rewriting
// it would silently run a different engine than the one selected.
func shouldRescueLoopbackProfile(p *Profile, settings Settings) bool {
	if p == nil || p.Core.IsSpoof() || p.Core.IsMihomo() {
		return false
	}
	if !spoofEnabled(settings) {
		// The user turned the engine off on purpose: a loopback address is
		// their own local tunnel, not a dead end to convert.
		return false
	}
	if !isLoopbackAddress(p.Address) {
		return false
	}
	if spoofEligibilityIssue(p) != nil {
		return false
	}
	return !loopbackListenerAlive(p.Address, p.Port)
}

// shouldRescueDirectSpoofProfile reports whether Connect should
// convert an eligible DIRECT-domain profile to the SNI spoof core on
// the fly (prompt.txt Test-CF: imported before the domain
// auto-detect, sitting on sing-box, DPI-blocked on every direct
// dial). True when the profile is engine-eligible, not already
// spoof, and addressed at a bare domain — the exact import
// auto-tag rule (import.go), so Connect converges a stale profile
// to the same core a fresh import would get. IP literals are
// excluded (never auto-tag: proving CF-fronting needs a live
// lookup). Callers gate on GOOS like the loopback rescue, and mihomo is
// excluded for the reason given there.
func shouldRescueDirectSpoofProfile(p *Profile, settings Settings) bool {
	if p == nil || p.Core.IsSpoof() || p.Core.IsMihomo() || !spoofEnabled(settings) {
		return false
	}
	if spoofEligibilityIssue(p) != nil {
		return false
	}
	return isDomainAddress(p.Address)
}

// resolveSNITarget returns the (CF IP, decoy SNI, remote port) triple
// to inject, based on the profile shape:
//   - loopback address (chained shape, e.g. 127.0.0.1:40443): the
//     profile's own ServerName/Host IS the SNI (imported from the
//     link's sni=/host= params) and is used when the SNI tab has no
//     checked SNI. Checked entries win: the pin while its entry stays
//     checked, else the first checked entry (spoofPinSNI/spoofPinIP).
//     The remote port is always 443 — profile.Port here is the stale
//     LOCAL listen port and must never be dialed upstream.
//   - domain address: same pin-first policy. The tab pins are the
//     decoys — a decoy pin makes the engine present it as the fake
//     SNI, exactly as loopback does (Test-CF vs APPXA parity). With
//     no pin, fall back to the profile's own SNI/Host + resolved CF
//     IP, remote port = profile.Port.
func resolveSNITarget(p *Profile, settings Settings) (ip, sni string, port int, err error) {
	if p == nil {
		return "", "", 0, fmt.Errorf("nil profile")
	}
	if spoofPlanA(settings) {
		return resolvePlanATarget(p, settings)
	}
	if isLoopbackAddress(p.Address) {
		// SNI: checked-entry pin wins, else the profile's own SNI
		// (the link already carries sni=...&host=...).
		sni = spoofPinSNI(&settings)
		if sni == "" {
			sni = normalizeFakeSNI(p.ServerName)
		}
		if sni == "" {
			sni = normalizeFakeSNI(p.Host)
		}
		if sni == "" {
			return "", "", 0, fmt.Errorf("loopback profile %q has no SNI (set sni/host in the link or check one in the SNI Spoofing tab)", p.Name)
		}
		// Endpoint IP: checked-entry pin wins, else resolve the SNI
		// domain.
		ip = spoofPinIP(&settings)
		if ip == "" {
			ip, err = resolveSpoofEndpointIP(sni)
			if err != nil {
				return "", "", 0, fmt.Errorf("loopback profile %q: auto-target %q failed: %w (or pin an IP in the SNI Spoofing tab)", p.Name, sni, err)
			}
		}
		return ip, sni, 443, nil
	}
	ip = spoofPinIP(&settings)
	if ip != "" {
		// Pinned/checked CF IP: the dial target, no DNS at all (a
		// hijacked local resolver must never break a pinned Start).
	} else if ip4, ok := cfIPLiteral(strings.TrimSpace(p.Address)); ok {
		// Bare CF IPv4 literal: use it directly, no lookup.
		ip = ip4
	} else {
		ip, err = resolveSpoofEndpointIP(strings.TrimSpace(p.Address))
		if err != nil {
			return "", "", 0, err
		}
	}
	// Pin-first, like loopback: the SNI tab's pinned decoy (or first
	// checked list entry) is the fake SNI the engine presents. Only
	// with no pin does the profile's own SNI/Host apply.
	sni = spoofPinSNI(&settings)
	if sni == "" {
		sni = normalizeFakeSNI(p.ServerName)
	}
	if sni == "" {
		sni = normalizeFakeSNI(p.Host)
	}
	if sni == "" {
		return "", "", 0, fmt.Errorf("profile %q needs a ServerName (or Host) to use as the fake SNI", p.Name)
	}
	// Pin-first for the endpoint too: a pinned/checked CF IP is the
	// dial target (parity with loopback; already resolved above, no
	// DNS happened). Only with no pin does the profile address's
	// resolved CF IP apply.
	port = p.Port
	if port < 1 || port > 65535 {
		port = 443
	}
	return ip, sni, port, nil
}

// resolvePlanATarget is Plan A's target rule: the pinned decoy SNI (or
// the profile's own SNI/Host with no pin) drives everything, and its
// address comes from DoH — never from the curated IP pool and never from
// the local resolver, which is the thing Plan A exists to bypass.
//
// The engine re-resolves on its own (RESOLVE_SNI); NekoGo resolves here
// only so that (a) a SNI with no usable A record fails as a readable
// Start error instead of a child process that dies after the startup
// timeout, and (b) the ENDPOINTS entry the config still carries is the
// honest current answer rather than a stale pool IP. The Cloudflare
// range check that Plan B applies is deliberately NOT applied: in Plan A
// the endpoint is by definition whatever the SNI resolves to.
func resolvePlanATarget(p *Profile, settings Settings) (ip, sni string, port int, err error) {
	sni = spoofPinSNI(&settings)
	if sni == "" {
		sni = normalizeFakeSNI(p.ServerName)
	}
	if sni == "" {
		sni = normalizeFakeSNI(p.Host)
	}
	if sni == "" {
		return "", "", 0, fmt.Errorf("plan A has nothing to resolve: check a decoy SNI in the SNI Spoofing tab, or set sni/host on profile %q", p.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*dohAttemptTimeout)
	defer cancel()
	ip, err = resolveA(ctx, sni)
	if err != nil {
		return "", "", 0, fmt.Errorf("plan A: %w (switch the SNI tab back to Plan B, or check a plain IP there)", err)
	}
	port = p.Port
	if isLoopbackAddress(p.Address) || port < 1 || port > 65535 {
		port = 443
	}
	return ip, sni, port, nil
}

// spoofPortForProfile decides the rewrite for one rendered config:
//   - CoreSpoof: always via the engine (loopback or domain — the
//     engine endpoints already hold the real target).
//   - Other cores: only loopback addresses while an engine runs.
//
// Returns ok=false when the config renders unchanged.
func spoofPortForProfile(profile *Profile, settings Settings) (port int, ok bool) {
	if profile == nil || !spoofEnabled(settings) {
		return 0, false
	}
	p, live := currentSpoofFrontPort()
	if !live {
		return 0, false
	}
	if profile.Core.IsSpoof() {
		return p, true
	}
	if shouldApplySNIFront(profile) {
		return p, true
	}
	return 0, false
}

// applySNIFrontSingBox rewrites the outbound "proxy" in the sing-box
// config map to point at 127.0.0.1:<spoofPort>. It does NOT change
// server_name, TLS settings, or Host headers — only where the raw
// bytes go.
func applySNIFrontSingBox(cfg map[string]any, spoofPort int) {
	obs, ok := cfg["outbounds"].([]any)
	if !ok {
		return
	}
	target := "proxy"
	for _, o := range obs {
		ob, ok := o.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := ob["tag"].(string); tag != target {
			continue
		}
		ob["server"] = "127.0.0.1"
		ob["server_port"] = spoofPort
	}
}

// applySNIFrontXray rewrites the outbound "proxy" in the xray config
// map to point at 127.0.0.1:<spoofPort>: vnext[0] for vless/vmess,
// servers[0] for trojan/shadowsocks (and whichever shape a raw
// outbound carries). Stream/security settings are untouched.
func applySNIFrontXray(cfg map[string]any, spoofPort int) {
	obs, ok := cfg["outbounds"].([]any)
	if !ok {
		return
	}
	for _, o := range obs {
		ob, ok := o.(map[string]any)
		if !ok {
			continue
		}
		if tag, _ := ob["tag"].(string); tag != "proxy" {
			continue
		}
		st, ok := ob["settings"].(map[string]any)
		if !ok {
			continue
		}
		rewriteAddrPort := func(listKey string) {
			arr, ok := st[listKey].([]any)
			if !ok || len(arr) == 0 {
				return
			}
			first, ok := arr[0].(map[string]any)
			if !ok {
				return
			}
			first["address"] = "127.0.0.1"
			first["port"] = spoofPort
		}
		rewriteAddrPort("vnext")
		rewriteAddrPort("servers")
	}
}
