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
- 管理面板 API：`/panel/v1/*`（注册/登录开放；其他接口需 JWT；管理员接口需 `role=admin`）
- 客户端 API Key 鉴权（Key 归属用户）
- 按用户汇总的 RPM、总请求上限、成功请求上限
- SQLite 持久化 API Key 与调用明细
- 仅统计真实 `tools/call` 调用，握手和工具列表请求不计入用量
- 上游 SSE 流式解析，并把搜索轮次转成 MCP progress 通知

## 快速开始

### 1. 构建

```powershell
go build -o grok-mcp.exe ./cmd/grok-mcp
```

可选：构建时注入版本号。

```powershell
go build -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" -o grok-mcp.exe ./cmd/grok-mcp
```

查看版本：

```powershell
./grok-mcp.exe -version
```

### 2. 配置并启动

```powershell
$env:CPA_BASE_URL = "http://127.0.0.1:8317"
$env:CPA_API_KEY = "replace-with-your-cpa-api-key"
$env:GROK_JWT_SECRET = "replace-with-a-strong-random-jwt-secret"
$env:GROK_HTTP_ADDR = ":8080"
$env:GROK_DB_PATH = "./grok-mcp.db"

./grok-mcp.exe
```

启动后：

- MCP 端点：`http://127.0.0.1:8080/mcp`
- 管理面板前端：`http://127.0.0.1:8080/panel/`
- 面板 API：`http://127.0.0.1:8080/panel/v1/*`

### 3. 注册、登录并创建客户端 API Key

```powershell
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8080/panel/v1/auth/register" `
  -ContentType "application/json" `
  -Body '{"username":"you","password":"your-password"}'

$login = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8080/panel/v1/auth/login" `
  -ContentType "application/json" `
  -Body '{"username":"you","password":"your-password"}'

$auth = @{ Authorization = "Bearer $($login.token)" }
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8080/panel/v1/keys" `
  -Headers $auth -ContentType "application/json" -Body '{"name":"local-client"}'
```

响应里的 `api_key` 只返回一次。后续 MCP 客户端访问 `/mcp` 时使用：

```text
Authorization: Bearer <api_key>
Accept: application/json, text/event-stream
Content-Type: application/json
```

### 4. Claude Code 客户端示例

Claude Code 连接的是 MCP 端点 `/mcp`，使用的是上一步创建的客户端 `api_key`，不是面板登录返回的 JWT。

```powershell
$env:GROK_MCP_API_KEY = "grok_xxx"

claude mcp add --transport http grok-mcp http://127.0.0.1:8080/mcp `
  --header "Authorization: Bearer $env:GROK_MCP_API_KEY"
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

## Docker Compose

复制配置模板并填入真实值：

```powershell
Copy-Item .env.example .env
```

启动：

```powershell
docker compose up -d --build
```

`.env` 至少需要设置：

- `CPA_API_KEY`
- `GROK_JWT_SECRET`

容器默认监听 `:8080`，SQLite 数据保存到命名卷 `grok-mcp-data`。

## 配置项

| 环境变量 | 必填 | 默认值 | 说明 |
|---|:---:|---|---|
| `CPA_API_KEY` | 是 | 无 | 调用 CPA 的 Bearer Key |
| `GROK_JWT_SECRET` | 是 | 无 | 面板 JWT HS256 签名密钥 |
| `GROK_DEFAULT_USER_RPM` | 否 | `60` | 新用户默认每分钟请求数 |
| `GROK_DEFAULT_USER_TOTAL_LIMIT` | 否 | `0` | 新用户默认总 `tools/call` 上限（0=不限） |
| `GROK_DEFAULT_USER_SUCCESS_LIMIT` | 否 | `0` | 新用户默认成功调用上限（0=不限） |
| `CPA_BASE_URL` | 否 | `http://127.0.0.1:8317` | CPA 根地址，不含尾部 `/` |
| `GROK_MODEL` | 否 | `grok-4.3` | 默认模型，可被工具参数 `model` 覆盖 |
| `GROK_HTTP_TIMEOUT` | 否 | `120` | 上游 HTTP 超时，单位秒 |
| `GROK_MCP_DEBUG` | 否 | 无 | 设为 `1`、`true` 或 `yes` 时输出调试日志 |
| `GROK_HTTP_ADDR` | 否 | `:8080` | HTTP 监听地址 |
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

首个注册用户自动为 `admin`。管理员可调整用户的 `rpm`、`total_limit`、`success_limit` 等。

## 代码结构

```text
cmd/grok-mcp/
  main.go                 进程入口
  http.go                 /mcp 与 /panel 路由组装

internal/panel/           面板 REST API
internal/quota/           用户汇总额度（tools/call）
internal/auth/            MCP API Key 与面板 JWT
internal/ratelimit/       按用户的内存 RPM 限流
internal/usage/           MCP tools/call 用量与 success 标记
internal/store/           SQLite、002 迁移、用户与 Key
```

## 测试

默认测试不触发真实上游调用：

```powershell
go test ./...
```

构建验证：

```powershell
go build ./cmd/grok-mcp
```

Docker Compose 配置验证：

```powershell
Copy-Item .env.example .env
docker compose config
Remove-Item .env
```

真实 CPA / xAI 集成测试需要显式打开：

```powershell
$env:GROK_INTEGRATION_TEST = "1"
$env:CPA_API_KEY = "replace-with-your-cpa-api-key"
$env:CPA_BASE_URL = "http://127.0.0.1:8317"
go test ./test/grok -run TestIntegrationSearchLiveCPA -v
```
