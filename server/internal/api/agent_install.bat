@echo off
setlocal
cd /d "%~dp0"

echo ============================================
echo  Baize Agent 安装程序
echo ============================================
echo.

REM ── 1. 检查管理员权限 ──
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [错误] 请以管理员身份运行本脚本！
    echo        右键点击 install.bat → 以管理员身份运行
    pause
    exit /b 1
)

REM ── 2. 检查安装文件是否齐全 ──
if not exist "baize-agent.exe" (
    echo [错误] 未找到 baize-agent.exe，请确认安装包完整
    pause
    exit /b 1
)
if not exist "agent.conf" (
    echo [警告] 未找到 agent.conf，将使用默认配置（连接 127.0.0.1:50051）
)

REM ── 3. 检查服务是否已安装 ──
sc query baize-agent >nul 2>&1
if %errorlevel% equ 0 (
    echo [错误] baize-agent 服务已存在。
    echo        如需重新安装，请先执行: sc delete baize-agent
    pause
    exit /b 1
)

REM ── 4. 拷贝文件到安装目录 ──
set "INSTALL_DIR=%ProgramFiles%\Baize"
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
copy /Y "baize-agent.exe" "%INSTALL_DIR%\" >nul
if %errorlevel% neq 0 (
    echo [错误] 拷贝 baize-agent.exe 失败
    pause
    exit /b 1
)
if exist "agent.conf" copy /Y "agent.conf" "%INSTALL_DIR%\" >nul
REM -- CA 证书（TLS 必须，缺失时连接失败）--
if exist "ca.crt" copy /Y "ca.crt" "%INSTALL_DIR%\" >nul
if not exist "%INSTALL_DIR%\ca.crt" (
    echo [警告] 未找到 ca.crt，TLS 连接将失败（请从 Server 下载页重新获取完整包）
)
echo [OK] 文件已安装到 %INSTALL_DIR%

REM ── 5. 启用进程创建审计（4688，只需一次） ──
auditpol /set /subcategory:{0CCE922B-69AE-11D9-BED3-505054503030} /success:enable >nul 2>&1
if %errorlevel% equ 0 (
    echo [OK] 进程创建审计已启用（4688）
) else (
    echo [警告] 启用进程审计失败，可稍后手动执行 auditpol 命令
)

REM ── 6. 安装并启动服务 ──
cd /d "%INSTALL_DIR%"
baize-agent.exe --install
if %errorlevel% neq 0 (
    echo [错误] 服务安装失败
    pause
    exit /b 1
)
echo [OK] 服务已安装

net start baize-agent
if %errorlevel% neq 0 (
    echo [错误] 服务启动失败，请查看日志: %ProgramData%\Baize\agent.log
    pause
    exit /b 1
)

echo.
echo ═══════════════════════════════════════════════
echo  Baize Agent 安装完成！
echo   - 服务名称 : baize-agent
echo   - 安装目录 : %INSTALL_DIR%
echo   - 配置文件 : %INSTALL_DIR%\agent.conf
echo   - 日志文件 : %ProgramData%\Baize\agent.log
echo   - 卸载命令 : sc stop baize-agent ^&^& sc delete baize-agent
echo ═══════════════════════════════════════════════
pause
