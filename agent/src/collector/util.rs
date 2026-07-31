// 公共工具函数 — EvtSubscribe XML 渲染与字段提取 (Windows only)
use std::ffi::OsString;
use std::os::windows::ffi::OsStringExt;

use windows::Win32::Foundation::*;
use windows::Win32::System::EventLog::*;
use windows::Win32::System::Threading::{
    OpenProcess, QueryFullProcessImageNameW, PROCESS_NAME_WIN32, PROCESS_QUERY_LIMITED_INFORMATION,
};

/// 渲染事件为 XML 字符串（两次 EvtRender 模式）
pub unsafe fn render_event_xml(event: EVT_HANDLE) -> Option<String> {
    // 第一次渲染：获取缓冲区大小
    let mut buf_used = 0u32;
    let _ = EvtRender(
        EVT_HANDLE::default(),
        event,
        EvtRenderEventXml.0,
        0,
        None::<*mut core::ffi::c_void>,
        &mut buf_used,
        std::ptr::null_mut(),
    );
    if buf_used == 0 {
        return None;
    }

    // 第二次渲染：获取 XML 内容
    let mut xml_buf = vec![0u16; buf_used as usize];
    let mut buf_used2 = 0u32;
    let result = EvtRender(
        EVT_HANDLE::default(),
        event,
        EvtRenderEventXml.0,
        (buf_used * 2) as u32,
        Some(xml_buf.as_mut_ptr() as *mut core::ffi::c_void),
        &mut buf_used2,
        std::ptr::null_mut(),
    );
    if result.is_err() {
        return None;
    }

    let xml = OsString::from_wide(&xml_buf).to_string_lossy().to_string();
    if xml.is_empty() { None } else { Some(xml) }
}

/// 提取 `<Data Name='attr'>` 或 `<Data Name="attr">` 标签内的文本
pub fn extract_xml(xml: &str, attr: &str) -> String {
    let open1 = format!("<Data Name='{}'>", attr);
    let open2 = format!("<Data Name=\"{}\">", attr);
    for open in [open1, open2] {
        if let Some(start) = xml.find(open.as_str()) {
            let content_start = start + open.len();
            if let Some(end) = xml[content_start..].find("</Data>") {
                return xml[content_start..content_start + end].to_string();
            }
        }
    }
    String::new()
}

/// 提取 PID（兼容 0x 十六进制和十进制）
pub fn extract_pid(xml: &str, attr: &str) -> u64 {
    let s = extract_xml(xml, attr);
    if s.starts_with("0x") || s.starts_with("0X") {
        u64::from_str_radix(&s[2..], 16).unwrap_or(0)
    } else {
        s.parse::<u64>().unwrap_or(0)
    }
}

/// 通过 PID 查询进程完整路径（QueryFullProcessImageNameW）
pub fn get_process_path(pid: u64) -> String {
    unsafe {
        let handle = match OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid as u32) {
            Ok(h) => h,
            Err(_) => return String::new(),
        };
        let mut buf = vec![0u16; 4096];
        let mut size = buf.len() as u32;
        let result = QueryFullProcessImageNameW(
            handle,
            PROCESS_NAME_WIN32,
            windows::core::PWSTR(buf.as_mut_ptr()),
            &mut size,
        );
        let _ = CloseHandle(handle);
        if result.is_ok() && size > 0 {
            return String::from_utf16_lossy(&buf[..size as usize]);
        }
    }
    String::new()
}
