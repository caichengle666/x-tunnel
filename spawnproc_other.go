//go:build !windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func spawnNewProcess(desiredTun bool) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[热加载] 获取可执行文件路径失败: %v", err)
		return
	}

	args := buildSpawnArgs(exe, desiredTun)

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[热加载] 启动新进程失败: %v", err)
		return
	}
	log.Printf("[热加载] 新进程已启动 (PID: %d)，当前进程退出", cmd.Process.Pid)
	os.Exit(0)
}

func buildSpawnArgs(exe string, desiredTun bool) []string {
	args := []string{}
	if configFile != "" {
		absConfig, err := filepath.Abs(configFile)
		if err == nil {
			args = append(args, "-config", absConfig)
		} else {
			args = append(args, "-config", configFile)
		}
	}
	if webListen != "" {
		args = append(args, "-web", webListen)
	}
	if desiredTun {
		args = append(args, "-tun")
	}
	return args
}

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
