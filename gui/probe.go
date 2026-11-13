package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
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

// inboundPortGrace bounds how long a connect waits for an inbound port
// that is occupied at the moment it is checked. A port held by the
// previous attempt's core — killed, still exiting, socket not yet
// released — frees within milliseconds, and that gap is exactly what a
// user means by "I clicked Connect many times and it finally worked":
// the clicks were not fixing anything, they were the waiting. A port
// held by another program never frees, so the window ends with the port
// named instead of another silent retry.
var inboundPortGrace = 2 * time.Second

// portOccupied reports whether a failed bind means the port is taken.
// Windows phrases it three ways (the API error, the socket error, and
// Go's wrapper), and anything else — an address this process may not
// use, a disabled adapter — is not a port collision, so it must not be
// reported as one or waited out; the core itself gives those details.
func portOccupied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "in use") || strings.Contains(msg, "only one usage")
}

// occupiedBindPorts probe-binds every address and returns the ones a
// collision accounts for. Other bind failures are dropped: they are the
// core's to report, and refusing a connect over them would be a guess.
func occupiedBindPorts(addrs []string) []string {
	var busy []string
	for _, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err != nil {
			if portOccupied(err) {
				busy = append(busy, a)
			}
			continue
		}
		_ = l.Close()
	}
	return busy
}

// bindPortHeldError describes a port that stayed busy for the whole
// grace window. The number is what the user has to act on, so it leads;
// the setting is named by the label the Settings tab shows for it.
func bindPortHeldError(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = addr
	}
	return fmt.Errorf("port %s in use — still held after waiting %s. Close whatever owns it (a core left by the last connect, another proxy, a second NekoGo window) or set a different Mixed port in Settings", port, inboundPortGrace)
}

// waitBindPortsFree holds a connect until every address is bindable or
// inboundPortGrace passes. Returns nil once they are all free.
func waitBindPortsFree(addrs []string) error {
	if len(addrs) == 0 {
		return nil
	}
	deadline := time.Now().Add(inboundPortGrace)
	for {
		busy := occupiedBindPorts(addrs)
		if len(busy) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return bindPortHeldError(busy[0])
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// singBoxBindPorts renders the core config once to learn which addresses
// this connect is about to bind, so the pre-flight checks exactly what
// the engine will: a full raw core override names its own ports, and a
// TUN inbound names none.
func singBoxBindPorts(profile *Profile, settings Settings) ([]string, error) {
	cfg, err := BuildSingBoxConfig(profile, settings)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Inbounds []struct {
			Listen     string `json:"listen"`
			ListenPort *int   `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
		return nil, err
	}
	var addrs []string
	for _, ib := range doc.Inbounds {
		if ib.ListenPort == nil || *ib.ListenPort <= 0 || *ib.ListenPort > 65535 {
			continue
		}
		host := strings.TrimSpace(ib.Listen)
		if host == "" {
			host = "127.0.0.1"
		}
		addrs = append(addrs, net.JoinHostPort(host, strconv.Itoa(*ib.ListenPort)))
	}
	return addrs, nil
}

// preflightSingBoxPorts refuses a sing-box connect whose inbound port is
// held by something else, before a core process exists. Both sing-box
// paths run it; xray and mihomo already refused this way and the custom
// core was the odd one out, failing seconds later with the engine's own
// bind error after the RPC handshake. Config-build failures are not
// reported here: Start renders the config again and owns that message.
func preflightSingBoxPorts(profile *Profile, settings Settings) error {
	if profile == nil {
		return nil
	}
	addrs, err := singBoxBindPorts(profile, settings)
	if err != nil {
		return nil
	}
	return waitBindPortsFree(addrs)
}

// proxiedHeadOrGet issues one HTTP GET through an HTTP proxy at// proxyAddr ("127.0.0.1:2080"), mirroring Throne's urlTest
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
