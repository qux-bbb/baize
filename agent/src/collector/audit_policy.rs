// 审计策略引用计数管理器 (Windows only)
// ──────────────────────────────────────────────────────────────────
// 多个采集器可能共享同一个 Windows 审计策略（auditpol subcategory /
// wevtutil 通道）。直接 enable/disable 会导致：A 功能关闭时把共享
// 策略禁用，连累 B 功能。用引用计数解决：
//
//   acquire(策略): refcount 0→1 时执行 enable
//   release(策略): refcount 1→0 时才执行 disable
//
// 例：4688(进程创建) 和 4689(进程终止) 共用一个 Process Creation
// 策略，关闭 4689 时 refcount 2→1，策略保持启用，4688 不受影响。
use std::collections::HashMap;
use std::process::Command;
use std::sync::Mutex;

/// Windows 审计策略
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub enum Policy {
    /// 进程创建审计 (EventID 4688/4689)
    ProcessCreation,
    /// 网络连接审计 (EventID 5156/5157)
    FilteringPlatformConnection,
    /// DNS Client 日志通道 (EventID 3008)
    DnsOperationalChannel,
}

impl Policy {
    pub fn name(&self) -> &'static str {
        match self {
            Policy::ProcessCreation => "ProcessCreation",
            Policy::FilteringPlatformConnection => "FilteringPlatformConnection",
            Policy::DnsOperationalChannel => "DnsOperationalChannel",
        }
    }

    /// 执行启用命令，返回是否成功
    fn enable(&self) -> bool {
        match self {
            Policy::ProcessCreation => {
                // 4688 默认不记录 CommandLine，须开注册表开关才记录命令行使
                let ok = run_auditpol("{0CCE922B-69AE-11D9-BED3-505054503030}", true);
                enable_cmdline_registry();
                ok
            }
            Policy::FilteringPlatformConnection => {
                run_auditpol("{0CCE9226-69AE-11D9-BED3-505054503030}", true)
            }
            Policy::DnsOperationalChannel => run_wevtutil(true),
        }
    }

    /// 执行禁用命令，返回是否成功
    fn disable(&self) -> bool {
        match self {
            Policy::ProcessCreation => run_auditpol("{0CCE922B-69AE-11D9-BED3-505054503030}", false),
            Policy::FilteringPlatformConnection => {
                run_auditpol("{0CCE9226-69AE-11D9-BED3-505054503030}", false)
            }
            Policy::DnsOperationalChannel => run_wevtutil(false),
        }
    }
}

fn run_auditpol(guid: &str, enable: bool) -> bool {
    let action = if enable { "enable" } else { "disable" };
    let out = Command::new("auditpol")
        .args([
            "/set",
            &format!("/subcategory:{}", guid),
            &format!("/success:{}", action),
        ])
        .output();
    match out {
        Ok(o) if o.status.success() => true,
        Ok(o) => {
            tracing::warn!(
                "[AuditPolicy] auditpol {} 失败: {}",
                action,
                String::from_utf8_lossy(&o.stderr).trim()
            );
            false
        }
        Err(e) => {
            tracing::warn!("[AuditPolicy] auditpol 执行失败: {}", e);
            false
        }
    }
}

fn enable_cmdline_registry() {
    // 4688 默认不记录 CommandLine，需 HKLM...\Audit\ProcessCreationIncludeCmdLine_Enabled=1 才记录。
    // 仅在启用侧调用；disable 不删除（保守：不干预系统已有审计配置）。
    let out = Command::new("reg")
        .args([
            "add",
            r"HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System\Audit",
            "/v",
            "ProcessCreationIncludeCmdLine_Enabled",
            "/t",
            "REG_DWORD",
            "/d",
            "1",
            "/f",
        ])
        .output();
    match out {
        Ok(o) if o.status.success() => tracing::info!("[AuditPolicy] 4688 命令行开关已开启"),
        Ok(o) => tracing::warn!(
            "[AuditPolicy] 开启 4688 命令行开关失败: {}",
            String::from_utf8_lossy(&o.stderr).trim()
        ),
        Err(e) => tracing::warn!("[AuditPolicy] reg 执行失败: {}", e),
    }
}

fn run_wevtutil(enable: bool) -> bool {
    let value = if enable { "true" } else { "false" };
    let out = Command::new("wevtutil")
        .args([
            "sl",
            "Microsoft-Windows-DNS-Client/Operational",
            &format!("/e:{}", value),
        ])
        .output();
    match out {
        Ok(o) if o.status.success() => true,
        Ok(o) => {
            // 已处于目标状态时 wevtutil 也可能报错，仅记录
            tracing::info!(
                "[AuditPolicy] wevtutil /e:{} 输出: {}",
                value,
                String::from_utf8_lossy(&o.stderr).trim()
            );
            false
        }
        Err(e) => {
            tracing::warn!("[AuditPolicy] wevtutil 执行失败: {}", e);
            false
        }
    }
}

/// 策略引用计数管理器（线程安全）
pub struct AuditPolicyManager {
    refcounts: Mutex<HashMap<Policy, usize>>,
}

impl AuditPolicyManager {
    pub fn new() -> Self {
        Self {
            refcounts: Mutex::new(HashMap::new()),
        }
    }

    /// 获取策略引用。计数 0→1 时执行 enable
    pub fn acquire(&self, policy: Policy) {
        let mut map = self.refcounts.lock().unwrap();
        let count = map.entry(policy).or_insert(0);
        if *count == 0 {
            if policy.enable() {
                tracing::info!("[AuditPolicy] {} 已启用", policy.name());
            }
        }
        *count += 1;
        tracing::debug!("[AuditPolicy] {} refcount={}", policy.name(), *count);
    }

    /// 释放策略引用。计数 1→0 时才执行 disable
    pub fn release(&self, policy: Policy) {
        let mut map = self.refcounts.lock().unwrap();
        let count = map.entry(policy).or_insert(0);
        if *count == 0 {
            tracing::warn!("[AuditPolicy] {} 释放时引用计数已为 0，忽略", policy.name());
            return;
        }
        *count -= 1;
        if *count == 0 {
            if policy.disable() {
                tracing::info!("[AuditPolicy] {} 已禁用", policy.name());
            }
        }
        tracing::debug!("[AuditPolicy] {} refcount={}", policy.name(), *count);
    }
}

impl Default for AuditPolicyManager {
    fn default() -> Self {
        Self::new()
    }
}
