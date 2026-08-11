@echo off
cd /d "%~dp0"
REM ============================================
REM  Baize (白泽) Agent 卸载程序
REM  移除服务 + 恢复审计策略 + 清理文件（可选）
REM ============================================
setlocal enabledelayedexpansion

REM ── 1. 自动请求管理员权限（非管理员时 UAC 提升自身重跑）──
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo 需要管理员权限，正在请求提升...
    powershell -Command "Start-Process -FilePath '%~f0' -Verb RunAs" >nul 2>&1
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
echo   3. 可选：删除安装文件与本地数据
echo.
set /p CONFIRM=确认卸载？(y/N): 
if /i not "%CONFIRM%"=="y" (
    echo 已取消。
    pause
    exit /b
)

REM ── 2. 停止并删除服务 ──
echo.
echo [1/3] 停止服务...
net stop baize-agent >nul 2>&1
timeout /t 2 /nobreak >nul
echo [2/3] 删除服务...
sc delete baize-agent >nul 2>&1
if %errorlevel% equ 0 (
    echo [OK] baize-agent 服务已删除
) else (
    echo [警告] 服务可能不存在或删除失败（可手动执行 sc delete baize-agent）
)

REM ── 3. 恢复审计策略（与安装对称：关掉 Agent 启用的审计子类）──
echo [3/3] 恢复审计策略...
auditpol /set /subcategory:{0CCE922B-69AE-11D9-BED3-505054503030} /success:disable >nul 2>&1
auditpol /set /subcategory:{0CCE9226-69AE-11D9-BED3-505054503030} /success:disable >nul 2>&1
echo [OK] 审计策略已恢复（4688 进程创建 / 5156 网络连接）

REM ── 4. 清理文件（可选）──
echo.
set /p DELFILES=是否删除安装文件与本地数据（C:\Program Files\Baize 等）？(y/N): 
if /i "%DELFILES%"=="y" (
    rmdir /s /q "C:\Program Files\Baize" >nul 2>&1
    if exist "C:\Program Files\Baize" (
        echo [警告] 安装目录删除失败（可能有文件被占用），请稍后手动删除
    ) else (
        echo [OK] 安装文件已删除: C:\Program Files\Baize
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
pause
