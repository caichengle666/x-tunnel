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

func bindSocketToPhysNIC(_ string, _ syscall.RawConn) error {
	return nil
}
