//! Stats + scoreboard: replaces `monitor_connection.py`.
//!
//! Python used module globals + `threading.Lock`. Rust uses a `Stats`
//! struct holding `parking_lot::Mutex<Inner>` so both Tokio tasks and
//! WinDivert callback threads share one instance via `Arc<Stats>`.
//! SNI board keys are hashed (privacy parity); endpoint keys stay literal.

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Instant;

const BOARD_CAP: usize = 200;

/// Latency at and above which a strategy/endpoint takes the full ranking
/// penalty. A method that works in 1500 ms is *usable* but is not what `auto`
/// picks when something faster also works — the user-visible symptom this
/// constant exists for is a working config that pings like a blocked one.
pub const LAT_PENALTY_FULL_MS: f64 = 1500.0;
/// Largest deduction `latency_ms` can make from a score. Kept below 1.0 so a
/// slow-but-working entry still outranks a failing one: latency demotes,
/// failure eliminates.
pub const MAX_LAT_PENALTY: f64 = 0.45;
/// Blend factor for the exponential moving averages below. 0.3 reacts to a
/// changed network in a handful of connections without letting one outlier
/// flip the board.
const RATE_ALPHA: f64 = 0.3;
const LAT_ALPHA: f64 = 0.4;

/// One scoreboard row: cumulative counts (for display) plus the recency-
/// weighted numbers `auto` actually ranks on.
#[derive(Debug, Default, Clone, Copy)]
struct Cell {
    ok: u32,
    fail: u32,
    /// EWMA of the outcome, so a strategy that worked yesterday and fails
    /// today stops winning today instead of hiding behind its lifetime rate.
    rate_ewma: f64,
    /// EWMA latency in ms, undefined until `measured`.
    lat_ms: f64,
    measured: bool,
}

impl Cell {
    fn rate(&self) -> f64 {
        let tot = self.ok as f64 + self.fail as f64;
        if tot == 0.0 {
            0.0
        } else {
            self.ok as f64 / tot
        }
    }

    fn record(&mut self, ok: bool) {
        if ok {
            self.ok = self.ok.saturating_add(1);
        } else {
            self.fail = self.fail.saturating_add(1);
        }
        let v = if ok { 1.0 } else { 0.0 };
        self.rate_ewma = if self.ok + self.fail == 1 {
            v
        } else {
            (1.0 - RATE_ALPHA) * self.rate_ewma + RATE_ALPHA * v
        };
    }

    fn record_latency(&mut self, ms: f64) {
        self.lat_ms = if self.measured {
            (1.0 - LAT_ALPHA) * self.lat_ms + LAT_ALPHA * ms
        } else {
            ms
        };
        self.measured = true;
    }

    /// Rank key for this row: proven quality minus latency drag.
    fn score(&self) -> f64 {
        let penalty = if self.measured {
            (self.lat_ms / LAT_PENALTY_FULL_MS).min(1.0) * MAX_LAT_PENALTY
        } else {
            0.0
        };
        self.rate_ewma - penalty
    }
}

#[derive(Debug, Default)]
struct Inner {
    active: u64,
    total: u64,
    success: u64,
    failed: u64,
    up_bytes: u64,
    down_bytes: u64,
    endpoints: HashMap<String, Cell>,
    snis: HashMap<String, Cell>,
    methods: HashMap<String, Cell>,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct Ranked {
    pub key: String,
    pub ok: u32,
    pub fail: u32,
    pub rate: f64,
    /// EWMA latency in ms; `null` when nothing has been timed for this row yet.
    pub latency_ms: Option<f64>,
    /// What `auto` ranked this row on: recent success rate minus latency drag.
    pub score: f64,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct Snapshot {
    #[serde(rename = "type")]
    pub kind: String,
    pub active: u64,
    pub total: u64,
    pub success: u64,
    pub failed: u64,
    pub uptime: f64,
    pub success_rate: f64,
    pub best_endpoint: String,
    pub best_method: String,
    pub methods: Vec<Ranked>,
    pub up_bytes: u64,
    pub down_bytes: u64,
}

pub struct Stats {
    inner: Mutex<Inner>,
    started: Instant,
    // Hot counters duplicated as atomics so relay fast-path avoids lock.
    active_a: AtomicU64,
    up_a: AtomicU64,
    down_a: AtomicU64,
}

impl Default for Stats {
    fn default() -> Self {
        Self::new()
    }
}

impl Stats {
    pub fn new() -> Self {
        Self {
            inner: Mutex::new(Inner::default()),
            started: Instant::now(),
            active_a: AtomicU64::new(0),
            up_a: AtomicU64::new(0),
            down_a: AtomicU64::new(0),
        }
    }

    pub fn increment_active(&self) {
        self.active_a.fetch_add(1, Ordering::Relaxed);
        self.inner.lock().active += 1;
    }

    pub fn increment_total(&self) {
        self.inner.lock().total += 1;
    }

    pub fn finish_success(&self) {
        self.decrement_active_inner();
        self.inner.lock().success += 1;
    }

    pub fn finish_failed(&self) {
        self.decrement_active_inner();
        self.inner.lock().failed += 1;
    }

    pub fn increment_failed(&self) {
        self.inner.lock().failed += 1;
    }

    /// Bump the success counter without touching `active`.
    /// FIX(stats): used by the relay-outcome accounting path so the
    /// worker can decide success/fail AFTER the relay ends, without
    /// the decrement-active side effect of `finish_success()`.
    pub fn increment_success(&self) {
        self.inner.lock().success += 1;
    }

    /// Release one active slot without counting success/fail (relay teardown).
    /// Phase 3 needs this so handshake success isn't double-counted.
    pub fn decrement_active(&self) {
        self.decrement_active_inner();
    }

    fn decrement_active_inner(&self) {
        let prev = self.active_a.load(Ordering::Relaxed);
        if prev > 0 {
            self.active_a.fetch_sub(1, Ordering::Relaxed);
        }
        let mut g = self.inner.lock();
        if g.active > 0 {
            g.active -= 1;
        }
    }

    pub fn add_traffic(&self, up: u64, down: u64) {
        self.up_a.fetch_add(up, Ordering::Relaxed);
        self.down_a.fetch_add(down, Ordering::Relaxed);
        let mut g = self.inner.lock();
        g.up_bytes = g.up_bytes.saturating_add(up);
        g.down_bytes = g.down_bytes.saturating_add(down);
    }

    /// Mirrors `record_result(endpoint, sni, ok, method)`.
    pub fn record_result(&self, endpoint: &str, sni: &str, ok: bool, method: &str) {
        let mut g = self.inner.lock();
        if !endpoint.is_empty() {
            let c = g.endpoints.entry(endpoint.to_string()).or_default();
            c.record(ok);
            prune(&mut g.endpoints);
        }
        if !sni.is_empty() {
            let key = hash_sni(sni);
            let c = g.snis.entry(key).or_default();
            c.record(ok);
            prune(&mut g.snis);
        }
        if !method.is_empty() {
            let c = g.methods.entry(method.to_string()).or_default();
            c.record(ok);
            prune(&mut g.methods);
        }
    }

    /// Attach a latency sample to the same rows `record_result` touched.
    ///
    /// Kept separate from `record_result` because the two measurements come
    /// from different places: outcome is known when the relay tears down,
    /// while the useful latency figure is the connect/handshake time observed
    /// earlier. `ms` is clamped so one absurd sample cannot poison a row.
    pub fn record_latency(&self, endpoint: &str, method: &str, ms: f64) {
        if !ms.is_finite() || ms < 0.0 {
            return;
        }
        let ms = ms.min(LAT_PENALTY_FULL_MS * 4.0);
        let mut g = self.inner.lock();
        if !endpoint.is_empty() {
            g.endpoints
                .entry(endpoint.to_string())
                .or_default()
                .record_latency(ms);
            prune(&mut g.endpoints);
        }
        if !method.is_empty() {
            g.methods
                .entry(method.to_string())
                .or_default()
                .record_latency(ms);
            prune(&mut g.methods);
        }
    }

    /// Rank score for a strategy key, or `None` if it has never been tried.
    /// `auto` uses the `None` case as its "explore this next" signal.
    pub fn strategy_score(&self, key: &str) -> Option<f64> {
        self.score_of(Board::Method, key)
    }

    /// Rank score for an `ip:port` endpoint, or `None` if untried.
    pub fn endpoint_score(&self, key: &str) -> Option<f64> {
        self.score_of(Board::Endpoint, key)
    }

    /// Latest EWMA latency for a strategy key.
    pub fn strategy_latency(&self, key: &str) -> Option<f64> {
        let g = self.inner.lock();
        g.methods
            .get(key)
            .and_then(|c| c.measured.then_some(c.lat_ms))
    }

    /// Fastest EWMA connect latency measured across endpoints. The engine uses
    /// it to size the per-attempt failover budget: once something has answered
    /// quickly, a connection that has not completed by a small multiple of that
    /// time is not worth waiting five more seconds for.
    pub fn best_endpoint_latency(&self) -> Option<f64> {
        let g = self.inner.lock();
        g.endpoints
            .values()
            .filter(|c| c.measured)
            .map(|c| c.lat_ms)
            .reduce(f64::min)
    }

    fn score_of(&self, board: Board, key: &str) -> Option<f64> {
        let g = self.inner.lock();
        let cell = match board {
            Board::Method => g.methods.get(key),
            Board::Endpoint => g.endpoints.get(key),
        }?;
        if cell.ok + cell.fail == 0 {
            return None;
        }
        Some(cell.score())
    }

    /// Mirrors `get_snapshot()` — serialized as one JSON line for the GUI.
    pub fn snapshot(&self) -> Snapshot {
        let g = self.inner.lock();
        let tot = g.success + g.failed;
        let eps = ranked(&g.endpoints, 5);
        let methods = ranked(&g.methods, 3);
        Snapshot {
            kind: "stats".to_string(),
            active: g.active,
            total: g.total,
            success: g.success,
            failed: g.failed,
            uptime: self.started.elapsed().as_secs_f64(),
            success_rate: if tot == 0 {
                0.0
            } else {
                g.success as f64 / tot as f64
            },
            best_endpoint: eps.first().map(|r| r.key.clone()).unwrap_or_default(),
            best_method: methods.first().map(|r| r.key.clone()).unwrap_or_default(),
            methods,
            up_bytes: g.up_bytes,
            down_bytes: g.down_bytes,
        }
    }

    pub fn reset(&self) {
        let mut g = self.inner.lock();
        g.active = 0;
        g.total = 0;
        g.success = 0;
        g.failed = 0;
        g.up_bytes = 0;
        g.down_bytes = 0;
        g.endpoints.clear();
        g.snis.clear();
        g.methods.clear();
        self.active_a.store(0, Ordering::Relaxed);
        self.up_a.store(0, Ordering::Relaxed);
        self.down_a.store(0, Ordering::Relaxed);
    }
}

/// Which scoreboard a lookup targets.
#[derive(Debug, Clone, Copy)]
enum Board {
    Method,
    Endpoint,
}

fn prune(board: &mut HashMap<String, Cell>) {
    while board.len() > BOARD_CAP {
        if let Some(k) = board.keys().next().cloned() {
            board.remove(&k);
        } else {
            break;
        }
    }
}

fn ranked(board: &HashMap<String, Cell>, limit: usize) -> Vec<Ranked> {
    let mut v: Vec<Ranked> = board
        .iter()
        .map(|(k, c)| Ranked {
            key: k.clone(),
            ok: c.ok,
            fail: c.fail,
            rate: (c.rate() * 1000.0).round() / 1000.0,
            latency_ms: c.measured.then_some((c.lat_ms * 10.0).round() / 10.0),
            score: (c.score() * 1000.0).round() / 1000.0,
        })
        .collect();
    // Same comparator `auto` uses, so the GUI shows exactly what the engine is
    // preferring rather than a stale success-rate ordering.
    v.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| (b.ok + b.fail).cmp(&(a.ok + a.fail)))
            .then_with(|| a.key.cmp(&b.key))
    });
    v.truncate(limit);
    v
}

/// One-way SNI tag for the board (parity with `utils.security.hash_sni`).
/// Phase 0 uses a non-crypto FNV tag; Phase 3 swaps in SHA-256 truncation.
fn hash_sni(s: &str) -> String {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in s.bytes() {
        h ^= b as u64;
        h = h.wrapping_mul(0x100000001b3);
    }
    format!("sni#{:016x}", h)
}

/// Smallest relayed byte count that counts as a live session. A real TLS
/// handshake plus one request/response is well past this; a connection the
/// interceptor destroyed usually moves only the client's own hello.
pub const MIN_RELAY_BYTES: u64 = 100;

/// Final verdict for one finished connection.
///
/// Bytes moved is the historical rule, and on its own it is not enough: an
/// interceptor's kill page is *also* bytes moved toward the client (this
/// network answers a blocked TLS connect with a 415-byte plaintext
/// `HTTP/1.1 302`), so every connection the DPI destroyed scored as a bypass
/// success and `auto` had nothing to rank on. An explicit interceptor answer
/// therefore overrides the byte count.
pub fn connection_verdict(bytes_moved: u64, interceptor_answered: bool) -> bool {
    !interceptor_answered && bytes_moved >= MIN_RELAY_BYTES
}

#[cfg(test)]
mod tests {
    use super::connection_verdict;

    #[test]
    fn an_interceptor_answer_overrides_the_byte_count() {
        // The measured kill: plaintext 302 on 443, plenty of bytes, no session.
        assert!(
            !connection_verdict(415, true),
            "an injected kill page must not score as a bypass success"
        );
    }

    #[test]
    fn a_real_session_still_passes_and_a_stubborn_stall_still_fails() {
        assert!(connection_verdict(5000, false));
        assert!(
            connection_verdict(100, false),
            "the 100-byte floor is >=, not >"
        );
        assert!(!connection_verdict(99, false));
        assert!(!connection_verdict(99, true));
    }
}
