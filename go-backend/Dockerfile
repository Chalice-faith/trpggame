# Go Backend Dockerfile — 多阶段构建
FROM golang:1.22-alpine AS builder

WORKDIR /build

# 安装 CA 证书（用于 HTTPS 请求）
RUN apk add --no-cache ca-certificates git

# 先复制依赖文件，利用 Docker 缓存层
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并构建
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server ./cmd/server

# 运行阶段 — 使用 scratch 最小化镜像
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/server /server
COPY --from=builder /build/configs /configs

EXPOSE 8080

ENTRYPOINT ["/server"]
