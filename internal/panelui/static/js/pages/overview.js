import {
  calculatePercent,
  escapeHTML,
  formatLimit,
  formatNumber,
  formatPercent,
  getSuccessRate
} from "../utils.js";
import { renderIcon } from "../components/icons.js";
import { renderPageHeading } from "../components/loading.js";
import { renderMetricCard } from "../components/metric-card.js";
import { renderToolBreakdown } from "../components/tool-breakdown.js";
import { renderChart } from "../components/usage-chart.js";

export function renderOverviewPage(state) {
  if (state.pageLoading && !state.data.overviewUsage) {
    return renderOverviewLoading();
  }

  const usage = state.data.overviewUsage || createEmptyUsage();
  const keys = state.data.keys || [];
  const keyPagination = state.pagination.keys;
  const user = state.user || {};
  const successRate = getSuccessRate(usage);
  const activeKeys = Number(keyPagination.activeCount || keys.filter((apiKey) => apiKey.enabled).length);
  const totalKeys = Number(keyPagination.totalCount || keys.length);
  const successLimit = Number(user.success_limit || 0);
  const successCalls = Number(user.success_calls || 0);
  const quotaPercent = successLimit > 0 ? calculatePercent(successCalls, successLimit) : 100;
  const greeting = getGreeting();

  return `
    ${renderPageHeading("总览", "查看最近 24 小时的服务调用、成功率与账户额度。")}
    <section class="hero-card">
      <div class="hero-content">
        <div>
          <p class="hero-kicker">Realtime MCP workspace</p>
          <h2>${escapeHTML(greeting)}，${escapeHTML(user.username || "用户")}。<br><span>一切运行正常。</span></h2>
        </div>
        <div class="hero-meta">
          <span class="hero-chip">方案 <strong>${escapeHTML(user.tier_name || "未分配")}</strong></span>
          <span class="hero-chip">RPM <strong>${escapeHTML(formatLimit(user.rpm))}</strong></span>
          <span class="hero-chip">身份 <strong>${user.role === "admin" ? "管理员" : "用户"}</strong></span>
        </div>
      </div>
      <div class="hero-gauge-wrap">
        <div class="hero-gauge" style="--gauge-value:${quotaPercent.toFixed(1)}%">
          <div class="hero-gauge-copy">
            <strong>${successLimit > 0 ? escapeHTML(formatPercent(quotaPercent, 0)) : "∞"}</strong>
            <span>${successLimit > 0 ? "本月额度已用" : "成功调用不限"}</span>
          </div>
        </div>
      </div>
    </section>

    <section class="metric-grid" aria-label="核心指标">
      ${renderMetricCard("总调用", formatNumber(usage.total_calls), "最近 24 小时", "activity", "#eeeaff", "#7667f4", false, "trend", usage.traffic_buckets)}
      ${renderMetricCard("成功率", formatPercent(successRate), `${formatNumber(usage.success_calls)} 次成功`, "shield", "#e8f8ef", "#238a54", successRate >= 95, "ring", successRate)}
      ${renderMetricCard("当前 RPM", formatNumber(usage.current_rpm), user.rpm > 0 ? `上限 ${formatNumber(user.rpm)}` : "当前方案不限速", "chart", "#e8f1ff", "#3d83f6", false, "pulse", usage.current_rpm)}
      ${renderMetricCard("可用密钥", formatNumber(activeKeys), `共 ${formatNumber(totalKeys)} 个密钥`, "key", "#fff6e5", "#d58a19", false, "nodes", activeKeys)}
    </section>

    <section class="dashboard-grid">
      <article class="content-card">
        <header class="card-header">
          <div><h2>调用趋势</h2><p>24 小时流量分布</p></div>
          <button class="button button-ghost button-sm" type="button" data-action="navigate" data-page="usage">完整分析 ${renderIcon("arrowRight")}</button>
        </header>
        <div class="card-body chart-wrap">${renderChart(usage.traffic_buckets)}</div>
      </article>
      <article class="content-card">
        <header class="card-header"><div><h2>热门工具</h2><p>按调用次数排序</p></div></header>
        <div class="card-body">${renderToolBreakdown(usage.by_tool)}</div>
      </article>
    </section>
  `;
}

function renderOverviewLoading() {
  return `
    ${renderPageHeading("总览", "正在同步服务状态与调用指标。")}
    <div class="skeleton" style="height:252px;border-radius:24px;margin-bottom:18px"></div>
    <section class="metric-grid">
      ${Array.from({ length: 4 }, () => '<div class="skeleton" style="height:142px;border-radius:16px"></div>').join("")}
    </section>
    <section class="dashboard-grid">
      <div class="skeleton" style="height:350px;border-radius:16px"></div>
      <div class="skeleton" style="height:350px;border-radius:16px"></div>
    </section>
  `;
}

function createEmptyUsage() {
  return { total_calls: 0, success_calls: 0, current_rpm: 0, by_tool: {}, traffic_buckets: [], records: [] };
}

function getGreeting() {
  const currentHour = new Date().getHours();
  if (currentHour < 6) return "夜深了";
  if (currentHour < 12) return "早上好";
  if (currentHour < 18) return "下午好";
  return "晚上好";
}
