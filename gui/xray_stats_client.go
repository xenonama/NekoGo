package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/golang/protobuf/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// This file is a minimal, dependency-light client for Xray's gRPC
// StatsService (xray/app/stats/command/command.proto).
//
// The messages below use the legacy github.com/golang/protobuf API whose
// wire encoding is driven purely by struct tags, so no generated descriptor
// bytes need to be vendored. The service method name matches the Xray proto:
//
//	service StatsService {
//	  rpc QueryStats(QueryStatsRequest) returns (QueryStatsResponse);
//	}
//
// Full proto package path: xray.app.stats.command

// Stat is one traffic counter: "outbound>>>proxy>>>traffic>>>uplink".
type Stat struct {
	Name  string `protobuf:"bytes,1,opt,name=name" json:"name,omitempty"`
	Value int64  `protobuf:"varint,2,opt,name=value" json:"value,omitempty"`

	XXX_unrecognized []byte `json:"-"`
}

func (m *Stat) Reset()         { *m = Stat{} }
func (m *Stat) String() string { return proto.CompactTextString(m) }
func (*Stat) ProtoMessage()    {}

// QueryStatsRequest asks for counters matching Pattern.
type QueryStatsRequest struct {
	Pattern string `protobuf:"bytes,1,opt,name=pattern" json:"pattern,omitempty"`
	Reset_  bool   `protobuf:"varint,2,opt,name=reset" json:"reset,omitempty"`

	XXX_unrecognized []byte `json:"-"`
}

func (m *QueryStatsRequest) Reset()         { *m = QueryStatsRequest{} }
func (m *QueryStatsRequest) String() string { return proto.CompactTextString(m) }
func (*QueryStatsRequest) ProtoMessage()    {}

// QueryStatsResponse carries the matched counters.
type QueryStatsResponse struct {
	Stat []*Stat `protobuf:"bytes,1,rep,name=stat" json:"stat,omitempty"`

	XXX_unrecognized []byte `json:"-"`
}

func (m *QueryStatsResponse) Reset()         { *m = QueryStatsResponse{} }
func (m *QueryStatsResponse) String() string { return proto.CompactTextString(m) }
func (*QueryStatsResponse) ProtoMessage()    {}

// XrayStatsClient queries an Xray API endpoint.
//
// The gRPC connection is REUSED across calls: the stats poll runs
// every second, and a fresh client (plus its connection) per call
// meant 60 connection setups per minute for one number. The connection
// is created on first use and dropped again when a call fails (a dead
// connection must not poison the next poll) or on Close.
type XrayStatsClient struct {
	addr string
	mu   sync.Mutex
	conn *grpc.ClientConn
}

// NewXrayStatsClient creates a client for e.g. "127.0.0.1:10085".
func NewXrayStatsClient(addr string) *XrayStatsClient {
	return &XrayStatsClient{addr: addr}
}

// grpcDial is the gRPC dialer (a variable so tests can count dials).
var grpcDial = func(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}

func (c *XrayStatsClient) dial() (*grpc.ClientConn, error) {
	conn, err := grpcDial(c.addr)
	if err != nil {
		return nil, fmt.Errorf("dial xray api %s: %w", c.addr, err)
	}
	return conn, nil
}

// client returns the cached connection, dialing on first use.
func (c *XrayStatsClient) client() (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn, nil
	}
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return conn, nil
}

// dropConn closes and forgets the cached connection (after a failed
// call, or on Close).
func (c *XrayStatsClient) dropConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// Close releases the cached connection.
func (c *XrayStatsClient) Close() {
	c.dropConn()
}

// QueryStats fetches counters matching pattern ("" fetches all).
func (c *XrayStatsClient) QueryStats(ctx context.Context, pattern string, reset bool) (*QueryStatsResponse, error) {
	conn, err := c.client()
	if err != nil {
		return nil, err
	}
	req := &QueryStatsRequest{Pattern: pattern, Reset_: reset}
	var resp QueryStatsResponse
	if err := conn.Invoke(ctx, "/xray.app.stats.command.StatsService/QueryStats", req, &resp); err != nil {
		// A failed call may mean the connection died (core restart):
		// drop it so the next poll reconnects.
		c.dropConn()
		return nil, fmt.Errorf("QueryStats: %w", err)
	}
	return &resp, nil
}

// parseXrayStats folds a QueryStats response into lifetime totals plus
// per-tag maps keyed by inbound/outbound tag. Counter names look like
// "inbound>>>http-in>>>traffic>>>uplink". Malformed names are skipped,
// and the internal "api-in" listener is excluded (its loopback traffic
// is not user traffic).
func parseXrayStats(resp *QueryStatsResponse) (up, down int64, upByTag, downByTag map[string]int64) {
	upByTag = map[string]int64{}
	downByTag = map[string]int64{}
	if resp == nil {
		return 0, 0, upByTag, downByTag
	}
	for _, s := range resp.Stat {
		if s == nil {
			continue
		}
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[2] != "traffic" {
			continue
		}
		kind, tag, dir := parts[0], parts[1], parts[3]
		if kind != "inbound" && kind != "outbound" {
			continue
		}
		if tag == "" || tag == "api-in" {
			continue
		}
		switch dir {
		case "uplink":
			up += s.Value
			upByTag[tag] += s.Value
		case "downlink":
			down += s.Value
			downByTag[tag] += s.Value
		}
	}
	return up, down, upByTag, downByTag
}
