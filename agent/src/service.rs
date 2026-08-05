// Windows 服务化
use anyhow::{Context, Result};
use std::ffi::OsString;
use std::sync::atomic::Ordering;
use tracing::info;
use windows_service::{
    service::{
        ServiceAccess, ServiceErrorControl, ServiceInfo, ServiceStartType,
        ServiceState, ServiceStatus, ServiceType, ServiceExitCode,
    },
    service_control_handler::{self, ServiceControlHandlerResult, ServiceStatusHandle},
    service_dispatcher,
    service_manager::{ServiceManager, ServiceManagerAccess},
};

extern "system" fn service_main(_argc: u32, _argv: *mut *mut u16) {
    let service_name = "baize-agent";

    let status_handle = match service_control_handler::register(service_name, move |control_event| {
        match control_event {
            windows_service::service::ServiceControl::Stop
            | windows_service::service::ServiceControl::Shutdown => {
                info!("[Service] 收到停止信号");
                crate::STOP_FLAG.store(true, Ordering::Relaxed);
                ServiceControlHandlerResult::NoError
            }
            _ => ServiceControlHandlerResult::NotImplemented,
        }
    }) {
        Ok(h) => h,
        Err(e) => {
            eprintln!("注册服务控制处理器失败: {:?}", e);
            return;
        }
    };

    if let Err(e) = status_handle.set_service_status(ServiceStatus {
        service_type: ServiceType::OWN_PROCESS,
        current_state: ServiceState::Running,
        controls_accepted: windows_service::service::ServiceControlAccept::STOP,
        exit_code: ServiceExitCode::NO_ERROR,
        checkpoint: 0,
        wait_hint: std::time::Duration::default(),
        process_id: None,
    }) {
        eprintln!("设置 Running 状态失败: {:?}", e);
        return;
    }

    info!("[Service] Baize Agent 服务已启动");

    // 创建 tokio runtime 并运行 Agent 主逻辑（采集 + gRPC 上报）。
    // 停止信号由控制处理器置位 crate::STOP_FLAG，主循环检测后干净退出。
    match tokio::runtime::Runtime::new() {
        Ok(rt) => {
            if let Err(e) = rt.block_on(crate::run_agent_loop(None, None, None, 30, None)) {
                eprintln!("[Service] Agent 主逻辑退出: {:?}", e);
            }
        }
        Err(e) => {
            eprintln!("[Service] 创建 tokio runtime 失败: {:?}", e);
        }
    }

    if let Err(e) = status_handle.set_service_status(ServiceStatus {
        service_type: ServiceType::OWN_PROCESS,
        current_state: ServiceState::Stopped,
        controls_accepted: windows_service::service::ServiceControlAccept::STOP,
        exit_code: ServiceExitCode::NO_ERROR,
        checkpoint: 0,
        wait_hint: std::time::Duration::default(),
        process_id: None,
    }) {
        eprintln!("设置 Stopped 状态失败: {:?}", e);
    }
    info!("[Service] Baize Agent 服务已停止");
}

pub fn run_as_service() -> Result<()> {
    service_dispatcher::start("baize-agent", service_main)
        .context("启动 Windows 服务失败")?;
    Ok(())
}

pub fn install() -> Result<()> {
    let manager = ServiceManager::local_computer(None::<&str>, ServiceManagerAccess::CREATE_SERVICE)
        .context("打开服务管理器失败（需要管理员权限）")?;

    let binary_path = std::env::current_exe()
        .context("无法获取当前可执行文件路径")?;

    let service_info = ServiceInfo {
        name: OsString::from("baize-agent"),
        display_name: OsString::from("Baize EDR Agent"),
        service_type: ServiceType::OWN_PROCESS,
        start_type: ServiceStartType::AutoStart,
        error_control: ServiceErrorControl::Normal,
        executable_path: binary_path.clone(),
        launch_arguments: vec![OsString::from("--service")],
        dependencies: vec![],
        account_name: None,
        account_password: None,
    };

    let _service = manager
        .create_service(&service_info, ServiceAccess::CHANGE_CONFIG)
        .context("创建服务失败（需要管理员权限）")?;

    info!("[Service] Baize Agent 服务已安装");
    info!("[Service] 启动: net start baize-agent");
    Ok(())
}

pub fn uninstall() -> Result<()> {
    let manager = ServiceManager::local_computer(None::<&str>, ServiceManagerAccess::CONNECT)
        .context("打开服务管理器失败（需要管理员权限）")?;

    let service = manager
        .open_service("baize-agent", ServiceAccess::DELETE)
        .context("打开服务失败（是否已安装？）")?;

    service
        .delete()
        .context("删除服务失败（需要管理员权限）")?;

    info!("[Service] Baize Agent 服务已卸载");
    Ok(())
}
