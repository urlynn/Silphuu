use std::fs::File;
use std::io::{BufRead, BufReader};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Instant;

#[repr(C, packed)]
#[derive(Clone, Copy, Default, Debug, bytemuck::Pod, bytemuck::Zeroable)]
pub struct MetricFrame {
    pub rx_kbps: f32,          // 4 bytes: inbound instant rate (KB/s)
    pub tx_kbps: f32,          // 4 bytes: outbound instant rate (KB/s)
    pub daily_rx_mb: f32,      // 4 bytes: daily inbound total (MB)
    pub daily_tx_mb: f32,      // 4 bytes: daily outbound total (MB)
    pub total_rx_mb: f32,      // 4 bytes: monthly inbound total (MB)
    pub total_tx_mb: f32,      // 4 bytes: monthly outbound total (MB)
    pub cdt_v6_mb: f32,        // 4 bytes: Aliyun CDT IPv6 official (MB)
    pub cdt_v4_mb: f32,        // 4 bytes: Aliyun CDT IPv4 official (MB)
    pub cdt_used_mb: f32,      // 4 bytes: Aliyun CDT total egress (MB)
    pub cpu_x100: u16,         // 2 bytes: CPU usage * 100
    pub ram_used_mb: u16,      // 2 bytes: RAM used (MB)
    pub ram_total_mb: u16,     // 2 bytes: RAM physical total (MB, /proc/meminfo)
    pub disk_used_mb: u32,     // 4 bytes: disk used (MB, statvfs)
    pub disk_total_mb: u32,    // 4 bytes: disk total (MB, statvfs)
    pub load_5m_x100: u16,     // 2 bytes: 5-minute load average * 100
} // 52 bytes

static LAST_RX: AtomicU64 = AtomicU64::new(0);
static LAST_TX: AtomicU64 = AtomicU64::new(0);
static LAST_TIME: parking_lot::RwLock<Option<Instant>> = parking_lot::RwLock::new(None);

pub fn collect_metrics() -> MetricFrame {
    let (cur_rx, cur_tx) = read_net_dev();
    let (cpu_x100, ram_used_mb, ram_total_mb) = read_sys_info();
    let (_, load_5m_x100) = read_loadavg();
    let (disk_used_mb, disk_total_mb) = read_disk_usage();

    let mut rx_kbps: f32 = 0.0;
    let mut tx_kbps: f32 = 0.0;

    let now = Instant::now();
    let mut last_t = LAST_TIME.write();
    if let Some(prev_time) = *last_t {
        let elapsed = now.duration_since(prev_time).as_secs_f32();
        if elapsed > 0.001 {
            let prev_rx = LAST_RX.load(Ordering::Relaxed);
            let prev_tx = LAST_TX.load(Ordering::Relaxed);
            if cur_rx >= prev_rx && cur_tx >= prev_tx {
                rx_kbps = ((cur_rx - prev_rx) as f32 / 1024.0) / elapsed;
                tx_kbps = ((cur_tx - prev_tx) as f32 / 1024.0) / elapsed;
            }
        }
    }
    *last_t = Some(now);
    LAST_RX.store(cur_rx, Ordering::Relaxed);
    LAST_TX.store(cur_tx, Ordering::Relaxed);

    let (guard_d_rx, guard_d_tx, guard_m_rx, guard_m_tx) = crate::guard::get_traffic_stats();
    let daily_rx_mb = if guard_d_rx > 0.0 { guard_d_rx } else { 0.0 };
    let daily_tx_mb = if guard_d_tx > 0.0 { guard_d_tx } else { 0.0 };

    let total_tx_mb = if guard_m_tx > 0.0 { guard_m_tx } else { (cur_tx as f32) / (1024.0 * 1024.0) };
    let total_rx_mb = if guard_m_rx > 0.0 { guard_m_rx } else { (cur_rx as f32) / (1024.0 * 1024.0) };

    let (cdt_v6_mb, cdt_v4_mb, cdt_used_mb) = read_official_cdt();

    MetricFrame {
        rx_kbps,
        tx_kbps,
        daily_rx_mb,
        daily_tx_mb,
        total_rx_mb,
        total_tx_mb,
        cdt_v6_mb,
        cdt_v4_mb,
        cdt_used_mb,
        cpu_x100,
        ram_used_mb,
        ram_total_mb,
        disk_used_mb,
        disk_total_mb,
        load_5m_x100,
    }
}

fn read_disk_usage() -> (u32, u32) {
    unsafe {
        let mut stat: libc::statvfs = std::mem::zeroed();
        let path = std::ffi::CString::new("/").unwrap();
        if libc::statvfs(path.as_ptr(), &mut stat) == 0 {
            let total_blocks = stat.f_blocks as u64;
            let free_blocks = stat.f_bavail as u64;
            let bsize = stat.f_frsize as u64;
            let total_mb = ((total_blocks * bsize) / (1024 * 1024)) as u32;
            let used_mb = (((total_blocks.saturating_sub(free_blocks)) * bsize) / (1024 * 1024)) as u32;
            return (used_mb, total_mb);
        }
    }
    (0, 0)
}

fn read_loadavg() -> (u16, u16) {
    if let Ok(file) = File::open("/proc/loadavg") {
        let reader = BufReader::new(file);
        if let Some(Ok(line)) = reader.lines().next() {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 {
                let l1: f32 = parts[0].parse().unwrap_or(0.0);
                let l5: f32 = parts[1].parse().unwrap_or(0.0);
                return ((l1 * 100.0).round() as u16, (l5 * 100.0).round() as u16);
            }
        }
    }
    (0, 0)
}

fn read_net_dev() -> (u64, u64) {
    let mut total_rx: u64 = 0;
    let mut total_tx: u64 = 0;

    if let Ok(file) = File::open("/proc/net/dev") {
        let reader = BufReader::new(file);
        for line in reader.lines().flatten() {
            if let Some(colon) = line.find(':') {
                let iface = line[..colon].trim();
                if iface == "lo" {
                    continue; // filter loopback
                }
                let parts: Vec<&str> = line[colon + 1..].split_whitespace().collect();
                if parts.len() >= 9 {
                    let rx: u64 = parts[0].parse().unwrap_or(0);
                    let tx: u64 = parts[8].parse().unwrap_or(0);
                    total_rx += rx;
                    total_tx += tx;
                }
            }
        }
    }
    (total_rx, total_tx)
}

static LAST_CPU_TOTAL: AtomicU64 = AtomicU64::new(0);
static LAST_CPU_WORK: AtomicU64 = AtomicU64::new(0);

fn read_sys_info() -> (u16, u16, u16) {
    let mut ram_used_mb = 0;
    let mut ram_total_mb = 512;
    if let Ok(file) = File::open("/proc/meminfo") {
        let reader = BufReader::new(file);
        let mut total_kb: u64 = 0;
        let mut avail_kb: u64 = 0;
        for line in reader.lines().flatten() {
            if line.starts_with("MemTotal:") {
                let parts: Vec<&str> = line.split_whitespace().collect();
                if parts.len() >= 2 {
                    total_kb = parts[1].parse().unwrap_or(0);
                }
            } else if line.starts_with("MemAvailable:") {
                let parts: Vec<&str> = line.split_whitespace().collect();
                if parts.len() >= 2 {
                    avail_kb = parts[1].parse().unwrap_or(0);
                }
            }
        }
        if total_kb > 0 {
            ram_total_mb = (total_kb / 1024) as u16;
            if total_kb > avail_kb {
                ram_used_mb = ((total_kb - avail_kb) / 1024) as u16;
            }
        }
    }

    let cpu_x100 = read_cpu_pct();
    (cpu_x100, ram_used_mb, ram_total_mb)
}

fn read_cpu_pct() -> u16 {
    if let Ok(file) = File::open("/proc/stat") {
        let reader = BufReader::new(file);
        if let Some(Ok(line)) = reader.lines().next() {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() > 4 && parts[0] == "cpu" {
                let mut total: u64 = 0;
                let mut work: u64 = 0;
                for (i, part) in parts[1..].iter().enumerate() {
                    let val: u64 = part.parse().unwrap_or(0);
                    total += val;
                    if i < 3 { // user, nice, system
                        work += val;
                    }
                }
                let prev_total = LAST_CPU_TOTAL.swap(total, Ordering::Relaxed);
                let prev_work = LAST_CPU_WORK.swap(work, Ordering::Relaxed);
                if prev_total > 0 && total > prev_total {
                    let delta_total = (total - prev_total) as f32;
                    let delta_work = (work - prev_work) as f32;
                    let pct = (delta_work / delta_total) * 100.0;
                    let x100 = (pct * 100.0).round().clamp(0.0, 10000.0) as u16;
                    return x100;
                }
            }
        }
    }
    0
}

static OFFICIAL_CDT_TOTAL_MB: AtomicU64 = AtomicU64::new(0);
static OFFICIAL_CDT_V6_MB: AtomicU64 = AtomicU64::new(0);
static OFFICIAL_CDT_V4_MB: AtomicU64 = AtomicU64::new(0);

pub async fn start_cdt_sync_loop() {
    sync_cdt_once().await;

    let mut interval = tokio::time::interval(std::time::Duration::from_secs(3600));
    loop {
        interval.tick().await;
        sync_cdt_once().await;
    }
}

pub async fn sync_cdt_once() {
    let mut attempt = 0;
    const MAX_NETWORK_RETRIES: usize = 3;

    loop {
        attempt += 1;
        let output = tokio::process::Command::new("aliyun")
            .args([
                "cdt",
                "ListCdtInternetTraffic",
                "--version",
                "2021-08-13",
                "--endpoint",
                "cdt.aliyuncs.com",
                "--force",
            ])
            .output()
            .await;

        match output {
            Ok(out) => {
                let stdout = String::from_utf8_lossy(&out.stdout);
                let stderr = String::from_utf8_lossy(&out.stderr);
                let combined = format!("{stdout}\n{stderr}");

                if out.status.success() {
                    if let Ok(v) = serde_json::from_str::<serde_json::Value>(&stdout) {
                        if let Some(details) = v.get("TrafficDetails").and_then(|d| d.as_array()).and_then(|a| a.first()) {
                            let mut v6_bytes = 0u64;
                            let mut v4_bytes = 0u64;
                            if let Some(products) = details.get("ProductTrafficDetails").and_then(|p| p.as_array()) {
                                for prod in products {
                                    let name = prod.get("Product").and_then(|p| p.as_str()).unwrap_or("");
                                    let traf = prod.get("Traffic").and_then(|t| t.as_u64()).unwrap_or(0);
                                    if name.eq_ignore_ascii_case("ipv6bandwidth") {
                                        v6_bytes += traf;
                                    } else if name.eq_ignore_ascii_case("publicip") {
                                        v4_bytes += traf;
                                    }
                                }
                            }
                            let total_bytes = details.get("Traffic").and_then(|t| t.as_u64()).unwrap_or(v6_bytes + v4_bytes);

                            let v6_mb = (v6_bytes as f32) / (1024.0 * 1024.0);
                            let v4_mb = (v4_bytes as f32) / (1024.0 * 1024.0);
                            let total_mb = (total_bytes as f32) / (1024.0 * 1024.0);

                            OFFICIAL_CDT_V6_MB.store(v6_mb.to_bits() as u64, Ordering::Relaxed);
                            OFFICIAL_CDT_V4_MB.store(v4_mb.to_bits() as u64, Ordering::Relaxed);
                            OFFICIAL_CDT_TOTAL_MB.store(total_mb.to_bits() as u64, Ordering::Relaxed);

                            println!(
                                "[net-traffic] Official CDT synced: Total={:.2} MB, V6={:.2} MB, V4={:.2} MB",
                                total_mb, v6_mb, v4_mb
                            );
                            return;
                        }
                    }
                    eprintln!("[net-traffic] Official CDT sync parse error: {}", stdout.trim());
                    return; // API returned successfully but the payload failed to parse; no retry
                }

                // Distinguish API errors from network failures
                let lower = combined.to_lowercase();
                let is_api = lower.contains("errorcode:")
                    || lower.contains("sdk.servererror")
                    || lower.contains("sdk.clienterror")
                    || lower.contains("requestid:")
                    || lower.contains("\"code\":")
                    || lower.contains("forbidden");

                if is_api {
                    eprintln!("[net-traffic] Official CDT sync API error (no retry): {}", stderr.trim());
                    return; // any API-level error: never retry
                }

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
                    || lower.contains("sdk.httperror")
                    || lower.contains("clientexception");

                if is_network && attempt < MAX_NETWORK_RETRIES {
                    eprintln!(
                        "[net-traffic] Official CDT sync network error (attempt {}/{}): {}. Retrying in 1s...",
                        attempt, MAX_NETWORK_RETRIES, stderr.trim()
                    );
                    tokio::time::sleep(std::time::Duration::from_millis(1000)).await;
                    continue;
                } else {
                    eprintln!("[net-traffic] Official CDT sync error: {}", stderr.trim());
                    return;
                }
            }
            Err(e) => {
                if attempt < MAX_NETWORK_RETRIES {
                    eprintln!(
                        "[net-traffic] Official CDT spawn error (attempt {}/{}): {}. Retrying in 1s...",
                        attempt, MAX_NETWORK_RETRIES, e
                    );
                    tokio::time::sleep(std::time::Duration::from_millis(1000)).await;
                    continue;
                } else {
                    eprintln!("[net-traffic] Official CDT spawn failed: {}", e);
                    return;
                }
            }
        }
    }
}

pub fn read_official_cdt() -> (f32, f32, f32) {
    let v6 = f32::from_bits(OFFICIAL_CDT_V6_MB.load(Ordering::Relaxed) as u32);
    let v4 = f32::from_bits(OFFICIAL_CDT_V4_MB.load(Ordering::Relaxed) as u32);
    let tot = f32::from_bits(OFFICIAL_CDT_TOTAL_MB.load(Ordering::Relaxed) as u32);
    (v6, v4, tot)
}

pub fn read_history_logs_zstd(limit: usize) -> Vec<u8> {
    let raw_json = read_history_logs_json(limit);
    let mut compressed = Vec::new();
    let _ = zstd::stream::copy_encode(raw_json.as_bytes(), &mut compressed, 1);
    compressed
}


fn parse_log_row(line: &str) -> Option<serde_json::Value> {
    let trimmed = line.trim();
    if trimmed.is_empty() {
        return None;
    }
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

        let size_str = format_bytes_rust(bytes_num);
        let cost_str = format!("{:.1} ms", cost_num * 1000.0);
        let ua_summary = extract_native_ua_comment(ua_str);
        let via_tag = crate::ban::tag_for_log(ip_str, time_str, via_raw);

        Some(serde_json::json!([
            time_str,
            ip_str,
            method_str,
            uri_str,
            status_num,
            size_str,
            cost_str,
            ua_summary,
            via_tag,
        ]))
    } else {
        None
    }
}

pub fn read_history_logs_json(limit: usize) -> String {
    let mut rows: Vec<serde_json::Value> = Vec::new();
    if let Ok(file) = File::open("/var/log/nginx/access_json.log") {
        let reader = BufReader::new(file);
        let lines: Vec<String> = reader.lines().flatten().collect();
        let start = if lines.len() > limit { lines.len() - limit } else { 0 };
        for line in lines[start..].iter().rev() {
            if let Some(row) = parse_log_row(line) {
                rows.push(row);
            }
        }
    }

    let resp = serde_json::json!({
        "cols": ["time", "ip", "method", "uri", "status", "size", "cost", "ua", "via"],
        "data": rows,
    });
    serde_json::to_string(&resp).unwrap_or_else(|_| "{}".to_string())
}

pub fn read_history_logs_by_range_zstd(start_ts: i64, end_ts: i64) -> Vec<u8> {
    let raw_json = read_history_logs_by_range(start_ts, end_ts);
    let mut compressed = Vec::new();
    let _ = zstd::stream::copy_encode(raw_json.as_bytes(), &mut compressed, 1);
    compressed
}

pub fn read_history_logs_by_range(start_ts: i64, end_ts: i64) -> String {
    let mut rows: Vec<serde_json::Value> = Vec::new();
    if let Ok(file) = File::open("/var/log/nginx/access_json.log") {
        let reader = BufReader::new(file);
        let lines: Vec<String> = reader.lines().flatten().collect();
        for line in lines.iter().rev() {
            if let Some(row) = parse_log_row(line) {
                if let Some(time_str) = row.get(0).and_then(|t| t.as_str()) {
                    if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(time_str) {
                        let ts = dt.timestamp();
                        if ts > end_ts {
                            continue;
                        }
                        if ts < start_ts {
                            break;
                        }
                        rows.push(row);
                    } else {
                        rows.push(row);
                    }
                }
            }
        }
    }

    let resp = serde_json::json!({
        "cols": ["time", "ip", "method", "uri", "status", "size", "cost", "ua", "via"],
        "data": rows,
    });
    serde_json::to_string(&resp).unwrap_or_else(|_| "{}".to_string())
}

pub fn format_bytes_rust(bytes: u64) -> String {
    if bytes < 1024 {
        format!("{} B", bytes)
    } else if bytes < 1024 * 1024 {
        format!("{:.1} KB", bytes as f64 / 1024.0)
    } else if bytes < 1024 * 1024 * 1024 {
        format!("{:.2} MB", bytes as f64 / (1024.0 * 1024.0))
    } else {
        format!("{:.2} GB", bytes as f64 / (1024.0 * 1024.0 * 1024.0))
    }
}

pub fn extract_native_ua_comment(ua: &str) -> &str {
    if let (Some(start), Some(end)) = (ua.find('('), ua.find(')')) {
        if start < end {
            return &ua[start + 1..end];
        }
    }
    ua
}
