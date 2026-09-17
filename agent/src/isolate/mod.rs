//! 主机网络隔离（Network Containment）
//!
//! 设计要点：
//! - 不修改系统防火墙默认策略，而是创建 Baize 专属的 WFP 子层并在
//!   ALE_AUTH_CONNECT 层挂过滤器：默认全断 + 管理通道白名单。
//!   好处：不碰用户/域组的防火墙配置（GPO 刷新覆盖不到），解除即删自己的过滤器。
//! - 白名单 = Agent 自身进程 → Server 端口、DNS(53)、loopback。
//!   管理通道被放行，因此隔离后 Agent 断线重连仍能成功（不会把自己彻底掐死）。
//! - 子层与过滤器使用固定 GUID：天然幂等（重复隔离/解除不会残留）。
//! - WFP 会话不设 `FWPM_SESSION_FLAG_DYNAMIC`：Agent 崩溃/被杀后隔离依然有效；
//!   解除由 Dashboard 下发 / `--isolate-off` / TTL 到期 / 卸载流程负责。
//! - 隔离状态落盘 isolate.json：系统重启后 WFP 过滤器不保留，Agent 启动时据此重新施加。
//! - Agent 是隔离状态的权威源，心跳携带 isolated 字段上报，Server 侧状态据此自愈。

#[cfg(windows)]
mod wfp;

use anyhow::Result;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

/// Server 默认 gRPC 端口（agent.conf 未显式指定端口时使用）
pub const DEFAULT_SERVER_PORT: u16 = 50051;

/// 隔离状态文件（exe 同目录，与 client.key / authd.pass 同级）
#[derive(Serialize, Deserialize, Default, Debug, Clone)]
#[serde(default)]
pub struct IsolateState {
    /// 是否处于隔离状态
    pub isolated: bool,
    /// 施加隔离的时刻（Unix 纳秒）
    pub since_ns: u64,
    /// 自动解除倒计时（秒），0 = 不自动解除
    pub ttl_seconds: u32,
    /// 下发原因（审计留痕）
    pub reason: String,
    /// 管理通道白名单端口（Server gRPC 端口）
    pub server_port: u16,
}

fn state_path() -> PathBuf {
    std::env::current_exe()
        .ok()
        .and_then(|exe| exe.parent().map(|d| d.join("isolate.json")))
        .unwrap_or_else(|| PathBuf::from("isolate.json"))
}

pub fn load_state() -> IsolateState {
    match std::fs::read_to_string(state_path()) {
        Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
        Err(_) => IsolateState::default(),
    }
}

fn save_state(state: &IsolateState) -> Result<()> {
    let path = state_path();
    std::fs::write(&path, serde_json::to_string_pretty(state)?)?;
    tracing::info!("[隔离] 状态已落盘: {}", path.display());
    Ok(())
}

/// 清除本地隔离状态记录（解除隔离后调用）
pub fn clear_state() {
    let path = state_path();
    if path.exists() {
        let _ = std::fs::remove_file(&path);
    }
}

fn now_ns() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0)
}

/// 当前是否真的处于隔离状态（以 WFP 实际状态为准，不是读状态文件）
pub fn is_active() -> bool {
    #[cfg(windows)]
    {
        wfp::is_active()
    }
    #[cfg(not(windows))]
    {
        false
    }
}

/// 施加网络隔离。
/// `server_port` = 管理通道白名单端口；`ttl_seconds` = 0 表示不自动解除。
pub fn apply(server_port: u16, ttl_seconds: u32, reason: &str) -> Result<()> {
    #[cfg(windows)]
    {
        let exe = std::env::current_exe()?;
        let exe_path = exe.to_string_lossy().to_string();
        let port = if server_port == 0 {
            DEFAULT_SERVER_PORT
        } else {
            server_port
        };
        wfp::apply(port, &exe_path)?;
        save_state(&IsolateState {
            isolated: true,
            since_ns: now_ns(),
            ttl_seconds,
            reason: reason.to_string(),
            server_port: port,
        })?;
        tracing::warn!(
            "[隔离] 主机已隔离：仅放行 Agent→Server(端口 {}）、DNS(53)、loopback，其余出站全部阻断；TTL={}",
            port,
            if ttl_seconds == 0 { "不自动解除".to_string() } else { format!("{}秒", ttl_seconds) }
        );
        Ok(())
    }
    #[cfg(not(windows))]
    {
        let _ = (server_port, ttl_seconds, reason);
        anyhow::bail!("网络隔离仅支持 Windows（当前平台未实现）")
    }
}

/// 解除网络隔离
pub fn release() -> Result<()> {
    #[cfg(windows)]
    {
        wfp::release()?;
        clear_state();
        tracing::warn!("[隔离] 主机隔离已解除");
        Ok(())
    }
    #[cfg(not(windows))]
    {
        anyhow::bail!("网络隔离仅支持 Windows（当前平台未实现）")
    }
}

/// 依据状态文件判断 TTL 是否到期（到期应自动解除）
pub fn ttl_expired(state: &IsolateState) -> bool {
    if !state.isolated || state.ttl_seconds == 0 {
        return false;
    }
    let deadline = state
        .since_ns
        .saturating_add(state.ttl_seconds as u64 * 1_000_000_000);
    now_ns() >= deadline
}

/// 启动时对账：状态文件说"隔离中"则重新施加（系统重启后 WFP 过滤器不保留）。
/// 返回是否重新施加了隔离。
pub fn reapply_if_needed() -> bool {
    let state = load_state();
    if !state.isolated {
        return false;
    }
    if ttl_expired(&state) {
        tracing::warn!("[隔离] 本地状态为隔离中，但 TTL 已到期，自动解除");
        let _ = release();
        return false;
    }
    tracing::warn!("[隔离] 检测到本地隔离状态，重新施加 WFP 过滤器");
    if let Err(e) = apply(
        if state.server_port == 0 {
            DEFAULT_SERVER_PORT
        } else {
            state.server_port
        },
        state.ttl_seconds,
        &state.reason,
    ) {
        tracing::error!("[隔离] 重新施加隔离失败: {:?}", e);
        return false;
    }
    true
}
