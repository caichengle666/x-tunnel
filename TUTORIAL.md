# x-tunnel 搭建使用教程

## 目录
1. [编译与准备](#1-编译与准备)
2. [快速上手：本地回环测试](#2-快速上手本地回环测试)
3. [生产部署：服务端（有公网 IP）](#3-生产部署服务端有公网-ip)
4. [生产部署：客户端](#4-生产部署客户端)
5. [TUN 模式：全局透明代理（Windows）](#5-tun-模式全局透明代理windows)
6. [高级用法](#6-高级用法)
7. [常见问题](#7-常见问题)

---

## 1. 编译与准备

### 环境要求
- Go 1.25+
- Windows + TUN 模式需要：Wintun 驱动（自动下载）

### 编译

```powershell
cd x-tunnel-src
go build -o x-tunnel.exe ./cmd/x-tunnel
```

编译后得到单个 `x-tunnel.exe`（约 15MB），无外部依赖，复制到任意目录即可运行。

### 架构概览

```
┌─────────────────────────────────────────────────────────┐
│                     客户端（本机）                        │
│  ┌──────────┐    ┌──────────────────┐                   │
│  │ 浏览器    │───▶│ SOCKS5/HTTP/TCP  │                   │
│  │ 命令行    │    │ 本地监听 (:1080)  │                   │
│  │ 游戏/APP  │    └────────┬─────────┘                   │
│  └──────────┘             │                             │
│                   ┌───────▼────────┐                    │
│                   │  ECHPool       │                    │
│                   │  (smux 多路复用) │                    │
│                   │  (N 条 WS 连接)  │                    │
│                   └───────┬────────┘                    │
│                           │ WSS                         │
└───────────────────────────┼─────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                     服务器（VPS）                         │
│                   ┌──────────────┐                      │
│                   │ WSS 监听 :443 │                      │
│                   └──────┬───────┘                      │
│                          │                              │
│                   ┌──────▼───────┐                      │
│                   │ smux 解复用   │                      │
│                   └──────┬───────┘                      │
│                          │                              │
│                   ┌──────▼───────┐                      │
│                   │ 直连 / SOCKS5 │                      │
│                   │ 转发到目标    │                      │
│                   └──────────────┘                      │
└─────────────────────────────────────────────────────────┘
```

---

## 2. 快速上手：本地回环测试

在一台机器上同时运行服务端和客户端，验证功能。

### 2.1 启动服务端

```powershell
# 终端 1：启动服务端（监听 WS，不加密方便测试）
x-tunnel.exe -l ws://127.0.0.1:8080/tunnel -token test123
```

输出示例：
```
[服务端] 直连模式（未配置SOCKS5代理）
[服务端] WebSocket 服务启动: ws://127.0.0.1:8080/tunnel
[服务端] 等待客户端连接...
```

### 2.2 启动客户端

```powershell
# 终端 2：启动客户端，连到上面的服务端，本地开 SOCKS5 代理
x-tunnel.exe -l socks5://127.0.0.1:1080 -f ws://127.0.0.1:8080/tunnel -token test123
```

输出示例：
```
[客户端] 客户端ID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
[客户端] SOCKS5 代理: 127.0.0.1:1080
[客户端] 通道 1 (IP:) 连接成功 (smux)
[客户端] 全部 1 条通道就绪
```

### 2.3 验证

```powershell
# 终端 3：测试代理是否工作
curl -x socks5://127.0.0.1:1080 https://www.baidu.com -v
```

看到正常返回 HTML 即表示隧道搭建成功。

---

## 3. 生产部署：服务端（有公网 IP）

### 场景 A：最简单部署（自签证书）

```powershell
# 服务端监听 WSS，自动生成自签证书
x-tunnel.exe -l wss://0.0.0.0:443/tunnel -token mytoken
```

客户端需要加 `-insecure` 跳过证书校验：

```powershell
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://你的服务器IP:443/tunnel -token mytoken -insecure
```

### 场景 B：使用合法证书（Let's Encrypt / 购买证书）

```powershell
x-tunnel.exe -l wss://0.0.0.0:443/tunnel -token mytoken ^
  -cert C:\certs\fullchain.pem ^
  -key C:\certs\privkey.pem
```

客户端正常连接（无需 -insecure）：

```powershell
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://your-domain.com/tunnel -token mytoken
```

### 场景 C：通过 SOCKS5 上游代理转发

如果服务端不能直连外网，需要前置 SOCKS5：

```powershell
x-tunnel.exe -l wss://0.0.0.0:443/tunnel ^
  -f socks5://user:pass@127.0.0.1:1080 ^
  -token mytoken
```

### 场景 D：套 Cloudflare CDN（隐藏服务器真实 IP）

服务端监听 WS（非加密，让 CDN 做 TLS 终止）：

```powershell
x-tunnel.exe -l ws://127.0.0.1:8080/tunnel -token mytoken
```

Nginx/Caddy 反代配置（以 Nginx 为例）：

```nginx
server {
    listen 443 ssl;
    server_name your-domain.com;
    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    location /tunnel {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
    }
}
```

---

## 4. 生产部署：客户端

### 4.1 SOCKS5 代理（最常用）

```powershell
# 基础 SOCKS5
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://your-server.com/tunnel -token mytoken

# 带认证的 SOCKS5（防止被局域网其他人用）
x-tunnel.exe -l socks5://user:pass@127.0.0.1:1080 -f wss://your-server.com/tunnel -token mytoken

# 带 WSS ECH 抗审查（默认自带，不用额外配置）
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://your-server.com/tunnel -token mytoken
```

### 4.2 HTTP 代理

```powershell
x-tunnel.exe -l http://127.0.0.1:8080 -f wss://your-server.com/tunnel -token mytoken

# 带认证
x-tunnel.exe -l http://user:pass@127.0.0.1:8080 -f wss://your-server.com/tunnel -token mytoken
```

### 4.3 TCP 端口转发

```powershell
# 本地 2000 端口 → 服务端 → 远程 1.2.3.4:22（SSH）
x-tunnel.exe -l tcp://0.0.0.0:2000/1.2.3.4:22 -f wss://your-server.com/tunnel -token mytoken
```

### 4.4 同时开启多个代理

```powershell
# 一条命令同时启动 SOCKS5 + HTTP 代理
x-tunnel.exe ^
  -l socks5://127.0.0.1:1080,http://127.0.0.1:8080 ^
  -f wss://your-server.com/tunnel ^
  -token mytoken
```

### 4.5 高性能：多连接并发

```powershell
# 每个 IP 建立 8 条 WebSocket 连接（默认 3）
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://your-server.com/tunnel -token mytoken -n 8
```

### 4.6 指定连接 IP（域名被污染时用）

```powershell
# 强制将 wss://your-server.com 解析到 1.2.3.4
x-tunnel.exe -l socks5://127.0.0.1:1080 ^
  -f wss://your-server.com/tunnel ^
  -token mytoken ^
  -ip 1.2.3.4,5.6.7.8
```

---

## 5. TUN 模式：全局透明代理（Windows）

TUN 模式创建一个虚拟网卡，接管系统所有流量，实现全局代理。

### 前提

1. **管理员权限**（TUN 驱动需要）
2. **下载 GeoIP 和 GeoSite 数据文件**（也叫 geoip.dat / geosite.dat，来自 v2fly 项目）
   - 下载后放在 x-tunnel.exe 同目录，或用 `-geoip` / `-geosite` 指定路径

### 5.1 基本用法

```powershell
# 管理员 PowerShell 运行
x-tunnel.exe -tun ^
  -f wss://your-server.com/tunnel ^
  -token mytoken ^
  -geoip geoip.dat ^
  -geosite geosite.dat
```

### 5.2 智能分流：国内直连 + 国外代理

```powershell
x-tunnel.exe -tun ^
  -f wss://your-server.com/tunnel ^
  -token mytoken ^
  -geoip geoip.dat ^
  -geosite geosite.dat ^
  -direct "geosite:cn,geoip:cn,geosite:private,geoip:private" ^
  -proxy "" ^
  -default proxy
```

规则说明：
- `geosite:cn` → 国内网站直连
- `geoip:cn` → 目标 IP 是中国 IP 直连
- `geoip:private` → 私有地址段直连
- 未命中以上规则的 → 走代理（`-default proxy`）

### 5.3 指定仅某些域名走代理

```powershell
# 仅 google/twitter/github 走代理，其他直连
x-tunnel.exe -tun ^
  -f wss://your-server.com/tunnel ^
  -token mytoken ^
  -geoip geoip.dat ^
  -geosite geosite.dat ^
  -direct "geosite:cn,geoip:cn,geoip:private" ^
  -proxy "domain:google.com,domain:twitter.com,domain:github.com" ^
  -default direct
```

### 5.4 自定义 TUN 参数

```powershell
# 修改网卡名、MTU、网段
x-tunnel.exe -tun ^
  -tun-name mytun ^
  -tun-mtu 1500 ^
  -tun-addr 10.0.0.1/24 ^
  -tun-dns 10.0.0.1 ^
  -f wss://your-server.com/tunnel ^
  -token mytoken
```

---

## 6. 高级用法

### 6.1 ECH 抗审查原理

```
正常 WSS 连接：
  TLS ClientHello → 明文 SNI: your-server.com → 墙看见并阻断

x-tunnel WSS + ECH：
  TLS ClientHello → SNI: cloudflare-ech.com（掩护域名）
                    + ECH extension（加密的真正目标）
                   → 墙只看到 cloudflare-ech.com，放行
                   → 服务器解密 ECH，知道真正目标
```

自定义 ECH 参数：

```powershell
# 换一个 ECH 掩护域名
x-tunnel.exe -l socks5://127.0.0.1:1080 ^
  -f wss://your-server.com/tunnel ^
  -ech cloudflare-ech.com ^
  -dns https://doh.pub/dns-query

# 彻底禁用 ECH，使用标准 TLS 1.3
x-tunnel.exe -l socks5://127.0.0.1:1080 ^
  -f wss://your-server.com/tunnel ^
  -fallback
```

### 6.2 服务端限来源 IP

```powershell
# 只允许两个 IP 段连接
x-tunnel.exe -l wss://0.0.0.0:443/tunnel -token mytoken -cidr "1.2.3.0/24,2400:cb00::/32"
```

### 6.3 客户端屏蔽 QUIC

默认已拦截 UDP 443（即 QUIC 协议），避免浏览器走 QUIC 绕过代理：

```powershell
# 自定义需要拦截的 UDP 端口
x-tunnel.exe -l socks5://127.0.0.1:1080 -f ws://... -block "443,8443,50000-50100"
```

### 6.4 IP 访问策略

```powershell
# 仅 IPv4
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://... -ips 4

# IPv4 优先，IPv6 备选
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://... -ips "4,6"
```

---

## 7. 常见问题

### Q：自签证书客户端连不上？
A：客户端加 `-insecure` 参数跳过证书校验。

### Q：连接超时？
A：检查防火墙是否放行了对应端口。服务端用 `-cidr` 确保允许客户端 IP。

### Q：Token 的作用？
A：服务端和客户端配相同的 token，作为 WebSocket 子协议认证，防止未授权连接。

### Q：TUN 模式需要什么权限？
A：需要管理员权限。Wintun 驱动需要安装到系统，首次运行会自动下载。

### Q：ECS 获取失败？
A：默认从 `doh.pub` 查询 `cloudflare-ech.com` 的 HTTPS 记录。如果网络环境屏蔽了 DoH，尝试换 DNS 或加 `-fallback` 禁用 ECH。

### Q：性能怎么样？
A：支持多 WebSocket 连接并行（`-n`），每个连接内部 smux 多路复用。实际吞吐取决于带宽和服务端 CPU。

### Q：能否在 Linux 上运行服务端？
A：Linux 上可以运行服务端（ws/wss 监听），但 TUN 模式仅在 Windows 实现。Linux 上编译需要删掉 `//go:build windows` 的文件或加 build tags。
