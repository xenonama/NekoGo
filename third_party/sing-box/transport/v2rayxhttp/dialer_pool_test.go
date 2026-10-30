package xhttp

import (
	"net/http"
	"testing"

	"golang.org/x/net/http2"
)

// foreignPool is a ClientConnPool with an unrelated dynamic type.
type foreignPool struct{}

func (foreignPool) GetClientConn(*http.Request, string) (*http2.ClientConn, error) {
	return nil, nil
}

func (foreignPool) MarkDead(*http2.ClientConn) {}

// TestTransportConnPoolRejectsUnknown — BUG-032: a nil Transport and a
// Transport with no pool report ok=false instead of leaking sockets or
// wild-casting after an x/net reshape.
func TestTransportConnPoolRejectsUnknown(t *testing.T) {
	if _, ok := transportConnPool(nil); ok {
		t.Fatal("nil transport accepted, want rejection")
	}
	if _, ok := transportConnPool(&http2.Transport{}); ok {
		t.Fatal("nil pool accepted, want rejection")
	}
}

// TestAsLocalPoolRejectsForeign — BUG-032: nil and foreign pool
// implementations are rejected before the eface cast.
func TestAsLocalPoolRejectsForeign(t *testing.T) {
	if _, ok := asLocalPool(nil); ok {
		t.Fatal("nil pool accepted, want rejection")
	}
	if _, ok := asLocalPool(foreignPool{}); ok {
		t.Fatal("foreign pool accepted, want rejection")
	}
}

// TestCloseFallsBackOnUnknownPool — BUG-032: Close on an http2 transport
// without a recognizable pool takes the public CloseIdleConnections path
// and must not panic.
func TestCloseFallsBackOnUnknownPool(t *testing.T) {
	c := &DefaultDialerClient{client: &http.Client{Transport: &http2.Transport{}}}
	c.Close() // must not panic
	if !c.IsClosed() {
		t.Fatal("client not marked closed after Close")
	}
}
