// Baize (白泽) EDR Agent — Rust 版本
mod collector;

#[cfg(windows)]
mod service;

pub mod pb {
    tonic::include_proto!("baize.v1");
}

use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result};
use clap::Parser;
use sysinfo::System;
use tokio::sync::mpsc;
use tokio::time;
use tonic::transport::Endpoint;
use tonic::Request;
use tracing::{error, info};

use pb::baize_service_client::BaizeServiceClient;
use pb::{AgentInfo, Event};

#[derive(Parser)]
#[command(name = "baize-agent", about = "Baize EDR Agent")]
struct Cli {
    #[arg(long, default_value = "http://127.0.0.1:50051")]
    server: String,
    #[arg(long)]
    agent_id: Option<String>,
    /// 进程采集间隔（秒）
    #[arg(long, default_value = "30")]
    interval: u64,
    /// 文件监控目录（逗号分隔）
    #[arg(long, default_value = "")]
    watch: String,
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

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter("baize_agent=info")
        .init();

    let cli = Cli::parse();

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
    let sys = Arc::new(tokio::sync::Mutex::new(System::new_all()));

    let hostname = cli.hostname.clone().unwrap_or_else(|| {
        sysinfo::System::host_name().unwrap_or_else(|| "unknown".into())
    });

    // Agent ID：优先用命令行指定的，否则从文件读取/自动生成并持久化
    let agent_id = if let Some(id) = cli.agent_id.clone() {
        id
    } else {
        load_or_create_agent_id()
    };

    info!("Agent {} ({}) 启动中...", agent_id, hostname);

    let agent_info = AgentInfo {
        agent_id,
        hostname,
        os_type: std::env::consts::OS.to_string(),
        os_version: std::env::consts::ARCH.to_string(),
        kernel_version: String::new(),
        agent_version: env!("CARGO_PKG_VERSION").to_string(),
        ip_addresses: vec![],
        boot_time_ns: 0,
        arch: std::env::consts::ARCH.to_string(),
    };

    // 断线重连循环
    loop {
        match run(&cli.server, agent_info.clone(), &sys, cli.interval, &cli.watch).await {
            Ok(()) => {
                info!("连接正常结束，5 秒后重连...");
                time::sleep(Duration::from_secs(5)).await;
            }
            Err(e) => {
                error!("连接错误: {:?}，15 秒后重试...", e);
                time::sleep(Duration::from_secs(15)).await;
            }
        }
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

async fn run(
    server: &str,
    agent_info: AgentInfo,
    system: &Arc<tokio::sync::Mutex<System>>,
    interval_secs: u64,
    watch: &str,
) -> Result<()> {
    let endpoint = Endpoint::from_shared(server.to_string())
        .context("无效的 Server 地址")?;
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

    // 进程事件由 EvtSubscribe 实时采集（见 wmi_process.rs），旧 EventLog 轮询已禁用
    #[cfg(target_os = "windows")]
    {
        let wmi_tx = tx.clone();
        std::thread::spawn(move || {
            if let Err(e) = collector::wmi_process::start_evtsub(wmi_tx) {
                tracing::error!("EventLog 监控错误: {:?}", e);
            }
        });
    }

    // 文件监控由 Server 通过 ConfigureFileWatchCommand 指令控制
    // Agent 不再默认启动文件监控，等待服务端下配置

    // 转发线程：rx → AgentInfo + seq → gRPC 流
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

    let response = client
        .agent_stream(Request::new(streaming_request))
        .await
        .context("AgentStream RPC 失败")?;

    info!("双向流已建立，等待 Server 指令...");

    let mut incoming = response.into_inner();

    while let Some(cmd) = incoming.message().await? {
        info!("收到指令: {:?}", cmd);

        // 执行指令
        if let Some(cmd_type) = &cmd.command_type {
            use pb::command::CommandType;
            let result = match cmd_type {
                CommandType::Isolate(isolate_cmd) => {
                    execute_isolate(isolate_cmd.isolate)
                }
                CommandType::KillProcess(kill_cmd) => {
                    execute_kill_process(kill_cmd.pid)
                }
                CommandType::DeleteFile(del_cmd) => {
                    execute_delete_file(&del_cmd.file_path, del_cmd.force)
                }
                CommandType::ExecuteScript(script_cmd) => {
                    execute_script(&script_cmd.script_content, &script_cmd.interpreter)
                }
                CommandType::ConfigureFileWatch(fw_cmd) => {
                    info!("[配置] 文件监控目录: {:?}", fw_cmd.watch_dirs);
                    let dirs: Vec<String> = fw_cmd.watch_dirs.clone();
                    if !dirs.is_empty() {
                        let file_tx = tx.clone();
                        tokio::spawn(async move {
                            if let Err(e) = collector::file::start(dirs, file_tx).await {
                                error!("文件监控错误: {:?}", e);
                            }
                        });
                    }
                    Ok(())
                }
            };

            let success = result.is_ok();
            let error_msg = result.err().map(|e| e.to_string()).unwrap_or_default();
            info!("指令 {} 执行结果: success={} error={}", cmd.command_id, success, error_msg);

            // 上报执行结果
            let result_msg = pb::CommandResult {
                command_id: cmd.command_id.clone(),
                success,
                error_message: error_msg,
                output: String::new(),
                completed_at_ns: chrono::Utc::now().timestamp_nanos_opt().unwrap_or(0) as u64,
            };
            let _ = client.report_command_result(result_msg).await;
        }
    }

    info!("Server 流已关闭");
    Ok(())
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

fn load_or_create_agent_id() -> String {
    let path = agent_id_path();

    // 尝试读取已有的 agent_id
    if let Ok(id) = std::fs::read_to_string(&path) {
        let id = id.trim().to_string();
        if !id.is_empty() {
            tracing::info!("读取已保存的 Agent ID: {}", id);
            return id;
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
    new_id
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
