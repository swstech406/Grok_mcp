# grok-search-mcp

[简体中文](./README_CN.md)

`grok-search-mcp` is an HTTP-only [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that exposes Grok-powered real-time web search, X/Twitter search, and model discovery to MCP clients.

It does **not** call the official xAI API directly. Instead, it connects to an existing [CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) deployment. CPA owns the upstream xAI authentication, while `grok-search-mcp` provides MCP transport, client API keys, quotas, usage tracking, and an administration panel.

> [!IMPORTANT]
> This project supports **Streamable HTTP only**. It does not provide a stdio transport or built-in TLS termination.

- `grok-search-mcp` must run as a standalone HTTP service, and MCP clients connect to `http://<host>:<port>/mcp`.
- It cannot be configured as a stdio server that an MCP client launches and communicates with over standard input and output.
- The service listens for plain HTTP and does not load HTTPS certificates or private keys or perform TLS handshakes.
- For an internet-facing deployment, place a trusted reverse proxy such as Nginx, Caddy, Traefik, Kubernetes Ingress, or a cloud load balancer in front of `grok-search-mcp`. The proxy should expose HTTPS and forward requests to `grok-search-mcp` over internal HTTP.

A typical production request path is:

```text
MCP client -- HTTPS --> reverse proxy / load balancer -- HTTP --> grok-search-mcp /mcp
                       (TLS terminates here)
```

## Features

- Streamable HTTP MCP endpoint at `/mcp`
- Three read-only MCP tools:
  - `grok_web_search`
  - `grok_x_search`
  - `grok_list_models`
- Selectable CPA upstream protocol: OpenAI Responses, OpenAI Chat Completions, or Anthropic Messages
- MCP progress notifications for upstream search rounds
- Per-user client API keys with enable/disable controls
- Tier-based RPM and monthly successful-call quotas
- Valid-forwarded-IP-triggered protection for `/mcp` and panel authentication
- SQLite persistence for users, keys, tiers, usage, invite codes, and server settings
- Embedded administration panel with no separate frontend build step
- Runtime updates for upstream settings, search concurrency, proxy settings, registration mode, and debug mode
- Docker Compose deployment with a non-root runtime image

## Architecture

```text
Streamable HTTP MCP client
        |
        |  POST /mcp
        |  Authorization: Bearer <MCP client API key>
        v
grok-search-mcp
  |     |
  |     +---- /panel/ and /panel/v1/* ---- administrators and users
  |
  +---------- SQLite -------------------- users, keys, tiers, usage, settings
  |
  |  POST /v1/responses, /v1/chat/completions, or /v1/messages
  |  GET  /v1/models
  |  Authorization: Bearer <CPA API key>
  v
CLIProxyAPI
  |
  v
xAI / Grok
```

### Credentials are not interchangeable

| Credential | Used between | Purpose |
|---|---|---|
| CPA API key | `grok-search-mcp` -> CPA | Authenticates the selected upstream search endpoint and `/v1/models` requests. |
| MCP client API key | MCP client -> `/mcp` | Created and copied on demand in the panel. The database stores an authentication hash plus recoverable ciphertext encrypted with a key derived from `GROK_JWT_SECRET`. |
| Panel JWT | Browser/API client -> `/panel/v1` | Returned by panel login. It cannot authenticate `/mcp`. |

## Requirements

- Linux is the currently documented local runtime target
- Go 1.25.0 or later for local builds
- A reachable CPA deployment with `/v1/models` and at least one compatible search endpoint: `/v1/responses`, `/v1/chat/completions`, or `/v1/messages`
- Docker and Docker Compose for the container workflow, if preferred
- An MCP client that supports Streamable HTTP and custom Bearer headers

The application uses pure-Go SQLite (`modernc.org/sqlite`) and does not require CGO.

## Quick start

### 1. Build

```bash
go build -o grok-search-mcp ./cmd/grok-search-mcp
```

Optionally inject a version at build time:

```bash
go build \
  -ldflags "-X github.com/MapleMapleCat/Grok_Search_Mcp/internal/version.Version=1.2.3" \
  -o grok-search-mcp ./cmd/grok-search-mcp

./grok-search-mcp -version
```

### 2. Configure

```bash
cp .env.example .env
mkdir -p data
${EDITOR:-vi} .env
```

At minimum, a new installation requires:

```dotenv
CPA_API_KEY=replace-with-your-cpa-api-key
GROK_JWT_SECRET=replace-with-a-strong-random-secret-of-at-least-32-bytes
```

Load the environment and start the server:

```bash
set -a
source .env
set +a

./grok-search-mcp
```

Default endpoints:

| Service | URL |
|---|---|
| MCP | `http://127.0.0.1:8080/mcp` |
| Administration panel | `http://127.0.0.1:8080/panel/` |
| Panel REST API | `http://127.0.0.1:8080/panel/v1/` |

### Usage retention and SQLite maintenance

Usage data is retained at progressively lower resolutions so long-running
installations do not keep every request forever:

| Environment variable | Default | Purpose |
|---|---:|---|
| `GROK_USAGE_RAW_RETENTION_DAYS` | `7` | Keeps per-request records and debug payloads before compacting them into hourly history. |
| `GROK_USAGE_HOURLY_RETENTION_DAYS` | `90` | Keeps hourly history before compacting it into daily history. |
| `GROK_USAGE_DAILY_RETENTION_DAYS` | `730` | Deletes daily history older than this window. |
| `GROK_USAGE_MAINTENANCE_INTERVAL` | `1h` | Runs retention, rollup, cleanup, and WAL checkpoint work. |

The hourly retention must exceed the raw retention, and the daily retention
must exceed the hourly retention. Historical totals and traffic charts combine
raw, hourly, and daily data; the recent-record list and individual debug details
are available only while the corresponding raw record is retained.

The main database and `<GROK_DB_PATH>.debug.sqlite` both use WAL mode. For a
live backup, use SQLite's online backup facilities for both database files. Do
not copy only the main `.db` file while the service is running. If using a
filesystem copy, stop the service first and copy both databases together with
any WAL/SHM sidecars. Scheduled maintenance checkpoints WAL files but does not
run `VACUUM`; use `VACUUM` or `VACUUM INTO` only as an explicit, infrequent
operator action when file-level space reclamation is required.

The primary SQLite database intentionally keeps a single write connection;
adding writers would increase lock competition rather than remove SQLite's
single-writer constraint. Connections use a 5-second `busy_timeout`. The
background usage writer combines up to 32 records, or records arriving within
10ms, into one transaction. Scheduled maintenance uses `PASSIVE` checkpoints
so active readers are not blocked by a periodic `TRUNCATE` checkpoint. Store
both SQLite databases on local SSD storage, not NFS, SMB, or a high-latency
network block volume.

Administrators can query live operational metrics:

```bash
curl -sS "http://127.0.0.1:8080/panel/v1/admin/operations/metrics" \
  -H "Authorization: Bearer ${login_token}" | jq
```

This admin-only endpoint reports connection-pool utilization and wait time for
the primary, read, and debug databases; quota reserve/release latency and
errors; SQLite busy/locked counts; usage batch, queue-depth, oldest-record,
write/queue latency, failure, and drop metrics; and maintenance plus WAL
checkpoint latency/frame counters. At minimum, alert on:

- sustained growth in `primary_write_pool.wait_count` or `wait_duration_ms`;
- any continuously increasing `busy_or_locked_errors` value;
- a usage queue that remains near capacity, increasing
  `oldest_queued_age_ms`, or dropped records;
- sustained quota reserve/release average or maximum latency growth;
- repeated checkpoint busy frames or increasing checkpoint duration.

If these signals remain elevated on local SSD storage after batching, the
workload has exceeded the intended embedded SQLite write envelope. High-write-
QPS deployments should migrate to PostgreSQL/MySQL or move quota accounting to
an external atomic counter instead of increasing SQLite write connections.

### 3. Sign in and create an MCP client key

When no enabled administrator exists, the server bootstraps an `admin` account and writes its one-time random password to the startup log. Sign in, rotate the credentials as soon as possible, and create an MCP client API key.

```bash
login_token="$(curl -sS -X POST "http://127.0.0.1:8080/panel/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password-from-startup-log"}' | jq -r '.token')"

curl -sS -X POST "http://127.0.0.1:8080/panel/v1/keys" \
  -H "Authorization: Bearer ${login_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"local-client"}'
```

The `api_key` in the response is returned only once. Store it securely.

### 4. Connect Claude Code

Claude Code is the client with a repository-documented setup example:

```bash
export GROK_SEARCH_MCP_API_KEY="grok_xxx"

claude mcp add --transport http grok-search-mcp http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer ${GROK_SEARCH_MCP_API_KEY}"
```

A project-level `.mcp.json` can use environment expansion:

```json
{
  "mcpServers": {
    "grok-search-mcp": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": {
        "Authorization": "Bearer ${GROK_SEARCH_MCP_API_KEY}"
      }
    }
  }
}
```

Do not commit a real API key. Other MCP clients can connect when they support Streamable HTTP with a custom `Authorization: Bearer ...` header, but client-specific configurations not documented in this repository should be treated as unverified.

## MCP tools

All tools are read-only. Search failures are returned as MCP tool results with `isError=true`, so a normal tool error does not terminate the MCP session.

### `grok_web_search`

Performs real-time public web search through Grok.

| Argument | Type | Required | Description |
|---|---|:---:|---|
| `query` | string | Yes | Non-empty search request. |
| `model` | string | No | Overrides the configured default; the value must contain `grok`. |
| `allowed_domains` | string[] | No | Searches only these domains; maximum 5. |
| `excluded_domains` | string[] | No | Excludes these domains; maximum 5. |
| `enable_image_understanding` | boolean | No | Enables image understanding for web search. |
| `enable_image_search` | boolean | No | Enables image search results. |

`allowed_domains` and `excluded_domains` are mutually exclusive. Entries must be plain domain names, not URLs. Wildcards, IP literals, ports, paths, `localhost`, and `.local` domains are rejected.

### `grok_x_search`

Searches real-time posts on X/Twitter through Grok.

| Argument | Type | Required | Description |
|---|---|:---:|---|
| `query` | string | Yes | Non-empty search request. |
| `model` | string | No | Overrides the configured default; the value must contain `grok`. |

Domain filters and image-related arguments apply only to `grok_web_search`.

The wire-level mapping depends on the selected upstream protocol. Responses uses CPA's native `x_search` tool, Chat Completions uses an `x` search source, and Anthropic Messages uses the supported server-side web-search tool restricted to `x.com`. The Anthropic mapping is necessary because a custom Anthropic `x_search` declaration is treated as a client-executed tool call by CPA and does not produce a final search answer on its own.

### `grok_list_models`

Accepts no arguments. It reads CPA `GET /v1/models`, trims and deduplicates IDs, keeps IDs containing `grok`, and excludes IDs containing `imagine` or `video`.

### Search result shape

```json
{
  "answer": "Answer synthesized by Grok",
  "citations": [
    "https://example.com/source"
  ],
  "sources": [
    {
      "url": "https://example.com/source",
      "title": "Example source"
    }
  ],
  "usage": {
    "input_tokens": 120,
    "output_tokens": 340,
    "total_tokens": 460,
    "reasoning_tokens": 0
  }
}
```

`citations`, `sources`, and `usage` may be omitted when the upstream response does not provide them. JSON-RPC batch requests are intentionally rejected because batching could bypass per-call quota reservation and usage accounting.

## Configuration

### Startup environment

| Variable | Default | Description |
|---|---|---|
| `GROK_JWT_SECRET` | None | Required HS256 panel signing secret; must be at least 32 bytes. Always supplied through the environment. |
| `CPA_API_KEY` | None | Required for a new database. Existing persisted server settings may provide it on later starts. |
| `CPA_BASE_URL` | `http://127.0.0.1:8317` | CPA root URL. |
| `GROK_UPSTREAM_PROTOCOL` | `responses` | Search protocol: `responses`, `chat_completions`, or `anthropic_messages`. |
| `GROK_MODEL` | `grok-4.3` | Default Grok model. |
| `GROK_HTTP_TIMEOUT` | `120` | Per-phase timeout in seconds for upstream connection establishment, TLS handshake, and response headers. It does not limit an active SSE response body; caller cancellation defines the total search lifetime. |
| `GROK_HTTP_ADDR` | `:8080` | HTTP listen address. Requires restart to change. |
| `GROK_DB_PATH` | `./grok-search-mcp.db` | SQLite database path. Requires restart to change. |
| `GROK_USAGE_RAW_RETENTION_DAYS` | `7` | Raw usage and debug-detail retention before hourly compaction. |
| `GROK_USAGE_HOURLY_RETENTION_DAYS` | `90` | Hourly usage retention before daily compaction. |
| `GROK_USAGE_DAILY_RETENTION_DAYS` | `730` | Daily aggregate retention before deletion. |
| `GROK_USAGE_MAINTENANCE_INTERVAL` | `1h` | Interval for rollup, cleanup, and WAL checkpoint maintenance. |
| `GROK_SEARCH_MCP_IP_RPM` | `300` | Source-IP RPM applied before MCP API-key authentication only when `X-Real-IP` or `X-Forwarded-For` contains a valid IP. |
| `GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY` | `16` | Environment default for the process-wide in-flight streaming search limit. The persisted panel setting takes precedence after initialization. |
| `GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY` | `4` | Environment default for the per-user limit; must not exceed the global limit. The persisted panel setting takes precedence after initialization. |
| `GROK_SEARCH_MCP_DEBUG` | `false` | Accepts `1`, `true`, or `yes`. May capture debug request/response context in usage records. |
| `GROK_PROXY_URL` | Empty | Explicit upstream HTTP(S) proxy URL. |
| `GROK_PROXY_ENABLED` | Inferred | Explicit proxy switch. When unset, a non-empty `GROK_PROXY_URL` enables it. |
| `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | Go defaults | Used by the standard transport when an explicit proxy is not enabled. |

The former `GROK_MCP_IP_RPM`, `GROK_MCP_GLOBAL_SEARCH_CONCURRENCY`,
`GROK_MCP_USER_SEARCH_CONCURRENCY`, and `GROK_MCP_DEBUG` names remain accepted
as compatibility aliases. When both names are configured, the corresponding
`GROK_SEARCH_MCP_*` variable takes precedence.

When either search concurrency limit is exhausted, the server rejects the request immediately with HTTP `503` and `Retry-After: 1` instead of queueing another long-lived HTTP/SSE request. Search responses expose semaphore acquisition time in `X-Grok-Search-Queue-Time-Ms`.

### Forwarded client-IP protection

Application-level IP protection is enabled only when a valid IP can be resolved from `X-Real-IP` or `X-Forwarded-For`. The same policy is used for:

- the `/mcp` token bucket that runs before API-key authentication;
- the panel login and registration endpoint token buckets;
- the panel username/IP failed-login lockout.

The request behavior is:

| Request state | IP-protection behavior |
|---|---|
| Both headers are absent, empty, or contain no valid IP | Application-level IP rate limiting and login IP lockout are skipped. User-tier RPM and quota checks still apply after successful MCP authentication. |
| A valid forwarded client-IP header is present | IP protection runs using `X-Real-IP` when valid; otherwise it uses the first valid IP in `X-Forwarded-For`. No trusted-proxy allowlist is required. |

> [!IMPORTANT]
> The application directly trusts `X-Real-IP` and `X-Forwarded-For`. It must be reachable only through a trusted reverse proxy that always injects a valid client IP and removes client-supplied values first. If clients can reach `grok-search-mcp` directly, they can omit or invalidate the headers to skip IP protection, or forge them to select arbitrary rate-limit buckets. Keep proxy-layer rate limits enabled for `/mcp`, `/panel/v1/auth/login`, and `/panel/v1/auth/register`.

The proxy must overwrite `X-Real-IP` and rebuild `X-Forwarded-For` from the connection source. Because the application selects the first valid `X-Forwarded-For` entry, do not preserve an untrusted client-provided chain.

Example Nginx forwarding configuration:

```nginx
location / {
    proxy_pass http://grok-search-mcp:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

### Persistence and live updates

On startup, environment variables are loaded first. If SQLite already contains server settings, the persisted upstream settings take precedence. Administrators can update the following values from **Server Settings** without restarting:

- CPA base URL and API key
- Upstream search protocol
- Default model and timeout
- Explicit proxy URL and enabled state
- Registration mode
- Debug mode
- Process-wide and per-user streaming search concurrency limits

The listen address, database path, JWT secret, and source-IP RPM remain startup-only settings.

> [!WARNING]
> The CPA API key is persisted in SQLite. Protect and back up the database as sensitive data. The panel only returns a masked preview of this key.

### Upstream protocol mapping

| Setting | Endpoint | Search mapping |
|---|---|---|
| `responses` | `POST /v1/responses` | CPA Responses built-ins (`web_search` / `x_search`); this remains the backward-compatible default and provides search-round progress events. |
| `chat_completions` | `POST /v1/chat/completions` | xAI-compatible `search_parameters`, with `web` or `x` sources and streamed Chat Completions chunks. Short status-only responses such as “searching...” are continued with bounded follow-up requests so MCP callers receive a final answer or an explicit error. |
| `anthropic_messages` | `POST /v1/messages` | Anthropic server-side `web_search_20250305` with Messages SSE events. Web searches preserve configured domain filters; X searches use the same server tool restricted to `x.com` and add an instruction to return direct X post URLs. |

Protocol support ultimately depends on the selected CPA version, provider, and model capabilities. Responses is the safest compatibility choice for existing Grok/CPA deployments. Image-search options are Responses-specific; other protocols ignore them when no equivalent wire option exists.

The protocols can expose different metadata even when their answer text is equivalent:

- Responses normally provides the richest search-round progress and structured citation data.
- Chat Completions emits progress only when CPA includes compatible nonstandard search events. Standard Chat chunks may contain only final text and usage.
- Anthropic Messages may include source URLs in the answer text without emitting structured citation blocks, depending on CPA's provider translation.
- `usage` is normalized across all protocols when the upstream response includes token counts.

## Users, registration, tiers, and quotas

Registration can be changed at runtime:

| Mode | Behavior |
|---|---|
| `free` | Public self-registration is allowed. |
| `invite` | A valid, enabled, non-exhausted invite code is required. |
| `disabled` | Public registration is disabled. |

Administrators can create, disable, and delete invite codes and set their registration limits.

Each user belongs to a tier. All of a user's API keys share that tier's RPM and monthly successful-call allowance. Only actual `tools/call` requests are metered; initialization, ping, and tool-list requests are not.

Default tiers for a new database:

| Tier | RPM | Monthly successful calls |
|---|---:|---:|
| `tier0` | 10 | 800 |
| `tier1` | 20 | 4,000 |
| `tier2` | 40 | 16,000 |
| `tier3` | 60 | 40,000 |
| `tier4` | 120 | 160,000 |
| `tier5` | 300 | 800,000 |
| `tier6` | Unlimited | Unlimited |

Successful-call periods use UTC calendar months. A call reserves quota before tool execution; failed calls roll the reservation back. Tier values can be customized in the panel.

The `/mcp` middleware order is shown below. The `IP RPM` step immediately passes through when neither forwarded header yields a valid IP:

```text
MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM -> Search Concurrency -> Quota -> Usage -> MCP handler
```

## Administration API overview

The embedded panel is served from `/panel/`. Its API is under `/panel/v1`.

Public authentication routes:

```text
GET  /panel/v1/auth/registration-settings
POST /panel/v1/auth/register
POST /panel/v1/auth/login
```

Authenticated user routes cover profile information, API-key management, and usage:

```text
GET    /panel/v1/me
GET    /panel/v1/keys
POST   /panel/v1/keys
POST   /panel/v1/keys/{id}/reveal
PATCH  /panel/v1/keys/{id}
DELETE /panel/v1/keys/{id}
GET    /panel/v1/keys/{id}/usage
GET    /panel/v1/usage
```

Administrator routes under `/panel/v1/admin/` manage users, tiers, server settings, invite codes, models, and usage. All non-public panel requests require:

```text
Authorization: Bearer <panel JWT>
```

## Docker Compose

Published release images are available from Docker Hub:

```bash
docker pull maplemaplecat/grok-search-mcp:v0.2.0
```

```bash
cp .env.example .env
${EDITOR:-vi} .env
docker compose up -d --build
```

If CPA runs directly on the Docker host, use:

```dotenv
CPA_BASE_URL=http://host.docker.internal:8317
```

The supplied container:

- Builds with `CGO_ENABLED=0`
- Runs as a non-root `app` user
- Listens on port 8080
- Stores SQLite data in `/app/data`
- Uses the `grok-search-mcp-data` named volume in Compose
- Health-checks `/panel/`

The Compose file does not forward every optional outbound proxy variable. Extend `environment` if the container needs `GROK_PROXY_URL`, `GROK_PROXY_ENABLED`, or the standard proxy variables.

## Production and security notes

- Put the service behind an HTTPS reverse proxy before exposing it publicly. The server does not provide TLS.
- Never expose panel JWTs, MCP client API keys, CPA keys, invite codes, or a real `.env` file.
- Rotate the bootstrap administrator credentials immediately.
- Restrict access to the SQLite file and include it in secure backups.
- Ensure the reverse proxy overwrites `X-Real-IP`, rebuilds `X-Forwarded-For`, and always adds at least one of them; otherwise application-level IP protection can be bypassed.
- Prevent all direct client access to the application port because forwarded client-IP headers are trusted without an application-level proxy allowlist.
- Add reverse-proxy rate limits for `/mcp`, panel login, and panel registration.
- Keep debug mode disabled unless troubleshooting. Debug context may retain request or response content even though authentication headers are redacted.
- MCP client API keys are shown once and stored only as hashes; losing the plaintext key requires creating a replacement.

## Development and testing

Run the default test suite:

```bash
go test ./...
```

Verify the executable builds:

```bash
go build ./cmd/grok-search-mcp
```

Live CPA/xAI integration tests are opt-in:

```bash
export GROK_INTEGRATION_TEST="1"
export CPA_API_KEY="replace-with-your-cpa-api-key"
export CPA_BASE_URL="http://127.0.0.1:8317"

go test ./test/grok -run TestIntegrationSearchLiveCPA -v
```

The panel frontend is embedded static HTML, CSS, and JavaScript; no Node.js build is required. The repository currently has no Makefile, task runner, or published CI/release pipeline. Use standard Go tooling such as `gofmt` and `go vet` when contributing.

### Project layout

```text
cmd/grok-search-mcp/ Process entry point and version flag
internal/app/       Application composition, bootstrap, HTTP server, shutdown
internal/auth/      MCP API-key authentication and panel JWTs
internal/config/    Environment loading and persisted settings mapping
internal/grok/      CPA requests, SSE parsing, model listing
internal/mcp/       MCP server instructions and tool registration
internal/panel/     Panel REST API
internal/panelui/   Embedded administration frontend
internal/quota/     Monthly successful-call reservation
internal/ratelimit/ Source-IP and per-user rate limiting
internal/store/     SQLite schema and persistence
internal/usage/     Tool-call usage and optional debug capture
test/http/          HTTP integration and protection tests
test/grok/          Opt-in live upstream integration tests
```

## Troubleshooting

| Symptom | Check |
|---|---|
| `GROK_JWT_SECRET is required` | Set a secret of at least 32 bytes in the service environment. |
| Startup fails on a new database | Set a valid `CPA_API_KEY` and verify the database directory is writable. |
| MCP returns `401` or `403` | Use an MCP client API key, not the panel JWT; verify the key and user are enabled. |
| MCP returns `429` | Check source-IP RPM, the user's tier RPM, and the monthly successful-call allowance. |
| Upstream timeout or HTTP error | Verify CPA URL, CPA key, proxy settings, and CPA health. |
| Docker cannot reach host CPA | Use `http://host.docker.internal:<port>` with the supplied Compose configuration. |
| Model list is empty | Confirm CPA returns Grok model IDs; `imagine` and `video` IDs are intentionally filtered. |
| Client cannot connect | Confirm it supports Streamable HTTP and sends the Bearer header to the exact `/mcp` URL. |

## License

This project is licensed under [CC BY-NC 4.0](./LICENSE). Copying, distribution, and modification are permitted for non-commercial purposes with attribution and compliance with the license terms. Commercial use requires prior written permission from the copyright holder.
