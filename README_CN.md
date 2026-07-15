# grok-mcp

[English](./README.md)

`grok-mcp` 是一个仅提供 HTTP 传输的 [Model Context Protocol（MCP）](https://modelcontextprotocol.io/)服务端，将 Grok 的实时网页搜索、X/Twitter 搜索和模型发现能力暴露给 MCP 客户端。

本项目**不直接调用 xAI 官方 API**，而是连接已经部署的 [CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI)。CPA 负责上游 xAI 认证，`grok-mcp` 负责 MCP 传输、客户端 API Key、限流配额、用量统计和管理面板。

> [!IMPORTANT]
> 本项目仅支持 **Streamable HTTP**，不提供 stdio 传输，也不内置 TLS 终止。

- `grok-mcp` 必须作为独立 HTTP 服务启动，MCP 客户端通过 `http://<host>:<port>/mcp` 连接。
- 不能将本项目配置为由 MCP 客户端通过命令启动、再使用标准输入和标准输出通信的 stdio 服务。
- 服务自身只监听普通 HTTP，不读取 HTTPS 证书或私钥，也不负责 TLS 握手。
- 公网部署时，应在 `grok-mcp` 前放置 Nginx、Caddy、Traefik、Kubernetes Ingress 或云负载均衡器等可信反向代理，由代理对外提供 HTTPS，再通过内部 HTTP 转发到 `grok-mcp`。

典型的生产请求链路如下：

```text
MCP 客户端 -- HTTPS --> 反向代理 / 负载均衡器 -- HTTP --> grok-mcp /mcp
                         （TLS 在此终止）
```

## 功能特性

- `/mcp` Streamable HTTP MCP 端点
- 三个只读 MCP 工具：
  - `grok_web_search`
  - `grok_x_search`
  - `grok_list_models`
- 可选择 CPA 上游协议：OpenAI Responses、OpenAI Chat Completions 或 Anthropic Messages
- 将上游搜索轮次转换为 MCP progress 通知
- 用户级客户端 API Key，可单独启用或禁用
- 基于 tier 的 RPM 和每月成功调用额度
- 由有效转发 IP 触发的 `/mcp` 与面板认证 IP 防护
- 使用 SQLite 持久化用户、Key、tier、用量、邀请码和服务设置
- 内嵌管理面板，无独立前端构建步骤
- 上游、搜索并发、代理、注册模式和 debug 设置支持运行时热更新
- 使用非 root 运行镜像的 Docker Compose 部署

## 架构

```text
支持 Streamable HTTP 的 MCP 客户端
        |
        |  POST /mcp
        |  Authorization: Bearer <MCP 客户端 API Key>
        v
grok-mcp
  |     |
  |     +---- /panel/ 与 /panel/v1/* ---- 管理员和用户
  |
  +---------- SQLite ------------------- 用户、Key、tier、用量、设置
  |
  |  POST /v1/responses、/v1/chat/completions 或 /v1/messages
  |  GET  /v1/models
  |  Authorization: Bearer <CPA API Key>
  v
CLIProxyAPI
  |
  v
xAI / Grok
```

### 三类凭证不可混用

| 凭证 | 使用位置 | 用途 |
|---|---|---|
| CPA API Key | `grok-mcp` -> CPA | 认证所选上游搜索端点和 `/v1/models` 请求。 |
| MCP 客户端 API Key | MCP 客户端 -> `/mcp` | 在面板创建并可按需复制；数据库保存鉴权哈希和由 `GROK_JWT_SECRET` 派生密钥加密的可恢复密文。 |
| 面板 JWT | 浏览器/API 客户端 -> `/panel/v1` | 登录面板后返回，不能用于认证 `/mcp`。 |

## 环境要求

- 当前文档化的本地运行目标为 Linux
- 本地构建需要 Go 1.25.0 或更高版本
- 可访问 `/v1/models`，并至少兼容 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 之一的 CPA 服务
- 容器部署可选用 Docker 和 Docker Compose
- MCP 客户端需要支持 Streamable HTTP 和自定义 Bearer Header

项目使用纯 Go SQLite 驱动 `modernc.org/sqlite`，不依赖 CGO。

## 快速开始

### 1. 构建

```bash
go build -o grok-mcp ./cmd/grok-mcp
```

可以在构建时注入版本号：

```bash
go build \
  -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" \
  -o grok-mcp ./cmd/grok-mcp

./grok-mcp -version
```

### 2. 配置并启动

```bash
cp .env.example .env
mkdir -p data
${EDITOR:-vi} .env
```

新数据库首次启动至少需要：

```dotenv
CPA_API_KEY=replace-with-your-cpa-api-key
GROK_JWT_SECRET=replace-with-a-strong-random-secret-of-at-least-32-bytes
```

加载环境变量并启动：

```bash
set -a
source .env
set +a

./grok-mcp
```

默认端点：

| 服务 | 地址 |
|---|---|
| MCP | `http://127.0.0.1:8080/mcp` |
| 管理面板 | `http://127.0.0.1:8080/panel/` |
| 面板 REST API | `http://127.0.0.1:8080/panel/v1/` |

### Usage 数据保留与 SQLite 维护

用量数据会按逐级降低的时间分辨率保留，避免长期运行后仍保存全部请求明细：

| 环境变量 | 默认值 | 用途 |
|---|---:|---|
| `GROK_USAGE_RAW_RETENTION_DAYS` | `7` | 保留逐请求明细和 debug 数据，之后压缩为小时级历史。 |
| `GROK_USAGE_HOURLY_RETENTION_DAYS` | `90` | 保留小时级历史，之后压缩为日级历史。 |
| `GROK_USAGE_DAILY_RETENTION_DAYS` | `730` | 删除超过此期限的日级历史。 |
| `GROK_USAGE_MAINTENANCE_INTERVAL` | `1h` | 执行聚合、清理以及主库和 debug 库的 WAL checkpoint。 |

小时级保留期限必须大于原始明细期限，日级保留期限必须大于小时级期限。
历史总量和流量图会合并原始、小时和日级数据；最近调用明细与单条 debug
详情只在对应原始记录仍处于保留期内时可用。

主数据库和 `<GROK_DB_PATH>.debug.sqlite` 都使用 WAL 模式。在线备份应对两个
数据库都使用 SQLite 在线备份机制，不能在服务运行时只复制主 `.db` 文件。
如果使用文件系统复制，应先停止服务，并同时复制两个数据库及其 WAL/SHM
旁路文件。定时维护会 checkpoint WAL，但不会自动执行 `VACUUM`；只有在需要
回收数据库文件本身空间时，才应由运维人员低频显式执行 `VACUUM` 或
`VACUUM INTO`。

### 3. 登录并创建 MCP 客户端 Key

当数据库中没有已启用的管理员时，服务会初始化 `admin` 账号，并在启动日志中输出一次性随机密码。登录后应尽快轮换凭据，再创建 MCP 客户端 API Key。

```bash
login_token="$(curl -sS -X POST "http://127.0.0.1:8080/panel/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password-from-startup-log"}' | jq -r '.token')"

curl -sS -X POST "http://127.0.0.1:8080/panel/v1/keys" \
  -H "Authorization: Bearer ${login_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"local-client"}'
```

响应中的 `api_key` 可立即使用；之后也可以在 **API 密钥** 页面按需复制。

### 4. 连接 Claude Code

Claude Code 是当前仓库内提供了明确配置示例的客户端：

```bash
export GROK_MCP_API_KEY="grok_xxx"

claude mcp add --transport http grok-mcp http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer ${GROK_MCP_API_KEY}"
```

项目级 `.mcp.json` 可以使用环境变量展开：

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

不要提交真实 API Key。其他客户端只要支持 Streamable HTTP 和自定义 `Authorization: Bearer ...` Header，协议上即可接入；仓库未提供的特定客户端配置不应视为已验证。

## MCP 工具

所有工具均为只读。搜索失败会作为 `isError=true` 的 MCP 工具结果返回，正常工具错误不会中断 MCP 会话。

### `grok_web_search`

通过 Grok 执行实时公开网页搜索。

| 参数 | 类型 | 必填 | 说明 |
|---|---|:---:|---|
| `query` | string | 是 | 非空搜索请求。 |
| `model` | string | 否 | 覆盖默认模型，值必须包含 `grok`。 |
| `allowed_domains` | string[] | 否 | 只搜索指定域名，最多 5 项。 |
| `excluded_domains` | string[] | 否 | 排除指定域名，最多 5 项。 |
| `enable_image_understanding` | boolean | 否 | 启用网页图片理解。 |
| `enable_image_search` | boolean | 否 | 启用图片搜索结果。 |

`allowed_domains` 与 `excluded_domains` 不能同时使用。域名项必须是纯域名，不能是 URL；通配符、IP、端口、路径、`localhost` 和 `.local` 域名会被拒绝。

### `grok_x_search`

通过 Grok 实时搜索 X/Twitter 帖子。

| 参数 | 类型 | 必填 | 说明 |
|---|---|:---:|---|
| `query` | string | 是 | 非空搜索请求。 |
| `model` | string | 否 | 覆盖默认模型，值必须包含 `grok`。 |

域名筛选和图片相关参数只适用于 `grok_web_search`。

具体的上游映射取决于所选协议：Responses 使用 CPA 原生的 `x_search` 工具，Chat Completions 使用 `x` 搜索来源，Anthropic Messages 使用 CPA 支持的服务端网页搜索工具并限制在 `x.com`。之所以不声明自定义 Anthropic `x_search` 工具，是因为 CPA 会将其视为需要客户端执行并回传结果的工具调用，单独使用不会产生最终搜索答案。

### `grok_list_models`

无参数。工具读取 CPA `GET /v1/models`，清理并去重模型 ID，只保留包含 `grok` 且不包含 `imagine`、`video` 的项目。

### 搜索结果结构

```json
{
  "answer": "Grok 综合检索后的回答",
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

上游未提供时，`citations`、`sources` 和 `usage` 可能省略。服务会主动拒绝 JSON-RPC batch 请求，避免批量调用绕过逐次配额预留和用量统计。

## 配置

### 启动环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `GROK_JWT_SECRET` | 无 | 面板 HS256 签名密钥，必填且至少 32 字节，始终通过环境变量提供。 |
| `CPA_API_KEY` | 无 | 新数据库必填；后续启动可以由 SQLite 中的服务设置提供。 |
| `CPA_BASE_URL` | `http://127.0.0.1:8317` | CPA 根地址。 |
| `GROK_UPSTREAM_PROTOCOL` | `responses` | 搜索协议：`responses`、`chat_completions` 或 `anthropic_messages`。 |
| `GROK_MODEL` | `grok-4.3` | 默认 Grok 模型。 |
| `GROK_HTTP_TIMEOUT` | `120` | 上游连接、TLS 握手和响应头各阶段的超时秒数，不限制已建立 SSE 响应体的持续时间；总搜索生命周期由调用方取消控制。 |
| `GROK_HTTP_ADDR` | `:8080` | HTTP 监听地址，修改后需要重启。 |
| `GROK_DB_PATH` | `./grok-mcp.db` | SQLite 路径，修改后需要重启。 |
| `GROK_USAGE_RAW_RETENTION_DAYS` | `7` | 原始用量和 debug 明细保留期限，之后压缩为小时级数据。 |
| `GROK_USAGE_HOURLY_RETENTION_DAYS` | `90` | 小时级用量保留期限，之后压缩为日级数据。 |
| `GROK_USAGE_DAILY_RETENTION_DAYS` | `730` | 日级聚合超过此期限后删除。 |
| `GROK_USAGE_MAINTENANCE_INTERVAL` | `1h` | 聚合、清理和 WAL checkpoint 的执行间隔。 |
| `GROK_MCP_IP_RPM` | `300` | 仅当 `X-Real-IP` 或 `X-Forwarded-For` 包含有效 IP 时，在 MCP API Key 鉴权前应用的来源 IP RPM。 |
| `GROK_MCP_GLOBAL_SEARCH_CONCURRENCY` | `16` | 进程级流式搜索同时在途上限的环境默认值；初始化后以面板持久化设置为准。 |
| `GROK_MCP_USER_SEARCH_CONCURRENCY` | `4` | 单用户上限的环境默认值，不得超过全局上限；初始化后以面板持久化设置为准。 |
| `GROK_MCP_DEBUG` | `false` | `1`、`true` 或 `yes` 启用；可能在用量记录中捕获调试上下文。 |
| `GROK_PROXY_URL` | 空 | 显式上游 HTTP(S) 代理。 |
| `GROK_PROXY_ENABLED` | 自动判断 | 显式代理开关；未设置时，非空 `GROK_PROXY_URL` 会启用代理。 |
| `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` | Go 默认行为 | 未启用显式代理时由标准 HTTP Transport 使用。 |

任一搜索并发容量耗尽时，服务会立即返回 HTTP `503` 和 `Retry-After: 1`，不会继续排队并占用长连接 goroutine/socket。搜索响应通过 `X-Grok-Search-Queue-Time-Ms` 暴露 semaphore 获取耗时。

### 转发客户端 IP 防护

应用层 IP 防护仅在能够从 `X-Real-IP` 或 `X-Forwarded-For` 解析出有效 IP 时启用。以下入口统一使用该规则：

- `/mcp` API Key 鉴权前的 IP 令牌桶；
- 面板登录和注册接口的 IP 令牌桶；
- 面板“用户名 + IP”维度的登录失败锁定。

具体行为如下：

| 请求状态 | IP 防护行为 |
|---|---|
| 两个 Header 都缺失、为空或不包含有效 IP | 跳过应用层 IP 限流和登录 IP 锁定。MCP 鉴权成功后，用户 tier RPM 与配额检查仍然生效。 |
| 存在有效的转发客户端 IP Header | 启用 IP 防护；有效的 `X-Real-IP` 优先，否则使用 `X-Forwarded-For` 中第一个有效 IP，不再需要可信代理白名单。 |

> [!IMPORTANT]
> 应用会直接信任 `X-Real-IP` 和 `X-Forwarded-For`。必须保证应用只能由可信反向代理访问，并由代理为每个请求注入有效客户端 IP，同时先清理客户端提供的同名 Header。如果客户端能够直连 `grok-mcp`，就可以省略或破坏 Header 跳过 IP 防护，或伪造 Header 任意选择限流桶。请继续在代理层对 `/mcp`、`/panel/v1/auth/login` 和 `/panel/v1/auth/register` 配置限流。

反向代理必须覆盖 `X-Real-IP`，并根据连接来源重新生成 `X-Forwarded-For`。由于应用会选择 `X-Forwarded-For` 中第一个有效 IP，不应保留不可信客户端提供的原始转发链。

Nginx 转发示例：

```nginx
location / {
    proxy_pass http://grok-mcp:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

### 持久化与热更新

服务启动时先加载环境变量；如果 SQLite 已保存服务设置，持久化的上游设置优先。管理员可以在 **Server Settings** 中热更新：

- CPA 地址和 API Key
- 上游搜索协议
- 默认模型和超时
- 显式代理地址及开关
- 注册模式
- Debug 模式
- 进程级和单用户流式搜索并发上限

监听地址、数据库路径、JWT 密钥和来源 IP RPM 仍然是仅启动时生效的配置。

> [!WARNING]
> CPA API Key 会持久化到 SQLite。请将数据库视为敏感数据进行权限控制和备份；面板响应只返回掩码预览。

### 上游协议映射

| 配置值 | 端点 | 搜索映射 |
|---|---|---|
| `responses` | `POST /v1/responses` | CPA Responses 内置工具（`web_search` / `x_search`）；这是向后兼容的默认值，并可提供搜索轮次进度。 |
| `chat_completions` | `POST /v1/chat/completions` | xAI 兼容的 `search_parameters`，使用 `web` 或 `x` 来源并解析 Chat Completions 流。对于“正在搜索”等仅表示状态的短回复，会在有限次数内自动请求继续回答，确保 MCP 收到最终答案或明确错误。 |
| `anthropic_messages` | `POST /v1/messages` | Anthropic 服务端 `web_search_20250305` 工具与 Messages SSE。网页搜索保留配置的域名筛选；X 搜索使用同一服务端工具并限制在 `x.com`，同时要求返回直接的 X 帖子链接。 |

实际能力取决于所使用的 CPA 版本、提供方和模型。对于现有 Grok/CPA 部署，Responses 仍是兼容性最稳妥的选项。图片搜索选项仅在 Responses 协议存在对应字段时生效，其他协议没有等价字段时会忽略。

即使答案正文相同，不同协议暴露的元数据也可能不同：

- Responses 通常提供最完整的搜索轮次进度和结构化引用数据。
- Chat Completions 只有在 CPA 返回兼容的非标准搜索事件时才会提供进度；标准 Chat 数据块可能只有最终文本和用量。
- Anthropic Messages 可能在答案正文中包含来源 URL，但是否返回结构化 citation 数据块取决于 CPA 的提供方转换实现。
- 只要上游提供 token 统计，服务会在不同协议之间统一规范化 `usage` 字段。

## 用户、注册、Tier 与配额

注册模式可以运行时切换：

| 模式 | 行为 |
|---|---|
| `free` | 允许公开自助注册。 |
| `invite` | 必须使用有效、已启用且未耗尽的邀请码。 |
| `disabled` | 禁止公开注册。 |

管理员可以创建、禁用和删除邀请码，并设置每个邀请码的注册次数上限。

每个用户属于一个 tier。该用户的所有 API Key 共享 tier 的 RPM 和月成功调用额度。只有实际 `tools/call` 会计量，初始化、ping、工具列表等请求不计入。

新数据库的默认 tier：

| Tier | RPM | 每月成功调用数 |
|---|---:|---:|
| `tier0` | 10 | 800 |
| `tier1` | 20 | 4,000 |
| `tier2` | 40 | 16,000 |
| `tier3` | 60 | 40,000 |
| `tier4` | 120 | 160,000 |
| `tier5` | 300 | 800,000 |
| `tier6` | 不限 | 不限 |

月度周期按 UTC 自然月计算。工具执行前先预留成功调用额度；调用失败时回滚。管理员可以在面板修改 tier 参数。

`/mcp` 中间件顺序如下；当两个转发 Header 都无法解析出有效 IP 时，`IP RPM` 步骤会直接放行：

```text
MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM -> Search Concurrency -> Quota -> Usage -> MCP handler
```

## 管理面板 API 概览

内嵌面板位于 `/panel/`，API 位于 `/panel/v1`。

公开认证路由：

```text
GET  /panel/v1/auth/registration-settings
POST /panel/v1/auth/register
POST /panel/v1/auth/login
```

登录用户路由涵盖用户信息、API Key 和用量：

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

`/panel/v1/admin/` 下的管理员路由用于管理用户、tier、服务设置、邀请码、模型和用量。除公开路由外，面板请求需要：

```text
Authorization: Bearer <面板 JWT>
```

## Docker Compose

```bash
cp .env.example .env
${EDITOR:-vi} .env
docker compose up -d --build
```

如果 CPA 直接运行在 Docker 宿主机上，请设置：

```dotenv
CPA_BASE_URL=http://host.docker.internal:8317
```

项目提供的容器：

- 使用 `CGO_ENABLED=0` 构建
- 以非 root `app` 用户运行
- 监听 8080 端口
- 将 SQLite 数据存放在 `/app/data`
- Compose 使用 `grok-mcp-data` 命名卷
- 通过 `/panel/` 执行健康检查

Compose 默认不会转发所有可选的上游代理变量；容器需要 `GROK_PROXY_URL`、`GROK_PROXY_ENABLED` 或标准代理变量时，请扩展 `environment` 配置。

## 生产部署与安全

- 公网暴露前必须放在 HTTPS 反向代理之后，服务本身不提供 TLS。
- 不要泄露面板 JWT、MCP 客户端 API Key、CPA Key、邀请码或真实 `.env`。
- 初始化管理员登录后应立即轮换凭据。
- 限制 SQLite 文件访问权限，并对其进行安全备份。
- 确保反向代理覆盖 `X-Real-IP`、重新生成 `X-Forwarded-For`，并始终添加其中至少一个，否则应用层 IP 防护可能被绕过。
- 禁止所有客户端直连应用端口，因为应用会直接信任转发客户端 IP Header，不再使用代理白名单二次校验。
- 在代理层对 `/mcp`、面板登录和注册接口增加限流。
- 除排障外保持 debug 关闭。即使认证 Header 会脱敏，debug 上下文仍可能保留请求或响应正文。
- MCP 客户端 API Key 的鉴权使用不可逆哈希；可复制内容以 AES-256-GCM 密文保存，并绑定密钥记录和所属用户。
- 更换 `GROK_JWT_SECRET` 或升级旧版 hash-only 数据库时，无法解密的 API Key 会自动轮换；客户端需要从面板复制替代密钥并更新配置。

## 开发与测试

运行默认测试：

```bash
go test ./...
```

验证构建：

```bash
go build ./cmd/grok-mcp
```

真实 CPA/xAI 集成测试需要显式启用：

```bash
export GROK_INTEGRATION_TEST="1"
export CPA_API_KEY="replace-with-your-cpa-api-key"
export CPA_BASE_URL="http://127.0.0.1:8317"

go test ./test/grok -run TestIntegrationSearchLiveCPA -v
```

面板前端是内嵌的原生 HTML、CSS 和 JavaScript，不需要 Node.js 构建。仓库目前没有 Makefile、任务运行器或公开的 CI/发布流水线。贡献代码时可使用 `gofmt`、`go vet` 等标准 Go 工具。

### 代码结构

```text
cmd/grok-mcp/       进程入口和版本参数
internal/app/       应用组合、初始化、HTTP 服务与优雅退出
internal/auth/      MCP API Key 鉴权和面板 JWT
internal/config/    环境变量与持久化设置映射
internal/grok/      CPA 请求、SSE 解析、模型列表
internal/mcp/       MCP Server Instructions 和工具注册
internal/panel/     面板 REST API
internal/panelui/   内嵌管理前端
internal/quota/     月成功调用额度预留
internal/ratelimit/ 来源 IP 与用户级限流
internal/store/     SQLite schema 和持久化
internal/usage/     工具调用统计及可选 debug 捕获
test/http/          HTTP 集成与防护测试
test/grok/          可选真实上游集成测试
```

## 故障排查

| 现象 | 检查项 |
|---|---|
| `GROK_JWT_SECRET is required` | 在服务环境中设置至少 32 字节的密钥。 |
| 新数据库启动失败 | 设置有效的 `CPA_API_KEY`，并确认数据库目录可写。 |
| MCP 返回 `401` 或 `403` | 使用 MCP 客户端 API Key 而不是面板 JWT，并检查 Key 和用户是否启用。 |
| MCP 返回 `429` | 检查来源 IP RPM、用户 tier RPM 和月成功调用额度。 |
| 上游超时或 HTTP 错误 | 检查 CPA 地址、CPA Key、代理设置和 CPA 健康状态。 |
| Docker 无法访问宿主机 CPA | 使用 Compose 提供的 `http://host.docker.internal:<port>`。 |
| 模型列表为空 | 确认 CPA 返回 Grok 模型 ID；`imagine` 和 `video` 会被主动过滤。 |
| 客户端无法连接 | 确认客户端支持 Streamable HTTP，并向准确的 `/mcp` 地址发送 Bearer Header。 |

## 许可证

本项目采用 [CC BY-NC 4.0](./LICENSE)。在署名并遵守许可证条款的前提下，可用于非商业复制、分发和修改；商业用途需要获得版权持有人的事先书面许可。
