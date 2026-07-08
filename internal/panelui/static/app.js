import { api, loadAggregatedUsage, loadKeys, loadServerSettings, loadTiers, loadUsageForSelection, loadUsers } from "./js/api.js";
import { renderAuth } from "./js/components/forms.js";
import { renderShell } from "./js/components/layout.js";
import { renderLoading } from "./js/components/loading.js";
import { renderModal } from "./js/components/modal.js";
import { notify, renderToast } from "./js/components/toast.js";
import { onChange, onClick, onInput, onSubmit } from "./js/events.js";
import { readRoute, renderRoute } from "./js/router.js";
import { clearSession, isAdmin, state, storage } from "./js/state.js";
import { errorText, setStored } from "./js/utils.js";

const app = document.getElementById("app");

state.route = readRoute();

document.addEventListener("submit", onSubmit);
document.addEventListener("click", onClick);
document.addEventListener("change", onChange);
document.addEventListener("input", onInput);
window.addEventListener("hashchange", async () => {
  const previousRoute = state.route;
  state.route = readRoute();
  resetUsageActivityView(previousRoute, state.route);
  await loadRouteData();
  render();
});

bootstrap();

export async function bootstrap() {
  if (!state.token) {
    state.ready = true;
    render();
    return;
  }
  try {
    state.user = await api("/me");
    setStored(storage.user, JSON.stringify(state.user));
    await loadRouteData();
  } catch (err) {
    clearSession();
    notify(errorText(err), "error");
  } finally {
    state.ready = true;
    render();
  }
}

export async function loadRouteData() {
  if (!state.token || !state.user) {
    return;
  }
  state.loading = true;
  render();
  try {
    state.user = await api("/me");
    setStored(storage.user, JSON.stringify(state.user));
    if (state.route === "dashboard") {
      await loadKeys();
      state.usage = await loadAggregatedUsage(state.sinceMode);
    } else if (state.route === "keys") {
      await loadKeys();
    } else if (state.route === "usage") {
      await loadKeys();
      state.usage = await loadUsageForSelection();
    } else if (state.route === "users" && isAdmin()) {
      await loadUsers();
      await loadTiers();
    } else if (state.route === "tiers" && isAdmin()) {
      await loadTiers();
    } else if (state.route === "settings" && isAdmin()) {
      await loadServerSettings();
    }
  } catch (err) {
    handleAPIError(err);
  } finally {
    state.loading = false;
  }
}

function resetUsageActivityView(previousRoute, nextRoute) {
  if (nextRoute !== "usage") {
    state.expandUsageActivityOnNextUsageNavigation = false;
    state.usageActivityPage = 1;
    return;
  }

  if (previousRoute !== "usage") {
    state.usageActivityCompact = !state.expandUsageActivityOnNextUsageNavigation;
    state.usageActivityPage = 1;
  }

  state.expandUsageActivityOnNextUsageNavigation = false;
}

export function render() {
  if (!state.ready) {
    app.innerHTML = renderLoading("加载管理面板...");
    return;
  }
  if (!state.token || !state.user) {
    app.innerHTML = renderAuth() + renderToast();
    return;
  }
  app.innerHTML = renderShell() + renderModal() + renderToast();
}

export function handleAPIError(err) {
  if (err && err.status === 401) {
    clearSession();
    notify("登录已失效，请重新登录。", "error");
  } else {
    notify(errorText(err), "error");
  }
}
