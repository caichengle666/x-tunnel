//go:build windows

package main

import (
	"log"
	"strings"
	"sync"
	"syscall"
	"time"
)

// physIfaceIndex 是物理网卡接口索引，用于绕过 TUN 路由。
var physIfaceIndex int = -1

// Runtime TUN toggle state
var (
	tunMu     sync.Mutex
	tunActive bool
	tunDevice *WindowsTun
	tunStack  *gVisorStack

	localListenersStarted bool
)

// IsTunActive returns true if TUN is currently running
func IsTunActive() bool {
	tunMu.Lock()
	defer tunMu.Unlock()
	return tunActive
}

// StopTun is the public wrapper that locks tunMu (for direct API calls).
func StopTun() {
	tunMu.Lock()
	defer tunMu.Unlock()
	stopTun()
}

// stopTun shuts down the TUN interface and gVisor stack.
// NOTE: Caller MUST hold tunMu lock before calling.
func stopTun() {
	if !tunActive {
		return
	}
	log.Printf("[TUN] stopping TUN...")
	if tunStack != nil {
		tunStack.gStack.Close()
		tunStack = nil
	}
	if tunDevice != nil {
		tunDevice.Close()
		tunDevice = nil
	}
	tunActive = false
	log.Printf("[TUN] TUN stopped")
}

// startLocalListeners starts SOCKS5/HTTP/TCP listeners based on listenAddr.
// Must be called only once (guarded by localListenersStarted).
func startLocalListeners() {
	listeners := strings.Split(listenAddr, ",")
	for _, listenerRule := range listeners {
		rule := strings.TrimSpace(listenerRule)
		if rule == "" {
			continue
		}
		if strings.HasPrefix(rule, "socks5://") {
			go runSOCKS5Listener(rule)
		} else if strings.HasPrefix(rule, "http://") {
			go runHTTPListener(rule)
		} else if strings.HasPrefix(rule, "tcp://") {
			go runTCPListener(rule)
		} else {
			log.Printf("[客户端] 忽略未知协议的监听地址: %s", rule)
		}
	}
}

// runTUNModeIfNeeded 是 TUN 模式的完整入口，仅在 Windows 上被调用。
func runTUNModeIfNeeded() {
	// Always ensure local listeners are started (only once)
	if !localListenersStarted {
		localListenersStarted = true
		startLocalListeners()
	}

	if !tunMode {
		return
	}

	log.Printf("[TUN] 等待 smux 通道就绪（最长 60 秒）...")
	if echPool.WaitForChannelReady(60 * time.Second) {
		log.Printf("[TUN] 通道已就绪，启动 TUN 模式")
	} else {
		log.Printf("[TUN] 通道就绪超时（60s），仍启动 TUN（后续流量将回退直连）")
	}

	physIfaceIndex = detectPhysIfaceIndex()
	if physIfaceIndex > 0 {
		log.Printf("[客户端] 物理网卡接口索引: %d (自身连接将绕过 TUN)", physIfaceIndex)
	} else {
		log.Printf("[客户端] 探测物理网卡索引失败")
	}

	loadGeoIP()
	loadGeoSite()

	if forwardAddr == "" && len(tunnelConfig.Servers) == 0 {
		log.Printf("[TUN] 警告: 未指定服务地址且配置文件无服务器，TUN 可能无法正常工作")
	}

	routes := []string{"0.0.0.0/0"}
	var dnsServers []string
	for _, d := range strings.Split(tunDNS, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			dnsServers = append(dnsServers, d)
		}
	}
	gateways := []string{tunAddress}

	enableIPv6 := ipStrategy != IPStrategyIPv4Only
	if enableIPv6 {
		gateways = append(gateways, "fdfe:dcba:9876::1/126")
		routes = append(routes, "::/0")
	}

	tunCfg := &TunConfig{
		Name:                   tunName,
		MTU:                    tunMTU,
		Gateway:                gateways,
		DNS:                    dnsServers,
		AutoSystemRoutingTable: routes,
	}
	log.Printf("[TUN] 配置: device=%s, mtu=%d, addr=%v, routes=%v",
		tunCfg.Name, tunCfg.MTU, tunCfg.Gateway, tunCfg.AutoSystemRoutingTable)

	if err := StartTun(tunCfg); err != nil {
		log.Printf("[TUN] TUN 启动失败: %v (代理仍可用)", err)
		return
	}
}

func bindSocketToPhysNIC(network string, c syscall.RawConn) error {
	if physIfaceIndex <= 0 {
		return nil
	}
	return c.Control(func(fd uintptr) {
		switch network {
		case "tcp4", "udp4":
			syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, 31, physIfaceIndex)
		case "tcp6", "udp6":
			syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IPV6, 31, physIfaceIndex)
		}
	})
}