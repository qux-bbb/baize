pub mod file;
pub mod process;

#[cfg(target_os = "windows")]
pub mod windows;
#[cfg(target_os = "windows")]
pub mod wmi_process;
