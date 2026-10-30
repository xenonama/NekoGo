// T4: loopback relay, MODE = "SNI Only". No Admin, no driver, no real net.
// A raw TCP client connects to the engine's LISTEN_PORT; a mock endpoint
// echoes; bytes must travel client -> endpoint -> client, stats must count
// success, and the [INFO] relay-done line must appear.

mod common;

use sni_core::{
    config::{Config, Endpoint},
    stats::Stats,
};
use sni_engine::worker::Worker;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

const ENGINE_ADDR: &str = "127.0.0.1:45555";
const MOCK_ADDR: &str = "127.0.0.1:45556";

fn test_config() -> Config {
    Config {
        listen_host: "127.0.0.1".to_string(),
        listen_port: 45555,
        endpoints: vec![Endpoint {
            ip: "127.0.0.1".to_string(),
            port: 45556,
        }],
        fake_snis: vec!["example.com".to_string()],
        bypass_method: "wrong_seq".to_string(),
        handshake_timeout: 0.5,
        ..Config::default()
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn t4_loopback_relay_echoes_both_ways() {
    common::init_capture();

    // Mock "Cloudflare endpoint": accept + echo everything back.
    let mock = common::bind_reuse(MOCK_ADDR).await;
    tokio::spawn(async move {
        loop {
            let (sock, _) = mock.accept().await.unwrap();
            tokio::spawn(async move {
                let (mut r, mut w) = sock.into_split();
                let _ = tokio::io::copy(&mut r, &mut w).await;
            });
        }
    });

    let stats = Arc::new(Stats::new());
    let worker = Arc::new(Worker::new(test_config(), Arc::clone(&stats)).unwrap());
    let listener = common::bind_reuse(ENGINE_ADDR).await;
    let w2 = Arc::clone(&worker);
    let run_task = tokio::spawn(async move {
        let _ = w2.run(listener).await;
    });

    // Raw TCP client (the real NekoGo contract: raw bytes, no proxy handshake).
    let mut client = tokio::net::TcpStream::connect(ENGINE_ADDR).await.unwrap();
    let payload = vec![0xABu8; 256];
    client.write_all(&payload).await.unwrap();
    let mut back = vec![0u8; 256];
    tokio::time::timeout(Duration::from_secs(10), client.read_exact(&mut back))
        .await
        .expect("echo read timed out")
        .unwrap();
    assert_eq!(payload, back, "endpoint echo did not round-trip");
    drop(client);

    // Wait for the relay verdict (handshake wait is 0.5 s in this config).
    let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
    loop {
        if stats.snapshot().success >= 1 {
            break;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "timed out waiting for success"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let snap = stats.snapshot();
    assert!(snap.total >= 1, "total not counted: {:?}", snap);
    assert_eq!(snap.failed, 0, "unexpected failure: {:?}", snap);
    println!(
        "T4 STATS: {}",
        serde_json::to_string(&snap).unwrap_or_default()
    );
    assert!(
        common::logs_contain("relay done") && common::logs_contain("result=ok"),
        "missing [INFO] relay-done line"
    );

    worker.request_shutdown();
    run_task.abort();
}
