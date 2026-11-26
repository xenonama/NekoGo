package main

// The ports one connect needs, checked before it starts anything.
//
// Three gaps, one symptom each:
//
//   - Only the two sing-box paths pre-flight, and only their own
//     inbounds. An Aether connect starts its engine first, so a held
//     mixed port was reported after the engine had been spawned, probed
//     and torn down — and the engine's own lookup error could arrive
//     instead of the port the user has to act on.
//   - Xray and mihomo refuse a busy port with no grace window, so a port
//     the previous connect's core is still releasing fails the next
//     Connect instantly. That is the "I clicked Connect many times and
//     it finally worked" report, fixed for sing-box only.
//   - A port picked for one leg can be handed to another: the chain's
//     listener and upstream inbound are published before the transport
//     starts, and the transport picks its RPC port with a bare freePort.
//     The core then binds a chain port and the chain leg fails after its
//     whole two- or five-minute readiness budget.

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withFreePortHook aims the kernel's port assignment at a fixed number,
// so a picker can be handed a port it must refuse.
func withFreePortHook(t *testing.T, port int) {
	t.Helper()
	prev := freePortHook
	freePortHook = func() (int, error) { return port, nil }
	t.Cleanup(func() { freePortHook = prev })
}

// publishedPortsCleanup unpublishes every leg port the test published.
func publishedPortsCleanup(t *testing.T) {
	t.Helper()
	prevFront := spoofFrontPort.Load()
	prevChain, prevUp := spoofChainPort.Load(), spoofChainUpstreamPort.Load()
	prevKind := spoofChainKind.Load()
	prevAether := aetherActivePort.Load()
	t.Cleanup(func() {
		spoofFrontPort.Store(prevFront)
		spoofChainPort.Store(prevChain)
		spoofChainUpstreamPort.Store(prevUp)
		spoofChainKind.Store(prevKind)
		aetherActivePort.Store(prevAether)
	})
}

// TestConnectRefusesAHeldPortBeforeAnyEngineStarts: the Aether path runs
// its engine before the main core, so the port the main core needs has to
// be checked before the engine exists. The deliberately missing engine
// binary proves the refusal came first: its own lookup error is what a
// connect that checks too late reports.
func TestConnectRefusesAHeldPortBeforeAnyEngineStarts(t *testing.T) {
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port
	s := portHeldSettings(t, port)
	prevExe := aetherExeOverride
	aetherExeOverride = "/nonexistent-aether-xyz"
	t.Cleanup(func() { aetherExeOverride = prevExe })

	p := &Profile{ID: "a", Name: "aether-held-port", Core: CoreSingBox, Protocol: "aether",
		Address: "203.0.113.7", Port: 443, Aether: aetherSettings{Protocol: "masque"}}
	m := NewCoreManager(
		NewSingBoxManager("/nonexistent-nekobox-core-xyz", make(chan LogEntry, 64)),
		NewXrayManager("/nonexistent-xray-xyz", "", make(chan LogEntry, 64)))
	m.SetAetherManager(NewAetherManager(make(chan LogEntry, 64)))

	err := m.Start(p, s)
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start accepted a connect whose inbound port is held")
	}
	msg := err.Error()
	if !strings.Contains(msg, "in use") || !strings.Contains(msg, strconv.Itoa(port)) {
		t.Errorf("Start error = %q, want it to name port %d as in use", msg, port)
	}
	if strings.Contains(msg, "nonexistent-aether-xyz") {
		t.Errorf("Start went looking for the engine before checking the port: %q", msg)
	}
	if _, _, running := m.Status(); running {
		t.Error("the manager reports a running group after a refused connect")
	}
}

// TestConnectPlanNamesThePortsTheConnectBinds: the plan is what the
// pre-flight checks and what the pickers must avoid, so it has to name
// every port fixed ahead of time — the transport's inbounds, xray's API
// listener, and the exits a hand-written engine command line names.
func TestConnectPlanNamesThePortsTheConnectBinds(t *testing.T) {
	mixed, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	hasPort := func(ports []int, want int) bool {
		for _, p := range ports {
			if p == want {
				return true
			}
		}
		return false
	}

	t.Run("sing-box inbound", func(t *testing.T) {
		_, ports := planConnectPorts(portTestProfile(), portHeldSettings(t, mixed))
		if !hasPort(ports, mixed) {
			t.Errorf("plan ports = %v, want the mixed inbound %d", ports, mixed)
		}
	})

	t.Run("xray mixed, socks and API", func(t *testing.T) {
		api, err := freePort()
		if err != nil {
			t.Skipf("no free loopback port: %v", err)
		}
		s := portHeldSettings(t, mixed)
		s.XrayAPIAddress = net.JoinHostPort("127.0.0.1", strconv.Itoa(api))
		p := portTestProfile()
		p.Core = CoreXray
		_, ports := planConnectPorts(p, s)
		for _, want := range []int{mixed, mixed + 1, api} {
			if !hasPort(ports, want) {
				t.Errorf("xray plan ports = %v, want %d among them", ports, want)
			}
		}
	})

	t.Run("mihomo mixed", func(t *testing.T) {
		p := portTestProfile()
		p.Core = CoreMihomo
		_, ports := planConnectPorts(p, portHeldSettings(t, mixed))
		if !hasPort(ports, mixed) {
			t.Errorf("mihomo plan ports = %v, want the mixed inbound %d", ports, mixed)
		}
	})

	t.Run("hand-written engine command exits", func(t *testing.T) {
		tunnel, err := freePort()
		if err != nil {
			t.Skipf("no free loopback port: %v", err)
		}
		hop, err := freePort()
		if err != nil {
			t.Skipf("no free loopback port: %v", err)
		}
		p := &Profile{ID: "c", Name: "own-command", Core: CoreSingBox, Protocol: "aether",
			Address: "203.0.113.7", Port: 443,
			Aether: aetherSettings{Command: "--bind 127.0.0.1:" + strconv.Itoa(tunnel) +
				" --tor --tor-bind 127.0.0.1:" + strconv.Itoa(hop)}}
		_, ports := planConnectPorts(p, portHeldSettings(t, mixed))
		for _, want := range []int{mixed, tunnel, hop} {
			if !hasPort(ports, want) {
				t.Errorf("command plan ports = %v, want %d among them", ports, want)
			}
		}
	})

	t.Run("a rendered engine port is picked, not planned", func(t *testing.T) {
		p := &Profile{ID: "r", Name: "rendered", Core: CoreSingBox, Protocol: "aether",
			Address: "203.0.113.7", Port: 443, Aether: aetherSettings{Protocol: "masque"}}
		_, ports := planConnectPorts(p, portHeldSettings(t, mixed))
		if !hasPort(ports, mixed) {
			t.Errorf("plan ports = %v, want the main core's inbound %d", ports, mixed)
		}
		if hasPort(ports, aetherDefaultCommandPort) {
			t.Errorf("plan ports = %v, want no listener the app picks at connect time", ports)
		}
	})
}

// TestConnectPlanSparesAKeptChainLegsPorts: a sticky reconnect adopts a
// chain child that is already listening, and the rendered config names
// its two ports. Checking them as "must be free" would refuse every
// reconnect that keeps its leg — the ports are ours, not a collision.
func TestConnectPlanSparesAKeptChainLegsPorts(t *testing.T) {
	publishedPortsCleanup(t)
	chain := holdFreePort(t)
	upstream := holdFreePort(t)
	chainPort := chain.Addr().(*net.TCPAddr).Port
	upPort := upstream.Addr().(*net.TCPAddr).Port
	setSpoofChainPorts(chainDriverTor, chainPort, upPort)

	mixed, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	s := portHeldSettings(t, mixed)
	s.SpoofTor = true
	p := &Profile{ID: "s", Name: "spoof-keep", Core: CoreSpoof,
		Protocol: "trojan", Address: "203.0.113.9", Port: 443, IDOrPassword: "pw"}

	addrs, ports := planConnectPorts(p, s)
	if len(addrs) == 0 {
		t.Fatal("the plan named no address at all for a spoof chain connect")
	}
	for _, held := range []int{chainPort, upPort} {
		for _, got := range ports {
			if got == held {
				t.Errorf("plan ports = %v, want the kept leg's own %d left out", ports, held)
			}
		}
		for _, a := range addrs {
			if a == net.JoinHostPort("127.0.0.1", strconv.Itoa(held)) {
				t.Errorf("plan addrs = %v, want the kept leg's own %d left out", addrs, held)
			}
		}
	}
	found := false
	for _, got := range ports {
		if got == mixed {
			found = true
		}
	}
	if !found {
		t.Errorf("plan ports = %v, want the mixed inbound %d still checked", ports, mixed)
	}
}

// TestXrayStartWaitsForAPortFreedAtOnce: the port the previous connect's
// xray is still releasing must be waited out inside this connect, exactly
// as the sing-box paths do. The missing binary proves the pre-flight let
// the connect continue instead of refusing it.
func TestXrayStartWaitsForAPortFreedAtOnce(t *testing.T) {
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port
	go func() {
		time.Sleep(min(200*time.Millisecond, inboundPortGrace/2))
		_ = held.Close()
	}()
	m := NewXrayManager("/nonexistent-xray-xyz", "", make(chan LogEntry, 64))
	err := m.Start(portTestProfile(), portHeldSettings(t, port))
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded with no xray binary")
	}
	msg := err.Error()
	if strings.Contains(msg, "in use") {
		t.Errorf("xray refused a port freed during the window: %q", msg)
	}
	if !strings.Contains(msg, "nonexistent-xray-xyz") {
		t.Errorf("xray Start error = %q, want the connect to reach the binary lookup", msg)
	}
}

// TestMihomoStartWaitsForAPortFreedAtOnce: same window, same reason — a
// mihomo connect must not hand the user a click to repeat.
func TestMihomoStartWaitsForAPortFreedAtOnce(t *testing.T) {
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port
	go func() {
		time.Sleep(min(200*time.Millisecond, inboundPortGrace/2))
		_ = held.Close()
	}()
	s := portHeldSettings(t, port)
	s.MihomoPath = "/nonexistent-mihomo-xyz"
	m := NewMihomoManager(make(chan LogEntry, 64))
	p := portTestProfile()
	p.Core = CoreMihomo
	err := m.Start(p, s)
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded with no mihomo binary")
	}
	msg := err.Error()
	if strings.Contains(msg, "in use") {
		t.Errorf("mihomo refused a port freed during the window: %q", msg)
	}
	if !strings.Contains(msg, "nonexistent-mihomo-xyz") {
		t.Errorf("mihomo Start error = %q, want the connect to reach the binary lookup", msg)
	}
}

// TestFreePortNeverHandsBackAPortTheConnectNeeds: a picker that returns a
// port another leg of the same connect is about to bind moves the failure
// to the end of that leg's readiness budget. Every number already spoken
// for — claimed by the connect in flight, or published by a leg that is
// running or about to run — is off limits.
func TestFreePortNeverHandsBackAPortTheConnectNeeds(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	other, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}

	t.Run("claimed by the connect in flight", func(t *testing.T) {
		release := claimConnectPorts([]int{port})
		t.Cleanup(release)
		withFreePortHook(t, port)
		got, err := freePort()
		if err == nil && got == port {
			t.Errorf("freePort handed back %d, which this connect already needs", port)
		}
	})

	t.Run("published chain listener", func(t *testing.T) {
		publishedPortsCleanup(t)
		setSpoofChainPorts(chainDriverTor, port, other)
		withFreePortHook(t, port)
		got, err := freePort()
		if err == nil && got == port {
			t.Errorf("freePort handed back the chain listener %d", port)
		}
	})

	t.Run("published chain upstream inbound", func(t *testing.T) {
		publishedPortsCleanup(t)
		setSpoofChainPorts(chainDriverPsiphon, other, port)
		withFreePortHook(t, port)
		got, err := freePort()
		if err == nil && got == port {
			t.Errorf("freePort handed back the chain upstream inbound %d", port)
		}
	})

	t.Run("published engine dial port", func(t *testing.T) {
		publishedPortsCleanup(t)
		setAetherActivePort(port)
		withFreePortHook(t, port)
		got, err := freePort()
		if err == nil && got == port {
			t.Errorf("freePort handed back the engine's dial port %d", port)
		}
	})

	t.Run("published spoof front port", func(t *testing.T) {
		publishedPortsCleanup(t)
		setSpoofFrontPort(port)
		withFreePortHook(t, port)
		got, err := freePort()
		if err == nil && got == port {
			t.Errorf("freePort handed back the spoof front port %d", port)
		}
	})

	t.Run("an unclaimed port is still handed out", func(t *testing.T) {
		withFreePortHook(t, port)
		got, err := freePort()
		if err != nil || got != port {
			t.Errorf("freePort = %d/%v, want the free port %d", got, err, port)
		}
	})

	t.Run("the TCP-and-UDP picker inherits the rule", func(t *testing.T) {
		release := claimConnectPorts([]int{port})
		t.Cleanup(release)
		withFreePortHook(t, port)
		got, err := freePortTCPAndUDP()
		if err == nil && got == port {
			t.Errorf("freePortTCPAndUDP handed back %d, which this connect already needs", port)
		}
	})
}

// TestAPickedChainPortStaysClaimedUntilItsLegBindsIt: the chain's two
// ports are published for the render and cleared once the transport has
// accepted the config — but the arti Tor leg opens its listener only at
// the end of a bootstrap measured in minutes. In that window the number
// is neither bound nor published, so a latency probe picking a free port
// can take it, and the leg then fails at the end of its whole budget.
func TestAPickedChainPortStaysClaimedUntilItsLegBindsIt(t *testing.T) {
	publishedPortsCleanup(t)
	chainPort, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	upPort, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	release := claimConnectPorts(nil)
	defer release()

	setSpoofChainPorts(chainDriverTor, chainPort, upPort)
	claimPickedConnectPorts(chainPort, upPort)
	// What startSpoofLocked does the moment the transport accepts the
	// config: ephemeral cores must not render the chain any more.
	clearSpoofChainPorts()

	for _, want := range []int{chainPort, upPort} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			withFreePortHook(t, want)
			got, err := freePort()
			if err == nil && got == want {
				t.Errorf("freePort handed back %d while a chain leg still has to bind it", want)
			}
		})
	}
}

// TestPickedPortClaimNeedsAConnectToBelongTo: the additive claim belongs
// to the connect in flight. With none running it must reserve nothing,
// or a port would stay off limits for the rest of the process's life.
func TestPickedPortClaimNeedsAConnectToBelongTo(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Skipf("no free loopback port: %v", err)
	}
	claimPickedConnectPorts(port)
	withFreePortHook(t, port)
	got, err := freePort()
	if err != nil || got != port {
		t.Errorf("freePort = %d/%v, want %d with no connect in flight", got, err, port)
	}
}

// TestChainLegsClaimThePortsTheyPick: the two places that number a leg
// while the connect runs have to claim what they picked, since the
// published ports are cleared before the child binds them.
func TestChainLegsClaimThePortsTheyPick(t *testing.T) {
	src := readGuiFile(t, "core_manager.go")
	for _, sig := range []string{
		"func (m *CoreManager) startSpoofLocked(",
		"func (m *CoreManager) startAetherTorHop(",
	} {
		body := goFuncBody(t, src, sig)
		if !strings.Contains(body, "claimPickedConnectPorts(") {
			t.Errorf("%s picks a port without claiming it for the connect", sig)
		}
	}
}

// TestEngineRunRefusesAHeldListenerBeforeTheReadinessBudget: an engine
// whose listener is taken cannot become ready, and waiting out its budget
// says so minutes later in words that name a timeout instead of the port.
// The check belongs after the previous child is reaped (its socket is the
// one most likely to be held) and before the new one is spawned.
func TestEngineRunRefusesAHeldListenerBeforeTheReadinessBudget(t *testing.T) {
	bin, _ := lingerHelperBinary(t)
	withAetherLingerExe(t, bin)
	held := holdFreePort(t)
	port := held.Addr().(*net.TCPAddr).Port

	prevGrace := inboundPortGrace
	inboundPortGrace = 300 * time.Millisecond
	t.Cleanup(func() { inboundPortGrace = prevGrace })

	const warmup = 4 * time.Second
	m := NewAetherManager(make(chan LogEntry, 64))
	start := time.Now()
	_, _, err := m.runProcess(bin, nil, port, warmup, "", "", nil)
	elapsed := time.Since(start)
	if err == nil {
		_ = m.Stop()
		t.Fatal("runProcess reported a ready engine on a port another process holds")
	}
	msg := err.Error()
	if !strings.Contains(msg, "in use") || !strings.Contains(msg, strconv.Itoa(port)) {
		t.Errorf("runProcess error = %q, want it to name port %d as in use", msg, port)
	}
	// The app numbers this listener, so sending the user to Settings to
	// move a port they cannot see there is advice that cannot be taken.
	if strings.Contains(msg, "Mixed port") {
		t.Errorf("runProcess error = %q, want no remedy naming a setting that does not move this port", msg)
	}
	if elapsed >= warmup {
		t.Errorf("runProcess spent %v of its %v budget on a port it could never bind", elapsed, warmup)
	}
	if m.Running() {
		t.Error("the manager reports a running engine after a refused spawn")
	}
}

// heldUDPBand occupies [n] consecutive UDP ports and returns the first.
// Windows reserves bands exactly like this inside the ephemeral range
// (WinNAT/Hyper-V): the TCP side binds, the UDP side is refused.
func heldUDPBand(t *testing.T, n int) int {
	t.Helper()
	for attempt := 0; attempt < 16; attempt++ {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("udp :0: %v", err)
		}
		base := pc.LocalAddr().(*net.UDPAddr).Port
		_ = pc.Close()
		if base+n > 65535 {
			continue
		}
		var held []net.PacketConn
		for i := 0; i < n; i++ {
			c, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+i)))
			if err != nil {
				break
			}
			held = append(held, c)
		}
		if len(held) == n {
			t.Cleanup(func() {
				for _, c := range held {
					_ = c.Close()
				}
			})
			return base
		}
		for _, c := range held {
			_ = c.Close()
		}
	}
	t.Skipf("no run of %d consecutive UDP ports free to hold", n)
	return 0
}

// TestFreePortTCPAndUDPEscapesAHeldBand: asking the kernel for another
// port is not a retry. It answers a :0 bind with the next number in
// sequence, so a picker whose UDP side is refused walks the same
// contiguous band every time and reports "no loopback port free" with
// most of the range empty. Measured here: five attempts at 64844, 64850,
// 64855, 64869, 64878 — all inside the reserved band 64826-65025 — and
// the Aether connect, the Tor hop and mihomo all pick through this one
// function.
func TestFreePortTCPAndUDPEscapesAHeldBand(t *testing.T) {
	const band = 5
	base := heldUDPBand(t, band)
	prev := freePortHook
	n := 0
	freePortHook = func() (int, error) {
		port := base + n%band
		n++
		return port, nil
	}
	t.Cleanup(func() { freePortHook = prev })

	port, err := freePortTCPAndUDP()
	if err != nil {
		t.Fatalf("the picker never left the held band %d..%d: %v", base, base+band-1, err)
	}
	if port >= base && port < base+band {
		t.Fatalf("handed back %d, inside the held band %d..%d", port, base, base+band-1)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("TCP %s not free: %v", addr, err)
	}
	_ = ln.Close()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("UDP %s not free: %v", addr, err)
	}
	_ = pc.Close()
}
