mod ban;
mod guard;
mod metrics;
mod wt_sync;

use std::fs;
use std::sync::LazyLock;
use std::time::Duration;
use tokio::sync::broadcast;
use wtransport::tls::Identity;
use wtransport::tls::Sha256Digest;
use wtransport::{Endpoint, ServerConfig};

static LOG_BROADCAST: LazyLock<broadcast::Sender<Vec<u8>>> =
    LazyLock::new(|| broadcast::channel(512).0);

pub fn broadcast_access_log(row_json: serde_json::Value) {
    if let Ok(json_str) = serde_json::to_string(&row_json) {
        let mut packet = Vec::with_capacity(1 + json_str.len());
        packet.push(0x01); // 0x01: realtime single-log frame
        packet.extend_from_slice(json_str.as_bytes());
        let _ = LOG_BROADCAST.send(packet);
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    println!("[net-traffic] Initializing WebTransport Datagram Daemon...");

    // 1. Build the SAN domain/IP list dynamically (strictly from the
    //    SENTINEL_IP_V4 / SENTINEL_IP_V6 / SENTINEL_HOSTS env vars; no
    //    hardcoding and no fallback allowed)
    let mut san_list: Vec<String> = vec![
        "localhost".to_string(),
        "127.0.0.1".to_string(),
        "::1".to_string(),
    ];

    if let Ok(v4) = std::env::var("SENTINEL_IP_V4") {
        let trimmed = v4.trim();
        if !trimmed.is_empty() && !san_list.contains(&trimmed.to_string()) {
            san_list.push(trimmed.to_string());
        }
    }

    if let Ok(v6) = std::env::var("SENTINEL_IP_V6") {
        let trimmed = v6.trim();
        if !trimmed.is_empty() && !san_list.contains(&trimmed.to_string()) {
            san_list.push(trimmed.to_string());
        }
    }

    if let Ok(hosts) = std::env::var("SENTINEL_HOSTS") {
        for h in hosts.split([',', ';', ' ']) {
            let trimmed = h.trim();
            if !trimmed.is_empty() && !san_list.contains(&trimmed.to_string()) {
                san_list.push(trimmed.to_string());
            }
        }
    }

    if san_list.len() <= 3 {
        panic!("Neither SENTINEL_IP_V4, SENTINEL_IP_V6, nor SENTINEL_HOSTS is set in environment! Refusing to start without explicit IP.");
    }

    let port_str = std::env::var("SENTINEL_PORT")
        .expect("SENTINEL_PORT environment variable is required (e.g. 39443)");
    let port: u16 = port_str
        .trim()
        .parse::<u16>()
        .expect("SENTINEL_PORT must be a valid u16 port number");

    println!("[net-traffic] Generating certificate with SAN: {:?}", san_list);
    let identity = Identity::self_signed(san_list.iter().map(|s| s.as_str()))?;

    // 2. Get the SHA-256 certificate fingerprint (32 bytes, hex)
    let cert_chain = identity.certificate_chain();
    if let Some(cert) = cert_chain.as_slice().first() {
        let hash: Sha256Digest = cert.hash();
        let hash_hex = hex::encode(hash.as_ref());
        println!("[net-traffic] Certificate SHA-256: {}", hash_hex);

        // Write the shared runtime file for the Go backend to read (multiple
        // candidate paths to cover container/local/production permission layouts)
        let hash_paths = [
            "/run/net-traffic.hash",
            "/tmp/net-traffic.hash",
            "./net-traffic.hash",
        ];
        for path in &hash_paths {
            let _ = fs::write(path, &hash_hex);
        }
    }

    // 3. Configure the WebTransport server (bind UDP SENTINEL_PORT)
    let config = ServerConfig::builder()
        .with_bind_default(port)
        .with_identity(identity)
        .build();

    let server = Endpoint::server(config)?;
    println!("[net-traffic] Listening on UDP 0.0.0.0:{} (WebTransport Datagram mode)", port);

    // Start the native traffic watchdog with Resend tiered alerts
    tokio::spawn(guard::start_traffic_guard_loop());

    // Start the CDT auto-reconciliation background task (once per hour)
    tokio::spawn(metrics::start_cdt_sync_loop());

    // Start the ban-scan background task (independent of WebTransport,
    // catches direct connections that bypass the CDN)
    tokio::spawn(ban::start_ban_scan_loop());

    // Start the active-active cross-node event-sync UDS gateway
    tokio::spawn(wt_sync::start_sync_server());

    // 4. Accept connections and push datagrams continuously
    loop {
        let incoming_session = server.accept().await;
        tokio::spawn(async move {
            match incoming_session.await {
                Ok(session_request) => {
                    println!("[net-traffic] Client session request: {}", session_request.authority());
                    match session_request.accept().await {
                        Ok(connection) => {
                            println!("[net-traffic] WebTransport Connection established: {}", connection.remote_address());

                            // Bidirectional stream listener: serves history log
                            // pulls (no HTTP, pure WebTransport internal transport)
                            let conn_streams = connection.clone();
                            tokio::spawn(async move {
                                while let Ok((mut send_stream, mut recv_stream)) = conn_streams.accept_bi().await {
                                    tokio::spawn(async move {
                                        use tokio::io::AsyncWriteExt;
                                        let mut buf = [0u8; 32];
                                        if let Ok(Some(n)) = recv_stream.read(&mut buf).await {
                                            if n > 0 && buf[0] == 0x02 {
                                                // Active-active node backend event-sync frame
                                                wt_sync::handle_incoming_wt_stream(send_stream, recv_stream, buf[0..n].to_vec()).await;
                                                return;
                                            }

                                            let zstd_bytes = if n >= 17 && buf[0] == 0x01 {
                                                // Timestamp-range mode: 0x01 + 8-byte start_ts + 8-byte end_ts
                                                if let (Ok(s_bytes), Ok(e_bytes)) = (buf[1..9].try_into(), buf[9..17].try_into()) {
                                                    let start_ts = i64::from_le_bytes(s_bytes);
                                                    let end_ts = i64::from_le_bytes(e_bytes);
                                                    metrics::read_history_logs_by_range_zstd(start_ts, end_ts)
                                                } else {
                                                    metrics::read_history_logs_zstd(100)
                                                }
                                            } else if n >= 3 && buf[0] == 0x00 {
                                                // Count mode: 0x00 + 2-byte limit
                                                let limit = (buf[1] as usize) | ((buf[2] as usize) << 8);
                                                let limit = if limit == 0 { 100 } else { limit };
                                                metrics::read_history_logs_zstd(limit)
                                            } else {
                                                // Default fallback: 100 entries
                                                metrics::read_history_logs_zstd(100)
                                            };
                                            let _ = send_stream.write_all(&zstd_bytes).await;
                                            let _ = send_stream.shutdown().await;
                                        } else {
                                            let zstd_bytes = metrics::read_history_logs_zstd(100);
                                            let _ = send_stream.write_all(&zstd_bytes).await;
                                            let _ = send_stream.shutdown().await;
                                        }
                                    });
                                }
                            });

                            // Control datagram listener: 0xFF = sync CDT immediately
                            let conn_datagram = connection.clone();
                            tokio::spawn(async move {
                                while let Ok(datagram) = conn_datagram.receive_datagram().await {
                                    if datagram.len() == 1 && datagram[0] == 0xFF {
                                        tokio::spawn(async { metrics::sync_cdt_once().await });
                                    }
                                }
                            });

                            // Dual-packet preemptive push loop: 1s telemetry packets
                            // interleaved with realtime access-log delta packets
                            let mut log_rx = LOG_BROADCAST.subscribe();
                            let mut interval = tokio::time::interval(Duration::from_secs(1));
                            loop {
                                tokio::select! {
                                    _ = connection.closed() => {
                                        println!("[net-traffic] Client closed connection: {}", connection.remote_address());
                                        break;
                                    }
                                    Ok(log_packet) = log_rx.recv() => {
                                        // Preemptively push the realtime single-log
                                        // frame and reset the 1s telemetry timer
                                        let _ = connection.send_datagram(&log_packet);
                                        interval.reset();
                                    }
                                    _ = interval.tick() => {
                                        // Collect hardware metrics (52-byte packed
                                        // struct with IPv6 CDT and float MB fields)
                                        let frame = metrics::collect_metrics();
                                        let bytes: &[u8] = bytemuck::bytes_of(&frame);
                                        let _ = connection.send_datagram(bytes);
                                    }
                                }
                            }
                        }
                        Err(e) => {
                            eprintln!("[net-traffic] Accept error: {:?}", e);
                        }
                    }
                }
                Err(e) => {
                    eprintln!("[net-traffic] Session handshake error: {:?}", e);
                }
            }
        });
    }
}
