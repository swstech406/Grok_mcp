import { api } from "../api.js";
import { metricCard, renderRecentActivity } from "./metric-card.js";
import { tierOptions } from "../pages/tiers.js";
import { state } from "../state.js";
import { escapeAttr, escapeHTML, formatNumber, relativeTime, successPercent } from "../utils.js";

export function renderModal() {
  if (!state.modal) return "";
  if (state.modal.type === "create-key") return renderCreateKeyModal();
  if (state.modal.type === "key-created") return renderKeyCreatedModal(state.modal);
  if (state.modal.type === "edit-key") return renderEditKeyModal(state.modal.key);
  if (state.modal.type === "create-invite-code") return renderCreateInviteCodeModal();
  if (state.modal.type === "edit-invite-code") return renderEditInviteCodeModal(state.modal.inviteCode);
  if (state.modal.type === "edit-user") return renderEditUserModal(state.modal.user);
  if (state.modal.type === "create-tier") return renderCreateTierModal();
  if (state.modal.type === "edit-tier") return renderEditTierModal(state.modal.tier);
  if (state.modal.type === "user-usage") return renderUserUsageModal(state.modal.user, state.modal.usage);
  if (state.modal.type === "user-usage-logs") return renderUserUsageLogsModal(state.modal.user, state.modal.usage);
  if (state.modal.type === "debug-json") return renderDebugJSONModal(state.modal.record);
  if (state.modal.type === "delete-confirm") return renderDeleteConfirmModal(state.modal);
  return "";
}

export function renderDebugJSONModal(record) {
  if (!record) return "";
  const requestID = `req_${String(record.id || "").padStart(8, "0").slice(-8)}`;
  const rawDebugJSON = String(record.debug_json || "{}");
  let parsedDebugJSON = null;
  let formattedDebugJSON = rawDebugJSON;
  let parseError = "";
  try {
    parsedDebugJSON = JSON.parse(rawDebugJSON);
    formattedDebugJSON = JSON.stringify(parsedDebugJSON, null, 2);
  } catch (error) {
    parseError = error instanceof Error ? error.message : "Invalid JSON payload";
  }
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal debug-json-modal" role="dialog" aria-modal="true" aria-label="Debug JSON" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Debug JSON</h3>
          <p>${escapeHTML(requestID)} full captured request and response payload.</p>
          <div class="debug-json-summary">
            ${renderDebugJSONSummaryItem("Tool", record.tool_name || "unknown")}
            ${renderDebugJSONSummaryItem("Status", record.success ? "Success" : "Failed", record.success ? "good" : "bad")}
            ${renderDebugJSONSummaryItem("Latency", record.duration_ms ? `${formatNumber(record.duration_ms)}ms` : "--")}
            ${renderDebugJSONSummaryItem("Time", relativeTime(record.timestamp))}
          </div>
          ${parseError ? `
            <div class="warning-box debug-json-warning">
              <span class="material-symbols-outlined">warning</span>
              <div>
                <strong>Unable to parse as JSON.</strong>
                <p>${escapeHTML(parseError)}</p>
              </div>
            </div>` : ""}
          <div class="debug-json-viewer">
            <div class="debug-json-panel">
              <div class="debug-json-panel-head">
                <span>Visual Tree</span>
                <span class="mono muted">${parsedDebugJSON === null ? "Raw fallback" : `${formatNumber(countDebugJSONNodes(parsedDebugJSON))} nodes`}</span>
              </div>
              <div class="debug-json-tree" role="tree">
                ${parsedDebugJSON === null ? `<pre class="debug-json-block"><code>${escapeHTML(formattedDebugJSON)}</code></pre>` : renderDebugJSONTree(parsedDebugJSON)}
              </div>
            </div>
            <details class="debug-json-panel debug-json-raw-panel">
              <summary class="debug-json-panel-head">
                <span>Formatted Raw JSON</span>
                <span class="mono muted">click to expand</span>
              </summary>
              <pre class="debug-json-block"><code>${escapeHTML(formattedDebugJSON)}</code></pre>
            </details>
          </div>
        </div>
      </section>
    </div>`;
}

function renderDebugJSONSummaryItem(label, value, tone = "") {
  return `
    <div class="debug-json-summary-item ${escapeAttr(tone)}">
      <span>${escapeHTML(label)}</span>
      <strong>${escapeHTML(value)}</strong>
    </div>`;
}

function renderDebugJSONTree(value, key = "root", depth = 0) {
  if (Array.isArray(value)) {
    return renderDebugJSONArray(value, key, depth);
  }
  if (value && typeof value === "object") {
    return renderDebugJSONObject(value, key, depth);
  }
  return renderDebugJSONPrimitive(key, value, depth);
}

function renderDebugJSONObject(value, key, depth) {
  const entries = Object.entries(value);
  const shouldOpen = depth < 2;
  return `
    <details class="debug-json-node" ${shouldOpen ? "open" : ""}>
      <summary>
        <span class="debug-json-key">${escapeHTML(key)}</span>
        <span class="debug-json-type">object</span>
        <span class="debug-json-count">${formatNumber(entries.length)} ${entries.length === 1 ? "field" : "fields"}</span>
      </summary>
      <div class="debug-json-children">
        ${entries.length ? entries.map(([entryKey, entryValue]) => renderDebugJSONTree(entryValue, entryKey, depth + 1)).join("") : `<span class="debug-json-empty">empty object</span>`}
      </div>
    </details>`;
}

function renderDebugJSONArray(value, key, depth) {
  const shouldOpen = depth < 2;
  return `
    <details class="debug-json-node" ${shouldOpen ? "open" : ""}>
      <summary>
        <span class="debug-json-key">${escapeHTML(key)}</span>
        <span class="debug-json-type">array</span>
        <span class="debug-json-count">${formatNumber(value.length)} ${value.length === 1 ? "item" : "items"}</span>
      </summary>
      <div class="debug-json-children">
        ${value.length ? value.map((entryValue, entryIndex) => renderDebugJSONTree(entryValue, `[${entryIndex}]`, depth + 1)).join("") : `<span class="debug-json-empty">empty array</span>`}
      </div>
    </details>`;
}

function renderDebugJSONPrimitive(key, value, depth) {
  const valueType = value === null ? "null" : typeof value;
  if (valueType === "string") {
    const parsedStringPayload = parseDebugJSONString(value);
    if (parsedStringPayload) {
      return renderDebugJSONParsedString(key, value, parsedStringPayload, depth);
    }
  }
  const displayValue = valueType === "string" ? `"${value}"` : String(value);
  return `
    <div class="debug-json-leaf">
      <span class="debug-json-key">${escapeHTML(key)}</span>
      <span class="debug-json-type">${escapeHTML(valueType)}</span>
      <span class="debug-json-value ${escapeAttr(`is-${valueType}`)}">${escapeHTML(displayValue)}</span>
    </div>`;
}

function renderDebugJSONParsedString(key, originalValue, parsedStringPayload, depth) {
  const shouldOpen = depth < 3;
  return `
    <details class="debug-json-node debug-json-string-node" ${shouldOpen ? "open" : ""}>
      <summary>
        <span class="debug-json-key">${escapeHTML(key)}</span>
        <span class="debug-json-type">string</span>
        <span class="debug-json-count">${escapeHTML(parsedStringPayload.label)}</span>
      </summary>
      <div class="debug-json-string-preview">${escapeHTML(createDebugJSONStringPreview(originalValue))}</div>
      <div class="debug-json-children">
        ${renderDebugJSONTree(parsedStringPayload.value, parsedStringPayload.key, depth + 1)}
      </div>
    </details>`;
}

function parseDebugJSONString(value) {
  const trimmedValue = value.trim();
  if (!trimmedValue) return null;

  const parsedServerSentEvents = parseDebugJSONServerSentEvents(trimmedValue);
  if (parsedServerSentEvents) return parsedServerSentEvents;

  const parsedJSONValue = parseDebugJSONText(trimmedValue);
  if (!parsedJSONValue) return null;

  return {
    key: "parsed",
    label: Array.isArray(parsedJSONValue) ? "parsed JSON array" : "parsed JSON object",
    value: parsedJSONValue
  };
}

function parseDebugJSONText(value) {
  const trimmedValue = value.trim();
  const looksLikeJSONContainer = trimmedValue.startsWith("{") || trimmedValue.startsWith("[");
  if (!looksLikeJSONContainer) return null;

  try {
    const parsedValue = JSON.parse(trimmedValue);
    return parsedValue && typeof parsedValue === "object" ? parsedValue : null;
  } catch {
    return null;
  }
}

function parseDebugJSONServerSentEvents(value) {
  const normalizedValue = value.replace(/\r\n/g, "\n");
  const lines = normalizedValue.split("\n");
  const hasServerSentEventField = lines.some((line) => /^(event|data|id|retry):/.test(line));
  if (!hasServerSentEventField) return null;

  const parsedEvents = [];
  let currentEvent = createEmptyDebugJSONServerSentEvent();
  for (const line of lines) {
    if (line === "") {
      appendDebugJSONServerSentEvent(parsedEvents, currentEvent);
      currentEvent = createEmptyDebugJSONServerSentEvent();
      continue;
    }
    if (line.startsWith(":")) continue;

    const separatorIndex = line.indexOf(":");
    const fieldName = separatorIndex === -1 ? line : line.slice(0, separatorIndex);
    const rawFieldValue = separatorIndex === -1 ? "" : line.slice(separatorIndex + 1);
    const fieldValue = rawFieldValue.startsWith(" ") ? rawFieldValue.slice(1) : rawFieldValue;

    if (fieldName === "event") {
      currentEvent.event = fieldValue;
    } else if (fieldName === "data") {
      currentEvent.dataLines.push(fieldValue);
    } else if (fieldName === "id") {
      currentEvent.id = fieldValue;
    } else if (fieldName === "retry") {
      currentEvent.retry = fieldValue;
    }
  }
  appendDebugJSONServerSentEvent(parsedEvents, currentEvent);

  if (!parsedEvents.length) return null;
  return {
    key: parsedEvents.length === 1 ? "sse" : "events",
    label: `${formatNumber(parsedEvents.length)} ${parsedEvents.length === 1 ? "SSE event" : "SSE events"}`,
    value: parsedEvents.length === 1 ? parsedEvents[0] : parsedEvents
  };
}

function createEmptyDebugJSONServerSentEvent() {
  return {
    event: "",
    id: "",
    retry: "",
    dataLines: []
  };
}

function appendDebugJSONServerSentEvent(parsedEvents, currentEvent) {
  const hasEventData = currentEvent.dataLines.length > 0;
  const hasEventMetadata = Boolean(currentEvent.event || currentEvent.id || currentEvent.retry);
  if (!hasEventData && !hasEventMetadata) return;

  const parsedEvent = {};
  if (currentEvent.event) parsedEvent.event = currentEvent.event;
  if (currentEvent.id) parsedEvent.id = currentEvent.id;
  if (currentEvent.retry) parsedEvent.retry = currentEvent.retry;
  if (hasEventData) {
    const dataValue = currentEvent.dataLines.join("\n");
    parsedEvent.data = parseDebugJSONText(dataValue) || dataValue;
  }
  parsedEvents.push(parsedEvent);
}

function createDebugJSONStringPreview(value) {
  const singleLineValue = value.replace(/\s+/g, " ").trim();
  if (singleLineValue.length <= 160) return singleLineValue;
  return `${singleLineValue.slice(0, 157)}...`;
}

function countDebugJSONNodes(value) {
  if (typeof value === "string") {
    const parsedStringPayload = parseDebugJSONString(value);
    return parsedStringPayload ? 1 + countDebugJSONNodes(parsedStringPayload.value) : 1;
  }
  if (!value || typeof value !== "object") return 1;
  const childValues = Array.isArray(value) ? value : Object.values(value);
  return 1 + childValues.reduce((nodeCount, childValue) => nodeCount + countDebugJSONNodes(childValue), 0);
}

export function renderCreateKeyModal() {
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Create New Key" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Create New Key</h3>
          <p>Create a client key for the current user. The raw key will be shown once.</p>
          <form id="create-key-form" class="form-stack" style="margin-top: 24px;">
            <div class="field">
              <label for="key-name">Key Name</label>
              <input id="key-name" name="name" class="input" placeholder="Production Backend" required>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" type="submit"><span class="material-symbols-outlined">add</span><span>Create</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}

export function renderKeyCreatedModal(modal) {
  const copyFailed = Boolean(modal.copyFailed);
  const copySucceeded = Boolean(modal.copySucceeded);
  const copyNote = copyFailed
    ? "浏览器拒绝自动复制。密钥已选中，请按 Ctrl+C 手动复制。"
    : copySucceeded
      ? "密钥已复制到剪贴板。此密钥只显示一次，请立即保存。"
      : "此密钥只显示一次，可以点击复制按钮或直接选中文本复制。";
  return `
    <div class="modal-backdrop">
      <section class="modal" role="dialog" aria-modal="true" aria-label="New API Key Created" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>New API Key Created</h3>
          <p>Your new key '${escapeHTML(modal.key.name)}' is ready to use.</p>
          <div class="warning-box">
            <span class="material-symbols-outlined">warning</span>
            <div>
              <strong>Save this key now.</strong>
              <p>For your security, it will only be shown once. If you lose it, you will need to generate a new key.</p>
            </div>
          </div>
          <div class="key-copy">
            <label class="field-label" for="created-api-key">Secret Key</label>
            <div class="copy-shell ${copyFailed ? "manual" : ""}">
              <input id="created-api-key" class="input mono subtle" value="${escapeAttr(modal.apiKey)}" readonly>
              <button class="mini-icon" data-action="copy-created-key" title="Copy" type="button"><span class="material-symbols-outlined">content_copy</span></button>
            </div>
            <p class="hint ${copyFailed ? "manual-copy-note" : copySucceeded ? "auto-copy-note" : ""}">${copyNote}</p>
          </div>
          <div class="modal-actions">
            <button class="button secondary" data-action="close-modal" type="button">I've Saved It</button>
            <button class="button" data-action="copy-created-key" type="button"><span class="material-symbols-outlined">content_copy</span><span>Copy Key</span></button>
          </div>
        </div>
      </section>
    </div>`;
}

export function renderEditKeyModal(key) {
  if (!key) return "";
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Edit Key" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Edit API Key</h3>
          <p>Update the key label or disable access immediately.</p>
          <form id="edit-key-form" class="form-stack" style="margin-top: 24px;">
            <input type="hidden" name="id" value="${escapeAttr(key.id)}">
            <div class="field">
              <label for="edit-key-name">Name</label>
              <input id="edit-key-name" name="name" class="input" value="${escapeAttr(key.name || "")}">
            </div>
            <div class="field-row">
              <span>
                <strong>Enabled</strong>
                <span class="hint" style="display: block;">Disabled keys cannot call /mcp.</span>
              </span>
              <label class="toggle">
                <input type="checkbox" name="enabled" ${key.enabled ? "checked" : ""}>
                <span></span>
              </label>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" data-action="submit-edit-key" type="button"><span class="material-symbols-outlined">save</span><span>Save</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}

export function renderCreateInviteCodeModal() {
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Create Invite Code" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Create Invite Code</h3>
          <p>Generate a one-time-visible registration invite code with a fixed registration limit.</p>
          <form id="create-invite-code-form" class="form-stack" style="margin-top: 24px;">
            <div class="field">
              <label for="create-invite-code-limit">Registration Limit</label>
              <input id="create-invite-code-limit" name="registration_limit" class="input mono" type="number" min="1" step="1" value="1" required>
              <span class="hint">每个邀请码最多可成功注册的账号数量。邀请码注册模式未启用时，该邀请码不会影响普通注册。</span>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" type="submit"><span class="material-symbols-outlined">add</span><span>Create</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}

export function renderEditInviteCodeModal(inviteCode) {
  if (!inviteCode) return "";
  const remainingRegistrations = Math.max(0, Number(inviteCode.registration_limit || 0) - Number(inviteCode.registration_count || 0));
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Edit Invite Code" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Edit Invite Code</h3>
          <p>Manage invite code <span class="mono">${escapeHTML(inviteCode.code_prefix || inviteCode.id || "unknown")}</span>.</p>
          <form id="edit-invite-code-form" class="form-stack" style="margin-top: 24px;">
            <input type="hidden" name="id" value="${escapeAttr(inviteCode.id)}">
            <div class="field">
              <label for="edit-invite-code-limit">Registration Limit</label>
              <input id="edit-invite-code-limit" name="registration_limit" class="input mono" type="number" min="${Number(inviteCode.registration_count) || 0}" step="1" value="${Number(inviteCode.registration_limit) || 1}" required>
              <span class="hint">已使用 ${formatNumber(inviteCode.registration_count || 0)} 次，剩余 ${formatNumber(remainingRegistrations)} 次；上限不能低于已使用次数。</span>
            </div>
            <div class="field-row">
              <span>
                <strong>Enabled</strong>
                <span class="hint" style="display: block;">Disabled invite codes cannot be used while invite-code registration mode is enabled.</span>
              </span>
              <label class="toggle">
                <input type="checkbox" name="enabled" ${inviteCode.enabled ? "checked" : ""}>
                <span></span>
              </label>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" data-action="submit-edit-invite-code" type="button"><span class="material-symbols-outlined">save</span><span>Save</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}

export function renderEditUserModal(user) {
  if (!user) return "";
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Edit User" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Edit User</h3>
          <p>${escapeHTML(user.username)} access and tier assignment.</p>
          <form id="edit-user-form" class="form-stack" style="margin-top: 24px;">
            <input type="hidden" name="id" value="${escapeAttr(user.id)}">
            <div class="field-row">
              <span>
                <strong>Enabled</strong>
                <span class="hint" style="display: block;">Disabled users cannot log in or use keys.</span>
              </span>
              <label class="toggle"><input type="checkbox" name="enabled" ${user.enabled ? "checked" : ""}><span></span></label>
            </div>
            <div class="field">
              <label for="edit-user-role">Role</label>
              <select id="edit-user-role" name="role" class="select">
                <option value="user" ${user.role === "user" ? "selected" : ""}>user</option>
                <option value="admin" ${user.role === "admin" ? "selected" : ""}>admin</option>
              </select>
            </div>
            <div class="field-row">
              <span>
                <strong>Revoke Tokens</strong>
                <span class="hint" style="display: block;">强制该用户所有已签发的登录令牌立即失效（强制下线）。</span>
              </span>
              <label class="toggle"><input type="checkbox" name="revoke_tokens"><span></span></label>
            </div>
            <div class="field">
              <label for="edit-user-tier">Tier</label>
              <select id="edit-user-tier" name="tier_id" class="select">
                ${tierOptions(user.tier_id || "")}
              </select>
              <span class="hint">必须选择 tier；限额（RPM / success limit）完全由 tier 决定，用户不再保留独立限额。调整 tier 预设请到 Tier Management 页。</span>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" data-action="submit-edit-user" type="button"><span class="material-symbols-outlined">save</span><span>Save</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}

export function renderUserUsageModal(user, usage) {
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="User Usage" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>User Usage</h3>
          <p>${escapeHTML(user.username)} aggregate usage.</p>
          <div class="grid metric-grid" style="grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 24px 0;">
            ${metricCard("Total Calls", formatNumber(usage.total_calls), "data_usage", "All user keys", "good", null)}
            ${metricCard("Success Calls", formatNumber(usage.success_calls), "check_circle", `${successPercent(usage)} success`, "good", null)}
          </div>
          ${renderRecentActivity(usage.records || [], true, {
            viewAllAction: "view-user-usage-logs",
            viewAllRoute: "",
            viewAllDataset: { userId: user.id },
            showRequestIdColumn: false,
            showLatencyColumn: false,
            compactTable: true
          })}
        </div>
      </section>
    </div>`;
}

export function renderUserUsageLogsModal(user, usage) {
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal usage-logs-modal" role="dialog" aria-modal="true" aria-label="User Usage Logs" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>User Usage Logs</h3>
          <p>${escapeHTML(user.username)} complete usage log view.</p>
          <div class="grid metric-grid" style="grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 24px 0;">
            ${metricCard("Total Calls", formatNumber(usage.total_calls), "data_usage", "All user keys", "good", null)}
            ${metricCard("Success Calls", formatNumber(usage.success_calls), "check_circle", `${successPercent(usage)} success`, "good", null)}
            ${metricCard("Failed Calls", formatNumber(Math.max(0, usage.total_calls - usage.success_calls)), "error", "Not counted as success quota", usage.total_calls === usage.success_calls ? "good" : "bad", null)}
          </div>
          ${renderRecentActivity(usage.records || [], false, {
            showViewAllButton: false,
            showPagination: true,
            page: state.usageActivityPage,
            pageSize: state.usageActivityPageSize,
            pageSizeOptions: [10, 20, 50, 100]
          })}
        </div>
      </section>
    </div>`;
}

export function renderDeleteConfirmModal(modal) {
  const title = modal.title || "Confirm Delete";
  const message = modal.message || "Are you sure you want to delete this item?";
  const detail = modal.detail || "This action cannot be undone.";
  const confirmLabel = modal.confirmLabel || "Delete";
  const confirmAction = modal.confirmAction || "close-modal";
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal confirm-modal" role="alertdialog" aria-modal="true" aria-label="${escapeAttr(title)}" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <div class="confirm-icon danger"><span class="material-symbols-outlined">delete</span></div>
          <h3>${escapeHTML(title)}</h3>
          <p>${escapeHTML(message)}</p>
          <div class="warning-box compact">
            <span class="material-symbols-outlined">warning</span>
            <div>
              <strong>Dangerous action</strong>
              <p>${escapeHTML(detail)}</p>
            </div>
          </div>
          <div class="modal-actions">
            <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
            <button class="button danger" data-action="${escapeAttr(confirmAction)}" data-target-id="${escapeAttr(modal.targetId || "")}" type="button"><span class="material-symbols-outlined">delete</span><span>${escapeHTML(confirmLabel)}</span></button>
          </div>
        </div>
      </section>
    </div>`;
}

export function renderCreateTierModal() {
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Create Tier" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Create Tier</h3>
          <p>新建一个等级预设。</p>
          <form id="create-tier-form" class="form-stack" style="margin-top: 24px;">
            <div class="field">
              <label for="create-tier-name">Name</label>
              <input id="create-tier-name" name="name" class="input" placeholder="tier7" required>
            </div>
            <div class="field">
              <label for="create-tier-level">Level</label>
              <input id="create-tier-level" name="level" class="input mono" type="number" min="0" value="0">
            </div>
            <div class="field">
              <label for="create-tier-rpm">RPM</label>
              <input id="create-tier-rpm" name="rpm" class="input mono" type="number" min="0" value="0">
              <span class="hint">0 means unlimited RPM.</span>
            </div>
            <div class="field">
              <label for="create-tier-success">Success Limit</label>
              <input id="create-tier-success" name="success_limit" class="input mono" type="number" min="0" value="0">
              <span class="hint">0 means unlimited.</span>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" type="submit"><span class="material-symbols-outlined">add</span><span>Create</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}

export function renderEditTierModal(tier) {
  if (!tier) return "";
  return `
    <div class="modal-backdrop" data-action="close-modal">
      <section class="modal" role="dialog" aria-modal="true" aria-label="Edit Tier" data-modal>
        <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
        <div class="modal-body">
          <h3>Edit Tier</h3>
          <p>${escapeHTML(tier.name)} preset values.</p>
          <form id="edit-tier-form" class="form-stack" style="margin-top: 24px;">
            <input type="hidden" name="id" value="${escapeAttr(tier.id)}">
            <div class="field">
              <label for="edit-tier-name">Name</label>
              <input id="edit-tier-name" name="name" class="input" value="${escapeAttr(tier.name || "")}" required>
            </div>
            <div class="field">
              <label for="edit-tier-level">Level</label>
              <input id="edit-tier-level" name="level" class="input mono" type="number" min="0" value="${Number(tier.level) || 0}">
            </div>
            <div class="field">
              <label for="edit-tier-rpm">RPM</label>
              <input id="edit-tier-rpm" name="rpm" class="input mono" type="number" min="0" value="${Number(tier.rpm) || 0}">
            </div>
            <div class="field">
              <label for="edit-tier-success">Success Limit</label>
              <input id="edit-tier-success" name="success_limit" class="input mono" type="number" min="0" value="${Number(tier.success_limit) || 0}">
              <span class="hint">0 means unlimited.</span>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
              <button class="button" type="submit"><span class="material-symbols-outlined">save</span><span>Save</span></button>
            </div>
          </form>
        </div>
      </section>
    </div>`;
}
