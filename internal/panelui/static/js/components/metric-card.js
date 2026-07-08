import { filteredRecords } from "../state.js";
import { bucketRecords, clamp, escapeAttr, escapeHTML, formatNumber, percentOf, relativeTime, trimToolName } from "../utils.js";

const DEFAULT_ACTIVITY_PAGE_SIZE_OPTIONS = [10, 20, 50, 100];

export function metricCard(title, value, icon, note, tone, progress, options = {}) {
  const shouldReserveProgressSpace = options.reserveProgressSpace === true;
  const trailingNoteMarkup = options.trailingNote
    ? `<span class="metric-note-trailing">${escapeHTML(options.trailingNote)}</span>`
    : "";
  const progressMarkup = progress === null
    ? (shouldReserveProgressSpace ? `<div class="progress metric-progress-placeholder" style="margin-top: 16px;" aria-hidden="true"></div>` : "")
    : `<div class="progress" style="margin-top: 16px;"><div class="progress-bar" style="width: ${clamp(progress, 0, 100)}%;"></div></div>`;

  return `
    <div class="card metric-card">
      <div class="metric-top">
        <span class="metric-title">${title}</span>
        <span class="material-symbols-outlined muted">${icon}</span>
      </div>
      <div>
        <div class="metric-value">${value}</div>
        ${progressMarkup}
        <div class="metric-note ${tone || ""}">
          <span class="material-symbols-outlined" style="font-size: 16px;">${tone === "bad" ? "trending_up" : "trending_flat"}</span>
          <span>${escapeHTML(note || "Stable")}</span>
          ${trailingNoteMarkup}
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

export function renderBars(records, mode = "24h") {
  const buckets = bucketRecords(records || [], mode);
  const maximumBucketValue = Math.max(0, ...buckets);
  const verticalAxisValues = createChartAxisValues(maximumBucketValue);
  const verticalAxisMaximum = verticalAxisValues[0] || 1;
  const horizontalAxisLabels = createChartTimeLabels(mode);

  return `
    <div class="bar-chart-shell" role="img" aria-label="流量柱状图">
      <div class="chart-y-axis" aria-hidden="true">
        ${verticalAxisValues.map((value) => `<span>${formatNumber(value)}</span>`).join("")}
      </div>
      <div class="chart-body">
        <div class="bar-chart">
          ${buckets.map((value) => `<div class="bar" title="${formatNumber(value)} calls" style="height: ${createBarHeightPercent(value, verticalAxisMaximum)}%;"></div>`).join("")}
        </div>
        <div class="chart-axis">${horizontalAxisLabels.map((label) => `<span>${escapeHTML(label)}</span>`).join("")}</div>
      </div>
    </div>`;
}

function createChartAxisValues(maximumBucketValue) {
  const safeMaximumValue = Math.max(1, Number(maximumBucketValue) || 0);
  const tickStep = Math.max(1, Math.ceil(safeMaximumValue / 4));
  const roundedMaximumValue = tickStep * 4;
  return Array.from({ length: 5 }, (_, tickIndex) => roundedMaximumValue - tickStep * tickIndex);
}

function createBarHeightPercent(value, maximumValue) {
  const safeValue = Number(value) || 0;
  if (safeValue <= 0) {
    return 0;
  }
  return Math.max(8, Math.round((safeValue / Math.max(1, maximumValue)) * 92));
}

function createChartTimeLabels(mode) {
  if (mode === "7d") {
    return ["7d ago", "5d ago", "3d ago", "1d ago", "Now"];
  }
  if (mode === "all") {
    return ["Oldest", "25%", "50%", "75%", "Now"];
  }
  return ["24h ago", "18h ago", "12h ago", "6h ago", "Now"];
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
  const allRows = filteredRecords(records || []);
  const showPagination = Boolean(options.showPagination) && !compact;
  const pageSizeOptions = normalizeActivityPageSizeOptions(options.pageSizeOptions);
  const pageSize = normalizeActivityPageSize(options.pageSize, pageSizeOptions);
  const totalRows = allRows.length;
  const totalPages = Math.max(1, Math.ceil(totalRows / pageSize));
  const currentPage = normalizeActivityPage(options.page, totalPages);
  const pageStartIndex = showPagination ? (currentPage - 1) * pageSize : 0;
  const pageEndIndex = showPagination ? pageStartIndex + pageSize : (compact ? 5 : allRows.length);
  const rows = allRows.slice(pageStartIndex, pageEndIndex);
  const showViewAllButton = options.showViewAllButton ?? compact;
  const showRequestIdColumn = options.showRequestIdColumn ?? true;
  const showLatencyColumn = options.showLatencyColumn ?? true;
  const showDebugColumn = options.showDebugColumn ?? true;
  const useCompactTableLayout = Boolean(options.compactTable);
  const visibleColumnCount = [true, showRequestIdColumn, true, showLatencyColumn, true, showDebugColumn].filter(Boolean).length;
  const viewAllAction = options.viewAllAction || "go";
  const viewAllRoute = options.viewAllRoute || "usage";
  const viewAllLabel = options.viewAllLabel || "View All Activity";
  const viewAllDataset = options.viewAllDataset || {};
  const viewAllAttributes = [
    `data-action="${escapeAttr(viewAllAction)}"`,
    viewAllRoute ? `data-route="${escapeAttr(viewAllRoute)}"` : "",
    ...Object.entries(viewAllDataset).map(([attributeName, attributeValue]) => {
      const dataAttributeName = String(attributeName).replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`);
      return `data-${escapeAttr(dataAttributeName)}="${escapeAttr(attributeValue)}"`;
    })
  ].filter(Boolean).join(" ");
  const tableCardClass = useCompactTableLayout ? "card table-card compact-activity-table" : "card table-card";
  return `
    <section class="${tableCardClass}">
      <div class="table-head">
        <h3>Recent Activity</h3>
        ${showViewAllButton ? `<button class="button ghost small" ${viewAllAttributes} type="button">${escapeHTML(viewAllLabel)}</button>` : ""}
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th class="activity-tool-column">TOOL NAME</th>
              ${showRequestIdColumn ? "<th>REQUEST ID</th>" : ""}
              <th class="activity-timestamp-column">TIMESTAMP</th>
              ${showLatencyColumn ? "<th>LATENCY</th>" : ""}
              <th class="activity-status-column right">STATUS</th>
              ${showDebugColumn ? "<th class=\"activity-debug-column right\">DEBUG</th>" : ""}
            </tr>
          </thead>
          <tbody>
            ${rows.length ? rows.map((record) => renderActivityRow(record, { showRequestIdColumn, showLatencyColumn, showDebugColumn })).join("") : renderEmptyRow("receipt_long", "No usage records", "MCP tools/call activity will appear here.", visibleColumnCount)}
          </tbody>
        </table>
      </div>
      ${showPagination ? renderActivityPagination({ totalRows, currentPage, totalPages, pageSize, pageSizeOptions, pageStartIndex, pageEndIndex }) : ""}
    </section>`;
}

function renderActivityPagination({ totalRows, currentPage, totalPages, pageSize, pageSizeOptions, pageStartIndex, pageEndIndex }) {
  const firstVisibleRowNumber = totalRows > 0 ? pageStartIndex + 1 : 0;
  const lastVisibleRowNumber = Math.min(totalRows, pageEndIndex);
  const previousPage = Math.max(1, currentPage - 1);
  const nextPage = Math.min(totalPages, currentPage + 1);
  const onFirstPage = currentPage <= 1;
  const onLastPage = currentPage >= totalPages;

  return `
      <div class="activity-pagination" aria-label="Recent Activity pagination">
        <div class="activity-pagination-summary">
          <span>Showing <strong>${formatNumber(firstVisibleRowNumber)}-${formatNumber(lastVisibleRowNumber)}</strong> of <strong>${formatNumber(totalRows)}</strong></span>
        </div>
        <div class="activity-pagination-controls">
          <label class="activity-page-size-field" for="usage-activity-page-size">
            <span>Rows per page</span>
            <select class="select activity-page-size-select" id="usage-activity-page-size" aria-label="Rows per page">
              ${pageSizeOptions.map((pageSizeOption) => `<option value="${escapeAttr(pageSizeOption)}" ${pageSizeOption === pageSize ? "selected" : ""}>${formatNumber(pageSizeOption)}</option>`).join("")}
            </select>
          </label>
          <div class="activity-page-buttons" role="group" aria-label="Recent Activity page controls">
            ${renderActivityPageButton("first_page", "First page", 1, onFirstPage)}
            ${renderActivityPageButton("chevron_left", "Previous page", previousPage, onFirstPage)}
            <span class="activity-page-status mono">Page ${formatNumber(currentPage)} / ${formatNumber(totalPages)}</span>
            ${renderActivityPageButton("chevron_right", "Next page", nextPage, onLastPage)}
            ${renderActivityPageButton("last_page", "Last page", totalPages, onLastPage)}
          </div>
        </div>
      </div>`;
}

function renderActivityPageButton(icon, label, page, disabled) {
  return `
            <button class="mini-icon activity-page-button" data-action="usage-activity-page" data-page="${escapeAttr(page)}" type="button" aria-label="${escapeAttr(label)}" title="${escapeAttr(label)}" ${disabled ? "disabled" : ""}>
              <span class="material-symbols-outlined">${icon}</span>
            </button>`;
}

function normalizeActivityPageSizeOptions(pageSizeOptions) {
  const normalizedPageSizeOptions = [...new Set((pageSizeOptions || DEFAULT_ACTIVITY_PAGE_SIZE_OPTIONS)
    .map((pageSizeOption) => Number(pageSizeOption))
    .filter((pageSizeOption) => Number.isInteger(pageSizeOption) && pageSizeOption > 0))]
    .sort((firstPageSize, secondPageSize) => firstPageSize - secondPageSize);

  return normalizedPageSizeOptions.length ? normalizedPageSizeOptions : DEFAULT_ACTIVITY_PAGE_SIZE_OPTIONS;
}

function normalizeActivityPageSize(pageSize, pageSizeOptions) {
  const numericPageSize = Number(pageSize);
  return pageSizeOptions.includes(numericPageSize) ? numericPageSize : pageSizeOptions[0];
}

function normalizeActivityPage(page, totalPages) {
  const numericPage = Math.floor(Number(page) || 1);
  return Math.min(Math.max(1, numericPage), Math.max(1, totalPages));
}

export function renderActivityRow(record, options = {}) {
  const showRequestIdColumn = options.showRequestIdColumn ?? true;
  const showLatencyColumn = options.showLatencyColumn ?? true;
  const showDebugColumn = options.showDebugColumn ?? true;
  const hasDebugJSON = Boolean(record.debug_json);
  return `
    <tr>
      <td class="activity-tool-column mono" style="color: var(--primary);">${escapeHTML(record.tool_name || "unknown")}</td>
      ${showRequestIdColumn ? `<td class="muted">${escapeHTML(`req_${String(record.id || "").padStart(8, "0").slice(-8)}`)}</td>` : ""}
      <td class="activity-timestamp-column">${relativeTime(record.timestamp)}</td>
      ${showLatencyColumn ? `<td>${record.duration_ms ? `${formatNumber(record.duration_ms)}ms` : "--"}</td>` : ""}
      <td class="activity-status-column right"><span class="badge ${record.success ? "" : "error"}">${record.success ? "Success" : "Failed"}</span></td>
      ${showDebugColumn ? `<td class="activity-debug-column right">${hasDebugJSON ? `<button class="mini-icon" data-action="view-debug-json" data-record-id="${escapeAttr(record.id)}" title="View Debug JSON" type="button"><span class="material-symbols-outlined">data_object</span></button>` : `<span class="muted">--</span>`}</td>` : ""}
    </tr>`;
}

export function renderEmptyRow(icon, title, text, columnCount = 5) {
  const safeColumnCount = Math.max(1, Number(columnCount) || 5);
  return `
    <tr>
      <td colspan="${safeColumnCount}">
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
