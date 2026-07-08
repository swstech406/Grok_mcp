import { metricCard, renderBars, renderDashboardAlert, renderRecentActivity, renderToolUsage } from "../components/metric-card.js";
import { state } from "../state.js";
import { buildDashboardAlert, countRecordsInWindow, escapeHTML, formatNumber, limitText, nextNaturalMonthResetText, percentOf, quotaNote, rangeLabel, rpmText } from "../utils.js";

export function renderDashboard() {
  const usage = state.usage;
  const successPct = percentOf(state.user.success_calls, state.user.success_limit);
  const recentMinuteCalls = countRecordsInWindow(usage.records, 60 * 1000);
  const rpmPct = percentOf(recentMinuteCalls, state.user.rpm);
  const rpmProgress = state.user.rpm > 0 ? rpmPct : null;
  const successRate = usage.total_calls > 0 ? Math.round((usage.success_calls / usage.total_calls) * 1000) / 10 : 100;
  const successRateTone = classifySuccessRateTone(successRate, usage.total_calls);
  const successRateValue = `<span class="success-rate-value ${successRateTone}">${successRate}%</span>`;
  const successLimitNote = state.user.success_limit > 0
    ? `${quotaNote(successPct)}，${nextNaturalMonthResetText()}`
    : quotaNote(successPct);
  const dashboardAlert = buildDashboardAlert(usage.records);
  return `
    ${renderDashboardAlert(dashboardAlert)}
    <section class="grid metric-grid">
      ${metricCard("Rate Per Minute<br>(RPM)", `${formatNumber(recentMinuteCalls)} <span class="muted">/ ${rpmText(state.user.rpm)}</span>`, "speed", "User-level shared rate limit", rpmPct >= 90 ? "bad" : "good", rpmProgress)}
      ${metricCard("Success Rate", successRateValue, "check_circle", usage.total_calls ? "Based on completed calls" : "No traffic yet", "good", null, { reserveProgressSpace: true })}
      ${metricCard("Success Limit", `${formatNumber(state.user.success_calls)} <span class="muted">/ ${limitText(state.user.success_limit)}</span>`, "check_circle", successLimitNote, successPct >= 90 ? "bad" : "good", successPct)}
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
    ${renderRecentActivity(usage.records, true)}`;
}

function classifySuccessRateTone(successRate, totalCalls) {
  if (totalCalls === 0) {
    return "neutral";
  }
  if (successRate >= 85) {
    return "good";
  }
  if (successRate >= 50) {
    return "warning";
  }
  return "bad";
}
