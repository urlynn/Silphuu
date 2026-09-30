use std::collections::HashMap;
use std::env;
use std::fs;
use std::io::{Read, Write};
use std::net::{IpAddr, SocketAddr};
use std::pin::Pin;
use std::sync::atomic::{AtomicI64, AtomicU16, AtomicU64, Ordering};
use std::sync::{Arc, LazyLock};
use std::time::{Duration, Instant};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixListener;
use tokio::sync::{mpsc, oneshot, Mutex as TokioMutex, RwLock as TokioRwLock};
use tokio::task::JoinSet;
use wtransport::config::{DnsLookupFuture, DnsResolver, QuicTransportConfig};
use wtransport::endpoint::endpoint_side;
use wtransport::quinn::congestion::CubicConfig;
use wtransport::quinn::rustls::pki_types::CertificateDer;
use wtransport::quinn::rustls::RootCertStore;
use wtransport::tls::client::build_default_tls_config;
use wtransport::tls::CertificateChain;
use wtransport::{ClientConfig, Connection, Endpoint};
use zstd::stream::read::Decoder;
use zstd::stream::write::Encoder;

/// Frame ids on a peer stream. The id selects the codec as well as the handler: events are JSON
/// and compress well, blobs are already entropy-coded media, where zstd would cost CPU to find
/// nothing.
pub const WT_FRAME_ID: u8 = 0x02;
pub const WT_FRAME_ID_BLOB: u8 = 0x03;

/// The `:path` the peer's connection is opened with. It is how the accepting end tells a
/// node-to-node link from a page: the two share one endpoint, and only a page wants the
/// telemetry push, a lease or the control datagrams.
pub const PEER_SYNC_PATH: &str = "/backend-sync";

const OP_SYNC_REQ: u8 = 1;
const OP_SYNC_RESP: u8 = 2;
const OP_BLOB_PUSH: u8 = 3;
const OP_BLOB_RESP: u8 = 4;

/// Bounds on the outbound link. The connection has no idle timeout (see peer_transport_config),
/// so nothing in the protocol ever notices a peer that died without closing — a restart, a
/// dropped route. These timeouts are what makes that survivable: a push either finishes or
/// fails, and a failure drops the cached connection so the next push reconnects.
const PEER_CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const PEER_IO_TIMEOUT: Duration = Duration::from_secs(15);

/// A blob round trip is not the same size of thing as an event round trip: one frame can carry
/// megabytes, and the receiver writes them to disk before it replies.
const BLOB_IO_TIMEOUT: Duration = Duration::from_secs(120);

static NEXT_PEER_REQ_ID: AtomicU64 = AtomicU64::new(100_000);

// The peer link's last round trip, in unix seconds: positive when one completed, negative when
// one failed, zero while the link has not carried one. Direction is not recorded, and the newer
// result replaces the older: a push this node sent and a push it received are the same fact for
// the page. It lives only in memory, so a restart of this daemon puts the mark back at zero.
static LAST_SYNC_MARK: AtomicI64 = AtomicI64::new(0);

// That same exchange's duration. The mark answers when it happened; this answers how long it
// took, which is the half a reader cannot recover from the mark.
static LAST_SYNC_RTT_MS: AtomicU16 = AtomicU16::new(0);

fn mark_sync_result(ok: bool, elapsed: Duration) {
    let now = chrono::Utc::now().timestamp().max(1);
    LAST_SYNC_MARK.store(if ok { now } else { -now }, Ordering::Relaxed);
    LAST_SYNC_RTT_MS.store(elapsed.as_millis().min(u16::MAX as u128) as u16, Ordering::Relaxed);
}

pub fn last_sync_mark() -> i64 {
    LAST_SYNC_MARK.load(Ordering::Relaxed)
}

pub fn peer_rtt_ms() -> u16 {
    LAST_SYNC_RTT_MS.load(Ordering::Relaxed)
}

// Global handle to the current Go UDS writer end
static ACTIVE_GO_TX: TokioRwLock<Option<mpsc::Sender<Vec<u8>>>> = TokioRwLock::const_new(None);

// Map of promises waiting for the Go response to a peer push event
// (req_id -> oneshot::Sender)
static PENDING_PEER_RESPONSES: LazyLock<TokioMutex<HashMap<u64, oneshot::Sender<Vec<u8>>>>> =
    LazyLock::new(|| TokioMutex::new(HashMap::new()));

fn encode_frame(op: u8, req_id: u64, payload: &[u8]) -> Vec<u8> {
    let mut frame = Vec::with_capacity(13 + payload.len());
    frame.extend_from_slice(&(payload.len() as u32).to_be_bytes());
    frame.push(op);
    frame.extend_from_slice(&req_id.to_be_bytes());
    frame.extend_from_slice(payload);
    frame
}

pub async fn start_sync_server() {
    let sock_path = match env::var("SYNC_UDS_SOCK") {
        Ok(p) if !p.trim().is_empty() => p,
        _ => {
            // SYNC_UDS_SOCK not configured: skip the gateway silently
            return;
        }
    };

    let _ = fs::remove_file(&sock_path);
    let listener = match UnixListener::bind(&sock_path) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("[wt_sync] Failed to bind UDS {}: {:?}", sock_path, e);
            return;
        }
    };

    println!("[wt_sync] Single UDS listener started at {}", sock_path);

    let peer_ip = env::var("SYNC_PEER_IP").ok();

    loop {
        if let Ok((socket, _)) = listener.accept().await {
            let (mut reader, mut writer) = socket.into_split();
            let (tx, mut rx) = mpsc::channel::<Vec<u8>>(64);

            {
                let mut guard = ACTIVE_GO_TX.write().await;
                *guard = Some(tx.clone());
            }

            // Writer task: pushes frames to Go
            let writer_task = tokio::spawn(async move {
                while let Some(frame) = rx.recv().await {
                    if let Err(e) = writer.write_all(&frame).await {
                        eprintln!("[wt_sync] Error writing frame to Go UDS: {:?}", e);
                        break;
                    }
                }
            });

            // Reader task: reads frames coming from Go
            let peer_ip_clone = peer_ip.clone();
            let tx_clone = tx.clone();

            tokio::spawn(async move {
                let mut hdr = [0u8; 13];
                while reader.read_exact(&mut hdr).await.is_ok() {
                    let payload_len = u32::from_be_bytes(hdr[0..4].try_into().unwrap()) as usize;
                    let op = hdr[4];
                    let req_id = u64::from_be_bytes(hdr[5..13].try_into().unwrap());
                    let mut payload = vec![0u8; payload_len];
                    if payload_len > 0 {
                        if let Err(e) = reader.read_exact(&mut payload).await {
                            eprintln!("[wt_sync] Error reading payload from Go UDS: {:?}", e);
                            break;
                        }
                    }

                    match op {
                        OP_SYNC_REQ => {
                            // Go pushes an event batch to the peer
                            tokio::spawn(relay_go_request(
                                payload,
                                peer_ip_clone.clone(),
                                WT_FRAME_ID,
                                OP_SYNC_RESP,
                                PEER_IO_TIMEOUT,
                                req_id,
                                tx_clone.clone(),
                            ));
                        }
                        OP_BLOB_PUSH => {
                            // Go pushes files from the sync face to the peer
                            tokio::spawn(relay_go_request(
                                payload,
                                peer_ip_clone.clone(),
                                WT_FRAME_ID_BLOB,
                                OP_BLOB_RESP,
                                BLOB_IO_TIMEOUT,
                                req_id,
                                tx_clone.clone(),
                            ));
                        }
                        OP_SYNC_RESP | OP_BLOB_RESP => {
                            // Go finished applying to memory; relay the piggybacked response
                            let mut pending = PENDING_PEER_RESPONSES.lock().await;
                            if let Some(sender) = pending.remove(&req_id) {
                                let _ = sender.send(payload);
                            }
                        }
                        _ => {}
                    }
                }

                // Go client disconnected; clear the active sender
                {
                    let mut guard = ACTIVE_GO_TX.write().await;
                    *guard = None;
                }
                writer_task.abort();
            });
        }
    }
}

/// The peer link, kept for the life of the process: the endpoint (which owns the UDP socket)
/// and the connection running on it.
///
/// Reconnecting costs a full handshake — a QUIC handshake, a TLS 1.3 handshake with certificate
/// pinning, and the WebTransport session — so the connection is made once and reused. The
/// endpoint is stored alongside it because dropping the endpoint closes the socket, and with it
/// the connection.
struct PeerLink {
    _endpoint: Endpoint<endpoint_side::Client>,
    connection: Connection,
}

static PEER_LINK: TokioRwLock<Option<PeerLink>> = TokioRwLock::const_new(None);

/// The idle policy of the sync link: hold it open indefinitely, and send nothing while it is
/// idle.
///
/// - `max_idle_timeout(None)`: an idle connection is never closed by either end. Both peers
///   negotiate the smaller value, so the server sets the same three values in main.rs; with
///   only one end changed the other end's 30-second default would still win.
/// - `keep_alive_interval(None)`: no heartbeat. Nothing is sent to say the link is alive.
/// - `mtu_discovery_config(None)`: quinn's DPLPMTUD is on by default and probes on a timer
///   (600 s), which is the only remaining periodic traffic on an idle connection — it sends a
///   padded PING whenever the connection has nothing else to send. Off, an idle link carries
///   zero bytes.
///
/// What this costs: a peer that dies without closing is never noticed by the protocol, so the
/// link is only as good as the timeout on each use. See PEER_IO_TIMEOUT.
/// The initial congestion window, in bytes, for both ends of the peer link and for the endpoint
/// pages use.
///
/// A fresh connection starts with this much in flight and doubles it every round trip, so on a
/// long path it decides how many round trips the first bytes of a transfer take. 32 KiB moves
/// every 32-64 KiB file one round trip sooner than quinn's 12 KiB default, and stays under the
/// largest flight the link between the two nodes carries.
const INITIAL_WINDOW_BYTES: u64 = 32 * 1024;

/// Applies the initial congestion window above, or the one SILPHD_INITIAL_WINDOW names.
///
/// The environment variable is how the value is measured: it lets one build be run at several
/// window sizes without a rebuild.
pub fn apply_initial_window(config: &mut QuicTransportConfig) {
    let bytes = match env::var("SILPHD_INITIAL_WINDOW") {
        Ok(raw) => match raw.trim().parse::<u64>() {
            Ok(bytes) => bytes,
            Err(_) => {
                eprintln!(
                    "[wt_sync] SILPHD_INITIAL_WINDOW is not a number: {raw:?}; using {INITIAL_WINDOW_BYTES}"
                );
                INITIAL_WINDOW_BYTES
            }
        },
        Err(_) => INITIAL_WINDOW_BYTES,
    };
    let mut cubic = CubicConfig::default();
    cubic.initial_window(bytes);
    config.congestion_controller_factory(Arc::new(cubic));
}

pub fn peer_transport_config() -> QuicTransportConfig {
    let mut config = QuicTransportConfig::default();
    config.max_idle_timeout(None);
    config.keep_alive_interval(None);
    config.mtu_discovery_config(None);
    apply_initial_window(&mut config);
    config
}

/// The name the peer's certificate is issued for, the port its peer endpoint binds, and the
/// addresses this node may reach that name at.
///
/// The link is verified against the authority the peer's certificate comes from, so what makes
/// the certificate valid is the name it carries — not a digest, which the peer reissues at every
/// daemon start.
#[derive(Debug)]
struct PeerName {
    host: String,
    port: u16,
    addrs: Vec<SocketAddr>,
}

fn peer_name() -> Result<PeerName, Box<dyn std::error::Error + Send + Sync>> {
    let host = env::var("SYNC_PEER_NAME").map_err(|_| "SYNC_PEER_NAME is required")?;
    let port: u16 = env::var("SYNC_PEER_PORT")
        .map_err(|_| "SYNC_PEER_PORT is required")?
        .trim()
        .parse()?;
    // The list holds bare addresses, and the port comes from SYNC_PEER_PORT: one value then
    // describes the port for every candidate, instead of repeating it in each entry.
    let raw = env::var("SYNC_PEER_IP").map_err(|_| "SYNC_PEER_IP is required")?;
    let mut addrs = Vec::new();
    for part in raw.split([',', ';', ' ']) {
        let trimmed = part.trim();
        if trimmed.is_empty() {
            continue;
        }
        let ip: IpAddr = trimmed
            .parse()
            .map_err(|_| format!("SYNC_PEER_IP is not an address: {trimmed:?}"))?;
        addrs.push(SocketAddr::new(ip, port));
    }
    if addrs.is_empty() {
        return Err("SYNC_PEER_IP lists no address".into());
    }
    Ok(PeerName { host, port, addrs })
}

/// Resolves the peer's name to the address this node reaches it at, instead of asking DNS.
///
/// The certificate names a domain the CDN answers for, so that name cannot also be the address
/// dialled; the deployment says which address the name stands for here.
#[derive(Debug)]
struct PeerResolver {
    host: String,
    addr: SocketAddr,
}

impl DnsResolver for PeerResolver {
    fn resolve(&self, host: &str) -> Pin<Box<dyn DnsLookupFuture>> {
        let same = host == self.host;
        let addr = self.addr;
        Box::pin(async move { Ok(if same { Some(addr) } else { None }) })
    }
}

/// Reads the authority the peer's certificate is issued by. Read once per connect, so the
/// candidates of one connect share it.
async fn peer_roots() -> Result<Arc<RootCertStore>, Box<dyn std::error::Error + Send + Sync>> {
    let ca_path = env::var("SYNC_PEER_CA_FILE").map_err(|_| "SYNC_PEER_CA_FILE is required")?;
    let chain = CertificateChain::load_pemfile(&ca_path).await?;
    let mut roots = RootCertStore::empty();
    for cert in chain.as_slice() {
        roots.add(CertificateDer::from(cert.der().to_vec()))?;
    }
    Ok(Arc::new(roots))
}

/// Builds the client configuration for one candidate address: the authority above, the idle
/// policy, and a resolver that hands this address back for the peer's name.
fn peer_client_config(
    host: &str,
    port: u16,
    addr: SocketAddr,
    roots: Arc<RootCertStore>,
) -> Result<ClientConfig, Box<dyn std::error::Error + Send + Sync>> {
    let tls = build_default_tls_config(roots, None);

    Ok(ClientConfig::builder()
        .with_bind_default()
        .with_custom_tls_and_transport(tls, peer_transport_config())
        .dns_resolver(PeerResolver {
            host: format!("{}:{}", host, port),
            addr,
        })
        .build())
}

/// Opens a new connection to the peer.
///
/// Every candidate address is dialled at once and the first to finish wins: they are different
/// routes to the same node, so whichever answers first is the one to use. A candidate that loses
/// costs nothing beyond its own handshake.
async fn connect_peer(
    peer: &PeerName,
) -> Result<PeerLink, Box<dyn std::error::Error + Send + Sync>> {
    let roots = peer_roots().await?;

    let mut attempts = JoinSet::new();
    for addr in peer.addrs.iter().copied() {
        let host = peer.host.clone();
        let port = peer.port;
        let roots = Arc::clone(&roots);
        attempts.spawn(async move {
            let config = peer_client_config(&host, port, addr, roots)?;
            let endpoint = Endpoint::client(config)?;
            let url = format!("https://{}:{}{}", host, port, PEER_SYNC_PATH);
            let connection = tokio::time::timeout(PEER_CONNECT_TIMEOUT, endpoint.connect(url))
                .await??;
            Ok::<PeerLink, Box<dyn std::error::Error + Send + Sync>>(PeerLink {
                _endpoint: endpoint,
                connection,
            })
        });
    }

    let mut last_error: Option<Box<dyn std::error::Error + Send + Sync>> = None;
    while let Some(joined) = attempts.join_next().await {
        match joined {
            Ok(Ok(link)) => {
                attempts.abort_all();
                return Ok(link);
            }
            Ok(Err(e)) => last_error = Some(e),
            Err(e) => last_error = Some(Box::new(e)),
        }
    }

    Err(last_error.unwrap_or_else(|| "SYNC_PEER_IP listed no address".into()))
}

/// The connection to push on: the cached one when there is one, a new one otherwise.
///
/// A cached connection is returned without a liveness check. There is nothing to check against:
/// with no idle timeout and no heartbeat, a peer that restarted looks exactly like an idle one.
/// The failure shows up on the next use, and the caller drops the cache then.
async fn peer_connection() -> Result<Connection, Box<dyn std::error::Error + Send + Sync>> {
    {
        let guard = PEER_LINK.read().await;
        if let Some(link) = guard.as_ref() {
            return Ok(link.connection.clone());
        }
    }

    let link = connect_peer(&peer_name()?).await?;

    let connection = link.connection.clone();
    {
        let mut guard = PEER_LINK.write().await;
        *guard = Some(link);
    }
    Ok(connection)
}

/// Drops the cached link, so the next push makes a new connection. Closing it here would be
/// pointless: the connection is suspect precisely because the peer stopped answering.
async fn drop_peer_link() {
    let mut guard = PEER_LINK.write().await;
    *guard = None;
}

/// One push on the given connection: open a stream, send the frame, read the reply.
///
/// The frame id selects the codec: zstd for events, raw for blobs.
async fn push_on_connection(
    connection: &Connection,
    frame_id: u8,
    payload: &[u8],
) -> Result<Vec<u8>, Box<dyn std::error::Error + Send + Sync>> {
    let (mut send_stream, mut recv_stream) = connection.open_bi().await?.await?;

    let mut frame = Vec::with_capacity(1 + payload.len());
    frame.push(frame_id);
    if frame_id == WT_FRAME_ID_BLOB {
        frame.extend_from_slice(payload);
    } else {
        // Compress with zstd level 1
        let mut encoder = Encoder::new(&mut frame, 1)?;
        encoder.write_all(payload)?;
        encoder.finish()?;
    }

    send_stream.write_all(&frame).await?;
    send_stream.finish().await?; // send FIN

    let mut response = Vec::new();
    recv_stream.read_to_end(&mut response).await?;

    if response.is_empty() || response[0] != frame_id {
        return Err("Invalid response from peer".into());
    }

    if frame_id == WT_FRAME_ID_BLOB {
        return Ok(response[1..].to_vec());
    }

    let mut decompressed = Vec::new();
    {
        let mut decoder = Decoder::new(&response[1..])?;
        decoder.read_to_end(&mut decompressed)?;
    }

    Ok(decompressed)
}

async fn forward_go_push_to_peer(
    payload: Vec<u8>,
    peer_ip: Option<String>,
    frame_id: u8,
    timeout: Duration,
) -> Result<Vec<u8>, Box<dyn std::error::Error + Send + Sync>> {
    // No round trip leaves this node in either of the two early returns below, so neither is
    // recorded: a mark here would put a timestamp on the page for a channel that carried
    // nothing, which is what "never synced" and "sync failed" both mean to lose.
    if payload.is_empty() {
        return Ok(Vec::new());
    }

    if peer_ip.is_none() {
        eprintln!("[wt_sync] SYNC_PEER_IP not configured, skipping outbound sync.");
        return Ok(Vec::new());
    }

    let push = async {
        let connection = peer_connection().await?;
        push_on_connection(&connection, frame_id, &payload).await
    };

    let started = Instant::now();
    match tokio::time::timeout(timeout, push).await {
        Ok(Ok(response)) => {
            mark_sync_result(true, started.elapsed());
            Ok(response)
        }
        Ok(Err(e)) => {
            drop_peer_link().await;
            mark_sync_result(false, started.elapsed());
            Err(e)
        }
        Err(_) => {
            drop_peer_link().await;
            mark_sync_result(false, started.elapsed());
            Err("peer push timed out".into())
        }
    }
}

/// Forwards one request from Go to the peer and answers Go with the peer's reply.
///
/// The reply opcode is the request's own, so the two directions stay distinguishable on a socket
/// that carries both faces.
async fn relay_go_request(
    payload: Vec<u8>,
    peer_ip: Option<String>,
    frame_id: u8,
    resp_op: u8,
    timeout: Duration,
    req_id: u64,
    tx: mpsc::Sender<Vec<u8>>,
) {
    let resp_payload = match forward_go_push_to_peer(payload, peer_ip, frame_id, timeout).await {
        Ok(p) => p,
        Err(e) => {
            eprintln!("[wt_sync] Peer forward error (op {}): {:?}", resp_op, e);
            Vec::new()
        }
    };
    let frame = encode_frame(resp_op, req_id, &resp_payload);
    let _ = tx.send(frame).await;
}

pub async fn handle_incoming_wt_stream(
    mut send_stream: wtransport::SendStream,
    mut recv_stream: wtransport::RecvStream,
    initial_buf: Vec<u8>,
) {
    tokio::spawn(async move {
        // The mark covers the whole inbound path, including the reads and decodes that return
        // early, because the question is only whether this exchange completed.
        let started = Instant::now();
        let result = process_incoming_wt(&mut send_stream, &mut recv_stream, initial_buf).await;
        mark_sync_result(result.is_ok(), started.elapsed());
        if let Err(e) = result {
            eprintln!("[wt_sync] Incoming WT processing error: {:?}", e);
        }
    });
}

async fn process_incoming_wt(
    send_stream: &mut wtransport::SendStream,
    recv_stream: &mut wtransport::RecvStream,
    initial_buf: Vec<u8>,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let mut request = initial_buf;
    recv_stream.read_to_end(&mut request).await?;

    if request.is_empty() || (request[0] != WT_FRAME_ID && request[0] != WT_FRAME_ID_BLOB) {
        return Err("Invalid incoming WT frame".into());
    }

    // The frame id says which face this is, and it is what the reply carries back.
    let frame_id = request[0];
    let is_blob = frame_id == WT_FRAME_ID_BLOB;
    let (req_op, timeout) = if is_blob {
        (OP_BLOB_PUSH, BLOB_IO_TIMEOUT)
    } else {
        (OP_SYNC_REQ, PEER_IO_TIMEOUT)
    };

    let payload = if is_blob {
        request[1..].to_vec()
    } else {
        let mut decompressed = Vec::new();
        let mut decoder = Decoder::new(&request[1..])?;
        decoder.read_to_end(&mut decompressed)?;
        decompressed
    };

    let go_tx = {
        let guard = ACTIVE_GO_TX.read().await;
        guard.clone()
    };

    let go_tx = match go_tx {
        Some(tx) => tx,
        None => {
            return Err("Go UDS client not connected; cannot deliver incoming WT frames".into());
        }
    };

    let req_id = NEXT_PEER_REQ_ID.fetch_add(1, Ordering::Relaxed);
    let (resp_tx, resp_rx) = oneshot::channel();

    {
        let mut pending = PENDING_PEER_RESPONSES.lock().await;
        pending.insert(req_id, resp_tx);
    }

    let frame = encode_frame(req_op, req_id, &payload);
    if let Err(e) = go_tx.send(frame).await {
        let mut pending = PENDING_PEER_RESPONSES.lock().await;
        pending.remove(&req_id);
        return Err(format!("Failed to send frame to Go UDS: {:?}", e).into());
    }

    // Wait for Go to finish applying to memory and relay the piggybacked data
    let go_response = match tokio::time::timeout(timeout, resp_rx).await {
        Ok(Ok(payload)) => payload,
        Ok(Err(_)) => {
            return Err("Go UDS connection closed while awaiting sync response".into());
        }
        Err(_) => {
            let mut pending = PENDING_PEER_RESPONSES.lock().await;
            pending.remove(&req_id);
            return Err("Timed out waiting for Go UDS response".into());
        }
    };

    let mut reply = Vec::with_capacity(1 + go_response.len());
    reply.push(frame_id);
    if is_blob {
        reply.extend_from_slice(&go_response);
    } else {
        let mut encoder = Encoder::new(&mut reply, 1)?;
        encoder.write_all(&go_response)?;
        encoder.finish()?;
    }

    send_stream.write_all(&reply).await?;
    send_stream.finish().await?;

    Ok(())
}

