# x-tunnel 多服务器支持开发计划

## 目标
一个客户端同时管理多个服务端连接，支持故障转移 / 负载均衡 / 动态配置热加载

---

## 阶段一：配置文件系统

### 新增文件
- `config.go` — JSON 配置结构体 + 读写逻辑

### 配置文件格式 `config.json`
```json
{
  "listen": "socks5://127.0.0.1:1080,http://127.0.0.1:30001",
  "strategy": "failover",
  "dns": "https://doh.pub/dns-query",
  "block_ports": "443",
  "ip_strategy": "",
  "servers": [
    {
      "name": "香港",
      "url": "ws://103.152.171.55:8090/tunnel",
      "token": "hksecret2026",
      "connections": 3,
      "weight": 100
    },
    {
      "name": "韩国",
      "url": "ws://129.154.49.95:8080/tunnel",
      "token": "mysecret123",
      "connections": 2,
      "weight": 50
    }
  ]
}
```

### 优先级
配置加载顺序：`config.json` > 命令行参数 `-c config.json` > 默认值

---

## 阶段二：多连接池 MultiPool

### 新增结构体
- `MultiPool` — 管理多个 `ECHPool`
- 每个 `ECHPool` 对应一个服务器，独立维护连接

### 策略实现
| 策略 | 说明 |
|------|------|
| `failover` | 按配置文件顺序优先，健康检测异常自动切换到下一个 |
| `loadbalance` | 按权重轮询分配请求 |
| `latency` | 每 30 秒测 RTT，选延迟最低的 |

### 接口
- `getStream()` → `(*smux.Stream, *ServerConfig, error)` 按策略选择一个可用通道
- `healthCheck()` → goroutine 定期探测所有服务器

---

## 阶段三：Web UI 多服务器管理

### 新增 API
- `GET /api/servers` — 列出所有服务器及健康状态
- `PUT /api/servers` — 修改服务器配置
- `POST /api/servers/:id/:action` — 切换策略 / 手动切换 / 删除

### 前端升级
- 服务器卡片列表，每张显示：名称、url、延迟、健康状态、连接数
- 策略选择器：failover / loadbalance / latency
- 服务器可增删改
- 配置修改后自动保存到 `config.json`

---

## 阶段四：可选增强
- [ ] 客户端自动识别服务端版本
- [ ] 流量统计（每服务器出入流量）
- [ ] 规则分流（按域名/geo 指定走哪个服务器）
- [ ] 保存历史连接日志
