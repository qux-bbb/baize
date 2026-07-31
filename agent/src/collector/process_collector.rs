// 进程创建采集器 — EvtSubscribe on Security EventID=4688 (Windows only)
// start: acquire ProcessCreation 策略 + EvtSubscribe
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
use super::util::{extract_pid, extract_xml, render_event_xml};

static PROCESS_TX: Mutex<Option<mpsc::Sender<pb::Event>>> = Mutex::new(None);

pub struct ProcessCollector {
    running: Arc<AtomicBool>,
    policies: Arc<AuditPolicyManager>,
    active: bool,
}

impl ProcessCollector {
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
        self.policies.acquire(Policy::ProcessCreation);
        // 2. 设置全局发送通道
        *PROCESS_TX.lock().unwrap() = Some(tx);

        // 3. 启动订阅线程（EvtSubscribe 回调在内部线程触发）
        self.running.store(true, Ordering::SeqCst);
        let running = Arc::clone(&self.running);
        std::thread::spawn(move || {
            unsafe {
                let channel = windows::core::w!("Security");
                let query = windows::core::w!("*[System[(EventID=4688)]]");

                match EvtSubscribe(
                    EVT_HANDLE::default(), // null session (local)
                    HANDLE::default(),     // no signal event (blocking callback)
                    PCWSTR::from_raw(channel.as_ptr()),
                    PCWSTR::from_raw(query.as_ptr()),
                    EVT_HANDLE::default(), // no bookmark
                    None::<*const core::ffi::c_void>,
                    Some(process_callback),
                    EvtSubscribeToFutureEvents.0,
                ) {
                    Ok(h) => {
                        tracing::info!("[ProcessMon] EvtSubscribe 成功 (4688)");
                        // 等待 stop 信号
                        while running.load(Ordering::SeqCst) {
                            std::thread::sleep(Duration::from_millis(200));
                        }
                        let _ = EvtClose(h);
                        tracing::info!("[ProcessMon] EvtSubscribe 已关闭 (4688)");
                    }
                    Err(e) => {
                        tracing::error!("[ProcessMon] EvtSubscribe 失败: {:?}", e);
                    }
                }
            }
        });

        self.active = true;
        tracing::info!("[ProcessMon] 采集器已启动");
        Ok(())
    }

    pub fn stop(&mut self) -> anyhow::Result<()> {
        if !self.active {
            return Ok(());
        }
        self.running.store(false, Ordering::SeqCst);
        self.active = false;
        // 释放策略引用（refcount 1→0 时才执行 auditpol disable）
        self.policies.release(Policy::ProcessCreation);
        tracing::info!("[ProcessMon] 采集器已停止");
        Ok(())
    }
}

unsafe extern "system" fn process_callback(
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
    if !xml.contains("EventID>4688") {
        return 0;
    }

    let pid = extract_pid(&xml, "NewProcessId");
    let parent_pid = extract_pid(&xml, "ProcessId");
    let image_path = extract_xml(&xml, "NewProcessName");
    let command_line = extract_xml(&xml, "CommandLine");
    if pid == 0 || image_path.is_empty() {
        return 0;
    }

    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
    if let Some(tx) = PROCESS_TX.lock().unwrap().as_ref() {
        let _ = tx.blocking_send(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::ProcessCreate(pb::ProcessCreateEvent {
                pid,
                parent_pid,
                command_line,
                image_path,
                hash_sha256: String::new(),
                timestamp_ns: now,
                user: String::new(),
                session_id: 0,
                is_elevated: false,
            })),
        });
    }
    0 // 继续订阅
}
