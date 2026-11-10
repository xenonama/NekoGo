//! Async orchestrator: port of `main.py` accept/failover/relay engine.
//!
//! Moved verbatim from `backend/src/worker.rs` into the unified `sni-gui`
//! binary. `Worker` owns ENDPOINTS (round-robin), FAKE_SNIS (random),
//! connection table, semaphore, stats. Packet-level WinDivert glue
//! (`engine.rs` DPI dispatch) signals handshake completion via
//! `complete_handshake`; until a packet matches, `handle()` times out per
//! HANDSHAKE_TIMEOUT (same observable path as Python DPI failure).
//!
//! One deliberate change vs the old backend: `reporter()` no longer prints
//! JSON to stdout (there is no GUI pipe anymore — the GUI polls
//! `Stats::snapshot()` directly (throttled to 1Hz). It now emits at `debug` level
//! and is unused by default; kept for parity/debugging.

use parking_lot::Mutex as ParkMutex;
use serde::Serialize;
use sni_core::{
    config::{Config, Endpoint},
    fake_tcp::{BypassMethod, Strategy},
    picker::{
        build_ipv6_drop_filter, build_quic_filter, build_tcp_filter, ep_key, pick_sni,
        EndpointPicker,
    },
    stats::{connection_verdict, Stats, MIN_RELAY_BYTES},
};
use std::{
    collections::HashMap,
    net::SocketAddr,
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc,
    },
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpSocket, TcpStream},
    sync::{Mutex as AsyncMutex, Notify, Semaphore},
};

/// 4-tuple connection id: (src_ip, src_port, dst_ip, dst_port).
/// Mirrors `FakeInjectiveConnection.id`.
pub type ConnId = (String, u16, String, u16);

/// One live relay session for the Active Connections view (U5).
/// `uptime_secs` is computed at snapshot time; `started` never crosses FFI.
#[derive(Debug, Clone, Serialize)]
pub struct ActiveConn {
    pub local_port: u16,
    pub remote: String,
    pub method: String,
    pub uptime_secs: u64,
}

/// Internal relay-session record (holds the start `Instant`).
struct ActiveRelay {
    local_port: u16,
    remote: String,
    method: String,
    started: Instant,
}

/// FIX(#3b): guarantees `decrement_active` and `active_relays.remove`
/// run even if `handle()` panics or is cancelled mid-relay. Armed
/// once, right after the `increment_active` / `active_relays.insert`,
/// so any subsequent `.await` is protected.
struct ActiveGuard {
    stats: Arc<Stats>,
    relays: Arc<ParkMutex<HashMap<ConnId, ActiveRelay>>>,
    // FIX(#6): per-connection resolved-method record, cleaned on drop.
    resolved_methods: Arc<ParkMutex<HashMap<ConnId, String>>>,
    // Interceptor flags are per-connection too: a connection that never
    // reaches the verdict line must not leave its flag behind.
    intercepted: Arc<ParkMutex<std::collections::HashSet<ConnId>>>,
    id: ConnId,
}

impl Drop for ActiveGuard {
    fn drop(&mut self) {
        self.stats.decrement_active();
        self.relays.lock().remove(&self.id);
        // FIX(#6): also drop the per-connection resolved-method record.
        self.resolved_methods.lock().remove(&self.id);
        self.intercepted.lock().remove(&self.id);
    }
}

/// FIX(#7)/v3: adaptive strategy selection for the "auto" config.
///
/// A *strategy* is a decoy shape plus whether the client's real ClientHello is
/// segmented, so `auto` explores 15 combinations instead of the 9 method names
/// the config file used to expose. Selection is sticky and scoreboard-driven:
/// the current strategy keeps serving connections until it either fails
/// repeatedly, gets slow, or the exploration queue says it is time to measure
/// another one.
pub struct AutoState {
    pub current: Strategy,
    pub attempts: u32,
    pub failures: u32,
    pub started_at: Instant,
    /// Strategies with zero samples since this process started, in a fresh
    /// random order. A restart must re-measure the board rather than inherit a
    /// row that was true for yesterday's network.
    pending: Vec<Strategy>,
}

impl Default for AutoState {
    fn default() -> Self {
        Self::new()
    }
}

impl AutoState {
    /// Rotate after this many consecutive failures on the same strategy.
    pub const MAX_FAILURES: u32 = 3;
    /// Even a strategy that works is re-examined after this many connections,
    /// so exploration cannot stall forever on "good enough".
    pub const EXPLORE_EVERY: u32 = 4;
    /// A working strategy above this EWMA latency is replaced. This is the
    /// 900-1000 ms case: it connects, and it feels broken.
    pub const SLOW_SWITCH_MS: f64 = 800.0;
    /// Latency judgement needs more than one sample.
    pub const MIN_LATENCY_SAMPLES: u32 = 2;

    pub fn new() -> Self {
        let mut pending = Strategy::pool_shuffled();
        let current = pending
            .pop()
            .unwrap_or_else(|| Strategy::plain(BypassMethod::WrongSeq));
        Self {
            current,
            attempts: 0,
            failures: 0,
            started_at: Instant::now(),
            pending,
        }
    }

    /// Record one connection outcome and apply the rotation policy. Returns
    /// `true` when `current` changed, so callers can log or re-plan.
    ///
    /// FIX(sticky-auto): a strategy that WORKS is kept. The pre-v3 state
    /// rotated on `attempts >= MAX_ATTEMPTS` or `elapsed >= MAX_SECS` even when
    /// every connection succeeded, so `auto` abandoned a bypass that defeated
    /// the local DPI and thrashed back onto failing ones.
    pub fn record(&mut self, stats: &Stats, success: bool) -> bool {
        self.attempts = self.attempts.saturating_add(1);
        if success {
            self.failures = 0;
        } else {
            self.failures = self.failures.saturating_add(1);
        }

        let key = self.current.board_key();
        let exhausted = self.failures >= Self::MAX_FAILURES;
        let slow = success
            && self.attempts >= Self::MIN_LATENCY_SAMPLES
            && stats
                .strategy_latency(&key)
                .is_some_and(|ms| ms > Self::SLOW_SWITCH_MS);
        let explore = !self.pending.is_empty() && self.attempts >= Self::EXPLORE_EVERY;
        if exhausted || slow || explore {
            self.rotate(stats);
            return true;
        }
        false
    }

    /// Move to the next strategy: an unmeasured one while the queue has them,
    /// otherwise the best-scoring one that is not the current row.
    pub fn rotate(&mut self, stats: &Stats) {
        let previous = self.current;
        let next = match self.pending.pop() {
            Some(s) => s,
            None => Self::best_scored(stats, &previous),
        };
        tracing::info!(
            "auto: rotating strategy {} -> {} (attempts={} failures={} pending={} elapsed={}s)",
            previous.board_key(),
            next.board_key(),
            self.attempts,
            self.failures,
            self.pending.len(),
            self.started_at.elapsed().as_secs()
        );
        self.current = next;
        self.attempts = 0;
        self.failures = 0;
    }

    /// Highest score on the board, ties broken deterministically. Falls back to
    /// the next entry in the pool when nothing else has been measured.
    fn best_scored(stats: &Stats, previous: &Strategy) -> Strategy {
        let mut best: Option<(f64, Strategy)> = None;
        for s in Strategy::pool() {
            if &s == previous {
                continue;
            }
            if let Some(score) = stats.strategy_score(&s.board_key()) {
                match &best {
                    Some((bs, _)) if *bs >= score => {}
                    _ => best = Some((score, s)),
                }
            }
        }
        match best {
            Some((score, s)) => {
                tracing::debug!("auto: best strategy {} score={:.3}", s.board_key(), score);
                s
            }
            // Stepping to the next pool entry, rather than drawing at random,
            // keeps a rotation from handing back the strategy it just dropped.
            None => {
                let pool = Strategy::pool();
                let at = pool
                    .iter()
                    .position(|s| s == previous)
                    .map(|i| (i + 1) % pool.len())
                    .unwrap_or(0);
                pool[at]
            }
        }
    }
}

/// Per-connection entry (subset of `FakeInjectiveConnection` needed by the
/// orchestrator; sockets stay owned by `handle()` tasks).
pub struct ConnEntry {
    #[allow(dead_code)]
    pub id: ConnId,
    pub created_at: Instant,
    pub monitor: AtomicBool,
    // FIX(dedup): dead `counted` flag removed (write-only after the
    // note_fail cleanup; verdict comes from bytes moved, not a flag).
    pub method: String,
    // FIX(dedup): `sni` lost its only reader with note_fail; kept as
    // debug state under allow(dead_code), same as `id` above.
    #[allow(dead_code)]
    pub sni: String,
    completed: Notify,
    result: parking_lot::Mutex<Option<bool>>,
}

impl ConnEntry {
    fn new(id: ConnId, method: String, sni: String) -> Self {
        Self {
            id,
            created_at: Instant::now(),
            monitor: AtomicBool::new(true),
            method,
            sni,
            completed: Notify::new(),
            result: parking_lot::Mutex::new(None),
        }
    }

    fn signal(&self, ok: bool) {
        *self.result.lock() = Some(ok);
        self.completed.notify_one();
    }

    async fn wait(&self) -> Option<bool> {
        self.completed.notified().await;
        *self.result.lock()
    }
}

/// Tokio orchestrator. Share via `Arc<Worker>` across accept/reaper tasks
/// (mirrors module-level globals + `_cycle_lock` in main.py).
pub struct Worker {
    pub config: Arc<Config>,
    pub picker: Arc<EndpointPicker>,
    pub stats: Arc<Stats>,
    pub conns: Arc<AsyncMutex<HashMap<ConnId, Arc<ConnEntry>>>>,
    pub sem: Arc<Semaphore>,
    pub shutdown: Arc<Notify>,
    pub interface_ipv4: String,
    // IMPROVE(U5): sync relay-session table for the Active Connections view.
    // `parking_lot` (sync) instead of the async `conns` map so the sync FFI
    // layer can snapshot without a Tokio runtime. Inserted before relay,
    // removed after; replaced per connection, never appended (D2: no leak).
    active_relays: Arc<ParkMutex<HashMap<ConnId, ActiveRelay>>>,
    // FIX(perf): sync mirror of `conns` for the DPI fast-path guard.
    // The capture thread (std::thread) cannot await the tokio Mutex,
    // and try_lock() on it was unreliable under contention. A
    // parking_lot Mutex<HashSet<ConnId>> is lock-free enough for the
    // read-heavy fast path.
    pub dpi_conns: Arc<ParkMutex<std::collections::HashSet<ConnId>>>,
    /// FIX(#7): adaptive state for the "auto" config. Unused when
    /// bypass_method is a concrete method, but always constructed so
    /// a live config change can flip into auto without a restart.
    pub auto_state: Arc<ParkMutex<AutoState>>,
    /// FIX(#6): resolved method per active connection. When config is
    /// "auto", the DPI state machine resolved it to one of the real
    /// methods per connection. This map records that choice so stats
    /// can rank the actual method instead of the useless "auto"
    /// placeholder. Cleaned up by ActiveGuard.
    pub resolved_methods: Arc<ParkMutex<HashMap<ConnId, String>>>,
    /// Connections the DPI state machine saw answered by the interceptor
    /// instead of a TLS endpoint. `ConnEntry` is evicted before the relay
    /// starts, so the handshake signal cannot carry a late intercept to the
    /// verdict; the relay takes this flag when it finishes and `ActiveGuard`
    /// clears it on every exit path.
    intercepted: Arc<ParkMutex<std::collections::HashSet<ConnId>>>,
}

impl Worker {
    /// Resolve interface IPv4 from the first endpoint (mirrors
    /// `get_default_interface_ipv4(ENDPOINTS[0])`; fatal when unroutable).
    pub fn new(config: Config, stats: Arc<Stats>) -> anyhow::Result<Self> {
        if config.endpoints.is_empty() {
            anyhow::bail!("No endpoints configured");
        }
        let probe = config.endpoints[0].ip.clone();
        let iface = sni_core::net::get_default_interface_ipv4(&probe).ok_or_else(|| {
            anyhow::anyhow!("cannot determine default interface IPv4 (no route?)")
        })?;
        let max = config.max_connections.clamp(10, 2000);
        Ok(Self {
            picker: Arc::new(EndpointPicker::new(config.endpoints.clone())),
            config: Arc::new(config),
            stats,
            conns: Arc::new(AsyncMutex::new(HashMap::new())),
            sem: Arc::new(Semaphore::new(max)),
            shutdown: Arc::new(Notify::new()),
            interface_ipv4: iface,
            active_relays: Arc::new(ParkMutex::new(HashMap::new())),
            // FIX: init sync mirror for DPI fast-path guard.
            dpi_conns: Arc::new(ParkMutex::new(std::collections::HashSet::new())),
            // FIX(#7): sticky-auto state + per-connection resolved methods.
            auto_state: Arc::new(ParkMutex::new(AutoState::new())),
            resolved_methods: Arc::new(ParkMutex::new(HashMap::new())),
            intercepted: Arc::new(ParkMutex::new(std::collections::HashSet::new())),
        })
    }

    /// Round-robin ordered endpoints for one connection.
    pub fn pick_endpoints(&self) -> Vec<Endpoint> {
        self.picker.pick_ordered()
    }

    /// Random SNI per connection.
    pub fn pick_sni(&self) -> String {
        pick_sni(&self.config.fake_snis).unwrap_or_default()
    }

    pub fn tcp_filter(&self) -> String {
        // FIX #8: pass ip_mode so "ipv6" mode returns the IPv6 drop filter.
        build_tcp_filter(
            &self.interface_ipv4,
            &self.config.endpoints,
            &self.config.ip_mode,
        )
    }

    pub fn quic_filter(&self) -> String {
        build_quic_filter(&self.interface_ipv4)
    }

    /// FIX #8: IPv6 drop filter. Only used when ip_mode is "ipv4" or
    /// "both" — forces the browser to fall back to IPv4 so the fake
    /// burst keeps working.
    pub fn ipv6_drop_filter(&self) -> String {
        build_ipv6_drop_filter(&self.config.endpoints)
    }

    // FIX(perf): sync fast-path check for the DPI dispatch guard. Uses the
    // existing `active_relays` table (sync parking_lot, lives for the whole
    // relay) so the capture thread can test membership without a Tokio
    // runtime. `conns` cannot be used here: it is a tokio Mutex (no sync
    // lock from a std thread) and it is evicted before relay starts.
    pub fn is_known_relay(&self, id: &ConnId) -> bool {
        self.active_relays.lock().contains_key(id)
    }

    /// FIX(#7): returns the concrete strategy this connection should use. For
    /// a non-auto config this is just the config method's strategy. For "auto"
    /// it is the current sticky strategy from `auto_state`.
    pub fn resolve_strategy_for_connection(&self, cfg_method: BypassMethod) -> Strategy {
        match cfg_method {
            BypassMethod::Auto => self.auto_state.lock().current,
            other => other.into_strategy(),
        }
    }

    /// FIX(#7): record a connection outcome and rotate the sticky strategy if
    /// the auto policy says so. No-op when the config is not "auto".
    pub fn record_connection_result(&self, cfg_method: BypassMethod, success: bool) {
        if !matches!(cfg_method, BypassMethod::Auto) {
            return;
        }
        let mut st = self.auto_state.lock();
        st.record(&self.stats, success);
    }

    /// FIX(#6): called by the DPI dispatch when it commits to a burst, so
    /// stats later record the strategy that actually went on the wire.
    pub fn record_resolved_strategy(&self, id: &ConnId, key: &str) {
        self.resolved_methods
            .lock()
            .insert(id.clone(), key.to_string());
    }

    /// Sync snapshot of live relay sessions for the U5 Active Connections
    /// view. Lock-free w.r.t. Tokio (parking_lot only), so the sync C ABI
    /// can call it directly. Sorted by uptime, longest first.
    pub fn snapshot_active(&self) -> Vec<ActiveConn> {
        let map = self.active_relays.lock();
        let now = Instant::now();
        let mut out: Vec<ActiveConn> = map
            .values()
            .map(|r| ActiveConn {
                local_port: r.local_port,
                remote: r.remote.clone(),
                method: r.method.clone(),
                uptime_secs: now.duration_since(r.started).as_secs(),
            })
            .collect();
        out.sort_by(|a, b| {
            b.uptime_secs
                .cmp(&a.uptime_secs)
                .then_with(|| a.local_port.cmp(&b.local_port))
        });
        out
    }

    pub fn request_shutdown(&self) {
        self.shutdown.notify_waiters();
    }

    /// Called by the DPI state machine when the inbound payload was the
    /// interceptor's own plaintext answer, not a TLS record. The relay reads
    /// this at the verdict, because the `ConnEntry` is already gone by then.
    pub fn mark_intercepted(&self, id: &ConnId) {
        self.intercepted.lock().insert(id.clone());
    }

    fn take_intercepted(&self, id: &ConnId) -> bool {
        self.intercepted.lock().remove(id)
    }

    /// Called by WinDivert injector threads on handshake outcome.
    /// Mirrors `t2a_event.set()` with `t2a_msg` success/failure.
    pub async fn complete_handshake(&self, id: &ConnId, ok: bool) {
        let entry = { self.conns.lock().await.get(id).cloned() };
        if let Some(e) = entry {
            e.monitor.store(false, Ordering::Relaxed);
            e.signal(ok);
        }
    }

    /// Accept loop. Mirrors `main()` while-not-shutdown accept + spawn.
    pub async fn run(self: Arc<Self>, listener: TcpListener) -> anyhow::Result<()> {
        tracing::info!("accept loop started");
        loop {
            tokio::select! {
                _ = self.shutdown.notified() => break,
                res = listener.accept() => {
                    match res {
                        Ok((sock, addr)) => {
                            let w = Arc::clone(&self);
                            tokio::spawn(async move { w.handle(sock, addr).await });
                        }
                        Err(e) => {
                            tracing::warn!("accept failed: {}", e);
                            tokio::time::sleep(Duration::from_millis(50)).await;
                        }
                    }
                }
            }
        }
        tracing::info!("accept loop stopped");
        Ok(())
    }

    /// One client connection. Mirrors `main.handle()` stages:
    /// pick SNI -> fake hello -> bind -> failover connect -> register ->
    /// handshake wait (timeout) -> relay. Early errors close + `note_fail`.
    async fn handle(self: Arc<Self>, incoming: TcpStream, _peer: SocketAddr) {
        // Non-blocking semaphore (mirrors `acquire(blocking=False)`).
        let _permit = match self.sem.clone().try_acquire_owned() {
            Ok(p) => p,
            Err(_) => {
                tracing::warn!("connection limit reached, dropping client");
                self.stats.increment_failed();
                return;
            }
        };

        let sni = self.pick_sni();
        if sni.is_empty() {
            tracing::error!("dropping client: no SNI available (FAKE_SNIS empty?)");
            self.stats.increment_failed();
            return;
        }
        // FIX P2: wire decoy is built in engine.rs DPI path; building another
        // hello here was dead code (never sent) with independent randomness,
        // causing stats-vs-wire SNI mismatch. Accounting SNI only.

        let ordered = self.order_endpoints(self.pick_endpoints());
        if ordered.is_empty() {
            tracing::error!("dropping client: no endpoints configured");
            self.stats.increment_failed();
            return;
        }
        let first_key = ep_key(&ordered[0]);

        // FIX(#7): the strategy for this connection. Resolved once here so the
        // scoreboard rows, the DPI burst and the failover budget all agree on
        // what is being tried; the DPI re-resolves the same sticky value.
        let cfg_method =
            BypassMethod::parse(&self.config.bypass_method).unwrap_or(BypassMethod::WrongSeq);
        let strategy = self.resolve_strategy_for_connection(cfg_method);
        let strategy_key = strategy.board_key();

        // Failover connect (fresh socket per attempt; Python reused one fd —
        // same observable behavior, avoids consumed-socket reuse).
        let (outgoing, connected_ep, connect_ms) = match self.try_connect(&ordered).await {
            Some(v) => v,
            None => {
                // Mirrors `note_fail(None, ep_key(first), sni)`.
                // FIX: use the strategy so the scoreboard sees this failure
                // against the thing we actually tried. Previously the config
                // string ("auto") was recorded, which made the auto row
                // unrankable and hid which decoy shape failed.
                self.stats
                    .record_result(&first_key, &sni, false, &strategy_key);
                self.stats.increment_failed();
                self.record_connection_result(cfg_method, false);
                return;
            }
        };
        let cur_key = ep_key(&connected_ep);
        // v3: connect time is the sample the scoreboard ranks on. A strategy
        // that eventually works after a 900 ms stall is measured as the slow
        // row it is, so `auto` can prefer the one that answers in 40 ms.
        self.stats
            .record_latency(&cur_key, &strategy_key, connect_ms);

        // Register under the REAL endpoint (mirrors re-key on failover).
        let method = strategy_key.clone();
        let local: SocketAddr = match outgoing.local_addr() {
            Ok(a) => a,
            Err(e) => {
                tracing::error!(
                    "dropping client for {}: egress local_addr failed: {}",
                    cur_key,
                    e
                );
                self.stats.record_result(&cur_key, &sni, false, &method);
                self.stats.increment_failed();
                return;
            }
        };
        let (local_ip, local_port) = match local {
            SocketAddr::V4(v) => (v.ip().to_string(), v.port()),
            SocketAddr::V6(_) => {
                tracing::error!("dropping client for {}: IPv6 egress unsupported", cur_key);
                self.stats.record_result(&cur_key, &sni, false, &method);
                self.stats.increment_failed();
                return;
            }
        };
        // FIX P4: use the actual socket local IP, not interface_ipv4. On
        // multi-route/VPN the egress IP for this endpoint can differ from the
        // route to endpoints[0]; using iface breaks ConnId<->DpiKey matching
        // (engine now uses WinDivert direction, so IDs must be real).
        let id: ConnId = (
            local_ip.clone(),
            local_port,
            connected_ep.ip.clone(),
            connected_ep.port,
        );
        let _ = local_ip; // (kept for future bind-verify logging)
        let entry = Arc::new(ConnEntry::new(id.clone(), method.clone(), sni.clone()));
        // Injector owns active/total counting once wired; counts here so the
        // dashboard stays correct with passthrough threads.
        self.stats.increment_total();
        self.stats.increment_active();
        // FIX: pre-register was a no-op (immediately removed). Just
        // register the real connection id under the actual connected
        // endpoint, which is what the DPI dispatch uses.
        // (Preserved history: pre-register with FIRST then re-key mirrored
        // main.py two-step, but first_id was removed right after insert.)
        {
            let mut map = self.conns.lock().await;
            map.insert(id.clone(), Arc::clone(&entry));
        }
        // FIX(perf): mirror to the sync-side set for the DPI fast path.
        self.dpi_conns.lock().insert(id.clone());

        // FIX(deadlock): start the relay IMMEDIATELY. The handshake signal
        // (Path B: payload > 0) cannot fire before the client's real
        // ClientHello reaches the server, which is impossible until the
        // relay is running. Run the handshake wait CONCURRENTLY as a
        // quality signal for stats only.
        // (Preserved history: Handshake wait mirrors
        // `wait_for(t2a_event.wait(), TIMEOUT)`; previously gated relay.)
        // Register the relay session BEFORE waiting so it appears live
        // in the Active Connections view.
        // IMPROVE(U5): register the relay session so the sync FFI snapshot
        // sees live connections (kept here, before relay, so the view is live
        // during the concurrent handshake wait).
        let relay_id = id.clone();
        self.active_relays.lock().insert(
            relay_id.clone(),
            ActiveRelay {
                local_port,
                remote: cur_key.clone(),
                method: entry.method.clone(),
                started: Instant::now(),
            },
        );

        // FIX(#3b): arm the drop guard so every exit path (return, panic,
        // cancellation) decrements active and clears the relay row.
        let _active_guard = ActiveGuard {
            stats: Arc::clone(&self.stats),
            relays: Arc::clone(&self.active_relays),
            // FIX(#6): guard also owns the resolved-method cleanup.
            resolved_methods: Arc::clone(&self.resolved_methods),
            intercepted: Arc::clone(&self.intercepted),
            id: relay_id.clone(),
        };

        // FIX(stats): the handshake signal is advisory — record it in a
        // shared flag, but do NOT decide success/fail here. The final
        // verdict comes from the relay outcome below.
        // FIX(deadlock): history preserved — the handshake wait previously
        // gated relay and did stats inline; now it only sets the advisory
        // flag while the relay runs concurrently.
        let hs_ok = Arc::new(AtomicBool::new(false));
        let hs_ok_bg = Arc::clone(&hs_ok);
        let entry_for_hs = Arc::clone(&entry);
        let timeout = Duration::from_secs_f64(self.config.handshake_timeout.clamp(0.5, 10.0));
        let hs_task = tokio::spawn(async move {
            match tokio::time::timeout(timeout, entry_for_hs.wait()).await {
                Ok(Some(true)) => {
                    hs_ok_bg.store(true, Ordering::Relaxed);
                    entry_for_hs.monitor.store(false, Ordering::Relaxed);
                }
                _ => {
                    // Timeout or explicit fail: nothing to record here.
                    // The relay will decide the final verdict.
                }
            }
        });

        // FIX(deadlock): evict the handshake conns entry (the DPI state machine
        // no longer needs it). The spawned task holds its own Arc<ConnEntry>.
        // (Preserved history: handshake done drops table entry before relay,
        // mirrors `monitor=False; pop` before relay loops.)
        self.evict(&id).await;
        // FIX(perf): remove from the sync-side mirror as well.
        // FIX: evict() already removes from dpi_conns; this second remove
        // is a harmless no-op that keeps the prompt-specified symmetry.
        self.dpi_conns.lock().remove(&id);

        // FIX(stats): per-connection byte counters for the relay verdict.
        // The old global-snapshot diff misattributed concurrent traffic;
        // these only move for THIS connection (atomics survive task abort).
        let up_count = Arc::new(AtomicU64::new(0));
        let down_count = Arc::new(AtomicU64::new(0));

        // Bidirectional relay (mirrors two `relay_main_loop` tasks + peer cancel).
        // FIX(deadlock): run the relay unconditionally.
        // FIX(stats): run the relay unconditionally (already the case).
        relay_bidirectional(
            incoming,
            outgoing,
            Arc::clone(&self.stats),
            Arc::clone(&up_count),
            Arc::clone(&down_count),
        )
        .await;

        // FIX(deadlock): relay done — cancel the pending handshake wait if still running.
        hs_task.abort();
        let _ = hs_task.await;

        // FIX(stats): final verdict — bytes this connection moved.
        let up = up_count.load(Ordering::Relaxed);
        let down = down_count.load(Ordering::Relaxed);
        let bytes_moved = up.saturating_add(down);
        // Minimum threshold: a real TLS session will move at least a few
        // hundred bytes. Below that, the endpoint never answered.
        //
        // The DPI's verdict overrides the byte count. This gateway's kill is a
        // 415-byte plaintext `HTTP/1.1 302` sent toward the client, which the
        // byte rule counted as a bypass success — so `auto` ranked destroyed
        // connections next to traversed ones and had nothing to select on.
        let intercepted = self.take_intercepted(&id);
        let relay_ok = connection_verdict(bytes_moved, intercepted);

        // FIX(#6): prefer the DPI-resolved method (real one) over the
        // config string. Falls back to the config string if the DPI
        // never committed to a fake burst.
        let method_for_stats: String = self
            .resolved_methods
            .lock()
            .get(&id)
            .cloned()
            .unwrap_or_else(|| entry.method.clone());

        if relay_ok {
            self.stats
                .record_result(&cur_key, &sni, true, &method_for_stats);
            self.stats.increment_success();
            // Handshake signal may or may not have fired — both are fine.
            let _ = hs_ok.load(Ordering::Relaxed);
            // Client-side completion line: the ONE per-connection [INFO].
            tracing::info!(
                "relay done key=({}, {}, {}, {}) up={} down={} result=ok",
                id.0,
                id.1,
                id.2,
                id.3,
                up,
                down
            );
        } else {
            self.stats
                .record_result(&cur_key, &sni, false, &method_for_stats);
            self.stats.increment_failed();
            let reason = if intercepted {
                "interceptor answered in plaintext, no TLS session".to_string()
            } else {
                format!(
                    "only {} bytes relayed (< {} minimum)",
                    bytes_moved, MIN_RELAY_BYTES
                )
            };
            tracing::warn!(
                "relay done key=({}, {}, {}, {}) up={} down={} result=fail reason={}",
                id.0,
                id.1,
                id.2,
                id.3,
                up,
                down,
                reason
            );
        }

        // FIX(#7): tell the auto state machine about this outcome. No-op
        // unless config is "auto".
        self.record_connection_result(cfg_method, relay_ok);

        // Clean up the per-connection method record for this id.
        self.resolved_methods.lock().remove(&id);

        // FIX(stats): single active decrement for this connection, in both
        // branches. The success path no longer needs a compensating
        // increment_active because we never call finish_success().
        // Relay finished: release the slot (handshake stats already counted).
        // FIX(#3b): explicit decrement_active + active_relays.remove removed;
        // the ActiveGuard above handles both on scope exit (all paths).
        // IMPROVE(U5): always evict the relay record, even on early return
        // paths above this point the insert never happened, so remove is a
        // no-op there; here it prevents stale rows (D2: no leak).
        // (Preserved: guard now performs this eviction.)
        // Permit (`_permit`) drops here (mirrors `conn_sem.release()`).
    }

    /// Per-attempt connect budget: 4x the best latency we have measured,
    /// floored at 800 ms and capped at the old flat 5 s.
    ///
    /// The flat 5 s was the second half of the "some configs give 900 ms"
    /// complaint: with a black-holed endpoint list, every dead endpoint burned
    /// the full budget before failover, so the first working one arrived after
    /// tens of seconds. Once any endpoint has proven it answers quickly, a
    /// connection that has not completed by 4x that time is not going to.
    fn connect_budget(&self) -> Duration {
        let best = self
            .stats
            .best_endpoint_latency()
            .map(|ms| ms * 4.0)
            .unwrap_or(5000.0);
        Duration::from_millis(best.clamp(800.0, 5000.0) as u64)
    }

    /// v3: endpoints the board has already measured sort ahead of unmeasured
    /// ones, so a known-fast anycast IP is tried before a stranger. Untried
    /// endpoints take a neutral 0.5, which keeps them behind a working row and
    /// ahead of a failing one. Round-robin order is preserved within ties.
    fn order_endpoints(&self, mut ordered: Vec<Endpoint>) -> Vec<Endpoint> {
        ordered.sort_by(|a, b| {
            let sa = self.stats.endpoint_score(&ep_key(a)).unwrap_or(0.5);
            let sb = self.stats.endpoint_score(&ep_key(b)).unwrap_or(0.5);
            sb.partial_cmp(&sa).unwrap_or(std::cmp::Ordering::Equal)
        });
        ordered
    }

    /// Try endpoints with a scoreboard-derived per-attempt timeout. Mirrors
    /// `try_connect`. Every skipped/failed attempt logs [WARN] with endpoint +
    /// reason so a dead endpoint list is diagnosable (Q9 item 1).
    /// Returns the stream, the endpoint, and the connect time in ms.
    async fn try_connect(&self, ordered: &[Endpoint]) -> Option<(TcpStream, Endpoint, f64)> {
        for ep in ordered {
            // Last-mile guard (mirrors `sanitize_for_socket`).
            let ip: std::net::IpAddr = match ep.ip.parse() {
                Ok(a) => a,
                Err(_) => {
                    tracing::warn!("connect {} failed: bad IP {:?}", ep_key(ep), ep.ip);
                    continue;
                }
            };
            if ep.port == 0 {
                tracing::warn!("connect {} failed: port 0", ep_key(ep));
                continue;
            }
            let target = SocketAddr::new(ip, ep.port);
            // Fresh socket per attempt, bound to the egress interface.
            let sock = match TcpSocket::new_v4() {
                Ok(s) => s,
                Err(e) => {
                    tracing::warn!("connect {} failed: socket create: {}", ep_key(ep), e);
                    continue;
                }
            };
            // FIX P4: a stale interface_ipv4 must skip this endpoint, not abort
            // all remaining endpoints (previously `ok()?` returned None).
            let bind_addr: SocketAddr = match format!("{}:0", self.interface_ipv4).parse() {
                Ok(a) => a,
                Err(_) => {
                    tracing::warn!(
                        "connect {} failed: bad egress interface {:?}",
                        ep_key(ep),
                        self.interface_ipv4
                    );
                    continue;
                }
            };
            if let Err(e) = sock.bind(bind_addr) {
                tracing::warn!(
                    "connect {} failed: egress bind {}: {}",
                    ep_key(ep),
                    bind_addr,
                    e
                );
                continue;
            }
            // FIX(#2a): pre-seed the outgoing tuple in dpi_conns. The WinDivert
            // capture thread runs concurrently and will see the SYN this
            // socket is about to send; without the seed the fast path cannot
            // distinguish it from foreign traffic to the same anycast IP.
            let seeded_key: Option<ConnId> = match sock.local_addr() {
                Ok(SocketAddr::V4(v4)) => {
                    let k: ConnId = (v4.ip().to_string(), v4.port(), ep.ip.clone(), ep.port);
                    self.dpi_conns.lock().insert(k.clone());
                    Some(k)
                }
                _ => None,
            };
            let budget = self.connect_budget();
            let started = Instant::now();
            match tokio::time::timeout(budget, sock.connect(target)).await {
                Ok(Ok(stream)) => {
                    return Some((stream, ep.clone(), started.elapsed().as_secs_f64() * 1000.0))
                }
                Ok(Err(e)) => {
                    // FIX(#2a): drop the seed if the connect failed.
                    if let Some(k) = seeded_key {
                        self.dpi_conns.lock().remove(&k);
                    }
                    tracing::warn!("connect {} failed: {}", ep_key(ep), e);
                    continue;
                }
                Err(_) => {
                    // FIX(#2a): drop the seed if the connect failed.
                    if let Some(k) = seeded_key {
                        self.dpi_conns.lock().remove(&k);
                    }
                    tracing::warn!(
                        "connect {} failed: timeout ({}ms budget)",
                        ep_key(ep),
                        budget.as_millis()
                    );
                    continue;
                }
            }
        }
        // Visible at INFO (not debug) so Smart Tools / console users see
        // failover exhaustion without --log-level DEBUG. Forwarded to the
        // GUI console by the tracing bridge in main.rs (no raw SNI logged).
        tracing::warn!("all endpoints failed");
        None
    }

    // FIX(dedup): dead note_fail helper removed (the concurrent hs_task
    // inlines its own accounting); ConnEntry.counted went with it.
    async fn evict(&self, id: &ConnId) {
        self.conns.lock().await.remove(id);
        // FIX(perf): keep the sync mirror in lockstep.
        self.dpi_conns.lock().remove(id);
    }

    /// Safety net: evict stale `monitor=false` entries older than max_age.
    /// Mirrors `connection_reaper(interval=60, max_age=120)`.
    pub async fn reaper(self: Arc<Self>, interval: Duration, max_age: Duration) {
        let mut tick = tokio::time::interval(interval);
        tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            tokio::select! {
                _ = self.shutdown.notified() => break,
                _ = tick.tick() => {
                    let now = Instant::now();
                    let mut map = self.conns.lock().await;
                    let before = map.len();
                    map.retain(|_, e| {
                        e.monitor.load(Ordering::Relaxed) || now.duration_since(e.created_at) <= max_age
                    });
                    let reaped = before - map.len();
                    if reaped > 0 {
                        tracing::debug!("reaped {} stale connection(s)", reaped);
                    }
                    // FIX(#3c): mirror cleanup. dpi_conns is the sync-side read set
                    // for the DPI fast path; active_relays drives the U5 view. Both
                    // can accumulate stale rows if the guard path was skipped.
                    // `active_relays` should always be paired with a live `conns`
                    // entry (the guard removes it), so any orphan is a leak.
                    drop(map); // release conns lock before taking dpi_conns / relays
                    {
                        let live: std::collections::HashSet<ConnId> = {
                            let map = self.conns.lock().await;
                            map.keys().cloned().collect()
                        };
                        // Also keep any ConnId that still appears in active_relays as
                        // long as the relay is young (< max_age). We do not have a
                        // started-at here; instead we treat "present in active_relays"
                        // as still-live and only remove dpi_conns entries that are
                        // absent from both.
                        let live_relays: std::collections::HashSet<ConnId> = {
                            self.active_relays.lock().keys().cloned().collect()
                        };
                        let mut dpi = self.dpi_conns.lock();
                        let before = dpi.len();
                        dpi.retain(|k| live.contains(k) || live_relays.contains(k));
                        let reaped = before - dpi.len();
                        if reaped > 0 {
                            tracing::debug!("reaped {} stale dpi_conns entry(ies)", reaped);
                        }
                    }
                }
            }
        }
    }

    /// In-process status tick. The old backend printed one JSON `Snapshot`
    /// line per interval for the GUI pipe; the unified binary polls
    /// `Stats::snapshot()` directly, so this only logs at debug level.
    /// Kept so background-task wiring stays identical.
    #[allow(dead_code)]
    pub async fn reporter(self: Arc<Self>, interval: Duration) {
        let mut tick = tokio::time::interval(interval);
        tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            tokio::select! {
                _ = self.shutdown.notified() => break,
                _ = tick.tick() => {
                    let snap = self.stats.snapshot();
                    tracing::debug!(
                        active = snap.active,
                        total = snap.total,
                        ok = snap.success,
                        fail = snap.failed,
                        "stats tick"
                    );
                }
            }
        }
    }
}

/// Bidirectional copy with per-direction traffic accounting.
/// Mirrors two `relay_main_loop` tasks (`up` clients->net, `down` net->clients)
/// with peer-cancel on EOF/error. `up_count`/`down_count` are the
/// per-connection verdict counters (atomics survive task abort); global
/// `stats` accounting is unchanged. Read errors, write errors and the idle
/// timeout log [WARN] with direction (Q9 item 8); clean EOF stays silent.
async fn relay_bidirectional(
    incoming: TcpStream,
    outgoing: TcpStream,
    stats: Arc<Stats>,
    up_count: Arc<AtomicU64>,
    down_count: Arc<AtomicU64>,
) {
    // FIX(#3a): read-idle timeout (5 minutes). Without it a half-open or
    // keep-alive connection with no traffic pins handle() forever and leaks Active.
    const RELAY_IDLE_SECS: u64 = 300;
    let (mut ri, mut wi) = incoming.into_split();
    let (mut ro, mut wo) = outgoing.into_split();
    let s_up = Arc::clone(&stats);
    let s_down = Arc::clone(&stats);
    // FIX P4: 16k buffers (was 64k x2 per conn, ~25MB churn at 200 conns);
    // still well above MSS, far less allocator pressure.
    // FIX P4: propagate FIN via shutdown(Write) on clean EOF so teardown is
    // graceful instead of RST-prone drop.
    // FIX(#3a): read-idle timeout. A half-open peer or keep-alive
    // with no traffic for RELAY_IDLE_SECS ends the relay so the
    // handler can decrement_active and clear active_relays.
    let mut up = tokio::spawn(async move {
        let mut buf = vec![0u8; 16384];
        let idle = Duration::from_secs(RELAY_IDLE_SECS);
        loop {
            match tokio::time::timeout(idle, ri.read(&mut buf)).await {
                Ok(Ok(0)) => {
                    let _ = wo.shutdown().await;
                    break;
                }
                Ok(Ok(n)) => {
                    s_up.add_traffic(n as u64, 0);
                    up_count.fetch_add(n as u64, Ordering::Relaxed);
                    if let Err(e) = wo.write_all(&buf[..n]).await {
                        tracing::warn!("relay client->endpoint write failed: {}", e);
                        break;
                    }
                }
                Ok(Err(e)) => {
                    tracing::warn!("relay client->endpoint read failed: {}", e);
                    break;
                }
                Err(_) => {
                    tracing::warn!("relay client->endpoint read idle timeout (300s), closing");
                    break;
                }
            }
        }
    });
    // FIX(#3a): read-idle timeout. A half-open peer or keep-alive
    // with no traffic for RELAY_IDLE_SECS ends the relay so the
    // handler can decrement_active and clear active_relays.
    let mut down = tokio::spawn(async move {
        let mut buf = vec![0u8; 16384];
        let idle = Duration::from_secs(RELAY_IDLE_SECS);
        loop {
            match tokio::time::timeout(idle, ro.read(&mut buf)).await {
                Ok(Ok(0)) => {
                    let _ = wi.shutdown().await;
                    break;
                }
                Ok(Ok(n)) => {
                    s_down.add_traffic(0, n as u64);
                    down_count.fetch_add(n as u64, Ordering::Relaxed);
                    if let Err(e) = wi.write_all(&buf[..n]).await {
                        tracing::warn!("relay endpoint->client write failed: {}", e);
                        break;
                    }
                }
                Ok(Err(e)) => {
                    tracing::warn!("relay endpoint->client read failed: {}", e);
                    break;
                }
                Err(_) => {
                    tracing::warn!("relay endpoint->client read idle timeout (300s), closing");
                    break;
                }
            }
        }
    });
    // FIX 3: abort the loser task as soon as one direction ends so half-open
    // sockets and tasks cannot leak under load (mirrors peer_task.cancel()).
    // First direction to finish wins (mirrors peer_task.cancel()).
    tokio::select! {
        _ = &mut up => {
            down.abort();
            let _ = down.await;
        }
        _ = &mut down => {
            up.abort();
            let _ = up.await;
        }
    }
}

#[cfg(test)]
mod auto_state_tests {
    use super::{AutoState, BypassMethod, Strategy};
    use sni_core::stats::Stats;
    use std::time::Instant;

    fn fresh() -> AutoState {
        AutoState {
            current: Strategy::plain(BypassMethod::WrongSeq),
            attempts: 0,
            failures: 0,
            started_at: Instant::now(),
            pending: Vec::new(),
        }
    }

    /// Measure `n` outcomes for `key` so the scoreboard has a row for it.
    fn seed(stats: &Stats, key: &str, ok: u32, fail: u32, latency_ms: Option<f64>) {
        for _ in 0..ok {
            stats.record_result("1.1.1.1:443", "a.com", true, key);
        }
        for _ in 0..fail {
            stats.record_result("1.1.1.1:443", "a.com", false, key);
        }
        if let Some(ms) = latency_ms {
            for _ in 0..(ok + fail).max(1) {
                stats.record_latency("1.1.1.1:443", key, ms);
            }
        }
    }

    #[test]
    fn new_state_has_an_exploration_queue() {
        let st = AutoState::new();
        // One strategy is current, the rest are queued for measurement.
        assert_eq!(st.pending.len(), Strategy::pool().len() - 1);
        assert!(!st.pending.contains(&st.current));
    }

    #[test]
    fn success_is_sticky_until_the_exploration_budget_is_spent() {
        let stats = Stats::new();
        let mut st = fresh();
        st.pending = vec![Strategy::plain(BypassMethod::Padding)];
        // FIX(sticky-auto): a working strategy is kept; the old attempt/time
        // budget abandoned it after 10 connections / 60 s. It is only
        // re-examined every EXPLORE_EVERY connections.
        for _ in 0..(AutoState::EXPLORE_EVERY - 1) {
            assert!(!st.record(&stats, true));
        }
        assert!(st.record(&stats, true));
        assert_eq!(st.attempts, 0);
    }

    #[test]
    fn three_consecutive_failures_rotate() {
        let stats = Stats::new();
        let mut st = fresh();
        assert!(!st.record(&stats, false));
        assert!(!st.record(&stats, false));
        assert!(st.record(&stats, false));
    }

    #[test]
    fn success_resets_consecutive_failures() {
        let stats = Stats::new();
        let mut st = fresh();
        assert!(!st.record(&stats, false));
        assert!(!st.record(&stats, false));
        // A success clears the budget, so two more failures are not enough to
        // rotate (exploration queue is empty here, so nothing else interferes).
        assert!(!st.record(&stats, true));
        assert!(!st.record(&stats, false));
        assert!(!st.record(&stats, false));
        assert!(st.record(&stats, false));
    }

    #[test]
    fn a_slow_working_strategy_gets_replaced() {
        // This is the 900 ms case: it connects, so success-rate ranking kept
        // it forever. Latency alone must be enough to move off it.
        let stats = Stats::new();
        let key = Strategy::plain(BypassMethod::WrongSeq).board_key();
        seed(&stats, &key, 4, 0, Some(1400.0));
        let mut st = fresh();
        st.attempts = AutoState::MIN_LATENCY_SAMPLES - 1;
        assert!(st.record(&stats, true));
        assert_ne!(st.current.board_key(), key);
    }

    #[test]
    fn one_sample_is_not_enough_to_judge_latency() {
        let stats = Stats::new();
        let key = Strategy::plain(BypassMethod::WrongSeq).board_key();
        seed(&stats, &key, 1, 0, Some(1400.0));
        let mut st = fresh();
        st.attempts = 0;
        assert!(!st.record(&stats, true));
    }

    #[test]
    fn exploration_picks_an_unmeasured_strategy_first() {
        let stats = Stats::new();
        seed(&stats, "padding", 9, 0, Some(10.0));
        let mut st = AutoState::new();
        // Pinned, not whatever the shuffled constructor drew: `current` is the
        // measured champion, and the unmeasured row must still win the rotate.
        st.current = Strategy::plain(BypassMethod::Padding);
        st.pending = vec![Strategy::plain(BypassMethod::Fragmented)];
        st.attempts = AutoState::EXPLORE_EVERY - 1;
        assert!(st.record(&stats, true));
        // A proven winner is NOT preferred over a strategy we have never
        // measured — otherwise the board can never be falsified.
        assert_eq!(st.current, Strategy::plain(BypassMethod::Fragmented));
    }

    #[test]
    fn after_exploration_the_board_decides() {
        let stats = Stats::new();
        for s in Strategy::pool() {
            let key = s.board_key();
            if key == "padding" {
                seed(&stats, &key, 6, 0, Some(30.0));
            } else {
                seed(&stats, &key, 1, 5, Some(900.0));
            }
        }
        let mut st = fresh();
        st.current = Strategy::plain(BypassMethod::WrongSeq);
        st.attempts = AutoState::EXPLORE_EVERY;
        st.failures = AutoState::MAX_FAILURES - 1;
        assert!(st.record(&stats, false));
        assert_eq!(st.current, Strategy::plain(BypassMethod::Padding));
    }

    #[test]
    fn rotate_with_an_empty_board_falls_back_to_a_pool_strategy() {
        let stats = Stats::new();
        let mut st = fresh();
        st.rotate(&stats);
        assert!(Strategy::pool().contains(&st.current));
        // An empty board must still move: a rotation that keeps the strategy it
        // just dropped would let a stalled one run forever.
        assert_ne!(st.current, Strategy::plain(BypassMethod::WrongSeq));
        assert_eq!(st.attempts, 0);
        assert_eq!(st.failures, 0);
    }

    #[test]
    fn config_method_maps_to_a_strategy() {
        assert_eq!(
            BypassMethod::SplitHello.into_strategy(),
            Strategy::split_only()
        );
        assert_eq!(
            BypassMethod::Fragmented.into_strategy(),
            Strategy::plain(BypassMethod::Fragmented)
        );
        // `auto` never reaches the wire as a decoy shape.
        assert!(
            !BypassMethod::Auto.into_strategy().decoy.plan_decoy()
                || BypassMethod::Auto.into_strategy().decoy != BypassMethod::Auto
        );
    }
}
