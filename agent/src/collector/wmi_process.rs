// ETW 实时进程监控 — Microsoft-Windows-Kernel-Process Provider
use anyhow::{Context, Result};
use std::mem::size_of;
use std::sync::OnceLock;
use tokio::sync::mpsc;
use tracing::info;
use windows::core::GUID;
use windows::Win32::Foundation::*;
use windows::Win32::System::Diagnostics::Etw::*;

use crate::pb;

const KERNEL_PROVIDER: GUID = GUID::from_u128(0x22FB2CD60E7B422BA0C72FAD1FD0E716);
static ETW_TX: OnceLock<mpsc::Sender<pb::Event>> = OnceLock::new();

unsafe extern "system" fn event_callback(rec: *mut EVENT_RECORD) {
    if rec.is_null() { return; }
    let r = &*rec;
    if r.EventHeader.ProviderId != KERNEL_PROVIDER { return; }
    if r.EventHeader.EventDescriptor.Id != 1 { return; } // ProcessStart

    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
    if let Some(tx) = ETW_TX.get() {
        let _ = tx.blocking_send(pb::Event {
            agent_info: None, sequence_id: 0,
            event_type: Some(pb::event::EventType::ProcessCreate(pb::ProcessCreateEvent {
                pid: r.EventHeader.ProcessId as u64, parent_pid: 0,
                command_line: String::new(), image_path: String::new(),
                hash_sha256: String::new(), timestamp_ns: now,
                user: String::new(), session_id: 0, is_elevated: false,
            })),
        });
    }
}

pub fn start_etw(tx: mpsc::Sender<pb::Event>) -> Result<()> {
    ETW_TX.set(tx).map_err(|_| anyhow::anyhow!("ETW 已初始化"))?;
    info!("[ETW] 启动内核进程跟踪...");

    unsafe {
        let name = windows::core::w!("NT Kernel Logger");
        let name_len = (std::mem::size_of_val(&[0u16; 18]) + 256) as u32; // ~34 + 256 padding

        let total = size_of::<EVENT_TRACE_PROPERTIES>() + name_len as usize;
        let mut buf = vec![0u8; total];
        let props = buf.as_mut_ptr() as *mut EVENT_TRACE_PROPERTIES;

        let mut p = EVENT_TRACE_PROPERTIES::default();
        p.Wnode.BufferSize = total as u32;
        p.Wnode.Flags = WNODE_FLAG_ALL_DATA;
        p.BufferSize = 256;
        p.MinimumBuffers = 1;
        p.MaximumBuffers = 4;
        p.LogFileMode = EVENT_TRACE_REAL_TIME_MODE;
        p.FlushTimer = 1;
        p.EnableFlags = EVENT_TRACE_FLAG_PROCESS;
        p.Anonymous = EVENT_TRACE_PROPERTIES_0 { AgeLimit: 0 };
        p.LoggerNameOffset = size_of::<EVENT_TRACE_PROPERTIES>() as u32;
        props.write(p);

        let mut handle = CONTROLTRACE_HANDLE::default();
        let status = StartTraceW(&mut handle, name, props);

        if status != ERROR_SUCCESS && status != ERROR_ALREADY_EXISTS {
            anyhow::bail!("StartTraceW 失败 (需要管理员权限): code={}", status.0);
        }

        if status == ERROR_SUCCESS {
            info!("[ETW] 跟踪会话已启动");
        } else {
            info!("[ETW] 内核跟踪会话已存在，尝试附加...");
        }

        // 打开跟踪
        let mut logfile: EVENT_TRACE_LOGFILEW = Default::default();
        logfile.LoggerName = windows::core::PWSTR(name.as_ptr() as *mut u16);
        logfile.Anonymous1.ProcessTraceMode = PROCESS_TRACE_MODE_REAL_TIME | PROCESS_TRACE_MODE_EVENT_RECORD;
        logfile.Anonymous2.EventRecordCallback = Some(event_callback);
        logfile.IsKernelTrace = 1;

        let trace = OpenTraceW(&mut logfile);
        if trace.Value == u64::MAX {
            anyhow::bail!("OpenTraceW 失败");
        }

        info!("[ETW] 开始处理事件...");
        let result = ProcessTrace(&[trace], None, None);
        info!("[ETW] ProcessTrace 结束: {:?}", result);
    }

    Ok(())
}
