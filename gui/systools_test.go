package main

import (
	"crypto/ecdh"
	"encoding/base64"
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
