import { loadRouteData, render } from "../app.js";
import { api, loadInviteCodes, loadKeys, loadRegistrationSettings, loadServerSettings, loadTiers, loadUsers } from "./api.js";
import { notify } from "./components/toast.js";
import { navigate } from "./router.js";
import { clearSession, state, storage } from "./state.js";
import { errorText, normalizeUsage, setStored } from "./utils.js";

const VALID_USAGE_RANGE_MODES = new Set(["24h", "7d", "all"]);
const VALID_USAGE_ACTIVITY_PAGE_SIZES = new Set([10, 20, 50, 100]);

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
  } else if (form.id === "create-invite-code-form") {
    await submitCreateInviteCode(form);
  } else if (form.id === "edit-invite-code-form") {
    await submitEditInviteCode(form);
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
  const body = { username, password };
  if (state.registrationSettings?.registration_mode === "invite") {
    body.invite_code = String(data.get("invite_code") || "").trim();
  }
  try {
    await api("/auth/register", {
      method: "POST",
      auth: false,
      body
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
  const name = String(data.get("name") || "").trim();
  if (!id) {
    notify("Key 信息缺失，请刷新后重试。", "error");
    render();
    return;
  }
  if (!name) {
    notify("请输入 API Key 名称后再保存。", "error");
    render();
    return;
  }
  const submitButton = form.querySelector('[data-action="submit-edit-key"]');
  const originalSubmitButtonHTML = submitButton ? submitButton.innerHTML : "";
  if (submitButton) {
    submitButton.disabled = true;
    submitButton.innerHTML = '<span class="material-symbols-outlined">hourglass_top</span><span>Saving...</span>';
  }
  try {
    const updatedKey = await api(`/keys/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: {
        name,
        enabled: data.get("enabled") === "on"
      }
    });
    state.keys = state.keys.map((key) => key.id === updatedKey.id ? updatedKey : key);
    state.modal = null;
    notify(updatedKey.enabled ? "Key 已启用。" : "Key 已禁用。", "success");
    render();
  } catch (err) {
    if (submitButton && document.contains(submitButton)) {
      submitButton.disabled = false;
      submitButton.innerHTML = originalSubmitButtonHTML;
    }
    notify(errorText(err), "error");
    render();
  }
}

export async function submitEditUser(form) {
  const data = new FormData(form);
  const id = String(data.get("id") || "");
  const tierID = String(data.get("tier_id") || "").trim();
  if (!id) {
    notify("用户信息缺失，请刷新后重试。", "error");
    render();
    return;
  }
  if (!tierID) {
    notify("请选择用户 Tier 后再保存。", "error");
    render();
    return;
  }
  const body = {
    enabled: data.get("enabled") === "on",
    role: String(data.get("role") || "user"),
    tier_id: tierID
  };
  if (data.get("revoke_tokens") === "on") {
    body.revoke_tokens = true;
  }
  const submitButton = form.querySelector('[data-action="submit-edit-user"]');
  const originalSubmitButtonHTML = submitButton ? submitButton.innerHTML : "";
  if (submitButton) {
    submitButton.disabled = true;
    submitButton.innerHTML = '<span class="material-symbols-outlined">hourglass_top</span><span>Saving...</span>';
  }
  try {
    const updatedUser = await api(`/admin/users/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body
    });
    state.users = state.users.map((user) => user.id === updatedUser.id ? updatedUser : user);
    if (state.user && state.user.id === updatedUser.id) {
      state.user = updatedUser;
      setStored(storage.user, JSON.stringify(state.user));
    }
    state.modal = null;
    notify("用户已更新。", "success");
    render();
  } catch (err) {
    if (submitButton && document.contains(submitButton)) {
      submitButton.disabled = false;
      submitButton.innerHTML = originalSubmitButtonHTML;
    }
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
    registration_mode: String(data.get("registration_mode") || "free"),
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
    state.registrationSettings = { registration_mode: state.serverSettings.registration_mode || "free" };
    notify("服务器设置已保存。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitCreateInviteCode(form) {
  const data = new FormData(form);
  try {
    const response = await api("/admin/invite-codes", {
      method: "POST",
      body: { registration_limit: Number(data.get("registration_limit") || 0) }
    });
    const createdInviteCode = response.invite_code || response;
    state.modal = null;
    await loadInviteCodes();
    notify(createdInviteCode.code ? "邀请码已创建，可在列表中复制。" : "邀请码已创建。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export async function submitEditInviteCode(form) {
  const data = new FormData(form);
  const id = String(data.get("id") || "");
  if (!id) {
    notify("邀请码信息缺失，请刷新后重试。", "error");
    render();
    return;
  }
  try {
    await api(`/admin/invite-codes/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: {
        registration_limit: Number(data.get("registration_limit") || 0),
        enabled: data.get("enabled") === "on"
      }
    });
    state.modal = null;
    await loadInviteCodes();
    notify("邀请码已更新。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    render();
  }
}

export function openDeleteInviteCodeModal(id) {
  const inviteCode = state.inviteCodes.find((item) => item.id === id);
  if (!inviteCode) return;
  state.modal = {
    type: "delete-confirm",
    title: "Delete invite code?",
    message: `Delete invite code "${inviteCode.code_prefix || inviteCode.id}"?`,
    detail: "Existing users created by this code are not affected. Deleted invite codes cannot be used again.",
    confirmLabel: "Delete Invite Code",
    confirmAction: "confirm-delete-invite-code",
    targetId: inviteCode.id
  };
  render();
}

export async function deleteInviteCode(id) {
  const inviteCode = state.inviteCodes.find((item) => item.id === id);
  if (!inviteCode) return;
  try {
    await api(`/admin/invite-codes/${encodeURIComponent(id)}`, { method: "DELETE" });
    state.modal = null;
    await loadInviteCodes();
    notify("邀请码已删除。", "success");
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
    const nextRoute = actionEl.dataset.route || "dashboard";
    state.expandUsageActivityOnNextUsageNavigation = nextRoute === "usage" && actionEl.dataset.expandUsageActivity === "true";
    navigate(nextRoute);
  } else if (action === "refresh") {
    await loadRouteData();
    render();
  } else if (action === "usage-range") {
    const nextUsageRangeMode = actionEl.dataset.range;
    if (!VALID_USAGE_RANGE_MODES.has(nextUsageRangeMode) || state.sinceMode === nextUsageRangeMode) {
      return;
    }
    state.sinceMode = nextUsageRangeMode;
    state.usageActivityPage = 1;
    await loadRouteData();
    render();
  } else if (action === "expand-usage-activity") {
    state.usageActivityCompact = false;
    state.usageActivityPage = 1;
    render();
  } else if (action === "usage-activity-page") {
    const nextUsageActivityPage = Math.floor(Number(actionEl.dataset.page) || 1);
    if (nextUsageActivityPage < 1 || state.usageActivityPage === nextUsageActivityPage) {
      return;
    }
    state.usageActivityPage = nextUsageActivityPage;
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
  } else if (action === "copy-created-invite-code") {
    await copyCreatedInviteCode();
  } else if (action === "dismiss-created-invite-code") {
    state.createdInviteCode = null;
    render();
  } else if (action === "edit-key") {
    const key = state.keys.find((item) => item.id === actionEl.dataset.keyId);
    if (!key) return;
    state.modal = { type: "edit-key", key };
    render();
  } else if (action === "submit-edit-key") {
    event.preventDefault();
    const form = actionEl.closest("form");
    if (form instanceof HTMLFormElement) {
      await submitEditKey(form);
    }
  } else if (action === "delete-key") {
    openDeleteKeyModal(actionEl.dataset.keyId);
  } else if (action === "key-usage") {
    state.selectedKeyID = actionEl.dataset.keyId || "all";
    navigate("usage");
  } else if (action === "edit-user") {
    const user = state.users.find((item) => item.id === actionEl.dataset.userId);
    if (!user) return;
    try {
      if (!state.tiers.length) {
        await loadTiers();
      }
    } catch (err) {
      notify(errorText(err), "error");
      render();
      return;
    }
    state.modal = { type: "edit-user", user };
    render();
  } else if (action === "submit-edit-user") {
    event.preventDefault();
    const form = actionEl.closest("form");
    if (form instanceof HTMLFormElement) {
      await submitEditUser(form);
    }
  } else if (action === "delete-user") {
    openDeleteUserModal(actionEl.dataset.userId);
  } else if (action === "confirm-delete-user") {
    await deleteUser(actionEl.dataset.targetId);
  } else if (action === "open-create-invite-code") {
    state.modal = { type: "create-invite-code" };
    render();
  } else if (action === "copy-invite-code") {
    await copyInviteCode(actionEl.dataset.inviteCodeId);
  } else if (action === "edit-invite-code") {
    const inviteCode = state.inviteCodes.find((item) => item.id === actionEl.dataset.inviteCodeId);
    if (!inviteCode) return;
    state.modal = { type: "edit-invite-code", inviteCode };
    render();
  } else if (action === "submit-edit-invite-code") {
    event.preventDefault();
    const form = actionEl.closest("form");
    if (form instanceof HTMLFormElement) {
      await submitEditInviteCode(form);
    }
  } else if (action === "delete-invite-code") {
    openDeleteInviteCodeModal(actionEl.dataset.inviteCodeId);
  } else if (action === "confirm-delete-invite-code") {
    await deleteInviteCode(actionEl.dataset.targetId);
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
    try {
      await loadRegistrationSettings();
      notify("已退出登录。", "success");
    } catch (err) {
      notify(`已退出登录，但注册设置刷新失败：${errorText(err)}`, "error");
    }
    render();
  }
}

export async function onChange(event) {
  const target = event.target;
  if (!(target instanceof HTMLElement)) return;

  if (target.matches("[data-key-toggle]")) {
    const checkbox = target;
    await updateKeyEnabled(checkbox.dataset.keyToggle, checkbox.checked);
  } else if (target.matches("[data-invite-code-toggle]")) {
    const checkbox = target;
    await updateInviteCodeEnabled(checkbox.dataset.inviteCodeToggle, checkbox.checked);
  } else if (target.id === "usage-key-select") {
    if (state.selectedKeyID === target.value) {
      return;
    }
    state.selectedKeyID = target.value;
    state.usageActivityPage = 1;
    await loadRouteData();
    render();
  } else if (target.id === "usage-since-select") {
    state.sinceMode = target.value;
    state.usageActivityPage = 1;
    await loadRouteData();
    render();
  } else if (target.id === "usage-activity-page-size") {
    const nextUsageActivityPageSize = Number(target.value);
    if (!VALID_USAGE_ACTIVITY_PAGE_SIZES.has(nextUsageActivityPageSize) || state.usageActivityPageSize === nextUsageActivityPageSize) {
      return;
    }
    state.usageActivityPageSize = nextUsageActivityPageSize;
    state.usageActivityPage = 1;
    render();
  }
}

export function onInput(event) {
  const target = event.target;
  if (!(target instanceof HTMLInputElement)) return;
  if (target.id === "global-search") {
    state.search = target.value;
    state.usageActivityPage = 1;
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

export async function updateInviteCodeEnabled(id, enabled) {
  try {
    const response = await api(`/admin/invite-codes/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: { enabled }
    });
    const updatedInviteCode = response.invite_code || response;
    state.inviteCodes = state.inviteCodes.map((inviteCode) => inviteCode.id === updatedInviteCode.id ? updatedInviteCode : inviteCode);
    notify(enabled ? "邀请码已启用。" : "邀请码已禁用。", "success");
    render();
  } catch (err) {
    notify(errorText(err), "error");
    await loadInviteCodes();
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
    state.usageActivityPage = 1;
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

export async function copyCreatedInviteCode(options = {}) {
  const input = document.getElementById("created-invite-code");
  const value = input ? input.value : state.createdInviteCode && (state.createdInviteCode.code || state.createdInviteCode.invite_code?.code);
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

  if (copied) {
    notify(options.automatic ? "邀请码已自动复制到剪贴板。" : "已复制到剪贴板。", "success");
  } else {
    window.clearTimeout(notify.timer);
    state.toast = null;
  }

  render();
  if (!copied) {
    selectCreatedInviteCode();
  }
  return copied;
}

export async function copyInviteCode(id) {
  const inviteCode = state.inviteCodes.find((item) => item.id === id);
  if (!inviteCode || !inviteCode.code) {
    notify("该邀请码没有可复制的明文，请重新生成邀请码。", "error");
    render();
    return false;
  }

  let copied = copyTextWithSelection(inviteCode.code);
  if (!copied) {
    try {
      if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") {
        throw new Error("clipboard unavailable");
      }
      await navigator.clipboard.writeText(inviteCode.code);
      copied = true;
    } catch {
      copied = false;
    }
  }

  if (copied) {
    notify("邀请码已复制到剪贴板。", "success");
  } else {
    notify("浏览器拒绝复制，请手动选中邀请码复制。", "error");
  }
  render();
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

export function selectCreatedInviteCode() {
  const input = document.getElementById("created-invite-code");
  if (!input) return;
  input.focus({ preventScroll: true });
  input.select();
  input.setSelectionRange(0, input.value.length);
}
