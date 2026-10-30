package main

import "fmt"

// GUI-owned system proxy (v2rayN pattern).
//
// The GUI process is the single source of truth for the OS proxy,
// independent of which core (sing-box, xray, future cores) is active.
// The core's only job is to expose local inbound proxy ports; it never
// touches the OS proxy. The GUI writes the OS proxy keys itself.
//
// This file is platform-agnostic. The actual OS writes live in
// sysproxy_windows.go (WinINET registry) and sysproxy_other.go
// (not implemented). Adding a new core only requires extending the
// ActiveProxyPorts switch below — nothing else in the system-proxy
// code needs to change.
//
// Design decision: the GUI path is used for ALL cores, including
// sing-box. The sing-box core's EnableSystemProxy RPC still exists
// (systools.go) and does extra work (DNS, WinHTTP), but it is no
// longer on the proxy path: one code path means one cache, one
// revert, testable without launching any core, and correct for
// cores without any RPC.

// ProxyPorts describes the local inbound ports that the active core
// exposes for OS-level proxying.
type ProxyPorts struct {
	HTTP int `json:"http"`
	// SOCKS proxy port, 0 if none.
	SOCKS int `json:"socks"`
	// Mixed means one port serves both HTTP and SOCKS
	// (sing-box mixed inbound). When true, HTTP and
	// SOCKS carry the same port.
	Mixed bool `json:"mixed"`
}

// ActiveProxyPorts returns the ports for the currently running core,
// or the zero value if no core is running. The GUI owns this
// knowledge; it does NOT dial the core or call any RPC.
func (a *App) ActiveProxyPorts() ProxyPorts {
	_, _, running := a.cores.Status()
	if !running {
		return ProxyPorts{}
	}
	mixed := a.store.GetSettings().MixedPort
	// The only per-core switch in the system-proxy code. A new core
	// adds a case here; everything downstream is core-agnostic.
	switch a.cores.ActiveCore() {
	case "xray":
		// Existing xray layout (do not change): split HTTP/SOCKS
		// inbounds on MixedPort and MixedPort+1.
		return ProxyPorts{HTTP: mixed, SOCKS: mixed + 1}
	default:
		// sing-box (and any future mixed-port core): one mixed
		// inbound serves both protocols on the same port.
		return ProxyPorts{HTTP: mixed, SOCKS: mixed, Mixed: true}
	}
}

// proxyServerValue formats the WinINET ProxyServer value: always a
// single "127.0.0.1:<port>". The OS proxy is an HTTP proxy (HTTPS rides
// it via CONNECT), so the HTTP inbound is the one that matters even when
// the core splits HTTP and SOCKS across two ports. The old
// "127.0.0.1:<http>;127.0.0.1:<socks>" pair was parsed by Windows as a
// failover list, so Settings showed the whole string as the Address and
// left the Port box empty.
// Pure formatting (no OS calls) so the ensure-check below and tests
// can use it on any platform.
func proxyServerValue(p ProxyPorts) string {
	port := p.HTTP
	if port <= 0 {
		port = p.SOCKS
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// systemProxyPointsAt reports whether the OS proxy is currently ON
// and pointing at our local ports. A read error returns false + the
// error so the caller falls back to (re-)applying.
func systemProxyPointsAt(p ProxyPorts) (bool, error) {
	read := sysProxyReader
	if read == nil {
		read = osSystemProxy
	}
	on, server, err := read()
	if err != nil {
		return false, err
	}
	if !on {
		return false, nil
	}
	return server == proxyServerValue(p), nil
}

// sysProxyReader overrides the OS proxy read in tests (nil in
// production, which reads the real OS switch). A variable so tests
// can simulate off/foreign/correct states without touching the
// registry.
var sysProxyReader func() (bool, string, error)

// ensureSystemProxy makes the OS proxy point at the given ports on
// every Connect: if it is already ON and already targets our proxy
// it is left alone; if it is OFF or points elsewhere it is
// (re-)applied via SetSystemProxy. Success (either path) marks
// autoProxyOn so Disconnect reverts what this session ensured.
// Zero ports (no core) are a no-op, matching SetSystemProxy.
func (a *App) ensureSystemProxy(p ProxyPorts) error {
	if p.HTTP > 0 || p.SOCKS > 0 {
		// Hooked (test) environments have no OS state to read: always
		// apply through the hook unless a state hook is also provided.
		if a.sysProxyHook == nil || a.sysStateHook != nil {
			read := a.sysStateHook
			if read == nil {
				read = sysProxyReader
				if read == nil {
					read = osSystemProxy
				}
			}
			if on, server, err := read(); err == nil && on && server == proxyServerValue(p) {
				a.setAutoProxy(true)
				a.emitLog("gui", "info", fmt.Sprintf("system proxy already on nekogo proxy: %+v", p))
				return nil
			}
		}
	}
	if err := a.SetSystemProxy(p); err != nil {
		return err
	}
	a.setAutoProxy(true)
	a.emitLog("gui", "info", fmt.Sprintf("auto system proxy enabled: %+v", p))
	return nil
}
func validateProxyPorts(p ProxyPorts) error {
	// Zero means "absent" and is always legal.
	if p.HTTP != 0 {
		if err := validateMixedPort(p.HTTP); err != nil {
			return err
		}
	}
	if p.SOCKS != 0 {
		if err := validateMixedPort(p.SOCKS); err != nil {
			return err
		}
	}
	return nil
}

// SetSystemProxy applies the OS proxy pointing at the given ports,
// remembering the previous OS values for later restore. No-op if
// ports are zero (no core running).
func (a *App) SetSystemProxy(p ProxyPorts) error {
	if a.sysProxyHook != nil {
		return a.sysProxyHook(true)
	}
	if p.HTTP <= 0 && p.SOCKS <= 0 {
		return nil
	}
	if err := validateProxyPorts(p); err != nil {
		return err
	}
	return sysproxyApply(p)
}

// ClearSystemProxy restores the OS proxy to the values captured by
// the last SetSystemProxy call.
func (a *App) ClearSystemProxy() error {
	if a.sysProxyHook != nil {
		return a.sysProxyHook(false)
	}
	return sysproxyRestore()
}
