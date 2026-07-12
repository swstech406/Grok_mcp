# grok-mcp

[简体中文](./README_CN.md)

`grok-mcp` is an HTTP-only [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that exposes Grok-powered real-time web search, X/Twitter search, and model discovery to MCP clients.

It does **not** call the official xAI API directly. Instead, it connects to an existing [CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) deployment. CPA owns the upstream xAI authentication, while `grok-mcp` provides MCP transport, client API keys, quotas, usage tracking, and an administration panel.

> [!IMPORTANT]
> This project supports **Streamable HTTP only**. It does not provide a stdio transport or built-in TLS termination.

## Features

- Streamable HTTP MCP endpoint at `/mcp`
- Three read-only MCP tools:
  - `grok_web_search`
  - `grok_x_search`
  - `grok_list_models`
- Grok response streaming through CPA `/v1/responses`
- MCP progress notifications for upstream search rounds
- Per-user client API keys with enable/disable controls
- Tier-based RPM and monthly successful-call quotas
- Pre-authentication source-IP rate limiting for `/mcp`
- SQLite persistence for users, keys, tiers, usage, invite codes, and server settings
- Embedded administration panel with no separate frontend build step
- Runtime updates for upstream settings, proxy settings, registration mode, and debug mode
- Docker Compose deployment with a non-root runtime image

## Architecture

```text
Streamable HTTP MCP client
        |
        |  POST /mcp
        |  Authorization: Bearer <MCP client API key>
        v
grok-mcp
  |     |
  |     +---- /panel/ and /panel/v1/* ---- administrators and users
  |
  +---------- SQLite -------------------- users, keys, tiers, usage, settings
  |
  |  POST /v1/responses
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
| CPA API key | `grok-mcp` -> CPA | Authenticates upstream `/v1/responses` and `/v1/models` requests. |
| MCP client API key | MCP client -> `/mcp` | Created and copied on demand in the panel. The database stores an authentication hash plus recoverable ciphertext encrypted with a key derived from `GROK_JWT_SECRET`. |
| Panel JWT | Browser/API client -> `/panel/v1` | Returned by panel login. It cannot authenticate `/mcp`. |

## Requirements

- Linux is the currently documented local runtime target
- Go 1.25.0 or later for local builds
- A reachable CPA deployment with compatible `/v1/responses` and `/v1/models` endpoints
- Docker and Docker Compose for the container workflow, if preferred
- An MCP client that supports Streamable HTTP and custom Bearer headers

The application uses pure-Go SQLite (`modernc.org/sqlite`) and does not require CGO.

## Quick start

### 1. Build

```bash
go build -o grok-mcp ./cmd/grok-mcp
```

Optionally inject a version at build time:

```bash
go build \
  -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" \
  -o grok-mcp ./cmd/grok-mcp

./grok-mcp -version
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

./grok-mcp
```

Default endpoints:

| Service | URL |
|---|---|
| MCP | `http://127.0.0.1:8080/mcp` |
| Administration panel | `http://127.0.0.1:8080/panel/` |
| Panel REST API | `http://127.0.0.1:8080/panel/v1/` |

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
export GROK_MCP_API_KEY="grok_xxx"

claude mcp add --transport http grok-mcp http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer ${GROK_MCP_API_KEY}"
```

A project-level `.mcp.json` can use environment expansion:

```json
{
  "mcpServers": {
    "grok-mcp": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": {
        "Authorization": "Bearer ${GROK_MCP_API_KEY}"
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
| `GROK_MODEL` | `grok-4.3` | Default Grok model. |
| `GROK_HTTP_TIMEOUT` | `120` | Upstream timeout in seconds. |
| `GROK_HTTP_ADDR` | `:8080` | HTTP listen address. Requires restart to change. |
| `GROK_DB_PATH` | `./grok-mcp.db` | SQLite database path. Requires restart to change. |
| `GROK_MCP_IP_RPM` | `300` | Source-IP RPM applied before MCP API-key authentication. |
| `GROK_TRUSTED_PROXIES` | Empty | Comma-separated trusted proxy IPs/CIDRs. Forwarded IP headers are ignored unless the direct peer is trusted. |
| `GROK_MCP_DEBUG` | `false` | Accepts `1`, `true`, or `yes`. May capture debug request/response context in usage records. |
| `GROK_PROXY_URL` | Empty | Explicit upstream HTTP(S) proxy URL. |
| `GROK_PROXY_ENABLED` | Inferred | Explicit proxy switch. When unset, a non-empty `GROK_PROXY_URL` enables it. |
| `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | Go defaults | Used by the standard transport when an explicit proxy is not enabled. |

### Persistence and live updates

On startup, environment variables are loaded first. If SQLite already contains server settings, the persisted upstream settings take precedence. Administrators can update the following values from **Server Settings** without restarting:

- CPA base URL and API key
- Default model and timeout
- Explicit proxy URL and enabled state
- Registration mode
- Debug mode

The listen address, database path, JWT secret, source-IP RPM, and trusted proxies remain startup-only settings.

> [!WARNING]
> The CPA API key is persisted in SQLite. Protect and back up the database as sensitive data. The panel only returns a masked preview of this key.

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

The `/mcp` middleware order is:

```text
MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM -> Quota -> Usage -> MCP handler
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
- Uses the `grok-mcp-data` named volume in Compose
- Health-checks `/panel/`

The Compose file does not forward every optional proxy or trusted-proxy variable by default. Extend its `environment` section if your deployment needs `GROK_TRUSTED_PROXIES`, `GROK_PROXY_URL`, `GROK_PROXY_ENABLED`, or the standard proxy variables.

## Production and security notes

- Put the service behind an HTTPS reverse proxy before exposing it publicly. The server does not provide TLS.
- Never expose panel JWTs, MCP client API keys, CPA keys, invite codes, or a real `.env` file.
- Rotate the bootstrap administrator credentials immediately.
- Restrict access to the SQLite file and include it in secure backups.
- Configure `GROK_TRUSTED_PROXIES` only for proxies within your trust boundary.
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
go build ./cmd/grok-mcp
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
cmd/grok-mcp/       Process entry point and version flag
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
