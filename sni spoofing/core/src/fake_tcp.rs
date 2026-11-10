//! Bypass methods + pure split math + fake planner + handshake state machine.
//!
//! Replaces `fake_tcp.py`:
//! - enums + `split_plan`/`overlap_plan`/SNI helpers (pure, tested)
//! - `plan_fake` (pure `fake_send_thread` branching — no I/O, fully testable)
//! - `split_hello` (segmentation of the client's REAL ClientHello, the only
//!   primitive that hides the true SNI from a DPI rather than feeding it a decoy)
//! - `HandshakeState` (pure `on_outbound/inbound_packet` validation)
//! - Wire I/O (`WinDivert send`, delays, executor pool) arrives in Phase 3.
//!
//! All sequence arithmetic uses `wrapping_*` (= Python `& 0xFFFFFFFF`).

use crate::tcp::TcpInfo;
use rand::seq::SliceRandom;
use rand::Rng;

/// Every accepted `BYPASS_METHOD` value. `double_sni` and `delayed_retry` are
/// gone (they were byte-identical to `wrong_seq` and a fixed 1.5 s stall);
/// `config::migrate` rewrites saved configs that still name them.
pub const SUPPORTED_METHODS: &[&str] = &[
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

/// Methods that emit something on the wire (everything but `auto`).
pub const REAL_METHODS: &[&str] = &[
    "wrong_seq",
    "wrong_seq_ttl",
    "split_seq",
    "fragmented",
    "padding",
    "hello_split",
    "hostfakesplit",
    "fakedsplit",
];

/// Decoy shapes `auto` may rotate through. `SplitHello` is excluded — it is a
/// complete strategy on its own (no decoy) and `auto` reaches it through the
/// `decoyless` slot of its own pool.
pub const DECOY_METHODS: &[BypassMethod] = &[
    BypassMethod::WrongSeq,
    BypassMethod::WrongSeqTtl,
    BypassMethod::SplitSeq,
    BypassMethod::Fragmented,
    BypassMethod::Padding,
    BypassMethod::HostFakeSplit,
    BypassMethod::FakeSplit,
];

/// Wire method. `Auto` never reaches the wire: `Strategy::random` expands it
/// into one of the concrete strategies before the first packet of a
/// connection is processed.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum BypassMethod {
    Auto,
    WrongSeq,
    WrongSeqTtl,
    SplitSeq,
    Fragmented,
    Padding,
    SplitHello,
    HostFakeSplit,
    FakeSplit,
}

impl BypassMethod {
    pub fn parse(name: &str) -> Result<Self, String> {
        match name.trim() {
            "auto" => Ok(Self::Auto),
            "wrong_seq" => Ok(Self::WrongSeq),
            "wrong_seq_ttl" => Ok(Self::WrongSeqTtl),
            "split_seq" => Ok(Self::SplitSeq),
            "fragmented" => Ok(Self::Fragmented),
            "padding" => Ok(Self::Padding),
            "hello_split" => Ok(Self::SplitHello),
            "hostfakesplit" => Ok(Self::HostFakeSplit),
            "fakedsplit" => Ok(Self::FakeSplit),
            other => Err(format!(
                "unsupported bypass method: {:?} (expected one of {:?})",
                other, SUPPORTED_METHODS
            )),
        }
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Auto => "auto",
            Self::WrongSeq => "wrong_seq",
            Self::WrongSeqTtl => "wrong_seq_ttl",
            Self::SplitSeq => "split_seq",
            Self::Fragmented => "fragmented",
            Self::Padding => "padding",
            Self::SplitHello => "hello_split",
            Self::HostFakeSplit => "hostfakesplit",
            Self::FakeSplit => "fakedsplit",
        }
    }

    /// Does this method emit a decoy burst? `SplitHello` emits none — its
    /// whole evasion is `split_real`, so it carries no decoy shape.
    pub fn plan_decoy(&self) -> bool {
        !matches!(self, Self::Auto | Self::SplitHello)
    }

    /// Concrete strategy for an explicit (non-auto) config value.
    ///
    /// `Auto` collapses to its default shape here; the runtime auto path uses
    /// `AutoState` + `Strategy::random`, not this function.
    pub fn into_strategy(&self) -> Strategy {
        match self {
            Self::Auto => Strategy::plain(Self::WrongSeq),
            Self::SplitHello => Strategy::split_only(),
            other => Strategy::plain(*other),
        }
    }
}

/// `resolve_method("auto")` parity: a concrete decoy shape for a name.
pub fn resolve_method(name: &str) -> Result<BypassMethod, String> {
    BypassMethod::parse(name).map(|m| m.into_strategy().decoy)
}

/// One DPI-evasion attempt: which decoy burst to inject, and whether the
/// client's real ClientHello is segmented on its way out.
///
/// Two independent axes, so a config that used to offer 9 hand-picked methods
/// now offers one (`auto`) and lets the scoreboard choose among
/// `7 decoys x {split, no-split} + decoyless-split` = 15 strategies.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Strategy {
    pub decoy: BypassMethod,
    pub split_real: bool,
}

impl Strategy {
    /// Decoy burst only, real ClientHello forwarded whole (the pre-v3 model).
    pub fn plain(decoy: BypassMethod) -> Self {
        Self {
            decoy,
            split_real: false,
        }
    }

    /// No decoy at all: the real ClientHello is the only hello on the wire and
    /// it is segmented, so no single segment carries a matchable hostname.
    pub fn split_only() -> Self {
        Self {
            decoy: BypassMethod::SplitHello,
            split_real: true,
        }
    }

    /// Scoreboard key. `auto` ranks strategies, not method names, so a decoy
    /// that only works when the hello is also split gets its own row.
    pub fn board_key(&self) -> String {
        if self.split_real && self.decoy.plan_decoy() {
            format!("{}+split", self.decoy.as_str())
        } else {
            self.decoy.as_str().to_string()
        }
    }

    /// Every strategy `auto` may pick.
    pub fn pool() -> Vec<Strategy> {
        let mut out = Vec::with_capacity(DECOY_METHODS.len() * 2 + 1);
        for &d in DECOY_METHODS {
            out.push(Strategy::plain(d));
            out.push(Strategy {
                decoy: d,
                split_real: true,
            });
        }
        out.push(Strategy::split_only());
        out
    }

    /// The pool in a fresh random order — the exploration queue for `auto`.
    pub fn pool_shuffled() -> Vec<Strategy> {
        let mut out = Self::pool();
        out.shuffle(&mut rand::thread_rng());
        out
    }

    pub fn random() -> Strategy {
        *Self::pool()
            .choose(&mut rand::thread_rng())
            .unwrap_or(&Self::plain(BypassMethod::WrongSeq))
    }
}

/// Pure helper: wrong-seq base offsets for a 2-segment split send.
///
/// Both segments live in 'old' sequence space so the real server ignores
/// them while DPI still parses the fake SNI. Returns (seq1, seq2).
/// Mirrors `fake_tcp.split_plan` exactly (wrapping `& 0xFFFFFFFF`).
pub fn split_plan(syn_seq: u32, total_len: u32, first_len: u32) -> (u32, u32) {
    let base = syn_seq.wrapping_add(1).wrapping_sub(total_len);
    (base, base.wrapping_add(first_len))
}

/// Pure helper for `fragmented` (split-seqovl). Mirrors `overlap_plan`:
/// piece 1 covers [0:len1], pieces 2/3 rewind by overlap so DPI reassembly
/// desyncs while the real server (old-seq drop) is unaffected.
pub fn overlap_plan(
    total_len: i64,
    len1: i64,
    len2: i64,
    overlap: i64,
) -> ((i64, i64, i64), (i64, i64)) {
    let mut ov = if overlap < 0 { 0 } else { overlap };
    let (mut off3, mut ov2);
    let ov1 = if len1 > 1 && len2 > 1 {
        let cap = (len1 - 1).min(len2 - 1).max(0);
        ov = ov.min(cap);
        ov
    } else {
        0
    };
    let off2 = len1 - ov1;
    ov2 = if len2 > 1 {
        ov.min((len2 - 1).max(0))
    } else {
        0
    };
    off3 = off2 + len2 - ov2;
    if total_len > 0 && off3 >= total_len {
        ov2 = (off2 + len2 - (total_len - 1)).max(0);
        off3 = off2 + len2 - ov2;
    }
    if off3 < 0 {
        off3 = off2;
        ov2 = 0;
    }
    ((0, off2, off3), (ov1, ov2))
}

/// Best-effort SNI extraction from a raw ClientHello record.
pub fn extract_sni_from_hello(data: &[u8]) -> Option<Vec<u8>> {
    if data.len() < 50 || data[0] != 0x16 {
        return None;
    }
    let end = data.len().saturating_sub(10);
    let mut i = 5usize;
    while i < end.max(6) && i + 9 < data.len() {
        if data[i] == 0x00 && data[i + 1] == 0x00 {
            let ext_len = ((data[i + 2] as usize) << 8) | data[i + 3] as usize;
            if (7..=260).contains(&ext_len) && i + 4 + ext_len <= data.len() {
                let lst_len = ((data[i + 4] as usize) << 8) | data[i + 5] as usize;
                if lst_len == ext_len - 2 && data[i + 6] == 0x00 {
                    let sni_len = ((data[i + 7] as usize) << 8) | data[i + 8] as usize;
                    if (1..=253).contains(&sni_len) && sni_len == ext_len - 5 {
                        let cand = &data[i + 9..i + 9 + sni_len];
                        if cand.contains(&b'.') {
                            return Some(cand.to_vec());
                        }
                    }
                }
            }
        }
        i += 1;
    }
    None
}

/// Same-length decoy hostname (keeps ClientHello length identical).
/// FIX P1: preserve dots, emit only valid hostname chars ([a-z0-9-]),
/// never start/end a label with '-', and avoid trivial xxx.com patterns.
pub fn same_length_fake_sni(sni: &[u8]) -> Vec<u8> {
    let n = sni.len();
    if n == 0 {
        return b"a.example.com".to_vec();
    }
    let mut rng = rand::thread_rng();
    // Try random valid labels preserving dot positions (up to 8 attempts).
    for _ in 0..8 {
        let mut cand = Vec::with_capacity(n);
        for &b in sni {
            if b == b'.' {
                cand.push(b'.');
            } else if b.is_ascii_digit() {
                cand.push(b'0' + (rng.gen_range(0..10) as u8));
            } else {
                cand.push(b'a' + (rng.gen_range(0..26) as u8));
            }
        }
        // Must differ, keep dots, stay valid: no leading/trailing '-' in a
        // label (we only emit alnum + '.', so valid by construction), must
        // still contain a dot and not equal the original.
        if cand != sni && cand.contains(&b'.') {
            return cand;
        }
    }
    if n >= 5 {
        let mut cand = vec![b'q'; n - 4];
        cand.extend_from_slice(b".com");
        if cand != sni {
            return cand;
        }
    }
    let mut m = sni.to_vec();
    let dot = m.iter().rposition(|&b| b == b'.');
    for (idx, byte) in m.iter_mut().enumerate() {
        if Some(idx) == dot {
            continue;
        }
        let c = *byte;
        // Only emit valid hostname chars: rotate within alnum, never 0x01.
        if c.is_ascii_lowercase() {
            *byte = if c == b'z' { b'a' } else { c + 1 };
            break;
        } else if c.is_ascii_uppercase() {
            *byte = if c == b'Z' { b'A' } else { c + 1 };
            break;
        } else if c.is_ascii_digit() {
            *byte = if c == b'9' { b'0' } else { c + 1 };
            break;
        } else if c == b'-' {
            *byte = b'a';
            break;
        }
    }
    if m != sni {
        m
    } else {
        sni.to_vec()
    }
}

/// MD5-signed fake payload for `fakedsplit`: md5(data) ++ data.
/// Mirrors `_md5_fake_payload` (never panics; empty -> 16B digest).
pub fn md5_fake_payload(data: &[u8]) -> Vec<u8> {
    let digest = md5::compute(data);
    let mut out = Vec::with_capacity(16 + data.len());
    out.extend_from_slice(&digest.0);
    out.extend_from_slice(data);
    out
}

/// Decoy hello for `hostfakesplit` (same length, other SNI).
/// Mirrors `_build_hostfake_variant` in-place path: extract SNI, derive
/// same-length decoy, byte-replace once. Fresh-template path (per-profile
/// regeneration) is a Phase 3 slice; in-place keeps framing always valid.
/// FIX P1: prefer the known SNI offset (127 for our 517B template) before
/// falling back to a blind byte search, so random rnd/sess/key collisions
/// can't cause a splice in the wrong field.
pub fn build_hostfake_variant(data: &[u8]) -> Vec<u8> {
    if data.len() < 2 {
        return data.to_vec();
    }
    if let Some(sni) = extract_sni_from_hello(data) {
        let decoy = same_length_fake_sni(&sni);
        if !decoy.is_empty() && decoy != sni && decoy.len() == sni.len() {
            // Known template layout: SNI starts at 127. Prefer it.
            if data.len() >= 127 + sni.len() && &data[127..127 + sni.len()] == sni.as_slice() {
                let mut out = Vec::with_capacity(data.len());
                out.extend_from_slice(&data[..127]);
                out.extend_from_slice(&decoy);
                out.extend_from_slice(&data[127 + sni.len()..]);
                return out;
            }
            // Find first occurrence and splice (== `orig.replace(sni, decoy, 1)`).
            if let Some(pos) = data.windows(sni.len()).position(|w| w == sni.as_slice()) {
                let mut out = Vec::with_capacity(data.len());
                out.extend_from_slice(&data[..pos]);
                out.extend_from_slice(&decoy);
                out.extend_from_slice(&data[pos + sni.len()..]);
                return out;
            }
        }
    }
    data.to_vec()
}

// ---------------------------------------------------------------------------
// Pure fake planner (no I/O): port of `fake_send_thread` branching.
// ---------------------------------------------------------------------------

/// Tunables (mirrors `FakeTcpInjector(fake_delay, seq_overlap, pad_size)`).
/// `fake_delay` is timing-only (handled by the sender), so the pure planner
/// carries only `seq_overlap` + `pad_size`.
#[derive(Debug, Clone, Copy)]
pub struct FakeParams {
    pub seq_overlap: u8,
    pub pad_size: u8,
}

impl Default for FakeParams {
    fn default() -> Self {
        Self {
            seq_overlap: 3,
            pad_size: 0,
        }
    }
}

impl FakeParams {
    pub fn new(seq_overlap: u8, pad_size: u8) -> Self {
        Self {
            seq_overlap: seq_overlap.min(16),
            pad_size,
        }
    }
}

/// One TCP segment to emit: `seq` + `payload` + PSH + ident step.
/// `ttl_decrement` implements `wrong_seq_ttl` (sender does `ttl - 1`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FakeSegment {
    pub seq: u32,
    pub payload: Vec<u8>,
    pub psh: bool,
    pub ident_plus: u16,
    pub ttl_decrement: bool,
}

impl FakeSegment {
    fn single(seq: u32, payload: Vec<u8>, psh: bool, ident_plus: u16) -> Self {
        Self {
            seq,
            payload,
            psh,
            ident_plus,
            ttl_decrement: false,
        }
    }
}

fn wrong_seq(syn_seq: u32, data: &[u8], ident_plus: u16) -> FakeSegment {
    let seq = syn_seq.wrapping_add(1).wrapping_sub(data.len() as u32);
    FakeSegment::single(seq, data.to_vec(), true, ident_plus)
}

/// Pure plan for one decoy burst. Mirrors `fake_send_thread` branch-for-branch,
/// including tiny-packet fallbacks and the hostfakesplit length-mismatch
/// fallback to single `wrong_seq`.
///
/// Returns no segments for `SplitHello`: that strategy injects no decoy and
/// instead segments the client's real hello (see `split_hello`).
pub fn plan_fake(
    method: BypassMethod,
    syn_seq: u32,
    data: &[u8],
    params: &FakeParams,
) -> Vec<FakeSegment> {
    let n = data.len();
    match method {
        BypassMethod::Auto | BypassMethod::WrongSeq => vec![wrong_seq(syn_seq, data, 1)],
        BypassMethod::SplitHello => vec![],
        BypassMethod::WrongSeqTtl => {
            let mut s = wrong_seq(syn_seq, data, 1);
            s.ttl_decrement = true;
            vec![s]
        }
        BypassMethod::SplitSeq => {
            if n == 0 {
                return vec![wrong_seq(syn_seq, data, 1)];
            }
            let half = (n / 2).max(1);
            let (seq1, seq2) = split_plan(syn_seq, n as u32, half as u32);
            vec![
                FakeSegment::single(seq1, data[..half.min(n)].to_vec(), false, 1),
                FakeSegment::single(seq2, data[half.min(n)..].to_vec(), true, 2),
            ]
        }
        BypassMethod::Fragmented => {
            if n < 3 {
                return vec![wrong_seq(syn_seq, data, 1)];
            }
            let mut len1 = ((n * 40) / 100).max(1);
            let mut len2 = ((n * 30) / 100).max(1);
            if len1 + len2 >= n {
                len2 = ((n - len1) / 2).max(1);
            }
            let ((_, off2, mut off3), _) = overlap_plan(
                n as i64,
                len1 as i64,
                len2 as i64,
                params.seq_overlap as i64,
            );
            let (off2, _) = (off2 as usize, 0);
            let mut len3 = n.saturating_sub(off3 as usize);
            if len3 < 1 {
                len3 = 1;
                off3 = (n - 1) as i64;
            }
            // Clamp to valid slicing (defensive; mirrors Python guards).
            len1 = len1.min(n);
            let off2 = off2.min(n);
            let off3 = (off3 as usize).min(n.saturating_sub(1));
            let l2 = len2.min(n.saturating_sub(off2));
            let l3 = len3.min(n.saturating_sub(off3));
            let base = syn_seq.wrapping_add(1).wrapping_sub(n as u32);
            vec![
                FakeSegment::single(base, data[0..len1].to_vec(), false, 1),
                FakeSegment::single(
                    base.wrapping_add(off2 as u32),
                    data[off2..off2 + l2].to_vec(),
                    false,
                    2,
                ),
                FakeSegment::single(
                    base.wrapping_add(off3 as u32),
                    data[off3..off3 + l3].to_vec(),
                    true,
                    3,
                ),
            ]
        }
        BypassMethod::Padding => {
            let pad_len = if params.pad_size != 0 {
                (params.pad_size as usize).clamp(1, 256)
            } else {
                rand::thread_rng().gen_range(16..=64usize)
            };
            // FIX P1: padding must be a suffix, not a prefix. A zero prefix
            // breaks the TLS record header (DPI expects 0x16 at offset 0 and
            // skips the fake entirely). Suffix keeps the hello parseable
            // while the old-seq window still hides it from the server.
            let mut padded = Vec::with_capacity(data.len() + pad_len);
            padded.extend_from_slice(data);
            padded.extend(std::iter::repeat_n(0u8, pad_len));
            let seq = syn_seq.wrapping_add(1).wrapping_sub(padded.len() as u32);
            vec![FakeSegment::single(seq, padded, true, 1)]
        }
        BypassMethod::HostFakeSplit => {
            if n < 2 {
                return vec![wrong_seq(syn_seq, data, 1)];
            }
            let decoy = build_hostfake_variant(data);
            if decoy.len() != n {
                // Length mismatch -> single wrong_seq fallback (Python parity).
                return vec![wrong_seq(syn_seq, data, 1)];
            }
            let total = (decoy.len() + n) as u32;
            let (seq1, seq2) = split_plan(syn_seq, total, decoy.len() as u32);
            vec![
                FakeSegment::single(seq1, decoy, false, 1),
                FakeSegment::single(seq2, data.to_vec(), true, 2),
            ]
        }
        BypassMethod::FakeSplit => {
            if n < 1 {
                let seq = syn_seq.wrapping_add(1).wrapping_sub(1);
                return vec![FakeSegment::single(seq, data.to_vec(), true, 1)];
            }
            let signed = md5_fake_payload(data);
            // FIX P1: 0x10000 (64k) can sit inside a window-scaled receive
            // window, so the "bad" segment might be accepted. Push it 1M back
            // to guarantee out-of-window while staying in u32 space.
            let badseq = syn_seq
                .wrapping_add(1)
                .wrapping_sub(signed.len() as u32)
                .wrapping_sub(0x100000);
            let seq_ok = syn_seq.wrapping_add(1).wrapping_sub(n as u32);
            vec![
                FakeSegment::single(badseq, signed, false, 1),
                FakeSegment::single(seq_ok, data.to_vec(), true, 2),
            ]
        }
    }
}

// ---------------------------------------------------------------------------
// Real-ClientHello segmentation (the anti-DPI primitive).
// ---------------------------------------------------------------------------
//
// Every other method here makes DPI *see a decoy*. None of them hides the
// client's true hostname: the real hello still leaves this host as one segment
// that starts with 0x16 and contains the SNI whole, so a DPI that parses the
// outbound payload — not just the decoy — reads the blocked name.
//
// `split_hello` cuts the real hello into two in-window, contiguous segments so
// no single captured segment holds a matchable hostname. The server reassembles
// the byte stream identically, so TLS is unaffected.

/// Bytes of a TLS record header (type + version + length). Cutting here leaves
/// the first segment a handshake record whose body the DPI never receives, and
/// the second segment a body with no record header.
pub const TLS_RECORD_HEADER: usize = 5;

/// Shortest hostname label worth cutting. A cut must land *inside* a label:
/// blocklist patterns are domain-shaped (`youtube`, `youtube.com`), so a label
/// left whole in any segment is a matchable run. Labels below this are too
/// short to split usefully and are left intact.
pub const MIN_SPLIT_LABEL: usize = 5;

/// At most this many cuts, so one hello costs at most 4 segments.
pub const MAX_HELLO_CUTS: usize = 3;

/// Locate the SNI hostname inside a ClientHello, returning (offset, length).
/// Same walk as `extract_sni_from_hello`, positional.
pub fn find_sni_host(data: &[u8]) -> Option<(usize, usize)> {
    if data.len() < 50 || data[0] != 0x16 {
        return None;
    }
    let end = data.len().saturating_sub(10);
    let mut i = 5usize;
    while i < end.max(6) && i + 9 < data.len() {
        if data[i] == 0x00 && data[i + 1] == 0x00 {
            let ext_len = ((data[i + 2] as usize) << 8) | data[i + 3] as usize;
            if (7..=260).contains(&ext_len) && i + 4 + ext_len <= data.len() {
                let lst_len = ((data[i + 4] as usize) << 8) | data[i + 5] as usize;
                if lst_len == ext_len - 2 && data[i + 6] == 0x00 {
                    let sni_len = ((data[i + 7] as usize) << 8) | data[i + 8] as usize;
                    if (1..=253).contains(&sni_len) && sni_len == ext_len - 5 {
                        let cand = &data[i + 9..i + 9 + sni_len];
                        if cand.contains(&b'.') {
                            return Some((i + 9, sni_len));
                        }
                    }
                }
            }
        }
        i += 1;
    }
    None
}

/// Absolute (offset, length) of every label inside the hostname.
pub fn hostname_labels(data: &[u8]) -> Vec<(usize, usize)> {
    let (off, len) = match find_sni_host(data) {
        Some(v) => v,
        None => return Vec::new(),
    };
    let host = &data[off..off + len];
    let mut out = Vec::new();
    let mut start = 0usize;
    for i in 0..=host.len() {
        if i == host.len() || host[i] == b'.' {
            if i > start {
                out.push((off + start, i - start));
            }
            start = i + 1;
        }
    }
    out
}

/// Where to cut: inside the hostname labels long enough to be a blocklist key,
/// at a random offset *within* each one, plus — when a budget slot is spare and
/// a coin says so — one shape cut inside the hello body.
///
/// One cut per long label is enough — a per-packet matcher needs the blocked
/// string contiguous in one segment, and a domain pattern always contains its
/// longest label. `www.youtube.com` cut at `yout|ube.com` plus a second cut in
/// the tail still leaves only partial runs, so neither the `youtube` nor the
/// `youtube.com` rule fires.
///
/// The offset is random per call and a call happens per connection, so two
/// visits to the same host never produce the same segment sizes. A fixed shape
/// is a signature on its own: once "the hello is cut at label midpoint into
/// 108+69" is known, a device can match the *split* instead of the hostname and
/// block it while still letting an intact hello through. Cutting each chosen
/// label anywhere in `1..len` keeps the only invariant that matters — no
/// segment holds a whole long label — and varies everything a matcher can see.
///
/// Anything unparseable (an ECH inner hello, a segment already starting
/// mid-record) falls back to the 5-byte record-header cut, which at least
/// separates the handshake type/length from the body so the payload cannot be
/// parsed as a ClientHello at all.
pub fn hello_split_points(data: &[u8]) -> Vec<usize> {
    if data.len() < TLS_RECORD_HEADER + 2 || data[0] != 0x16 {
        return Vec::new();
    }
    let mut rng = rand::thread_rng();
    let labels: Vec<(usize, usize)> = hostname_labels(data)
        .into_iter()
        .filter(|(_, len)| *len >= MIN_SPLIT_LABEL)
        .collect();
    let mut chosen: Vec<usize> = Vec::new();
    if !labels.is_empty() {
        // Longest labels first: the budget is spent where the matchable run
        // most likely lives. Beyond the budget the rest are sampled randomly
        // instead of always dropping the same tail, which would leave one
        // particular label whole on every single connection.
        let mut ordered = labels.clone();
        ordered.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
        let picks: Vec<(usize, usize)> = if ordered.len() <= MAX_HELLO_CUTS {
            ordered
        } else {
            let longest = ordered[0];
            let rest = &ordered[1..];
            std::iter::once(longest)
                .chain(rest.choose_multiple(&mut rng, MAX_HELLO_CUTS - 1).cloned())
                .collect()
        };
        for (off, len) in picks {
            // Strictly inside the label, so both halves are non-empty and the
            // whole label appears in no segment.
            chosen.push(off + rng.gen_range(1..len));
        }
    }
    // A spare slot buys an extra cut in the random/session-id area, which
    // shrinks the first segment without touching the hostname guarantee. A
    // lead segment must not itself look like a record start, or the split
    // becomes matchable as a hello again. Only for a hello that parsed: an
    // unparseable one takes the record-header fallback below instead, which is
    // the cut that provably cannot leave a matchable ClientHello anywhere.
    if !labels.is_empty() && chosen.len() < MAX_HELLO_CUTS && data.len() > 48 && rng.gen_bool(0.5) {
        let at = TLS_RECORD_HEADER + 1 + rng.gen_range(0..(48 - TLS_RECORD_HEADER - 1));
        if data[at] != 0x16 {
            chosen.push(at);
        }
    }
    chosen.retain(|at| *at > TLS_RECORD_HEADER && *at < data.len());
    chosen.sort_unstable();
    chosen.dedup();
    if chosen.is_empty() {
        chosen.push(TLS_RECORD_HEADER);
    }
    chosen
}

/// The segments that replace one outbound ClientHello segment.
///
/// Sequence numbers stay in the real (in-window) space and are contiguous —
/// unlike `plan_fake`, nothing here is hidden from the server, which reassembly
/// gets the identical byte stream. `None` when the payload is not a handshake
/// record or cannot be cut usefully, in which case the caller forwards the
/// packet untouched.
pub fn split_hello(seq: u32, data: &[u8]) -> Option<Vec<FakeSegment>> {
    let points = hello_split_points(data);
    if points.is_empty() {
        return None;
    }
    let mut segs = Vec::with_capacity(points.len() + 1);
    let mut start = 0usize;
    let mut ident = 1u16;
    let boundaries: Vec<usize> = points.iter().cloned().chain([data.len()]).collect();
    for end in boundaries {
        if end <= start {
            continue;
        }
        let body = &data[start..end];
        let last = end == data.len();
        segs.push(FakeSegment::single(
            seq.wrapping_add(start as u32),
            body.to_vec(),
            last,
            ident,
        ));
        ident = ident.saturating_add(1);
        start = end;
    }
    if segs.len() < 2 {
        return None;
    }
    Some(segs)
}

/// Convenience: the single primary cut point (first boundary), if any.
pub fn hello_split_point(data: &[u8]) -> Option<usize> {
    hello_split_points(data).into_iter().next()
}

// ---------------------------------------------------------------------------
// Handshake state machine (pure `on_outbound/inbound_packet` validation).
// ---------------------------------------------------------------------------

/// Connection handshake state (subset of `FakeInjectiveConnection` relevant
/// to TCP validation; sockets/stats live in Phase 3).
#[derive(Debug, Clone)]
pub struct HandshakeState {
    pub strategy: Strategy,
    pub syn_seq: Option<u32>,
    pub syn_ack_seq: Option<u32>,
    pub sch_fake_sent: bool,
    pub fake_sent: bool,
    pub monitor: bool,
}

impl HandshakeState {
    pub fn new(strategy: Strategy) -> Self {
        Self {
            strategy,
            syn_seq: None,
            syn_ack_seq: None,
            sch_fake_sent: false,
            fake_sent: false,
            monitor: true,
        }
    }

    pub fn mark_fake_sent(&mut self) {
        self.fake_sent = true;
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum OutboundAction {
    /// Forward unchanged (`w.send(packet, False)`).
    Reinject,
    /// SYN-ACK handshake done on our side: emit `plan_fake(...)`.
    TriggerFake,
    /// Payload-bearing packet after the decoy burst, on a strategy that
    /// segments real hellos. The caller re-emits this packet's payload with
    /// `split_hello` and must NOT reinject the original; when `split_hello`
    /// returns `None` (not a handshake record) it forwards after all.
    MaybeSplitHello,
    /// Protocol violation: close + `w.send(packet, False)` (Python
    /// `on_unexpected_packet` always reinjects after closing).
    Unexpected(String),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum InboundAction {
    Reinject,
    /// Bypass complete: `monitor=false`, stats success, wake `t2a_event`.
    Success,
    /// The flow was answered by the interceptor, not by a TLS endpoint: the
    /// payload on port 443 does not start with a TLS record header. The
    /// measured shape here is an injected plaintext `HTTP/1.1 302` redirect
    /// to the captive portal, which the old "any inbound data = success" rule
    /// scored as a bypass win.
    Intercept(String),
    Unexpected(String),
}

/// Does this payload begin with a TLS record header? Content types 20-23
/// (ChangeCipherSpec, Alert, Handshake, ApplicationData) are the only four a
/// server may open a record with, so this is the whole test for "a TLS
/// endpoint answered".
pub fn looks_like_tls_record(payload: &[u8]) -> bool {
    matches!(
        payload.first(),
        Some(0x14) | Some(0x15) | Some(0x16) | Some(0x17)
    )
}

/// Short printable preview of the first bytes, for the intercept log. TLS
/// records are binary, so this is only ever shown for injected plaintext.
fn preview_bytes(payload: &[u8]) -> String {
    let head = &payload[..payload.len().min(48)];
    let mut out = String::with_capacity(head.len());
    for b in head {
        out.push(if b.is_ascii_graphic() || *b == b' ' {
            *b as char
        } else {
            '.'
        });
    }
    out
}

impl HandshakeState {
    /// Port of `on_outbound_packet`.
    pub fn on_outbound(&mut self, info: TcpInfo) -> OutboundAction {
        if self.sch_fake_sent {
            // The decoy is out. Anything the client sends now is its real
            // traffic, and on a splitting strategy that includes the real
            // ClientHello we still have to hide.
            if self.strategy.split_real && info.payload_len > 0 {
                return OutboundAction::MaybeSplitHello;
            }
            return OutboundAction::Unexpected(
                "unexpected outbound packet, recv packet after fake sent!".to_string(),
            );
        }
        // Outbound SYN (no ACK/RST/FIN, no payload).
        if info.syn && !info.ack_flag && !info.rst && !info.fin && info.payload_len == 0 {
            if info.ack != 0 {
                return OutboundAction::Unexpected(
                    "unexpected outbound syn packet, ack_num is not zero!".to_string(),
                );
            }
            if let Some(s) = self.syn_seq {
                if s != info.seq {
                    return OutboundAction::Unexpected(format!(
                        "unexpected outbound syn packet, seq not matched! {} {}",
                        info.seq, s
                    ));
                }
            }
            self.syn_seq = Some(info.seq);
            return OutboundAction::Reinject;
        }
        // Outbound ACK (completes local handshake, triggers fake send).
        if info.ack_flag && !info.syn && !info.rst && !info.fin && info.payload_len == 0 {
            match self.syn_seq {
                Some(s) if info.seq == s.wrapping_add(1) => {}
                _ => {
                    return OutboundAction::Unexpected(format!(
                        "unexpected outbound ack packet, seq not matched! {} {:?}",
                        info.seq, self.syn_seq
                    ))
                }
            }
            match self.syn_ack_seq {
                Some(sa) if info.ack == sa.wrapping_add(1) => {}
                _ => {
                    return OutboundAction::Unexpected(format!(
                        "unexpected outbound ack packet, ack not matched! {} {:?}",
                        info.ack, self.syn_ack_seq
                    ))
                }
            }
            self.sch_fake_sent = true;
            return OutboundAction::TriggerFake;
        }
        OutboundAction::Unexpected("unexpected outbound packet".to_string())
    }

    /// Port of `on_inbound_packet`. `payload` is the inbound TCP payload (empty
    /// when the caller only has header information, which keeps the historical
    /// "inbound data = success" verdict).
    pub fn on_inbound(&mut self, info: TcpInfo, payload: &[u8]) -> InboundAction {
        // FIX(diag): tracing at every inbound decision point.
        tracing::trace!(
            "HS in: syn={} ack={} rst={} fin={} pay={} fake_sent={} syn_ack_seq={:?}",
            info.syn,
            info.ack_flag,
            info.rst,
            info.fin,
            info.payload_len,
            self.fake_sent,
            self.syn_ack_seq
        );
        let syn_seq = match self.syn_seq {
            Some(s) => s,
            None => {
                return InboundAction::Unexpected(
                    "unexpected inbound packet, no syn sent!".to_string(),
                )
            }
        };
        // Inbound SYN-ACK.
        if info.syn && info.ack_flag && !info.rst && !info.fin && info.payload_len == 0 {
            if let Some(sa) = self.syn_ack_seq {
                if sa != info.seq {
                    return InboundAction::Unexpected(format!(
                        "unexpected inbound syn-ack packet, seq change! {} {}",
                        info.seq, sa
                    ));
                }
            }
            if info.ack != syn_seq.wrapping_add(1) {
                return InboundAction::Unexpected(format!(
                    "unexpected inbound syn-ack packet, ack not matched! {} {}",
                    info.ack, syn_seq
                ));
            }
            self.syn_ack_seq = Some(info.seq);
            return InboundAction::Reinject;
        }
        // Inbound data after our fake burst: the payload decides.
        // A TLS record header means a real endpoint answered, i.e. the DPI let
        // the handshake through (bypass complete). Plaintext on 443 is the
        // interceptor's own kill page, which is a FAILURE: scoring it as a win
        // is what let decoy-only strategies look healthy forever.
        // FIX P0: previously only a pure ACK counted, which never happens
        // in a real flow -> every connection timed out via HANDSHAKE_TIMEOUT.
        if self.fake_sent && info.payload_len > 0 && !info.syn && !info.rst && !info.fin {
            self.monitor = false;
            if !payload.is_empty() && !looks_like_tls_record(payload) {
                return InboundAction::Intercept(format!(
                    "injected non-TLS payload on port 443 ({} bytes): {:?}",
                    info.payload_len,
                    preview_bytes(payload)
                ));
            }
            return InboundAction::Success;
        }
        // Inbound ACK for our fake data = SUCCESS (synthetic pure-ACK path,
        // kept for selftest + stacks that ACK old-seq).
        if info.ack_flag
            && !info.syn
            && !info.rst
            && !info.fin
            && info.payload_len == 0
            && self.fake_sent
        {
            match self.syn_ack_seq {
                Some(sa) if info.seq == sa.wrapping_add(1) => {}
                _ => {
                    return InboundAction::Unexpected(format!(
                        "unexpected inbound ack packet, seq not matched! {} {:?}",
                        info.seq, self.syn_ack_seq
                    ))
                }
            }
            if info.ack != syn_seq.wrapping_add(1) {
                return InboundAction::Unexpected(format!(
                    "unexpected inbound ack packet, ack not matched! {} {}",
                    info.ack, syn_seq
                ));
            }
            self.monitor = false;
            return InboundAction::Success;
        }
        InboundAction::Unexpected("unexpected inbound packet".to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tls::{build_fake_client_hello, TlsProfile};

    fn hello(host: &str, profile: TlsProfile) -> Vec<u8> {
        build_fake_client_hello(host.as_bytes(), profile)
    }

    #[test]
    fn split_never_leaks_a_whole_hostname_label() {
        for profile in [TlsProfile::Legacy, TlsProfile::Chrome, TlsProfile::Firefox] {
            let host = "www.youtube.com";
            let h = hello(host, profile);
            let segs = split_hello(1000, &h).expect("split");
            assert!(segs.len() >= 2);
            let text: Vec<String> = segs
                .iter()
                .map(|s| String::from_utf8_lossy(&s.payload).into_owned())
                .collect();
            // Every label long enough to be a blocklist key must be broken.
            for label in host.split('.').filter(|l| l.len() >= MIN_SPLIT_LABEL) {
                for t in &text {
                    assert!(
                        !t.contains(label),
                        "profile {:?} segment holds the whole label {:?}",
                        profile,
                        label
                    );
                }
            }
            // And no segment parses as a ClientHello on its own.
            for s in &segs {
                assert!(extract_sni_from_hello(&s.payload).is_none());
            }
        }
    }

    #[test]
    fn split_is_contiguous_and_lossless() {
        let h = hello("example.com", TlsProfile::Legacy);
        let segs = split_hello(777, &h).unwrap();
        // The server must reassemble the identical byte stream, in order.
        assert!(segs.len() >= 2);
        let mut expect_seq = 777u32;
        let mut joined = Vec::new();
        for (i, s) in segs.iter().enumerate() {
            assert_eq!(s.seq, expect_seq, "segment {} not contiguous", i);
            assert_eq!(s.psh, i + 1 == segs.len(), "only the tail sets PSH");
            assert!(s.ident_plus >= 1);
            joined.extend_from_slice(&s.payload);
            expect_seq = expect_seq.wrapping_add(s.payload.len() as u32);
        }
        assert_eq!(joined, h);
    }

    #[test]
    fn every_long_label_gets_its_own_cut() {
        // Three long labels, but the cut budget is capped so one hello cannot
        // turn into an unbounded number of packets.
        let h = hello(
            "facebook-secure.youtube-long.google.com",
            TlsProfile::Legacy,
        );
        let pts = hello_split_points(&h);
        assert!(!pts.is_empty());
        assert!(pts.len() <= MAX_HELLO_CUTS);
        assert!(pts.iter().all(|p| *p > TLS_RECORD_HEADER && *p < h.len()));
        let segs = split_hello(9000, &h).unwrap();
        assert_eq!(segs.len(), pts.len() + 1);
    }

    #[test]
    fn non_handshake_payload_is_not_split() {
        // Application data (0x17) and junk must fall out to the caller's
        // forward path rather than being chopped up.
        assert!(split_hello(1, &[0x17; 200]).is_none());
        assert!(split_hello(1, b"GET / HTTP/1.1\r\n\r\n").is_none());
        assert!(split_hello(1, &[0x16, 0x03, 0x01]).is_none());
    }

    #[test]
    fn unparseable_hello_falls_back_to_record_header_cut() {
        let mut h = hello("example.com", TlsProfile::Legacy);
        // Corrupt the extension area so find_sni_host fails but the record
        // header still says handshake.
        for b in h.iter_mut().skip(40) {
            *b = 0xFF;
        }
        assert_eq!(hello_split_point(&h), Some(TLS_RECORD_HEADER));
    }

    #[test]
    fn split_shape_varies_across_connections() {
        // A fixed cut is a matchable signature, so the same hostname must not
        // produce one wire shape. Every draw still has to hold the guarantees
        // the other split tests pin.
        let host = "www.youtube.com";
        let mut shapes = std::collections::HashSet::new();
        for _ in 0..200 {
            let h = hello(host, TlsProfile::Legacy);
            let segs = split_hello(1000, &h).expect("split");
            assert!(
                segs.len() >= 2,
                "one segment is not a split: {:?}",
                segs.len()
            );
            assert!(segs.len() <= MAX_HELLO_CUTS + 1);
            let mut expect_seq = 1000u32;
            let mut joined = Vec::new();
            for (i, s) in segs.iter().enumerate() {
                assert_eq!(s.seq, expect_seq, "segment {} not contiguous", i);
                assert_eq!(s.psh, i + 1 == segs.len(), "only the tail sets PSH");
                joined.extend_from_slice(&s.payload);
                expect_seq = expect_seq.wrapping_add(s.payload.len() as u32);
                let text = String::from_utf8_lossy(&s.payload).into_owned();
                assert!(!text.contains("youtube"), "whole label leaked: {:?}", text);
                assert!(extract_sni_from_hello(&s.payload).is_none());
            }
            assert_eq!(joined, h, "reassembly is not byte-identical");
            shapes.insert(segs.iter().map(|s| s.payload.len()).collect::<Vec<usize>>());
        }
        assert!(
            shapes.len() >= 3,
            "only {} distinct shapes across 200 connections",
            shapes.len()
        );
    }

    #[test]
    fn auto_pool_has_no_degenerate_entries() {
        let pool = Strategy::pool();
        let mut keys: Vec<String> = pool.iter().map(|s| s.board_key()).collect();
        keys.sort();
        let before = keys.len();
        keys.dedup();
        assert_eq!(keys.len(), before, "auto pool has duplicate strategies");
        // The two removed methods must not come back through any door.
        assert!(BypassMethod::parse("double_sni").is_err());
        assert!(BypassMethod::parse("delayed_retry").is_err());
        assert!(keys.contains(&"hello_split".to_string()));
        assert!(keys.contains(&"fragmented+split".to_string()));
    }

    #[test]
    fn split_only_strategy_emits_no_decoy() {
        let params = FakeParams::default();
        let h = hello("example.com", TlsProfile::Legacy);
        assert!(plan_fake(BypassMethod::SplitHello, 100, &h, &params).is_empty());
        assert!(!BypassMethod::SplitHello.plan_decoy());
        assert!(BypassMethod::WrongSeq.plan_decoy());
    }

    #[test]
    fn real_hello_split_is_decided_by_the_state_machine() {
        let info = |payload: usize, ack: bool| TcpInfo {
            seq: 0,
            ack: 0,
            syn: false,
            ack_flag: ack,
            rst: false,
            fin: false,
            psh: true,
            payload_len: payload,
        };
        let mut st = HandshakeState::new(Strategy {
            decoy: BypassMethod::WrongSeq,
            split_real: true,
        });
        st.sch_fake_sent = true;
        assert_eq!(
            st.on_outbound(info(517, true)),
            OutboundAction::MaybeSplitHello
        );
        assert_eq!(
            st.on_outbound(info(0, true)),
            OutboundAction::Unexpected(
                "unexpected outbound packet, recv packet after fake sent!".to_string()
            )
        );
        // A non-splitting strategy keeps the old behaviour exactly.
        let mut plain = HandshakeState::new(Strategy::plain(BypassMethod::WrongSeq));
        plain.sch_fake_sent = true;
        assert!(matches!(
            plain.on_outbound(info(517, true)),
            OutboundAction::Unexpected(_)
        ));
    }

    fn inbound_data(payload_len: usize) -> TcpInfo {
        TcpInfo {
            seq: 7001,
            ack: 5001,
            syn: false,
            ack_flag: true,
            rst: false,
            fin: false,
            psh: true,
            payload_len,
        }
    }

    fn armed() -> HandshakeState {
        let mut st = HandshakeState::new(Strategy::plain(BypassMethod::SplitHello));
        st.syn_seq = Some(5000);
        st.syn_ack_seq = Some(7000);
        st.mark_fake_sent();
        st
    }

    #[test]
    fn injected_plaintext_is_scored_as_an_intercept_not_a_win() {
        // This gateway's kill: a plaintext 302 to the captive portal, sent on
        // port 443 in place of a ServerHello. Counting it as success made every
        // strategy look working, including the ones the DPI destroys.
        let kill = b"HTTP/1.1 302 found\r\nLocation: http://10.10.34.35:80\r\n\r\n";
        let mut st = armed();
        match st.on_inbound(inbound_data(kill.len()), kill) {
            InboundAction::Intercept(msg) => {
                assert!(msg.contains("HTTP/1.1 302"), "preview lost: {msg}")
            }
            other => panic!("expected Intercept, got {other:?}"),
        }
        assert!(!st.monitor, "an intercept ends the handshake watch");
    }

    #[test]
    fn every_tls_record_content_type_still_counts_as_traversal() {
        // 20 CCS / 21 alert / 22 handshake / 23 app data all come from a real
        // endpoint, so the flow got through even when the session then failed.
        for first in [0x14u8, 0x15, 0x16, 0x17] {
            let mut st = armed();
            assert_eq!(
                st.on_inbound(inbound_data(64), &[first, 0x03, 0x03]),
                InboundAction::Success,
                "content type {first:#04x} must not be treated as an intercept"
            );
        }
    }

    #[test]
    fn a_caller_with_headers_only_keeps_the_old_verdict() {
        // selftest and any header-only view pass no bytes; refusing to guess
        // there keeps those flows on the historical success path.
        let mut st = armed();
        assert_eq!(
            st.on_inbound(inbound_data(517), &[]),
            InboundAction::Success
        );
    }
}
