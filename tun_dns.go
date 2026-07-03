//go:build windows

package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	dnsTypeA    uint16 = 1
	dnsTypeAAAA uint16 = 28
)

// DNSHandler manages TUN DNS processing
type DNSHandler struct {
	handler    *tunConnHandler
	physIface  *net.Interface
	ipStrategy byte
	pool       *ECHPool

	dnsMu          sync.Mutex
	dnsServers     []string
	dnsServersTime time.Time
}

var dnsHandlerInstance *DNSHandler

func newDNSHandler(handler *tunConnHandler) *DNSHandler {
	dnsHandlerInstance = &DNSHandler{
		handler:    handler,
		physIface:  handler.physIface,
		ipStrategy: ipStrategy,
		pool:       handler.pool,
	}
	return dnsHandlerInstance
}

// ProcessDNSQuery processes a DNS query packet and returns the response.
func ProcessDNSQuery(q []byte) []byte {
	if dnsHandlerInstance == nil {
		return nil
	}
	t0 := time.Now()
	domain := extractDNSName(q)
	if domain == "?" || domain == "." {
		return nil
	}

	var reply []byte

	// Fast path: check DNS cache first
	if cached := lookupDNSCache(domain, q); cached != nil {
		cached[0], cached[1] = q[0], q[1] // match query ID
		cached = dnsHandlerInstance.applyIPStrategyToDNS(domain, q, cached)
		cached[0], cached[1] = q[0], q[1]
		logDNSResult(domain, q, cached, "cache", time.Since(t0))
		return cached
	}

	decision, matchedRule := dnsRouteForDomain(domain)
	route := dnsRouteLabel(decision, matchedRule)
	if decision == DecisionDirect {
		reply = dnsHandlerInstance.resolveLocal(q)
		if reply != nil {
			route = dnsRouteLabel(DecisionDirect, matchedRule)
		}
	}
	if reply == nil {
		if decision == DecisionDirect {
			route = "proxy:fallback-direct:" + matchedRule
		}
		reply = dnsHandlerInstance.resolveRemote(q)
	}
	if reply == nil {
		qEnd := findQuestionEnd(q)
		sf := make([]byte, qEnd)
		copy(sf, q[:qEnd])
		sf[2] = 0x81
		sf[3] = 0x82
		for i := 6; i < 12; i += 2 {
			sf[i] = 0
			sf[i+1] = 0
		}
		reply = sf
		logDNSServFail(domain, q, route, time.Since(t0))
	} else {
		reply = dnsHandlerInstance.applyIPStrategyToDNS(domain, q, reply)
		reply[0], reply[1] = q[0], q[1]
		cacheDNSReply(domain, reply)
		logDNSResult(domain, q, reply, route, time.Since(t0))
	}
	return reply
}

func findQuestionEnd(q []byte) int {
	qEnd := 12
	for qEnd < len(q) {
		if q[qEnd] == 0 {
			qEnd += 5
			break
		}
		if q[qEnd]&0xC0 == 0xC0 {
			qEnd += 6
			break
		}
		qEnd += int(q[qEnd]) + 1
	}
	return qEnd
}

// runDNSListener starts DNS listeners on TUN gateway addresses
func runDNSListener(cfg *TunConfig, handler *tunConnHandler) {
	dnsH := newDNSHandler(handler)
	seen := make(map[string]bool)
	for _, gw := range cfg.Gateway {
		host := strings.TrimSpace(gw)
		if host == "" {
			continue
		}
		if p, err := netip.ParsePrefix(host); err == nil {
			host = p.Addr().String()
		} else if strings.Contains(host, "/") {
			host = strings.Split(host, "/")[0]
		}
		addr := net.JoinHostPort(host, "53")
		if seen[addr] {
			continue
		}
		seen[addr] = true
		go dnsH.start(addr)
	}
}

func (d *DNSHandler) start(addr string) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Printf("[DNS] resolve %s failed: %v", addr, err)
		return
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		log.Printf("[DNS] listen %s failed: %v", addr, err)
		return
	}
	defer c.Close()
	log.Printf("[DNS] listener started %s", addr)

	b := make([]byte, 1500)
	for {
		n, ca, err := c.ReadFromUDP(b)
		if err != nil || n < 12 {
			continue
		}
		q := make([]byte, n)
		copy(q, b[:n])
		go d.handle(c, q, ca)
	}
}

func (d *DNSHandler) handle(c *net.UDPConn, q []byte, ca *net.UDPAddr) {
	t0 := time.Now()
	domain := extractDNSName(q)
	if domain == "?" || domain == "." {
		return
	}

	var reply []byte

	// Fast path: check DNS cache first
	if cached := lookupDNSCache(domain, q); cached != nil {
		cached[0], cached[1] = q[0], q[1]
		cached = d.applyIPStrategyToDNS(domain, q, cached)
		cached[0], cached[1] = q[0], q[1]
		c.WriteToUDP(cached, ca)
		logDNSResult(domain, q, cached, "cache", time.Since(t0))
		return
	}

	decision, matchedRule := dnsRouteForDomain(domain)
	route := dnsRouteLabel(decision, matchedRule)
	if decision == DecisionDirect {
		reply = d.resolveLocal(q)
		if reply != nil {
			route = dnsRouteLabel(DecisionDirect, matchedRule)
		}
	}
	if reply == nil {
		if decision == DecisionDirect {
			route = "proxy:fallback-direct:" + matchedRule
		}
		reply = d.resolveRemote(q)
	}
	if reply == nil {
		qEnd := findQuestionEnd(q)
		sf := make([]byte, qEnd)
		copy(sf, q[:qEnd])
		sf[2] = 0x81
		sf[3] = 0x82
		for i := 6; i < 12; i += 2 {
			sf[i] = 0
			sf[i+1] = 0
		}
		c.WriteToUDP(sf, ca)
		logDNSServFail(domain, q, route, time.Since(t0))
		return
	}

	reply = d.applyIPStrategyToDNS(domain, q, reply)
	reply[0], reply[1] = q[0], q[1]
	cacheDNSReply(domain, reply)
	c.WriteToUDP(reply, ca)
	logDNSResult(domain, q, reply, route, time.Since(t0))
}

func (d *DNSHandler) applyIPStrategyToDNS(domain string, q, reply []byte) []byte {
	switch d.ipStrategy {
	case IPStrategyIPv4Only:
		return filterDNSReply(reply, IPStrategyIPv4Only)
	case IPStrategyIPv6Only:
		return filterDNSReply(reply, IPStrategyIPv6Only)
	}
	return reply
}

func (d *DNSHandler) domainHasRecord(domain string, originalQuery []byte, rrType uint16) bool {
	q := cloneDNSQueryWithType(originalQuery, rrType)
	if q == nil {
		return false
	}
	resp := d.resolveForPolicy(domain, q)
	return dnsReplyHasType(resp, rrType)
}

func (d *DNSHandler) resolveForPolicy(domain string, q []byte) []byte {
	decision, _ := dnsRouteForDomain(domain)
	if decision == DecisionDirect {
		if reply := d.resolveLocal(q); reply != nil {
			return reply
		}
	}
	return d.resolveRemote(q)
}

// resolveLocal sends DNS query through physical NIC to system DNS servers.
// Queries all servers concurrently and returns the first valid response.
func (d *DNSHandler) resolveLocal(q []byte) []byte {
	dnsServers := d.getSystemDNSServersCached()
	if len(dnsServers) == 0 {
		return nil
	}
	type result struct {
		data []byte
	}
	ch := make(chan result, len(dnsServers))
	for _, srv := range dnsServers {
		go func(srv string) {
			host, portStr, err := net.SplitHostPort(srv)
			if err != nil {
				ch <- result{}
				return
			}
			port := 53
			if portStr != "" {
				fmt.Sscanf(portStr, "%d", &port)
			}
			conn, err := directDialUDP(host, port, d.physIface)
			if err != nil {
				ch <- result{}
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write(q); err != nil {
				ch <- result{}
				return
			}
			resp := make([]byte, 1500)
			n, err := conn.Read(resp)
			if err == nil && n > 12 {
				ch <- result{resp[:n]}
				return
			}
			ch <- result{}
		}(srv)
	}
	// Wait for first valid response or all to fail
	for range dnsServers {
		r := <-ch
		if r.data != nil {
			return r.data
		}
	}
	return nil
}

func (d *DNSHandler) getSystemDNSServersCached() []string {
	d.dnsMu.Lock()
	defer d.dnsMu.Unlock()
	if !d.dnsServersTime.IsZero() && time.Since(d.dnsServersTime) < 30*time.Second {
		return append([]string(nil), d.dnsServers...)
	}
	servers := getSystemDNSServers(d.physIface)
	d.dnsServers = append([]string(nil), servers...)
	d.dnsServersTime = time.Now()
	return servers
}

// getSystemDNSServers reads DNS server addresses from the physical interface
func getSystemDNSServers(iface *net.Interface) []string {
	if iface == nil {
		return nil
	}
	var size uint32
	err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_FRIENDLY_NAME, 0, nil, &size)
	if err != nil && err != windows.ERROR_BUFFER_OVERFLOW {
		return nil
	}
	buf := make([]byte, size)
	addr := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
	err = windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_FRIENDLY_NAME, 0, addr, &size)
	if err != nil {
		return nil
	}
	var result []string
	seen := make(map[string]bool)
	for a := addr; a != nil; a = a.Next {
		if a.IfIndex != uint32(iface.Index) && a.Ipv6IfIndex != uint32(iface.Index) {
			continue
		}
		for dns := a.FirstDnsServerAddress; dns != nil; dns = dns.Next {
			ip := dns.Address.IP()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			addr := net.JoinHostPort(ip.String(), "53")
			if seen[addr] {
				continue
			}
			seen[addr] = true
			result = append(result, addr)
		}
	}
	return result
}

// resolveRemote resolves DNS via tunnel DoH to cloudflare-dns.com
func (d *DNSHandler) resolveRemote(q []byte) []byte {
	if d.pool == nil {
		return nil
	}
	s, _, _, err := d.pool.openTCPStream("cloudflare-dns.com:443")
	if err != nil {
		return nil
	}
	defer s.Close()
	tc := tls.Client(s, &tls.Config{ServerName: "cloudflare-dns.com", MinVersion: tls.VersionTLS12})
	tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err != nil {
		return nil
	}
	defer tc.Close()
	req, err := http.NewRequest(http.MethodPost, "https://cloudflare-dns.com/dns-query", bytes.NewReader(q))
	if err != nil {
		return nil
	}
	req.ContentLength = int64(len(q))
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	req.Host = "cloudflare-dns.com"
	req.Close = true
	if err := req.Write(tc); err != nil {
		return nil
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || len(body) < 12 {
		return nil
	}
	return body
}

// ====== DNS protocol utility functions ======

func extractDNSName(q []byte) string {
	if len(q) < 12 {
		return "?"
	}
	var p []byte
	pos := 12
	for pos < len(q) {
		b := q[pos]
		if b == 0 {
			break
		}
		if b&0xC0 == 0xC0 {
			break
		}
		l := int(b)
		pos++
		if pos+l > len(q) {
			return string(p)
		}
		if len(p) > 0 {
			p = append(p, '.')
		}
		p = append(p, q[pos:pos+l]...)
		pos += l
	}
	if len(p) == 0 {
		return "."
	}
	return string(p)
}

func unpackDNSHeader(q []byte) (id uint16, flags uint16, qdcount uint16) {
	if len(q) < 12 {
		return
	}
	id = binary.BigEndian.Uint16(q[0:2])
	flags = binary.BigEndian.Uint16(q[2:4])
	qdcount = binary.BigEndian.Uint16(q[4:6])
	return
}

func dnsQuestionType(q []byte) uint16 {
	if len(q) < 12 {
		return 0
	}
	pos := 12
	for pos < len(q) {
		if q[pos] == 0 {
			pos++
			break
		}
		if q[pos]&0xC0 == 0xC0 {
			pos += 2
			break
		}
		l := int(q[pos])
		pos++
		if pos+l > len(q) {
			return 0
		}
		pos += l
	}
	if pos+4 > len(q) {
		return 0
	}
	return binary.BigEndian.Uint16(q[pos : pos+2])
}

func dnsQuestionTypeName(q []byte) string {
	switch dnsQuestionType(q) {
	case dnsTypeA:
		return "A"
	case dnsTypeAAAA:
		return "AAAA"
	default:
		return fmt.Sprintf("TYPE%d", dnsQuestionType(q))
	}
}

func dnsTypeName(rrType uint16) string {
	switch rrType {
	case dnsTypeA:
		return "A"
	case dnsTypeAAAA:
		return "AAAA"
	default:
		return fmt.Sprintf("TYPE%d", rrType)
	}
}

func logDNSResult(domain string, q, reply []byte, route string, elapsed time.Duration) {
	answers := extractDNSAnswerIPs(reply)
	action, reason := dnsLogParts(route)
	log.Printf("[DNS][%s] %s %s -> %s (%s, %v)", action, domain, dnsQuestionTypeName(q), answers, reason, elapsed)
}

func logDNSServFail(domain string, q []byte, route string, elapsed time.Duration) {
	action, reason := dnsLogParts(route)
	log.Printf("[DNS][%s] %s %s -> SERVFAIL (%s, %v)", action, domain, dnsQuestionTypeName(q), reason, elapsed)
}

func dnsRouteLabel(decision RouteDecision, matchedRule string) string {
	if matchedRule == "" {
		matchedRule = "default"
	}
	if decision == DecisionDirect {
		return "direct:" + matchedRule
	}
	return "proxy:" + matchedRule
}

func dnsLogParts(route string) (action string, reason string) {
	switch {
	case route == "cache":
		return "cache", "hit"
	case strings.HasPrefix(route, "direct:"):
		return "direct", strings.TrimPrefix(route, "direct:")
	case strings.HasPrefix(route, "proxy:"):
		return "proxy", strings.TrimPrefix(route, "proxy:")
	case route == "remote":
		return "proxy", "remote"
	case route != "":
		return "proxy", route
	default:
		return "proxy", "default"
	}
}

func refreshDirectDomainFamily(domain string, rrType uint16) []string {
	if dnsHandlerInstance == nil || domain == "" {
		return nil
	}
	q := buildDNSQuery(domain, rrType)
	if len(q) == 0 {
		return nil
	}
	reply := dnsHandlerInstance.resolveLocal(q)
	if len(reply) < 12 {
		return nil
	}
	cacheDNSReply(domain, reply)
	return lookupIPsByDomainFamily(domain, rrType == dnsTypeAAAA)
}

func skipDNSName(msg []byte, pos int) (int, bool) {
	for pos < len(msg) {
		b := msg[pos]
		if b == 0 {
			return pos + 1, true
		}
		if b&0xC0 == 0xC0 {
			if pos+2 > len(msg) {
				return 0, false
			}
			return pos + 2, true
		}
		pos++
		if pos+int(b) > len(msg) {
			return 0, false
		}
		pos += int(b)
	}
	return 0, false
}

func skipDNSQuestions(msg []byte, pos int, qdcount int) (int, bool) {
	for i := 0; i < qdcount; i++ {
		var ok bool
		pos, ok = skipDNSName(msg, pos)
		if !ok || pos+4 > len(msg) {
			return 0, false
		}
		pos += 4
	}
	return pos, true
}

func cloneDNSQueryWithType(q []byte, qtype uint16) []byte {
	if len(q) < 12 {
		return nil
	}
	out := make([]byte, len(q))
	copy(out, q)
	pos := 12
	for pos < len(out) {
		if out[pos] == 0 {
			pos++
			break
		}
		if out[pos]&0xC0 == 0xC0 {
			pos += 2
			break
		}
		l := int(out[pos])
		pos++
		if pos+l > len(out) {
			return nil
		}
		pos += l
	}
	if pos+4 > len(out) {
		return nil
	}
	binary.BigEndian.PutUint16(out[pos:pos+2], qtype)
	return out
}

func dnsReplyHasType(reply []byte, rrType uint16) bool {
	if len(reply) < 12 {
		return false
	}
	qdcount := int(binary.BigEndian.Uint16(reply[4:6]))
	pos, ok := skipDNSQuestions(reply, 12, qdcount)
	if !ok {
		return false
	}
	ancount := int(binary.BigEndian.Uint16(reply[6:8]))
	for i := 0; i < ancount; i++ {
		if pos >= len(reply) {
			return false
		}
		if reply[pos]&0xC0 == 0xC0 {
			pos += 2
		} else {
			for pos < len(reply) && reply[pos] != 0 {
				l := int(reply[pos])
				pos++
				if pos+l > len(reply) {
					return false
				}
				pos += l
			}
			pos++
		}
		if pos+10 > len(reply) {
			return false
		}
		atype := binary.BigEndian.Uint16(reply[pos : pos+2])
		dlen := int(binary.BigEndian.Uint16(reply[pos+8 : pos+10]))
		pos += 10
		if pos+dlen > len(reply) {
			return false
		}
		if atype == rrType {
			return true
		}
		pos += dlen
	}
	return false
}

func extractDNSAnswerIPs(reply []byte) string {
	if len(reply) < 12 {
		return "?"
	}
	qdcount := int(binary.BigEndian.Uint16(reply[4:6]))
	pos, ok := skipDNSQuestions(reply, 12, qdcount)
	if !ok {
		return "?"
	}
	var ips []string
	totalRR := int(binary.BigEndian.Uint16(reply[6:8])) +
		int(binary.BigEndian.Uint16(reply[8:10])) +
		int(binary.BigEndian.Uint16(reply[10:12]))
	rrCount := 0
	for i := 0; i < totalRR; i++ {
		if pos+12 > len(reply) {
			break
		}
		if reply[pos]&0xC0 == 0xC0 {
			pos += 2
		} else {
			for pos < len(reply) && reply[pos] != 0 {
				l := int(reply[pos])
				pos++
				if pos+l > len(reply) {
					return "?"
				}
				pos += l
			}
			pos++
		}
		if pos+10 > len(reply) {
			break
		}
		atype := binary.BigEndian.Uint16(reply[pos:])
		dlen := int(binary.BigEndian.Uint16(reply[pos+8:]))
		pos += 10
		if pos+dlen > len(reply) {
			break
		}
		rrCount++
		if atype == 1 && dlen == 4 {
			ips = append(ips, net.IP(reply[pos:pos+4]).String())
		} else if atype == 28 && dlen == 16 {
			ips = append(ips, net.IP(reply[pos:pos+16]).String())
		}
		pos += dlen
	}
	if len(ips) > 0 {
		return strings.Join(ips, ",")
	}
	if rrCount > 0 {
		return fmt.Sprintf("rr=%d", rrCount)
	}
	return "no-answer"
}

func filterDNSReply(reply []byte, strategy byte) []byte {
	if strategy == IPStrategyDefault || strategy == IPStrategyPv4Pv6 {
		return reply
	}
	if len(reply) < 12 {
		return reply
	}

	hasIPv4 := strategy == IPStrategyIPv4Only
	hasIPv6 := strategy == IPStrategyIPv6Only
	if strategy == IPStrategyPv6Pv4 {
		hasIPv4 = true
		hasIPv6 = true
	}

	out := make([]byte, len(reply))
	copy(out, reply)

	pos := 12
	for pos < len(out) {
		if out[pos] == 0 {
			pos++
			break
		}
		if out[pos]&0xC0 == 0xC0 {
			pos += 2
			break
		}
		l := int(out[pos])
		pos++
		if pos+l > len(out) {
			return reply
		}
		pos += l
	}
	if pos+4 > len(out) {
		return reply
	}
	pos += 4

	origAncount := int(binary.BigEndian.Uint16(out[6:8]))
	if origAncount == 0 {
		return reply
	}

	var newAnswers []byte
	newAncount := uint16(0)
	for i := 0; i < origAncount; i++ {
		if pos+12 > len(out) {
			break
		}
		answerStart := pos
		if out[pos]&0xC0 == 0xC0 {
			pos += 2
		} else {
			for pos < len(out) && out[pos] != 0 {
				l := int(out[pos])
				pos++
				if pos+l > len(out) {
					return reply
				}
				pos += l
			}
			pos++
		}
		if pos+10 > len(out) {
			break
		}
		atype := binary.BigEndian.Uint16(out[pos:])
		dlen := int(binary.BigEndian.Uint16(out[pos+8:]))
		pos += 10
		if pos+dlen > len(out) {
			break
		}
		keep := false
		if atype == 1 {
			keep = hasIPv4
		} else if atype == 28 {
			keep = hasIPv6
		} else {
			keep = true
		}
		if keep {
			seg := make([]byte, pos+dlen-answerStart)
			copy(seg, out[answerStart:pos+dlen])
			newAnswers = append(newAnswers, seg...)
			newAncount++
		}
		pos += dlen
	}

	hdr := make([]byte, 12)
	copy(hdr, out[:12])
	binary.BigEndian.PutUint16(hdr[6:8], newAncount)
	binary.BigEndian.PutUint16(hdr[8:10], 0)
	binary.BigEndian.PutUint16(hdr[10:12], 0)

	qpos := 12
	for qpos < len(out) {
		if out[qpos] == 0 {
			qpos += 5
			break
		}
		if out[qpos]&0xC0 == 0xC0 {
			qpos += 6
			break
		}
		l := int(out[qpos])
		qpos++
		qpos += l
	}

	var result []byte
	result = append(result, hdr...)
	result = append(result, out[12:qpos]...)
	result = append(result, newAnswers...)
	return result
}
