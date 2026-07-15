import { escapeHTML, formatNumber, formatPercent, getSuccessRate } from "../utils.js";
import { renderPageHeading } from "../components/loading.js";
import { renderMetricCard } from "../components/metric-card.js";
import { renderToolBreakdown } from "../components/tool-breakdown.js";
import { renderChart } from "../components/usage-chart.js";
import { renderUsageRecords } from "../components/usage-records.js";
import { renderCollectionPagination } from "../components/pagination.js";

export function renderUsagePage(state) {
  const period = state.filters.usagePeriod;
  const periodFilters = `
    <div class="filter-pills" aria-label="时间范围">
      ${[["24h", "24 小时"], ["7d", "7 天"], ["30d", "30 天"], ["all", "全部"]].map(([value, label]) => `
        <button class="filter-pill ${period === value ? "is-active" : ""}" type="button" data-action="set-usage-period" data-period="${value}">${label}</button>
      `).join("")}
    </div>
  `;

  if (state.pageLoading && !state.data.usage) {
    return `${renderPageHeading("调用分析", "按时间范围查看请求趋势、工具分布和最近调用记录。", periodFilters)}${renderUsageLoading()}`;
  }

  const usage = state.data.usage || createEmptyUsage();
  const successRate = getSuccessRate(usage);
  return `
    ${renderPageHeading("调用分析", "按时间范围查看请求趋势、工具分布和最近调用记录。", periodFilters)}
    <section class="metric-grid">
      ${renderMetricCard("总调用", formatNumber(usage.total_calls), getPeriodLabel(period), "activity", "#eeeaff", "#7667f4", false, "trend", usage.traffic_buckets)}
      ${renderMetricCard("成功调用", formatNumber(usage.success_calls), formatPercent(successRate), "shield", "#e8f8ef", "#238a54", successRate >= 95, "ring", successRate)}
      ${renderMetricCard("当前 RPM", formatNumber(usage.current_rpm), "最近一分钟", "chart", "#e8f1ff", "#3d83f6", false, "pulse", usage.current_rpm)}
      ${renderMetricCard("工具种类", formatNumber(Object.keys(usage.by_tool || {}).length), "已调用工具", "model", "#fff6e5", "#d58a19", false, "nodes", Object.keys(usage.by_tool || {}).length)}
    </section>

    <section class="dashboard-grid" style="margin-bottom:18px">
      <article class="content-card"><header class="card-header"><div><h2>调用趋势</h2><p>${escapeHTML(getPeriodLabel(period))}</p></div></header><div class="card-body chart-wrap">${renderChart(usage.traffic_buckets)}</div></article>
      <article class="content-card"><header class="card-header"><div><h2>工具分布</h2><p>调用量占比</p></div></header><div class="card-body">${renderToolBreakdown(usage.by_tool)}</div></article>
    </section>

    <article class="data-card">
      <header class="card-header"><div><h2>最近调用</h2><p>请求结果与耗时明细</p></div></header>
      ${renderUsageRecords(usage.records)}
      ${renderCollectionPagination("usageRecords", state.pagination.usageRecords, usage.records.length)}
    </article>
  `;
}

function renderUsageLoading() {
  return `<section class="metric-grid">${Array.from({ length: 4 }, () => '<div class="skeleton" style="height:142px;border-radius:16px"></div>').join("")}</section><div class="skeleton" style="height:360px;border-radius:16px"></div>`;
}

function createEmptyUsage() {
  return { total_calls: 0, success_calls: 0, current_rpm: 0, by_tool: {}, traffic_buckets: [], records: [] };
}

function getPeriodLabel(period) {
  const labels = { "24h": "最近 24 小时", "7d": "最近 7 天", "30d": "最近 30 天", all: "全部时间" };
  return labels[period] || labels["24h"];
}
