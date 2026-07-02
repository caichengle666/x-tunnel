# =============================================================================
# x-tunnel Server Docker Image
# Build: Linux binary (tun_*.go excluded via //go:build windows)
# =============================================================================

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o x-tunnel .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S xtunnel && adduser -S xtunnel -G xtunnel
USER xtunnel

WORKDIR /app
COPY --from=builder /build/x-tunnel /app/x-tunnel

ENTRYPOINT ["/app/x-tunnel"]