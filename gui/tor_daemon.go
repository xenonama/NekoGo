package main

// tor_daemon.go — the Tor Project's tor.exe run as an engine of its own.
//
// NekoGo has only ever reached Tor through the arti client linked into
// aether.exe. On the spoofed path that client stops at 15% of a
// bootstrap: the bridges carry the handshake to the guards, and no
// directory ever comes back through them ("Retiring circuit because of
// directory failure: Partial response"). The standalone daemon on the
// same path — the chain's fronted SOCKS as its outbound proxy, no
// bridges — went 0% to Bootstrapped 100% in 2m45s and carried traffic,
// measured on the machine this ships to on 2026-11-15. So the hop needs
// its own driver, and this file is the shape that driver cannot borrow
// from the arti one:
//
//   - tor.exe opens its --SocksPort at 0% of the bootstrap, so a
//     listener answer says only that a daemon is running. Readiness
//     rides the engine's own Bootstrapped line, then the listener.
//   - state is kept between connects, not per-connect: what makes the
//     second bootstrap seconds instead of minutes is the consensus and
//     guard set cached in --DataDirectory, and runProcess deletes the
//     directory it is handed.
//   - a bridge line is taken at face value at config time and then
//     fails: one that does not parse stops the run before it starts, and
//     a front-less webtunnel line leaves the transport dialing an empty
//     address for the whole readiness budget. Both were measured
//     against the shipped binary, so the lines the daemon cannot dial
//     are sorted out here and named in the log — without repeating
//     bridge material into it.

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// torDaemonLogArg is --Log's value: notice level on stdout. The
// bootstrap percentage the readiness rule waits for arrives there, and
// so do the daemon's own warnings and errors, which is what a failed
// connect is reported from.
const torDaemonLogArg = "notice stdout"

// torDaemonStateDirName is the folder a standalone daemon keeps its
// state in under the engine work dir: consensus and microdescriptor
// caches, guard state and the like. One folder for every connect, the
// way an installed Tor keeps one.
const torDaemonStateDirName = "tor-state"

// torBootstrapRe matches the daemon's progress lines —
// "Nov 15 23:35:13.000 [notice] Bootstrapped 30% (loading_status):
// Loading networkstatus consensus" — and nothing aether's arti prints,
// whose progress wording is "[*] tor reaching the network: 15%:".
var torBootstrapRe = regexp.MustCompile(`(?i)\bBootstrapped (\d{1,3})%`)

// torBridgeFPRe is the digest length tor.exe insists on: a shorter one
// is a config failure ("Key digest for Bridge is wrong length"), not a
// line the daemon tries and gives up on.
var torBridgeFPRe = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)

// torTransportNameRe matches the transport token a bridge line opens
// with: obfs4, webtunnel, snowflake, conjure.
var torTransportNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,31}$`)

// torTransportBinaries maps a bridge line's transport onto the pluggable
// transport binary beside tor.exe that serves it. lyrebird carries obfs4
// and webtunnel, which is what the bridge pages hand out; conjure is the
// bundle's own second binary. A transport not in this table has no
// client here, and tor.exe dials nothing for those lines.
var torTransportBinaries = map[string]string{
	"obfs4":     "lyrebird.exe",
	"webtunnel": "lyrebird.exe",
	"conjure":   "conjure-client.exe",
}

// torDaemonStateDir is the persistent --DataDirectory, created before
// the child runs. It sits under the engine work dir with the other
// engines' data, so it follows that folder's test hook and its uninstall
// and never lands in a temporary directory that gets pruned.
func torDaemonStateDir() string {
	dir := filepath.Join(aetherWorkDir(), torDaemonStateDirName)
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// torTransportBinaryPath locates a transport binary for the daemon to
// exec: the bundle's own pluggable_transports folder first (that is
// where the expert bundle and NekoGo's Tor updater put them, beside
// tor.exe), then the pt\ folder aether uses, so a tree staged for either
// engine serves the other. "" when it has neither.
func torTransportBinaryPath(exe, name string) string {
	for _, dir := range []string{torPtDir(exe), filepath.Join(filepath.Dir(exe), "pt")} {
		if p := filepath.Join(dir, name); exeExists(p) {
			return p
		}
	}
	return ""
}

// torBridgeReason sorts one saved bridge line into what the daemon can
// dial. The empty string means it can; anything else says what is wrong
// in shape words, because the reason goes into the app's log and the
// line itself must not.
func torBridgeReason(line string) (transport, reason string) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", "a line that is not a bridge line (transport, address, fingerprint)"
	}
	transport = strings.ToLower(fields[0])
	if !torTransportNameRe.MatchString(transport) {
		return "", "a line that names no transport"
	}
	host, port, err := net.SplitHostPort(fields[1])
	if err != nil || host == "" {
		return transport, "an address that is not host:port"
	}
	if p, perr := strconv.Atoi(port); perr != nil || p < 1 || p > 65535 {
		return transport, "an address with a port outside 1..65535"
	}
	if !torBridgeFPRe.MatchString(fields[2]) {
		return transport, "a line whose fingerprint is not 40 hex digits, which the daemon refuses before it starts"
	}
	if transport == "webtunnel" && !torLineHasOption(fields, "url=") {
		// Measured: with no front to build its request from, lyrebird
		// answers "dial tcp: missing address" once a second for as long
		// as the run is allowed to live.
		return transport, "a webtunnel line with no url= front, which its transport can only answer with an empty address"
	}
	if _, ok := torTransportBinaries[transport]; !ok {
		return transport, "a " + transport + " line, which no transport binary beside tor.exe serves"
	}
	return transport, ""
}

// torLineHasOption reports whether a bridge line carries an option whose
// name is [prefix] (url=…, iat-mode=…).
func torLineHasOption(fields []string, prefix string) bool {
	for _, f := range fields[3:] {
		if strings.HasPrefix(strings.ToLower(f), prefix) {
			return true
		}
	}
	return false
}

// torBridgeArgs renders the Chain tab's Tor bridge settings as tor.exe
// options. The answer is the argv to append, a note for the run's log
// when the argv does not say what the settings do, and an error when
// nothing the user asked for can be dialed at all — which has to be the
// app's word, not a bootstrap that stops at 2%.
func torBridgeArgs(exe string, s Settings) (args []string, note string, err error) {
	mode := aetherEnumDefault(aetherTorBridges, s.ChainTorBridges, "auto")
	saved := aetherBridgeLines(s.ChainTorBridgeLines)

	// Only own hands lines over. The other three are aether's shapes:
	// auto waits for plain Tor to fail and then fetches, first starts
	// over fetched bridges, never takes none at all. A standalone
	// daemon fetches nothing (BridgeDistribution is refused without
	// BridgeRelay, measured), so two of them arrive at plain relays —
	// and the run says so instead of leaving the user to work it out
	// after a five-minute wait.
	if mode != "own" {
		if len(saved) > 0 {
			note = torBridgesIdleNote(mode, len(saved))
		}
		return nil, note, nil
	}
	if len(saved) == 0 {
		// UseBridges with no bridge is a config failure that stops the
		// daemon at once, so the flag is left off and the log says why
		// the bridges the user believes in are not there.
		return nil, "Bridges is own but no bridge lines are saved: this run dials " +
			"plain relays through the front. Paste them under Chain → Tor to bridge it.", nil
	}

	var usable []string
	var reasons []string        // shape reasons, in first-seen order
	counted := map[string]int{} // reason → how many lines it covers
	seen := map[string]bool{}   // transports of the usable lines, in order
	for _, line := range saved {
		transport, why := torBridgeReason(line)
		if why != "" {
			counted[why]++
			if !seenReason(reasons, why) {
				reasons = append(reasons, why)
			}
			continue
		}
		binary := torTransportBinaries[transport]
		if torTransportBinaryPath(exe, binary) == "" {
			why = "an " + transport + " line, whose transport binary " + binary +
				" is not beside tor.exe"
			counted[why]++
			if !seenReason(reasons, why) {
				reasons = append(reasons, why)
			}
			continue
		}
		usable = append(usable, line)
		if !seen[transport] {
			seen[transport] = true
		}
	}

	if len(usable) == 0 {
		return nil, "", fmt.Errorf("none of the %d bridge lines on disk can be dialed by %s: %s — "+
			"set Bridges to never to dial plain relays through the front, or hand the daemon lines it can use",
			len(saved), torExeName, strings.Join(reasonList(counted, reasons), ", "))
	}

	args = append(args, "--UseBridges", "1")
	for _, line := range usable {
		args = append(args, "--Bridge", line)
	}
	// One argument per transport binary, naming every type it serves:
	// the daemon execs one process per entry and asks it for the types
	// in that entry's list.
	for _, binary := range torBinaryOrder() {
		var types []string
		for _, name := range torTransportOrder {
			if torTransportBinaries[name] == binary && seen[name] {
				types = append(types, name)
			}
		}
		if len(types) == 0 {
			continue
		}
		args = append(args, "--ClientTransportPlugin",
			strings.Join(types, ",")+" exec "+torTransportBinaryPath(exe, binary))
	}
	if len(reasons) > 0 {
		note = fmt.Sprintf("%d of the %d bridge lines on disk are not dialable by %s: %s; this run dials the other %d.",
			len(saved)-len(usable), len(saved), torExeName,
			strings.Join(reasonList(counted, reasons), ", "), len(usable))
	}
	return args, note, nil
}

// torTransportOrder is the transport-name order the plugin arguments are
// built in, so a rendered argv is stable: the map decides which binary
// serves a type, this decides where the type appears in the list.
var torTransportOrder = []string{"obfs4", "webtunnel", "conjure"}

// torBinaryOrder lists the transport binaries in the order their
// --ClientTransportPlugin arguments are emitted.
func torBinaryOrder() []string {
	var out []string
	for _, name := range torTransportOrder {
		binary := torTransportBinaries[name]
		if !seenReason(out, binary) {
			out = append(out, binary)
		}
	}
	return out
}

func seenReason(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// reasonList writes each reason that covers at least one line with its
// count, in the order the lines showed them.
func reasonList(counted map[string]int, reasons []string) []string {
	var out []string
	for _, why := range reasons {
		n := counted[why]
		if n == 0 {
			continue
		}
		if n == 1 {
			out = append(out, "one is "+why)
			continue
		}
		out = append(out, fmt.Sprintf("%d are %s", n, why))
	}
	return out
}

// torBridgesIdleNote is bridgeLinesIdleNote for this engine: the same
// warning a run whose saved lines stay home has to give, in the words of
// a daemon that fetches nothing of its own.
func torBridgesIdleNote(mode string, saved int) string {
	why := "which would dial plain relays first and fetch bridges only after that fails"
	switch mode {
	case "first":
		why = "which would start over on bridges the engine fetched"
	case "never":
		why = "which dials plain relays and takes no bridges at all"
	}
	take := "lines"
	if saved == 1 {
		take = "line"
	}
	return fmt.Sprintf("this run takes none of the %d bridge %s on disk: Bridges is %s, %s, and %s "+
		"fetches nothing of its own, so it dials plain relays through the front. "+
		"Set Bridges to own to hand them to the daemon.", saved, take, mode, why, torExeName)
}

// torDaemonArgs builds the argv one standalone-daemon run is started
// with: [socksPort] is the listener the chain publishes, [frontPort] the
// loopback inbound that routes into the spoofed tunnel (0 dials on its
// own), and [stateDir] the persistent --DataDirectory.
func torDaemonArgs(exe string, s Settings, socksPort, frontPort int, stateDir string) (args []string, note string, err error) {
	if socksPort < 1 || socksPort > 65535 {
		return nil, "", fmt.Errorf("tor daemon SOCKS port must be 1..65535 (got %d)", socksPort)
	}
	if frontPort < 0 || frontPort > 65535 {
		return nil, "", fmt.Errorf("tor daemon front port must be 0..65535 (got %d)", frontPort)
	}
	if exe == "" {
		return nil, "", fmt.Errorf("no %s to run", torExeName)
	}
	args = []string{
		// Absolute paths only: a relative --DataDirectory is resolved
		// against the child's working directory and the daemon says so
		// on every start.
		"--DataDirectory", stateDir,
		"--SocksPort", fmt.Sprintf("%s:%d", aetherLoopback, socksPort),
		"--AvoidDiskWrites", "1",
		"--Log", torDaemonLogArg,
		// The daemon sleeps while it thinks Tor is blocked; the chain
		// starts it because the user asked for Tor.
		"--DormantCanceledByStartup", "1",
	}
	if frontPort >= 1 {
		args = append(args, "--Socks5Proxy", fmt.Sprintf("%s:%d", aetherLoopback, frontPort))
	}
	bridgeArgs, note, err := torBridgeArgs(exe, s)
	if err != nil {
		return nil, note, err
	}
	return append(args, bridgeArgs...), note, nil
}

// noteBootstrap remembers how far a standalone daemon's bootstrap got,
// from its own Bootstrapped lines. The readiness gate and the failure
// report both read it, and a later line never lowers it.
func (m *AetherManager) noteBootstrap(line string) {
	groups := torBootstrapRe.FindStringSubmatch(line)
	if groups == nil {
		return
	}
	pct, err := strconv.Atoi(groups[1])
	if err != nil || pct < 0 || pct > 100 {
		return
	}
	for {
		have := int(m.bootPct.Load())
		if pct <= have || m.bootPct.CompareAndSwap(int32(have), int32(pct)) {
			return
		}
	}
}

// torDaemonReady is a standalone run's readiness: the daemon's own word
// that the bootstrap finished, and a listener that answers a SOCKS
// greeting. The listener alone is the false ready — tor.exe opens
// --SocksPort at 0%, long before a circuit exists to carry anything.
func (m *AetherManager) torDaemonReady(port int) bool {
	return m.bootPct.Load() >= 100 && answersSocksOnce(port)
}

// StartStandaloneTor runs the Tor Project's tor.exe as the chain's Tor
// hop: a plain client whose dials ride [upstreamPort], the loopback
// inbound that routes into the spoof front, publishing its own SOCKS
// listener on [port]. The state directory is kept afterwards — the
// cached consensus is why the next connect is not another minutes-long
// fetch — so nothing here hands runProcess a data dir to remove.
//
// [frontAlive] is for the hop that dials a child of this app rather
// than a port that will always be there: nil lets the run wait its whole
// budget, and a function that says no ends the wait at once, because a
// front that is gone cannot carry a circuit however long the daemon
// tries. The caller reads that outcome from the error it gets back.
// The token names the child this run spawned, so the connect that
// aborts later stops exactly it.
func (m *AetherManager) StartStandaloneTor(settings Settings, port, upstreamPort int,
	warmup time.Duration, frontAlive func() bool) (int, uint64, error) {
	if port < 1 || port > 65535 || upstreamPort < 1 || upstreamPort > 65535 {
		return 0, 0, fmt.Errorf("tor chain ports must be 1..65535 (got %d, %d)", port, upstreamPort)
	}
	exe, err := resolveTorExe(settings)
	if err != nil {
		return 0, 0, err
	}
	state := torDaemonStateDir()
	args, note, err := torDaemonArgs(exe, settings, port, upstreamPort, state)
	if err != nil {
		return 0, 0, err
	}
	m.log("info", fmt.Sprintf("tor chain: %s on %s:%d, dialling through %s:%d (state %s)",
		torExeName, aetherLoopback, port, aetherLoopback, upstreamPort, state))
	if note != "" {
		m.log("warn", note)
	}
	ready := m.torDaemonReady
	if frontAlive != nil {
		inner := m.torDaemonReady
		ready = func(p int) bool { return !frontAlive() || inner(p) }
	}
	listen, token, err := m.runProcess(exe, args, port, warmup, "", "", ready)
	return listen, token, err
}

// standaloneTorWarmup is the readiness budget one standalone tor.exe
// hop gets for this connect. The base covers a hop that has cached its
// Tor directory; with an empty state dir the same hop has to fetch the
// consensus and the bridge descriptors over the front it just stood up
// before its first circuit exists, which is minutes, so it gets
// torColdChainWarmup instead and the run says why. A base already worth
// more than that is left alone: the budget is a floor for the cold
// fetch, not a ceiling on what a caller asked for.
func standaloneTorWarmup(mgr *AetherManager, base time.Duration) time.Duration {
	if base >= torColdChainWarmup {
		return base
	}
	state := torDaemonStateDir()
	if torStateDirHasCache(state) {
		return base
	}
	mgr.log("info", fmt.Sprintf("tor chain: no Tor directory cached under %s yet, so this bootstrap gets %s for its first fetch instead of %s",
		state, torColdChainWarmup, base))
	return torColdChainWarmup
}

// torStateDirHasCache reports whether the daemon's state dir holds
// anything a previous run wrote: the files and their names differ
// between tor releases, and a directory full of zero-byte stubs is not
// a cache, so what counts is one non-empty regular file anywhere under
// it.
func torStateDirHasCache(dir string) bool {
	cached := false
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		if fi, err := e.Info(); err == nil && fi.Size() > 0 {
			cached = true
			return filepath.SkipAll
		}
		return nil
	})
	return cached
}

// standaloneTorFor chooses the implementation the Tor hop runs on: the
// Tor Project's daemon whenever the machine has one, and otherwise the
// reason it does not, in the words the fallback log line quotes.
func standaloneTorFor(settings Settings) (exe, whyArti string) {
	p, err := resolveTorExe(settings)
	if err != nil {
		return "", fmt.Sprintf("no standalone %s: %v", torExeName, err)
	}
	return p, ""
}
