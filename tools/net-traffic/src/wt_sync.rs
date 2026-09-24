use std::collections::HashMap;
use std::env;
use std::fs;
use std::io::{Read, Write};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::LazyLock;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixListener;
use tokio::sync::{mpsc, oneshot, Mutex as TokioMutex, RwLock as TokioRwLock};
use wtransport::tls::Sha256Digest;
use wtransport::{ClientConfig, Endpoint};
use zstd::stream::read::Decoder;
use zstd::stream::write::Encoder;

const WT_FRAME_ID: u8 = 0x02;
const OP_SYNC_REQ: u8 = 1;
const OP_SYNC_RESP: u8 = 2;

static NEXT_PEER_REQ_ID: AtomicU64 = AtomicU64::new(100_000);

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
    let peer_hash = env::var("SYNC_PEER_CERT_HASH").ok();

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
            let peer_hash_clone = peer_hash.clone();
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
                            // Go pushes an event to the peer
                            let tx_inner = tx_clone.clone();
                            let ip = peer_ip_clone.clone();
                            let hash = peer_hash_clone.clone();
                            tokio::spawn(async move {
                                let resp = forward_go_push_to_peer(payload, ip, hash).await;
                                let resp_payload = match resp {
                                    Ok(p) => p,
                                    Err(e) => {
                                        eprintln!("[wt_sync] Peer forward error: {:?}", e);
                                        Vec::new()
                                    }
                                };
                                let frame = encode_frame(OP_SYNC_RESP, req_id, &resp_payload);
                                let _ = tx_inner.send(frame).await;
                            });
                        }
                        OP_SYNC_RESP => {
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

async fn forward_go_push_to_peer(
    payload: Vec<u8>,
    peer_ip: Option<String>,
    peer_hash: Option<String>,
) -> Result<Vec<u8>, Box<dyn std::error::Error + Send + Sync>> {
    if payload.is_empty() {
        return Ok(Vec::new());
    }

    let peer = match peer_ip {
        Some(ref p) => p,
        None => {
            eprintln!("[wt_sync] SYNC_PEER_IP not configured, skipping outbound sync.");
            return Ok(Vec::new());
        }
    };

    let url = format!("https://{}:39443/backend-sync", peer);

    let config = if let Some(hash_hex) = peer_hash {
        let digest = if let Ok(hash_bytes) = hex::decode(hash_hex) {
            if let Ok(arr) = <[u8; 32]>::try_from(hash_bytes.as_slice()) {
                Some(Sha256Digest::new(arr))
            } else {
                None
            }
        } else {
            None
        };

        if let Some(d) = digest {
            ClientConfig::builder()
                .with_bind_default()
                .with_server_certificate_hashes(vec![d])
                .build()
        } else {
            return Err("Invalid SYNC_PEER_CERT_HASH".into());
        }
    } else {
        return Err("SYNC_PEER_CERT_HASH environment variable is required for CONNECTOR".into());
    };

    let endpoint = Endpoint::client(config)?;
    let conn = endpoint.connect(url).await?;
    let (mut send_stream, mut recv_stream) = conn.open_bi().await?.await?;

    // Compress with zstd level 1
    let mut compressed = Vec::new();
    compressed.push(WT_FRAME_ID);
    {
        let mut encoder = Encoder::new(&mut compressed, 1)?;
        encoder.write_all(&payload)?;
        encoder.finish()?;
    }

    send_stream.write_all(&compressed).await?;
    send_stream.finish().await?; // send FIN

    let mut response_compressed = Vec::new();
    recv_stream.read_to_end(&mut response_compressed).await?;

    if response_compressed.is_empty() || response_compressed[0] != WT_FRAME_ID {
        return Err("Invalid response from peer".into());
    }

    let mut decompressed = Vec::new();
    {
        let mut decoder = Decoder::new(&response_compressed[1..])?;
        decoder.read_to_end(&mut decompressed)?;
    }

    Ok(decompressed)
}

pub async fn handle_incoming_wt_stream(
    mut send_stream: wtransport::SendStream,
    mut recv_stream: wtransport::RecvStream,
    initial_buf: Vec<u8>,
) {
    tokio::spawn(async move {
        if let Err(e) = process_incoming_wt(&mut send_stream, &mut recv_stream, initial_buf).await {
            eprintln!("[wt_sync] Incoming WT processing error: {:?}", e);
        }
    });
}

async fn process_incoming_wt(
    send_stream: &mut wtransport::SendStream,
    recv_stream: &mut wtransport::RecvStream,
    initial_buf: Vec<u8>,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let mut request_compressed = initial_buf;
    recv_stream.read_to_end(&mut request_compressed).await?;

    if request_compressed.is_empty() || request_compressed[0] != WT_FRAME_ID {
        return Err("Invalid incoming WT frame".into());
    }

    let mut decompressed = Vec::new();
    {
        let mut decoder = Decoder::new(&request_compressed[1..])?;
        decoder.read_to_end(&mut decompressed)?;
    }

    let go_tx = {
        let guard = ACTIVE_GO_TX.read().await;
        guard.clone()
    };

    let go_tx = match go_tx {
        Some(tx) => tx,
        None => {
            return Err("Go UDS client not connected; cannot deliver incoming WT events".into());
        }
    };

    let req_id = NEXT_PEER_REQ_ID.fetch_add(1, Ordering::Relaxed);
    let (resp_tx, resp_rx) = oneshot::channel();

    {
        let mut pending = PENDING_PEER_RESPONSES.lock().await;
        pending.insert(req_id, resp_tx);
    }

    let frame = encode_frame(OP_SYNC_REQ, req_id, &decompressed);
    if let Err(e) = go_tx.send(frame).await {
        let mut pending = PENDING_PEER_RESPONSES.lock().await;
        pending.remove(&req_id);
        return Err(format!("Failed to send frame to Go UDS: {:?}", e).into());
    }

    // Wait for Go to finish applying to memory and relay the piggybacked data (15s timeout)
    let go_response = match tokio::time::timeout(std::time::Duration::from_secs(15), resp_rx).await {
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

    let mut compressed_reply = Vec::new();
    compressed_reply.push(WT_FRAME_ID);
    {
        let mut encoder = Encoder::new(&mut compressed_reply, 1)?;
        encoder.write_all(&go_response)?;
        encoder.finish()?;
    }

    send_stream.write_all(&compressed_reply).await?;
    send_stream.finish().await?;

    Ok(())
}

