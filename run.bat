@echo off
cd /d "%~dp0"
title x-tunnel
echo [x-tunnel] Web GUI: http://localhost:9090
echo [x-tunnel] SOCKS5: 127.0.0.1:1080 / HTTP: 127.0.0.1:1090
echo [x-tunnel] TUN mode can be switched in Web GUI
echo Close this window to stop service
echo ================================
x-tunnel-new.exe -config config.json -web :9090
pause
