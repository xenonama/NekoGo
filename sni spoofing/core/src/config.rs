//! Config schema: replaces `utils/config_manager.py` + `main.py::load_config`.
//!
//! Python `migrate()` = fill defaults + derive ENDPOINTS/FAKE_SNIS from
//! legacy single values. Python `validate()` = range/shape checks.
//! Rust splits this into typed `Config` + `ConfigError` (no exceptions).

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::path::Path;
use std::time::Duration;

pub const MAX_ENDPOINTS: usize = 64;
pub const MAX_SNIS: usize = 200;
pub const MAX_CONFIG_BYTES: u64 = 256 * 1024;
/// Plan A resolves at most this many SNIs per load: each lookup can burn a
/// couple of seconds when DoH is blocked.
pub const MAX_RESOLVED_ENDPOINTS: usize = 8;
/// Per-candidate TCP connect budget for the Plan A reachability scan. Kept
/// short so a black-holed answer cannot stall config load; the scan runs one
/// thread per candidate, so the wall cost is roughly this value, not N×.
pub const PLAN_A_SCAN_TIMEOUT: Duration = Duration::from_millis(1500);

/// Accepted `BYPASS_METHOD` values. One source of truth: `fake_tcp` owns the
/// strategy model, config only validates the string the user wrote.
pub const SUPPORTED_METHODS: &[&str] = crate::fake_tcp::SUPPORTED_METHODS;

pub const SUPPORTED_FINGERPRINTS: &[&str] = &["legacy", "chrome", "firefox"];

pub const SUPPORTED_QUIC_MODES: &[&str] = &["block", "spoof", "passthrough"];
pub const SUPPORTED_MODES: &[&str] = &["SNI Only"];
// FIX #8: address family selection modes.
pub const SUPPORTED_IP_MODES: &[&str] = &["ipv4", "ipv6", "both"];

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Endpoint {
    pub ip: String,
    pub port: u16,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Config {
    pub listen_host: String,
    pub listen_port: u16,
    pub endpoints: Vec<Endpoint>,
    pub fake_snis: Vec<String>,
    pub bypass_method: String,
    pub handshake_timeout: f64,
    pub max_connections: usize,
    pub fake_delay: f64,
    pub seq_overlap: u8,
    pub tls_fingerprint: String,
    pub padding_size: u8,
    pub quic_mode: String,
    pub mode: String,
    /// FIX #8: address family selection.
    #[serde(default = "default_ip_mode")]
    pub ip_mode: String,
    /// Plan A: resolve `fake_snis` through DoH and use the answer as the
    /// connect address. Configured endpoints stay in the file but are ignored
    /// for this run, so clearing the flag restores Plan B.
    #[serde(default)]
    pub resolve_sni: bool,
    /// Connect address that wins over both the configured endpoints and DoH.
    #[serde(default)]
    pub connect_override: String,
    // FIX(#3): tray on/off toggle (takes effect on next launch).
    #[serde(default = "default_tray_enabled")]
    pub tray_enabled: bool,
    // GUI extras (live in config.json.full.json on Python side).
    #[serde(default = "default_probe_tries")]
    pub probe_tries: u32,
    #[serde(default = "default_probe_timeout")]
    pub probe_timeout: f64,
    #[serde(default = "default_socks_port")]
    pub socks5_port: u16,
    #[serde(default = "default_http_port")]
    pub http_port: u16,
}

fn default_probe_tries() -> u32 {
    2
}
fn default_probe_timeout() -> f64 {
    3.0
}
fn default_socks_port() -> u16 {
    10808
}
fn default_http_port() -> u16 {
    10809
}
// FIX #8: default address family is IPv4-only bypass.
fn default_ip_mode() -> String {
    "ipv4".to_string()
}
// FIX(#3): tray defaults to on.
fn default_tray_enabled() -> bool {
    true
}

impl Default for Config {
    fn default() -> Self {
        Self {
            // FIX(WP0.1): loopback-only by default; the user can still type
            // "0.0.0.0" to expose the relay on LAN (validation allows it).
            listen_host: "127.0.0.1".to_string(),
            listen_port: 40443,
            endpoints: vec![],
            fake_snis: vec![],
            bypass_method: "auto".to_string(),
            handshake_timeout: 2.0,
            max_connections: 200,
            fake_delay: 0.001,
            seq_overlap: 3,
            tls_fingerprint: "legacy".to_string(),
            padding_size: 0,
            quic_mode: "block".to_string(),
            mode: "SNI Only".to_string(),
            // FIX #8: default to IPv4 bypass + IPv6 drop.
            ip_mode: "ipv4".to_string(),
            // Plan B is the default: no DoH, no override.
            resolve_sni: false,
            connect_override: String::new(),
            // FIX(#3): tray on by default.
            tray_enabled: true,
            probe_tries: 2,
            probe_timeout: 3.0,
            socks5_port: 10808,
            http_port: 10809,
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("cannot read config {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("invalid config file: {0}")]
    Parse(String),
    #[error("config validation failed: {0}")]
    Validation(String),
}

/// Load `config.json` + merge `config.json.full.json` (Python parity).
///
/// Rules (mirrors `main.py::load_config` + `config_manager::load`):
/// - `config.json` wins; `.full.json` only fills missing keys (e.g. MODE).
/// - Size-capped read (MAX_CONFIG_BYTES). Root must be a JSON object.
/// - Legacy `CONNECT_IP`/`CONNECT_PORT` and `FAKE_SNI` derive the lists.
/// - `RESOLVE_SNI`/`CONNECT_OVERRIDE` derive them instead (Plan A).
pub fn load(path: &Path) -> Result<Config, ConfigError> {
    let raw = read_capped_json(path)?;
    let merged = merge_full_json(path, raw);
    let mut cfg = migrate(merged)?;
    apply_plan_a(&mut cfg)?;
    validate(&cfg)?;
    Ok(cfg)
}

fn read_capped_json(path: &Path) -> Result<serde_json::Value, ConfigError> {
    let meta = std::fs::metadata(path).map_err(|e| ConfigError::Io {
        path: path.display().to_string(),
        source: e,
    })?;
    if meta.len() > MAX_CONFIG_BYTES {
        return Err(ConfigError::Parse(format!(
            "config larger than {} bytes",
            MAX_CONFIG_BYTES
        )));
    }
    let text = std::fs::read_to_string(path).map_err(|e| ConfigError::Io {
        path: path.display().to_string(),
        source: e,
    })?;
    let v: serde_json::Value =
        serde_json::from_str(&text).map_err(|e| ConfigError::Parse(e.to_string()))?;
    if !v.is_object() {
        return Err(ConfigError::Parse(
            "config root must be a JSON object".to_string(),
        ));
    }
    Ok(v)
}

fn merge_full_json(path: &Path, mut base: serde_json::Value) -> serde_json::Value {
    // `<path>.full.json` — same convention as Python (`path + ".full.json"`).
    let full_path = format!("{}.full.json", path.display());
    let text = match std::fs::read_to_string(&full_path) {
        Ok(t) => t,
        Err(_) => return base,
    };
    let extra: serde_json::Value = match serde_json::from_str(&text) {
        Ok(v) => v,
        Err(_) => return base,
    };
    let base_obj = match base.as_object_mut() {
        Some(o) => o,
        None => return base,
    };
    if let Some(extra_obj) = extra.as_object() {
        for (k, v) in extra_obj {
            base_obj.entry(k.clone()).or_insert_with(|| v.clone());
        }
    }
    base
}

/// `double_sni` and `delayed_retry` were removed from the strategy model:
/// the first emitted a burst byte-identical to `wrong_seq`, the second added a
/// fixed 1.5 s injection. Both are worth more to `auto` as nothing at all, so
/// a saved config that still names one runs `auto` instead of failing at
/// startup.
fn canonical_method(name: &str) -> String {
    let trimmed = name.trim();
    match trimmed {
        "double_sni" | "delayed_retry" => {
            tracing::warn!(
                "BYPASS_METHOD {:?} was removed from this build; using \"auto\" \
                 (which now ranks real-hello splitting too)",
                trimmed
            );
            "auto".to_string()
        }
        other => other.to_string(),
    }
}

/// `"Trojan + Xray"` was the pre-audit spelling of the only mode this build
/// implements — no Xray or Trojan transport has ever existed here, so the flag
/// never changed behaviour, only the config's name for it. A saved config that
/// still carries it runs SNI-only instead of failing at startup, which is what
/// keeps installs written before the removal working. `validate()` still rejects
/// the raw spelling for callers that check a `Config` without migrating.
fn canonical_mode(name: &str) -> String {
    let trimmed = name.trim();
    if trimmed.eq_ignore_ascii_case("Trojan + Xray") {
        tracing::warn!(
            "MODE {:?} was removed from this build; using \"SNI Only\" \
             (no Xray/Trojan transport is implemented here)",
            trimmed
        );
        return "SNI Only".to_string();
    }
    trimmed.to_string()
}

/// Fill defaults + normalize legacy keys. Mirrors `config_manager.migrate()`.
pub fn migrate(v: serde_json::Value) -> Result<Config, ConfigError> {
    let mut cfg = Config::default();
    let obj = v.as_object().cloned().unwrap_or_default();

    let get_str = |key: &str| -> Option<String> {
        obj.get(key)
            .and_then(|x| x.as_str().map(|s| s.trim().to_string()))
            .filter(|s| !s.is_empty())
    };
    let get_u16 = |key: &str| -> Option<u16> {
        obj.get(key).and_then(|x| {
            x.as_u64()
                .and_then(|n| u16::try_from(n).ok())
                .or_else(|| x.as_str()?.trim().parse::<u16>().ok())
        })
    };
    let get_f64 = |key: &str| -> Option<f64> {
        obj.get(key).and_then(|x| {
            x.as_f64().or_else(|| {
                x.as_str()
                    .and_then(|s| s.trim().parse::<f64>().ok())
                    .or_else(|| x.as_u64().map(|n| n as f64))
            })
        })
    };

    if let Some(s) = get_str("LISTEN_HOST") {
        cfg.listen_host = s;
    }
    if let Some(p) = get_u16("LISTEN_PORT") {
        cfg.listen_port = p;
    }
    // Endpoints: ENDPOINTS[] else CONNECT_IP/CONNECT_PORT fallback.
    let mut endpoints: Vec<Endpoint> = vec![];
    let mut seen: HashSet<(String, u16)> = HashSet::new();
    if let Some(arr) = obj.get("ENDPOINTS").and_then(|x| x.as_array()) {
        let default_port = get_u16("CONNECT_PORT").unwrap_or(443);
        for e in arr {
            let (ip, port) = if let Some(o) = e.as_object() {
                let ip = o
                    .get("ip")
                    .and_then(|x| x.as_str())
                    .unwrap_or("")
                    .trim()
                    .to_string();
                let port = o
                    .get("port")
                    .and_then(|x| {
                        x.as_u64()
                            .and_then(|n| u16::try_from(n).ok())
                            .or_else(|| x.as_str()?.trim().parse::<u16>().ok())
                    })
                    .unwrap_or(default_port);
                (ip, port)
            } else if let Some(s) = e.as_str() {
                // Tolerate "ip:port" strings (backend compat).
                let s = s.trim();
                if let Some((a, b)) = s.rsplit_once(':') {
                    if !a.contains('/') {
                        match b.trim().parse::<u16>() {
                            Ok(p) => (a.trim().to_string(), p),
                            Err(_) => (s.to_string(), default_port),
                        }
                    } else {
                        (s.to_string(), default_port)
                    }
                } else {
                    (s.to_string(), default_port)
                }
            } else {
                continue;
            };
            if ip.is_empty() || !seen.insert((ip.clone(), port)) {
                continue;
            }
            endpoints.push(Endpoint { ip, port });
        }
    }
    if endpoints.is_empty() {
        if let Some(ip) = get_str("CONNECT_IP") {
            let port = get_u16("CONNECT_PORT").unwrap_or(443);
            endpoints.push(Endpoint { ip, port });
        }
    }
    cfg.endpoints = endpoints;

    // SNIs: FAKE_SNIS[] else FAKE_SNI fallback. Lowercase, strip trailing dot.
    let mut snis: Vec<String> = vec![];
    let mut seen_sni: HashSet<String> = HashSet::new();
    if let Some(arr) = obj.get("FAKE_SNIS").and_then(|x| x.as_array()) {
        for s in arr {
            if let Some(raw) = s.as_str() {
                let mut norm = raw.trim().to_lowercase();
                if norm.ends_with('.') {
                    norm.pop();
                }
                if !norm.is_empty() && seen_sni.insert(norm.clone()) {
                    snis.push(norm);
                }
            }
        }
    }
    if snis.is_empty() {
        if let Some(s) = get_str("FAKE_SNI").or_else(|| get_str("FAKE_SNIS")) {
            let mut norm = s.trim().to_lowercase();
            if norm.ends_with('.') {
                norm.pop();
            }
            if !norm.is_empty() {
                snis.push(norm);
            }
        }
    }
    cfg.fake_snis = snis;

    if let Some(m) = get_str("BYPASS_METHOD") {
        cfg.bypass_method = canonical_method(&m);
    }
    if let Some(t) = get_f64("HANDSHAKE_TIMEOUT") {
        cfg.handshake_timeout = t;
    }
    if let Some(m) = obj.get("MAX_CONNECTIONS").and_then(|x| {
        x.as_u64()
            .map(|n| n as usize)
            .or_else(|| x.as_str()?.trim().parse::<usize>().ok())
    }) {
        cfg.max_connections = m;
    }
    if let Some(d) = get_f64("FAKE_DELAY") {
        cfg.fake_delay = d;
    }
    if let Some(o) = obj.get("SEQ_OVERLAP").and_then(|x| {
        x.as_u64()
            .and_then(|n| u8::try_from(n).ok())
            .or_else(|| x.as_str()?.trim().parse::<u8>().ok())
    }) {
        // FIX: don't coerce here; let validate() reject >16 so bad input
        // surfaces instead of silently clamping (previously unreachable check).
        cfg.seq_overlap = o;
    }
    if let Some(f) = get_str("TLS_FINGERPRINT") {
        // Canonicalized through the builder enum so the retired per-version
        // spellings collapse to the three shapes that actually exist.
        cfg.tls_fingerprint = crate::tls::TlsProfile::parse(&f)
            .map(|p| p.as_str().to_string())
            .unwrap_or_else(|_| f.to_lowercase());
    }
    if let Some(p) = obj.get("PADDING_SIZE").and_then(|x| {
        x.as_u64()
            .and_then(|n| u8::try_from(n).ok())
            .or_else(|| x.as_str()?.trim().parse::<u8>().ok())
    }) {
        cfg.padding_size = p;
    }
    if let Some(q) = get_str("QUIC_MODE") {
        cfg.quic_mode = q.to_lowercase();
    }
    if let Some(m) = get_str("MODE") {
        cfg.mode = canonical_mode(&m);
    }
    // FIX #8: address family selection (IP_MODE / ip_mode).
    if let Some(m) = get_str("IP_MODE").or_else(|| get_str("ip_mode")) {
        cfg.ip_mode = m.to_lowercase();
    }
    // Plan A: RESOLVE_SNI turns fake_snis into the connect endpoints, and
    // CONNECT_OVERRIDE pins them. Absent in every Plan B config.
    if let Some(v) = obj.get("RESOLVE_SNI").or_else(|| obj.get("resolve_sni")) {
        cfg.resolve_sni = value_is_true(v);
    }
    if let Some(s) = get_str("CONNECT_OVERRIDE").or_else(|| get_str("connect_override")) {
        cfg.connect_override = s;
    }
    // FIX(tray-rewrite): tray on/off toggle with tolerant coercion.
    // Accepts bool, "true/false/1/0/yes/no/on/off" strings (case-insensitive,
    // trimmed), and numbers (0=false, nonzero=true) so hand-edited configs
    // and frontend payloads never silently misread.
    if let Some(v) = obj.get("TRAY_ENABLED").or_else(|| obj.get("tray_enabled")) {
        if let Some(b) = v.as_bool() {
            cfg.tray_enabled = b;
        } else if let Some(s) = v.as_str() {
            let t = s.trim().to_ascii_lowercase();
            cfg.tray_enabled = matches!(t.as_str(), "true" | "1" | "yes" | "y" | "on");
        } else if let Some(n) = v.as_u64() {
            cfg.tray_enabled = n != 0;
        } else if let Some(n) = v.as_i64() {
            cfg.tray_enabled = n != 0;
        }
    }
    if let Some(n) = obj
        .get("PROBE_TRIES")
        .and_then(|x| x.as_u64().map(|v| v as u32))
    {
        cfg.probe_tries = n;
    }
    if let Some(t) = get_f64("PROBE_TIMEOUT") {
        cfg.probe_timeout = t;
    }
    if let Some(p) = get_u16("SOCKS5_PORT") {
        cfg.socks5_port = p;
    }
    if let Some(p) = get_u16("HTTP_PORT") {
        cfg.http_port = p;
    }
    Ok(cfg)
}

/// Truthiness for a hand-edited bool, with the same accepted set as
/// `TRAY_ENABLED`; anything unrecognized is false.
fn value_is_true(v: &serde_json::Value) -> bool {
    if let Some(b) = v.as_bool() {
        return b;
    }
    if let Some(s) = v.as_str() {
        return matches!(
            s.trim().to_ascii_lowercase().as_str(),
            "true" | "1" | "yes" | "y" | "on"
        );
    }
    if let Some(n) = v.as_u64() {
        return n != 0;
    }
    v.as_i64().is_some_and(|n| n != 0)
}

/// Plan A reachability scanner seam: given candidate `(ip, port)` pairs and a
/// timeout, return the reachable ones ranked best-first. `None` skips scanning
/// (resolution order is kept), which is what the offline test seam uses.
pub type PlanAScanner = fn(&[(String, u16)], Duration) -> Vec<(String, u16)>;

/// Plan A: derive the connect endpoints from `fake_snis` (DoH) or from
/// `connect_override`. Plan B never reaches past the guard below, and
/// `resolve_sni` only deactivates the configured endpoints for this run — the
/// file keeps them, so clearing the flag restores Plan B unchanged.
///
/// Production path: every default DoH provider is tried, then the OS resolver
/// as a last resort, and the resolved addresses are scanned + ranked so the
/// engine dials a live, fast endpoint first.
pub fn apply_plan_a(cfg: &mut Config) -> Result<(), ConfigError> {
    apply_plan_a_inner(
        cfg,
        crate::net::DEFAULT_DOH_SERVERS,
        true,
        Some(crate::scan::rank_reachable),
    )
}

/// Testable seam for [`apply_plan_a`]: the DoH servers come from the caller, so
/// a test can point them at a local fake instead of 1.1.1.1. OS-resolver
/// fallback and the reachability scan are OFF here, so this stays a pure
/// resolution step and offline tests remain deterministic and instant.
pub fn apply_plan_a_with_servers(
    cfg: &mut Config,
    servers: &[crate::net::DohServer<'_>],
) -> Result<(), ConfigError> {
    apply_plan_a_inner(cfg, servers, false, None)
}

/// Full seam: choose the DoH servers, whether the OS resolver may back them
/// up, and the reachability scanner. Used by the offline Plan A tests that
/// exercise provider failover and scan+rank against local fakes.
pub fn apply_plan_a_full(
    cfg: &mut Config,
    servers: &[crate::net::DohServer<'_>],
    os_fallback: bool,
    scanner: Option<PlanAScanner>,
) -> Result<(), ConfigError> {
    apply_plan_a_inner(cfg, servers, os_fallback, scanner)
}

fn apply_plan_a_inner(
    cfg: &mut Config,
    servers: &[crate::net::DohServer<'_>],
    os_fallback: bool,
    scanner: Option<PlanAScanner>,
) -> Result<(), ConfigError> {
    // 1. CONNECT_OVERRIDE wins outright; DoH and the scan are skipped.
    if !cfg.connect_override.is_empty() {
        let ip = cfg.connect_override.clone();
        let port = endpoint_port(cfg);
        cfg.endpoints = vec![Endpoint { ip, port }];
        return Ok(());
    }
    // 2. Plan B: nothing to do.
    if !cfg.resolve_sni {
        return Ok(());
    }
    if cfg.fake_snis.is_empty() {
        return Err(ConfigError::Validation(
            "RESOLVE_SNI: nothing to resolve (set FAKE_SNI or FAKE_SNIS)".to_string(),
        ));
    }
    let port = endpoint_port(cfg);
    if cfg.fake_snis.len() > MAX_RESOLVED_ENDPOINTS {
        tracing::warn!(
            "RESOLVE_SNI: resolving the first {} of {} SNIs",
            MAX_RESOLVED_ENDPOINTS,
            cfg.fake_snis.len()
        );
    }

    // 3. Resolve every SNI best-effort. A single dead name no longer aborts
    //    the whole run; it is logged and skipped, and only an empty candidate
    //    set fails fast (naming every cause).
    let mut candidates: Vec<(String, u16)> = Vec::new();
    let mut seen: HashSet<(String, u16)> = HashSet::new();
    let mut failures: Vec<String> = Vec::new();
    for sni in cfg.fake_snis.iter().take(MAX_RESOLVED_ENDPOINTS) {
        let resolved = match crate::net::resolve_a(servers, sni) {
            Ok(ip) => Some(ip),
            Err(cause) => {
                // OS-resolver last resort (production only): Plan A exists to
                // dodge a poisoned local resolver, so warn loudly when reached.
                if os_fallback {
                    match crate::net::resolve_a_os(sni) {
                        Some(os_ip) => {
                            tracing::warn!(
                                "RESOLVE_SNI: DoH failed for {sni} ({cause}); OS resolver fallback -> {os_ip}"
                            );
                            Some(os_ip)
                        }
                        None => {
                            failures.push(format!("{sni}: {cause}"));
                            None
                        }
                    }
                } else {
                    failures.push(format!("{sni}: {cause}"));
                    None
                }
            }
        };
        if let Some(ip) = resolved {
            if seen.insert((ip.clone(), port)) {
                candidates.push((ip, port));
            }
        }
    }

    if candidates.is_empty() {
        return Err(ConfigError::Validation(format!(
            "RESOLVE_SNI: no A record for any SNI via DoH ({})",
            failures.join("; ")
        )));
    }
    if !failures.is_empty() {
        tracing::warn!(
            "RESOLVE_SNI: {} SNI(s) failed to resolve and were skipped: {}",
            failures.len(),
            failures.join("; ")
        );
    }

    // 4. Scan + rank the survivors so the engine dials a live, fast endpoint
    //    first. When the scan finds nothing reachable (the same network that
    //    blocks DoH can black-hole the scan too), fall back to resolution
    //    order rather than failing — an endpoint that is only reachable from
    //    the server side is still worth trying.
    let chosen: Vec<(String, u16)> = match scanner {
        Some(scan) => {
            let ranked = scan(&candidates, PLAN_A_SCAN_TIMEOUT);
            if ranked.is_empty() {
                tracing::warn!(
                    "RESOLVE_SNI: scan found no reachable endpoint among {} candidate(s); using resolved IPs unranked",
                    candidates.len()
                );
                candidates
            } else {
                ranked
            }
        }
        None => candidates,
    };

    cfg.endpoints = chosen
        .into_iter()
        .map(|(ip, p)| Endpoint { ip, port: p })
        .collect();
    Ok(())
}

/// Port for the derived endpoints: the first configured endpoint already
/// carries `CONNECT_PORT` from `migrate()`; with none at all, 443.
fn endpoint_port(cfg: &Config) -> u16 {
    cfg.endpoints
        .first()
        .map(|e| e.port)
        .filter(|p| *p != 0)
        .unwrap_or(443)
}

/// Strict checks. Mirrors `config_manager.validate()` + `main.load_config` ranges.
pub fn validate(cfg: &Config) -> Result<(), ConfigError> {
    let mut errs: Vec<String> = vec![];
    if cfg.listen_host != "0.0.0.0" && !is_valid_ipv4(&cfg.listen_host) {
        errs.push(format!(
            "LISTEN_HOST invalid: {}",
            truncate(&cfg.listen_host, 64)
        ));
    }
    if cfg.listen_port == 0 {
        errs.push("LISTEN_PORT must be 1-65535".to_string());
    }
    if cfg.endpoints.is_empty() {
        // A resolve_sni config is legitimately empty until apply_plan_a() fills
        // it in, so callers that only validate (GUI save) must not reject it.
        if !cfg.resolve_sni {
            errs.push("No endpoints (set CONNECT_IP or ENDPOINTS)".to_string());
        }
    } else if cfg.endpoints.len() > MAX_ENDPOINTS {
        errs.push(format!("Too many endpoints (max {})", MAX_ENDPOINTS));
    } else {
        for e in &cfg.endpoints {
            if !is_valid_ipv4(&e.ip) {
                errs.push(format!("Bad endpoint IP: {}", truncate(&e.ip, 64)));
            }
            if e.port == 0 {
                errs.push(format!("Bad endpoint port for {}", truncate(&e.ip, 32)));
            }
        }
    }
    if !cfg.connect_override.is_empty() && !is_valid_ipv4(&cfg.connect_override) {
        errs.push(format!(
            "CONNECT_OVERRIDE must be an IPv4 address: {}",
            truncate(&cfg.connect_override, 64)
        ));
    }
    if cfg.fake_snis.is_empty() {
        errs.push("No FAKE_SNI set".to_string());
    } else if cfg.fake_snis.len() > MAX_SNIS {
        errs.push(format!("Too many SNIs (max {})", MAX_SNIS));
    } else {
        for s in &cfg.fake_snis {
            if !is_valid_sni(s) {
                errs.push(format!("Bad SNI: {}", truncate(s, 64)));
            }
        }
    }
    if !SUPPORTED_METHODS.contains(&cfg.bypass_method.as_str()) {
        errs.push(format!(
            "BYPASS_METHOD must be one of {:?}",
            SUPPORTED_METHODS
        ));
    }
    if !(0.5..=10.0).contains(&cfg.handshake_timeout) {
        errs.push("HANDSHAKE_TIMEOUT should be 0.5..10s".to_string());
    }
    if !(10..=2000).contains(&cfg.max_connections) {
        errs.push("MAX_CONNECTIONS should be 10..2000".to_string());
    }
    if !(0.0..=5.0).contains(&cfg.fake_delay) {
        errs.push("FAKE_DELAY should be 0..5s".to_string());
    }
    if cfg.seq_overlap > 16 {
        errs.push("SEQ_OVERLAP should be 0..16".to_string());
    }
    if !SUPPORTED_FINGERPRINTS.contains(&cfg.tls_fingerprint.as_str()) {
        errs.push(format!(
            "TLS_FINGERPRINT must be one of {:?}",
            SUPPORTED_FINGERPRINTS
        ));
    }
    if !SUPPORTED_QUIC_MODES.contains(&cfg.quic_mode.as_str()) {
        errs.push(format!(
            "QUIC_MODE must be one of {:?}",
            SUPPORTED_QUIC_MODES
        ));
    }
    // FIX(core): "Trojan + Xray" was accepted and then silently did nothing
    // (no Xray/Trojan code exists). Fail fast with an actionable message.
    if cfg.mode == "Trojan + Xray" {
        errs.push(
            "MODE \"Trojan + Xray\" is not implemented in this build (only \"SNI Only\" is supported)"
                .to_string(),
        );
    } else if !SUPPORTED_MODES.contains(&cfg.mode.as_str()) {
        errs.push(format!("MODE must be one of {:?}", SUPPORTED_MODES));
    }
    // FIX: previously migrated but never validated — bad values passed silently.
    if cfg.padding_size > 128 {
        errs.push("PADDING_SIZE should be 0..128".to_string());
    }
    if !(1..=10).contains(&cfg.probe_tries) {
        errs.push("PROBE_TRIES should be 1..10".to_string());
    }
    if !(0.5..=10.0).contains(&cfg.probe_timeout) {
        errs.push("PROBE_TIMEOUT should be 0.5..10s".to_string());
    }
    if cfg.socks5_port == 0 || cfg.http_port == 0 {
        errs.push("SOCKS5_PORT/HTTP_PORT must be 1-65535".to_string());
    }
    if cfg.socks5_port == cfg.listen_port
        || cfg.http_port == cfg.listen_port
        || (cfg.socks5_port == cfg.http_port && cfg.socks5_port != 0)
    {
        errs.push(
            "SOCKS5_PORT/HTTP_PORT must not collide with LISTEN_PORT or each other".to_string(),
        );
    }
    // FIX #8: address family selection must be a known mode.
    if !SUPPORTED_IP_MODES.contains(&cfg.ip_mode.as_str()) {
        errs.push(format!("IP_MODE must be one of {:?}", SUPPORTED_IP_MODES));
    }
    if errs.is_empty() {
        Ok(())
    } else {
        Err(ConfigError::Validation(errs.join("; ")))
    }
}

pub fn is_valid_ipv4(ip: &str) -> bool {
    let parts: Vec<&str> = ip.trim().split('.').collect();
    if parts.len() != 4 {
        return false;
    }
    parts.iter().all(|p| {
        !p.is_empty()
            && p.len() <= 3
            && p.bytes().all(|b| b.is_ascii_digit())
            && p.parse::<u8>().is_ok()
    })
}

pub fn is_valid_sni(s: &str) -> bool {
    // FIX P2: align with injector template (tls.rs MAX_SNI_LEN=219) so a
    // config-valid SNI can never silently produce an empty fake hello.
    // Also reject malformed hostnames: empty labels, leading/trailing
    // dot/hyphen, spaces/slashes, invalid chars, IP literals get checked
    // by the caller where relevant.
    let s = s.trim();
    if s.is_empty() || s.len() > 219 || !s.contains('.') {
        return false;
    }
    if s.contains(' ') || s.contains('/') || s.contains(':') {
        return false;
    }
    if s.starts_with('.') || s.ends_with('.') || s.contains("..") {
        return false;
    }
    if s.starts_with('-') || s.ends_with('-') {
        return false;
    }
    for label in s.split('.') {
        if label.is_empty() || label.len() > 63 {
            return false;
        }
        if label.starts_with('-') || label.ends_with('-') {
            return false;
        }
        if !label
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-')
        {
            return false;
        }
    }
    true
}

fn truncate(s: &str, n: usize) -> String {
    if s.len() <= n {
        s.to_string()
    } else {
        format!("{}…", &s[..n])
    }
}
