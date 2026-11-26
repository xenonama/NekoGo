package main

// Aether argument building and profile normalization, ported from
// PattNG's AetherCoreManager.buildArguments and AetherFmt.normalize.
// The core CLI (CluvexStudio/aether) is shared with PattNG, so the
// arguments built here must match its word for word.

import (
	"fmt"
	"strconv"
	"strings"
)

// aetherSettingValue reads a setting value as an option value of the
// core: null when it is blank or starts with '-'. One that read as an
// option of its own would break wherever the app looks options up word
// by word (AetherCoreManager.settingValue).
func aetherSettingValue(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "-") {
		return "", false
	}
	return v, true
}

// aetherMasqueOverHTTP2 reports whether a tunnel over MASQUE runs over
// HTTP/2: chosen so, or carried by Tor or Psiphon around it, which take
// TCP alone (AetherCoreManager.masqueOverHttp2).
func aetherMasqueOverHTTP2(protocol, transport, tor, psiphon string) bool {
	return aetherIsOverMasque(protocol) &&
		(transport == "h2" || tor == "reverse" || psiphon == "reverse")
}

// buildAetherArguments builds the core's command line for [p] with the
// engine listening on [port] — the direct port of PattNG's
// AetherCoreManager.buildArguments with scan=false (NekoGo has no scan
// UI; the core's own --scan arguments come from the settings alone).
func buildAetherArguments(p *Profile, port int) []string {
	cfg := p.Aether
	protocol := aetherProtocolDefault(cfg.Protocol)
	tor := aetherEnumDefault(aetherTors, cfg.Tor, "off")
	psiphon := aetherEnumDefault(aetherPsiphons, cfg.Psiphon, "off")
	// The listener the app dials takes [port]: Psiphon's or Tor's when
	// one runs inside the tunnel, else the tunnel's own. The rest take
	// the ports after it in the order: the tunnel's own, then Tor's,
	// then Psiphon's. Psiphon around the tunnel is the exception:
	// nothing of the app dials its listener and the core takes the
	// port the Psiphon client reports.
	dialsPsiphon := psiphon == "chain"
	dialsTor := tor == "chain" && !dialsPsiphon
	next := port + 1
	// takeNext hands out the cursor's current value and then advances it,
	// exactly like the reference's `next++`. Handing out a pointer to the
	// cursor instead made every listener that took one read the LAST
	// number allocated rather than its own.
	takeNext := func() int {
		v := next
		next++
		return v
	}
	own := port
	if dialsPsiphon || dialsTor {
		own = takeNext()
	}
	var torBind, psiphonBind *int
	switch tor {
	case "chain":
		if dialsTor {
			p := port
			torBind = &p
		} else {
			v := takeNext()
			torBind = &v
		}
	case "reverse":
		v := takeNext()
		torBind = &v
	}
	switch psiphon {
	case "chain":
		if dialsPsiphon {
			p := port
			psiphonBind = &p
		} else {
			v := takeNext()
			psiphonBind = &v
		}
	case "reverse":
		z := 0 // the core takes the port the Psiphon client reports
		psiphonBind = &z
	}

	args := []string{aetherFlagBind, fmt.Sprintf("%s:%d", aetherLoopback, own)}
	if psiphon != "only" && tor != "only" {
		args = append(args, "--protocol", aetherProtocolCore(protocol))
		// goool has meant WireGuard over MASQUE since aether 2.3.0;
		// WARP-in-WARP is the classic gool, asked for by name.
		if protocol == "gool" {
			args = append(args, "--gool-classic")
		}
		args = append(args, "--scan", aetherEnumDefault(aetherScanModes, cfg.ScanMode, "balanced"))
		// Automatic obfuscation is the core's own choice per protocol;
		// MASQUE over HTTP/2 takes none at all (obfuscation shapes the
		// UDP of WireGuard and HTTP/3 alone).
		transport := aetherEnumDefault(aetherTransports, cfg.Transport, "h3")
		if ob := aetherEnumDefault(aetherObfuscations, cfg.Obfuscation, "auto"); ob != "auto" && !aetherMasqueOverHTTP2(protocol, transport, tor, psiphon) {
			args = append(args, "--noize", ob)
		}
		args = append(args, "--ip", aetherEnumDefault(aetherIPVersions, cfg.IPVersion, "v4"))
		if v, ok := aetherSettingValue(cfg.DNS); ok {
			args = append(args, "--dns", v)
		}
		if v, ok := aetherSettingValue(cfg.ExitLoc); ok {
			args = append(args, "--exit-loc", v)
		}
		// aether 2.3.0 removed --masque-sni; no replacement flag or
		// AETHER_MASQUE_SNI env var exists in the current release.
		if aetherIsOverMasque(protocol) && transport == "h2" {
			args = append(args, "--h2")
			// The ClientHello of the MASQUE handshake and of the calls
			// to the WARP API; without the flag both send whole. Said
			// only when asked: the core fragments by default because
			// Iran's firewall resets a whole ClientHello whose SNI ends
			// in cloudflareclient.com, and --no-fragment would turn that
			// defence off on every profile that never touched the setting
			// (PattN AetherArgs.kt:247 has no else branch either).
			if cfg.Fragment {
				args = append(args, "--fragment")
				if r, ok := aetherRangeParse(cfg.FragmentSz, aetherFragmentSizeBounds); ok {
					args = append(args, "--fragment-size", r.String())
				}
				if r, ok := aetherRangeParse(cfg.FragmentDl, aetherFragmentDelayBounds); ok {
					args = append(args, "--fragment-delay", r.String())
				}
			}
		}
		// Encrypted Client Hello hides the server name of the MASQUE
		// handshake, with the key of the ECH domain's HTTPS record,
		// asked of the ECH resolver.
		if aetherIsOverMasque(protocol) && cfg.Ech {
			args = append(args, "--ech", "auto")
			if v, ok := aetherSettingValue(cfg.EchDNS); ok {
				args = append(args, "--ech-dns", v)
			} else {
				args = append(args, "--ech-dns", aetherECHDNS)
			}
			if v, ok := aetherSettingValue(cfg.EchDomain); ok {
				args = append(args, "--ech-domain", v)
			} else {
				args = append(args, "--ech-domain", aetherECHDomain)
			}
		}
		// The ClientHello of the MASQUE handshakes and of the WARP API
		// and ECH key lookups.
		if aetherIsOverMasque(protocol) {
			args = append(args, aetherFingerprintArguments(aetherFingerprintDefault(cfg.Fingerprint))...)
		}
		if protocol == "wg-over-masque" {
			// The outer hop is a MASQUE gateway; the inner one is the
			// WireGuard endpoint dialled inside the tunnel.
			if ep, ok := aetherEndpointParse(cfg.WiwOuter); ok {
				args = append(args, "--peer", aetherEndpointString(ep))
			}
			if ep, ok := aetherEndpointParse(cfg.WiwInner); ok {
				args = append(args, "--gool-peer", aetherEndpointString(ep))
			}
		} else if aetherIsTwoHops(protocol) {
			hop := "--wiw"
			if protocol == "mim" {
				hop = "--mim"
			}
			outer, okOuter := aetherEndpointParse(cfg.WiwOuter)
			inner, okInner := aetherEndpointParse(cfg.WiwInner)
			if okOuter {
				args = append(args, hop+"-outer", aetherEndpointString(outer))
			}
			if okInner {
				args = append(args, hop+"-inner", aetherEndpointString(inner))
			}
			if !okOuter && !okInner {
				args = append(args, hop+"-scan")
			}
		} else if ep, ok := aetherEndpointOf(p.Address, strconv.Itoa(p.Port)); ok {
			args = append(args, "--peer", aetherEndpointString(ep))
		}
		args = append(args, "--quick-reconnect")
	}

	switch tor {
	case "off":
	case "chain":
		args = append(args, "--tor")
	case "reverse":
		args = append(args, "--tor-reverse")
	case "only":
		args = append(args, "--tor-only")
	}
	if torBind != nil {
		args = append(args, aetherFlagTorBind, fmt.Sprintf("%s:%d", aetherLoopback, *torBind))
	}
	if tor != "off" {
		args = append(args, torOptionArgs(cfg.TorBridges, cfg.TorBridgeLines, cfg.TorRelays)...)
	}

	switch psiphon {
	case "off":
	case "chain":
		args = append(args, "--psiphon")
	case "reverse":
		args = append(args, "--psiphon-reverse")
	case "only":
		args = append(args, "--psiphon-only")
	}
	if psiphonBind != nil {
		args = append(args, aetherFlagPsiphonBind, fmt.Sprintf("%s:%d", aetherLoopback, *psiphonBind))
	}
	if psiphon != "off" {
		args = append(args, psiphonOptionArgs(cfg.PsiphonMode, cfg.PsiphonCDN, cfg.PsiphonSNI, cfg.PsiphonSets, cfg.PsiphonRegn)...)
		args = append(args, psiphonBundledArgs(cfg.PsiphonBundledList)...)
	}
	return args
}

// psiphonOptionArgs renders the Psiphon shape flags both Psiphon paths
// pass to aether.exe: an Aether profile's own settings (above) and the
// Chain tab's global options (psiphon-only runs have no profile to
// read). Sharing one renderer is the point — a flag one path emitted
// and the other did not would make the Chain tab change one leg's
// behaviour in silence.
func psiphonOptionArgs(mode, cdnIPs, cdnSNI, cdnSets, region string) []string {
	shape := aetherEnumDefault(aetherPsiphonModes, mode, "auto")
	args := []string{"--psiphon-mode", shape}
	// The CDN lists feed the fronted transports alone; the server
	// names count only beside an IP list of one's own.
	cdn, hasIPs := "", false
	if v, ok := aetherSettingValue(cdnIPs); ok && shape != "direct" {
		cdn, hasIPs = v, true
		args = append(args, "--psiphon-cdn-ips", cdn)
	}
	if hasIPs {
		if v, ok := aetherSettingValue(cdnSNI); ok {
			args = append(args, "--psiphon-cdn-sni", v)
		}
	}
	if shape != "direct" {
		if sets := aetherCdnSetJoin(cdnSets); sets != "" {
			args = append(args, "--psiphon-cdn-sets", sets)
		}
	}
	if v, ok := aetherSettingValue(region); ok {
		args = append(args, "--psiphon-region", v)
	}
	return args
}

// torOptionArgs renders the Tor shape flags both Tor paths pass to
// aether.exe: an Aether profile's own settings (above) and the Chain
// tab's global options (tor-only runs have no profile to read). Sharing
// one renderer is the point — a flag one path emitted and the other did
// not would make the Chain tab change one leg's behaviour in silence.
func torOptionArgs(bridges, bridgeLines, relays string) []string {
	var args []string
	// Told nothing, the core tries Tor plainly and turns to fetched
	// bridges where Tor is blocked.
	shape := aetherEnumDefault(aetherTorBridges, bridges, "auto")
	switch shape {
	case "auto":
	case "first":
		args = append(args, "--tor-bridges")
	case "never":
		args = append(args, "--no-tor-bridges")
	case "own":
		for _, line := range aetherBridgeLines(bridgeLines) {
			if v, ok := aetherSettingValue(line); ok {
				args = append(args, "--tor-bridge", v)
			}
		}
	}
	// Where fetched bridges come from; with the profile's own lines, or
	// none at all, nothing is fetched.
	if shape == "auto" || shape == "first" {
		if r := aetherEnumDefault(aetherTorRelays, relays, "auto"); r != "auto" {
			args = append(args, "--tor-relays", r)
		}
	}
	return args
}

// bridgeLinesIdleNote is what torOptionArgs left out, in the run's own
// words: the Chain tab keeps its bridge lines whatever Bridges says, and
// only own passes them, so a tab moved off own after a paste — or left
// there because the lines were typed before the shape was chosen — runs
// a Tor the user believes they have bridged and waits the whole
// readiness budget on a directory fetch this network truncates. The
// reason is read back out of [argv] rather than out of the settings so
// it cannot disagree with what is actually being run. Empty means the
// run has nothing to explain.
func bridgeLinesIdleNote(argv []string, lines string) string {
	if aetherTorModeOf(argv) == "off" || containsStr(argv, "--tor-bridge") {
		return ""
	}
	n := len(aetherBridgeLines(lines))
	if n == 0 {
		return ""
	}
	why := "auto, which waits for plain Tor to fail and then fetches its own"
	switch {
	case containsStr(argv, "--no-tor-bridges"):
		why = "never, which dials plain relays and takes no bridges at all"
	case containsStr(argv, "--tor-bridges"):
		why = "first, which always starts over bridges the engine fetched"
	}
	take := "lines"
	if n == 1 {
		take = "line"
	}
	return fmt.Sprintf("this run takes none of the %d bridge %s saved: Bridges is %s. "+
		"Set it to own to hand them to the engine.", n, take, why)
}

// normalizeChainTor canonicalizes the Chain tab's Tor options the way
// normalizeChainPsiphon does Psiphon's: aether's enums matched
// case-sensitively, so an unknown spelling falls back to the default
// rather than reaching the core, and the bridge lines keep the shape a
// torrc uses (one per line) with the editor's line endings removed.
// Called from SaveSettings, so a bad value never reaches a chain run.
func normalizeChainTor(cfg *Settings) error {
	cfg.ChainTorBridges = aetherEnumDefault(aetherTorBridges, strings.TrimSpace(cfg.ChainTorBridges), "auto")
	cfg.ChainTorRelays = aetherEnumDefault(aetherTorRelays, strings.TrimSpace(cfg.ChainTorRelays), "auto")
	cfg.ChainTorBridgeLines = strings.TrimSpace(
		strings.ReplaceAll(cfg.ChainTorBridgeLines, "\r\n", "\n"))
	return nil
}

// aetherTorChainProfile returns the profile the engine should run for
// [p]: a copy with tor=chain when Settings.AetherTor arms Tor on top of
// the Aether tunnel. The stored profile is never mutated, and a profile
// that names its own Tor mode (or carries a hand-written command) keeps
// it — the toggle only fills the default. The Chain tab's Tor options
// ride along, exactly as the Psiphon toggle carries its own.
func aetherTorChainProfile(p *Profile, settings Settings) *Profile {
	if p == nil || p.Protocol != "aether" || !settings.AetherTor {
		return p
	}
	if strings.TrimSpace(p.Aether.Command) != "" {
		return p
	}
	if aetherEnumDefault(aetherTors, p.Aether.Tor, "off") != "off" {
		return p
	}
	cp := *p
	cp.Aether.Tor = "chain"
	cp.Aether.TorBridges = settings.ChainTorBridges
	cp.Aether.TorRelays = settings.ChainTorRelays
	cp.Aether.TorBridgeLines = settings.ChainTorBridgeLines
	return &cp
}

// aetherProfileTorChain reports whether the engine for [p] runs Tor on
// top of the tunnel (tor=chain): the app then dials the Tor listener,
// and connect must wait for a real circuit before the transport starts.
func aetherProfileTorChain(p *Profile) bool {
	return p != nil && p.Protocol == "aether" &&
		aetherEnumDefault(aetherTors, p.Aether.Tor, "off") == "chain"
}

// aetherTunnelOnlyProfile returns the profile the engine runs when the
// Tor hop is not its own business: a copy of [p] with tor turned off, so
// the arguments stop carrying --tor and --tor-bind, the tunnel takes
// back the port the app dials, and readiness is the tunnel's own budget
// again instead of a bootstrap's. What the hop needs — the bridge mode
// and the lines — stays on the copy for aetherTorHopSettings to read.
func aetherTunnelOnlyProfile(p *Profile) *Profile {
	if !aetherProfileTorChain(p) {
		return p
	}
	cp := *p
	cp.Aether.Tor = "off"
	return &cp
}

// aetherTorHopSettings returns the settings a standalone daemon carries
// the hop with. The Chain tab's Tor options are what a connect renders
// into the engine's arguments, whether the toggle armed the hop
// (aetherTorChainProfile copied them over) or the profile named its own
// mode and brought its own lines; reading them back out of [hop] hands
// the daemon exactly the bridges the engine run would have dialled
// instead of quietly switching to a different set.
func aetherTorHopSettings(settings Settings, hop *Profile) Settings {
	if hop == nil {
		return settings
	}
	cp := settings
	cp.ChainTorBridges = hop.Aether.TorBridges
	cp.ChainTorBridgeLines = hop.Aether.TorBridgeLines
	return cp
}

// normalizeChainPsiphon canonicalizes the Chain tab's Psiphon options
// the way normalizePsiphon does a profile's: mode to aether's enum,
// lists comma-separated, sets in built-in order, region upper-case —
// and rejects a country aether cannot read (a two-letter code or
// nothing). Called from SaveSettings, so a bad value never reaches a
// chain run.
func normalizeChainPsiphon(cfg *Settings) error {
	cfg.ChainPsiphonMode = aetherEnumDefault(aetherPsiphonModes, strings.TrimSpace(cfg.ChainPsiphonMode), "auto")
	cfg.ChainPsiphonCDN = joinComma(cfg.ChainPsiphonCDN)
	cfg.ChainPsiphonSNI = joinComma(cfg.ChainPsiphonSNI)
	cfg.ChainPsiphonSets = aetherCdnSetJoin(cfg.ChainPsiphonSets)
	cfg.ChainPsiphonRegion = strings.ToUpper(strings.TrimSpace(cfg.ChainPsiphonRegion))
	if cfg.ChainPsiphonRegion != "" && !validCountryCode(cfg.ChainPsiphonRegion) {
		return fmt.Errorf("chainPsiphonRegion must be a two-letter country code (e.g. DE) or empty")
	}
	return nil
}

// aetherPsiphonChainProfile returns the profile the engine should run
// for [p]: a copy with psiphon=chain when Settings.AetherPsiphon arms
// Psiphon on top of the Aether tunnel. The stored profile is never
// mutated, and a profile that names its own Psiphon mode (or carries a
// hand-written command) keeps it — the toggle only fills the default.
// The Chain tab's options ride along: they belong to the chain legs, and
// this leg runs Psiphon only because the chain toggle said so.
func aetherPsiphonChainProfile(p *Profile, settings Settings) *Profile {
	if p == nil || p.Protocol != "aether" || !settings.AetherPsiphon {
		return p
	}
	if strings.TrimSpace(p.Aether.Command) != "" {
		return p
	}
	if aetherEnumDefault(aetherPsiphons, p.Aether.Psiphon, "off") != "off" {
		return p
	}
	cp := *p
	cp.Aether.Psiphon = "chain"
	cp.Aether.PsiphonMode = settings.ChainPsiphonMode
	cp.Aether.PsiphonCDN = settings.ChainPsiphonCDN
	cp.Aether.PsiphonSNI = settings.ChainPsiphonSNI
	cp.Aether.PsiphonSets = settings.ChainPsiphonSets
	cp.Aether.PsiphonRegn = settings.ChainPsiphonRegion
	return &cp
}

// aetherProfilePsiphonChain reports whether the engine for [p] runs
// Psiphon on top of the tunnel (psiphon=chain): the app then dials the
// Psiphon listener, and connect must wait for a real tunnel before the
// transport starts.
func aetherProfilePsiphonChain(p *Profile) bool {
	return p != nil && p.Protocol == "aether" &&
		aetherEnumDefault(aetherPsiphons, p.Aether.Psiphon, "off") == "chain"
}

// aetherCoreOf returns the arguments the engine runs for [p] on [port]:
// the hand-written command when the profile carries one (its own
// listener stands, and when it names none the engine port is added),
// otherwise the settings on [port]. The log level is the engine's to
// add (AetherCore.of).
func aetherCoreOf(p *Profile, port int) (argv []string, listen int, custom bool) {
	if cmd := strings.TrimSpace(p.Aether.Command); cmd != "" {
		if args, _, ok := aetherCoreOfCommand(cmd); ok {
			listener := aetherListenerFlagOf(args)
			if containsStr(args, listener) {
				if l, okBy := aetherPortAfter(args, listener); okBy {
					return args, l, true
				}
			}
			// A command that named no listener gets a dialer on [port].
			fixed := aetherWithListener(args, listener, port)
			return fixed, port, true
		}
	}
	args := buildAetherArguments(p, port)
	// The dialer's listener: Psiphon's when it runs inside the tunnel,
	// Tor's when Tor does, the tunnel's own otherwise.
	listener := aetherListenerFlagOf(args)
	listen = port
	if l, okBy := aetherPortAfter(args, listener); okBy {
		listen = l
	}
	return args, listen, false
}

// --- profile normalization (AetherFmt.normalize) ---
//
// Every check runs in the order PattNG's normalize chain runs and
// returns the same rejection as an error; storage is only cleaned, so
// a value waiting out of use stays as written.

// normalizeAether cleans [p.Aether] in place and rejects what the core
// would not connect with. Called on SaveProfile, mirror of
// AetherFmt.normalize with the taken-port checks left out (NekoGo picks
// its engine port at connect time).
func normalizeAether(p *Profile) error {
	if p.Protocol != "aether" {
		return nil
	}
	cfg := &p.Aether
	if err := normalizeMasqueSNI(cfg); err != nil {
		return err
	}
	if err := normalizeFragment(cfg); err != nil {
		return err
	}
	if err := normalizeAetherEndpoints(p, cfg); err != nil {
		return err
	}
	if err := normalizeAetherDNS(cfg); err != nil {
		return err
	}
	if err := normalizeExitLoc(cfg); err != nil {
		return err
	}
	if err := normalizeECH(cfg); err != nil {
		return err
	}
	if err := normalizePsiphon(cfg); err != nil {
		return err
	}
	return normalizeTor(cfg)
}

// normalizeMasqueSNI: left out when it is the default, kept as written
// whatever the protocol; refused only while a MASQUE tunnel takes it.
func normalizeMasqueSNI(cfg *aetherSettings) error {
	name := strings.TrimSpace(cfg.MasqueSNI)
	inUse := aetherIsOverMasque(aetherProtocolDefault(cfg.Protocol)) &&
		cfg.Psiphon != "only" && cfg.Tor != "only"
	if inUse && name != "" && !aetherIsMasqueSNI(name) {
		return fmt.Errorf("masque sni %q is not a domain name aether takes", name)
	}
	if name == "" || name == aetherMasqueSNI {
		cfg.MasqueSNI = ""
	}
	return nil
}

// normalizeFragment: only over MASQUE over HTTP/2 are the bounds checked.
func normalizeFragment(cfg *aetherSettings) error {
	inUse := aetherIsOverMasque(aetherProtocolDefault(cfg.Protocol)) &&
		aetherEnumDefault(aetherTransports, cfg.Transport, "h3") == "h2" &&
		cfg.Fragment
	sizeIn := strings.TrimSpace(cfg.FragmentSz)
	dlIn := strings.TrimSpace(cfg.FragmentDl)
	size, sizeOK := "", false
	if r, ok := aetherRangeParse(sizeIn, aetherFragmentSizeBounds); ok {
		size, sizeOK = r.String(), true
	}
	delay, delayOK := "", false
	if r, ok := aetherRangeParse(dlIn, aetherFragmentDelayBounds); ok {
		delay, delayOK = r.String(), true
	}
	if inUse {
		if sizeIn != "" && !sizeOK {
			return fmt.Errorf("fragment size %q is not a value aether takes (1-4096)", sizeIn)
		}
		if dlIn != "" && !delayOK {
			return fmt.Errorf("fragment delay %q is not a value aether takes (0-1000)", dlIn)
		}
	}
	cfg.FragmentSz = size
	cfg.FragmentDl = delay
	return nil
}

// normalizeAetherEndpoints: server + port for one-hop profiles, the
// outer/inner pair for two-hop ones (AetherFmt.normalizeEndpoints).
// aetherPortUnused reports whether the profile's own address and port
// reach nothing: a two-hop tunnel names its hops instead, and a
// carrier-only run dials no WARP endpoint at all. The editor hides the
// endpoint row for both, so save must not demand a port it never shows.
func aetherPortUnused(p Profile) bool {
	if p.Protocol != "aether" {
		return false
	}
	return aetherIsTwoHops(aetherEnumDefault(aetherProtocols, p.Aether.Protocol, "wg")) ||
		p.Aether.Psiphon == "only" || p.Aether.Tor == "only"
}

func normalizeAetherEndpoints(p *Profile, cfg *aetherSettings) error {
	protocol := aetherEnumDefault(aetherProtocols, cfg.Protocol, "wg")
	if aetherIsTwoHops(protocol) {
		outerText := strings.TrimSpace(cfg.WiwOuter)
		innerText := strings.TrimSpace(cfg.WiwInner)
		outer, okOuter := aetherEndpointParse(outerText)
		inner, okInner := aetherEndpointParse(innerText)
		if (outerText != "" && !okOuter) || (innerText != "" && !okInner) {
			return fmt.Errorf("endpoint missing or unreadable (host:port)")
		}
		if aetherNeedsDistinctHops(protocol) && okOuter && okInner && outer.host == inner.host {
			return fmt.Errorf("the two hops of a gool/mim tunnel must be different addresses")
		}
		cfg.WiwOuter = ""
		cfg.WiwInner = ""
		if okOuter {
			cfg.WiwOuter = aetherEndpointString(outer)
		}
		if okInner {
			cfg.WiwInner = aetherEndpointString(inner)
		}
		p.Address = ""
		p.Port = 0
		return nil
	}
	if trimmed := strings.TrimSpace(p.Address); trimmed != "" {
		ep, ok := aetherEndpointOf(trimmed, strconv.Itoa(p.Port))
		if !ok {
			return fmt.Errorf("peer endpoint %q is not an address:port", p.Address)
		}
		p.Address = ep.host
		p.Port = ep.port
	}
	cfg.WiwOuter, cfg.WiwInner = "", ""
	return nil
}

// normalizeAetherDNS: resolvers, each an address with or without a
// port, written back comma-separated (AetherFmt.normalizeDns).
func normalizeAetherDNS(cfg *aetherSettings) error {
	resolvers := splitCommaSpacer(cfg.DNS)
	for _, r := range resolvers {
		if _, ok := aetherEndpointParse(r); ok {
			continue
		}
		if _, ok := aetherEndpointOf(r, "53"); ok {
			continue
		}
		return fmt.Errorf("dns resolver %q is not an address aether dials", r)
	}
	cfg.DNS = strings.Join(resolvers, ",")
	return nil
}

// normalizeExitLoc: the exit rule as the core reads it, kept uppercase.
func normalizeExitLoc(cfg *aetherSettings) error {
	var sb strings.Builder
	for _, r := range cfg.ExitLoc {
		if !isSpaceRune(r) {
			sb.WriteRune(r)
		}
	}
	rule := strings.ToUpper(sb.String())
	if rule == "" {
		cfg.ExitLoc = ""
		return nil
	}
	if !aetherExitRule.MatchString(rule) {
		return fmt.Errorf("exit rule %q is not country codes aether reads (e.g. DE or DE,!NL)", rule)
	}
	cfg.ExitLoc = rule
	return nil
}

// normalizeECH: the resolver and the domain are left out when they are
// the defaults so the profile follows them, and refused only while ECH
// is in use over MASQUE with a WARP tunnel.
func normalizeECH(cfg *aetherSettings) error {
	dns := strings.TrimSpace(cfg.EchDNS)
	domain := strings.TrimSpace(cfg.EchDomain)
	inUse := cfg.Ech &&
		aetherIsOverMasque(aetherProtocolDefault(cfg.Protocol)) &&
		cfg.Psiphon != "only" && cfg.Tor != "only"
	if inUse && dns != "" && !aetherIsECHDNS(dns) {
		return fmt.Errorf("ech dns %q is not a resolver aether asks for the ECH key", dns)
	}
	if inUse && domain != "" && !aetherIsECHDomain(domain) {
		return fmt.Errorf("ech domain %q is not a domain aether asks the ECH record of", domain)
	}
	if dns == "" || dns == aetherECHDNS {
		dns = ""
	}
	if domain == "" || domain == aetherECHDomain {
		domain = ""
	}
	cfg.EchDNS, cfg.EchDomain = dns, domain
	return nil
}

// normalizePsiphon: mode cleaned, lists comma-separated, region upper.
func normalizePsiphon(cfg *aetherSettings) error {
	psiphon := aetherEnumDefault(aetherPsiphons, cfg.Psiphon, "off")
	if psiphon == "off" {
		cfg.Psiphon, cfg.PsiphonMode = "", ""
		cfg.PsiphonCDN, cfg.PsiphonSNI, cfg.PsiphonSets, cfg.PsiphonRegn = "", "", "", ""
		cfg.PsiphonBundledList = nil
		return nil
	}
	// Psiphon carries TCP alone and WARP's WireGuard endpoints answer
	// on UDP; the core refuses the pair. WireGuard over MASQUE goes,
	// as its WireGuard rides inside the MASQUE tunnel.
	if psiphon == "reverse" && !aetherIsOverMasque(aetherProtocolDefault(cfg.Protocol)) {
		return fmt.Errorf("psiphon reverse needs a MASQUE protocol: Psiphon carries TCP alone")
	}
	cfg.Psiphon = psiphon
	cfg.PsiphonMode = aetherEnumDefault(aetherPsiphonModes, cfg.PsiphonMode, "auto")
	cfg.PsiphonCDN = joinComma(cfg.PsiphonCDN)
	cfg.PsiphonSNI = joinComma(cfg.PsiphonSNI)
	cfg.PsiphonSets = aetherCdnSetJoin(cfg.PsiphonSets)
	cfg.PsiphonRegn = strings.ToUpper(strings.TrimSpace(cfg.PsiphonRegn))
	// Stored only when it says no; yes is the default.
	if cfg.PsiphonBundledList != nil && *cfg.PsiphonBundledList {
		cfg.PsiphonBundledList = nil
	}
	return nil
}

func joinComma(text string) string {
	parts := splitCommaSpacer(text)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ",")
}

// checkAetherShareable rejects aether profiles a link cannot carry
// round-trip: an endpoint-less one-hop profile. A hand-written command
// is not part of the link shape; the link exports the settings, which
// command carriers keep in sync through the editor anyway.
func checkAetherShareable(p *Profile) error {
	if p == nil {
		return fmt.Errorf("no profile")
	}
	protocol := aetherEnumDefault(aetherProtocols, p.Aether.Protocol, "wg")
	if aetherIsTwoHops(protocol) {
		if _, ok := aetherEndpointParse(p.Aether.WiwOuter); !ok {
			if _, ok := aetherEndpointParse(p.Aether.WiwInner); !ok {
				return fmt.Errorf("aether profile %q names no outer or inner hop", p.Name)
			}
		}
		return nil
	}
	if _, ok := aetherEndpointOf(p.Address, strconv.Itoa(p.Port)); !ok {
		return fmt.Errorf("aether profile %q has no readable endpoint (address:port)", p.Name)
	}
	return nil
}

// normalizeTor: bridges/relays cleaned, bridge lines one per line,
// Tor+Psiphon only nested (AetherFmt.normalizeTor).
func normalizeTor(cfg *aetherSettings) error {
	tor := aetherEnumDefault(aetherTors, cfg.Tor, "off")
	if tor == "off" {
		cfg.Tor, cfg.TorBridges, cfg.TorBridgeLines, cfg.TorRelays = "", "", "", ""
		return nil
	}
	if tor == "reverse" && !aetherIsOverMasque(aetherProtocolDefault(cfg.Protocol)) {
		return fmt.Errorf("tor reverse needs a MASQUE protocol: Tor carries TCP alone")
	}
	psiphon := aetherEnumDefault(aetherPsiphons, cfg.Psiphon, "off")
	// Tor and Psiphon go together only nested: two around it the core
	// refuses, two inside it or one alone leaves the app nothing to
	// dial the other on.
	nested := psiphon == "off" ||
		(tor == "chain" && psiphon == "reverse") ||
		(tor == "reverse" && psiphon == "chain")
	if !nested {
		return fmt.Errorf("tor and psiphon go together only nested (one inside the tunnel, the other around it)")
	}
	bridges := aetherEnumDefault(aetherTorBridges, cfg.TorBridges, "auto")
	lines := aetherBridgeLines(cfg.TorBridgeLines)
	if bridges == "own" && len(lines) == 0 {
		return fmt.Errorf("tor bridges 'own' needs at least one bridge line")
	}
	cfg.Tor = tor
	cfg.TorBridges = bridges
	cfg.TorBridgeLines = ""
	if bridges == "own" {
		cfg.TorBridgeLines = strings.Join(lines, "\n")
	}
	cfg.TorRelays = aetherEnumDefault(aetherTorRelays, cfg.TorRelays, "auto")
	return nil
}

// AetherCommandPreview renders the engine line a profile's settings
// produce, for the Aether tab's command box: the same shaping a connect
// applies, the hand-written command left out (the box compares against
// it), and the port a command that names no listener gets. The listen
// port a connect really uses is picked then, so this one is the
// placeholder the command reader would supply.
func (a *App) AetherCommandPreview(p Profile) string {
	if p.Protocol != "aether" || a.store == nil {
		return ""
	}
	settings := a.store.GetSettings()
	shaped := aetherTorChainProfile(aetherPsiphonChainProfile(&p, settings), settings)
	shaped.Aether.Command = ""
	args := buildAetherArguments(shaped, aetherDefaultCommandPort)
	args = append(args, "--log-level", aetherLogLevel(settings.LogLevel))
	return aetherCommandLine(args)
}

// aetherCommandLine writes argv as one line aetherWords reads back into
// the same words: a bridge line carries spaces, so it goes in quotes,
// and the quote chosen is the one the word does not contain.
func aetherCommandLine(argv []string) string {
	words := make([]string, 0, len(argv))
	for _, w := range argv {
		switch {
		case w == "":
			words = append(words, `""`)
		case !strings.ContainsFunc(w, isSpaceRune) && !strings.ContainsAny(w, `"'`):
			words = append(words, w)
		case !strings.Contains(w, `"`):
			words = append(words, `"`+w+`"`)
		case !strings.Contains(w, `'`):
			words = append(words, `'`+w+`'`)
		default:
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}
