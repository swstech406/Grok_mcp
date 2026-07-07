import { filteredRecords } from "../state.js";
import { bucketRecords, clamp, escapeAttr, escapeHTML, formatNumber, percentOf, relativeTime, trimToolName } from "../utils.js";

export function metricCard(title, value, icon, note, tone, progress) {
  return `
    <div class="card metric-card">
      <div class="metric-top">
        <span class="metric-title">${title}</span>
        <span class="material-symbols-outlined muted">${icon}</span>
      </div>
      <div>
        <div class="metric-value">${value}</div>
        ${progress === null ? "" : `<div class="progress" style="margin-top: 16px;"><div class="progress-bar" style="width: ${clamp(progress, 0, 100)}%;"></div></div>`}
        <div class="metric-note ${tone || ""}">
          <span class="material-symbols-outlined" style="font-size: 16px;">${tone === "bad" ? "trending_up" : "trending_flat"}</span>
          <span>${escapeHTML(note || "Stable")}</span>
        </div>
      </div>
    </div>`;
}

export function renderDashboardAlert(alert) {
  if (!alert) return "";
  return `
    <section class="alert">
      <span class="material-symbols-outlined">warning</span>
      <div>
        <h3>${escapeHTML(alert.title)}</h3>
        <p>${escapeHTML(alert.body)}</p>
      </div>
      <button class="button secondary" data-action="go" data-route="account" type="button">Review Quotas</button>
    </section>`;
}

export function renderBars(records) {
  const buckets = bucketRecords(records || []);
  const max = Math.max(1, ...buckets);
  return `
    <div class="bar-chart" aria-label="流量柱状图">
      ${buckets.map((value) => `<div class="bar" title="${value} calls" style="height: ${Math.max(8, Math.round((value / max) * 92))}%;"></div>`).join("")}
    </div>
    <div class="chart-axis"><span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>Now</span></div>`;
}

export function renderToolUsage(usage) {
  const rows = Object.entries(usage.by_tool || {}).sort((a, b) => b[1] - a[1]);
  const total = rows.reduce((sum, row) => sum + row[1], 0);
  const top = rows[0] || ["No Data", 0];
  const pct = total ? Math.round((top[1] / total) * 100) : 0;
  const rest = Math.max(0, 100 - pct);
  // When only two tools exist, show the second tool's real name instead of "Other Tools".
  const second = rows[1];
  const secondLabel = rows.length > 2 ? "Other Tools" : (second ? second[0] : null);
  return `
    <div class="card panel donut-wrap">
      <div class="panel-head">
        <h3>Tool Usage</h3>
      </div>
      <div class="donut" style="--donut-value: ${pct}%">
        <div class="donut-inner">
          <div>
            <strong>${pct}%</strong>
            <span>${escapeHTML(trimToolName(top[0]))}</span>
          </div>
        </div>
      </div>
      <div class="legend">
        <div class="legend-row">
          <span class="legend-name"><span class="dot"></span>${escapeHTML(top[0])}</span>
          <span class="mono">${pct}%</span>
        </div>
        ${secondLabel ? `
        <div class="legend-row">
          <span class="legend-name"><span class="dot light"></span>${escapeHTML(secondLabel)}</span>
          <span class="mono">${rest}%</span>
        </div>` : ""}
      </div>
    </div>`;
}

export function renderRecentActivity(records, compact, options = {}) {
  const rows = filteredRecords(records || []).slice(0, compact ? 5 : 500);
  const showViewAllButton = options.showViewAllButton ?? compact;
  const viewAllAction = options.viewAllAction || "go";
  const viewAllRoute = options.viewAllRoute || "usage";
  const viewAllLabel = options.viewAllLabel || "View All Logs";
  const viewAllDataset = options.viewAllDataset || {};
  const viewAllAttributes = [
    `data-action="${escapeAttr(viewAllAction)}"`,
    viewAllRoute ? `data-route="${escapeAttr(viewAllRoute)}"` : "",
    ...Object.entries(viewAllDataset).map(([attributeName, attributeValue]) => {
      const dataAttributeName = String(attributeName).replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`);
      return `data-${escapeAttr(dataAttributeName)}="${escapeAttr(attributeValue)}"`;
    })
  ].filter(Boolean).join(" ");
  return `
    <section class="card table-card">
      <div class="table-head">
        <h3>Recent Activity</h3>
        ${showViewAllButton ? `<button class="button ghost small" ${viewAllAttributes} type="button">${escapeHTML(viewAllLabel)}</button>` : ""}
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>TOOL NAME</th>
              <th>REQUEST ID</th>
              <th>TIMESTAMP</th>
              <th>LATENCY</th>
              <th class="right">STATUS</th>
            </tr>
          </thead>
          <tbody>
            ${rows.length ? rows.map(renderActivityRow).join("") : renderEmptyRow("receipt_long", "No usage records", "MCP tools/call activity will appear here.")}
          </tbody>
        </table>
      </div>
    </section>`;
}

export function renderActivityRow(record) {
  return `
    <tr>
      <td class="mono" style="color: var(--primary);">${escapeHTML(record.tool_name || "unknown")}</td>
      <td class="muted">${escapeHTML(`req_${String(record.id || "").padStart(8, "0").slice(-8)}`)}</td>
      <td>${relativeTime(record.timestamp)}</td>
      <td>${record.duration_ms ? `${formatNumber(record.duration_ms)}ms` : "--"}</td>
      <td class="right"><span class="badge ${record.success ? "" : "error"}">${record.success ? "Success" : "Failed"}</span></td>
    </tr>`;
}

export function renderEmptyRow(icon, title, text) {
  return `
    <tr>
      <td colspan="8">
        <div class="empty">
          <div>
            <span class="material-symbols-outlined">${icon}</span>
            <h3>${escapeHTML(title)}</h3>
            <p>${escapeHTML(text)}</p>
          </div>
        </div>
      </td>
    </tr>`;
}

export function quotaProgress(label, used, limit, note) {
  const pct = percentOf(used, limit);
  return `
    <div class="quota-item">
      <div class="field-row">
        <span class="field-label">${escapeHTML(label)}</span>
        <span class="mono">${formatNumber(used)}${limit ? ` / ${formatNumber(limit)}` : " / unlimited"}</span>
      </div>
      <div class="progress"><div class="progress-bar" style="width: ${limit ? clamp(pct, 0, 100) : 100}%;"></div></div>
      <span class="hint">${escapeHTML(note)}</span>
    </div>`;
}

export function summaryItem(label, value) {
  return `<div class="summary-item"><span class="summary-label">${escapeHTML(label)}</span><span>${value}</span></div>`;
}
