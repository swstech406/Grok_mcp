export function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

export function formatNumber(value, options = {}) {
  const numberValue = Number(value);
  if (!Number.isFinite(numberValue)) {
    return "0";
  }

  const defaultOptions = options.compact
    ? { notation: "compact", maximumFractionDigits: 1 }
    : { maximumFractionDigits: 0 };
  return new Intl.NumberFormat("zh-CN", { ...defaultOptions, ...options }).format(numberValue);
}

export function formatPercent(value, maximumFractionDigits = 1) {
  const numberValue = Number(value);
  if (!Number.isFinite(numberValue)) {
    return "0%";
  }
  return `${numberValue.toFixed(maximumFractionDigits).replace(/\.0$/, "")}%`;
}

export function calculatePercent(value, total) {
  const numericValue = Number(value);
  const numericTotal = Number(total);
  if (!Number.isFinite(numericValue) || !Number.isFinite(numericTotal) || numericTotal <= 0) {
    return 0;
  }
  return Math.max(0, Math.min(100, (numericValue / numericTotal) * 100));
}

export function formatLimit(value, suffix = "") {
  const numberValue = Number(value);
  if (!Number.isFinite(numberValue) || numberValue <= 0) {
    return "不限";
  }
  return `${formatNumber(numberValue)}${suffix}`;
}

export function formatDateTime(value) {
  if (!value) {
    return "从未";
  }
  const dateValue = new Date(value);
  if (Number.isNaN(dateValue.getTime())) {
    return "未知";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(dateValue);
}

export function formatShortDate(value) {
  if (!value) {
    return "--";
  }
  const dateValue = new Date(value);
  if (Number.isNaN(dateValue.getTime())) {
    return "--";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(dateValue);
}

export function formatRelativeTime(value) {
  if (!value) {
    return "从未使用";
  }
  const timestamp = new Date(value).getTime();
  if (!Number.isFinite(timestamp)) {
    return "未知时间";
  }

  const elapsedSeconds = Math.round((timestamp - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });
  const absoluteSeconds = Math.abs(elapsedSeconds);

  if (absoluteSeconds < 60) {
    return formatter.format(elapsedSeconds, "second");
  }
  if (absoluteSeconds < 3600) {
    return formatter.format(Math.round(elapsedSeconds / 60), "minute");
  }
  if (absoluteSeconds < 86400) {
    return formatter.format(Math.round(elapsedSeconds / 3600), "hour");
  }
  if (absoluteSeconds < 2592000) {
    return formatter.format(Math.round(elapsedSeconds / 86400), "day");
  }
  return formatDateTime(value);
}

export function getInitials(username) {
  const normalizedUsername = String(username || "U").trim();
  if (!normalizedUsername) {
    return "U";
  }
  return normalizedUsername.slice(0, 2).toUpperCase();
}

export function getUsagePeriodSince(period) {
  const currentTime = new Date();
  const periodToMilliseconds = {
    "24h": 24 * 60 * 60 * 1000,
    "7d": 7 * 24 * 60 * 60 * 1000,
    "30d": 30 * 24 * 60 * 60 * 1000
  };
  const milliseconds = periodToMilliseconds[period];
  if (!milliseconds) {
    return "";
  }
  return new Date(currentTime.getTime() - milliseconds).toISOString();
}

export function getSuccessRate(usage) {
  const totalCalls = Number(usage?.total_calls || 0);
  const successCalls = Number(usage?.success_calls || 0);
  if (totalCalls <= 0) {
    return 0;
  }
  return Math.max(0, Math.min(100, (successCalls / totalCalls) * 100));
}

export function createFormDataObject(formElement) {
  return Object.fromEntries(new FormData(formElement).entries());
}
