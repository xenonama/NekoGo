// T5: broken endpoint. Nothing listens on the mock port, so every upstream
// connect is refused. The engine must: name the endpoint in a [WARN],
// count failed >= 1, keep success at 0. (Per approved item 4 the relay
// verdict logs [WARN], not [ERROR] — the STEP 3 text's "[ERROR] line" is
// covered by the fail WARN; see report.)

mod common;

use sni_core::{
    config::{Config, Endpoint},
    stats::Stats,
};
use sni_engine::worker::Worker;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

const ENGINE_ADDR: &str = "127.0.0.1:45557";
// NOTE: nothing binds 45558 — connects are refused.

fn test_config() -> Config {
    Config {
        listen_host: "127.0.0.1".to_string(),
        listen_port: 45557,
        endpoints: vec![Endpoint {
            ip: "127.0.0.1".to_string(),
            port: 45558,
        }],
        fake_snis: vec!["example.com".to_string()],
        bypass_method: "wrong_seq".to_string(),
        handshake_timeout: 0.5,
        ..Config::default()
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn t5_broken_endpoint_counts_failure_and_warns() {
    common::init_capture();

    let stats = Arc::new(Stats::new());
    let worker = Arc::new(Worker::new(test_config(), Arc::clone(&stats)).unwrap());
    let listener = common::bind_reuse(ENGINE_ADDR).await;
    let w2 = Arc::clone(&worker);
    let run_task = tokio::spawn(async move {
        let _ = w2.run(listener).await;
    });

    // Client connects; upstream is dead so the relay never forms.
    let mut client = tokio::net::TcpStream::connect(ENGINE_ADDR).await.unwrap();
    let _ = client.write_all(&[0xCDu8; 64]).await;
    // Engine drops us after failed failover; drain whatever comes back.
    let mut buf = [0u8; 64];
    let _ = tokio::time::timeout(Duration::from_secs(5), client.read(&mut buf)).await;
    drop(client);

    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    loop {
        let snap = stats.snapshot();
        if snap.failed >= 1 {
            assert_eq!(
                snap.success, 0,
                "broken endpoint counted as success: {:?}",
                snap
            );
            break;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "timed out waiting for failure"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert!(
        common::logs_contain("connect 127.0.0.1:45558 failed"),
        "missing per-endpoint [WARN] naming the dead endpoint"
    );
    assert!(
        common::logs_contain("all endpoints failed"),
        "missing aggregate [WARN]"
    );

    worker.request_shutdown();
    run_task.abort();
}
