package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Share-link builders: the exact reverse of the import.go parsers,
// so parseOneLink(BuildShareLink(p)) round-trips every stored field.
// Only the five routable link protocols are covered; profiles whose
// shape needs rawOutbound JSON (or an exotic protocol) get a clear
// error instead of a lossy link. Standard library only.

// effectiveShareFP resolves the fingerprint a link carries: the
// TLSFingerprint preset wins, empty/"none" keeps the legacy field
// (same precedence as profileFingerprint in the config builders,
// lowercase-normalized like the rendered configs). Empty when the
// user set nothing — the link then omits fp and both cores fall back
// to chrome, exactly as before.
func effectiveShareFP(p *Profile) string {
	if fp := normalizeFingerprint(p.TLSFingerprint); fp != "" && fp != "none" {
		return fp
	}
	return normalizeFingerprint(p.Fingerprint)
}

// checkShareable rejects profiles no link format can represent.
func checkShareable(p *Profile) error {
	if p == nil {
		return fmt.Errorf("no profile")
	}
	if strings.TrimSpace(p.RawOutbound) != "" {
		return fmt.Errorf("profile %q uses raw JSON and cannot be represented as a share link", p.Name)
	}
	if strings.TrimSpace(p.Address) == "" {
		return fmt.Errorf("profile %q has no server address", p.Name)
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("profile %q has an invalid port %d", p.Name, p.Port)
	}
	return nil
}

// buildVmessLink emits vmess://base64(json), the v2rayN shape the
// parser reads back (add/port/id/aid/scy/net/path/host/tls/sni/fp/alpn).
func buildVmessLink(p *Profile) (string, error) {
	if strings.TrimSpace(p.IDOrPassword) == "" {
		return "", fmt.Errorf("vmess profile %q has no id", p.Name)
	}
	// The vmess JSON shape carries fp but has no mux/fragment fields:
	// refuse a lossy link instead of silently dropping them.
	if p.MuxEnabled {
		return "", fmt.Errorf("vmess profile %q uses mux, which has no vmess link format (use vless/trojan or raw JSON export)", p.Name)
	}
	if frag := strings.ToLower(strings.TrimSpace(p.TLSFragment)); frag != "" && frag != "off" {
		return "", fmt.Errorf("vmess profile %q uses fragment, which has no vmess link format (use vless/trojan or raw JSON export)", p.Name)
	}
	if p.TLS {
		if err := validateFingerprint(p); err != nil {
			return "", err
		}
	}
	scy := p.Security
	if scy == "" {
		scy = "auto"
	}
	netw := p.Network
	if netw == "" {
		netw = "tcp"
	}
	tls := ""
	if p.TLS {
		tls = "tls"
	}
	doc := map[string]any{
		"v": "2", "ps": p.Name, "add": p.Address,
		"port": strconv.Itoa(p.Port), "id": p.IDOrPassword,
		"aid": p.AlterID, "scy": scy, "net": netw,
		"path": p.Path, "host": p.Host, "tls": tls,
		"sni": p.ServerName, "fp": effectiveShareFP(p), "alpn": p.ALPN,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil
}

// shareQuery accumulates non-empty transport/TLS params for the
// vless:// and trojan:// URL shapes.
func shareQuery(p *Profile, security string) url.Values {
	q := url.Values{}
	if security != "" {
		q.Set("security", security)
	}
	if p.Flow != "" {
		q.Set("flow", p.Flow)
	}
	if p.Network != "" && p.Network != "tcp" {
		q.Set("type", p.Network)
	}
	switch p.Network {
	case "ws", "h2", "xhttp":
		if p.Path != "" {
			q.Set("path", p.Path)
		}
		if p.Host != "" {
			q.Set("host", p.Host)
		}
	case "grpc":
		if p.Path != "" {
			q.Set("serviceName", p.Path)
		}
		if p.Host != "" {
			q.Set("authority", p.Host)
		}
	default:
		if p.Path != "" {
			q.Set("path", p.Path)
		}
		if p.Host != "" {
			q.Set("host", p.Host)
		}
	}
	if p.TLS || security != "" {
		if sni := strings.TrimSpace(p.ServerName); sni != "" {
			q.Set("sni", sni)
		}
		if fp := effectiveShareFP(p); fp != "" {
			q.Set("fp", fp)
		}
		if p.ALPN != "" {
			q.Set("alpn", p.ALPN)
		}
	}
	if p.RealityEnabled {
		if p.RealityPubKey != "" {
			q.Set("pbk", p.RealityPubKey)
		}
		if p.RealityShortID != "" {
			q.Set("sid", p.RealityShortID)
		}
		if p.RealitySpiderX != "" {
			q.Set("spx", p.RealitySpiderX)
		}
	}
	if p.AllowInsecure {
		q.Set("allowInsecure", "1")
	}
	// Fragment + mux (Streisand/v2rayNG URI convention — the exact
	// reverse of the import.go parser, so links round-trip).
	if frag := buildFragmentParam(p); frag != "" {
		q.Set("fragment", frag)
	}
	if mux := buildMuxParam(p); mux != "" {
		q.Set("mux", mux)
	}
	if p.MuxEnabled && strings.TrimSpace(p.MuxProtocol) != "" {
		q.Set("muxproto", muxProtocolOrDefault(p))
	}
	return q
}

// buildUserinfoURL renders cred@host:port?query#name for vless
// and trojan links. Assembled by hand (not url.URL.String) so the
// secret keeps url.User escaping while the fragment keeps
// PathEscape form, which round-trips through linkName.
func buildUserinfoURL(scheme, cred string, p *Profile, q url.Values) string {
	host := p.Address
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	var sb strings.Builder
	sb.WriteString(scheme + "://" + url.User(cred).String() + "@")
	sb.WriteString(fmt.Sprintf("%s:%d", host, p.Port))
	if enc := q.Encode(); enc != "" {
		sb.WriteString("?" + enc)
	}
	if p.Name != "" {
		sb.WriteString("#" + url.PathEscape(p.Name))
	}
	return sb.String()
}

// buildVlessLink emits vless://uuid@host:port?...#name.
func buildVlessLink(p *Profile) (string, error) {
	if strings.TrimSpace(p.IDOrPassword) == "" {
		return "", fmt.Errorf("vless profile %q has no id", p.Name)
	}
	// Fail honestly instead of emitting a link that parses back
	// differently (or not at all).
	if err := validateMux(p); err != nil {
		return "", err
	}
	if err := validateFragment(p); err != nil {
		return "", err
	}
	if err := validateFingerprint(p); err != nil {
		return "", err
	}
	security := ""
	if p.RealityEnabled {
		security = "reality"
	} else if p.TLS {
		security = "tls"
	}
	q := shareQuery(p, security)
	q.Set("encryption", "none")
	return buildUserinfoURL("vless", p.IDOrPassword, p, q), nil
}

// buildTrojanLink emits trojan://password@host:port?...#name
// (trojan is always TLS; a reality pin rides along when set).
func buildTrojanLink(p *Profile) (string, error) {
	if strings.TrimSpace(p.IDOrPassword) == "" {
		return "", fmt.Errorf("trojan profile %q has no password", p.Name)
	}
	// Fail honestly instead of emitting a link that parses back
	// differently (or not at all).
	if err := validateMux(p); err != nil {
		return "", err
	}
	if err := validateFragment(p); err != nil {
		return "", err
	}
	if err := validateFingerprint(p); err != nil {
		return "", err
	}
	security := "tls"
	if p.RealityEnabled {
		security = "reality"
	}
	return buildUserinfoURL("trojan", p.IDOrPassword, p, shareQuery(p, security)), nil
}

// buildSSLink emits the userinfo-base64 ss:// shape the parser
// reads (method:password b64 + plain host:port).
func buildSSLink(p *Profile) (string, error) {
	if strings.TrimSpace(p.Method) == "" || strings.TrimSpace(p.IDOrPassword) == "" {
		return "", fmt.Errorf("shadowsocks profile %q needs a method and password", p.Name)
	}
	userinfo := base64.StdEncoding.EncodeToString([]byte(p.Method + ":" + p.IDOrPassword))
	host := p.Address
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	link := fmt.Sprintf("ss://%s@%s:%d", userinfo, host, p.Port)
	if p.Name != "" {
		link += "#" + url.PathEscape(p.Name)
	}
	return link, nil
}

// buildSocksLink emits socks://[user[:pass]@]host:port#name.
func buildSocksLink(p *Profile) (string, error) {
	host := p.Address
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	auth := ""
	if c := strings.TrimSpace(p.IDOrPassword); c != "" {
		// PathEscape (not QueryEscape): the parser PathUnescapes,
		// and QueryEscape's "+" for spaces would not round-trip.
		if u, pw, ok := strings.Cut(c, ":"); ok {
			auth = url.PathEscape(u) + ":" + url.PathEscape(pw) + "@"
		} else {
			auth = url.PathEscape(c) + "@"
		}
	}
	link := fmt.Sprintf("socks://%s%s:%d", auth, host, p.Port)
	if p.Name != "" {
		link += "#" + url.PathEscape(p.Name)
	}
	return link, nil
}

// BuildShareLink renders one stored profile as an importable share
// link (Throne's "Copy links"). Round-trips through parseOneLink
// for every field NekoGo stores; anything else is an error.
func (a *App) BuildShareLink(id string) (string, error) {
	p, ok := a.store.GetProfile(id)
	if !ok {
		return "", fmt.Errorf("profile %q not found", id)
	}
	switch p.Protocol {
	case "aether":
		// An aether link names its own endpoint or hop pair; the usual
		// address checks do not apply (checkShareable refuses the
		// two-hop case's empty address).
		if err := checkAetherShareable(&p); err != nil {
			return "", err
		}
		return buildAetherLink(&p)
	}
	if err := checkShareable(&p); err != nil {
		return "", err
	}
	switch p.Protocol {
	case "vmess":
		return buildVmessLink(&p)
	case "vless":
		return buildVlessLink(&p)
	case "trojan":
		return buildTrojanLink(&p)
	case "shadowsocks":
		return buildSSLink(&p)
	case "socks":
		return buildSocksLink(&p)
	default:
		return "", fmt.Errorf("protocol %q has no share-link format (use raw JSON export)", p.Protocol)
	}
}
