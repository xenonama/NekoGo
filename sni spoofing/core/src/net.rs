//! Network helpers: replaces `utils/network_tools.py`.
//!
//! `get_default_interface_ipv4` binds a UDP socket and "connects" it to the
//! target (no packets sent for UDP) then reads the local address — identical
//! technique to the Python version.
//!
//! Plan A (`RESOLVE_SNI`) adds `resolve_a`: a DNS-over-HTTPS A lookup that
//! turns a fake SNI into the connect IP. It dials Cloudflare *by address* and
//! uses the hostname only for the TLS handshake, so a poisoned local resolver
//! cannot steer the answer while certificate verification stays on.

use std::collections::HashMap;
use std::net::{IpAddr, SocketAddr, ToSocketAddrs, UdpSocket};
use std::sync::OnceLock;
use std::time::Duration;

use parking_lot::Mutex;
use ureq::config::Config as UreqConfig;
use ureq::http::Uri;
use ureq::tls::{TlsConfig, TlsProvider};
use ureq::unversioned::resolver::{ResolvedSocketAddrs, Resolver};
use ureq::unversioned::transport::{DefaultConnector, NextTimeout};
use ureq::{Agent, Error};

use crate::config::is_valid_ipv4;

/// Local IPv4 used to reach `addr` (default 8.8.8.8). Empty string on failure
/// would be Python behavior; Rust returns `None` instead (no sentinel strings).
pub fn get_default_interface_ipv4(addr: &str) -> Option<String> {
    let target = format!("{}:53", addr);
    let sock = UdpSocket::bind("0.0.0.0:0").ok()?;
    sock.connect(target).ok()?;
    let local = sock.local_addr().ok()?;
    match local.ip() {
        IpAddr::V4(v4) => Some(v4.to_string()),
        IpAddr::V6(_) => None,
    }
}

/// IPv6 counterpart (mirrors `get_default_interface_ipv6`).
pub fn get_default_interface_ipv6(addr: &str) -> Option<String> {
    let target = format!("[{}]:53", addr);
    let sock = UdpSocket::bind("[::]:0").ok()?;
    sock.connect(target).ok()?;
    let local = sock.local_addr().ok()?;
    match local.ip() {
        IpAddr::V6(v6) => Some(v6.to_string()),
        IpAddr::V4(_) => None,
    }
}

/// One DoH endpoint. `url` supplies the TLS hostname (so the certificate
/// verifies); `pin_ip`, when set, is the address actually dialed, which is how
/// the lookup avoids the local resolver entirely.
#[derive(Debug, Clone, Copy)]
pub struct DohServer<'a> {
    pub url: &'a str,
    pub pin_ip: Option<&'a str>,
}

/// DoH providers tried in order. Each entry pins the address actually dialed
/// while the URL keeps the provider hostname for SNI + certificate
/// verification, so the local resolver is never consulted. Diversity is the
/// point: a network that intercepts Cloudflare DoH (common on censored links)
/// usually still reaches Google or Quad9, and `resolve_a_os` is the last
/// resort behind all of them. Worst case is one pass per server
/// (`DOH_CONNECT_TIMEOUT + DOH_RESPONSE_TIMEOUT`), then the OS fallback.
pub const DEFAULT_DOH_SERVERS: &[DohServer<'static>] = &[
    DohServer {
        url: "https://api.cloudflare-dns.com/dns-query",
        pin_ip: Some("1.1.1.1"),
    },
    DohServer {
        url: "https://api.cloudflare-dns.com/dns-query",
        pin_ip: Some("1.0.0.1"),
    },
    DohServer {
        url: "https://dns.google/resolve",
        pin_ip: Some("8.8.8.8"),
    },
    DohServer {
        url: "https://dns.google/resolve",
        pin_ip: Some("8.8.4.4"),
    },
    DohServer {
        url: "https://dns.quad9.net/dns-query",
        pin_ip: Some("9.9.9.9"),
    },
    DohServer {
        url: "https://dns.quad9.net/dns-query",
        pin_ip: Some("149.112.112.9"),
    },
];

const DOH_CONNECT_TIMEOUT: Duration = Duration::from_secs(2);
const DOH_RESPONSE_TIMEOUT: Duration = Duration::from_secs(3);

/// Successful answers live for the life of the process. Failures are never
/// cached, so one DoH outage cannot poison later connections.
static DOH_CACHE: OnceLock<Mutex<HashMap<String, String>>> = OnceLock::new();

fn doh_cache() -> &'static Mutex<HashMap<String, String>> {
    DOH_CACHE.get_or_init(|| Mutex::new(HashMap::new()))
}

/// IPv4 A record for `host` via DoH, trying each server in order and caching
/// the answer. `Err` carries every per-server cause, joined.
pub fn resolve_a(servers: &[DohServer<'_>], host: &str) -> Result<String, String> {
    if let Some(cached) = doh_cache().lock().get(host) {
        return Ok(cached.clone());
    }
    let mut causes = Vec::new();
    for server in servers {
        match doh_query(server, host) {
            Ok(Some(ip)) => {
                doh_cache().lock().insert(host.to_string(), ip.clone());
                return Ok(ip);
            }
            Ok(None) => causes.push(format!("{}: no A record", describe(server))),
            Err(e) => causes.push(format!("{}: {}", describe(server), e)),
        }
    }
    Err(causes.join(", "))
}

fn describe(server: &DohServer<'_>) -> String {
    server.pin_ip.unwrap_or(server.url).to_string()
}

/// Last-resort OS-resolver lookup for `host` (first IPv4 answer). Plan A
/// exists precisely to bypass a poisoned local resolver, so this is only
/// reached when every DoH provider fails; the caller warns when it is used.
pub fn resolve_a_os(host: &str) -> Option<String> {
    (host, 0)
        .to_socket_addrs()
        .ok()?
        .find_map(|sa| match sa.ip() {
            IpAddr::V4(v4) => Some(v4.to_string()),
            IpAddr::V6(_) => None,
        })
}

fn doh_query(server: &DohServer<'_>, host: &str) -> Result<Option<String>, String> {
    let mut response = agent_for(server)
        .get(server.url)
        .query("name", host)
        .query("type", "A")
        .header("accept", "application/dns-json")
        .call()
        .map_err(|e| e.to_string())?;
    let body = response
        .body_mut()
        .read_to_string()
        .map_err(|e| e.to_string())?;
    first_a_record(&body)
}

fn agent_for(server: &DohServer<'_>) -> Agent {
    let config = Agent::config_builder()
        // native-tls is the only TLS provider compiled in (Schannel on
        // Windows), but the setting is never picked up implicitly.
        .tls_config(
            TlsConfig::builder()
                .provider(TlsProvider::NativeTls)
                .build(),
        )
        .timeout_connect(Some(DOH_CONNECT_TIMEOUT))
        .timeout_recv_response(Some(DOH_RESPONSE_TIMEOUT))
        .build();
    let pin = server.pin_ip.and_then(|ip| ip.parse().ok());
    Agent::with_parts(config, DefaultConnector::default(), PinnedResolver { pin })
}

#[derive(Debug)]
struct PinnedResolver {
    pin: Option<IpAddr>,
}

impl Resolver for PinnedResolver {
    fn resolve(
        &self,
        uri: &Uri,
        _config: &UreqConfig,
        _timeout: NextTimeout,
    ) -> Result<ResolvedSocketAddrs, Error> {
        let authority = uri.authority().ok_or(Error::HostNotFound)?;
        let port = authority
            .port_u16()
            .unwrap_or_else(|| match uri.scheme().map(|s| s.as_str()) {
                Some("http") => 80,
                _ => 443,
            });
        let mut found: Vec<SocketAddr> = Vec::new();
        match self.pin {
            Some(ip) => found.push(SocketAddr::new(ip, port)),
            None => {
                let host_and_port = format!("{}:{}", authority.host(), port);
                found.extend(
                    host_and_port
                        .to_socket_addrs()
                        .map_err(|_| Error::HostNotFound)?,
                );
            }
        }
        if found.is_empty() {
            return Err(Error::HostNotFound);
        }
        let mut addrs = self.empty();
        for addr in found {
            addrs.push(addr);
        }
        Ok(addrs)
    }
}

/// First IPv4 A answer in a `application/dns-json` body. `Ok(None)` means the
/// name has no usable A record (NXDOMAIN, SERVFAIL, or AAAA-only).
fn first_a_record(body: &str) -> Result<Option<String>, String> {
    let value: serde_json::Value =
        serde_json::from_str(body).map_err(|e| format!("response is not JSON: {e}"))?;
    // Status 0 == NOERROR; 2 SERVFAIL / 3 NXDOMAIN and friends carry no answer.
    if value
        .get("Status")
        .and_then(|status| status.as_u64())
        .unwrap_or(0)
        != 0
    {
        return Ok(None);
    }
    let answers: Vec<&serde_json::Value> = match value.get("Answer") {
        // A single RR is emitted as an object, not a one-item array.
        Some(serde_json::Value::Array(items)) => items.iter().collect(),
        Some(single @ serde_json::Value::Object(_)) => vec![single],
        _ => return Ok(None),
    };
    for answer in answers {
        if answer.get("type").and_then(|t| t.as_u64()) != Some(1) {
            continue;
        }
        if let Some(data) = answer.get("data").and_then(|d| d.as_str()) {
            if is_valid_ipv4(data) {
                return Ok(Some(data.to_string()));
            }
        }
    }
    Ok(None)
}
