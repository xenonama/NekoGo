//! In-process backend engine for the `sni_engine` cdylib.
//!
//! Moved verbatim from `gui/src/engine.rs` (Slint frontend). The Wails/Go
//! frontend in `gui/` loads this crate at runtime via purego and polls
//! `Stats::snapshot()` through the C ABI in `lib.rs`.
//!
//! DPI dispatch (`dispatch_tcp_packet`) wires the `sni-core` primitives:
//! `parse_ip_tcp`/`tcp_info` -> per-connection `HandshakeState` ->
//! `plan_fake` -> `build_fake_tcp` -> reinject, with
//! `Worker::complete_handshake` signalling the relay engine.
//! QUIC handling: `block` and `spoof` both drop so the browser falls
//! back to TCP (same-length swap lives in a future slice); `passthrough`
//! has no capture thread by construction.

use crate::worker::{ConnId, Worker};
use parking_lot::Mutex as ParkMutex;
use sni_core::{
    config::Config,
    fake_tcp::{
        plan_fake, split_hello, BypassMethod, FakeParams, HandshakeState, InboundAction,
        OutboundAction, Strategy,
    },
    picker::pick_sni,
    stats::Stats,
    tcp::{build_fake_tcp, parse_ip_tcp, tcp_info, tcp_payload},
    tls::{build_fake_client_hello, TlsProfile},
};
use sni_windivert::{Packet, WindivertHandle};
use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::Duration,
};
use tokio::{net::TcpListener, runtime::Handle};

/// Normalized client-first 4-tuple: (client_ip, client_port, server_ip, server_port).
/// The client side is always the egress interface address.
type DpiKey = (String, u16, String, u16);

type DpiMap = Arc<ParkMutex<HashMap<DpiKey, HandshakeState>>>;

/// v3: a decoy burst that must leave later than the packet that triggered it
/// (`FAKE_DELAY`). One feeder thread drains these, so the capture thread never
/// sleeps and never delays unrelated connections.
pub(crate) struct DelayedBurst {
    at: std::time::Instant,
    packets: Vec<Packet>,
}

pub(crate) type DelayTx = std::sync::mpsc::SyncSender<DelayedBurst>;

/// Send queued bursts when they are due. Exits when every sender is gone,
/// which happens once the capture thread has returned.
fn run_delay_feeder(rx: std::sync::mpsc::Receiver<DelayedBurst>, handle: WindivertHandle) {
    loop {
        let burst = match rx.recv_timeout(Duration::from_millis(250)) {
            Ok(b) => b,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => continue,
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => break,
        };
        if let Some(wait) = burst.at.checked_duration_since(std::time::Instant::now()) {
            std::thread::sleep(wait);
        }
        for p in &burst.packets {
            if let Err(e) = handle.send(p) {
                throttled_error(&FAKE_SEND_THROTTLE, "delayed fake send", format!("{}", e));
            }
        }
    }
}

// Rate-limited [ERROR] promotion for the hot capture path (Q9 items 5-6):
// at most one line per tag per second; drops in between are counted and
// noted. Debug-level spam would hide a dead driver; unbounded errors would
// flood the host's line reader.
struct ErrThrottle {
    last: Option<std::time::Instant>,
    dropped: u64,
}

static REINJECT_THROTTLE: ParkMutex<ErrThrottle> = ParkMutex::new(ErrThrottle {
    last: None,
    dropped: 0,
});
static FAKE_SEND_THROTTLE: ParkMutex<ErrThrottle> = ParkMutex::new(ErrThrottle {
    last: None,
    dropped: 0,
});

fn throttled_error(throttle: &ParkMutex<ErrThrottle>, tag: &str, msg: String) {
    note_error(throttle, tag, msg, std::time::Instant::now())
}

/// `throttled_error` with the clock handed in, so the tests can move the
/// window instead of sleeping across it.
fn note_error(throttle: &ParkMutex<ErrThrottle>, tag: &str, msg: String, now: std::time::Instant) {
    let mut t = throttle.lock();
    let window_elapsed = t
        .last
        .map(|l| now.saturating_duration_since(l).as_secs_f64() >= 1.0)
        .unwrap_or(true);
    if !window_elapsed {
        t.dropped += 1;
        return;
    }
    if t.dropped > 0 {
        tracing::error!("{} failed ({} further errors suppressed)", tag, t.dropped);
        t.dropped = 0;
    }
    tracing::error!("{} failed: {}", tag, msg);
    t.last = Some(now);
}

/// Running backend. `shutdown()` stops captures, cancels the accept loop and
/// shuts the runtime down; the runtime/capture threads are reaped on a spare
/// thread so the caller never blocks.
pub struct EngineHandle {
    worker: Arc<Worker>,
    tcp: WindivertHandle,
    quic: Option<WindivertHandle>,
    // FIX #8: IPv6 drop handle (only when ip_mode is "ipv4" or
    // "both"). Drops all IPv6 TCP to the endpoints so the browser
    // falls back to IPv4.
    ipv6: Option<WindivertHandle>,
    threads: Vec<std::thread::JoinHandle<()>>,
    alive: Arc<AtomicBool>,
    runtime_thread: Option<std::thread::JoinHandle<()>>,
}

impl EngineHandle {
    /// Start everything for `cfg`. Blocking briefly (bind + driver open) so
    /// failures can be reported synchronously to the caller.
    /// Logs flow through the global tracing layer into the shared
    /// log buffer (drained by the frontend 1Hz poll). All capture/relay
    /// logic below is unchanged from the Slint build.
    pub fn start(cfg: Config, stats: Arc<Stats>) -> Result<Self, String> {
        let worker = Arc::new(Worker::new(cfg, Arc::clone(&stats)).map_err(|e| e.to_string())?);
        let wcfg = worker.config.clone();
        let bind_addr = format!("{}:{}", wcfg.listen_host, wcfg.listen_port);

        // Driver open first: fail fast with actionable hints (mirrors main.py).
        let tcp_filter = worker.tcp_filter();
        let tcp = WindivertHandle::open(tcp_filter)
            .map_err(|e| format!("WinDivert open failed: {}. Run as Administrator, keep WinDivert.dll + WinDivert64.sys next to the exe.", e))?;

        let quic_mode = wcfg.quic_mode.clone();
        let quic_need = quic_mode == "block" || quic_mode == "spoof";
        // FIX P3: fail-closed QUIC. If the user asked to block/spoof QUIC and
        // the driver can't open, return an error instead of silently
        // continuing TCP-only (which would leak QUIC around the bypass).
        let quic: Option<WindivertHandle> = if quic_need {
            match WindivertHandle::open(worker.quic_filter()) {
                Ok(h) => Some(h),
                Err(e) => {
                    return Err(format!(
                        "WinDivert QUIC open failed (quic_mode={}): {}. Run as Administrator, keep WinDivert.dll + WinDivert64.sys next to the exe.",
                        quic_mode, e
                    ));
                }
            }
        } else {
            None
        };

        // FIX #8: open the IPv6 drop handle when the user wants IPv4-only
        // bypass (or "both"). Failures are non-fatal — log and continue.
        let ip_mode = wcfg.ip_mode.clone();
        let need_ipv6_drop = ip_mode == "ipv4" || ip_mode == "both";
        let ipv6: Option<WindivertHandle> = if need_ipv6_drop {
            match WindivertHandle::open(worker.ipv6_drop_filter()) {
                Ok(h) => {
                    tracing::info!("IPv6 drop handle opened (mode={})", ip_mode);
                    Some(h)
                }
                Err(e) => {
                    tracing::warn!(
                        "IPv6 drop handle failed to open ({}). Continuing with IPv4 only.",
                        e
                    );
                    None
                }
            }
        } else {
            None
        };

        // Runtime for the accept loop + reaper. Built here so we can bind
        // synchronously and return bind errors to the caller.
        // FIX(D): worker_threads(4) exactly per spec (accept loop + reaper +
        // relay tasks); capture threads stay on their own blocking threads.
        let rt = tokio::runtime::Builder::new_multi_thread()
            .enable_all()
            .thread_name("sni-tokio")
            .worker_threads(4)
            .max_blocking_threads(2)
            .build()
            .map_err(|e| format!("tokio runtime failed: {}", e))?;
        let rt_handle = rt.handle().clone();

        let listener = rt
            .block_on(TcpListener::bind(&bind_addr))
            .map_err(|e| format!("cannot bind {}: {}", bind_addr, e))?;

        tracing::info!(
            "listening on {} (method={}, {} endpoint(s), {} fake SNI(s))",
            bind_addr,
            wcfg.bypass_method,
            wcfg.endpoints.len(),
            wcfg.fake_snis.len()
        );

        let alive = Arc::new(AtomicBool::new(true));
        let mut threads = Vec::new();

        // ---- v3: FAKE_DELAY feeder thread ----
        // Bounded so a stalled driver cannot make memory grow without limit;
        // when the queue is full the capture thread sends the burst itself
        // rather than dropping the decoy.
        let (delay_tx, delay_rx) = std::sync::mpsc::sync_channel::<DelayedBurst>(64);
        if wcfg.fake_delay > 0.0 {
            let fh = tcp.clone();
            threads.push(
                std::thread::Builder::new()
                    .name("sni-fake-delay".into())
                    .spawn(move || run_delay_feeder(delay_rx, fh))
                    .map_err(|e| format!("cannot spawn fake-delay thread: {}", e))?,
            );
        } else {
            // Nothing consumes the queue when the knob is off; drop the
            // receiver so a stray try_send fails fast instead of buffering.
            drop(delay_rx);
        }

        // ---- TCP capture thread (blocking Recv + DPI dispatch) ----
        {
            let h = tcp.clone();
            let w = Arc::clone(&worker);
            let rt_h = rt_handle.clone();
            let dpi: DpiMap = Arc::new(ParkMutex::new(HashMap::new()));
            let method = BypassMethod::parse(&wcfg.bypass_method).unwrap_or(BypassMethod::WrongSeq);
            let params = FakeParams::new(wcfg.seq_overlap, wcfg.padding_size);
            let profile = TlsProfile::parse(&wcfg.tls_fingerprint).unwrap_or(TlsProfile::Legacy);
            let snis = wcfg.fake_snis.clone();
            let iface = worker.interface_ipv4.clone();
            let fake_delay = wcfg.fake_delay;
            let delay = delay_tx.clone();
            threads.push(
                std::thread::Builder::new()
                    .name("windivert-tcp".into())
                    .spawn(move || {
                        h.run(|pkt| {
                            dispatch_tcp_packet(
                                &h, &pkt, &w, &rt_h, &dpi, method, &params, profile, &snis, &iface,
                                fake_delay, &delay,
                            );
                        });
                    })
                    .map_err(|e| format!("cannot spawn capture thread: {}", e))?,
            );
        }

        // ---- QUIC capture thread (block/spoof only) ----
        if let Some(qh) = quic.clone() {
            threads.push(
                std::thread::Builder::new()
                    .name("windivert-quic".into())
                    .spawn(move || {
                        qh.run(|_pkt| {
                            // block + spoof (no SNI-swap yet): drop so the
                            // browser falls back to TCP instead of leaking.
                            // No Trojan/Xray passthrough exists in this
                            // build; with passthrough there is no thread
                            // by construction.
                        });
                    })
                    .map_err(|e| format!("cannot spawn QUIC thread: {}", e))?,
            );
        }

        // ---- IPv6 drop thread (ipv4 / both modes) ----
        // FIX #8: drop IPv6 silently. Not reinjecting means WinDivert
        // swallows the packet, forcing the browser to retry on IPv4.
        if let Some(ih) = ipv6.clone() {
            threads.push(
                std::thread::Builder::new()
                    .name("windivert-ipv6".into())
                    .spawn(move || {
                        ih.run(|_pkt| {
                            // FIX #8: drop IPv6 silently. Not reinjecting
                            // means WinDivert swallows the packet, forcing
                            // the browser to retry on IPv4.
                        });
                    })
                    .map_err(|e| format!("cannot spawn IPv6 thread: {}", e))?,
            );
        }

        // ---- Tokio thread: reaper + accept loop ----
        let w2 = Arc::clone(&worker);
        let runtime_thread = std::thread::Builder::new()
            .name("sni-runtime".into())
            .spawn(move || {
                rt.block_on(async move {
                    let w3 = Arc::clone(&w2);
                    let reaper = tokio::spawn(async move {
                        w3.reaper(Duration::from_secs(60), Duration::from_secs(120))
                            .await
                    });
                    let res = w2.run(listener).await;
                    reaper.abort();
                    if let Err(e) = res {
                        tracing::warn!("accept loop ended: {}", e);
                    }
                });
            })
            .map_err(|e| format!("cannot spawn runtime thread: {}", e))?;

        Ok(Self {
            worker,
            tcp,
            quic,
            // FIX #8: IPv6 drop handle.
            ipv6,
            threads,
            alive,
            runtime_thread: Some(runtime_thread),
        })
    }

    pub fn is_running(&self) -> bool {
        self.alive.load(Ordering::Relaxed)
    }

    /// Live relay sessions for the U5 Active Connections view.
    /// Delegates to the worker's sync snapshot (no runtime needed).
    pub fn active_connections(&self) -> Vec<crate::worker::ActiveConn> {
        self.worker.snapshot_active()
    }

    /// Signal shutdown and reap threads off the caller thread.
    /// Also resets shared traffic counters so the next Start begins at zero
    /// (the frontend calls `Stats::reset()` too; double-reset is harmless).
    pub fn shutdown(&mut self) {
        if !self.alive.swap(false, Ordering::Relaxed) {
            return;
        }
        self.tcp.stop();
        if let Some(q) = self.quic.take() {
            q.stop();
        }
        // FIX #8: stop the IPv6 drop handle.
        if let Some(h) = self.ipv6.take() {
            h.stop();
        }
        self.worker.request_shutdown();
        // Reset traffic + scoreboard so stale values never flash on next Start.
        self.worker.stats.reset();
        // In tokio 1.40, Handle does not have shutdown_background().
        // The runtime thread will exit naturally when the accept loop and
        // reaper finish because we called worker.request_shutdown().
        let mut threads = std::mem::take(&mut self.threads);
        if let Some(rt) = self.runtime_thread.take() {
            threads.push(rt);
        }
        std::thread::spawn(move || {
            for h in threads {
                let _ = h.join();
            }
        });
    }
}

impl Drop for EngineHandle {
    fn drop(&mut self) {
        self.shutdown();
    }
}

/// One captured TCP packet: DPI state machine + fake injection + reinject.
///
/// Never panics and never kills the capture loop (mirrors the Python
/// per-packet guard): unparsable packets are forwarded untouched.
#[allow(clippy::too_many_arguments)]
fn dispatch_tcp_packet(
    handle: &WindivertHandle,
    pkt: &Packet,
    worker: &Arc<Worker>,
    rt: &Handle,
    dpi: &DpiMap,
    method: BypassMethod,
    params: &FakeParams,
    profile: TlsProfile,
    snis: &[String],
    iface: &str,
    fake_delay: f64,
    delay_tx: &DelayTx,
) {
    let raw = pkt.bytes();
    let reinject = || {
        if let Err(e) = handle.send(pkt) {
            throttled_error(
                &REINJECT_THROTTLE,
                "reinject",
                format!("tcp reinject failed: {}", e),
            );
        }
    };

    let (ip, _, ip_hlen, tcp_hlen) = match parse_ip_tcp(raw) {
        Some(v) => v,
        None => {
            reinject();
            return;
        }
    };
    if raw.len() < ip_hlen + 4 {
        reinject();
        return;
    }
    let t = ip_hlen;
    let sport = u16::from_be_bytes([raw[t], raw[t + 1]]);
    let dport = u16::from_be_bytes([raw[t + 2], raw[t + 3]]);
    let src_ip = format!("{}.{}.{}.{}", ip.src[0], ip.src[1], ip.src[2], ip.src[3]);
    let dst_ip = format!("{}.{}.{}.{}", ip.dst[0], ip.dst[1], ip.dst[2], ip.dst[3]);

    // FIX P4: use WinDivert direction when known; IP compare breaks on
    // VPN / multi-route where the socket local IP != interface_ipv4.
    let outbound = match pkt.direction {
        sni_windivert::Direction::Outbound => true,
        sni_windivert::Direction::Inbound => false,
        sni_windivert::Direction::Unknown => src_ip == iface,
    };
    // FIX(diag): per-packet entry log (direction + endpoints + length).
    // FIX(perf): demoted to debug — info here fires for EVERY packet
    // (hundreds of thousands of lines/sec) and saturates the log buffer.
    tracing::debug!(
        "DPI pkt dir={} {}:{} -> {}:{} len={}",
        if outbound { "OUT" } else { "IN" },
        src_ip,
        sport,
        dst_ip,
        dport,
        raw.len()
    );
    // Normalized client-first key; doubles as the Worker's ConnId.
    let key: DpiKey = if outbound {
        (src_ip, sport, dst_ip, dport)
    } else {
        (dst_ip, dport, src_ip, sport)
    };

    // FIX(perf): tcp_info moved UP so the fast-path guard below can test
    // is_handshake_pkt before touching any DPI state.
    let info = match tcp_info(raw) {
        Some(i) => i,
        None => {
            reinject();
            return;
        }
    };

    // FIX(perf): only run the DPI state machine for tuples that belong
    // to a connection the Worker actually created (client side matched
    // by (client_ip, client_port, endpoint_ip, endpoint_port)). Any
    // other traffic that happens to share the endpoint IP is forwarded
    // untouched — this prevents the DPI filter from acting on the
    // system's normal Cloudflare traffic.
    // FIX(perf): sync check via active_relays (parking_lot, no runtime
    // needed). `conns` is a tokio Mutex and is evicted before relay, so
    // it cannot be used here.
    // FIX(perf): fast-path guard. The DPI filter matches traffic by
    // (client IP, endpoint IP, port 443) and can capture unrelated
    // system traffic when the endpoint is a shared anycast IP (e.g.
    // Cloudflare). Only run the state machine for tuples we actually
    // own.
    // FIX: `dpi_conns` mirrors `conns` (short-lived: evicted before relay),
    // so a ClientHello sent after evict would miss it. Also accept
    // `is_known_relay` (active_relays, lives for the whole relay) as a
    // fallback — otherwise the bypass silently disables. Either hit means
    // "our connection"; anything else is forwarded untouched.
    // FIX(#2b): after FIX(#2a) the outgoing tuple is seeded in
    // dpi_conns before the SYN leaves the socket, so we no longer
    // need to exempt SYN / SYN-ACK / RST / FIN. Only process tuples
    // the Worker actually owns.
    let known = worker
        .dpi_conns
        .lock()
        .contains(&(key.0.clone(), key.1, key.2.clone(), key.3));
    let known = known || worker.is_known_relay(&key);
    if !known {
        reinject();
        return;
    }

    // RST/FIN: connection is dead — drop DPI state, wake the waiter so the
    // relay task doesn't linger until HANDSHAKE_TIMEOUT.
    // FIX: RST/FIN before fake = real failure; after fake = server
    // rejecting our old-seq segment, which is EXPECTED. Do not fail
    // the connection in that case — let the relay continue; the real
    // ClientHello may still succeed.
    if info.rst || info.fin {
        let should_fail = {
            let map = dpi.lock();
            map.get(&key).map(|st| !st.fake_sent).unwrap_or(true)
        };
        if should_fail {
            dpi.lock().remove(&key);
            let conn: ConnId = (key.0.clone(), key.1, key.2.clone(), key.3);
            // FIX(perf): spawn instead of block_on — the capture thread
            // must not stall waiting for the runtime.
            let w = Arc::clone(worker);
            rt.spawn(async move {
                w.complete_handshake(&conn, false).await;
            });
        }
        reinject();
        return;
    }

    if outbound {
        enum Next {
            Forward,
            /// Decoy burst, sent after (or, when delayed, instead of) the
            /// original. The original still goes out: decoys live in old-seq
            /// space and the real handshake must continue.
            FakeBurst(Vec<Vec<u8>>, bool),
            /// v3: the client's REAL ClientHello, segmented. The original is
            /// suppressed — reinjecting it would hand DPI the whole hostname.
            Split(Vec<Vec<u8>>),
            Fail,
        }
        // FIX P4: narrow DPI lock scope — only touch state under lock,
        // build fake bytes outside so other connections don't head-of-line
        // block on pick_sni / ClientHello / checksums.
        // FIX P0: bound DpiMap so a Success-never-arrives regression can't
        // grow it without limit (removal still happens on Success/Fail/RST).
        enum OutNext {
            Forward,
            Fail,
            NeedFake(u32, Strategy),
            /// Payload the splitter must take apart, plus the seq it carries.
            NeedSplit(Vec<u8>, u32, Strategy),
        }
        let out_next: OutNext = {
            let mut map = dpi.lock();
            if map.len() > 4096 {
                map.clear();
            }
            // FIX(#7): ask the Worker for the sticky strategy when config is
            // "auto". For a concrete config this returns the same strategy
            // every time, so behaviour is unchanged.
            let conn_strategy = worker.resolve_strategy_for_connection(method);
            let st = map
                .entry(key.clone())
                .or_insert_with(|| HandshakeState::new(conn_strategy));
            match st.on_outbound(info) {
                OutboundAction::Reinject => OutNext::Forward,
                OutboundAction::TriggerFake => {
                    let syn = st.syn_seq.unwrap_or_else(|| info.seq.wrapping_sub(1));
                    let s = st.strategy;
                    OutNext::NeedFake(syn, s)
                }
                OutboundAction::MaybeSplitHello => {
                    let start = (ip_hlen + tcp_hlen).min(raw.len());
                    let payload = raw[start..].to_vec();
                    OutNext::NeedSplit(payload, info.seq, st.strategy)
                }
                OutboundAction::Unexpected(msg) => {
                    // Post-fake data (e.g. relay bytes on the same 4-tuple)
                    // must NOT fail the connection — forward quietly. Only
                    // genuine handshake violations fail early.
                    if st.fake_sent {
                        tracing::debug!("post-fake outbound (forwarding): {}", msg);
                        OutNext::Forward
                    } else {
                        tracing::debug!("unexpected outbound (failing): {}", msg);
                        map.remove(&key);
                        OutNext::Fail
                    }
                }
            }
        };
        let next = match out_next {
            OutNext::Forward => Next::Forward,
            OutNext::Fail => Next::Fail,
            OutNext::NeedSplit(payload, seq, strategy) => {
                let segs = match split_hello(seq, &payload) {
                    Some(s) => s,
                    // Not a handshake record (HTTP, app data) — forward it.
                    None => return reinject(),
                };
                let ident = u16::from_be_bytes([raw[4], raw[5]]);
                let mut built = Vec::with_capacity(segs.len());
                for s in &segs {
                    if let Some(bytes) = build_fake_tcp(
                        raw,
                        s.seq,
                        &s.payload,
                        s.psh,
                        ident.wrapping_add(s.ident_plus),
                        None,
                    ) {
                        built.push(bytes);
                    }
                }
                if built.len() < segs.len() {
                    // A segment failed to build: sending a partial hello would
                    // hang the handshake. Prefer correctness over evasion.
                    tracing::debug!(
                        "hello split build incomplete ({}/{}), forwarding original",
                        built.len(),
                        segs.len()
                    );
                    Next::Forward
                } else {
                    tracing::debug!(
                        "DPI: real ClientHello split into {} segment(s), strategy={}",
                        built.len(),
                        strategy.board_key()
                    );
                    worker.record_resolved_strategy(
                        &(key.0.clone(), key.1, key.2.clone(), key.3),
                        &strategy.board_key(),
                    );
                    Next::Split(built)
                }
            }
            OutNext::NeedFake(syn, strategy) => {
                // Decoy ClientHello with a random SNI from the pool. Strategies
                // that carry no decoy (`hello_split`) skip this and rely on
                // segmenting the real hello below.
                let sni = pick_sni(snis).unwrap_or_else(|| "example.com".to_string());
                let hello = build_fake_client_hello(sni.as_bytes(), profile);
                // FIX P2: empty hello (SNI too long) must not emit a bare ACK;
                // forward the original so bypass doesn't silently disable.
                if hello.is_empty() {
                    tracing::debug!("empty fake hello (SNI too long), forwarding");
                    Next::Forward
                } else {
                    let segs = plan_fake(strategy.decoy, syn, &hello, params);
                    // FIX(#6): tell the worker which strategy the DPI committed
                    // to. Stats rank the strategy that went on the wire, not
                    // the useless "auto" config string.
                    {
                        let conn: ConnId = (key.0.clone(), key.1, key.2.clone(), key.3);
                        worker.record_resolved_strategy(&conn, &strategy.board_key());
                    }
                    let ident = u16::from_be_bytes([raw[4], raw[5]]);
                    let ttl = raw[8];
                    let mut built = Vec::with_capacity(segs.len());
                    for s in &segs {
                        // FIX P1: wrong_seq_ttl ttl-1 never expires (64->63 still
                        // reaches server). Use low TTL so local DPI sees it but
                        // it dies before the server (classic TTL trick).
                        let ttl_ov = if s.ttl_decrement {
                            Some(ttl.clamp(1, 8))
                        } else {
                            None
                        };
                        if let Some(bytes) = build_fake_tcp(
                            raw,
                            s.seq,
                            &s.payload,
                            s.psh,
                            ident.wrapping_add(s.ident_plus),
                            ttl_ov,
                        ) {
                            built.push(bytes);
                        }
                    }
                    // Mark sent under lock (entry must still exist; TriggerFake
                    // created it above).
                    {
                        let mut map = dpi.lock();
                        if let Some(st) = map.get_mut(&key) {
                            st.mark_fake_sent();
                        }
                    }
                    if built.is_empty() {
                        if !strategy.decoy.plan_decoy() {
                            // hello_split deliberately sends no decoy burst; the
                            // real hello gets segmented on the next packet.
                            tracing::debug!(
                                "no decoy for strategy={} (real-hello split will follow)",
                                strategy.board_key()
                            );
                        } else {
                            // Hardening: every segment failed to build (malformed
                            // template/packet). Forwarding without ANY fake means
                            // the bypass is off for this connection — say so.
                            tracing::warn!(
                                "fake burst build failed for key={:?} (forwarding original, bypass off)",
                                key
                            );
                        }
                        Next::Forward
                    } else {
                        Next::FakeBurst(built, strategy.split_real)
                    }
                }
            }
        };
        match next {
            Next::Forward => reinject(),
            Next::Split(blobs) => {
                // The original is NOT reinjected: these segments are it, same
                // bytes, same sequence space, just in more packets.
                for b in &blobs {
                    let fp = pkt.with_raw(b.clone());
                    if let Err(e) = handle.send(&fp) {
                        throttled_error(
                            &FAKE_SEND_THROTTLE,
                            "split send",
                            format!("hello split send failed: {}", e),
                        );
                        // A segment failed: send the intact original so the
                        // handshake still completes. Re-transmitting bytes the
                        // server already has is harmless (in-window overlap is
                        // dropped), whereas a missing tail hangs the client.
                        reinject();
                        return;
                    }
                }
                std::thread::yield_now();
            }
            Next::FakeBurst(blobs, split_real) => {
                // FIX(diag): fake burst size/method visibility.
                // FIX(perf): demoted to debug to avoid per-handshake info spam.
                tracing::debug!(
                    "DPI: sending {} fake segment(s) method={} split_real={}",
                    blobs.len(),
                    method.as_str(),
                    split_real
                );
                // v3: FAKE_DELAY is served by the feeder thread, never by a
                // sleep here — a stall on the capture thread held every other
                // connection's packets, which is what made a working config
                // feel like a 900 ms one.
                let mut send_now = fake_delay <= 0.0;
                if !send_now {
                    let packets: Vec<Packet> =
                        blobs.iter().map(|b| pkt.with_raw(b.clone())).collect();
                    let burst = DelayedBurst {
                        at: std::time::Instant::now()
                            + Duration::from_secs_f64(fake_delay.clamp(0.0, 5.0)),
                        packets,
                    };
                    // Queue full or shutting down: losing the delay is better
                    // than losing the decoy, so send it now instead.
                    if delay_tx.try_send(burst).is_err() {
                        send_now = true;
                    }
                }
                if send_now {
                    for b in &blobs {
                        let fp = pkt.with_raw(b.clone());
                        if let Err(e) = handle.send(&fp) {
                            throttled_error(
                                &FAKE_SEND_THROTTLE,
                                "fake send",
                                format!("fake send failed: {}", e),
                            );
                        }
                    }
                }
                reinject(); // original follows the fakes (wrong_seq primitive)
                            // Cooperative yield: injection bursts are hot paths; yielding
                            // avoids starving the 2-thread Tokio runtime.
                std::thread::yield_now();
            }
            Next::Fail => {
                let conn: ConnId = (key.0.clone(), key.1, key.2.clone(), key.3);
                // FIX(perf): spawn instead of block_on — the capture thread
                // must not stall waiting for the runtime.
                let w = Arc::clone(worker);
                rt.spawn(async move {
                    w.complete_handshake(&conn, false).await;
                });
                reinject();
            }
        }
    } else {
        enum NextIn {
            Forward,
            Succeed,
            Fail,
            /// The interceptor answered in plaintext. Same wake-up as `Fail`,
            /// plus the flag the relay verdict reads, because by then the
            /// `ConnEntry` this thread could signal is already evicted.
            Intercept,
        }
        let next = {
            let mut map = dpi.lock();
            // FIX(#7): ask the Worker for the sticky strategy when config is
            // "auto". For a concrete config this returns the same strategy
            // every time, so behaviour is unchanged.
            let conn_strategy = worker.resolve_strategy_for_connection(method);
            let st = map
                .entry(key.clone())
                .or_insert_with(|| HandshakeState::new(conn_strategy));
            // The payload is what separates "a TLS endpoint answered" from
            // "the interceptor served its kill page on 443"; TcpInfo alone
            // cannot, and scoring the kill page as a success kept dead
            // strategies in the pool.
            let inbound_payload = tcp_payload(raw).unwrap_or(&[]);
            match st.on_inbound(info, inbound_payload) {
                InboundAction::Reinject => NextIn::Forward,
                InboundAction::Success => {
                    // FIX(diag): visible success path (pure ACK vs ServerHello payload).
                    tracing::info!("DPI: inbound SUCCESS key={:?}", key);
                    map.remove(&key);
                    NextIn::Succeed
                }
                InboundAction::Intercept(msg) => {
                    tracing::info!("DPI: inbound INTERCEPT key={:?}: {}", key, msg);
                    map.remove(&key);
                    NextIn::Intercept
                }
                InboundAction::Unexpected(msg) => {
                    if st.fake_sent {
                        // FIX(diag): inbound UNEXPECTED after fake_sent (forward path).
                        tracing::info!(
                            "DPI: inbound UNEXPECTED fake_sent={}: {}",
                            st.fake_sent,
                            msg
                        );
                        tracing::debug!("post-fake inbound (forwarding): {}", msg);
                        NextIn::Forward
                    } else {
                        // FIX(diag): inbound UNEXPECTED before fake_sent (fail path).
                        // The no-syn-sent flood (capture race vs our own egress
                        // socket, retransmitted SYN-ACKs, foreign traffic to
                        // the same anycast IPs) is DEBUG-only; [INFO] is
                        // reserved for lifecycle + per-connection completion.
                        // Keep the DEBUG text identical.
                        if msg.contains("no syn sent") {
                            tracing::debug!(
                                "DPI: inbound UNEXPECTED fake_sent={}: {}",
                                st.fake_sent,
                                msg
                            );
                        } else {
                            tracing::info!(
                                "DPI: inbound UNEXPECTED fake_sent={}: {}",
                                st.fake_sent,
                                msg
                            );
                        }
                        tracing::debug!("unexpected inbound (failing): {}", msg);
                        map.remove(&key);
                        NextIn::Fail
                    }
                }
            }
        };
        match next {
            NextIn::Forward => reinject(),
            NextIn::Succeed => {
                reinject();
                let conn: ConnId = (key.0.clone(), key.1, key.2.clone(), key.3);
                // FIX(perf): spawn instead of block_on — the capture thread
                // must not stall waiting for the runtime.
                let w = Arc::clone(worker);
                rt.spawn(async move {
                    w.complete_handshake(&conn, true).await;
                });
            }
            NextIn::Intercept => {
                let conn: ConnId = (key.0.clone(), key.1, key.2.clone(), key.3);
                // Marking is a sync parking_lot insert, safe on the capture
                // thread; only the handshake wake-up needs the runtime.
                worker.mark_intercepted(&conn);
                let w = Arc::clone(worker);
                rt.spawn(async move {
                    w.complete_handshake(&conn, false).await;
                });
                reinject();
            }
            NextIn::Fail => {
                let conn: ConnId = (key.0.clone(), key.1, key.2.clone(), key.3);
                // FIX(perf): spawn instead of block_on — the capture thread
                // must not stall waiting for the runtime.
                let w = Arc::clone(worker);
                rt.spawn(async move {
                    w.complete_handshake(&conn, false).await;
                });
                reinject();
            }
        }
    }
}

#[cfg(test)]
mod throttle_tests {
    use super::{note_error, ErrThrottle};
    use parking_lot::Mutex as ParkMutex;
    use std::time::{Duration, Instant};

    // A local throttle plus a handed-in clock: the old version slept 1.1 s and
    // asserted against wall time, which failed whenever a loaded machine
    // stretched the gap between the two calls past the 1 s window.
    fn fresh() -> ParkMutex<ErrThrottle> {
        ParkMutex::new(ErrThrottle {
            last: None,
            dropped: 0,
        })
    }

    #[test]
    fn first_error_arms_the_window_and_suppresses_nothing() {
        let th = fresh();
        let t0 = Instant::now();
        note_error(&th, "reinject", "one".to_string(), t0);
        let g = th.lock();
        assert_eq!(g.dropped, 0);
        assert_eq!(g.last, Some(t0));
    }

    #[test]
    fn second_error_inside_window_is_counted() {
        let th = fresh();
        let t0 = Instant::now();
        note_error(&th, "reinject", "one".to_string(), t0);
        note_error(
            &th,
            "reinject",
            "two".to_string(),
            t0 + Duration::from_millis(900),
        );
        let g = th.lock();
        assert_eq!(g.dropped, 1);
        // The suppressed error must not move the window start.
        assert_eq!(g.last, Some(t0));
    }

    #[test]
    fn window_elapse_resets_drop_counter() {
        let th = fresh();
        let t0 = Instant::now();
        note_error(&th, "fake send", "one".to_string(), t0);
        for i in 0..5 {
            note_error(
                &th,
                "fake send",
                format!("suppressed {}", i),
                t0 + Duration::from_millis(100 + i * 100),
            );
        }
        assert_eq!(th.lock().dropped, 5);
        // Past the window the next error prints and the counter is cleared.
        note_error(
            &th,
            "fake send",
            "three".to_string(),
            t0 + Duration::from_millis(1100),
        );
        let g = th.lock();
        assert_eq!(g.dropped, 0);
        assert_eq!(g.last, Some(t0 + Duration::from_millis(1100)));
    }
}
