package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var errConnectAborted = errors.New("connect aborted (concurrent stop)")

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
	// aetherTorActive marks an Aether connect that put its Tor hop on a
	// second child — the standalone daemon behind the tunnel — instead of
	// inside the engine. It is the group's own record, because a death
	// report reaches handleEngineExit after the manager that owned the
	// child already cleared its running flag.
	aetherTorActive bool
	// onFault receives an unexpected engine death (PattN SessionEnded)
	// after the group state flips and before the teardown runs. App uses
	// it to log, surface LastError and revert the auto system proxy.
	onFault func(EngineExit)

	// faultRaceHook is a test seam. It runs in the window between
	// accepting a death report and acting on it — the window a concurrent
	// Start lands in, which is what makes a death report "stale". Without
	// it the race can only be hit by luck, and a test that relies on luck
	// is not a regression test. Production leaves it nil.
	faultRaceHook func()

	// startHook pauses initial startup in lifecycle tests; nil in production.
	startHook func()

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
	// psiphonMgr and torMgr own the second aether.exe child of the SNI
	// spoof chain, run in psiphon-only or tor-only mode with its
	// upstream set to the spoof transport. One of them runs per connect,
	// whichever Settings armed; chainActive marks it as a member of the
	// committed CoreSpoof group so Stop and fault teardown cover it, and
	// chainKind says which one it was.
	psiphonMgr  *AetherManager
	torMgr      *AetherManager
	chainActive bool
	chainKind   string
	chainPort   int
	// chainUpstreamPort is the loopback inbound the chain's own dials
	// return through, paired with chainPort. A sticky reconnect needs
	// both to hand the same child to the next connect; see reconnect.go.
	chainUpstreamPort int
	// chainKeep is the one-shot a sticky reconnect arms so Stop spares
	// the exit leg. Stop always clears it, in the same critical section
	// that reads it.
	chainKeep bool
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
	// mihomoMgr owns the mihomo.exe (Clash Meta) child process for
	// CoreMihomo profiles. It is a whole core, not a transport: a mihomo
	// connect runs it alone, and its own external controller serves the
	// stats, connection table and latency probes.
	mihomoMgr *MihomoManager
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

var ephemeralImageApply atomic.Bool

var errCoreImageApply = errors.New("core update in progress")

// ephProbe is one in-flight ephemeral latency test. stops holds the
// teardown hooks for the throwaway core that belongs to this probe, so
// StopEphemeralTests can abort the right process without touching
// sibling probes.
type ephProbe struct {
	owner   string
	cancel  context.CancelFunc
	stopped bool
	stops   []func()
	done    chan struct{}
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
		return m.chainActive && m.chainKind == chainDriverPsiphon
	case "tor":
		// Two legs can run the daemon: the spoof chain's exit leg and the
		// Aether hop behind the tunnel. Either death takes its group down.
		return (m.chainActive && m.chainKind == chainDriverTor) || m.aetherTorActive
	case "spoof":
		// The SNI engine is the group's front: sing-box/xray only feed it.
		return m.activeCore == CoreSpoof
	case "mihomo":
		return m.activeCore == CoreMihomo
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
// returning. The teardown is generation-checked (teardownFaultedGroup):
// a Start does not block here — the lock is released between the two —
// so a report that lands after a newer group committed must leave that
// group standing.
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
	if m.faultRaceHook != nil {
		m.faultRaceHook()
	}
	// Nothing is unpublished here. These death reports are async: the
	// group they describe may already have been replaced by a Start that
	// published a fresh SNI front / aether listener, and clearing on sight
	// used to take that live port down (the epoch is only consulted in
	// teardownFaultedGroup below). The port instead goes away in the two
	// places that know which process owned it — the manager's own reaper
	// (spoof_manager.go, aether_manager.go: value-scoped, so a newer spawn
	// survives) and stopLocked, which runs only for the accepted teardown.
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

// SetMihomoManager attaches the mihomo.exe (Clash Meta) manager.
// Called once from NewApp; a nil manager makes CoreMihomo profiles fail
// fast instead of silently falling back to another engine.
func (m *CoreManager) SetMihomoManager(mgr *MihomoManager) {
	m.mu.Lock()
	m.mihomoMgr = mgr
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

// SetTorManager attaches the Tor-only aether instance the SNI spoof
// chain runs. Called once from NewApp; a nil manager makes a Tor chain
// fail fast, exactly as a nil Psiphon one does.
func (m *CoreManager) SetTorManager(mgr *AetherManager) {
	m.mu.Lock()
	m.torMgr = mgr
	m.mu.Unlock()
	if mgr != nil {
		mgr.SetExitHandler(m.handleEngineExit)
	}
}

// chainMgrLocked returns the manager that owns the [driver] chain leg.
// Caller holds m.mu.
func (m *CoreManager) chainMgrLocked(driver string) *AetherManager {
	if driver == chainDriverTor {
		return m.torMgr
	}
	return m.psiphonMgr
}

// ChainRun reports the chain leg of the committed CoreSpoof group:
// whether an exit chain runs, which driver it is, and the local SOCKS
// port it listens on.
func (m *CoreManager) ChainRun() (active bool, driver string, port int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.chainActive, m.chainKind, m.chainPort
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
func (m *CoreManager) Start(profile *Profile, settings Settings) (err error) {
	m.mu.Lock()
	m.epoch++
	epoch := m.epoch
	m.mu.Unlock()
	defer func() {
		if err == nil {
			return
		}
		m.mu.RLock()
		stale := m.epoch != epoch
		m.mu.RUnlock()
		if stale {
			err = errConnectAborted
		}
	}()
	if m.startHook != nil {
		m.startHook()
	}
	choice, err := resolveProfileCore(profile, settings)
	if err != nil {
		return err
	}
	profile = &choice.Profile
	m.mu.Lock()
	if m.epoch != epoch {
		m.mu.Unlock()
		return errConnectAborted
	}
	if m.running {
		if err := m.stopLocked(); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("stop previous core: %w", err)
		}
	}
	wantXray := profile.Core.IsXray()
	wantSpoof := spoofConnectEnabled(profile, settings)
	wantAether := aetherDriverProfile(profile)
	wantUpstream := choice.Engine == "upstream"
	wantMihomo := profile.Core.IsMihomo()
	if wantMihomo {
		wantUpstream = false
	}
	aetherMgr := m.aetherMgr
	torMgr := m.torMgr
	upstreamMgr := m.upstreamMgr
	mihomoMgr := m.mihomoMgr
	m.mu.Unlock()
	m.mu.RLock()
	stale := m.epoch != epoch
	m.mu.RUnlock()
	if stale {
		return errConnectAborted
	}

	// A chain leg a sticky reconnect left standing can only be adopted by
	// a spoof-chain connect (startSpoofLocked adopts it, or retires it if
	// the driver changed). Every other group — an aether profile, a plain
	// spoof profile, mihomo or xray — has no chain to inherit, so the
	// leftover child is reaped here rather than serving a port no config
	// names any more.
	if !wantSpoof || !spoofChain(profile, settings) {
		m.retireChainLeg()
	}

	// Every port this connect names is checked before any child exists,
	// whichever leg turns out to need it: the Aether path starts its
	// engine first and the spoof path its front, so a held inbound used to
	// be reported only after a child had been spawned, probed and torn
	// down — or behind that child's own unrelated error. The claim holds
	// the same numbers for the rest of the connect, so the ports picked
	// along the way (an RPC listener, a chain hop) cannot take one.
	addrs, ports := planConnectPorts(profile, settings)
	if err := waitBindPortsFree(addrs); err != nil {
		return err
	}
	defer claimConnectPorts(ports)()

	if wantAether {
		if aetherMgr == nil {
			return fmt.Errorf("aether manager not wired")
		}
		// The engine first: it owns the listener the main core's
		// outbound dials. A main-core failure below rolls the engine
		// back so no orphan aether process survives the Connect.
		// Settings.AetherPsiphon / AetherTor arm the engine's own chain
		// mode (Psiphon or Tor on top of the tunnel) for a profile that
		// names no mode of its own. One driver per leg, so only one of
		// the two copies anything.
		//
		// A Tor hop has two possible engines, and the choice is made
		// before the tunnel starts: with a tor.exe on the machine the
		// engine renders the tunnel alone and the daemon carries the hop
		// behind it, because the arti client linked into the engine
		// stops at 15% of its bootstrap on the networks this app ships
		// to. hopProfile keeps the Tor options either way — they are what
		// the daemon is handed when the split happens.
		hopProfile := aetherTorChainProfile(aetherPsiphonChainProfile(profile, settings), settings)
		daemonHop, hopNote := aetherTorHopDecision(hopProfile, settings, torMgr)
		engineProfile := hopProfile
		if daemonHop {
			engineProfile = aetherTunnelOnlyProfile(hopProfile)
		}
		if hopNote != "" {
			aetherMgr.log("info", hopNote)
		}
		port, token, startErr := aetherMgr.StartWithToken(engineProfile, settings)
		if startErr != nil {
			return startErr
		}
		// What the connect publishes and what the rollback unpublishes are
		// one value: the tunnel's listener until the hop takes it over.
		dialPort := port
		var hopToken uint64
		defer func() {
			if err != nil {
				// Stop only this Start's children: a newer Start may own
				// either manager by now (its own spawn reaped ours), and
				// the published port may already be the newer engine's.
				// The hop goes first: it dials through the tunnel, so a
				// daemon left behind would keep answering on a port whose
				// front no longer exists.
				_ = torMgr.StopToken(hopToken)
				_ = aetherMgr.StopToken(token)
				clearAetherActivePortIf(dialPort)
			}
		}()
		setAetherActivePort(port)
		switch {
		case aetherProfilePsiphonChain(engineProfile):
			// The Psiphon listener accepts a CONNECT before a tunnel
			// exists; only a real payload round trip proves the chain.
			if err = m.waitChainTunnel(chainDriverPsiphon, port, psiphonChainWarmup, epoch, aetherMgr); err != nil {
				return err
			}
		case aetherProfileTorChain(engineProfile):
			// The arti shape: Tor's listener answers a CONNECT while the
			// circuit is still being built, for the same reason Psiphon's
			// does, on a longer budget — a cold start fetches a directory
			// consensus first. With a daemon on the machine the profile
			// renders no tor mode at all and this case does not run.
			if err = m.waitChainTunnel(chainDriverTor, port, torChainWarmup, epoch, aetherMgr); err != nil {
				return err
			}
		}
		if daemonHop {
			// The second child, and the port that matters from here: the
			// main core renders an outbound to tor.exe, which dials the
			// tunnel started above as its own front.
			if dialPort, hopToken, err = m.startAetherTorHop(torMgr, settings, hopProfile,
				port, aetherMgr.Running); err != nil {
				return err
			}
		}
		if err = m.startAetherMainCore(profile, settings, choice.Engine, wantXray, wantMihomo); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.epoch != epoch {
			_ = m.stopLocked()
			// Named err: the deferred rollback above is what unpublishes
			// this connect's engine port and stops exactly its child, and
			// it only fires on a non-nil one.
			err = errConnectAborted
			return err
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
		if !m.transportAlive(wantXray, wantUpstream, wantMihomo) {
			_ = m.stopLocked()
			err = fmt.Errorf("core exited during connect")
			return err
		}
		// And for a hop that rides the tunnel: it passed its own gate
		// seconds ago, and a daemon that died in the render window would
		// otherwise be published as a listener nothing answers on.
		if daemonHop && !torMgr.Running() {
			_ = m.stopLocked()
			err = fmt.Errorf("tor hop exited during connect")
			return err
		}
		switch {
		case wantMihomo:
			m.activeCore = CoreMihomo
		case wantXray:
			m.activeCore = CoreXray
		default:
			m.activeCore = CoreSingBox
		}
		m.running = true
		m.activeID = profile.ID
		m.aetherActive = true
		// Which engine carries the hop is group state, not manager state:
		// the daemon's death report arrives once its own manager has
		// already forgotten it, and only the group knows the connect had a
		// second child worth tearing down.
		m.aetherTorActive = daemonHop
		m.upstreamActive = !wantXray && !wantMihomo && choice.Engine == "upstream"
		m.setTunSessionLocked(tunModeFor(profile, settings))
		return nil
	}
	if wantSpoof {
		m.activeCoreHintSpoof()
		err = m.startSpoofLocked(profile, settings)
	} else if wantMihomo {
		if mihomoMgr == nil {
			return fmt.Errorf("mihomo manager not wired")
		}
		m.activeCoreHintMihomo()
		err = mihomoMgr.Start(profile, settings)
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
		return errConnectAborted
	}
	if !m.groupMemberAlive(wantSpoof, wantXray, wantUpstream, wantMihomo) {
		_ = m.stopLocked()
		return fmt.Errorf("core exited during connect")
	}
	if wantSpoof {
		m.activeCore = CoreSpoof
		// The spoof group's transport can be the upstream CLI too; the
		// connection table and latency probes must then read the Clash API
		// (GetStats keeps reporting the spoof engine's own counters).
		m.upstreamActive = m.spoofUpstream
	} else if wantMihomo {
		m.activeCore = CoreMihomo
		m.upstreamActive = false
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
	m.aetherTorActive = false
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

// waitChainTunnel blocks until the aether engine's exit chain carries
// real traffic (a SOCKS CONNECT plus TLS handshake through it). Both
// drivers answer their SOCKS listener before a tunnel exists —
// psiphon-tunnel-core immediately, Tor while the circuit is still being
// built — so a listener-only check would publish a group whose every
// page hangs. It aborts on a racing Stop (epoch) or a dead engine, and
// gives up after timeout.
func (m *CoreManager) waitChainTunnel(driver string, port int, timeout time.Duration, epoch uint64, aetherMgr *AetherManager) error {
	deadline := time.Now().Add(timeout)
	for {
		m.mu.RLock()
		stale := m.epoch != epoch
		m.mu.RUnlock()
		if stale {
			return errConnectAborted
		}
		if aetherMgr == nil || !aetherMgr.Running() {
			return fmt.Errorf("aether engine exited during connect")
		}
		if probePsiphonTunnel(port) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s chain on the aether tunnel did not carry traffic in %s", driver, timeout)
		}
		time.Sleep(time.Second)
	}
}

// transportAlive reports whether the transport a pending (or committed)
// group chose is still running. Caller holds m.mu; the managers' locks
// are leaves, so the nesting cannot deadlock.
func (m *CoreManager) transportAlive(xray, upstream, mihomo bool) bool {
	switch {
	case mihomo:
		return m.mihomoMgr != nil && m.mihomoMgr.IsRunning()
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
func (m *CoreManager) groupMemberAlive(spoof, xray, upstream, mihomo bool) bool {
	switch {
	case spoof:
		if m.spoofMgr == nil || !m.spoofMgr.IsRunning() {
			return false
		}
		if m.chainActive {
			// The chain's exit hop keeps answering after its transport
			// died; both legs must be up for the connect to mean anything.
			mgr := m.chainMgrLocked(m.chainKind)
			if mgr == nil || !mgr.Running() {
				return false
			}
			return m.transportAlive(m.spoofXray, m.spoofUpstream, false)
		}
		return m.transportAlive(m.spoofXray, m.spoofUpstream, false)
	case mihomo:
		return m.mihomoMgr != nil && m.mihomoMgr.IsRunning()
	case xray:
		return m.xrayMgr != nil && m.xrayMgr.IsRunning()
	case upstream:
		return m.upstreamMgr != nil && m.upstreamMgr.IsRunning()
	default:
		return m.singboxMgr != nil && m.singboxMgr.IsRunning()
	}
}

// startAetherMainCore starts the resolved transport that dials the running engine.
func (m *CoreManager) startAetherMainCore(profile *Profile, settings Settings, engine string, wantXray, wantMihomo bool) error {
	if wantMihomo {
		if m.mihomoMgr == nil {
			return fmt.Errorf("mihomo manager not wired")
		}
		m.activeCoreHintMihomo()
		return m.mihomoMgr.Start(profile, settings)
	}
	if wantXray {
		m.activeCoreHint(true)
		return m.xrayMgr.Start(profile, settings)
	}
	m.activeCoreHint(false)
	if engine == "upstream" {
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
	spoofMgr, singboxMgr, xrayMgr := m.spoofMgr, m.singboxMgr, m.xrayMgr
	psiphonMgr, torMgr := m.psiphonMgr, m.torMgr
	upstreamMgr := m.upstreamMgr
	m.mu.RUnlock()
	if spoofMgr == nil {
		return fmt.Errorf("sni-spoof manager not wired")
	}
	driver := ""
	if spoofChain(profile, settings) {
		driver = spoofChainDriver(settings)
	}
	chain := driver != ""
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
		if driver == chainDriverTor {
			if torMgr == nil {
				return fmt.Errorf("tor chain manager not wired")
			}
		} else if psiphonMgr == nil {
			return fmt.Errorf("psiphon chain manager not wired")
		}
		// The chain lives in the rendered config; a full raw override
		// replaces that config and would leave the chain unreachable.
		if _, full, err := rawCoreOverride(settings.CoreOverrides); err == nil && full {
			return fmt.Errorf("the SNI spoof + %s chain needs the rendered core config: clear the full raw core override first", driver)
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
	// The chain's listener and the inbound its own dials take are chosen
	// before rendering: the transport config must name both, and name the
	// hop they belong to. A leg a sticky reconnect handed over keeps its
	// pair — the child already listens on one and dials the other, so the
	// new transport has to name the same two numbers or the reuse is just
	// a cold start behind different addresses.
	var chainPort, upstreamPort int
	kept := false
	if port, up, ok := m.chainHandoff(driver); ok {
		chainPort, upstreamPort, kept = port, up, true
	} else {
		// Not this connect's leg (no keep armed, the other driver, or a
		// child that died on its own): reap whatever the previous group
		// left standing before taking new ports.
		m.retireChainLeg()
		var allocErr error
		chainPort, upstreamPort, allocErr = allocateSpoofChainPorts(settings, spoofMgr.GetListenPort())
		if allocErr != nil {
			_ = spoofMgr.Stop()
			clearSpoofFrontPort()
			return allocErr
		}
	}
	claimPickedConnectPorts(chainPort, upstreamPort)
	setSpoofChainPorts(driver, chainPort, upstreamPort)
	transportErr := transportStart()
	// The config is rendered and accepted; ephemeral test cores must
	// never see the chain afterwards.
	clearSpoofChainPorts()
	if transportErr != nil {
		_ = spoofMgr.Stop()
		clearSpoofFrontPort()
		// A connect that failed names no chain ports at all, so the
		// handed-over child has nothing left to serve.
		if kept {
			m.retireChainLeg()
		}
		return transportErr
	}
	warmup := chainWarmup(driver)
	var startErr error
	switch {
	case kept:
		// No spawn, no home-server handshake, no consensus fetch: that is
		// the whole point of the keep. The child's tunnels are already
		// built and its streams resume as the transport's upstream inbound
		// starts listening again, seconds later.
	case driver == chainDriverTor:
		startErr = m.startTorChainLeg(torMgr, settings, chainPort, upstreamPort, warmup)
	default:
		_, startErr = psiphonMgr.StartPsiphonOnly(settings, chainPort, upstreamPort, warmup)
	}
	if startErr != nil {
		_ = transportStop()
		_ = spoofMgr.Stop()
		clearSpoofFrontPort()
		if kept {
			m.retireChainLeg()
		}
		return fmt.Errorf("%s chain: %w", driver, startErr)
	}
	m.mu.Lock()
	m.chainActive = true
	m.chainKind = driver
	m.chainPort = chainPort
	m.chainUpstreamPort = upstreamPort
	m.mu.Unlock()
	committed = true
	return nil
}

// startTorChainLeg runs the chain's Tor hop, and picks the Tor
// implementation that carries it.
//
// There are two: the Tor Project's tor.exe and the arti client linked
// into aether.exe. The daemon is the one that bootstraps on the spoofed
// path — arti stops at 15% there, where a fronted tor.exe reached 100%
// and carried traffic on the machine this ships to — so it runs whenever
// the machine has one, and its error is returned as it came: after an
// installed tor.exe has failed, falling through to arti would hide that
// failure behind a second engine's whole readiness budget. Only a machine
// with no tor.exe at all takes the arti leg, which is the shape that ran
// before the daemon existed. Either way the run says which engine carried
// the hop, because the two answer to different version numbers and one
// word, "tor", for both is how a compare-the-two-connects user ends up
// chasing the wrong binary.
func (m *CoreManager) startTorChainLeg(torMgr *AetherManager, settings Settings, chainPort, upstreamPort int, warmup time.Duration) error {
	exe, whyArti := standaloneTorFor(settings)
	if exe != "" {
		_, _, err := torMgr.StartStandaloneTor(settings, chainPort, upstreamPort,
			standaloneTorWarmup(torMgr, warmup), nil)
		return err
	}
	torMgr.log("info", fmt.Sprintf("tor chain: %s; running the hop on aether.exe's arti client instead", whyArti))
	_, err := torMgr.StartTorOnly(settings, chainPort, upstreamPort, warmup)
	return err
}

// startAetherTorHop runs the Aether leg's Tor hop on the standalone
// daemon: [torMgr] owns the child, [hop] is the profile the engine would
// have carried the hop with (its Tor options are the daemon's bridges),
// [tunnelPort] the listener the daemon dials through and [tunnelAlive]
// the engine's own answer, which ends the wait when the tunnel the hop
// depends on goes away — the same rule waitChainTunnel keeps for the
// arti shape, where a dead front would otherwise cost the connect its
// whole bootstrap budget.
//
// The daemon's listener is what the connect publishes: the main core's
// outbound has to dial Tor, and the tunnel becomes the daemon's own
// front. Nothing is published on the way out, so a hop that failed
// leaves the rollback to unpublish the tunnel it did start.
func (m *CoreManager) startAetherTorHop(torMgr *AetherManager, settings Settings, hop *Profile,
	tunnelPort int, tunnelAlive func() bool) (int, uint64, error) {
	// The engine holds tunnelPort already, so the probe cannot hand it
	// back; TCP and UDP both, because a WARP or scan listener can sit on
	// the number's UDP side alone.
	daemonPort, err := freePortTCPAndUDP()
	if err != nil {
		return 0, 0, err
	}
	claimPickedConnectPorts(daemonPort)
	listen, token, err := torMgr.StartStandaloneTor(aetherTorHopSettings(settings, hop),
		daemonPort, tunnelPort, standaloneTorWarmup(torMgr, torChainWarmup), tunnelAlive)
	if err != nil {
		return 0, token, err
	}
	if !tunnelAlive() {
		_ = torMgr.StopToken(token)
		return 0, 0, fmt.Errorf("the aether tunnel exited while %s was bootstrapping, so the hop has no front to dial", torExeName)
	}
	setAetherActivePort(listen)
	return listen, token, nil
}

// activeCoreHintSpoof records a pending spoof Start like
// activeCoreHint does for the other cores.
func (m *CoreManager) activeCoreHintSpoof() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeCore = CoreSpoof
}

// activeCoreHintMihomo records a pending mihomo Start like
// activeCoreHint does for the other cores.
func (m *CoreManager) activeCoreHintMihomo() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeCore = CoreMihomo
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

// stopSingBoxLegs takes down both sing-box binaries: the official CLI
// and the custom core. Which one a group runs is committed by Start only
// after that engine is already up, so `upstreamActive` still carries the
// PREVIOUS connect's answer for the whole probe window — and a
// Disconnect landing in it would otherwise stop a core that never ran
// and leave the live sing-box.exe behind, with its manager still
// reporting running: every later Connect then fails "upstream sing-box
// already running" until the app is restarted. A Stop on an idle manager
// is a no-op, so asking both is always safe (the Psiphon and aether legs
// above are stopped the same way).
func stopSingBoxLegs(upstreamActive bool, upstreamMgr *UpstreamSingBoxManager, singboxMgr *SingBoxManager) error {
	var err error
	if upstreamActive && upstreamMgr == nil {
		err = fmt.Errorf("upstream sing-box manager is nil")
	} else if !upstreamActive && singboxMgr == nil {
		err = fmt.Errorf("sing-box manager is nil")
	}
	if upstreamMgr != nil {
		if uErr := upstreamMgr.Stop(); uErr != nil && err == nil {
			err = uErr
		}
	}
	if singboxMgr != nil {
		if sErr := singboxMgr.Stop(); sErr != nil && err == nil {
			err = sErr
		}
	}
	return err
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
	mihomoMgr := m.mihomoMgr
	psiphonMgr, torMgr, spoofXray := m.psiphonMgr, m.torMgr, m.spoofXray
	spoofUpstream := m.spoofUpstream
	aetherWasActive := m.aetherActive
	// A sticky reconnect arms the keep for exactly this teardown; reading
	// and clearing it here is what stops a later plain Disconnect from
	// inheriting it. Only a committed spoof chain has a leg worth keeping.
	keepChain := m.chainKeep && m.chainActive && m.activeCore == CoreSpoof
	m.chainKeep = false
	var keepMgr *AetherManager
	if keepChain {
		keepMgr = m.chainMgrLocked(m.chainKind)
	} else {
		m.chainActive = false
		m.chainKind = ""
		m.chainPort = 0
		m.chainUpstreamPort = 0
	}
	m.aetherActive = false
	m.aetherTorActive = false
	m.spoofXray = false
	m.spoofUpstream = false
	m.mu.Unlock()
	var err error
	// The chain's Psiphon/Tor leg is stopped unconditionally, whatever
	// core the group reports: a Disconnect landing while the chain was
	// still warming never committed chainActive, and a stale leg from an
	// earlier group must not keep serving after the disconnect. Asking
	// both managers is always safe — the one that never started is idle,
	// and Stop on an idle manager is a no-op. The exception is the leg a
	// sticky reconnect handed over, which the next connect adopts.
	for _, chainMgr := range []*AetherManager{psiphonMgr, torMgr} {
		if chainMgr == nil || chainMgr == keepMgr {
			continue
		}
		if cErr := chainMgr.Stop(); cErr != nil && err == nil {
			err = cErr
		}
	}
	if aetherWasActive {
		// The aether connect's transport is mihomo, xray, the upstream CLI
		// or the custom core; stop the one this group started (the others'
		// Stop is a no-op).
		switch {
		case core == CoreMihomo && mihomoMgr != nil:
			err = mihomoMgr.Stop()
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
	case CoreMihomo:
		if mihomoMgr == nil {
			err = fmt.Errorf("mihomo manager is nil")
		} else {
			err = mihomoMgr.Stop()
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
		err = stopSingBoxLegs(upstreamActive, upstreamMgr, singboxMgr)
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
	// an earlier group must not outlive the group it belonged to. Asking
	// both managers is always safe — the one that never started is idle,
	// and Stop on an idle manager is a no-op.
	for _, chainMgr := range []*AetherManager{m.psiphonMgr, m.torMgr} {
		if chainMgr == nil {
			continue
		}
		if cErr := chainMgr.Stop(); cErr != nil && err == nil {
			err = cErr
		}
	}
	if m.aetherActive {
		m.aetherActive = false
		m.aetherTorActive = false
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
	case CoreMihomo:
		if m.mihomoMgr == nil {
			err = fmt.Errorf("mihomo manager is nil")
		} else {
			err = m.mihomoMgr.Stop()
		}
	case CoreSpoof:
		// The exit chain (Psiphon or Tor) is already stopped above; then
		// the transport, then the engine. A pending connect never keeps a
		// leg for the next one: this rollback reaps what it did not commit.
		m.chainActive = false
		m.chainKind = ""
		m.chainPort = 0
		m.chainUpstreamPort = 0
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
		err = stopSingBoxLegs(m.upstreamActive, m.upstreamMgr, m.singboxMgr)
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
	mihomoMgr := m.mihomoMgr
	m.mu.RUnlock()
	if !running {
		return &Stats{Running: false, Core: core.String()}, nil
	}
	if tunActive {
		if st := m.tunStats(core, upstreamActive, tunSeedUp, tunSeedDown); st != nil {
			return st, nil
		}
	}
	st, err := m.engineStats(core, upstreamActive, spoofMgr, xrayMgr, upstreamMgr, mihomoMgr, singboxMgr)
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
func (m *CoreManager) engineStats(core CoreType, upstreamActive bool, spoofMgr *SpoofManager, xrayMgr *XrayManager, upstreamMgr *UpstreamSingBoxManager, mihomoMgr *MihomoManager, singboxMgr *SingBoxManager) (*Stats, error) {
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
	if core == CoreMihomo {
		if mihomoMgr == nil {
			return nil, fmt.Errorf("mihomo manager is nil")
		}
		return mihomoMgr.GetStats()
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
// and mihomo backends have no Thrift API, so their rows come from the
// Clash REST controller each one runs; the custom core keeps using its
// ListConnections RPC. Xray exposes no connection table, so a running
// xray/spoof-on-xray core returns an empty list.
func (m *CoreManager) ListConnections() (*ConnectionList, error) {
	m.mu.RLock()
	running := m.running
	core := m.activeCore
	upstreamActive := m.upstreamActive
	spoofXray := m.spoofXray
	singboxMgr := m.singboxMgr
	upstreamMgr := m.upstreamMgr
	mihomoMgr := m.mihomoMgr
	m.mu.RUnlock()
	if !running {
		return &ConnectionList{}, nil
	}
	if spoofXray {
		// A spoof connect on the Xray transport has no connection table.
		return &ConnectionList{}, nil
	}
	if core == CoreMihomo {
		if mihomoMgr == nil {
			return &ConnectionList{}, nil
		}
		return mihomoMgr.ListConnections()
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
	mihomoMgr := m.mihomoMgr
	m.mu.RUnlock()
	if !running {
		return nil, fmt.Errorf("no active connection")
	}
	if url == "" {
		url = defaultTestURL
	}
	if core == CoreMihomo {
		// mihomo has no Test RPC either: measure through its own mixed
		// inbound, the same path live traffic takes.
		if mihomoMgr == nil {
			return nil, fmt.Errorf("mihomo manager is nil")
		}
		return mihomoMgr.URLTest(url, int32(timeoutMs))
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
	// The connected profile reuses the live engine, and the probe dials
	// what the live config dials: the published port. With a tor.exe hop
	// behind the tunnel that is the daemon's front, not the tunnel's own
	// listener — taking Port() first measured WARP alone and reported it
	// as the chain's latency.
	if running && aetherActive && profile.ID != "" && activeID == profile.ID && aetherMgr.Running() {
		if p, ok := currentAetherActivePort(); ok {
			return p, noRestore, nil
		}
		if p := aetherMgr.Port(); p > 0 {
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
	if ephemeralImageApply.Load() {
		cancel()
		return ctx, cancel, 0
	}
	if m.ephProbes == nil {
		m.ephProbes = make(map[uint64]*ephProbe)
	}
	m.ephGen++
	m.ephProbes[m.ephGen] = &ephProbe{owner: owner, cancel: cancel, done: make(chan struct{})}
	return ctx, cancel, m.ephGen
}

// ephProbeForget removes one probe registration. It is keyed by the
// probe generation, so an older probe finishing late can never wipe a
// newer sibling's entry.
func (m *CoreManager) ephProbeForget(gen uint64) {
	m.ephMu.Lock()
	if p, ok := m.ephProbes[gen]; ok {
		delete(m.ephProbes, gen)
		close(p.done)
	}
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
	for _, p := range m.ephProbes {
		if owner != "" && p.owner != owner {
			continue
		}
		p.stopped = true
		cancels = append(cancels, p.cancel)
		stops = append(stops, p.stops...)
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

func (m *CoreManager) beginImageApply() {
	ephemeralStartMu.Lock()
	ephemeralImageApply.Store(true)
	ephemeralStartMu.Unlock()
	m.StopEphemeralTests("")
}

func (m *CoreManager) waitImageApplyProbes(timeout time.Duration) error {
	m.ephMu.Lock()
	var done []<-chan struct{}
	for _, p := range m.ephProbes {
		done = append(done, p.done)
	}
	m.ephMu.Unlock()
	if len(done) == 0 {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for _, ch := range done {
		select {
		case <-ch:
		case <-timer.C:
			return errors.New("timed out stopping ephemeral core tests")
		}
	}
	return nil
}

func (m *CoreManager) endImageApply() {
	ephemeralImageApply.Store(false)
}

func commandUsesImage(cmd *exec.Cmd, path string) bool {
	return cmd != nil && samePath(cmd.Path, path)
}

func (m *CoreManager) usesImage(path string) bool {
	if m.singboxMgr != nil {
		m.singboxMgr.mu.Lock()
		used := commandUsesImage(m.singboxMgr.cmd, path) || commandUsesImage(m.singboxMgr.ephCmd, path)
		m.singboxMgr.mu.Unlock()
		if used {
			return true
		}
	}
	if m.xrayMgr != nil {
		m.xrayMgr.mu.Lock()
		used := commandUsesImage(m.xrayMgr.cmd, path) || commandUsesImage(m.xrayMgr.ephCmd, path)
		m.xrayMgr.mu.Unlock()
		if used {
			return true
		}
	}
	if m.upstreamMgr != nil {
		m.upstreamMgr.mu.Lock()
		used := commandUsesImage(m.upstreamMgr.cmd, path)
		m.upstreamMgr.mu.Unlock()
		if used {
			return true
		}
	}
	if m.mihomoMgr != nil {
		m.mihomoMgr.mu.Lock()
		used := commandUsesImage(m.mihomoMgr.cmd, path) || commandUsesImage(m.mihomoMgr.ephCmd, path)
		m.mihomoMgr.mu.Unlock()
		if used {
			return true
		}
	}
	for _, mgr := range []*AetherManager{m.aetherMgr, m.psiphonMgr, m.torMgr} {
		if mgr == nil {
			continue
		}
		mgr.mu.Lock()
		used := commandUsesImage(mgr.cmd, path)
		mgr.mu.Unlock()
		if used {
			return true
		}
	}
	return false
}

func (m *CoreManager) stopImageUsers(path string) error {
	var err error
	if m.singboxMgr != nil {
		m.singboxMgr.mu.Lock()
		active := commandUsesImage(m.singboxMgr.cmd, path)
		ephemeral := commandUsesImage(m.singboxMgr.ephCmd, path)
		m.singboxMgr.mu.Unlock()
		if active {
			if e := m.singboxMgr.Stop(); e != nil && err == nil {
				err = e
			}
		}
		if ephemeral {
			m.singboxMgr.StopEphemeral()
		}
	}
	if m.xrayMgr != nil {
		m.xrayMgr.mu.Lock()
		active := commandUsesImage(m.xrayMgr.cmd, path)
		ephemeral := commandUsesImage(m.xrayMgr.ephCmd, path)
		m.xrayMgr.mu.Unlock()
		if active {
			if e := m.xrayMgr.Stop(); e != nil && err == nil {
				err = e
			}
		}
		if ephemeral {
			m.xrayMgr.StopEphemeral()
		}
	}
	if m.upstreamMgr != nil {
		m.upstreamMgr.mu.Lock()
		active := commandUsesImage(m.upstreamMgr.cmd, path)
		m.upstreamMgr.mu.Unlock()
		if active {
			if e := m.upstreamMgr.Stop(); e != nil && err == nil {
				err = e
			}
		}
	}
	if m.mihomoMgr != nil {
		m.mihomoMgr.mu.Lock()
		active := commandUsesImage(m.mihomoMgr.cmd, path)
		ephemeral := commandUsesImage(m.mihomoMgr.ephCmd, path)
		m.mihomoMgr.mu.Unlock()
		if active {
			if e := m.mihomoMgr.Stop(); e != nil && err == nil {
				err = e
			}
		}
		if ephemeral {
			m.mihomoMgr.StopEphemeral()
		}
	}
	for _, mgr := range []*AetherManager{m.aetherMgr, m.psiphonMgr, m.torMgr} {
		if mgr == nil {
			continue
		}
		mgr.mu.Lock()
		active := commandUsesImage(mgr.cmd, path)
		mgr.mu.Unlock()
		if active {
			if e := mgr.Stop(); e != nil && err == nil {
				err = e
			}
		}
	}
	return err
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
	if ephemeralImageApply.Load() {
		return 0, nil, errCoreImageApply
	}
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
	choice, err := resolveProfileCore(profile, settings)
	if err != nil {
		return nil, err
	}
	profile = &choice.Profile
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
	if gen == 0 {
		return nil, errCoreImageApply
	}
	defer func() {
		cancel()
		m.ephProbeForget(gen)
	}()
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
	mihomoMgr, upstreamMgr := m.mihomoMgr, m.upstreamMgr
	m.mu.RUnlock()

	// A plain spoof connect may run on either transport
	// (Settings.SpoofXray), so the probe must match what Connect would
	// start.
	useXray := profile.Core.IsXray() || spoofTransportXray(profile, settings)

	// Spoof profiles are probed through a temporary engine rendered from
	// this profile and the current settings. Reusing a published live front
	// would test whichever config started that engine, not the selected row.
	// The temporary port stays on the profile copy, so the live connection's
	// process-wide front is never replaced or cleared.
	//
	// Settings → SNI spoofing turns this off together with the engine
	// itself: the probe then dials the profile's own address (e.g. a
	// local tunnel), exactly like its connect would.
	var prepare func() (func(), error)
	if spoofConnectEnabled(profile, settings) {
		prepare = func() (func(), error) {
			if spoofMgr == nil {
				return nil, fmt.Errorf("sni-spoof manager not wired")
			}
			tmp := NewSpoofManager(nil)
			// Share the exe override path: tmp.Start resolves the
			// same binary via the package-level overrides. Start
			// blocks until the engine reports its listener ready, so
			// the private port is guaranteed to accept the transport
			// dial that follows.
			if err := tmp.Start(profile, settings); err != nil {
				return nil, fmt.Errorf("spoof probe engine: %w", err)
			}
			listenPort := tmp.GetListenPort()
			if listenPort <= 0 {
				_ = tmp.Stop()
				return nil, fmt.Errorf("spoof probe engine: not listening after start")
			}
			m.ephProbeAddStop(gen, func() { _ = tmp.Stop() })
			clone := *probeProfile
			clone.SpoofEnginePort = listenPort
			probeProfile = &clone
			return func() { _ = tmp.Stop() }, nil
		}
	}

	// A mihomo profile probes on its own throwaway engine: the profile is
	// rendered as a Clash Meta document, so neither the sing-box nor the
	// Xray probe path can measure it. It is never a spoof profile (that
	// needs CoreSpoof), so no spoof engine is prepared above.
	if profile.Core.IsMihomo() {
		if mihomoMgr == nil {
			return nil, fmt.Errorf("mihomo manager is nil")
		}
		probe := NewMihomoManager(mihomoMgr.logCh)
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
		return &TestResult{OutboundTag: mihomoProxyName, LatencyMs: int32(d.Milliseconds())}, nil
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

	if choice.Engine == "upstream" {
		if upstreamMgr == nil {
			return nil, fmt.Errorf("upstream sing-box manager is nil")
		}
		probe := NewUpstreamSingBoxManager(upstreamMgr.logCh)
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
		return &TestResult{OutboundTag: clashProxyTag, LatencyMs: int32(d.Milliseconds())}, nil
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
// "singbox" | "xray" | "spoof" | "mihomo" | "". Used by the GUI-side
// system-proxy path, which needs the xray label for a spoof connect on
// the Xray transport too (it exposes split HTTP/SOCKS inbounds, not one
// mixed port). mihomo falls in the mixed-port default: one port serves
// HTTP and SOCKS, like sing-box.
func (m *CoreManager) ActiveCore() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch m.effectiveCoreLocked() {
	case CoreXray:
		return "xray"
	case CoreSpoof:
		return "spoof"
	case CoreMihomo:
		return "mihomo"
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
