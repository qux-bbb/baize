// 文件监控采集器 — 基于 notify crate
// 监控敏感目录的文件创建/修改/删除事件
use anyhow::{Context, Result};
use notify::{Config, Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher};
use std::path::Path;
use std::sync::mpsc;
use tokio::sync::mpsc as tmpsc;

use crate::pb;

/// 启动文件监控
/// `paths`: 要监控的目录列表
/// `tx`: 事件发送通道
pub async fn start(
    paths: Vec<String>,
    tx: tmpsc::Sender<pb::Event>,
) -> Result<()> {
    let (raw_tx, raw_rx) = mpsc::channel::<notify::Result<Event>>();

    // 创建 watcher
    let mut watcher = RecommendedWatcher::new(
        move |res| {
            let _ = raw_tx.send(res);
        },
        Config::default(),
    )
    .context("创建文件监控器失败")?;

    // 添加监控目录
    for path in &paths {
        watcher
            .watch(Path::new(path), RecursiveMode::Recursive)
            .with_context(|| format!("添加监控目录失败: {}", path))?;
        tracing::info!("[FileMon] 监控: {}", path);
    }

    // 将 notify 事件转发到 tokio 通道
    let tx_clone = tx.clone();
    tokio::task::spawn_blocking(move || {
        loop {
            match raw_rx.recv() {
                Ok(Ok(event)) => {
                    if let Some(event_pb) = convert_event(event) {
                        let _ = tx_clone.blocking_send(event_pb);
                    }
                }
                Ok(Err(e)) => {
                    tracing::error!("[FileMon] 通知错误: {:?}", e);
                }
                Err(_) => break,
            }
        }
    });

    // 保持 watcher 存活
    // 使用 std::mem::forget 让 watcher 一直运行
    // 或者返回 Ok，让 watcher 被 drop 但 spawn_blocking 中继续处理
    // 实际上 watcher 在 drop 时会停止通知，所以需要让调用者持有它
    // 这里我们使用一个 trick：把 watcher 泄漏掉
    std::mem::forget(watcher);

    // 阻塞以防止此任务退出
    futures::future::pending::<()>().await;
    Ok(())
}

/// 将 notify::Event 转为 protobuf Event
fn convert_event(event: Event) -> Option<pb::Event> {
    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;

    // 取第一个路径（notify 可能有多个关联路径）
    let file_path = event.paths.first()?.to_string_lossy().to_string();
    let file_size = std::fs::metadata(&file_path).ok().map(|m| m.len()).unwrap_or(0);

    match event.kind {
        EventKind::Create(_) => Some(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::FileCreate(pb::FileCreateEvent {
                file_path,
                file_size,
                hash_sha256: String::new(),
                pid: 0,
                process_name: String::new(),
                timestamp_ns: now,
            })),
        }),

        EventKind::Modify(_) => Some(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::FileModify(pb::FileModifyEvent {
                file_path,
                file_size_before: 0,
                file_size_after: file_size,
                hash_sha256_before: String::new(),
                hash_sha256_after: String::new(),
                pid: 0,
                process_name: String::new(),
                timestamp_ns: now,
            })),
        }),

        EventKind::Remove(_) => Some(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::FileDelete(pb::FileDeleteEvent {
                file_path,
                original_size: file_size,
                hash_sha256: String::new(),
                pid: 0,
                process_name: String::new(),
                timestamp_ns: now,
            })),
        }),

        _ => None,
    }
}
