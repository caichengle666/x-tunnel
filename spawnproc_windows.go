//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	argStr := windowsCommandLine(args)

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
			restoreListenersAfterSpawnFailure()
			return
		}
		log.Printf("[热加载] 管理员进程已请求")
		time.Sleep(800 * time.Millisecond)
		log.Printf("[热加载] 当前进程退出")
		os.Exit(0)
	}

	// 关闭 TUN 模式：释放端口后用 ShellExecute 创建独立进程
	tunMu.Lock()
	softStopTun()
	tunMu.Unlock()
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
			restoreListenersAfterSpawnFailure()
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

func windowsCommandLine(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}

func restoreListenersAfterSpawnFailure() {
	log.Printf("[热加载] 新进程启动失败，恢复当前进程监听...")
	startWebGUI()
	startListeners()
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
	} else {
		// 无配置文件时保留命令行关键参数，避免重启丢配置
		if listenAddr != "" {
			args = append(args, "-l", listenAddr)
		}
		if forwardAddr != "" {
			args = append(args, "-f", forwardAddr)
		}
		if token != "" {
			args = append(args, "-token", token)
		}
		if connectionNum > 0 {
			args = append(args, "-n", strconv.Itoa(connectionNum))
		}
		if insecure {
			args = append(args, "-insecure")
		}
		if ips != "" {
			args = append(args, "-ips", ips)
		}
	}
	if webListen != "" {
		args = append(args, "-web", webListen)
	}
	if desiredTun {
		args = append(args, "-tun")
	} else {
		args = append(args, "-tun=false")
	}
	return args
}
