// Baize (白泽) Agent — Rust 版本
mod collector;
mod isolate;

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
use std::str::FromStr;
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
    /// 将注册 token 写入 exe 同目录 authd.pass 后退出（供 MSI 安装器调用；空串=删除）
    #[arg(long)]
    write_token: Option<String>,
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
    /// 解除本机网络隔离后退出（Agent 失联时的本地救援通道，需管理员权限）
    #[arg(long)]
    isolate_off: bool,
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
    // 注册 token 写入子命令（MSI 安装器调用）：写 authd.pass 后退出
    if let Some(token) = cli.write_token {
        return write_token_file(&token);
    }

    // 服务管理命令（Windows only）
    #[cfg(windows)]
    {
        if cli.install {
            return service::install().map_err(|e| anyhow::anyhow!("{}", e));
        }
        if cli.uninstall {
            // 卸载前先解除隔离：否则本机残留无主的阻断规则（无法再通过 Dashboard 解除）
            if isolate::is_active() {
                match isolate::release() {
                    Ok(()) => println!("已解除网络隔离"),
                    Err(e) => tracing::warn!("[隔离] 卸载前解除隔离失败: {:?}", e),
                }
            }
            return service::uninstall().map_err(|e| anyhow::anyhow!("{}", e));
        }
        if cli.service {
            info!("[Service] 以 Windows 服务模式启动...");
            return service::run_as_service().map_err(|e| anyhow::anyhow!("{}", e));
        }
    }

    // 本地解除隔离（Agent 失联时的现场救援通道）
    if cli.isolate_off {
        isolate::release()?;
        println!("已解除网络隔离");
        return Ok(());
    }

    run_agent_loop(cli.server, cli.watch, cli.agent_id, cli.interval, cli.hostname).await
}

/// 从 Server 地址解析 gRPC 端口（用作隔离期间的管理通道白名单）。
/// 例：http://192.168.1.10:50051 → 50051；未写端口时回落到默认端口。
fn parse_server_port(server: &str) -> u16 {
    server
        .rsplit(':')
        .next()
        .and_then(|p| p.trim_end_matches('/').parse::<u16>().ok())
        .unwrap_or(isolate::DEFAULT_SERVER_PORT)
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

/// 将注册 token 写入 exe 同目录 authd.pass（MSI 安装器 custom action 调用）。
/// token 为空串时删除文件（卸载清理 / 重装前清旧 token）。
fn write_token_file(token: &str) -> Result<()> {
    let exe = std::env::current_exe().context("无法获取可执行文件路径")?;
    let dir = exe.parent().ok_or_else(|| anyhow::anyhow!("无法获取安装目录"))?;
    let path = dir.join("authd.pass");

    if token.trim().is_empty() {
        if path.exists() {
            std::fs::remove_file(&path)?;
            println!("authd.pass 已删除: {}", path.display());
        }
        return Ok(());
    }
    std::fs::write(&path, token.trim())?;
    println!("authd.pass 已写入: {}", path.display());
    Ok(())
}

// ── Agent 身份文件（client.key / authd.pass）──────────────

/// 通信身份密钥文件路径（exe 同目录，对标 Wazuh client.keys）
fn client_key_path() -> std::path::PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|exe| exe.parent().map(|d| d.join("client.key")))
        .unwrap_or_else(|| std::path::PathBuf::from("client.key"))
}

/// 注册 token 文件路径（exe 同目录，对标 Wazuh authd.pass）
fn authd_pass_path() -> std::path::PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|exe| exe.parent().map(|d| d.join("authd.pass")))
        .unwrap_or_else(|| std::path::PathBuf::from("authd.pass"))
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

    // 管理通道白名单端口（隔离期间放行 Agent → Server）
    let server_port = parse_server_port(&server);

    // 系统重启后 WFP 过滤器不保留：本地状态若为"隔离中"，此处重新施加
    isolate::reapply_if_needed();

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
        isolated: isolate::is_active(),
    };

    // 隔离 TTL 到期自动解除（每 30 秒检查一次；ttl_seconds=0 表示不自动解除）
    tokio::spawn(async move {
        loop {
            time::sleep(Duration::from_secs(30)).await;
            let state = isolate::load_state();
            if isolate::ttl_expired(&state) {
                tracing::warn!("[隔离] TTL 到期，自动解除网络隔离");
                if let Err(e) = isolate::release() {
                    tracing::error!("[隔离] TTL 自动解除失败: {:?}", e);
                }
            }
        }
    });

    // 断线重连循环
    let watch_str = watch.join(",");

    // 注册状态机：有 client.key 直接用；无 → 读 authd.pass token → Register RPC 换取身份密钥
    let mut agent_key = ensure_client_key(&server, &agent_info, &cfg.ca).await?;
    info!("Agent 身份密钥就绪 (client.key)");

    // 身份失效标志：心跳/连接被拒（Unauthenticated）时置位，触发删 key 重新注册
    let auth_failed = Arc::new(AtomicBool::new(false));

    // 快照只在首次成功连接时发送一次（发完即置 false，重连不再发）
    let mut need_snapshot = need_snapshot;
    loop {
        if stop_requested() {
            info!("收到停止请求，Agent 退出");
            break;
        }
        match run(&server, server_port, agent_info.clone(), &sys, interval_secs, &watch_str, cfg.ca.clone(), &agent_key, &auth_failed, &mut need_snapshot).await {
            Ok(()) => {
                info!("连接正常结束，5 秒后重连...");
                if sleep_interruptible(Duration::from_secs(5)).await {
                    info!("停止请求打断重连等待");
                    break;
                }
            }
            Err(e) => {
                // 身份失效（被吊销/key 被换）：删除本地 client.key，凭 token 重新注册
                if auth_failed.load(Ordering::Relaxed) {
                    auth_failed.store(false, Ordering::Relaxed);
                    let _ = std::fs::remove_file(client_key_path());
                    match ensure_client_key(&server, &agent_info, &cfg.ca).await {
                        Ok(k) => {
                            agent_key = k;
                            info!("已凭 enrollment token 重新注册");
                        }
                        Err(re) => error!("重新注册失败（token 可能已吊销）: {:?}", re),
                    }
                }
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

// 建立 gRPC Channel（https:// 时启用 TLS，用 agent.conf 的 ca 证书验证 Server）
async fn make_channel(server: &str, ca: &Option<String>) -> Result<tonic::transport::Channel> {
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
    endpoint.connect().await.context("连接 Server 失败")
}

/// 注册状态机：确保 Agent 持有通信身份密钥（client.key）。
/// 已有 → 直接复用；无 → 读 authd.pass 的 enrollment token → Register RPC → 存 client.key。
async fn ensure_client_key(
    server: &str,
    agent_info: &pb::AgentInfo,
    ca: &Option<String>,
) -> Result<String> {
    let key_path = client_key_path();
    if let Ok(k) = std::fs::read_to_string(&key_path) {
        let k = k.trim().to_string();
        if !k.is_empty() {
            info!("已加载 client.key: {}", key_path.display());
            return Ok(k);
        }
    }

    // 无 client.key → 注册流程
    let pass_path = authd_pass_path();
    let token = std::fs::read_to_string(&pass_path)
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .ok_or_else(|| {
            anyhow::anyhow!(
                "未找到注册 token（{}）。请使用 install.bat <Server地址> <注册token> 重新安装",
                pass_path.display()
            )
        })?;

    info!("无 client.key，正在向 Server 注册...");
    let channel = make_channel(server, ca).await?;
    let mut client = BaizeServiceClient::new(channel);
    let resp = client
        .register(pb::RegisterRequest {
            agent_id: agent_info.agent_id.clone(),
            hostname: agent_info.hostname.clone(),
            token,
        })
        .await
        .context("注册失败（请检查 enrollment token 是否有效、Server 地址是否正确）")?;
    let key = resp.into_inner().agent_key;
    if key.is_empty() {
        anyhow::bail!("注册响应缺少 agent_key");
    }

    if let Some(parent) = key_path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    match std::fs::write(&key_path, &key) {
        Ok(_) => info!("client.key 已保存: {}", key_path.display()),
        Err(e) => error!("client.key 保存失败（仅本次运行有效）: {:?}", e),
    }
    info!("Agent 注册成功");
    Ok(key)
}

async fn run(
    server: &str,
    server_port: u16,
    agent_info: AgentInfo,
    system: &Arc<tokio::sync::Mutex<System>>,
    interval_secs: u64,
    watch: &str,
    ca: Option<String>,
    agent_key: &str,
    auth_failed: &Arc<AtomicBool>,
    need_snapshot: &mut bool,
) -> Result<()> {
    let channel = make_channel(server, &ca).await?;
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

    let response = {
        let mut req = Request::new(streaming_request);
        req.metadata_mut().insert(
            "baize-agent-key",
            tonic::metadata::MetadataValue::from_str(agent_key)
                .map_err(|e| anyhow::anyhow!("非法 agent_key: {}", e))?,
        );
        match client.agent_stream(req).await {
            Ok(r) => r,
            Err(e) if e.code() == tonic::Code::Unauthenticated => {
                error!("连接被拒：身份校验失败（Agent 可能已被吊销）");
                auth_failed.store(true, Ordering::Relaxed);
                return Err(anyhow::anyhow!("身份校验失败"));
            }
            Err(e) => return Err(e.into()),
        }
    };

    info!("双向流已建立，等待 Server 指令...");

    let mut incoming = response.into_inner();

    // 心跳循环：每 30 秒上报 AgentInfo（注册 / 保活，独立于事件流）。
    // 身份校验失败（如被 Server 吊销）→ 置 auth_failed 标志，主循环据此断开连接
    {
        let mut hb_client = client.clone();
        let hb_info = agent_info.clone();
        let hb_key = agent_key.to_string();
        let hb_failed = auth_failed.clone();
        tokio::spawn(async move {
            loop {
                time::sleep(Duration::from_secs(30)).await;
                // 心跳携带当前隔离状态（Agent 是隔离状态的权威源，Server 据此更新主机状态）
                let mut info = hb_info.clone();
                info.isolated = crate::isolate::is_active();
                let mut req = Request::new(info);
                if let Ok(v) = tonic::metadata::MetadataValue::from_str(&hb_key) {
                    req.metadata_mut().insert("baize-agent-key", v);
                }
                match hb_client.heartbeat(req).await {
                    Ok(_) => {}
                    Err(e) if e.code() == tonic::Code::Unauthenticated => {
                        error!("身份校验失败（Agent 可能已被吊销），断开连接");
                        hb_failed.store(true, Ordering::Relaxed);
                        break;
                    }
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
                    // 是否后台异步传输任务（不在此处同步执行/上报，由后台任务经流/CommandResult 回报）
                    let mut async_spawned = false;
                    let result: Option<Result<String>> = match cmd_type {
                        CommandType::Isolate(isolate_cmd) => Some(execute_isolate(
                            isolate_cmd.isolate,
                            isolate_cmd.ttl_seconds,
                            &isolate_cmd.reason,
                            server_port,
                        )),
                        CommandType::KillProcess(kill_cmd) => {
                            Some(execute_kill_process(kill_cmd.pid).map(|_| String::new()))
                        }
                        CommandType::DeletePath(del_cmd) => {
                            Some(execute_delete_path(&del_cmd.path, del_cmd.recursive).map(|_| String::new()))
                        }
                        CommandType::ExecuteScript(script_cmd) => {
                            // 后台异步执行：长命令不阻塞消息循环（心跳/事件上报/新指令接收不受影响）
                            let mut c = client.clone();
                            let cid = cmd.command_id.clone();
                            let content = script_cmd.script_content.clone();
                            let interp = script_cmd.interpreter.clone();
                            let tsecs = script_cmd.timeout_secs;
                            tokio::spawn(async move {
                                let (success, output, error_msg) = match execute_script(&content, &interp, tsecs).await {
                                    Ok(r) if r.exit_code == 0 => (true, r.output, String::new()),
                                    // 非 0 退出码：输出照常回传，失败原因放在 error_message
                                    Ok(r) => (false, r.output, format!("退出码 {}", r.exit_code)),
                                    Err(e) => (false, String::new(), e.to_string()),
                                };
                                info!("[响应] 命令 {} 执行完成: success={} error={}", cid, success, error_msg);
                                let result_msg = pb::CommandResult {
                                    command_id: cid,
                                    success,
                                    error_message: error_msg,
                                    output,
                                    completed_at_ns: chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64,
                                    data: Vec::new(),
                                };
                                if let Err(e) = c.report_command_result(result_msg).await {
                                    error!("[响应] 命令结果上报失败: {:?}", e);
                                }
                            });
                            async_spawned = true;
                            None
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
                            Some(Ok(String::new()))
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
                            Some(query_system_info())
                        }
                        CommandType::ConfigureEventTypes(et_cmd) => {
                            info!("[配置] 事件类型开关: {:?}", et_cmd.categories);
                            for (k, v) in &et_cmd.categories {
                                apply_event_type(&collectors, k, *v, &tx);
                            }
                            Some(Ok(String::new()))
                        }
                        CommandType::ListDir(ld_cmd) => {
                            Some(execute_list_dir(&ld_cmd.dir_path))
                        }
                        CommandType::FileDownload(fd_cmd) => {
                            let c = client.clone();
                            let aid = agent_info.agent_id.clone();
                            let tid = fd_cmd.transfer_id.clone();
                            let path = fd_cmd.file_path.clone();
                            tokio::spawn(async move {
                                if let Err(e) = run_download(c, aid, &tid, &path).await {
                                    error!("[传输] 下载失败 task={}: {:#}", tid, e);
                                }
                            });
                            async_spawned = true;
                            None
                        }
                        CommandType::FileUpload(fu_cmd) => {
                            let c = client.clone();
                            let aid = agent_info.agent_id.clone();
                            let tid = fu_cmd.transfer_id.clone();
                            let dest = fu_cmd.dest_path.clone();
                            let overwrite = fu_cmd.overwrite;
                            let cid = cmd.command_id.clone();
                            tokio::spawn(async move {
                                if let Err(e) = run_upload(c, aid, &tid, &dest, overwrite, &cid).await {
                                    error!("[传输] 上传失败 task={}: {:#}", tid, e);
                                }
                            });
                            async_spawned = true;
                            None
                        }
                    };

                    if async_spawned {
                        info!("[传输] task={} 已交给后台任务（结果经流/CommandResult 回报）", cmd.command_id);
                    } else if let Some(res) = result {
                        let (success, output, error_msg) = match res {
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
                            data: Vec::new(),
                        };
                        let _ = client.report_command_result(result_msg).await;
                    }
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
                if auth_failed.load(Ordering::Relaxed) {
                    info!("身份已失效（Agent 被吊销），关闭连接");
                    break;
                }
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

/// 隔离/解除隔离主机（WFP 专属子层：默认全断 + 管理通道白名单）
///
/// 与"改系统防火墙默认策略"的区别：
/// - 不碰用户/域组的防火墙配置（GPO 刷新覆盖不到）
/// - 放行 Agent→Server 端口，隔离后断线重连仍能成功（不会把自己彻底掐死）
/// - 解除 = 删自己的子层/过滤器，幂等且无残留
fn execute_isolate(
    isolate: bool,
    ttl_seconds: u32,
    reason: &str,
    server_port: u16,
) -> anyhow::Result<String> {
    if isolate {
        crate::isolate::apply(server_port, ttl_seconds, reason)?;
        tracing::warn!("[响应] 主机已隔离（仅保留管理通道与 DNS）");
        Ok(format!(
            "已隔离：仅放行管理通道（Server 端口 {}）、DNS(53) 与 loopback，其余出站全部阻断{}",
            server_port,
            if ttl_seconds == 0 {
                String::new()
            } else {
                format!("；{} 秒后自动解除", ttl_seconds)
            }
        ))
    } else {
        crate::isolate::release()?;
        tracing::warn!("[响应] 主机隔离已解除");
        Ok("已解除网络隔离".to_string())
    }
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

/// 删除文件或目录（永久删除，不可恢复）
fn execute_delete_path(path: &str, recursive: bool) -> Result<String> {
    let p = std::path::Path::new(path);
    let meta = std::fs::symlink_metadata(p)
        .map_err(|e| anyhow::anyhow!("无法访问 {}: {}", path, e))?;
    if meta.is_dir() {
        if !recursive {
            anyhow::bail!("{} 是目录，删除目录需启用递归删除", path);
        }
        std::fs::remove_dir_all(p)
            .map_err(|e| anyhow::anyhow!("递归删除 {} 失败: {}", path, e))?;
    } else {
        std::fs::remove_file(p)
            .map_err(|e| anyhow::anyhow!("删除 {} 失败: {}", path, e))?;
    }
    tracing::warn!("[响应] 已永久删除: {}", path);
    Ok(String::new())
}

/// 列出目录条目（目录优先、名称排序）。空路径 → Windows 返回驱动器列表 / Linux 返回根目录。
fn execute_list_dir(dir_path: &str) -> Result<String> {
    if dir_path.trim().is_empty() {
        return list_roots();
    }
    let path = std::path::Path::new(dir_path);
    if !path.is_dir() {
        anyhow::bail!("{} 不是目录或不存在", dir_path);
    }
    let mut entries = Vec::new();
    for entry in std::fs::read_dir(path)
        .map_err(|e| anyhow::anyhow!("读取目录 {} 失败: {}", dir_path, e))?
    {
        let entry = entry.map_err(|e| anyhow::anyhow!("读取条目失败: {}", e))?;
        let ft = entry.file_type().map_err(|e| anyhow::anyhow!("获取条目类型失败: {}", e))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        let full = entry.path().to_string_lossy().into_owned();
        let (is_dir, size, modified) = if ft.is_dir() {
            (true, 0i64, 0i64)
        } else if ft.is_file() {
            match entry.metadata() {
                Ok(md) => {
                    let size = md.len() as i64;
                    let modified = md
                        .modified()
                        .ok()
                        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                        .map(|d| d.as_secs() as i64)
                        .unwrap_or(0);
                    (false, size, modified)
                }
                Err(_) => continue,
            }
        } else {
            // 符号链接等其它类型
            (false, 0i64, 0i64)
        };
        entries.push(serde_json::json!({
            "name": name,
            "path": full,
            "is_dir": is_dir,
            "size": size,
            "modified": modified,
        }));
    }
    entries.sort_by(|a, b| {
        let dir_a = a["is_dir"].as_bool().unwrap_or(false);
        let dir_b = b["is_dir"].as_bool().unwrap_or(false);
        dir_b.cmp(&dir_a).then_with(|| {
            let na = a["name"].as_str().unwrap_or("");
            let nb = b["name"].as_str().unwrap_or("");
            na.to_lowercase().cmp(&nb.to_lowercase())
        })
    });
    serde_json::to_string(&entries).map_err(|e| anyhow::anyhow!("序列化失败: {}", e))
}

/// 列出可浏览的根（Windows: 驱动器列表；其它平台: /）
fn list_roots() -> Result<String> {
    let mut entries = Vec::new();
    #[cfg(windows)]
    {
        use windows::Win32::Storage::FileSystem::GetLogicalDrives;
        let mask = unsafe { GetLogicalDrives() };
        for i in 0..26u32 {
            if mask & (1 << i) != 0 {
                let letter = (b'A' + i as u8) as char;
                let root = format!("{}:\\", letter);
                entries.push(serde_json::json!({
                    "name": root.clone(),
                    "path": root,
                    "is_dir": true,
                    "size": 0i64,
                    "modified": 0i64,
                }));
            }
        }
    }
    #[cfg(not(windows))]
    {
        entries.push(serde_json::json!({
            "name": "/",
            "path": "/",
            "is_dir": true,
            "size": 0i64,
            "modified": 0i64,
        }));
    }
    serde_json::to_string(&entries).map_err(|e| anyhow::anyhow!("序列化失败: {}", e))
}

/// 远程执行脚本/命令的返回
struct ScriptResult {
    /// stdout + stderr 合并后的输出（超过上限时已截断）
    output: String,
    /// 进程退出码（异常终止时为 -1）
    exit_code: i32,
}

/// 解码子进程输出为字符串。
///
/// Windows 中文系统上 cmd/PowerShell 经管道输出的是本地代码页（GBK/936）字节，
/// 直接按 UTF-8 解会把中文毁成 U+FFFD（数据在这一步就丢了）。
/// 因此先严格试 UTF-8，失败再按系统 ANSI 代码页（CP_ACP，中文系统即 GBK）解码；
/// 其他平台退回 lossy（UTF-8 为本地编码，无需回退）。
#[cfg(windows)]
fn decode_console_output(bytes: &[u8]) -> String {
    if bytes.is_empty() {
        return String::new();
    }
    if let Ok(s) = std::str::from_utf8(bytes) {
        return s.to_string();
    }
    unsafe {
        use windows::Win32::Globalization::{
            MultiByteToWideChar, CP_ACP, MULTI_BYTE_TO_WIDE_CHAR_FLAGS,
        };
        let flags = MULTI_BYTE_TO_WIDE_CHAR_FLAGS(0);
        // 第一次调用传 None 只为取所需宽字符数
        let len = MultiByteToWideChar(CP_ACP, flags, bytes, None);
        if len > 0 {
            let mut buf = vec![0u16; len as usize];
            let n = MultiByteToWideChar(CP_ACP, flags, bytes, Some(&mut buf));
            if n > 0 {
                buf.truncate(n as usize);
                return String::from_utf16_lossy(&buf);
            }
        }
        String::from_utf8_lossy(bytes).to_string()
    }
}

#[cfg(not(windows))]
fn decode_console_output(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes).to_string()
}

/// 命令输出截断上限（64KB）：防止 `dir /s` 之类的海量输出打爆内存
const SCRIPT_MAX_OUTPUT: usize = 64 * 1024;
/// timeout_secs = 0 时使用的默认超时（秒）
const SCRIPT_DEFAULT_TIMEOUT_SECS: u64 = 30;

/// 远程执行脚本/命令
///
/// 与旧实现（同步 `std::process::Command::output()`）的差别：
///   - 异步子进程 + 超时控制，不阻塞 Agent 消息循环；
///   - 支持 timeout_secs（超时后经 kill_on_drop 终止子进程，不留孤儿）；
///   - stdout/stderr 一并回传 Server（旧实现丢弃了 stdout，前端拿不到输出）。
async fn execute_script(content: &str, interpreter: &str, timeout_secs: u32) -> anyhow::Result<ScriptResult> {
    use tokio::process::Command as TokioCommand;

    let mut cmd = match interpreter {
        "powershell" => {
            let mut c = TokioCommand::new("powershell");
            c.args(["-NoProfile", "-NonInteractive", "-Command", content]);
            c
        }
        "cmd" => {
            let mut c = TokioCommand::new("cmd");
            c.args(["/C", content]);
            c
        }
        "bash" => {
            let mut c = TokioCommand::new("bash");
            c.args(["-c", content]);
            c
        }
        "python" => {
            let mut c = TokioCommand::new("python");
            c.args(["-c", content]);
            c
        }
        _ => anyhow::bail!("不支持的脚本解释器: {}", interpreter),
    };

    // 超时后子进程句柄 drop 时自动终止，避免命令还在后台跑
    cmd.kill_on_drop(true);
    cmd.stdout(std::process::Stdio::piped());
    cmd.stderr(std::process::Stdio::piped());

    let child = cmd
        .spawn()
        .map_err(|e| anyhow::anyhow!("启动 {} 失败: {}", interpreter, e))?;

    let wait_secs = if timeout_secs == 0 {
        SCRIPT_DEFAULT_TIMEOUT_SECS
    } else {
        timeout_secs as u64
    };
    let out = match time::timeout(Duration::from_secs(wait_secs), child.wait_with_output()).await {
        Ok(Ok(o)) => o,
        Ok(Err(e)) => anyhow::bail!("等待进程结束失败: {}", e),
        Err(_) => anyhow::bail!("执行超时（{} 秒），已终止子进程", wait_secs),
    };

    let exit_code = out.status.code().unwrap_or(-1);
    let mut output = decode_console_output(&out.stdout);
    let stderr = decode_console_output(&out.stderr);
    if !stderr.trim().is_empty() {
        if !output.is_empty() && !output.ends_with('\n') {
            output.push('\n');
        }
        output.push_str(stderr.trim_end());
        output.push('\n');
    }

    let mut truncated = false;
    if output.len() > SCRIPT_MAX_OUTPUT {
        // 按 UTF-8 字符边界截断，避免切出非法字符串
        let mut end = SCRIPT_MAX_OUTPUT;
        while end > 0 && !output.is_char_boundary(end) {
            end -= 1;
        }
        output.truncate(end);
        output.push_str("\n…（输出已截断，仅保留前 64KB）");
        truncated = true;
    }

    tracing::warn!(
        "[响应] 命令执行完成: exit_code={} 输出 {} 字节{}",
        exit_code,
        output.len(),
        if truncated { "（已截断）" } else { "" }
    );

    Ok(ScriptResult { output, exit_code })
}

// ── 文件传输（流式）──────────────────────────────────

/// 传输分块大小（512KB）
const TRANSFER_CHUNK_SIZE: usize = 512 * 1024;

/// 上报指令执行结果（传输任务用）
async fn report_transfer_result(
    client: &mut BaizeServiceClient<tonic::transport::Channel>,
    command_id: &str,
    success: bool,
    error_message: String,
) {
    let msg = pb::CommandResult {
        command_id: command_id.to_string(),
        success,
        error_message,
        output: String::new(),
        completed_at_ns: chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64,
        data: Vec::new(),
    };
    if let Err(e) = client.report_command_result(msg).await {
        error!("[传输] 结果上报失败: {:?}", e);
    }
}

/// 后台下载：读取文件 → 建 FileTransferStream 推送分块（A→S，真流式，内存恒定）
async fn run_download(
    mut client: BaizeServiceClient<tonic::transport::Channel>,
    agent_id: String,
    transfer_id: &str,
    file_path: &str,
) -> Result<()> {
    use tokio::io::AsyncReadExt;

    let tid = transfer_id.to_string();
    let path_owned = file_path.to_string();
    let stream = async_stream::stream! {
        let mut file = match tokio::fs::File::open(&path_owned).await {
            Ok(f) => f,
            Err(e) => {
                yield pb::FileChunk {
                    transfer_id: tid.clone(),
                    seq: 0,
                    data: Vec::new(),
                    last: true,
                    error: format!("打开文件失败: {}", e),
                };
                return;
            }
        };
        let mut buf = vec![0u8; TRANSFER_CHUNK_SIZE];
        let mut seq: u64 = 0;
        loop {
            match file.read(&mut buf).await {
                Ok(0) => {
                    yield pb::FileChunk {
                        transfer_id: tid.clone(),
                        seq,
                        data: Vec::new(),
                        last: true,
                        error: String::new(),
                    };
                    break;
                }
                Ok(n) => {
                    yield pb::FileChunk {
                        transfer_id: tid.clone(),
                        seq,
                        data: buf[..n].to_vec(),
                        last: false,
                        error: String::new(),
                    };
                    seq += 1;
                }
                Err(e) => {
                    yield pb::FileChunk {
                        transfer_id: tid.clone(),
                        seq,
                        data: Vec::new(),
                        last: true,
                        error: format!("读取文件失败: {}", e),
                    };
                    break;
                }
            }
        }
    };

    let mut req = tonic::Request::new(stream);
    req.metadata_mut().insert(
        "x-agent-id",
        tonic::metadata::MetadataValue::from_str(&agent_id)
            .map_err(|e| anyhow::anyhow!("非法 agent_id: {}", e))?,
    );
    req.metadata_mut().insert(
        "x-transfer-id",
        tonic::metadata::MetadataValue::from_str(transfer_id)
            .map_err(|e| anyhow::anyhow!("非法 transfer_id: {}", e))?,
    );
    req.metadata_mut()
        .insert("x-direction", tonic::metadata::MetadataValue::from_static("download"));

    let resp = client
        .file_transfer_stream(req)
        .await
        .map_err(|e| anyhow::anyhow!("建立传输流失败: {}", e))?;
    info!("[传输] 下载流已建立 task={} file={}", transfer_id, file_path);

    // 消费 Server → Agent 方向（通常为空；Server 关闭流即结束）
    let mut inbound = resp.into_inner();
    while let Some(_chunk) = inbound
        .message()
        .await
        .map_err(|e| anyhow::anyhow!("传输流错误: {}", e))?
    {}

    info!("[传输] 下载完成 task={} file={}", transfer_id, file_path);
    Ok(())
}

/// 后台上传：建 FileTransferStream 接收分块 → 临时文件 → 原子重命名落盘（S→A）
async fn run_upload(
    mut client: BaizeServiceClient<tonic::transport::Channel>,
    agent_id: String,
    transfer_id: &str,
    dest_path: &str,
    overwrite: bool,
    command_id: &str,
) -> Result<()> {
    use tokio::io::AsyncWriteExt;

    // 目标已存在且未允许覆盖 → 直接失败
    if !overwrite && tokio::fs::metadata(dest_path).await.is_ok() {
        let msg = format!("目标已存在: {}（未允许覆盖）", dest_path);
        report_transfer_result(&mut client, command_id, false, msg.clone()).await;
        anyhow::bail!(msg);
    }

    // 自动创建父目录
    if let Some(parent) = std::path::Path::new(dest_path).parent() {
        if !parent.as_os_str().is_empty() {
            tokio::fs::create_dir_all(parent)
                .await
                .map_err(|e| anyhow::anyhow!("创建目录 {} 失败: {}", parent.display(), e))?;
        }
    }

    // 建流（本端发送方向为空：只接收 Server 推来的分块）
    let empty = futures::stream::empty::<pb::FileChunk>();
    let mut req = tonic::Request::new(empty);
    req.metadata_mut().insert(
        "x-agent-id",
        tonic::metadata::MetadataValue::from_str(&agent_id)
            .map_err(|e| anyhow::anyhow!("非法 agent_id: {}", e))?,
    );
    req.metadata_mut().insert(
        "x-transfer-id",
        tonic::metadata::MetadataValue::from_str(transfer_id)
            .map_err(|e| anyhow::anyhow!("非法 transfer_id: {}", e))?,
    );
    req.metadata_mut()
        .insert("x-direction", tonic::metadata::MetadataValue::from_static("upload"));

    let resp = client
        .file_transfer_stream(req)
        .await
        .map_err(|e| anyhow::anyhow!("建立传输流失败: {}", e))?;
    info!("[传输] 上传流已建立 task={} dest={}", transfer_id, dest_path);

    let tmp_path = format!("{}.baize-upload-{}", dest_path, transfer_id);
    let mut out = tokio::fs::File::create(&tmp_path)
        .await
        .map_err(|e| anyhow::anyhow!("创建临时文件 {} 失败: {}", tmp_path, e))?;

    let mut inbound = resp.into_inner();
    let mut total: u64 = 0;
    loop {
        let msg = inbound.message().await;
        match msg {
            Ok(Some(chunk)) => {
                if !chunk.error.is_empty() {
                    drop(out);
                    let _ = tokio::fs::remove_file(&tmp_path).await;
                    let m = format!("传输失败: {}", chunk.error);
                    report_transfer_result(&mut client, command_id, false, m.clone()).await;
                    anyhow::bail!(m);
                }
                if !chunk.data.is_empty() {
                    out.write_all(&chunk.data)
                        .await
                        .map_err(|e| anyhow::anyhow!("写入临时文件失败: {}", e))?;
                    total += chunk.data.len() as u64;
                }
                if chunk.last {
                    break;
                }
            }
            Ok(None) => {
                drop(out);
                let _ = tokio::fs::remove_file(&tmp_path).await;
                let m = "传输中断：Server 提前关闭流".to_string();
                report_transfer_result(&mut client, command_id, false, m.clone()).await;
                anyhow::bail!(m);
            }
            Err(e) => {
                drop(out);
                let _ = tokio::fs::remove_file(&tmp_path).await;
                let m = format!("传输流错误: {}", e);
                report_transfer_result(&mut client, command_id, false, m.clone()).await;
                anyhow::bail!(m);
            }
        }
    }

    out.flush()
        .await
        .map_err(|e| anyhow::anyhow!("刷新临时文件失败: {}", e))?;
    drop(out);
    tokio::fs::rename(&tmp_path, dest_path)
        .await
        .map_err(|e| anyhow::anyhow!("重命名落盘失败: {}", e))?;

    info!(
        "[传输] 上传完成 task={} dest={} bytes={}",
        transfer_id, dest_path, total
    );
    report_transfer_result(&mut client, command_id, true, String::new()).await;
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
