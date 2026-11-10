// T7: Plan A (RESOLVE_SNI / CONNECT_OVERRIDE). Config-level tests plus an
// in-process fake DNS-over-HTTPS server. Nothing here contacts 1.1.1.1: the
// fake is an IP-literal loopback listener and every lookup goes to it.

use std::io::{Read, Write};
use std::net::TcpListener;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use serde_json::json;
use sni_core::config::{apply_plan_a_full, apply_plan_a_with_servers, migrate, validate, Endpoint};
use sni_core::net::DohServer;
use sni_core::scan::rank_reachable;

/// Names the fake answers, with RFC 5737 documentation addresses (never
/// routable, so a stray real lookup cannot mask a broken test).
const ANSWERS: &[(&str, &str)] = &[("plan-a.example", "203.0.113.10")];

struct FakeDoh {
    base: String,
    hits: Arc<AtomicUsize>,
}

impl FakeDoh {
    /// Serves the Cloudflare `application/dns-json` shape from the default
    /// `ANSWERS` table. Known names get one A record; anything else is
    /// NXDOMAIN (`Status: 3`).
    fn start() -> Self {
        Self::with(
            ANSWERS
                .iter()
                .map(|(h, ip)| (h.to_string(), ip.to_string()))
                .collect(),
        )
    }

    /// Same fake, but with a caller-supplied answer table so a test can model
    /// several providers / names without touching the global default.
    fn with(answers: Vec<(String, String)>) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind fake DoH");
        let addr = listener.local_addr().expect("fake DoH addr");
        let hits = Arc::new(AtomicUsize::new(0));
        let counter = Arc::clone(&hits);
        let answers = Arc::new(answers);
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(mut stream) = stream else { continue };
                let hits = Arc::clone(&counter);
                let answers = Arc::clone(&answers);
                std::thread::spawn(move || {
                    let _ = stream.set_read_timeout(Some(Duration::from_secs(5)));
                    let mut request = [0u8; 1024];
                    let read = stream.read(&mut request).unwrap_or(0);
                    hits.fetch_add(1, Ordering::SeqCst);
                    let body = dns_json(
                        &answers,
                        &requested_name(&String::from_utf8_lossy(&request[..read])),
                    );
                    let response = format!(
                        "HTTP/1.1 200 OK\r\ncontent-type: application/dns-json\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
                        body.len()
                    );
                    let _ = stream.write_all(response.as_bytes());
                    let _ = stream.flush();
                });
            }
        });
        Self {
            base: format!("http://{addr}/dns-query"),
            hits,
        }
    }

    /// Queries received, so a test can prove the DoH path was skipped.
    fn hits(&self) -> usize {
        self.hits.load(Ordering::SeqCst)
    }
}

fn requested_name(request: &str) -> String {
    let path = request.split_whitespace().nth(1).unwrap_or_default();
    let query = path.split_once('?').map(|(_, q)| q).unwrap_or_default();
    query
        .split('&')
        .find_map(|pair| pair.strip_prefix("name="))
        .unwrap_or_default()
        .to_string()
}

fn dns_json(answers: &[(String, String)], name: &str) -> String {
    match answers.iter().find(|(host, _)| host == name) {
        Some((host, ip)) => format!(
            r#"{{"Status":0,"Answer":[{{"name":"{host}","type":1,"TTL":300,"data":"{ip}"}}]}}"#
        ),
        None => r#"{"Status":3,"Authority":[]}"#.to_string(),
    }
}

fn servers(doh: &FakeDoh) -> [DohServer<'_>; 1] {
    // pin_ip: None — the fake's URL host is already an IP literal, so no name
    // resolution of any kind happens.
    [DohServer {
        url: doh.base.as_str(),
        pin_ip: None,
    }]
}

#[test]
fn t7_plan_b_config_is_untouched() {
    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45570,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 45571}],
        "FAKE_SNIS": ["example.com"],
    });
    let mut cfg = migrate(v).expect("plan B migrates");
    assert!(!cfg.resolve_sni);
    assert!(cfg.connect_override.is_empty());
    // Deliberately zero servers: a Plan B config must never reach DoH.
    apply_plan_a_with_servers(&mut cfg, &[]).expect("plan B applies");
    assert_eq!(
        cfg.endpoints,
        vec![Endpoint {
            ip: "127.0.0.1".to_string(),
            port: 45571
        }]
    );
    validate(&cfg).expect("plan B still validates");
}

#[test]
fn t7_plan_a_resolves_fake_sni_via_doh() {
    let doh = FakeDoh::start();
    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45572,
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": true,
    });
    let mut cfg = migrate(v).expect("plan A migrates");
    assert!(cfg.endpoints.is_empty());
    // Before resolution the list is legitimately empty, so a config a caller
    // only validates (GUI save) is not rejected.
    validate(&cfg).expect("plan A validates before resolution");
    apply_plan_a_with_servers(&mut cfg, &servers(&doh)).expect("plan A resolves");
    assert_eq!(
        cfg.endpoints,
        vec![Endpoint {
            ip: "203.0.113.10".to_string(),
            port: 443
        }]
    );
    validate(&cfg).expect("resolved plan A validates");
    assert!(doh.hits() >= 1, "the fake server must have been queried");
}

#[test]
fn t7_connect_override_wins_and_skips_doh() {
    let doh = FakeDoh::start();
    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45573,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 8443}],
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": true,
        "CONNECT_OVERRIDE": "198.51.100.7",
    });
    let mut cfg = migrate(v).expect("migrates");
    apply_plan_a_with_servers(&mut cfg, &servers(&doh)).expect("override applies");
    // The configured endpoint supplies the port; its IP is replaced.
    assert_eq!(
        cfg.endpoints,
        vec![Endpoint {
            ip: "198.51.100.7".to_string(),
            port: 8443
        }]
    );
    assert_eq!(doh.hits(), 0, "connect_override must not query DoH");
    validate(&cfg).expect("override validates");
}

#[test]
fn t7_plan_a_fails_fast_and_names_the_sni() {
    let doh = FakeDoh::start();
    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45574,
        "FAKE_SNIS": ["nxdomain.example"],
        "RESOLVE_SNI": true,
    });
    let mut cfg = migrate(v).expect("migrates");
    let err =
        apply_plan_a_with_servers(&mut cfg, &servers(&doh)).expect_err("NXDOMAIN must fail fast");
    let msg = err.to_string();
    assert!(msg.contains("nxdomain.example"), "must name the SNI: {msg}");
    assert!(msg.contains("RESOLVE_SNI"), "must name the field: {msg}");
    assert!(
        cfg.endpoints.is_empty(),
        "a failed lookup leaves the list untouched"
    );
}

#[test]
fn t7_resolve_sni_without_fake_sni_is_rejected() {
    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45575,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 45576}],
        "RESOLVE_SNI": true,
    });
    let cfg = migrate(v).expect("migrates");
    let err = validate(&cfg).expect_err("resolve_sni needs a FAKE_SNI");
    assert!(err.to_string().contains("No FAKE_SNI set"), "{err}");
}

#[test]
fn t7_new_keys_migrate_and_bad_override_is_rejected() {
    let tolerant = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45577,
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": "yes",
    });
    let cfg = migrate(tolerant).expect("string bool migrates");
    assert!(cfg.resolve_sni);

    let bad = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45577,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 45578}],
        "CONNECT_OVERRIDE": "not-an-ip",
    });
    let cfg = migrate(bad).expect("migrates");
    let err = validate(&cfg).expect_err("bad override is rejected");
    assert!(
        err.to_string()
            .contains("CONNECT_OVERRIDE must be an IPv4 address"),
        "{err}"
    );
}

/// A dead first provider (closed loopback port → connection refused) must fall
/// through to the next, which is the whole point of multi-provider DoH. No
/// real 1.1.1.1/8.8.8.8/9.9.9.9 is ever dialed.
#[test]
fn t7_plan_a_multi_provider_failover() {
    // Bind then drop to obtain a loopback port that is guaranteed closed.
    let dead = TcpListener::bind("127.0.0.1:0").expect("bind dead port");
    let dead_port = dead.local_addr().expect("dead addr").port();
    drop(dead);
    let dead_url = format!("http://127.0.0.1:{dead_port}/dns-query");

    let good = FakeDoh::start();
    let servers = [
        DohServer {
            url: dead_url.as_str(),
            pin_ip: None,
        },
        DohServer {
            url: good.base.as_str(),
            pin_ip: None,
        },
    ];

    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45580,
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": true,
    });
    let mut cfg = migrate(v).expect("migrates");
    apply_plan_a_with_servers(&mut cfg, &servers).expect("failover resolves");
    assert_eq!(
        cfg.endpoints,
        vec![Endpoint {
            ip: "203.0.113.10".to_string(),
            port: 443
        }]
    );
    assert!(
        good.hits() >= 1,
        "the second provider must have been queried"
    );
}

/// After resolution the candidates are scanned: an unreachable documentation
/// address is dropped and the live loopback listener is kept, proving the
/// scan+rank step runs and filters. The fake DoH answers two names; only the
/// one pointing at a real listener survives.
#[test]
fn t7_plan_a_scans_and_ranks_reachable_first() {
    // A live loopback listener (reachable) standing in for a good CF edge.
    let live = TcpListener::bind("127.0.0.1:0").expect("bind live");
    let live_port = live.local_addr().expect("live addr").port();
    std::thread::spawn(move || {
        for s in live.incoming().flatten() {
            // Hold the connection open briefly so the connect completes.
            let _ = s.set_read_timeout(Some(Duration::from_millis(50)));
        }
    });

    let doh = FakeDoh::with(vec![
        ("dead.example".to_string(), "203.0.113.99".to_string()),
        ("live.example".to_string(), "127.0.0.1".to_string()),
    ]);
    let servers = [DohServer {
        url: doh.base.as_str(),
        pin_ip: None,
    }];

    let v = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45581,
        // ENDPOINTS only supplies the port the derived endpoints inherit.
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": live_port}],
        // dead first on purpose: ranking, not resolution order, must decide.
        "FAKE_SNIS": ["dead.example", "live.example"],
        "RESOLVE_SNI": true,
    });
    let mut cfg = migrate(v).expect("migrates");
    apply_plan_a_full(&mut cfg, &servers, false, Some(rank_reachable)).expect("scan applies");
    assert_eq!(
        cfg.endpoints,
        vec![Endpoint {
            ip: "127.0.0.1".to_string(),
            port: live_port
        }],
        "the unreachable candidate must be scanned out"
    );
    validate(&cfg).expect("scanned plan A validates");
}
