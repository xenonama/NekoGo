// T8: Plan A across the C ABI boundary (the GUI path).
//
// PART 2 put Plan A in `core`; this file covers the two engine-ffi glue
// points that let NekoGo use it: `config_to_json` (emits the opt-in keys)
// and `parse_and_validate` (derives the endpoints before the engine sees
// them). Both are private, so the tests drive the real exported entry points
// (`sni_set_config_path` / `sni_save_config` / `sni_load_config`) — the same
// calls the Wails layer makes.
//
// Nothing here contacts 1.1.1.1. The one test that needs an actual DNS answer
// uses `apply_plan_a_with_servers` (the same injectable seam as T7) against a
// loopback fake DoH, because `parse_and_validate` hard-codes
// `DEFAULT_DOH_SERVERS` and is not reachable from an integration test.

use std::ffi::CStr;
use std::io::{Read, Write};
use std::net::TcpListener;
use std::os::raw::c_char;
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Duration;

use serde_json::{json, Value};
use sni_core::config::{apply_plan_a_with_servers, migrate, validate, Endpoint};
use sni_core::net::DohServer;

/// `sni_set_config_path` writes process-global state, so the C-ABI tests
/// (which run on separate threads) take turns.
fn ffi_lock() -> &'static Mutex<()> {
    static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
    LOCK.get_or_init(|| Mutex::new(()))
}

fn ffi_guard() -> std::sync::MutexGuard<'static, ()> {
    // A panicking test must not wedge the other ones.
    ffi_lock()
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

/// Names the fake answers, with RFC 5737 documentation addresses.
const ANSWERS: &[(&str, &str)] = &[("plan-a.example", "203.0.113.10")];

struct FakeDoh {
    base: String,
    hits: Arc<AtomicUsize>,
}

impl FakeDoh {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind fake DoH");
        let addr = listener.local_addr().expect("fake DoH addr");
        let hits = Arc::new(AtomicUsize::new(0));
        let counter = Arc::clone(&hits);
        std::thread::spawn(move || {
            for stream in listener.incoming() {
                let Ok(mut stream) = stream else { continue };
                let hits = Arc::clone(&counter);
                std::thread::spawn(move || {
                    let _ = stream.set_read_timeout(Some(Duration::from_secs(5)));
                    let mut request = [0u8; 1024];
                    let read = stream.read(&mut request).unwrap_or(0);
                    hits.fetch_add(1, Ordering::SeqCst);
                    let body =
                        dns_json(&requested_name(&String::from_utf8_lossy(&request[..read])));
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

fn dns_json(name: &str) -> String {
    match ANSWERS.iter().find(|(host, _)| *host == name) {
        Some((host, ip)) => format!(
            r#"{{"Status":0,"Answer":[{{"name":"{host}","type":1,"TTL":300,"data":"{ip}"}}]}}"#
        ),
        None => r#"{"Status":3,"Authority":[]}"#.to_string(),
    }
}

fn servers(doh: &FakeDoh) -> [DohServer<'_>; 1] {
    [DohServer {
        url: doh.base.as_str(),
        pin_ip: None,
    }]
}

/// Owns one C-string return value and frees it on drop, per the ABI contract.
struct Returned(*mut c_char);

impl Returned {
    fn json(&self) -> Value {
        if self.0.is_null() {
            return Value::Null;
        }
        // SAFETY: pointer came from `CString::into_raw` in this library.
        let text = unsafe { CStr::from_ptr(self.0) }
            .to_string_lossy()
            .into_owned();
        serde_json::from_str(&text).unwrap_or_else(|e| panic!("not JSON ({e}): {text}"))
    }
}

impl Drop for Returned {
    fn drop(&mut self) {
        // Safe to call: `sni_free_string` is a safe `extern "C"` fn and is
        // null-tolerant; the pointer came from this library's into_raw.
        sni_engine::sni_free_string(self.0);
    }
}

fn to_c(text: &str) -> std::ffi::CString {
    std::ffi::CString::new(text).expect("no interior NUL in test input")
}

/// Point the engine at a private config file and return its path.
fn use_temp_config(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("sni-t8-{name}"));
    std::fs::create_dir_all(&dir).expect("temp dir");
    let path = dir.join("config.json");
    // A stale `.full.json` from an earlier run would merge into load().
    let _ = std::fs::remove_file(path.with_extension("json.full.json"));
    let c = to_c(&path.display().to_string());
    sni_engine::sni_set_config_path(c.as_ptr());
    path
}

fn save(cfg: &Value) -> Returned {
    let c = to_c(&cfg.to_string());
    Returned(sni_engine::sni_save_config(c.as_ptr()))
}

fn endpoints_of(val: &Value) -> Vec<(String, u16)> {
    val["ENDPOINTS"]
        .as_array()
        .expect("ENDPOINTS array")
        .iter()
        .map(|e| {
            (
                e["ip"].as_str().expect("ip").to_string(),
                e["port"].as_u64().expect("port") as u16,
            )
        })
        .collect()
}

#[test]
fn t8_ffi_plan_b_unchanged() {
    let _guard = ffi_guard();
    let path = use_temp_config("plan-b");
    let cfg = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45580,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 45581}],
        "FAKE_SNIS": ["example.com"],
    });
    let res = save(&cfg);
    assert_eq!(
        res.json()["ok"],
        json!(true),
        "plan B save: {:?}",
        res.json()
    );

    // The saved file is `config_to_json`'s canonical schema: the two Plan A
    // keys must be ABSENT, not merely false/empty, so a Plan B config.json
    // keeps byte-compatible key sets with the pre-Plan-A build.
    let saved: Value = serde_json::from_str(&std::fs::read_to_string(&path).expect("read back"))
        .expect("saved JSON");
    let obj = saved.as_object().expect("object");
    assert!(
        !obj.contains_key("RESOLVE_SNI"),
        "RESOLVE_SNI leaked into a plan B save: {saved}"
    );
    assert!(
        !obj.contains_key("CONNECT_OVERRIDE"),
        "CONNECT_OVERRIDE leaked into a plan B save: {saved}"
    );
    // Endpoints survive untouched — `apply_plan_a` returned at its Plan B
    // guard, so no derivation and no lookup happened.
    assert_eq!(endpoints_of(&saved), vec![("127.0.0.1".to_string(), 45581)]);
    assert_eq!(saved["LISTEN_PORT"], json!(45580));
}

#[test]
fn t8_ffi_connect_override_wins() {
    let _guard = ffi_guard();
    let path = use_temp_config("override");
    // Both Plan A fields set, plus a configured endpoint whose port must still
    // be honoured. No FAKE_SNI lookup and no DoH server is even listening in
    // this test, so a stray lookup would surface as a save error.
    let cfg = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45582,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 8443}],
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": true,
        "CONNECT_OVERRIDE": "198.51.100.7",
    });
    let res = save(&cfg);
    assert_eq!(
        res.json()["ok"],
        json!(true),
        "override save: {:?}",
        res.json()
    );

    let saved: Value = serde_json::from_str(&std::fs::read_to_string(&path).expect("read back"))
        .expect("saved JSON");
    // parse_and_validate applied Plan A before config_to_json ran.
    assert_eq!(
        endpoints_of(&saved),
        vec![("198.51.100.7".to_string(), 8443)],
        "CONNECT_OVERRIDE must replace the endpoint IP, keeping the port"
    );
    // config_to_json re-emits both opt-in keys, so the GUI round-trips them.
    assert_eq!(saved["RESOLVE_SNI"], json!(true));
    assert_eq!(saved["CONNECT_OVERRIDE"], json!("198.51.100.7"));

    // An invalid override is rejected by core's validate(), and the message
    // reaches the FFI error JSON unchanged (same text the CLI prints).
    let bad = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45582,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 8443}],
        "FAKE_SNIS": ["plan-a.example"],
        "CONNECT_OVERRIDE": "not-an-ip",
    });
    let err = save(&bad).json();
    assert_eq!(err["ok"], json!(false));
    let msg = err["error"].as_str().unwrap_or_default().to_string();
    assert!(
        msg.contains("CONNECT_OVERRIDE must be an IPv4 address"),
        "override error changed: {msg}"
    );
}

#[test]
fn t8_ffi_plan_a_resolves() {
    let doh = FakeDoh::start();
    // The RESOLVE_SNI branch of parse_and_validate calls apply_plan_a() with
    // the hardcoded DEFAULT_DOH_SERVERS. apply_plan_a now layers an OS-resolver
    // fallback and a reachability scan on top of resolution (both covered in
    // T7); apply_plan_a_with_servers is the resolution-only seam, so it drives
    // the same DoH code the GUI hits, against the loopback fake instead of
    // 1.1.1.1.
    let cfg = json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45584,
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": true,
    });
    let mut resolved = migrate(cfg.clone()).expect("plan A migrates");
    assert!(resolved.endpoints.is_empty());
    // Pre-resolution the endpoint list is legitimately empty: core's validate
    // must let it through, because parse_and_validate validates first.
    validate(&resolved).expect("plan A validates before resolution");

    apply_plan_a_with_servers(&mut resolved, &servers(&doh)).expect("plan A resolves");
    assert_eq!(
        resolved.endpoints,
        vec![Endpoint {
            ip: "203.0.113.10".to_string(),
            port: 443
        }]
    );
    validate(&resolved).expect("resolved plan A validates");
    assert!(doh.hits() >= 1, "the fake DoH must have been queried");

    // The resolved config is what the engine gets, and it still carries
    // RESOLVE_SNI — so the very JSON a GUI save would emit keeps Plan A on for
    // the next start, with the stale IP re-derived rather than trusted.
    assert_eq!(cfg["RESOLVE_SNI"], json!(true));
    let re_derived = migrate(json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45584,
        "ENDPOINTS": [{"ip": "203.0.113.10", "port": 443}],
        "FAKE_SNIS": ["plan-a.example"],
        "RESOLVE_SNI": true,
    }))
    .expect("resolved form migrates");
    assert!(re_derived.resolve_sni, "the flag must survive a round trip");
}
