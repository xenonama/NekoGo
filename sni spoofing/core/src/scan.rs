//! Plan A IP scan + rank.
//!
//! After `RESOLVE_SNI` turns fake SNIs into candidate connect addresses, this
//! module dials each one to measure TCP reachability + connect latency and
//! returns them ranked best-first, so the engine spends its first connections
//! on a live, fast endpoint instead of round-robining into a black hole.
//!
//! Std-only and self-contained on purpose: `sni-core` cannot depend on
//! `sni-engine` (where the richer `probe.rs` TLS handshake scanner lives), so
//! this is a deliberately lean TCP-RTT scan rather than a reuse of that code.
//! Concurrent (one thread per candidate), total, never panics.

use std::net::{Ipv4Addr, SocketAddr, TcpStream};
use std::time::{Duration, Instant};

/// One scanned candidate: its address and, when the TCP connect succeeded, the
/// round-trip time in milliseconds.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ScanHit {
    pub ip: String,
    pub port: u16,
    pub latency_ms: Option<u128>,
}

impl ScanHit {
    pub fn reachable(&self) -> bool {
        self.latency_ms.is_some()
    }
}

/// Scan `candidates` for TCP reachability + connect latency.
///
/// Returns every candidate (reachable and not) as `ScanHit`s, ordered
/// reachable-first then lowest-latency-first, with unreachable entries last
/// (by address, for a stable order). The caller decides whether to keep only
/// the reachable band; nothing is dropped here so a fully-blocked scan is
/// still observable.
pub fn scan_candidates(candidates: &[(String, u16)], timeout: Duration) -> Vec<ScanHit> {
    let mut handles = Vec::with_capacity(candidates.len());
    for (ip, port) in candidates {
        let ip = ip.clone();
        let port = *port;
        handles.push(std::thread::spawn(move || scan_one(&ip, port, timeout)));
    }
    let mut hits: Vec<ScanHit> = handles
        .into_iter()
        .filter_map(|h| match h.join() {
            Ok(hit) => Some(hit),
            Err(_) => {
                tracing::warn!("scan thread panicked (candidate skipped)");
                None
            }
        })
        .collect();
    hits.sort_by(|a, b| {
        match (a.latency_ms, b.latency_ms) {
            (Some(x), Some(y)) => x.cmp(&y),
            (Some(_), None) => std::cmp::Ordering::Less,
            (None, Some(_)) => std::cmp::Ordering::Greater,
            (None, None) => std::cmp::Ordering::Equal,
        }
        .then_with(|| a.ip.cmp(&b.ip))
        .then_with(|| a.port.cmp(&b.port))
    });
    hits
}

/// Convenience for the common Plan A call: scan, then return only the
/// reachable candidates ranked best-first. Empty when nothing answered.
pub fn rank_reachable(candidates: &[(String, u16)], timeout: Duration) -> Vec<(String, u16)> {
    scan_candidates(candidates, timeout)
        .into_iter()
        .filter(|h| h.reachable())
        .map(|h| (h.ip, h.port))
        .collect()
}

fn scan_one(ip: &str, port: u16, timeout: Duration) -> ScanHit {
    let miss = ScanHit {
        ip: ip.to_string(),
        port,
        latency_ms: None,
    };
    let v4: Ipv4Addr = match ip.parse() {
        Ok(a) => a,
        Err(_) => return miss,
    };
    if port == 0 {
        return miss;
    }
    let addr = SocketAddr::new(v4.into(), port);
    let start = Instant::now();
    match TcpStream::connect_timeout(&addr, timeout) {
        Ok(_) => ScanHit {
            ip: ip.to_string(),
            port,
            latency_ms: Some(start.elapsed().as_millis()),
        },
        Err(_) => miss,
    }
}
