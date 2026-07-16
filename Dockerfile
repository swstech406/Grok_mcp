# ---- build stage ----
# Production defaults use immutable multi-platform image-index digests. Override
# these build arguments only when intentionally testing replacement images.
ARG GO_IMG=golang:1.25.12-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587
ARG RUNTIME_IMG=alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

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
# does not need a compiler toolchain or libc compatibility packages. The image
# version comes directly from internal/version.Version in the copied source.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w" \
        -o /out/grok-search-mcp ./cmd/grok-search-mcp

# ---- runtime stage ----
FROM ${RUNTIME_IMG}

LABEL org.opencontainers.image.title="grok-search-mcp"

RUN apk add --no-cache ca-certificates tzdata wget \
        && addgroup -S app \
        && adduser -S app -G app

WORKDIR /app

RUN mkdir -p /app/data && chown -R app:app /app

COPY --from=builder /out/grok-search-mcp /app/grok-search-mcp

USER app

ENV GROK_HTTP_ADDR=:8080 \
    GROK_DB_PATH=/app/data/grok-search-mcp.db

EXPOSE 8080
VOLUME ["/app/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
        CMD wget -q -S -O /dev/null http://127.0.0.1:8080/panel/ 2>&1 | grep -qE 'HTTP/[0-9.]+ 200' || exit 1

ENTRYPOINT ["/app/grok-search-mcp"]
