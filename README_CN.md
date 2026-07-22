# grok-search-mcp

[English](./README.md)

`grok-search-mcp` 是一个仅提供 HTTP 传输的 [Model Context Protocol（MCP）](https://modelcontextprotocol.io/)服务端，将 Grok 的实时网页搜索、X/Twitter 搜索和模型发现能力暴露给 MCP 客户端。

本项目**不直接调用 xAI 官方 API**，而是连接已经部署的 [CLIProxyAPI（CPA）](https://github.com/router-for-me/CLIProxyAPI)。CPA 负责上游 xAI 认证，`grok-search-mcp` 负责 MCP 传输、客户端 API Key、限流配额、用量统计和管理面板。

> [!IMPORTANT]
> 本项目仅支持 **Streamable HTTP**，不提供 stdio 传输，也不内置 TLS 终止。

- `grok-search-mcp` 必须作为独立 HTTP 服务启动，MCP 客户端通过 `http://<host>:<port>/mcp` 连接。
- 不能将本项目配置为由 MCP 客户端通过命令启动、再使用标准输入和标准输出通信的 stdio 服务。
- 服务自身只监听普通 HTTP，不读取 HTTPS 证书或私钥，也不负责 TLS 握手。
- 公网部署时，应在 `grok-search-mcp` 前放置 Nginx、Caddy、Traefik、Kubernetes Ingress 或云负载均衡器等可信反向代理，由代理对外提供 HTTPS，再通过内部 HTTP 转发到 `grok-search-mcp`。

典型的生产请求链路如下：

```text
MCP 客户端 -- HTTPS --> 反向代理 / 负载均衡器 -- HTTP --> grok-search-mcp /mcp
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
- 面向直连或可信代理的对端感知 `/mcp` 与面板认证 IP 防护
- 使用 SQLite 持久化用户、Key、tier、用量、邀请码和服务设置
- 内嵌管理面板，无独立前端构建步骤
- 上游、搜索并发、代理、注册模式、debug 和运行指标采集设置支持运行时热更新
- 使用非 root 运行镜像的 Docker Compose 部署

## 架构

```text
支持 Streamable HTTP 的 MCP 客户端
        |
        |  POST /mcp
        |  Authorization: Bearer <MCP 客户端 API Key>
        v
grok-search-mcp
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
| CPA API Key | `grok-search-mcp` -> CPA | 认证所选上游搜索端点和 `/v1/models` 请求。 |
| MCP 客户端 API Key | MCP 客户端 -> `/mcp` | 在面板创建并可按需复制；数据库保存鉴权哈希和由 `GROK_JWT_SECRET` 派生密钥加密的可恢复密文。 |
| 面板 JWT | 浏览器/API 客户端 -> `/panel/v1` | 登录面板后返回，不能用于认证 `/mcp`。 |

## 环境要求

- 当前文档化的本地运行目标为 Linux
- 本地构建需要 Go 1.25.12 或更高版本
- 可访问 `/v1/models`，并至少兼容 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 之一的 CPA 服务
- 容器部署可选用 Docker 和 Docker Compose
- MCP 客户端需要支持 Streamable HTTP 和自定义 Bearer Header

项目使用纯 Go SQLite 驱动 `modernc.org/sqlite`，不依赖 CGO。

## 快速开始

### 1. 构建

```bash
go build -o grok-search-mcp ./cmd/grok-search-mcp
```

可以在构建时注入版本号：

```bash
go build \
  -ldflags "-X github.com/MapleMapleCat/Grok_Search_Mcp/internal/version.Version=1.2.3" \
  -o grok-search-mcp ./cmd/grok-search-mcp

./grok-search-mcp -version
```

### 2. 配置并启动

以下命令默认在源码仓库中执行。当前 Linux 发布压缩包只包含二进制文件和 README，不包含 `.env.example` 或 Compose 文件；压缩包用户需要根据下方配置表手动创建 `.env`，或从仓库获取这些配套文件。

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

./grok-search-mcp
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

SQLite 主库有意保持单写连接，避免通过增加连接数制造更多写锁竞争。连接设置
包含 5 秒 `busy_timeout`，usage 后台写入器会将最多 32 条记录或 10ms 内到达的
记录合并为一个事务；定时维护使用非阻塞读者的 `PASSIVE` checkpoint。生产环境
必须把数据库放在本地 SSD 上，不应放在 NFS、SMB 或高延迟网络块存储上。

运行指标默认关闭。管理员需要先在 **服务设置** 中启用
**数据库运行指标**；关闭时，以下接口返回 HTTP `404`。

管理员可以查询实时运行指标：

```bash
curl -sS "http://127.0.0.1:8080/panel/v1/admin/operations/metrics" \
  -H "Authorization: Bearer ${login_token}" | jq
```

该接口仅允许管理员访问，包含：主写库、读库和 debug 库的连接池状态与等待时间；
quota reserve/release 延迟和错误；SQLite busy/locked 次数；usage 批次、队列深度、
最老排队记录年龄、写入/排队延迟和丢弃量；维护及主库/debug 库 WAL checkpoint
延迟与 frame 计数；以及有界来源 IP 注册表的当前/最大条目数、独立条目接纳、
过期清理、降级请求和降级拒绝计数。同一接口还会按公开认证 endpoint 汇总面板
认证保护器的容量、接纳、过期清理、降级请求/拒绝和登录失败容量拒绝计数，
不会暴露 IP 地址或用户名。建议至少为以下情况配置告警：

- `primary_write_pool.wait_count` 或 `wait_duration_ms` 持续快速增长；
- `busy_or_locked_errors` 非零并持续增长；
- usage 队列长期接近容量、`oldest_queued_age_ms` 增长或出现丢弃；
- quota reserve/release 最大或平均延迟持续升高；
- checkpoint 持续出现 busy frame 或耗时升高；
- 来源 IP 注册表持续饱和或降级拒绝数持续增长；
- 面板认证 endpoint 持续出现降级流量、降级拒绝或登录失败容量拒绝。

如果在本地 SSD 和批量写入下仍长期出现上述压力，说明工作负载已超过内嵌
SQLite 单写者模型的目标范围。高写入 QPS 部署应迁移到 PostgreSQL/MySQL，或将
quota 计数迁移到具备原子操作的外部计数器，而不是继续增加 SQLite 写连接数。

### 3. 登录并创建 MCP 客户端 Key

当数据库中没有已启用的管理员时，服务会初始化 `admin` 账号，并把凭据写入精确
权限为 `0600` 的有界 JSON 文件。默认路径是
`<GROK_DB_PATH>.bootstrap-admin`；启动日志只输出文件路径，绝不输出密码。请使用
运行服务的同一操作系统用户读取该文件，并尽快轮换密码：

```bash
bootstrap_password="$(jq -r '.password' ./data/grok-search-mcp.db.bootstrap-admin)"
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

响应中的 `api_key` 可立即使用；之后也可以在 **API 密钥** 页面按需复制。每位
用户默认最多拥有 20 个 Key；已禁用 Key 仍计数，删除 Key 会释放容量。

bootstrap 管理员成功修改密码并提交数据库后，服务会尽力删除凭据文件；删除失败
不会回滚已生效的密码，请手动清理残留文件。若启动在创建账号前失败，下次启动会
安全复用已有的合规凭据文件。不要编辑、放宽权限或从备份恢复过期副本。

### 4. 连接 Claude Code

Claude Code 是当前仓库内提供了明确配置示例的客户端：

```bash
export GROK_SEARCH_MCP_API_KEY="grok_xxx"

claude mcp add --transport http grok-search-mcp http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer ${GROK_SEARCH_MCP_API_KEY}"
```

项目级 `.mcp.json` 可以使用环境变量展开：

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

上游未提供时，`citations`、`sources` 和 `usage` 可能省略。服务会在配额预留和用量统计前主动拒绝 JSON-RPC batch 请求，以及重复或大小写冲突的 `method`、`params`、`params.name` 路由字段。

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
| `GROK_DB_PATH` | `./grok-search-mcp.db` | SQLite 路径，修改后需要重启。 |
| `GROK_BOOTSTRAP_CREDENTIALS_PATH` | `<GROK_DB_PATH>.bootstrap-admin` | bootstrap 管理员 JSON 凭据文件的仅启动时路径；已有路径必须是精确 `0600` 的普通非符号链接文件。 |
| `GROK_CLIENT_IP_MODE` | `direct` | 仅启动时生效的客户端身份模式：`direct` 使用 `RemoteAddr` 并忽略转发 Header；`trusted_proxy` 先认证直接对端再接受转发 Header。 |
| `GROK_TRUSTED_PROXY_CIDRS` | 空 | 可信直接代理对端的 IPv4/IPv6 CIDR，逗号分隔。`trusted_proxy` 模式必填且非空；两种模式都会在启动时解析校验。 |
| `GROK_INITIAL_REGISTRATION_MODE` | `disabled` | 初始注册策略：`disabled`、`invite` 或 `free`。仅在尚无持久化服务设置行时使用。 |
| `GROK_MAX_API_KEYS_PER_USER` | `20` | 仅启动时生效的单用户 API Key 行数上限，范围 1-1,000；禁用仍计数，删除释放容量。 |
| `GROK_AUTH_PASSWORD_MAX_CONCURRENT` | `4` | 登录、注册和密码修改共享的进程级 bcrypt 并发上限，范围 1-64。 |
| `GROK_AUTH_KEY_MISS_MAX_CONCURRENT` | `32` | 不同无效 API Key 并发解析的 SQLite 上限；相同 Key 的 miss 会合并，范围 1-1,024。 |
| `GROK_USAGE_RAW_RETENTION_DAYS` | `7` | 原始用量和 debug 明细保留期限，之后压缩为小时级数据。 |
| `GROK_USAGE_HOURLY_RETENTION_DAYS` | `90` | 小时级用量保留期限，之后压缩为日级数据。 |
| `GROK_USAGE_DAILY_RETENTION_DAYS` | `730` | 日级聚合超过此期限后删除。 |
| `GROK_USAGE_MAINTENANCE_INTERVAL` | `1h` | 聚合、清理和 WAL checkpoint 的执行间隔。 |
| `GROK_SEARCH_MCP_IP_RPM` | `300` | 在 MCP API Key 鉴权前，对每个请求按 `GROK_CLIENT_IP_MODE` 选出的来源 IP 应用 RPM。 |
| `GROK_SEARCH_MCP_IP_MAX_ENTRIES_PER_SHARD` | `2048` | 64 个注册表分片各自最多保留的独立来源 IP 令牌桶数；默认进程总上限为 131,072 个条目，可配置范围为 1-65,536，修改后需要重启。 |
| `GROK_SEARCH_MCP_IP_FALLBACK_BUCKETS_PER_SHARD` | `16` | 分片满载且清理过期条目后仍无容量时，新 IP 使用的固定共享降级桶数；可配置范围为 1-1,024，修改后需要重启。 |
| `GROK_SEARCH_MCP_GLOBAL_SEARCH_CONCURRENCY` | `16` | 进程级流式搜索同时在途上限的环境默认值；初始化后以面板持久化设置为准。 |
| `GROK_SEARCH_MCP_USER_SEARCH_CONCURRENCY` | `4` | 单用户上限的环境默认值，不得超过全局上限；初始化后以面板持久化设置为准。 |
| `GROK_AUTH_USER_RPM_MAX_ENTRIES` | `16,384` | 已鉴权用户 RPM 专用状态的启动时容量上限，范围 1-65,536；溢出身份使用固定共享降级桶。 |
| `GROK_AUTH_USER_RPM_FALLBACK_BUCKETS` | `64` | 已鉴权用户 RPM 共享降级桶数量，范围 1-1,024。 |
| `GROK_SEARCH_MCP_DEBUG` | `false` | `1`、`true` 或 `yes` 启用；可能在用量记录中捕获调试上下文。 |
| `GROK_PROXY_URL` | 空 | 显式上游 HTTP(S) 代理。 |
| `GROK_PROXY_ENABLED` | `false` | 显式代理开关；必须与 `GROK_PROXY_URL` 一起设置为 `true`，仅设置 URL 不会启用项目显式代理。 |
| `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` | Go 默认行为 | 未启用显式代理时由标准 HTTP Transport 使用。 |

旧的 `GROK_MCP_IP_RPM`、`GROK_MCP_GLOBAL_SEARCH_CONCURRENCY`、
`GROK_MCP_USER_SEARCH_CONCURRENCY` 和 `GROK_MCP_DEBUG` 仍作为兼容别名接受。
当新旧名称同时配置时，对应的 `GROK_SEARCH_MCP_*` 变量优先生效。

任一搜索并发容量耗尽时，服务会立即返回 HTTP `503` 和 `Retry-After: 1`，不会继续排队并占用长连接 goroutine/socket。搜索响应通过 `X-Grok-Search-Queue-Time-Ms` 暴露 semaphore 获取耗时。

### 客户端 IP 信任模式

应用层 IP 防护始终要求有效客户端身份。以下入口注入并共用同一个仅启动时配置的解析器：

- `/mcp` API Key 鉴权前的 IP 令牌桶；
- 面板登录和注册接口的 IP 令牌桶；
- 面板“用户名 + IP”维度的登录失败锁定。

两种模式的行为如下：

| 模式 / 请求状态 | IP 防护行为 |
|---|---|
| `direct`（默认） | 每个请求都使用连接 `RemoteAddr` 中的规范化 IP，并完全忽略 `X-Real-IP` 与 `X-Forwarded-For`，包括格式错误或伪造值。缺失、格式错误或带 zone 的 `RemoteAddr` 返回 HTTP `400`。 |
| `trusted_proxy`，直接对端不在 `GROK_TRUSTED_PROXY_CIDRS` | 不接受任何转发身份，直接返回 HTTP `403`。 |
| `trusted_proxy`，可信对端未提供转发 Header | 返回 HTTP `400`，不存在无 Header 绕过。 |
| `trusted_proxy`，可信对端提供了格式错误、重复、超长、跳数过多或冲突的转发 Header | 返回 HTTP `400`。 |
| `trusted_proxy`，可信对端提供有效转发 Header | 优先使用 `X-Real-IP`，否则使用 `X-Forwarded-For` 中第一个 IP；两者同时存在时，规范化客户端地址必须一致。 |

`GROK_TRUSTED_PROXY_CIDRS` 最多接受 256 个逗号分隔的规范 IPv4/IPv6
前缀。direct 模式会校验但忽略非空列表；trusted-proxy 模式必须提供列表。信任只
针对 TCP 直接对端；可信代理仍负责删除客户端提供的同名 Header，并根据自身连接
元数据重新生成转发 Header。

`/mcp` 来源 IP 注册表具有硬容量上限。现有 IP 会持续使用自己的独立令牌桶，
直到正常空闲 TTL 到期；系统不会为了接纳新身份而淘汰活跃条目。分片已满时，
限流器会先同步删除过期条目；如果仍然满载，则通过进程随机哈希把新 IP 映射到
固定的共享降级桶，不再创建对应的 map 条目。因此多个降级身份可能共享限流状态，
并在饱和期间共同收到 `429`。启用运维指标后，管理员指标接口会暴露注册表容量、
当前条目数、独立条目接纳/过期数和降级请求/拒绝数，但不会暴露 IP 地址。

公开面板认证保护器同样具有固定的进程内硬容量：登录 endpoint 最多保留 4,096
个独立 IP 桶，注册 endpoint 为 2,048 个，注册 challenge endpoint 为 2,048 个，
规范化“用户名 + IP”登录失败状态为 8,192 个。三个 endpoint 拥有相互独立的容量
域和各自 16 个固定共享降级桶。endpoint 满载时会先同步回收过期条目；若仍满载，
新 IP 共享降级限流状态且不会新增 map 条目。系统不会为了接纳新身份而淘汰仍然
有效的独立令牌桶。

登录失败状态不使用共享降级桶，避免哈希碰撞导致无关用户被共同锁定。清理过期
条目后仍满载时，新的“用户名 + IP”会在用户查询和 bcrypt 前收到通用 `429`；
已有失败计数、活跃锁定和在途尝试会继续保留。这些预算是固定安全上限，不属于
环境变量或面板设置。管理员运行指标只暴露聚合容量和饱和计数。所有面板认证
保护状态及累计计数均为进程内状态，服务重启后会重置。

> [!IMPORTANT]
> 只有确认 `grok-search-mcp` 实际看到的代理 CIDR 后才启用 `trusted_proxy`；它可能是容器网桥或负载均衡子网，而不是代理公网地址。CIDR 配错会安全失败并返回 `403`。代理层仍应对 `/mcp`、`/panel/v1/auth/login` 和 `/panel/v1/auth/register` 配置限流。

反向代理必须覆盖 `X-Real-IP`，并根据连接来源重新生成 `X-Forwarded-For`。由于应用会选择 `X-Forwarded-For` 中第一个有效 IP，不应保留不可信客户端提供的原始转发链。

Nginx 转发示例：

```nginx
location / {
    proxy_pass http://grok-search-mcp:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

同时配置类似以下启动变量：

```dotenv
GROK_CLIENT_IP_MODE=trusted_proxy
GROK_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
```

Compose 默认发布为 `127.0.0.1:8080:8080`，不会在宿主机所有接口暴露明文
后端。只有在明确设计好代理和网络边界后才应修改该宿主机绑定。

### 持久化与热更新

服务启动时，环境变量提供初始运行时默认值。`GROK_INITIAL_REGISTRATION_MODE` 只在 SQLite 尚无服务设置行时提供注册策略，安全默认值为 `disabled`。如果 SQLite 已保存服务设置，则完整持久化对象优先，包括注册模式；重启时修改初始值不会覆盖管理员选择。监听地址、数据库路径、JWT 密钥、客户端 IP 信任模式/CIDR、IP RPM/注册表容量、保留期限和维护周期仍只由环境变量控制。管理员可以在 **Server Settings** 中热更新：

- CPA 地址和 API Key
- 上游搜索协议
- 默认模型和超时
- 显式代理地址及开关
- 注册模式
- Debug 模式
- 进程级和单用户流式搜索并发上限
- 运行指标采集开关

设置更新会先写入持久化存储，再应用到当前运行进程。面板分别显示已保存设置版本和已确认运行版本。如果持久化成功但运行时应用失败，保存值仍然有效，面板会明确提示“已保存，尚未应用”而不是笼统的保存失败，并重新加载持久化值。版本不一致期间，上游健康状态返回未知，避免使用混合配置进行探测。服务重启成功后会加载持久化版本，并恢复两个版本一致。

监听地址、数据库路径、JWT 密钥、客户端 IP 模式/可信 CIDR、来源 IP RPM、
注册表容量和降级桶数量仍然是仅启动时生效的配置。

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

新数据库默认禁用注册；只有显式将 `GROK_INITIAL_REGISTRATION_MODE` 设为
`invite` 或 `free` 才会在首次持久化时选择其他模式。初始设置行创建后，注册模式
可以运行时切换，且持久化值始终优先：

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

`/mcp` 中间件顺序保持不变；`IP RPM` 会在 API Key 鉴权前始终解析并校验客户端身份：

```text
MaxBody -> IP RPM -> API Key -> ExtractToolName -> User RPM -> Search Concurrency -> Quota -> Usage -> MCP handler
```

## 管理面板 API 概览

内嵌面板位于 `/panel/`，API 位于 `/panel/v1`。

公开认证路由：

```text
GET  /panel/v1/auth/registration-settings
POST /panel/v1/auth/registration-challenge
POST /panel/v1/auth/register
POST /panel/v1/auth/login
```

注册采用一次性工作量证明：客户端先请求有效期为 5 分钟的签名挑战，在本地计算满足难度要求的 SHA-256 nonce，再将 `proof.challenge` 和 `proof.nonce` 随注册请求提交。默认难度为 20 个前导零位；验证成功后挑战立即失效，不能复用。内嵌面板会在 Web Worker 中完成计算，避免阻塞页面交互。

登录用户路由涵盖用户信息、密码/会话生命周期、API Key 和用量：

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

修改密码要求 `current_password` 和 `new_password` 均为 8-72 字节。两个生命周期
接口都会递增当前用户的 `token_version`，立即使此前签发的所有面板 JWT 失效，并
返回新的 `token`、`expires_at` 和当前 `user`。账户页面会把替换令牌及过期时间
作为一个值原子写入 `sessionStorage`。吊销全部会话只影响面板 JWT，不影响 MCP
API Key。

`GET /panel/v1/overview/health` 用于向已登录的面板展示上游和模型可用性；它不同于容器对 `/panel/` 执行的未鉴权存活检查。

`/panel/v1/admin/` 下的管理员路由用于管理用户、tier、服务设置、邀请码、模型和用量。除公开路由外，面板请求需要：

```text
Authorization: Bearer <面板 JWT>
```

## Docker 部署

使用项目提供的 Compose 文件构建并运行当前源码：

```bash
cp .env.example .env
${EDITOR:-vi} .env
docker compose up -d --build
```

直接运行已发布镜像、避免重新构建本地源码：

```bash
docker pull maplemaplecat/grok-search-mcp:v0.2.2
docker run -d \
  --name grok-search-mcp \
  --restart unless-stopped \
  --env-file .env \
  -p 127.0.0.1:8080:8080 \
  -v grok-search-mcp-data:/app/data \
  maplemaplecat/grok-search-mcp:v0.2.2
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
- Compose 使用 `grok-search-mcp-data` 命名卷
- 通过 `/panel/` 执行健康检查

Compose 默认不会转发所有可选的上游代理变量；容器需要 `GROK_PROXY_URL`、`GROK_PROXY_ENABLED` 或标准代理变量时，请扩展 `environment` 配置。

## 生产部署与安全

- 公网暴露前必须放在 HTTPS 反向代理之后，服务本身不提供 TLS。
- 不要泄露面板 JWT、MCP 客户端 API Key、CPA Key、邀请码或真实 `.env`。
- 初始化管理员登录后应立即轮换凭据。
- 限制 SQLite 文件访问权限，并对其进行安全备份。
- 客户端直连时保持 `GROK_CLIENT_IP_MODE=direct`；转发 Header 会被忽略，无法选择限流身份。
- 反向代理部署应设置 `GROK_CLIENT_IP_MODE=trusted_proxy`，只允许代理的直接对端 CIDR，并由代理覆盖 `X-Real-IP`、重建 `X-Forwarded-For`。缺失 Header 返回 `400`，不可信对端返回 `403`。
- 明文应用端口应只绑定回环或内部网络；项目提供的 Compose 和 `docker run` 示例默认只发布到宿主机回环地址。
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
go build ./cmd/grok-search-mcp
```

真实 CPA/xAI 集成测试需要显式启用：

```bash
export GROK_INTEGRATION_TEST="1"
export CPA_API_KEY="replace-with-your-cpa-api-key"
export CPA_BASE_URL="http://127.0.0.1:8317"

go test ./test/grok -run TestIntegrationSearchLiveCPA -v
```

面板前端是内嵌的原生 HTML、CSS 和 JavaScript，不需要 Node.js 构建。仓库目前没有 Makefile 或任务运行器。GitHub Actions 的发布/手动工作流会运行 `go test ./...`、构建 Linux 压缩包并发布 Docker 镜像；目前尚无 push 或 pull request 校验工作流。贡献代码时可使用 `gofmt`、`go vet` 等标准 Go 工具。

### 代码结构

```text
cmd/grok-search-mcp/ 进程入口和版本参数
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
