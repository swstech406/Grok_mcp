import { metricCard, renderBars, renderDashboardAlert, renderRecentActivity, renderToolUsage } from "../components/metric-card.js";
import { state } from "../state.js";
import { buildDashboardAlert, escapeHTML, formatNumber, limitText, nextNaturalMonthResetText, percentOf, quotaNote, rangeLabel, rpmText } from "../utils.js";

export function renderDashboard() {
  const usage = state.usage;
  const limitsUnavailable = Boolean(state.user.limits_unavailable);
  const limitOpts = { unavailable: limitsUnavailable };
  const successPct = limitsUnavailable ? 0 : percentOf(state.user.success_calls, state.user.success_limit);
  const recentMinuteCalls = Math.max(0, Number(usage.current_rpm) || 0);
  const rpmPct = limitsUnavailable ? 0 : percentOf(recentMinuteCalls, state.user.rpm);
  const rpmProgress = !limitsUnavailable && state.user.rpm > 0 ? rpmPct : null;
  const successRate = usage.total_calls > 0 ? Math.round((usage.success_calls / usage.total_calls) * 1000) / 10 : 100;
  const successRateTone = classifySuccessRateTone(successRate, usage.total_calls);
  const successRateValue = `<span class="success-rate-value ${successRateTone}">${successRate}%</span>`;
  const successLimitResetText = !limitsUnavailable && state.user.success_limit > 0 ? nextNaturalMonthResetText() : "";
  const dashboardAlert = limitsUnavailable
    ? { title: "Limits Unavailable", body: "User tier could not be resolved; RPM and success limits are not shown as unlimited." }
    : buildDashboardAlert(recentMinuteCalls);
  const successNote = limitsUnavailable ? "Limits unavailable" : quotaNote(successPct);
  const rpmTone = limitsUnavailable ? "bad" : (rpmPct >= 90 ? "bad" : "good");
  const successTone = limitsUnavailable ? "bad" : (successPct >= 90 ? "bad" : "good");
  return `
    ${renderDashboardAlert(dashboardAlert)}
    <section class="grid metric-grid">
      ${metricCard("Rate Per Minute (RPM)", `${formatNumber(recentMinuteCalls)} <span class="muted">/ ${rpmText(state.user.rpm, limitOpts)}</span>`, "speed", limitsUnavailable ? "Tier limits unavailable" : "User-level shared rate limit", rpmTone, rpmProgress)}
      ${metricCard("Success Limit", `${formatNumber(state.user.success_calls)} <span class="muted">/ ${limitText(state.user.success_limit, limitOpts)}</span>`, "check_circle", successNote, successTone, limitsUnavailable ? null : successPct, { trailingNote: successLimitResetText })}
      ${metricCard("Success Rate", successRateValue, "check_circle", usage.total_calls ? "Based on completed calls" : "No traffic yet", "good", null, { reserveProgressSpace: true })}
      ${renderUserTierCard()}
    </section>
    <section class="grid viz-grid">
      <div class="card panel">
        <div class="panel-head">
          <h3>Traffic Volume</h3>
          <span class="mono muted">${escapeHTML(rangeLabel(state.sinceMode))}</span>
        </div>
        ${renderBars(usage.traffic_buckets, state.sinceMode)}
      </div>
      ${renderToolUsage(usage)}
    </section>
    ${renderRecentActivity(usage.records, true, {
      viewAllDataset: { expandUsageActivity: "true" }
    })}`;
}

function renderUserTierCard() {
  const tierLabel = formatTierLabel(state.user.tier_name || state.user.tier_id);
  return `
    <div class="card metric-card dashboard-tier-card">
      <div class="metric-top">
        <span class="metric-title">User Tier</span>
      </div>
      <div class="dashboard-tier-body">
        <div class="metric-value">${escapeHTML(tierLabel)}</div>
      </div>
    </div>`;
}

function formatTierLabel(value) {
  const rawTierLabel = String(value || "").trim();
  if (!rawTierLabel) {
    return "Unassigned";
  }
  return rawTierLabel.replace(/(^|[\s_-])tier/gi, (matchedPrefix) => `${matchedPrefix.slice(0, -4)}Tier`);
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
