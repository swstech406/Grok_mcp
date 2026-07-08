import { metricCard, renderBars, renderRecentActivity, renderToolUsage } from "../components/metric-card.js";
import { filteredRecords, state } from "../state.js";
import { escapeAttr, escapeHTML, formatNumber, rangeLabel, successPercent } from "../utils.js";

const USAGE_RANGE_OPTIONS = [
  { value: "24h", shortLabel: "24H", label: "Last 24 Hours" },
  { value: "7d", shortLabel: "7D", label: "Last 7 Days" },
  { value: "all", shortLabel: "All", label: "All Time" }
];

export function renderUsage() {
  const usage = state.usage;
  return `
    <div class="page-head usage-page-head">
      <div>
        <h2>Usage Stats</h2>
        <p>Review MCP tool calls, latency and success counters.</p>
      </div>
      ${renderUsageFilters()}
    </div>
    <section class="grid metric-grid">
      ${metricCard("Total Calls", formatNumber(usage.total_calls), "data_usage", "Selected range", "good", null)}
      ${metricCard("Success Calls", formatNumber(usage.success_calls), "check_circle", `${successPercent(usage)} success`, "good", null)}
      ${metricCard("Failed Calls", formatNumber(Math.max(0, usage.total_calls - usage.success_calls)), "error", "Not counted as success quota", usage.total_calls === usage.success_calls ? "good" : "bad", null)}
      ${metricCard("Active Keys", formatNumber(state.keys.filter((key) => key.enabled).length), "vpn_key", `${state.keys.length} total keys`, "good", null)}
    </section>
    <section class="grid viz-grid">
      <div class="card panel">
        <div class="panel-head">
          <h3>Traffic Volume</h3>
          <span class="mono muted">${escapeHTML(rangeLabel(state.sinceMode))}</span>
        </div>
        ${renderBars(usage.records, state.sinceMode)}
      </div>
      ${renderToolUsage(usage)}
    </section>
    ${renderRecentActivity(filteredRecords(usage.records), state.usageActivityCompact, {
      showViewAllButton: state.usageActivityCompact,
      viewAllAction: "expand-usage-activity",
      viewAllRoute: "",
      viewAllLabel: "View All Activity"
    })}`;
}

function renderUsageFilters() {
  return `
      <div class="usage-filters" aria-label="Usage filters">
        ${renderUsageKeyPicker()}
        ${renderUsageRangeTabs()}
        <div class="usage-filter-actions">
          <button class="usage-refresh-button" data-action="refresh" type="button" aria-label="Refresh usage stats" title="Refresh usage stats">
            <span class="material-symbols-outlined">refresh</span>
            <span>Refresh</span>
          </button>
        </div>
      </div>`;
}

function renderUsageKeyPicker() {
  const selectedUsageKey = findSelectedUsageKey();
  const selectedUsageKeySummary = selectedUsageKey ? usageKeyDisplayName(selectedUsageKey) : "All Keys";
  const selectedUsageKeyMeta = renderUsageKeyMeta(selectedUsageKey);
  const usageKeyDescriptionAttribute = selectedUsageKeyMeta ? ` aria-describedby="usage-key-summary"` : "";

  return `
        <div class="usage-filter-card usage-key-card">
          <div class="usage-filter-head">
            <span class="usage-filter-label">API Key</span>
            <span class="usage-filter-summary" title="${escapeAttr(selectedUsageKeySummary)}">${escapeHTML(selectedUsageKeySummary)}</span>
          </div>
          <select class="select usage-key-select" id="usage-key-select" aria-label="Choose API key for usage stats"${usageKeyDescriptionAttribute}>
            ${renderUsageKeyOptions()}
          </select>
          ${selectedUsageKeyMeta ? `<div class="usage-filter-meta" id="usage-key-summary">${selectedUsageKeyMeta}</div>` : ""}
        </div>`;
}

function renderUsageKeyOptions() {
  const totalKeysCount = state.keys.length;
  const allKeysSelected = state.selectedKeyID === "all";
  const keyOptions = state.keys.map((key) => {
    const keySelected = state.selectedKeyID === key.id;
    return `<option value="${escapeAttr(key.id)}" ${keySelected ? "selected" : ""}>${escapeHTML(usageKeyOptionLabel(key))}</option>`;
  });

  return [
    `<option value="all" ${allKeysSelected ? "selected" : ""}>All Keys (${formatNumber(totalKeysCount)})</option>`,
    ...keyOptions
  ].join("");
}

function renderUsageKeyMeta(selectedUsageKey) {
  if (!selectedUsageKey) {
    return "";
  }

  const keyStatusClass = selectedUsageKey.enabled ? "enabled" : "disabled";
  const keyStatusLabel = selectedUsageKey.enabled ? "Enabled" : "Disabled";
  const keyPrefixLabel = selectedUsageKey.key_prefix || "No prefix";

  return `
              <span class="usage-key-status ${keyStatusClass}">${keyStatusLabel}</span>
              <span class="mono" title="${escapeAttr(keyPrefixLabel)}">${escapeHTML(keyPrefixLabel)}</span>
              <span>${formatNumber(selectedUsageKey.total_calls)} total calls</span>`;
}

function renderUsageRangeTabs() {
  return `
        <div class="usage-filter-card usage-range-card">
          <div class="usage-filter-head">
            <span class="usage-filter-label">Time Range</span>
            <span class="usage-filter-summary">${escapeHTML(rangeLabel(state.sinceMode))}</span>
          </div>
          <div class="usage-range-tabs" role="group" aria-label="Choose usage time range">
            ${USAGE_RANGE_OPTIONS.map(renderUsageRangeOption).join("")}
          </div>
        </div>`;
}

function renderUsageRangeOption(rangeOption) {
  const rangeSelected = state.sinceMode === rangeOption.value;
  return `
            <button class="usage-range-option ${rangeSelected ? "active" : ""}" data-action="usage-range" data-range="${escapeAttr(rangeOption.value)}" type="button" aria-pressed="${rangeSelected ? "true" : "false"}">
              <span>${escapeHTML(rangeOption.shortLabel)}</span>
              <small>${escapeHTML(rangeOption.label)}</small>
            </button>`;
}

function findSelectedUsageKey() {
  if (state.selectedKeyID === "all") {
    return null;
  }
  return state.keys.find((key) => key.id === state.selectedKeyID) || null;
}

function usageKeyOptionLabel(key) {
  const displayName = usageKeyDisplayName(key);
  const keyPrefix = key.key_prefix && key.key_prefix !== displayName ? ` - ${key.key_prefix}` : "";
  const disabledSuffix = key.enabled ? "" : " - Disabled";
  return `${displayName}${keyPrefix}${disabledSuffix}`;
}

function usageKeyDisplayName(key) {
  return String(key.name || key.key_prefix || key.id || "Unnamed Key").trim();
}
