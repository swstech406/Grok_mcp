import {
  createInviteCode,
  createKey,
  createTier,
  deleteAdminUser,
  deleteInviteCode,
  deleteKey,
  deleteTier,
  fetchAdminUserUsage,
  fetchKeyUsage,
  fetchModels,
	fetchUsageRecordDetail,
  login,
  panelAPI,
  register,
  revealKey,
  updateAdminUser,
  updateInviteCode,
  updateKey,
  updateSettings,
  updateTier
} from "./api.js";
import { renderIcon } from "./components/icons.js";
import { showToast } from "./components/toast.js";
import { adminPages, availablePages, readPageFromLocation } from "./router.js";
import {
  clearAuthenticatedState,
  clearCachedData,
  COLLECTION_PAGE_SIZE,
  COLLECTION_PAGE_SIZE_OPTIONS,
  compareTiers,
  movePaginationCursor,
  normalizeUsage,
  removeItemByIdentifier,
  resetPagination,
  replaceItemByIdentifier,
  restorePaginationCursor,
  setPaginationPageSize,
  state
} from "./state.js";
import { createFormDataObject } from "./utils.js";

export function createApplicationEvents({
  applicationElement,
  modalRegionElement,
  renderApplication,
  renderModalRegion,
  loadCurrentPage,
  abortCurrentPageLoad,
  normalizeCurrentPageForRole,
  handleSessionError
}) {
  let activeModalRequestController = null;
  let activeModalRequestIdentifier = 0;

  function registerEventHandlers() {
    applicationElement.addEventListener("click", handleApplicationClick);
    applicationElement.addEventListener("change", handleApplicationChange);
    applicationElement.addEventListener("submit", handleFormSubmit);
    applicationElement.addEventListener("input", handleApplicationInput);
    modalRegionElement.addEventListener("click", handleModalClick);
    modalRegionElement.addEventListener("change", handleModalChange);
    modalRegionElement.addEventListener("submit", handleFormSubmit);
    window.addEventListener("hashchange", handleLocationChange);
    document.addEventListener("keydown", handleGlobalKeydown);
  }

  async function handleApplicationClick(event) {
    const actionElement = event.target.closest("[data-action]");
    if (!actionElement) {
      return;
    }

    const action = actionElement.dataset.action;
    switch (action) {
      case "switch-auth":
        state.authMode = actionElement.dataset.mode || "login";
        state.authError = "";
        renderApplication();
        break;
      case "toggle-password":
        togglePasswordVisibility(actionElement);
        break;
      case "navigate":
        navigateToPage(actionElement.dataset.page);
        break;
      case "toggle-sidebar":
        state.sidebarOpen = !state.sidebarOpen;
        renderApplication();
        break;
      case "close-sidebar":
        state.sidebarOpen = false;
        renderApplication();
        break;
      case "logout":
        abortCurrentPageLoad();
        logout();
        break;
      case "refresh-page":
        resetCurrentPagePagination();
        await loadCurrentPage({ refreshing: true });
        break;
      case "change-list-page":
        await changeListPage(actionElement.dataset.list, actionElement.dataset.direction);
        break;
      case "open-create-key":
        openModal({ type: "createKey", busy: false, error: "" });
        break;
      case "open-edit-key":
        openEditKeyModal(actionElement.dataset.id);
        break;
      case "copy-key":
        await copyAPIKey(actionElement.dataset.id, actionElement);
        break;
      case "open-key-usage":
        await openKeyUsageModal(actionElement.dataset.id);
        break;
      case "confirm-delete-key":
        confirmDeleteKey(actionElement.dataset.id);
        break;
      case "set-usage-period":
        state.filters.usagePeriod = actionElement.dataset.period || "24h";
        state.data.usage = null;
        resetPagination("usageRecords");
        await loadCurrentPage();
        break;
      case "view-debug-json":
		await openDebugJSONModal(actionElement.dataset.recordId);
        break;
      case "open-edit-user":
        openEditUserModal(actionElement.dataset.id);
        break;
      case "open-user-usage":
        await openUserUsageModal(actionElement.dataset.id);
        break;
      case "confirm-delete-user":
        confirmDeleteUser(actionElement.dataset.id);
        break;
      case "open-create-tier":
        openModal({ type: "createTier", busy: false, error: "" });
        break;
      case "open-edit-tier":
        openEditTierModal(actionElement.dataset.id);
        break;
      case "confirm-delete-tier":
        confirmDeleteTier(actionElement.dataset.id);
        break;
      case "open-create-invite":
        openModal({ type: "createInvite", busy: false, error: "" });
        break;
      case "toggle-invite":
        await toggleInviteCode(actionElement.dataset.id);
        break;
      case "copy-value":
        await copyValue(actionElement.dataset.value || "");
        break;
      case "confirm-delete-invite":
        confirmDeleteInviteCode(actionElement.dataset.id);
        break;
      case "load-models":
        await loadAvailableModels(actionElement);
        break;
      default:
        break;
    }
  }

  async function handleModalClick(event) {
    const actionElement = event.target.closest("[data-action]");
    if (!actionElement) {
      return;
    }

    const action = actionElement.dataset.action;
    if (action === "modal-backdrop" && event.target !== actionElement) {
      return;
    }

    switch (action) {
      case "close-modal":
      case "modal-backdrop":
        if (!state.modal?.busy) {
          closeModal();
        }
        break;
      case "copy-value":
        await copyValue(actionElement.dataset.value || "");
        break;
      case "view-debug-json":
		await openDebugJSONModal(actionElement.dataset.recordId);
        break;
      case "view-user-usage-logs":
        openUserUsageLogsModal();
        break;
      case "view-user-usage-summary":
        openUserUsageSummaryModal();
        break;
      case "change-user-usage-page":
        await changeUserUsagePage(actionElement.dataset.direction);
        break;
      case "copy-debug-json":
        if (state.modal?.type === "debugJSON") {
          const debugRecord = state.modal.record || {};
          let completeDebugJSON = String(debugRecord.debug_json || "");
          try {
            const parsedDebugJSON = JSON.parse(completeDebugJSON);
            const isObject = parsedDebugJSON !== null && typeof parsedDebugJSON === "object" && !Array.isArray(parsedDebugJSON);
            if (isObject) {
              if (typeof debugRecord.debug_request_body === "string") {
                const requestMetadata = parsedDebugJSON.request !== null
                  && typeof parsedDebugJSON.request === "object"
                  && !Array.isArray(parsedDebugJSON.request)
                  ? parsedDebugJSON.request
                  : {};
                parsedDebugJSON.request = { ...requestMetadata, body: debugRecord.debug_request_body };
              }
              if (typeof debugRecord.debug_response_body === "string") {
                const responseMetadata = parsedDebugJSON.response !== null
                  && typeof parsedDebugJSON.response === "object"
                  && !Array.isArray(parsedDebugJSON.response)
                  ? parsedDebugJSON.response
                  : {};
                parsedDebugJSON.response = { ...responseMetadata, body: debugRecord.debug_response_body };
              }
              completeDebugJSON = JSON.stringify(parsedDebugJSON, null, 2);
            }
          } catch {
            // Preserve legacy behavior for malformed metadata.
          }
          await copyValue(completeDebugJSON);
        }
        break;
      case "execute-confirm":
        await executeConfirmedAction();
        break;
      default:
        break;
    }
  }

  async function handleApplicationChange(event) {
    const actionElement = event.target.closest('[data-action="change-list-page-size"]');
    if (!actionElement) {
      return;
    }

    const collectionName = actionElement.dataset.list;
    if (collectionName !== "usageRecords" || state.currentPage !== "usage") {
      return;
    }

    const previousPageSize = state.pagination.usageRecords.pageSize;
    if (!setPaginationPageSize(collectionName, actionElement.value)) {
      return;
    }

    const loaded = await loadCurrentPage({ refreshing: true });
    if (!loaded && state.authenticated) {
      setPaginationPageSize(collectionName, previousPageSize);
      renderApplication();
    }
  }

  async function handleModalChange(event) {
    const actionElement = event.target.closest('[data-action="change-user-usage-page-size"]');
    if (!actionElement) {
      return;
    }
    await changeUserUsagePageSize(actionElement.value);
  }

  function handleApplicationInput(event) {
    if (!event.target.matches('[data-filter="user-search"]')) {
      return;
    }

    const selectionStart = event.target.selectionStart;
    state.filters.userSearch = event.target.value;
    renderApplication();
    const replacementInput = applicationElement.querySelector('[data-filter="user-search"]');
    replacementInput?.focus();
    replacementInput?.setSelectionRange(selectionStart, selectionStart);
  }

  async function handleFormSubmit(event) {
    const formElement = event.target.closest("form[data-form]");
    if (!formElement) {
      return;
    }
    event.preventDefault();

    if (!formElement.reportValidity()) {
      return;
    }

    switch (formElement.dataset.form) {
      case "login":
        await submitLogin(formElement);
        break;
      case "register":
        await submitRegistration(formElement);
        break;
      case "create-key":
        await submitCreateKey(formElement);
        break;
      case "edit-key":
        await submitEditKey(formElement);
        break;
      case "edit-user":
        await submitEditUser(formElement);
        break;
      case "create-tier":
        await submitTier(formElement, false);
        break;
      case "edit-tier":
        await submitTier(formElement, true);
        break;
      case "create-invite":
        await submitCreateInviteCode(formElement);
        break;
      case "settings":
        await submitSettings(formElement);
        break;
      default:
        break;
    }
  }

  function handleLocationChange() {
    if (!state.authenticated) {
      return;
    }
    const requestedPage = readPageFromLocation();
    if (requestedPage === state.currentPage) {
      return;
    }
    state.currentPage = requestedPage;
    normalizeCurrentPageForRole();
    state.sidebarOpen = false;
    abortCurrentModalRequest();
    state.modal = null;
    loadCurrentPage();
  }

  function resetCurrentPagePagination() {
    const paginationByPage = {
      overview: "keys",
      keys: "keys",
      usage: "usageRecords",
      users: "users",
      tiers: "tiers",
      invites: "invites"
    };
    const collectionName = paginationByPage[state.currentPage];
    if (collectionName) {
      resetPagination(collectionName);
    }
  }

  async function changeListPage(collectionName, direction) {
    const pageByCollection = {
      keys: "keys",
      users: "users",
      tiers: "tiers",
      invites: "invites",
      usageRecords: "usage"
    };
    if (pageByCollection[collectionName] !== state.currentPage) {
      return;
    }

    const cursorSnapshot = movePaginationCursor(collectionName, direction);
    if (!cursorSnapshot) {
      return;
    }
    const loaded = await loadCurrentPage({ refreshing: true });
    if (!loaded && state.authenticated) {
      restorePaginationCursor(collectionName, cursorSnapshot);
      renderApplication();
    }
  }

  function handleGlobalKeydown(event) {
    if (event.key === "Escape" && state.modal && !state.modal.busy) {
      closeModal();
    }
  }

  function togglePasswordVisibility(actionElement) {
    const passwordInput = document.getElementById(actionElement.dataset.target);
    if (!passwordInput) {
      return;
    }
    const shouldShowPassword = passwordInput.type === "password";
    passwordInput.type = shouldShowPassword ? "text" : "password";
    actionElement.innerHTML = renderIcon(shouldShowPassword ? "eyeOff" : "eye");
    passwordInput.focus();
  }

  function navigateToPage(page) {
    if (!availablePages.has(page)) {
      return;
    }
    if (adminPages.has(page) && state.user?.role !== "admin") {
      showToast("权限不足", "当前账户无法访问系统管理页面。", "error");
      return;
    }

    state.sidebarOpen = false;
    abortCurrentModalRequest();
    state.modal = null;
    if (state.currentPage === page) {
      renderApplication();
      return;
    }
    window.location.hash = page;
  }

  function logout() {
    abortCurrentModalRequest();
    panelAPI.clearSession();
    clearAuthenticatedState();
    state.authError = "";
    state.currentPage = "overview";
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}`);
    renderApplication();
    showToast("已退出", "当前会话已从浏览器标签页中清除。", "success");
  }

  async function submitLogin(formElement) {
    const credentials = createFormDataObject(formElement);
    state.authBusy = true;
    state.authError = "";
    renderApplication();

    try {
      const loginResponse = await login({
        username: String(credentials.username || "").trim(),
        password: String(credentials.password || "")
      });
      panelAPI.saveSession(loginResponse.token, loginResponse.expires_at);
      state.user = loginResponse.user;
      state.authenticated = true;
      state.currentPage = "overview";
      state.authBusy = false;
      state.authError = "";
      clearCachedData();
      window.history.replaceState(null, "", "#overview");
      renderApplication();
      showToast("欢迎回来", `已以 ${state.user.username} 的身份安全登录。`, "success");
      await loadCurrentPage();
    } catch (error) {
      state.authBusy = false;
      state.authError = withRetryAfter(getErrorMessage(error), error);
      renderApplication();
    }
  }

  async function submitRegistration(formElement) {
    const registrationData = createFormDataObject(formElement);
    state.authBusy = true;
    state.authError = "";
    renderApplication();

    try {
      await register({
        username: String(registrationData.username || "").trim(),
        password: String(registrationData.password || ""),
        ...(state.registrationMode === "invite"
          ? { invite_code: String(registrationData.invite_code || "").trim() }
          : {})
      });
      state.authBusy = false;
      state.authMode = "login";
      state.authError = "";
      renderApplication();
      showToast("账户已创建", "请使用刚刚设置的用户名和密码登录。", "success");
    } catch (error) {
      state.authBusy = false;
      state.authError = withRetryAfter(getErrorMessage(error), error);
      renderApplication();
    }
  }

  function abortCurrentModalRequest() {
    activeModalRequestController?.abort();
    activeModalRequestController = null;
    activeModalRequestIdentifier += 1;
  }

  function startModalRequest() {
    abortCurrentModalRequest();
    const requestController = new AbortController();
    const requestIdentifier = activeModalRequestIdentifier;
    activeModalRequestController = requestController;
    return { requestController, requestIdentifier };
  }

  function isCurrentModalRequest(requestContext) {
    return requestContext.requestIdentifier === activeModalRequestIdentifier
      && activeModalRequestController === requestContext.requestController;
  }

  function finishModalRequest(requestContext) {
    if (activeModalRequestController === requestContext.requestController) {
      activeModalRequestController = null;
    }
  }

  function openModal(modalState) {
    abortCurrentModalRequest();
    state.modal = modalState;
    renderModalRegion();
    window.requestAnimationFrame(() => {
      modalRegionElement.querySelector("[autofocus]")?.focus();
    });
  }

	async function openDebugJSONModal(recordIdentifier) {
		const pageUsageRecords = state.data.usage?.records || [];
		const modalUsageRecords = state.modal?.usage?.records || [];
		const matchingRecord = [...modalUsageRecords, ...pageUsageRecords].find(
			(usageRecord) => String(usageRecord.id) === String(recordIdentifier)
    );

    if (!matchingRecord?.debug_json) {
      showToast("调试详情不可用", "该调用没有可展示的调试数据，请刷新后重试。", "error");
      return;
    }

		openModal({
			type: "debugJSON",
			record: { ...matchingRecord },
			loading: true,
			busy: false,
			error: ""
		});

		const requestContext = startModalRequest();
		try {
			const recordDetail = await fetchUsageRecordDetail(recordIdentifier, {
				signal: requestContext.requestController.signal
			});
			if (isCurrentModalRequest(requestContext)
				&& state.modal?.type === "debugJSON"
				&& String(state.modal.record?.id) === String(recordIdentifier)) {
				state.modal.record = recordDetail;
				state.modal.loading = false;
				renderModalRegion();
			}
		} catch (error) {
			if (error?.name !== "AbortError"
				&& isCurrentModalRequest(requestContext)
				&& !handleSessionError(error)
				&& state.modal?.type === "debugJSON"
				&& String(state.modal.record?.id) === String(recordIdentifier)) {
				state.modal.loading = false;
				state.modal.error = getErrorMessage(error);
				renderModalRegion();
			}
		} finally {
			finishModalRequest(requestContext);
		}
	}

  function closeModal() {
    abortCurrentModalRequest();
    state.modal = null;
    renderModalRegion();
  }

  function setModalBusy(busy, error = "") {
    if (!state.modal) {
      return;
    }
    state.modal.busy = busy;
    state.modal.error = error;
    renderModalRegion();
  }

  async function submitCreateKey(formElement) {
    const formData = createFormDataObject(formElement);
    setModalBusy(true);
    try {
      const createResponse = await createKey({ name: String(formData.name || "").trim() });
      state.data.keys = [createResponse.key, ...(state.data.keys || [])].slice(0, COLLECTION_PAGE_SIZE);
      state.modal = {
        type: "secret",
        secretType: "key",
        secret: createResponse.api_key,
        title: "API 密钥已创建",
        subtitle: createResponse.key?.name || "创建成功"
      };
      renderApplication();
    } catch (error) {
      if (!handleSessionError(error)) {
        setModalBusy(false, getErrorMessage(error));
      }
    }
  }

  async function copyAPIKey(keyIdentifier, actionElement) {
    if (!keyIdentifier) {
      showToast("无法复制密钥", "密钥标识缺失，请刷新页面后重试。", "error");
      return;
    }

    actionElement.disabled = true;
    try {
      const revealResponse = await revealKey(keyIdentifier);
      await copyValue(String(revealResponse?.api_key || ""));
    } catch (error) {
      if (!handleSessionError(error)) {
        showToast("无法复制密钥", getErrorMessage(error), "error");
      }
    } finally {
      actionElement.disabled = false;
    }
  }

  function openEditKeyModal(keyIdentifier) {
    const apiKey = (state.data.keys || []).find((candidateKey) => candidateKey.id === keyIdentifier);
    if (!apiKey) {
      showToast("密钥不存在", "请刷新页面后重试。", "error");
      return;
    }
    openModal({ type: "editKey", data: { ...apiKey }, busy: false, error: "" });
  }

  async function submitEditKey(formElement) {
    const keyIdentifier = formElement.dataset.id;
    const formData = createFormDataObject(formElement);
    setModalBusy(true);
    try {
      const updatedKey = await updateKey(keyIdentifier, {
        name: String(formData.name || "").trim(),
        enabled: formElement.elements.enabled.checked
      });
      state.data.keys = replaceItemByIdentifier(state.data.keys, updatedKey);
      closeModal();
      renderApplication();
      showToast("密钥已更新", "名称与访问状态已保存。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        setModalBusy(false, getErrorMessage(error));
      }
    }
  }

  async function openKeyUsageModal(keyIdentifier) {
    const apiKey = (state.data.keys || []).find((candidateKey) => candidateKey.id === keyIdentifier);
    openModal({ type: "keyUsage", keyIdentifier, title: apiKey?.name || "密钥调用分析", loading: true, usage: null });
    const requestContext = startModalRequest();
    try {
      const usage = await fetchKeyUsage(keyIdentifier, { signal: requestContext.requestController.signal });
      if (isCurrentModalRequest(requestContext)
        && state.modal?.type === "keyUsage"
        && state.modal.keyIdentifier === keyIdentifier) {
        state.modal.loading = false;
        state.modal.usage = normalizeUsage(usage);
        renderModalRegion();
      }
    } catch (error) {
      if (error?.name !== "AbortError" && isCurrentModalRequest(requestContext) && !handleSessionError(error)) {
        closeModal();
        showToast("无法加载密钥用量", getErrorMessage(error), "error");
      }
    } finally {
      finishModalRequest(requestContext);
    }
  }

  function confirmDeleteKey(keyIdentifier) {
    const apiKey = (state.data.keys || []).find((candidateKey) => candidateKey.id === keyIdentifier);
    openModal({
      type: "confirm",
      confirmAction: "deleteKey",
      identifier: keyIdentifier,
      title: "删除 API 密钥",
      message: `删除“${apiKey?.name || "该密钥"}”后，使用它的 MCP 客户端将立即无法访问服务。此操作无法撤销。`,
      confirmLabel: "删除密钥",
      busy: false,
      error: ""
    });
  }

  function openEditUserModal(userIdentifier) {
    const user = (state.data.users || []).find((candidateUser) => candidateUser.id === userIdentifier);
    if (!user) {
      showToast("用户不存在", "请刷新页面后重试。", "error");
      return;
    }
    openModal({ type: "editUser", data: { ...user }, busy: false, error: "" });
  }

  async function submitEditUser(formElement) {
    const userIdentifier = formElement.dataset.id;
    const updatePayload = {
      tier_id: String(formElement.elements.tier_id.value || "").trim(),
      revoke_tokens: formElement.elements.revoke_tokens.checked
    };
    if (!formElement.elements.role.disabled) {
      updatePayload.role = formElement.elements.role.value;
    }
    if (!formElement.elements.enabled.disabled) {
      updatePayload.enabled = formElement.elements.enabled.checked;
    }

    setModalBusy(true);
    try {
      const updatedUser = await updateAdminUser(userIdentifier, updatePayload);
      state.data.users = replaceItemByIdentifier(state.data.users, updatedUser);
      if (updatedUser.id === state.user?.id) {
        state.user = updatedUser;
      }
      closeModal();
      renderApplication();
      showToast("用户已更新", "角色、等级与会话策略已应用。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        setModalBusy(false, getErrorMessage(error));
      }
    }
  }

  async function openUserUsageModal(userIdentifier) {
    const user = (state.data.users || []).find((candidateUser) => candidateUser.id === userIdentifier);
    openModal({
      type: "userUsage",
      userIdentifier,
      username: user?.username || "用户",
      loading: true,
      loadingRecords: false,
      usage: null,
      recentRecords: null,
      pageSize: 20,
      cursor: "",
      nextCursor: "",
      previousCursors: [],
      hasMore: false
    });
    await loadAdminUserUsagePage({ closeModalOnError: true });
  }

  async function loadAdminUserUsagePage(options = {}) {
    const modal = state.modal;
    if (!modal || !["userUsage", "userUsageLogs"].includes(modal.type)) {
      return false;
    }

    const userIdentifier = modal.userIdentifier;
    const requestedCursor = modal.cursor;
    const requestedPageSize = modal.pageSize;
    const requestContext = startModalRequest();
    try {
      const usageResponse = await fetchAdminUserUsage(userIdentifier, {
        signal: requestContext.requestController.signal,
        cursor: requestedCursor,
        limit: requestedPageSize
      });
      if (isCurrentModalRequest(requestContext)
        && ["userUsage", "userUsageLogs"].includes(state.modal?.type)
        && state.modal.userIdentifier === userIdentifier) {
        const usage = normalizeUsage(usageResponse);
        state.modal.loading = false;
        state.modal.loadingRecords = false;
        state.modal.usage = usage;
        state.modal.nextCursor = usage.next_cursor;
        state.modal.hasMore = Boolean(usage.has_more && usage.next_cursor);
        if (!Array.isArray(state.modal.recentRecords)) {
          state.modal.recentRecords = usage.records.slice(0, 8);
        }
        renderModalRegion();
        return true;
      }
    } catch (error) {
      if (error?.name !== "AbortError" && isCurrentModalRequest(requestContext) && !handleSessionError(error)) {
        if (options.closeModalOnError) {
          closeModal();
        } else if (["userUsage", "userUsageLogs"].includes(state.modal?.type)) {
          state.modal.loading = false;
          state.modal.loadingRecords = false;
          renderModalRegion();
        }
        showToast("无法加载用户用量", getErrorMessage(error), "error");
      }
    } finally {
      finishModalRequest(requestContext);
    }
    return false;
  }

  function openUserUsageLogsModal() {
    if (state.modal?.type !== "userUsage" || state.modal.loading || !state.modal.usage) {
      return;
    }

    state.modal.type = "userUsageLogs";
    renderModalRegion();
  }

  function openUserUsageSummaryModal() {
    if (state.modal?.type !== "userUsageLogs") {
      return;
    }

    state.modal.type = "userUsage";
    renderModalRegion();
  }

  async function changeUserUsagePage(direction) {
    if (state.modal?.type !== "userUsageLogs" || state.modal.loadingRecords) {
      return;
    }

    const paginationSnapshot = createUserUsagePaginationSnapshot(state.modal);
    if (direction === "next") {
      if (!state.modal.hasMore || !state.modal.nextCursor) {
        return;
      }
      state.modal.previousCursors.push(state.modal.cursor);
      state.modal.cursor = state.modal.nextCursor;
    } else if (direction === "previous" && state.modal.previousCursors.length > 0) {
      state.modal.cursor = state.modal.previousCursors.pop() || "";
    } else {
      return;
    }

    state.modal.loadingRecords = true;
    renderModalRegion();
    const loaded = await loadAdminUserUsagePage();
    if (!loaded && state.modal?.type === "userUsageLogs") {
      restoreUserUsagePaginationSnapshot(state.modal, paginationSnapshot);
      renderModalRegion();
    }
  }

  async function changeUserUsagePageSize(requestedPageSize) {
    if (state.modal?.type !== "userUsageLogs" || state.modal.loadingRecords) {
      return;
    }

    const pageSize = Number(requestedPageSize);
    if (!COLLECTION_PAGE_SIZE_OPTIONS.includes(pageSize) || pageSize === state.modal.pageSize) {
      return;
    }

    const paginationSnapshot = createUserUsagePaginationSnapshot(state.modal);
    state.modal.pageSize = pageSize;
    state.modal.cursor = "";
    state.modal.nextCursor = "";
    state.modal.previousCursors = [];
    state.modal.hasMore = false;
    state.modal.loadingRecords = true;
    renderModalRegion();
    const loaded = await loadAdminUserUsagePage();
    if (!loaded && state.modal?.type === "userUsageLogs") {
      restoreUserUsagePaginationSnapshot(state.modal, paginationSnapshot);
      renderModalRegion();
    }
  }

  function createUserUsagePaginationSnapshot(modal) {
    return {
      cursor: modal.cursor,
      nextCursor: modal.nextCursor,
      previousCursors: [...modal.previousCursors],
      hasMore: modal.hasMore,
      pageSize: modal.pageSize,
      usage: modal.usage
    };
  }

  function restoreUserUsagePaginationSnapshot(modal, snapshot) {
    modal.cursor = snapshot.cursor;
    modal.nextCursor = snapshot.nextCursor;
    modal.previousCursors = [...snapshot.previousCursors];
    modal.hasMore = snapshot.hasMore;
    modal.pageSize = snapshot.pageSize;
    modal.usage = snapshot.usage;
    modal.loadingRecords = false;
  }

  function confirmDeleteUser(userIdentifier) {
    const user = (state.data.users || []).find((candidateUser) => candidateUser.id === userIdentifier);
    openModal({
      type: "confirm",
      confirmAction: "deleteUser",
      identifier: userIdentifier,
      title: "删除用户",
      message: `删除“${user?.username || "该用户"}”会同时删除其全部 API 密钥与调用日志，且无法恢复。`,
      confirmLabel: "删除用户",
      busy: false,
      error: ""
    });
  }

  function openEditTierModal(tierIdentifier) {
    const tier = (state.data.tiers || []).find((candidateTier) => candidateTier.id === tierIdentifier);
    if (!tier) {
      showToast("等级不存在", "请刷新页面后重试。", "error");
      return;
    }
    openModal({ type: "editTier", data: { ...tier }, busy: false, error: "" });
  }

  async function submitTier(formElement, isEdit) {
    const formData = createFormDataObject(formElement);
    const tierPayload = {
      name: String(formData.name || "").trim(),
      level: Number(formData.level),
      rpm: Number(formData.rpm),
      success_limit: Number(formData.success_limit)
    };
    const tierIdentifier = formElement.dataset.id;
    setModalBusy(true);

    try {
      const tier = isEdit
        ? await updateTier(tierIdentifier, tierPayload)
        : await createTier(tierPayload);
      if (isEdit) {
        state.data.tiers = replaceItemByIdentifier(state.data.tiers, tier);
      } else {
        state.data.tiers = [...(state.data.tiers || []), tier]
          .sort(compareTiers)
          .slice(0, COLLECTION_PAGE_SIZE);
      }
      closeModal();
      renderApplication();
      showToast(isEdit ? "方案已更新" : "方案已创建", "新的配额方案已可以分配给用户。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        setModalBusy(false, getErrorMessage(error));
      }
    }
  }

  function confirmDeleteTier(tierIdentifier) {
    const tier = (state.data.tiers || []).find((candidateTier) => candidateTier.id === tierIdentifier);
    openModal({
      type: "confirm",
      confirmAction: "deleteTier",
      identifier: tierIdentifier,
      title: "删除配额方案",
      message: `将永久删除“${tier?.name || "该方案"}”。仍有用户使用的方案无法删除。`,
      confirmLabel: "删除方案",
      busy: false,
      error: ""
    });
  }

  async function submitCreateInviteCode(formElement) {
    const formData = createFormDataObject(formElement);
    setModalBusy(true);
    try {
      const createResponse = await createInviteCode({
        registration_limit: Number(formData.registration_limit)
      });
      state.data.invites = [createResponse.invite_code, ...(state.data.invites || [])]
        .slice(0, COLLECTION_PAGE_SIZE);
      state.modal = {
        type: "secret",
        secretType: "invite",
        secret: createResponse.code,
        title: "邀请码已创建",
        subtitle: `最多可注册 ${createResponse.invite_code?.registration_limit || 1} 位用户`
      };
      renderApplication();
    } catch (error) {
      if (!handleSessionError(error)) {
        setModalBusy(false, getErrorMessage(error));
      }
    }
  }

  async function toggleInviteCode(inviteIdentifier) {
    const inviteCode = (state.data.invites || []).find((candidateInvite) => candidateInvite.id === inviteIdentifier);
    if (!inviteCode) {
      return;
    }
    try {
      const updatedInviteCode = await updateInviteCode(inviteIdentifier, { enabled: !inviteCode.enabled });
      state.data.invites = replaceItemByIdentifier(state.data.invites, updatedInviteCode);
      renderApplication();
      showToast(updatedInviteCode.enabled ? "邀请码已启用" : "邀请码已停用", "注册策略已即时更新。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        showToast("操作失败", getErrorMessage(error), "error");
      }
    }
  }

  function confirmDeleteInviteCode(inviteIdentifier) {
    const inviteCode = (state.data.invites || []).find((candidateInvite) => candidateInvite.id === inviteIdentifier);
    openModal({
      type: "confirm",
      confirmAction: "deleteInvite",
      identifier: inviteIdentifier,
      title: "删除邀请码",
      message: `删除“${inviteCode?.code_prefix || "该邀请码"}”后，尚未使用的注册名额也会立即失效。`,
      confirmLabel: "删除邀请码",
      busy: false,
      error: ""
    });
  }

  async function submitSettings(formElement) {
    const formData = createFormDataObject(formElement);
    const globalSearchConcurrency = Number(formData.mcp_global_search_concurrency);
    const userSearchConcurrency = Number(formData.mcp_user_search_concurrency);
    if (userSearchConcurrency > globalSearchConcurrency) {
      const userConcurrencyInput = formElement.elements.mcp_user_search_concurrency;
      userConcurrencyInput.setCustomValidity("单用户搜索并发不得超过全局搜索并发。");
      userConcurrencyInput.reportValidity();
      userConcurrencyInput.setCustomValidity("");
      return;
    }
    const settingsPayload = {
      cpa_base_url: String(formData.cpa_base_url || "").trim(),
      upstream_protocol: String(formData.upstream_protocol || ""),
      model: String(formData.model || "").trim(),
      timeout_seconds: Number(formData.timeout_seconds),
      mcp_global_search_concurrency: globalSearchConcurrency,
      mcp_user_search_concurrency: userSearchConcurrency,
      proxy_url: String(formData.proxy_url || "").trim(),
      proxy_enabled: formElement.elements.proxy_enabled.checked,
      registration_mode: formElement.elements.registration_mode.value,
      debug: formElement.elements.debug.checked
    };
    const apiKey = String(formData.cpa_api_key || "").trim();
    if (apiKey) {
      settingsPayload.cpa_api_key = apiKey;
    }

    state.formBusy = true;
    renderApplication();
    try {
      state.data.settings = await updateSettings(settingsPayload);
      state.registrationMode = state.data.settings.registration_mode || state.registrationMode;
      state.formBusy = false;
      renderApplication();
      showToast("设置已应用", "上游客户端和搜索并发控制已使用新的运行时配置。", "success");
    } catch (error) {
      state.formBusy = false;
      if (!handleSessionError(error)) {
        renderApplication();
        showToast("保存失败", getErrorMessage(error), "error");
      }
    }
  }

  async function loadAvailableModels(actionElement) {
    const previousContent = actionElement.innerHTML;
    actionElement.disabled = true;
    actionElement.innerHTML = `${renderIcon("refresh")} 正在拉取`;
    try {
      const modelResponse = await fetchModels();
      state.data.models = modelResponse?.models || [];
      renderApplication();
      showToast("模型列表已更新", `发现 ${state.data.models.length} 个可用 Grok 模型。`, "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        actionElement.disabled = false;
        actionElement.innerHTML = previousContent;
        showToast("模型加载失败", getErrorMessage(error), "error");
      }
    }
  }

  async function executeConfirmedAction() {
    if (!state.modal || state.modal.type !== "confirm") {
      return;
    }
    const { confirmAction, identifier } = state.modal;
    setModalBusy(true);

    try {
      switch (confirmAction) {
        case "deleteKey":
          await deleteKey(identifier);
          state.data.keys = removeItemByIdentifier(state.data.keys, identifier);
          break;
        case "deleteUser":
          await deleteAdminUser(identifier);
          state.data.users = removeItemByIdentifier(state.data.users, identifier);
          break;
        case "deleteTier":
          await deleteTier(identifier);
          state.data.tiers = removeItemByIdentifier(state.data.tiers, identifier);
          break;
        case "deleteInvite":
          await deleteInviteCode(identifier);
          state.data.invites = removeItemByIdentifier(state.data.invites, identifier);
          break;
        default:
          throw new Error("未知的确认操作。");
      }
      closeModal();
      renderApplication();
      showToast("删除成功", "资源已从服务中永久移除。", "success");
    } catch (error) {
      if (!handleSessionError(error)) {
        setModalBusy(false, getErrorMessage(error));
      }
    }
  }

  async function copyValue(value) {
    if (!value) {
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
      showToast("已复制", "内容已写入剪贴板。", "success");
    } catch {
      const fallbackTextArea = document.createElement("textarea");
      fallbackTextArea.value = value;
      fallbackTextArea.setAttribute("readonly", "");
      fallbackTextArea.style.position = "fixed";
      fallbackTextArea.style.opacity = "0";
      document.body.appendChild(fallbackTextArea);
      fallbackTextArea.select();
      const copySucceeded = document.execCommand("copy");
      fallbackTextArea.remove();
      showToast(
        copySucceeded ? "已复制" : "复制失败",
        copySucceeded ? "内容已写入剪贴板。" : "请手动选择并复制内容。",
        copySucceeded ? "success" : "error"
      );
    }
  }

  return { register: registerEventHandlers };
}

function getErrorMessage(error) {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return "发生未知错误，请稍后重试。";
}

function withRetryAfter(message, error) {
  if (Number.isFinite(error?.retryAfterSeconds) && error.retryAfterSeconds > 0) {
    return `${message} 约 ${Math.ceil(error.retryAfterSeconds)} 秒后可重试。`;
  }
  return message;
}
