//go:build !windows

package main

import "syscall"

var (
	tunMode    bool
	tunName    string
	tunMTU     int
	tunAddress string
	tunDNS     string
)

func runTUNModeIfNeeded() {}

func StartTun(cfg *TunConfig) error {
	return nil
}

func IsTunActive() bool { return false }

func StopTun() {}

func ensureControlPlaneBypass() {}

func bindSocketToPhysNIC(_ string, _ syscall.RawConn) error {
	return nil
}

// probeIfaceUDP 非 Windows 平台不做网卡探测（默认返回 true，使用默认路由）
func probeIfaceUDP(_ int) bool {
	return true
}
