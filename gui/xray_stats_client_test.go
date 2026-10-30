package main

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestXrayStatsClientReusesConnection — the stats poll runs every
// second: the client must dial ONCE and reuse the connection, and a
// failed call must drop it so the next poll reconnects (a dead
// connection must not poison the session).
func TestXrayStatsClientReusesConnection(t *testing.T) {
	old := grpcDial
	defer func() { grpcDial = old }()

	dials := 0
	// A dead address: the dial succeeds (gRPC dials lazily) but the
	// call fails -> the connection must be dropped.
	grpcDial = func(addr string) (*grpc.ClientConn, error) {
		dials++
		return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	c := NewXrayStatsClient("127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := c.QueryStats(ctx, "", false); err == nil {
		t.Fatal("expected the query to fail against a dead address")
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
	// The failed call dropped the connection: the next poll dials again.
	if _, err := c.QueryStats(ctx, "", false); err == nil {
		t.Fatal("expected the query to fail against a dead address")
	}
	if dials != 2 {
		t.Fatalf("dials after a failed call = %d, want 2 (the dead connection must be dropped)", dials)
	}
}
