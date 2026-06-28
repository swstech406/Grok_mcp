# ---- 构建阶段 ----
# 基础镜像可经 build-arg 覆盖，便于在国内网络下改走镜像加速源。Docker Hub 拉取失败时任选其一：
#   方式 A（临时，构建时指定）：
#     docker build \
#       --build-arg GO_IMG=docker.1ms.run/library/golang:1.25-alpine \
#       --build-arg RUNTIME_IMG=docker.1ms.run/library/alpine:3.20 \
#       -t grok-mcp .
#   方式 B（一劳永逸）：在 Docker Desktop → Settings → Docker Engine 的 registry-mirrors
#     加入加速源，此后所有官方镜像（golang / alpine）自动走加速，Dockerfile 无需改动。
ARG GO_IMG=golang:1.25-alpine
ARG RUNTIME_IMG=alpine:3.20

FROM ${GO_IMG} AS builder

# Go module 代理：国内默认走 goproxy.cn，避免 proxy.golang.org 超时（第二道墙）。
# CI / 海外想用官方：--build-arg GOPROXY=https://proxy.golang.org,direct
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

# 先拷依赖清单，利用 Docker 层缓存（依赖不变时跳过 go mod download）。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# modernc.org/sqlite 是纯 Go 驱动，关闭 CGO 即可得到可静态链接的单一二进制，
# 因此运行时镜像不需要 gcc/libc，任意基础镜像均可。
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X github.com/grok-mcp/internal/version.Version=${VERSION}" \
        -o /out/grok-mcp ./cmd/grok-mcp

# ---- 运行阶段 ----
FROM ${RUNTIME_IMG}

# Alpine 包仓库镜像；默认阿里云，国内构建稳定（与 GOPROXY=goproxy.cn 同理）。
# 海外构建可传 --build-arg ALPINE_MIRROR= 清空，回退官方 dl-cdn.alpinelinux.org。
ARG ALPINE_MIRROR=mirrors.aliyun.com
RUN if [ -n "${ALPINE_MIRROR}" ]; then \
        sed -i "s|dl-cdn.alpinelinux.org|${ALPINE_MIRROR}|g" /etc/apk/repositories; \
    fi \
        && apk add --no-cache ca-certificates tzdata wget \
        && addgroup -S app \
        && adduser -S app -G app

WORKDIR /app

# SQLite 数据目录，通过卷持久化（见 VOLUME）。
RUN mkdir -p /app/data && chown -R app:app /app

COPY --from=builder /out/grok-mcp /app/grok-mcp

USER app

# 容器内固定以 HTTP 模式运行；真实密钥/CPA 地址由运行时环境变量或 .env 注入。
ENV GROK_HTTP_ADDR=:8080 \
    GROK_DB_PATH=/app/data/grok-mcp.db \
    GROK_DEFAULT_USER_RPM=60

EXPOSE 8080
VOLUME ["/app/data"]

# /mcp 未带凭证会返回 401；健康检查用 /panel/ 的 302 重定向判断进程存活，
# 避免把 401 误判为健康（旧实现 grep 任意 HTTP 响应行会匹配到 401）。
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
        CMD wget -q -S -O /dev/null http://127.0.0.1:8080/panel/ 2>&1 | grep -qE 'HTTP/[0-9.]+ 200' || exit 1

ENTRYPOINT ["/app/grok-mcp"]
