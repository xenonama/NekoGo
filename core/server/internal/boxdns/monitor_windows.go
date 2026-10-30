//go:build windows
package boxdns

import (
	"fmt"
	"sync"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	logger2 "github.com/sagernet/sing/common/logger"
)

func init() {
	logger := logger2.NOP()
	updMonitor, err := tun.NewNetworkUpdateMonitor(logger)
	if err != nil {
		fmt.Println("Could not create NetworkUpdateMonitor")
		return
	}
	monitor, err := tun.NewDefaultInterfaceMonitor(updMonitor, logger, tun.DefaultInterfaceMonitorOptions{
		InterfaceFinder: control.NewDefaultInterfaceFinder(),
	})
	if err != nil {
		fmt.Println("Could not create DefaultInterfaceMonitor")
		return
	}
	// BUG-036: publish the manager only after BOTH monitors started.
	// Any Start failure closes what was already started and leaves
	// DnsManagerInstance nil instead of a half-alive manager.
	mgr := &DnsManager{Monitor: monitor}
	monitor.RegisterCallback(mgr.HandleSystemDNS)
	if err = updMonitor.Start(); err != nil {
		fmt.Println("Could not start updMonitor")
		_ = updMonitor.Close()
		return
	}
	if err = monitor.Start(); err != nil {
		fmt.Println("Could not start monitor")
		_ = monitor.Close()
		_ = updMonitor.Close()
		return
	}
	DnsManagerInstance = mgr
}

var DnsManagerInstance *DnsManager

type DnsManager struct {
	Monitor tun.DefaultInterfaceMonitor
	lastIfc *control.Interface
	// mu guards dnsIsSet/lastIfc: SetSystemDNS (Thrift handler
	// goroutines) and HandleSystemDNS (interface-monitor callback)
	// run concurrently.
	mu sync.Mutex
	// dnsIsSet mirrors the former package-global flag; per-manager
	// state so concurrent readers see one consistent value.
	dnsIsSet bool
}
