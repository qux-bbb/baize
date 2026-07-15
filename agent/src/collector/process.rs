// 进程监控采集器 — 基于 sysinfo 轮询
use anyhow::Result;
use std::collections::HashSet;
use std::sync::Arc;
use std::time::Duration;
use sysinfo::{Pid, ProcessesToUpdate, System};
use tokio::sync::mpsc;
use tokio::time;

use crate::pb;

/// Pid → u64 转换（sysinfo 0.33 Pid 是 newtype，通过 From<Pid> for usize 转换）
fn p2u64(pid: Pid) -> u64 {
    let v: usize = pid.into();
    v as u64
}

pub async fn start(
    system: Arc<tokio::sync::Mutex<System>>,
    tx: mpsc::Sender<pb::Event>,
    interval: Duration,
) -> Result<()> {
    let mut known_pids: HashSet<Pid> = HashSet::new();

    loop {
        {
            let mut sys = system.lock().await;
            sys.refresh_processes(ProcessesToUpdate::All, true);
        }

        let current_pids: HashSet<Pid> = {
            let sys = system.lock().await;
            sys.processes().keys().cloned().collect()
        };

        let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;

        // 新进程
        for pid in current_pids.difference(&known_pids) {
            let sys = system.lock().await;
            if let Some(proc) = sys.process(*pid) {
                let event = pb::Event {
                    agent_info: None,
                    sequence_id: 0,
                    event_type: Some(pb::event::EventType::ProcessCreate(
                        pb::ProcessCreateEvent {
                            pid: p2u64(proc.pid()),
                            parent_pid: proc.parent().map(p2u64).unwrap_or(0),
                            command_line: proc.cmd().iter().map(|s| s.to_string_lossy()).collect::<Vec<_>>().join(" "),
                            image_path: proc
                                .exe()
                                .map(|p| p.to_string_lossy().to_string())
                                .unwrap_or_default(),
                            hash_sha256: String::new(),
                            timestamp_ns: now,
                            user: String::new(),
                            session_id: 0,
                            is_elevated: false,
                        },
                    )),
                };
                let _ = tx.send(event).await;
            }
        }

        // 终止进程
        for pid in known_pids.difference(&current_pids) {
            let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
            let event = pb::Event {
                agent_info: None,
                sequence_id: 0,
                event_type: Some(pb::event::EventType::ProcessTerminate(
                    pb::ProcessTerminateEvent {
                        pid: p2u64(*pid),
                        timestamp_ns: now,
                        exit_code: 0,
                    },
                )),
            };
            let _ = tx.send(event).await;
        }

        known_pids = current_pids;
        time::sleep(interval).await;
    }
}
