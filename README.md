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
- Peer-aware direct or trusted-proxy IP protection for `/mcp` and panel authentication
- SQLite persistence for users, keys, tiers, usage, invite codes, and server settings
- Embedded administration panel with no separate frontend build step
- Runtime updates for upstream settings, search concurrency, proxy settings, registration mode, debug mode, and operational metrics collection
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
- Go 1.25.12 or later for local builds
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

Startup configuration is split into two layers:

- `.env` is the user-owned basic configuration and contains the CPA upstream
  address/port plus credentials required for a first deployment;
- `advanced.env` contains the remaining settings with safe defaults and
  normally needs no changes for the first deployment.

Linux release archives include `.env.example` and `advanced.env`, so the same
flow works with a prebuilt binary. The Compose file remains available only in a
source checkout.

```bash
cp .env.example .env
${EDITOR:-vi} .env
```

The basic `.env` contains the CPA upstream address and two required
credentials:

```dotenv
CPA_BASE_URL=
CPA_API_KEY=replace-with-your-cpa-api-key
GROK_JWT_SECRET=replace-with-a-strong-random-secret-of-at-least-32-bytes
```

When a local binary connects to a CPA on the same host, `CPA_BASE_URL` may stay
empty and defaults to `http://127.0.0.1:8317`. Under Docker Compose, an empty
value defaults to `http://host.docker.internal:8317`. If CPA uses another host
or port, enter the complete URL directly in the basic `.env`.

Generate the JWT secret with `openssl rand -hex 32`. Do not put the generated
value in `advanced.env` or commit it to source control.

For a local binary, load the advanced defaults first and then the user-owned
`.env`, so an explicit user value always takes precedence:

```bash
mkdir -p data
set -a
source advanced.env
source .env
set +a

./grok-search-mcp
```

With the source checkout's Docker Compose deployment, Compose automatically
loads `advanced.env` and `.env` in the same order:

```bash
docker compose up -d --build
```

Compose uses the container-specific `http://host.docker.internal:8317` default
for a CPA running on the host. To change the Compose CPA endpoint, edit
`CPA_BASE_URL` in the basic `.env` so it is available during Compose
interpolation.

Most deployments do not need to edit `advanced.env`. Change it only when
customizing the upstream protocol, listener or storage, retention,
authentication protection, trusted proxies, capacity limits, debug mode, or an
upstream proxy. Configure the CPA address and port only in the basic `.env`.
Existing full `.env` files remain compatible: because `.env` is loaded last,
its values override `advanced.env`. The service still reads ordinary
environment variables and does not introduce another configuration format.

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

Operational metrics are disabled by default. An administrator must first enable
**Database operational metrics** in **Server Settings**. When disabled, the
endpoint below returns HTTP `404`.

Administrators can query live operational metrics:

```bash
curl -sS "http://127.0.0.1:8080/panel/v1/admin/operations/metrics" \
  -H "Authorization: Bearer ${login_token}" | jq
```

This admin-only endpoint reports connection-pool utilization and wait time for
the primary, read, and debug databases; quota reserve/release latency and
errors; SQLite busy/locked counts; usage batch, queue-depth, oldest-record,
write/queue latency, failure, and drop metrics; and maintenance plus WAL
checkpoint latency/frame counters. It also reports the bounded source-IP
registry's current/capacity values and dedicated-admission, expiration,
fallback-request, and fallback-rejection counters. The same response reports
panel-auth protector capacity, admission, expiry,
fallback, fallback-rejection, and login-failure capacity-rejection counters,
grouped by public auth endpoint without exposing IP addresses or usernames. At
minimum, alert on:

- sustained growth in `primary_write_pool.wait_count` or `wait_duration_ms`;
- any continuously increasing `busy_or_locked_errors` value;
- a usage queue that remains near capacity, increasing
  `oldest_queued_age_ms`, or dropped records;
- sustained quota reserve/release average or maximum latency growth;
- repeated checkpoint busy frames or increasing checkpoint duration;
- sustained source-IP registry saturation or increasing fallback rejections;
- sustained panel-auth endpoint fallback traffic, fallback rejections, or
  login-failure capacity rejections.

If these signals remain elevated on local SSD storage after batching, the
workload has exceeded the intended embedded SQLite write envelope. High-write-
QPS deployments should migrate to PostgreSQL/MySQL or move quota accounting to
an external atomic counter instead of increasing SQLite write connections.

### 3. Sign in and create an MCP client key

When no enabled administrator exists, the server bootstraps an `admin` account
and writes a bounded JSON credential file with exact `0600` permissions. By
default it is `<GROK_DB_PATH>.bootstrap-admin`; startup logs report only that
path and never the password. Read it as the same operating-system user that
runs the service, then rotate the password promptly.

For a local binary deployment, read it from the local data directory:

```bash
bootstrap_password="$(jq -r '.password' ./data/grok-search-mcp.db.bootstrap-admin)"
```

For Docker Compose, read it from the container data volume while keeping JSON
parsing in the host's `jq` process:

```bash
bootstrap_password="$(docker compose exec -T grok-search-mcp \
  sh -c 'cat /app/data/grok-search-mcp.db.bootstrap-admin' | jq -r '.password')"
```

For the published-image `docker run` deployment below, read it through the
container name:

```bash
bootstrap_password="$(docker exec -i grok-search-mcp \
  sh -c 'cat /app/data/grok-search-mcp.db.bootstrap-admin' | jq -r '.password')"
```

After obtaining the bootstrap password, sign in, rotate it, and create the
first MCP key:

```bash
login_token="$(curl -sS -X POST "http://127.0.0.1:8080/panel/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg password "${bootstrap_password}" '{username:"admin",password:$password}')" | jq -r '.token')"

replacement_session="$(curl -sS -X POST "http://127.0.0.1:8080/panel/v1/me/change-password" \
  -H "Authorization: Bearer ${login_token}" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg current "${bootstrap_password}" --arg new "replace-with-a-new-password" \
    '{current_password:$current,new_password:$new}')")"
login_token="$(jq -r '.token' <<<"${replacement_session}")"

curl -sS -X POST "http://127.0.0.1:8080/panel/v1/keys" \
  -H "Authorization: Bearer ${login_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"local-client"}'
```

The `api_key` in the response can be used immediately. It can also be revealed
and copied later from the **API Keys** page. Protect access to the panel and
the database because the key can be recovered there. A user may own at most 20
keys by default; disabled keys count, while deleting a key frees capacity.

Changing the bootstrap administrator password removes the credential file on a
best-effort basis after the database update commits. A removal failure never
rolls back the password change; remove any stale file manually. Existing secure
credential files are reused after a startup failure before account creation,
so do not edit, broaden permissions on, or restore stale copies from backups.

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

`citations`, `sources`, and `usage` may be omitted when the upstream response does not provide them. JSON-RPC batch requests and duplicate or case-colliding `method`, `params`, or `params.name` routing fields are intentionally rejected before quota reservation and usage accounting.

## Configuration

### Startup environment

| Variable | Default | Description |
|---|---|---|
| `GROK_JWT_SECRET` | None | Required HS256 panel signing secret; must be at least 32 bytes. Always supplied through the environment. |
| `CPA_API_KEY` | None | Required for a new database. Existing persisted server settings may provide it on later starts. |
| `CPA_BASE_URL` | `http://127.0.0.1:8317` | CPA root URL. |
| `GROK_UPSTREAM_PROTOCOL` | `responses` | Search protocol: `responses`, `chat_completions`, or `anthropic_messages`. |
| `GROK_MODEL` | `grok-4.5` | Default Grok model. |
| `GROK_HTTP_TIMEOUT` | `120` | Per-phase timeout in seconds for upstream connection establishment, TLS handshake, and response headers. It does not limit an active SSE response body; caller cancellation defines the total search lifetime. |
| `GROK_HTTP_ADDR` | `:8080` | HTTP listen address. Requires restart to change. |
| `GROK_DB_PATH` | `./grok-search-mcp.db` | SQLite database path. Requires restart to change. |
| `GROK_BOOTSTRAP_CREDENTIALS_PATH` | `<GROK_DB_PATH>.bootstrap-admin` | Startup-only path for the `0600` bootstrap administrator JSON credential file. Existing files must be regular, non-symlink files with exact restrictive permissions. |
| `GROK_CLIENT_IP_MODE` | `direct` | Startup-only client identity mode: `direct` uses `RemoteAddr` and ignores forwarding headers; `trusted_proxy` authenticates the immediate peer before accepting forwarding headers. |
| `GROK_TRUSTED_PROXY_CIDRS` | Empty | Comma-separated IPv4/IPv6 prefixes for trusted immediate proxy peers. Required, parsed, and validated only in `trusted_proxy` mode; ignored in `direct` mode. |
| `GROK_INITIAL_REGISTRATION_MODE` | `disabled` | Initial registration policy: `disabled`, `invite`, or `free`. Used only when no persisted server-settings row exists. |
| `GROK_MAX_API_KEYS_PER_USER` | `20` | Startup-only per-user API-key row limit; accepted range 1-1,000. Disabled keys count and deletion frees capacity. |
| `GROK_AUTH_PASSWORD_MAX_CONCURRENT` | `4` | Startup-only process-wide bcrypt work limit for login, registration, and password changes; accepted range 1-64. |
| `GROK_AUTH_KEY_MISS_MAX_CONCURRENT` | `32` | Startup-only concurrent SQLite resolution limit for distinct API-key cache misses; same-key misses are coalesced; accepted range 1-1,024. |
| `GROK_USAGE_RAW_RETENTION_DAYS` | `7` | Raw usage and debug-detail retention before hourly compaction. |
| `GROK_USAGE_HOURLY_RETENTION_DAYS` | `90` | Hourly usage retention before daily compaction. |
| `GROK_USAGE_DAILY_RETENTION_DAYS` | `730` | Daily aggregate retention before deletion. |
| `GROK_USAGE_MAINTENANCE_INTERVAL` | `1h` | Interval for rollup, cleanup, and WAL checkpoint maintenance. |
| `GROK_SEARCH_MCP_IP_RPM` | `300` | Source-IP RPM applied before MCP API-key authentication to every request using the identity selected by `GROK_CLIENT_IP_MODE`. |
| `GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD` | `2048` | Maximum dedicated source-IP token buckets retained in each of the 64 registry shards. The default process-wide bound is 131,072 entries; accepted values are 1-65,536. Requires restart to change. |
| `GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD` | `16` | Fixed shared buckets used by new IPs when their shard remains full after expired-entry cleanup; accepted values are 1-1,024. Requires restart to change. |
| `GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY` | `16` | Environment default for the process-wide in-flight streaming search limit. The persisted panel setting takes precedence after initialization. |
| `GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY` | `4` | Environment default for the per-user limit; must not exceed the global limit. The persisted panel setting takes precedence after initialization. |
| `GROK_AUTH_USER_RPM_MAX_ENTRIES` | `16,384` | Startup-only maximum dedicated authenticated-user RPM entries; accepted range 1-65,536. Overflow identities use fixed shared fallback buckets. |
| `GROK_AUTH_USER_RPM_FALLBACK_BUCKETS` | `64` | Startup-only number of shared authenticated-user RPM fallback buckets; accepted range 1-1,024. |
| `GROK_SEARCH_MCP_DEBUG` | `false` | Accepts `1`, `true`, or `yes`. May capture debug request/response context in usage records. |
| `GROK_PROXY_URL` | Empty | Explicit upstream HTTP(S) proxy URL. |
| `GROK_PROXY_ENABLED` | `false` | Explicit proxy switch. Set this to `true` together with `GROK_PROXY_URL`; the URL alone does not enable the project-specific proxy. |
| `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | Go defaults | Used by the standard transport when an explicit proxy is not enabled. |

The former `GROK_MCP_IP_RPM`, `GROK_MCP_GLOBAL_SEARCH_CONCURRENCY`,
`GROK_MCP_USER_SEARCH_CONCURRENCY`, and `GROK_MCP_DEBUG` names remain accepted
as compatibility aliases. When both names are configured, the corresponding
`GROK_SEARCH_MCP_*` variable takes precedence.

When either search concurrency limit is exhausted, the server rejects the request immediately with HTTP `503` and `Retry-After: 1` instead of queueing another long-lived HTTP/SSE request. Search responses expose semaphore acquisition time in `X-Grok-Search-Queue-Time-Ms`.

### Client-IP trust modes

Application-level IP protection always requires a valid client identity. The same startup-only resolver is injected into:

- the `/mcp` token bucket that runs before API-key authentication;
- the panel login and registration endpoint token buckets;
- the panel username/IP failed-login lockout.

The two modes behave as follows:

| Mode / request state | IP-protection behavior |
|---|---|
| `direct` (default) | Uses the canonical IP from the connection's `RemoteAddr` for every request and completely ignores `X-Real-IP` and `X-Forwarded-For`, including malformed or spoofed values. A missing, malformed, or zoned `RemoteAddr` is rejected with HTTP `400`. |
| `trusted_proxy`, immediate peer outside `GROK_TRUSTED_PROXY_CIDRS` | Rejects with HTTP `403` without accepting any forwarded identity. |
| `trusted_proxy`, trusted peer with no forwarding header | Rejects with HTTP `400`; there is no headerless bypass. |
| `trusted_proxy`, trusted peer with malformed, duplicated, oversized, excessive-hop, or conflicting forwarding headers | Rejects with HTTP `400`. |
| `trusted_proxy`, trusted peer with valid forwarding headers | Uses `X-Real-IP` when present; otherwise uses the first `X-Forwarded-For` IP. If both are present, their canonical client addresses must agree. |

`GROK_TRUSTED_PROXY_CIDRS` accepts at most 256 comma-separated canonical IPv4
or IPv6 prefixes in trusted-proxy mode, where the list is mandatory. Direct
mode does not parse or validate this variable because it ignores all proxy
identity configuration. Trust applies only to the immediate TCP peer; the
trusted proxy remains responsible for removing client-supplied forwarding
headers and rebuilding them from its own connection metadata.

The `/mcp` source-IP registry is capacity bounded. Existing IPs keep their
dedicated token bucket until the normal idle TTL expires; they are never
evicted merely to admit a new identity. When a shard is full, the limiter first
removes expired entries. If it remains full, new IPs are mapped with a
process-randomized hash onto fixed shared fallback buckets and no per-IP map
entry is allocated. Multiple fallback identities can therefore share rate
state and may jointly receive `429` responses during saturation. The opt-in
admin operational-metrics endpoint exposes registry capacity, current entries,
fallback requests/rejections, admissions, and expirations without exposing IP
addresses.

The public panel authentication protector is also capacity bounded with fixed,
process-local budgets: 4,096 dedicated login IP buckets, 2,048 registration IP
buckets, 2,048 registration-challenge IP buckets, and 8,192 normalized
username/IP login-failure entries. Each endpoint has its own capacity domain
and 16 fixed fallback buckets. When an endpoint is full, expired entries are
reclaimed first; if it remains full, new IPs share fallback rate state without
creating map entries. Live dedicated buckets are never evicted merely to admit
new identities.

Login-failure state has no shared fallback because collisions could lock out
unrelated users. When its table remains full after expired-entry cleanup, a new
username/IP pair receives a generic `429` before user lookup or bcrypt. Existing
failure counts, active lockouts, and in-flight attempts are retained. These
budgets are fixed security limits rather than environment or panel settings.
The admin operational-metrics endpoint reports only aggregate capacity and
saturation counters. All panel-auth protector state and cumulative counters are
process-local and reset when the service restarts.

> [!IMPORTANT]
> Enable `trusted_proxy` only after identifying the CIDR of the proxy as seen by `grok-search-mcp`, which may be a container bridge or load-balancer subnet rather than the proxy's public address. A wrong CIDR fails closed with `403`. Keep proxy-layer rate limits enabled for `/mcp`, `/panel/v1/auth/login`, and `/panel/v1/auth/register`.

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

Pair that proxy with startup settings such as:

```dotenv
GROK_CLIENT_IP_MODE=trusted_proxy
GROK_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
```

Compose publishes `0.0.0.0:8080:8080`, so virtual machines, LAN clients, and
container-network clients can reach the service through the host. For an
internet-facing deployment, restrict external access with a firewall and a
trusted HTTPS reverse proxy.

### Persistence and live updates

On startup, environment variables provide the initial runtime defaults. `GROK_INITIAL_REGISTRATION_MODE` supplies registration policy only when SQLite has no server-settings row; its safe default is `disabled`. If SQLite already contains server settings, the complete persisted runtime settings object takes precedence, including registration mode, and restarting with a different initial value does not overwrite the administrator's choice. Listener address, database path, JWT secret, client-IP trust mode/CIDRs, IP RPM/registry capacity, and retention/maintenance settings remain environment-only. Administrators can update the following values from **Server Settings** without restarting:

- CPA base URL and API key
- Upstream search protocol
- Default model and timeout
- Explicit proxy URL and enabled state
- Registration mode
- Debug mode
- Process-wide and per-user streaming search concurrency limits
- Operational metrics collection

Settings updates are persisted before the running process applies them. The panel exposes separate persisted and confirmed-live settings versions. If persistence succeeds but live application fails, the saved values remain durable, the panel shows **saved but not applied** instead of a generic save failure, and the settings form reloads the persisted values. While the versions differ, upstream health is reported as unknown to avoid probing with mixed configuration state. A service restart loads the persisted revision and restores the versions to a synchronized state after startup succeeds.

The listen address, database path, JWT secret, client-IP mode/trusted CIDRs,
source-IP RPM, registry capacity, and fallback-bucket count remain startup-only
settings.

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

New databases begin with registration disabled unless
`GROK_INITIAL_REGISTRATION_MODE` explicitly selects `invite` or `free`. After
the initial settings row is created, registration can be changed at runtime and
the persisted value remains authoritative:

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

The `/mcp` middleware order is unchanged. The `IP RPM` step always resolves and validates a client identity before API-key authentication:

```text
MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM -> Search Concurrency -> Quota -> Usage -> MCP handler
```

## Administration API overview

The embedded panel is served from `/panel/`. Its API is under `/panel/v1`.

Public authentication routes:

```text
GET  /panel/v1/auth/registration-settings
POST /panel/v1/auth/registration-challenge
POST /panel/v1/auth/register
POST /panel/v1/auth/login
```

Registration uses a one-time proof of work. The client first requests a signed challenge that is valid for five minutes, locally finds a SHA-256 nonce satisfying the required difficulty, and submits `proof.challenge` plus `proof.nonce` with the registration request. The default target requires 20 leading zero bits. A successfully verified challenge is consumed and cannot be replayed. The embedded panel performs this work in a Web Worker so the page remains responsive.

Authenticated user routes cover profile information, credential/session
lifecycle, API-key management, and usage:

```text
GET    /panel/v1/me
POST   /panel/v1/me/change-password
POST   /panel/v1/me/revoke-sessions
GET    /panel/v1/overview/health
GET    /panel/v1/keys
POST   /panel/v1/keys
POST   /panel/v1/keys/{id}/reveal
PATCH  /panel/v1/keys/{id}
DELETE /panel/v1/keys/{id}
GET    /panel/v1/keys/{id}/usage
GET    /panel/v1/usage
GET    /panel/v1/usage/records
GET    /panel/v1/usage/records/{id}
```

Password changes require `current_password` and `new_password` values of 8-72
bytes. Both lifecycle endpoints increment the current user's `token_version`,
immediately invalidating every previously issued panel JWT, and return a
replacement `token`, `expires_at`, and current `user`. The account page stores
the replacement token and expiry together in `sessionStorage`. Revoke-all is
self-service panel-session revocation; it does not revoke MCP API keys.

`GET /panel/v1/overview/health` reports authenticated upstream/model availability for the dashboard. It is distinct from the container's unauthenticated `/panel/` liveness check.

Administrator routes under `/panel/v1/admin/` manage users, tiers, server settings, invite codes, models, and usage. All non-public panel requests require:

```text
Authorization: Bearer <panel JWT>
```

## Docker deployment

To build and run the current source checkout with the supplied Compose file:

```bash
cp .env.example .env
${EDITOR:-vi} .env
docker compose up -d --build
```

To run the published release image without rebuilding local source:

```bash
docker pull maplemaplecat/grok-search-mcp:latest
docker run -d \
  --name grok-search-mcp \
  --restart unless-stopped \
  --pull always \
  --env-file advanced.env \
  --env-file .env \
  --add-host host.docker.internal:host-gateway \
  -p 127.0.0.1:8080:8080 \
  -v grok-search-mcp-data:/app/data \
  maplemaplecat/grok-search-mcp:latest
```

Each published GitHub Release updates both its immutable version tag and the
mutable `latest` tag. Use a version tag instead when deployments must remain
pinned to an exact release. An already-running container is not replaced merely
because `latest` changes; recreate it through your deployment automation to
apply the new image.

For a direct published-image deployment, set the container-reachable CPA URL in
the basic `.env`. If CPA runs on the Docker host, use:

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

Compose passes enabled explicit-proxy and standard-proxy variables from
`advanced.env` into the container. If a proxy value contains real credentials,
put it in the untracked `.env` or an external secret manager instead.

## Production and security notes

- Put the service behind an HTTPS reverse proxy before exposing it publicly. The server does not provide TLS.
- Never expose panel JWTs, MCP client API keys, CPA keys, invite codes, or a real `.env` file.
- Protect the `0600` bootstrap credential file, rotate the password immediately, and exclude stale credential-file copies from ordinary backups/log collection.
- Restrict access to the SQLite file and include it in secure backups.
- Keep `GROK_CLIENT_IP_MODE=direct` when clients connect directly; forwarding headers are ignored and cannot select limiter identities.
- For reverse-proxy deployment, set `GROK_CLIENT_IP_MODE=trusted_proxy`, allow only the proxy's immediate-peer CIDRs, and make the proxy overwrite `X-Real-IP` and rebuild `X-Forwarded-For`. Missing headers fail with `400`; untrusted peers fail with `403`.
- Keep the plaintext application port loopback- or network-internally bound; the supplied Compose and `docker run` examples publish it on host loopback only.
- Add reverse-proxy rate limits for `/mcp`, panel login, and panel registration.
- Keep debug mode disabled unless troubleshooting. Debug context may retain request or response content even though authentication headers are redacted.
- MCP client API keys authenticate through an irreversible hash and are also stored as AES-256-GCM ciphertext for authorized panel reveal/copy. Protect database backups and `GROK_JWT_SECRET` as access to recoverable client credentials.

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

The panel frontend is embedded static HTML, CSS, and JavaScript; no Node.js build is required. The repository currently has no Makefile or task runner. A release/manual GitHub Actions workflow runs `go test ./...`, builds Linux archives, and publishes Docker images; there is not yet a push or pull-request validation workflow. Use standard Go tooling such as `gofmt` and `go vet` when contributing.

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
