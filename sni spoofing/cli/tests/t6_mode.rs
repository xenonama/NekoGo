// T6: MODE "Trojan + Xray" is the pre-audit spelling of the only mode this
// build implements. Loading migrates it to "SNI Only" so saved configs still
// start; validating a Config that was never migrated still fails fast with the
// exact message. Pure config-level tests.

#[test]
fn t6_legacy_trojan_mode_migrates_to_sni_only() {
    let v = serde_json::json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45559,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 443}],
        "FAKE_SNIS": ["example.com"],
        "MODE": "Trojan + Xray",
    });
    let cfg = sni_core::config::migrate(v)
        .and_then(|c| sni_core::config::validate(&c).map(|_| c))
        .expect("a saved Trojan + Xray config must still load");
    assert_eq!(cfg.mode, "SNI Only");
}

#[test]
fn t6_unmigrated_trojan_mode_rejected_with_exact_message() {
    let cfg = sni_core::config::Config {
        listen_port: 45559,
        endpoints: vec![sni_core::config::Endpoint {
            ip: "127.0.0.1".to_string(),
            port: 443,
        }],
        fake_snis: vec!["example.com".to_string()],
        mode: "Trojan + Xray".to_string(),
        ..sni_core::config::Config::default()
    };
    let err = sni_core::config::validate(&cfg).unwrap_err();
    assert_eq!(
        err.to_string(),
        "config validation failed: MODE \"Trojan + Xray\" is not implemented in this build (only \"SNI Only\" is supported)"
    );
}

#[test]
fn t6_sni_only_still_valid() {
    let v = serde_json::json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45559,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 443}],
        "FAKE_SNIS": ["example.com"],
        "MODE": "SNI Only",
    });
    let cfg = sni_core::config::migrate(v)
        .and_then(|c| sni_core::config::validate(&c).map(|_| c))
        .expect("SNI Only must validate");
    assert_eq!(cfg.mode, "SNI Only");
}
