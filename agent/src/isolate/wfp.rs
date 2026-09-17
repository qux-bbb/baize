//! Windows Filtering Platform 隔离实现
//!
//! 在 ALE_AUTH_CONNECT 层（出站连接发起时授权）建立 Baize 专属子层：
//!   permit: Agent 进程 → Server 端口  (管理通道，隔离后可重连)
//!   permit: 远端端口 53               (DNS，Server 用域名时重连必需)
//!   permit: loopback                  (本机 IPC，不打断)
//!   block : 无条件                    (兜底，其余出站全部阻断)
//! V4 / V6 各一套。子层权重取最大，permit 权重高于 block 权重。
//!
//! 会话不设 FWPM_SESSION_FLAG_DYNAMIC → 过滤器常驻，Agent 进程退出后隔离仍生效。

use anyhow::{bail, Result};
use std::ffi::c_void;

use windows::core::{GUID, PCWSTR, PWSTR};
use windows::Win32::Foundation::{
    FWP_E_ACTION_INCOMPATIBLE_WITH_LAYER, FWP_E_ACTION_INCOMPATIBLE_WITH_SUBLAYER,
    FWP_E_ALREADY_EXISTS, FWP_E_DUPLICATE_CONDITION, FWP_E_FILTER_NOT_FOUND,
    FWP_E_INCOMPATIBLE_LAYER, FWP_E_INVALID_ACTION_TYPE, FWP_E_INVALID_PARAMETER,
    FWP_E_INVALID_WEIGHT, FWP_E_MATCH_TYPE_MISMATCH, FWP_E_NOT_FOUND, FWP_E_NULL_DISPLAY_NAME,
    FWP_E_SUBLAYER_NOT_FOUND, FWP_E_TYPE_MISMATCH, HANDLE,
};
use windows::Win32::NetworkManagement::WindowsFilteringPlatform::*;
use windows::Win32::Security::PSECURITY_DESCRIPTOR;
use windows::Win32::System::Rpc::RPC_C_AUTHN_WINNT;

const ERROR_SUCCESS: u32 = 0;
const ERROR_NOT_FOUND: u32 = 1168;
/// WFP 错误码统一前缀（HRESULT 形态，0x8032xxxx）
const FWP_E_PREFIX: u32 = 0x8032_0000;

fn wfp_err(code: u32) -> String {
    match code {
        5 => format!("错误码 {}（拒绝访问：需要管理员/SYSTEM 权限）", code),
        87 => format!("错误码 {}（参数无效）", code),
        c if c == ERROR_NOT_FOUND => format!("错误码 {}（对象不存在）", c),
        c if c == FWP_E_FILTER_NOT_FOUND.0 as u32 => format!("0x{:08X}（过滤器不存在）", c),
        c if c == FWP_E_SUBLAYER_NOT_FOUND.0 as u32 => format!("0x{:08X}（隔离子层不存在）", c),
        c if c == FWP_E_NOT_FOUND.0 as u32 => format!("0x{:08X}（对象不存在）", c),
        c if c == FWP_E_ALREADY_EXISTS.0 as u32 => format!("0x{:08X}（对象已存在）", c),
        c if c == FWP_E_INVALID_WEIGHT.0 as u32 => {
            format!("0x{:08X}（过滤器权重非法：只接受 FWP_UINT64 / FWP_EMPTY / FWP_UINT8[0,15]）", c)
        }
        c if c == FWP_E_INVALID_ACTION_TYPE.0 as u32 => format!("0x{:08X}（动作类型非法）", c),
        c if c == FWP_E_TYPE_MISMATCH.0 as u32 => format!("0x{:08X}（条件值类型不匹配）", c),
        c if c == FWP_E_MATCH_TYPE_MISMATCH.0 as u32 => format!("0x{:08X}（匹配类型与操作数不兼容）", c),
        c if c == FWP_E_DUPLICATE_CONDITION.0 as u32 => {
            format!("0x{:08X}（同一字段出现了多条条件：应合并为一条）", c)
        }
        c if c == FWP_E_NULL_DISPLAY_NAME.0 as u32 => format!("0x{:08X}（displayData.name 为空）", c),
        c if c == FWP_E_INCOMPATIBLE_LAYER.0 as u32 => format!("0x{:08X}（该层不支持此操作）", c),
        c if c == FWP_E_ACTION_INCOMPATIBLE_WITH_LAYER.0 as u32 => {
            format!("0x{:08X}（动作类型与该层不兼容）", c)
        }
        c if c == FWP_E_ACTION_INCOMPATIBLE_WITH_SUBLAYER.0 as u32 => {
            format!("0x{:08X}（动作类型与该子层不兼容）", c)
        }
        c if c == FWP_E_INVALID_PARAMETER.0 as u32 => format!("0x{:08X}（参数不正确）", c),
        c if c & 0xFFFF_0000 == FWP_E_PREFIX => format!("0x{:08X}（WFP 错误）", c),
        c => format!("错误码 {}", c),
    }
}

/// "对象不存在"类错误：删除时不视为失败（幂等前提）
fn is_not_found(rc: u32) -> bool {
    rc == ERROR_NOT_FOUND
        || rc == FWP_E_FILTER_NOT_FOUND.0 as u32
        || rc == FWP_E_SUBLAYER_NOT_FOUND.0 as u32
        || rc == FWP_E_NOT_FOUND.0 as u32
}

// ── 固定 GUID（幂等基础：重复隔离/解除不会残留多余对象）────────────
const SUBLAYER_KEY: GUID = GUID::from_u128(0x8b2d7e6e_0637_4c0a_a5e7_6b2a23b99eba);
const F_V4_APP: GUID = GUID::from_u128(0x1f00ab2f_f776_4648_bf40_e35329a50881);
const F_V4_DNS: GUID = GUID::from_u128(0x62d0e8a1_5380_4509_9926_0dc9f439eef3);
const F_V4_LOOPBACK: GUID = GUID::from_u128(0xe8bcf02c_9b1e_4683_aefe_3160df9f9f33);
const F_V4_BLOCK: GUID = GUID::from_u128(0x0fdf7e0d_e4e4_4855_98aa_d278b71f3058);
const F_V6_APP: GUID = GUID::from_u128(0xa27ce81e_e73f_46d4_b778_9325c253f19f);
const F_V6_DNS: GUID = GUID::from_u128(0x38e8d7c1_a6e8_4b2d_84c0_36df1ea34b9f);
const F_V6_LOOPBACK: GUID = GUID::from_u128(0xd314a5ba_3340_40bb_8c51_0e10fee83473);
const F_V6_BLOCK: GUID = GUID::from_u128(0x6492a844_48e8_4e82_b15a_6f6704879faf);

const ALL_FILTER_KEYS: [GUID; 8] = [
    F_V4_APP,
    F_V4_DNS,
    F_V4_LOOPBACK,
    F_V4_BLOCK,
    F_V6_APP,
    F_V6_DNS,
    F_V6_LOOPBACK,
    F_V6_BLOCK,
];

/// 子层权重（FWPM_SUBLAYER0.weight 是 UINT16 字段，越大越先评估）：取最大，压过系统防火墙子层
const SUBLAYER_WEIGHT: u16 = 0xFFFF;
/// 过滤器权重（跨区间）：WFP 只接受 FWP_UINT64 / FWP_EMPTY / FWP_UINT8[0,15] 三种形式，
/// FWP_UINT16 会返回 FWP_E_INVALID_WEIGHT(0x80320025)。这里用 FWP_UINT8 区间标识：
/// 15 = 最高区间（放行），1 = 兜底区间（阻断），确保 permit 先于 block 评估。
const PERMIT_WEIGHT: u8 = 15;
/// 兜底阻断过滤器权重（低于放行）
const BLOCK_WEIGHT: u8 = 1;

/// DNS 端口（隔离期间保留域名解析，Server 用域名时重连必需）
const DNS_PORT: u16 = 53;

// ── RAII 包装 ──────────────────────────────────────────────

/// WFP 引擎会话句柄
struct Engine(HANDLE);

impl Engine {
    fn open() -> Result<Self> {
        // flags = 0（不设 FWPM_SESSION_FLAG_DYNAMIC）：过滤器随会话结束仍保留，
        // Agent 崩溃/被杀后隔离依然生效。
        let session = FWPM_SESSION0 {
            flags: 0,
            txnWaitTimeoutInMSec: 0,
            ..Default::default()
        };
        let mut handle = HANDLE::default();
        let rc = unsafe {
            FwpmEngineOpen0(
                PCWSTR::null(),
                RPC_C_AUTHN_WINNT,
                None,
                Some(&session),
                &mut handle,
            )
        };
        if rc != ERROR_SUCCESS {
            bail!("打开 WFP 引擎失败: {}", wfp_err(rc));
        }
        Ok(Engine(handle))
    }
}

impl Drop for Engine {
    fn drop(&mut self) {
        unsafe {
            FwpmEngineClose0(self.0);
        }
    }
}

/// FwpmGetAppIdFromFileName0 返回的 blob（需要 FwpmFreeMemory0 释放）
struct AppId(*mut FWP_BYTE_BLOB);

impl AppId {
    fn from_exe(path: &str) -> Result<Self> {
        let wide: Vec<u16> = path.encode_utf16().chain(std::iter::once(0)).collect();
        let mut blob: *mut FWP_BYTE_BLOB = std::ptr::null_mut();
        let rc = unsafe { FwpmGetAppIdFromFileName0(PCWSTR(wide.as_ptr()), &mut blob) };
        if rc != ERROR_SUCCESS {
            bail!("解析 Agent 程序路径失败（WFP app id）: {}", wfp_err(rc));
        }
        if blob.is_null() {
            bail!("解析 Agent 程序路径失败（WFP app id 为空）");
        }
        Ok(AppId(blob))
    }
}

impl Drop for AppId {
    fn drop(&mut self) {
        let mut p = self.0 as *mut c_void;
        unsafe {
            FwpmFreeMemory0(&mut p);
        }
    }
}

// ── 结构构造辅助 ────────────────────────────────────────────

fn weight_value(w: u8) -> FWP_VALUE0 {
    FWP_VALUE0 {
        r#type: FWP_UINT8,
        Anonymous: FWP_VALUE0_0 { uint8: w },
    }
}

fn action_value(action: FWP_ACTION_TYPE) -> FWPM_ACTION0 {
    FWPM_ACTION0 {
        r#type: action,
        Anonymous: FWPM_ACTION0_0 {
            filterType: GUID::from_u128(0),
        },
    }
}

fn cond_port(port: u16) -> FWP_CONDITION_VALUE0 {
    FWP_CONDITION_VALUE0 {
        r#type: FWP_UINT16,
        Anonymous: FWP_CONDITION_VALUE0_0 { uint16: port },
    }
}

fn cond_app(blob: *mut FWP_BYTE_BLOB) -> FWP_CONDITION_VALUE0 {
    FWP_CONDITION_VALUE0 {
        r#type: FWP_BYTE_BLOB_TYPE,
        Anonymous: FWP_CONDITION_VALUE0_0 { byteBlob: blob },
    }
}

fn cond_loopback() -> FWP_CONDITION_VALUE0 {
    FWP_CONDITION_VALUE0 {
        r#type: FWP_UINT32,
        Anonymous: FWP_CONDITION_VALUE0_0 {
            uint32: FWP_CONDITION_FLAG_IS_LOOPBACK,
        },
    }
}

fn cond_app_id(blob: *mut FWP_BYTE_BLOB) -> FWPM_FILTER_CONDITION0 {
    FWPM_FILTER_CONDITION0 {
        fieldKey: FWPM_CONDITION_ALE_APP_ID,
        matchType: FWP_MATCH_EQUAL,
        conditionValue: cond_app(blob),
    }
}

fn cond_remote_port(port: u16) -> FWPM_FILTER_CONDITION0 {
    FWPM_FILTER_CONDITION0 {
        fieldKey: FWPM_CONDITION_IP_REMOTE_PORT,
        matchType: FWP_MATCH_EQUAL,
        conditionValue: cond_port(port),
    }
}

fn cond_is_loopback() -> FWPM_FILTER_CONDITION0 {
    FWPM_FILTER_CONDITION0 {
        fieldKey: FWPM_CONDITION_FLAGS,
        matchType: FWP_MATCH_FLAGS_ALL_SET,
        conditionValue: cond_loopback(),
    }
}

/// 添加一条过滤器；返回 Win32 错误码（0 = 成功）
fn add_filter(
    engine: HANDLE,
    key: GUID,
    name: &str,
    layer: GUID,
    weight: u8,
    action: FWP_ACTION_TYPE,
    conds: &mut [FWPM_FILTER_CONDITION0],
) -> u32 {
    let name_w: Vec<u16> = name.encode_utf16().chain(std::iter::once(0)).collect();
    let filter = FWPM_FILTER0 {
        filterKey: key,
        displayData: FWPM_DISPLAY_DATA0 {
            name: PWSTR(name_w.as_ptr() as *mut u16),
            description: PWSTR::null(),
        },
        flags: FWPM_FILTER_FLAG_NONE,
        providerKey: std::ptr::null_mut(),
        providerData: FWP_BYTE_BLOB::default(),
        layerKey: layer,
        subLayerKey: SUBLAYER_KEY,
        weight: weight_value(weight),
        numFilterConditions: conds.len() as u32,
        filterCondition: conds.as_mut_ptr(),
        action: action_value(action),
        Anonymous: FWPM_FILTER0_0 { rawContext: 0 },
        reserved: std::ptr::null_mut(),
        filterId: 0,
        effectiveWeight: FWP_VALUE0::default(),
    };
    unsafe { FwpmFilterAdd0(engine, &filter, PSECURITY_DESCRIPTOR::default(), None) }
}

fn add_sublayer(engine: HANDLE) -> u32 {
    let name_w: Vec<u16> = "Baize Network Isolation"
        .encode_utf16()
        .chain(std::iter::once(0))
        .collect();
    let desc_w: Vec<u16> = "Baize 主机网络隔离子层"
        .encode_utf16()
        .chain(std::iter::once(0))
        .collect();
    let sublayer = FWPM_SUBLAYER0 {
        subLayerKey: SUBLAYER_KEY,
        displayData: FWPM_DISPLAY_DATA0 {
            name: PWSTR(name_w.as_ptr() as *mut u16),
            description: PWSTR(desc_w.as_ptr() as *mut u16),
        },
        flags: 0,
        providerKey: std::ptr::null_mut(),
        providerData: FWP_BYTE_BLOB::default(),
        weight: SUBLAYER_WEIGHT,
    };
    unsafe { FwpmSubLayerAdd0(engine, &sublayer, PSECURITY_DESCRIPTOR::default()) }
}

/// 删除可能残留的子层与过滤器（幂等前提）
fn cleanup(engine: HANDLE) {
    for key in ALL_FILTER_KEYS {
        let rc = unsafe { FwpmFilterDeleteByKey0(engine, &key) };
        if rc != ERROR_SUCCESS && !is_not_found(rc) {
            tracing::debug!("[隔离] 清理旧过滤器 {:?} 返回 {}", key, rc);
        }
    }
    let rc = unsafe { FwpmSubLayerDeleteByKey0(engine, &SUBLAYER_KEY) };
    if rc != ERROR_SUCCESS && !is_not_found(rc) {
        tracing::debug!("[隔离] 清理旧子层返回 {}", rc);
    }
}

/// 当前是否处于隔离状态（子层存在即视为隔离中）
pub fn is_active() -> bool {
    let Ok(engine) = Engine::open() else {
        return false;
    };
    let mut sublayer: *mut FWPM_SUBLAYER0 = std::ptr::null_mut();
    let rc = unsafe { FwpmSubLayerGetByKey0(engine.0, &SUBLAYER_KEY, &mut sublayer) };
    if rc == ERROR_SUCCESS && !sublayer.is_null() {
        let mut p = sublayer as *mut c_void;
        unsafe {
            FwpmFreeMemory0(&mut p);
        }
        true
    } else {
        false
    }
}

/// 施加隔离
pub fn apply(server_port: u16, exe_path: &str) -> Result<()> {
    let engine = Engine::open()?;
    // 幂等：先清掉可能存在的旧对象
    cleanup(engine.0);
    let app_id = AppId::from_exe(exe_path)?;

    let rc = unsafe { FwpmTransactionBegin0(engine.0, 0) };
    if rc != ERROR_SUCCESS {
        bail!("WFP 事务开启失败: {}", wfp_err(rc));
    }

    match apply_inner(engine.0, server_port, app_id.0) {
        Ok(()) => {
            let rc = unsafe { FwpmTransactionCommit0(engine.0) };
            if rc != ERROR_SUCCESS {
                bail!("WFP 事务提交失败: {}", wfp_err(rc));
            }
            Ok(())
        }
        Err(e) => {
            unsafe {
                FwpmTransactionAbort0(engine.0);
            }
            Err(e)
        }
    }
}

fn apply_inner(engine: HANDLE, server_port: u16, app_blob: *mut FWP_BYTE_BLOB) -> Result<()> {
    let rc = add_sublayer(engine);
    if rc != ERROR_SUCCESS {
        bail!("创建隔离子层失败: {}", wfp_err(rc));
    }

    // 管理通道：Agent 进程 → Server 端口（两个条件为 AND）
    let mut conds_app = [cond_app_id(app_blob), cond_remote_port(server_port)];
    // DNS：任意进程 → 53（Server 用域名时重连必需）
    let mut conds_dns = [cond_remote_port(DNS_PORT)];
    // loopback：本机 IPC 不打断
    let mut conds_loopback = [cond_is_loopback()];
    // 兜底：无条件下阻断
    let mut conds_block: [FWPM_FILTER_CONDITION0; 0] = [];

    for (layer, keys, family) in [
        (
            FWPM_LAYER_ALE_AUTH_CONNECT_V4,
            (F_V4_APP, F_V4_DNS, F_V4_LOOPBACK, F_V4_BLOCK),
            "V4",
        ),
        (
            FWPM_LAYER_ALE_AUTH_CONNECT_V6,
            (F_V6_APP, F_V6_DNS, F_V6_LOOPBACK, F_V6_BLOCK),
            "V6",
        ),
    ] {
        let (k_app, k_dns, k_loop, k_block) = keys;
        let rc = add_filter(
            engine,
            k_app,
            &format!("Baize Isolate: Agent 管理通道 ({})", family),
            layer,
            PERMIT_WEIGHT,
            FWP_ACTION_PERMIT,
            &mut conds_app,
        );
        if rc != ERROR_SUCCESS {
            bail!("添加管理通道放行规则失败({}): {}", family, wfp_err(rc));
        }

        let rc = add_filter(
            engine,
            k_dns,
            &format!("Baize Isolate: DNS ({})", family),
            layer,
            PERMIT_WEIGHT,
            FWP_ACTION_PERMIT,
            &mut conds_dns,
        );
        if rc != ERROR_SUCCESS {
            bail!("添加 DNS 放行规则失败({}): {}", family, wfp_err(rc));
        }

        let rc = add_filter(
            engine,
            k_loop,
            &format!("Baize Isolate: loopback ({})", family),
            layer,
            PERMIT_WEIGHT,
            FWP_ACTION_PERMIT,
            &mut conds_loopback,
        );
        if rc != ERROR_SUCCESS {
            bail!("添加 loopback 放行规则失败({}): {}", family, wfp_err(rc));
        }

        let rc = add_filter(
            engine,
            k_block,
            &format!("Baize Isolate: 阻断其余出站 ({})", family),
            layer,
            BLOCK_WEIGHT,
            FWP_ACTION_BLOCK,
            &mut conds_block,
        );
        if rc != ERROR_SUCCESS {
            bail!("添加兜底阻断规则失败({}): {}", family, wfp_err(rc));
        }
    }
    Ok(())
}

/// 解除隔离（删除自己的子层与过滤器；不存在视为已解除，幂等）
pub fn release() -> Result<()> {
    let engine = Engine::open()?;
    let mut errors: Vec<String> = Vec::new();

    for key in ALL_FILTER_KEYS {
        let rc = unsafe { FwpmFilterDeleteByKey0(engine.0, &key) };
        if rc != ERROR_SUCCESS && !is_not_found(rc) {
            errors.push(format!("删除过滤器 {:?}: {}", key, wfp_err(rc)));
        }
    }
    let rc = unsafe { FwpmSubLayerDeleteByKey0(engine.0, &SUBLAYER_KEY) };
    if rc != ERROR_SUCCESS && !is_not_found(rc) {
        errors.push(format!("删除隔离子层: {}", wfp_err(rc)));
    }

    if errors.is_empty() {
        Ok(())
    } else {
        bail!("解除隔离失败: {}", errors.join("; "))
    }
}
