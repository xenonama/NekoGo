package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// upstreamCoreName is the Stats.Core label the upstream backend reports.
const upstreamCoreName = "upstream sing-box"

// clashAPISecretBytes is the entropy of the generated clash_api secret;
// hex encoding doubles it, so 16 bytes render the 32-character token.
const clashAPISecretBytes = 16

// clashClosedTag buckets the bytes of connections that already closed.
// GET /connections reports uploadTotal/downloadTotal for the whole
// session (closed connections included) but per-connection counters
// only for the live ones, so the difference is attributed here: the
// ledger totals then match the API's own totals, and the bucket only
// ever grows (a closed connection never loses its bytes).
const clashClosedTag = "closed"

// clashProxyTag is the outbound tag BuildSingBoxConfig gives the
// profile's own outbound; latency queries address it directly.
const clashProxyTag = "proxy"

// maxClashDelayTimeoutMs is the largest timeout the Clash API accepts
// on /proxies/{tag}/delay: sing-box parses it with ParseInt(bitSize 16).
const maxClashDelayTimeoutMs = 32767

// defaultClashDelayTimeoutMs is the probe timeout used when the caller
// passes a non-positive value (matches TestProfile's default).
const defaultClashDelayTimeoutMs = 8000

// UpstreamSingBoxManager spawns the official SagerNet sing-box.exe CLI as
// a child process and talks to it through its local mixed inbound
// (SOCKS+HTTP on 127.0.0.1:MixedPort). There is no Thrift RPC on this
// backend, so stats, the connection table and latency probes go through
// the Clash API that Start injects into the generated config
// (experimental.clash_api on 127.0.0.1:<freePort>, Bearer secret).
type UpstreamSingBoxManager struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	running    bool
	startEpoch uint64
	logCh      chan LogEntry
	socksPort  int
	// apiPort/apiSecret address the injected Clash API; both are zero
	// while no upstream core runs.
	apiPort   int
	apiSecret string
	// traffic is the same monotonic ledger the custom core feeds, so a
	// stats poll can never move the counter backwards.
	traffic trafficTotals
	// apiErrLogged throttles the Clash API failure log to one line per
	// outage instead of one per second.
	apiErrLogged   bool
	tempConfigPath string
	// startedAt and exitHandler mirror SingBoxManager: an unexpected
	// death of the upstream CLI must fault the whole connection group
	// (spoof engine, Psiphon leg, aether engine) exactly like the custom
	// core's death does.
	startedAt   time.Time
	exitHandler func(EngineExit)
}

// SetExitHandler registers the cross-engine death report callback
// (CoreManager.handleEngineExit). Called once at wiring time.
func (m *UpstreamSingBoxManager) SetExitHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exitHandler = fn
}

func NewUpstreamSingBoxManager(logCh chan LogEntry) *UpstreamSingBoxManager {
	return &UpstreamSingBoxManager{logCh: logCh}
}

func (m *UpstreamSingBoxManager) log(level, msg string) {
	if m.logCh == nil {
		return
	}
	// Best-effort (see SingBoxManager.log): the commit below logs while
	// holding m.mu, so a full channel must not wedge the manager.
	select {
	case m.logCh <- LogEntry{Level: level, Source: "upstream-sing-box", Message: msg, Time: time.Now().Format(time.RFC3339)}:
	default:
	}
}

func (m *UpstreamSingBoxManager) Start(profile *Profile, settings Settings) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("upstream sing-box already running")
	}
	m.startEpoch++
	epoch := m.startEpoch
	m.socksPort = settings.MixedPort
	if m.socksPort <= 0 {
		m.socksPort = 2080
	}
	core, err := m.resolveUpstreamCore(settings)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	cfg, err := BuildSingBoxConfig(profile, settings)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	// FIX A: the official binary exposes no Thrift API, so stats,
	// connections and latency probes ride the Clash API — injected here
	// and never into the custom core's config.
	apiPort, err := freePort()
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("pick upstream clash api port: %w", err)
	}
	apiSecret, err := randomClashAPISecret()
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("generate upstream clash api secret: %w", err)
	}
	cfg, err = injectClashAPI(cfg, apiPort, apiSecret)
	if err != nil {
		m.mu.Unlock()
		return err
	}

	dir, err := os.MkdirTemp("", "nekogo-upstream-config-*")
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("create upstream config dir: %w", err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		m.mu.Unlock()
		return fmt.Errorf("write upstream config: %w", err)
	}
	m.tempConfigPath = dir

	cmd := exec.Command(core, "run", "-c", configPath)
	setHideWindow(cmd)
	cmd.Env = childEnvWithLogLevel(os.Environ(), settings.LogLevel, func(msg string) {
		m.log("warn", msg)
	})
	cmd.Stdout = &logWriter{ch: m.logCh, source: "upstream-sing-box"}
	cmd.Stderr = &logWriter{ch: m.logCh, source: "upstream-sing-box"}
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("start upstream sing-box: %w", err)
	}
	m.cmd = cmd
	m.startedAt = time.Now()
	// Snapshot under the lock: the probe below runs unlocked, and reading
	// m.socksPort there races a second Start writing it (a double connect
	// then probes the wrong port and reports a phantom "inbound not ready").
	socksPort := m.socksPort
	m.mu.Unlock()

	exited := make(chan error, 1)
	go m.watchChild(cmd, epoch, exited)

	addr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if m.startAborted(epoch) {
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(dir)
			return fmt.Errorf("upstream sing-box start aborted (concurrent stop)")
		}
		select {
		case err := <-exited:
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(dir)
			if m.startAborted(epoch) {
				return fmt.Errorf("upstream sing-box start aborted (concurrent stop)")
			}
			return fmt.Errorf("upstream sing-box exited during startup: %v", err)
		default:
		}
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(dir)
			if m.startAborted(epoch) {
				return fmt.Errorf("upstream sing-box start aborted (concurrent stop)")
			}
			return fmt.Errorf("upstream sing-box mixed inbound not ready at %s", addr)
		}
		time.Sleep(200 * time.Millisecond)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startEpoch != epoch {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("upstream sing-box start aborted (concurrent stop)")
	}
	m.running = true
	m.apiPort = apiPort
	m.apiSecret = apiSecret
	// A new core session starts a new traffic ledger: the counters of
	// the previous process are gone with the previous process.
	m.traffic.reset()
	m.apiErrLogged = false
	m.log("info", fmt.Sprintf("upstream sing-box started (profile %q, clash api 127.0.0.1:%d)", profile.Name, apiPort))
	return nil
}

func (m *UpstreamSingBoxManager) startAborted(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startEpoch != epoch
}

// watchChild owns Wait() for the child. epoch is the Start generation
// that spawned it: Stop (or a superseding Start) bumps startEpoch, so a
// child that exits after a deliberate stop is reported as stopped
// rather than as a crash.
func (m *UpstreamSingBoxManager) watchChild(cmd *exec.Cmd, epoch uint64, exited chan<- error) {
	err := cmd.Wait()
	if exited != nil {
		select {
		case exited <- err:
		default:
		}
	}
	m.mu.Lock()
	current := m.cmd == cmd && m.running && m.startEpoch == epoch
	dir := ""
	if m.cmd == cmd {
		m.cmd = nil
		m.running = false
		// The temp config carries the session's credentials; a crash used
		// to strand it because Stop's cleanup only runs while a cmd is
		// registered. Take it here, remove after unlocking.
		dir = m.tempConfigPath
		m.tempConfigPath = ""
	}
	handler := m.exitHandler
	started := m.startedAt
	m.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	if current {
		m.log("warn", fmt.Sprintf("upstream sing-box exited unexpectedly: %v", err))
		// A death after the commit is a fault: the group's other members
		// (spoof engine, Psiphon, aether) must be torn down like they are
		// for the custom core.
		if handler != nil {
			handler(newEngineExit("sing-box", cmd, err, started, nil))
		}
	} else {
		m.log("info", "upstream sing-box stopped")
	}
}

func (m *UpstreamSingBoxManager) Stop() error {
	m.mu.Lock()
	m.startEpoch++
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		m.mu.Lock()
		m.running = false
		m.apiPort = 0
		m.apiSecret = ""
		m.mu.Unlock()
		return nil
	}
	_ = cmd.Process.Kill()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.running
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	m.mu.Lock()
	m.running = false
	dir := m.tempConfigPath
	m.tempConfigPath = ""
	m.apiPort = 0
	m.apiSecret = ""
	m.cmd = nil
	m.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	m.log("info", "upstream sing-box stopped")
	return nil
}

func (m *UpstreamSingBoxManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// GetStats reads GET /connections from the injected Clash API and folds
// the byte counters into the shared monotonic ledger.
func (m *UpstreamSingBoxManager) GetStats() (*Stats, error) {
	if !m.IsRunning() {
		return &Stats{Running: false, Core: upstreamCoreName}, nil
	}
	var snap clashConnections
	if err := m.apiGet("/connections", 3*time.Second, &snap); err != nil {
		// The child is usually still fine (the API comes up moments
		// after the mixed inbound): flag the counters as stale instead
		// of blanking them, exactly like the custom core does on an RPC
		// failure.
		m.logAPIError(err)
		return &Stats{Running: true, Core: upstreamCoreName, ApiDown: true}, nil
	}
	m.resetAPIError()
	upByTag := map[string]int64{}
	downByTag := map[string]int64{}
	var liveUp, liveDown int64
	for _, c := range snap.Connections {
		tag := clashConnectionTag(c)
		upByTag[tag] += c.Upload
		downByTag[tag] += c.Download
		liveUp += c.Upload
		liveDown += c.Download
	}
	// The session totals also cover connections that already closed;
	// attribute that difference to clashClosedTag so the ledger total
	// equals what the core reports. It is never negative: the totals
	// only grow and always include every live connection.
	if rest := snap.UploadTotal - liveUp; rest > 0 {
		upByTag[clashClosedTag] += rest
	}
	if rest := snap.DownloadTotal - liveDown; rest > 0 {
		downByTag[clashClosedTag] += rest
	}
	up, down, upCum, downCum := m.traffic.observe(upByTag, downByTag)
	return &Stats{
		Running:       true,
		Core:          upstreamCoreName,
		Uplink:        up,
		Downlink:      down,
		UplinkByTag:   upCum,
		DownlinkByTag: downCum,
	}, nil
}

// ListConnections maps GET /connections rows onto the core-agnostic
// Connection struct (the Clash API exposes no protocol name, so that
// field stays empty).
func (m *UpstreamSingBoxManager) ListConnections() (*ConnectionList, error) {
	if !m.IsRunning() {
		return &ConnectionList{}, nil
	}
	var snap clashConnections
	if err := m.apiGet("/connections", 3*time.Second, &snap); err != nil {
		return nil, err
	}
	out := &ConnectionList{Connections: make([]Connection, 0, len(snap.Connections))}
	for _, c := range snap.Connections {
		host := c.Metadata.Host
		if host == "" {
			host = c.Metadata.DestinationIP
		}
		dest := host
		if c.Metadata.DestinationPort != "" {
			dest = net.JoinHostPort(host, c.Metadata.DestinationPort)
		}
		out.Connections = append(out.Connections, Connection{
			ID:       c.ID,
			Outbound: clashConnectionTag(c),
			Network:  c.Metadata.Network,
			Dest:     dest,
			Domain:   c.Metadata.Host,
			Process:  c.Metadata.ProcessPath,
			Upload:   c.Upload,
			Download: c.Download,
		})
	}
	return out, nil
}

// URLTest measures url through the profile's outbound using the Clash
// API delay endpoint and returns the latency in milliseconds.
func (m *UpstreamSingBoxManager) URLTest(url string, timeoutMs int) (int, error) {
	if !m.IsRunning() {
		return 0, fmt.Errorf("upstream sing-box not running")
	}
	if url == "" {
		url = defaultTestURL
	}
	// The API parses timeout as a 16-bit integer, so a larger value is a
	// 400 rather than a long probe.
	timeout := timeoutMs
	if timeout <= 0 {
		timeout = defaultClashDelayTimeoutMs
	}
	if timeout > maxClashDelayTimeoutMs {
		timeout = maxClashDelayTimeoutMs
	}
	q := neturl.Values{}
	q.Set("url", url)
	q.Set("timeout", strconv.Itoa(timeout))
	var resp struct {
		Delay int `json:"delay"`
	}
	path := "/proxies/" + neturl.PathEscape(clashProxyTag) + "/delay?" + q.Encode()
	if err := m.apiGet(path, time.Duration(timeout+5000)*time.Millisecond, &resp); err != nil {
		return 0, err
	}
	if resp.Delay <= 0 {
		return 0, fmt.Errorf("clash api returned no delay")
	}
	return resp.Delay, nil
}

// CheckConfig validates a config by running the official CLI's own
// `sing-box check`, which is the only validator this backend has (no
// Thrift CheckConfig RPC). A non-zero exit returns the CLI's stderr.
func (m *UpstreamSingBoxManager) CheckConfig(cfg string) error {
	core, err := m.resolveUpstreamCore(Settings{})
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "nekogo-upstream-check-*")
	if err != nil {
		return fmt.Errorf("create upstream check dir: %w", err)
	}
	defer os.RemoveAll(dir)
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("write upstream check config: %w", err)
	}
	cmd := exec.Command(core, "check", "-c", configPath)
	setHideWindow(cmd)
	var stderr strings.Builder
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("invalid sing-box config: %s", msg)
		}
		return fmt.Errorf("sing-box check failed: %w", err)
	}
	return nil
}

// injectClashAPI merges the clash_api block into a rendered upstream
// config. An existing experimental section (Advanced -> Config editor)
// is preserved: only clash_api is set.
func injectClashAPI(cfg string, port int, secret string) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(cfg), &doc); err != nil {
		return "", fmt.Errorf("upstream config: %w", err)
	}
	experimental, ok := doc["experimental"].(map[string]any)
	if !ok {
		experimental = map[string]any{}
	}
	experimental["clash_api"] = map[string]any{
		"external_controller": fmt.Sprintf("127.0.0.1:%d", port),
		"secret":              secret,
	}
	doc["experimental"] = experimental
	data, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("upstream config: %w", err)
	}
	return string(data), nil
}

// randomClashAPISecret returns a random 32-character hex token.
func randomClashAPISecret() (string, error) {
	buf := make([]byte, clashAPISecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// clashConnections mirrors the GET /connections response of the Clash
// API (sing-box experimental/clashapi/connections.go).
type clashConnections struct {
	DownloadTotal int64             `json:"downloadTotal"`
	UploadTotal   int64             `json:"uploadTotal"`
	Connections   []clashConnection `json:"connections"`
}

type clashConnection struct {
	ID       string            `json:"id"`
	Upload   int64             `json:"upload"`
	Download int64             `json:"download"`
	Chains   []string          `json:"chains"`
	Rule     string            `json:"rule"`
	Metadata clashConnMetadata `json:"metadata"`
}

type clashConnMetadata struct {
	Network         string `json:"network"`
	Type            string `json:"type"`
	SourceIP        string `json:"sourceIP"`
	DestinationIP   string `json:"destinationIP"`
	SourcePort      string `json:"sourcePort"`
	DestinationPort string `json:"destinationPort"`
	Host            string `json:"host"`
	ProcessPath     string `json:"processPath"`
}

// clashConnectionTag is the outbound a connection was routed to:
// chains[0], with a fallback for a row that carries no chain.
func clashConnectionTag(c clashConnection) string {
	for _, tag := range c.Chains {
		if tag != "" {
			return tag
		}
	}
	return "unknown"
}

// apiGet performs one authenticated GET against the injected Clash API
// and decodes its JSON body.
func (m *UpstreamSingBoxManager) apiGet(path string, timeout time.Duration, out any) error {
	m.mu.Lock()
	port, secret := m.apiPort, m.apiSecret
	m.mu.Unlock()
	if port <= 0 {
		return fmt.Errorf("upstream clash api is not running")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("clash api %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("clash api %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("clash api %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("clash api %s: HTTP %d: %s", path, resp.StatusCode, msg)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("clash api %s: %w", path, err)
	}
	return nil
}

// logAPIError logs one line per Clash API outage instead of one per
// stats poll (the frontend polls once a second).
func (m *UpstreamSingBoxManager) logAPIError(err error) {
	m.mu.Lock()
	already := m.apiErrLogged
	m.apiErrLogged = true
	m.mu.Unlock()
	if !already {
		m.log("warn", fmt.Sprintf("upstream clash api unavailable: %v", err))
	}
}

func (m *UpstreamSingBoxManager) resetAPIError() {
	m.mu.Lock()
	m.apiErrLogged = false
	m.mu.Unlock()
}

// upstreamUnsupportedProtocols are the NekoGo-only outbound types the
// official sing-box binary is not built with; a profile that needs one
// of them can only run on the custom core.
var upstreamUnsupportedProtocols = map[string]bool{
	"mieru":       true,
	"juicity":     true,
	"trusttunnel": true,
}

// upstreamUnsupportedProfileMessage is the Connect-time refusal shown
// when the opt-in upstream core is asked to run a NekoGo-only profile.
const upstreamUnsupportedProfileMessage = "This profile uses a protocol the upstream sing-box core cannot run. Switch to the Custom core in Settings → Connection, or use a different profile."

// upstreamUnsupportedReason reports why p cannot run on the official
// sing-box build ("" when it can). Detection is deliberately narrow and
// exact: a NekoGo-only protocol name, a RawOutbound of such a type, or
// a VLESS outbound carrying the custom core's encryption extension.
func upstreamUnsupportedReason(p *Profile) string {
	if p == nil {
		return ""
	}
	if protocol := strings.ToLower(strings.TrimSpace(p.Protocol)); upstreamUnsupportedProtocols[protocol] {
		return fmt.Sprintf("protocol %q is NekoGo-only", protocol)
	}
	raw := strings.TrimSpace(p.RawOutbound)
	if raw == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return ""
	}
	typ, ok := obj["type"].(string)
	if !ok {
		return ""
	}
	typ = strings.ToLower(strings.TrimSpace(typ))
	if upstreamUnsupportedProtocols[typ] {
		return fmt.Sprintf("outbound type %q is NekoGo-only", typ)
	}
	if typ != "vless" {
		return ""
	}
	if enc, ok := obj["encryption"].(string); ok {
		enc = strings.ToLower(strings.TrimSpace(enc))
		if enc != "" && enc != "none" {
			return "VLESS encryption is NekoGo-only"
		}
	}
	return ""
}

// isOfficialSingBoxName reports whether [p] names the official sing-box
// CLI by file name. The name is what tells the two engines apart: the
// custom core is nekobox_core.exe, the official CLI sing-box.exe, and a
// setting pointing at the wrong one must fail instead of launching it.
func isOfficialSingBoxName(p string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(p)))
	return base == "sing-box.exe" || base == "sing-box"
}

// resolveUpstreamCore finds the official sing-box CLI: the dedicated
// UpstreamCorePath override first (a directory hosting sing-box[.exe] or
// a sing-box-named file), then bin\ next to nekogo.exe (shipped
// layout), then the exe's own folder (legacy copies), then PATH.
// Settings.CorePath is the custom core's override and is never consulted
// here.
func (m *UpstreamSingBoxManager) resolveUpstreamCore(settings Settings) (string, error) {
	if p := strings.TrimSpace(settings.UpstreamCorePath); p != "" {
		if info, err := os.Stat(p); err == nil {
			if info.IsDir() {
				c := filepath.Join(p, "sing-box.exe")
				if _, err := os.Stat(c); err == nil {
					return c, nil
				}
				c = filepath.Join(p, "sing-box")
				if _, err := os.Stat(c); err == nil {
					return c, nil
				}
			} else {
				base := strings.ToLower(filepath.Base(p))
				if base == "sing-box.exe" || base == "sing-box" {
					return p, nil
				}
			}
		}
		return "", fmt.Errorf("upstream sing-box not found at %q", p)
	}
	if c, ok := firstEngineFile("sing-box.exe", "sing-box"); ok {
		return c, nil
	}
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("sing-box.exe"); err == nil {
		return p, nil
	}
	return "", errors.New("sing-box.exe not found (place it in bin\\ next to nekogo.exe or set upstreamCorePath in settings)")
}
