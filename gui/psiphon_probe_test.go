package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"
)

// fakeSocks accepts one client and speaks SOCKS5 up to a successful
// CONNECT reply, then hands the raw stream to [after]. The CONNECT
// target is reported on the returned channel.
func fakeSocks(t *testing.T, after func(net.Conn)) (int, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	target := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		var greeting [3]byte
		if _, err := io.ReadFull(c, greeting[:]); err != nil || greeting[0] != 5 {
			return
		}
		if _, err := c.Write([]byte{5, 0}); err != nil {
			return
		}
		var head [4]byte
		if _, err := io.ReadFull(c, head[:]); err != nil || head[0] != 5 || head[1] != 1 {
			return
		}
		var host string
		switch head[3] {
		case 1:
			var ip [4]byte
			if _, err := io.ReadFull(c, ip[:]); err != nil {
				return
			}
			host = net.IP(ip[:]).String()
		case 3:
			var l [1]byte
			if _, err := io.ReadFull(c, l[:]); err != nil {
				return
			}
			name := make([]byte, int(l[0]))
			if _, err := io.ReadFull(c, name); err != nil {
				return
			}
			host = string(name)
		default:
			return
		}
		var p [2]byte
		if _, err := io.ReadFull(c, p[:]); err != nil {
			return
		}
		target <- net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:]))))
		if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			return
		}
		_ = c.SetDeadline(time.Time{})
		after(c)
	}()
	return ln.Addr().(*net.TCPAddr).Port, target
}

func testTLSCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "probe.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// A SOCKS CONNECT that succeeds and then carries no data is exactly the
// false-ready state Psiphon shows before its tunnel exists: the probe
// must reject it.
func TestProbePsiphonTunnelRejectsConnectWithoutData(t *testing.T) {
	port, want := fakeSocks(t, func(net.Conn) {}) // closes right after the reply
	if probePsiphonTunnel(port) {
		t.Fatal("probe = true for a CONNECT that carries no data")
	}
	if got := <-want; got != "1.1.1.1:443" {
		t.Fatalf("CONNECT target = %q, want 1.1.1.1:443", got)
	}
}

// A tunnel that really carries bytes completes the probe's TLS
// handshake with the canary.
func TestProbePsiphonTunnelAcceptsCarryingTunnel(t *testing.T) {
	cert := testTLSCert(t)
	port, _ := fakeSocks(t, func(c net.Conn) {
		_ = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}}).Handshake()
	})
	if !probePsiphonTunnel(port) {
		t.Fatal("probe = false for a tunnel that carries a TLS stream")
	}
}

// Anything that is not a SOCKS5 server (or refuses the CONNECT target)
// is never ready.
func TestProbePsiphonTunnelRejectsNonSocks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			_ = c.Close()
		}
	}()
	if probePsiphonTunnel(ln.Addr().(*net.TCPAddr).Port) {
		t.Fatal("probe = true for a non-SOCKS listener")
	}
}
