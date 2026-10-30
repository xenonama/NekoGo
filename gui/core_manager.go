package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CoreManager routes Start/Stop/Stats calls to the sing-box or Xray
// manager based on the active profile, per the
// architecture overview.
type CoreManager struct {
	mu         sync.RWMutex
	activeCore CoreType
	running    bool
	activeID   string

	// epoch invalidates in-flight Start probes: Stop (or a superseding
	// Start) bumps it, and Start commits only on an unchanged epoch, so
	// a racing Stop aborts the pending connect instead of being blocked
	// by it (integration BUG-001/002).
	epoch uint64

	// ephMu guards the owner-scoped ephemeral probes
	// (integration BUG-009): every EphemeralURLTest runs under a
	// cancellable scope tagged with its owner ("proxies" for
	// TestProfile rows, "network" for Network-tab use), so StopTest can
	// abort network-owned probes without touching the sibling tab's
	// (integration BUG-008). Probes are independent: Test All runs
	// several at once, and each one owns its own throwaway core, so a
	// new probe must NOT supersede an older one.
	ephMu     sync.Mutex
	ephGen    uint64
	ephProbes map[uint64]*ephProbe

	// aetherActive marks a connect that also runs the aether engine;
	// Stop/stopLocked then stop the engine with the transport.
	aetherActive bool
	// onFault receives an unexpected engine death (PattN SessionEnded)
	// after the group state flips and before the teardown runs. App uses
	// it to log, surface LastError and revert the auto system proxy.
	onFault func(EngineExit)

	singboxMgr *SingBoxManager
	xrayMgr    *XrayManager
	// spoofMgr owns the sni-spoof.exe child process (CoreSpoof).
	// The sing-box transport still carries the profile: CoreSpoof
	// connects run BOTH (spoof engine first, then sing-box with the
	// front-rewritten config).
	spoofMgr *SpoofManager
	// aetherMgr owns the aether.exe engine for aether profiles (the
	// one auxiliary process a connect runs; PattNG parity). Started
	// before the main core, which dials its SOCKS listener.
	aetherMgr *AetherManager
	// psiphonMgr owns the second aether.exe child of the SNI spoof →
	// Psiphon chain, run in psiphon-only mode with its upstream set to
	// the spoof transport. psiphonActive marks it as a member of the
	// committed CoreSpoof group so Stop and fault teardown cover it.
	psiphonMgr    *AetherManager
	psiphonActive bool
	psiphonPort   int
	// spoofXray records which transport the committed CoreSpoof group
	// runs on: Xray when Settings.SpoofXray selected it, sing-box
	// otherwise (always sing-box for the Psiphon chain).
	spoofXray bool
	// spoofUpstream records which sing-box binary the committed CoreSpoof
	// group's transport runs: the official CLI (Settings.CoreEngine
	// == "upstream") or the custom nekobox_core. Only meaningful when
	// spoofXray is false.
	spoofUpstream bool
	// upstreamMgr owns the official sing-box.exe CLI child process when
	// Settings.CoreEngine == "upstream". CLI-only: no Thrift stats,
	// connections, or tests.
	upstreamMgr *UpstreamSingBoxManager
	// upstreamActive records whether the active group's sing-box leg is
	// the upstream CLI instead of the custom nekobox_core: a CoreSingBox
	// connect, the transport of a CoreSpoof group, or the transport of an
	// aether connect.
	upstreamActive bool

	// tunActive marks a committed connect whose rendered config carries
	// the TUN inbound. GetStats then reports the adapter's own byte
	// counters (tun_traffic.go) instead of the engine's, which can stay
	// at zero in TUN mode while the tunnel carries the system.
	tunActive  bool
	tunTraffic tunTraffic
	// tunEngineUp/Down keep the engine's last cumulative reading of this
	// TUN session. When the adapter only becomes readable after a poll,
	// the adapter ledger starts from these numbers instead of zero, so
	// the reported total never falls at the source handoff.
	tunEngineUp   int64
	tunEngineDown int64
}

// Probe owners for EphemeralURLTest scoping.
const (
	probeOwnerProxies = "proxies"
	probeOwnerNetwork = "network"
)

// spoofProbeMu serializes probes that need a temporary SNI-spoof engine:
// sni_front.go publishes the live engine port through one process-wide
// atomic, so two temporary engines must never race on it. The lock is
// only taken for CoreSpoof profiles, never for ordinary sing-box/xray
// probes, so Test All stays concurrent.
var spoofProbeMu sync.Mutex

// aetherProbeMu serializes probes that spawn a temporary aether engine:
// aether.exe is the heaviest child of the app, and a Test All running
// one engine per ping piled up processes. Probes are serialized and reuse
// the live engine whenever one is published.
var aetherProbeMu sync.Mutex

// ephemeralStartMu serializes the short startup window (pick free port,
// spawn the throwaway core, wait for its listener) of ephemeral probes.
// freePort closes the probe socket before returning, so two concurrent
// probes can otherwise pick the same port and one core fails to bind.
// The latency probe itself runs outside this lock.
var ephemeralStartMu sync.Mutex

// ephProbe is one in-flight ephemeral latency test. stops holds the
// teardown hooks for the throwaway core that belongs to this probe, so
// StopEphemeralTests can abort the right process without touching
// sibling probes.
type ephProbe struct {
	owner   string
	cancel  context.CancelFunc
	stopped bool
	stops   []func()
}

// NewCoreManager wires both engine managers to one log channel and
// registers the engine-death callback on each of them.
func NewCoreManager(singboxMgr *SingBoxManager, xrayMgr *XrayManager) *CoreManager {
	m := &CoreManager{singboxMgr: singboxMgr, xrayMgr: xrayMgr}
	if singboxMgr != nil {
		singboxMgr.SetExitHandler(m.handleEngineExit)
	}
	if xrayMgr != nil {
		xrayMgr.SetExitHandler(m.handleEngineExit)
	}
	return m
}

// SetFaultHandler registers the App-side fault observer. Called once
// from NewApp.
func (m *CoreManager) SetFaultHandler(fn func(EngineExit)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onFault = fn
}

// engineInGroupLocked reports whether engine is a member of the
// committed connection group. A death report from a retired generation
// (already stopped or replaced) is ignored.
func (m *CoreManager) engineInGroupLocked(engine string) bool {
	switch engine {
	case "aether":
		return m.aetherActive
	case "psiphon":
		return m.psiphonActive
	case "spoof":
		// The SNI engine is the group's front: sing-box/xray only feed it.
		return m.activeCore == CoreSpoof
	case "sing-box":
		return m.aetherActive || m.activeCore == CoreSingBox ||
			(m.activeCore == CoreSpoof && !m.spoofXray)
	case "xray":
		return m.aetherActive || m.activeCore == CoreXray ||
			(m.activeCore == CoreSpoof && m.spoofXray)
	}
	return false
}

// handleEngineExit is the managers' death callback (PattN's
// SessionEnded → OnAetherSessionEnded). It flips the group state at
// once, tells App, and tears the surviving members down before
// returning: a Start that runs concurrently waits on the same lock, so
// it can never begin while a half-dead group still holds ports.
func (m *CoreManager) handleEngineExit(ev EngineExit) {
	m.mu.Lock()
	if !m.running || !m.engineInGroupLocked(ev.Engine) {
		m.mu.Unlock()
		return
	}
	epoch := m.epoch
	m.running = false
	m.activeID = ""
	handler := m.onFault
	m.mu.Unlock()
	if ev.Engine == "aether" {
		// Config rendering must not point at a dead SOCKS listener.
		clearAetherActivePort()
	}
	if ev.Engine == "spoof" {
		// Same for the published SNI front port: leaving it makes later
		// loopback connects and latency probes dial a closed port.
		clearSpoofFrontPort()
	}
	if handler != nil {
		handler(ev)
	}
	m.teardownFaultedGroup(epoch)
}

// teardownFaultedGroup stops the group that was current when the engine
// died. It refuses when Start already began a new generation, so a late
// teardown can never kill a fresh connection.
func (m *CoreManager) teardownFaultedGroup(epoch uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != epoch {
		return
	}
	m.epoch++
	_ = m.stopLocked()
}

// SetSpoofManager attaches the sni-spoof process manager. Called once
// from NewApp; a nil manager means CoreSpoof profiles fail fast.
func (m *CoreManager) SetSpoofManager(mgr *SpoofManager) {
	m.mu.Lock()
	m.spoofMgr = mgr
	m.mu.Unlock()
	if mgr != nil {
		mgr.SetExitHandler(m.handleEngineExit)
	}
}

// SpoofManager exposes the attached spoof manager for App wiring.
func (m *CoreManager) SpoofManager() *SpoofManager {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.spoofMgr
}

// SetAetherManager attaches the aether engine process manager.
// Called once from NewApp; a nil manager makes aether profiles fail
// fast.
func (m *CoreManager) SetAetherManager(mgr *AetherManager) {
	m.mu.Lock()
	m.aetherMgr = mgr
	m.mu.Unlock()
	if mgr != nil {
		mgr.SetExitHandler(m.handleEngineExit)
	}
}

// SetUpstreamSingBoxManager attaches the official sing-box.exe CLI manager.
// Called once from NewApp; a nil manager makes upstream CoreEngine fail fast.
func (m *CoreManager) SetUpstreamSingBoxManager(mgr *UpstreamSingBoxManager) {
	m.mu.Lock()
	m.upstreamMgr = mgr
	m.mu.Unlock()
	if mgr != nil {
		mgr.SetExitHandler(m.handleEngineExit)
	}
}

// SetPsiphonManager attaches the Psiphon-only aether instance the SNI
// spoof chain runs. Called once from NewApp; a nil manager makes the
// chain fail fast.
func (m *CoreManager) SetPsiphonManager(mgr *AetherManager) {
	m.mu.Lock()
	m.psiphonMgr = mgr
	m.mu.Unlock()
	if mgr != nil {
		mgr.SetExitHandler(m.handleEngineExit)
	}
}

// PsiphonChain reports the chain leg of the committed CoreSpoof group:
// whether Psiphon runs and the local SOCKS port it listens on.
func (m *CoreManager) PsiphonChain() (active bool, port int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.psiphonActive, m.psiphonPort
}

// aetherDriverProfile reports whether [p] needs the aether engine to
// run: protocol aether with a built outbound (raw JSON bypasses the
// engine and is dialed as a plain outbound).
func aetherDriverProfile(p *Profile) bool {
	return p != nil && p.Protocol == "aether" && strings.TrimSpace(p.RawOutbound) == ""
}

// Start connects using profile; stops any running core first.
//
// Lifecycle: stopping the previous core runs under the lock (as before),
// but the multi-second engine probe runs UNLOCKED. A Stop racing the
// probe bumps the epoch, so the commit below aborts (tearing down what
// was just started) instead of publishing a stale connection.
func (m *CoreManager) Start(profile *Profile, settings Settings) error {
	if profile == nil {
		return fmt.Errorf("nil profile")
	}
	m.mu.Lock()
	if m.running {
		if err := m.stopLocked(); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("stop previous core: %w", err)
		}
	}
	m.epoch++
	epoch := m.epoch
	wantXray := profile.Core.IsXray()
	wantSpoof := profile.Core.IsSpoof()
	wantAether := aetherDriverProfile(profile)
	wantUpstream := settings.CoreEngine == "upstream"
	aetherMgr := m.aetherMgr
	upstreamMgr := m.upstreamMgr
	m.mu.Unlock()

	var err error
	if wantAether {
		if aetherMgr == nil {
			return fmt.Errorf("aether manager not wired")
		}
		// The engine first: it owns the listener the main core's
		// outbound dials. A main-core failure below rolls the engine
		// back so no orphan aether process survives the Connect.
		// Settings.AetherPsiphon arms the engine's own chain mode
		// (Psiphon on top of the tunnel) for a profile that names no
		// Psiphon mode of its own.
		engineProfile := aetherPsiphonChainProfile(profile, settings)
		port, token, startErr := aetherMgr.StartWithToken(engineProfile, settings)
		if startErr != nil {
			return startErr
		}
		defer func() {
			if err != nil {
				// Stop only this Start's child: a newer Start may own the
				// manager by now (its own spawn reaped ours), and the
				// published port may already be the newer engine's.
				_ = aetherMgr.StopToken(token)
				clearAetherActivePortIf(port)
			}
		}()
		setAetherActivePort(port)
		if aetherProfilePsiphonChain(engineProfile) {
			// The Psiphon listener accepts a CONNECT before a tunnel
			// exists; only a real payload round trip proves the chain.
			if err = m.waitAetherPsiphonTunnel(port, psiphonChainWarmup, epoch, aetherMgr); err != nil {
				return err
			}
		}
		if err = m.startAetherMainCore(profile, settings, wantXray); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.epoch != epoch {
			_ = m.stopLocked()
			return fmt.Errorf("connect aborted (concurrent stop)")
		}
		// The engine can die between its readiness probe and this
		// commit; publishing then would advertise a group that already
		// lost the listener the main core dials.
		if !aetherMgr.Running() {
			_ = m.stopLocked()
			err = fmt.Errorf("aether engine exited during connect")
			return err
		}
		// Same for the transport: its death report was dropped while no
		// group was committed, so the commit itself has to see it.
		if !m.transportAlive(wantXray, wantUpstream) {
			_ = m.stopLocked()
			err = fmt.Errorf("core exited during connect")
			return err
		}
		if wantXray {
			m.activeCore = CoreXray
		} else {
			m.activeCore = CoreSingBox
		}
		m.running = true
		m.activeID = profile.ID
		m.aetherActive = true
		m.upstreamActive = !wantXray && settings.CoreEngine == "upstream"
		m.setTunSessionLocked(tunModeFor(profile, settings))
		return nil
	}
	if wantSpoof {
		m.activeCoreHintSpoof()
		err = m.startSpoofLocked(profile, settings)
	} else if wantXray {
		m.activeCoreHint(true)
		err = m.xrayMgr.Start(profile, settings)
	} else {
		m.activeCoreHint(false)
		if wantUpstream {
			if upstreamMgr == nil {
				return fmt.Errorf("upstream sing-box manager not wired")
			}
			err = upstreamMgr.Start(profile, settings)
		} else {
			err = m.singboxMgr.Start(profile, settings)
		}
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != epoch {
		// A Stop raced our probe: take down what we just started and
		// refuse to publish it.
		_ = m.stopLocked()
		return fmt.Errorf("connect aborted (concurrent stop)")
	}
	if !m.groupMemberAlive(wantSpoof, wantXray, wantUpstream) {
		_ = m.stopLocked()
		return fmt.Errorf("core exited during connect")
	}
	if wantSpoof {
		m.activeCore = CoreSpoof
		// The spoof group's transport can be the upstream CLI too; the
		// connection table and latency probes must then read the Clash API
		// (GetStats keeps reporting the spoof engine's own counters).
		m.upstreamActive = m.spoofUpstream
	} else if wantXray {
		m.activeCore = CoreXray
		m.upstreamActive = false
	} else {
		m.activeCore = CoreSingBox
		m.upstreamActive = wantUpstream
	}
	m.running = true
	m.activeID = profile.ID
	m.aetherActive = false
	m.setTunSessionLocked(tunModeFor(profile, settings))
	return nil
}

// setTunSessionLocked records whether this connect renders the TUN
// inbound and starts a fresh adapter-counter baseline. The baseline is
// read here, at connect, not on the first stats poll: the adapter's
// counters are lifetime totals, so a late baseline silently drops every
// byte carried in between. Callers hold m.mu.
func (m *CoreManager) setTunSessionLocked(on bool) {
	m.tunActive = on
	m.tunTraffic.reset()
	m.tunEngineUp, m.tunEngineDown = 0, 0
	if !on {
		return
	}
	if in, out, ok, _ := tunCountersFunc(tunInterfaceName); ok {
		m.tunTraffic.begin(in, out)
	}
}

// waitAetherPsiphonTunnel blocks until the aether engine's Psiphon chain
// carries real traffic (a SOCKS CONNECT plus TLS handshake through it).
// psiphon-tunnel-core answers its SOCKS listener before a tunnel exists,
// so a listener-only check would publish a group whose every page hangs.
// It aborts on a racing Stop (epoch) or a dead engine, and gives up
// after timeout.
func (m *CoreManager) waitAetherPsiphonTunnel(port int, timeout time.Duration, epoch uint64, aetherMgr *AetherManager) error {
	deadline := time.Now().Add(timeout)
	for {
		m.mu.RLock()
		stale := m.epoch != epoch
		m.mu.RUnlock()
		if stale {
			return fmt.Errorf("connect aborted (concurrent stop)")
		}
		if aetherMgr == nil || !aetherMgr.Running() {
			return fmt.Errorf("aether engine exited during connect")
		}
		if probePsiphonTunnel(port) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("psiphon chain on the aether tunnel did not carry traffic in %s", timeout)
		}
		time.Sleep(time.Second)
	}
}

// transportAlive reports whether the transport a pending (or committed)
// group chose is still running. Caller holds m.mu; the managers' locks
// are leaves, so the nesting cannot deadlock.
func (m *CoreManager) transportAlive(xray, upstream bool) bool {
	switch {
	case xray:
		return m.xrayMgr != nil && m.xrayMgr.IsRunning()
	case upstream:
		return m.upstreamMgr != nil && m.upstreamMgr.IsRunning()
	default:
		return m.singboxMgr != nil && m.singboxMgr.IsRunning()
	}
}

// groupMemberAlive reports whether the engine a pending Start just
// probed is still running at commit time. A manager that is nil never
// counts as alive: its start leg would have failed earlier.
func (m *CoreManager) groupMemberAlive(spoof, xray, upstream bool) bool {
	switch {
	case spoof:
		if m.spoofMgr == nil || !m.spoofMgr.IsRunning() {
			return false
		}
		if m.psiphonActive {
			// The chain's Psiphon exit keeps answering after its transport
			// died; both legs must be up for the connect to mean anything.
			if m.psiphonMgr == nil || !m.psiphonMgr.Running() {
				return false
			}
			return m.transportAlive(m.spoofXray, m.spoofUpstream)
		}
		return m.transportAlive(m.spoofXray, m.spoofUpstream)
	case xray:
		return m.xrayMgr != nil && m.xrayMgr.IsRunning()
	case upstream:
		return m.upstreamMgr != nil && m.upstreamMgr.IsRunning()
	default:
		return m.singboxMgr != nil && m.singboxMgr.IsRunning()
	}
}

// startAetherMainCore starts the transport that dials the running
// engine: xray per the profile's core choice, the official sing-box CLI
// when CoreEngine=upstream, the custom core otherwise.
func (m *CoreManager) startAetherMainCore(profile *Profile, settings Settings, wantXray bool) error {
	if wantXray {
		m.activeCoreHint(true)
		return m.xrayMgr.Start(profile, settings)
	}
	m.activeCoreHint(false)
	if settings.CoreEngine == "upstream" {
		if m.upstreamMgr == nil {
			return fmt.Errorf("upstream sing-box manager not wired")
		}
		return m.upstreamMgr.Start(profile, settings)
	}
	return m.singboxMgr.Start(profile, settings)
}

// startSpoofLocked runs the CoreSpoof connect sequence: the spoof
// engine FIRST (it owns the listen port), then the transport carrying
// the front-rewritten config — Xray when Settings.SpoofXray asks for it,
// sing-box otherwise. A transport failure rolls the engine back so no
// orphan spoof process survives the Connect. No silent fallbacks: any leg
// failing aborts the whole connect with its own error.
//
// With Settings.SpoofPsiphon the sequence gains a third leg: Psiphon
// runs as a plain proxy whose own dials ride the spoof transport, and
// the transport config routes the TUN (and the mixed inbound) into it.
// That chain always runs on sing-box: its TUN inbound is rendered there
// alone.
func (m *CoreManager) startSpoofLocked(profile *Profile, settings Settings) error {
	m.mu.RLock()
	spoofMgr, singboxMgr, xrayMgr, psiphonMgr := m.spoofMgr, m.singboxMgr, m.xrayMgr, m.psiphonMgr
	upstreamMgr := m.upstreamMgr
	m.mu.RUnlock()
	if spoofMgr == nil {
		return fmt.Errorf("sni-spoof manager not wired")
	}
	chain := spoofPsiphonChain(profile, settings)
	// Settings.SpoofXray moves a plain spoof connect onto the Xray
	// transport. The chain never follows: its TUN inbound exists only
	// in the sing-box rendering.
	useXray := settings.SpoofXray && !chain
	// Settings.CoreEngine selects WHICH sing-box binary carries the
	// spoofed traffic: the official CLI (upstream) or the custom core.
	// The engine choice applies to every sing-box leg, not just plain
	// profiles — otherwise selecting the official sing-box would still
	// spawn nekobox_core for the SNI transport.
	useUpstream := !useXray && settings.CoreEngine == "upstream"
	if useXray && xrayMgr == nil {
		return fmt.Errorf("xray manager is nil")
	}
	if useUpstream && upstreamMgr == nil {
		return fmt.Errorf("upstream sing-box manager not wired")
	}
	if !useXray && !useUpstream && singboxMgr == nil {
		return fmt.Errorf("sing-box manager is nil")
	}
	transportStop := func() error {
		switch {
		case useXray:
			return xrayMgr.Stop()
		case useUpstream:
			return upstreamMgr.Stop()
		default:
			return singboxMgr.Stop()
		}
	}
	transportStart := func() error {
		switch {
		case useXray:
			return xrayMgr.Start(profile, settings)
		case useUpstream:
			return upstreamMgr.Start(profile, settings)
		default:
			return singboxMgr.Start(profile, settings)
		}
	}
	m.mu.Lock()
	m.spoofXray = useXray
	m.spoofUpstream = useUpstream
	m.mu.Unlock()
	committed := false
	defer func() {
		if !committed {
			m.mu.Lock()
			m.spoofXray = false
			m.spoofUpstream = false
			m.mu.Unlock()
		}
	}()
	if chain {
		if psiphonMgr == nil {
			return fmt.Errorf("psiphon chain manager not wired")
		}
		// The chain lives in the rendered config; a full raw override
		// replaces that config and would leave the chain unreachable.
		if _, full, err := rawCoreOverride(settings.CoreOverrides); err == nil && full {
			return fmt.Errorf("the SNI spoof + Psiphon chain needs the rendered core config: clear the full raw core override first")
		}
	}
	if err := spoofMgr.Start(profile, settings); err != nil {
		return err
	}
	setSpoofFrontPort(spoofMgr.GetListenPort())
	if !chain {
		if err := transportStart(); err != nil {
			_ = spoofMgr.Stop()
			clearSpoofFrontPort()
			return err
		}
		committed = true
		return nil
	}
	// Psiphon's listener and the inbound its own dials take are chosen
	// before rendering: the transport config must name both.
	psiphonPort, upstreamPort, err := allocateSpoofChainPorts(settings, spoofMgr.GetListenPort())
	if err != nil {
		_ = spoofMgr.Stop()
		clearSpoofFrontPort()
		return err
	}
	setSpoofChainPorts(psiphonPort, upstreamPort)
	startErr := transportStart()
	// The config is rendered and accepted; ephemeral test cores must
	// never see the chain afterwards.
	clearSpoofChainPorts()
	if startErr != nil {
		_ = spoofMgr.Stop()
		clearSpoofFrontPort()
		return startErr
	}
	if _, err := psiphonMgr.StartPsiphonOnly(settings, psiphonPort, upstreamPort, psiphonChainWarmup); err != nil {
		_ = transportStop()
		_ = spoofMgr.Stop()
		clearSpoofFrontPort()
		return fmt.Errorf("psiphon chain: %w", err)
	}
	m.mu.Lock()
	m.psiphonActive = true
	m.psiphonPort = psiphonPort
	m.mu.Unlock()
	committed = true
	return nil
}

// activeCoreHintSpoof records a pending spoof Start like
// activeCoreHint does for the other cores.
func (m *CoreManager) activeCoreHintSpoof() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeCore = CoreSpoof
}

// activeCoreHint records which engine a pending Start is probing so
// readers (GetStats routing, tray labels) see a sensible core during
// the unlocked probe window. It never flips the committed state.
func (m *CoreManager) activeCoreHint(xray bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if xray {
		m.activeCore = CoreXray
	} else {
		m.activeCore = CoreSingBox
	}
}

// Stop disconnects whichever core is active. It bumps the epoch first so
// an in-flight Start aborts, then stops the engine WITHOUT holding m.mu
// (the engine managers have their own locks) so Shutdown/tray-Quit can
// never block on a hung probe.
func (m *CoreManager) Stop() error {
	m.mu.Lock()
	m.epoch++
	xrayMgr, singboxMgr, spoofMgr, aetherMgr, core := m.xrayMgr, m.singboxMgr, m.spoofMgr, m.aetherMgr, m.activeCore
	upstreamMgr, upstreamActive := m.upstreamMgr, m.upstreamActive
	psiphonMgr, spoofXray := m.psiphonMgr, m.spoofXray
	spoofUpstream := m.spoofUpstream
	aetherWasActive := m.aetherActive
	m.aetherActive = false
	m.psiphonActive = false
	m.psiphonPort = 0
	m.spoofXray = false
	m.spoofUpstream = false
	m.mu.Unlock()
	var err error
	// The chain's Psiphon leg is stopped unconditionally, whatever core
	// the group reports: a Disconnect landing while the chain was still
	// warming never committed psiphonActive, and a stale leg from an
	// earlier group must not keep serving after the disconnect. Stop on an
	// idle manager is a no-op.
	if psiphonMgr != nil {
		if pErr := psiphonMgr.Stop(); pErr != nil && err == nil {
			err = pErr
		}
	}
	if aetherWasActive {
		// The aether connect's transport is xray, the upstream CLI or the
		// custom core; stop the one this group started (the others' Stop is
		// a no-op).
		switch {
		case core == CoreXray && xrayMgr != nil:
			err = xrayMgr.Stop()
		case upstreamActive && upstreamMgr != nil:
			err = upstreamMgr.Stop()
		case singboxMgr != nil:
			err = singboxMgr.Stop()
		default:
			err = fmt.Errorf("sing-box manager is nil")
		}
		if aetherMgr != nil {
			if e2 := aetherMgr.Stop(); e2 != nil && err == nil {
				err = e2
			}
		}
		clearAetherActivePort()
		m.mu.Lock()
		m.running = false
		m.activeID = ""
		m.upstreamActive = false
		m.mu.Unlock()
		return err
	}
	switch core {
	case CoreXray:
		if xrayMgr == nil {
			err = fmt.Errorf("xray manager is nil")
		} else {
			err = xrayMgr.Stop()
		}
	case CoreSpoof:
		// Spoof connects run up to three legs; stop them in reverse
		// dependency order — Psiphon (the exit, already stopped above),
		// the transport carrying its dials, then the spoof engine — worst
		// error wins.
		// The transport is whichever SpoofXray / CoreEngine selected; the
		// other's Stop is a no-op.
		if spoofXray {
			if xrayMgr == nil {
				err = fmt.Errorf("xray manager is nil")
			} else if xErr := xrayMgr.Stop(); xErr != nil && err == nil {
				err = xErr
			}
		} else if spoofUpstream {
			if upstreamMgr == nil {
				err = fmt.Errorf("upstream sing-box manager is nil")
			} else if uErr := upstreamMgr.Stop(); uErr != nil && err == nil {
				err = uErr
			}
		} else if singboxMgr == nil {
			err = fmt.Errorf("sing-box manager is nil")
		} else if sErr := singboxMgr.Stop(); sErr != nil && err == nil {
			err = sErr
		}
		if spoofMgr != nil {
			if sErr := spoofMgr.Stop(); sErr != nil && err == nil {
				err = sErr
			}
		}
		clearSpoofChainPorts()
		clearSpoofFrontPort()
	default:
		if upstreamActive {
			if upstreamMgr != nil {
				err = upstreamMgr.Stop()
			} else {
				err = fmt.Errorf("upstream sing-box manager is nil")
			}
		} else {
			if singboxMgr == nil {
				err = fmt.Errorf("sing-box manager is nil")
			} else {
				err = singboxMgr.Stop()
			}
		}
	}
	if !aetherWasActive && aetherMgr != nil {
		// A Start still warming an aether engine (or waiting on its
		// Psiphon chain) has not committed aetherActive yet; without this
		// a Disconnect during that window would leave aether.exe behind.
		// Its own epoch check then aborts the connect.
		if aErr := aetherMgr.Stop(); aErr != nil && err == nil {
			err = aErr
		}
	}
	m.mu.Lock()
	m.running = false
	m.activeID = ""
	m.upstreamActive = false
	m.setTunSessionLocked(false)
	m.mu.Unlock()
	return err
}

func (m *CoreManager) stopLocked() error {
	var err error
	// Unconditional, like Stop: a half-started chain or a stale leg from
	// an earlier group must not outlive the group it belonged to. Stop on
	// an idle manager is a no-op.
	if m.psiphonMgr != nil {
		if pErr := m.psiphonMgr.Stop(); pErr != nil && err == nil {
			err = pErr
		}
	}
	if m.aetherActive {
		m.aetherActive = false
		clearAetherActivePort()
		if m.aetherMgr != nil {
			if e2 := m.aetherMgr.Stop(); e2 != nil && err == nil {
				err = e2
			}
		}
	}
	switch m.activeCore {
	case CoreXray:
		if m.xrayMgr == nil {
			err = fmt.Errorf("xray manager is nil")
		} else {
			err = m.xrayMgr.Stop()
		}
	case CoreSpoof:
		// Psiphon (the exit) is already stopped above; then the
		// transport, then the engine.
		m.psiphonActive = false
		m.psiphonPort = 0
		// The transport is whichever SpoofXray / CoreEngine selected; the
		// other's Stop is a no-op.
		if m.spoofXray {
			if m.xrayMgr == nil {
				err = fmt.Errorf("xray manager is nil")
			} else if xErr := m.xrayMgr.Stop(); xErr != nil && err == nil {
				err = xErr
			}
		} else if m.spoofUpstream {
			if m.upstreamMgr == nil {
				err = fmt.Errorf("upstream sing-box manager is nil")
			} else if uErr := m.upstreamMgr.Stop(); uErr != nil && err == nil {
				err = uErr
			}
		} else if m.singboxMgr == nil {
			err = fmt.Errorf("sing-box manager is nil")
		} else if sErr := m.singboxMgr.Stop(); sErr != nil && err == nil {
			err = sErr
		}
		m.spoofXray = false
		m.spoofUpstream = false
		if m.spoofMgr != nil {
			if sErr := m.spoofMgr.Stop(); sErr != nil && err == nil {
				err = sErr
			}
		}
		clearSpoofChainPorts()
		clearSpoofFrontPort()
	default:
		if m.upstreamActive {
			if m.upstreamMgr == nil {
				err = fmt.Errorf("upstream sing-box manager is nil")
			} else {
				err = m.upstreamMgr.Stop()
			}
		} else if m.singboxMgr == nil {
			err = fmt.Errorf("sing-box manager is nil")
		} else {
			err = m.singboxMgr.Stop()
		}
	}
	m.running = false
	m.activeID = ""
	m.upstreamActive = false
	m.setTunSessionLocked(false)
	return err
}

// GetStats returns traffic for the active core. A TUN connect reports
// the adapter's own byte counters first (the tunnel's ground truth for
// every engine); everything else, and a TUN adapter that cannot be
// read, falls through to the engine's counters. A spoof connect reports
// the engine's own counters (up/down bytes from its [STATS] snapshots)
// under core name "spoof".
func (m *CoreManager) GetStats() (*Stats, error) {
	m.mu.RLock()
	core := m.activeCore
	running := m.running
	upstreamActive := m.upstreamActive
	tunActive := m.tunActive
	tunSeedUp, tunSeedDown := m.tunEngineUp, m.tunEngineDown
	singboxMgr := m.singboxMgr
	xrayMgr := m.xrayMgr
	spoofMgr := m.spoofMgr
	upstreamMgr := m.upstreamMgr
	m.mu.RUnlock()
	if !running {
		return &Stats{Running: false, Core: core.String()}, nil
	}
	if tunActive {
		if st := m.tunStats(core, upstreamActive, tunSeedUp, tunSeedDown); st != nil {
			return st, nil
		}
	}
	st, err := m.engineStats(core, upstreamActive, spoofMgr, xrayMgr, upstreamMgr, singboxMgr)
	// A TUN session that is still on the engine fallback (the adapter is
	// not readable yet) remembers that reading: when the adapter takes
	// over, its ledger starts from it instead of restarting at zero.
	if tunActive && err == nil && st != nil && st.Running && !st.ApiDown {
		m.mu.Lock()
		if m.tunActive {
			m.tunEngineUp, m.tunEngineDown = st.Uplink, st.Downlink
		}
		m.mu.Unlock()
	}
	return st, err
}

// engineStats reports the active engine's own counters: the non-TUN
// source, and the fallback for a TUN session whose adapter cannot be
// read (yet).
func (m *CoreManager) engineStats(core CoreType, upstreamActive bool, spoofMgr *SpoofManager, xrayMgr *XrayManager, upstreamMgr *UpstreamSingBoxManager, singboxMgr *SingBoxManager) (*Stats, error) {
	if core == CoreSpoof {
		if spoofMgr == nil {
			return nil, fmt.Errorf("sni-spoof manager is nil")
		}
		st, err := spoofMgr.GetStats()
		if err != nil {
			return nil, err
		}
		return &Stats{
			Uplink:   int64(st.UpBytes),
			Downlink: int64(st.DownBytes),
			Running:  spoofMgr.IsRunning(),
			Core:     "spoof",
		}, nil
	}
	if core == CoreXray {
		if xrayMgr == nil {
			return nil, fmt.Errorf("xray manager is nil")
		}
		return xrayMgr.GetStats()
	}
	if upstreamActive {
		if upstreamMgr == nil {
			return nil, fmt.Errorf("upstream sing-box manager is nil")
		}
		return upstreamMgr.GetStats()
	}
	if singboxMgr == nil {
		return nil, fmt.Errorf("sing-box manager is nil")
	}
	return singboxMgr.GetStats()
}

// tunStats reports the TUN adapter's own byte counters for a TUN
// session. nil means "no reading yet": the adapter is not up, has
// carried nothing this session, or cannot be enumerated — the caller
// then asks the engine, exactly as before this source existed.
func (m *CoreManager) tunStats(core CoreType, upstream bool, seedUp, seedDown int64) *Stats {
	in, out, ok, _ := tunCountersFunc(tunInterfaceName)
	if !ok {
		return nil
	}
	up, down := m.tunTraffic.sample(in, out, seedUp, seedDown)
	if up <= 0 && down <= 0 {
		return nil
	}
	name := core.String()
	if core == CoreSingBox && upstream {
		name = upstreamCoreName
	}
	return &Stats{
		Running: true, Core: name,
		Uplink: up, Downlink: down,
		UplinkByTag:   map[string]int64{tunTrafficTag: up},
		DownlinkByTag: map[string]int64{tunTrafficTag: down},
	}
}

// ListConnections returns the active connection table. The upstream CLI
// backend has no Thrift API, so its rows come from the Clash API; the
// custom core keeps using its ListConnections RPC. Xray exposes no
// connection table, so a running xray/spoof core returns an empty list.
func (m *CoreManager) ListConnections() (*ConnectionList, error) {
	m.mu.RLock()
	running := m.running
	upstreamActive := m.upstreamActive
	spoofXray := m.spoofXray
	singboxMgr := m.singboxMgr
	upstreamMgr := m.upstreamMgr
	m.mu.RUnlock()
	if !running {
		return &ConnectionList{}, nil
	}
	if spoofXray {
		// A spoof connect on the Xray transport has no connection table.
		return &ConnectionList{}, nil
	}
	if upstreamActive {
		if upstreamMgr == nil {
			return nil, fmt.Errorf("upstream sing-box manager is nil")
		}
		return upstreamMgr.ListConnections()
	}
	if singboxMgr == nil {
		return &ConnectionList{}, nil
	}
	return singboxMgr.ListConnections()
}

// ProxiedURLTest measures the test URL through the RUNNING core, using the
// same mechanism as live traffic (sing-box Test RPC / xray local proxy).
func (m *CoreManager) ProxiedURLTest(url string, timeoutMs int) (*TestResult, error) {
	m.mu.RLock()
	core := m.activeCore
	running := m.running
	spoofXray := m.spoofXray
	upstreamActive := m.upstreamActive
	upstreamMgr := m.upstreamMgr
	xrayMgr := m.xrayMgr
	m.mu.RUnlock()
	if !running {
		return nil, fmt.Errorf("no active connection")
	}
	if url == "" {
		url = defaultTestURL
	}
	if upstreamActive {
		// The official CLI has no Test RPC: measure through the Clash
		// API delay endpoint, which probes the profile's own outbound.
		if upstreamMgr == nil {
			return nil, fmt.Errorf("upstream sing-box manager is nil")
		}
		latency, err := upstreamMgr.URLTest(url, timeoutMs)
		if err != nil {
			return nil, err
		}
		return &TestResult{OutboundTag: clashProxyTag, LatencyMs: int32(latency)}, nil
	}
	if core == CoreXray || spoofXray {
		if xrayMgr == nil {
			return nil, fmt.Errorf("xray manager is nil")
		}
		return xrayMgr.URLTest(url, int32(timeoutMs))
	}
	results, err := m.singboxMgr.URLTest(url, int32(timeoutMs))
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no test result")
	}
	r := results[0]
	return &TestResult{OutboundTag: r.OutboundTag, LatencyMs: r.LatencyMs, Error: r.Error}, nil
}

// beginAetherProbe makes an aether engine available for [profile] and
// returns the engine port the throwaway config must dial, plus the
// restore func. Port 0 means the profile needs no engine.
//
// The live connection's own engine is reused when it runs the same
// profile (never a second aether.exe for a ping), and its published
// port is left alone. Any other running aether engine shares this
// app's WARP identity files, so a second tunnel would disturb it:
// testing another profile then refuses, the same rule the scan follows
// (AetherScan). Otherwise one temporary engine runs at a time:
// aether.exe is the heaviest child, and a Test All used to spawn one
// per row. A probe never arms the Psiphon chain: it measures the
// profile itself, and a cold chain would fail every ping.
//
// The temporary engine's port is handed back to the caller instead of
// being published process-wide, so a Connect racing the probe can never
// have its published port replaced or cleared by it.
func (m *CoreManager) beginAetherProbe(profile *Profile, settings Settings) (int, func(), error) {
	noRestore := func() {}
	if !aetherDriverProfile(profile) {
		return 0, noRestore, nil
	}
	m.mu.RLock()
	aetherMgr := m.aetherMgr
	running := m.running
	aetherActive := m.aetherActive
	activeID := m.activeID
	m.mu.RUnlock()
	if aetherMgr == nil {
		return 0, noRestore, fmt.Errorf("aether manager not wired")
	}
	// The connected profile reuses the live engine. Port() is the
	// engine's own listener; the published port holds the same value and
	// is what the live config dials.
	if running && aetherActive && profile.ID != "" && activeID == profile.ID && aetherMgr.Running() {
		if p := aetherMgr.Port(); p > 0 {
			return p, noRestore, nil
		}
		if p, ok := currentAetherActivePort(); ok {
			return p, noRestore, nil
		}
		return 0, noRestore, fmt.Errorf("aether engine is running but its listener port is unknown")
	}
	// Any other aether engine — a committed aether connect, or one still
	// warming — shares this app's WARP identity files (aetherWorkDir).
	// A second tunnel on the same identity disturbs the live session
	// (the scan refuses for the same reason), so testing another aether
	// profile has to wait for the disconnect.
	if aetherMgr.Running() {
		return 0, noRestore, fmt.Errorf("disconnect the running Aether tunnel before testing another Aether profile")
	}
	aetherProbeMu.Lock()
	tmp := NewAetherManager(nil)
	port, err := tmp.Start(profile, settings)
	if err != nil {
		aetherProbeMu.Unlock()
		return 0, noRestore, fmt.Errorf("aether probe engine: %w", err)
	}
	return port, func() {
		_ = tmp.Stop()
		aetherProbeMu.Unlock()
	}, nil
}

// ephProbeCtx opens the owner-scoped probe context for one ephemeral
// test. Every call registers a new independent probe; a running probe
// is never cancelled by a newer one, so concurrent Test All workers do
// not abort each other. The caller must defer both the returned cancel
// and ephProbeForget(gen) so bookkeeping clears when its scope ends.
func (m *CoreManager) ephProbeCtx(owner string) (context.Context, context.CancelFunc, uint64) {
	m.ephMu.Lock()
	defer m.ephMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	if m.ephProbes == nil {
		m.ephProbes = make(map[uint64]*ephProbe)
	}
	m.ephGen++
	m.ephProbes[m.ephGen] = &ephProbe{owner: owner, cancel: cancel}
	return ctx, cancel, m.ephGen
}

// ephProbeForget removes one probe registration. It is keyed by the
// probe generation, so an older probe finishing late can never wipe a
// newer sibling's entry.
func (m *CoreManager) ephProbeForget(gen uint64) {
	m.ephMu.Lock()
	delete(m.ephProbes, gen)
	m.ephMu.Unlock()
}

// ephProbeAddStop registers a throwaway-core teardown hook for the
// probe gen. If the probe was already aborted between registration and
// this call, the hook runs immediately so no process is left behind.
func (m *CoreManager) ephProbeAddStop(gen uint64, fn func()) {
	m.ephMu.Lock()
	p, ok := m.ephProbes[gen]
	if ok && !p.stopped {
		p.stops = append(p.stops, fn)
		m.ephMu.Unlock()
		return
	}
	m.ephMu.Unlock()
	fn()
}

// StopEphemeralTests aborts the in-flight ephemeral probes owned by
// owner ("" matches any). Each probe tears down its own throwaway core
// when its scope ends; cancelling the scope (and invoking the registered
// stop hooks) makes that happen promptly. Probes owned by another tab
// are left untouched (integration BUG-008). Reports whether anything was
// stopped.
func (m *CoreManager) StopEphemeralTests(owner string) bool {
	m.ephMu.Lock()
	var cancels []context.CancelFunc
	var stops []func()
	for gen, p := range m.ephProbes {
		if owner != "" && p.owner != owner {
			continue
		}
		p.stopped = true
		cancels = append(cancels, p.cancel)
		stops = append(stops, p.stops...)
		delete(m.ephProbes, gen)
	}
	m.ephMu.Unlock()
	if len(cancels) == 0 {
		return false
	}
	for _, cancel := range cancels {
		cancel()
	}
	for _, fn := range stops {
		fn()
	}
	return true
}

// startEphemeralProbe picks a free mixed port and starts the throwaway
// core under ephemeralStartMu. prepare (optional) runs under the same
// lock before the spawn and returns a teardown func owned by the caller
// (returned even when startup fails). Holding the lock until the core's
// listener is up prevents two concurrent probes from picking the same
// free port before either has bound it.
func startEphemeralProbe(ctx context.Context, prepare func() (func(), error), start func(mixedPort int) (int, error)) (port int, teardown func(), err error) {
	ephemeralStartMu.Lock()
	defer ephemeralStartMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if prepare != nil {
		teardown, err = prepare()
		if err != nil {
			return 0, nil, err
		}
	}
	// StopEphemeralTests may have aborted us while prepare ran.
	if err := ctx.Err(); err != nil {
		return 0, teardown, err
	}
	mixedPort, err := freePort()
	if err != nil {
		return 0, teardown, err
	}
	port, err = start(mixedPort)
	if err != nil {
		return 0, teardown, err
	}
	return port, teardown, nil
}

// EphemeralURLTest measures the test URL through a throwaway core built
// from the given profile, then tears it down. The active connection (if
// any) is never touched: the ephemeral core uses its own process, RPC
// socket and mixed port.
//
// Every call owns a dedicated manager instance, so concurrent probes
// (Test All runs up to five) never share process state or supersede
// each other. The probe leg and the throwaway core run under the
// owner-scoped context, so StopEphemeralTests(owner) aborts exactly the
// probes it owns.
func (m *CoreManager) EphemeralURLTest(profile *Profile, settings Settings, url string, timeoutMs int, owner string) (*TestResult, error) {
	if profile == nil {
		return nil, fmt.Errorf("nil profile")
	}
	if owner == "" {
		owner = probeOwnerProxies
	}
	if url == "" {
		url = defaultTestURL
	}
	// BUG-038: clamp inside EphemeralURLTest (mirror
	// clampProbeTimeoutMs) so direct Go callers with negative/huge
	// values cannot arm a no-timeout client or overflow int64 nanos.
	if timeoutMs < minProbeTimeoutMs {
		timeoutMs = minProbeTimeoutMs
	} else if timeoutMs > maxProbeTimeoutMs {
		timeoutMs = maxProbeTimeoutMs
	}
	ctx, cancel, gen := m.ephProbeCtx(owner)
	defer cancel()
	defer m.ephProbeForget(gen)
	timeout := time.Duration(timeoutMs) * time.Millisecond
	// Aether profiles probe THROUGH their own thrownaway engine: the
	// ephemeral config dials 127.0.0.1:<engine port>, so a temp engine
	// runs while the probe does. The port is pinned on a copy of the
	// profile, never published, so the live connection's port is not
	// touched even when a Connect races the probe.
	aetherPort, restoreAether, err := m.beginAetherProbe(profile, settings)
	if err != nil {
		return nil, err
	}
	defer restoreAether()
	probeProfile := profile
	if aetherPort > 0 {
		clone := *profile
		clone.AetherEnginePort = aetherPort
		probeProfile = &clone
	}

	m.mu.RLock()
	xrayMgr, singboxMgr, spoofMgr := m.xrayMgr, m.singboxMgr, m.spoofMgr
	m.mu.RUnlock()

	// A plain spoof connect may run on either transport
	// (Settings.SpoofXray), so the probe must match what Connect would
	// start.
	useXray := profile.Core.IsXray() || spoofTransportXray(profile, settings)

	// Spoof profiles must be probed THROUGH the spoof engine: an
	// ephemeral core rendered without a live engine dials the remote (or
	// a dead loopback) directly, so DPI blocks it and the Test button
	// reported fail/slow even when Connect works. When no engine runs,
	// spin a temporary one for the probe; when the user is connected,
	// reuse the live engine port. Both transports need it.
	//
	// sni_front.go publishes that port process-wide, so temporary
	// engines are serialized. Ordinary probes stay concurrent.
	var prepare func() (func(), error)
	if profile.Core.IsSpoof() {
		spoofProbeMu.Lock()
		defer spoofProbeMu.Unlock()
		prepare = func() (func(), error) {
			if _, live := currentSpoofFrontPort(); live {
				return nil, nil
			}
			if spoofMgr == nil {
				return nil, fmt.Errorf("sni-spoof manager not wired")
			}
			tmp := NewSpoofManager(nil)
			// Share the exe override path: tmp.Start resolves the
			// same binary via the package-level overrides. Start
			// blocks until the engine reports its listener ready, so
			// the port published here is guaranteed to accept the
			// transport dial that follows.
			if err := tmp.Start(profile, settings); err != nil {
				return nil, fmt.Errorf("spoof probe engine: %w", err)
			}
			listenPort := tmp.GetListenPort()
			if listenPort <= 0 {
				_ = tmp.Stop()
				return nil, fmt.Errorf("spoof probe engine: not listening after start")
			}
			m.ephProbeAddStop(gen, func() { _ = tmp.Stop() })
			setSpoofFrontPort(listenPort)
			return func() {
				clearSpoofFrontPort()
				_ = tmp.Stop()
			}, nil
		}
	}

	if useXray {
		if xrayMgr == nil {
			return nil, fmt.Errorf("xray manager is nil")
		}
		// A dedicated manager per probe: EphemeralRun keeps a single
		// instance, so sharing the app-wide manager made concurrent
		// Test All workers abort one another.
		probe := NewXrayManager(xrayMgr.getXrayPath(), xrayMgr.workDir, xrayMgr.logCh)
		m.ephProbeAddStop(gen, probe.StopEphemeral)
		defer probe.StopEphemeral()
		port, teardown, err := startEphemeralProbe(ctx, prepare, func(mixedPort int) (int, error) {
			return probe.EphemeralRun(probeProfile, settings, mixedPort)
		})
		if teardown != nil {
			defer teardown()
		}
		if err != nil {
			return nil, err
		}
		d, err := proxiedHeadOrGetCtx(ctx, "127.0.0.1:"+strconv.Itoa(port), url, timeout)
		if err != nil {
			return nil, err
		}
		return &TestResult{OutboundTag: "proxy", LatencyMs: int32(d.Milliseconds())}, nil
	}

	if singboxMgr == nil {
		return nil, fmt.Errorf("sing-box manager is nil")
	}
	probe := NewSingBoxManager(singboxMgr.getCorePath(), singboxMgr.logCh)
	m.ephProbeAddStop(gen, probe.StopEphemeral)
	defer probe.StopEphemeral()
	port, teardown, err := startEphemeralProbe(ctx, prepare, func(mixedPort int) (int, error) {
		return probe.EphemeralRun(probeProfile, settings, mixedPort)
	})
	if teardown != nil {
		defer teardown()
	}
	if err != nil {
		return nil, err
	}
	d, err := proxiedHeadOrGetCtx(ctx, "127.0.0.1:"+strconv.Itoa(port), url, timeout)
	if err != nil {
		return nil, err
	}
	return &TestResult{OutboundTag: "proxy", LatencyMs: int32(d.Milliseconds())}, nil
}

// effectiveCoreLocked maps the committed group to the core that actually
// carries its traffic. A spoof connect runs on whichever transport
// Settings.SpoofXray selected, so with Xray the sing-box leg is not running
// and every tool that routes on the core type must see xray — otherwise it
// dials the dead Thrift port and reports "core not running".
func (m *CoreManager) effectiveCoreLocked() CoreType {
	if m.spoofXray {
		return CoreXray
	}
	return m.activeCore
}

// ActiveCore reports the committed-or-probing core name:
// "singbox" | "xray" | "spoof" | "". Used by the GUI-side system-proxy
// path, which needs the xray label for a spoof connect on the Xray
// transport too (it exposes split HTTP/SOCKS inbounds, not one mixed
// port).
func (m *CoreManager) ActiveCore() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch m.effectiveCoreLocked() {
	case CoreXray:
		return "xray"
	case CoreSpoof:
		return "spoof"
	case CoreSingBox:
		return "singbox"
	default:
		return ""
	}
}

// Status reports the active core, profile id and running flag.
func (m *CoreManager) Status() (core string, profileID string, running bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeCore.String(), m.activeID, m.running
}
