# x-tunnel

基于 WebSocket 的多路复用隧道工具，支持 SOCKS5/HTTP 代理、TCP 端口转发，以及 Windows TUN 全局透明代理。

## 特性

- **WebSocket 隧道** — 通过 WS/WSS 加密隧道转发流量，支持 ECH（Encrypted Client Hello）抗审查
- **多路复用** — smux 协议，单连接承载多个 TCP/UDP 流
- **多协议代理** — SOCKS5、HTTP CONNECT 代理
- **智能连接池** — 多 WebSocket 并发，自动选择 RTT 最优通道
- **TUN 全局代理**（仅 Windows）— 虚拟网卡接管系统流量，支持 GeoIP/GeoSite 规则分流
- **轻量** — 单文件，无外部运行时依赖

## 快速开始

### 服务端（Linux，推荐 Docker）

`ash
docker compose up -d tunnel-wss
`

详见 [README-docker.md](README-docker.md)

### 本地测试（同机回环）

`powershell
# 终端 1：启动服务端
x-tunnel.exe -l ws://127.0.0.1:8080/tunnel -token test123

# 终端 2：启动客户端
x-tunnel.exe -l socks5://127.0.0.1:1080 -f ws://127.0.0.1:8080/tunnel -token test123

# 终端 3：验证
curl -x socks5://127.0.0.1:1080 https://www.baidu.com
`

## 编译

`powershell
go build -o x-tunnel.exe .
`

- **Windows + TUN 模式**：需要管理员权限，Wintun 驱动自动下载
- **Linux 服务端**：tun_*.go 因 //go:build windows 自动排除，直接编译即可

## 项目结构

`
x-tunnel/
├── x-tunnel.go            # 主程序入口
├── tun_*.go               # TUN 模式（//go:build windows）
├── go.mod / go.sum
├── Dockerfile             # 服务端 Docker 镜像
├── docker-compose.yml     # 服务端部署示例
├── README-docker.md       # Docker 部署详细文档
├── TUTORIAL.md            # 完整使用教程
├── .env.example
└── .github/
    └── workflows/
        ├── build.yml      # 跨平台编译 + Release 附件
        └── docker.yml     # Docker 镜像推送到 GHCR
`

## 核心参数

| 参数 | 说明 |
|------|------|
| -l ws://... | 服务端监听 WebSocket |
| -l socks5://... | 客户端本地 SOCKS5 代理 |
| -f ws://... | 客户端连接的服务端地址 |
| -token | 认证令牌（服务端/客户端需一致） |
| -n | 客户端 WebSocket 连接数（默认 3） |
| -tun | 启用 TUN 全局代理（仅 Windows） |
| -ech | ECH 掩护域名（wss 模式） |
