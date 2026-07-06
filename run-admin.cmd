@echo off
cd /d "D:\测试专用\x-tunnel-fresh"
start "" "%~dp0x-tunnel-new.exe" -config config.json -web :9090
