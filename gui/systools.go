package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"

	gen "nekogo/thriftgen"
)

// Advanced Mode Phase D: system tools (Console -> System sub-tab).
//
// Wired RPCs: EnableSystemProxy, DisableSystemProxy, SetSystemDNS,
// IsPrivileged, IsSupported, GenWgKeyPair, CacheHTTP — all previously
// without GUI callers. Same transport rule as Phase C (nettools.go):
// dedicated throwaway transports per call, never the shared client.
// These are all quick ops, so the default 30s socket conf suffices.
//
// xray/mihomo gaps: upstream Xray-core has no system-proxy/DNS/cache
// RPCs and mihomo exposes only its Clash REST controller. Those three
// legs return a clear "sing-box only" error and the UI disables their
// controls on both. IsPrivileged, IsSupported and GenWgKeyPair have local
// client-side legs (documented below), so they work with any core — and
// even with no core running.

// errSingBoxOnly is returned for legs that need the nekobox_core RPCs.
var errSingBoxOnly = fmt.Errorf("only available with the sing-box core")

// noCoreRPCs reports whether the active engine exposes none of the
// nekobox_core Thrift RPCs: xray speaks only its stats gRPC, mihomo only
// its Clash REST controller. Their system legs run locally instead.
func noCoreRPCs(core CoreType) bool {
	return core == CoreXray || core == CoreMihomo
}

// validateMixedPort rejects out-of-range inbound ports BEFORE any
// int32 cast (BUG-019): a huge int from a hand-edited config would
// otherwise wrap and program the OS proxy with the wrong port.
func validateMixedPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid mixed port %d: want 1..65535", port)
	}
	return nil
}

// normalizeInboundAddress resolves the listen address for local
// inbounds (Throne parity: LAN share). Empty means loopback.
// Anything that is not a parseable IP is rejected so a typo cannot
// render an unbindable config that fails late at Connect.
func normalizeInboundAddress(addr string) (string, error) {
	t := strings.TrimSpace(addr)
	if t == "" {
		return "127.0.0.1", nil
	}
	if net.ParseIP(t) == nil {
		return "", fmt.Errorf("invalid inbound address %q: must be an IP (e.g. 127.0.0.1 or 0.0.0.0)", addr)
	}
	return t, nil
}

// validateDNSServer rejects upstream values sing-box would choke
// on. Emptiness is legal and means "system default" (no dns block
// rendered). The same parser the renderer uses decides acceptance, so
// a value that saves can never fail at Connect (the old fallback
// silently swapped unknown values for a public resolver).
// Surrounding whitespace is trimmed rather than rejected — a pasted
// value routinely carries it — while an over-long one cannot be a
// server anyone meant.
func validateDNSServer(v string) error {
	t := strings.TrimSpace(v)
	if t == "" {
		return nil
	}
	if len(t) > 4096 {
		return fmt.Errorf("invalid DNS server %q: too long (max 4096 bytes)", v)
	}
	_, err := parseDNSAddr(t)
	return err
}

// --- sing-box legs (dedicated transports) ---

// EnableSystemProxy points the OS proxy at the running core's mixed
// inbound (HTTP+SOCKS share the sing-box mixed port).
func (m *SingBoxManager) EnableSystemProxy(settings Settings) error {
	if err := validateMixedPort(settings.MixedPort); err != nil {
		return err
	}
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return err
	}
	defer done()
	port := settings.MixedPort
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.EnableSystemProxy(ctx, &gen.SystemProxy{
		Address:      "127.0.0.1",
		Port:         int32(port),
		SupportSocks: true,
	})
	if err != nil {
		return err
	}
	if resp.GetError() != "" {
		return fmt.Errorf("%s", resp.GetError())
	}
	return nil
}

// DisableSystemProxy restores the OS proxy settings.
func (m *SingBoxManager) DisableSystemProxy() error {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.DisableSystemProxy(ctx, &gen.EmptyReq{})
	if err != nil {
		return err
	}
	if resp.GetError() != "" {
		return fmt.Errorf("%s", resp.GetError())
	}
	return nil
}

// SetSystemDNS applies (clear=false) or restores (clear=true) the
// core's local DNS on the default interface. NOTE: the Thrift request
// carries only the clear flag — there is NO address field, so a
// custom DNS address cannot be sent. The UI therefore offers
// Apply/Restore, not an address input.
func (m *SingBoxManager) SetSystemDNS(clear bool) error {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = client.SetSystemDNS(ctx, &gen.SetSystemDNSRequest{Clear: clear})
	return err
}

// IsPrivileged reports the CORE process elevation.
func (m *SingBoxManager) IsPrivileged() (bool, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return false, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.IsPrivileged(ctx, &gen.EmptyReq{})
	if err != nil {
		return false, err
	}
	return resp.GetHasPrivilege(), nil
}

// IsSupported probes one capability name server-side. Only "naive" is
// a real check (everything else is unconditionally true there).
func (m *SingBoxManager) IsSupported(kind string) (bool, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return false, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.IsSupported(ctx, &gen.Type{Type: kind})
	if err != nil {
		return false, err
	}
	return resp.GetOk(), nil
}

// GenWgKeyPair generates a WireGuard keypair server-side.
func (m *SingBoxManager) GenWgKeyPair() (*WgKeyPair, error) {
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.GenWgKeyPair(ctx, &gen.EmptyReq{})
	if err != nil {
		return nil, err
	}
	if resp.GetError() != "" {
		return nil, fmt.Errorf("%s", resp.GetError())
	}
	return &WgKeyPair{PrivateKey: resp.GetPrivateKey(), PublicKey: resp.GetPublicKey()}, nil
}

// CacheHTTP fetches url into the core's ruleset cache, returning the
// local path. use_default_outbound=false routes the fetch THROUGH the
// active core proxy (note: the server log labels this flag the other
// way round — "with proxy" when direct — a server-side naming wart
// left untouched here).
func (m *SingBoxManager) CacheHTTP(url string) (string, error) {
	if url == "" {
		return "", fmt.Errorf("empty URL")
	}
	client, done, err := m.sbTestDial(false)
	if err != nil {
		return "", err
	}
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := client.CacheHTTP(ctx, &gen.CacheURLRequest{
		HTTPURL:            url,
		UseDefaultOutbound: false,
	})
	if err != nil {
		return "", err
	}
	if resp.GetFilePath() == "" {
		return "", fmt.Errorf("empty cache path")
	}
	return resp.GetFilePath(), nil
}

// --- local client-side legs (either core, or none) ---
// isGuiElevated is platform-specific: systools_windows.go on Windows
// (process token check), systools_other.go elsewhere (always false).

// genWgKeyPairLocal generates a WireGuard keypair without any core:
// 32 random bytes with WG clamping, base64; public is X25519 against
// the basepoint. This mirrors the server's wg.GeneratePrivateKey
// semantics using only the standard library (no new module deps).
func genWgKeyPairLocal() (*WgKeyPair, error) {
	var sk [32]byte
	if _, err := rand.Read(sk[:]); err != nil {
		return nil, fmt.Errorf("random key: %w", err)
	}
	sk[0] &= 248
	sk[31] &= 127
	sk[31] |= 64
	priv, err := ecdh.X25519().NewPrivateKey(sk[:])
	if err != nil {
		return nil, fmt.Errorf("curve key: %w", err)
	}
	return &WgKeyPair{
		PrivateKey: base64.StdEncoding.EncodeToString(sk[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()),
	}, nil
}

// isSupportedLocal mirrors the server's IsSupported logic
// (core/server/server.go): only "naive" is a real check, and
// CheckNaive() returns false in BOTH build variants (naive.go and
// naive_stub.go), so the mirror is exact for every project-built core.
func isSupportedLocal(kind string) bool {
	return kind != "naive"
}

// osSystemProxy is platform-specific (BUG-044): systools_windows.go reads
// the ACTUAL OS proxy switch from HKCU Internet Settings ProxyEnable plus
// ProxyServer (integration BUG-010 ground truth); systools_other.go
// reports an unsupported error elsewhere.

// --- CoreManager routing ---

// sysTarget snapshots the active core and running flag without
// requiring a connection (some legs are core-independent).
func (m *CoreManager) sysTarget() (core CoreType, running bool, err error) {
	if m.singboxMgr == nil || m.xrayMgr == nil {
		return CoreSingBox, false, fmt.Errorf("core managers not initialized")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.effectiveCoreLocked(), m.running, nil
}

// requireSingBox errors unless the sing-box core is actively running.
func (m *CoreManager) requireSingBox() (*SingBoxManager, error) {
	core, running, err := m.sysTarget()
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, fmt.Errorf("no active connection")
	}
	if noCoreRPCs(core) {
		return nil, errSingBoxOnly
	}
	return m.singboxMgr, nil
}

// EnableSystemProxy toggles the OS proxy via the running sing-box core.
func (m *CoreManager) EnableSystemProxy(settings Settings, enable bool) error {
	sb, err := m.requireSingBox()
	if err != nil {
		return err
	}
	if !enable {
		return sb.DisableSystemProxy()
	}
	return sb.EnableSystemProxy(settings)
}

// SetSystemDNS applies (clear=false) or restores (clear=true) the
// core's DNS. Sing-box only; the RPC takes no address.
func (m *CoreManager) SetSystemDNS(clear bool) error {
	sb, err := m.requireSingBox()
	if err != nil {
		return err
	}
	return sb.SetSystemDNS(clear)
}

// IsPrivileged reports elevation: the CORE process via RPC when
// sing-box is active, otherwise this GUI process (xray, mihomo or offline).
func (m *CoreManager) IsPrivileged() (bool, error) {
	core, running, err := m.sysTarget()
	if err != nil {
		return false, err
	}
	if running && !noCoreRPCs(core) {
		return m.singboxMgr.IsPrivileged()
	}
	return isGuiElevated()
}

// IsSupported probes one capability: server-side when sing-box is
// active, otherwise the exact local mirror.
func (m *CoreManager) IsSupported(kind string) (bool, error) {
	core, running, err := m.sysTarget()
	if err != nil {
		return false, err
	}
	if running && !noCoreRPCs(core) {
		return m.singboxMgr.IsSupported(kind)
	}
	return isSupportedLocal(kind), nil
}

// GenWgKeyPair generates a WireGuard pair: server-side when sing-box
// is active, otherwise locally (identical semantics).
func (m *CoreManager) GenWgKeyPair() (*WgKeyPair, error) {
	core, running, err := m.sysTarget()
	if err != nil {
		return nil, err
	}
	if running && !noCoreRPCs(core) {
		return m.singboxMgr.GenWgKeyPair()
	}
	return genWgKeyPairLocal()
}

// CacheHTTP fetches url into the core ruleset cache. Sing-box only.
func (m *CoreManager) CacheHTTP(url string) (string, error) {
	sb, err := m.requireSingBox()
	if err != nil {
		return "", err
	}
	return sb.CacheHTTP(url)
}
