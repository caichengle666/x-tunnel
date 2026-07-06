# x-tunnel 服务端 Docker 部署

服务端推荐使用 GHCR 镜像部署。默认使用 `8090` 一个端口，同时提供：

- WebSocket 隧道入口：`/tunnel`
- 服务端状态页：`/`

## 快速启动

一键部署：

```bash
curl -fsSL https://raw.githubusercontent.com/caichengle666/x-tunnel/main/deploy-server.sh -o deploy-server.sh
sudo TOKEN=change-me bash deploy-server.sh
```

可选参数：

```bash
sudo bash deploy-server.sh --token change-me --port 8090 --cidr 0.0.0.0/0
```

手动部署：

```bash
mkdir -p /opt/xtunnel
cd /opt/xtunnel

cat > docker-compose.yml <<'YAML'
services:
  tunnel:
    image: ghcr.io/caichengle666/x-tunnel:latest
    container_name: xtunnel
    restart: always
    ports:
      - "${PORT:-8090}:8090"
    command:
      - -l
      - ws://0.0.0.0:8090/tunnel
      - -web
      - :8090
      - -token
      - ${TOKEN:?TOKEN is required}
      - -cidr
      - ${CIDR:-0.0.0.0/0}
    healthcheck:
      test: ["CMD", "nc", "-z", "127.0.0.1", "8090"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 5s
YAML

cat > .env <<'ENV'
TOKEN=change-me
CIDR=0.0.0.0/0
PORT=8090
ENV

docker compose pull
docker compose up -d
```

## 检查状态

```bash
docker compose ps
docker compose logs -f
curl http://127.0.0.1:8090/
```

正常状态页会显示：

```text
x-tunnel 运行中
WebSocket endpoint: /tunnel
Token: 已设置
Status: OK
```

## 客户端连接

公网直连：

```bash
x-tunnel.exe -l socks5://127.0.0.1:1080 -f ws://SERVER_IP:8090/tunnel -token change-me
```

Cloudflare Tunnel：

```bash
x-tunnel.exe -l socks5://127.0.0.1:1080 -f wss://xhk.example.com/tunnel -token change-me
```

Cloudflare Tunnel 的服务 URL 填：

```text
http://localhost:8090
```

不要填 `ws://localhost:8090`。Cloudflare 会把外部 `wss://域名/tunnel` 转发到容器的 HTTP/WebSocket 服务。

## 更新镜像

```bash
cd /opt/xtunnel
docker compose pull
docker compose up -d
docker image prune -f
```

也可以重新执行一键脚本：

```bash
sudo TOKEN=change-me bash deploy-server.sh
```

## 常用配置

`.env` 支持：

```env
TOKEN=change-me
CIDR=0.0.0.0/0
PORT=8090
```

- `TOKEN` 必填，客户端和服务端必须一致。
- `CIDR` 限制允许连接的客户端来源 IP，默认 `0.0.0.0/0`。
- `PORT` 是宿主机暴露端口，容器内固定为 `8090`。

## 本地构建镜像

通常不需要本地构建。开发测试时可以使用：

```bash
docker build -t xtunnel-server .
docker run --rm -p 8090:8090 xtunnel-server \
  -l ws://0.0.0.0:8090/tunnel \
  -web :8090 \
  -token change-me \
  -cidr 0.0.0.0/0
```

## 安全说明

- 服务端模式下 Web 只保留状态查看，客户端配置修改、重启、Geo 更新等管理接口会返回 `403`。
- 不要使用空 `TOKEN`。`docker-compose.yml` 已设置为未填写 `TOKEN` 时拒绝启动。
- 如果直接公网暴露 `8090`，建议配合防火墙或 `CIDR` 限制来源。
