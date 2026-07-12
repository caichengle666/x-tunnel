//go:build windows

package main

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"os/exec"
	"strconv"
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

var (
	controlPlaneRouteMu  sync.Mutex
	controlPlaneRouteIPs []string
)

// IsTunActive returns true if TUN is currently running
func IsTunActive() bool {
	tunMu.Lock()
	defer tunMu.Unlock()
	return tunActive
}

func ensureControlPlaneBypass() {
	if physIfaceIndex > 0 {
		return
	}
	physIfaceIndex = detectPhysIfaceIndex()
	if physIfaceIndex > 0 {
		log.Printf("[客户端] 控制面物理网卡接口索引: %d", physIfaceIndex)
	}
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
	log.Printf("[TUN] stopping TUN (gVisor stack may panic, contained)...")
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TUN] stopTun recovered from panic: %v", r)
		}
	}()
	// Close TUN device first (this triggers Wintun to restore routes)
	if tunDevice != nil {
		tunDevice.Close()
		tunDevice = nil
	}
	removeControlPlaneHostRoutes()
	// Close gVisor stack last (may panic on active connections)
	if tunStack != nil {
		tunStack.gStack.Close()
		tunStack = nil
	}
	tunActive = false
	log.Printf("[TUN] TUN stopped")
}

// softStopTun marks TUN as inactive without closing the gVisor stack.
// Used when toggling TUN OFF from the Web UI to avoid crashes.
func softStopTun() {
	if !tunActive {
		return
	}
	log.Printf("[TUN] soft-stopping TUN (preserving gVisor stack)...")
	// Close the TUN device to restore OS routes
	if tunDevice != nil {
		tunDevice.Close()
		tunDevice = nil
	}
	removeControlPlaneHostRoutes()
	// Don't close the gVisor stack - it may panic on active TCP connections
	// The stack will be cleaned up when the process exits
	tunActive = false
	routesRestored()
	log.Printf("[TUN] TUN soft-stopped, routes restored")
}

// restores original routes after TUN is disabled.
func routesRestored() {
	if physIfaceIndex <= 0 {
		return
	}
	// Force reopen the physical NIC to refresh routes
	log.Printf("[TUN] 物理网卡索引 %d, 路由将被 Windows 自动恢复", physIfaceIndex)
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

func installControlPlaneHostRoutes() {
	if physIfaceIndex <= 0 {
		return
	}
	gateway, err := defaultGatewayForInterface(physIfaceIndex)
	if err != nil {
		log.Printf("[TUN] control-plane route skipped: %v", err)
		return
	}
	hosts := controlPlaneHosts()
	added := 0
	seen := map[string]bool{}
	for _, host := range hosts {
		ips, err := resolveControlPlaneIPs(host)
		if err != nil {
			log.Printf("[TUN] control-plane resolve failed %s: %v", host, err)
			continue
		}
		for _, ip := range ips {
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			ipStr := ip4.String()
			if seen[ipStr] {
				continue
			}
			seen[ipStr] = true
			if err := addControlPlaneHostRoute(ipStr, gateway, physIfaceIndex); err != nil {
				log.Printf("[TUN] control-plane route add failed %s via %s if %d: %v", ipStr, gateway, physIfaceIndex, err)
				continue
			}
			added++
		}
	}
	if added > 0 {
		log.Printf("[TUN] installed %d control-plane host routes via %s if %d", added, gateway, physIfaceIndex)
	}
}

func controlPlaneHosts() []string {
	var hosts []string
	addHost := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		host := u.Hostname()
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	addHost(forwardAddr)
	for _, srv := range tunnelConfig.Servers {
		addHost(srv.URL)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}

func defaultGatewayForInterface(ifaceIndex int) (string, error) {
	ps := fmt.Sprintf("(Get-NetRoute -DestinationPrefix '0.0.0.0/0' -InterfaceIndex %d | Sort-Object RouteMetric,InterfaceMetric | Select-Object -First 1 -ExpandProperty NextHop)", ifaceIndex)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return "", err
	}
	gateway := strings.TrimSpace(string(out))
	if gateway == "" || gateway == "0.0.0.0" {
		return "", fmt.Errorf("no default gateway on interface %d", ifaceIndex)
	}
	return gateway, nil
}

func addControlPlaneHostRoute(ip, gateway string, ifaceIndex int) error {
	args := []string{"ADD", ip, "MASK", "255.255.255.255", gateway, "METRIC", "1", "IF", strconv.Itoa(ifaceIndex)}
	if out, err := exec.Command("route", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	controlPlaneRouteMu.Lock()
	controlPlaneRouteIPs = append(controlPlaneRouteIPs, ip)
	controlPlaneRouteMu.Unlock()
	return nil
}

func removeControlPlaneHostRoutes() {
	controlPlaneRouteMu.Lock()
	ips := append([]string(nil), controlPlaneRouteIPs...)
	controlPlaneRouteIPs = nil
	controlPlaneRouteMu.Unlock()
	for _, ip := range ips {
		_ = exec.Command("route", "DELETE", ip).Run()
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

	installControlPlaneHostRoutes()

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
		var sockErr error
		switch network {
		case "tcp4", "udp4":
			sockErr = setUnicastIF(fd, physIfaceIndex, false)
		case "tcp6", "udp6":
			sockErr = setUnicastIF(fd, physIfaceIndex, true)
		}
		if sockErr != nil {
			log.Printf("[TUN] bind physical NIC failed: %v", sockErr)
		}
	})
}

// probeIfaceUDP 测试指定网卡能否访问公网。
// 先测 UDP DNS（223.5.5.5:53），失败再测 TCP 443，避免只拦 UDP 53 的环境误判。
func probeIfaceUDP(ifaceIdx int) bool {
	iface, err := net.InterfaceByIndex(ifaceIdx)
	if err != nil {
		return false
	}
	// 1) UDP DNS
	if conn, err := directDialUDP("223.5.5.5", 53, iface); err == nil {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		query := buildDNSQuery("cloudflare.com", 1)
		_, werr := conn.Write(query)
		buf := make([]byte, 512)
		n, rerr := conn.Read(buf)
		conn.Close()
		if werr == nil && rerr == nil && n >= 12 {
			return true
		}
	}
	// 2) TCP 443 兜底（部分云电脑只放行 TCP）
	if conn, err := directDialTCP("1.1.1.1:443", iface); err == nil {
		conn.Close()
		return true
	}
	return false
}
