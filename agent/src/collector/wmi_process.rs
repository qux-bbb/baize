// EvtSubscribe 实时进程监控 — Subscribe Security 日志 4688 事件
// ──────────────────────────────────────────────────────────────────
// 使用 Windows EvtSubscribe API 实时订阅 Security 通道的进程创建事件
// (EventID 4688)，纯 API 调用，无需外部进程，管理员权限下实时推送。
//
// 未来 ETW 研究参考:
// ──────────────────────────────────────────────────────────────────
// ETW 能收更多事件类型 (DLL加载、注册表、线程等)，但需要 SYSTEM 权限
// 和 EnableTraceEx2 API。当前 EvtSubscribe 只收 4688 已经够用。
// 如果以后要扩展 ETW，请参考:
//   - Provider: Microsoft-Windows-Kernel-Process (22FB2CD6-...)
//   - 用 EnableTraceEx2 在私有会话上启用提供者
//   - 需要 SE_SYSTEM_PROFILE_NAME 特权

use anyhow::{Context, Result};
use std::ffi::OsString;
use std::os::windows::ffi::OsStringExt;
use std::sync::Mutex;
use std::time::Duration;
use tokio::sync::mpsc;
use tracing::{error, info};
use windows::core::PCWSTR;
use windows::Win32::Foundation::*;
use windows::Win32::System::EventLog::*;

use crate::pb;

static EVTSUB_TX: Mutex<Option<mpsc::Sender<pb::Event>>> = Mutex::new(None);

/// 从 XML 片段中提取标签内的文本
fn extract_xml(xml: &str, _tag: &str, attr: Option<&str>) -> String {
    let open = if let Some(attr_val) = attr {
        // 兼容单引号和双引号
        format!("<Data Name='{}'>", attr_val)
    } else {
        format!("<{}>", _tag)
    };
    // 也尝试双引号版本
    let open2 = if let Some(attr_val) = attr {
        format!("<Data Name=\"{}\">", attr_val)
    } else {
        String::new()
    };
    let close = if attr.is_some() {
        "</Data>"
    } else {
        "</"
    };

    for open_str in [open, open2].iter().filter(|s| !s.is_empty()) {
        if let Some(start) = xml.find(open_str.as_str()) {
            let content_start = start + open_str.len();
            if let Some(end) = xml[content_start..].find(close) {
                return xml[content_start..content_start + end].to_string();
            }
        }
    }
    String::new()
}

/// 从 XML 中提取十六进制 PID 并转为十进制
fn extract_pid(xml: &str, attr: &str) -> u64 {
    let hex = extract_xml(xml, "Data", Some(attr));
    if hex.starts_with("0x") || hex.starts_with("0X") {
        u64::from_str_radix(&hex[2..], 16).unwrap_or(0)
    } else {
        hex.parse::<u64>().unwrap_or(0)
    }
}

unsafe extern "system" fn subscribe_callback(
    action: EVT_SUBSCRIBE_NOTIFY_ACTION,
    _usercontext: *const core::ffi::c_void,
    event: EVT_HANDLE,
) -> u32 {
    static CALLBACK_COUNT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let n = CALLBACK_COUNT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    tracing::info!("[EventLog] 回调触发 #{}: action={:?}", n, action);

    if action != EvtSubscribeActionDeliver {
        return 0;
    }

    // 第一次渲染：获取缓冲区大小
    let mut buf_used = 0u32;
    let r1 = EvtRender(
        EVT_HANDLE::default(),
        event,
        EvtRenderEventXml.0,
        0,
        None::<*mut core::ffi::c_void>,
        &mut buf_used,
        std::ptr::null_mut(),
    );
    if buf_used == 0 {
        tracing::warn!("[EventLog] EvtRender 返回空: {:?}", r1);
        return 0;
    }
    tracing::info!("[EventLog] EvtRender 大小: {} bytes", buf_used);


    // 第二次渲染：获取 XML
    let mut xml_buf = vec![0u16; buf_used as usize];
    let mut buf_used2 = 0u32;
    let render_result = EvtRender(
        EVT_HANDLE::default(),
        event,
        EvtRenderEventXml.0,
        (buf_used * 2) as u32,
        Some(xml_buf.as_mut_ptr() as *mut core::ffi::c_void),
        &mut buf_used2,
        std::ptr::null_mut(),
    );
    if render_result.is_err() {
        tracing::warn!("[EventLog] 二次渲染失败: {:?}", render_result);
        return 0;
    }

    let xml = OsString::from_wide(&xml_buf).to_string_lossy().to_string();
    let xml_preview: String = xml.chars().take(200).collect();
    tracing::info!("[EventLog] XML 预览: {}", xml_preview);

    if !xml.contains("EventID>4688") {
        let has_eventid = xml.contains("EventID");
        if !xml.contains("EventID>4688") && !xml.contains("EventID>5156") {
            return 0;
        }

        if xml.contains("EventID>4688") {
            // 进程创建事件
            let pid = extract_pid(&xml, "NewProcessId");
            let parent_pid = extract_pid(&xml, "ProcessId");
            let image_path = extract_xml(&xml, "Data", Some("NewProcessName"));
            let command_line = extract_xml(&xml, "Data", Some("CommandLine"));

            if pid == 0 || image_path.is_empty() {
                return 0;
            }

            let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
            if let Some(tx) = EVTSUB_TX.lock().unwrap().as_ref() {
                let _ = tx.blocking_send(pb::Event {
                    agent_info: None, sequence_id: 0,
                    event_type: Some(pb::event::EventType::ProcessCreate(pb::ProcessCreateEvent {
                        pid, parent_pid, command_line, image_path,
                        hash_sha256: String::new(), timestamp_ns: now,
                        user: String::new(), session_id: 0, is_elevated: false,
                    })),
                });
            }
        } else if xml.contains("EventID>5156") {
            // 网络连接事件
            let pid = extract_pid(&xml, "ProcessID");
            let process_name = extract_xml(&xml, "Data", Some("Application"));
            let local_ip = extract_xml(&xml, "Data", Some("SourceAddress"));
            let local_port_str = extract_xml(&xml, "Data", Some("SourcePort"));
            let remote_ip = extract_xml(&xml, "Data", Some("DestAddress"));
            let remote_port_str = extract_xml(&xml, "Data", Some("DestPort"));
            let protocol_str = extract_xml(&xml, "Data", Some("Protocol"));
            let direction_str = extract_xml(&xml, "Data", Some("Direction"));

            let local_port = local_port_str.parse::<u32>().unwrap_or(0);
            let remote_port = remote_port_str.parse::<u32>().unwrap_or(0);
            let protocol = if protocol_str == "6" { "tcp" } else if protocol_str == "17" { "udp" } else { "other" };
            let direction = if direction_str.contains("14593") { "outbound" } else { "inbound" };

            let now = chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64;
            if let Some(tx) = EVTSUB_TX.lock().unwrap().as_ref() {
                let _ = tx.blocking_send(pb::Event {
                    agent_info: None, sequence_id: 0,
                    event_type: Some(pb::event::EventType::NetworkConnection(pb::NetworkConnectionEvent {
                        pid, process_name, local_ip, local_port, remote_ip, remote_port,
                        protocol: protocol.to_string(), direction: direction.to_string(), timestamp_ns: now,
                    })),
                });
            }
        }
    }
    0 // 继续订阅
}

/// 自动启用进程创建 (4688) 和网络连接 (5156) 审计策略
fn enable_audit_policies() {
    // 启用审计策略
    let process_guid = "{0CCE922B-69AE-11D9-BED3-505054503030}";
    let network_guid = "{0CCE9226-69AE-11D9-BED3-505054503030}";

    for guid in [process_guid, network_guid] {
        let output = std::process::Command::new("auditpol")
            .args(["/set", &format!("/subcategory:{}", guid), "/success:enable"])
            .output();
        match output {
            Ok(out) if out.status.success() => {
                tracing::info!("[审计] 已启用: {}", guid);
            }
            Ok(out) => {
                let stderr = String::from_utf8_lossy(&out.stderr);
                tracing::warn!("[审计] 启用失败 {}: {}", guid, stderr.trim());
            }
            Err(e) => {
                tracing::warn!("[审计] auditpol 执行失败: {}", e);
            }
        }
    }

    // 启用 DNS Client 日志
    let dns_channel = "Microsoft-Windows-DNS-Client/Operational";
    if let Ok(out) = std::process::Command::new("wevtutil")
        .args(["sl", dns_channel, "/e:true"])
        .output()
    {
        if out.status.success() {
            tracing::info!("[DNS] 日志通道已启用");
        } else {
            let stderr = String::from_utf8_lossy(&out.stderr);
            tracing::info!("[DNS] 日志通道可能已启用: {}", stderr.trim());
        }
    }
}

/// 订阅 DNS Client 日志（EventID 3008）
fn start_dns_subscription(tx: mpsc::Sender<pb::Event>) -> Result<()> {
    info!("[DNS] 订阅 Microsoft-Windows-DNS-Client/Operational...");

    unsafe {
        let channel = windows::core::w!("Microsoft-Windows-DNS-Client/Operational");
        let query = windows::core::w!("*[System[(EventID=3008)]]");

        let handle = EvtSubscribe(
            EVT_HANDLE::default(),
            HANDLE::default(),
            PCWSTR::from_raw(channel.as_ptr()),
            PCWSTR::from_raw(query.as_ptr()),
            EVT_HANDLE::default(),
            None::<*const core::ffi::c_void>,
            Some(dns_callback),
            EvtSubscribeToFutureEvents.0,
        );

        match handle {
            Ok(h) => {
                info!("[DNS] EvtSubscribe 成功");
                loop { std::thread::sleep(Duration::from_secs(3600)); }
            }
            Err(e) => {
                anyhow::bail!("DNS EvtSubscribe 失败: {:?}", e);
            }
        }
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

    // 渲染 XML
    let mut buf_used = 0u32;
    let _ = EvtRender(EVT_HANDLE::default(), event, EvtRenderEventXml.0, 0,
        None::<*mut core::ffi::c_void>, &mut buf_used, std::ptr::null_mut());
    if buf_used == 0 { return 0; }

    let mut xml_buf = vec![0u16; buf_used as usize];
    let mut buf_used2 = 0u32;
    if EvtRender(EVT_HANDLE::default(), event, EvtRenderEventXml.0,
        (buf_used * 2) as u32, Some(xml_buf.as_mut_ptr() as *mut core::ffi::c_void),
        &mut buf_used2, std::ptr::null_mut()).is_err() { return 0; }

    let xml = OsString::from_wide(&xml_buf).to_string_lossy().to_string();
    if !xml.contains("EventID>3008") { return 0; }

    // DNS 事件的 PID 在 <Execution ProcessID='...'/> 属性中
    let mut pid: u64 = 0;
    if let Some(start) = xml.find("ProcessID='") {
        let s = start + "ProcessID='".len();
        if let Some(end) = xml[s..].find("'") {
            pid = xml[s..s+end].parse::<u32>().unwrap_or(0) as u64;
        }
    }
    let query_name = extract_xml(&xml, "Data", Some("QueryName"));
    let query_type_str = extract_xml(&xml, "Data", Some("QueryType"));
    let result_ips = extract_xml(&xml, "Data", Some("QueryResults"));
    // QueryResults 格式: 以 ; 分隔，每个条目为:
    //   ::ffff:x.x.x.x  → A 记录 (IPv4-mapped IPv6)
    //   type: 5 domain  → CNAME
    //   type: 16 text   → 诊断信息 (跳过)
    //   裸 IPv4/IPv6     → A/AAAA 记录
    let ips: Vec<String> = result_ips.split(';')
        .filter_map(|s| {
            let s = s.trim();
            if s.is_empty() { return None; }

            if let Some(rest) = s.strip_prefix("type: ") {
                // type: N value 格式
                let rest = rest.trim();
                let type_end = rest.find(char::is_whitespace).unwrap_or(rest.len());
                let rec_type: u16 = rest[..type_end].parse().ok()?;
                let value = rest[type_end..].trim();

                match rec_type {
                    5 => Some(format!("CNAME {}", value)), // CNAME 保留参考
                    16 => None,                             // 诊断信息跳过
                    _ => None,
                }
            } else {
                // 裸 IP 地址
                if let Some(ipv4) = s.strip_prefix("::ffff:") {
                    Some(ipv4.to_string()) // ::ffff:x.x.x.x → x.x.x.x
                } else if s.contains(':') || s.contains('.') {
                    Some(s.to_string())    // 裸 IPv4 或 IPv6
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
    if let Some(tx) = EVTSUB_TX.lock().unwrap().as_ref() {
        let _ = tx.blocking_send(pb::Event {
            agent_info: None, sequence_id: 0,
            event_type: Some(pb::event::EventType::DnsQuery(pb::DnsQueryEvent {
                pid, process_name: String::new(), query_name,
                query_type: qtype.to_string(), result_ips, timestamp_ns: now,
            })),
        });
    }
    0
}

pub fn start_evtsub(tx: mpsc::Sender<pb::Event>) -> Result<()> {
    // DNS 订阅需要独立的 tx（在 tx 被 EVTSUB_TX 消费前 clone）
    let dns_tx = tx.clone();

    *EVTSUB_TX.lock().unwrap() = Some(tx);

    // 自动启用 Windows 审计策略（进程创建 4688 + 网络连接 5156）
    enable_audit_policies();

    info!("[EventLog] EvtSubscribe 启动: Security 通道 4688...");

    // 启动 DNS 订阅（独立线程）
    std::thread::spawn(move || {
        if let Err(e) = start_dns_subscription(dns_tx) {
            tracing::warn!("[DNS] 订阅失败 (日志可能未启用): {:?}", e);
        }
    });

    unsafe {
        let channel = windows::core::w!("Security");
        let query = windows::core::w!("*[System[(EventID=4688 or EventID=5156)]]");

        let handle = EvtSubscribe(
            EVT_HANDLE::default(),  // null session (local)
            HANDLE::default(),      // no signal event (blocking callback)
            PCWSTR::from_raw(channel.as_ptr()),
            PCWSTR::from_raw(query.as_ptr()),
            EVT_HANDLE::default(),  // no bookmark
            None::<*const core::ffi::c_void>,
            Some(subscribe_callback),
            EvtSubscribeToFutureEvents.0,
        );

        match handle {
            Ok(h) => {
                info!("[EventLog] EvtSubscribe 成功");
                // 阻塞，让回调处理事件
                loop {
                    std::thread::sleep(Duration::from_secs(3600));
                    // 检查连接：每5分钟ping一次回调是否活跃
                }
            }
            Err(e) => {
                anyhow::bail!("EvtSubscribe 失败 (需要管理员权限): {:?}", e);
            }
        }
    }
}
