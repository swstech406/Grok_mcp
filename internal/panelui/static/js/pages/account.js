import { renderPageHeading, renderStatusBadge } from "../components/loading.js";
import {
  calculatePercent,
  escapeHTML,
  formatDateTime,
  formatLimit,
  formatNumber,
  formatPercent,
  getInitials
} from "../utils.js";

export function renderAccountPage(state) {
  const user = state.user || {};
  const limitsUnavailable = Boolean(user.limits_unavailable);
  const successCalls = Number(user.success_calls || 0);
  const successLimit = Number(user.success_limit || 0);
  const requestsPerMinute = Number(user.rpm || 0);
  const hasSuccessLimit = !limitsUnavailable && successLimit > 0;
  const successQuotaPercent = hasSuccessLimit
    ? calculatePercent(successCalls, successLimit)
    : 0;

  const logoutAction = `
    <button class="button button-secondary" type="button" data-action="logout">
      退出登录
    </button>
  `;

  return `
    ${renderPageHeading("账户信息", "查看当前登录账户的身份、配额方案与使用情况。", logoutAction)}

    <section class="account-identity-card">
      <span class="account-avatar">${escapeHTML(getInitials(user.username))}</span>
      <div class="account-identity-copy">
        <span class="account-eyebrow">Current account</span>
        <h2>${escapeHTML(user.username || "用户")}</h2>
        <div class="account-badges">
          <span class="role-badge ${user.role === "admin" ? "is-admin" : "is-user"}">
            ${user.role === "admin" ? "管理员" : "用户"}
          </span>
          ${renderStatusBadge(Boolean(user.enabled), "账户正常", "账户已禁用")}
          <span class="tier-badge">${escapeHTML(user.tier_name || "未分配方案")}</span>
        </div>
      </div>
    </section>

    <section class="account-grid">
      <article class="content-card account-card">
        <header class="account-card-header">
          <div>
            <span class="account-card-kicker">Profile</span>
            <h2>基本资料</h2>
          </div>
          <p>当前会话对应的账户记录</p>
        </header>
        <dl class="account-detail-list">
          ${renderAccountDetail("用户名", user.username || "--")}
          ${renderAccountDetail("用户 ID", user.id || "--", true)}
          ${renderAccountDetail("账户角色", user.role === "admin" ? "管理员" : "普通用户")}
          ${renderAccountDetail("配额方案", user.tier_name || "未分配")}
          ${renderAccountDetail("创建时间", formatDateTime(user.created_at))}
          ${renderAccountDetail("更新时间", formatDateTime(user.updated_at))}
        </dl>
      </article>

      <article class="content-card account-card">
        <header class="account-card-header">
          <div>
            <span class="account-card-kicker">Quotas</span>
            <h2>账户配额</h2>
          </div>
          <p>所有 API 密钥共享以下限制</p>
        </header>
        ${limitsUnavailable ? `
          <div class="account-limit-warning" role="status">
            当前无法解析所属配额方案，限额数据暂不可用。
          </div>
        ` : ""}
        <div class="account-quota-list">
          ${renderQuotaItem({
            label: "每分钟请求数",
            value: limitsUnavailable
              ? "不可用"
              : (requestsPerMinute > 0 ? `${formatNumber(requestsPerMinute)} 次/分钟` : "不限"),
            note: "账户下所有 API 密钥共享此速率上限。"
          })}
          ${renderQuotaItem({
            label: "本月成功调用",
            value: limitsUnavailable
              ? "不可用"
              : `${formatNumber(successCalls)} / ${formatLimit(successLimit)}`,
            note: hasSuccessLimit
              ? `已使用 ${formatPercent(successQuotaPercent, 0)}`
              : (limitsUnavailable ? "等待配额方案恢复后显示。" : "当前方案不限制成功调用次数。"),
            progressPercent: hasSuccessLimit ? successQuotaPercent : null
          })}
        </div>
      </article>

      <article class="content-card account-card account-security-card">
        <header class="account-card-header">
          <div>
            <span class="account-card-kicker">Security</span>
            <h2>修改密码</h2>
          </div>
          <p>更新后自动吊销此前签发的全部会话</p>
        </header>
        <form class="account-security-form" data-form="change-password">
          <label class="field-group is-full">
            <span class="field-label">当前密码</span>
            <input class="text-input" name="current_password" type="password" minlength="8" maxlength="72" autocomplete="current-password" required>
          </label>
          <label class="field-group is-full">
            <span class="field-label">新密码</span>
            <input class="text-input" name="new_password" type="password" minlength="8" maxlength="72" autocomplete="new-password" required>
          </label>
          <label class="field-group is-full">
            <span class="field-label">确认新密码</span>
            <input class="text-input" name="confirm_new_password" type="password" minlength="8" maxlength="72" autocomplete="new-password" required>
          </label>
          <button class="button button-primary" type="submit" ${state.formBusy ? "disabled" : ""}>
            ${state.formBusy ? "处理中..." : "更新密码并替换会话"}
          </button>
        </form>
      </article>

      <article class="content-card account-card account-security-card">
        <header class="account-card-header">
          <div>
            <span class="account-card-kicker">Sessions</span>
            <h2>吊销全部会话</h2>
          </div>
          <p>当前标签页会收到一个新的替换会话</p>
        </header>
        <form class="account-security-form" data-form="revoke-sessions">
          <p class="account-security-copy">
            此操作会立即使包括当前令牌在内的所有旧面板令牌失效。API 密钥不受影响。
          </p>
          <label class="account-security-confirmation">
            <input type="checkbox" required>
            <span>我确认吊销此前签发的全部面板会话</span>
          </label>
          <button class="button button-secondary" type="submit" ${state.formBusy ? "disabled" : ""}>
            ${state.formBusy ? "处理中..." : "吊销并替换当前会话"}
          </button>
        </form>
      </article>
    </section>
  `;
}

function renderAccountDetail(label, value, monospace = false) {
  return `
    <div class="account-detail-row">
      <dt>${escapeHTML(label)}</dt>
      <dd class="${monospace ? "is-monospace" : ""}">${escapeHTML(value)}</dd>
    </div>
  `;
}

function renderQuotaItem({ label, value, note, progressPercent = null }) {
  const hasProgress = Number.isFinite(progressPercent);
  const normalizedProgress = hasProgress
    ? Math.max(0, Math.min(100, progressPercent))
    : 0;

  return `
    <div class="account-quota-item">
      <div class="account-quota-heading">
        <span>${escapeHTML(label)}</span>
        <strong>${escapeHTML(value)}</strong>
      </div>
      ${hasProgress ? `
        <div class="account-progress" role="progressbar" aria-label="${escapeHTML(label)}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${normalizedProgress.toFixed(1)}">
          <span style="width:${normalizedProgress.toFixed(1)}%"></span>
        </div>
      ` : ""}
      <p>${escapeHTML(note)}</p>
    </div>
  `;
}
