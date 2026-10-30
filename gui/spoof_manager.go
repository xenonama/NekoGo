package main

// SpoofManager owns the sni-spoof.exe child PROCESS (out-of-process:
// spawn + stdout scraping, no FFI, no cgo, no unsafe.Pointer).
//
// Traffic shape: sing-box transports raw TLS bytes to
// 127.0.0.1:<spoof port>; the engine fake-injects the ClientHello on
// the endpoint leg via WinDivert and relays the rest (CORE_REPORT.md
// section 13). MODE is always "SNI Only" — "Trojan + Xray" was
// removed from the Rust build and its validator rejects it
// (CORE_REPORT.md section 10).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// spoofExeName is the Rust core binary. It must sit next to nekogo.exe.
const spoofExeName = "sni-spoof.exe"

// spoofExeOverride / spoofExeDirOverride are test hooks (nil/empty in
// production): SpoofStart tests point them at a mock exe so Start
// exercises config rendering + spawn + [STATS] scraping without the
// real WinDivert engine.
var (
	spoofExeOverride    string
	spoofExeDirOverride string
)

// spoofStartupTimeout bounds Start's wait for the engine's readiness
// line. The engine opens up to three WinDivert handles before binding
// its listener; on a cold driver that can take seconds, but a wedged
// engine must not hang Connect or a latency probe forever. The process
// exiting early (the common failure: no admin rights) fails fast via
// the reaper instead of waiting this out.
var spoofStartupTimeout = 10 * time.Second

// SpoofManager spawns sni-spoof.exe and scrapes its stdout.
type SpoofManager struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	process    *os.Process
	configPath string
	port       int
	running    bool
	// stopRequested marks an exit Stop asked for, so the reaper reports a
	// crash only for a death nobody requested (SingBoxManager's contract).
	stopRequested bool
	startedAt     time.Time
	lastError     string
	lastStats     SpoofStats
	logCh         chan LogEntry
	// recent keeps the child's last lines for an EngineExit report
	// (engine_exit.go), like every other engine manager.
	recent lineRing
	// exitHandler reports the death of a COMMITTED engine upward, so
	// CoreManager can tear the group down instead of leaving the UI
	// connected to a dead WinDivert listener.
	exitHandler func(EngineExit)
}

// NewSpoofManager wires the manager to the shared log bus.
func NewSpoofManager(logCh chan LogEntry) *SpoofManager {
	return &SpoofManager{logCh: logCh}
}

// SetExitHandler registers the cross-engine death report callback
// (CoreManager.handleEngineExit). Called once from SetSpoofManager.
func (m *SpoofManager) SetExitHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exitHandler = fn
}

func (m *SpoofManager) log(level, msg string) {
	if m.logCh == nil {
		return
	}
	// Best-effort: the log pump writes a file per entry, so a slow or dead
	// LogOutputPath fills the channel. Several call sites hold m.mu, and a
	// blocking send would then freeze Start/Stop and every status poll.
	select {
	case m.logCh <- LogEntry{Level: level, Source: "spoof", Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
	}
}

// resolveSpoofExe locates sni-spoof.exe: bin\ next to nekogo.exe
// (shipped layout), then the exe's own folder (legacy portable copies),
// or the test override dir. No silent fallbacks: a missing exe is a hard
// error and Connect must fail with it.
func (m *SpoofManager) resolveSpoofExe() (string, error) {
	if spoofExeOverride != "" {
		if _, err := os.Stat(spoofExeOverride); err == nil {
			return spoofExeOverride, nil
		}
		return "", fmt.Errorf("sni-spoof mock exe not found at %q", spoofExeOverride)
	}
	dir := spoofExeDirOverride
	if dir == "" {
		if c, ok := firstEngineFile(spoofExeName); ok {
			return c, nil
		}
		dir = exeDir()
	}
	candidate := filepath.Join(dir, spoofExeName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("sni-spoof.exe not found in bin\\ next to nekogo.exe (%s): run the SNI core build step", candidate)
}

// spoofExeDir returns the directory the temp config is written to:
// the exe's own directory (production) or the override dir (tests).
func spoofExeDir(exePath string) string {
	if spoofExeDirOverride != "" {
		return spoofExeDirOverride
	}
	return filepath.Dir(exePath)
}

// cloudflareCIDRs is the published Cloudflare IPv4 set
// (https://www.cloudflare.com/ips-v4). The previous prefix check
// (104./172.67./141.101./108.162.) rejected valid CF endpoints such
// as 172.64.x.x, 162.158.x.x or 188.114.x.x, so direct-CF trojan
// profiles failed with "not Cloudflare-fronted" while v2rayN worked.
var cloudflareCIDRs = []string{
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"108.162.192.0/18",
	"131.0.72.0/22",
	"141.101.64.0/18",
	"162.158.0.0/15",
	"172.64.0.0/13",
	"173.245.48.0/20",
	"188.114.96.0/20",
	"190.93.240.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
}

var parsedCFCIDRs = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cloudflareCIDRs))
	for _, c := range cloudflareCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isCloudflareIP reports whether ip belongs to a Cloudflare-fronted
// range. The engine only makes sense in front of Cloudflare, so
// anything else is a configuration error, not a dial-and-hope.
func isCloudflareIP(ip string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, n := range parsedCFCIDRs {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// isDomainAddress reports whether addr is a bare domain name (not an
// IP literal, not loopback, not empty): the prompt.txt Test-CF shape
// (www.calmlunch.com) vs the normal shape (an IPv6 literal like
// [2a06:98c1:3101::6812:202f]). DNS-free, so the import auto-detect
// can use it synchronously.
func isDomainAddress(addr string) bool {
	s := strings.ToLower(strings.TrimSpace(addr))
	if s == "" {
		return false
	}
	s = strings.Trim(s, "[]")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "localhost" {
		return false
	}
	if ip := net.ParseIP(s); ip != nil {
		return false
	}
	if isLoopbackAddress(s) {
		return false
	}
	return strings.Contains(s, ".")
}

// normalizeFakeSNI lowercases and dot-strips the fake SNI: the Rust
// validator requires it (CORE_REPORT.md section 3).
func normalizeFakeSNI(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.Trim(s, ".")
}

// spoofEligibilityIssue reports why a profile cannot ride the
// sni-spoof core (nil = eligible): trojan/vless over TLS+WS, no
// REALITY, no xtls flow (the engine relays raw TLS only,
// CORE_REPORT.md section 13). Single source of truth for both the
// link-import auto-detect and the SNI tab's SpoofUseProfile.
func spoofEligibilityIssue(p *Profile) error {
	if p == nil {
		return fmt.Errorf("nil profile")
	}
	if strings.TrimSpace(p.Address) == "" {
		return fmt.Errorf("profile has no address")
	}
	if p.Protocol != "trojan" && p.Protocol != "vless" {
		return fmt.Errorf("sni-spoof needs a trojan or vless profile (got %q)", p.Protocol)
	}
	if !p.TLS {
		return fmt.Errorf("sni-spoof needs TLS (tls+ws config)")
	}
	if p.RealityEnabled {
		return fmt.Errorf("REALITY stays on the xray core")
	}
	if p.Flow != "" {
		return fmt.Errorf("xtls flow %q stays on the xray core", p.Flow)
	}
	if p.Network != "ws" {
		return fmt.Errorf("transport %q is not ws (sni-spoof configs are tls+ws)", p.Network)
	}
	return nil
}

// cfIPLiteral returns the address when it is a bare Cloudflare IPv4
// literal (no DNS): lets a literal-addressed profile Start without
// touching a (possibly hijacked) resolver.
func cfIPLiteral(addr string) (string, bool) {
	s := strings.TrimSpace(addr)
	s = strings.Trim(s, "[]")
	s = strings.TrimSuffix(s, ".")
	v4 := net.ParseIP(s).To4()
	if v4 == nil {
		return "", false
	}
	if !isCloudflareIP(v4.String()) {
		return "", false
	}
	return v4.String(), true
}

// resolveDomainToCFIP resolves a domain to its first Cloudflare IP.
// Non-CF resolutions are rejected with the resolved set named.
// The lookup budget is 3s: this runs synchronously inside Start
// (which holds the manager mutex), so a slow resolver would stall
// both Connect and the SNI tab's 2s status poll behind it.
// Private/reserved hits (e.g. a localhost/DPI interceptor answering
// 10.x for a public domain) are skipped, not fatal: the lookup
// keeps scanning the answer set for a real CF edge IP before
// failing.
func resolveDomainToCFIP(domain string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", domain, err)
	}
	seen := make([]string, 0, len(ips))
	skipped := make([]string, 0, len(ips))
	for _, ip := range ips {
		v4 := ip.IP.To4()
		if v4 == nil {
			continue
		}
		s := v4.String()
		if v4.IsPrivate() || v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsMulticast() {
			skipped = append(skipped, s)
			continue
		}
		seen = append(seen, s)
		if isCloudflareIP(s) {
			return s, nil
		}
	}
	if len(seen) == 0 {
		if len(skipped) > 0 {
			return "", fmt.Errorf("resolve %q: only private answers (%s) — local resolver/DPI intercepted it, retry with the SNI tab's pinned IP checked", domain, strings.Join(skipped, ", "))
		}
		return "", fmt.Errorf("resolve %q: no IPv4 address found (sni-spoof needs a Cloudflare IPv4 endpoint)", domain)
	}
	return "", fmt.Errorf("resolve %q: %s is not Cloudflare-fronted (want 104./172.67./141.101./108.162.)", domain, strings.Join(seen, ", "))
}

// spoofEndpointPort returns the remote port the engine must dial.
// Loopback profiles (chained shape, e.g. 127.0.0.1:40443) carry the
// LOCAL listen port in profile.Port — dialing it upstream (CF:40443)
// times out every connection ("slow or never connects"). The remote
// port for loopback is always 443; direct profiles keep their own.
func spoofEndpointPort(profile *Profile) int {
	if profile == nil {
		return 443
	}
	if isLoopbackAddress(profile.Address) {
		return 443
	}
	if profile.Port >= 1 && profile.Port <= 65535 {
		return profile.Port
	}
	return 443
}

// healthRank buckets a scan-stamped pool entry for failover ordering:
// 0 = scanned OK (faster LastMs first), 1 = never scanned, 2 = failed.
// The engine round-robins the pool per connection, so pinning a
// known-dead entry at the front costs a failed first connection on
// every new flow (part of the "so slow" report); failed entries go
// last until a later scan re-stamps them.
func healthRank(ok bool, at string, ms int) (int, int) {
	switch {
	case ok:
		return 0, ms
	case at == "":
		return 1, 0
	default:
		return 2, 0
	}
}

// orderByHealth stably sorts scan-stamped candidates by healthRank
// (LastOK fastest-first, then unscanned, then failed). The input is
// copied: Settings is shared with callers.
func orderByHealth[T any](in []T, stamp func(T) (bool, string, int)) []T {
	out := append([]T(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, mi := healthRank(stamp(out[i]))
		rj, mj := healthRank(stamp(out[j]))
		if ri != rj {
			return ri < rj
		}
		return mi < mj
	})
	return out
}

// buildSpoofEndpoints renders the engine ENDPOINTS pool: the resolved
// primary first, then the checked SNIIPs as failover (deduped, capped
// at the engine's 64-entry limit), ordered by scan health so dead IPs
// never take the first connection. Unchecked entries never reach the
// pool. A single hard-coded IP with no fallback made every handshake
// hang when that one IP was throttled — the "so slow" half of the
// report.
func buildSpoofEndpoints(primaryIP string, primaryPort int, settings Settings) []any {
	seen := map[string]bool{}
	out := []any{}
	push := func(ip string, port int) {
		ip = strings.TrimSpace(ip)
		if ip == "" || port < 1 || port > 65535 {
			return
		}
		key := fmt.Sprintf("%s:%d", ip, port)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, map[string]any{"ip": ip, "port": port})
	}
	push(primaryIP, primaryPort)
	ordered := orderByHealth(activeSNIIPs(settings.SNIIPs), func(e SNIIP) (bool, string, int) {
		return e.LastOK, e.LastAt, e.LastMs
	})
	for _, e := range ordered {
		if len(out) >= 64 {
			break
		}
		push(strings.TrimSpace(e.IP), primaryPort)
	}
	return out
}

// buildSpoofSNIs renders the engine FAKE_SNIS pool: the resolved
// primary first, then the checked SNISNIs as rotation candidates
// (normalized, deduped, capped at 200). Unchecked entries never reach
// the pool. Single-SNI mode defeats the engine's per-connection
// rotation and is fragile when one decoy gets throttled.
func buildSpoofSNIs(primarySNI string, settings Settings) []any {
	seen := map[string]bool{}
	out := []any{}
	push := func(s string) {
		s = normalizeFakeSNI(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	push(primarySNI)
	ordered := orderByHealth(activeSNISNIs(settings.SNISNIs), func(e SNISNI) (bool, string, int) {
		return e.LastOK, e.LastAt, e.LastMs
	})
	for _, e := range ordered {
		if len(out) >= 200 {
			break
		}
		push(e.Host)
	}
	return out
}

// activeSNIIPs/activeSNISNIs drop the entries the user unchecked in
// the SNI tab (nil Selected = legacy/seeded = active). The engine
// pools must never carry unchecked entries: the checkboxes are the
// single on/off switch for what the spoofer may use.
func activeSNIIPs(in []SNIIP) []SNIIP {
	out := make([]SNIIP, 0, len(in))
	for _, e := range in {
		if e.IsSelected() {
			out = append(out, e)
		}
	}
	return out
}

func activeSNISNIs(in []SNISNI) []SNISNI {
	out := make([]SNISNI, 0, len(in))
	for _, e := range in {
		if e.IsSelected() {
			out = append(out, e)
		}
	}
	return out
}

// buildSpoofConfigJSON renders the exact engine config schema
// (CORE_REPORT.md section 3). Option keys with zero/empty values are
// omitted so the engine default applies. MODE is always "SNI Only".
func buildSpoofConfigJSON(profile *Profile, settings Settings, listenPort int, endpointIP string, endpointPort int, fakeSNI string) ([]byte, error) {
	if endpointPort < 1 || endpointPort > 65535 {
		return nil, fmt.Errorf("spoof endpoint port must be 1..65535 (got %d)", endpointPort)
	}
	if strings.TrimSpace(endpointIP) == "" {
		return nil, fmt.Errorf("spoof endpoint IP is empty")
	}
	if normalizeFakeSNI(fakeSNI) == "" {
		return nil, fmt.Errorf("spoof fake SNI is empty")
	}
	cfg := map[string]any{
		"LISTEN_HOST": "127.0.0.1",
		"LISTEN_PORT": listenPort,
		"ENDPOINTS":   buildSpoofEndpoints(endpointIP, endpointPort, settings),
		"FAKE_SNIS":   buildSpoofSNIs(fakeSNI, settings),
		"MODE":        "SNI Only",
	}
	sc := settings.SNIConfig
	if strings.TrimSpace(sc.Method) != "" {
		cfg["BYPASS_METHOD"] = strings.TrimSpace(sc.Method)
	}
	if strings.TrimSpace(sc.Fingerprint) != "" {
		cfg["TLS_FINGERPRINT"] = strings.ToLower(strings.TrimSpace(sc.Fingerprint))
	}
	if strings.TrimSpace(sc.QUICMode) != "" {
		cfg["QUIC_MODE"] = strings.ToLower(strings.TrimSpace(sc.QUICMode))
	}
	if strings.TrimSpace(sc.IPMode) != "" {
		cfg["IP_MODE"] = strings.ToLower(strings.TrimSpace(sc.IPMode))
	}
	if sc.HandshakeTimeout != 0 {
		cfg["HANDSHAKE_TIMEOUT"] = sc.HandshakeTimeout
	}
	if sc.MaxConnections != 0 {
		cfg["MAX_CONNECTIONS"] = sc.MaxConnections
	}
	if sc.FakeDelay != 0 {
		cfg["FAKE_DELAY"] = sc.FakeDelay
	}
	if sc.SeqOverlap != 0 {
		cfg["SEQ_OVERLAP"] = sc.SeqOverlap
	}
	if sc.PaddingSize != 0 {
		cfg["PADDING_SIZE"] = sc.PaddingSize
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// Start validates the profile, renders the engine config, and spawns
// sni-spoof.exe. Only trojan/vless profiles can ride the spoof core:
// the engine relays raw TLS bytes, nothing else.
//
// Target resolution (DNS, up to 3s) runs WITHOUT holding the manager
// mutex so the SNI tab's 2s status poll and GetStats never stall
// behind a slow resolver — that stall was part of the "so slow"
// report.
func (m *SpoofManager) Start(profile *Profile, settings Settings) error {
	if profile == nil {
		return fmt.Errorf("nil profile")
	}
	if !profile.Core.IsSpoof() {
		return fmt.Errorf("spoof manager needs a CoreSpoof profile (got %s)", profile.Core.String())
	}
	if profile.Protocol != "trojan" && profile.Protocol != "vless" {
		return fmt.Errorf("sni-spoof needs a trojan or vless profile (got %q)", profile.Protocol)
	}
	if strings.TrimSpace(profile.Address) == "" {
		return fmt.Errorf("sni-spoof needs a profile address (domain or loopback)")
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("sni-spoof already running")
	}
	m.mu.Unlock()
	// Step 1: target resolution is the only network wait before spawn
	// (3s DNS budget in resolveDomainToCFIP). Unlocked on purpose.
	m.log("info", fmt.Sprintf("[spoof] starting spoofer (profile %q)", profile.Name))
	endpointIP, fakeSNI, endpointPort, err := resolveSNITarget(profile, settings)
	if err != nil {
		m.mu.Lock()
		m.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("sni-spoof already running")
	}
	exe, err := m.resolveSpoofExe()
	if err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	port, err := freePort()
	if err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("pick spoof listen port: %w", err)
	}
	cfgJSON, err := buildSpoofConfigJSON(profile, settings, port, endpointIP, endpointPort, fakeSNI)
	if err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	cfgPath := filepath.Join(spoofExeDir(exe), fmt.Sprintf("spoof-config-%d.json", port))
	if err := os.WriteFile(cfgPath, cfgJSON, 0o600); err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("write spoof config: %w", err)
	}
	// Step 2: render the config and spawn the child, then WAIT for the
	// engine's readiness line before returning. engine.rs opens its
	// WinDivert handles and binds LISTEN_PORT inside
	// EngineHandle::start, and the CLI prints "[INFO] engine RUNNING"
	// (tracing: "listening on ...") only after that. Callers dial the
	// port immediately (Connect's transport, the spoof latency probe),
	// so returning at spawn time raced the bind: the first dial hit a
	// refused connection even though the engine was about to come up.
	m.log("info", fmt.Sprintf("[spoof] rendering config (port %d, endpoint %s:%d)", port, endpointIP, endpointPort))
	cmd := exec.Command(exe, "run", "-c", cfgPath)
	setHideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.Remove(cfgPath)
		m.lastError = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("spoof stdout pipe: %w", err)
	}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "spoof", ring: &m.recent}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(cfgPath)
		m.lastError = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("start sni-spoof: %w", err)
	}
	m.cmd = cmd
	m.process = cmd.Process
	m.configPath = cfgPath
	m.port = port
	m.running = true
	m.startedAt = time.Now()
	m.lastError = ""
	m.lastStats = SpoofStats{}
	m.recent.reset()
	m.mu.Unlock()

	ready := make(chan struct{})
	exited := make(chan struct{})
	var readyOnce sync.Once
	markReady := func() { readyOnce.Do(func() { close(ready) }) }

	// Scrape stdout: lifecycle lines to the log bus, [STATS] JSON
	// into lastStats. The readiness line releases the Start wait below.
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			m.recent.push(line)
			switch {
			case strings.HasPrefix(line, "[STATS]"):
				raw := strings.TrimSpace(strings.TrimPrefix(line, "[STATS]"))
				var st SpoofStats
				if err := json.Unmarshal([]byte(raw), &st); err != nil {
					m.mu.Lock()
					m.lastError = fmt.Sprintf("spoof stats parse: %v", err)
					m.mu.Unlock()
					continue
				}
				m.mu.Lock()
				m.lastStats = st
				m.mu.Unlock()
			case strings.HasPrefix(line, "[INFO]"):
				msg := strings.TrimSpace(strings.TrimPrefix(line, "[INFO]"))
				m.log("info", msg)
				if spoofReadyLine(msg) {
					markReady()
				}
			case strings.HasPrefix(line, "[WARN]"):
				m.log("warn", strings.TrimSpace(strings.TrimPrefix(line, "[WARN]")))
			case strings.HasPrefix(line, "[ERROR]"):
				msg := strings.TrimSpace(strings.TrimPrefix(line, "[ERROR]"))
				m.mu.Lock()
				m.lastError = msg
				m.mu.Unlock()
				m.log("error", msg)
			}
		}
	}()

	// Reap the child: an exit (crash or clean shutdown) flips running off
	// and logs, so Status never reports a dead process. A COMMITTED engine
	// that dies on its own is also reported upward (engine_exit.go): the
	// group's transport core keeps dialing the dead loopback port, so
	// without the report the UI stays "connected" over a black hole.
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		current := m.cmd == cmd && m.running && !m.stopRequested
		port := m.port
		if m.cmd == cmd {
			m.running = false
			m.stopRequested = false
		}
		handler := m.exitHandler
		started := m.startedAt
		m.mu.Unlock()
		close(exited)
		if !current {
			// Stop (or a newer spawn) already cleared the state: a clean
			// shutdown, not a fault.
			m.log("info", "spoof process exited")
			return
		}
		// The engine that published this port is gone. A committed group
		// gets the port cleared by handleEngineExit; a STANDALONE start (SNI
		// tab) has no group, so the port used to survive the crash and later
		// loopback connects / latency probes dialled it.
		clearSpoofFrontPortIf(port)
		ev := newEngineExit("spoof", cmd, err, started, m.recent.tail(engineLastLines))
		m.log("warn", ev.Summary())
		if handler != nil {
			handler(ev)
		}
	}()

	// The wait runs unlocked: a dead engine must fail with its own
	// [ERROR] text (e.g. WinDivert access denied) instead of letting
	// callers dial a port nobody listens on.
	timer := time.NewTimer(spoofStartupTimeout)
	defer timer.Stop()
	select {
	case <-ready:
		if !m.IsRunning() {
			last := m.GetLastError()
			if last == "" {
				last = "engine exited before its listener could accept"
			}
			_ = m.Stop()
			return fmt.Errorf("sni-spoof engine stopped during startup: %s", last)
		}
	case <-exited:
		last := m.GetLastError()
		_ = m.Stop()
		if last == "" {
			last = "the engine process exited"
		}
		return fmt.Errorf("sni-spoof engine failed to start: %s", last)
	case <-timer.C:
		last := m.GetLastError()
		_ = m.Stop()
		if last != "" {
			return fmt.Errorf("sni-spoof engine not ready after %s: %s", spoofStartupTimeout, last)
		}
		return fmt.Errorf("sni-spoof engine not ready after %s (no engine RUNNING line on stdout)", spoofStartupTimeout)
	}

	m.log("info", fmt.Sprintf("sni-spoof started on 127.0.0.1:%d -> %s:%d (sni=%s)", port, endpointIP, endpointPort, fakeSNI))
	m.log("info", fmt.Sprintf("[spoof] spoofer port %d ready", port))
	return nil
}

// spoofReadyLine reports whether an engine [INFO] line proves the
// listener is up: cli/main.rs prints "engine RUNNING" right after
// EngineHandle::start returns (WinDivert open + TcpListener bind
// complete), and the engine's tracing layer prints "listening on ..."
// immediately after the bind.
func spoofReadyLine(msg string) bool {
	return strings.Contains(msg, "engine RUNNING") || strings.Contains(msg, "listening on")
}

// Stop terminates the engine, waits up to 3s, removes the temp
// config, and marks the manager stopped. A non-running manager is a
// nil op. The stdout/Wait goroutine owns Wait(); Stop only signals
// and polls for the reaping (same contract as SingBoxManager.Stop).
func (m *SpoofManager) Stop() error {
	m.mu.Lock()
	proc, cmd, cfgPath := m.process, m.cmd, m.configPath
	running := m.running
	if running {
		// The reaper must see this exit as requested, not as a fault.
		m.stopRequested = true
	}
	m.mu.Unlock()
	if !running {
		// Crashed without Stop: still drop the temp config.
		if cfgPath != "" {
			_ = os.Remove(cfgPath)
			m.mu.Lock()
			if m.configPath == cfgPath {
				m.configPath = ""
				m.cmd = nil
				m.process = nil
			}
			m.mu.Unlock()
		}
		return nil
	}

	if proc == nil || cmd == nil {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return nil
	}
	// SIGINT first: the engine runs its shutdown path ([WARN] engine
	// stopped, exit 0 per CORE_REPORT.md section 6). Windows Go
	// cannot deliver SIGINT to another process, so escalate to Kill
	// when the graceful exit does not land.
	_ = proc.Signal(os.Interrupt)
	deadline := time.Now().Add(3 * time.Second)
	killed := false
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if !running {
			break
		}
		if time.Since(deadline.Add(-3*time.Second)) > 500*time.Millisecond && !killed {
			_ = proc.Kill()
			killed = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = proc.Kill()
	_ = os.Remove(cfgPath)

	m.mu.Lock()
	m.running = false
	m.cmd = nil
	m.process = nil
	m.configPath = ""
	m.mu.Unlock()
	return nil
}

// IsRunning reports the real child state (mutex-guarded, never an
// optimistic UI flag).
func (m *SpoofManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// GetStats returns the last scraped [STATS] snapshot.
func (m *SpoofManager) GetStats() (*SpoofStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.lastStats
	return &st, nil
}

// GetListenPort returns the engine LISTEN_PORT (0 when stopped).
func (m *SpoofManager) GetListenPort() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return 0
	}
	return m.port
}

// GetConfigPath returns the temp config path (tests + diagnostics).
func (m *SpoofManager) GetConfigPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configPath
}

// GetLastError returns the last engine error verbatim.
func (m *SpoofManager) GetLastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastError
}

// StartedAt returns the engine start time.
func (m *SpoofManager) StartedAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startedAt
}
