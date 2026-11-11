package main

// Share-link and subscription import (Issue 5).
//
// Supported inbound formats (one per line):
//   vmess://base64(json)          Trojan://, vless://, ss://, socks:// URLs
//   http(s)://...                 subscription: fetched, base64-or-plaintext
//                                 link list, parsed recursively
//
// All parsing is standard-library only. Parsed profiles are mapped onto the
// canonical Profile struct; persistence stays in ConfigStore.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Subscription is a recorded subscription URL that can be re-fetched.
type Subscription struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	// Enabled pauses this feed: nil/true = active (legacy records stay
	// active), false = skipped by UpdateSubscriptions + auto-update.
	Enabled *bool `json:"enabled,omitempty"`
	// Usage mirrors the provider's `subscription-userinfo` header
	// (upload/download/total bytes + expire stamp, "" when the
	// provider sends none). Powers the per-feed quota card.
	UsageUpload   int64  `json:"usageUpload,omitempty"`
	UsageDownload int64  `json:"usageDownload,omitempty"`
	UsageTotal    int64  `json:"usageTotal,omitempty"`
	UsageExpire   string `json:"usageExpire,omitempty"`
}

// IsEnabled reports whether the feed takes part in updates
// (nil = legacy record, treated as enabled).
func (s Subscription) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// SubUserInfo is the parsed `subscription-userinfo` response header
// (upload=..; download=..; total=..; expire=..). Missing keys stay
// zero/empty — most providers send only a subset.
type SubUserInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   string
}

// parseSubUserInfo parses one `subscription-userinfo` header value.
// Malformed pairs are skipped, never fatal (a broken header must
// not fail the whole subscription refresh).
func parseSubUserInfo(header string) SubUserInfo {
	var out SubUserInfo
	for _, part := range strings.Split(header, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		into := func(dst *int64) {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				*dst = n
			}
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			into(&out.Upload)
		case "download":
			into(&out.Download)
		case "total":
			into(&out.Total)
		case "expire":
			out.Expire = v
		}
	}
	return out
}

// LinkImportResult reports what a preview/import run produced.
type LinkImportResult struct {
	Profiles []Profile `json:"profiles"`
	Invalid  int       `json:"invalid"`
}

// maxSubRedirects is the chain length the default net/http policy would
// have enforced; supplying CheckRedirect replaces that limit, so the
// client has to carry its own.
const maxSubRedirects = 10

var subHTTP = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxSubRedirects {
			return fmt.Errorf("subscription redirect chain longer than %d", maxSubRedirects)
		}
		return subRedirectAllowed(req.URL)
	},
}

// subRedirectAllowed gates the hops a subscription URL can point at.
//
// A subscription is fetched again on a timer (Settings → auto-update), so a
// redirect is a second hop the server picked and nobody looks at: a public
// feed can 302 to 127.0.0.1:8080 (a local admin API), 169.254.169.254
// (cloud metadata) or an RFC1918 host, and the reply body gets scanned for
// "://" and reported back — a read primitive against the user's own
// network.
//
// Only redirect targets are gated: the first URL is the user's own choice
// and may legitimately be a LAN subscription server. Hosts are judged as
// written, without a DNS round trip — a name that happens to resolve to a
// private address is outside what this can see, and resolving inside the
// client would make every refresh depend on the resolver.
func subRedirectAllowed(target *url.URL) error {
	switch target.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("subscription redirects to unsupported scheme %q", target.Scheme)
	}
	host := target.Hostname()
	if host == "" {
		return fmt.Errorf("subscription redirect has no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		// 100.64.0.0/10 is not routable anywhere and is exactly where
		// Tailscale puts its peers (plus 100.100.100.100, served by the
		// local tailscaled) — RFC 1918 alone would miss it.
		if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 0x40 {
			return fmt.Errorf("subscription redirects to shared-address space %s", host)
		}
		// 0.0.0.0 / :: mean "this host" to the dialer.
		if ip.IsUnspecified() || !isPublicIP(ip) {
			return fmt.Errorf("subscription redirects to a local or private address (%s)", host)
		}
		return nil
	}
	low := strings.ToLower(strings.TrimSuffix(host, "."))
	switch {
	case low == "localhost",
		strings.HasSuffix(low, ".localhost"), // RFC 6761
		strings.HasSuffix(low, ".local"),     // mDNS
		strings.HasSuffix(low, ".internal"),
		strings.HasSuffix(low, ".home.arpa"):
		return fmt.Errorf("subscription redirects to a local hostname (%s)", host)
	case !strings.Contains(low, "."):
		// No public name is dot-free; a single label only resolves
		// through whatever search domain this machine appends.
		return fmt.Errorf("subscription redirects to a single-label hostname (%s)", host)
	}
	return nil
}

// decodeB64 tries all common base64 alphabets, padded or raw.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	return p, nil
}

func splitHostPort(hp string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		return "", 0, fmt.Errorf("bad endpoint %q", hp)
	}
	port, err := parsePort(portStr)
	if err != nil {
		return "", 0, err
	}
	return strings.Trim(host, "[]"), port, nil
}

// splitLinkFragment cuts the label off the end of a link BEFORE url.Parse
// sees it. A bare "%" in a name ("#100%", "#50% off") is not a valid
// percent-escape, and Go rejects the entire URL for it — so a node whose
// endpoint is perfectly fine used to be reported as an import error simply
// because of its label. Generators do write labels unencoded.
func splitLinkFragment(link string) (rest, frag string) {
	if i := strings.IndexByte(link, '#'); i >= 0 {
		return link[:i], link[i+1:]
	}
	return link, ""
}

// escapeUserinfoPercents rewrites every "%" in a link's credential that is
// not a valid percent-escape into "%25", so url.Parse accepts the link and
// its own unescaping returns exactly the bytes the generator wrote. Go
// rejects the ENTIRE URL over one invalid escape, which used to lose a node
// whose endpoint was perfectly readable just because its password held a
// bare "%" — the same class as the label, but in the userinfo. A correctly
// escaped credential is left untouched and still decodes once.
func escapeUserinfoPercents(rest string) string {
	i := strings.Index(rest, "://")
	if i < 0 {
		return rest
	}
	start := i + 3
	// The authority ends at the first "/" or "?", matching url.Parse.
	end := len(rest)
	if j := strings.IndexAny(rest[start:], "/?"); j >= 0 {
		end = start + j
	}
	// url.Parse splits host from userinfo at the LAST "@", so the userinfo
	// may itself hold an escaped "@"; only a bare "%" needs fixing.
	at := strings.LastIndexByte(rest[start:end], '@')
	if at < 0 {
		return rest
	}
	userinfo := rest[start : start+at]
	if !strings.Contains(userinfo, "%") {
		return rest
	}
	var b strings.Builder
	b.Grow(len(userinfo) + 8)
	for k := 0; k < len(userinfo); k++ {
		if userinfo[k] == '%' && !hasHexEscape(userinfo[k:]) {
			b.WriteString("%25")
			continue
		}
		b.WriteByte(userinfo[k])
	}
	return rest[:start] + b.String() + rest[start+at:]
}

// hasHexEscape reports whether s begins with a complete "%XX" escape.
func hasHexEscape(s string) bool {
	return len(s) >= 3 && s[0] == '%' && isHexDigit(s[1]) && isHexDigit(s[2])
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// linkName turns a link's fragment into the profile label. frag must be
// given in its ON-THE-WIRE (percent-encoded) form: url.Parse already
// decodes u.Fragment, so passing that here decoded twice and rewrote
// "a%41b" into "aAb".
func linkName(frag, fallback string) string {
	if frag != "" {
		if n, err := url.PathUnescape(frag); err == nil && n != "" {
			return n
		}
		// Not percent-encoded after all — a bare "%" is ordinary in a
		// label ("100%"). Falling back here threw the user's own name
		// away and replaced it with host:port.
		return frag
	}
	return fallback
}

// --- vmess ---

// parseVmess parses vmess://base64(json), with or without a trailing
// #remark. Generators that put the label in the fragment are common, and
// "#" is outside every base64 alphabet: before the split the payload looked
// like corrupt base64 and the whole node was rejected as unreadable.
func parseVmess(link string) (*Profile, error) {
	raw, rawFrag := splitLinkFragment(strings.TrimPrefix(link, "vmess://"))
	data, err := decodeB64(raw)
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("vmess: bad json: %w", err)
	}
	// A JSON null is a converter's way of writing "option absent". Turning
	// the value into a string with fmt.Sprint rendered that as the literal
	// "<nil>", which then reached the profile as an SNI/Host/path of <nil>
	// and broke TLS on an otherwise readable node.
	str := func(k string) string {
		switch v := m[k].(type) {
		case nil:
			return ""
		case string:
			return v
		default:
			return fmt.Sprint(v)
		}
	}
	addr := str("add")
	if addr == "" {
		return nil, fmt.Errorf("vmess: missing address")
	}
	port, err := parsePort(str("port"))
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}
	aid := 0
	if v, ok := m["aid"]; ok {
		fmt.Sscan(fmt.Sprint(v), &aid)
	}
	p := &Profile{
		Protocol:     "vmess",
		Core:         CoreSingBox,
		Address:      addr,
		Port:         port,
		IDOrPassword: str("id"),
		AlterID:      aid,
		Security:     str("scy"),
		Network:      str("net"),
		TLS:          str("tls") == "tls",
		ServerName:   str("sni"),
		Fingerprint:  str("fp"),
		Path:         str("path"),
		Host:         str("host"),
		ALPN:         str("alpn"),
	}
	if p.Network == "" {
		p.Network = "tcp"
	}
	// "ps" is the field the format reserves for the remark, so it wins over
	// the fragment; the fragment (percent-encoded on the wire, so it goes
	// through linkName) is the fallback for payloadless labels.
	p.Name = str("ps")
	if p.Name == "" {
		p.Name = linkName(rawFrag, fmt.Sprintf("%s:%d", addr, port))
	}
	if p.IDOrPassword == "" {
		return nil, fmt.Errorf("vmess: missing id")
	}
	return p, nil
}

// --- vless / trojan ---

// linkFlag reads a URI query boolean. "1" is what this app writes, but
// generators emit "true" just as often, and a value that only matched "1"
// dropped the option without a word — for allowInsecure that meant
// verifying a certificate the link explicitly said not to.
func linkFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true":
		return true
	}
	return false
}

func queryLower(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[strings.ToLower(k)] = v
	}
	return out
}

// parseVlessTrojan parses vless://uuid@host:port?...#name and
// trojan://password@host:port?...#name.
func parseVlessTrojan(link string) (*Profile, error) {
	isVless := strings.HasPrefix(link, "vless://")
	rest, rawFrag := splitLinkFragment(link)
	u, err := url.Parse(escapeUserinfoPercents(rest))
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	cred, _ := url.PathUnescape(u.User.String())
	host := u.Hostname()
	port, err := parsePort(u.Port())
	if err != nil {
		return nil, err
	}
	if cred == "" || host == "" {
		return nil, fmt.Errorf("missing credentials or host")
	}
	q := queryLower(u.Query())
	get := func(k string) string { return q.Get(k) }
	sec := strings.ToLower(get("security"))
	p := &Profile{Address: host, Port: port}
	if isVless {
		p.Protocol = "vless"
		p.IDOrPassword = cred
		p.Flow = get("flow")
		p.Core = CoreSingBox
		if sec == "reality" || p.Flow != "" {
			p.Core = CoreXray
		}
	} else {
		p.Protocol = "trojan"
		p.IDOrPassword = cred
		p.Core = CoreSingBox
		p.TLS = true
	}
	if sec == "tls" || sec == "reality" {
		p.TLS = true
	}
	p.RealityEnabled = sec == "reality"
	p.RealityPubKey = get("pbk")
	p.RealityShortID = get("sid")
	p.RealitySpiderX = get("spx")
	p.ServerName = get("sni")
	if p.ServerName == "" {
		p.ServerName = host
	}
	p.Fingerprint = get("fp")
	p.Network = get("type")
	if p.Network == "" {
		// WS inference: links like the prompt's Test-CF carry
		// ws-only params (path/host) without type=ws. A tcp
		// transport would drop them in the builder, so infer the
		// transport instead of losing them.
		if strings.TrimSpace(get("servicename")) != "" {
			p.Network = "grpc"
		} else if strings.TrimSpace(get("path")) != "" || strings.TrimSpace(get("host")) != "" || strings.TrimSpace(get("authority")) != "" {
			p.Network = "ws"
		} else {
			p.Network = "tcp"
		}
	}
	// SNI-spoof auto-detect: configs the spoof engine serves auto-route
	// to CoreSpoof (spoof tag in the proxies tab):
	//   - chained shape (trojan/vless over TLS+WS at 127.0.0.1:40443):
	//     only the engine serves that loopback port;
	//   - direct domain shape (trojan/vless over TLS+WS at a bare
	//     domain like prompt.txt Test-CF www.calmlunch.com): the
	//     direct dial is DPI-blocked, only the engine's fake
	//     ClientHello passes — tag it too so it arrives with the
	//     spoof tag like APPXA.
	// The normal config (an IPv6 literal, prompt.txt
	// nat3.xxai.dpdns.org) is not a domain and stays a normal direct
	// profile (no spoof tag). IP literals never auto-tag: proving
	// CF-fronting needs a live lookup and import stays synchronous —
	// the SNI tab remains the opt-in path there (SpoofUseProfile).
	// Reality/flow stay on xray (the engine relays raw TLS only).
	// Flows through every sync path (preview/import/subscription)
	// since they all share parseOneLink; the rule itself lives in
	// spoofEligibilityIssue (spoof_manager.go).
	// front, so these auto-route to CoreSpoof (spoof tag). The SNI
	// tab remains the opt-in path for IP literals and for switching
	// back (SpoofUseProfile / profile form core selector).
	// Reality/flow stay on xray (the engine relays raw TLS only).
	if spoofEligibilityIssue(p) == nil &&
		(isLoopbackAddress(p.Address) || isDomainAddress(p.Address)) {
		p.Core = CoreSpoof
	}
	p.Path = get("path")
	if p.Path == "" {
		p.Path = get("servicename")
	}
	p.Host = get("host")
	if p.Host == "" {
		p.Host = get("authority")
	}
	p.ALPN = get("alpn")
	p.AllowInsecure = linkFlag(get("allowinsecure")) || linkFlag(get("insecure")) || linkFlag(get("skip-cert-verify"))
	// Fragment + mux (Streisand/v2rayNG URI convention):
	// fragment=<packets>,<length>,<interval> (either part order),
	// mux=<concurrency>, muxproto=<h2mux|smux|yamux>. A muxproto
	// without mux enables mux at the core default concurrency.
	if frag := strings.TrimSpace(get("fragment")); frag != "" {
		mode, length, iv, err := parseFragmentParam(frag)
		if err != nil {
			return nil, err
		}
		p.TLSFragment, p.TLSFragmentLen, p.TLSFragmentInt = mode, length, iv
	}
	if muxRaw := strings.TrimSpace(get("mux")); muxRaw != "" {
		cc, err := parseMuxParam(muxRaw)
		if err != nil {
			return nil, err
		}
		p.MuxEnabled = true
		p.MuxConcurrency = cc
	}
	if proto := strings.ToLower(strings.TrimSpace(get("muxproto"))); proto != "" {
		if !containsFold(supportedMuxProtocols, proto) {
			return nil, fmt.Errorf("invalid muxproto %q: want one of %v", get("muxproto"), supportedMuxProtocols)
		}
		p.MuxEnabled = true
		p.MuxProtocol = proto
	}
	p.Name = linkName(rawFrag, fmt.Sprintf("%s:%d", host, port))
	return p, nil
}

// --- shadowsocks ---

// parseSS parses ss:// links in all common shapes:
// ss://method:password@host:port#name,
// ss://base64(method:password)@host:port#name,
// ss://base64(method:password@host:port)#name
func parseSS(link string) (*Profile, error) {
	rest := strings.TrimPrefix(link, "ss://")
	name := ""
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		name = rest[i+1:]
		rest = rest[:i]
	}
	// A query is not part of the endpoint: generators emit a bare
	// "…@host:443?#name" as often as "?plugin=…", and leaving the "?" glued
	// to the port failed the import. Base64 never carries a "?", so this
	// cannot truncate the 2019 whole-link form.
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query := rest[i+1:]
		rest = rest[:i]
		// NekoGo has no SIP003 plugin support, and dropping the option
		// silently would hand the user a profile that never connects.
		if v, err := url.ParseQuery(query); err == nil {
			if _, ok := queryLower(v)["plugin"]; ok {
				return nil, fmt.Errorf("ss: plugin options are not supported (%s)", query)
			}
		}
	}
	var method, password, hostport string
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		userinfo, hp := rest[:i], rest[i+1:]
		// ":" is in no base64 alphabet, so a literal colon can only come from
		// the plaintext method:password shape — whatever else that password
		// holds ("=", "%", "+", "/"). Requiring "no = or %" as well used to
		// send such userinfo to the base64 decoder, which refused it and lost
		// a readable node.
		if strings.Contains(userinfo, ":") {
			method, password, _ = strings.Cut(userinfo, ":")
		} else {
			dec, err := decodeB64(userinfo)
			if err != nil {
				return nil, fmt.Errorf("ss: bad userinfo: %w", err)
			}
			method, password, _ = strings.Cut(string(dec), ":")
		}
		hostport = hp
	} else {
		dec, err := decodeB64(rest)
		if err != nil {
			return nil, fmt.Errorf("ss: bad payload: %w", err)
		}
		s := string(dec)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return nil, fmt.Errorf("ss: no host part")
		}
		method, password, _ = strings.Cut(s[:at], ":")
		hostport = s[at+1:]
	}
	if method == "" || password == "" {
		return nil, fmt.Errorf("ss: missing method or password")
	}
	// The password is stored verbatim. Both base64 shapes just decoded it
	// from the payload, so it is raw bytes and "%" is an ordinary password
	// character there — percent-decoding emptied a password carrying a
	// stray "%" and rewrote "%41" into "A". The plaintext branch takes its
	// password verbatim for the same reason, and a round-trip through this
	// app never reaches it: buildSSLink base64s without escaping.
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}
	return &Profile{
		Protocol:     "shadowsocks",
		Core:         CoreSingBox,
		Name:         linkName(name, fmt.Sprintf("%s:%d", host, port)),
		Address:      host,
		Port:         port,
		IDOrPassword: password,
		Method:       method,
	}, nil
}

// --- socks ---

func parseSocks(link string) (*Profile, error) {
	rest, rawFrag := splitLinkFragment(link)
	u, err := url.Parse(escapeUserinfoPercents(rest))
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	host := u.Hostname()
	port, err := parsePort(u.Port())
	if err != nil {
		return nil, err
	}
	cred := ""
	if u.User != nil {
		user := u.User.Username()
		if pw, ok := u.User.Password(); ok {
			cred = user + ":" + pw
		} else {
			cred = user
		}
	}
	return &Profile{
		Protocol:     "socks",
		Core:         CoreSingBox,
		Network:      "tcp",
		Name:         linkName(rawFrag, fmt.Sprintf("%s:%d", host, port)),
		Address:      host,
		Port:         port,
		IDOrPassword: cred,
	}, nil
}

// parseOneLink parses a single non-subscription link.
func parseOneLink(link string) (*Profile, error) {
	l := strings.TrimSpace(link)
	switch {
	case strings.HasPrefix(l, aetherURLPrefix):
		return parseAetherLink(l)
	case strings.HasPrefix(l, "vmess://"):
		return parseVmess(l)
	case strings.HasPrefix(l, "vless://"), strings.HasPrefix(l, "trojan://"):
		return parseVlessTrojan(l)
	case strings.HasPrefix(l, "ss://"):
		return parseSS(l)
	case strings.HasPrefix(l, "socks://"):
		return parseSocks(l)
	default:
		return nil, fmt.Errorf("unsupported scheme")
	}
}

// fetchSubscription downloads a subscription URL and returns raw
// link lines plus the parsed `subscription-userinfo` header (quota /
// expiry for the feed card; zero when the provider sends none).
func fetchSubscription(rawURL string) ([]string, SubUserInfo, error) {
	var info SubUserInfo
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, info, err
	}
	req.Header.Set("User-Agent", "NekoBox")
	resp, err := subHTTP.Do(req)
	if err != nil {
		return nil, info, err
	}
	defer resp.Body.Close()
	// BUG-025: a 404/500 HTML page containing "://" would otherwise be
	// parsed as profiles and counted as success. Only 200 proceeds.
	// (The URL is deliberately omitted: subscription URLs may carry
	// tokens that must not land in logs.)
	if resp.StatusCode != http.StatusOK {
		return nil, info, fmt.Errorf("subscription download: unexpected HTTP status %s", resp.Status)
	}
	info = parseSubUserInfo(resp.Header.Get("subscription-userinfo"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, info, err
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, info, fmt.Errorf("empty subscription body")
	}
	if !strings.Contains(text, "://") {
		if dec, err := decodeB64(text); err == nil && strings.Contains(string(dec), "://") {
			text = string(dec)
		}
	}
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > 5000 {
		lines = lines[:5000]
	}
	return lines, info, nil
}

// collectLinks splits pasted text into (direct links, subscription URLs).
func collectLinks(text string) (direct, subs []string) {
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		low := strings.ToLower(ln)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
			subs = append(subs, ln)
		} else {
			direct = append(direct, ln)
		}
	}
	return direct, subs
}

// usableParsedLink reports whether parseOneLink produced a profile worth
// storing. The Aether two-hop protocols (gool/mim/wg-over-masque) keep
// their endpoints in the outer/inner hops and deliberately leave Address
// empty, so the "no address" rejection must exempt them — the same rule
// SaveProfile applies. Every import path shares this so a previewed link
// can never be one the importer then drops.
func usableParsedLink(p *Profile, err error) bool {
	if err != nil || p == nil {
		return false
	}
	if p.Address != "" {
		return true
	}
	return aetherDriverProfile(p) && aetherProfileNeedsNoAddress(p)
}

// previewText parses without persisting. group tags subscription results.
func previewText(text, group string) (profiles []Profile, invalid int) {
	direct, subs := collectLinks(text)
	for _, ln := range direct {
		p, err := parseOneLink(ln)
		if !usableParsedLink(p, err) {
			invalid++
			continue
		}
		p.Group = group
		profiles = append(profiles, *p)
	}
	for _, su := range subs {
		lines, _, err := fetchSubscription(su)
		if err != nil {
			invalid++
			continue
		}
		u, _ := url.Parse(su)
		g := group
		if g == "" && u != nil {
			g = u.Hostname()
		}
		for _, ln := range lines {
			p, err := parseOneLink(ln)
			if !usableParsedLink(p, err) {
				invalid++
				continue
			}
			p.Group = g
			profiles = append(profiles, *p)
		}
	}
	return profiles, invalid
}

func dedupeKey(p *Profile) string {
	var b strings.Builder
	for _, s := range []string{
		p.Protocol, p.Address, strconv.Itoa(p.Port),
		p.IDOrPassword, p.Method, p.Security,
		p.Network, boolStr(p.TLS),
		p.ServerName, p.Fingerprint, p.TLSFingerprint,
		boolStr(p.RealityEnabled), p.RealityPubKey,
		p.RealityShortID, p.RealitySpiderX,
		p.Flow, p.Path, p.Host, p.ALPN,
		boolStr(p.AllowInsecure),
		strconv.Itoa(p.AlterID),
		p.TLSFragment, p.TLSFragmentLen, strconv.Itoa(p.TLSFragmentInt),
		boolStr(p.MuxEnabled), p.MuxProtocol, strconv.Itoa(p.MuxConcurrency),
		// Aether settings are the identity of an aether link: two-hop links
		// leave Address empty, so without this every such pair looked
		// identical and the second import was dropped as a duplicate.
		aetherSettingsKey(p.Aether),
	} {
		b.WriteString(s)
		b.WriteByte(0x1f) // ASCII unit separator
	}
	return b.String()
}

// aetherSettingsKey renders the aether options as a stable string for
// dedupe (json.Marshal on a struct is deterministic).
func aetherSettingsKey(a aetherSettings) string {
	data, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(data)
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// urlParseHost returns the hostname of a URL or "" when unparseable.
func urlParseHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

// subFetchError renders a subscription download failure so it can be
// written down. Every transport failure comes back as a *url.Error whose
// text repeats the request URL, and a feed URL carries the access token in
// its query — the log file is the thing users hand to whoever is helping
// them, so only the wrapped cause is safe to keep.
func subFetchError(err error) string {
	if err == nil {
		return ""
	}
	if ue, ok := err.(*url.Error); ok && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// subLabel names a feed by host alone: enough to tell WHICH subscription
// failed, without the query that holds the token.
func subLabel(rawURL string) string {
	if h := urlParseHost(rawURL); h != "" {
		return h
	}
	return "(unreadable feed URL)"
}
