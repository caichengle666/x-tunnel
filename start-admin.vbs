Set UAC = CreateObject("Shell.Application")
UAC.ShellExecute "D:\????\x-tunnel-fresh\x-tunnel-new.exe", "-config config.json -web :9090", "D:\????\x-tunnel-fresh", "runas", 1
