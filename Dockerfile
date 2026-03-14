# ==============================================================================
# 多阶段构建 — 最终镜像仅包含静态二进制文件
#
# 构建 sproxy（默认）：
#   docker build -t pairproxy/sproxy .
#
# 构建 cproxy：
#   docker build --build-arg BINARY=cproxy -t pairproxy/cproxy .
#
# 注入版本信息：
#   docker build \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg BUILT=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t pairproxy/sproxy .
# ==============================================================================

# ------------------------------------------------------------------------------
# Stage 1: builder
# ------------------------------------------------------------------------------
FROM golang:1.24-alpine AS builder

# 安装 git（go mod download 可能需要）
RUN apk add --no-cache git

WORKDIR /src

# 优先复制依赖文件，利用 Docker 层缓存：
# 只要 go.mod/go.sum 不变，依赖层就不会重新下载
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# 构建参数
ARG BINARY=sproxy
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT=unknown

# 静态编译（CGO_ENABLED=0）
# glebarez/sqlite 是纯 Go 实现，无需 CGO
# -s -w 去除符号表和调试信息，缩小二进制体积
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
      -X github.com/l17728/pairproxy/internal/version.Version=${VERSION} \
      -X github.com/l17728/pairproxy/internal/version.Commit=${COMMIT} \
      -X github.com/l17728/pairproxy/internal/version.BuiltAt=${BUILT}" \
    -trimpath \
    -o /out/${BINARY} \
    ./cmd/${BINARY}

# ------------------------------------------------------------------------------
# Stage 2: final — distroless（仅含 CA 证书 + 时区数据，无 shell）
#
# 如需调试 shell，改用：
#   FROM gcr.io/distroless/static-debian12:debug
# 或：
#   FROM alpine:3.22
#   RUN apk add --no-cache ca-certificates tzdata
# ------------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12

ARG BINARY=sproxy

# 从 builder 复制二进制
COPY --from=builder /out/${BINARY} /usr/local/bin/sproxy

# 使用 distroless 内置非 root 用户（UID 65532）
USER 65532:65532

# 数据目录和配置目录（通过 volume/mount 提供）
VOLUME ["/var/lib/pairproxy", "/etc/pairproxy"]

EXPOSE 9000

ENTRYPOINT ["/usr/local/bin/sproxy"]
CMD ["start", "--config", "/etc/pairproxy/sproxy.yaml"]
