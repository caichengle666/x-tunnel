//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const defaultNIC tcpip.NICID = 1

type tunLinkEndpoint struct {
	mtu        uint32
	device     *WindowsTun
	dispatcher stack.NetworkDispatcher
	cancel     context.CancelFunc
	icmpFunc   func(proto tcpip.NetworkProtocolNumber, src, dst net.IP, msg []byte) bool
}

var _ stack.LinkEndpoint = (*tunLinkEndpoint)(nil)

func (e *tunLinkEndpoint) MTU() uint32                             { return e.mtu }
func (e *tunLinkEndpoint) SetMTU(uint32)                           {}
func (e *tunLinkEndpoint) MaxHeaderLength() uint16                 { return 0 }
func (e *tunLinkEndpoint) LinkAddress() tcpip.LinkAddress          { return "" }
func (e *tunLinkEndpoint) SetLinkAddress(tcpip.LinkAddress)        {}
func (e *tunLinkEndpoint) ARPHardwareType() header.ARPHardwareType { return header.ARPHardwareNone }
func (e *tunLinkEndpoint) AddHeader(*stack.PacketBuffer)           {}
func (e *tunLinkEndpoint) ParseHeader(*stack.PacketBuffer) bool    { return true }
func (e *tunLinkEndpoint) Wait()                                   {}
func (e *tunLinkEndpoint) SetOnCloseAction(func())                 {}
func (e *tunLinkEndpoint) Capabilities() stack.LinkEndpointCapabilities {
	return stack.CapabilityRXChecksumOffload
}
func (e *tunLinkEndpoint) IsAttached() bool { return e.dispatcher != nil }
func (e *tunLinkEndpoint) Close() {
	if e.cancel != nil {
		e.cancel()
	}
}
func (e *tunLinkEndpoint) Attach(dispatcher stack.NetworkDispatcher) {
	if e.cancel != nil {
		e.cancel()
	}
	if dispatcher != nil {
		ctx, cancel := context.WithCancel(context.Background())
		go e.dispatch(ctx, dispatcher)
		e.cancel = cancel
	}
	e.dispatcher = dispatcher
}
func (e *tunLinkEndpoint) WritePackets(pktList stack.PacketBufferList) (int, tcpip.Error) {
	var n int
	for _, pkt := range pktList.AsSlice() {
		slices := pkt.AsSlices()
		var total int
		for _, v := range slices {
			total += len(v)
		}
		buf := make([]byte, total)
		pos := 0
		for _, v := range slices {
			pos += copy(buf[pos:], v)
		}
		if err := e.device.WritePacket(buf); err != nil {
			return n, &tcpip.ErrAborted{}
		}
		n++
	}
	return n, nil
}
func (e *tunLinkEndpoint) dispatch(ctx context.Context, dispatcher stack.NetworkDispatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		data, err := e.device.ReadPacket()
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				_, _ = windows.WaitForSingleObject(e.device.ReadWaitEvent(), windows.INFINITE)
				continue
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if len(data) < 1 {
			continue
		}
		version := data[0] >> 4
		switch version {
		case 4:
			if e.handleICMPPacket(header.IPv4ProtocolNumber, data) {
				continue
			}
			dispatcher.DeliverNetworkPacket(header.IPv4ProtocolNumber,
				stack.NewPacketBuffer(stack.PacketBufferOptions{
					Payload:           buffer.MakeWithView(buffer.NewViewWithData(data)),
					IsForwardedPacket: true,
				}))
		case 6:
			if e.handleICMPPacket(header.IPv6ProtocolNumber, data) {
				continue
			}
			dispatcher.DeliverNetworkPacket(header.IPv6ProtocolNumber,
				stack.NewPacketBuffer(stack.PacketBufferOptions{
					Payload:           buffer.MakeWithView(buffer.NewViewWithData(data)),
					IsForwardedPacket: true,
				}))
		}
	}
}

func (e *tunLinkEndpoint) handleICMPPacket(proto tcpip.NetworkProtocolNumber, data []byte) bool {
	if e.icmpFunc == nil {
		return false
	}
	if proto == header.IPv4ProtocolNumber {
		if len(data) < 28 || data[0]>>4 != 4 {
			return false
		}
		ihl := int(data[0]&0x0f) * 4
		if ihl < 20 || len(data) < ihl+8 || data[9] != 1 {
			return false
		}
		frag := binary.BigEndian.Uint16(data[6:8])
		if frag&0x3fff != 0 { // fragmented ICMP is not handled here
			return false
		}
		src := append(net.IP(nil), data[12:16]...)
		dst := append(net.IP(nil), data[16:20]...)
		msg := append([]byte(nil), data[ihl:]...)
		return e.icmpFunc(proto, src, dst, msg)
	}

	if len(data) < 48 || data[0]>>4 != 6 || data[6] != 58 {
		return false
	}
	src := append(net.IP(nil), data[8:24]...)
	dst := append(net.IP(nil), data[24:40]...)
	msg := append([]byte(nil), data[40:]...)
	return e.icmpFunc(proto, src, dst, msg)
}

// ======================== gVisor Stack ========================

type gVisorStack struct {
	gStack  *stack.Stack
	device  *WindowsTun
	endpt   *tunLinkEndpoint
	handler *tunConnHandler
}

type tunConnHandler struct {
	pool       *MultiPool
	blockPorts map[int]struct{}
	gStack     *stack.Stack
	physIface  *net.Interface
	device     *WindowsTun
}

func StartTun(cfg *TunConfig) error {
	log.Printf("[TUN] starting (gVisor, MTU=%d, dev=%s)", cfg.MTU, cfg.Name)
	if err := EnsureWintunLoaded(); err != nil {
		return err
	}
	tun, err := NewTun(cfg)
	if err != nil {
		return err
	}
	ep := &tunLinkEndpoint{mtu: uint32(cfg.MTU), device: tun}
	gStack, err := createGVisorStack(ep)
	if err != nil {
		tun.Close()
		return err
	}
	ts := &gVisorStack{
		gStack: gStack, device: tun, endpt: ep,
		handler: &tunConnHandler{
			pool: echPool, blockPorts: udpBlockPorts, gStack: gStack, device: tun,
		},
	}
	ep.icmpFunc = ts.handler.handleICMPMessage
	physIface := findPhysicalInterface()
	if physIface != nil {
		ts.handler.physIface = physIface
		log.Printf("[TUN] physical interface: %s (idx=%d)", physIface.Name, physIface.Index)
	}
	initRules(directStr, proxyStr, defaultRouteStr)
	tcpFwd := tcp.NewForwarder(gStack, 0, 65535, func(r *tcp.ForwarderRequest) {
		ts.handler.handleTCP(r)
	})
	gStack.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpMgr := newUDPAssocManager(ts.handler)
	gStack.SetTransportProtocolHandler(udp.ProtocolNumber,
		func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
			return udpMgr.onPacket(id, pkt)
		})
	gStack.SetTransportProtocolHandler(icmp.ProtocolNumber4,
		func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
			return ts.handler.handleICMP(header.IPv4ProtocolNumber, id, pkt)
		})
	gStack.SetTransportProtocolHandler(icmp.ProtocolNumber6,
		func(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
			return ts.handler.handleICMP(header.IPv6ProtocolNumber, id, pkt)
		})
	if err := tun.Start(); err != nil {
		gStack.Close()
		tun.Close()
		return err
	}
	log.Printf("[TUN] interface %s up", cfg.Name)
	log.Printf("[TUN] DNS listener on TUN gateways")
	go runDNSListener(cfg, ts.handler)

	// Store TUN state for runtime management
	tunMu.Lock()
	tunActive = true
	tunDevice = tun
	tunStack = ts
	tunMu.Unlock()

	log.Printf("[TUN] TUN fully active (runtime-togglable)")
	return nil
}

func createGVisorStack(ep *tunLinkEndpoint) (*stack.Stack, error) {
	opts := stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6,
		},
		HandleLocal: false,
	}
	gStack := stack.New(opts)
	if err := gStack.CreateNIC(defaultNIC, ep); err != nil {
		return nil, errors.New(err.String())
	}
	gStack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: defaultNIC},
		{Destination: header.IPv6EmptySubnet, NIC: defaultNIC},
	})
	if err := gStack.SetSpoofing(defaultNIC, true); err != nil {
		return nil, errors.New(err.String())
	}
	if err := gStack.SetPromiscuousMode(defaultNIC, true); err != nil {
		return nil, errors.New(err.String())
	}
	cOpt := tcpip.CongestionControlOption("cubic")
	gStack.SetTransportProtocolOption(tcp.ProtocolNumber, &cOpt)
	sOpt := tcpip.TCPSACKEnabled(true)
	gStack.SetTransportProtocolOption(tcp.ProtocolNumber, &sOpt)
	mOpt := tcpip.TCPModerateReceiveBufferOption(true)
	gStack.SetTransportProtocolOption(tcp.ProtocolNumber, &mOpt)
	rOpt := tcpip.TCPRecovery(0)
	gStack.SetTransportProtocolOption(tcp.ProtocolNumber, &rOpt)
	tcpRXBuf := tcpip.TCPReceiveBufferSizeRangeOption{
		Min: tcp.MinBufferSize, Default: tcp.DefaultReceiveBufferSize, Max: 8 << 20,
	}
	gStack.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpRXBuf)
	tcpTXBuf := tcpip.TCPSendBufferSizeRangeOption{
		Min: tcp.MinBufferSize, Default: tcp.DefaultSendBufferSize, Max: 6 << 20,
	}
	gStack.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpTXBuf)
	return gStack, nil
}

// ======================== TCP handling ========================

func (h *tunConnHandler) handleTCP(r *tcp.ForwarderRequest) {
	var wq waiter.Queue
	id := r.ID()
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		r.Complete(true)
		return
	}
	opts := ep.SocketOptions()
	opts.SetKeepAlive(false)
	conn := gonet.NewTCPConn(&wq, ep)
	targetHost := net.IP(id.LocalAddress.AsSlice()).String()
	target := net.JoinHostPort(targetHost, fmt.Sprintf("%d", id.LocalPort))

	go func() {
		defer conn.Close()
		defer r.Complete(false)
		defer ep.Close()
		if h.pool == nil {
			return
		}

		// === Sniff HTTP Host / TLS SNI (like Xray) ===
		initial, sniffedDomain, _ := sniffTCPInitial(conn)
		var proxyTarget string
		if sniffedDomain != "" {
			proxyTarget = net.JoinHostPort(sniffedDomain, fmt.Sprintf("%d", id.LocalPort))
		} else {
			proxyTarget = target
		}

		targetAddr := addrToNetip(id.LocalAddress)
		routeDecision, routeReason := routeTCP(sniffedDomain, targetHost)

		// === Routing decision ===
		if h.physIface != nil && routeDecision == DecisionDirect {
			direct, logTarget, derr := h.dialDirectTCPWithFallback(target, targetAddr, sniffedDomain, id.LocalPort)
			if derr == nil {
				log.Printf("[TUN][TCP][direct] %s -> %s (%s)", conn.RemoteAddr(), logTarget, routeReason)
				if initial != nil {
					direct.Write(initial)
				}
				directProxyStream(conn, direct)
				return
			}
			log.Printf("[TUN][TCP][direct] %s -> %s (%s, direct failed: %v)", conn.RemoteAddr(), logTarget, routeReason, derr)
		}
		// === Proxy outlet ===
		stream, _, channelID, perr := h.pool.openTCPStream(proxyTarget)
		if perr != nil {
			log.Printf("[TUN][TCP][proxy] %s -> %s (%s, open failed: %v)", conn.RemoteAddr(), proxyTarget, routeReason, perr)
			return
		}
		log.Printf("[TUN][TCP][proxy] %s -> %s (%s, ch=%d)", conn.RemoteAddr(), proxyTarget, routeReason, channelID)
		if initial != nil {
			stream.Write(initial)
		}
		proxyConnStream(conn, stream)
	}()
}

func directFamilyFallbackAllowed(rrType uint16) bool {
	switch rrType {
	case dnsTypeA:
		return ipStrategy != IPStrategyIPv6Only
	case dnsTypeAAAA:
		return ipStrategy != IPStrategyIPv4Only
	default:
		return false
	}
}

func shouldTryAlternateFamilyAfterError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{
		"no local ip",
		"no global ipv6",
		"no ipv4",
		"network is unreachable",
		"unreachable",
		"cannot assign requested address",
		"requested address is not valid in its context",
		"address not available",
		"address unavailable",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func appendUniqueStrings(dst []string, seen map[string]bool, values []string) []string {
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		dst = append(dst, v)
	}
	return dst
}

func (h *tunConnHandler) tryDirectCandidates(domain string, port uint16, rrType uint16) (net.Conn, error) {
	if !directFamilyFallbackAllowed(rrType) {
		return nil, fmt.Errorf("%s fallback disabled by -ips", dnsTypeName(rrType))
	}

	seen := make(map[string]bool)
	candidates := appendUniqueStrings(nil, seen, lookupIPsByDomainFamily(domain, rrType == dnsTypeAAAA))
	var lastErr error
	for _, ip := range candidates {
		target := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
		conn, err := directDialTCP(target, h.physIface)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}

	refreshed := appendUniqueStrings(nil, seen, refreshDirectDomainFamily(domain, rrType))
	for _, ip := range refreshed {
		target := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
		conn, err := directDialTCP(target, h.physIface)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}

	if len(candidates) == 0 && len(refreshed) == 0 {
		return nil, fmt.Errorf("no %s candidate", dnsTypeName(rrType))
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%s fallback failed", dnsTypeName(rrType))
}

func (h *tunConnHandler) dialDirectTCPWithFallback(target string, targetAddr netip.Addr, sniffedDomain string, port uint16) (net.Conn, string, error) {
	logTarget := target
	if !targetAddr.IsValid() {
		conn, err := directDialTCP(target, h.physIface)
		return conn, logTarget, err
	}

	isIPv6 := targetAddr.Is6()
	if isIPv6 && !hasGlobal6(h.physIface) {
		if sniffedDomain == "" {
			return nil, logTarget, fmt.Errorf("no IPv6 route, no domain for A fallback")
		}
		conn, err := h.tryDirectCandidates(sniffedDomain, port, dnsTypeA)
		if err != nil {
			return nil, sniffedDomain, fmt.Errorf("no IPv6 route, A fallback failed: %w", err)
		}
		return conn, sniffedDomain, nil
	}
	if !isIPv6 && !hasIPv4(h.physIface) {
		if sniffedDomain == "" {
			return nil, logTarget, fmt.Errorf("no IPv4 route, no domain for AAAA fallback")
		}
		conn, err := h.tryDirectCandidates(sniffedDomain, port, dnsTypeAAAA)
		if err != nil {
			return nil, sniffedDomain, fmt.Errorf("no IPv4 route, AAAA fallback failed: %w", err)
		}
		return conn, sniffedDomain, nil
	}

	conn, err := directDialTCP(target, h.physIface)
	if err == nil {
		return conn, logTarget, nil
	}

	// 直连失败时重新探测物理网卡（处理 Wifi→有线切换等情况）
	if newIface := findPhysicalInterface(); newIface != nil && newIface.Index != h.physIface.Index {
		log.Printf("[TUN] 物理网卡变更: %s(idx=%d)->%s(idx=%d)",
			h.physIface.Name, h.physIface.Index, newIface.Name, newIface.Index)
		h.physIface = newIface
		conn, err := directDialTCP(target, h.physIface)
		if err == nil {
			return conn, logTarget, nil
		}
	}

	if sniffedDomain == "" || !shouldTryAlternateFamilyAfterError(err) {
		return nil, logTarget, err
	}

	if isIPv6 {
		conn, fallbackErr := h.tryDirectCandidates(sniffedDomain, port, dnsTypeA)
		if fallbackErr != nil {
			return nil, sniffedDomain, fmt.Errorf("IPv6 direct failed: %v; A fallback failed: %w", err, fallbackErr)
		}
		return conn, sniffedDomain, nil
	}

	conn, fallbackErr := h.tryDirectCandidates(sniffedDomain, port, dnsTypeAAAA)
	if fallbackErr != nil {
		return nil, sniffedDomain, fmt.Errorf("IPv4 direct failed: %v; AAAA fallback failed: %w", err, fallbackErr)
	}
	return conn, sniffedDomain, nil
}

// ======================== UDP (FullCone) ========================

type udpAssoc struct {
	owner      *udpAssocManager
	key        udpAssocKey
	ch         chan []byte
	activity   chan struct{} // signals any outbound data activity (for idle timeout)
	closeOnce  sync.Once
	closed     chan struct{}
	directConn *net.UDPConn // non-nil for direct UDP (bypass proxy)
}

type udpAssocKey struct {
	src netip.AddrPort
	dst netip.AddrPort
}

func (a *udpAssoc) touch() {
	if a == nil || a.activity == nil {
		return
	}
	select {
	case a.activity <- struct{}{}:
	default:
	}
}

type udpAssocManager struct {
	handler *tunConnHandler
	assocs  map[udpAssocKey]*udpAssoc
	mu      sync.RWMutex
}

func newUDPAssocManager(h *tunConnHandler) *udpAssocManager {
	return &udpAssocManager{handler: h, assocs: make(map[udpAssocKey]*udpAssoc)}
}

func (m *udpAssocManager) onPacket(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
	src := netip.AddrPortFrom(
		addrToNetip(id.RemoteAddress),
		uint16(id.RemotePort))
	dst := netip.AddrPortFrom(
		addrToNetip(id.LocalAddress),
		uint16(id.LocalPort))

	data := append([]byte(nil), pkt.Data().AsRange().ToSlice()...)

	// DNS hijack: intercept UDP port 53 queries and process them locally
	if dst.Port() == 53 {
		reply := ProcessDNSQuery(data)
		if reply != nil {
			if err := m.writeUDPReplyToTUN(src, dst, reply); err != nil {
				log.Printf("[TUN][UDP][dns] %s -> %s (write back failed: %v)", src, dst, err)
			}
		} else {
			log.Printf("[TUN][UDP][dns] %s -> %s (query failed)", src, dst)
		}
		return true
	}

	// Block QUIC ports (UDP 443, 8443 etc.) to force TCP fallback.
	// We intentionally don't try to sniff QUIC SNI here: browser / quic-go will
	// retry via TCP, and the existing TCP TLS SNI sniffing will route by domain.
	if _, blocked := m.handler.blockPorts[int(dst.Port())]; blocked {
		log.Printf("[TUN][UDP][blocked] %s -> %s (force-tcp-fallback)", src, dst)
		return true
	}

	// routing: direct UDP (IP rules only; UDP has no reliable domain without QUIC parsing)
	useDirectAssoc := false
	routeReason := "default"
	if m.handler.physIface != nil {
		goDirect, rule := shouldDirectUDP(dst.Addr().String())
		if rule != "" && rule != "default" {
			routeReason = "ip:" + rule
		}
		if goDirect {
			isIPv6 := dst.Addr().Is6()
			if isIPv6 && !hasGlobal6(m.handler.physIface) {
				// No IPv6 route on physical NIC, fall through to proxy
			} else {
				useDirectAssoc = true
			}
		}
	}

	key := udpAssocKey{src: src, dst: dst}
	m.mu.RLock()
	assoc, ok := m.assocs[key]
	m.mu.RUnlock()
	if ok {
		select {
		case <-assoc.closed:
			// association is closing, reopen below
		default:
			select {
			case <-assoc.closed:
				// association is closing, reopen below
			case assoc.ch <- data:
				return true
			default:
				log.Printf("[TUN][UDP][drop] %s -> %s (queue full)", src, dst)
				return true
			}
		}
	}
	if useDirectAssoc {
		log.Printf("[TUN][UDP][direct] %s -> %s (%s)", src, dst, routeReason)
		go m.startDirectAssoc(key, data)
	} else {
		log.Printf("[TUN][UDP][proxy] %s -> %s (%s)", src, dst, routeReason)
		go m.startAssoc(key, data)
	}
	return true
}

// startDirectAssoc creates a persistent UDP association that bypasses the
// proxy and sends packets directly through the physical NIC.
func (m *udpAssocManager) startDirectAssoc(key udpAssocKey, firstData []byte) {
	assoc := &udpAssoc{
		owner:    m,
		key:      key,
		ch:       make(chan []byte, 256),
		activity: make(chan struct{}, 1),
		closed:   make(chan struct{}),
	}
	m.mu.Lock()
	if existing, ok := m.assocs[key]; ok {
		m.mu.Unlock()
		select {
		case <-existing.closed:
		default:
			select {
			case <-existing.closed:
			case existing.ch <- firstData:
			default:
				log.Printf("[TUN][UDP][drop] %s -> %s (direct queue full)", key.src, key.dst)
			}
		}
		return
	}
	m.assocs[key] = assoc
	m.mu.Unlock()

	host := key.dst.Addr().String()
	port := int(key.dst.Port())
	conn, err := directDialUDP(host, port, m.handler.physIface)
	if err != nil {
		log.Printf("[TUN][UDP][direct] %s -> %s (dial failed: %v)", key.src, key.dst, err)
		m.cleanup(assoc)
		return
	}
	assoc.directConn = conn

	// Send first packet
	if _, err := conn.Write(firstData); err != nil {
		log.Printf("[TUN][UDP][direct] %s -> %s (write failed: %v)", key.src, key.dst, err)
		m.cleanup(assoc)
		return
	}
	assoc.touch()

	// Goroutine: forward packets from TUN to direct UDP
	go func() {
		defer m.cleanup(assoc)
		for {
			select {
			case <-assoc.closed:
				return
			default:
			}
			select {
			case <-assoc.closed:
				return
			case data := <-assoc.ch:
				if _, err := conn.Write(data); err != nil {
					return
				}
				assoc.touch()
			}
		}
	}()

	// Goroutine: read replies from direct UDP and write back to TUN
	go func() {
		defer m.cleanup(assoc)
		buf := make([]byte, 65535)
		for {
			select {
			case <-assoc.closed:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return
			}
			if n > 0 {
				assoc.touch()
				if err := m.writeUDPReplyToTUN(key.src, key.dst, buf[:n]); err != nil {
					log.Printf("[TUN][UDP][direct] %s -> %s (write back failed: %v)", key.dst, key.src, err)
				}
			}
		}
	}()

	// Idle timeout: close after 30s of inactivity
	go func() {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-assoc.closed:
				return
			case <-assoc.activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(30 * time.Second)
			case <-timer.C:
				m.cleanup(assoc)
				return
			}
		}
	}()
}

func (m *udpAssocManager) startAssoc(key udpAssocKey, firstData []byte) {
	assoc := &udpAssoc{
		owner:    m,
		key:      key,
		ch:       make(chan []byte, 1024),
		activity: make(chan struct{}, 1),
		closed:   make(chan struct{}),
	}
	m.mu.Lock()
	if existing, ok := m.assocs[key]; ok {
		m.mu.Unlock()
		select {
		case <-existing.closed:
		default:
			select {
			case <-existing.closed:
			case existing.ch <- firstData:
			default:
				log.Printf("[TUN][UDP][drop] %s -> %s (proxy queue full)", key.src, key.dst)
			}
		}
		return
	}
	m.assocs[key] = assoc
	m.mu.Unlock()
	if m.handler.pool == nil {
		m.cleanup(assoc)
		return
	}
	target := net.JoinHostPort(key.dst.Addr().String(), fmt.Sprintf("%d", key.dst.Port()))
	stream, _, _, err := m.handler.pool.openUDPStream(target)
	if err != nil {
		log.Printf("[TUN][UDP][proxy] %s -> %s (open failed: %v)", key.src, key.dst, err)
		m.cleanup(assoc)
		return
	}

	// Send first UDP packet using writeChunk format: [len(2 bytes)][data]
	if err := writeChunk(stream, firstData); err != nil {
		log.Printf("[TUN][UDP][proxy] %s -> %s (write failed: %v)", key.src, key.dst, err)
		m.cleanup(assoc)
		return
	}
	assoc.touch()

	// Goroutine: forward UDP packets from TUN to stream
	go func() {
		defer m.cleanup(assoc)
		defer stream.Close()
		for {
			select {
			case <-assoc.closed:
				return
			default:
			}
			select {
			case <-assoc.closed:
				return
			case data := <-assoc.ch:
				if err := writeChunk(stream, data); err != nil {
					return
				}
				assoc.touch()
			}
		}
	}()

	// Goroutine: receive UDP packets from stream and write back to TUN
	go func() {
		defer m.cleanup(assoc)
		defer stream.Close()
		for {
			addrStr, payload, err := readUDPReply(stream)
			if err != nil {
				return
			}
			replyDst := key.dst
			if ap, ok := parseUDPReplyAddr(addrStr); ok {
				replyDst = ap
			}
			assoc.touch()
			if err := m.writeUDPReplyToTUN(key.src, replyDst, payload); err != nil {
				log.Printf("[TUN][UDP][proxy] %s -> %s (write back failed: %v)", replyDst, key.src, err)
			}
		}
	}()

	// Idle timeout: close after 60s of inactivity (longer than direct, proxy has more latency)
	go func() {
		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-assoc.closed:
				return
			case <-assoc.activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(60 * time.Second)
			case <-timer.C:
				m.cleanup(assoc)
				return
			}
		}
	}()
}

// writeUDPReplyToTUN constructs an IP+UDP packet and writes it back to TUN
func (m *udpAssocManager) writeUDPReplyToTUN(src, dst netip.AddrPort, payload []byte) error {
	// Determine IP version
	srcIP := src.Addr()
	dstIP := dst.Addr()
	isIPv6 := srcIP.Is6() || dstIP.Is6()

	// UDP header: 8 bytes
	// [src_port(2)][dst_port(2)][length(2)][checksum(2)]
	udpLen := 8 + len(payload)
	udpHdr := make([]byte, 8)
	binary.BigEndian.PutUint16(udpHdr[0:2], dst.Port()) // src port in reply = dst port in request
	binary.BigEndian.PutUint16(udpHdr[2:4], src.Port()) // dst port in reply = src port in request
	binary.BigEndian.PutUint16(udpHdr[4:6], uint16(udpLen))
	binary.BigEndian.PutUint16(udpHdr[6:8], 0)

	var packet []byte

	if isIPv6 {
		// IPv6 header: 40 bytes
		// [version(4)+traffic_class(8)+flow_label(20)][payload_length(16)][next_header(8)=17][hop_limit(8)][src(16)][dst(16)]
		ipv6Hdr := make([]byte, 40)
		ipv6Hdr[0] = 0x60 // version 6
		binary.BigEndian.PutUint16(ipv6Hdr[4:6], uint16(udpLen))
		ipv6Hdr[6] = 17                       // next header = UDP
		ipv6Hdr[7] = 64                       // hop limit
		copy(ipv6Hdr[8:24], dstIP.AsSlice())  // src = dst of original
		copy(ipv6Hdr[24:40], srcIP.AsSlice()) // dst = src of original
		binary.BigEndian.PutUint16(udpHdr[6:8], udpChecksum(udpHdr, payload, dstIP, srcIP))
		packet = append(ipv6Hdr, udpHdr...)
		packet = append(packet, payload...)
	} else {
		// IPv4 header: 20 bytes (no options)
		// [version(4)+ihl(4)=0x45][tos(8)][total_length(16)][id(16)][flags+frag(16)][ttl(8)][protocol(8)=17][checksum(16)][src(4)][dst(4)]
		ipHdr := make([]byte, 20)
		ipHdr[0] = 0x45 // version 4, IHL 5
		totalLen := 20 + udpLen
		binary.BigEndian.PutUint16(ipHdr[2:4], uint16(totalLen))
		ipHdr[8] = 64                       // TTL
		ipHdr[9] = 17                       // protocol = UDP
		copy(ipHdr[12:16], dstIP.AsSlice()) // src = dst of original
		copy(ipHdr[16:20], srcIP.AsSlice()) // dst = src of original
		binary.BigEndian.PutUint16(udpHdr[6:8], udpChecksum(udpHdr, payload, dstIP, srcIP))
		// Calculate IP checksum
		binary.BigEndian.PutUint16(ipHdr[10:12], ipChecksum(ipHdr))
		packet = append(ipHdr, udpHdr...)
		packet = append(packet, payload...)
	}

	return m.handler.device.WritePacket(packet)
}

func udpChecksum(udpHdr, payload []byte, src, dst netip.Addr) uint16 {
	var sum uint32
	addBytes := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}

	src = src.Unmap()
	dst = dst.Unmap()
	addBytes(src.AsSlice())
	addBytes(dst.AsSlice())
	if src.Is6() || dst.Is6() {
		var pseudo [8]byte
		binary.BigEndian.PutUint32(pseudo[0:4], uint32(len(udpHdr)+len(payload)))
		pseudo[7] = 17
		addBytes(pseudo[:])
	} else {
		var pseudo [4]byte
		pseudo[1] = 17
		binary.BigEndian.PutUint16(pseudo[2:4], uint16(len(udpHdr)+len(payload)))
		addBytes(pseudo[:])
	}
	addBytes(udpHdr)
	addBytes(payload)
	for (sum >> 16) != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	csum := ^uint16(sum)
	if csum == 0 {
		csum = 0xFFFF
	}
	return csum
}

// ipChecksum calculates the IP header checksum
func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i < len(hdr); i += 2 {
		if i+1 < len(hdr) {
			sum += uint32(hdr[i])<<8 + uint32(hdr[i+1])
		} else {
			sum += uint32(hdr[i]) << 8
		}
	}
	for (sum >> 16) != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return uint16(^sum)
}

func (m *udpAssocManager) cleanup(a *udpAssoc) {
	a.closeOnce.Do(func() {
		close(a.closed)
		if a.directConn != nil {
			a.directConn.Close()
		}
		m.mu.Lock()
		delete(m.assocs, a.key)
		m.mu.Unlock()
	})
}

func parseUDPReplyAddr(addrStr string) (netip.AddrPort, bool) {
	if ap, err := netip.ParseAddrPort(addrStr); err == nil {
		return ap, true
	}
	host, portStr, err := net.SplitHostPort(addrStr)
	if err != nil {
		return netip.AddrPort{}, false
	}
	port, err := net.LookupPort("udp", portStr)
	if err != nil {
		return netip.AddrPort{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}

func addrToNetip(a tcpip.Address) netip.Addr {
	b := a.AsSlice()
	switch len(b) {
	case 4:
		if addr, ok := netip.AddrFromSlice(b); ok {
			return addr.Unmap()
		}
	case 16:
		if addr, ok := netip.AddrFromSlice(b); ok {
			if addr.Is4In6() {
				return addr.Unmap()
			}
			return addr
		}
	default:
		// gVisor may return addresses with unusual lengths (e.g. 0 or padded).
		// Try AddrFromSlice as a last resort.
		if addr, ok := netip.AddrFromSlice(b); ok {
			return addr.Unmap()
		}
	}
	return netip.Addr{}
}

func (h *tunConnHandler) handleICMP(proto tcpip.NetworkProtocolNumber, id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
	// gVisor keeps the ICMP header in TransportHeader and the remaining body in Data.
	// Windows raw sockets need the full ICMP message: type/code/checksum/id/seq + payload.
	th := pkt.TransportHeader().Slice()
	body := pkt.Data().AsRange().ToSlice()
	data := make([]byte, 0, len(th)+len(body))
	data = append(data, th...)
	data = append(data, body...)
	if len(data) == 0 {
		return true
	}

	dst := net.IP(id.LocalAddress.AsSlice())
	src := net.IP(id.RemoteAddress.AsSlice())
	return h.handleICMPMessage(proto, src, dst, data)
}

func (h *tunConnHandler) handleICMPMessage(proto tcpip.NetworkProtocolNumber, src, dst net.IP, data []byte) bool {
	if h.physIface == nil || h.device == nil {
		return false
	}
	if len(data) == 0 {
		return true
	}
	isIPv6 := proto == header.IPv6ProtocolNumber

	// Only forward ICMP echo request (IPv4 type 8, IPv6 type 128).
	if isIPv6 {
		if len(data) < 8 || data[0] != 128 {
			return true
		}
	} else {
		if len(data) < 8 || data[0] != 8 {
			return true
		}
	}

	log.Printf("[TUN][ICMP][direct] %s -> %s", src, dst)
	go func() {
		reply, err := directSendICMP(h.physIface, src, dst, data, isIPv6)
		if err != nil {
			log.Printf("[TUN][ICMP][direct] %s -> %s (failed: %v)", src, dst, err)
			return
		}
		if len(reply) == 0 {
			return
		}
		// icmp reply received
		if err := h.device.WritePacket(reply); err != nil {
			log.Printf("[TUN][ICMP][direct] %s -> %s (reply write failed: %v)", dst, src, err)
		}
	}()
	return true
}

// directSendICMP sends one ICMP echo request through the physical interface and
// returns a raw IP packet suitable for writing back to the TUN device.
func directSendICMP(iface *net.Interface, tunSrc, dst net.IP, request []byte, isIPv6 bool) ([]byte, error) {
	localIP, err := getLocalIPFromInterface(iface, isIPv6)
	if err != nil {
		return nil, fmt.Errorf("no local IP on %s: %w", iface.Name, err)
	}

	domain := windows.AF_INET
	proto := windows.IPPROTO_ICMP
	if isIPv6 {
		domain = windows.AF_INET6
		proto = windows.IPPROTO_ICMPV6
	}

	fd, err := windows.Socket(domain, windows.SOCK_RAW, proto)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}
	defer windows.Closesocket(fd)

	if err := setUnicastIF(uintptr(fd), iface.Index, isIPv6); err != nil {
		return nil, fmt.Errorf("set unicast if: %w", err)
	}

	// Bind source to the physical adapter address, never the TUN address.
	if isIPv6 {
		var sa windows.SockaddrInet6
		copy(sa.Addr[:], localIP.To16())
		if err := windows.Bind(fd, &sa); err != nil {
			return nil, fmt.Errorf("bind: %w", err)
		}
	} else {
		var sa windows.SockaddrInet4
		copy(sa.Addr[:], localIP.To4())
		if err := windows.Bind(fd, &sa); err != nil {
			return nil, fmt.Errorf("bind: %w", err)
		}
	}

	timeout := windows.Timeval{Sec: 3}
	_ = windows.SetsockoptTimeval(fd, windows.SOL_SOCKET, windows.SO_RCVTIMEO, &timeout)

	if isIPv6 {
		var sa windows.SockaddrInet6
		copy(sa.Addr[:], dst.To16())
		if err := windows.Sendto(fd, request, 0, &sa); err != nil {
			return nil, fmt.Errorf("sendto: %w", err)
		}
	} else {
		var sa windows.SockaddrInet4
		copy(sa.Addr[:], dst.To4())
		if err := windows.Sendto(fd, request, 0, &sa); err != nil {
			return nil, fmt.Errorf("sendto: %w", err)
		}
	}

	wantIDSeq := []byte(nil)
	if len(request) >= 8 {
		wantIDSeq = append([]byte(nil), request[4:8]...)
	}

	buf := make([]byte, 65535)
	for {
		n, _, err := windows.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("recvfrom: %w", err)
		}
		if n <= 0 {
			continue
		}
		raw := append([]byte(nil), buf[:n]...)
		pkt, icmpMsg := normalizeICMPReplyPacket(raw, tunSrc, dst, isIPv6)
		if len(icmpMsg) < 8 {
			continue
		}
		if isIPv6 {
			if icmpMsg[0] != 129 { // Echo Reply
				continue
			}
		} else {
			if icmpMsg[0] != 0 { // Echo Reply
				continue
			}
		}
		if len(wantIDSeq) == 4 && !bytes.Equal(icmpMsg[4:8], wantIDSeq) {
			continue
		}
		return pkt, nil
	}
}

func normalizeICMPReplyPacket(raw []byte, tunSrc, dst net.IP, isIPv6 bool) ([]byte, []byte) {
	if isIPv6 {
		if len(raw) >= 40 && raw[0]>>4 == 6 {
			copy(raw[24:40], tunSrc.To16())
			icmpMsg := raw[40:]
			fixICMPv6Checksum(icmpMsg, raw[8:24], raw[24:40])
			return raw, icmpMsg
		}
		// Some Windows raw IPv6 sockets return only the ICMPv6 payload.
		pkt := make([]byte, 40+len(raw))
		pkt[0] = 0x60
		binary.BigEndian.PutUint16(pkt[4:6], uint16(len(raw)))
		pkt[6] = 58 // ICMPv6
		pkt[7] = 64
		copy(pkt[8:24], dst.To16())
		copy(pkt[24:40], tunSrc.To16())
		copy(pkt[40:], raw)
		fixICMPv6Checksum(pkt[40:], pkt[8:24], pkt[24:40])
		return pkt, pkt[40:]
	}

	if len(raw) >= 20 && raw[0]>>4 == 4 {
		ihl := int(raw[0]&0x0f) * 4
		if ihl >= 20 && len(raw) >= ihl+8 {
			copy(raw[16:20], tunSrc.To4())
			raw[10], raw[11] = 0, 0
			binary.BigEndian.PutUint16(raw[10:12], ipChecksum(raw[:ihl]))
			return raw, raw[ihl:]
		}
	}
	// Some raw sockets return only the ICMP payload. Build an IPv4 packet.
	pkt := make([]byte, 20+len(raw))
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
	pkt[8] = 64
	pkt[9] = 1 // ICMP
	copy(pkt[12:16], dst.To4())
	copy(pkt[16:20], tunSrc.To4())
	binary.BigEndian.PutUint16(pkt[10:12], ipChecksum(pkt[:20]))
	copy(pkt[20:], raw)
	return pkt, pkt[20:]
}

func fixICMPv6Checksum(icmpMsg []byte, src, dst []byte) {
	if len(icmpMsg) < 4 || len(src) != 16 || len(dst) != 16 {
		return
	}
	icmpMsg[2], icmpMsg[3] = 0, 0
	var sum uint32
	add16 := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	add16(src)
	add16(dst)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(icmpMsg)))
	add16(l[:])
	sum += 58 // next header ICMPv6
	add16(icmpMsg)
	for (sum >> 16) != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	binary.BigEndian.PutUint16(icmpMsg[2:4], ^uint16(sum))
}
