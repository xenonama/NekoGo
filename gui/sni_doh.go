package main

// sni_doh.go — DNS-over-HTTPS A-record lookup for Plan A.
//
// Plan A hands the engine only the decoy SNI and lets it derive the
// connect endpoint (core "RESOLVE_SNI", CORE_REPORT.md section 17).
// NekoGo needs the same answer two other times: before spawning the
// engine (to report a dead SNI as a Start error instead of a silent
// 10 s timeout) and when the SNI tab scans a row. Both must agree with
// what the core will do, so this follows core/src/net.rs: dns-json
// providers dialed at their pinned anycast address so a hijacked local
// resolver cannot steer the answer, successes cached for the process,
// failures never cached.
//
// Where it deliberately goes further: an answer is only usable if it can
// actually be a decoy endpoint (spoofEndpointUsable). The resolved address
// IS what gets dialed, so a loopback/RFC1918 reply — a poisoned resolver or
// a captive portal — must move on to the next provider instead of handing
// the tunnel to the interceptor. Plan B has always refused those answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider JSON endpoints. Cloudflare, Google and Quad9 all serve the same
// `application/dns-json` shape (Status + Answer[].type/data), so firstARecord
// parses any of them. Diversity is the point: a network that intercepts
// Cloudflare DoH (common on censored links) usually still reaches Google or
// Quad9, and resolveAOS is the last resort behind all of them. Each is dialed
// at a pinned anycast address while the URL hostname drives TLS SNI +
// certificate verification — the local resolver is never consulted.
const (
	dohCloudflareURL = "https://api.cloudflare-dns.com/dns-query"
	dohGoogleURL     = "https://dns.google/resolve"
	dohQuad9URL      = "https://dns.quad9.net/dns-query"
)

const (
	// Same budgets as core/src/net.rs.
	dohConnectTimeout  = 2 * time.Second
	dohAttemptTimeout  = 5 * time.Second
	dohMaxResponseSize = 64 << 10
)

// dohServer is one resolver endpoint: the URL supplies the TLS server
// name, pin (when set) is the address actually dialed.
type dohServer struct {
	url string
	pin string
}

func (s dohServer) describe() string {
	if s.pin != "" {
		return s.pin
	}
	return s.url
}

var dohDefaultServers = []dohServer{
	{url: dohCloudflareURL, pin: "1.1.1.1"},
	{url: dohCloudflareURL, pin: "1.0.0.1"},
	{url: dohGoogleURL, pin: "8.8.8.8"},
	{url: dohGoogleURL, pin: "8.8.4.4"},
	{url: dohQuad9URL, pin: "9.9.9.9"},
	{url: dohQuad9URL, pin: "149.112.112.9"},
}

// dohServersOverride is a test hook: when non-empty it replaces the
// default servers, so a test can point resolution at a loopback fake
// and no lookup can ever reach 1.1.1.1.
var dohServersOverride []dohServer

func dohServers() []dohServer {
	if len(dohServersOverride) > 0 {
		return dohServersOverride
	}
	return dohDefaultServers
}

// dohCache keeps successful answers for the life of the process; a
// failed lookup is never cached, so one resolver outage cannot poison
// later connects.
var dohCache = struct {
	mu  sync.Mutex
	ips map[string]string
}{ips: map[string]string{}}

// resolveA returns the first dialable IPv4 A record for host. Every DoH
// provider is tried in order; in production (no test override) a final
// OS-resolver fallback runs when all of them fail, because a dead DoH path
// must not make Plan A unusable. The fallback is skipped while
// dohServersOverride is set so offline tests stay deterministic and never
// touch the real resolver. Answers that cannot be a real decoy endpoint
// (spoofEndpointUsable) are rejected here rather than passed on, on every
// path including the fallback.
func resolveA(ctx context.Context, host string) (string, error) {
	ip, err := resolveAVia(ctx, dohServers(), host)
	if err == nil {
		return ip, nil
	}
	if len(dohServersOverride) == 0 {
		if osIP, osErr := resolveAOS(ctx, host); osErr == nil && osIP != "" {
			log.Printf("[WARN] Plan A: DoH failed for %s (%v); OS resolver fallback -> %s", host, err, osIP)
			return osIP, nil
		}
	}
	return "", err
}

// osLookupIP is the OS-resolver seam behind resolveAOS. A poisoned local
// resolver is the case Plan A exists for, so the last-resort path has to be
// testable without one: tests swap this for a function that returns answers.
var osLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip4", host)
}

// resolveAOS is the last-resort OS-resolver lookup (first dialable IPv4).
// Plan A exists to bypass a poisoned local resolver, so this is only reached
// when every DoH provider fails; the caller warns when it is used. It is also
// the single most likely source of an intercepted answer, so the same gate
// that Plan B applies to its resolver output runs here.
func resolveAOS(ctx context.Context, host string) (string, error) {
	host = normalizeFakeSNI(host)
	if host == "" {
		return "", fmt.Errorf("nothing to resolve (empty host)")
	}
	ips, err := osLookupIP(ctx, host)
	if err != nil {
		return "", err
	}
	skipped := make([]string, 0, len(ips))
	for _, ip := range ips {
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		if !spoofEndpointUsable(v4) {
			skipped = append(skipped, v4.String())
			continue
		}
		return v4.String(), nil
	}
	if len(skipped) > 0 {
		return "", fmt.Errorf("OS resolver answered %q with undialable addresses only (%s) — it is poisoned or a portal intercepted the lookup", host, strings.Join(skipped, ", "))
	}
	return "", fmt.Errorf("no IPv4 from OS resolver for %q", host)
}

// resolveAVia is the injectable seam behind resolveA (same shape as
// core's apply_plan_a_with_servers): every test drives this one.
func resolveAVia(ctx context.Context, servers []dohServer, host string) (string, error) {
	host = normalizeFakeSNI(host)
	if host == "" {
		return "", fmt.Errorf("nothing to resolve (empty host)")
	}
	dohCache.mu.Lock()
	if ip, ok := dohCache.ips[host]; ok {
		dohCache.mu.Unlock()
		return ip, nil
	}
	dohCache.mu.Unlock()

	causes := make([]string, 0, len(servers))
	for _, s := range servers {
		ip, err := dohQueryA(ctx, s, host)
		if err == nil && ip != "" {
			dohCache.mu.Lock()
			dohCache.ips[host] = ip
			dohCache.mu.Unlock()
			return ip, nil
		}
		if err != nil {
			causes = append(causes, fmt.Sprintf("%s: %v", s.describe(), err))
		} else {
			causes = append(causes, s.describe()+": no A record")
		}
	}
	return "", fmt.Errorf("no A record for %q via DoH (%s)", host, strings.Join(causes, ", "))
}

// dohQueryA asks one server. The returned empty string with a nil error
// means "answered, but no usable A record" (NXDOMAIN / no Answer).
func dohQueryA(ctx context.Context, s dohServer, host string) (string, error) {
	u, err := url.Parse(s.url)
	if err != nil {
		return "", fmt.Errorf("bad resolver URL: %w", err)
	}
	q := u.Query()
	q.Set("name", host)
	q.Set("type", "A")
	u.RawQuery = q.Encode()

	attemptCtx, cancel := context.WithTimeout(ctx, dohAttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "application/dns-json")

	tr := &http.Transport{
		DisableKeepAlives:     true,
		DialContext:           pinnedDialer(s.pin),
		TLSHandshakeTimeout:   dohConnectTimeout + dohConnectTimeout,
		ExpectContinueTimeout: time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: dohAttemptTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dohMaxResponseSize))
	if err != nil {
		return "", err
	}
	return firstARecord(body)
}

// pinnedDialer replaces the hostname in the dial address with pin while
// the request URL keeps its own host — so TLS server name, certificate
// validation and the HTTP Host header all stay on the resolver's
// hostname. An empty pin dials the URL host normally (loopback fakes).
func pinnedDialer(pin string) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dohConnectTimeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if pin != "" {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			addr = net.JoinHostPort(pin, port)
		}
		return d.DialContext(ctx, network, addr)
	}
}

// firstARecord picks the first type-1 (A) answer that is a dialable IPv4.
// Cloudflare sends Answer as an array, but a single-object body is also
// accepted (same tolerance as the core).
//
// An address the engine cannot legitimately dial is reported as an error,
// not as "no record": this answer IS the Plan A connect endpoint, so a
// loopback or RFC1918 reply means the lookup was intercepted and must not
// be cached, must not be handed downstream, and should push the caller on
// to the next provider.
func firstARecord(body []byte) (string, error) {
	var reply struct {
		Status int             `json:"Status"`
		Answer json.RawMessage `json:"Answer"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return "", fmt.Errorf("bad dns-json: %w", err)
	}
	if reply.Status != 0 || len(reply.Answer) == 0 || string(reply.Answer) == "null" {
		return "", nil
	}
	var records []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(reply.Answer, &records); err != nil {
		var one struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		}
		if err2 := json.Unmarshal(reply.Answer, &one); err2 != nil {
			return "", fmt.Errorf("bad dns-json Answer: %w", err)
		}
		records = []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		}{one}
	}
	skipped := make([]string, 0, len(records))
	for _, r := range records {
		if r.Type != 1 {
			continue
		}
		v4 := net.ParseIP(strings.TrimSpace(r.Data)).To4()
		if v4 == nil {
			continue
		}
		if !spoofEndpointUsable(v4) {
			skipped = append(skipped, v4.String())
			continue
		}
		return v4.String(), nil
	}
	if len(skipped) > 0 {
		return "", fmt.Errorf("answered only undialable addresses (%s) — resolver or DPI intercepted this lookup", strings.Join(skipped, ", "))
	}
	return "", nil
}
