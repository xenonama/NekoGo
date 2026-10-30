package boxdns

import (
	"encoding/binary"
	"fmt"
	"log"
	"nekobox_core/internal/boxdns/winipcfg"
	"net/netip"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing/common/control"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/windnsapi"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	nameServerRegistryKey = "NameServer"
	localAddr             = "127.1.1.1"
	dhcpMarkAddr          = "127.1.2.3"
	setMarkAddr           = "127.3.2.1"
)

func (d *DnsManager) HandleSystemDNS(ifc *control.Interface, flag int) {
	if d == nil {
		log.Println("No DnsManager, you may need to restart nekobox")
		return
	}
	if ifc == nil {
		return
	}
	// BUG-007: snapshot, decision AND apply all run under the mutex.
	// A SetSystemDNS(clear) racing this callback can no longer flip
	// dnsIsSet between the snapshot and the winipcfg calls and make us
	// apply a stale branch. The winipcfg calls are short.
	current := *ifc
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.dnsIsSet {
		if err := d.restoreSystemDNS(current); err != nil {
			log.Println("[HandleSystemDNS] restore failed:", err)
		}
	} else {
		if err := d.setSystemDNS(current); err != nil {
			log.Println("[HandleSystemDNS] apply failed:", err)
		}
	}
	last := d.lastIfc
	if last != nil && last.Index != ifc.Index {
		if err := d.restoreSystemDNS(*last); err != nil {
			log.Println("[HandleSystemDNS] restore previous failed:", err)
		}
	}
	// BUG-008: remember the handled (current-default) interface so the
	// next flap-back compares against fresh state instead of a stale
	// entry that would leak the override on the abandoned interface.
	cp := current
	d.lastIfc = &cp
}

// IsSet reports whether a system-DNS override is currently applied.
func (d *DnsManager) IsSet() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dnsIsSet
}

func (d *DnsManager) getInterfaceGuid(ifc control.Interface) (string, error) {
	if d.Monitor == nil {
		return "", E.New("No Dns Manager, you may need to restart nekobox")
	}
	index := ifc.Index
	u, err := ifcIdxtoUUID(index)
	if err != nil {
		return "", err
	}
	guidStr := "{" + u.String() + "}"

	return guidStr, nil
}

func ifcIdxtoUUID(index int) (*uuid.UUID, error) {
	luid, err := winipcfg.LUIDFromIndex(uint32(index))
	if err != nil {
		log.Println("Could not get luid from index")
		return nil, err
	}
	guid, err := luid.GUID()
	if err != nil {
		log.Println("Could not get guid from luid")
		return nil, err
	}
	data1 := make([]byte, 4)
	data2 := make([]byte, 2)
	data3 := make([]byte, 2)
	binary.LittleEndian.PutUint32(data1, guid.Data1)
	binary.LittleEndian.PutUint16(data2, guid.Data2)
	binary.LittleEndian.PutUint16(data3, guid.Data3)
	u, _ := uuid.FromBytes([]byte{
		data1[3], data1[2], data1[1], data1[0],
		data2[1], data2[0],
		data3[1], data3[0],
		guid.Data4[0], guid.Data4[1], guid.Data4[2], guid.Data4[3],
		guid.Data4[4], guid.Data4[5], guid.Data4[6], guid.Data4[7],
	})
	return &u, nil
}

func (d *DnsManager) isIfcDNSDhcp(ifc control.Interface) (dhcp bool, err error) {
	if d == nil {
		log.Println("No DnsManager, you may need to restart nekobox")
		return false, E.New("No Dns Manager, you may need to restart nekobox")
	}

	luid, err := winipcfg.LUIDFromIndex(uint32(ifc.Index))
	if err != nil {
		log.Println("[isIfcDNSDhcp] failed to get luid from index:", err)
		return
	}

	dnsServers, err := luid.DNS()
	if err != nil {
		log.Println("[isIfcDNSDhcp] failed to get luid dns servers:", err)
		return
	}
	for _, server := range dnsServers {
		if server.String() == dhcpMarkAddr {
			return true, nil
		}
	}

	guidStr, err := d.getInterfaceGuid(ifc)
	if err != nil {
		return false, err
	}

	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\\CurrentControlSet\\Services\\Tcpip\\Parameters\\Interfaces\\`+guidStr, registry.QUERY_VALUE)
	if err != nil {
		log.Println("getNameServersForInterface OpenKey:", err)
		return false, err
	}
	defer key.Close()

	if manualNSs, _, err := key.GetStringValue(nameServerRegistryKey); err == nil {
		if len(strings.TrimSpace(manualNSs)) > 0 {
			return false, nil
		}
	}

	return true, nil
}

func (d *DnsManager) restoreSystemDNS(ifx control.Interface) error {
	luid, err := winipcfg.LUIDFromIndex(uint32(ifx.Index))
	if err != nil {
		log.Println("[restoreSystemDNS] failed to get luid from index:", err)
		return fmt.Errorf("luid from index %d: %w", ifx.Index, err)
	}

	dnsServers, err := luid.DNS()
	if err != nil {
		log.Println("[restoreSystemDNS] failed to get luid dns servers:", err)
		return fmt.Errorf("read dns servers: %w", err)
	}

	isDhcp := false
	newDnsServers := make([]netip.Addr, 0)
	wasSet := false
	for _, server := range dnsServers {
		if server.String() == setMarkAddr || server.String() == dhcpMarkAddr {
			isDhcp = server.String() == dhcpMarkAddr
			wasSet = true
			continue
		}
		newDnsServers = append(newDnsServers, server)
	}

	if !wasSet {
		log.Println("[restoreSystemDNS] no action needed")
		return nil
	}

	if len(newDnsServers) > 0 && newDnsServers[0].String() == localAddr {
		newDnsServers = newDnsServers[1:]
	}

	if isDhcp {
		err = luid.SetDNS(winipcfg.AddressFamily(windows.AF_INET), nil, nil)
	} else {
		err = luid.SetDNS(winipcfg.AddressFamily(windows.AF_INET), newDnsServers, nil)
	}
	if err != nil {
		log.Println("[restoreSystemDNS] failed to set dns servers:", err)
		return fmt.Errorf("restore dns servers: %w", err)
	}
	_ = windnsapi.FlushResolverCache()

	log.Println("[restoreSystemDNS] Local DNS Server removed for:", ifx.Name)
	return nil
}

func (d *DnsManager) setSystemDNS(ifx control.Interface) error {
	luid, err := winipcfg.LUIDFromIndex(uint32(ifx.Index))
	if err != nil {
		log.Println("[setSystemDNS] failed to get luid from index:", err)
		return fmt.Errorf("luid from index %d: %w", ifx.Index, err)
	}

	dnsServers, err := luid.DNS()
	if err != nil {
		log.Println("[setSystemDNS] failed to get luid dns servers:", err)
		return fmt.Errorf("read dns servers: %w", err)
	}

	wasSet := false
	wasDHCP := false
	newDnsServers := make([]netip.Addr, 0)
	for _, server := range dnsServers {
		if server.String() == setMarkAddr || server.String() == dhcpMarkAddr {
			wasSet = true
			wasDHCP = server.String() == dhcpMarkAddr
			continue
		}
		newDnsServers = append(newDnsServers, server)
	}
	if wasSet && len(newDnsServers) > 0 && newDnsServers[0].String() == localAddr {
		newDnsServers = newDnsServers[1:]
	}
	serverAddr, _ := netip.ParseAddr(localAddr)
	newDnsServers = append([]netip.Addr{serverAddr}, newDnsServers...)

	dhcp, err := d.isIfcDNSDhcp(ifx)
	if err != nil {
		log.Println("[setSystemDNS] failed to determine whether ifc DNS is dhcp:", err)
	}
	if dhcp || wasDHCP {
		markAddr, _ := netip.ParseAddr(dhcpMarkAddr)
		newDnsServers = append(newDnsServers, markAddr)
	} else {
		markAddr, _ := netip.ParseAddr(setMarkAddr)
		newDnsServers = append(newDnsServers, markAddr)
	}

	if err = luid.SetDNS(winipcfg.AddressFamily(windows.AF_INET), newDnsServers, nil); err != nil {
		log.Println("[setSystemDNS] failed to set dns servers:", err)
		return fmt.Errorf("apply dns servers: %w", err)
	}
	_ = windnsapi.FlushResolverCache()

	log.Println("[setSystemDNS] Local DNS Server added for:", ifx.Name)
	return nil
}

func (d *DnsManager) SetSystemDNS(ifc *control.Interface, clear bool) error {
	if d == nil {
		log.Println("No DnsManager, you may need to restart nekobox")
		return E.New("No dns Manager, you may need to restart nekobox")
	}

	if ifc == nil {
		ifc = d.Monitor.DefaultInterface()
		if ifc == nil {
			log.Println("Default interface is nil!")
			return E.New("Default interface is nil!")
		}
	}
	log.Println("[SetSystemDNS] Setting system dns for", ifc.Name, "clear is", clear)

	// BUG-027: the apply/restore result is propagated (the old code
	// always returned nil), and dnsIsSet flips only on success — a
	// failed apply must not report itself as applied.
	if clear {
		if err := d.restoreSystemDNS(*ifc); err != nil {
			return err
		}
		d.mu.Lock()
		d.dnsIsSet = false
		d.mu.Unlock()
		return nil
	}
	d.mu.Lock()
	d.lastIfc = ifc
	d.mu.Unlock()
	if err := d.setSystemDNS(*ifc); err != nil {
		return err
	}
	d.mu.Lock()
	d.dnsIsSet = true
	d.mu.Unlock()
	return nil
}
