mod ban;
mod guard;
mod metrics;
mod wt_sync;

use std::fs;
use std::sync::atomic::{AtomicU16, AtomicU64, Ordering};
use std::sync::{Arc, LazyLock};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::sync::broadcast;
use wtransport::config::QuicTransportConfig;
use wtransport::endpoint::endpoint_side;
use wtransport::quinn::IdleTimeout;
use wtransport::tls::Identity;
use wtransport::tls::Sha256Digest;
use wtransport::{Endpoint, ServerConfig, VarInt};

static LOG_BROADCAST: LazyLock<broadcast::Sender<Vec<u8>>> =
    LazyLock::new(|| broadcast::channel(512).0);

/// The client sessions this daemon is pushing to, reported in every telemetry frame.
///
/// A reader cannot derive it from anything else it sees: the frames arrive per session, so the
/// only way to tell one session from twenty is for the daemon to count them.
static LIVE_SESSIONS: AtomicU16 = AtomicU16::new(0);

pub fn live_sessions() -> u16 {
    LIVE_SESSIONS.load(Ordering::Relaxed)
}

/// Holds one session's place in the count for as long as the push loop runs.
///
/// The loop leaves in more than one way — the lease lapsing, the connection closing, the peer
/// failing — and every one of them has to release the count, so the release is tied to the
/// scope rather than repeated at each exit.
struct SessionGuard;

impl Drop for SessionGuard {
    fn drop(&mut self) {
        LIVE_SESSIONS.fetch_sub(1, Ordering::Relaxed);
    }
}

/// How long a session may go without a renewal from its client before this end releases it.
///
/// The peer's acknowledgements keep the transport alive for as long as its stack runs, so
/// nothing in the transport separates a page that is being watched from one that was closed
/// while its connection stayed up — a pushed session is never idle. The renewal carries that
/// fact instead: the page sends one while it is open and again on every return to the
/// foreground, and a session that stops renewing is closed. The window is the price of a
/// client that vanished: at most this long of frames at the push rate, once.
const LEASE_SECS: u64 = 300;

/// The control datagrams a page sends. Each is one byte, and the datagram listener acts only
/// on that length, so a telemetry frame can never be mistaken for one.
const CTL_SYNC_CDT: u8 = 0xFF;
const CTL_LEASE: u8 = 0x20;

fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// How long a client connection may stay silent before this end gives up on it.
///
/// A client is pushed a telemetry frame every second, so silence this long means the peer is
/// gone. The bound is what lets this process release a connection whose client left without
/// closing: without one the connection stays in the table and its push loop keeps sending for
/// as long as the process lives.
///
/// The outbound sync link is the opposite case and keeps its timeout off
/// (wt_sync::peer_transport_config): it is silent for hours by design and must survive that.
const CLIENT_IDLE_TIMEOUT: Duration = Duration::from_secs(30);

/// The validity of the certificate issued at startup.
///
/// An upper bound rather than a choice: a client that pins the certificate by digest accepts it
/// only while it is valid, and only while its validity period is at most two weeks.
const CERT_VALIDITY_DAYS: u32 = 14;

/// How long before the certificate expires the daemon exits to be issued a new one.
const CERT_RENEW_MARGIN: Duration = Duration::from_secs(2 * 24 * 60 * 60);

/// Exits shortly before the certificate expires, so the supervisor starts a run that issues a new
/// one.
///
/// The identity is bound into the endpoint that is already serving and cannot be replaced in
/// place. Without this the certificate expires under a daemon that keeps running, and every
/// pinned client — the browser and the peer's outbound link — starts failing with no error that
/// names the certificate.
fn spawn_certificate_renewal() {
    tokio::spawn(async move {
        let renew_at = Duration::from_secs(u64::from(CERT_VALIDITY_DAYS) * 24 * 60 * 60)
            .saturating_sub(CERT_RENEW_MARGIN);
        tokio::time::sleep(renew_at).await;
        println!("[silphd] Certificate approaches expiry; exiting for reissue");
        std::process::exit(0);
    });
}

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
    println!("[silphd] Initializing WebTransport Datagram Daemon...");

    // 1. Build the SAN domain/IP list dynamically (strictly from the
    //    SILPHD_IP_V4 / SILPHD_IP_V6 / SILPHD_HOSTS env vars; no
    //    hardcoding and no fallback allowed)
    let mut san_list: Vec<String> = vec![
        "localhost".to_string(),
        "127.0.0.1".to_string(),
        "::1".to_string(),
    ];

    if let Ok(v4) = std::env::var("SILPHD_IP_V4") {
        let trimmed = v4.trim();
        if !trimmed.is_empty() && !san_list.contains(&trimmed.to_string()) {
            san_list.push(trimmed.to_string());
        }
    }

    if let Ok(v6) = std::env::var("SILPHD_IP_V6") {
        let trimmed = v6.trim();
        if !trimmed.is_empty() && !san_list.contains(&trimmed.to_string()) {
            san_list.push(trimmed.to_string());
        }
    }

    if let Ok(hosts) = std::env::var("SILPHD_HOSTS") {
        for h in hosts.split([',', ';', ' ']) {
            let trimmed = h.trim();
            if !trimmed.is_empty() && !san_list.contains(&trimmed.to_string()) {
                san_list.push(trimmed.to_string());
            }
        }
    }

    if san_list.len() <= 3 {
        panic!("Neither SILPHD_IP_V4, SILPHD_IP_V6, nor SILPHD_HOSTS is set in environment! Refusing to start without explicit IP.");
    }

    let port_str = std::env::var("SILPHD_PORT")
        .expect("SILPHD_PORT environment variable is required");
    let port: u16 = port_str
        .trim()
        .parse::<u16>()
        .expect("SILPHD_PORT must be a valid u16 port number");

    println!("[silphd] Generating certificate with SAN: {:?}", san_list);
    let identity = Identity::self_signed_builder()
        .subject_alt_names(san_list.iter().map(|s| s.as_str()))
        .from_now_utc()
        .validity_days(CERT_VALIDITY_DAYS)
        .build()?;

    // 2. Get the SHA-256 certificate fingerprint (32 bytes, hex)
    let cert_chain = identity.certificate_chain();
    if let Some(cert) = cert_chain.as_slice().first() {
        let hash: Sha256Digest = cert.hash();
        let hash_hex = hex::encode(hash.as_ref());
        println!("[silphd] Certificate SHA-256: {}", hash_hex);

        // Write the shared runtime file for the Go backend to read (multiple
        // candidate paths to cover container/local/production permission layouts)
        let hash_paths = [
            "/run/silphd.hash",
            "/tmp/silphd.hash",
            "./silphd.hash",
        ];
        for path in &hash_paths {
            let _ = fs::write(path, &hash_hex);
        }
    }

    // 3. Configure the WebTransport server (bind UDP SILPHD_PORT)
    //
    // The idle timeout is bounded on the client endpoint only. A client is pushed a frame every
    // second, so silence means the peer is gone and an unbounded connection is one this process
    // never releases. No heartbeat and no periodic path-MTU probe, as on the sync link.
    let mut transport = QuicTransportConfig::default();
    transport.max_idle_timeout(Some(IdleTimeout::try_from(CLIENT_IDLE_TIMEOUT)?));
    transport.keep_alive_interval(None);
    transport.mtu_discovery_config(None);
    wt_sync::apply_initial_window(&mut transport);

    let config = ServerConfig::builder()
        .with_bind_default(port)
        .with_custom_transport(identity, transport)
        .build();

    let server = Endpoint::server(config)?;
    println!("[silphd] Listening on UDP 0.0.0.0:{} (WebTransport Datagram mode)", port);

    // The endpoint the node-to-node link is served on, when the deployment names a certificate
    // for it.
    //
    // It is a second endpoint because the two kinds of client want different things: a page is
    // pushed a frame every second and is released when it stops renewing, while the link is
    // silent for hours by design. And the link is verified against the certificate authority the
    // site already has, not against a digest the peer reissues at every daemon start.
    let peer_server = peer_endpoint().await?;
    if let Some((peer_port, _)) = peer_server.as_ref() {
        println!("[silphd] Listening on UDP 0.0.0.0:{} (peer link)", peer_port);
    }

    // Start the fixed-cadence sampler the telemetry frames read their rates from
    metrics::start_sampler_loop();

    // Start the native traffic watchdog with Resend tiered alerts
    tokio::spawn(guard::start_traffic_guard_loop());

    // Start the CDT auto-reconciliation background task (once per hour)
    tokio::spawn(metrics::start_cdt_sync_loop());

    // Start the ban-scan background task (independent of WebTransport,
    // catches direct connections that bypass the CDN)
    tokio::spawn(ban::start_ban_scan_loop());

    // Start the active-active cross-node event-sync UDS gateway
    tokio::spawn(wt_sync::start_sync_server());

    // Reissue the certificate before it expires
    spawn_certificate_renewal();

    // 4. Accept connections and push datagrams continuously
    //
    // Both endpoints feed one handler. What a connection is for is decided by its path, not by
    // the port it arrived on, so the peer link reaches the same code as a page and skips the
    // parts it has no use for.
    loop {
        let incoming_session = match peer_server.as_ref() {
            Some((_, peer)) => tokio::select! {
                incoming = server.accept() => incoming,
                incoming = peer.accept() => incoming,
            },
            None => server.accept().await,
        };
        tokio::spawn(async move {
            match incoming_session.await {
                Ok(session_request) => {
                    println!("[silphd] Client session request: {}", session_request.authority());
                    // The node-to-node link carries its frames on bidirectional streams and has
                    // no use for the telemetry push, the lease or the page's control datagrams,
                    // so none of those are started for it.
                    let is_peer_link = session_request.path() == wt_sync::PEER_SYNC_PATH;
                    match session_request.accept().await {
                        Ok(connection) => {
                            println!("[silphd] WebTransport Connection established: {}", connection.remote_address());

                            // Bidirectional stream listener: serves history log
                            // pulls (no HTTP, pure WebTransport internal transport)
                            let conn_streams = connection.clone();
                            tokio::spawn(async move {
                                while let Ok((mut send_stream, mut recv_stream)) = conn_streams.accept_bi().await {
                                    tokio::spawn(async move {
                                        use tokio::io::AsyncWriteExt;
                                        let mut buf = [0u8; 32];
                                        if let Ok(Some(n)) = recv_stream.read(&mut buf).await {
                                            if n > 0
                                                && (buf[0] == wt_sync::WT_FRAME_ID
                                                    || buf[0] == wt_sync::WT_FRAME_ID_BLOB)
                                            {
                                                // Node-to-node backend frame: the event channel
                                                // (0x02) or the blob face (0x03)
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

                            if !is_peer_link {
                                LIVE_SESSIONS.fetch_add(1, Ordering::Relaxed);
                                let _session = SessionGuard;

                                // When the page last renewed its lease, written by the
                                // datagram listener and read by the push loop.
                                let lease = Arc::new(AtomicU64::new(now_secs()));

                                // Control datagram listener: a lease renewal, or
                                // 0xFF = sync CDT immediately
                                let conn_datagram = connection.clone();
                                let datagram_lease = Arc::clone(&lease);
                                tokio::spawn(async move {
                                    while let Ok(datagram) = conn_datagram.receive_datagram().await {
                                        if datagram.len() != 1 {
                                            continue;
                                        }
                                        match datagram[0] {
                                            CTL_SYNC_CDT => {
                                                tokio::spawn(async { metrics::sync_cdt_once().await });
                                            }
                                            CTL_LEASE => {
                                                datagram_lease.store(now_secs(), Ordering::Relaxed);
                                            }
                                            _ => {}
                                        }
                                    }
                                });

                                // Dual-packet preemptive push loop: 1s telemetry packets
                                // interleaved with realtime access-log delta packets
                                let mut log_rx = LOG_BROADCAST.subscribe();
                                let mut interval = tokio::time::interval(Duration::from_secs(1));
                                loop {
                                    // A page that stops renewing is one whose client left while
                                    // its connection stayed up. Closing is what releases the
                                    // session: the stream and datagram listeners hold their own
                                    // handles, so leaving the loop alone would not.
                                    if now_secs().saturating_sub(lease.load(Ordering::Relaxed)) > LEASE_SECS {
                                        println!("[silphd] Client stopped renewing, dropping session: {}", connection.remote_address());
                                        connection.close(VarInt::from_u32(0), b"lease expired");
                                        break;
                                    }
                                    tokio::select! {
                                        _ = connection.closed() => {
                                            println!("[silphd] Client closed connection: {}", connection.remote_address());
                                            break;
                                        }
                                        res = log_rx.recv() => {
                                            // Drain the channel either way: a subscriber that
                                            // never reads would only make the sender lag.
                                            let Ok(log_packet) = res else { continue };
                                            // No separate subscription: the log table lives on
                                            // the page that holds the lease, so a session that
                                            // is still in this loop is one somebody is reading.
                                            // The lease check at the top of the loop is what
                                            // stops the frames, within one second of it lapsing.
                                            // Preemptively push the realtime single-log
                                            // frame and reset the 1s telemetry timer.
                                            // A failed send is not something to ignore: the datagram
                                            // is 64 B, so the only reason to refuse it is a
                                            // connection that is no longer there, and a loop that
                                            // ignores it keeps a frame a second going to a client
                                            // that has left.
                                            if connection.send_datagram(&log_packet).is_err() {
                                                println!("[silphd] Client unreachable, dropping session: {}", connection.remote_address());
                                                break;
                                            }
                                            interval.reset();
                                        }
                                        _ = interval.tick() => {
                                            // Collect hardware metrics (64-byte packed
                                            // struct with IPv6 CDT and float MB fields)
                                            let frame = metrics::collect_metrics();
                                            let bytes: &[u8] = bytemuck::bytes_of(&frame);
                                            if connection.send_datagram(bytes).is_err() {
                                                println!("[silphd] Client unreachable, dropping session: {}", connection.remote_address());
                                                break;
                                            }
                                        }
                                    }
                                }
                            }
                        }
                        Err(e) => {
                            eprintln!("[silphd] Accept error: {:?}", e);
                        }
                    }
                }
                Err(e) => {
                    eprintln!("[silphd] Session handshake error: {:?}", e);
                }
            }
        });
    }
}

/// Builds the endpoint the node-to-node link is served on, or None when the deployment does not
/// name a certificate for it.
///
/// The certificate is the site's own, so the peer verifies this node against the authority that
/// issued it rather than against a digest this daemon reissues at every restart.
async fn peer_endpoint() -> Result<Option<(u16, Endpoint<endpoint_side::Server>)>, Box<dyn std::error::Error>> {
    let (Ok(cert), Ok(key), Ok(port)) = (
        std::env::var("SILPHD_PEER_CERT"),
        std::env::var("SILPHD_PEER_KEY"),
        std::env::var("SILPHD_PEER_PORT"),
    ) else {
        return Ok(None);
    };
    let port: u16 = port.trim().parse()?;
    let identity = Identity::load_pemfiles(cert.trim(), key.trim()).await?;
    let config = ServerConfig::builder()
        .with_bind_default(port)
        .with_custom_transport(identity, wt_sync::peer_transport_config())
        .build();
    Ok(Some((port, Endpoint::server(config)?)))
}
