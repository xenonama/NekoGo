// Shared log capture for the temporary loopback harness (T4/T5).
// Installs ONE process-wide fmt subscriber writing plain lines into LOGS.
// Tests assert on substrings ("relay done", "result=ok"/"result=fail").

use std::sync::{Mutex, OnceLock};

static LOGS: OnceLock<Mutex<Vec<u8>>> = OnceLock::new();
static INIT: std::sync::Once = std::sync::Once::new();

struct CapWriter;

impl std::io::Write for CapWriter {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        if let Some(logs) = LOGS.get() {
            logs.lock().unwrap().extend_from_slice(buf);
        }
        Ok(buf.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

pub fn init_capture() {
    INIT.call_once(|| {
        LOGS.get_or_init(|| Mutex::new(Vec::new()));
        let _ = tracing_subscriber::fmt()
            .with_max_level(tracing::Level::INFO)
            .with_writer(|| CapWriter)
            .try_init();
    });
}

pub fn logs_contain(s: &str) -> bool {
    let logs = LOGS.get().expect("init_capture not called");
    String::from_utf8_lossy(&logs.lock().unwrap()).contains(s)
}

/// Bind a loopback listener with SO_REUSEADDR so back-to-back harness runs
/// never fail on a lingering port.
pub async fn bind_reuse(addr: &str) -> tokio::net::TcpListener {
    let sock = tokio::net::TcpSocket::new_v4().unwrap();
    sock.set_reuseaddr(true).unwrap();
    sock.bind(addr.parse().unwrap()).unwrap();
    sock.listen(128).unwrap()
}
