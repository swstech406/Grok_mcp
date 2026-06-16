# grok-mcp

> 用 Go 实现的 **MCP（Model Context Protocol）服务端**，把 Grok 的实时联网搜索能力封装成两个 MCP 工具（`grok_web_search` / `grok_x_search`），供 Claude Desktop、Claude Code、Cursor 等客户端通过 stdio 调用。

本工具**不直接对接 xAI 官方 API**，而是作为你已部署的 [**CLIProxyAPI (CPA)**](https://github.com/router-for-me/CLIProxyAPI) 的客户端：所有请求经 CPA 的 `POST /v1/responses` 转发，CPA 内部完成对 xAI 的认证。你只需持有一个 CPA 的 `api-keys`。

```
MCP 客户端 (Claude / Cursor / ...)
      │  stdio（JSON-RPC over stdin/stdout）
      ▼
grok-mcp（本项目）
      │  POST /v1/responses  +  tools:[web_search | x_search]
      ▼
CLIProxyAPI（你已部署）   ←  对 xAI 的 OAuth / api-key 由它处理
      │
      ▼
xAI Responses API（grok-4.3）
```

## 特性

- 🌐 **两个搜索工具**：`grok_web_search`（全网实时搜索）、`grok_x_search`（X / Twitter 实时搜索）
- 📎 **自动带引用**：返回 Grok 的答案文本 + 来源 URL（`citations`）与标题（`sources`）
- 📡 **流式搜索 + 进度通知**：上游多轮搜索/读取时通过 MCP progress 推送进度
- 🔌 **标准 stdio 传输**：即插即用接入任意 MCP 客户端
- 🔑 **零认证负担**：复用 CPA 的鉴权，只需一个 CPA key
- 🧪 **用量友好**：单元测试零配额，真实调用测试由环境变量门控

---

## 前置条件

| 项 | 要求 |
|---|---|
| Go | 1.24+ |
| CPA | 一个已运行、可访问的 CLIProxyAPI 实例（默认 `http://127.0.0.1:8317`） |
| API Key | 在 CPA 配置 `api-keys` 中生成的一个 key |
| 模型 | CPA 已配置好 `grok-4.3`（或你指定的模型） |

---

## 快速开始

### 1. 构建

```powershell
git clone <your-repo> grok-mcp
cd grok-mcp
go mod tidy
go build -o grok-mcp.exe ./cmd/grok-mcp

# 可选：构建时注入版本号
go build -ldflags "-X github.com/grok-mcp/internal/version.Version=1.2.3" -o grok-mcp.exe ./cmd/grok-mcp
```

> Linux/macOS 用 `go build -o grok-mcp ./cmd/grok-mcp`。

### 2. 验证可独立运行

```powershell
$env:CPA_API_KEY = "你的CPA-key"
./grok-mcp.exe
```

进程会阻塞等待 stdin 输入（这是正常的，MCP 协议走 stdio）。**不报错**即说明配置加载与 server 启动正常；按 `Ctrl+C` 可优雅退出（`SIGINT`/`SIGTERM`）。日志输出在 stderr，不会污染 stdout 协议流。

查看版本（仅打印版本后退出，不写 MCP 协议流）：

```powershell
./grok-mcp.exe -version
```

### 3. 接入 MCP 客户端

任选一种 ↓ 见 [客户端接入](#接入-mcp-客户端)。

---

## 配置（环境变量）

所有配置通过环境变量传入（由 MCP 客户端在启动子进程时注入，不落盘）：

| 变量 | 必填 | 默认值 | 说明 |
|---|:---:|---|---|
| `CPA_API_KEY` | ✅ | — | CPA 对外 API Key，作为 `Authorization: Bearer` 发送 |
| `CPA_BASE_URL` | ❌ | `http://127.0.0.1:8317` | CPA 基础地址（**不含**尾部 `/`） |
| `GROK_MODEL` | ❌ | `grok-4.3` | 默认模型，可被工具参数 `model` 单次覆盖 |
| `GROK_HTTP_TIMEOUT` | ❌ | `120` | HTTP 超时（秒）。搜索类请求较慢，建议保持较大值 |
| `GROK_MCP_DEBUG` | ❌ | — | 设为 `1`/`true`/`yes` 启用调试日志（输出到 stderr） |
| `GROK_INTEGRATION_TEST` | ❌ | — | 设为 `1` 才运行真实调用集成测试（见 [测试](#测试)） |

> 缺失 `CPA_API_KEY` 会在启动时直接报错退出（fail-fast）。

---

## 接入 MCP 客户端

### Claude Desktop

编辑配置文件（Windows：`%APPDATA%\Claude\claude_desktop_config.json`；macOS：`~/Library/Application Support/Claude/claude_desktop_config.json`）：

```json
{
  "mcpServers": {
    "grok-mcp": {
      "command": "D:/workspace/Grok_mcp/grok-mcp.exe",
      "args": [],
      "env": {
        "CPA_BASE_URL": "http://127.0.0.1:8317",
        "CPA_API_KEY": "replace-with-your-cpa-api-key",
        "GROK_MODEL": "grok-4.3",
        "GROK_HTTP_TIMEOUT": "120"
      }
    }
  }
}
```

保存后重启 Claude Desktop，在对话中即可让 Claude 调用 `grok_web_search` / `grok_x_search`。

### Claude Code

命令行注册（用户级）：

```powershell
claude mcp add grok-mcp `
  --env CPA_API_KEY=你的CPA-key `
  --env CPA_BASE_URL=http://127.0.0.1:8317 `
  --env GROK_MODEL=grok-4.3 `
  -- D:/workspace/Grok_mcp/grok-mcp.exe
```

或项目级 `.mcp.json`（与团队共享）：

```json
{
  "mcpServers": {
    "grok-mcp": {
      "command": "D:/workspace/Grok_mcp/grok-mcp.exe",
      "env": {
        "CPA_API_KEY": "replace-with-your-cpa-api-key",
        "CPA_BASE_URL": "http://127.0.0.1:8317",
        "GROK_MODEL": "grok-4.3"
      }
    }
  }
}
```

> ⚠️ 真实 key 不要提交到版本库；团队共享版用占位符，各人本地覆盖。

### Cursor

`Settings → MCP → Add new MCP Server`：
- Type: `stdio`
- Command: `D:/workspace/Grok_mcp/grok-mcp.exe`
- Env: 同上的 `CPA_API_KEY` / `CPA_BASE_URL` / `GROK_MODEL`

### MCP Inspector（调试用）

```powershell
npx @modelcontextprotocol/inspector D:/workspace/Grok_mcp/grok-mcp.exe
```

在 Inspector 的 Environment 里填入上述环境变量，即可手动调用两个工具、查看返回。

---

## 工具详解

### `grok_web_search` — 实时网页搜索

让 Grok 联网检索网页并综合作答。

**参数：**

| 参数 | 类型 | 必填 | 说明 |
|---|---|:---:|---|
| `query` | string | ✅ | 搜索问题 |
| `model` | string | ❌ | 覆盖默认模型 |
| `allowed_domains` | string[] | ❌ | 仅在指定域名内搜索（最多 5 个） |
| `excluded_domains` | string[] | ❌ | 排除指定域名（最多 5 个） |
| `enable_image_understanding` | bool | ❌ | 启用网页图片理解 |
| `enable_image_search` | bool | ❌ | 在答案中嵌入图片搜索结果 |

> `allowed_domains` 与 `excluded_domains` 不可同时使用。

### `grok_x_search` — 实时 X / Twitter 搜索

让 Grok 检索 X 平台帖子并综合作答。

**参数：**

| 参数 | 类型 | 必填 | 说明 |
|---|---|:---:|---|
| `query` | string | ✅ | 搜索问题 |
| `model` | string | ❌ | 覆盖默认模型 |

> 注：当前 `grok_x_search` 仅 `query` / `model` 生效（X 搜索使用与网页搜索不同的参数体系）。工具为兼容统一结构暂保留其他字段，但不会透传给 X 搜索。

## 流式与进度通知

本服务**仅保留流式搜索路径**（`SearchStream`），非流式 `Search()` 已移除。调用工具时：

1. 上游每完成一轮 `web_search_call`（搜索 query 或读取 URL），服务端会通过 MCP `notifications/progress` 推送一条进度消息（需客户端在 `tools/call` 时传入 `progressToken`）。
2. 进度文案示例：`🔍 第1轮：搜索 "capital of France"`、`📄 第2轮：读取 https://example.com/...`
3. 全部轮次结束后，工具返回最终 `answer` + `citations` + `sources` + `usage`。

> 调试：设置 `GROK_MCP_DEBUG=1` 可在 stderr 看到请求摘要、每轮 action、HTTP 错误与 token 用量；stdout 仍保持 MCP 协议纯净。

### 返回结构（两个工具一致）

```json
{
  "answer": "Grok 综合检索后给出的答案文本……",
  "citations": [
    "https://example.com/source-1",
    "https://example.com/source-2"
  ],
  "sources": [
    {"url": "https://example.com/source-1", "title": "Source One"},
    {"url": "https://example.com/source-2", "title": "Source Two"}
  ],
  "usage": {
    "input_tokens": 120,
    "output_tokens": 340,
    "total_tokens": 460,
    "reasoning_tokens": 0
  }
}
```

- `answer`：Grok 的最终答案
- `citations`：去重后的来源 URL 列表（兼容顶层字符串数组与对象数组、`annotations.url_citation` 多种返回形态）
- `sources`：带来源标题的结构化引用列表（`url` + 可选 `title`）
- `usage`：上游 token 用量（若上游返回）

---

## ⚠️ 用量与费用提醒

> 本工具每次调用都会触发上游真实联网搜索，**请控制调用频率**。

- 默认模型 `grok-4.3` 属于 reasoning 系列，单次成本通常高于 fast 系列（含 token + 工具调用费用）。
- 可通过 `GROK_MODEL` 切换到更便宜的模型（前提是 CPA 已配置该模型）。
- 工具刻意**不暴露** `max_search_results` 等放大用量的旋钮，采用上游合理默认值。
- 工具返回 `usage`，便于你观测单次消耗。

---

## 测试

### 单元测试（默认，零配额）

用纯函数测试 + SSE mock 验证请求拼装、响应解析、流式轮次与错误处理，不触发任何真实调用：

```powershell
go test ./...
```

### 门控集成测试（真实调用 CPA / xAI，消耗配额）

默认 `t.Skip`，仅在显式开启时运行，只发 2 条简单 query（web / x 各一）：

```powershell
$env:GROK_INTEGRATION_TEST = "1"
$env:CPA_API_KEY = "你的CPA-key"
$env:CPA_BASE_URL = "http://127.0.0.1:8317"
go test ./internal/grok -run TestIntegrationSearchLiveCPA -v
```

首次运行会用 `-v` 打印完整上游 JSON，便于确认 `citations` 的真实层级与字段格式。

---

## 故障排查

| 现象 | 排查方向 |
|---|---|
| 启动即报 `CPA_API_KEY is required` | 未注入环境变量，检查客户端 `env` 配置 |
| 调用返回 `upstream returned HTTP 401/403` | CPA key 无效或未在 CPA `api-keys` 配置 |
| 调用返回 `HTTP 404` | `CPA_BASE_URL` 拼写错误，或 CPA 未启用 `/v1/responses` |
| 调用返回 `HTTP 410` / `Live search deprecated` | CPA 路由到了旧版 `/v1/chat/completions`；确认走的是 `/v1/responses`，并升级 CPA |
| 始终超时 | 上游搜索较慢，调大 `GROK_HTTP_TIMEOUT`；或确认 CPA → xAI 链路通畅 |
| `answer` 为空 | 上游未返回文本，看 `citations` 是否也为空；用集成测试打印 raw JSON 定位 |
| Claude 里看不到工具 | MCP 配置未生效，重启客户端；用 MCP Inspector 单独验证进程能启动 |

---

## 项目结构

```
grok-mcp/
├── cmd/grok-mcp/main.go               # 入口：-version / signal 优雅关闭 / stdio 启动
├── internal/
│   ├── config/                        # 环境变量加载与校验
│   ├── version/version.go             # 版本号（支持 -ldflags 覆盖）
│   ├── grok/
│   │   ├── types.go                  # 上游请求/响应结构体
│   │   ├── client.go                  # 请求校验、响应解析 helper
│   │   ├── client_stream.go           # 唯一搜索入口 SearchStream
│   │   ├── result_test.go             # 纯函数测试（解析/校验/请求体）
│   │   ├── client_stream_test.go      # SSE mock 测试
│   │   └── client_integration_test.go # 门控集成测试
│   └── mcp/tools.go                  # 注册 grok_web_search / grok_x_search
├── examples/claude_desktop_config.json
└── go.mod
```

---

## 许可证

MIT
