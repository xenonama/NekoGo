package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// defaultTestURL is the latency probe target when neither the profile nor
// the settings override it: tiny android connectivity-check endpoint.
const defaultTestURL = "https://www.gstatic.com/generate_204"

// Probe timeout bounds (BUG-020).
const (
	minProbeTimeoutMs = 100
	maxProbeTimeoutMs = 60000
)

// clampProbeTimeoutMs pins a probe timeout into the sane range before
// any time.Duration arithmetic (BUG-020): timeoutMs is int32, so adding
// a headroom constant wraps in 32-bit arithmetic near MaxInt32 and
// yields a negative/immediate-cancel context.
func clampProbeTimeoutMs(timeoutMs int32) int32 {
	if timeoutMs < minProbeTimeoutMs {
		return minProbeTimeoutMs
	}
	if timeoutMs > maxProbeTimeoutMs {
		return maxProbeTimeoutMs
	}
	return timeoutMs
}

// effectiveMixedPort applies the 2080 default shared by both config builders.
func effectiveMixedPort(p int) int {
	if p <= 0 {
		return 2080
	}
	return p
}

// waitForPort polls a TCP listener until it accepts or the timeout passes.
func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("port %s not listening after %s", addr, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// distinctPort returns a free loopback port different from every port in
// avoid. Two consecutive freePort calls can return the same port (nothing
// holds it in between), and Windows tends to hand out sequential ports, so
// callers binding more than one listener must exclude all of them.
func distinctPort(avoid ...int) (int, error) {
	for i := 0; i < 25; i++ {
		p, err := freePort()
		if err != nil {
			return 0, err
		}
		clash := false
		for _, a := range avoid {
			if p == a {
				clash = true
				break
			}
		}
		if !clash {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port distinct from %v", avoid)
}

// proxiedHeadOrGet issues one HTTP GET through an HTTP proxy at
// proxyAddr ("127.0.0.1:2080"), mirroring Throne's urlTest
// (core/internal/probe/urltest.go): a single GET whose latency runs
// from request start to headers received, then the body is closed
// unread. No HEAD attempt, no body read — those extras made NekoGo
// fail/slow on endpoints Throne reports fine.
//
// Status gating: none, with one exception — a bare "502 Bad Gateway"
// is not an origin response, it is sing's HTTP inbound synthesizing a
// failure when the outbound cannot be established (protocol/http
// responseWith; it has no Date/Server/Content-Type headers, only an
// empty body). Accepting it gave dead paths a ~2ms "latency".
func proxiedHeadOrGet(proxyAddr, rawURL string, timeout time.Duration) (time.Duration, error) {
	return proxiedHeadOrGetCtx(context.Background(), proxyAddr, rawURL, timeout)
}

// proxiedHeadOrGetCtx is proxiedHeadOrGet with a parent scope (BUG-031):
// cancelling ctx aborts the GET, so StopTest preempts the xray
// SpeedTest latency leg instead of waiting it out.
func proxiedHeadOrGetCtx(ctx context.Context, proxyAddr, rawURL string, timeout time.Duration) (time.Duration, error) {
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr})}
	client := &http.Client{Transport: transport, Timeout: timeout}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(get)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if isSyntheticProxy502(resp) {
		return 0, fmt.Errorf("proxy could not reach the target (synthetic 502 from the local inbound)")
	}
	return time.Since(start), nil
}

// isSyntheticProxy502 recognizes sing's HTTP-inbound failure response:
// status 502 with no origin headers at all (the only header Go may
// materialize is Content-Length: 0). Origin 502s carry Date/Server/
// Content-Type (or any other header) and remain valid measurements.
func isSyntheticProxy502(resp *http.Response) bool {
	if resp.StatusCode != http.StatusBadGateway {
		return false
	}
	if resp.Header.Get("Date") != "" || resp.Header.Get("Server") != "" || resp.Header.Get("Content-Type") != "" {
		return false
	}
	return len(resp.Header) == 0 || (len(resp.Header) == 1 && resp.Header.Get("Content-Length") == "0")
}
