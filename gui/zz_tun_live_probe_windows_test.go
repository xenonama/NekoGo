//go:build windows

package main

// TUN live probe. Not part of the suite: it drives a real engine, so it is
// skipped unless NEKOGO_TUN_PROBE=1. Run it from an ELEVATED shell (TUN
// needs admin) and read the report it prints and leaves in %TEMP%:
//
//	NEKOGO_TUN_PROBE=1 go test -run TestZZTunLiveProbe -v -count=1 ./
//
// Env overrides, so one build can A/B the tun fields without editing code:
//
//	NEKOGO_TUN_STACK=system|gvisor|mixed      (default: the live setting;
//	                                           sing-box accepts no other
//	                                           stack on Windows)
//	NEKOGO_TUN_STRICT=1|0                     (default: the live setting)
//	NEKOGO_TUN_LEVEL=info|debug|trace         (default: info)
//	NEKOGO_TUN_REPORT=<name>                  (report file in %TEMP%)
//
// Leaving NEKOGO_TUN_STACK unset is the run that matters for a user: it
// drives the settings exactly as their config.json holds them, so the
// stack the shipped default resolves to is the one under test.
//
// Stages, in order: elevated, settings, rendered-config (the JSON handed
// to the core), firewall-before, Start attempts, firewall-after,
// tun-interface, proxy-leg (loopback control: proves the outbound chain
// is alive before any tunnel verdict is read), dns-system, dns-8.8.8.8
// (the port-53 hijack), tcp-target, tcp, tls (a real handshake, because
// "read: EOF" on cleartext 443 only shows the peer was reached), udp,
// counters-after, then the engine log lines.
//
// It copies the live config into a scratch dir first, so nothing here can
// write to %APPDATA%\NekoGo.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type probeReport struct {
	sink *os.File
}

func (r *probeReport) say(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Println(line)
	if r.sink != nil {
		r.sink.WriteString(line + "\n")
		r.sink.Sync()
	}
}

func tlsName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLSv1.3"
	case tls.VersionTLS12:
		return "TLSv1.2"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

func probeEnv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func TestZZTunLiveProbe(t *testing.T) {
	if os.Getenv("NEKOGO_TUN_PROBE") != "1" {
		t.Skip("set NEKOGO_TUN_PROBE=1 to drive a real engine")
	}
	out := filepath.Join(os.TempDir(), probeEnv("NEKOGO_TUN_REPORT", "nekogo-tun-probe.log"))
	sink, err := os.Create(out)
	if err != nil {
		t.Fatalf("open report: %v", err)
	}
	defer sink.Close()
	r := &probeReport{sink: sink}

	elev, err := isGuiElevated()
	r.say("stage=elevated value=%v err=%v", elev, err)
	if !elev {
		r.say("NOTE: not elevated - the tun inbound will fail at 'configure tun interface'.")
		r.say("NOTE: rerun from an elevated shell for the routing and DNS stages below.")
	}

	core, err := filepath.Abs(filepath.Join("build", "bin", "nekobox_core.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(core); err != nil {
		t.Skipf("no engine at %s", core)
	}

	// Live config is copied, never opened for write.
	scratch := t.TempDir()
	liveDir, err := defaultConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(liveDir, "config.json"))
	if err != nil {
		t.Fatalf("read live config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(scratch)
	if err != nil {
		t.Fatal(err)
	}
	s := store.GetSettings()
	s.CorePath = core
	s.LogLevel = probeEnv("NEKOGO_TUN_LEVEL", "info")
	if v := probeEnv("NEKOGO_TUN_STACK", ""); v != "" {
		s.TunStack = v
	}
	if v := os.Getenv("NEKOGO_TUN_STRICT"); v == "0" || v == "1" {
		s.TunStrictRoute = v == "1"
	}
	// The engines are found next to the running exe, and under `go test`
	// that exe is a scratch binary. Point at the tree instead.
	binDir := filepath.Dir(core)
	if _, err := os.Stat(filepath.Join(binDir, "aether.exe")); err == nil {
		s.AetherPath = filepath.Join(binDir, "aether.exe")
	}

	var live fileConfig
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	profile := findProfileByID(live.Profiles, s.ActiveProfileID)
	if profile == nil {
		r.say("stage=profile active=%q not found, using a synthetic sing-box node", s.ActiveProfileID)
		profile = &Profile{ID: "probe", Name: "probe", Core: CoreSingBox, Protocol: "shadowsocks",
			Address: "1.2.3.4", Port: 8388, IDOrPassword: "pw"}
	}
	// Names and addresses only: never the credential, never a subscription URL.
	r.say("stage=profile protocol=%s core=%s address=%s port=%d", profile.Protocol,
		profile.Core.String(), profile.Address, profile.Port)
	r.say("stage=settings tunEnabled=%v tunStack=%q tunAutoRoute=%s tunStrictRoute=%v "+
		"systemProxy=%v coreEngine=%q dnsRemote=%q", s.TunEnabled, s.TunStack,
		autoRouteText(s.TunAutoRoute), s.TunStrictRoute, s.SystemProxy, s.CoreEngine,
		schemeHost(s.DnsRemote))
	r.say("stage=settings tunMTU=%d tunAddress=%q tunAddress6=%q tunEnableIPv6=%v tunIcmp=%q",
		s.TunMTU, s.TunAddress, s.TunAddress6, s.TunEnableIPv6, s.TunIcmp)

	cfg, err := BuildSingBoxConfig(profile, s)
	if err != nil {
		r.say("stage=render error=%v", err)
		t.Fatal(err)
	}
	cfgDump := strings.TrimSuffix(out, ".log") + "-config.json"
	if err := os.WriteFile(cfgDump, []byte(cfg), 0o600); err != nil {
		r.say("stage=render config dump: %v", err)
	}
	r.say("stage=render config written to %s (%d bytes)", cfgDump, len(cfg))

	// The "system" stack terminates TCP in the OS: sing-tun rewrites the
	// app's SYN and sends it back to the engine's own listener on the
	// tunnel address, which is an INBOUND connection to the engine process.
	// The library tries to allow that through Windows Firewall and throws
	// away the error, so the rule is the only visible evidence.
	r.say("stage=firewall-before %s", firewallSingTunRules())

	logCh := make(chan LogEntry, 65536)
	app := NewApp(store, logCh)
	// NewApp read the store's settings before the overrides above, so the
	// managers still point at "next to this test binary".
	app.singbox.setCorePath(core)
	stopDrain := make(chan struct{})
	go func() {
		for {
			select {
			case e := <-logCh:
				if e.Source == "gui" {
					continue
				}
				r.say("engine[%s] %s", e.Level, strings.TrimSpace(e.Message))
			case <-stopDrain:
				return
			}
		}
	}()

	r.say("--- connect ---")
	// Registered before Start: a failed Start can still have left the
	// aether/psiphon sidecars up, and the probe must not leak them.
	defer func() {
		r.say("stage=Stop error=%v", app.cores.Stop())
	}()
	// The aether/psiphon leg is slow on this network (observed 30s to
	// 130s), and a Start that times out upstream says nothing about the
	// tun fields under test. Retry before believing it.
	var startErr error
	for attempt := 1; attempt <= 3; attempt++ {
		start := time.Now()
		startErr = app.cores.Start(profile, s)
		r.say("stage=Start attempt=%d elapsed=%v error=%v",
			attempt, time.Since(start).Round(time.Millisecond), startErr)
		if startErr == nil {
			break
		}
		r.say("stage=Stop attempt=%d error=%v", attempt, app.cores.Stop())
	}
	err = startErr
	if err != nil {
		close(stopDrain)
		return
	}
	r.say("stage=firewall-after %s", firewallSingTunRules())

	// The adapter appears a moment after Start returns.
	var tunIface *net.Interface
	for attempt := 0; attempt < 30; attempt++ {
		ifaces, err := net.Interfaces()
		if err != nil {
			r.say("stage=interfaces error=%v", err)
			break
		}
		for i := range ifaces {
			if strings.EqualFold(ifaces[i].Name, tunInterfaceName) {
				tunIface = &ifaces[i]
				break
			}
		}
		if tunIface != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if tunIface == nil {
		r.say("stage=adapter RESULT=no interface named %q after 15s - traffic cannot enter the tunnel",
			tunInterfaceName)
		for _, list := range []string{"Get-NetAdapter | Format-Table Name,InterfaceDescription,Status"} {
			cmd := exec.Command("powershell", "-NoProfile", "-Command", list)
			out, err := cmd.CombinedOutput()
			r.say("adapter-list(%v): %s", err, strings.TrimSpace(string(out)))
		}
	} else {
		r.say("stage=adapter RESULT=%q index=%d mtu=%d flags=%v", tunIface.Name,
			tunIface.Index, tunIface.MTU, tunIface.Flags)
		addrs, err := tunIface.Addrs()
		r.say("stage=adapter addresses err=%v %v", err, addrs)
	}

	// Routes: who owns the default route?
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"Get-NetRoute -DestinationPrefix '0.0.0.0/0' | Select-Object ifIndex,InterfaceAlias,NextHop,RouteMetric | Format-Table -AutoSize | Out-String -Width 200")
	outR, err := cmd.CombinedOutput()
	r.say("stage=routes default-ipv4 (%v):\n%s", err, strings.TrimSpace(string(outR)))

	// Counters before the probes: did anything enter the tunnel yet?
	in0, out0, ok, err := readTunInterfaceCounters(tunInterfaceName)
	r.say("stage=counters-before ok=%v in=%d out=%d err=%v", ok, in0, out0, err)

	// CONTROL: the same proxy leg without the tunnel. If this works, the
	// outbound chain is alive and every failure below is in the TUN path,
	// not in the upstream.
	r.say("stage=proxy-leg %s", proxyLegProbe(s.MixedPort))

	// DNS through whatever the OS now uses.
	r.say("stage=dns-system probe=%v", withTimeout(8*time.Second, func() error {
		_, err := net.LookupHost("www.google.com")
		return err
	}))
	// DNS to an explicit off-LAN resolver: this is the port-53 hijack path.
	cmd = exec.Command("powershell", "-NoProfile", "-Command",
		"Resolve-DnsName -Name www.google.com -Server 8.8.8.8 -DnsOnly -NoHostsFile -QuickTimeout | Format-List | Out-String -Width 200")
	outD, err := cmd.CombinedOutput()
	r.say("stage=dns-8.8.8.8 (%v):\n%s", err, strings.TrimSpace(string(outD)))

	// A real TCP connection, end to end through the tunnel. Resolve first
	// and print the address, so the engine log lines match the dial.
	var target string
	r.say("stage=tcp-target probe=%v", withTimeout(8*time.Second, func() error {
		addrs, err := net.LookupHost("www.google.com")
		if err != nil {
			return err
		}
		if len(addrs) == 0 {
			return fmt.Errorf("no addresses")
		}
		target = addrs[0]
		return nil
	}))
	r.say("stage=tcp-target address=%s", target)
	if target != "" {
		r.say("stage=tcp probe=%v", withTimeout(15*time.Second, func() error {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(target, "443"), 15*time.Second)
			if err != nil {
				return err
			}
			n, err := c.Write([]byte("HEAD / HTTP/1.0\r\n\r\n"))
			if err != nil {
				c.Close()
				return fmt.Errorf("write %d bytes: %w", n, err)
			}
			buf := make([]byte, 64)
			c.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, err = c.Read(buf)
			c.Close()
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}
			r.say("stage=tcp first-bytes=%q", string(buf[:n]))
			return nil
		}))
		// A real TLS handshake, which is what a browser does. A TCP
		// connect alone can end in "read: EOF" because the peer closed a
		// cleartext request on 443; the handshake proves the tunnel
		// carries a completed TLS session, not just a SYN/ACK.
		r.say("stage=tls probe=%v", withTimeout(20*time.Second, func() error {
			conn, err := tls.Dial("tcp", net.JoinHostPort(target, "443"), &tls.Config{
				ServerName: "www.google.com",
			})
			if err != nil {
				return err
			}
			defer conn.Close()
			state := conn.ConnectionState()
			r.say("stage=tls negotiated=%s resumed=%v certs=%d",
				tlsName(state.Version), state.DidResume, len(state.PeerCertificates))
			if _, err := conn.Write([]byte("HEAD / HTTP/1.1\r\nHost: www.google.com\r\nConnection: close\r\n\r\n")); err != nil {
				return fmt.Errorf("tls write: %w", err)
			}
			buf := make([]byte, 128)
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, err := conn.Read(buf)
			if err != nil && n == 0 {
				return fmt.Errorf("tls read: %w", err)
			}
			r.say("stage=tls first-bytes=%q", string(buf[:n]))
			return nil
		}))
	}

	// UDP through the tunnel that is not the DNS path. The system stack
	// hands UDP straight to the handler while TCP round-trips through the
	// OS, so the pair separates the two.
	r.say("stage=udp probe=%v", withTimeout(8*time.Second, func() error {
		c, err := net.DialTimeout("udp", "1.1.1.1:443", 5*time.Second)
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.Write([]byte{0x00, 0x01})
		return err
	}))

	in1, out1, ok, err := readTunInterfaceCounters(tunInterfaceName)
	r.say("stage=counters-after ok=%v in=%d out=%d err=%v delta in=%d out=%d",
		ok, in1, out1, err, in1-in0, out1-out0)
	if tunIface != nil && in1 == in0 && out1 == out0 {
		r.say("VERDICT: the tunnel exists but nothing entered it - the default route is not")
		r.say("VERDICT: pointing into the adapter, or the OS is not sending traffic there.")
	} else if in1 != in0 || out1 != out0 {
		r.say("VERDICT: traffic DID enter the tunnel, so routing works; a failure above is")
		r.say("VERDICT: in the tunnel stack, the DNS leg or the proxy leg, not the adapter.")
	}
	close(stopDrain)
	r.say("report: %s", out)
}

// proxyLegProbe talks to the engine's own mixed inbound over loopback, so it
// exercises the profile's outbound with the tun device out of the picture.
func proxyLegProbe(port int) string {
	if port == 0 {
		return "no mixed port"
	}
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "connect " + addr + ": " + err.Error()
	}
	defer c.Close()
	if _, err := c.Write([]byte("CONNECT www.google.com:443 HTTP/1.1\r\nHost: www.google.com\r\n\r\n")); err != nil {
		return "connect request: " + err.Error()
	}
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	buf := make([]byte, 128)
	n, err := c.Read(buf)
	if err != nil {
		return "reply: " + err.Error()
	}
	reply := string(buf[:n])
	if !strings.Contains(reply, "200") {
		return "reply=" + firstLine(reply)
	}
	if _, err := c.Write([]byte("HEAD / HTTP/1.0\r\n\r\n")); err != nil {
		return "tunnel write: " + err.Error()
	}
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	n, err = c.Read(buf)
	if err != nil {
		return "CONNECT ok, then read: " + err.Error()
	}
	return "ok, first-bytes=" + fmt.Sprintf("%q", string(buf[:n]))
}

// firewallSingTunRules reports whether the inbound-allow rule the "system"
// stack needs is present. Readable only when elevated, and that is the
// point: sing-tun discards the failure of the call that adds it.
func firewallSingTunRules() string {
	cmd := exec.Command("cmd", "/c",
		`netsh advfirewall firewall show rule name=all | findstr /i /c:"sing-tun"`)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if text == "" {
		if err != nil && !strings.Contains(err.Error(), "exit status 1") {
			return fmt.Sprintf("query failed: %v", err)
		}
		return "no sing-tun inbound-allow rule present"
	}
	return fmt.Sprintf("%d line(s):\n%s", len(strings.Split(text, "\n")), text)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\r'); i >= 0 {
		return s[:i]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func autoRouteText(p *bool) string {
	if p == nil {
		return "unset(=true)"
	}
	return fmt.Sprint(*p)
}

// schemeHost reports only the scheme and host of a DNS URL: a query string
// can carry a token, and a path can carry a key.
func schemeHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<unparsed>"
	}
	return u.Scheme + "://" + u.Host
}

func findProfileByID(ps []Profile, id string) *Profile {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

func withTimeout(d time.Duration, fn func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return fmt.Errorf("timed out after %v", d)
	}
}
