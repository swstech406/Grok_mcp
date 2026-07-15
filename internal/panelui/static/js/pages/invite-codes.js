import { calculatePercent, escapeHTML, formatNumber, formatShortDate } from "../utils.js";
import { renderIcon } from "../components/icons.js";
import { renderEmptyState, renderPageHeading, renderStatusBadge } from "../components/loading.js";
import { renderCollectionPagination } from "../components/pagination.js";

export function renderInvitesPage(state) {
  const createButton = `<button class="button button-primary" type="button" data-action="open-create-invite">${renderIcon("plus")} 创建邀请码</button>`;
  if (state.pageLoading && !state.data.invites) {
    return `${renderPageHeading("邀请码", "为邀请注册模式创建、停用和追踪一次性注册凭证。", createButton)}<div class="skeleton" style="height:310px;border-radius:16px"></div>`;
  }

  const invites = state.data.invites || [];
  return `
    ${renderPageHeading("邀请码", "为邀请注册模式创建、停用和追踪注册凭证。", createButton)}
    ${invites.length === 0 ? `<div class="data-card">${renderEmptyState("ticket", "还没有邀请码", "创建邀请码后，用户可在邀请注册模式下完成账户创建。", createButton)}</div>` : `
      <div class="invite-list">${invites.map((inviteCode) => {
        const usagePercent = calculatePercent(inviteCode.registration_count, inviteCode.registration_limit);
        const visibleCode = inviteCode.code || `${inviteCode.code_prefix || "invite"}••••••••`;
        return `
          <article class="invite-card">
            <div class="invite-code">
              <span class="invite-code-mark">${renderIcon("ticket")}</span>
              <span class="invite-code-copy"><strong>${escapeHTML(visibleCode)}</strong><span>${renderStatusBadge(Boolean(inviteCode.enabled), "可用", "已停用")} · ${escapeHTML(formatShortDate(inviteCode.created_at))}</span></span>
            </div>
            <div class="usage-progress">
              <div class="usage-progress-copy"><span>注册用量</span><strong>${escapeHTML(formatNumber(inviteCode.registration_count))} / ${escapeHTML(formatNumber(inviteCode.registration_limit))}</strong></div>
              <div class="progress-track"><span style="width:${usagePercent}%"></span></div>
            </div>
            <div class="table-actions">
              ${inviteCode.code ? `<button class="table-action" type="button" data-action="copy-value" data-value="${escapeHTML(inviteCode.code)}" aria-label="复制邀请码">${renderIcon("copy")}</button>` : ""}
              <button class="table-action" type="button" data-action="toggle-invite" data-id="${escapeHTML(inviteCode.id)}" aria-label="${inviteCode.enabled ? "停用" : "启用"}邀请码">${inviteCode.enabled ? renderIcon("close") : renderIcon("check")}</button>
              <button class="table-action is-danger" type="button" data-action="confirm-delete-invite" data-id="${escapeHTML(inviteCode.id)}" aria-label="删除邀请码">${renderIcon("trash")}</button>
            </div>
          </article>
        `;
      }).join("")}</div>
      ${renderCollectionPagination("invites", state.pagination.invites, invites.length)}
    `}
  `;
}
