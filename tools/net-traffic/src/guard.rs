//! TrafficGuard: native Linux physical traffic monitoring, monthly reset and
//! archival, and a Resend tiered alert/fuse engine.
//! 
//! Second-precision physical outbound monitoring implemented as a resident
//! tokio task.

use std::fs::{self, File};
use std::io::{BufRead, BufReader};
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{LazyLock, Mutex};
use std::time::Duration;

use serde::{Deserialize, Serialize};

const STATE_DIR: &str = "/var/lib/net-traffic";
const STATE_FILE: &str = "/var/lib/net-traffic/traffic_state.json";

pub fn detect_default_interface() -> String {
    if let Ok(file) = File::open("/proc/net/route") {
        let reader = BufReader::new(file);
        for line in reader.lines().flatten() {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 && parts[1] == "00000000" {
                return parts[0].to_string();
            }
        }
    }
    "eth0".to_string()
}

// Thresholds (bytes)
const GIGABYTE: u64 = 1024 * 1024 * 1024;
const DAILY_WARN_BYTES: u64 = 1 * GIGABYTE;          // 1.00 GB daily warning
const MONTHLY_WARN_BYTES: u64 = 18 * GIGABYTE;       // 18.00 GB monthly warning (90%)
const MONTHLY_LIMIT_BYTES: u64 = 20 * GIGABYTE;      // 20.00 GB protective throttle
const MONTHLY_FUSE_BYTES: u64 = 30 * GIGABYTE;       // 30.00 GB graceful fuse (stop Web, keep the machine on)

fn gb(bytes: u64) -> f64 {
    bytes as f64 / GIGABYTE as f64
}

/// Site name used in alert subjects/bodies. Set SITE_NAME to your own site name;
/// alerts fall back to the project name when unset.
fn site_name() -> String {
    std::env::var("SITE_NAME").unwrap_or_else(|_| "Silphuu".to_string())
}

#[derive(Serialize, Deserialize, Debug, Clone)]
pub struct TrafficState {
    pub current_month: String,
    pub current_day: String,
    pub monthly_tx_bytes: u64,
    pub monthly_rx_bytes: u64,
    pub daily_tx_bytes: u64,
    pub daily_rx_bytes: u64,
    pub last_raw_tx: u64,
    pub last_raw_rx: u64,
    pub flag_daily_warn: bool,
    pub flag_monthly_warn: bool,
    pub flag_monthly_limit: bool,
    pub flag_monthly_fuse: bool,
}

impl Default for TrafficState {
    fn default() -> Self {
        Self {
            current_month: get_current_month(),
            current_day: get_current_day(),
            monthly_tx_bytes: 0,
            monthly_rx_bytes: 0,
            daily_tx_bytes: 0,
            daily_rx_bytes: 0,
            last_raw_tx: 0,
            last_raw_rx: 0,
            flag_daily_warn: false,
            flag_monthly_warn: false,
            flag_monthly_limit: false,
            flag_monthly_fuse: false,
        }
    }
}

static STATE: LazyLock<Mutex<TrafficState>> = LazyLock::new(|| Mutex::new(TrafficState::default()));

// Atomic cache read by metrics.rs (f32 MB values stored as bits)
static ATOMIC_MONTHLY_TX_MB: AtomicU64 = AtomicU64::new(0);
static ATOMIC_MONTHLY_RX_MB: AtomicU64 = AtomicU64::new(0);
static ATOMIC_DAILY_TX_MB: AtomicU64 = AtomicU64::new(0);
static ATOMIC_DAILY_RX_MB: AtomicU64 = AtomicU64::new(0);

pub fn get_traffic_stats() -> (f32, f32, f32, f32) {
    let d_rx = f32::from_bits(ATOMIC_DAILY_RX_MB.load(Ordering::Relaxed) as u32);
    let d_tx = f32::from_bits(ATOMIC_DAILY_TX_MB.load(Ordering::Relaxed) as u32);
    let m_rx = f32::from_bits(ATOMIC_MONTHLY_RX_MB.load(Ordering::Relaxed) as u32);
    let m_tx = f32::from_bits(ATOMIC_MONTHLY_TX_MB.load(Ordering::Relaxed) as u32);
    (d_rx, d_tx, m_rx, m_tx)
}

fn get_current_month() -> String {
    if let Ok(out) = std::process::Command::new("date").arg("+%Y-%m").output() {
        if let Ok(s) = String::from_utf8(out.stdout) {
            let t = s.trim();
            if !t.is_empty() {
                return t.to_string();
            }
        }
    }
    "2026-08".to_string()
}

fn get_current_day() -> String {
    if let Ok(out) = std::process::Command::new("date").arg("+%Y-%m-%d").output() {
        if let Ok(s) = String::from_utf8(out.stdout) {
            let t = s.trim();
            if !t.is_empty() {
                return t.to_string();
            }
        }
    }
    "2026-08-28".to_string()
}

fn read_raw_net_dev() -> (u64, u64) {
    let target_iface = detect_default_interface();
    let mut rx = 0u64;
    let mut tx = 0u64;
    if let Ok(file) = File::open("/proc/net/dev") {
        let reader = BufReader::new(file);
        for line in reader.lines().flatten() {
            if let Some(colon) = line.find(':') {
                let iface = line[..colon].trim();
                if iface == target_iface {
                    let fields: Vec<&str> = line[colon + 1..].split_whitespace().collect();
                    if fields.len() >= 9 {
                        rx = fields[0].parse().unwrap_or(0);
                        tx = fields[8].parse().unwrap_or(0);
                        return (rx, tx);
                    }
                }
            }
        }
    }
    (rx, tx)
}

fn load_state() {
    let mut state = STATE.lock().unwrap();
    if Path::new(STATE_FILE).exists() {
        if let Ok(data) = fs::read_to_string(STATE_FILE) {
            if let Ok(saved) = serde_json::from_str::<TrafficState>(&data) {
                *state = saved;
                let tx_mb = (state.monthly_tx_bytes as f32) / (1024.0 * 1024.0);
                let rx_mb = (state.monthly_rx_bytes as f32) / (1024.0 * 1024.0);
                ATOMIC_MONTHLY_TX_MB.store(tx_mb.to_bits() as u64, Ordering::Relaxed);
                ATOMIC_MONTHLY_RX_MB.store(rx_mb.to_bits() as u64, Ordering::Relaxed);
                println!(
                    "[TrafficGuard] Loaded persisted state: month={}, monthly_tx={:.2} GB, daily_tx={:.2} MB",
                    state.current_month,
                    state.monthly_tx_bytes as f64 / 1024.0 / 1024.0 / 1024.0,
                    state.daily_tx_bytes as f64 / 1024.0 / 1024.0
                );
                return;
            }
        }
    }

    // Initialize the last_raw counters
    let (cur_rx, cur_tx) = read_raw_net_dev();
    state.last_raw_rx = cur_rx;
    state.last_raw_tx = cur_tx;
}

fn persist_state() {
    let state = STATE.lock().unwrap();
    let _ = fs::create_dir_all(STATE_DIR);
    if let Ok(json) = serde_json::to_string_pretty(&*state) {
        let _ = fs::write(STATE_FILE, json);
    }
}

// ── Resend email sender ──
async fn send_resend_email(subject: &str, html_body: &str) {
    let api_key = match std::env::var("RESEND_API_KEY") {
        Ok(k) if !k.trim().is_empty() => k.trim().to_string(),
        _ => {
            eprintln!("[TrafficGuard] RESEND_API_KEY not set, skipping email: {}", subject);
            return;
        }
    };

    let to_email = std::env::var("ALERT_EMAIL_TO")
        .unwrap_or_else(|_| "admin@example.com".to_string());
    let from_email = std::env::var("ALERT_EMAIL_FROM")
        .unwrap_or_else(|_| "Net-Traffic Alert <onboarding@resend.dev>".to_string());

    let payload = serde_json::json!({
        "from": from_email,
        "to": [to_email],
        "subject": subject,
        "html": html_body
    });

    let payload_str = payload.to_string();

    let output = tokio::process::Command::new("curl")
        .args([
            "-s", "-X", "POST", "https://api.resend.com/emails",
            "-H", &format!("Authorization: Bearer {}", api_key),
            "-H", "Content-Type: application/json",
            "-d", &payload_str
        ])
        .output()
        .await;

    match output {
        Ok(out) if out.status.success() => {
            let res = String::from_utf8_lossy(&out.stdout);
            println!("[TrafficGuard] Email sent successfully via Resend: {} -> {}", subject, res.trim());
        }
        Ok(out) => {
            let err = String::from_utf8_lossy(&out.stderr);
            eprintln!("[TrafficGuard] Failed to send email: status={}, err={}", out.status, err);
        }
        Err(e) => {
            eprintln!("[TrafficGuard] Failed to invoke curl for Resend: {}", e);
        }
    }
}

// ── Main watchdog loop ──
pub async fn start_traffic_guard_loop() {
    load_state();

    let mut interval = tokio::time::interval(Duration::from_secs(1));
    let mut flush_counter = 0u64;

    loop {
        interval.tick().await;

        let (cur_rx, cur_tx) = read_raw_net_dev();
        let today = get_current_day();
        let this_month = get_current_month();

        let mut trigger_monthly_report = None;
        let mut trigger_daily_report = None;
        let mut trigger_daily_warn = false;
        let mut trigger_monthly_warn = false;
        let mut trigger_monthly_limit = false;
        let mut trigger_monthly_fuse = false;

        let cur_m_tx = {
            let mut state = STATE.lock().unwrap();

            // 1. Monthly rollover reset (1st of the month, 00:00:00)
            if this_month != state.current_month {
                let last_month_gb = state.monthly_tx_bytes as f64 / 1024.0 / 1024.0 / 1024.0;
                trigger_monthly_report = Some((state.current_month.clone(), last_month_gb));

                state.monthly_tx_bytes = 0;
                state.monthly_rx_bytes = 0;
                state.current_month = this_month.clone();
                state.flag_monthly_warn = false;
                state.flag_monthly_limit = false;
                state.flag_monthly_fuse = false;

                // Remove the throttle
                let _ = std::process::Command::new("tc")
                    .args(["qdisc", "del", "dev", &detect_default_interface(), "root"])
                    .output();
            }

            // 2. Day rollover reset
            if today != state.current_day {
                let yesterday_gb = state.daily_tx_bytes as f64 / 1024.0 / 1024.0 / 1024.0;
                trigger_daily_report = Some((state.current_day.clone(), yesterday_gb));

                state.daily_tx_bytes = 0;
                state.daily_rx_bytes = 0;
                state.current_day = today.clone();
                state.flag_daily_warn = false;
            }

            // 3. Compute the physical delta
            let delta_tx = if cur_tx >= state.last_raw_tx {
                cur_tx - state.last_raw_tx
            } else {
                cur_tx // counter reset on reboot
            };
            let delta_rx = if cur_rx >= state.last_raw_rx {
                cur_rx - state.last_raw_rx
            } else {
                cur_rx
            };

            state.last_raw_tx = cur_tx;
            state.last_raw_rx = cur_rx;
            state.monthly_tx_bytes += delta_tx;
            state.monthly_rx_bytes += delta_rx;
            state.daily_tx_bytes += delta_tx;
            state.daily_rx_bytes += delta_rx;

            // Update the atomic cache
            let tx_mb = (state.monthly_tx_bytes as f32) / (1024.0 * 1024.0);
            let rx_mb = (state.monthly_rx_bytes as f32) / (1024.0 * 1024.0);
            let d_tx_mb = (state.daily_tx_bytes as f32) / (1024.0 * 1024.0);
            let d_rx_mb = (state.daily_rx_bytes as f32) / (1024.0 * 1024.0);
            ATOMIC_MONTHLY_TX_MB.store(tx_mb.to_bits() as u64, Ordering::Relaxed);
            ATOMIC_MONTHLY_RX_MB.store(rx_mb.to_bits() as u64, Ordering::Relaxed);
            ATOMIC_DAILY_TX_MB.store(d_tx_mb.to_bits() as u64, Ordering::Relaxed);
            ATOMIC_DAILY_RX_MB.store(d_rx_mb.to_bits() as u64, Ordering::Relaxed);

            // 4. Tiered alert evaluation
            if state.daily_tx_bytes >= DAILY_WARN_BYTES && !state.flag_daily_warn {
                state.flag_daily_warn = true;
                trigger_daily_warn = true;
            }
            if state.monthly_tx_bytes >= MONTHLY_WARN_BYTES && !state.flag_monthly_warn {
                state.flag_monthly_warn = true;
                trigger_monthly_warn = true;
            }
            if state.monthly_tx_bytes >= MONTHLY_LIMIT_BYTES && !state.flag_monthly_limit {
                state.flag_monthly_limit = true;
                trigger_monthly_limit = true;
            }
            if state.monthly_tx_bytes >= MONTHLY_FUSE_BYTES && !state.flag_monthly_fuse {
                state.flag_monthly_fuse = true;
                trigger_monthly_fuse = true;
            }

            state.monthly_tx_bytes
        };

        // Run email sending and system commands asynchronously (outside the lock)
        if let Some((last_m, gb_val)) = trigger_monthly_report {
            tokio::spawn(async move {
                let site = site_name();
                let html = format!(
                    "<h2>[{site} 流量月报]</h2><p>上月（<b>{last_m}</b>）总物理出站流量：<b>{gb_val:.2} GB</b></p><p>本月月度配额（{:.0} GB）已自动重置清零。</p>",
                    gb(MONTHLY_LIMIT_BYTES)
                );
                send_resend_email(&format!("[{site} 流量月报] ({last_m}): {gb_val:.2} GB"), &html).await;
            });
        }

        if let Some((last_d, gb_val)) = trigger_daily_report {
            tokio::spawn(async move {
                let site = site_name();
                let html = format!(
                    "<h3>[{site} 流量日报]</h3><p>昨日（<b>{last_d}</b>）总物理出站流量：<b>{gb_val:.2} GB</b></p>"
                );
                send_resend_email(&format!("[{site} 流量日报] ({last_d}): {gb_val:.2} GB"), &html).await;
            });
        }

        if trigger_daily_warn {
            tokio::spawn(async move {
                let site = site_name();
                let html = format!(
                    "<h3>[警告] 单日流量异常预警</h3><p>今日物理出站流量已突破 <b>{:.2} GB</b>！请注意检查是否有爬虫或恶意盗链。</p>",
                    gb(DAILY_WARN_BYTES)
                );
                send_resend_email(
                    &format!("[警告] {site} 当日物理出站突破 {:.2} GB", gb(DAILY_WARN_BYTES)),
                    &html,
                )
                .await;
            });
        }

        if trigger_monthly_warn {
            let gb_val = cur_m_tx as f64 / GIGABYTE as f64;
            tokio::spawn(async move {
                let site = site_name();
                let pct = MONTHLY_WARN_BYTES * 100 / MONTHLY_LIMIT_BYTES;
                let html = format!(
                    "<h3>[预警] 月度额度预警 ({pct}%)</h3><p>本月物理出站已累计消耗 <b>{gb_val:.2} GB</b>（即将触及 {:.0} GB 月度额度上限）！</p>",
                    gb(MONTHLY_LIMIT_BYTES)
                );
                send_resend_email(
                    &format!(
                        "[预警] {site} 当月出站已达 {gb_val:.2} GB ({pct}% 额度)"
                    ),
                    &html,
                )
                .await;
            });
        }

        if trigger_monthly_limit {
            let gb_val = cur_m_tx as f64 / GIGABYTE as f64;
            tokio::spawn(async move {
                // Apply the tc throttle
                let iface = detect_default_interface();
                let _ = tokio::process::Command::new("tc")
                    .args(["qdisc", "replace", "dev", &iface, "root", "tbf", "rate", "128kbit", "burst", "32kbit", "latency", "400ms"])
                    .output()
                    .await;

                let site = site_name();
                let html = format!(
                    "<h3>[限速通知] 安全限速已生效</h3><p>本月物理出站已达到 <b>{gb_val:.2} GB</b>（超过 {:.0} GB 月度额度）。</p><p>为防止产生高额账单，已自动对网卡启动保护性限速。</p>",
                    gb(MONTHLY_LIMIT_BYTES)
                );
                send_resend_email(&format!("[限速通知] {site} 已自动启动流量保护"), &html).await;
            });
        }

        if trigger_monthly_fuse {
            let gb_val = cur_m_tx as f64 / GIGABYTE as f64;
            tokio::spawn(async move {
                // Graceful fuse: stop the web tier, cutting all public traffic
                // while keeping SSH available for remote debugging.
                let _ = tokio::process::Command::new("rc-service")
                    .args(["nginx", "stop"])
                    .output()
                    .await;
                let _ = tokio::process::Command::new("rc-service")
                    .args(["origin_server", "stop"])
                    .output()
                    .await;

                let site = site_name();
                let fuse_gb = gb(MONTHLY_FUSE_BYTES);
                let html = format!(
                    "<h2 style='color:red;'>[严重熔断] {fuse_gb:.0} GB 流量熔断已触发</h2><p>本月物理出站已突破 <b>{gb_val:.2} GB</b> 警戒线！</p><p>为彻底避免天价账单，已自动关停 Web 服务（公网流量降为 0）。</p><p><b>注：系统保持开机，SSH 仍然畅通，可随时登录排查。</b></p>"
                );
                send_resend_email(
                    &format!("[严重熔断] {site} 本月出站突破 {fuse_gb:.0} GB：Web 服务已关停"),
                    &html,
                )
                .await;
            });
        }

        // Persist to disk every 60 seconds
        flush_counter += 1;
        if flush_counter % 60 == 0 {
            persist_state();
        }
    }
}
