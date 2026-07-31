// 网络连接采集器 — EvtSubscribe on Security EventID=5156 (Windows only)
// start: acquire FilteringPlatformConnection 策略 + EvtSubscribe
// stop:  退出订阅线程 (EvtClose) + release 策略
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tokio::sync::mpsc;
use windows::core::PCWSTR;
use windows::Win32::Foundation::*;
use windows::Win32::System::EventLog::*;

use crate::pb;

use super::audit_policy::{AuditPolicyManager, Policy};
use super::util::{extract_pid, extract_xml, get_process_path, render_event_xml};

static NETWORK_TX: Mutex<Option<mpsc::Sender<pb::Event>>> = Mutex::new(None);

pub struct NetworkCollector {
    running: Arc<AtomicBool>,
    policies: Arc<AuditPolicyManager>,
    active: bool,
}

impl NetworkCollector {
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
        // 1. 获取审计策略引用（refcount 0→1 时执行 auditpol enable）
        self.policies.acquire(Policy::FilteringPlatformConnection);

        // 2. 设置全局发送通道
        *NETWORK_TX.lock().unwrap() = Some(tx);

        // 3. 启动订阅线程
        self.running.store(true, Ordering::SeqCst);
        let running = Arc::clone(&self.running);
        std::thread::spawn(move || {
            unsafe {
                let channel = windows::core::w!("Security");
                let query = windows::core::w!("*[System[(EventID=5156)]]");

                match EvtSubscribe(
                    EVT_HANDLE::default(),
                    HANDLE::default(),
                    PCWSTR::from_raw(channel.as_ptr()),
                    PCWSTR::from_raw(query.as_ptr()),
                    EVT_HANDLE::default(),
                    None::<*const core::ffi::c_void>,
                    Some(network_callback),
                    EvtSubscribeToFutureEvents.0,
                ) {
                    Ok(h) => {
                        tracing::info!("[NetworkMon] EvtSubscribe 成功 (5156)");
                        while running.load(Ordering::SeqCst) {
                            std::thread::sleep(Duration::from_millis(200));
                        }
                        let _ = EvtClose(h);
                        tracing::info!("[NetworkMon] EvtSubscribe 已关闭 (5156)");
                    }
                    Err(e) => {
                        tracing::error!("[NetworkMon] EvtSubscribe 失败: {:?}", e);
                    }
                }
            }
        });

        self.active = true;
        tracing::info!("[NetworkMon] 采集器已启动");
        Ok(())
    }

    pub fn stop(&mut self) -> anyhow::Result<()> {
        if !self.active {
            return Ok(());
        }
        self.running.store(false, Ordering::SeqCst);
        self.active = false;
        self.policies.release(Policy::FilteringPlatformConnection);
        tracing::info!("[NetworkMon] 采集器已停止");
        Ok(())
    }
}

unsafe extern "system" fn network_callback(
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
    if !xml.contains("EventID>5156") {
        return 0;
    }

    let pid = extract_pid(&xml, "ProcessID");
    let local_ip = extract_xml(&xml, "SourceAddress");
    let local_port_str = extract_xml(&xml, "SourcePort");
    let remote_ip = extract_xml(&xml, "DestAddress");
    let remote_port_str = extract_xml(&xml, "DestPort");
    let protocol_str = extract_xml(&xml, "Protocol");
    let direction_str = extract_xml(&xml, "Direction");

    let local_port = local_port_str.parse::<u32>().unwrap_or(0);
    let remote_port = remote_port_str.parse::<u32>().unwrap_or(0);
    let protocol = if protocol_str == "6" { "tcp" } else if protocol_str == "17" { "udp" } else { "other" };
    let direction = if direction_str.contains("14593") { "outbound" } else { "inbound" };

    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
    if let Some(tx) = NETWORK_TX.lock().unwrap().as_ref() {
        let _ = tx.blocking_send(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::NetworkConnection(pb::NetworkConnectionEvent {
                pid,
                process_name: extract_xml(&xml, "Application"),
                image_path: get_process_path(pid),
                local_ip,
                local_port,
                remote_ip,
                remote_port,
                protocol: protocol.to_string(),
                direction: direction.to_string(),
                timestamp_ns: now,
            })),
        });
    }
    0 // 继续订阅
}
