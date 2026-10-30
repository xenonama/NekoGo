package internal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"nekobox_core/internal/boxbox"

	"github.com/sagernet/sing-box/common/settings"
	"github.com/sagernet/sing/common/metadata"
)

func closedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// TestCacheHttpBoolUnreachableReturnsError — a failed download must come
// back as an error, never os.Exit (the old log.Fatalf killed the whole
// core from an RPC-reachable path on transient network errors).
func TestCacheHttpBoolUnreachableReturnsError(t *testing.T) {
	old := GetRulesetCachedir()
	SetRulesetCachedir(t.TempDir())
	defer SetRulesetCachedir(old)
	var fetched bool
	url := fmt.Sprintf("http://127.0.0.1:%d/unreachable.dat", closedLoopbackPort(t))
	path, err := CacheHttpBool(url, false, &fetched)
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
	if path != "" {
		t.Fatalf("path = %q, want empty on failure", path)
	}
	t.Logf("returned error as expected: %v", err)
}

// TestUrlToPathRejectsEscape — "../etc/passwd" must not resolve
// outside the ruleset cache dir (P3-11).
func TestUrlToPathRejectsEscape(t *testing.T) {
	old := GetRulesetCachedir()
	SetRulesetCachedir(t.TempDir())
	defer SetRulesetCachedir(old)
	if _, err := urlToPath("../etc/passwd"); err == nil {
		t.Fatal("expected error for ../etc/passwd, got nil")
	}
	if _, err := urlToPath("https://example.com/rules.dat"); err != nil {
		t.Fatalf("legit URL rejected: %v", err)
	}
}

// TestCacheHttpBoolCachesOnSuccess — on-disk caching behavior is
// unchanged: first call downloads (fetched=true), second is cached.
func TestCacheHttpBoolCachesOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ruleset-bytes"))
	}))
	defer srv.Close()
	old := GetRulesetCachedir()
	SetRulesetCachedir(t.TempDir())
	defer SetRulesetCachedir(old)
	var fetched bool
	path, err := CacheHttpBool(srv.URL+"/rules.dat", false, &fetched)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("first download should report fetched=true")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "ruleset-bytes" {
		t.Fatalf("cached content = %q", raw)
	}
	path2, err := CacheHttpBool(srv.URL+"/rules.dat", false, &fetched)
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Fatal("second call should be served from cache (fetched=false)")
	}
	if path2 != path {
		t.Fatalf("cache path changed: %q vs %q", path2, path)
	}
}

// TestSetSystemProxyHandlesConstructorError (BUG-003): when controller
// construction fails — or returns (nil, nil) — SetSystemProxy must
// return an error, never panic on a nil dereference.
func TestSetSystemProxyHandlesConstructorError(t *testing.T) {
	oldBox := BoxInstance
	oldCtl := SystemProxyController
	oldCtor := newSystemProxy
	oldAddr, oldPort, oldSocks := SystemProxyAddr, SystemProxyPort, SystemProxySupportSOCKS
	defer func() {
		BoxInstance = oldBox
		SystemProxyController = oldCtl
		newSystemProxy = oldCtor
		SystemProxyAddr, SystemProxyPort, SystemProxySupportSOCKS = oldAddr, oldPort, oldSocks
	}()
	// Bypass the no-instance gate so the constructor path is exercised.
	BoxInstance = &boxbox.Box{}
	SystemProxyController = nil

	newSystemProxy = func(context.Context, metadata.Socksaddr, bool, []string) (settings.SystemProxy, error) {
		return nil, errors.New("constructor boom")
	}
	if err := SetSystemProxy(context.Background(), "127.0.0.1", 2080, false); err == nil {
		t.Fatal("want constructor error, got nil (possible nil-deref panic path)")
	}

	newSystemProxy = func(context.Context, metadata.Socksaddr, bool, []string) (settings.SystemProxy, error) {
		return nil, nil
	}
	if err := SetSystemProxy(context.Background(), "127.0.0.1", 2080, false); err == nil {
		t.Fatal("want unavailable-controller error for (nil, nil), got nil")
	}
}
