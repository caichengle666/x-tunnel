#!/usr/bin/env bash
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/xtunnel}"
IMAGE="${IMAGE:-ghcr.io/caichengle666/x-tunnel:latest}"
PORT="${PORT:-8090}"
CIDR="${CIDR:-0.0.0.0/0}"
TOKEN="${TOKEN:-}"

usage() {
  cat <<'EOF'
x-tunnel Docker server one-click deploy

Usage:
  TOKEN=your-secret bash deploy-server.sh
  bash deploy-server.sh --token your-secret [--port 8090] [--cidr 0.0.0.0/0] [--dir /opt/xtunnel]

Options:
  --token TOKEN     Required. Client and server token.
  --port PORT       Host port, default: 8090.
  --cidr CIDR       Allowed client CIDR, default: 0.0.0.0/0.
  --dir DIR         Install directory, default: /opt/xtunnel.
  --image IMAGE     Docker image, default: ghcr.io/caichengle666/x-tunnel:latest.
  -h, --help        Show help.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --token)
      TOKEN="${2:-}"
      shift 2
      ;;
    --port)
      PORT="${2:-}"
      shift 2
      ;;
    --cidr)
      CIDR="${2:-}"
      shift 2
      ;;
    --dir)
      APP_DIR="${2:-}"
      shift 2
      ;;
    --image)
      IMAGE="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root, or use: sudo TOKEN=your-secret bash deploy-server.sh" >&2
  exit 1
fi

if [ -z "$TOKEN" ]; then
  echo "TOKEN is required. Example: TOKEN=your-secret bash deploy-server.sh" >&2
  exit 1
fi

if ! echo "$PORT" | grep -Eq '^[0-9]+$' || [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
  echo "Invalid PORT: $PORT" >&2
  exit 1
fi

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    return
  fi

  echo "[x-tunnel] Installing Docker..."
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y ca-certificates curl gnupg
    install -m 0755 -d /etc/apt/keyrings
    rm -f /etc/apt/keyrings/docker.gpg
    curl -fsSL https://download.docker.com/linux/$(. /etc/os-release && echo "$ID")/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    chmod a+r /etc/apt/keyrings/docker.gpg
    . /etc/os-release
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$ID $VERSION_CODENAME stable" > /etc/apt/sources.list.d/docker.list
    apt-get update
    apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y dnf-plugins-core
    dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
    dnf install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    systemctl enable --now docker
  elif command -v yum >/dev/null 2>&1; then
    yum install -y yum-utils
    yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
    yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    systemctl enable --now docker
  else
    echo "Unsupported Linux distribution. Install Docker manually first." >&2
    exit 1
  fi

  systemctl enable --now docker
}

write_files() {
  mkdir -p "$APP_DIR"
  cd "$APP_DIR"

  cat > .env <<EOF
TOKEN=$TOKEN
CIDR=$CIDR
PORT=$PORT
IMAGE=$IMAGE
EOF

  cat > docker-compose.yml <<'EOF'
services:
  tunnel:
    image: ${IMAGE:-ghcr.io/caichengle666/x-tunnel:latest}
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
EOF
}

deploy() {
  cd "$APP_DIR"
  docker compose pull
  docker compose up -d
}

show_status() {
  cd "$APP_DIR"
  echo
  docker compose ps
  echo
  if command -v curl >/dev/null 2>&1; then
    curl -fsS "http://127.0.0.1:${PORT}/" || true
    echo
  fi
  cat <<EOF

[x-tunnel] Server deployed.
Directory: $APP_DIR
Status URL: http://SERVER_IP:$PORT/
Tunnel URL: ws://SERVER_IP:$PORT/tunnel

Cloudflare Tunnel service URL:
  http://localhost:$PORT

Client example:
  x-tunnel.exe -l socks5://127.0.0.1:1080 -f ws://SERVER_IP:$PORT/tunnel -token '$TOKEN'
EOF
}

install_docker
write_files
deploy
show_status
