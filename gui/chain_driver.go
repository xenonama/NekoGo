package main

import (
	"fmt"
	"strings"
	"time"
)

// chain_driver.go — which exit chain a connect runs.
//
// A chain is an extra hop in front of the tunnel, published to the app as
// a loopback SOCKS listener. Two drivers exist and both are aether.exe
// runs, because aether carries the clients itself: Psiphon
// (--psiphon-only / --psiphon) and Tor (--tor-only / --tor, arti plus the
// pluggable transports in bin\pt). Each leg of the app arms at most one
// of them:
//
//	the SNI-spoof leg — Settings.SpoofPsiphon / SpoofTor, a chain run of
//	its own whose dials ride the spoofed tunnel;
//	the Aether leg    — Settings.AetherPsiphon / AetherTor, the engine's
//	own chain mode, carried inside the tunnel.
//
// One driver per leg is not a UI preference but a property of the shape:
// the leg publishes one listener, the rendered config routes into one
// outbound, and the manager that owns the child reports one readiness.

// The driver names. They are the log label, the status field and the
// rendered outbound tag, so they read as the thing they are.
const (
	chainDriverPsiphon = "psiphon"
	chainDriverTor     = "tor"
)

// torChainWarmup is how long a Tor chain gets to carry traffic. A cold
// Tor start fetches a directory consensus before its first byte moves,
// and on a network that blocks Tor it then walks a bridge pool whose own
// per-bridge budget is 360s (AETHER_TOR_BRIDGE_SECS) — so Psiphon's 120s
// would kill a bootstrap that was still working and report it as a
// failed connect.
const torChainWarmup = 300 * time.Second

// torColdChainWarmup is what the same hop gets before it has ever
// cached a directory: the first connect of an install fetches the
// consensus and the bridge descriptors over the front it just stood
// up, which the base budget cuts off on the way in.
const torColdChainWarmup = 10 * time.Minute

// spoofChainDriver reports the exit chain the SNI-spoof leg runs, or ""
// when it runs none. Psiphon wins a config that arms both: it is the
// switch that was there first, so a hand-edited file keeps doing what it
// did. resolveChainDrivers clears the loser on save, so this is only
// ever a choice for a config that was never saved through the app.
func spoofChainDriver(settings Settings) string {
	switch {
	case settings.SpoofPsiphon:
		return chainDriverPsiphon
	case settings.SpoofTor:
		return chainDriverTor
	}
	return ""
}

// aetherChainDriver reports the exit chain the Aether leg runs, or ""
// when the profile's tunnel goes out on its own.
func aetherChainDriver(settings Settings) string {
	switch {
	case settings.AetherPsiphon:
		return chainDriverPsiphon
	case settings.AetherTor:
		return chainDriverTor
	}
	return ""
}

// aetherTorHopDecision reports whether the Aether leg's Tor hop runs on
// the standalone tor.exe behind the tunnel instead of on the arti client
// linked into the engine, and says which of the two the connect chose in
// the words the log quotes.
//
// The two are not interchangeable here. arti lives inside the process
// that builds the tunnel, so its directory fetch rides the tunnel's own
// path and stops at 15% on the networks this app ships to; the daemon is
// a second child dialling the tunnel as its SOCKS front, which is the
// shape measured bootstrapping to 100% and carrying traffic. That comes
// at a price the choice has to respect: two children to supervise, a
// published port that belongs to neither of the shapes the profile
// renders, and a hop that cannot move until the tunnel it dials exists.
// So the daemon runs only where the app itself chose the engine's
// arguments — a hand-written command line owns its exits and stays as
// written — and only for the hop the app actually dials: with
// psiphon=chain too, Psiphon's listener is what the transport connects
// to and Tor stays where the profile put it.
func aetherTorHopDecision(hop *Profile, settings Settings, mgr *AetherManager) (bool, string) {
	if !aetherProfileTorChain(hop) || aetherProfilePsiphonChain(hop) {
		return false, ""
	}
	if strings.TrimSpace(hop.Aether.Command) != "" {
		return false, "the Aether tor hop stays as written: the profile brings its own engine " +
			"command line, which names its own exits"
	}
	if mgr == nil {
		return false, "the Aether tor hop runs on aether.exe's own arti client: no tor manager is wired to run a second child"
	}
	exe, whyArti := standaloneTorFor(settings)
	if exe == "" {
		return false, fmt.Sprintf("the Aether tor hop runs on aether.exe's own arti client: %s", whyArti)
	}
	return true, fmt.Sprintf("the Aether tor hop runs on %s behind the tunnel: the engine carries the tunnel alone", torExeName)
}

// resolveChainDrivers enforces one exit chain per leg and returns a note
// for every switch it had to drop, so SaveSettings can say what the
// stored config now does instead of quietly picking one.
func resolveChainDrivers(cfg *Settings) []string {
	var notes []string
	if cfg.SpoofPsiphon && cfg.SpoofTor {
		cfg.SpoofTor = false
		notes = append(notes, "Tor turned off for the SNI spoofing leg: Psiphon was armed too, and a leg runs one exit chain")
	}
	if cfg.AetherPsiphon && cfg.AetherTor {
		cfg.AetherTor = false
		notes = append(notes, "Tor turned off for the Aether leg: Psiphon was armed too, and a leg runs one exit chain")
	}
	return notes
}

// chainWarmup is the readiness budget for one driver's chain run.
func chainWarmup(driver string) time.Duration {
	if driver == chainDriverTor {
		return torChainWarmup
	}
	return psiphonChainWarmup
}

// chainTorOnlyWarmup is the budget one aether --tor-only run is worth.
// The base is what a hop with a cached consensus costs, and this hop
// never has one: newChainTorDir makes a --tor-dir for the connect and
// the run after it removes the previous one, so every bootstrap walks
// the whole directory fetch, not only the first. Measured for that
// fetch on this machine: 5m43s, which is past the base and the reason a
// connect through the arti leg reported a hop that was still working.
func chainTorOnlyWarmup(base time.Duration) time.Duration {
	if base < torColdChainWarmup {
		return torColdChainWarmup
	}
	return base
}

// idleChainNotes names every exit-chain switch this connect leaves
// unused, with the reason a chain is not a global setting but a property
// of a leg: the SNI-spoof one runs only for a profile that goes through
// the spoofing engine, the Aether one only for a profile that IS an
// Aether tunnel naming no mode of its own. Arming Tor and connecting the
// node next in the list used to yield an ordinary connect and no word
// about it, which read as NekoGo refusing to use Tor.
func idleChainNotes(p *Profile, settings Settings) []string {
	var out []string
	if driver := spoofChainDriver(settings); driver != "" && !spoofChain(p, settings) {
		out = append(out, spoofIdleNote(p, settings, driver))
	}
	if driver := aetherChainDriver(settings); driver != "" && !aetherChainRuns(p, settings) {
		out = append(out, aetherIdleNote(p, settings, driver))
	}
	return out
}

// chainDriverLabel writes a driver the way the Chain tab names it.
func chainDriverLabel(driver string) string {
	if driver == chainDriverTor {
		return "Tor"
	}
	return "Psiphon"
}

// spoofIdleNote explains why the SNI-spoof leg's chain does not run.
func spoofIdleNote(p *Profile, settings Settings, driver string) string {
	why := "this connect has no spoofing leg"
	switch {
	case p == nil:
	case !p.Core.IsSpoof():
		why = fmt.Sprintf("%q runs on %s, not the spoofing engine", p.Name, p.Core.String())
	case !spoofEnabled(settings):
		why = fmt.Sprintf("SNI spoofing is switched off in Settings, so %q dials without the engine", p.Name)
	}
	return fmt.Sprintf("the SNI spoof → %s chain is armed, but %s: this connect runs no chain and %s is not in its path",
		chainDriverLabel(driver), why, chainDriverLabel(driver))
}

// aetherChainRuns reports whether the Aether leg's engine really chains,
// the same way Start decides it: the engine runs only for an aether
// profile with a built outbound, and the switch only applies when the
// profile brings neither a command line nor its own mode. A profile that
// names tor=chain itself is not an idle switch — the hop is there.
func aetherChainRuns(p *Profile, settings Settings) bool {
	if !aetherDriverProfile(p) {
		return false
	}
	ep := aetherTorChainProfile(aetherPsiphonChainProfile(p, settings), settings)
	return aetherProfilePsiphonChain(ep) || aetherProfileTorChain(ep)
}

// aetherIdleNote explains why the Aether leg's chain does not run.
func aetherIdleNote(p *Profile, settings Settings, driver string) string {
	label := chainDriverLabel(driver)
	why := "this connect has no Aether tunnel"
	switch {
	case p == nil:
	case !aetherDriverProfile(p):
		if p.Protocol == "aether" {
			why = fmt.Sprintf("%q carries a hand-written raw outbound, so the Aether engine never starts", p.Name)
		} else {
			why = fmt.Sprintf("%q is not an Aether node", p.Name)
		}
	case strings.TrimSpace(p.Aether.Command) != "":
		why = fmt.Sprintf("%q brings its own engine command line, which names its own exits", p.Name)
	default:
		why = fmt.Sprintf("%q names its own chain mode, so the Chain tab's switch is not read", p.Name)
	}
	return fmt.Sprintf("the Aether %s chain is armed, but %s: the engine runs no %s on this connect", label, why, label)
}
