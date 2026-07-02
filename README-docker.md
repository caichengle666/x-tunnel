# x-tunnel 服务端 Docker 部署

## 目录

1. [镜像体积估算](#镜像体积估算)
2. [快速启动](#快速启动)
3. [三种部署方案](#三种部署方案)
4. [客户端连接](#客户端连接)
5. [维护命令](#维护命令)

---

## 镜像体积估算

| 分层 | 说明 | 预估大小 |
|------|------|---------|
| alpine:3.21 基础 | Linux 最小镜像 | ~8 MB |
| ca-certificates + tzdata | TLS 证书 + 时区 | ~2 MB |
| x-tunnel 二进制文件 | 含 gvisor 等依赖 | ~20–30 MB |
| **总计** | | **~30–40 MB** |

> gvisor 库较大（TCP/IP 协议栈），导致二进制比普通 Go 程序重。压缩后实际下载约 25 MB。

---

## 快速启动

`powershell
# 1. 编译镜像（从 x-tunnel-src 目录）
cd G:\项目代码\x-tunnel\x-tunnel-src
docker build -t xtunnel-server .

# 2. 创建环境变量文件
\"TOKEN=mysecret123\" | Out-File -Encoding UTF8 .env

# 3. 启动（选一种方案，见下文）
docker compose up -d tunnel-wss

# 4. 查看日志
docker compose logs -f tunnel-wss
`

---

## 三种部署方案

### 方案 A：WS 明文（开发 / 内网测试）

`powershell
docker compose up -d tunnel-ws
`

- 端口：8080
- 优点：简单，无需证书
- 缺点：流量明文，不推荐公网使用

### 方案 B：WSS + TLS（生产推荐）

`powershell
# 先创建证书目录（可选用自己的证书）
mkdir certs
# 放你的 server.crt 和 server.key，或者直接用自动生成的自签证书

# 启动
docker compose up -d tunnel-wss
`

- 端口：443
- 优点：加密传输，难被识别
- 备注：没挂载证书时会自动生成自签证书；客户端需加 -insecure 或挂载真实证书

### 方案 C：WSS + 上游 SOCKS5 代理

适用于服务端的出口流量也需要走代理的场景（如多级代理链）：

`powershell
 = \"socks5://user:pass@10.0.0.1:1080\"
docker compose up -d tunnel-wss-proxy
`

---

## 客户端连接

服务端启动后，客户端使用对应的地址连接：

`powershell
# 连接到 WS 服务端
x-tunnel.exe -l socks5://127.0.0.1:1080 -f ws://YOUR_SERVER_IP:8080/tunnel -token mysecret123

# 连接到 WSS 服务端
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://YOUR_DOMAIN.com/tunnel -token mysecret123 -insecure
`

---

## 维护命令

`powershell
# 查看运行状态
docker compose ps

# 查看实时日志
docker compose logs -f

# 重启
docker compose restart tunnel-wss

# 停止
docker compose down

# 重新构建（代码更新后）
docker compose build --no-cache tunnel-wss && docker compose up -d tunnel-wss

# 进入容器（调试）
docker exec -it xtunnel-wss sh
`

---

## 常用环境变量

在 .env 文件中配置：

`env
# 必须：认证令牌（服务端和客户端必须一致）
TOKEN=changeme

# 可选：允许连接的客户端 IP 段（留空表示不限制）
CIDR=0.0.0.0/0,::/0

# 可选：TLS 证书目录
CERT_DIR=./certs

# 可选：SOCKS5 上游代理（方案 C）
SOCKS5_PROXY=socks5://user:pass@10.0.0.1:1080
`

---

## 注意事项

1. **端口权限**：443/80 端口需要 root 权限，普通用户用 >1024 的端口（如 8443）
2. **防火墙**：确保服务器防火墙放行了对应端口
3. **自签证书**：客户端连接时需加 -insecure 参数忽略证书校验
4. **数据持久化**：服务端无状态，不需要挂载 volume；如需持久日志，可挂载 /tmp 或重定向到文件
5. **多实例**：不同端口运行多个实例，只需修改 docker-compose.yml 中的端口映射
