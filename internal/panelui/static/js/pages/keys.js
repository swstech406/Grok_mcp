import { escapeHTML, formatDateTime, formatNumber, formatRelativeTime } from "../utils.js";
import { renderIcon } from "../components/icons.js";
import { renderEmptyState, renderLoadingTable, renderPageHeading, renderStatusBadge } from "../components/loading.js";

export function renderKeysPage(state) {
  const createButton = `<button class="button button-primary" type="button" data-action="open-create-key">${renderIcon("plus")} 创建密钥</button>`;
  if (state.pageLoading && !state.data.keys) {
    return `${renderPageHeading("API 密钥", "为 MCP 客户端创建独立凭证，并控制每个凭证的启用状态。", createButton)}${renderLoadingTable(6, 5)}`;
  }

  const keys = state.data.keys || [];
  return `
    ${renderPageHeading("API 密钥", "为 MCP 客户端创建独立凭证，并控制每个凭证的启用状态。", createButton)}
    ${keys.length === 0 ? `
      <div class="data-card">${renderEmptyState("key", "还没有 API 密钥", "创建第一个密钥，然后将它作为 Bearer Token 连接 /mcp 端点。", createButton)}</div>
    ` : `
      <div class="data-card">
        <div class="data-table-wrap">
          <table class="data-table">
            <thead><tr><th>密钥</th><th>状态</th><th>累计调用</th><th>最近使用</th><th>创建时间</th><th aria-label="操作"></th></tr></thead>
            <tbody>${keys.map((apiKey) => `
              <tr>
                <td><div class="primary-cell"><span class="cell-icon">${renderIcon("key")}</span><span class="cell-copy"><strong>${escapeHTML(apiKey.name)}</strong><span>${escapeHTML(apiKey.key_prefix)}••••••••</span></span></div></td>
                <td>${renderStatusBadge(Boolean(apiKey.enabled))}</td>
                <td><strong>${escapeHTML(formatNumber(apiKey.total_calls))}</strong></td>
                <td>${escapeHTML(formatRelativeTime(apiKey.last_used_at))}</td>
                <td>${escapeHTML(formatDateTime(apiKey.created_at))}</td>
                <td><div class="table-actions">
                  <button class="table-action" type="button" data-action="copy-key" data-id="${escapeHTML(apiKey.id)}" aria-label="复制密钥" title="复制完整密钥">${renderIcon("copy")}</button>
                  <button class="table-action" type="button" data-action="open-key-usage" data-id="${escapeHTML(apiKey.id)}" aria-label="查看用量">${renderIcon("chart")}</button>
                  <button class="table-action" type="button" data-action="open-edit-key" data-id="${escapeHTML(apiKey.id)}" aria-label="编辑密钥">${renderIcon("edit")}</button>
                  <button class="table-action is-danger" type="button" data-action="confirm-delete-key" data-id="${escapeHTML(apiKey.id)}" aria-label="删除密钥">${renderIcon("trash")}</button>
                </div></td>
              </tr>
            `).join("")}</tbody>
          </table>
        </div>
      </div>
    `}
  `;
}
