@echo off
cd /d "%~dp0"
REM ============================================
REM  Baize (白泽) Agent 卸载程序
REM  移除服务 + 恢复审计策略 + 清理文件（可选）
REM ============================================
setlocal enabledelayedexpansion

REM ── 0. 静默模式（/S：控制面板卸载时跳过交互确认）──
set SILENT=0
if /i "%~1"=="/S" set SILENT=1

REM ── 1. 自动请求管理员权限（非管理员时 UAC 提升自身重跑）──
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo 需要管理员权限，正在请求提升...
    REM 注意: PS 5.1 的 Start-Process -ArgumentList 不接受空字符串(双击无参数时 '' 会直接报错)
    if /i "%~1"=="/S" (
        powershell -Command "Start-Process -FilePath '%~f0' -ArgumentList '/S' -Verb RunAs" >nul 2>&1
    ) else (
        powershell -Command "Start-Process -FilePath '%~f0' -Verb RunAs" >nul 2>&1
    )
    if !errorlevel! neq 0 (
        echo [错误] 未能获取管理员权限，请右键选择"以管理员身份运行"
        pause
    )
    exit /b
)

echo ============================================
echo  Baize (白泽) Agent 卸载程序
echo ============================================
echo 本操作将:
echo   1. 停止并删除 baize-agent 服务
echo   2. 恢复进程创建/网络连接审计策略
echo   3. 移除控制面板卸载入口
echo   4. 可选：删除安装文件与本地数据
echo.
if %SILENT% equ 0 (
    set /p CONFIRM=确认卸载？^(y/N^): 
    if /i not "!CONFIRM!"=="y" (
        echo 已取消。
        pause
        exit /b
    )
)

REM ── 2. 停止并删除服务 ──
echo.
echo [1/4] 停止服务...
sc query baize-agent >nul 2>&1
if %errorlevel% neq 0 (
    echo [提示] baize-agent 服务不存在，跳过停止
    goto NO_SERVICE
)
net stop baize-agent >nul 2>&1
REM 轮询等待服务真正停止（最多 30 秒），确认停止后再删除
set /a WAIT=0
:WAIT_STOP
sc query baize-agent 2>nul | findstr /i "STOPPED" >nul 2>&1
if !errorlevel! equ 0 goto STOPPED
timeout /t 2 /nobreak >nul
set /a WAIT+=1
if !WAIT! lss 15 goto WAIT_STOP
echo [警告] 服务 30 秒内未停止，继续尝试删除（可能失败）
:STOPPED
echo [2/4] 删除服务...
sc delete baize-agent >nul 2>&1
if %errorlevel% equ 0 (
    echo [OK] baize-agent 服务已删除
) else (
    echo [警告] 服务可能不存在或删除失败（可手动执行 sc delete baize-agent）
)
:NO_SERVICE

REM 服务仍存在（删除失败）则跳过审计恢复，避免与运行中 Agent 的审计 refcount 冲突
sc query baize-agent >nul 2>&1
if %errorlevel% equ 0 (
    echo [警告] baize-agent 服务仍存在，跳过审计策略恢复（请先手动停止并删除服务）
    goto SKIP_AUDIT
)
REM ── 3. 恢复审计策略（与安装对称：关掉 Agent 启用的审计子类）──
echo [3/4] 恢复审计策略...
auditpol /set /subcategory:{0CCE922B-69AE-11D9-BED3-505054503030} /success:disable >nul 2>&1
auditpol /set /subcategory:{0CCE9226-69AE-11D9-BED3-505054503030} /success:disable >nul 2>&1
echo [OK] 审计策略已恢复（4688 进程创建 / 5156 网络连接）
:SKIP_AUDIT

REM ── 3.5 移除控制面板卸载入口 ──
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Baize Agent" /f >nul 2>&1
if %errorlevel% equ 0 (
    echo [OK] 控制面板卸载入口已移除
) else (
    echo [提示] 控制面板卸载入口不存在或已移除
)

REM ── 4. 清理文件（可选）──
REM 安装目录：优先标准路径（%ProgramFiles%\Baize，兼容从解压目录运行），否则用脚本所在目录
set "INSTALL_DIR=%ProgramFiles%\Baize"
REM 回退保护：仅当脚本所在目录确实含 baize-agent.exe 时才回退，避免误删任意目录
if not exist "%INSTALL_DIR%\baize-agent.exe" (
    if exist "%~dp0baize-agent.exe" set "INSTALL_DIR=%~dp0"
)
cd /d "C:\"
echo.
if %SILENT% equ 0 (
    set /p DELFILES=是否删除安装文件与本地数据（%INSTALL_DIR% 与 C:\ProgramData\Baize）？^(y/N^): 
) else (
    set DELFILES=y
)
if /i "%DELFILES%"=="y" (
    REM 脚本在安装目录内运行时，先删自身，避免 rmdir 删除运行中的 bat 导致 cmd 无法读完脚本
    if /i "%INSTALL_DIR%\"=="%~dp0" del "%~f0" >nul 2>&1
    rmdir /s /q "%INSTALL_DIR%" >nul 2>&1
    if exist "%INSTALL_DIR%" (
        echo [警告] 安装目录删除失败（可能有文件被占用），请稍后手动删除
    ) else (
        echo [OK] 安装文件已删除: %INSTALL_DIR%
    )
    rmdir /s /q "C:\ProgramData\Baize" >nul 2>&1
    if exist "C:\ProgramData\Baize" (
        echo [警告] 数据目录删除失败，请稍后手动删除
    ) else (
        echo [OK] 本地数据已删除: C:\ProgramData\Baize
    )
) else (
    echo 已保留安装文件与数据（如需清理请手动删除上述目录）
)

echo.
echo 卸载完成。
if %SILENT% equ 0 pause
