# ---- build stage ----
# Uses the official Docker Hub images by default. Override these build args only
# when you explicitly want to pin or test a different official-compatible image.
ARG GO_IMG=golang:1.25-alpine
ARG RUNTIME_IMG=alpine:3.20

FROM ${GO_IMG} AS builder

# Use the upstream Go module proxy by default.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

# Copy dependency manifests first so Docker can cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# modernc.org/sqlite is pure Go, so CGO can stay disabled and the runtime image
# does not need a compiler toolchain or libc compatibility packages.
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X github.com/grok-mcp/internal/version.Version=${VERSION}" \
        -o /out/grok-mcp ./cmd/grok-mcp

# ---- runtime stage ----
FROM ${RUNTIME_IMG}

RUN apk add --no-cache ca-certificates tzdata wget \
        && addgroup -S app \
        && adduser -S app -G app

WORKDIR /app

RUN mkdir -p /app/data && chown -R app:app /app

COPY --from=builder /out/grok-mcp /app/grok-mcp

USER app

ENV GROK_HTTP_ADDR=:8080 \
    GROK_DB_PATH=/app/data/grok-mcp.db

EXPOSE 8080
VOLUME ["/app/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
        CMD wget -q -S -O /dev/null http://127.0.0.1:8080/panel/ 2>&1 | grep -qE 'HTTP/[0-9.]+ 200' || exit 1

ENTRYPOINT ["/app/grok-mcp"]
