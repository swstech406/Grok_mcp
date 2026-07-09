# grok-mcp

`grok-mcp` 是一个 HTTP-only 的 MCP（Model Context Protocol）服务端。它把 Grok 的实时联网搜索与模型列举能力封装成 MCP 工具：

- `grok_web_search`：实时网页搜索
- `grok_x_search`：实时 X / Twitter 搜索
- `grok_list_models`：列举上游 CPA 可用的 Grok 模型（过滤 `imagine` / `video`）

本项目不直接对接 xAI 官方 API，而是作为已部署的 CLIProxyAPI（CPA）客户端工作：搜索请求转发到 CPA 的 `POST /v1/responses`，模型列表来自 CPA 的 `GET /v1/models`；CPA 负责到 xAI 的认证。

```text
支持 Streamable HTTP 的 MCP 客户端
        |
        |  POST /mcp
        |  Authorization: Bearer <grok-mcp 客户端 API Key>
        v
grok-mcp  (cmd/grok-mcp → internal/app 组合根)
        |
        |  POST /v1/responses  ·  GET /v1/models
        |  Authorization: Bearer <CPA_API_KEY>
        v
CLIProxyAPI
        |
        v
xAI / Grok
```

项目只保留 HTTP 模式（Streamable HTTP），运行时不再提供传输模式开关。

## 功能

- Streamable HTTP MCP 端点：`/mcp`
- 管理面板前端：`/panel/`；REST API：`/panel/v1/*`
  - 空库启动自动创建 `admin`（日志输出一次性随机密码）
  - 注册/登录开放；其余接口需面板 JWT；`/panel/v1/admin/*` 需 `role=admin`
- 客户端 API Key 鉴权（Key 归属用户；支持短 TTL 缓存，用户/tier 限额每次重新加载）
- 用户限额以 **tier** 为唯一来源（RPM、当月 success limit）；请求链路上为 `auth.AuthenticatedUser` 运行时视图
- SQLite 持久化用户、Key、tier、用量明细与可热更的上游服务器设置
- 仅统计真实 `tools/call`：握手 / `tools/list` 等不计入 RPM、配额与用量
- 上游 SSE 流式解析；搜索轮次可转成 MCP progress 通知
- `/mcp` 链路中间件顺序（由外到内生效）：  
  `MaxBody → IP RPM → API Key → ExtractToolName → User RPM → Quota → Usage → MCP handler`

## Linux 快速开始

### 1. 构建

```bash
go build -o grok-mcp ./cmd/grok-mcp
```

可选：构建时注入版本号。

```bash
go build -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" -o grok-mcp ./cmd/grok-mcp
```

查看版本：

```bash
./grok-mcp -version
```

### 2. 配置并启动

复制配置模板并填入真实值：

```bash
cp .env.example .env
mkdir -p data
${EDITOR:-vi} .env
```

启动服务：

```bash
set -a
source .env
set +a

./grok-mcp
```

启动后：

- MCP 端点：`http://127.0.0.1:8080/mcp`
- 管理面板前端：`http://127.0.0.1:8080/panel/`
- 面板 API：`http://127.0.0.1:8080/panel/v1/*`

### 3. 获取自动初始化管理员、登录并创建客户端 API Key

空库首次启动时，服务会自动创建用户名为 `admin` 的管理员账号，并在控制台 / Docker 日志中输出一次性随机 12 位密码。请首次登录后尽快在面板中创建新的管理员或轮换凭据。

```bash
login_token="$(curl -sS -X POST "http://127.0.0.1:8080/panel/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password-from-startup-log"}' | jq -r '.token')"

curl -sS -X POST "http://127.0.0.1:8080/panel/v1/keys" \
  -H "Authorization: Bearer ${login_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"local-client"}'
```

上面的示例使用 `jq` 提取登录 token；如果环境没有 `jq`，可从登录响应中手动复制 `token` 字段。

响应里的 `api_key` 只返回一次。后续 MCP 客户端访问 `/mcp` 时使用：

```text
Authorization: Bearer <api_key>
Accept: application/json, text/event-stream
Content-Type: application/json
```

### 4. Claude Code 客户端示例

Claude Code 连接的是 MCP 端点 `/mcp`，使用的是上一步创建的客户端 `api_key`，不是面板登录返回的 JWT。

```bash
export GROK_MCP_API_KEY="grok_xxx"

claude mcp add --transport http grok-mcp http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer ${GROK_MCP_API_KEY}"
```

添加后在 Claude Code 会话中执行：

```text
/mcp
```

确认 `grok-mcp` 已连接，并能看到工具：

- `grok_web_search`
- `grok_x_search`
- `grok_list_models`

实际使用时可以直接在 Claude Code 中提出搜索需求，Claude Code 会按需调用对应 MCP 工具。例如：

```text
使用 grok-mcp 搜索今天 OpenAI API 的最新发布，并列出来源。
```

```text
用 X 搜索最近 24 小时里关于 Grok 的主要讨论，给出摘要和链接。
```

如果需要明确指定工具，可以在提示词里写出工具名：`grok_web_search` 用于网页搜索，`grok_x_search` 用于 X / Twitter 搜索。

项目级共享配置也可以写入仓库根目录的 `.mcp.json`。不要把真实 `api_key` 提交进仓库，推荐使用环境变量展开：

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

如果 `grok-mcp` 部署在远程服务器，建议通过 HTTPS 反向代理暴露 `/mcp`，并把 URL 改成公网地址，例如 `https://mcp.example.com/mcp`。

### 5. 反向代理安全建议

服务端内置了面板登录/注册的内存限流、登录失败短期锁定，以及 `/mcp` 鉴权前来源 IP 限流；公网部署时仍建议在 Nginx、Caddy、Traefik 等反向代理层额外启用 IP 级 rate limit，尤其是：

- `POST /panel/v1/auth/login`
- `POST /panel/v1/auth/register`
- 对外开放的 `/mcp` 端点

Nginx 示例：

```nginx
limit_req_zone $binary_remote_addr zone=grok_panel_auth:10m rate=10r/m;
limit_req_zone $binary_remote_addr zone=grok_mcp:10m rate=60r/m;

location = /panel/v1/auth/login {
    limit_req zone=grok_panel_auth burst=5 nodelay;
    proxy_pass http://127.0.0.1:8080;
}

location = /panel/v1/auth/register {
    limit_req zone=grok_panel_auth burst=3 nodelay;
    proxy_pass http://127.0.0.1:8080;
}

location = /mcp {
    limit_req zone=grok_mcp burst=30 nodelay;
    proxy_pass http://127.0.0.1:8080;
}
```

如果反向代理与应用不在同一信任边界内，请在代理层完成限流，不要直接信任客户端伪造的 `X-Forwarded-For`。内置 IP 限流默认按 TCP 连接的 `RemoteAddr` 识别 IP；仅当设置了 `GROK_TRUSTED_PROXIES` 且对端命中时，才解析 `X-Forwarded-For` / `X-Real-IP`。

## Docker Compose

Docker 构建默认使用官方原生源：

- 构建镜像：`golang:1.25-alpine`
- 运行镜像：`alpine:3.20`
- Go module proxy：`https://proxy.golang.org,direct`

复制配置模板并填入真实值：

```bash
cp .env.example .env
${EDITOR:-vi} .env
```

如果 CPA 服务运行在宿主机而不是容器内，请把 `.env` 里的 `CPA_BASE_URL` 改为：

```bash
CPA_BASE_URL=http://host.docker.internal:8317
```

启动：

```bash
docker compose up -d --build
```

`.env` 至少需要设置：

- `CPA_API_KEY`
- `GROK_JWT_SECRET`

容器内默认监听 `:8080`，Compose 示例默认映射宿主机 `8080`。如需公网访问，请通过 HTTPS 反向代理暴露服务。SQLite 数据保存到命名卷 `grok-mcp-data`。

## 配置项

| 环境变量 | 必填 | 默认值 | 说明 |
|---|:---:|---|---|
| `CPA_API_KEY` | 是* | 无 | 调用 CPA 的 Bearer Key（*启动时可由 DB 中已保存的服务器设置覆盖/补全） |
| `GROK_JWT_SECRET` | 是 | 无 | 面板 JWT HS256 签名密钥（至少 32 字节） |
| `CPA_BASE_URL` | 否 | `http://127.0.0.1:8317` | CPA 根地址，不含尾部 `/` |
| `GROK_MODEL` | 否 | `grok-4.3` | 默认模型，可被工具参数 `model` 覆盖 |
| `GROK_HTTP_TIMEOUT` | 否 | `120` | 上游 HTTP 超时，单位秒 |
| `GROK_MCP_DEBUG` | 否 | 无 | 设为 `1`、`true` 或 `yes` 时输出调试日志，并可能在用量记录中捕获 debug 上下文 |
| `GROK_HTTP_ADDR` | 否 | `:8080` | HTTP 监听地址；直接公网暴露明文 HTTP 会泄露 JWT/API key |
| `GROK_DB_PATH` | 否 | `./grok-mcp.db` | SQLite 数据库路径 |
| `GROK_DEFAULT_USER_RPM` | 否 | 未设置时为 `-1` | 仅作为内存 `UserLimiter` 构造参数；用户实际 RPM **始终来自 tier**。未设置时无正数兜底（`rpm==0` 表示不限）。设为非 0 整数可作兜底；`0` 非法 |
| `GROK_MCP_IP_RPM` | 否 | `300` | `/mcp` 在 API Key 鉴权前按来源 IP 限流的 RPM |
| `GROK_TRUSTED_PROXIES` | 否 | 空 | 可信反向代理 CIDR/IP（逗号分隔）；命中时才解析转发头 |
| `GROK_PROXY_URL` | 否 | 空 | 上游请求显式 HTTP(S) 代理 |
| `GROK_PROXY_ENABLED` | 否 | 见说明 | 是否启用 `GROK_PROXY_URL`；未设显式代理时仍可回退 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` |

说明：

- 用户限额（RPM / 当月 success limit）**只**由 tier 决定，在面板 Tier Management 维护。
- CPA URL/Key、模型、超时、代理、debug 等可在管理员面板 **Server Settings** 中热更新并写回 SQLite；监听地址、DB 路径、JWT 密钥仍需改环境变量后重启。
- （已移除 `GROK_ADMIN_TOKEN` 与 `/admin/v1`；请使用 `/panel/v1`。）

## MCP 工具

### `grok_web_search`

参数：

| 参数 | 类型 | 必填 | 说明 |
|---|---|:---:|---|
| `query` | string | 是 | 搜索问题 |
| `model` | string | 否 | 覆盖默认模型 |
| `allowed_domains` | string[] | 否 | 仅搜索指定域名，最多 5 个 |
| `excluded_domains` | string[] | 否 | 排除指定域名，最多 5 个 |
| `enable_image_understanding` | bool | 否 | 启用网页图片理解 |
| `enable_image_search` | bool | 否 | 启用图片搜索结果 |

`allowed_domains` 和 `excluded_domains` 不能同时使用。

### `grok_x_search`

参数：

| 参数 | 类型 | 必填 | 说明 |
|---|---|:---:|---|
| `query` | string | 是 | 搜索问题 |
| `model` | string | 否 | 覆盖默认模型 |

### `grok_list_models`

无入参。从上游 CPA `GET /v1/models` 拉取模型列表，服务端只保留 ID 中包含 `grok`、且不包含 `imagine` / `video` 的模型。

成功时大致结构：

```json
{
  "models": [
    {"id": "grok-4.3"}
  ]
}
```

## 返回结构（搜索类工具）

`grok_web_search` 与 `grok_x_search` 返回同一类结构：

```json
{
  "answer": "Grok 综合检索后给出的答案文本",
  "citations": [
    "https://example.com/source-1"
  ],
  "sources": [
    {"url": "https://example.com/source-1", "title": "Source One"}
  ],
  "usage": {
    "input_tokens": 120,
    "output_tokens": 340,
    "total_tokens": 460,
    "reasoning_tokens": 0
  }
}
```

搜索失败时以 MCP 工具结果 `isError=true` 返回错误文案，会话保持连接（不作为传输层 Go error 断开）。

## 面板 API（`/panel/v1`）

后端内置无构建步骤的管理面板前端，访问：

```text
GET /panel/
```

前端在浏览器本地保存登录 JWT，用于调用 `/panel/v1`。

除注册/登录外，请求需携带：

```text
Authorization: Bearer <JWT>
```

认证与用户 Key：

```text
POST   /panel/v1/auth/register
POST   /panel/v1/auth/login
GET    /panel/v1/me
GET    /panel/v1/keys
POST   /panel/v1/keys
PATCH  /panel/v1/keys/{id}
DELETE /panel/v1/keys/{id}
GET    /panel/v1/keys/{id}/usage
GET    /panel/v1/usage
```

管理员（`role=admin`）：

```text
GET    /panel/v1/admin/users
GET    /panel/v1/admin/users/{id}
PATCH  /panel/v1/admin/users/{id}
DELETE /panel/v1/admin/users/{id}
GET    /panel/v1/admin/users/{id}/usage
GET    /panel/v1/admin/tiers
POST   /panel/v1/admin/tiers
PATCH  /panel/v1/admin/tiers/{id}
DELETE /panel/v1/admin/tiers/{id}
GET    /panel/v1/admin/settings
PATCH  /panel/v1/admin/settings
GET    /panel/v1/admin/models
```

空库启动时会自动创建 `admin` 管理员并在启动日志输出一次性随机 12 位密码；自助注册始终创建普通用户。用户限额（`rpm`、当月 `success_limit`）以 tier 为唯一来源：管理员在 Tier Management 维护预设，在用户编辑页为用户指定 tier。当月成功调用额度在 `tools/call` 前原子预留，失败时回滚。

## 代码结构

```text
cmd/grok-mcp/
  main.go                 薄入口：配置、MCP 注册、信号、调用 app.Run

internal/app/             HTTP 组合根：DB/bootstrap、中间件链、路由、优雅退出
internal/config/          环境变量加载、校验、ServerSettings 与 store 映射
internal/auth/            API Key / 面板 JWT；AuthenticatedUser（含 tier 生效限额）
internal/panel/           面板 REST API（/panel/v1）
internal/panelui/         面板前端静态资源（embed）
internal/quota/           tools/call 成功额度预留（小接口 SuccessQuotaReserver）
internal/ratelimit/       用户 RPM 与来源 IP RPM（令牌桶）
internal/usage/           tools/call 用量、debug 捕获、失败时 success 回滚
internal/store/           SQLite、迁移、用户/Key/tier/设置、异步用量写入
internal/grok/            上游 CPA 客户端、SSE、模型列表过滤
internal/mcp/             MCP 工具注册与 ServerInstructions
internal/keyhash/         API Key 哈希
internal/logx/            调试日志
internal/version/         构建时注入的版本号

test/http/                HTTP 集成与防护测试
test/grok/                上游集成测试（需显式开关）
```

设计要点（近期结构调整）：

- **`cmd` 薄、`app` 厚**：进程入口与 HTTP 装配分离，便于测试 bootstrap / 安全头等组合逻辑。
- **持久化用户 vs 运行时用户**：`store.User` 不含 RPM/SuccessLimit；鉴权后写入 `auth.AuthenticatedUser`。
- **消费方小接口**：`auth` / `quota` / `usage` 只依赖各自需要的 store 方法，而不是处处绑定完整 `store.Store`。
- **ServerSettings 单一映射**：`config.ServerSettingsFromStore` / `StoreServerSettings` 集中转换，避免字段拷贝散落。

## 测试

默认测试不触发真实上游调用：

```bash
go test ./...
```

构建验证：

```bash
go build ./cmd/grok-mcp
```

真实 CPA / xAI 集成测试需要显式打开：

```bash
export GROK_INTEGRATION_TEST="1"
export CPA_API_KEY="replace-with-your-cpa-api-key"
export CPA_BASE_URL="http://127.0.0.1:8317"
go test ./test/grok -run TestIntegrationSearchLiveCPA -v
```
