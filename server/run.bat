@echo off
echo Building agent...
cd /d %~dp0..\agent
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
