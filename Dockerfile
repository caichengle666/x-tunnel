# =============================================================================
# x-tunnel Server Docker Image
# Build: Linux binary (Windows TUN files excluded via //go:build windows)
# =============================================================================

FROM golang:1.25-alpine AS builder

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

RUN apk add --no-cache ca-certificates git

WORKDIR /build

COPY go.mod go.sum ./
COPY cmd ./cmd

RUN go mod download

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o x-tunnel ./cmd/x-tunnel

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S xtunnel && adduser -S xtunnel -G xtunnel
USER xtunnel

WORKDIR /app
COPY --from=builder /build/x-tunnel /app/x-tunnel

EXPOSE 8090

ENTRYPOINT ["/app/x-tunnel"]
