@echo off
setlocal enabledelayedexpansion

echo ============================================================
echo   Baize - build and run
echo ============================================================
echo.

echo [check 1/2] existing Baize Server instance
set "SERVER_FOUND="
set "SERVER_PIDS="
set "PORT_FOUND="
set "OTHER_BUSY="

for /f "usebackq tokens=*" %%i in (`powershell -NoProfile -Command "Get-Process baize-server -ErrorAction SilentlyContinue | ForEach-Object { 'process  PID={0}  {1}  started {2}' -f $_.Id, $_.Path, $_.StartTime }"`) do (
    echo     %%i
    set "SERVER_FOUND=1"
)
for /f "usebackq tokens=*" %%p in (`powershell -NoProfile -Command "(Get-Process baize-server -ErrorAction SilentlyContinue).Id -join ' '"`) do set "SERVER_PIDS=%%p"
for /f "usebackq tokens=*" %%i in (`powershell -NoProfile -Command "Get-NetTCPConnection -LocalPort 8080,50051 -State Listen -ErrorAction SilentlyContinue | Sort-Object LocalPort -Unique | ForEach-Object { $p = Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue; 'port {0}  PID={1}  {2}  {3}' -f $_.LocalPort, $_.OwningProcess, $p.ProcessName, $p.Path }"`) do (
    echo     %%i
    set "PORT_FOUND=1"
)
for /f "usebackq tokens=*" %%i in (`powershell -NoProfile -Command "Get-NetTCPConnection -LocalPort 8080,50051 -State Listen -ErrorAction SilentlyContinue | ForEach-Object { $p = Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue; if ($p -and $p.ProcessName -ne 'baize-server') { $p.ProcessName } }"`) do set "OTHER_BUSY=1"

if not defined SERVER_FOUND if not defined PORT_FOUND echo     none found
echo.

if defined SERVER_FOUND (
    echo [warning] Baize Server is already running - see the process list above.
    echo           A second instance would block on the Bleve index file lock:
    echo           it would neither listen on any port nor exit.
    echo.
    echo           Kill these processes and continue? [y/N]
    set /p "ANSWER="
    if /i not "!ANSWER!"=="y" goto :cancelled
    for %%p in (!SERVER_PIDS!) do (
        echo           killing PID=%%p
        taskkill /F /PID %%p >nul 2>&1
    )
    echo.
)

if defined OTHER_BUSY (
    echo [warning] port 8080 / 50051 is held by another program - see the port list above.
    echo           Close it and run this script again.
    goto :port_busy
)

echo [check 2/2] agent build conflict
set "AGENT_FOUND="
for /f "usebackq tokens=*" %%i in (`powershell -NoProfile -Command "Get-Process baize-agent -ErrorAction SilentlyContinue | Where-Object { $_.Path -like '*target\release*' } | ForEach-Object { 'agent  PID={0}  {1}' -f $_.Id, $_.Path }"`) do (
    echo     %%i
    set "AGENT_FOUND=1"
)
if defined AGENT_FOUND (
    echo [warning] agent is running from target\release.
    echo           cargo cannot overwrite a running exe - the build would fail with LNK1104.
    echo           Stop that agent process and run this script again.
    goto :agent_busy
)
echo     none running from target\release
echo.

echo Building agent...
cd /d %~dp0..\agent
set "PROTOC=%USERPROFILE%\protoc\bin\protoc.exe"
call cargo build --release
if %errorlevel% neq 0 (
    echo Agent build failed, check cargo Rust toolchain
    pause
    exit /b %errorlevel%
)
cd /d %~dp0
echo Building frontend...
cd web
call npm run build
if %errorlevel% neq 0 (
    echo Frontend build failed, check npm
    pause
    exit /b %errorlevel%
)
cd ..
echo Building server...
go build -o baize-server.exe ./cmd/
if %errorlevel% neq 0 (
    echo Server build failed, check Go
    pause
    exit /b %errorlevel%
)
echo Starting server...
baize-server.exe -agent-binary ..\agent\target\release\baize-agent.exe
exit /b 0


:cancelled
echo.
echo startup cancelled.
exit /b 1

:port_busy
echo.
pause
exit /b 1

:agent_busy
echo.
pause
exit /b 1
