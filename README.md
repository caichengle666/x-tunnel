# x-tunnel

x-tunnel is a WebSocket multiplex tunnel tool. It supports:

- Server mode over `ws://` or `wss://`
- Client local SOCKS5 / HTTP proxy
- Multi-server failover, load balancing, and latency strategy
- Web management panel on the client
- Windows TUN mode with GeoIP / GeoSite split routing
- Docker server deployment through GHCR image

## Quick Server Deploy

Recommended Docker one-click deploy on a Linux VPS:

```bash
curl -fsSL https://raw.githubusercontent.com/caichengle666/x-tunnel/main/deploy-server.sh | sudo bash
```

The script asks for:

```text
Enter server token:
Enter server port [8090]:
Enter allowed client CIDR [0.0.0.0/0]:
```

Default server endpoint:

```text
ws://SERVER_IP:8090/tunnel
```

For Cloudflare Tunnel, publish the service URL as:

```text
http://localhost:8090
```

Then clients connect with:

```text
wss://your-domain.example/tunnel
```

More details: [README-docker.md](README-docker.md)

## Client Quick Start

Run from a config file:

```powershell
x-tunnel-new.exe -config config.json -web :9090
```

Open the Web panel:

```text
http://127.0.0.1:9090/
```

Typical local listeners:

```text
SOCKS5: 127.0.0.1:1080
HTTP:   127.0.0.1:1090
```

## Simple Local Test

Server:

```powershell
x-tunnel-new.exe -l ws://127.0.0.1:8080/tunnel -token test123
```

Client:

```powershell
x-tunnel-new.exe -l socks5://127.0.0.1:1080 -f ws://127.0.0.1:8080/tunnel -token test123
```

Test:

```powershell
curl.exe --socks5 127.0.0.1:1080 http://ipinfo.io/ip
```

## Build

```bash
go build -o x-tunnel .
```

Windows binary:

```powershell
go build -o x-tunnel-new.exe .
```

## Important Flags

| Flag | Description |
| --- | --- |
| `-l` | Listen address, for example `socks5://127.0.0.1:1080` or `ws://0.0.0.0:8090/tunnel` |
| `-f` | Server address for client mode |
| `-token` | WebSocket authentication token |
| `-config` | JSON config file path |
| `-web` | Web panel listen address, for example `:9090` |
| `-n` | WebSocket channel count |
| `-tun` | Enable Windows TUN mode |
| `-direct` | Direct routing rules for TUN mode |
| `-proxy` | Proxy routing rules for TUN mode |
| `-default` | TUN default route: `proxy` or `direct` |

## Repository Layout

```text
x-tunnel.go          Main program
config.go           JSON config handling
multipool.go        Multi-server pool
web_gui.go          Web panel and API
spawnproc_*.go      Process restart helpers
tun_*.go            Windows TUN mode
deploy-server.sh    One-click Docker server deploy
Dockerfile          Server image build
docker-compose.yml  Server compose example
README-docker.md    Docker deployment guide
```

## Notes

- Docker server deployment uses `ghcr.io/caichengle666/x-tunnel:latest`.
- Server mode exposes `/tunnel` and `/` on the same port.
- Server Web page is read-only; client management APIs are disabled in server mode.
- Windows TUN mode requires administrator privileges and `wintun.dll` next to the executable.
