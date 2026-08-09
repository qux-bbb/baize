// Baize (白泽) Agent — Rust 版本
mod collector;

#[cfg(windows)]
mod service;

/// 停止标志：Windows 服务模式收到 SCM 停止信号时置位，
/// Agent 主循环据此干净退出。前台模式恒为 false。
pub static STOP_FLAG: AtomicBool = AtomicBool::new(false);

pub fn stop_requested() -> bool {
    STOP_FLAG.load(Ordering::Relaxed)
}

pub mod pb {
    tonic::include_proto!("baize.v1");
}

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use anyhow::{Context, Result};
use clap::Parser;
use std::fs;
use std::io::Write;
use sysinfo::System;
use tokio::sync::mpsc;
use tokio::time;
use tonic::transport::Endpoint;
use tonic::Request;
use tracing::{error, info};

use pb::baize_service_client::BaizeServiceClient;
use pb::{AgentInfo, Event};

#[derive(Parser)]
#[command(name = "baize-agent", about = "Baize Agent")]
struct Cli {
    #[arg(long, help = "Server gRPC 地址，如 http://192.168.1.10:50051；优先于 agent.conf")]
    server: Option<String>,
    #[arg(long)]
    agent_id: Option<String>,
    /// 进程采集间隔（秒）
    #[arg(long, default_value = "30")]
    interval: u64,
    /// 文件监控目录（逗号分隔），优先于 agent.conf
    #[arg(long)]
    watch: Option<String>,
    /// 将 Server 地址写入 exe 同目录 agent.conf 后退出（供 MSI 安装器调用）
    #[arg(long)]
    write_config: Option<String>,
    #[arg(long)]
    hostname: Option<String>,
    /// 安装为 Windows 服务
    #[arg(long)]
    install: bool,
    /// 卸载 Windows 服务
    #[arg(long)]
    uninstall: bool,
    /// 以 Windows 服务模式运行（由 SCM 调用）
    #[arg(long)]
    service: bool,
}

/// 初始化日志：stderr + 文件双写。
/// Windows 写到 %PROGRAMDATA%\Baize\agent.log（服务模式工作目录不可控，不能用相对路径）；
/// 其他平台写到 data/agent.log。
fn init_logging() {
    #[cfg(windows)]
    let log_path = {
        let base = std::env::var("PROGRAMDATA").unwrap_or_else(|_| "C:\\ProgramData".into());
        let dir = std::path::Path::new(&base).join("Baize");
        if let Err(e) = fs::create_dir_all(&dir) {
            eprintln!("创建日志目录失败: {:?}", e);
        }
        dir.join("agent.log")
    };
    #[cfg(not(windows))]
    let log_path = {
        fs::create_dir_all("data").ok();
        std::path::PathBuf::from("data").join("agent.log")
    };

    let Ok(log_file) = fs::OpenOptions::new()
        .create(true).append(true).open(&log_path) else {
        eprintln!("打开日志文件失败: {}", log_path.display());
        // 无文件日志时仍输出到 stderr
        tracing_subscriber::fmt()
            .with_env_filter("baize_agent=info")
            .with_ansi(false)
            .init();
        return;
    };

    tracing_subscriber::fmt()
        .with_env_filter("baize_agent=info")
        .with_ansi(false)
        .with_writer(move || {
            let file = log_file.try_clone().unwrap();
            struct Tee {
                stderr: std::io::Stderr,
                file: fs::File,
            }
            impl Write for Tee {
                fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
                    let n = self.stderr.write(buf)?;
                    self.file.write_all(buf)?;
                    Ok(n)
                }
                fn flush(&mut self) -> std::io::Result<()> {
                    self.stderr.flush()?;
                    self.file.flush()
                }
            }
            Tee { stderr: std::io::stderr(), file }
        })
        .init();
    info!("日志文件: {}", log_path.display());
}

#[tokio::main]
async fn main() -> Result<()> {
    init_logging();

    let cli = Cli::parse();

    // 配置写入子命令（MSI 安装器 custom action 调用）：写 agent.conf 后退出
    if let Some(addr) = cli.write_config {
        return write_config_file(&addr);
    }

    // 服务管理命令（Windows only）
    #[cfg(windows)]
    {
        if cli.install {
            return service::install().map_err(|e| anyhow::anyhow!("{}", e));
        }
        if cli.uninstall {
            return service::uninstall().map_err(|e| anyhow::anyhow!("{}", e));
        }
        if cli.service {
            info!("[Service] 以 Windows 服务模式启动...");
            return service::run_as_service().map_err(|e| anyhow::anyhow!("{}", e));
        }
    }
    run_agent_loop(cli.server, cli.watch, cli.agent_id, cli.interval, cli.hostname).await
}

/// exe 同目录的 agent.conf（JSON）。服务模式和前台模式都从可执行文件所在目录读取，
/// 不依赖工作目录。CLI 参数优先级高于配置文件。
#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct AgentConfig {
    /// Server gRPC 地址，如 http://192.168.1.10:50051
    server: Option<String>,
    /// TLS CA 证书路径（预留，TLS 上线后填写）
    ca: Option<String>,
    /// 文件监控目录（预留）
    watch_dirs: Option<Vec<String>>,
}

impl AgentConfig {
    fn load() -> Self {
        let Some(exe) = std::env::current_exe().ok() else {
            return AgentConfig::default();
        };
        let Some(dir) = exe.parent() else {
            return AgentConfig::default();
        };
        let path = dir.join("agent.conf");
        match std::fs::read_to_string(&path) {
            Ok(content) => match serde_json::from_str(&content) {
                Ok(cfg) => {
                    tracing::info!("已加载配置: {}", path.display());
                    cfg
                }
                Err(e) => {
                    tracing::warn!("agent.conf 解析失败（{}）: {}", e, path.display());
                    AgentConfig::default()
                }
            },
            Err(_) => AgentConfig::default(),
        }
    }
}

/// 将 Server 地址写入 exe 同目录 agent.conf（MSI 安装器 custom action 调用）。
/// 保留已有 ca/watch_dirs 配置；文件不存在或解析失败时生成默认结构。
fn write_config_file(server: &str) -> Result<()> {
    let exe = std::env::current_exe().context("无法获取可执行文件路径")?;
    let dir = exe.parent().ok_or_else(|| anyhow::anyhow!("无法获取安装目录"))?;
    let path = dir.join("agent.conf");

    let mut conf: serde_json::Value = match std::fs::read_to_string(&path) {
        Ok(content) => serde_json::from_str(&content).unwrap_or_else(|_| serde_json::json!({})),
        Err(_) => serde_json::json!({}),
    };
    conf["server"] = serde_json::Value::String(server.to_string());
    if conf.get("ca").is_none() {
        conf["ca"] = serde_json::Value::String(String::new());
    }
    if conf.get("watch_dirs").is_none() {
        conf["watch_dirs"] = serde_json::Value::Array(Vec::new());
    }

    std::fs::write(&path, serde_json::to_string_pretty(&conf)?)?;
    println!("agent.conf 已写入: {}", path.display());
    Ok(())
}

/// Agent 主循环：读取配置 → 构造主机信息 → 连接 Server → 断线重连。
/// 服务模式下由 service::service_main 调用；前台模式由 main 调用。
/// 参数优先级：CLI > agent.conf > 内置默认值。
pub async fn run_agent_loop(
    cli_server: Option<String>,
    cli_watch: Option<String>,
    cli_agent_id: Option<String>,
    interval_secs: u64,
    hostname_override: Option<String>,
) -> Result<()> {
    let cfg = AgentConfig::load();

    // server 地址：CLI > 配置 > 默认（空串视为未配置）
    let server = cli_server
        .filter(|s| !s.is_empty())
        .or_else(|| cfg.server.clone().filter(|s| !s.is_empty()))
        .unwrap_or_else(|| "http://127.0.0.1:50051".to_string());

    // 文件监控目录：CLI > 配置 > 默认（空，由 Server 下发）
    let watch: Vec<String> = if let Some(w) = cli_watch {
        if w.trim().is_empty() {
            Vec::new()
        } else {
            w.split(',')
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect()
        }
    } else if let Some(dirs) = &cfg.watch_dirs {
        dirs.clone()
    } else {
        Vec::new()
    };

    if let Some(ca) = cfg.ca.as_deref().filter(|s| !s.is_empty()) {
        info!("TLS CA 已配置: {}", ca);
    }

    let sys = Arc::new(tokio::sync::Mutex::new(System::new_all()));

    let hostname = hostname_override.unwrap_or_else(|| {
        sysinfo::System::host_name().unwrap_or_else(|| "unknown".into())
    });

    // Agent ID：优先用命令行指定的，否则从文件读取/自动生成并持久化
    // 第二返回值：是否首次上线（新建 agent_id 或 CLI 指定）→ 决定是否上报系统快照
    let (agent_id, need_snapshot) = if let Some(id) = cli_agent_id {
        (id, true)
    } else {
        load_or_create_agent_id()
    };

    info!("Agent {} ({}) 启动中...", agent_id, hostname);
    info!("连接 Server: {}", server);

    // 采集主机信息：OS 版本 / 内核版本 / 启动时间 / 网卡 IP（sysinfo 跨平台，0.33 为关联函数）
    let os_version = sysinfo::System::os_version().unwrap_or_default();
    let kernel_version = sysinfo::System::kernel_version().unwrap_or_default();
    let boot_time_ns = sysinfo::System::boot_time().saturating_mul(1_000_000_000);
    let net = sysinfo::Networks::new_with_refreshed_list();
    let mut ips: Vec<String> = Vec::new();
    for data in net.list().values() {
        for ip in data.ip_networks() {
            if ip.addr.is_ipv4() && !ip.addr.is_loopback() && !ips.contains(&ip.addr.to_string()) {
                ips.push(ip.addr.to_string());
            }
        }
    }

    let agent_info = AgentInfo {
        agent_id,
        hostname,
        os_type: std::env::consts::OS.to_string(),
        os_version,
        kernel_version,
        agent_version: env!("CARGO_PKG_VERSION").to_string(),
        ip_addresses: ips,
        boot_time_ns,
        arch: std::env::consts::ARCH.to_string(),
    };

    // 断线重连循环
    let watch_str = watch.join(",");
    // 快照只在首次成功连接时发送一次（发完即置 false，重连不再发）
    let mut need_snapshot = need_snapshot;
    loop {
        if stop_requested() {
            info!("收到停止请求，Agent 退出");
            break;
        }
        match run(&server, agent_info.clone(), &sys, interval_secs, &watch_str, cfg.ca.clone(), &mut need_snapshot).await {
            Ok(()) => {
                info!("连接正常结束，5 秒后重连...");
                if sleep_interruptible(Duration::from_secs(5)).await {
                    info!("停止请求打断重连等待");
                    break;
                }
            }
            Err(e) => {
                error!("连接错误: {:?}，15 秒后重试...", e);
                if sleep_interruptible(Duration::from_secs(15)).await {
                    info!("停止请求打断重试等待");
                    break;
                }
            }
        }
    }

    Ok(())
}

/// 睡眠期间可被停止请求打断（服务停止时无需等满 sleep 窗口）
async fn sleep_interruptible(d: Duration) -> bool {
    tokio::select! {
        _ = time::sleep(d) => false,
        _ = async {
            while !stop_requested() {
                time::sleep(Duration::from_millis(100)).await;
            }
        } => true,
    }
}

/// 获取默认文件监控目录（按平台区分）
fn get_default_watch_dirs() -> Vec<String> {
    #[cfg(target_os = "windows")]
    {
        // Windows 临时目录 + 用户下载目录
        let mut dirs = vec![
            "C:\\Windows\\Temp".into(),
            std::env::var("TEMP").unwrap_or_else(|_| "C:\\Temp".into()),
        ];
        if let Ok(home) = std::env::var("USERPROFILE") {
            dirs.push(format!("{}\\Downloads", home));
            dirs.push(format!("{}\\AppData\\Local\\Temp", home));
        }
        dirs.push("C:\\Users\\Public".into());
        dirs
    }
    #[cfg(not(target_os = "windows"))]
    {
        vec!["/tmp".into()]
    }
}

// 解析 CA 证书路径：相对路径按 exe 同目录解析（安装场景 agent.conf 与 ca.crt 同在安装目录）
fn resolve_ca_path(p: &str) -> std::path::PathBuf {
    let pb = std::path::PathBuf::from(p);
    if pb.is_absolute() {
        return pb;
    }
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            return dir.join(pb);
        }
    }
    pb
}

async fn run(
    server: &str,
    agent_info: AgentInfo,
    system: &Arc<tokio::sync::Mutex<System>>,
    interval_secs: u64,
    watch: &str,
    ca: Option<String>,
    need_snapshot: &mut bool,
) -> Result<()> {
    // TLS：server 为 https:// 时启用（ca 指向 CA 证书，相对路径按 exe 同目录解析）
    let mut endpoint = Endpoint::from_shared(server.to_string()).context("无效的 Server 地址")?;
    if server.starts_with("https://") {
        let ca_val = ca.as_deref().filter(|s| !s.is_empty()).ok_or_else(|| {
            anyhow::anyhow!("Server 地址为 https://，但 agent.conf 未配置 ca（TLS CA 证书路径）")
        })?;
        let ca_path = resolve_ca_path(ca_val);
        let ca_pem = std::fs::read(&ca_path).with_context(|| format!("读取 CA 证书失败: {}", ca_path.display()))?;
        let tls = tonic::transport::ClientTlsConfig::new()
            .ca_certificate(tonic::transport::Certificate::from_pem(ca_pem));
        endpoint = endpoint.tls_config(tls).context("TLS 配置失败")?;
        info!("已启用 TLS，CA: {}", ca_path.display());
    }
    let channel = endpoint.connect().await.context("连接 Server 失败")?;
    let mut client = BaizeServiceClient::new(channel);
    info!("已连接到 Server: {}", server);

    let (tx, mut rx) = mpsc::channel::<Event>(1024);

    // 启动进程快照（不产生事件，事件由 EventLog 4688 实时采集）
    let _tx = tx.clone();
    let _sys = Arc::clone(system);
    tokio::spawn(async move {
        if let Err(e) = collector::process::start(_sys, _tx).await {
            error!("进程快照错误: {:?}", e);
        }
    });

    // 进程事件由独立采集器实时采集（EvtSubscribe），由 Server 配置控制启停

    // 采集器管理器：持有各事件类型的采集器实例，随 Server 指令启停
    let collectors = Arc::new(CollectorManager::new());

    // 转发线程：rx → AgentInfo + seq → gRPC 流
    // （不再过滤事件类型 — 采集器关闭后根本不会产生对应事件）
    let (request_tx, request_rx) = mpsc::channel::<Event>(1024);
    let agent_info_clone = agent_info.clone();

    tokio::spawn(async move {
        let mut seq: u64 = 0;
        while let Some(mut event) = rx.recv().await {
            seq += 1;
            event.agent_info = Some(agent_info_clone.clone());
            event.sequence_id = seq;
            if request_tx.send(event).await.is_err() {
                break;
            }
        }
    });

    let streaming_request = tokio_stream::wrappers::ReceiverStream::new(request_rx);

    // 在建立 gRPC 流之前先放入上线事件，避免死锁
    let _ = tx.send(pb::Event {
        agent_info: None,
        sequence_id: 0,
        event_type: Some(pb::event::EventType::ProcessCreate(pb::ProcessCreateEvent::default())),
    }).await;

    // 首次上线：采集并上报系统状态（进程 + TCP/UDP 连接）
    // 发送后置 false —— 断线重连不再重发；Server 侧按 agent_id 幂等覆盖存储
    if *need_snapshot {
        let st = collect_system_state();
        info!(
            "[状态] 首次上线上报: {} 进程, {} TCP, {} UDP",
            st.processes.len(),
            st.tcp_connections.len(),
            st.udp_endpoints.len()
        );
        let _ = tx.send(pb::Event {
            agent_info: None,
            sequence_id: 0,
            event_type: Some(pb::event::EventType::SystemState(st)),
        }).await;
        *need_snapshot = false;
    }

    let response = client
        .agent_stream(Request::new(streaming_request))
        .await
        .context("AgentStream RPC 失败")?;

    info!("双向流已建立，等待 Server 指令...");

    let mut incoming = response.into_inner();

    // 心跳循环：每 30 秒上报 AgentInfo（注册 / 保活，独立于事件流）
    {
        let mut hb_client = client.clone();
        let hb_info = agent_info.clone();
        tokio::spawn(async move {
            loop {
                time::sleep(Duration::from_secs(30)).await;
                match hb_client.heartbeat(Request::new(hb_info.clone())).await {
                    Ok(_) => {}
                    Err(e) => error!("心跳上报失败: {:?}", e),
                }
            }
        });
    }

    loop {
        tokio::select! {
            msg = incoming.message() => {
                match msg {
                    Ok(Some(cmd)) => {
                        info!("收到指令: {:?}", cmd);

        // 执行指令
        if let Some(cmd_type) = &cmd.command_type {
            use pb::command::CommandType;
            let result = match cmd_type {
                CommandType::Isolate(isolate_cmd) => {
                    execute_isolate(isolate_cmd.isolate).map(|_| String::new())
                }
                CommandType::KillProcess(kill_cmd) => {
                    execute_kill_process(kill_cmd.pid).map(|_| String::new())
                }
                CommandType::DeleteFile(del_cmd) => {
                    execute_delete_file(&del_cmd.file_path, del_cmd.force).map(|_| String::new())
                }
                CommandType::ExecuteScript(script_cmd) => {
                    execute_script(&script_cmd.script_content, &script_cmd.interpreter).map(|_| String::new())
                }
                CommandType::ConfigureFileWatch(fw_cmd) => {
                    info!("[配置] 文件监控目录: {:?}", fw_cmd.watch_dirs);
                    let dirs: Vec<String> = fw_cmd.watch_dirs.clone();
                    // 记录目录（短持有锁）
                    {
                        let mut fc = collectors.file.lock().unwrap();
                        fc.set_dirs(dirs.clone());
                    }
                    // 事件类型 file 开关开启时才启动
                    if dirs.is_empty() {
                        collectors.file.lock().unwrap().stop();
                    } else if event_type_enabled(&collectors, "file") {
                        if let Err(e) = collectors.file.lock().unwrap().start(dirs, tx.clone()) {
                            error!("文件监控启动失败: {:?}", e);
                        }
                    } else {
                        info!("[配置] file 开关未开启，仅记录目录，等待开启后启动");
                    }
                    Ok(String::new())
                }
                CommandType::QuerySystemInfo(_) => {
                    // 手动刷新：先上报系统状态（Server 覆盖落库，刷新 = 更新资产状态），
                    // 再返回实时 JSON（Dashboard 展示用）
                    let st = collect_system_state();
                    info!(
                        "[状态] 手动刷新上报: {} 进程, {} TCP, {} UDP",
                        st.processes.len(),
                        st.tcp_connections.len(),
                        st.udp_endpoints.len()
                    );
                    let _ = tx.send(pb::Event {
                        agent_info: None,
                        sequence_id: 0,
                        event_type: Some(pb::event::EventType::SystemState(st)),
                    }).await;
                    query_system_info()
                }
                CommandType::ConfigureEventTypes(et_cmd) => {
                    info!("[配置] 事件类型开关: {:?}", et_cmd.categories);
                    for (k, v) in &et_cmd.categories {
                        apply_event_type(&collectors, k, *v, &tx);
                    }
                    Ok(String::new())
                }
            };

            let (success, output, error_msg) = match result {
                Ok(out) => (true, out, String::new()),
                Err(e) => (false, String::new(), e.to_string()),
            };
            info!("指令 {} 执行结果: success={} error={}", cmd.command_id, success, error_msg);

            // 上报执行结果
            let result_msg = pb::CommandResult {
                command_id: cmd.command_id.clone(),
                success,
                error_message: error_msg,
                output,
                completed_at_ns: chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64,
            };
            let _ = client.report_command_result(result_msg).await;
                        }
                    }
                    Ok(None) => {
                        info!("Server 流已关闭");
                        break;
                    }
                    Err(e) => return Err(e.into()),
                }
            }
            _ = time::sleep(Duration::from_secs(1)) => {
                if stop_requested() {
                    info!("收到停止请求，关闭连接");
                    break;
                }
            }
        }
    }

    info!("Server 流已关闭");
    Ok(())
}

// ── 辅助函数 ──────────────────────────────────────────

/// 采集器管理器 — 持有各事件类型的采集器实例
/// 事件类型开关由 Server 通过 ConfigureEventTypesCommand 控制，
/// 关闭时停止对应采集器（EvtClose + 释放审计策略），不再本地采集。
struct CollectorManager {
    #[cfg(target_os = "windows")]
    process: std::sync::Mutex<collector::process_collector::ProcessCollector>,
    #[cfg(target_os = "windows")]
    network: std::sync::Mutex<collector::network_collector::NetworkCollector>,
    #[cfg(target_os = "windows")]
    dns: std::sync::Mutex<collector::dns_collector::DnsCollector>,
    file: std::sync::Mutex<collector::file::FileCollector>,
    /// 事件类型开关状态（Server 下发的最新值）
    states: std::sync::Mutex<HashMap<String, bool>>,
}

impl CollectorManager {
    fn new() -> Self {
        #[cfg(target_os = "windows")]
        {
            let policies = Arc::new(collector::audit_policy::AuditPolicyManager::new());
            Self {
                process: std::sync::Mutex::new(collector::process_collector::ProcessCollector::new(Arc::clone(&policies))),
                network: std::sync::Mutex::new(collector::network_collector::NetworkCollector::new(Arc::clone(&policies))),
                dns: std::sync::Mutex::new(collector::dns_collector::DnsCollector::new(Arc::clone(&policies))),
                file: std::sync::Mutex::new(collector::file::FileCollector::new()),
                states: std::sync::Mutex::new(HashMap::new()),
            }
        }
        #[cfg(not(target_os = "windows"))]
        {
            Self {
                file: std::sync::Mutex::new(collector::file::FileCollector::new()),
                states: std::sync::Mutex::new(HashMap::new()),
            }
        }
    }
}

/// 应用单个事件类型的开关配置：true 启动采集器，false 停止
fn apply_event_type(collectors: &CollectorManager, category: &str, enabled: bool, tx: &mpsc::Sender<pb::Event>) {
    collectors.states.lock().unwrap().insert(category.to_string(), enabled);

    match category {
        #[cfg(target_os = "windows")]
        "process" => {
            let mut c = collectors.process.lock().unwrap();
            if enabled {
                if let Err(e) = c.start(tx.clone()) {
                    error!("process 采集器启动失败: {:?}", e);
                }
            } else if let Err(e) = c.stop() {
                error!("process 采集器停止失败: {:?}", e);
            }
        }
        #[cfg(target_os = "windows")]
        "network" => {
            let mut c = collectors.network.lock().unwrap();
            if enabled {
                if let Err(e) = c.start(tx.clone()) {
                    error!("network 采集器启动失败: {:?}", e);
                }
            } else if let Err(e) = c.stop() {
                error!("network 采集器停止失败: {:?}", e);
            }
        }
        #[cfg(target_os = "windows")]
        "dns" => {
            let mut c = collectors.dns.lock().unwrap();
            if enabled {
                if let Err(e) = c.start(tx.clone()) {
                    error!("dns 采集器启动失败: {:?}", e);
                }
            } else if let Err(e) = c.stop() {
                error!("dns 采集器停止失败: {:?}", e);
            }
        }
        "file" => {
            let mut c = collectors.file.lock().unwrap();
            if enabled {
                let dirs = c.dirs().to_vec();
                if dirs.is_empty() {
                    info!("[配置] file 已开启但尚未收到监控目录，等待 ConfigureFileWatch");
                } else if let Err(e) = c.start(dirs, tx.clone()) {
                    error!("file 采集器启动失败: {:?}", e);
                }
            } else {
                c.stop();
            }
        }
        "registry" | "task" | "yara" => {
            if enabled {
                info!("[配置] 事件类型 {} 尚未实现，忽略", category);
            }
        }
        _ => {
            info!("[配置] 未知事件类型: {}", category);
        }
    }
}

/// 查询某事件类型的开关状态
fn event_type_enabled(collectors: &CollectorManager, category: &str) -> bool {
    collectors
        .states
        .lock()
        .unwrap()
        .get(category)
        .copied()
        .unwrap_or(false)
}

// ── Agent ID 持久化 ─────────────────────────────────────

fn agent_id_path() -> std::path::PathBuf {
    if cfg!(target_os = "windows") {
        let dir = std::path::PathBuf::from(std::env::var("PROGRAMDATA").unwrap_or_else(|_| "C:\\ProgramData".into()));
        dir.join("Baize").join("agent_id")
    } else {
        std::path::PathBuf::from("/var/lib/baize/agent_id")
    }
}

/// 读取或创建 Agent ID。返回 (id, 是否新建)。
/// 新建 = agent_id 文件首次创建 = 真·首次上线 → 需要上报系统快照。
/// CLI 显式指定 agent_id 时也视为"需要快照"（开发/调试场景，Server 幂等覆盖无害）。
fn load_or_create_agent_id() -> (String, bool) {
    let path = agent_id_path();

    // 尝试读取已有的 agent_id
    if let Ok(id) = std::fs::read_to_string(&path) {
        let id = id.trim().to_string();
        if !id.is_empty() {
            tracing::info!("读取已保存的 Agent ID: {}", id);
            return (id, false);
        }
    }

    // 生成新 UUID 并保存
    let new_id = uuid::Uuid::new_v4().to_string();
    if let Some(parent) = path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    match std::fs::write(&path, &new_id) {
        Ok(_) => tracing::info!("Agent ID 已保存到: {:?}", path),
        Err(e) => tracing::warn!("无法保存 Agent ID 文件: {:?}", e),
    }
    (new_id, true)
}

// ── 指令执行器 ──────────────────────────────────────────

use std::process::Command as StdCommand;

/// 隔离/解除隔离主机
fn execute_isolate(isolate: bool) -> anyhow::Result<()> {
    if cfg!(target_os = "windows") {
        if isolate {
            // 修改防火墙规则阻止所有出站连接
            StdCommand::new("netsh")
                .args(["advfirewall", "set", "allprofiles", "firewallpolicy", "blockinbound,blockoutbound"])
                .output()?;
            tracing::warn!("[响应] 主机已隔离（出站已阻断）");
        } else {
            StdCommand::new("netsh")
                .args(["advfirewall", "set", "allprofiles", "firewallpolicy", "blockinbound,allowoutbound"])
                .output()?;
            tracing::warn!("[响应] 主机隔离已解除");
        }
    }
    Ok(())
}

/// 杀进程
fn execute_kill_process(pid: u64) -> anyhow::Result<()> {
    if cfg!(target_os = "windows") {
        StdCommand::new("taskkill")
            .args(["/F", "/PID", &pid.to_string()])
            .output()?;
    } else {
        StdCommand::new("kill")
            .args(["-9", &pid.to_string()])
            .output()?;
    }
    tracing::warn!("[响应] 已终止进程 PID={}", pid);
    Ok(())
}

/// 删除文件
fn execute_delete_file(path: &str, force: bool) -> anyhow::Result<()> {
    if cfg!(target_os = "windows") {
        let mut cmd = StdCommand::new("del");
        if force { cmd.arg("/F"); }
        cmd.arg("/Q").arg(path).output()?;
    } else {
        let mut cmd = StdCommand::new("rm");
        if force { cmd.arg("-f"); }
        cmd.arg(path).output()?;
    }
    tracing::warn!("[响应] 已删除: {}", path);
    Ok(())
}

/// 远程执行脚本
fn execute_script(content: &str, interpreter: &str) -> anyhow::Result<()> {
    let mut cmd = match interpreter {
        "powershell" => {
            let mut c = StdCommand::new("powershell");
            c.args(["-NoProfile", "-Command", content]);
            c
        }
        "cmd" => {
            let mut c = StdCommand::new("cmd");
            c.args(["/C", content]);
            c
        }
        "bash" => {
            let mut c = StdCommand::new("bash");
            c.args(["-c", content]);
            c
        }
        "python" => {
            let mut c = StdCommand::new("python");
            c.args(["-c", content]);
            c
        }
        _ => anyhow::bail!("不支持的脚本解释器: {}", interpreter),
    };
    let output = cmd.output()?;
    let stdout = String::from_utf8_lossy(&output.stdout);
    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        anyhow::bail!("脚本执行失败: {}", stderr);
    }
    tracing::warn!("[响应] 脚本执行成功: {} bytes", stdout.len());
    Ok(())
}

// ── 系统信息查询 ──────────────────────────────────────

/// 采集系统状态（首次上线 / 手动刷新时的当前进程 + 网络连接）。
/// 进程：sysinfo（跨平台）；TCP/UDP 连接表：GetExtendedTcpTable/GetExtendedUdpTable（仅 Windows）。
/// 与 query_system_info 的区别：返回结构化 proto message（落库），而非 JSON 字符串（Dashboard 展示）。
fn collect_system_state() -> pb::SystemStateEvent {
    let mut st = pb::SystemStateEvent {
        captured_at_ns: chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64,
        processes: Vec::new(),
        tcp_connections: Vec::new(),
        udp_endpoints: Vec::new(),
    };

    // 进程信息 (sysinfo)
    let mut sys = sysinfo::System::new();
    sys.refresh_all();
    for (pid, proc) in sys.processes() {
        // cpu 可能为 NaN（进程 CPU 时间未采样/权限不足），JSON 不支持 NaN，清洗为 0
        let cpu = proc.cpu_usage();
        let cpu = if cpu.is_finite() { cpu } else { 0.0 };
        st.processes.push(pb::ProcessInfo {
            pid: pid.as_u32() as u64,
            name: proc.name().to_string_lossy().into(),
            exe: proc.exe().map(|p| p.to_string_lossy().into()).unwrap_or_default(),
            cpu,
            memory: proc.memory(),
        });
    }

    #[cfg(windows)]
    collect_network_state(&mut st);

    st
}

#[cfg(windows)]
fn collect_network_state(st: &mut pb::SystemStateEvent) {
    use windows::Win32::NetworkManagement::IpHelper::*;

    const AF_INET: u32 = 2;

    unsafe {
        // TCP 连接表
        let mut buf_size: u32 = 0;
        let _ = GetExtendedTcpTable(
            None, &mut buf_size as *mut u32, false, AF_INET, TCP_TABLE_OWNER_PID_ALL, 0,
        );
        if buf_size > 0 {
            let mut buf = vec![0u8; buf_size as usize];
            if GetExtendedTcpTable(
                Some(buf.as_mut_ptr() as *mut core::ffi::c_void),
                &mut buf_size as *mut u32, false, AF_INET, TCP_TABLE_OWNER_PID_ALL, 0,
            ) == 0 {
                let table = &*(buf.as_ptr() as *const MIB_TCPTABLE_OWNER_PID);
                let entries = std::slice::from_raw_parts(
                    table.table.as_ptr(),
                    table.dwNumEntries as usize,
                );
                for row in entries {
                    let state = match row.dwState {
                        1 => "CLOSED", 2 => "LISTEN", 3 => "SYN_SENT",
                        4 => "SYN_RCVD", 5 => "ESTABLISHED", 6 => "FIN_WAIT1",
                        7 => "FIN_WAIT2", 8 => "CLOSE_WAIT", 9 => "CLOSING",
                        10 => "LAST_ACK", 11 => "TIME_WAIT", _ => "UNKNOWN",
                    };
                    st.tcp_connections.push(pb::ConnectionInfo {
                        pid: row.dwOwningPid as u64,
                        local_ip: std::net::Ipv4Addr::from(row.dwLocalAddr.to_ne_bytes()).to_string(),
                        local_port: u16::from_be(row.dwLocalPort as u16) as u32,
                        remote_ip: std::net::Ipv4Addr::from(row.dwRemoteAddr.to_ne_bytes()).to_string(),
                        remote_port: u16::from_be(row.dwRemotePort as u16) as u32,
                        state: state.to_string(),
                    });
                }
            }
        }

        // UDP 监听表
        let mut buf_size: u32 = 0;
        let _ = GetExtendedUdpTable(
            None, &mut buf_size as *mut u32, false, AF_INET, UDP_TABLE_OWNER_PID, 0,
        );
        if buf_size > 0 {
            let mut buf = vec![0u8; buf_size as usize];
            if GetExtendedUdpTable(
                Some(buf.as_mut_ptr() as *mut core::ffi::c_void),
                &mut buf_size as *mut u32, false, AF_INET, UDP_TABLE_OWNER_PID, 0,
            ) == 0 {
                let table = &*(buf.as_ptr() as *const MIB_UDPTABLE_OWNER_PID);
                let entries = std::slice::from_raw_parts(
                    table.table.as_ptr(),
                    table.dwNumEntries as usize,
                );
                for row in entries {
                    st.udp_endpoints.push(pb::ConnectionInfo {
                        pid: row.dwOwningPid as u64,
                        local_ip: std::net::Ipv4Addr::from(row.dwLocalAddr.to_ne_bytes()).to_string(),
                        local_port: u16::from_be(row.dwLocalPort as u16) as u32,
                        remote_ip: String::new(),
                        remote_port: 0,
                        state: "LISTEN".to_string(),
                    });
                }
            }
        }
    }
}

#[cfg(not(windows))]
fn collect_network_state(_st: &mut pb::SystemStateEvent) {}
#[cfg(windows)]
fn query_system_info() -> anyhow::Result<String> {
    use serde::Serialize;
    use windows::Win32::NetworkManagement::IpHelper::*;

    const AF_INET: u32 = 2;

    #[derive(Serialize)]
    struct SysInfo {
        processes: Vec<ProcInfo>,
        tcp_connections: Vec<ConnInfo>,
        udp_endpoints: Vec<ConnInfo>,
    }
    #[derive(Serialize)]
    struct ProcInfo {
        pid: u32,
        name: String,
        exe: String,
        cpu: f32,
        memory: u64,
    }
    #[derive(Serialize)]
    struct ConnInfo {
        pid: u32,
        local: String,
        remote: String,
        state: String,
    }

    let mut info = SysInfo {
        processes: vec![],
        tcp_connections: vec![],
        udp_endpoints: vec![],
    };

    // 进程信息 (sysinfo)
    let mut sys = sysinfo::System::new();
    sys.refresh_all();
    for (pid, proc) in sys.processes() {
        info.processes.push(ProcInfo {
            pid: pid.as_u32(),
            name: proc.name().to_string_lossy().into(),
            exe: proc.exe().map(|p| p.to_string_lossy().into()).unwrap_or_default(),
            cpu: proc.cpu_usage(),
            memory: proc.memory(),
        });
    }

    unsafe {
        // TCP 连接表
        let mut buf_size: u32 = 0;
        let _ = GetExtendedTcpTable(
            None, &mut buf_size as *mut u32, false, AF_INET, TCP_TABLE_OWNER_PID_ALL, 0,
        );
        if buf_size > 0 {
            let mut buf = vec![0u8; buf_size as usize];
            if GetExtendedTcpTable(
                Some(buf.as_mut_ptr() as *mut core::ffi::c_void),
                &mut buf_size as *mut u32, false, AF_INET, TCP_TABLE_OWNER_PID_ALL, 0,
            ) == 0 {
                let table = &*(buf.as_ptr() as *const MIB_TCPTABLE_OWNER_PID);
                let entries = std::slice::from_raw_parts(
                    table.table.as_ptr(),
                    table.dwNumEntries as usize,
                );
                for row in entries {
                    let local = std::net::Ipv4Addr::from(row.dwLocalAddr.to_ne_bytes());
                    let remote = std::net::Ipv4Addr::from(row.dwRemoteAddr.to_ne_bytes());
                    let state = match row.dwState {
                        1 => "CLOSED", 2 => "LISTEN", 3 => "SYN_SENT",
                        4 => "SYN_RCVD", 5 => "ESTABLISHED", 6 => "FIN_WAIT1",
                        7 => "FIN_WAIT2", 8 => "CLOSE_WAIT", 9 => "CLOSING",
                        10 => "LAST_ACK", 11 => "TIME_WAIT", _ => "UNKNOWN",
                    };
                    info.tcp_connections.push(ConnInfo {
                        pid: row.dwOwningPid,
                        local: format!("{}:{}", local, u16::from_be(row.dwLocalPort as u16)),
                        remote: format!("{}:{}", remote, u16::from_be(row.dwRemotePort as u16)),
                        state: state.to_string(),
                    });
                }
            }
        }

        // UDP 监听表
        let mut buf_size: u32 = 0;
        let _ = GetExtendedUdpTable(
            None, &mut buf_size as *mut u32, false, AF_INET, UDP_TABLE_OWNER_PID, 0,
        );
        if buf_size > 0 {
            let mut buf = vec![0u8; buf_size as usize];
            if GetExtendedUdpTable(
                Some(buf.as_mut_ptr() as *mut core::ffi::c_void),
                &mut buf_size as *mut u32, false, AF_INET, UDP_TABLE_OWNER_PID, 0,
            ) == 0 {
                let table = &*(buf.as_ptr() as *const MIB_UDPTABLE_OWNER_PID);
                let entries = std::slice::from_raw_parts(
                    table.table.as_ptr(),
                    table.dwNumEntries as usize,
                );
                for row in entries {
                    let local = std::net::Ipv4Addr::from(row.dwLocalAddr.to_ne_bytes());
                    info.udp_endpoints.push(ConnInfo {
                        pid: row.dwOwningPid,
                        local: format!("{}:{}", local, u16::from_be(row.dwLocalPort as u16)),
                        remote: String::new(),
                        state: "LISTEN".to_string(),
                    });
                }
            }
        }
    }

    Ok(serde_json::to_string(&info)?)
}

#[cfg(not(windows))]
fn query_system_info() -> anyhow::Result<String> {
    Err(anyhow::anyhow!("系统信息查询仅支持 Windows"))
}
