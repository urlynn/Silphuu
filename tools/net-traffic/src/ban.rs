//! Ban scanner: a resident background loop, independent of WebTransport.
//!
//! Incrementally scans nginx access_json.log. For IPs that bypass the CDN
//! (DIRECT) and receive a 418, STRIKE_LIMIT strikes within a 7-day rolling
//! window push a `drop` rule to the Aliyun security group (zero traffic). Bans
//! expire automatically after 7 days. Ban records are persisted and
//! re-applied (with extended expiry) after a process restart.

use std::collections::{HashMap, VecDeque};
use std::fs;
use std::io::{Read, Seek, SeekFrom};
use std::path::Path;
use std::sync::{LazyLock, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
#[cfg(unix)]
use std::os::unix::fs::MetadataExt;

use serde::{Deserialize, Serialize};

const LOG_PATH: &str = "/var/log/nginx/access_json.log";
const PERSIST_PATH: &str = "/var/lib/net-traffic/banlist.json";
const ATTACKS_PATH: &str = "/var/lib/net-traffic/attacks.json";
const BAN_SECS: i64 = 7 * 24 * 3600; // ban duration: 7 days
const WINDOW_SECS: i64 = 7 * 24 * 3600; // strike counting window: 7 days
const STRIKE_LIMIT: usize = 2; // two 418 strikes trigger a ban

fn region() -> String {
    std::env::var("SENTINEL_ECS_REGION").unwrap_or_else(|_| "cn-wulanchabu".to_string())
}
fn security_group() -> String {
    std::env::var("SENTINEL_ECS_SG").unwrap_or_else(|_| "sg-0jl0ua10wyyvzronup9d".to_string())
}

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

// ── Ban records and shared state ──
#[derive(Clone, Serialize, Deserialize)]
struct BanRecord {
    rule_id: String,
    banned_at: i64,
    expire_at: i64,
}

/// ip -> ban record (in memory + on disk)
static BANS: LazyLock<Mutex<HashMap<String, BanRecord>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));
/// (ip, time) of the log line that triggered each ban, so /status can
/// highlight the exact offending line
static TRIGGERS: LazyLock<Mutex<Vec<(String, String)>>> = LazyLock::new(|| Mutex::new(Vec::new()));

/// Persistence: ban list + trigger highlights, both on disk, nothing lost on restart
#[derive(Serialize, Deserialize, Default)]
struct PersistState {
    bans: HashMap<String, BanRecord>,
    triggers: Vec<(String, String)>,
}

// ── Attack signature rules (instant ban) ──
/// attacks.json rule-file schema: any hit from a DIRECT source bans on first offense
#[derive(Clone, Default, Deserialize)]
struct AttackRules {
    ext: Vec<String>,    // last URI segment ends with .<ext>
    file: Vec<String>,   // last URI segment equals the file name exactly
    prefix: Vec<String>, // URI path starts with the string
    eq: Vec<String>,     // URI path equals the string exactly
}

static ATTACKS: LazyLock<Mutex<AttackRules>> = LazyLock::new(|| Mutex::new(AttackRules::default()));

fn reload_attacks() {
    if let Ok(s) = fs::read_to_string(ATTACKS_PATH) {
        match serde_json::from_str::<AttackRules>(&s) {
            Ok(r) => *ATTACKS.lock().unwrap() = r,
            Err(e) => eprintln!("[ban] attacks.json parse error, keep previous: {e}"),
        }
    }
}

fn attack_rules() -> AttackRules {
    ATTACKS.lock().unwrap().clone()
}

fn match_attack(uri: &str, r: &AttackRules) -> bool {
    let path = uri.split('?').next().unwrap_or(uri);
    let last = path.rsplit('/').next().unwrap_or("");
    for e in &r.ext {
        if !e.is_empty() && last.ends_with(&format!(".{e}")) {
            return true;
        }
    }
    for f in &r.file {
        if last == f.as_str() {
            return true;
        }
    }
    for p in &r.prefix {
        if !p.is_empty() && path.starts_with(p.as_str()) {
            return true;
        }
    }
    for p in &r.eq {
        if path == p.as_str() {
            return true;
        }
    }
    false
}

/// Exempt addresses such as local testing; never banned
fn is_exempt(ip: &str) -> bool {
    ip == "127.0.0.1" || ip == "::1"
}

// ── Aliyun CLI wrapper and retry policy ──
const MAX_NETWORK_RETRIES: usize = 3; // at most 3 attempts total (network failures only)

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AliyunError {
    Network(String),
    Api(String),
    Other(String),
}

impl std::fmt::Display for AliyunError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AliyunError::Network(s) => write!(f, "Network Error: {s}"),
            AliyunError::Api(s) => write!(f, "API Error: {s}"),
            AliyunError::Other(s) => write!(f, "Error: {s}"),
        }
    }
}

fn classify_aliyun_error(msg: &str) -> AliyunError {
    let lower = msg.to_lowercase();

    // API business/parameter/permission errors: never retry
    let is_api = lower.contains("errorcode:")
        || lower.contains("sdk.servererror")
        || lower.contains("sdk.clienterror")
        || lower.contains("invalidparam")
        || lower.contains("requestid:")
        || lower.contains("\"code\":")
        || lower.contains("forbidden")
        || lower.contains("invalidsecuritygroup")
        || lower.contains("notfound")
        || lower.contains("alreadyexists");

    if is_api {
        return AliyunError::Api(msg.to_string());
    }

    // Only network-layer failures are allowed to retry
    let is_network = lower.contains("dial tcp")
        || lower.contains("connection refused")
        || lower.contains("connection reset")
        || lower.contains("no such host")
        || lower.contains("i/o timeout")
        || lower.contains("timed out")
        || lower.contains("tls handshake")
        || lower.contains("network is unreachable")
        || lower.contains("temporary failure in name resolution")
        || lower.contains("broken pipe")
        || lower.contains("httptimeout")
        || lower.contains("sdk.httperror")
        || lower.contains("clientexception");

    if is_network {
        return AliyunError::Network(msg.to_string());
    }

    AliyunError::Other(msg.to_string())
}

async fn run_aliyun(args: &[&str]) -> Result<String, AliyunError> {
    for attempt in 1..=MAX_NETWORK_RETRIES {
        let mut cmd = tokio::process::Command::new("aliyun");
        cmd.args(args);
        let out = match cmd.output().await {
            Ok(o) => o,
            Err(e) => {
                let err = AliyunError::Network(format!("aliyun spawn/io error: {e}"));
                if attempt < MAX_NETWORK_RETRIES {
                    eprintln!(
                        "[ban] aliyun {:?} spawn/network error (attempt {}/{}): {}. Retrying in 1s...",
                        args.get(1).unwrap_or(&""), attempt, MAX_NETWORK_RETRIES, e
                    );
                    tokio::time::sleep(Duration::from_millis(1000)).await;
                    continue;
                } else {
                    eprintln!(
                        "[ban] aliyun {:?} spawn/network error exhausted {} attempts: {}",
                        args.get(1).unwrap_or(&""), MAX_NETWORK_RETRIES, e
                    );
                    return Err(err);
                }
            }
        };

        let stdout = String::from_utf8_lossy(&out.stdout).to_string();
        let stderr = String::from_utf8_lossy(&out.stderr).to_string();
        let combined = format!("{stdout}\n{stderr}");

        if out.status.success() {
            let classified = classify_aliyun_error(&stdout);
            if let AliyunError::Api(api_err) = classified {
                eprintln!(
                    "[ban] aliyun {:?} returned API error (no retry): {}",
                    args.get(1).unwrap_or(&""), api_err.trim()
                );
                return Err(AliyunError::Api(api_err));
            }
            return Ok(stdout);
        }

        let classified = classify_aliyun_error(&combined);
        match classified {
            AliyunError::Network(ref net_err) => {
                if attempt < MAX_NETWORK_RETRIES {
                    eprintln!(
                        "[ban] aliyun {:?} network failure (attempt {}/{}): {}. Retrying in 1s...",
                        args.get(1).unwrap_or(&""), attempt, MAX_NETWORK_RETRIES, net_err.trim()
                    );
                    tokio::time::sleep(Duration::from_millis(1000)).await;
                    continue;
                } else {
                    eprintln!(
                        "[ban] aliyun {:?} network failure exhausted {} attempts: {}",
                        args.get(1).unwrap_or(&""), MAX_NETWORK_RETRIES, net_err.trim()
                    );
                    return Err(classified);
                }
            }
            AliyunError::Api(ref api_err) => {
                eprintln!(
                    "[ban] aliyun {:?} API error (no retry): {}",
                    args.get(1).unwrap_or(&""), api_err.trim()
                );
                return Err(classified);
            }
            AliyunError::Other(ref other_err) => {
                eprintln!(
                    "[ban] aliyun {:?} non-network failure (no retry): {}",
                    args.get(1).unwrap_or(&""), other_err.trim()
                );
                return Err(classified);
            }
        }
    }
    Err(AliyunError::Network("Exceeded maximum retry attempts".to_string()))
}

fn ban_cidr(ip: &str) -> (bool, String) {
    // A colon means IPv6, otherwise IPv4
    if ip.contains(':') {
        (true, format!("{ip}/128"))
    } else {
        (false, format!("{ip}/32"))
    }
}

/// Add an inbound drop rule to the security group (port 80, priority 1, idempotent)
async fn authorize_ban(ip: &str) -> Result<(), AliyunError> {
    let (is_v6, cidr) = ban_cidr(ip);
    let region = region();
    let sg = security_group();
    let mut args = vec![
        "ecs", "AuthorizeSecurityGroup",
        "--RegionId", &region,
        "--SecurityGroupId", &sg,
        "--IpProtocol", "tcp",
        "--PortRange", "80/80",
        "--Policy", "drop",
        "--Priority", "1",
        "--NicType", "intranet",
    ];
    if is_v6 {
        args.push("--Ipv6SourceCidrIp");
    } else {
        args.push("--SourceCidrIp");
    }
    args.push(&cidr);
    run_aliyun(&args).await?;
    Ok(())
}

/// Look up the SecurityGroupRuleId of the rule just created from the security group attributes
async fn find_rule_id(ip: &str) -> Result<Option<String>, AliyunError> {
    let (is_v6, cidr) = ban_cidr(ip);
    let region = region();
    let sg = security_group();
    let out = run_aliyun(&[
        "ecs", "DescribeSecurityGroupAttribute",
        "--RegionId", &region,
        "--SecurityGroupId", &sg,
        "--Direction", "ingress",
    ]).await?;
    let v: serde_json::Value = serde_json::from_str(&out).map_err(|e| AliyunError::Other(e.to_string()))?;
    if let Some(arr) = v.pointer("/Permissions/Permission").and_then(|p| p.as_array()) {
        for item in arr {
            let port = item.get("PortRange").and_then(|s| s.as_str()).unwrap_or("");
            let proto = item.get("IpProtocol").and_then(|s| s.as_str()).unwrap_or("").to_uppercase();
            let policy = item.get("Policy").and_then(|s| s.as_str()).unwrap_or("");
            let pri = item.get("Priority").and_then(|s| s.as_u64()).unwrap_or(0);
            let src = if is_v6 {
                item.get("Ipv6SourceCidrIp").and_then(|s| s.as_str()).unwrap_or("")
            } else {
                item.get("SourceCidrIp").and_then(|s| s.as_str()).unwrap_or("")
            };
            let rid = item.get("SecurityGroupRuleId").and_then(|s| s.as_str()).unwrap_or("");
            if port == "80/80" && proto == "TCP" && policy.eq_ignore_ascii_case("drop")
                && pri == 1 && src == cidr && !rid.is_empty()
            {
                return Ok(Some(rid.to_string()));
            }
        }
    }
    Ok(None)
}

/// Delete precisely by rule ID (unban)
async fn revoke_rule(rule_id: &str) -> Result<(), AliyunError> {
    let region = region();
    let sg = security_group();
    run_aliyun(&[
        "ecs", "RevokeSecurityGroup",
        "--RegionId", &region,
        "--SecurityGroupId", &sg,
        "--SecurityGroupRuleId.1", rule_id,
    ]).await?;
    Ok(())
}

// ── Status access / persistence ──
fn is_banned(ip: &str) -> bool {
    BANS.lock().unwrap().contains_key(ip)
}

fn add_ban(ip: &str, rule_id: String, now: i64) {
    let rec = BanRecord { rule_id, banned_at: now, expire_at: now + BAN_SECS };
    BANS.lock().unwrap().insert(ip.to_string(), rec);
    persist();
    eprintln!("[ban] BANNED {ip} until {}", now + BAN_SECS);
}

fn record_trigger(ip: &str, time: &str) {
    TRIGGERS.lock().unwrap().push((ip.to_string(), time.to_string()));
}

fn persist() {
    let state = {
        let b = BANS.lock().unwrap();
        let t = TRIGGERS.lock().unwrap();
        PersistState { bans: (*b).clone(), triggers: t.clone() }
    };
    let json = serde_json::to_string(&state).unwrap_or_default();
    if let Some(dir) = Path::new(PERSIST_PATH).parent() {
        let _ = fs::create_dir_all(dir);
    }
    let _ = fs::write(PERSIST_PATH, json);
}

fn load_persisted() {
    if let Ok(s) = fs::read_to_string(PERSIST_PATH) {
        if let Ok(st) = serde_json::from_str::<PersistState>(&s) {
            *BANS.lock().unwrap() = st.bans;
            *TRIGGERS.lock().unwrap() = st.triggers;
            eprintln!("[ban] loaded {} persisted bans", BANS.lock().unwrap().len());
        }
    }
}

/// Label used by metrics when reading historical log lines:
/// BANNED (the line that triggered a ban) / PROXY / DIRECT
pub fn tag_for_log(ip: &str, time: &str, via: &str) -> String {
    {
        let t = TRIGGERS.lock().unwrap();
        if t.iter().any(|(i, tm)| i == ip && tm == time) {
            return "BANNED".to_string();
        }
    }
    if via.is_empty() {
        // Old logs have no via field; infer from whether the ip is empty:
        // empty = DIRECT, otherwise = CLIENT
        if ip.is_empty() { "DIRECT".to_string() } else { "CLIENT".to_string() }
    } else if via.eq_ignore_ascii_case("PROXY") || via.eq_ignore_ascii_case("VIACDN") {
        "CLIENT".to_string()
    } else {
        via.to_string()
    }
}

// ── Incremental log reading ──
struct Tail {
    inode: u64,
    offset: u64,
}

impl Tail {
    fn new() -> Self {
        Tail { inode: 0, offset: 0 }
    }
    /// Return the log lines appended since the last read (handles rotation)
    fn next_lines(&mut self) -> Vec<String> {
        let meta = match fs::metadata(LOG_PATH) {
            Ok(m) => m,
            Err(_) => return Vec::new(),
        };
        let size = meta.len();
        if self.inode == 0 {
            self.inode = meta.ino();
            self.offset = 0;
        } else if meta.ino() != self.inode {
            self.inode = meta.ino();
            self.offset = 0; // rotated, start from the beginning
        }
        if self.offset > size {
            self.offset = 0; // file was truncated
        }
        let mut f = match fs::File::open(LOG_PATH) {
            Ok(f) => f,
            Err(_) => return Vec::new(),
        };
        if f.seek(SeekFrom::Start(self.offset)).is_err() {
            return Vec::new();
        }
        if self.offset > 0 && self.offset < size {
            // Drop a possible partial line left over from the previous read to avoid gluing a bad line
        }
        let mut buf = Vec::new();
        if f.read_to_end(&mut buf).is_err() {
            return Vec::new();
        }
        self.offset = size;
        String::from_utf8_lossy(&buf).lines().map(|s| s.to_string()).collect()
    }
}

fn parse_line(line: &str) -> Option<(String, String, u64, String, String)> {
    let v: serde_json::Value = serde_json::from_str(line).ok()?;
    let ip = v.get("ip").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let via = v.get("via").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let status = v.get("status").and_then(|s| s.as_u64()).unwrap_or(0);
    let time = v.get("time").and_then(|s| s.as_str()).unwrap_or("").to_string();
    let uri = v.get("uri").and_then(|s| s.as_str()).unwrap_or("").to_string();
    Some((ip, via, status, time, uri))
}

/// Perform the actual ban: add the security group rule, look up the rule_id,
/// persist, and record the trigger line for highlighting
async fn trigger_ban(ip: &str, time: &str) {
    let now = now_secs();
    let rid = match authorize_ban(ip).await {
        Ok(_) => match find_rule_id(ip).await {
            Ok(r) => r,
            Err(e) => {
                eprintln!("[ban] rule lookup fail {ip}: {e}");
                None
            }
        },
        Err(e) => {
            eprintln!("[ban] authorize fail {ip}: {e}");
            None
        }
    };
    if let Some(rid) = rid {
        add_ban(ip, rid, now);
        record_trigger(ip, time);
    }
}

// ── Main loop ──
pub async fn start_ban_scan_loop() {
    load_persisted();
    reload_attacks();
    let mut tail = Tail::new();
    // Processed offset: consume the pre-existing log backlog first so startup
    // does not ban on historical lines
    tail.next_lines();
    let mut counters: HashMap<String, VecDeque<i64>> = HashMap::new();
    let mut interval = tokio::time::interval(Duration::from_millis(250));

    loop {
        interval.tick().await;
        reload_attacks();
        let rules = attack_rules();
        for line in tail.next_lines() {
            let trimmed = line.trim();
            if trimmed.is_empty() {
                continue;
            }

            // Live broadcast to all active WebTransport clients (single raw frame)
            if let Ok(v) = serde_json::from_str::<serde_json::Value>(trimmed) {
                let time_str = v.get("time").and_then(|s| s.as_str()).unwrap_or("");
                let ip_str = v.get("ip").and_then(|s| s.as_str()).unwrap_or("");
                let method_str = v.get("method").and_then(|s| s.as_str()).unwrap_or("GET");
                let uri_str = v.get("uri").and_then(|s| s.as_str()).unwrap_or("");
                let status_num = v.get("status").and_then(|s| s.as_u64()).unwrap_or(200);
                let bytes_num = v.get("bytes").and_then(|s| s.as_u64()).unwrap_or(0);
                let cost_num = v.get("cost").and_then(|s| s.as_f64()).unwrap_or(0.0);
                let ua_str = v.get("ua").and_then(|s| s.as_str()).unwrap_or("");
                let via_raw = v.get("via").and_then(|s| s.as_str()).unwrap_or("");

                let size_str = crate::metrics::format_bytes_rust(bytes_num);
                let cost_str = format!("{:.1} ms", cost_num * 1000.0);
                let ua_summary = crate::metrics::extract_native_ua_comment(ua_str);
                let via_tag = tag_for_log(ip_str, time_str, via_raw);

                let row = serde_json::json!([
                    time_str, ip_str, method_str, uri_str, status_num, size_str, cost_str, ua_summary, via_tag
                ]);
                crate::broadcast_access_log(row);
            }

            if let Some((ip, via, status, time, uri)) = parse_line(trimmed) {
                let direct = via.eq_ignore_ascii_case("DIRECT") && !ip.is_empty() && !is_exempt(&ip);
                if !direct {
                    continue;
                }
                // Instant ban: an attack-signature hit (regardless of status code) bans on first offense
                if match_attack(&uri, &rules) && !is_banned(&ip) {
                    trigger_ban(&ip, &time).await;
                    continue;
                }
                // Base rule: 2 strikes of 418 from a DIRECT source within the 7-day window
                if status == 418 {
                    let now = now_secs();
                    let dq = counters.entry(ip.clone()).or_insert_with(VecDeque::new);
                    dq.push_back(now);
                    while dq.front().map_or(false, |&t| now - t > WINDOW_SECS) {
                        dq.pop_front();
                    }
                    if dq.len() >= STRIKE_LIMIT && !is_banned(&ip) {
                        trigger_ban(&ip, &time).await;
                    }
                }
            }
        }
        // Auto-unban expired entries
        let now = now_secs();
        let expired: Vec<(String, String)> = BANS
            .lock()
            .unwrap()
            .iter()
            .filter(|(_, r)| now >= r.expire_at)
            .map(|(ip, r)| (ip.clone(), r.rule_id.clone()))
            .collect();
        for (ip, rid) in expired {
            match revoke_rule(&rid).await {
                Ok(_) => {
                    BANS.lock().unwrap().remove(&ip);
                    eprintln!("[ban] unban success {ip}");
                }
                Err(AliyunError::Api(e)) => {
                    // API error (rule missing/already deleted/invalid params): never retry,
                    // just drop it from the pending-unban list
                    BANS.lock().unwrap().remove(&ip);
                    eprintln!("[ban] unban {ip} discarded due to API error (no retry): {e}");
                }
                Err(e) => {
                    // Network failure already retried 3 times internally; remove after
                    // retries are exhausted to avoid an endless loop every 250ms
                    BANS.lock().unwrap().remove(&ip);
                    eprintln!("[ban] unban fail {ip} after network retries exhausted, removed from queue: {e}");
                }
            }
        }
        persist();
    }
}