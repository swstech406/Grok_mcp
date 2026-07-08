import { state } from "./state.js";

export function rpmText(rpm) {
  return limitText(rpm);
}

export function emptyUsage() {
  return { total_calls: 0, success_calls: 0, by_tool: {}, records: [] };
}

export function normalizeUsage(data) {
  return {
    total_calls: Number(data && data.total_calls) || 0,
    success_calls: Number(data && data.success_calls) || 0,
    by_tool: data && data.by_tool ? data.by_tool : {},
    records: Array.isArray(data && data.records) ? data.records : []
  };
}

export function aggregateUsage(parts) {
  const usage = emptyUsage();
  for (const part of parts.map(normalizeUsage)) {
    usage.total_calls += part.total_calls;
    usage.success_calls += part.success_calls;
    for (const [tool, count] of Object.entries(part.by_tool || {})) {
      usage.by_tool[tool] = (usage.by_tool[tool] || 0) + Number(count || 0);
    }
    usage.records.push(...(part.records || []));
  }
  usage.records.sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
  return usage;
}

export function sinceQuery(mode) {
  if (mode === "all") return "";
  const now = Date.now();
  const ms = mode === "7d" ? 7 * 24 * 60 * 60 * 1000 : 24 * 60 * 60 * 1000;
  return `?since=${encodeURIComponent(new Date(now - ms).toISOString())}`;
}

export function rangeLabel(mode) {
  if (mode === "7d") return "Last 7 Days";
  if (mode === "all") return "All Time";
  return "Last 24 Hours";
}

export function bucketRecords(records, mode = "24h") {
  const buckets = new Array(8).fill(0);
  const now = Date.now();
  const validTimestamps = (records || [])
    .map((record) => new Date(record.timestamp).getTime())
    .filter((timestamp) => Number.isFinite(timestamp));
  const bucketWindow = resolveBucketWindow(validTimestamps, mode, now);
  const bucketWindowDuration = Math.max(1, bucketWindow.end - bucketWindow.start);

  for (const timestamp of validTimestamps) {
    if (timestamp < bucketWindow.start || timestamp > bucketWindow.end) continue;
    const bucketIndex = Math.min(7, Math.max(0, Math.floor(((timestamp - bucketWindow.start) / bucketWindowDuration) * 8)));
    buckets[bucketIndex] += 1;
  }
  return buckets;
}

function resolveBucketWindow(timestamps, mode, now) {
  if (mode === "7d") {
    return { start: now - 7 * 24 * 60 * 60 * 1000, end: now };
  }
  if (mode === "all") {
    return resolveAllTimeBucketWindow(timestamps, now);
  }
  return { start: now - 24 * 60 * 60 * 1000, end: now };
}

function resolveAllTimeBucketWindow(timestamps, now) {
  const historicalTimestamps = timestamps.filter((timestamp) => timestamp <= now);
  if (!historicalTimestamps.length) {
    return { start: now - 24 * 60 * 60 * 1000, end: now };
  }

  const oldestTimestamp = Math.min(...historicalTimestamps);
  return {
    start: oldestTimestamp < now ? oldestTimestamp : now - 24 * 60 * 60 * 1000,
    end: now
  };
}

export function buildDashboardAlert(records) {
  const successLimitAlert = buildSuccessQuotaDashboardAlert();
  if (successLimitAlert) {
    return successLimitAlert;
  }
  return buildRPMDashboardAlert(records);
}

export function buildSuccessQuotaDashboardAlert() {
  const successLimit = Number(state.user.success_limit) || 0;
  if (successLimit <= 0) {
    return null;
  }
  const successLimitPercent = percentOf(state.user.success_calls, successLimit);
  if (successLimitPercent < 90) {
    return null;
  }
  return {
    title: "Success Limit Near Capacity",
    body: `You are currently using ${Math.round(successLimitPercent)}% of your success limit.`
  };
}

export function buildRPMDashboardAlert(records) {
  const rpmLimit = Number(state.user.rpm) || 0;
  if (rpmLimit <= 0) {
    return null;
  }
  const recentMinuteCalls = countRecordsInWindow(records, 60 * 1000);
  const rpmWarningThreshold = Math.max(1, Math.ceil(rpmLimit * 0.9));
  if (recentMinuteCalls < rpmWarningThreshold) {
    return null;
  }
  return {
    title: "Rate Limit Near Capacity",
    body: `${formatNumber(recentMinuteCalls)} calls in the last 60 seconds are approaching the configured user-level RPM limit.`
  };
}

export function countRecordsInWindow(records, windowMs) {
  const now = Date.now();
  const earliestAllowedTimestamp = now - windowMs;
  return (records || []).reduce((count, record) => {
    const timestamp = new Date(record.timestamp).getTime();
    if (!Number.isFinite(timestamp) || timestamp < earliestAllowedTimestamp || timestamp > now) {
      return count;
    }
    return count + 1;
  }, 0);
}

export function quotaNote(pct) {
  if (!Number.isFinite(pct) || pct === 0) return "Unlimited or unused";
  return `${Math.round(pct)}% used`;
}

export function nextNaturalMonthResetText(referenceDate = new Date()) {
  const resetDate = new Date(referenceDate.getFullYear(), referenceDate.getMonth() + 1, 1);
  const resetMonth = String(resetDate.getMonth() + 1).padStart(2, "0");
  const resetDay = String(resetDate.getDate()).padStart(2, "0");
  return `在${resetMonth}.${resetDay}进行重置`;
}

export function percentOf(value, limit) {
  const n = Number(value) || 0;
  const l = Number(limit) || 0;
  if (l <= 0) return 0;
  return (n / l) * 100;
}

export function successPercent(usage) {
  if (!usage.total_calls) return "100%";
  return `${Math.round((usage.success_calls / usage.total_calls) * 1000) / 10}%`;
}

export function limitText(limit) {
  const n = Number(limit) || 0;
  return n <= 0 ? "∞" : formatNumber(n);
}

export function formatNumber(value) {
  const n = Number(value) || 0;
  return new Intl.NumberFormat("en-US").format(n);
}

export function formatDate(value) {
  if (!value) return "--";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "--";
  return date.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "2-digit" });
}

export function formatDateTime(value) {
  if (!value) return "--";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "--";
  return date.toLocaleString("en-US");
}

export function relativeTime(value) {
  if (!value) return "Never";
  const ts = new Date(value).getTime();
  if (!Number.isFinite(ts)) return "Never";
  const diff = Date.now() - ts;
  const abs = Math.abs(diff);
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;
  const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  if (abs < minute) return "Just now";
  if (abs < hour) return rtf.format(-Math.round(diff / minute), "minute");
  if (abs < day) return rtf.format(-Math.round(diff / hour), "hour");
  return rtf.format(-Math.round(diff / day), "day");
}

export function trimToolName(name) {
  if (!name || name === "No Data") return name || "No Data";
  const parts = String(name).split(".");
  return parts[parts.length - 1] || name;
}

export function shortID(id) {
  const text = String(id || "");
  return text.length > 12 ? `${text.slice(0, 6)}...${text.slice(-4)}` : text;
}

export function initials(username) {
  const text = String(username || "U").trim();
  return text ? text[0].toUpperCase() : "U";
}

export function clamp(value, min, max) {
  return Math.min(max, Math.max(min, Number(value) || 0));
}

export function escapeHTML(value) {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

export function escapeAttr(value) {
  return escapeHTML(value);
}

export function getStored(key) {
  try {
    return window.localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

export function setStored(key, value) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    return undefined;
  }
}

export function removeStored(key) {
  try {
    window.localStorage.removeItem(key);
  } catch {
    return undefined;
  }
}

export function readJSON(key) {
  const raw = getStored(key);
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

export function errorText(err) {
  if (!err) return "请求失败。";
  if (err.status === 401) return "认证失败，请检查账号、密码或 Token。";
  if (err.status === 403) return "权限不足或用户已被禁用。";
  if (err.status === 409) return "用户名已存在。";
  if (err.status === 429) return "请求被限流或额度已耗尽。";
  return err.message || "请求失败。";
}
