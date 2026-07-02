//go:build !windows

package main

import "syscall"

// TUN mode is Windows-only. These stubs ensure the code compiles on Linux/macOS.

var (
	tunMode         bool
	tunName         string
	tunMTU          int
	tunAddress      string
	tunDNS          string
	directStr       string
	proxyStr        string
	defaultRouteStr string
	geoipFile       string
	geositeFile     string
)

func runTUNModeIfNeeded() {}

func loadGeoIP()   {}
func loadGeoSite() {}

func StartTun(cfg *TunConfig) error {
	return nil
}

func bindSocketToPhysNIC(_ string, _ syscall.RawConn) error {
	return nil
}
