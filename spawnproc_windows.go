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

// spawnNewProcess starts a new instance and exits the current one.
// desiredTun=true requests UAC elevation for TUN mode.
func spawnNewProcess(desiredTun bool) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[热加载] 获取可执行文件路径失败: %v", err)
		return
	}

	args := buildSpawnArgs(exe, desiredTun)
	argStr := strings.Join(args, " ")

	if desiredTun {
		log.Printf("[热加载] TUN 模式，释放端口并请求管理员权限...")
		stopAllListeners()
		time.Sleep(500 * time.Millisecond)
		verb, _ := windows.UTF16PtrFromString("runas")
		file, _ := windows.UTF16PtrFromString(exe)
		params, _ := windows.UTF16PtrFromString(argStr)
		dir, _ := windows.UTF16PtrFromString("")
		var showCmd int32 = 1
		err := windows.ShellExecute(0, verb, file, params, dir, showCmd)
		if err != nil {
			log.Printf("[热加载] ShellExecute 失败: %v", err)
			return
		}
		log.Printf("[热加载] 管理员进程已请求")
		time.Sleep(800 * time.Millisecond)
		log.Printf("[热加载] 当前进程退出")
		os.Exit(0)
	}

	// 关闭 TUN 模式：释放端口后用 ShellExecute 创建独立进程
	log.Printf("[热加载] 停止监听器释放端口...")
	stopAllListeners()
	time.Sleep(500 * time.Millisecond)

	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(argStr)
	dir, _ := windows.UTF16PtrFromString("")
	var showCmd int32 = 1
	err = windows.ShellExecute(0, verb, file, params, dir, showCmd)
	if err != nil {
		log.Printf("[热加载] ShellExecute 失败: %v", err)
		// Fallback to exec.Command
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		}
		if err2 := cmd.Start(); err2 != nil {
			log.Printf("[热加载] 子进程启动也失败: %v", err2)
			return
		}
		log.Printf("[热加载] 子进程 PID=%d (fallback)", cmd.Process.Pid)
		time.Sleep(800 * time.Millisecond)
		log.Printf("[热加载] 当前进程退出")
		os.Exit(0)
	}

	log.Printf("[热加载] 新独立进程已启动")
	time.Sleep(800 * time.Millisecond)
	log.Printf("[热加载] 当前进程退出")
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
