# grok-mcp

`grok-mcp` 是一个 HTTP-only 的 MCP（Model Context Protocol）服务端。它把 Grok 的实时联网搜索能力封装成两个 MCP 工具：

- `grok_web_search`：实时网页搜索
- `grok_x_search`：实时 X / Twitter 搜索

本项目不直接对接 xAI 官方 API，而是作为已部署的 CLIProxyAPI（CPA）客户端工作：所有搜索请求都会转发到 CPA 的 `POST /v1/responses`，CPA 负责处理到 xAI 的认证。

```text
支持 Streamable HTTP 的 MCP 客户端
        |
        |  POST /mcp
        |  Authorization: Bearer <grok-mcp 客户端 API Key>
        v
grok-mcp
        |
        |  POST /v1/responses
        |  Authorization: Bearer <CPA_API_KEY>
        v
CLIProxyAPI
        |
        v
xAI / Grok
```

项目现在只保留 HTTP 模式，运行时不再提供传输模式开关。

## 功能

- Streamable HTTP MCP 端点：`/mcp`
- 管理面板 API：`/panel/v1/*`（默认仅允许空库 bootstrap 首个管理员；登录开放；其他接口需 JWT；管理员接口需 `role=admin`）
- 客户端 API Key 鉴权（Key 归属用户）
- 按用户汇总的 RPM 与 success limit
- SQLite 持久化 API Key 与调用明细
- 仅统计真实 `tools/call` 调用，握手和工具列表请求不计入用量
- 上游 SSE 流式解析，并把搜索轮次转成 MCP progress 通知

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

复制 Linux 本地配置模板，并填入真实值：

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

### 3. Bootstrap 管理员、登录并创建客户端 API Key

默认 `GROK_PANEL_REGISTRATION=bootstrap-only`：只允许空库注册首个管理员，后续用户请由管理员在面板中管理。如配置了 `GROK_SETUP_TOKEN`，注册请求还必须携带同名的 `setup_token` JSON 字段。

```bash
curl -sS -X POST "http://127.0.0.1:8080/panel/v1/auth/register" \
  -H "Content-Type: application/json" \
  -d '{"username":"you","password":"your-password"}'

login_token="$(curl -sS -X POST "http://127.0.0.1:8080/panel/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"you","password":"your-password"}' | jq -r '.token')"

curl -sS -X POST "http://127.0.0.1:8080/panel/v1/keys" \
  -H "Authorization: Bearer ${login_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"local-client"}'
```

上面的示例使用 `jq` 提取登录 token；如果你的 Linux 环境没有安装 `jq`，也可以从登录响应中手动复制 `token` 字段。

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

如果反向代理与应用不在同一信任边界内，请在代理层完成限流，不要直接信任客户端伪造的 `X-Forwarded-For`。内置限流默认按 TCP 连接的 `RemoteAddr` 识别 IP。

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

容器内默认监听 `:8080`，Compose 示例默认只绑定宿主机 `127.0.0.1:8080`。如需公网访问，请通过 HTTPS 反向代理暴露服务。SQLite 数据保存到命名卷 `grok-mcp-data`。

## 配置项

| 环境变量 | 必填 | 默认值 | 说明 |
|---|:---:|---|---|
| `CPA_API_KEY` | 是 | 无 | 调用 CPA 的 Bearer Key |
| `GROK_JWT_SECRET` | 是 | 无 | 面板 JWT HS256 签名密钥 |
| `GROK_PANEL_REGISTRATION` | 否 | `bootstrap-only` | 自助注册策略：`bootstrap-only`、`open` 或 `disabled` |
| `GROK_SETUP_TOKEN` | 否 | 无 | 设置后，注册请求必须携带匹配的 `setup_token` |
| `GROK_DEFAULT_USER_RPM` | 否 | `60` | 内存限流器的兜底 RPM；用户实际 RPM 由 tier 决定 |
| `GROK_MCP_IP_RPM` | 否 | `300` | `/mcp` API key 鉴权前按 TCP 来源 IP 限流的 RPM |
| `CPA_BASE_URL` | 否 | `http://127.0.0.1:8317` | CPA 根地址，不含尾部 `/` |
| `GROK_MODEL` | 否 | `grok-4.3` | 默认模型，可被工具参数 `model` 覆盖 |
| `GROK_HTTP_TIMEOUT` | 否 | `120` | 上游 HTTP 超时，单位秒 |
| `GROK_MCP_DEBUG` | 否 | 无 | 设为 `1`、`true` 或 `yes` 时输出调试日志 |
| `GROK_HTTP_ADDR` | 否 | `127.0.0.1:8080` | HTTP 监听地址；直接公网暴露明文 HTTP 会泄露 JWT/API key |
| `GROK_DB_PATH` | 否 | `./grok-mcp.db` | SQLite 数据库路径 |

（已移除 `GROK_ADMIN_TOKEN` 与 `/admin/v1`；请使用 `/panel/v1`。）

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

## 返回结构

两个工具都返回同一类结构：

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

## 面板 API（`/panel/v1`）

后端内置一个无构建步骤的管理面板前端，访问：

```text
GET /panel/
```

前端页面会在浏览器本地保存登录 JWT，用于调用 `/panel/v1`。

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
```

管理员（`role=admin`）：

```text
GET    /panel/v1/admin/users
GET    /panel/v1/admin/users/{id}
PATCH  /panel/v1/admin/users/{id}
GET    /panel/v1/admin/users/{id}/usage
```

默认注册策略为 `bootstrap-only`：首个注册用户自动为 `admin`，空库初始化完成后自助注册会返回 403。若确需继续开放注册，可显式设置 `GROK_PANEL_REGISTRATION=open`；生产公网部署建议保留默认值，并可额外设置 `GROK_SETUP_TOKEN` 防止首个管理员被抢注。用户限额（`rpm`、`success_limit`）以 tier 为唯一来源，管理员通过 Tier Management 页维护 tier 预设，并在用户编辑页为用户指定 tier。

## 代码结构

```text
cmd/grok-mcp/
  main.go                 进程入口
  http.go                 /mcp 与 /panel 路由组装

internal/panel/           面板 REST API（/panel/v1）
internal/panelui/         面板前端静态资源（embed）
internal/quota/           用户成功请求额度（tools/call 的 success 预留与失败回滚）
internal/auth/            MCP API Key 与面板 JWT（HS256，含 iss/aud 校验）
internal/ratelimit/       按用户和来源 IP 的内存 RPM 限流（令牌桶）
internal/usage/           MCP tools/call 用量与 success 标记，含 panic 回滚
internal/store/           SQLite、迁移、用户与 Key CRUD、异步用量写入
internal/grok/            上游 CPA /v1/responses 客户端与 SSE 解析
internal/mcp/             MCP 工具注册（grok_web_search、grok_x_search）
internal/config/          环境变量加载与校验
internal/logx/            调试日志器 + slog 包级实例
internal/version/         构建时注入的版本号
```

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
