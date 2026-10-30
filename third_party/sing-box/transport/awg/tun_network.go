package awg

import (
	"context"
	"net"
	"net/netip"
	"syscall"

	"github.com/amnezia-vpn/amneziawg-go/tun"
	"github.com/amnezia-vpn/amneziawg-go/tun/netstack"
	"github.com/sagernet/sing/common/metadata"
)

type networkTun struct {
	tun.Device
	conn      *netstack.Net
	addresses []netip.Addr
}

func newNetworkTun(address []netip.Prefix, mtu uint32) (tunAdapter, error) {
	var localAddresses []netip.Addr
	for _, prefix := range address {
		localAddresses = append(localAddresses, prefix.Addr())
	}

	tun, conn, err := netstack.CreateNetTUN(localAddresses, []netip.Addr{}, int(mtu))
	if err != nil {
		return nil, err
	}

	return &networkTun{
		Device:    tun,
		conn:      conn,
		addresses: localAddresses,
	}, nil
}

func (t *networkTun) Start() error {
	return nil
}

func (t *networkTun) DialContext(ctx context.Context, network string, destination metadata.Socksaddr) (net.Conn, error) {
	return t.conn.DialContext(ctx, network, destination.String())
}

func (t *networkTun) ListenPacket(ctx context.Context, destination metadata.Socksaddr) (net.PacketConn, error) {
	laddr := destination.AddrPort()
	if laddr.Addr().IsUnspecified() {
		local, ok := t.localAddress(laddr.Addr().Is4())
		if !ok {
			return nil, syscall.EAFNOSUPPORT
		}
		laddr = netip.AddrPortFrom(local, laddr.Port())
	}
	return t.conn.ListenUDPAddrPort(laddr)
}

func (t *networkTun) localAddress(is4 bool) (netip.Addr, bool) {
	for _, addr := range t.addresses {
		if addr.Is4() == is4 {
			return addr, true
		}
	}
	return netip.Addr{}, false
}
