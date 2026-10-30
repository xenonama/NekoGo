package main

// psiphon_probe.go — data-level readiness for the SNI spoof → Psiphon
// chain.
//
// psiphon-tunnel-core opens its SOCKS listener and answers both the
// greeting and CONNECT before a tunnel exists: measured against a dead
// upstream, CONNECT still replies success and the stream then stalls.
// A listener-only probe therefore calls the chain "up" while every page
// hangs. The probe below proves the path end to end — a SOCKS CONNECT to
// a canary followed by a full TLS handshake on the same socket. Only
// real bytes from the far side complete it.

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// The canary the chain must reach: an address that answers TLS directly,
// so a completed handshake proves Psiphon carried the stream through the
// spoofed tunnel instead of merely accepting the CONNECT.
const (
	psiphonProbeIP   = "1.1.1.1"
	psiphonProbePort = 443
	psiphonProbeSNI  = "one.one.one.one"
)

// psiphonProbeTotal bounds one probe. waitSocksOrExit polls between
// attempts, so a tunnel that is not carrying data yet costs one budget
// rather than hanging until the warmup deadline.
const psiphonProbeTotal = 8 * time.Second

// probePsiphonTunnel reports whether [port] carries a usable tunnel
// (SOCKS CONNECT plus a TLS handshake through it).
func probePsiphonTunnel(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(aetherLoopback, strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(psiphonProbeTotal))
	if err := socksConnect(conn, psiphonProbeIP, psiphonProbePort); err != nil {
		return false
	}
	tc := tls.Client(conn, &tls.Config{
		ServerName: psiphonProbeSNI,
		// Reachability, not identity: the canary is a fixed IP, and any
		// completed handshake proves the tunnel moved bytes both ways.
		InsecureSkipVerify: true,
	})
	return tc.Handshake() == nil
}

// socksConnect performs the SOCKS5 greeting and a CONNECT to host:port on
// an established connection, verifying the server's reply — what a SOCKS
// outbound does before any application bytes flow.
func socksConnect(conn net.Conn, host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("socks connect: port %d out of range", port)
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		return fmt.Errorf("socks greeting: version %d method %d", greeting[0], greeting[1])
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return fmt.Errorf("socks connect: target %q is not an IPv4 literal", host)
	}
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != 5 || head[1] != 0 {
		return fmt.Errorf("socks connect: version %d reply %d", head[0], head[1])
	}
	// Drain the bound address so the next read starts at the TLS stream.
	var err error
	switch head[3] {
	case 1:
		_, err = io.ReadFull(conn, make([]byte, 4+2))
	case 4:
		_, err = io.ReadFull(conn, make([]byte, 16+2))
	case 3:
		var l [1]byte
		if _, err = io.ReadFull(conn, l[:]); err == nil {
			_, err = io.ReadFull(conn, make([]byte, int(l[0])+2))
		}
	default:
		err = fmt.Errorf("socks connect: address type %d", head[3])
	}
	return err
}
