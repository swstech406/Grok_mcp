import { emptyUsage, getStored, readJSON, removeStored } from "./utils.js";

export const storage = {
  token: "grok_mcp_panel_token",
  user: "grok_mcp_panel_user"
};

export const state = {
  ready: false,
  loading: false,
  authMode: "login",
  route: "dashboard",
  token: getStored(storage.token),
  user: readJSON(storage.user),
  keys: [],
  users: [],
  tiers: [],
  usage: emptyUsage(),
  selectedKeyID: "all",
  selectedUsageUserID: "",
  sinceMode: "24h",
  search: "",
  modal: null,
  toast: null
};

export function clearSession() {
  state.token = "";
  state.user = null;
  state.keys = [];
  state.users = [];
  state.usage = emptyUsage();
  state.selectedKeyID = "all";
  state.selectedUsageUserID = "";
  removeStored(storage.token);
  removeStored(storage.user);
}

export function isAdmin() {
  return state.user && state.user.role === "admin";
}

export function filteredKeys() {
  const q = state.search.trim().toLowerCase();
  if (!q) return state.keys;
  return state.keys.filter((key) => [key.name, key.key_prefix, key.id].some((value) => String(value || "").toLowerCase().includes(q)));
}

export function filteredUsers() {
  const q = state.search.trim().toLowerCase();
  if (!q) return state.users;
  return state.users.filter((user) => [user.username, user.role, user.id].some((value) => String(value || "").toLowerCase().includes(q)));
}

export function filteredRecords(records) {
  const q = state.search.trim().toLowerCase();
  const sorted = [...(records || [])].sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
  if (!q) return sorted;
  return sorted.filter((record) => [record.tool_name, record.key_id, record.id, record.success ? "success" : "failed"].some((value) => String(value || "").toLowerCase().includes(q)));
}
