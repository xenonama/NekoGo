// T9: the v3 strategy model at the config boundary.
//
// Two bypass methods were deleted (`double_sni` emitted a burst byte-identical
// to `wrong_seq`, `delayed_retry` added a fixed 1.5 s injection) and one was
// added (`hello_split`, which segments the client's real ClientHello instead of
// feeding DPI a decoy). Four TLS fingerprint spellings collapsed onto the three
// builders that actually produce different bytes.
//
// A saved config must keep working through that change: the deleted names
// migrate to `auto` with a warning instead of aborting startup, and the retired
// fingerprint names canonicalize.

use sni_core::config::{migrate, validate};

fn base(method: &str) -> serde_json::Value {
    serde_json::json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45559,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 443}],
        "FAKE_SNIS": ["example.com"],
        "BYPASS_METHOD": method,
    })
}

fn load(v: serde_json::Value) -> Result<sni_core::config::Config, sni_core::config::ConfigError> {
    migrate(v).and_then(|c| validate(&c).map(|_| c))
}

#[test]
fn t9_deleted_methods_migrate_to_auto_instead_of_failing() {
    for dead in ["double_sni", "delayed_retry"] {
        let cfg = load(base(dead)).unwrap_or_else(|e| panic!("{} must load, got {}", dead, e));
        assert_eq!(cfg.bypass_method, "auto", "{} should migrate to auto", dead);
    }
}

#[test]
fn t9_hello_split_is_accepted_and_maps_to_a_decoyless_strategy() {
    let cfg = load(base("hello_split")).expect("hello_split must validate");
    assert_eq!(cfg.bypass_method, "hello_split");
    let m = sni_core::fake_tcp::BypassMethod::parse(&cfg.bypass_method).unwrap();
    let s = m.into_strategy();
    assert!(s.split_real, "hello_split must segment the real hello");
    assert!(!s.decoy.plan_decoy(), "hello_split must not inject a decoy");
    assert_eq!(s.board_key(), "hello_split");
}

#[test]
fn t9_supported_method_names_are_exactly_the_v3_set() {
    // The GUI list, the validator and the planner read from one source; this
    // pins that source so a rename cannot silently drift.
    assert_eq!(
        sni_core::config::SUPPORTED_METHODS,
        sni_core::fake_tcp::SUPPORTED_METHODS
    );
    let expected = [
        "auto",
        "wrong_seq",
        "wrong_seq_ttl",
        "split_seq",
        "fragmented",
        "padding",
        "hello_split",
        "hostfakesplit",
        "fakedsplit",
    ];
    assert_eq!(sni_core::fake_tcp::SUPPORTED_METHODS, &expected);
    // Every accepted name parses, and nothing else does.
    for name in expected {
        sni_core::fake_tcp::BypassMethod::parse(name)
            .unwrap_or_else(|e| panic!("{} rejected: {}", name, e));
        assert!(load(base(name)).is_ok(), "config must accept {}", name);
    }
    for gone in ["double_sni", "delayed_retry", "bogus_seq", ""] {
        assert!(
            sni_core::fake_tcp::BypassMethod::parse(gone).is_err(),
            "{} must not parse",
            gone
        );
    }
    // A name that is neither current nor migratable still fails validation.
    let err = load(base("teleport_sni")).unwrap_err();
    assert!(
        err.to_string().contains("BYPASS_METHOD"),
        "unexpected error: {}",
        err
    );
}

#[test]
fn t9_retired_fingerprint_spellings_canonicalize() {
    for (alias, want) in [
        ("chrome_120", "chrome"),
        ("chrome_124", "chrome"),
        ("firefox_122", "firefox"),
        ("firefox_124", "firefox"),
        ("custom", "legacy"),
        ("chrome", "chrome"),
    ] {
        let mut v = base("auto");
        v["TLS_FINGERPRINT"] = serde_json::Value::String(alias.to_string());
        let cfg = load(v).unwrap_or_else(|e| panic!("{} must load: {}", alias, e));
        assert_eq!(
            cfg.tls_fingerprint, want,
            "{} must canonicalize to {}",
            alias, want
        );
    }
    // The validator only admits the three canonical names.
    assert_eq!(
        sni_core::config::SUPPORTED_FINGERPRINTS,
        &["legacy", "chrome", "firefox"]
    );
    let mut v = base("auto");
    v["TLS_FINGERPRINT"] = serde_json::Value::String("safari_17".to_string());
    let err = load(v).unwrap_err();
    assert!(
        err.to_string().contains("TLS_FINGERPRINT"),
        "unexpected error: {}",
        err
    );
}

#[test]
fn t9_auto_covers_every_surviving_shape() {
    // Losing two methods must not shrink what auto tries: the pool is decoy x
    // split_real plus the decoyless split, so every surviving method still
    // appears in at least one strategy.
    let pool = sni_core::fake_tcp::Strategy::pool();
    for &m in sni_core::fake_tcp::DECOY_METHODS {
        assert!(
            pool.iter().any(|s| s.decoy == m),
            "{:?} missing from the auto pool",
            m
        );
    }
    assert!(pool.iter().any(|s| !s.decoy.plan_decoy() && s.split_real));
    // 7 decoys x {split, no-split} + the decoyless split.
    assert_eq!(pool.len(), 15);
    let mut keys: Vec<String> = pool.iter().map(|s| s.board_key()).collect();
    keys.sort();
    let n = keys.len();
    keys.dedup();
    assert_eq!(keys.len(), n, "auto pool has duplicate strategies");
}

/// The offline self-test is what the GUI's "Self test" button runs, so the new
/// checks have to pass end-to-end against a real config file — not just in
/// unit tests.
#[test]
fn t9_selftest_passes_with_the_v3_checks() {
    let dir = std::env::temp_dir();
    let path = dir.join(format!("sni_t9_selftest_{}.json", std::process::id()));
    let cfg = serde_json::json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45561,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 443}],
        "FAKE_SNIS": ["example.com", "fastly.net"],
        "MODE": "SNI Only",
        "BYPASS_METHOD": "auto",
        "TLS_FINGERPRINT": "chrome",
        "QUIC_MODE": "block",
    });
    std::fs::write(&path, serde_json::to_vec_pretty(&cfg).unwrap()).expect("write temp config");
    let (ok, report) = sni_core::selftest::run(&path);
    let _ = std::fs::remove_file(&path);
    let checks = report
        .get("checks")
        .and_then(|c| c.as_object())
        .cloned()
        .unwrap_or_default();
    let mut failed: Vec<String> = checks
        .iter()
        .filter(|(_, v)| v.get("ok").and_then(|b| b.as_bool()) != Some(true))
        .map(|(k, v)| {
            format!(
                "{}: {}",
                k,
                v.get("detail").and_then(|d| d.as_str()).unwrap_or("?")
            )
        })
        .collect();
    failed.sort();
    assert!(ok, "selftest failed: {:#?}", failed);
    assert_eq!(failed, Vec::<String>::new());
    // The three v3 checks must actually be in the report, not silently skipped.
    for name in ["hello_split", "strategy_pool", "latency_ranking"] {
        assert!(
            checks.contains_key(name),
            "selftest is missing check {}",
            name
        );
    }
}
