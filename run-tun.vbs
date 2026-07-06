Set UAC = CreateObject("Shell.Application")
UAC.ShellExecute "D:\测试专用\x-tunnel-fresh\x-tunnel-new.exe", "-config config.json -web :9090", "D:\测试专用\x-tunnel-fresh", "runas", 1
