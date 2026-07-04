package main

import (
	"encoding/json"
	"os"
	"strings"
)

// ServerConfig 单台服务器配置
type ServerConfig struct {
	Name        string `json:"name"`                  // 显示名称
	URL         string `json:"url"`                   // ws:// 或 wss:// 地址
	Token       string `json:"token,omitempty"`       // 独立 token（空则用全局）
	Connections int    `json:"connections,omitempty"` // 连接数（0 用全局默认）
	Weight      int    `json:"weight,omitempty"`      // 负载均衡权重（默认 100）
	Insecure    *bool  `json:"insecure,omitempty"`    // 跳过证书校验
	IPs         string `json:"ips,omitempty"`         // IP 策略
}

// TunnelConfig 完整配置文件
type TunnelConfig struct {
	Listen      string         `json:"listen"`                 // 本地监听地址
	Strategy    string         `json:"strategy"`                // failover | loadbalance | latency
	DNS         string         `json:"dns,omitempty"`           // DNS 服务器
	BlockPorts  string         `json:"block_ports,omitempty"`   // UDP 拦截端口
	Connections int            `json:"connections,omitempty"`   // 全局默认连接数
	IPStrategy  string         `json:"ip_strategy,omitempty"`   // 全局 IP 策略
	Servers     []ServerConfig `json:"servers"`                 // 服务器列表
}

// ConfigPaths 配置文件搜索路径
var ConfigPaths = []string{
	"config.json",
	"x-tunnel.json",
}

func defaultConfig() TunnelConfig {
	return TunnelConfig{
		Listen:      "socks5://127.0.0.1:1080,http://127.0.0.1:30001",
		Strategy:    "failover",
		DNS:         "https://doh.pub/dns-query",
		BlockPorts:  "443",
		Connections: 3,
		Servers:     []ServerConfig{},
	}
}

// LoadConfig 从文件加载配置
func LoadConfig(path string) (*TunnelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// 补全默认值
	for i := range cfg.Servers {
		if cfg.Servers[i].Connections <= 0 {
			cfg.Servers[i].Connections = cfg.Connections
		}
		if cfg.Servers[i].Weight <= 0 {
			cfg.Servers[i].Weight = 100
		}
	}
	return &cfg, nil
}

// SaveConfig 保存配置到文件
func SaveConfig(path string, cfg *TunnelConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// FindConfig 自动搜索配置文件
func FindConfig() string {
	for _, p := range ConfigPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// parseMultiForward 解析逗号分隔的多个服务端地址
func parseMultiForward(forwardStr string) []string {
	if forwardStr == "" {
		return nil
	}
	parts := strings.Split(forwardStr, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// configToServers 将命令行参数转为 ServerConfig 列表
func configToServers(addrs []string) []ServerConfig {
	servers := make([]ServerConfig, 0, len(addrs))
	for _, addr := range addrs {
		servers = append(servers, ServerConfig{
			Name:        addr,
			URL:         addr,
			Token:       token,
			Connections: connectionNum,
			Weight:      100,
		})
	}
	return servers
}

var tunnelConfig = defaultConfig()
