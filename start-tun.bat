@echo off
chcp 65001 >nul 2>&1

net session >nul 2>&1
if %errorlevel% neq 0 (
    powershell -Command "Start-Process '%~f0' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"

if not exist x-tunnel.exe (
    echo [BUILD] Building...
    go build -o x-tunnel.exe .
    if %errorlevel% neq 0 (
        echo [FAIL] Build error
        pause
        exit /b 1
    )
)

if not exist wintun.dll (
    echo [ERROR] wintun.dll not found!
    pause
    exit /b 1
)

if not exist config.json (
    echo [ERROR] config.json not found!
    pause
    exit /b 1
)

echo ============================================
echo   x-tunnel Client ^(TUN + Web GUI + Config^)
echo ============================================
echo.

x-tunnel.exe -config "%~dp0config.json"

echo.
pause