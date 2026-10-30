// T6: MODE = "Trojan + Xray" is rejected at validation with the exact
// message (mode removed from this build). Pure config-level test.

#[test]
fn t6_trojan_mode_rejected_with_exact_message() {
    let v = serde_json::json!({
        "LISTEN_HOST": "127.0.0.1",
        "LISTEN_PORT": 45559,
        "ENDPOINTS": [{"ip": "127.0.0.1", "port": 443}],
        "FAKE_SNIS": ["example.com"],
        "MODE": "Trojan + Xray",
    });
    let err = sni_core::config::migrate(v)
        .and_then(|c| sni_core::config::validate(&c).map(|_| c))
        .unwrap_err();
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
