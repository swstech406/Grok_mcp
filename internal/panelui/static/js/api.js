import { state } from "./state.js";
import { normalizeUsage, sinceQuery } from "./utils.js";

const API_BASE = "/panel/v1";

export async function api(path, options = {}) {
  const headers = {
    "Accept": "application/json"
  };
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  if (options.auth !== false && state.token) {
    headers.Authorization = `Bearer ${state.token}`;
  }
  const res = await fetch(`${API_BASE}${path}`, {
    method: options.method || "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body)
  });
  if (res.status === 204) {
    return null;
  }
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { error: text };
    }
  }
  if (!res.ok) {
    const err = new Error(data && data.error ? data.error : `HTTP ${res.status}`);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

export async function loadKeys() {
  const data = await api("/keys");
  state.keys = Array.isArray(data.keys) ? data.keys : [];
  ensureSelectedUsageKeyExists();
}

function ensureSelectedUsageKeyExists() {
  if (state.selectedKeyID === "all") {
    return;
  }
  const selectedKeyExists = state.keys.some((key) => key.id === state.selectedKeyID);
  if (!selectedKeyExists) {
    state.selectedKeyID = "all";
  }
}

export async function loadUsers() {
  const data = await api("/admin/users");
  state.users = Array.isArray(data.users) ? data.users : [];
}

export async function loadTiers() {
  const data = await api("/admin/tiers");
  state.tiers = Array.isArray(data.tiers) ? data.tiers : [];
}

export async function loadServerSettings() {
  state.serverSettings = await api("/admin/settings");
}

export async function loadAggregatedUsage(mode) {
  const since = sinceQuery(mode);
  const data = await api(`/usage${since}`);
  return normalizeUsage(data);
}

export async function loadUsageForSelection() {
  const since = sinceQuery(state.sinceMode);
  if (state.selectedKeyID === "all") {
    return loadAggregatedUsage(state.sinceMode);
  }
  const data = await api(`/keys/${encodeURIComponent(state.selectedKeyID)}/usage${since}`);
  return normalizeUsage(data);
}
