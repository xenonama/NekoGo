package main

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"testing"
)

// Pure/local-leg tests for Phase D (no core process needed).

// TestGenWgKeyPairLocalShape checks WireGuard key semantics: 32-byte
// clamped private, base64 lengths, public re-derives from private,
// and two calls differ.
func TestGenWgKeyPairLocalShape(t *testing.T) {
	a, err := genWgKeyPairLocal()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	b, err := genWgKeyPairLocal()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if a.PrivateKey == b.PrivateKey || a.PublicKey == b.PublicKey {
		t.Fatal("two keypairs must differ")
	}
	sk, err := base64.StdEncoding.DecodeString(a.PrivateKey)
	if err != nil {
		t.Fatalf("private base64: %v", err)
	}
	if len(sk) != 32 {
		t.Fatalf("private len = %d, want 32", len(sk))
	}
	if sk[0]&7 != 0 || sk[31]&128 != 0 || sk[31]&64 == 0 {
		t.Fatal("private key clamping bits wrong")
	}
	if len(a.PrivateKey) != 44 || len(a.PublicKey) != 44 {
		t.Fatalf("base64 lens = %d/%d, want 44/44", len(a.PrivateKey), len(a.PublicKey))
	}
	priv, err := ecdh.X25519().NewPrivateKey(sk)
	if err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	if got := base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()); got != a.PublicKey {
		t.Fatal("public key does not re-derive from private")
	}
}

// TestIsSupportedLocalMirror pins the exact server logic
// (core/server/server.go IsSupported + naive.go/naive_stub.go, where
// CheckNaive is false in both build variants).
func TestIsSupportedLocalMirror(t *testing.T) {
	for _, kind := range []string{"wireguard", "vless", "tailscale", "shadowsocks", ""} {
		if kind == "naive" {
			continue
		}
		if !isSupportedLocal(kind) {
			t.Errorf("isSupportedLocal(%q) = false, want true", kind)
		}
	}
	if isSupportedLocal("naive") {
		t.Error(`isSupportedLocal("naive") = true, want false`)
	}
}

// TestValidateMixedPort — BUG-019: only 1..65535 passes; anything else
// would wrap on the int32 cast for the proxy RPC.
func TestValidateMixedPort(t *testing.T) {
	for _, p := range []int{1, 2080, 65535} {
		if err := validateMixedPort(p); err != nil {
			t.Errorf("validateMixedPort(%d) = %v, want nil", p, err)
		}
	}
	for _, p := range []int{0, -1, 65536, 70000, 1 << 30} {
		if err := validateMixedPort(p); err == nil {
			t.Errorf("validateMixedPort(%d) = nil, want error", p)
		}
	}
}

// TestEnableSystemProxyValidatesPort — BUG-019: a bad MixedPort fails
// before any dial, so no core needs to run for the check to fire.
func TestEnableSystemProxyValidatesPort(t *testing.T) {
	m := NewSingBoxManager("", make(chan LogEntry, 4))
	s := DefaultSettings()
	s.MixedPort = 70000
	if err := m.EnableSystemProxy(s); err == nil {
		t.Fatal("EnableSystemProxy(MixedPort=70000) = nil, want range error")
	}
}

// TestNoCoreRPCs pins which engines the system legs may dial: only the
// nekobox_core Thrift server has these RPCs. xray speaks stats gRPC and
// mihomo its Clash REST controller, so both take the local legs.
func TestNoCoreRPCs(t *testing.T) {
	for _, c := range []struct {
		core CoreType
		want bool
	}{
		{CoreSingBox, false},
		{CoreXray, true},
		{CoreSpoof, false}, // a spoof group keeps its sing-box transport
		{CoreMihomo, true},
	} {
		if got := noCoreRPCs(c.core); got != c.want {
			t.Errorf("noCoreRPCs(%v) = %v, want %v", c.core, got, c.want)
		}
	}
}

// TestMihomoSystemToolsRouting: with mihomo active the sing-box-only legs
// must refuse clearly, and the local legs must answer WITHOUT dialing the
// idle Thrift core (which would report "core not running" instead).
func TestMihomoSystemToolsRouting(t *testing.T) {
	logCh := make(chan LogEntry, 8)
	cores := NewCoreManager(NewSingBoxManager("", logCh), NewXrayManager("", t.TempDir(), logCh))
	cores.SetMihomoManager(NewMihomoManager(logCh))
	cores.mu.Lock()
	cores.running, cores.activeCore = true, CoreMihomo
	cores.mu.Unlock()

	if err := cores.SetSystemDNS(false); !errors.Is(err, errSingBoxOnly) {
		t.Errorf("SetSystemDNS() err = %v, want errSingBoxOnly", err)
	}
	if _, err := cores.CacheHTTP("https://example.com/ruleset.srs"); !errors.Is(err, errSingBoxOnly) {
		t.Errorf("CacheHTTP() err = %v, want errSingBoxOnly", err)
	}
	if err := cores.EnableSystemProxy(DefaultSettings(), true); !errors.Is(err, errSingBoxOnly) {
		t.Errorf("EnableSystemProxy() err = %v, want errSingBoxOnly", err)
	}
	if _, err := cores.IsPrivileged(); err != nil {
		t.Errorf("IsPrivileged() err = %v, want the local elevation check", err)
	}
	if ok, err := cores.IsSupported("wireguard"); err != nil || !ok {
		t.Errorf("IsSupported(wireguard) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := cores.IsSupported("naive"); err != nil || ok {
		t.Errorf("IsSupported(naive) = %v, %v; want the local mirror (false, nil)", ok, err)
	}
	pair, err := cores.GenWgKeyPair()
	if err != nil || pair == nil || pair.PrivateKey == "" || pair.PublicKey == "" {
		t.Fatalf("GenWgKeyPair() = %+v, %v; want a locally generated pair", pair, err)
	}
}
