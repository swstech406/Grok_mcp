import { metricCard, renderBars, renderRecentActivity, renderToolUsage } from "../components/metric-card.js";
import { filteredRecords, isAdmin, state } from "../state.js";
import { escapeAttr, escapeHTML, formatNumber, rangeLabel, successPercent } from "../utils.js";

export function renderUsage() {
  const usage = state.usage;
  const selectedUsageUser = state.users.find((user) => user.id === state.selectedUsageUserID);
  const selectedUsageUsername = selectedUsageUser ? selectedUsageUser.username : "Selected user";
  const isViewingAdminUserUsage = Boolean(state.selectedUsageUserID);
  return `
    <div class="page-head">
      <div>
        <h2>${isViewingAdminUserUsage ? `${escapeHTML(selectedUsageUsername)} Usage Logs` : "Usage Stats"}</h2>
        <p>${isViewingAdminUserUsage ? "Review this user's MCP tool calls, latency and success counters." : "Review MCP tool calls, latency and success counters."}</p>
      </div>
      <div class="toolbar">
        ${isAdmin() ? renderUsageUserSelect() : ""}
        ${isViewingAdminUserUsage ? "" : `<select class="select" id="usage-key-select" aria-label="选择 Key">
          <option value="all" ${state.selectedKeyID === "all" ? "selected" : ""}>All Keys</option>
          ${state.keys.map((key) => `<option value="${escapeAttr(key.id)}" ${state.selectedKeyID === key.id ? "selected" : ""}>${escapeHTML(key.name || key.key_prefix)}</option>`).join("")}
        </select>`}
        <select class="select" id="usage-since-select" aria-label="选择时间范围">
          <option value="24h" ${state.sinceMode === "24h" ? "selected" : ""}>Last 24 Hours</option>
          <option value="7d" ${state.sinceMode === "7d" ? "selected" : ""}>Last 7 Days</option>
          <option value="all" ${state.sinceMode === "all" ? "selected" : ""}>All Time</option>
        </select>
        <button class="button secondary" data-action="refresh" type="button"><span class="material-symbols-outlined">refresh</span><span>Refresh</span></button>
      </div>
    </div>
    <section class="grid metric-grid">
      ${metricCard("Total Calls", formatNumber(usage.total_calls), "data_usage", "Selected range", "good", null)}
      ${metricCard("Success Calls", formatNumber(usage.success_calls), "check_circle", `${successPercent(usage)} success`, "good", null)}
      ${metricCard("Failed Calls", formatNumber(Math.max(0, usage.total_calls - usage.success_calls)), "error", "Not counted as success quota", usage.total_calls === usage.success_calls ? "good" : "bad", null)}
      ${isViewingAdminUserUsage
        ? metricCard("User", escapeHTML(selectedUsageUsername), "person", "All user keys", "good", null)
        : metricCard("Active Keys", formatNumber(state.keys.filter((key) => key.enabled).length), "vpn_key", `${state.keys.length} total keys`, "good", null)}
    </section>
    <section class="grid viz-grid">
      <div class="card panel">
        <div class="panel-head">
          <h3>Traffic Volume</h3>
          <span class="mono muted">${escapeHTML(rangeLabel(state.sinceMode))}</span>
        </div>
        ${renderBars(usage.records)}
      </div>
      ${renderToolUsage(usage)}
    </section>
    ${renderRecentActivity(filteredRecords(usage.records), false)}`;
}

export function renderUsageUserSelect() {
  return `
    <select class="select" id="usage-user-select" aria-label="选择用户">
      <option value="" ${state.selectedUsageUserID ? "" : "selected"}>My Usage</option>
      ${state.users.map((user) => `<option value="${escapeAttr(user.id)}" ${state.selectedUsageUserID === user.id ? "selected" : ""}>${escapeHTML(user.username)}</option>`).join("")}
    </select>`;
}
