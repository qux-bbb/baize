// Windows 服务化
use anyhow::{Context, Result};
use std::ffi::OsString;
use std::sync::atomic::{AtomicBool, Ordering};
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

static SERVICE_STOPPED: AtomicBool = AtomicBool::new(false);

pub fn should_stop() -> bool {
    SERVICE_STOPPED.load(Ordering::Relaxed)
}

extern "system" fn service_main(_argc: u32, _argv: *mut *mut u16) {
    let service_name = "baize-agent";

    let status_handle = match service_control_handler::register(service_name, move |control_event| {
        match control_event {
            windows_service::service::ServiceControl::Stop
            | windows_service::service::ServiceControl::Shutdown => {
                info!("[Service] 收到停止信号");
                SERVICE_STOPPED.store(true, Ordering::Relaxed);
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

    // 等待停止信号
    while !SERVICE_STOPPED.load(Ordering::Relaxed) {
        std::thread::sleep(std::time::Duration::from_secs(1));
    }

    // 这里后续可以把 agent 的主逻辑（tokio runtime + gRPC）搬进来
    // 目前只支持服务生命周期管理

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
