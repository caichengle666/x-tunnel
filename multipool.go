package main

import (
	"errors"
	"github.com/xtaci/smux"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// PoolState 单台服务器连接池的状态
type PoolState int

const (
	PoolUnknown  PoolState = iota
	PoolHealthy            // 健康：有可用通道
	PoolDegraded           // 降级：部分通道异常
	PoolDead               // 死亡：所有通道断开
)

// ServerPool 包装单台服务器的 ECHPool + 元数据
type ServerPool struct {
	Config  ServerConfig
	Pool    *ECHPool
	State   PoolState
	RTT     int64 // 最近平均延迟 (ns)
	Updated time.Time

	mu sync.RWMutex
}

// MultiPool 管理多台服务器的连接池
type MultiPool struct {
	servers  []*ServerPool
	strategy string // failover | loadbalance | latency

	closed   bool
	closeMu  sync.Mutex
	stopChan chan struct{}

	// 负载均衡轮询
	rrCounter uint64
}

// NewMultiPool 创建多服务器连接池
func NewMultiPool(cfg *TunnelConfig) *MultiPool {
	mp := &MultiPool{
		strategy: cfg.Strategy,
		stopChan: make(chan struct{}),
	}
	for _, srv := range cfg.Servers {
		sp := &ServerPool{
			Config: srv,
			State:  PoolUnknown,
		}
		mp.servers = append(mp.servers, sp)
	}
	return mp
}

// Start 启动所有服务器的连接池
func (mp *MultiPool) Start() {
	for _, sp := range mp.servers {
		sp.mu.Lock()
		srvToken := sp.Config.Token
		if srvToken == "" {
			srvToken = token
		}
		sp.Pool = NewECHPool(sp.Config.URL, sp.Config.Connections, nil, clientID, srvToken)
		sp.Pool.Start()
		sp.State = PoolHealthy
		sp.Updated = time.Now()
		sp.mu.Unlock()
		log.Printf("[多服务器] 已启动: %s (%s)", sp.Config.Name, sp.Config.URL)
	}
	// 健康检测循环
	go mp.healthLoop()
}

// Stop 停止所有连接池
func (mp *MultiPool) Stop() {
	mp.closeMu.Lock()
	if mp.closed {
		mp.closeMu.Unlock()
		return
	}
	mp.closed = true
	close(mp.stopChan)
	mp.closeMu.Unlock()

	for _, sp := range mp.servers {
		sp.mu.Lock()
		if sp.Pool != nil {
			sp.Pool.Stop()
		}
		sp.State = PoolDead
		sp.mu.Unlock()
	}
}

// PickServer 按策略选择一台服务器
func (mp *MultiPool) PickServer() *ServerPool {
	mp.closeMu.Lock()
	if mp.closed {
		mp.closeMu.Unlock()
		return nil
	}
	mp.closeMu.Unlock()

	switch mp.strategy {
	case "loadbalance":
		return mp.pickRoundRobin()
	case "latency":
		return mp.pickLatency()
	default: // failover
		return mp.pickFailover()
	}
}

// pickFailover 按顺序选第一个健康的
func (mp *MultiPool) pickFailover() *ServerPool {
	for _, sp := range mp.servers {
		sp.mu.RLock()
		state := sp.State
		pool := sp.Pool
		sp.mu.RUnlock()
		if state == PoolHealthy && pool != nil && pool.HasHealthyChannel() {
			return sp
		}
	}
	// 降级：所有服务器都不健康时返回第一个
	for _, sp := range mp.servers {
		sp.mu.RLock()
		pool := sp.Pool
		sp.mu.RUnlock()
		if pool != nil && pool.HasHealthyChannel() {
			return sp
		}
	}
	return nil
}

// pickRoundRobin 轮询选择
func (mp *MultiPool) pickRoundRobin() *ServerPool {
	healthy := make([]*ServerPool, 0, len(mp.servers))
	for _, sp := range mp.servers {
		sp.mu.RLock()
		state := sp.State
		pool := sp.Pool
		sp.mu.RUnlock()
		if state == PoolHealthy && pool != nil && pool.HasHealthyChannel() {
			healthy = append(healthy, sp)
		}
	}
	if len(healthy) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&mp.rrCounter, 1) % uint64(len(healthy))
	return healthy[idx]
}

// pickLatency 选延迟最低的
func (mp *MultiPool) pickLatency() *ServerPool {
	var best *ServerPool
	bestRTT := int64(1<<63 - 1)
	for _, sp := range mp.servers {
		sp.mu.RLock()
		state := sp.State
		rtt := sp.RTT
		pool := sp.Pool
		sp.mu.RUnlock()
		if state == PoolHealthy && pool != nil && pool.HasHealthyChannel() {
			if rtt > 0 && rtt < bestRTT {
				bestRTT = rtt
				best = sp
			}
		}
	}
	if best != nil {
		return best
	}
	// 没有 RTT 数据用 failover
	return mp.pickFailover()
}

// OpenStream 按策略打开一个 TCP 流
func (mp *MultiPool) OpenStream(target string) (*smux.Stream, int, int, error) {
	if mp.strategy == "failover" {
		return mp.openStreamFailover(target)
	}

	sp := mp.PickServer()
	if sp == nil {
		return nil, 0, 0, errors.New("没有可用的服务器")
	}
	sp.mu.RLock()
	pool := sp.Pool
	sp.mu.RUnlock()
	if pool == nil {
		return nil, 0, 0, errors.New("连接池未初始化")
	}
	return pool.openTCPStream(target)
}

func (mp *MultiPool) openStreamFailover(target string) (*smux.Stream, int, int, error) {
	maxAttempts := len(mp.servers)
	if maxAttempts == 0 {
		return nil, 0, 0, errors.New("没有可用的服务器")
	}

	var lastErr error
	for _, sp := range mp.servers {
		sp.mu.RLock()
		state := sp.State
		pool := sp.Pool
		sp.mu.RUnlock()
		if state == PoolDead || pool == nil || !pool.HasHealthyChannel() {
			lastErr = errors.New("连接池未初始化")
			continue
		}
		stream, idx, decision, err := pool.openTCPStream(target)
		if err == nil || idx < 0 {
			return stream, idx, decision, err
		}
		lastErr = err
		sp.mu.Lock()
		sp.State = PoolDegraded
		sp.mu.Unlock()
	}
	if lastErr != nil {
		return nil, 0, 0, lastErr
	}
	return nil, 0, 0, errors.New("没有可用的服务器")
}

// OpenUDPStream 按策略打开一个 UDP 流
func (mp *MultiPool) OpenUDPStream(target string) (*smux.Stream, int, int, error) {
	sp := mp.PickServer()
	if sp == nil {
		return nil, 0, 0, errors.New("没有可用的服务器")
	}
	sp.mu.RLock()
	pool := sp.Pool
	sp.mu.RUnlock()
	if pool == nil {
		return nil, 0, 0, errors.New("连接池未初始化")
	}
	return pool.openUDPStream(target)
}

// HasHealthyChannel 是否有健康的通道
func (mp *MultiPool) HasHealthyChannel() bool {
	mp.closeMu.Lock()
	if mp.closed {
		mp.closeMu.Unlock()
		return false
	}
	mp.closeMu.Unlock()
	for _, sp := range mp.servers {
		sp.mu.RLock()
		pool := sp.Pool
		sp.mu.RUnlock()
		if pool != nil && pool.HasHealthyChannel() {
			return true
		}
	}
	return false
}

// WaitForChannelReady 等待至少一台服务器就绪
func (mp *MultiPool) WaitForChannelReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if mp.HasHealthyChannel() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return mp.HasHealthyChannel()
}

// Servers 返回所有服务器状态
func (mp *MultiPool) Servers() []ServerStatus {
	result := make([]ServerStatus, 0, len(mp.servers))
	for _, sp := range mp.servers {
		sp.mu.RLock()
		stateStr := "unknown"
		switch sp.State {
		case PoolHealthy:
			stateStr = "healthy"
		case PoolDegraded:
			stateStr = "degraded"
		case PoolDead:
			stateStr = "dead"
		}
		rttMs := int64(0)
		if sp.RTT > 0 {
			rttMs = sp.RTT / 1e6
		}
		channels := 0
		healthy := false
		sent := int64(0)
		recv := int64(0)
		sentSpeed := int64(0)
		recvSpeed := int64(0)
		if sp.Pool != nil {
			healthy = sp.Pool.HasHealthyChannel()
			if sp.Pool.smuxConns != nil {
				channels = len(sp.Pool.smuxConns)
			}
			sent = atomic.LoadInt64(&sp.Pool.bytesSent)
			recv = atomic.LoadInt64(&sp.Pool.bytesRecv)
			sentSpeed = atomic.LoadInt64(&sp.Pool.sentSpeed)
			recvSpeed = atomic.LoadInt64(&sp.Pool.recvSpeed)
		}
		sp.mu.RUnlock()
		result = append(result, ServerStatus{
			Name:      sp.Config.Name,
			URL:       sp.Config.URL,
			State:     stateStr,
			RTT:       rttMs,
			Channels:  channels,
			Healthy:   healthy,
			Updated:   sp.Updated,
			Sent:      sent,
			Recv:      recv,
			SentSpeed: sentSpeed,
			RecvSpeed: recvSpeed,
		})
	}
	return result
}

// ServerStatus API 返回的服务器状态
type ServerStatus struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	RTT       int64     `json:"rtt"`
	Channels  int       `json:"channels"`
	Healthy   bool      `json:"healthy"`
	Sent      int64     `json:"sent"`
	Recv      int64     `json:"recv"`
	SentSpeed int64     `json:"sent_speed"`
	RecvSpeed int64     `json:"recv_speed"`
	Updated   time.Time `json:"updated"`
}

// healthLoop 定期检测所有服务器的健康状态
func (mp *MultiPool) healthLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-mp.stopChan:
			return
		case <-ticker.C:
			mp.checkAllHealth()
		}
	}
}

func (mp *MultiPool) checkAllHealth() {
	for _, sp := range mp.servers {
		sp.mu.Lock()
		if sp.Pool == nil {
			sp.State = PoolDead
			sp.mu.Unlock()
			continue
		}
		if sp.Pool.HasHealthyChannel() {
			oldState := sp.State
			sp.State = PoolHealthy
			if oldState != PoolHealthy {
				log.Printf("[多服务器] %s 已恢复", sp.Config.Name)
			}
		} else {
			sp.State = PoolDegraded
		}
		sp.Updated = time.Now()
		sp.mu.Unlock()
	}
}

// 初始化随机种子
func init() {
	rand.Seed(time.Now().UnixNano())
}

// ====== 兼容 ECHPool 接口的方法 ======

// openTCPStream 按策略选择服务器打开 TCP 流
func (mp *MultiPool) openTCPStream(target string) (*smux.Stream, int, int, error) {
	return mp.OpenStream(target)
}

// openUDPStream 按策略打开 UDP 流
func (mp *MultiPool) openUDPStream(target string) (*smux.Stream, int, int, error) {
	sp := mp.PickServer()
	if sp == nil {
		return nil, 0, 0, errors.New("没有可用的服务器")
	}
	sp.mu.RLock()
	pool := sp.Pool
	sp.mu.RUnlock()
	if pool == nil {
		return nil, 0, 0, errors.New("连接池未初始化")
	}
	return pool.openUDPStream(target)
}

// ChannelCount 返回所有服务器的通道总数
func (mp *MultiPool) ChannelCount() int {
	total := 0
	for _, sp := range mp.servers {
		sp.mu.RLock()
		if sp.Pool != nil && sp.Pool.smuxConns != nil {
			total += len(sp.Pool.smuxConns)
		}
		sp.mu.RUnlock()
	}
	return total
}

// ServerPools 返回所有服务器池（给 web GUI 用）
func (mp *MultiPool) ServerPools() []*ServerPool {
	return mp.servers
}
