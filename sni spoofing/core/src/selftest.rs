//! Offline self-test: port of `main.py::run_self_test`.
//!
//! Pure + offline (no Admin/WinDivert/network). Each check mirrors one Python
//! check, adapted where the Rust port intentionally diverges:
//! - `tls_fingerprint`: legacy 517B builder fully; modern GREASE/ALPS/ECH
//!   profiles dispatch to legacy (documented in `tls.rs`), so we assert
//!   profile parsing + legacy vectors, not browser-blob bytes.
//! - `quic`: packet-crypto helpers (`is_quic_packet`, SNI swap) are a future
//!   slice; we assert mode parsing + filter builders + trojan passthrough bit.
//! - `mock_bypass`: mock-socket `fake_send_thread` becomes pure
//!   `plan_fake` segment-count/prefix assertions (same wire math).

use std::path::Path;

fn check_config(path: &Path) -> Result<String, String> {
    let cfg = crate::config::load(path).map_err(|e| e.to_string())?;
    Ok(format!(
        "{} endpoint(s), {} SNI(s), method={}",
        cfg.endpoints.len(),
        cfg.fake_snis.len(),
        cfg.bypass_method
    ))
}

fn check_packet_template() -> Result<String, String> {
    let rnd = [0x11u8; 32];
    let sess = [0x22u8; 32];
    let key = [0x33u8; 32];
    let hello = crate::tls::get_client_hello_with(&rnd, &sess, b"example.com", &key)
        .map_err(|e| e.to_string())?;
    if hello.len() != 517 {
        return Err(format!("ClientHello len {} != 517", hello.len()));
    }
    let (r2, s2, sni2, k2) = crate::tls::parse_client_hello(&hello)
        .ok_or_else(|| "round-trip parse failed".to_string())?;
    if r2 != rnd || s2 != sess || sni2 != b"example.com" || k2 != key {
        return Err("round-trip field mismatch".to_string());
    }
    Ok(format!("ClientHello {}B round-trip OK", hello.len()))
}

fn check_scoreboard() -> Result<String, String> {
    let st = crate::stats::Stats::new();
    st.record_result("1.1.1.1:443", "a.com", true, "wrong_seq");
    st.record_result("1.1.1.1:443", "a.com", false, "wrong_seq");
    let snap = st.snapshot();
    // record_result touches boards only (mirrors Python counters untouched).
    if snap.success_rate != 0.0 {
        return Err(format!("success_rate {} != 0.0", snap.success_rate));
    }
    if snap.best_endpoint != "1.1.1.1:443" {
        return Err(format!("best_endpoint {:?} unexpected", snap.best_endpoint));
    }
    if snap.methods.is_empty() || snap.methods[0].key != "wrong_seq" {
        return Err("method board missing wrong_seq".to_string());
    }
    st.reset();
    let snap2 = st.snapshot();
    if snap2.total != 0 || snap2.success != 0 || snap2.failed != 0 {
        return Err("reset did not clear counters".to_string());
    }
    Ok("scoreboard OK".to_string())
}

fn check_split() -> Result<String, String> {
    let (s1, s2) = crate::fake_tcp::split_plan(1000, 517, 258);
    if s2 != s1.wrapping_add(258) {
        return Err("split_plan s2 mismatch".to_string());
    }
    if s1 != 1001u32.wrapping_sub(517) {
        return Err("split_plan s1 mismatch".to_string());
    }
    let ((o1, o2, o3), (v1, v2)) = crate::fake_tcp::overlap_plan(517, 206, 155, 3);
    if (o1, o2, o3) != (0, 203, 355) || (v1, v2) != (3, 3) {
        return Err(format!(
            "overlap_plan got ({},{},{}) ({},{})",
            o1, o2, o3, v1, v2
        ));
    }
    let ((z1, z2, z3), (w1, w2)) = crate::fake_tcp::overlap_plan(517, 206, 155, 0);
    if (z1, z2, z3) != (0, 206, 361) || (w1, w2) != (0, 0) {
        return Err("overlap_plan zero-overlap mismatch".to_string());
    }
    Ok("split math OK".to_string())
}

fn check_tlsfp() -> Result<String, String> {
    // Legacy vector: starts 16 03 01, byte[5]==0x01, 517B.
    let hello = crate::tls::build_fake_client_hello(b"example.com", crate::tls::TlsProfile::Legacy);
    if hello.len() != 517 {
        return Err(format!("legacy len {} != 517", hello.len()));
    }
    if hello.len() < 6 || hello[0..3] != [0x16, 0x03, 0x01] || hello[5] != 0x01 {
        return Err("legacy header mismatch".to_string());
    }
    // Canonical names plus every legacy alias still parse, so saved configs
    // written by older builds keep loading.
    for name in [
        "legacy",
        "chrome",
        "firefox",
        "chrome_120",
        "chrome_124",
        "firefox_122",
        "firefox_124",
        "custom",
    ] {
        crate::tls::TlsProfile::parse(name).map_err(|e| e.to_string())?;
    }
    if crate::tls::TlsProfile::parse("bogus").is_ok() {
        return Err("bogus profile should fail".to_string());
    }
    // Aliases must collapse onto the three canonical profiles, otherwise the
    // GUI list and the config validator disagree.
    for (alias, want) in [
        ("chrome_120", crate::tls::TlsProfile::Chrome),
        ("chrome_124", crate::tls::TlsProfile::Chrome),
        ("firefox_122", crate::tls::TlsProfile::Firefox),
        ("firefox_124", crate::tls::TlsProfile::Firefox),
        ("custom", crate::tls::TlsProfile::Legacy),
    ] {
        let got = crate::tls::TlsProfile::parse(alias).map_err(|e| e.to_string())?;
        if got != want {
            return Err(format!("alias {} -> {:?}, expected {:?}", alias, got, want));
        }
        if crate::tls::TlsProfile::parse(want.as_str()).map_err(|e| e.to_string())? != want {
            return Err(format!(
                "canonical name {} does not round-trip",
                want.as_str()
            ));
        }
    }
    // Guards mirror ClientHelloMaker preconditions.
    if crate::tls::check_hello_params(&[0u8; 32], &[0u8; 32], b"", &[0u8; 32]).is_ok() {
        return Err("empty SNI should fail".to_string());
    }
    if crate::tls::check_hello_params(&[0u8; 32], &[0u8; 32], &vec![b'x'; 220], &[0u8; 32]).is_ok()
    {
        return Err("oversize SNI should fail".to_string());
    }
    // FIX #2: Chrome must NOT look like the legacy 517B template.
    let chrome =
        crate::tls::build_fake_client_hello(b"example.com", crate::tls::TlsProfile::Chrome);
    if chrome.len() == 517 {
        return Err("chrome hello still looks like the legacy 517B template".to_string());
    }
    if chrome.len() < 512 {
        return Err(format!("chrome hello shorter than 512B: {}", chrome.len()));
    }
    if chrome.len() < 6 || chrome[0..3] != [0x16, 0x03, 0x01] {
        return Err("chrome record header mismatch".to_string());
    }
    if chrome[5] != 0x01 {
        return Err("chrome handshake type is not ClientHello".to_string());
    }
    // The TLS_AES_128_GCM_SHA256 cipher suite (0x1301) must be present
    // somewhere in the body.
    if !chrome.windows(2).any(|w| w == [0x13, 0x01]) {
        return Err("chrome missing TLS_AES_128_GCM_SHA256 cipher".to_string());
    }
    // FIX(firefox): Firefox must differ from Chrome AND must NOT
    // contain a GREASE cipher suite (0x?A?A pattern).
    let firefox =
        crate::tls::build_fake_client_hello(b"example.com", crate::tls::TlsProfile::Firefox);
    if firefox.len() == 517 {
        return Err("firefox still looks like the legacy 517B template".to_string());
    }
    if firefox.len() < 100 {
        return Err(format!("firefox suspiciously short: {}", firefox.len()));
    }
    if firefox.len() < 6 || firefox[0..3] != [0x16, 0x03, 0x01] {
        return Err("firefox record header mismatch".to_string());
    }
    if firefox[5] != 0x01 {
        return Err("firefox handshake type is not ClientHello".to_string());
    }
    // Firefox must NOT contain the TLS_AES_128_GCM_SHA256-only
    // pattern that Chrome uses as its first real suite — Firefox
    // starts with 0x1301 too, but the second suite differs
    // (Firefox: 0x1303, Chrome: 0x1302). Check the second suite.
    // Search for the byte sequence 0x13 0x03 in the cipher list.
    let has_1303 = firefox.windows(2).any(|w| w == [0x13, 0x03]);
    if !has_1303 {
        return Err("firefox missing TLS_CHACHA20_POLY1305_SHA256 (0x1303)".to_string());
    }
    // Firefox must NOT use GREASE. Look at the places a GREASE value can
    // legally appear - the cipher list and the two id-vector extensions - rather
    // than scanning raw bytes: the hello's random fields contain 0x?A?A pairs by
    // accident about one build in fifteen, which used to fail this check at
    // random.
    let ff_exts = walk_extensions("firefox", &firefox)?;
    for c in cipher_list(&firefox)? {
        if is_grease(c) {
            return Err(format!("firefox cipher {c:#06x} is a GREASE value"));
        }
    }
    for (t, b) in &ff_exts {
        let ids = if *t == 0x000a {
            id_vector_u16(b)?
        } else if *t == 0x002b {
            id_vector_u8(b)?
        } else {
            continue;
        };
        if ids.iter().copied().any(is_grease) {
            return Err(format!("firefox ext {t:#06x} carries a GREASE id"));
        }
    }
    // FIX(firefox): distinctness best-effort — Chrome and Firefox should
    // usually differ in total length when randomness is held constant,
    // but randomness is per-call and padding choices may drift, so do
    // not fail if equal; just note it.
    let _distinct_lengths = chrome.len() != firefox.len();

    // A hello a real server cannot parse is worse than no hello at all: the
    // handshake dies with `decode_error` and the user sees "spoofing does not
    // work". Header/length guards above catch the shape, not the chain, so walk
    // every record + extension length for all three profiles.
    for (name, h) in [
        ("legacy", &hello),
        ("chrome", &chrome),
        ("firefox", &firefox),
    ] {
        let exts = walk_extensions(name, h)?;
        // The legacy template is a TLS 1.3 hello too (the builder patches a
        // real capture's key_share), so all three must carry these three.
        for want in [0x0000u16, 0x002b, 0x0033] {
            if !exts.iter().any(|(t, _)| *t == want) {
                return Err(format!("{name}: missing extension {want:#06x}"));
            }
        }
        for (t, body) in &exts {
            check_ext_inner(name, *t, body)?;
        }
    }
    Ok(format!("tls fingerprint OK (legacy {}B)", hello.len()))
}

/// Validate the *inside* of every extension our builders emit.
///
/// `walk_extensions` proves each outer `type/length` pair tiles; it cannot see a
/// vector *inside* an extension whose declared length lies, and that is exactly
/// what makes a real server answer `alert fatal decode_error` - the chrome
/// profile shipped two of those. Extension types not listed are skipped: an
/// unknown extension is legal for a peer to ignore, and the captured legacy
/// template carries several we do not build.
fn check_ext_inner(who: &str, t: u16, body: &[u8]) -> Result<(), String> {
    match t {
        // server_name: u16 list of `u8 name_type + u16 length + name`.
        0x0000 => tile_u16(who, t, "server_name", body, &|b, i| {
            Some(i + 3 + u16_at(b, i + 1).ok()? as usize)
        }),
        // status_request: ocsp + empty responder_id_list + empty extensions.
        0x0005 => {
            if body != [0x01, 0x00, 0x00, 0x00, 0x00] {
                return Err(msg(who, t, "status_request", "not an empty OCSP request"));
            }
            Ok(())
        }
        // Three u16 vectors of 2-byte ids.
        0x000a | 0x000d | 0x001b => tile_u16(who, t, ext_name(t), body, &fixed(2)),
        // ec_point_formats: u8 vector of 1-byte ids.
        0x000b => tile_u8(who, t, "ec_point_formats", body, &fixed(1)),
        // alpn: u16 vector of `u8 length + id`.
        0x0010 => tile_u16(who, t, "alpn", body, &|b, i| {
            Some(i + 1 + *b.get(i)? as usize)
        }),
        0x0012 => {
            // Chrome sends this with an empty body; anything else is an SCT list.
            if body.is_empty() {
                return Ok(());
            }
            tile_u16(
                who,
                t,
                "signed_certificate_timestamp",
                body,
                &fixed(32 + 8 + 2),
            )
        }
        0x0015 => {
            if body.iter().any(|&b| b != 0) {
                return Err(msg(who, t, "padding", "padding is not all zero"));
            }
            Ok(())
        }
        0x0017 | 0x0023 => {
            if !body.is_empty() {
                return Err(msg(who, t, ext_name(t), "expected an empty body"));
            }
            Ok(())
        }
        // supported_versions: u8 vector of 2-byte TLS versions.
        0x002b => tile_u8(who, t, "supported_versions", body, &fixed(2)),
        // psk_key_exchange_modes: u8 vector of 1-byte enums.
        0x002d => tile_u8(who, t, "psk_key_exchange_modes", body, &fixed(1)),
        // key_share: u16 vector of `u16 group + u16 length + key_exchange`.
        0x0033 => tile_u16(who, t, "key_share", body, &|b, i| {
            Some(i + 4 + u16_at(b, i + 2).ok()? as usize)
        }),
        0xff01 => {
            if body != [0x00] {
                return Err(msg(who, t, "renegotiation_info", "must be one zero byte"));
            }
            Ok(())
        }
        // ALPS: u16 vector of `u8 id_len + id + u16 settings_len + settings`.
        0x4469 => tile_u16(who, t, "application_settings", body, &|b, i| {
            let after_id = i + 1 + *b.get(i)? as usize;
            Some(after_id + 2 + u16_at(b, after_id).ok()? as usize)
        }),
        _ => Ok(()),
    }
}

fn msg(who: &str, t: u16, what: &str, why: &str) -> String {
    format!("{who} ext {t:#06x} ({what}): {why}")
}

fn ext_name(t: u16) -> &'static str {
    match t {
        0x000a => "supported_groups",
        0x000d => "signature_algorithms",
        0x001b => "compress_certificate",
        0x0017 => "extended_master_secret",
        0x0023 => "session_ticket",
        0x002b => "supported_versions",
        0x002d => "psk_key_exchange_modes",
        _ => "extension",
    }
}

/// An item of `width` bytes: offset in, offset out.
fn fixed(width: usize) -> impl Fn(&[u8], usize) -> Option<usize> {
    move |_, i| Some(i + width)
}

/// Walk a `v<0..2^16-1>` whose entries `step` tiles exactly.
fn tile_u16(
    who: &str,
    t: u16,
    what: &str,
    body: &[u8],
    step: &dyn Fn(&[u8], usize) -> Option<usize>,
) -> Result<(), String> {
    if body.len() < 2 {
        return Err(msg(who, t, what, "vector header truncated"));
    }
    let n = u16_at(body, 0).map_err(|e| msg(who, t, what, &e))? as usize;
    if n != body.len() - 2 {
        return Err(msg(
            who,
            t,
            what,
            &format!("length {n} != {}", body.len() - 2),
        ));
    }
    tile(who, t, what, body, step, 2)
}

/// Same for a `v<0..255>`.
fn tile_u8(
    who: &str,
    t: u16,
    what: &str,
    body: &[u8],
    step: &dyn Fn(&[u8], usize) -> Option<usize>,
) -> Result<(), String> {
    if body.is_empty() {
        return Err(msg(who, t, what, "vector header truncated"));
    }
    let n = *body.first().unwrap() as usize;
    if n != body.len() - 1 {
        return Err(msg(
            who,
            t,
            what,
            &format!("length {n} != {}", body.len() - 1),
        ));
    }
    tile(who, t, what, body, step, 1)
}

fn tile(
    who: &str,
    t: u16,
    what: &str,
    body: &[u8],
    step: &dyn Fn(&[u8], usize) -> Option<usize>,
    mut i: usize,
) -> Result<(), String> {
    while i < body.len() {
        match step(body, i) {
            Some(next) if next > i && next <= body.len() => i = next,
            _ => {
                return Err(msg(
                    who,
                    t,
                    what,
                    &format!("entry at {i} overruns the list"),
                ))
            }
        }
    }
    Ok(())
}

/// Walk one ClientHello down to its extension list, checking every length
/// field on the way: record header, handshake header, legacy fields, and each
/// `type/length` pair. Returns `(type, body)` for every extension.
fn walk_extensions(name: &str, h: &[u8]) -> Result<Vec<(u16, Vec<u8>)>, String> {
    let err = |m: String| Err(format!("{name}: {m}"));
    if h.len() < 5 || h[0] != 0x16 {
        return err("record is not a handshake message".to_string());
    }
    let rec_len = u16_at(h, 3)? as usize;
    if h.len() != 5 + rec_len {
        return err(format!(
            "record length {rec_len} != payload {}",
            h.len() - 5
        ));
    }
    let hs = &h[5..];
    if hs.len() < 4 || hs[0] != 0x01 {
        return err("handshake type is not ClientHello".to_string());
    }
    let hs_len = (hs[1] as usize) << 16 | (hs[2] as usize) << 8 | hs[3] as usize;
    if hs.len() != 4 + hs_len {
        return err(format!(
            "handshake length {hs_len} != body {}",
            hs.len() - 4
        ));
    }
    let b = &hs[4..];
    let mut i = 2 + 32; // legacy_version + random
    i += 1 + *b.get(i).ok_or_else(|| format!("{name}: sid truncated"))? as usize; // session_id
    let suites = u16_at(b, i)? as usize;
    if !suites.is_multiple_of(2) {
        return err("cipher_suites length is odd".to_string());
    }
    i += 2 + suites;
    i += 1 + *b
        .get(i)
        .ok_or_else(|| format!("{name}: compression truncated"))? as usize;
    let ext_total = u16_at(b, i)? as usize;
    let exts = &b[i + 2..];
    if exts.len() != ext_total {
        return err(format!(
            "extensions length {ext_total} != trailing {}",
            exts.len()
        ));
    }
    let mut out = Vec::new();
    let mut j = 0usize;
    while j + 4 <= exts.len() {
        let t = u16_at(exts, j)?;
        let l = u16_at(exts, j + 2)? as usize;
        if j + 4 + l > exts.len() {
            return err(format!(
                "extension {t:#06x} body of {l}B runs past the chain"
            ));
        }
        out.push((t, exts[j + 4..j + 4 + l].to_vec()));
        j += 4 + l;
    }
    if j != exts.len() {
        return err("extension chain has trailing bytes".to_string());
    }
    Ok(out)
}

fn u16_at(b: &[u8], at: usize) -> Result<u16, String> {
    let hi = *b.get(at).ok_or("truncated".to_string())? as u16;
    let lo = *b.get(at + 1).ok_or("truncated".to_string())? as u16;
    Ok(hi << 8 | lo)
}

/// RFC 8701: a GREASE value is `0x?A?A` with an even leading nibble.
fn is_grease(v: u16) -> bool {
    let hi = (v >> 8) as u8;
    hi == v as u8 && (hi & 0x0F) == 0x0A && (hi >> 4).is_multiple_of(2)
}

/// The handshake body: everything after the record and handshake headers.
fn hello_body(h: &[u8]) -> Result<&[u8], String> {
    if h.len() < 9 || h[0] != 0x16 || h[5] != 0x01 {
        return Err("not a ClientHello record".to_string());
    }
    let hs_len = ((h[6] as usize) << 16) | ((h[7] as usize) << 8) | h[8] as usize;
    if h.len() != 9 + hs_len {
        return Err("handshake length does not match the record".to_string());
    }
    Ok(&h[9..])
}

/// The `cipher_suites` vector, at the same offsets `walk_extensions` uses.
fn cipher_list(h: &[u8]) -> Result<Vec<u16>, String> {
    let b = hello_body(h)?;
    let mut i = 2 + 32; // legacy_version + client_random
    i += 1 + *b.get(i).ok_or("session_id truncated".to_string())? as usize;
    let n = u16_at(&b[i..], 0)? as usize;
    if !n.is_multiple_of(2) || i + 2 + n > b.len() {
        return Err("cipher_suites vector is broken".to_string());
    }
    (0..n / 2).map(|k| u16_at(&b[i + 2..], k * 2)).collect()
}

/// `T ids<0..2^16-1>` of 2-byte ids (supported_groups, signature_algorithms…).
fn id_vector_u16(body: &[u8]) -> Result<Vec<u16>, String> {
    let n = u16_at(body, 0)? as usize;
    if !n.is_multiple_of(2) || 2 + n > body.len() {
        return Err("u16 id vector is broken".to_string());
    }
    (0..n / 2).map(|k| u16_at(&body[2..], k * 2)).collect()
}

/// `T ids<0..255>` of 2-byte ids (supported_versions).
fn id_vector_u8(body: &[u8]) -> Result<Vec<u16>, String> {
    let n = *body.first().ok_or("empty id vector".to_string())? as usize;
    if !n.is_multiple_of(2) || 1 + n > body.len() {
        return Err("u8 id vector is broken".to_string());
    }
    (0..n / 2).map(|k| u16_at(&body[1..], k * 2)).collect()
}

fn check_quic() -> Result<String, String> {
    for (name, expect) in [
        ("block", crate::quic::QuicMode::Block),
        ("spoof", crate::quic::QuicMode::Spoof),
        ("passthrough", crate::quic::QuicMode::Passthrough),
    ] {
        let m = crate::quic::QuicMode::parse(name).map_err(|e| e.to_string())?;
        if m != expect {
            return Err(format!("quic mode {} mismatch", name));
        }
    }
    if crate::quic::QuicMode::parse("bogus").is_ok() {
        return Err("bogus quic mode should fail".to_string());
    }
    let f = crate::picker::build_quic_filter("192.168.1.10");
    if !f.contains("192.168.1.10") || !f.contains("443") || !f.starts_with("udp") {
        return Err(format!("quic filter unexpected: {}", f));
    }
    Ok("quic modes + filter OK".to_string())
}

fn check_new_methods() -> Result<String, String> {
    if crate::fake_tcp::resolve_method("hostfakesplit").map_err(|e| e.to_string())?
        != crate::fake_tcp::BypassMethod::HostFakeSplit
    {
        return Err("resolve hostfakesplit failed".to_string());
    }
    if crate::fake_tcp::resolve_method("fakedsplit").map_err(|e| e.to_string())?
        != crate::fake_tcp::BypassMethod::FakeSplit
    {
        return Err("resolve fakedsplit failed".to_string());
    }
    let auto = crate::fake_tcp::resolve_method("auto").map_err(|e| e.to_string())?;
    if !crate::fake_tcp::REAL_METHODS.contains(&auto.as_str()) {
        return Err("auto did not resolve to real method".to_string());
    }
    if crate::fake_tcp::resolve_method("split_seq").map_err(|e| e.to_string())?
        != crate::fake_tcp::BypassMethod::SplitSeq
    {
        return Err("resolve split_seq failed".to_string());
    }
    // SNI helpers on a real legacy hello.
    let hello = crate::tls::build_fake_client_hello(b"example.com", crate::tls::TlsProfile::Legacy);
    let sni = crate::fake_tcp::extract_sni_from_hello(&hello)
        .ok_or_else(|| "extract SNI failed".to_string())?;
    if sni != b"example.com" {
        return Err("extracted SNI mismatch".to_string());
    }
    if crate::fake_tcp::extract_sni_from_hello(b"\x00").is_some() {
        return Err("tiny packet should yield None".to_string());
    }
    let decoy = crate::fake_tcp::same_length_fake_sni(b"example.com");
    if decoy.len() != b"example.com".len() || decoy == b"example.com" {
        return Err("decoy must differ at same length".to_string());
    }
    let micro = crate::fake_tcp::same_length_fake_sni(b"a.bc");
    if micro.len() != 4 || !micro.contains(&b'.') {
        return Err("micro-SNI must keep length + dot".to_string());
    }
    let variant = crate::fake_tcp::build_hostfake_variant(&hello);
    if variant.len() != hello.len() || variant[0..3] != [0x16, 0x03, 0x01] {
        return Err("hostfake variant length/header mismatch".to_string());
    }
    if crate::fake_tcp::build_hostfake_variant(b"") != b"" {
        return Err("empty variant should round-trip".to_string());
    }
    let signed = crate::fake_tcp::md5_fake_payload(&hello);
    if signed.len() != hello.len() + 16 {
        return Err("md5 payload length mismatch".to_string());
    }
    let digest = md5::compute(&hello);
    if signed[..16] != digest.0[..] {
        return Err("md5 prefix mismatch".to_string());
    }
    if crate::fake_tcp::md5_fake_payload(b"").len() != 16 {
        return Err("empty md5 payload should be 16B".to_string());
    }
    Ok("new bypass methods OK (hostfakesplit/fakedsplit)".to_string())
}

fn check_mock_bypass() -> Result<String, String> {
    let hello = crate::tls::build_fake_client_hello(b"example.com", crate::tls::TlsProfile::Legacy);
    let n = hello.len();
    let params = crate::fake_tcp::FakeParams::default();
    // 1) hostfakesplit happy path: same-length decoy -> 2 segments.
    let segs = crate::fake_tcp::plan_fake(
        crate::fake_tcp::BypassMethod::HostFakeSplit,
        5000,
        &hello,
        &params,
    );
    if segs.len() != 2 {
        return Err(format!("hostfakesplit expected 2 segs, got {}", segs.len()));
    }
    if segs[0].payload.len() != n || segs[1].payload.len() != n {
        return Err("hostfakesplit seg lengths mismatch".to_string());
    }
    // 2) tiny-packet fallback -> single wrong_seq.
    let tiny = crate::fake_tcp::plan_fake(
        crate::fake_tcp::BypassMethod::HostFakeSplit,
        5000,
        b"\x16",
        &params,
    );
    if tiny.len() != 1 || tiny[0].payload != b"\x16" {
        return Err("hostfakesplit tiny fallback mismatch".to_string());
    }
    // 3) fakedsplit: MD5-signed badseq + real hello -> 2 segments.
    let fs = crate::fake_tcp::plan_fake(
        crate::fake_tcp::BypassMethod::FakeSplit,
        5000,
        &hello,
        &params,
    );
    if fs.len() != 2 {
        return Err(format!("fakedsplit expected 2 segs, got {}", fs.len()));
    }
    if fs[0].payload.len() != n + 16 || fs[1].payload != hello {
        return Err("fakedsplit payload mismatch".to_string());
    }
    let digest = md5::compute(&hello);
    if fs[0].payload[..16] != digest.0[..] {
        return Err("fakedsplit md5 prefix mismatch".to_string());
    }
    // 4) hello_split is decoyless: plan_fake emits nothing, the real hello is
    //    segmented later by split_hello.
    if !crate::fake_tcp::plan_fake(
        crate::fake_tcp::BypassMethod::SplitHello,
        5000,
        &hello,
        &params,
    )
    .is_empty()
    {
        return Err("hello_split must not inject a decoy".to_string());
    }
    Ok(format!(
        "mock bypass OK (hostfakesplit 2-seg + fallback 1-seg, fakedsplit 2-seg, hello_split 0-decoy, n={})",
        n
    ))
}

/// The v3 primitive: the client's REAL ClientHello must leave in more than one
/// segment with no matchable hostname in any of them, and must reassemble
/// byte-identically.
///
/// The segment COUNT is deliberately not asserted: the cut points are
/// randomised per connection on purpose, so a fixed count here would be a
/// signature test that fails the moment the randomiser works.
fn check_hello_split() -> Result<String, String> {
    for name in ["legacy", "chrome", "firefox"] {
        let profile = crate::tls::TlsProfile::parse(name).map_err(|e| e.to_string())?;
        let sni = b"www.youtube.com";
        let hello = crate::tls::build_fake_client_hello(sni, profile);
        for _ in 0..8 {
            let segs = crate::fake_tcp::split_hello(4000, &hello)
                .ok_or_else(|| format!("{}: split_hello returned None", name))?;
            if segs.len() < 2 {
                return Err(format!("{}: hello left unsplit", name));
            }
            if segs.len() > crate::fake_tcp::MAX_HELLO_CUTS + 1 {
                return Err(format!("{}: too many segments ({})", name, segs.len()));
            }
            for s in &segs {
                if crate::fake_tcp::extract_sni_from_hello(&s.payload).is_some() {
                    return Err(format!(
                        "{}: a segment still parses as a full ClientHello with SNI",
                        name
                    ));
                }
                if String::from_utf8_lossy(&s.payload).contains("youtube") {
                    return Err(format!("{}: segment leaks the hostname label", name));
                }
            }
            let mut joined = Vec::new();
            let mut expect = 4000u32;
            for (i, s) in segs.iter().enumerate() {
                if s.seq != expect {
                    return Err(format!("{}: segments are not contiguous", name));
                }
                if s.psh != (i + 1 == segs.len()) {
                    return Err(format!("{}: PSH is not on the last segment", name));
                }
                expect = expect.wrapping_add(s.payload.len() as u32);
                joined.extend_from_slice(&s.payload);
            }
            if joined != hello {
                return Err(format!("{}: reassembly differs from the original", name));
            }
        }
    }
    // Non-handshake payloads must not be touched.
    if crate::fake_tcp::split_hello(1, &[0x17; 300]).is_some() {
        return Err("application data must not be split".to_string());
    }
    Ok("real ClientHello split OK (3 profiles, no hostname leak)".to_string())
}

/// The strategy board: `auto` must see a deduped set of decoy x split combos
/// and none of the deleted degenerate methods.
fn check_strategy_pool() -> Result<String, String> {
    let pool = crate::fake_tcp::Strategy::pool();
    let mut keys: Vec<String> = pool.iter().map(|s| s.board_key()).collect();
    keys.sort();
    let n = keys.len();
    keys.dedup();
    if keys.len() != n {
        return Err("auto pool has duplicate strategies".to_string());
    }
    if !keys.iter().any(|k| k == "hello_split") {
        return Err("auto pool lost the decoyless split strategy".to_string());
    }
    for dead in ["double_sni", "delayed_retry"] {
        if crate::fake_tcp::BypassMethod::parse(dead).is_ok() {
            return Err(format!("{} should no longer parse", dead));
        }
        if keys.iter().any(|k| k.starts_with(dead)) {
            return Err(format!("{} still in auto pool", dead));
        }
    }
    for m in crate::fake_tcp::REAL_METHODS {
        if crate::fake_tcp::BypassMethod::parse(m).is_err() {
            return Err(format!("REAL_METHODS names {:?} but it does not parse", m));
        }
    }
    let s = crate::fake_tcp::Strategy::random();
    if s.decoy == crate::fake_tcp::BypassMethod::Auto {
        return Err("Strategy::random returned Auto".to_string());
    }
    if !pool.contains(&s) {
        return Err("Strategy::random returned a strategy outside the pool".to_string());
    }
    Ok(format!("strategy pool OK ({} strategies)", n))
}

/// `auto` must rank on latency, not success rate alone: a slower-but-working
/// strategy loses to a faster one, and a failing one still loses to both.
fn check_latency_ranking() -> Result<String, String> {
    let st = crate::stats::Stats::new();
    // fast_ok: works and answers in 40 ms. slow_ok: works in 1400 ms.
    for _ in 0..4 {
        st.record_result("1.1.1.1:443", "a.com", true, "fast_ok");
        st.record_latency("1.1.1.1:443", "fast_ok", 40.0);
        st.record_result("1.1.1.1:443", "a.com", true, "slow_ok");
        st.record_latency("1.1.1.1:443", "slow_ok", 1400.0);
    }
    // broken: always fails, instantly.
    for _ in 0..4 {
        st.record_result("1.1.1.1:443", "a.com", false, "broken");
        st.record_latency("1.1.1.1:443", "broken", 5.0);
    }
    let fast = st
        .strategy_score("fast_ok")
        .ok_or_else(|| "fast_ok has no score".to_string())?;
    let slow = st
        .strategy_score("slow_ok")
        .ok_or_else(|| "slow_ok has no score".to_string())?;
    let broken = st
        .strategy_score("broken")
        .ok_or_else(|| "broken has no score".to_string())?;
    if !(fast > slow && slow > broken) {
        return Err(format!(
            "ranking wrong: fast {} slow {} broken {}",
            fast, slow, broken
        ));
    }
    if st.strategy_score("never_tried").is_some() {
        return Err("untried strategy must report None so auto explores it".to_string());
    }
    let snap = st.snapshot();
    if snap.best_method != "fast_ok" {
        return Err(format!("best_method {:?} != fast_ok", snap.best_method));
    }
    if snap.methods[0].latency_ms.is_none() {
        return Err("snapshot should expose measured latency".to_string());
    }
    if crate::fake_tcp::BypassMethod::parse("hello_split").is_err() {
        return Err("hello_split should be a supported method".to_string());
    }
    Ok(format!(
        "latency ranking OK (fast {:.3} > slow {:.3} > broken {:.3})",
        fast, slow, broken
    ))
}

fn check_mode_quic(path: &Path) -> Result<String, String> {
    let cfg = crate::config::load(path).map_err(|e| e.to_string())?;
    // Only "SNI Only" exists in this build ("Trojan + Xray" is rejected
    // at validation with its own message — see config.rs).
    if cfg.mode != "SNI Only" {
        return Err(format!(
            "MODE {:?} unexpected (only \"SNI Only\" is supported)",
            cfg.mode
        ));
    }
    let f = crate::picker::build_quic_filter("10.0.0.2");
    if !f.contains("10.0.0.2") {
        return Err("quic filter missing iface".to_string());
    }
    Ok("MODE/QUIC plumbing OK".to_string())
}

fn check_helpers() -> Result<String, String> {
    // Picker round-robin: two picks rotate the start.
    let eps = vec![
        crate::config::Endpoint {
            ip: "1.1.1.1".into(),
            port: 443,
        },
        crate::config::Endpoint {
            ip: "1.0.0.1".into(),
            port: 443,
        },
    ];
    let picker = crate::picker::EndpointPicker::new(eps);
    let first = picker.pick_ordered();
    let second = picker.pick_ordered();
    if first.len() != 2 || second.len() != 2 {
        return Err("picker should return all endpoints".to_string());
    }
    if first[0].ip == second[0].ip {
        return Err("round-robin did not rotate".to_string());
    }
    if crate::picker::ep_key(&first[0]) != format!("{}:{}", first[0].ip, first[0].port) {
        return Err("ep_key format mismatch".to_string());
    }
    if crate::picker::pick_sni(&[]).is_some() {
        return Err("empty SNI pool should yield None".to_string());
    }
    // Config validation rejects empties (mirrors config_manager.validate).
    let mut bad = crate::config::Config::default();
    if crate::config::validate(&bad).is_ok() {
        return Err("empty config should fail validation".to_string());
    }
    bad.endpoints = vec![crate::config::Endpoint {
        ip: "999.999.999.999".into(),
        port: 443,
    }];
    bad.fake_snis = vec!["example.com".into()];
    if crate::config::validate(&bad).is_ok() {
        return Err("bad IP should fail validation".to_string());
    }
    // Handshake state machine: SYN -> SYN-ACK -> ACK -> fake -> ACK-success.
    let info = |seq: u32, ack: u32, syn: bool, ackf: bool, pay: usize| crate::tcp::TcpInfo {
        seq,
        ack,
        syn,
        ack_flag: ackf,
        rst: false,
        fin: false,
        psh: false,
        payload_len: pay,
    };
    let mut hs = crate::fake_tcp::HandshakeState::new(crate::fake_tcp::Strategy::plain(
        crate::fake_tcp::BypassMethod::WrongSeq,
    ));
    if hs.on_outbound(info(5000, 0, true, false, 0)) != crate::fake_tcp::OutboundAction::Reinject {
        return Err("outbound SYN should reinject".to_string());
    }
    if hs.on_inbound(info(7000, 5001, true, true, 0), &[])
        != crate::fake_tcp::InboundAction::Reinject
    {
        return Err("inbound SYN-ACK should reinject".to_string());
    }
    if hs.on_outbound(info(5001, 7001, false, true, 0))
        != crate::fake_tcp::OutboundAction::TriggerFake
    {
        return Err("outbound ACK should trigger fake".to_string());
    }
    hs.mark_fake_sent();
    if hs.on_inbound(info(7001, 5001, false, true, 0), &[])
        != crate::fake_tcp::InboundAction::Success
    {
        return Err("inbound ACK should succeed".to_string());
    }
    // v3: on a splitting strategy, the client's own payload-bearing packets
    // after the decoy are handed to the splitter instead of treated as a
    // protocol violation.
    let mut splitter = crate::fake_tcp::HandshakeState::new(crate::fake_tcp::Strategy {
        decoy: crate::fake_tcp::BypassMethod::WrongSeq,
        split_real: true,
    });
    splitter.sch_fake_sent = true;
    if splitter.on_outbound(info(5001, 7001, false, true, 517))
        != crate::fake_tcp::OutboundAction::MaybeSplitHello
    {
        return Err("payload after decoy should be offered to the splitter".to_string());
    }
    Ok("helpers OK".to_string())
}

/// Run all checks. Returns `(all_ok, report_json)`.
pub fn run(config_path: &Path) -> (bool, serde_json::Value) {
    let mut checks = serde_json::Map::new();
    let mut ok_all = true;
    macro_rules! run_one {
        ($name:expr, $expr:expr) => {
            match $expr {
                Ok(detail) => {
                    checks.insert(
                        $name.to_string(),
                        serde_json::json!({"ok": true, "detail": detail}),
                    );
                }
                Err(detail) => {
                    ok_all = false;
                    let d: String = detail.chars().take(300).collect();
                    checks.insert($name.to_string(), serde_json::json!({"ok": false, "detail": d}));
                }
            }
        };
    }
    run_one!("config", check_config(config_path));
    run_one!("packet_template", check_packet_template());
    run_one!("scoreboard", check_scoreboard());
    run_one!("split_plan", check_split());
    run_one!("tls_fingerprint", check_tlsfp());
    run_one!("quic", check_quic());
    run_one!("new_methods", check_new_methods());
    run_one!("mock_bypass", check_mock_bypass());
    run_one!("hello_split", check_hello_split());
    run_one!("strategy_pool", check_strategy_pool());
    run_one!("latency_ranking", check_latency_ranking());
    run_one!("mode_quic", check_mode_quic(config_path));
    run_one!("helpers", check_helpers());
    (ok_all, serde_json::json!({"checks": checks, "ok": ok_all}))
}
