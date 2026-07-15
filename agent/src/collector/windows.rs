// Windows EventLog 实时采集 — EventID 4688 (进程创建) + 5156 (网络连接)
// 使用 wevtutil 命令行工具轮询最新事件，近实时读取
use anyhow::{Context, Result};
use std::process::Command;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio::time;

use crate::pb;

/// 启动 Windows 事件日志监控
pub async fn start(tx: mpsc::Sender<pb::Event>) -> Result<()> {
    // 尝试启用所需 Audit 策略（若未启用则静默跳过）
    let _ = Command::new("auditpol")
        .args(["/set", "/subcategory:\"Process Creation\"", "/success:enable"])
        .output();
    let _ = Command::new("auditpol")
        .args(["/set", "/subcategory:\"Filtering Platform Connection\"", "/success:enable"])
        .output();

    // 记录上次读到的事件时间戳
    let mut last_seen = String::new();

    loop {
        // 查询最新的 1 条 4688 或 5156 事件，按时间倒序 + 前向滚动
        let query = format!(
            "wevtutil qe Security /q:\"*[System[(EventID=4688 or EventID=5156)]]\" /f:xml /c:1 /rd:true /e:{}",
            if last_seen.is_empty() { "false" } else { "true" }
        );

        let output = Command::new("wevtutil")
            .args([
                "qe", "Security",
                "/q", &format!("*[System[(EventID=4688 or EventID=5156)]]"),
                "/f:xml", "/c:1", "/rd:true", "/e:true",
            ])
            .output()
            .context("wevtutil 执行失败")?;

        let stdout = String::from_utf8_lossy(&output.stdout);

        if !stdout.trim().is_empty() && stdout.contains("<Event") {
            // 解析事件
            if let Some(event_pb) = parse_event(&stdout) {
                let _ = tx.send(event_pb).await;
            }
        }

        // 每 500ms 轮询一次（近实时）
        time::sleep(Duration::from_millis(500)).await;
    }
}

/// 从 wevtutil XML 输出中解析事件
fn parse_event(xml: &str) -> Option<pb::Event> {
    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;

    // 提取 EventID
    let event_id = extract_tag(xml, "EventID")?;
    let event_id: u32 = event_id.parse().ok()?;

    match event_id {
        4688 => parse_process_create(xml, now),
        5156 => parse_network_connect(xml, now),
        _ => None,
    }
}

/// 解析 4688 进程创建事件
fn parse_process_create(xml: &str, now: u64) -> Option<pb::Event> {
    let pid_str = extract_tag(xml, "NewProcessId")?;
    // PID 格式如 "0x1abc"
    let pid = u64::from_str_radix(pid_str.trim_start_matches("0x"), 16).ok()?;
    let ppid_str = extract_data(xml, "ParentProcessID")?;
    let ppid: u64 = ppid_str.parse().ok()?;
    let cmdline = extract_data(xml, "CommandLine").unwrap_or_default();
    let image = extract_data(xml, "NewProcessName").unwrap_or_default();
    let user = extract_data(xml, "SubjectUserName").unwrap_or_default();

    Some(pb::Event {
        agent_info: None,
        sequence_id: 0,
        event_type: Some(pb::event::EventType::ProcessCreate(pb::ProcessCreateEvent {
            pid,
            parent_pid: ppid,
            command_line: cmdline,
            image_path: image,
            hash_sha256: String::new(),
            timestamp_ns: now,
            user,
            session_id: 0,
            is_elevated: false,
        })),
    })
}

/// 解析 5156 网络连接事件
fn parse_network_connect(xml: &str, now: u64) -> Option<pb::Event> {
    let pid = extract_tag(xml, "ProcessId")?;
    let pid: u64 = pid.parse().ok()?;

    let direction = extract_tag(xml, "Direction")?; // 1=入站 2=出站
    let dir = if direction.trim() == "2" { "outbound" } else { "inbound" };

    let src_addr = extract_data(xml, "SourceAddress").unwrap_or_default();
    let src_port = extract_data(xml, "SourcePort").unwrap_or_default();
    let dst_addr = extract_data(xml, "DestAddress").unwrap_or_default();
    let dst_port = extract_data(xml, "DestPort").unwrap_or_default();
    let protocol_str = extract_data(xml, "Protocol").unwrap_or_default(); // 6=TCP 17=UDP
    let protocol = if protocol_str.trim() == "17" { "udp" } else { "tcp" };

    let process_name = extract_data(xml, "Application").unwrap_or_default();

    Some(pb::Event {
        agent_info: None,
        sequence_id: 0,
        event_type: Some(pb::event::EventType::NetworkConnection(pb::NetworkConnectionEvent {
            pid,
            process_name,
            local_ip: if dir == "outbound" { src_addr.clone() } else { dst_addr.clone() },
            local_port: if dir == "outbound" { src_port.parse().unwrap_or(0) } else { dst_port.parse().unwrap_or(0) },
            remote_ip: if dir == "outbound" { dst_addr } else { src_addr },
            remote_port: if dir == "outbound" { dst_port.parse().unwrap_or(0) } else { src_port.parse().unwrap_or(0) },
            protocol: protocol.to_string(),
            direction: dir.to_string(),
            timestamp_ns: now,
        })),
    })
}

/// 提取 XML 标签之间的文本（不含嵌套）
fn extract_tag<'a>(xml: &'a str, tag: &str) -> Option<String> {
    let open = format!("<{}>", tag);
    let close = format!("</{}>", tag);
    let start = xml.find(&open)?;
    let end = xml[start..].find(&close)?;
    let value = xml[start + open.len()..][..end].trim();
    if value.is_empty() { None } else { Some(value.to_string()) }
}

/// 提取 EventData 中的 Data 项
fn extract_data(xml: &str, name: &str) -> Option<String> {
    let marker = format!("Data Name=\"{}\"", name);
    let start = xml.find(&marker)?;
    let after = &xml[start + marker.len()..];
    let val_start = after.find('>')?;
    let val_end = after[val_start + 1..].find('<')?;
    let value = after[val_start + 1..][..val_end].trim();
    if value.is_empty() { None } else { Some(value.to_string()) }
}
