import { readPageFromLocation } from "./router.js";
import { COLLECTION_PAGE_SIZE, COLLECTION_PAGE_SIZE_OPTIONS } from "./pagination-config.js";

export { COLLECTION_PAGE_SIZE, COLLECTION_PAGE_SIZE_OPTIONS } from "./pagination-config.js";

function createPaginationState(pageSize = COLLECTION_PAGE_SIZE) {
  return {
    cursor: "",
    nextCursor: "",
    previousCursors: [],
    hasMore: false,
    pageSize,
    totalCount: 0,
    activeCount: 0,
    assignedUserCount: 0
  };
}

export const state = {
  authenticated: false,
  user: null,
  registrationMode: "free",
  authMode: "login",
  authBusy: false,
  authError: "",
  currentPage: readPageFromLocation(),
  pageLoading: false,
  refreshing: false,
  formBusy: false,
  settingsApplyWarning: null,
  sidebarOpen: false,
  modal: null,
  filters: {
    usagePeriod: "24h",
    userSearch: ""
  },
  pagination: {
    keys: createPaginationState(),
    users: createPaginationState(),
    tiers: createPaginationState(),
    invites: createPaginationState(),
    usageRecords: createPaginationState()
  },
  data: {
    keys: null,
    overviewHealth: null,
    overviewUsage: null,
    usage: null,
    users: null,
    tiers: null,
    invites: null,
    settings: null,
    operationsMetrics: null,
    models: null
  }
};

export function clearCachedData() {
  for (const dataKey of Object.keys(state.data)) {
    state.data[dataKey] = null;
  }
  state.settingsApplyWarning = null;
  for (const paginationKey of Object.keys(state.pagination)) {
    resetPagination(paginationKey, { preservePageSize: false });
  }
}

export function resetPagination(collectionName, options = {}) {
  if (!Object.hasOwn(state.pagination, collectionName)) {
    return;
  }
  const preservePageSize = options.preservePageSize !== false;
  const currentPageSize = preservePageSize
    ? state.pagination[collectionName]?.pageSize
    : COLLECTION_PAGE_SIZE;
  state.pagination[collectionName] = createPaginationState(currentPageSize);
}

export function setPaginationPageSize(collectionName, requestedPageSize) {
  const pagination = state.pagination[collectionName];
  const pageSize = Number(requestedPageSize);
  if (!pagination || !COLLECTION_PAGE_SIZE_OPTIONS.includes(pageSize) || pagination.pageSize === pageSize) {
    return false;
  }
  state.pagination[collectionName] = createPaginationState(pageSize);
  return true;
}

export function movePaginationCursor(collectionName, direction) {
  const pagination = state.pagination[collectionName];
  if (!pagination) {
    return null;
  }
  const snapshot = {
    cursor: pagination.cursor,
    previousCursors: [...pagination.previousCursors]
  };
  if (direction === "next") {
    if (!pagination.hasMore || !pagination.nextCursor) {
      return null;
    }
    pagination.previousCursors.push(pagination.cursor);
    pagination.cursor = pagination.nextCursor;
    return snapshot;
  }
  if (direction === "previous" && pagination.previousCursors.length > 0) {
    pagination.cursor = pagination.previousCursors.pop() || "";
    return snapshot;
  }
  return null;
}

export function restorePaginationCursor(collectionName, snapshot) {
  const pagination = state.pagination[collectionName];
  if (!pagination || !snapshot) {
    return;
  }
  pagination.cursor = snapshot.cursor;
  pagination.previousCursors = [...snapshot.previousCursors];
}

function commitPagination(collectionName, response) {
  const pagination = state.pagination[collectionName];
  if (!pagination) {
    return;
  }
  pagination.nextCursor = String(response?.next_cursor || "");
  pagination.hasMore = Boolean(response?.has_more && pagination.nextCursor);
  pagination.totalCount = Number(response?.total_count ?? pagination.totalCount ?? 0);
  pagination.activeCount = Number(response?.active_count ?? pagination.activeCount ?? 0);
  pagination.assignedUserCount = Number(response?.assigned_user_count ?? pagination.assignedUserCount ?? 0);
}

export function clearAuthenticatedState() {
  state.authenticated = false;
  state.user = null;
  state.authBusy = false;
  state.formBusy = false;
  state.pageLoading = false;
  state.refreshing = false;
  state.sidebarOpen = false;
  state.modal = null;
  state.authMode = "login";
  clearCachedData();
}

export function pageHasExistingData(page) {
  switch (page) {
    case "overview":
      return Boolean(state.data.overviewUsage && state.data.keys);
    case "usage":
      return Boolean(state.data.usage);
    case "keys":
      return Boolean(state.data.keys);
    case "users":
      return Boolean(state.data.users && state.data.tiers);
    case "tiers":
      return Boolean(state.data.tiers);
    case "invites":
      return Boolean(state.data.invites);
    case "settings":
      return Boolean(state.data.settings);
    case "operationsMetrics":
      return Boolean(state.data.operationsMetrics);
    case "account":
      return Boolean(state.user);
    default:
      return false;
  }
}

export function commitPageData(page, pageResult) {
  switch (page) {
    case "overview":
      state.user = pageResult.user;
      resetPagination("keys");
      state.data.keys = pageResult.keyResponse?.keys || [];
      commitPagination("keys", pageResult.keyResponse);
      state.data.overviewUsage = pageResult.overviewUsage;
      if (pageResult.settings) {
        state.data.settings = pageResult.settings;
        state.settingsApplyWarning = null;
      }
      break;
    case "keys":
      state.data.keys = pageResult.keyResponse?.keys || [];
      commitPagination("keys", pageResult.keyResponse);
      break;
    case "usage":
      state.data.usage = pageResult.usage;
      commitPagination("usageRecords", pageResult.usage);
      break;
    case "users":
      state.data.users = pageResult.userResponse?.users || [];
      state.data.tiers = pageResult.tierResponse?.tiers || [];
      commitPagination("users", pageResult.userResponse);
      break;
    case "tiers":
      state.data.tiers = pageResult.tierResponse?.tiers || [];
      commitPagination("tiers", pageResult.tierResponse);
      break;
    case "invites":
      state.data.invites = pageResult.inviteResponse?.invite_codes || [];
      commitPagination("invites", pageResult.inviteResponse);
      break;
    case "settings":
      state.data.settings = pageResult.settings;
      state.settingsApplyWarning = null;
      break;
    case "operationsMetrics":
      state.data.operationsMetrics = pageResult.operationsMetrics;
      break;
    case "account":
      state.user = pageResult.user;
      break;
    default:
      break;
  }
}

export function normalizeUsage(usage) {
  return {
    total_calls: Number(usage?.total_calls || 0),
    success_calls: Number(usage?.success_calls || 0),
    current_rpm: Number(usage?.current_rpm || 0),
    by_tool: usage?.by_tool || {},
    traffic_buckets: usage?.traffic_buckets || [],
    records: usage?.records || [],
    next_cursor: String(usage?.next_cursor || ""),
    has_more: Boolean(usage?.has_more)
  };
}

export function replaceItemByIdentifier(items, updatedItem) {
  if (!Array.isArray(items) || !updatedItem?.id) {
    return Array.isArray(items) ? [...items] : [];
  }

  return items.map((item) => item.id === updatedItem.id ? updatedItem : item);
}

export function removeItemByIdentifier(items, identifier) {
  return Array.isArray(items) ? items.filter((item) => item.id !== identifier) : [];
}

export function compareTiers(firstTier, secondTier) {
  return Number(firstTier.level || 0) - Number(secondTier.level || 0)
    || String(firstTier.name || "").localeCompare(String(secondTier.name || ""), "zh-CN");
}
