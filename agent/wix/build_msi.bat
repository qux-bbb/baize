@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

REM ═══════════════════════════════════════════════════════════
REM  Baize Agent MSI 构建脚本（WiX 3.14）
REM  用法: build_msi.bat [baize-agent.exe 路径]
REM  默认使用 ..\..\target\debug\baize-agent.exe
REM  WiX 工具链: 环境变量 WIX_BIN 指向 candle.exe 所在目录
REM              （默认 %USERPROFILE%\wix314\bin314）
REM  产物: baize-agent.msi
REM  安装: msiexec /i baize-agent.msi /q SERVER_ADDR="http://10.0.0.1:50051"
REM ═══════════════════════════════════════════════════════════

set "AGENT_EXE=%~1"
if "%AGENT_EXE%"=="" set "AGENT_EXE=..\target\debug\baize-agent.exe"

set "WIX_BIN=%WIX_BIN%"
if "%WIX_BIN%"=="" set "WIX_BIN=%USERPROFILE%\wix314\bin314"

if not exist "%AGENT_EXE%" (
    echo [错误] 未找到 Agent 二进制: %AGENT_EXE%
    echo        请先执行 cargo build（需设置 PROTOC 环境变量）
    exit /b 1
)
if not exist "%WIX_BIN%\candle.exe" (
    echo [错误] 未找到 WiX 工具链: %WIX_BIN%\candle.exe
    echo        请下载 wix314-binaries.zip 解压后设置 WIX_BIN 环境变量
    echo        下载: https://github.com/wixtoolset/wix3/releases/tag/wix3141rtm
    exit /b 1
)

echo [1/3] 准备 TLS CA 证书 ...
set "HAS_CA=no"
if exist "%~dp0ca.crt" (
    set "HAS_CA=yes"
    echo   找到 ca.crt，MSI 将内置 TLS CA 证书
    > "%~dp0agent.conf" echo {"server": "", "ca": "ca.crt", "watch_dirs": []}
) else (
    echo   未找到 ca.crt（TLS 模式需先拷贝 Server 生成的 ca.crt 到本目录）
    > "%~dp0agent.conf" echo {"server": "", "ca": "", "watch_dirs": []}
)

echo [2/3] 编译 Product.wxs ...
"%WIX_BIN%\candle.exe" -arch x64 Product.wxs -d"AgentExe=%AGENT_EXE%" -d"HasCaCrt=%HAS_CA%"
if %errorlevel% neq 0 (
    echo [错误] candle 编译失败
    exit /b 1
)

echo [3/3] 链接生成 MSI ...
"%WIX_BIN%\light.exe" Product.wixobj -ext "%WIX_BIN%\WixUtilExtension.dll" -o baize-agent.msi
if %errorlevel% neq 0 (
    echo [错误] light 链接失败
    exit /b 1
)

echo.
echo 构建完成: %~dp0baize-agent.msi
echo.
echo 静默安装示例（TLS 模式，需已内置 ca.crt）:
echo   msiexec /i "%~dp0baize-agent.msi" /q SERVER_ADDR="https://10.0.0.1:50051"
echo 不带地址安装（装后手动编辑 agent.conf 配置）:
echo   msiexec /i "%~dp0baize-agent.msi" /q
echo.
echo TLS 说明: ca.crt 由 Server 首次启动生成（server\data\ca.crt），
echo          构建 MSI 前请拷贝到本目录（agent\wix\ca.crt）
