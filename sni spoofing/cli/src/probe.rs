//! Driverless DPI probe: `sni-spoof.exe probe ...`.
//!
//! Why this exists: every on-wire claim about the split vectors used to need
//! the WinDivert driver, i.e. Admin. Without it the vectors could only be
//! argued about, never compared, which is how a decoy that has not traversed a
//! gateway for a year stays in the default pool. A probe needs no driver: it
//! dials the target IP over an ordinary socket, writes the same ClientHello the
//! engine builds in the same segment shapes, and classifies what came back.
//!
//! What a ServerHello proves: this gateway let this shape through to a real
//! server and let the answer come back. What it does not prove: that the
//! browser's own hello traverses (different bytes, same shape class), or that
//! the rule holds tomorrow. The numbers here are a snapshot of one network.

use serde_json::json;
use sni_core::fake_tcp::{split_hello, TLS_RECORD_HEADER};
use sni_core::tls::{build_fake_client_hello, TlsProfile};
use std::io::{Read, Write};
use std::net::{IpAddr, SocketAddr, TcpStream, ToSocketAddrs};
use std::time::{Duration, Instant};

/// The vectors measured when `--vector` is not given. `whole` is the control:
/// it is what an unmodified client sends, so its failure rate is the baseline
/// every other row has to beat.
pub const DEFAULT_VECTORS: &[&str] = &["whole", "split", "split_paced", "lead5"];

/// One attempt's outcome, as reported. `ServerHello` is the only success: the
/// record type says a TLS peer answered, everything below says a device
/// intervened or the path is dead.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Outcome {
    ServerHello,
    Alert,
    InjectedHttp,
    Rst,
    Closed,
    Timeout,
    ConnectFail,
    WriteFail,
}

impl Outcome {
    fn as_str(self) -> &'static str {
        match self {
            Outcome::ServerHello => "SERVERHELLO",
            Outcome::Alert => "ALERT",
            Outcome::InjectedHttp => "INJECTED-HTTP",
            Outcome::Rst => "RST",
            Outcome::Closed => "CLOSED",
            Outcome::Timeout => "TIMEOUT",
            Outcome::ConnectFail => "CONNECT-FAIL",
            Outcome::WriteFail => "WRITE-FAIL",
        }
    }

    pub fn is_success(self) -> bool {
        self == Outcome::ServerHello
    }
}

/// Segment shapes for one vector. Splitting is delegated to the engine's own
/// `split_hello`, so what the probe measures is what the relay puts on the
/// wire — including the per-connection shape randomisation.
pub fn shapes(hello: &[u8], vector: &str) -> Vec<Vec<u8>> {
    match vector {
        "whole" => vec![hello.to_vec()],
        "split" | "split_paced" => match split_hello(0, hello) {
            Some(segs) => segs.into_iter().map(|s| s.payload).collect(),
            // Not a splittable hello: send it as one piece rather than
            // silently reporting a shape that never happened.
            None => vec![hello.to_vec()],
        },
        "lead5" => {
            if hello.len() > TLS_RECORD_HEADER + 1 {
                vec![
                    hello[..TLS_RECORD_HEADER].to_vec(),
                    hello[TLS_RECORD_HEADER..].to_vec(),
                ]
            } else {
                vec![hello.to_vec()]
            }
        }
        _ => vec![hello.to_vec()],
    }
}

/// Classify the first bytes of a reply. A real TLS peer answers ServerHello
/// (0x16) or alert (0x15); an `HTTP/1.1 302` on a TLS port is a gateway writing
/// into the stream, which is the signature this network's interceptor leaves.
pub fn classify(buf: &[u8]) -> Outcome {
    if buf.is_empty() {
        return Outcome::Closed;
    }
    if buf[0] == 0x16 && buf.len() >= 6 {
        return if buf[5] == 0x02 {
            Outcome::ServerHello
        } else {
            Outcome::Alert
        };
    }
    if buf[0] == 0x15 {
        return Outcome::Alert;
    }
    if buf.starts_with(b"HTTP/") || buf.starts_with(b"GET ") {
        return Outcome::InjectedHttp;
    }
    // Anything else on 443 is not a TLS record: report it as an injection
    // rather than pretending the handshake worked.
    Outcome::InjectedHttp
}

/// An address the gateway handed out instead of the real one: private,
/// loopback, link-local, CGNAT or the interceptor's own range. Dialing these
/// measures nothing, so a probe refuses them.
pub fn is_intercepted(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => {
            let o = v4.octets();
            v4.is_private()
                || v4.is_loopback()
                || v4.is_link_local()
                || v4.is_unspecified()
                // 100.64.0.0/10 carrier-grade NAT, used by interceptors.
                || (o[0] == 100 && (o[1] & 0xc0) == 64)
        }
        IpAddr::V6(v6) => v6.is_loopback(),
    }
}

#[derive(Debug)]
pub struct Args {
    pub host: String,
    pub ip: Option<IpAddr>,
    pub port: u16,
    pub profile: TlsProfile,
    pub vectors: Vec<String>,
    pub reps: usize,
    pub delay_ms: u64,
    pub timeout_ms: u64,
}

impl Default for Args {
    fn default() -> Self {
        Self {
            host: String::new(),
            ip: None,
            port: 443,
            profile: TlsProfile::Legacy,
            vectors: DEFAULT_VECTORS.iter().map(|s| s.to_string()).collect(),
            reps: 5,
            delay_ms: 20,
            timeout_ms: 3000,
        }
    }
}

/// `--key=value` flags only, no argument parser dependency: this binary's
/// documented surface is three subcommands and probe must not change that.
pub fn parse_args(argv: &[String]) -> Result<Args, String> {
    let mut a = Args::default();
    for raw in argv {
        let Some((k, v)) = raw.split_once('=') else {
            return Err(format!("expected --key=value, got {raw}"));
        };
        let k = k.trim_start_matches("--");
        match k {
            "host" => a.host = v.trim().to_string(),
            "ip" => {
                let ip: IpAddr = v
                    .trim()
                    .parse()
                    .map_err(|_| format!("--ip {v} is not an IP address"))?;
                a.ip = Some(ip);
            }
            "port" => {
                a.port = v
                    .trim()
                    .parse()
                    .map_err(|_| format!("--port {v} is not a number"))?
            }
            "profile" => a.profile = TlsProfile::parse(v)?,
            "vector" => {
                a.vectors = v
                    .split(',')
                    .map(|s| s.trim().to_string())
                    .filter(|s| !s.is_empty())
                    .collect();
            }
            "reps" => {
                a.reps = v
                    .trim()
                    .parse()
                    .map_err(|_| format!("--reps {v} is not a number"))?
            }
            "delay" => {
                a.delay_ms = v
                    .trim()
                    .parse()
                    .map_err(|_| format!("--delay {v} is not a number"))?
            }
            "timeout" => {
                a.timeout_ms = v
                    .trim()
                    .parse()
                    .map_err(|_| format!("--timeout {v} is not a number"))?
            }
            other => return Err(format!("unknown flag --{other}")),
        }
    }
    if a.host.is_empty() {
        return Err("--host=<SNI hostname> is required".to_string());
    }
    if a.reps == 0 || a.reps > 50 {
        return Err("--reps must be 1..=50".to_string());
    }
    if a.vectors.is_empty() {
        return Err("--vector list is empty".to_string());
    }
    Ok(a)
}

/// Resolve the host the way the engine would have to: through whatever DNS the
/// box is given. On an intercepting network that answer may be a lie, so a
/// poisoned answer is refused rather than dialled — the caller passes `--ip`
/// with the real edge address it pinned instead.
pub fn resolve(host: &str, port: u16) -> Option<IpAddr> {
    let addrs = (host, port).to_socket_addrs().ok()?;
    addrs.map(|a| a.ip()).find(|ip| !is_intercepted(*ip))
}

/// One attempt: dial, write the shape, read the first reply.
fn attempt(
    addr: SocketAddr,
    segs: &[Vec<u8>],
    pacing: Duration,
    timeout: Duration,
) -> (Outcome, u64, Vec<usize>, Option<String>) {
    let sizes: Vec<usize> = segs.iter().map(|s| s.len()).collect();
    let stream = match TcpStream::connect_timeout(&addr, timeout) {
        Ok(s) => s,
        Err(_) => return (Outcome::ConnectFail, 0, sizes, None),
    };
    // Nagle would coalesce the segments back into one write, which is exactly
    // the shape the vector is trying to avoid. Without this the split vectors
    // measure nothing.
    let _ = stream.set_nodelay(true);
    let mut stream = stream;
    let start = Instant::now();
    let mut wrote_any = false;
    for (i, seg) in segs.iter().enumerate() {
        if let Err(e) = stream.write_all(seg) {
            // A reset that lands during the write is still the gateway's
            // answer to the shape; only a first-segment failure means the path
            // itself was dead.
            return if wrote_any || e.kind() == std::io::ErrorKind::ConnectionRefused {
                (
                    Outcome::Rst,
                    start.elapsed().as_millis() as u64,
                    sizes,
                    None,
                )
            } else {
                (
                    Outcome::WriteFail,
                    start.elapsed().as_millis() as u64,
                    sizes,
                    None,
                )
            };
        }
        wrote_any = true;
        let _ = stream.flush();
        if i + 1 < segs.len() && !pacing.is_zero() {
            std::thread::sleep(pacing);
        }
    }
    let mut buf = vec![0u8; 4096];
    if let Err(e) = stream.set_read_timeout(Some(timeout)) {
        return (
            Outcome::Timeout,
            start.elapsed().as_millis() as u64,
            sizes,
            Some(e.to_string()),
        );
    }
    match stream.read(&mut buf) {
        Ok(0) => (
            Outcome::Closed,
            start.elapsed().as_millis() as u64,
            sizes,
            None,
        ),
        Ok(n) => {
            let head = String::from_utf8_lossy(&buf[..n.min(64)]).into_owned();
            let printable: String = head
                .chars()
                .map(|c| {
                    if c.is_ascii_graphic() || c == ' ' {
                        c
                    } else {
                        '.'
                    }
                })
                .collect();
            (
                classify(&buf[..n]),
                start.elapsed().as_millis() as u64,
                sizes,
                Some(printable),
            )
        }
        Err(e) => {
            let ms = start.elapsed().as_millis() as u64;
            if e.kind() == std::io::ErrorKind::ConnectionReset {
                (Outcome::Rst, ms, sizes, None)
            } else {
                (Outcome::Timeout, ms, sizes, Some(e.kind().to_string()))
            }
        }
    }
}

fn median(mut v: Vec<u64>) -> u64 {
    if v.is_empty() {
        return 0;
    }
    v.sort_unstable();
    v[v.len() / 2]
}

/// Run every vector and print one `[PROBE]` line per attempt plus a
/// `[SUMMARY]` per vector. Returns 0 when at least one shape traversed.
pub fn run(a: &Args) -> i32 {
    let ip = match a.ip.or_else(|| resolve(&a.host, a.port)) {
        Some(ip) => ip,
        None => {
            println!(
                "[PROBE] {}",
                json!({"error": "dns resolution failed", "host": a.host})
            );
            eprintln!(
                "probe: cannot resolve {} — pass --ip=<public edge IPv4>",
                a.host
            );
            return 2;
        }
    };
    if is_intercepted(ip) {
        eprintln!(
            "probe: {ip} is a private/intercept address — the resolver is being lied to; pass --ip=<real edge IP>"
        );
        return 2;
    }
    let addr = SocketAddr::new(ip, a.port);
    let hello = build_fake_client_hello(a.host.as_bytes(), a.profile);
    let timeout = Duration::from_millis(a.timeout_ms);
    let pacing = Duration::from_millis(a.delay_ms);
    let mut any_ok = false;
    for vector in &a.vectors {
        let segs = shapes(&hello, vector);
        // Only the vector that names a delay is paced; the rest go out as one
        // burst so the comparison is about shape, not timing.
        let vector_pacing = if vector == "split_paced" {
            pacing
        } else {
            Duration::ZERO
        };
        let mut ok = 0usize;
        let mut ms = Vec::new();
        let mut counts: Vec<(String, usize)> = Vec::new();
        for _ in 0..a.reps {
            let (outcome, elapsed, sizes, head) = attempt(addr, &segs, vector_pacing, timeout);
            if outcome.is_success() {
                ok += 1;
                ms.push(elapsed);
            }
            any_ok |= outcome.is_success();
            match counts.iter_mut().find(|(k, _)| *k == outcome.as_str()) {
                Some((_, n)) => *n += 1,
                None => counts.push((outcome.as_str().to_string(), 1)),
            }
            let line = json!({
                "host": a.host,
                "ip": ip.to_string(),
                "port": a.port,
                "profile": profile_name(&a.profile),
                "vector": vector,
                "result": outcome.as_str(),
                "ms": elapsed,
                "seg_sizes": sizes,
                "head": head,
            });
            println!("[PROBE] {line}");
        }
        let line = json!({
            "vector": vector,
            "reps": a.reps,
            "ok": ok,
            "median_ms": median(ms),
            "outcomes": counts,
        });
        println!("[SUMMARY] {line}");
    }
    if any_ok {
        0
    } else {
        1
    }
}

fn profile_name(p: &TlsProfile) -> &'static str {
    match p {
        TlsProfile::Legacy => "legacy",
        TlsProfile::Chrome => "chrome",
        TlsProfile::Firefox => "firefox",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample(host: &str) -> Vec<u8> {
        build_fake_client_hello(host.as_bytes(), TlsProfile::Legacy)
    }

    #[test]
    fn whole_vector_is_one_segment_of_the_full_hello() {
        let h = sample("www.youtube.com");
        let segs = shapes(&h, "whole");
        assert_eq!(segs.len(), 1);
        assert_eq!(segs[0].len(), h.len());
    }

    #[test]
    fn split_vector_never_sends_the_whole_hostname_in_one_piece() {
        let h = sample("www.youtube.com");
        // Every shape must reassemble exactly.
        for vector in ["split", "split_paced", "lead5"] {
            let segs = shapes(&h, vector);
            assert!(segs.len() >= 2, "{vector} produced one segment");
            let joined: Vec<u8> = segs.iter().flat_map(|s| s.clone()).collect();
            assert_eq!(joined, h, "{vector} lost or added bytes");
        }
        // Only the label-splitting vectors hide the hostname. `lead5` is a
        // different question — it moves the record header out of the first
        // segment and deliberately leaves the body whole.
        for vector in ["split", "split_paced"] {
            for s in &shapes(&h, vector) {
                let text = String::from_utf8_lossy(s);
                assert!(!text.contains("youtube"), "{vector} leaked the label");
            }
        }
    }

    #[test]
    fn lead5_cuts_at_the_record_header() {
        let h = sample("www.google.com");
        let segs = shapes(&h, "lead5");
        assert_eq!(segs[0], &h[..TLS_RECORD_HEADER]);
        assert_eq!(segs[1], &h[TLS_RECORD_HEADER..]);
    }

    #[test]
    fn classification_reads_the_gateway_signatures() {
        assert_eq!(
            classify(&[0x16, 0x03, 0x03, 0, 10, 0x02, 0, 0, 0, 0, 0, 0]),
            Outcome::ServerHello
        );
        assert_eq!(
            classify(&[0x15, 0x03, 0x03, 0, 2, 0x02, 0x32]),
            Outcome::Alert
        );
        assert_eq!(classify(b"HTTP/1.1 302 found\r\n"), Outcome::InjectedHttp);
        assert_eq!(classify(&[]), Outcome::Closed);
        assert_eq!(
            classify(&[0x17, 0x03, 0x03, 0, 1, 0]),
            Outcome::InjectedHttp
        );
    }

    #[test]
    fn intercepted_addresses_are_refused() {
        for bad in [
            "10.10.34.35",
            "10.10.34.36",
            "127.0.0.1",
            "192.168.1.1",
            "100.64.0.1",
            "169.254.1.1",
        ] {
            assert!(is_intercepted(bad.parse::<IpAddr>().unwrap()), "{bad}");
        }
        for good in ["57.144.244.34", "216.239.38.119", "157.240.7.14"] {
            assert!(!is_intercepted(good.parse::<IpAddr>().unwrap()), "{good}");
        }
    }

    #[test]
    fn args_reject_bad_input_and_default_the_rest() {
        let a = parse_args(&["--host=www.youtube.com".to_string()]).unwrap();
        assert_eq!(a.port, 443);
        assert_eq!(a.reps, 5);
        assert_eq!(
            a.vectors,
            DEFAULT_VECTORS
                .iter()
                .map(|s| s.to_string())
                .collect::<Vec<_>>()
        );
        assert!(
            parse_args(&["--ip=1.2.3.4".to_string()]).is_err(),
            "host required"
        );
        assert!(parse_args(&["--host=a".to_string(), "--reps=0".to_string()]).is_err());
        assert!(parse_args(&["--host=a".to_string(), "--bogus=1".to_string()]).is_err());
        assert!(parse_args(&["--host=a".to_string(), "--profile=netscape".to_string()]).is_err());
        let a = parse_args(&[
            "--host=a".to_string(),
            "--vector=whole,split".to_string(),
            "--delay=5".to_string(),
        ])
        .unwrap();
        assert_eq!(a.vectors, vec!["whole".to_string(), "split".to_string()]);
        assert_eq!(a.delay_ms, 5);
    }

    #[test]
    fn median_of_empty_is_zero_and_of_odds_is_the_middle() {
        assert_eq!(median(vec![]), 0);
        assert_eq!(median(vec![900, 40, 120]), 120);
    }
}
