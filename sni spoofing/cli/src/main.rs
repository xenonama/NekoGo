//! sni-spoof.exe: standalone core (xray.exe role).
//!
//! CLI surface (exact, no other flags):
//!   sni-spoof.exe version        -> version on stdout, exit 0
//!   sni-spoof.exe run -c <file>  -> load config, start engine, stdout logs, run until signal
//!   sni-spoof.exe help           -> usage on stdout, exit 0
//!
//! Logging: plain text to stdout, `[INFO]/[WARN]/[ERROR]/[STATS]` prefixes.
//! Stats: `[STATS] {snapshot JSON}` every 5s from the shared `Stats`.
//! Shutdown: SIGINT/SIGTERM (Windows: Ctrl+C/Close/Shutdown/Break) -> `EngineHandle::shutdown`.
//!
//! FIX(nested-runtime): this binary owns NO async runtime on purpose.
//! EngineHandle::start builds + block_ons its own runtime internally, so an
//! async entry-point macro here nested runtime-inside-runtime and panicked
//! after the WinDivert setup. Signal wait + [STATS] ticker are plain std
//! threads on one process-wide AtomicBool stop flag.

use sni_core::stats::Stats;
use sni_engine::engine::EngineHandle;
use std::io::Write as _;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
};
use std::time::Duration;

// ---------------------------------------------------------------------------
// tracing -> stdout layer (stable prefixes for the host line reader)
// ---------------------------------------------------------------------------

struct StdoutLayer;

struct FieldVisitor {
    message: String,
}

impl tracing::field::Visit for FieldVisitor {
    fn record_str(&mut self, field: &tracing::field::Field, value: &str) {
        if field.name() == "message" {
            self.message = value.to_string();
        } else if !value.is_empty() {
            if !self.message.is_empty() {
                self.message.push(' ');
            }
            self.message
                .push_str(&format!("{}={}", field.name(), value));
        }
    }
    fn record_debug(&mut self, field: &tracing::field::Field, value: &dyn std::fmt::Debug) {
        let s = format!("{:?}", value);
        if field.name() == "message" {
            self.message = s;
        } else {
            if !self.message.is_empty() {
                self.message.push(' ');
            }
            self.message
                .push_str(&format!("{}={:?}", field.name(), value));
        }
    }
}

impl<S> tracing_subscriber::layer::Layer<S> for StdoutLayer
where
    S: tracing::Subscriber,
{
    fn on_event(
        &self,
        event: &tracing::Event<'_>,
        _ctx: tracing_subscriber::layer::Context<'_, S>,
    ) {
        let tag = match *event.metadata().level() {
            tracing::Level::ERROR => "[ERROR]",
            tracing::Level::WARN => "[WARN]",
            tracing::Level::INFO => "[INFO]",
            tracing::Level::DEBUG => "[DEBUG]",
            tracing::Level::TRACE => "[TRACE]",
        };
        let mut v = FieldVisitor {
            message: String::new(),
        };
        event.record(&mut v);
        let body = v.message.trim();
        let line = if body.is_empty() {
            tag.to_string()
        } else {
            format!("{} {}", tag, body)
        };
        let mut out = std::io::stdout().lock();
        let _ = writeln!(out, "{}", line);
        let _ = out.flush();
    }
}

fn init_stdout_logging() {
    use tracing_subscriber::prelude::*;
    let _ = tracing_subscriber::registry()
        .with(tracing_subscriber::EnvFilter::new("info"))
        .with(StdoutLayer)
        .try_init();
}

fn out_line(tag: &str, msg: &str) {
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{} {}", tag, msg);
    let _ = out.flush();
}

fn usage_stdout() {
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "usage:");
    let _ = writeln!(out, "  sni-spoof.exe version");
    let _ = writeln!(out, "  sni-spoof.exe run -c <config.json>");
    let _ = writeln!(out, "  sni-spoof.exe help");
    let _ = out.flush();
}

fn usage_stderr() {
    eprintln!("usage:");
    eprintln!("  sni-spoof.exe version");
    eprintln!("  sni-spoof.exe run -c <config.json>");
    eprintln!("  sni-spoof.exe help");
}

// ---------------------------------------------------------------------------
// shutdown signal (std only, no runtime): one process-wide stop flag.
// Unix: SIGINT + SIGTERM via signal-hook. Windows: Ctrl+C/Break/Close/
// Shutdown via SetConsoleCtrlHandler (same four events as before).
// ---------------------------------------------------------------------------

/// Process-wide stop flag. Set by the OS signal/console handler, polled by
/// `run()` and the stats thread. Plain static: one run per process.
static STOP: AtomicBool = AtomicBool::new(false);

/// Returns true when at least one shutdown path is armed. A false return
/// means signals would be missed entirely (flag never flips, run() parks
/// forever) — the caller must refuse to run in that case.
fn install_signal_handlers() -> bool {
    #[cfg(unix)]
    {
        use signal_hook::consts::{SIGINT, SIGTERM};
        // low_level needs its SigIds retained (drop unregisters); the
        // registration lives as long as the process, so forget them.
        let mut ids = Vec::with_capacity(2);
        for sig in [SIGINT, SIGTERM] {
            match signal_hook::low_level::register(sig, || {
                STOP.store(true, Ordering::Relaxed);
            }) {
                Ok(id) => ids.push(id),
                Err(e) => out_line(
                    "[WARN]",
                    &format!("signal handler install failed for {}: {}", sig, e),
                ),
            }
        }
        let armed = !ids.is_empty();
        std::mem::forget(ids);
        armed
    }
    #[cfg(windows)]
    {
        install_windows_console_handler()
    }
    #[cfg(not(any(unix, windows)))]
    {
        // No signal API here; the engine cannot start on this platform
        // anyway (WinDivert stub fails fast), so the flag is never polled.
        true
    }
}

/// Windows console handler: Ctrl+C, Break, Close, Shutdown all mean stop.
/// Uses the already-workspace-pinned `windows` crate (same pattern as
/// windivert/). Runs on an OS thread — only touches the AtomicBool.
/// Returns true when the handler is armed.
#[cfg(windows)]
fn install_windows_console_handler() -> bool {
    use windows::Win32::Foundation::BOOL;
    use windows::Win32::System::Console::{
        SetConsoleCtrlHandler, CTRL_BREAK_EVENT, CTRL_CLOSE_EVENT, CTRL_C_EVENT,
        CTRL_SHUTDOWN_EVENT,
    };
    unsafe extern "system" fn handler(ctrl_type: u32) -> BOOL {
        match ctrl_type {
            CTRL_C_EVENT | CTRL_BREAK_EVENT | CTRL_CLOSE_EVENT | CTRL_SHUTDOWN_EVENT => {
                STOP.store(true, Ordering::Relaxed);
                true.into()
            }
            _ => false.into(),
        }
    }
    // SAFETY: `handler` is a plain fn item (no captures); the OS only calls
    // it with a console-event code, and it only stores an AtomicBool.
    match unsafe { SetConsoleCtrlHandler(Some(handler), true) } {
        Ok(()) => true,
        Err(e) => {
            out_line(
                "[WARN]",
                &format!("console handler install failed: {:?}", e),
            );
            false
        }
    }
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let rest = &args[1..];
    match rest {
        [cmd] if cmd == "version" => {
            println!("sni-spoof {}", env!("CARGO_PKG_VERSION"));
        }
        [cmd] if cmd == "help" => {
            usage_stdout();
        }
        [cmd, flag, file] if cmd == "run" && flag == "-c" && !file.trim().is_empty() => {
            run(file);
        }
        _ => {
            usage_stderr();
            std::process::exit(2);
        }
    }
}

fn run(config_path: &str) {
    init_stdout_logging();
    let path = std::path::PathBuf::from(config_path);
    let cfg = match sni_core::config::load(&path) {
        Ok(c) => c,
        Err(e) => {
            out_line(
                "[ERROR]",
                &format!("cannot load config {}: {}", path.display(), e),
            );
            std::process::exit(1);
        }
    };
    if !install_signal_handlers() {
        out_line(
            "[ERROR]",
            "no shutdown signal handler armed; refusing to run",
        );
        std::process::exit(1);
    }
    let stats = Arc::new(Stats::new());
    let mut handle = match EngineHandle::start(cfg, Arc::clone(&stats)) {
        Ok(h) => h,
        Err(e) => {
            out_line("[ERROR]", &format!("start failed: {}", e));
            std::process::exit(1);
        }
    };
    out_line("[INFO]", "engine RUNNING");

    // Stats line for the host (no RPC): `[STATS] {...}` every 5s.
    // Sleeps in 100ms slices so shutdown stays snappy (<100ms late).
    let ticker = std::thread::Builder::new()
        .name("stats-ticker".into())
        .spawn({
            let stats = Arc::clone(&stats);
            move || {
                let mut slices: u32 = 0;
                while !STOP.load(Ordering::Relaxed) {
                    std::thread::sleep(Duration::from_millis(100));
                    if STOP.load(Ordering::Relaxed) {
                        break;
                    }
                    slices += 1;
                    if slices >= 50 {
                        slices = 0;
                        match serde_json::to_string(&stats.snapshot()) {
                            Ok(js) => out_line("[STATS]", &js),
                            Err(e) => out_line("[WARN]", &format!("stats serialize failed: {}", e)),
                        }
                    }
                }
            }
        });
    let ticker = match ticker {
        Ok(h) => h,
        Err(e) => {
            out_line("[ERROR]", &format!("stats thread failed: {}", e));
            handle.shutdown();
            std::process::exit(1);
        }
    };

    // Block on the stop flag (100ms poll, no runtime, no busy-spin).
    while !STOP.load(Ordering::Relaxed) {
        std::thread::sleep(Duration::from_millis(100));
    }
    out_line("[WARN]", "shutdown signal received");
    // Ticker exits on the same flag; join it first (existing order:
    // stats stop -> engine shutdown -> stopped line -> exit 0).
    let _ = ticker.join();
    handle.shutdown();
    out_line("[WARN]", "engine stopped");
}
