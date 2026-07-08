import { loadRouteData, render } from "../app.js";
import { api, loadKeys, loadServerSettings, loadTiers, loadUsers } from "./api.js";
import { notify } from "./components/toast.js";
import { navigate } from "./router.js";
import { clearSession, state, storage } from "./state.js";
import { errorText, normalizeUsage, setStored } from "./utils.js";

export async function onSubmit(event) {
  const form = event.target;
  if (!(form instanceof HTMLFormElement)) return;
  event.preventDefault();

  if (form.id === "login-form") {
    await submitLogin(form);
  } else if (form.id === "register-form") {
    await submitRegister(form);
  } else if (form.id === "create-key-form") {
    await submitCreateKey(form);
  } else if (form.id === "edit-key-form") {
    await submitEditKey(form);
  } else if (form.id === "edit-user-form") {
    await submitEditUser(form);
  } else if (form.id === "create-tier-form") {
    await submitCreateTier(form);
  } else if (form.id === "edit-tier-form") {
    await submitEditTier(form);
  } else if (form.id === "server-settings-form") {
    await submitServerSettings(form);
  }
}

export async function submitLogin(form) {
  const data = new FormData(form);
  try {
    const resp = await api("/auth/login", {
      method: "POST",
      auth: false,
      body: {
        username: String(data.get("username") || "").trim(),
        password: String(data.get("password") || "")
      }
    });
    state.token = resp.token;
    state.user = resp.user;
    setStored(storage.token, state.token);
    setStored(storage.user, JSON.stringify(state.user));
    notify("登录成功。", "success");
    navigate("dashboard");
    await loadRouteData();
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitRegister(form) {
  const data = new FormData(form);
  const username = String(data.get("username") || "").trim();
  const password = String(data.get("password") || "");
  try {
    await api("/auth/register", {
      method: "POST",
      auth: false,
      body: { username, password }
    });
    notify("注册成功，正在登录。", "success");
    const loginForm = new FormData();
    loginForm.set("username", username);
    loginForm.set("password", password);
    const resp = await api("/auth/login", {
      method: "POST",
      auth: false,
      body: { username, password }
    });
    state.token = resp.token;
    state.user = resp.user;
    setStored(storage.token, state.token);
    setStored(storage.user, JSON.stringify(state.user));
    navigate("dashboard");
    await loadRouteData();
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitCreateKey(form) {
  const data = new FormData(form);
  try {
    const resp = await api("/keys", {
      method: "POST",
      body: { name: String(data.get("name") || "").trim() }
    });
    await loadKeys();
    state.modal = { type: "key-created", key: resp.key, apiKey: resp.api_key };
    window.clearTimeout(notify.timer);
    state.toast = null;
    render();
    await copyCreatedKey({ automatic: true });
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitEditKey(form) {
  const data = new FormData(form);
  const id = String(data.get("id") || "");
  try {
    await api(`/keys/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: {
        name: String(data.get("name") || "").trim(),
        enabled: data.get("enabled") === "on"
      }
    });
    state.modal = null;
    await loadKeys();
    notify("Key 已更新。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitEditUser(form) {
  const data = new FormData(form);
  const id = String(data.get("id") || "");
  const body = {
    enabled: data.get("enabled") === "on",
    role: String(data.get("role") || "user"),
    tier_id: String(data.get("tier_id") || "")
  };
  if (data.get("revoke_tokens") === "on") {
    body.revoke_tokens = true;
  }
  try {
    await api(`/admin/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body
    });
    state.modal = null;
    await loadUsers();
    notify("用户已更新。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitCreateTier(form) {
  const data = new FormData(form);
  try {
    await api("/admin/tiers", {
      method: "POST",
      body: {
        name: String(data.get("name") || "").trim(),
        level: Number(data.get("level") || 0),
        rpm: Number(data.get("rpm") || 0),
        success_limit: Number(data.get("success_limit") || 0)
      }
    });
    state.modal = null;
    await loadTiers();
    notify("等级已创建。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitEditTier(form) {
  const data = new FormData(form);
  const id = String(data.get("id") || "");
  try {
    await api(`/admin/tiers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: {
        name: String(data.get("name") || "").trim(),
        level: Number(data.get("level") || 0),
        rpm: Number(data.get("rpm") || 0),
        success_limit: Number(data.get("success_limit") || 0)
      }
    });
    state.modal = null;
    await loadTiers();
    notify("等级已更新。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitServerSettings(form) {
  const data = new FormData(form);
  const cpaAPIKey = String(data.get("cpa_api_key") || "").trim();
  const body = {
    cpa_base_url: String(data.get("cpa_base_url") || "").trim(),
    model: String(data.get("model") || "").trim(),
    timeout_seconds: Number(data.get("timeout_seconds") || 0),
    proxy_url: String(data.get("proxy_url") || "").trim(),
    proxy_enabled: data.get("proxy_enabled") === "on",
    debug: data.get("debug") === "on"
  };
  if (cpaAPIKey !== "") {
    body.cpa_api_key = cpaAPIKey;
  }
  try {
    state.serverSettings = await api("/admin/settings", {
      method: "PATCH",
      body
    });
    notify("服务器设置已保存。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export function openDeleteTierModal(id) {
  const tier = state.tiers.find((item) => item.id === id);
  if (!tier) return;
  state.modal = {
    type: "delete-confirm",
    title: "Delete tier?",
    message: `Delete tier "${tier.name}"?`,
    detail: "Users must be reassigned before an in-use tier can be deleted.",
    confirmLabel: "Delete Tier",
    confirmAction: "confirm-delete-tier",
    targetId: tier.id
  };
  render();
}

export async function deleteTier(id) {
  const tier = state.tiers.find((item) => item.id === id);
  if (!tier) return;
  try {
    await api(`/admin/tiers/${encodeURIComponent(id)}`, { method: "DELETE" });
    state.modal = null;
    await loadTiers();
    notify("等级已删除。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export function openDeleteUserModal(id) {
  const user = state.users.find((item) => item.id === id);
  if (!user) return;
  state.modal = {
    type: "delete-confirm",
    title: "Delete user?",
    message: `Delete user "${user.username}"?`,
    detail: "This will also delete this user's API keys and usage logs.",
    confirmLabel: "Delete User",
    confirmAction: "confirm-delete-user",
    targetId: user.id
  };
  render();
}

export async function deleteUser(id) {
  const user = state.users.find((item) => item.id === id);
  if (!user) return;
  try {
    await api(`/admin/users/${encodeURIComponent(id)}`, { method: "DELETE" });
    state.modal = null;
    await loadUsers();
    notify("用户已删除。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function onClick(event) {
  const actionEl = event.target.closest("[data-action]");
  if (!actionEl) return;
  const action = actionEl.dataset.action;

  if (action === "auth-tab") {
    state.authMode = actionEl.dataset.tab === "register" ? "register" : "login";
    render();
  } else if (action === "go") {
    navigate(actionEl.dataset.route || "dashboard");
  } else if (action === "refresh") {
    await loadRouteData();
    render();
  } else if (action === "expand-usage-activity") {
    state.usageActivityCompact = false;
    render();
  } else if (action === "reload-server-settings") {
    await loadServerSettings();
    notify("服务器设置已刷新。", "success");
    render();
  } else if (action === "open-create-key") {
    state.modal = { type: "create-key" };
    render();
  } else if (action === "close-modal") {
    if (!event.target.closest("[data-modal]") || actionEl.classList.contains("modal-close") || actionEl.classList.contains("button")) {
      state.modal = null;
      render();
    }
  } else if (action === "copy-created-key") {
    await copyCreatedKey();
  } else if (action === "edit-key") {
    const key = state.keys.find((item) => item.id === actionEl.dataset.keyId);
    state.modal = { type: "edit-key", key };
    render();
  } else if (action === "delete-key") {
    openDeleteKeyModal(actionEl.dataset.keyId);
  } else if (action === "key-usage") {
    state.selectedKeyID = actionEl.dataset.keyId || "all";
    navigate("usage");
  } else if (action === "edit-user") {
    const user = state.users.find((item) => item.id === actionEl.dataset.userId);
    state.modal = { type: "edit-user", user };
    render();
  } else if (action === "delete-user") {
    openDeleteUserModal(actionEl.dataset.userId);
  } else if (action === "confirm-delete-user") {
    await deleteUser(actionEl.dataset.targetId);
  } else if (action === "open-create-tier") {
    state.modal = { type: "create-tier" };
    render();
  } else if (action === "edit-tier") {
    const tier = state.tiers.find((item) => item.id === actionEl.dataset.tierId);
    state.modal = { type: "edit-tier", tier };
    render();
  } else if (action === "delete-tier") {
    openDeleteTierModal(actionEl.dataset.tierId);
  } else if (action === "confirm-delete-tier") {
    await deleteTier(actionEl.dataset.targetId);
  } else if (action === "confirm-delete-key") {
    await deleteKey(actionEl.dataset.targetId);
  } else if (action === "user-usage") {
    await openUserUsage(actionEl.dataset.userId);
  } else if (action === "view-user-usage-logs") {
    await viewUserUsageLogs(actionEl.dataset.userId);
  } else if (action === "view-debug-json") {
    openDebugJSONModal(actionEl.dataset.recordId);
  } else if (action === "logout") {
    clearSession();
    notify("已退出登录。", "success");
    render();
  }
}

export async function onChange(event) {
  const target = event.target;
  if (!(target instanceof HTMLElement)) return;

  if (target.matches("[data-key-toggle]")) {
    const checkbox = target;
    await updateKeyEnabled(checkbox.dataset.keyToggle, checkbox.checked);
  } else if (target.id === "usage-key-select") {
    state.selectedKeyID = target.value;
    await loadRouteData();
    render();
  } else if (target.id === "usage-since-select") {
    state.sinceMode = target.value;
    await loadRouteData();
    render();
  }
}

export function onInput(event) {
  const target = event.target;
  if (!(target instanceof HTMLInputElement)) return;
  if (target.id === "global-search") {
    state.search = target.value;
    render();
    const next = document.getElementById("global-search");
    if (next) {
      next.focus();
      next.setSelectionRange(next.value.length, next.value.length);
    }
  }
}

export function openDebugJSONModal(id) {
  const modalUsageRecords = state.modal && state.modal.usage ? state.modal.usage.records || [] : [];
  const records = [...(state.usage.records || []), ...modalUsageRecords];
  const record = records.find((item) => String(item.id) === String(id));
  if (!record || !record.debug_json) return;
  state.modal = { type: "debug-json", record };
  render();
}

export async function updateKeyEnabled(id, enabled) {
  try {
    await api(`/keys/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: { enabled }
    });
    const key = state.keys.find((item) => item.id === id);
    if (key) key.enabled = enabled;
    notify(enabled ? "Key 已启用。" : "Key 已禁用。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    await loadKeys();
    render();
  }
}

export function openDeleteKeyModal(id) {
  const key = state.keys.find((item) => item.id === id);
  if (!key) return;
  state.modal = {
    type: "delete-confirm",
    title: "Delete API key?",
    message: `Delete API key "${key.name || key.key_prefix}"?`,
    detail: "MCP clients using this key will stop working immediately.",
    confirmLabel: "Delete Key",
    confirmAction: "confirm-delete-key",
    targetId: key.id
  };
  render();
}

export async function deleteKey(id) {
  const key = state.keys.find((item) => item.id === id);
  if (!key) return;
  try {
    await api(`/keys/${encodeURIComponent(id)}`, { method: "DELETE" });
    state.modal = null;
    await loadKeys();
    notify("Key 已删除。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function openUserUsage(id) {
  const user = state.users.find((item) => item.id === id);
  if (!user) return;
  try {
    const usage = await api(`/admin/users/${encodeURIComponent(id)}/usage`);
    state.modal = { type: "user-usage", user, usage: normalizeUsage(usage) };
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function viewUserUsageLogs(id) {
  const user = state.users.find((item) => item.id === id) || (state.modal && state.modal.user && state.modal.user.id === id ? state.modal.user : null);
  if (!user) return;
  try {
    const usage = await api(`/admin/users/${encodeURIComponent(id)}/usage`);
    state.modal = { type: "user-usage-logs", user, usage: normalizeUsage(usage) };
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function copyCreatedKey(options = {}) {
  const input = document.getElementById("created-api-key");
  const value = input ? input.value : state.modal && state.modal.apiKey;
  if (!value) return;
  let copied = copyTextWithSelection(value, input);
  if (!copied) {
    try {
      if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") {
        throw new Error("clipboard unavailable");
      }
      await navigator.clipboard.writeText(value);
      copied = true;
    } catch {
      copied = false;
    }
  }

  if (state.modal && state.modal.type === "key-created") {
    state.modal.copyFailed = !copied;
    state.modal.copySucceeded = copied;
  }

  if (copied) {
    notify(options.automatic ? "Key 已自动复制到剪贴板。" : "已复制到剪贴板。", "success");
  } else {
    window.clearTimeout(notify.timer);
    state.toast = null;
  }

  render();
  if (state.modal && state.modal.type === "key-created" && state.modal.copyFailed) {
    selectCreatedKey();
  }
  return copied;
}

export function copyTextWithSelection(value, input) {
  const target = input || document.createElement("textarea");
  let appended = false;

  if (!input) {
    target.value = value;
    target.setAttribute("readonly", "");
    target.style.position = "fixed";
    target.style.left = "-9999px";
    target.style.top = "0";
    document.body.appendChild(target);
    appended = true;
  }

  try {
    target.focus({ preventScroll: true });
    target.select();
    target.setSelectionRange(0, target.value.length);
    return typeof document.execCommand === "function" && document.execCommand("copy");
  } catch {
    return false;
  } finally {
    if (appended) {
      target.remove();
    }
  }
}

export function selectCreatedKey() {
  const input = document.getElementById("created-api-key");
  if (!input) return;
  input.focus({ preventScroll: true });
  input.select();
  input.setSelectionRange(0, input.value.length);
}
