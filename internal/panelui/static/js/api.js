const configuredAPIBase = new URLSearchParams(window.location.search).get("apiBase") || "";
const normalizedAPIBase = configuredAPIBase.trim().replace(/\/$/, "");
const panelSessionStorageKey = "grok-search-mcp-panel-session";
const panelTokenStorageKey = "grok-search-mcp-panel-token";
const panelTokenExpiryStorageKey = "grok-search-mcp-panel-token-expiry";
const legacyPanelTokenStorageKey = "grok-mcp-panel-token";
const legacyPanelTokenExpiryStorageKey = "grok-mcp-panel-token-expiry";

export class APIError extends Error {
  constructor(message, status, retryAfterSeconds = null, code = "", details = null) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.retryAfterSeconds = retryAfterSeconds;
    this.code = code;
    this.details = details && typeof details === "object" ? details : null;
  }
}

export class PanelAPI {
  constructor() {
    const storedSession = readStoredSession();
    this.token = storedSession.token
      || sessionStorage.getItem(panelTokenStorageKey)
      || sessionStorage.getItem(legacyPanelTokenStorageKey)
      || "";
    this.expiresAt = storedSession.expiresAt
      || sessionStorage.getItem(panelTokenExpiryStorageKey)
      || sessionStorage.getItem(legacyPanelTokenExpiryStorageKey)
      || "";

    if (this.token) {
      this.saveSession(this.token, this.expiresAt);
      sessionStorage.removeItem(legacyPanelTokenStorageKey);
      sessionStorage.removeItem(legacyPanelTokenExpiryStorageKey);
    }
  }

  hasSession() {
    if (!this.token) {
      return false;
    }

    if (this.expiresAt) {
      const expirationTimestamp = new Date(this.expiresAt).getTime();
      if (Number.isFinite(expirationTimestamp) && expirationTimestamp <= Date.now()) {
        this.clearSession();
        return false;
      }
    }
    return true;
  }

  saveSession(token, expiresAt) {
    const replacementSession = {
      token: String(token || ""),
      expires_at: String(expiresAt || "")
    };
    if (!replacementSession.token) {
      throw new Error("The replacement session token is missing.");
    }

    // One storage write publishes the token and expiry together. In-memory
    // state changes only after that atomic browser-storage operation succeeds.
    sessionStorage.setItem(panelSessionStorageKey, JSON.stringify(replacementSession));
    this.token = replacementSession.token;
    this.expiresAt = replacementSession.expires_at;
    sessionStorage.removeItem(panelTokenStorageKey);
    sessionStorage.removeItem(panelTokenExpiryStorageKey);
  }

  clearSession() {
    this.token = "";
    this.expiresAt = "";
    sessionStorage.removeItem(panelSessionStorageKey);
    sessionStorage.removeItem(panelTokenStorageKey);
    sessionStorage.removeItem(panelTokenExpiryStorageKey);
    sessionStorage.removeItem(legacyPanelTokenStorageKey);
    sessionStorage.removeItem(legacyPanelTokenExpiryStorageKey);
  }

  async request(path, options = {}) {
    const requestHeaders = new Headers(options.headers || {});
    const hasBody = options.body !== undefined && options.body !== null;
    if (hasBody && !requestHeaders.has("Content-Type")) {
      requestHeaders.set("Content-Type", "application/json");
    }
    if (options.auth !== false && this.token) {
      requestHeaders.set("Authorization", `Bearer ${this.token}`);
    }

    let response;
    try {
      response = await fetch(`${normalizedAPIBase}${path}`, {
        method: options.method || "GET",
        headers: requestHeaders,
        body: hasBody && typeof options.body !== "string" ? JSON.stringify(options.body) : options.body,
        signal: options.signal,
        credentials: "same-origin"
      });
    } catch (requestError) {
      if (requestError?.name === "AbortError") {
        throw requestError;
      }
      throw new APIError("无法连接 Grok Search MCP 后端，请确认服务地址与运行状态。", 0);
    }

    const responseData = await parseResponseData(response);

    if (!response.ok) {
      if (response.status === 401 && options.auth !== false && options.clearSessionOnUnauthorized !== false) {
        this.clearSession();
      }
      const retryAfterHeader = response.headers.get("Retry-After");
      const retryAfterSeconds = retryAfterHeader ? Number(retryAfterHeader) : null;
      const upstreamMessage = typeof responseData === "object" ? responseData?.error : "";
      const errorCode = typeof responseData === "object" ? String(responseData?.code || "") : "";
      throw new APIError(
        translateBackendError(upstreamMessage, response.status),
        response.status,
        retryAfterSeconds,
        errorCode,
        typeof responseData === "object" ? responseData : null
      );
    }

    return responseData;
  }
}

async function parseResponseData(response) {
  if (response.status === 204) {
    return null;
  }

  const contentType = String(response.headers.get("Content-Type") || "").toLowerCase();
  if (contentType.includes("application/json")) {
    try {
      return await response.json();
    } catch (parseError) {
      if (parseError?.name === "AbortError") {
        throw parseError;
      }
      if (!response.ok) {
        return null;
      }
      throw new APIError("后端返回了无效的 JSON 响应。", response.status);
    }
  }

  return response.ok ? null : await response.text();
}

function buildCollectionPath(path, { cursor = "", limit = 50, since = "" } = {}) {
  const query = new URLSearchParams();
  if (since) {
    query.set("since", since);
  }
  if (cursor) {
    query.set("cursor", cursor);
  }
  if (limit) {
    query.set("limit", String(limit));
  }
  const queryString = query.toString();
  return queryString ? `${path}?${queryString}` : path;
}

function translateBackendError(message, status) {
  const normalizedMessage = String(message || "").trim();
  const messageTranslations = {
    "invalid credentials": "用户名或密码不正确。",
    "current password is incorrect": "当前密码不正确。",
    "API key limit reached": "已达到当前账户的 API 密钥数量上限。",
    "user disabled": "该账户已被管理员禁用。",
    "too many failed login attempts": "登录失败次数过多，请稍后再试。",
    "rate limit exceeded": "请求过于频繁，请稍后再试。",
    "username already taken": "该用户名已被使用。",
    "registration is disabled": "当前服务已关闭公开注册。",
    "registration proof is required": "请完成注册前的本地计算验证。",
    "registration proof is invalid": "注册计算验证无效，请重试。",
    "registration proof expired": "注册计算任务已过期，请重试。",
    "registration proof already used": "注册计算任务已使用，请重试。",
    "valid invite code is required": "请输入有效的邀请码。",
    "invite code is disabled": "该邀请码已被停用。",
    "invite code registration limit reached": "该邀请码的注册名额已用完。",
    "unauthorized": "会话已失效，请重新登录。",
    "api key not found": "未找到指定的 API 密钥。",
    "user not found": "未找到指定用户。",
    "tier not found": "未找到指定配额方案。",
    "invite code not found": "未找到指定邀请码。",
    "cannot delete current user": "不能删除当前登录账户。",
    "cannot disable current user": "不能禁用当前登录账户。",
    "cannot downgrade current user": "不能降低当前登录账户的角色。",
    "cannot remove last enabled admin": "系统必须保留至少一位启用中的管理员。",
    "tier is in use; reassign users first": "该配额方案仍有用户使用，请先重新分配用户。",
    "tier name already taken": "该配额方案名称已存在。",
    "registration_limit cannot be lower than current usage": "注册上限不能低于当前已使用次数。",
    "failed to list upstream models": "无法从上游获取模型列表。",
    "model listing is not configured": "后端尚未配置模型列表服务。"
  };

  if (messageTranslations[normalizedMessage]) {
    return messageTranslations[normalizedMessage];
  }
  if (normalizedMessage) {
    return normalizedMessage;
  }

  const statusMessages = {
    400: "请求内容不正确，请检查后重试。",
    401: "会话已失效，请重新登录。",
    403: "当前账户没有执行此操作的权限。",
    404: "请求的资源不存在。",
    409: "操作与当前资源状态冲突。",
    413: "请求内容过大。",
    429: "请求过于频繁，请稍后再试。",
    500: "服务暂时无法处理请求。",
    502: "上游 Grok 服务暂时不可用。",
    503: "服务尚未完成配置。"
  };
  return statusMessages[status] || `请求失败（HTTP ${status}）。`;
}

export const panelAPI = new PanelAPI();

function readStoredSession() {
  const serializedSession = sessionStorage.getItem(panelSessionStorageKey);
  if (!serializedSession) {
    return { token: "", expiresAt: "" };
  }
  try {
    const parsedSession = JSON.parse(serializedSession);
    return {
      token: typeof parsedSession?.token === "string" ? parsedSession.token : "",
      expiresAt: typeof parsedSession?.expires_at === "string" ? parsedSession.expires_at : ""
    };
  } catch {
    sessionStorage.removeItem(panelSessionStorageKey);
    return { token: "", expiresAt: "" };
  }
}

export function fetchRegistrationSettings() {
  return panelAPI.request("/panel/v1/auth/registration-settings", { auth: false });
}

export function fetchRegistrationChallenge() {
  return panelAPI.request("/panel/v1/auth/registration-challenge", {
    method: "POST",
    auth: false
  });
}

export function login(credentials) {
  return panelAPI.request("/panel/v1/auth/login", {
    method: "POST",
    auth: false,
    body: credentials
  });
}

export function register(registrationData) {
  return panelAPI.request("/panel/v1/auth/register", {
    method: "POST",
    auth: false,
    body: registrationData
  });
}

export function fetchCurrentUser(options = {}) {
  return panelAPI.request("/panel/v1/me", options);
}

export async function changePassword(passwords) {
  const replacementSession = await panelAPI.request("/panel/v1/me/change-password", {
    method: "POST",
    body: passwords,
    clearSessionOnUnauthorized: false
  });
  panelAPI.saveSession(replacementSession.token, replacementSession.expires_at);
  return replacementSession;
}

export async function revokeSessions() {
  const replacementSession = await panelAPI.request("/panel/v1/me/revoke-sessions", {
    method: "POST"
  });
  panelAPI.saveSession(replacementSession.token, replacementSession.expires_at);
  return replacementSession;
}

export function fetchOverviewHealth(options = {}) {
  return panelAPI.request("/panel/v1/overview/health", options);
}

export function fetchKeys(options = {}) {
  const { cursor = "", limit = 50, ...requestOptions } = options;
  return panelAPI.request(buildCollectionPath("/panel/v1/keys", { cursor, limit }), requestOptions);
}

export function createKey(keyData) {
  return panelAPI.request("/panel/v1/keys", { method: "POST", body: keyData });
}

export function revealKey(keyIdentifier) {
  return panelAPI.request(`/panel/v1/keys/${encodeURIComponent(keyIdentifier)}/reveal`, { method: "POST" });
}

export function updateKey(keyIdentifier, keyData) {
  return panelAPI.request(`/panel/v1/keys/${encodeURIComponent(keyIdentifier)}`, {
    method: "PATCH",
    body: keyData
  });
}

export function deleteKey(keyIdentifier) {
  return panelAPI.request(`/panel/v1/keys/${encodeURIComponent(keyIdentifier)}`, { method: "DELETE" });
}

export function fetchKeyUsage(keyIdentifier, options = {}) {
  return panelAPI.request(`/panel/v1/keys/${encodeURIComponent(keyIdentifier)}/usage`, options);
}

export function fetchUsage(since = "", options = {}) {
  const { cursor = "", limit = 50, ...requestOptions } = options;
  return panelAPI.request(buildCollectionPath("/panel/v1/usage", { since, cursor, limit }), requestOptions);
}

export function fetchUsageRecords(since = "", options = {}) {
  const { cursor = "", limit = 50, ...requestOptions } = options;
  return panelAPI.request(buildCollectionPath("/panel/v1/usage/records", { since, cursor, limit }), requestOptions);
}

export function fetchUsageRecordDetail(recordIdentifier, options = {}) {
	return panelAPI.request(`/panel/v1/usage/records/${encodeURIComponent(recordIdentifier)}`, options);
}

export function fetchAdminUsers(options = {}) {
  const { cursor = "", limit = 50, ...requestOptions } = options;
  return panelAPI.request(buildCollectionPath("/panel/v1/admin/users", { cursor, limit }), requestOptions);
}

export function updateAdminUser(userIdentifier, userData) {
  return panelAPI.request(`/panel/v1/admin/users/${encodeURIComponent(userIdentifier)}`, {
    method: "PATCH",
    body: userData
  });
}

export function deleteAdminUser(userIdentifier) {
  return panelAPI.request(`/panel/v1/admin/users/${encodeURIComponent(userIdentifier)}`, { method: "DELETE" });
}

export function fetchAdminUserUsage(userIdentifier, options = {}) {
  const { since = "", cursor = "", limit = 50, ...requestOptions } = options;
  return panelAPI.request(buildCollectionPath(
    `/panel/v1/admin/users/${encodeURIComponent(userIdentifier)}/usage`,
    { since, cursor, limit }
  ), requestOptions);
}

export function fetchTiers(options = {}) {
  const { cursor = "", limit = 50, ...requestOptions } = options;
  return panelAPI.request(buildCollectionPath("/panel/v1/admin/tiers", { cursor, limit }), requestOptions);
}

export async function fetchAllTiers(options = {}) {
  const { cursor = "", limit = 100, ...requestOptions } = options;
  const tiers = [];
  const seenCursors = new Set();
  const seenTierIdentifiers = new Set();
  let currentCursor = cursor;
  let firstPageResponse = null;

  while (true) {
    if (seenCursors.has(currentCursor)) {
      throw new Error("配额方案分页游标重复，无法加载完整列表。");
    }
    seenCursors.add(currentCursor);

    const pageResponse = await fetchTiers({
      ...requestOptions,
      cursor: currentCursor,
      limit
    });
    if (!Array.isArray(pageResponse?.tiers)) {
      throw new Error("配额方案分页响应无效，无法加载完整列表。");
    }
    if (!firstPageResponse) {
      firstPageResponse = pageResponse;
    }

    for (const tier of pageResponse.tiers) {
      const tierIdentifier = String(tier?.id || "");
      if (!tierIdentifier || seenTierIdentifiers.has(tierIdentifier)) {
        throw new Error("配额方案分页结果重复或缺少标识，无法加载完整列表。");
      }
      seenTierIdentifiers.add(tierIdentifier);
      tiers.push(tier);
    }

    if (!pageResponse.has_more) {
      return {
        ...firstPageResponse,
        tiers,
        next_cursor: "",
        has_more: false
      };
    }

    const nextCursor = pageResponse.next_cursor;
    if (typeof nextCursor !== "string" || nextCursor.length === 0) {
      throw new Error("配额方案分页缺少后续游标，无法加载完整列表。");
    }
    currentCursor = nextCursor;
  }
}

export function createTier(tierData) {
  return panelAPI.request("/panel/v1/admin/tiers", { method: "POST", body: tierData });
}

export function updateTier(tierIdentifier, tierData) {
  return panelAPI.request(`/panel/v1/admin/tiers/${encodeURIComponent(tierIdentifier)}`, {
    method: "PATCH",
    body: tierData
  });
}

export function deleteTier(tierIdentifier) {
  return panelAPI.request(`/panel/v1/admin/tiers/${encodeURIComponent(tierIdentifier)}`, { method: "DELETE" });
}

export function fetchInviteCodes(options = {}) {
	const { cursor = "", limit = 50, ...requestOptions } = options;
	return panelAPI.request(buildCollectionPath("/panel/v1/admin/invite-codes", { cursor, limit }), requestOptions);
}

export function fetchInviteCodeRedemptions(inviteIdentifier, options = {}) {
	const { cursor = "", limit = 50, ...requestOptions } = options;
	return panelAPI.request(
		buildCollectionPath(
			`/panel/v1/admin/invite-codes/${encodeURIComponent(inviteIdentifier)}/redemptions`,
			{ cursor, limit }
		),
		requestOptions
	);
}

export function createInviteCode(inviteCodeData) {
  return panelAPI.request("/panel/v1/admin/invite-codes", { method: "POST", body: inviteCodeData });
}

export function updateInviteCode(inviteIdentifier, inviteCodeData) {
  return panelAPI.request(`/panel/v1/admin/invite-codes/${encodeURIComponent(inviteIdentifier)}`, {
    method: "PATCH",
    body: inviteCodeData
  });
}

export function deleteInviteCode(inviteIdentifier) {
  return panelAPI.request(`/panel/v1/admin/invite-codes/${encodeURIComponent(inviteIdentifier)}`, { method: "DELETE" });
}

export function fetchSettings(options = {}) {
  return panelAPI.request("/panel/v1/admin/settings", options);
}

export function fetchOperationalMetrics(options = {}) {
  return panelAPI.request("/panel/v1/admin/operations/metrics", options);
}

export function updateSettings(settingsData) {
  return panelAPI.request("/panel/v1/admin/settings", { method: "PATCH", body: settingsData });
}

export function fetchModels() {
  return panelAPI.request("/panel/v1/admin/models");
}
