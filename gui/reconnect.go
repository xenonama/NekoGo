package main

// The Reconnect modes.
//
// Reconnect is one shape for the user — take the tunnel down, raise it
// again on the same profile — but what happens to the exit chain in
// between is a choice, and it costs minutes either way. "restart" is
// what the button has always done: every leg cold-starts, so a Psiphon
// handshakes from its home-server list again and Tor refetches a
// consensus before the first byte. "sticky" hands the SNI-spoof chain's
// aether child to the next connect on the same ports it was already
// listening and dialing on, which is the expensive part; the spoof
// engine and the transport underneath it are still re-made, which is
// usually the thing that broke. "off" hides the button entirely.
//
// The Aether leg is not part of the offer: its chain runs inside the
// aether.exe engine process, so restarting the engine necessarily
// restarts the chain with it.

import "strings"

const (
	reconnectOff     = "off"
	reconnectRestart = "restart"
	reconnectSticky  = "sticky"
)

// normalizeReconnectMode canonicalises the stored mode. A config that
// never carried the key — and anything written by hand that does not say
// one of the three — means "restart", the behaviour the button shipped
// with, so no switch statement downstream has to guess.
func normalizeReconnectMode(s *Settings) error {
	switch strings.ToLower(strings.TrimSpace(s.ReconnectMode)) {
	case reconnectOff:
		s.ReconnectMode = reconnectOff
	case reconnectSticky:
		s.ReconnectMode = reconnectSticky
	default:
		s.ReconnectMode = reconnectRestart
	}
	return nil
}

// reconnectKeepsChain reports whether a reconnect of this mode may hand
// its exit chain to the connect that follows. Only "sticky" does: the
// other two must never leave a chain child standing after a teardown.
func reconnectKeepsChain(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), reconnectSticky)
}

// StopWithChainKeep is the teardown a sticky reconnect asks for: the
// SNI-spoof chain's exit leg survives it, still listening on the port
// its config names and still dialing the upstream inbound it was given,
// and the next spoof-chain connect adopts it (see chainHandoff).
//
// The keep is exactly one Stop wide. Stop reads and clears the flag in
// the same critical section that decides what to reap, so a plain
// Disconnect that follows — or any Stop that was not asked to keep —
// cannot inherit it and orphan a Psiphon or Tor child nobody owns.
func (m *CoreManager) StopWithChainKeep() error {
	m.mu.Lock()
	// Only a committed spoof chain has a leg the next connect could use;
	// arming for any other group would keep a child nothing adopts.
	m.chainKeep = m.chainActive && m.activeCore == CoreSpoof
	m.mu.Unlock()
	return m.Stop()
}

// chainHandoff offers the leg a previous connect left standing, for a
// connect that wants the same driver. The ports come back exactly as the
// child was given them: the chain's SOCKS listener is what the new
// transport's outbound names, and the loopback inbound its own dials
// return through is what the child dials, so reusing them is what keeps
// the handoff from turning into a cold start with new numbers.
//
// ok is false unless the bookkeeping and a live child agree, and the
// driver matches. A dead child (it crashed, or the UI has not noticed
// yet) is not offered, and the other driver's leg is never handed across.
func (m *CoreManager) chainHandoff(driver string) (chainPort, upstreamPort int, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.chainActive || m.chainKind != driver {
		return 0, 0, false
	}
	mgr := m.chainMgrLocked(driver)
	if mgr == nil || !mgr.Running() || m.chainPort < 1 || m.chainUpstreamPort < 1 {
		return 0, 0, false
	}
	return m.chainPort, m.chainUpstreamPort, true
}

// retireChainLeg takes the handed-over leg down when the connect that
// could have used it does not: a different driver, another core, or a
// failure before the new group commits. Whatever it is serving on now is
// a port the incoming config never names, so the leg goes and the
// bookkeeping goes with it. Idle managers and an empty group are no-ops.
func (m *CoreManager) retireChainLeg() {
	m.mu.Lock()
	m.chainActive = false
	m.chainKind = ""
	m.chainPort = 0
	m.chainUpstreamPort = 0
	m.chainKeep = false
	psiphonMgr, torMgr := m.psiphonMgr, m.torMgr
	m.mu.Unlock()
	for _, chainMgr := range []*AetherManager{psiphonMgr, torMgr} {
		if chainMgr == nil {
			continue
		}
		_ = chainMgr.Stop()
	}
	// The published chain ports belong to the group that is being torn
	// down, not to the leg.
	clearSpoofChainPorts()
}
