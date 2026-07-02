//go:build windows

package main

import (
	"log"
	"strings"
	"syscall"
	"time"
)

// physIfaceIndex 是物理网卡接口索引，用于绕过 TUN 路由。
var physIfaceIndex int = -1

// runTUNModeIfNeeded 是 TUN 模式的完整入口，仅在 Windows 上被调用。
func runTUNModeIfNeeded() {
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

	if forwardAddr == "" {
		log.Fatalf("[TUN] TUN 模式必须指定服务地址 (-f ws:// 或 wss://)")
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

	// 在启动 TUN 之前，先启动本地监听
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
		}
	}

	if err := StartTun(tunCfg); err != nil {
		log.Fatalf("[TUN] TUN 启动失败: %v", err)
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
