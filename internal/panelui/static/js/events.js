import { state } from "./state.js";
import { createAccountEvents } from "./events/account-events.js";
import { createAuthEvents } from "./events/auth-events.js";
import { createConfirmationModalEvents } from "./events/confirmation-modal.js";
import { createDebugJSONModalEvents } from "./events/debug-json-modal.js";
import { copyValue } from "./events/event-helpers.js";
import { createInviteEvents } from "./events/invite-events.js";
import { createKeyEvents } from "./events/key-events.js";
import { createModalController } from "./events/modal-controller.js";
import { createNavigationEvents } from "./events/navigation-events.js";
import { createSettingsEvents } from "./events/settings-events.js";
import { createTierEvents } from "./events/tier-events.js";
import { createUserEvents } from "./events/user-events.js";
import { createUserUsageModalEvents } from "./events/user-usage-modal.js";

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
  let eventHandlersRegistered = false;

  const modalController = createModalController({
    state,
    modalRegionElement,
    renderModalRegion
  });
  const navigationEvents = createNavigationEvents({
    state,
    modalController,
    renderApplication,
    loadCurrentPage,
    normalizeCurrentPageForRole
  });
  const authEvents = createAuthEvents({
    state,
    modalController,
    renderApplication,
    loadCurrentPage,
    abortCurrentPageLoad
  });
  const accountEvents = createAccountEvents({
    state,
    renderApplication,
    handleSessionError
  });
  const keyEvents = createKeyEvents({
    state,
    modalController,
    renderApplication,
    renderModalRegion,
    handleSessionError,
    copyValue
  });
  const userEvents = createUserEvents({
    state,
    modalController,
    renderApplication,
    handleSessionError
  });
  const userUsageModalEvents = createUserUsageModalEvents({
    state,
    modalController,
    renderModalRegion,
    handleSessionError
  });
  const tierEvents = createTierEvents({
    state,
    modalController,
    renderApplication,
    handleSessionError
  });
  const inviteEvents = createInviteEvents({
    state,
    modalController,
    renderApplication,
    renderModalRegion,
    handleSessionError,
    loadCurrentPage
  });
  const settingsEvents = createSettingsEvents({
    state,
    renderApplication,
    handleSessionError
  });
  const debugJSONModalEvents = createDebugJSONModalEvents({
    state,
    modalController,
    renderModalRegion,
    handleSessionError,
    copyValue
  });
  const confirmationModalEvents = createConfirmationModalEvents({
    state,
    modalController,
    executors: {
      deleteKey: keyEvents.deleteConfirmed,
      deleteUser: userEvents.deleteConfirmed,
      deleteTier: tierEvents.deleteConfirmed,
      deleteInvite: inviteEvents.deleteConfirmed
    },
    renderApplication,
    handleSessionError
  });

  const applicationActionHandlers = {
    "switch-auth": (actionElement) => authEvents.switchAuthMode(actionElement.dataset.mode),
    "toggle-password": (actionElement) => authEvents.togglePasswordVisibility(actionElement),
    navigate: (actionElement) => navigationEvents.navigateToPage(actionElement.dataset.page),
    "toggle-sidebar": () => {
      state.sidebarOpen = !state.sidebarOpen;
      renderApplication();
    },
    "close-sidebar": () => {
      state.sidebarOpen = false;
      renderApplication();
    },
    logout: () => authEvents.logout(),
    "refresh-page": () => navigationEvents.refreshCurrentPage(),
    "change-list-page": (actionElement) => navigationEvents.changeListPage(
      actionElement.dataset.list,
      actionElement.dataset.direction
    ),
    "open-create-key": () => keyEvents.openCreateModal(),
    "open-edit-key": (actionElement) => keyEvents.openEditModal(actionElement.dataset.id),
    "copy-key": (actionElement) => keyEvents.copyAPIKey(
      actionElement.dataset.id,
      actionElement
    ),
    "open-key-usage": (actionElement) => keyEvents.openUsageModal(actionElement.dataset.id),
    "confirm-delete-key": (actionElement) => keyEvents.openDeleteConfirmation(
      actionElement.dataset.id
    ),
    "set-usage-period": (actionElement) => navigationEvents.setUsagePeriod(
      actionElement.dataset.period
    ),
    "view-debug-json": (actionElement) => debugJSONModalEvents.openDebugJSONModal(
      actionElement.dataset.recordId
    ),
    "open-edit-user": (actionElement) => userEvents.openEditModal(actionElement.dataset.id),
    "open-user-usage": (actionElement) => userUsageModalEvents.openUserUsageModal(
      actionElement.dataset.id
    ),
    "confirm-delete-user": (actionElement) => userEvents.openDeleteConfirmation(
      actionElement.dataset.id
    ),
    "open-create-tier": () => tierEvents.openCreateModal(),
    "open-edit-tier": (actionElement) => tierEvents.openEditModal(actionElement.dataset.id),
    "confirm-delete-tier": (actionElement) => tierEvents.openDeleteConfirmation(
      actionElement.dataset.id
    ),
    "open-create-invite": () => inviteEvents.openCreateModal(),
    "toggle-invite": (actionElement) => inviteEvents.toggleEnabled(actionElement.dataset.id),
    "view-invite-redemptions": (actionElement) => inviteEvents.openRedemptions(
      actionElement.dataset.id
    ),
    "copy-value": (actionElement) => copyValue(actionElement.dataset.value || ""),
    "confirm-delete-invite": (actionElement) => inviteEvents.openDeleteConfirmation(
      actionElement.dataset.id
    ),
    "load-models": (actionElement) => settingsEvents.loadAvailableModels(actionElement)
  };

  const modalActionHandlers = {
    "copy-value": (actionElement) => copyValue(actionElement.dataset.value || ""),
    "view-debug-json": (actionElement) => debugJSONModalEvents.openDebugJSONModal(
      actionElement.dataset.recordId
    ),
    "view-user-usage-logs": () => userUsageModalEvents.openUserUsageLogsModal(),
    "view-user-usage-summary": () => userUsageModalEvents.openUserUsageSummaryModal(),
    "change-user-usage-page": (actionElement) => userUsageModalEvents.changeUserUsagePage(
      actionElement.dataset.direction
    ),
    "change-invite-redemptions-page": (actionElement) => inviteEvents.changeRedemptionsPage(
      actionElement.dataset.direction
    ),
    "copy-debug-json": () => debugJSONModalEvents.copyDebugJSON(),
    "execute-confirm": () => confirmationModalEvents.executeConfirmedAction()
  };

  const formSubmitHandlers = {
    login: authEvents.submitLogin,
    register: authEvents.submitRegistration,
    "change-password": accountEvents.submitPasswordChange,
    "revoke-sessions": accountEvents.submitSessionRevocation,
    "create-key": keyEvents.submitCreate,
    "edit-key": keyEvents.submitEdit,
    "edit-user": userEvents.submitEdit,
    "create-tier": tierEvents.submitCreate,
    "edit-tier": tierEvents.submitEdit,
    "create-invite": inviteEvents.submitCreate,
    settings: settingsEvents.submitSettings
  };

  function registerEventHandlers() {
    if (eventHandlersRegistered) {
      return;
    }
    eventHandlersRegistered = true;

    applicationElement.addEventListener("click", handleApplicationClick);
    applicationElement.addEventListener("change", handleApplicationChange);
    applicationElement.addEventListener("submit", handleFormSubmit);
    applicationElement.addEventListener("input", handleApplicationInput);
    modalRegionElement.addEventListener("click", handleModalClick);
    modalRegionElement.addEventListener("change", handleModalChange);
    modalRegionElement.addEventListener("submit", handleFormSubmit);
    window.addEventListener("hashchange", navigationEvents.handleLocationChange);
    document.addEventListener("keydown", handleGlobalKeydown);
  }

  async function handleApplicationClick(event) {
    const actionElement = event.target.closest("[data-action]");
    if (!actionElement) {
      return;
    }

    const actionHandler = applicationActionHandlers[actionElement.dataset.action];
    if (actionHandler) {
      await actionHandler(actionElement, event);
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
    if (action === "close-modal" || action === "modal-backdrop") {
      if (!state.modal?.busy) {
        modalController.closeModal();
      }
      return;
    }

    const actionHandler = modalActionHandlers[action];
    if (actionHandler) {
      await actionHandler(actionElement, event);
    }
  }

  async function handleApplicationChange(event) {
    const actionElement = event.target.closest('[data-action="change-list-page-size"]');
    if (!actionElement || actionElement.dataset.list !== "usageRecords") {
      return;
    }
    await navigationEvents.changeUsagePageSize(actionElement.value);
  }

  async function handleModalChange(event) {
    const actionElement = event.target.closest("[data-action]");
    if (!actionElement) {
      return;
    }
    if (actionElement.dataset.action === "change-user-usage-page-size") {
      await userUsageModalEvents.changeUserUsagePageSize(actionElement.value);
    } else if (actionElement.dataset.action === "change-invite-redemptions-page-size") {
      await inviteEvents.changeRedemptionsPageSize(actionElement.value);
    }
  }

  function handleApplicationInput(event) {
    if (!event.target.matches('[data-filter="user-search"]')) {
      return;
    }

    const selectionStart = event.target.selectionStart ?? event.target.value.length;
    const selectionEnd = event.target.selectionEnd ?? selectionStart;
    const selectionDirection = event.target.selectionDirection || "none";
    userEvents.updateSearchFilter(event.target.value);
    renderApplication();

    const replacementInput = applicationElement.querySelector('[data-filter="user-search"]');
    replacementInput?.focus();
    replacementInput?.setSelectionRange(selectionStart, selectionEnd, selectionDirection);
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

    const submitHandler = formSubmitHandlers[formElement.dataset.form];
    if (submitHandler) {
      await submitHandler(formElement);
    }
  }

  function handleGlobalKeydown(event) {
    if (event.key === "Escape" && state.modal && !state.modal.busy) {
      modalController.closeModal();
    }
  }

  return { register: registerEventHandlers };
}
