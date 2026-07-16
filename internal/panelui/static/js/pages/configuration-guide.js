import { renderIcon } from "../components/icons.js";
import { escapeHTML } from "../utils.js";

export function renderConfigurationGuidePage() {
  const mcpEndpoint = buildMCPEndpoint();
  const environmentCommand = 'export GROK_SEARCH_MCP_API_KEY="grok_xxx"';
  const claudeCodeCommand = `claude mcp add --transport http grok-search-mcp \\
  ${mcpEndpoint} \\
  --header "Authorization: Bearer \${GROK_SEARCH_MCP_API_KEY}"`;
  const cursorConfiguration = JSON.stringify({
    mcpServers: {
      "grok-search": {
        url: mcpEndpoint,
        headers: {
          Authorization: "Bearer YOUR_MCP_API_KEY"
        }
      }
    }
  }, null, 2);
  const codexDirectConfiguration = `[mcp_servers.grok_search]
transport = "streamable_http"
url = "${mcpEndpoint}"
headers = { Authorization = "Bearer YOUR_MCP_API_KEY" }`;
  const codexBridgeConfiguration = `[mcp_servers.grok_search]
command = "npx"
args = [
  "-y",
  "mcp-remote",
  "${mcpEndpoint}",
  "--header",
  "Authorization: Bearer YOUR_MCP_API_KEY"
]`;

  return `
    <section class="guide-page">
      <header class="guide-hero">
        <div class="guide-hero-copy">
          <p class="guide-eyebrow">MCP CLIENT SETUP</p>
          <h1>连接 Grok Search MCP</h1>
          <p>创建客户端密钥，并将当前服务接入支持 Streamable HTTP 的 MCP 客户端。</p>
        </div>
        <div class="guide-endpoint-card">
          <span>当前 MCP 地址</span>
          <strong>${escapeHTML(mcpEndpoint)}</strong>
          <button class="button button-secondary" type="button" data-action="copy-value" data-value="${escapeHTML(mcpEndpoint)}">
            ${renderIcon("copy")} 复制地址
          </button>
        </div>
      </header>

      <div class="guide-notice" role="note">
        <span class="guide-notice-icon">${renderIcon("shield")}</span>
        <div>
          <strong>请使用客户端 API Key</strong>
          <p>MCP 端点使用的是“API 密钥”页面创建的 <code>api_key</code>，不是登录管理面板时使用的 JWT。</p>
        </div>
      </div>

      <div class="guide-steps">
        ${renderGuideStep({
          number: "01",
          title: "创建 API 密钥",
          description: "前往 API 密钥页面创建一个专用于 MCP 客户端的凭证。完整密钥只展示一次，请立即保存。",
          content: `
            <div class="guide-action-row">
              <button class="button button-primary" type="button" data-action="navigate" data-page="keys">
                ${renderIcon("key")} 前往 API 密钥
              </button>
              <span>建议按客户端或设备分别创建，便于后续停用与审计。</span>
            </div>
          `
        })}

        ${renderGuideStep({
          number: "02",
          title: "设置本地环境变量",
          description: "把刚刚创建的完整密钥写入本地环境变量，避免将凭证直接提交到仓库。",
          content: renderCodeBlock("Terminal", environmentCommand)
        })}

        ${renderGuideStep({
          number: "03",
          title: "选择客户端配置方式",
          description: "旧版教程覆盖 Cursor、Claude Code 与 Codex CLI。选择对应客户端，并在保存配置后重新加载 MCP 工具。",
          content: `
            <div class="guide-config-grid">
              ${renderClientCard({
                icon: "code",
                title: "Cursor",
                subtitle: "全局或项目级 mcp.json",
                steps: [
                  "全局配置打开 ~/.cursor/mcp.json，项目配置打开 .cursor/mcp.json。",
                  "加入下面的服务器配置，并在本地替换 YOUR_MCP_API_KEY。",
                  "重启 Cursor，或重新加载 MCP 工具。"
                ],
                codeBlocks: [{ label: "JSON", value: cursorConfiguration }],
                note: "不要将真实密钥写入配置后提交到版本控制。"
              })}
              ${renderClientCard({
                icon: "code",
                title: "Claude Code",
                subtitle: "命令行注册 HTTP MCP",
                steps: [
                  "在需要使用此服务的项目终端中执行命令。",
                  "确保 GROK_SEARCH_MCP_API_KEY 已设置为完整的客户端 API Key。",
                  "注册后列出 MCP 服务并确认连接状态。"
                ],
                codeBlocks: [{ label: "Terminal", value: `${claudeCodeCommand}\n\nclaude mcp list` }],
                note: "也可以在 Claude Code 会话中执行 /mcp 查看连接和工具。"
              })}
              ${renderClientCard({
                icon: "layers",
                title: "Codex CLI",
                subtitle: "Streamable HTTP 或 stdio 桥接",
                steps: [
                  "打开 ~/.codex/config.toml。",
                  "如果当前 Codex 版本支持远程 MCP，优先使用 Streamable HTTP 直连。",
                  "替换 YOUR_MCP_API_KEY；如果不支持远程 HTTP，则使用 mcp-remote 作为 stdio 桥接。"
                ],
                codeBlocks: [
                  { label: "TOML · Direct HTTP", value: codexDirectConfiguration },
                  { label: "TOML · mcp-remote fallback", value: codexBridgeConfiguration }
                ],
                note: "桥接方式需要本机可通过 npx 启动 mcp-remote。"
              })}
            </div>
          `
        })}
      </div>

      <section class="guide-checklist">
        <div class="guide-section-heading">
          <span>${renderIcon("check")}</span>
          <div><h2>连接检查</h2><p>客户端连接后应能看到以下三个工具。</p></div>
        </div>
        <div class="guide-tool-list">
          ${renderTool("grok_web_search", "实时网页搜索")}
          ${renderTool("grok_x_search", "实时 X / Twitter 搜索")}
          ${renderTool("grok_list_models", "列出可用 Grok 模型")}
        </div>
      </section>

      <section class="guide-troubleshooting">
        <div class="guide-section-heading">
          <span>${renderIcon("warning")}</span>
          <div><h2>常见问题</h2><p>连接失败时优先检查地址、凭证与网络安全配置。</p></div>
        </div>
        <div class="guide-faq-grid">
          ${renderTroubleshootingItem("确认服务可达", `从客户端所在机器访问 ${mcpEndpoint}；远程部署建议通过 HTTPS 反向代理暴露该端点。`)}
          ${renderTroubleshootingItem("确认 Authorization 请求头", "请求必须携带 Authorization: Bearer <api_key>，并使用创建时返回的完整密钥。")}
          ${renderTroubleshootingItem("不要使用面板 JWT", "MCP 客户端使用 API 密钥页面生成的凭证，不使用登录面板获得的 JWT、密钥名称或脱敏前缀。")}
          ${renderTroubleshootingItem("重新加载客户端", "编辑配置文件后重启客户端，或使用客户端提供的重新加载 MCP 工具功能。")}
          ${renderTroubleshootingItem("区分服务路径", "MCP 流量使用 /mcp；/panel/v1 仅供管理面板 API 使用。")}
          ${renderTroubleshootingItem("检查传输兼容性", "客户端需支持 Streamable HTTP；不支持远程 HTTP 的 Codex CLI 可使用 mcp-remote 桥接。")}
        </div>
      </section>
    </section>
  `;
}

function buildMCPEndpoint() {
  const currentOrigin = window.location.origin;
  return currentOrigin === "null" ? "http://127.0.0.1:8080/mcp" : `${currentOrigin}/mcp`;
}

function renderGuideStep({ number, title, description, content }) {
  return `
    <section class="guide-step">
      <div class="guide-step-marker"><span>${escapeHTML(number)}</span></div>
      <div class="guide-step-body">
        <header><h2>${escapeHTML(title)}</h2><p>${escapeHTML(description)}</p></header>
        ${content}
      </div>
    </section>
  `;
}

function renderCodeBlock(label, codeValue) {
  return `
    <div class="guide-code-block">
      <div class="guide-code-toolbar">
        <span>${escapeHTML(label)}</span>
        <button type="button" data-action="copy-value" data-value="${escapeHTML(codeValue)}" aria-label="复制代码">
          ${renderIcon("copy")} 复制
        </button>
      </div>
      <pre><code>${escapeHTML(codeValue)}</code></pre>
    </div>
  `;
}

function renderClientCard({ icon, title, subtitle, steps, codeBlocks, note }) {
  return `
    <article class="guide-config-card">
      <div class="guide-config-heading">
        <span class="guide-config-icon">${renderIcon(icon)}</span>
        <div><strong>${escapeHTML(title)}</strong><span>${escapeHTML(subtitle)}</span></div>
      </div>
      <ol class="guide-client-steps">
        ${steps.map((step) => `<li>${escapeHTML(step)}</li>`).join("")}
      </ol>
      ${codeBlocks.map((codeBlock) => renderCodeBlock(codeBlock.label, codeBlock.value)).join("")}
      <p>${escapeHTML(note)}</p>
    </article>
  `;
}

function renderTool(name, description) {
  return `
    <article class="guide-tool-item">
      <span>${renderIcon("spark")}</span>
      <div><code>${escapeHTML(name)}</code><p>${escapeHTML(description)}</p></div>
    </article>
  `;
}

function renderTroubleshootingItem(title, description) {
  return `<article><strong>${escapeHTML(title)}</strong><p>${escapeHTML(description)}</p></article>`;
}
