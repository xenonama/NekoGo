package constant

const (
	TypeTun                = "tun"
	TypeRedirect           = "redirect"
	TypeTProxy             = "tproxy"
	TypeDirect             = "direct"
	TypeBridge             = "bridge"
	TypeBlock              = "block"
	TypeDNS                = "dns"
	TypeSOCKS              = "socks"
	TypeHTTP               = "http"
	TypeMixed              = "mixed"
	TypeShadowsocks        = "shadowsocks"
	TypeSnell              = "snell"
	TypeVMess              = "vmess"
	TypeTrojan             = "trojan"
	TypeNaive              = "naive"
	TypeWireGuard          = "wireguard"
	TypeHysteria           = "hysteria"
	TypeTor                = "tor"
	TypeSSH                = "ssh"
	TypeShadowTLS          = "shadowtls"
	TypeAnyTLS             = "anytls"
	TypeMieru              = "mieru"
	TypeShadowsocksR       = "shadowsocksr"
	TypeVLESS              = "vless"
	TypeTUIC               = "tuic"
	TypeHysteria2          = "hysteria2"
	TypeOpenConnect        = "openconnect"
	TypeOpenVPNClient      = "openvpn-client"
	TypeOpenVPNServer      = "openvpn-server"
	TypeTailscale          = "tailscale"
	TypeCloudflared        = "cloudflared"
	TypeDERP               = "derp"
	TypeResolved           = "resolved"
	TypeSSMAPI             = "ssm-api"
	TypeAPI                = "api"
	TypeCCM                = "ccm"
	TypeOCM                = "ocm"
	TypeOOMKiller          = "oom-killer"
	TypeUSBIPServer        = "usbip-server"
	TypeUSBIPClient        = "usbip-client"
	TypeHysteriaRealm      = "hysteria-realm"
	TypeACME               = "acme"
	TypeCloudflareOriginCA = "cloudflare-origin-ca"
	TypeJuicity            = "juicity"
	TypeTrustTunnel        = "trusttunnel"
	TypeAwg                = "awg"
)

const (
	TypeSelector = "selector"
	TypeURLTest  = "urltest"
)

func ProxyDisplayName(proxyType string) string {
	switch proxyType {
		case TypeTun:
			return "TUN"
		case TypeRedirect:
			return "Redirect"
		case TypeTProxy:
			return "TProxy"
		case TypeDirect:
			return "Direct"
		case TypeBridge:
			return "Bridge"
		case TypeBlock:
			return "Block"
		case TypeDNS:
			return "DNS"
		case TypeSOCKS:
			return "SOCKS"
		case TypeHTTP:
			return "HTTP"
		case TypeMixed:
			return "Mixed"
		case TypeShadowsocks:
			return "Shadowsocks"
		case TypeSnell:
			return "Snell"
		case TypeVMess:
			return "VMess"
		case TypeTrojan:
			return "Trojan"
		case TypeNaive:
			return "Naive"
		case TypeWireGuard:
			return "WireGuard"
		case TypeHysteria:
			return "Hysteria"
		case TypeTor:
			return "Tor"
		case TypeSSH:
			return "SSH"
		case TypeShadowTLS:
			return "ShadowTLS"
		case TypeShadowsocksR:
			return "ShadowsocksR"
		case TypeVLESS:
			return "VLESS"
		case TypeTUIC:
			return "TUIC"
		case TypeHysteria2:
			return "Hysteria2"
		case TypeAnyTLS:
			return "AnyTLS"
		case TypeMieru:
			return "Mieru"
		case TypeOpenConnect:
			return "OpenConnect"
		case TypeOpenVPNClient:
			return "OpenVPN Client"
		case TypeOpenVPNServer:
			return "OpenVPN Server"
		case TypeTailscale:
			return "Tailscale"
		case TypeCloudflared:
			return "Cloudflared"
		case TypeDERP:
			return "DERP"
		case TypeResolved:
			return "Resolved"
		case TypeSSMAPI:
			return "SSM API"
		case TypeAPI:
			return "API"
		case TypeCCM:
			return "CCM"
		case TypeOCM:
			return "OCM"
		case TypeOOMKiller:
			return "OOM Killer"
		case TypeUSBIPServer:
			return "USB/IP Server"
		case TypeUSBIPClient:
			return "USB/IP Client"
		case TypeHysteriaRealm:
			return "Hysteria Realm"
		case TypeACME:
			return "ACME"
		case TypeCloudflareOriginCA:
			return "Cloudflare Origin CA"
		case TypeSelector:
			return "Selector"
		case TypeURLTest:
			return "URLTest"
		case TypeJuicity:
			return "Juicity"
		case TypeTrustTunnel:
			return "TrustTunnel"
		case TypeAwg:
			return "AWG"
		default:
			return "Unknown"
	}
}
