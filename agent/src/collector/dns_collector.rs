// DNS 查询采集器 — EvtSubscribe on Microsoft-Windows-DNS-Client/Operational EventID=3008 (Windows only)
// start: acquire DnsOperationalChannel 策略 (wevtutil enable) + EvtSubscribe
// stop:  退出订阅线程 (EvtClose) + release 策略 (wevtutil disable)
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tokio::sync::mpsc;
use windows::core::PCWSTR;
use windows::Win32::Foundation::*;
use windows::Win32::System::EventLog::*;

use crate::pb;

use super::audit_policy::{AuditPolicyManager, Policy};
use super::util::{extract_xml, render_event_xml};

static DNS_TX: Mutex<Option<mpsc::Sender<pb::Event>>> = Mutex::new(None);

pub struct DnsCollector {
    running: Arc<AtomicBool>,
    policies: Arc<AuditPolicyManager>,
    active: bool,
}

impl DnsCollector {
    pub fn new(policies: Arc<AuditPolicyManager>) -> Self {
        Self {
            running: Arc::new(AtomicBool::new(false)),
            policies,
            active: false,
        }
    }

    pub fn start(&mut self, tx: mpsc::Sender<pb::Event>) -> anyhow::Result<()> {
        if self.active {
            return Ok(());
        }
        // 1. 获取审计策略引用（refcount 0→1 时执行 wevtutil enable）
        self.policies.acquire(Policy::DnsOperationalChannel);

        // 2. 设置全局发送通道
        *DNS_TX.lock().unwrap() = Some(tx);

        // 3. 启动订阅线程
        self.running.store(true, Ordering::SeqCst);
        let running = Arc::clone(&self.running);
        std::thread::spawn(move || {
            unsafe {
                let channel = windows::core::w!("Microsoft-Windows-DNS-Client/Operational");
                let query = windows::core::w!("*[System[(EventID=3008)]]");

                match EvtSubscribe(
                    EVT_HANDLE::default(),
                    HANDLE::default(),
                    PCWSTR::from_raw(channel.as_ptr()),
                    PCWSTR::from_raw(query.as_ptr()),
                    EVT_HANDLE::default(),
                    None::<*const core::ffi::c_void>,
                    Some(dns_callback),
                    EvtSubscribeToFutureEvents.0,
                ) {
                    Ok(h) => {
                        tracing::info!("[DnsMon] EvtSubscribe 成功 (3008)");
                        while running.load(Ordering::SeqCst) {
                            std::thread::sleep(Duration::from_millis(200));
                        }
                        let _ = EvtClose(h);
                        tracing::info!("[DnsMon] EvtSubscribe 已关闭 (3008)");
                    }
                    Err(e) => {
                        tracing::error!("[DnsMon] EvtSubscribe 失败: {:?}", e);
                    }
                }
            }
        });

        self.active = true;
        tracing::info!("[DnsMon] 采集器已启动");
        Ok(())
    }

    pub fn stop(&mut self) -> anyhow::Result<()> {
        if !self.active {
            return Ok(());
        }
        self.running.store(false, Ordering::SeqCst);
        self.active = false;
        self.policies.release(Policy::DnsOperationalChannel);
        tracing::info!("[DnsMon] 采集器已停止");
        Ok(())
    }
}

unsafe extern "system" fn dns_callback(
    action: EVT_SUBSCRIBE_NOTIFY_ACTION,
    _usercontext: *const core::ffi::c_void,
    event: EVT_HANDLE,
) -> u32 {
    if action != EvtSubscribeActionDeliver {
        return 0;
    }
    let Some(xml) = render_event_xml(event) else {
        return 0;
    };
    if !xml.contains("EventID>3008") {
        return 0;
    }

    // DNS 事件的 PID 在 <Execution ProcessID='...'/> 属性中
    let mut pid: u64 = 0;
    if let Some(start) = xml.find("ProcessID='") {
        let s = start + "ProcessID='".len();
        if let Some(end) = xml[s..].find("'") {
            pid = xml[s..s + end].parse::<u32>().unwrap_or(0) as u64;
        }
    }

    let query_name = extract_xml(&xml, "QueryName");
    let query_type_str = extract_xml(&xml, "QueryType");
    let result_ips = extract_xml(&xml, "QueryResults");
    // QueryResults 格式: 以 ; 分隔，每个条目为:
    //   ::ffff:x.x.x.x  → A 记录 (IPv4-mapped IPv6)
    //   type: 5 domain  → CNAME
    //   type: 16 text   → 诊断信息 (跳过)
    //   裸 IPv4/IPv6     → A/AAAA 记录
    let ips: Vec<String> = result_ips
        .split(';')
        .filter_map(|s| {
            let s = s.trim();
            if s.is_empty() {
                return None;
            }
            if let Some(rest) = s.strip_prefix("type: ") {
                let rest = rest.trim();
                let type_end = rest.find(char::is_whitespace).unwrap_or(rest.len());
                let rec_type: u16 = rest[..type_end].parse().ok()?;
                let value = rest[type_end..].trim();
                match rec_type {
                    5 => Some(format!("CNAME {}", value)),
                    16 => None,
                    _ => None,
                }
            } else {
                if let Some(ipv4) = s.strip_prefix("::ffff:") {
                    Some(ipv4.to_string())
                } else if s.contains(':') || s.contains('.') {
                    Some(s.to_string())
                } else {
                    None
                }
            }
        })
        .collect();
    let result_ips = ips.join("; ");

    let qtype = match query_type_str.as_str() {
        "1" => "A",
        "28" => "AAAA",
        "5" => "CNAME",
        "15" => "MX",
        _ => &query_type_str,
    };

    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
    if let Some(tx) = DNS_TX.lock().unwrap().as_ref() {
        let _ = tx.blocking_send(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::DnsQuery(pb::DnsQueryEvent {
                pid,
                process_name: String::new(),
                query_name,
                query_type: qtype.to_string(),
                result_ips,
                timestamp_ns: now,
            })),
        });
    }
    0 // 继续订阅
}
