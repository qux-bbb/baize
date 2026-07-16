// 进程快照（不产生事件）
// 进程事件由 ETW + EventLog 4688 实时采集
use anyhow::Result;
use std::sync::Arc;
use tokio::sync::mpsc;

use crate::pb;

pub async fn start(
    _system: Arc<tokio::sync::Mutex<sysinfo::System>>,
    _tx: mpsc::Sender<pb::Event>,
) -> Result<()> {
    Ok(())
}
