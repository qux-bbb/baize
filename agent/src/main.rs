// Baize (白泽) EDR Agent — Rust 版本
mod collector;

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
    #[arg(long, default_value = "3")]
    interval: u64,
    #[arg(long)]
    hostname: Option<String>,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter("baize_agent=info")
        .init();

    let cli = Cli::parse();

    let sys = Arc::new(tokio::sync::Mutex::new(System::new_all()));

    let hostname = cli.hostname.clone().unwrap_or_else(|| {
        sysinfo::System::host_name().unwrap_or_else(|| "unknown".into())
    });

    let agent_id = cli.agent_id.clone().unwrap_or_else(|| {
        uuid::Uuid::new_v4().to_string()
    });

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
        match run(&cli.server, agent_info.clone(), &sys, cli.interval).await {
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

async fn run(
    server: &str,
    agent_info: AgentInfo,
    system: &Arc<tokio::sync::Mutex<System>>,
    interval_secs: u64,
) -> Result<()> {
    let endpoint = Endpoint::from_shared(server.to_string())
        .context("无效的 Server 地址")?;
    let channel = endpoint.connect().await.context("连接 Server 失败")?;
    let mut client = BaizeServiceClient::new(channel);
    info!("已连接到 Server: {}", server);

    let (tx, mut rx) = mpsc::channel::<Event>(1024);

    // 启动进程采集器
    let collect_tx = tx.clone();
    let collect_sys = Arc::clone(system);
    tokio::spawn(async move {
        if let Err(e) = collector::process::start(
            collect_sys,
            collect_tx,
            Duration::from_secs(interval_secs),
        )
        .await
        {
            error!("进程采集器错误: {:?}", e);
        }
    });

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
    let response = client
        .agent_stream(Request::new(streaming_request))
        .await
        .context("AgentStream RPC 失败")?;

    let mut incoming = response.into_inner();
    info!("双向流已建立，等待 Server 指令...");

    while let Some(cmd) = incoming.message().await? {
        info!("收到指令: {:?}", cmd);
    }

    info!("Server 流已关闭");
    Ok(())
}
