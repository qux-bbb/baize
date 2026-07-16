@echo off
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
echo Starting server...
go run ./cmd/
