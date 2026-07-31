// 文件监控采集器 — 基于 notify crate
// 监控敏感目录的文件创建/修改/删除事件
// start: 创建 watcher 注册目录；stop: drop watcher 停止通知
use anyhow::{Context, Result};
use notify::{Config, Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher};
use std::path::Path;
use std::sync::mpsc;
use tokio::sync::mpsc as tmpsc;

use crate::pb;

pub struct FileCollector {
    watcher: Option<RecommendedWatcher>,
    worker: Option<std::thread::JoinHandle<()>>,
    dirs: Vec<String>,
}

impl FileCollector {
    pub fn new() -> Self {
        Self {
            watcher: None,
            worker: None,
            dirs: Vec::new(),
        }
    }

    /// 记录监控目录（不启动），供事件类型开关开启时使用
    pub fn set_dirs(&mut self, dirs: Vec<String>) {
        self.dirs = dirs;
    }

    pub fn dirs(&self) -> &[String] {
        &self.dirs
    }

    /// 启动文件监控
    pub fn start(&mut self, paths: Vec<String>, tx: tmpsc::Sender<pb::Event>) -> Result<()> {
        if self.watcher.is_some() {
            return Ok(()); // 已在运行
        }

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

        // 转发线程：notify 事件 → tokio 通道
        // watcher 被 drop 时 raw_tx 关闭，recv 返回 Err，线程退出
        let tx_clone = tx.clone();
        let worker = std::thread::spawn(move || {
            while let Ok(Ok(event)) = raw_rx.recv() {
                if let Some(event_pb) = convert_event(event) {
                    let _ = tx_clone.blocking_send(event_pb);
                }
            }
            tracing::info!("[FileMon] 转发线程退出");
        });

        self.watcher = Some(watcher);
        self.worker = Some(worker);
        tracing::info!("[FileMon] 采集器已启动 ({} 个目录)", paths.len());
        Ok(())
    }

    /// 停止文件监控
    pub fn stop(&mut self) {
        if self.watcher.is_none() {
            return;
        }
        // drop watcher → raw_tx 关闭 → 转发线程 recv Err 退出
        self.watcher.take();
        if let Some(w) = self.worker.take() {
            let _ = w.join();
        }
        tracing::info!("[FileMon] 采集器已停止");
    }
}

impl Default for FileCollector {
    fn default() -> Self {
        Self::new()
    }
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
