# =============================================================================
# x-tunnel 服务端 Docker 镜像
# 编译：go build (Linux)，TUN 模式的 tun_*.go 会因 //go:build windows 被自动排除
# =============================================================================

# -------------------------------
# Stage 1: Build
# -------------------------------
FROM --platform=\ golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /build

# 分离依赖层，修改 go.mod/go.sum 后能复用缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制全部 .go 文件；Linux 编译时 tun_*.go 有 //go:build windows，自动不参与编译
COPY *.go ./

# 剥离调试信息，压缩二进制体积
RUN CGO_ENABLED=0 go build -ldflags=\"-s -w\" -o x-tunnel .

# -------------------------------
# Stage 2: Runtime
# -------------------------------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

# 以非 root 用户运行，增强容器安全性
RUN addgroup -S xtunnel && adduser -S xtunnel -G xtunnel
USER xtunnel

WORKDIR /app
COPY --from=builder /build/x-tunnel /app/x-tunnel

# x-tunnel 所有日志输出到 stdout/stderr，docker logs / kubectl logs 自动收集
ENTRYPOINT [\"/app/x-tunnel\"]
