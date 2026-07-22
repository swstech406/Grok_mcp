import { escapeHTML, formatDateTime, formatNumber, formatPercent, getSuccessRate } from "../utils.js";
import { renderIcon } from "./icons.js";
import { renderInviteRedemptionsModal } from "./invite-redemptions-modal.js";
import { renderMetricCard } from "./metric-card.js";
import { renderChart } from "./usage-chart.js";
import { renderUsageRecords } from "./usage-records.js";
import { isCurrentTierAvailable } from "./tier-selection.js";
import { COLLECTION_PAGE_SIZE_OPTIONS } from "../pagination-config.js";

export function renderModal(state) {
  const modal = state.modal;
  if (!modal) {
    return "";
  }

  switch (modal.type) {
    case "createKey":
      return renderCreateKeyModal(modal);
    case "editKey":
      return renderEditKeyModal(modal);
    case "secret":
      return renderSecretModal(modal);
    case "keyUsage":
      return renderKeyUsageModal(modal);
    case "editUser":
      return renderEditUserModal(modal, state.data.tiers || [], state.user);
    case "userUsage":
      return renderUserUsageModal(modal);
    case "userUsageLogs":
      return renderUserUsageLogsModal(modal);
    case "createTier":
      return renderTierModal(modal, false);
    case "editTier":
      return renderTierModal(modal, true);
    case "createInvite":
      return renderCreateInviteModal(modal);
    case "inviteRedemptions":
      return renderInviteRedemptionsModal(modal, renderModalFrame);
    case "debugJSON":
      return renderDebugJSONModal(modal);
    case "confirm":
      return renderConfirmModal(modal);
    default:
      return "";
  }
}

function renderModalFrame({ title, description, body, footer, wide = false, closeDisabled = false, modalClass = "" }) {
  return `
    <div class="modal-backdrop" data-action="modal-backdrop">
      <section class="modal-card ${wide ? "is-wide" : ""} ${escapeHTML(modalClass)}" role="dialog" aria-modal="true" aria-labelledby="modal-title">
        <header class="modal-header"><div><h2 id="modal-title">${escapeHTML(title)}</h2>${description ? `<p>${escapeHTML(description)}</p>` : ""}</div><button class="modal-close" type="button" data-action="close-modal" aria-label="关闭" ${closeDisabled ? "disabled" : ""}>${renderIcon("close")}</button></header>
        <div class="modal-body">${body}</div>
        ${footer ? `<footer class="modal-footer">${footer}</footer>` : ""}
      </section>
    </div>
  `;
}

function renderDebugJSONModal(modal) {
  const record = modal.record || {};

	if (modal.loading) {
		return renderModalFrame({
			title: "正在加载调试详情",
			description: `正在按需读取调用 #${record.id ?? "--"} 的请求与响应正文。`,
			body: `<div class="inline-alert">${renderIcon("activity")}<span>调试正文不会随用量列表提前传输，请稍候。</span></div>`,
			footer: `<button class="button button-secondary" type="button" data-action="close-modal">取消</button>`,
			wide: true,
			modalClass: "debug-json-modal"
		});
	}

	if (modal.error) {
		return renderModalFrame({
			title: "无法加载调试详情",
			description: `调用 #${record.id ?? "--"} 的调试正文未能读取。`,
			body: `<div class="inline-alert">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>`,
			footer: `<button class="button button-secondary" type="button" data-action="close-modal">关闭</button>`,
			wide: true,
			modalClass: "debug-json-modal"
		});
	}

  const rawDebugJSON = String(record.debug_json || "");
  let parsedDebugJSON = null;
  let parseError = "";

  try {
    parsedDebugJSON = JSON.parse(rawDebugJSON);
    if (isDebugJSONObject(parsedDebugJSON)) {
      if (typeof record.debug_request_body === "string") {
        parsedDebugJSON.request = isDebugJSONObject(parsedDebugJSON.request)
          ? { ...parsedDebugJSON.request, body: record.debug_request_body }
          : { body: record.debug_request_body };
      }
      if (typeof record.debug_response_body === "string") {
        parsedDebugJSON.response = isDebugJSONObject(parsedDebugJSON.response)
          ? { ...parsedDebugJSON.response, body: record.debug_response_body }
          : { body: record.debug_response_body };
      }
    }
  } catch (error) {
    parseError = error instanceof Error ? error.message : "无法解析调试数据";
  }

  const body = `
    <section class="debug-summary-grid" aria-label="调用摘要">
      ${renderDebugSummaryItem("工具", record.tool_name || "unknown", "code")}
      ${renderDebugSummaryItem("结果", record.success ? "成功" : "失败", record.success ? "success" : "danger")}
      ${renderDebugSummaryItem("耗时", `${formatNumber(record.duration_ms)} ms`, "latency")}
      ${renderDebugSummaryItem("发生时间", formatDateTime(record.timestamp), "time")}
    </section>

    ${parseError ? `
      <div class="inline-alert debug-parse-alert">
        ${renderIcon("alert")}
        <span><strong>调试数据不是有效 JSON</strong><small>${escapeHTML(parseError)}，已回退为原始内容展示。</small></span>
      </div>
      <div class="debug-json-stack">
        ${renderDebugJSONSection("原始调试内容", "保留后端返回的原始文本", rawDebugJSON, true)}
      </div>
    ` : renderParsedDebugJSON(parsedDebugJSON)}

    <div class="warning-callout debug-privacy-note">
      ${renderIcon("warning")}
      <span>调试记录可能包含请求参数和响应内容。请仅在可信环境中查看或复制，并在排查完成后关闭调试模式。</span>
    </div>
  `;
  const footer = `
    <button class="button button-secondary" type="button" data-action="close-modal">关闭</button>
    <button class="button button-primary" type="button" data-action="copy-debug-json">${renderIcon("copy")} 复制完整 JSON</button>
  `;

  return renderModalFrame({
    title: "调试详情",
    description: `调用 #${record.id ?? "--"} 的请求、响应与执行上下文。`,
    body,
    footer,
    wide: true,
    modalClass: "debug-json-modal"
  });
}

function renderDebugSummaryItem(label, value, tone) {
  return `
    <div class="debug-summary-item is-${escapeHTML(tone)}">
      <span>${escapeHTML(label)}</span>
      <strong>${escapeHTML(value)}</strong>
    </div>
  `;
}

function renderParsedDebugJSON(parsedDebugJSON) {
  if (!isDebugJSONObject(parsedDebugJSON)) {
    return `<div class="debug-json-stack">${renderDebugJSONSection("完整调试数据", "后端返回的 JSON 内容", parsedDebugJSON, true)}</div>`;
  }

  const requestPayload = parsedDebugJSON.request;
  const responsePayload = parsedDebugJSON.response;
  const contextPayload = Object.fromEntries(
    Object.entries(parsedDebugJSON).filter(([fieldName]) => fieldName !== "request" && fieldName !== "response")
  );

  return `
    <div class="debug-json-stack">
      ${requestPayload !== undefined ? renderDebugHTTPSection("请求", requestPayload, true) : ""}
      ${responsePayload !== undefined ? renderDebugHTTPSection("响应", responsePayload, false) : ""}
      ${Object.keys(contextPayload).length > 0 ? renderDebugJSONSection("执行上下文", "认证、MCP 调用与采集元数据", contextPayload, false) : ""}
      ${requestPayload === undefined && responsePayload === undefined && Object.keys(contextPayload).length === 0
        ? renderDebugJSONSection("完整调试数据", "后端返回的 JSON 内容", parsedDebugJSON, true)
        : ""}
    </div>
  `;
}

function renderDebugHTTPSection(title, payload, expanded) {
  if (!isDebugJSONObject(payload)) {
    return renderDebugJSONSection(title, "后端返回的捕获内容", payload, expanded);
  }

  const hasBody = Object.prototype.hasOwnProperty.call(payload, "body");
  const bodyPayload = payload.body;
  const metadataPayload = Object.fromEntries(
    Object.entries(payload).filter(([fieldName]) => fieldName !== "body")
  );
  const sectionDescription = title === "请求"
    ? [payload.method, payload.path].filter(Boolean).join(" ") || "请求元数据与请求体"
    : payload.status !== undefined
      ? `HTTP ${payload.status}`
      : "响应元数据与响应体";

  return `
    <details class="debug-json-section" ${expanded ? "open" : ""}>
      <summary>
        <span class="debug-json-section-icon">${renderIcon(title === "请求" ? "arrowRight" : "activity")}</span>
        <span class="debug-json-section-copy"><strong>${escapeHTML(title)}</strong><small>${escapeHTML(sectionDescription)}</small></span>
        ${renderIcon("chevronDown", "debug-json-chevron")}
      </summary>
      <div class="debug-json-section-body">
        ${Object.keys(metadataPayload).length > 0 ? renderDebugJSONCodeBlock("元数据", metadataPayload) : ""}
        ${hasBody ? renderDebugJSONCodeBlock(`${title}体`, bodyPayload) : '<p class="debug-json-no-body">未捕获正文内容</p>'}
      </div>
    </details>
  `;
}

function renderDebugJSONSection(title, description, value, expanded) {
  return `
    <details class="debug-json-section" ${expanded ? "open" : ""}>
      <summary>
        <span class="debug-json-section-icon">${renderIcon("code")}</span>
        <span class="debug-json-section-copy"><strong>${escapeHTML(title)}</strong><small>${escapeHTML(description)}</small></span>
        ${renderIcon("chevronDown", "debug-json-chevron")}
      </summary>
      <div class="debug-json-section-body">${renderDebugJSONCodeBlock("内容", value)}</div>
    </details>
  `;
}

function renderDebugJSONCodeBlock(label, value) {
  const displayValue = createDebugJSONDisplayValue(value);
  return `
    <div class="debug-json-code-group">
      <div class="debug-json-code-label"><span>${escapeHTML(label)}</span><small>${escapeHTML(displayValue.description)}</small></div>
      <pre class="debug-json-code"><code>${escapeHTML(displayValue.text)}</code></pre>
    </div>
  `;
}

function createDebugJSONDisplayValue(value) {
  if (typeof value === "string") {
    const trimmedValue = value.trim();
    if (trimmedValue.startsWith("{") || trimmedValue.startsWith("[")) {
      try {
        const parsedValue = JSON.parse(trimmedValue);
        return {
          text: JSON.stringify(parsedValue, null, 2),
          description: "嵌套 JSON"
        };
      } catch {
        // Keep malformed or truncated nested payloads readable as their original text.
      }
    }
    return {
      text: value || "(空字符串)",
      description: `${formatNumber(value.length)} 个字符`
    };
  }

  const formattedValue = JSON.stringify(value, null, 2);
  if (Array.isArray(value)) {
    return { text: formattedValue, description: `${formatNumber(value.length)} 项` };
  }
  if (isDebugJSONObject(value)) {
    return { text: formattedValue, description: `${formatNumber(Object.keys(value).length)} 个字段` };
  }
  return { text: formattedValue ?? String(value), description: value === null ? "null" : typeof value };
}

function isDebugJSONObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function renderCreateKeyModal(modal) {
  const body = `<form class="stack-form" id="create-key-form" data-form="create-key"><label class="field-group"><span class="field-label">密钥名称</span><input class="text-input" name="name" type="text" maxlength="120" placeholder="例如：Claude Desktop" required autofocus></label>${modal.error ? `<div class="inline-alert">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>` : ""}</form>`;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">取消</button><button class="button button-primary" type="submit" form="create-key-form" ${modal.busy ? "disabled" : ""}>${modal.busy ? "正在创建" : `${renderIcon("plus")} 创建密钥`}</button>`;
  return renderModalFrame({ title: "创建 API 密钥", description: "明文密钥只会在创建成功后显示一次。", body, footer });
}

function renderEditKeyModal(modal) {
  const apiKey = modal.data || {};
  const body = `<form class="stack-form" id="edit-key-form" data-form="edit-key" data-id="${escapeHTML(apiKey.id)}"><label class="field-group"><span class="field-label">密钥名称</span><input class="text-input" name="name" type="text" maxlength="120" value="${escapeHTML(apiKey.name)}" required autofocus></label><label class="switch-row"><span class="switch-copy"><strong>启用密钥</strong><span>停用后 MCP 请求会立即失去授权</span></span><span class="switch"><input name="enabled" type="checkbox" ${apiKey.enabled ? "checked" : ""}><span class="switch-track"></span></span></label>${modal.error ? `<div class="inline-alert">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>` : ""}</form>`;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">取消</button><button class="button button-primary" type="submit" form="edit-key-form" ${modal.busy ? "disabled" : ""}>保存更改</button>`;
  return renderModalFrame({ title: "编辑 API 密钥", description: apiKey.key_prefix ? `前缀 ${apiKey.key_prefix}` : "", body, footer });
}

function renderSecretModal(modal) {
  const secretType = modal.secretType === "invite" ? "邀请码" : "API 密钥";
  const body = `
    <div class="secret-display"><span class="secret-label">${escapeHTML(secretType)}</span><code class="secret-value">${escapeHTML(modal.secret)}</code></div>
    <div class="warning-callout">${renderIcon("warning")}<span>${modal.secretType === "invite" ? "请立即复制并安全发送给目标用户。关闭窗口后，完整邀请码将无法再次获取。" : "请立即复制并安全保存。关闭窗口后，API 密钥明文将无法再次获取。"}</span></div>
  `;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">完成</button><button class="button button-accent" type="button" data-action="copy-value" data-value="${escapeHTML(modal.secret)}">${renderIcon("copy")} 复制${escapeHTML(secretType)}</button>`;
  return renderModalFrame({ title: modal.title || `${secretType}已创建`, description: modal.subtitle || "创建成功", body, footer });
}

function renderKeyUsageModal(modal) {
  const usage = modal.usage;
  const body = modal.loading ? '<div class="skeleton" style="height:300px"></div>' : `
    <section class="metric-grid" style="grid-template-columns:repeat(3,minmax(0,1fr))">
      ${renderMetricCard("总调用", formatNumber(usage?.total_calls), "全部时间", "activity", "#eeeaff", "#7667f4", false, "trend", usage?.traffic_buckets)}
      ${renderMetricCard("成功调用", formatNumber(usage?.success_calls), formatPercent(getSuccessRate(usage)), "shield", "#e8f8ef", "#238a54", false, "ring", getSuccessRate(usage))}
      ${renderMetricCard("当前 RPM", formatNumber(usage?.current_rpm), "最近一分钟", "chart", "#e8f1ff", "#3d83f6", false, "pulse", usage?.current_rpm)}
    </section>
    <div class="chart-wrap">${renderChart(usage?.traffic_buckets || [])}</div>
  `;
  return renderModalFrame({ title: modal.title || "密钥调用分析", description: "该密钥的调用量与成功率。", body, footer: '<button class="button button-secondary" type="button" data-action="close-modal">关闭</button>', wide: true });
}

function renderEditUserModal(modal, tiers, currentUser) {
  const user = modal.data || {};
  const isCurrentUser = user.id === currentUser?.id;
  const currentTierAvailable = isCurrentTierAvailable(tiers, user.tier_id);
  const unavailableTierOption = currentTierAvailable
    ? ""
    : '<option value="" selected disabled>当前配额方案不可用</option>';
  const unavailableTierAlert = currentTierAvailable
    ? ""
    : `<div class="inline-alert">${renderIcon("alert")}<span>当前配额方案已不存在或无法加载，请先选择新的配额方案。</span></div>`;
  const body = `
    <form class="stack-form" id="edit-user-form" data-form="edit-user" data-id="${escapeHTML(user.id)}">
      <div class="form-grid">
        <label class="field-group"><span class="field-label">角色</span><select class="select-input" name="role" ${isCurrentUser ? "disabled" : ""}><option value="user" ${user.role === "user" ? "selected" : ""}>用户</option><option value="admin" ${user.role === "admin" ? "selected" : ""}>管理员</option></select></label>
        <label class="field-group"><span class="field-label">配额方案</span><select class="select-input" name="tier_id" required>${unavailableTierOption}${tiers.map((tier) => `<option value="${escapeHTML(tier.id)}" ${tier.id === user.tier_id ? "selected" : ""}>${escapeHTML(tier.name)}</option>`).join("")}</select></label>
      </div>
      <label class="switch-row"><span class="switch-copy"><strong>启用账户</strong><span>禁用后用户的面板与 MCP 访问都会失效</span></span><span class="switch"><input name="enabled" type="checkbox" ${user.enabled ? "checked" : ""} ${isCurrentUser ? "disabled" : ""}><span class="switch-track"></span></span></label>
      <label class="switch-row"><span class="switch-copy"><strong>吊销全部会话</strong><span>保存后立即使该用户的所有现有 JWT 失效</span></span><span class="switch"><input name="revoke_tokens" type="checkbox"><span class="switch-track"></span></span></label>
      ${unavailableTierAlert}
      ${modal.error ? `<div class="inline-alert">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>` : ""}
    </form>
  `;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">取消</button><button class="button button-primary" type="submit" form="edit-user-form" ${modal.busy ? "disabled" : ""}>保存用户</button>`;
  return renderModalFrame({ title: `编辑 ${user.username || "用户"}`, description: "角色或启用状态变化会自动吊销旧会话。", body, footer });
}

function renderUserUsageModal(modal) {
  const usage = modal.usage;
  const recentRecords = modal.recentRecords || usage?.records?.slice(0, 8) || [];
  const body = modal.loading ? '<div class="skeleton" style="height:380px"></div>' : `
    <section class="metric-grid" style="grid-template-columns:repeat(3,minmax(0,1fr))">
      ${renderMetricCard("总调用", formatNumber(usage?.total_calls), "全部密钥", "activity", "#eeeaff", "#7667f4", false, "trend", usage?.traffic_buckets)}
      ${renderMetricCard("成功调用", formatNumber(usage?.success_calls), formatPercent(getSuccessRate(usage)), "shield", "#e8f8ef", "#238a54", false, "ring", getSuccessRate(usage))}
      ${renderMetricCard("当前 RPM", formatNumber(usage?.current_rpm), "最近一分钟", "chart", "#e8f1ff", "#3d83f6", false, "pulse", usage?.current_rpm)}
    </section>
    <div class="chart-wrap">${renderChart(usage?.traffic_buckets || [])}</div>
    ${renderUsageRecords(recentRecords)}
  `;
  const footer = `
    <button class="button button-secondary" type="button" data-action="close-modal">关闭</button>
    ${!modal.loading && recentRecords.length > 0 ? `<button class="button button-primary" type="button" data-action="view-user-usage-logs">${renderIcon("activity")} 查看调用记录</button>` : ""}
  `;
  return renderModalFrame({ title: `${modal.username || "用户"} 的调用分析`, description: "聚合该用户全部 API 密钥的调用数据，最近活动仅展示前 8 条。", body, footer, wide: true });
}

function renderUserUsageLogsModal(modal) {
  const usage = modal.usage;
  const usageRecords = usage?.records || [];
  const currentPage = (modal.previousCursors?.length || 0) + 1;
  const previousPageAvailable = (modal.previousCursors?.length || 0) > 0;
  const nextPageAvailable = Boolean(modal.hasMore && modal.nextCursor);
  const failedCalls = Math.max(0, Number(usage?.total_calls || 0) - Number(usage?.success_calls || 0));

  const body = `
    <section class="metric-grid" style="grid-template-columns:repeat(3,minmax(0,1fr))">
      ${renderMetricCard("总调用", formatNumber(usage?.total_calls), "全部密钥", "activity", "#eeeaff", "#7667f4")}
      ${renderMetricCard("成功调用", formatNumber(usage?.success_calls), formatPercent(getSuccessRate(usage)), "shield", "#e8f8ef", "#238a54")}
      ${renderMetricCard("失败调用", formatNumber(failedCalls), "不计入成功调用额度", "alert", "#fff1f0", "#d84a45")}
    </section>
    ${modal.loadingRecords ? '<div class="skeleton" style="height:320px"></div>' : renderUsageRecords(usageRecords)}
  `;
  const footer = `
    <button class="button button-secondary" type="button" data-action="view-user-usage-summary" ${modal.loadingRecords ? "disabled" : ""}>返回用量摘要</button>
    <span class="muted modal-pagination-status">第 ${escapeHTML(formatNumber(currentPage))} 页 · 本页 ${escapeHTML(formatNumber(usageRecords.length))} 条</span>
    <label class="pagination-page-size">
      <span>每页</span>
      <select class="select-input" data-action="change-user-usage-page-size" aria-label="每页显示条数" ${modal.loadingRecords ? "disabled" : ""}>
        ${COLLECTION_PAGE_SIZE_OPTIONS.map((pageSize) => `<option value="${pageSize}" ${Number(modal.pageSize) === pageSize ? "selected" : ""}>${pageSize} 条</option>`).join("")}
      </select>
    </label>
    <button class="button button-secondary" type="button" data-action="change-user-usage-page" data-direction="previous" ${!modal.loadingRecords && previousPageAvailable ? "" : "disabled"}>上一页</button>
    <button class="button button-primary" type="button" data-action="change-user-usage-page" data-direction="next" ${!modal.loadingRecords && nextPageAvailable ? "" : "disabled"}>下一页</button>
  `;

  return renderModalFrame({
    title: `${modal.username || "用户"} 的调用记录`,
    description: "查看该用户全部 API 密钥的最近调用明细。",
    body,
    footer,
    wide: true
  });
}

function renderTierModal(modal, isEdit) {
  const tier = modal.data || { name: "", level: 0, rpm: 0, success_limit: 0 };
  const formName = isEdit ? "edit-tier" : "create-tier";
  const body = `
    <form class="stack-form" id="tier-form" data-form="${formName}" ${isEdit ? `data-id="${escapeHTML(tier.id)}"` : ""}>
      <label class="field-group"><span class="field-label">方案名称</span><input class="text-input" name="name" type="text" value="${escapeHTML(tier.name)}" placeholder="例如：Pro" required autofocus></label>
      <div class="form-grid">
        <label class="field-group"><span class="field-label"><span>展示顺序</span><span class="field-hint">越小越靠前</span></span><input class="text-input" name="level" type="number" min="0" step="1" value="${escapeHTML(tier.level)}" required></label>
        <label class="field-group"><span class="field-label"><span>RPM</span><span class="field-hint">0 = 不限</span></span><input class="text-input" name="rpm" type="number" min="0" step="1" value="${escapeHTML(tier.rpm)}" required></label>
        <label class="field-group is-full"><span class="field-label"><span>月度成功调用额度</span><span class="field-hint">0 = 不限</span></span><input class="text-input" name="success_limit" type="number" min="0" step="1" value="${escapeHTML(tier.success_limit)}" required></label>
      </div>
      ${modal.error ? `<div class="inline-alert">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>` : ""}
    </form>
  `;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">取消</button><button class="button button-primary" type="submit" form="tier-form" ${modal.busy ? "disabled" : ""}>${isEdit ? "保存方案" : `${renderIcon("plus")} 创建方案`}</button>`;
  return renderModalFrame({ title: isEdit ? "编辑配额方案" : "创建配额方案", description: "方案控制用户限额；展示顺序只影响列表排列，不代表权限或套餐高低。", body, footer });
}

function renderCreateInviteModal(modal) {
  const body = `<form class="stack-form" id="create-invite-form" data-form="create-invite"><label class="field-group"><span class="field-label">最多注册人数</span><input class="text-input" name="registration_limit" type="number" min="1" step="1" value="1" required autofocus></label><div class="warning-callout">${renderIcon("warning")}<span>完整邀请码只显示一次，请在创建后立即保存。邀请码可重复使用，直到达到注册人数上限或被管理员停用。</span></div>${modal.error ? `<div class="inline-alert">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>` : ""}</form>`;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">取消</button><button class="button button-primary" type="submit" form="create-invite-form" ${modal.busy ? "disabled" : ""}>${renderIcon("plus")} 创建邀请码</button>`;
  return renderModalFrame({ title: "创建邀请码", description: "为邀请注册模式生成新的注册凭证。", body, footer });
}

function renderConfirmModal(modal) {
  const body = `<div class="confirm-visual">${renderIcon("trash")}</div><p class="confirm-copy">${escapeHTML(modal.message || "此操作无法撤销，是否继续？")}</p>${modal.error ? `<div class="inline-alert" style="margin-top:14px">${renderIcon("alert")}<span>${escapeHTML(modal.error)}</span></div>` : ""}`;
  const footer = `<button class="button button-secondary" type="button" data-action="close-modal">取消</button><button class="button button-danger" type="button" data-action="execute-confirm" ${modal.busy ? "disabled" : ""}>${modal.busy ? "正在处理" : escapeHTML(modal.confirmLabel || "确认删除")}</button>`;
  return renderModalFrame({ title: modal.title || "确认操作", description: modal.description || "请确认你的操作", body, footer, closeDisabled: modal.busy });
}
