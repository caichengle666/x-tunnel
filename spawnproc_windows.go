//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func spawnNewProcess(desiredTun bool) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[热加载] 获取可执行文件路径失败: %v", err)
		return
	}

	args := buildSpawnArgs(exe, desiredTun)

	if desiredTun {
		log.Printf("[热加载] TUN 模式，请求管理员权限...")
		verb, _ := windows.UTF16PtrFromString("runas")
		file, _ := windows.UTF16PtrFromString(exe)
		argStr := strings.Join(args, " ")
		params, _ := windows.UTF16PtrFromString(argStr)
		dir, _ := windows.UTF16PtrFromString("")
		var showCmd int32 = 1
		err := windows.ShellExecute(0, verb, file, params, dir, showCmd)
		if err != nil {
			log.Printf("[热加载] ShellExecute 失败: %v", err)
			return
		}
		log.Printf("[热加载] 管理员进程已请求")
		log.Printf("[热加载] 当前进程退出")
		os.Exit(0)
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[热加载] 启动新进程失败: %v", err)
		return
	}
	log.Printf("[热加载] 新进程已启动 (PID: %d)，等待端口释放...", cmd.Process.Pid)
	// Wait briefly for child to bind ports before exiting
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}

func buildSpawnArgs(exe string, desiredTun bool) []string {
	args := []string{}
	if configFile != "" {
		// Use absolute path so spawned process finds config from any CWD
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
