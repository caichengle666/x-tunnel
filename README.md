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

### Server Web + WebSocket Port Reuse

On the server, the Web management panel and WebSocket tunnel can share a single port:

```text
x-tunnel -l ws://0.0.0.0:8090/tunnel -token your-token
```

- `/tunnel` handles the WebSocket tunnel
- `/` serves the server Web status page

Port reuse happens automatically when:

- The server `-l` address uses `ws://` or `wss://`
- No separate `-web` flag is specified, or the `-web` port matches the tunnel port

If you need the Web panel on a different port, use `-web` explicitly:

```text
x-tunnel -l ws://0.0.0.0:8090/tunnel -token your-token -web :9090
```

## Client Quick Start

If no config file exists, the client auto-generates a default `config.json` on first run:

```text
listen:      socks5://127.0.0.1:1080,http://127.0.0.1:30001
token:       123456
web_listen:  :9090
servers:     ws://127.0.0.1:8090/tunnel (token 123456)
```

You can then edit the token, servers, and other settings from the Web panel at `http://127.0.0.1:9090/`.

Or run from a config file explicitly:


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
go build -o x-tunnel ./cmd/x-tunnel
```

Windows binary:

```powershell
go build -o x-tunnel-new.exe ./cmd/x-tunnel
```

GitHub Actions release artifacts are packaged as archives. The Windows amd64 package automatically downloads official Wintun `0.14.1` during CI and includes `wintun.dll` next to `x-tunnel.exe` for TUN mode.

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
cmd/x-tunnel/       Main Go package, client/server/Web/TUN code
deploy-server.sh    One-click Docker server deploy
Dockerfile          Server image build
docker-compose.yml  Server compose example
README-docker.md    Docker deployment guide
```

## Notes

- Docker server deployment uses `ghcr.io/caichengle666/x-tunnel:latest`.
- Server mode exposes `/tunnel` and `/` on the same port.
- Server Web page is read-only; client management APIs are disabled in server mode.
- Windows TUN mode requires administrator privileges and `wintun.dll` next to the executable. The GitHub Windows amd64 release package includes it automatically.
