// Windows EventLog 实时采集 — EventID 4688 (进程创建) + 5156 (网络连接)
// 优先 wevtutil，失败时 fallback 到 PowerShell Get-WinEvent
use anyhow::Result;
use std::process::Command;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio::time;

use crate::pb;

pub async fn start(tx: mpsc::Sender<pb::Event>) -> Result<()> {
    // 尝试启用 Audit 策略（静默跳过失败）
    let _ = Command::new("auditpol")
        .args(["/set", "/subcategory:\"Process Creation\"", "/success:enable"])
        .output();
    let _ = Command::new("auditpol")
        .args(["/set", "/subcategory:\"Filtering Platform Connection\"", "/success:enable"])
        .output();

    loop {
        // 尝试 wevtutil → PowerShell → 都失败则等5秒重试
        let stdout = match read_event() {
            Some(s) => s,
            None => {
                time::sleep(Duration::from_secs(5)).await;
                continue;
            }
        };

        if let Some(event_pb) = parse_event(&stdout) {
            let _ = tx.send(event_pb).await;
        }

        time::sleep(Duration::from_millis(500)).await;
    }
}

/// 读取最新一条 4688 或 5156 事件
fn read_event() -> Option<String> {
    // 1. wevtutil
    let out = Command::new("wevtutil")
        .args(["qe", "Security",
            "/q", "*[System[(EventID=4688 or EventID=5156)]]",
            "/f:xml", "/c:1", "/rd:true", "/e:true"])
        .output().ok()?;
    if out.status.success() {
        let s = String::from_utf8_lossy(&out.stdout).to_string();
        if s.contains("<Event") { return Some(s); }
    }

    // 2. PowerShell fallback
    let ps = Command::new("powershell")
        .args(["-Command",
            "Get-WinEvent -FilterHashtable @{LogName='Security'; Id=4688,5156} -MaxEvents 1 | ForEach-Object { $_.ToXml() }"])
        .output().ok()?;
    if ps.status.success() {
        let s = String::from_utf8_lossy(&ps.stdout).to_string();
        if s.contains("<Event") { return Some(s); }
    }

    tracing::warn!("[EventLog] 读取 Security 日志失败（可能需要管理员权限）");
    None
}

// ── 解析 ────────────────────────────────────────────

fn parse_event(xml: &str) -> Option<pb::Event> {
    let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
    let event_id = extract_tag(xml, "EventID")?.parse::<u32>().ok()?;
    match event_id {
        4688 => parse_process_create(xml, now),
        5156 => parse_network_connect(xml, now),
        _ => None,
    }
}

fn parse_process_create(xml: &str, now: u64) -> Option<pb::Event> {
    let pid = u64::from_str_radix(
        extract_tag(xml, "NewProcessId")?.trim_start_matches("0x"), 16).ok()?;
    let ppid = extract_data(xml, "ParentProcessID")?.parse().ok()?;
    let cmdline = extract_data(xml, "CommandLine").unwrap_or_default();
    let image = extract_data(xml, "NewProcessName").unwrap_or_default();
    let user = extract_data(xml, "SubjectUserName").unwrap_or_default();

    Some(pb::Event {
        agent_info: None, sequence_id: 0,
        event_type: Some(pb::event::EventType::ProcessCreate(pb::ProcessCreateEvent {
            pid, parent_pid: ppid, command_line: cmdline, image_path: image,
            hash_sha256: String::new(), timestamp_ns: now, user,
            session_id: 0, is_elevated: false,
        })),
    })
}

fn parse_network_connect(xml: &str, now: u64) -> Option<pb::Event> {
    let pid: u64 = extract_tag(xml, "ProcessId")?.parse().ok()?;
    let dir = if extract_tag(xml, "Direction")?.trim() == "2" { "outbound" } else { "inbound" };
    let src_addr = extract_data(xml, "SourceAddress").unwrap_or_default();
    let src_port = extract_data(xml, "SourcePort").unwrap_or_default();
    let dst_addr = extract_data(xml, "DestAddress").unwrap_or_default();
    let dst_port = extract_data(xml, "DestPort").unwrap_or_default();
    let proto = if extract_data(xml, "Protocol").unwrap_or_default().trim() == "17" { "udp" } else { "tcp" };
    let process_name = extract_data(xml, "Application").unwrap_or_default();

    Some(pb::Event {
        agent_info: None, sequence_id: 0,
        event_type: Some(pb::event::EventType::NetworkConnection(pb::NetworkConnectionEvent {
            pid, process_name,
            local_ip: if dir == "outbound" { src_addr.clone() } else { dst_addr.clone() },
            local_port: if dir == "outbound" { src_port.parse().unwrap_or(0) } else { dst_port.parse().unwrap_or(0) },
            remote_ip: if dir == "outbound" { dst_addr } else { src_addr },
            remote_port: if dir == "outbound" { dst_port.parse().unwrap_or(0) } else { src_port.parse().unwrap_or(0) },
            protocol: proto.to_string(), direction: dir.to_string(), timestamp_ns: now,
        })),
    })
}

fn extract_tag(xml: &str, tag: &str) -> Option<String> {
    let o = format!("<{}>", tag);
    let c = format!("</{}>", tag);
    let s = xml.find(&o)?;
    let e = xml[s..].find(&c)?;
    let v = xml[s + o.len()..][..e].trim();
    if v.is_empty() { None } else { Some(v.to_string()) }
}

fn extract_data(xml: &str, name: &str) -> Option<String> {
    let m = format!("Data Name=\"{}\"", name);
    let s = xml.find(&m)?;
    let a = &xml[s + m.len()..];
    let vs = a.find('>')?;
    let ve = a[vs + 1..].find('<')?;
    let v = a[vs + 1..][..ve].trim();
    if v.is_empty() { None } else { Some(v.to_string()) }
}
