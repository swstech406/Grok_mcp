import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const sessionValues = new Map();
globalThis.window = { location: { search: "" } };
globalThis.sessionStorage = {
  getItem(key) {
    return sessionValues.get(key) ?? null;
  },
  setItem(key, value) {
    sessionValues.set(key, String(value));
  },
  removeItem(key) {
    sessionValues.delete(key);
  }
};

async function importStandaloneModule(relativeModulePath) {
  const moduleSource = await readFile(new URL(relativeModulePath, import.meta.url), "utf8");
  const encodedModuleSource = Buffer.from(moduleSource).toString("base64");
  return import(`data:text/javascript;base64,${encodedModuleSource}`);
}

async function importBrowserModule(relativeModulePath) {
  const moduleURL = new URL(relativeModulePath, import.meta.url);
  return import(await buildBrowserModuleDataURL(moduleURL));
}

async function buildBrowserModuleDataURL(moduleURL) {
  let moduleSource = await readFile(moduleURL, "utf8");
  const relativeImportPattern = /from\s+["'](\.[^"']+)["']/g;
  const relativeSpecifiers = Array.from(
    moduleSource.matchAll(relativeImportPattern),
    (match) => match[1]
  );
  for (const relativeSpecifier of new Set(relativeSpecifiers)) {
    const dependencyDataURL = await buildBrowserModuleDataURL(new URL(relativeSpecifier, moduleURL));
    moduleSource = moduleSource
      .replaceAll(`"${relativeSpecifier}"`, `"${dependencyDataURL}"`)
      .replaceAll(`'${relativeSpecifier}'`, `'${dependencyDataURL}'`);
  }
  return `data:text/javascript;base64,${Buffer.from(moduleSource).toString("base64")}`;
}

const { panelAPI } = await importStandaloneModule("../static/js/api.js");
const {
  applySettingsResponseToState,
  markSettingsSavedNotApplied,
  reloadSavedNotAppliedSettings,
  savedNotAppliedCondition
} = await importStandaloneModule("../static/js/settings-apply-state.js");
const { renderSettingsPage } = await importBrowserModule("../static/js/pages/settings.js");
const { createSettingsEvents } = await importBrowserModule("../static/js/events/settings-events.js");

test("API errors retain saved-not-applied version details", async (testContext) => {
  const originalFetch = globalThis.fetch;
  testContext.after(() => {
    globalThis.fetch = originalFetch;
  });

  globalThis.fetch = async () => new Response(JSON.stringify({
    code: savedNotAppliedCondition,
    error: "settings were saved but are not active",
    persisted_version: 12,
    live_version: 11
  }), {
    status: 500,
    headers: { "Content-Type": "application/json" }
  });

  await assert.rejects(
    panelAPI.request("/panel/v1/admin/settings", {
      method: "PATCH",
      body: { model: "grok-4.4" }
    }),
    (error) => {
      assert.equal(error.code, savedNotAppliedCondition);
      assert.equal(error.details.persisted_version, 12);
      assert.equal(error.details.live_version, 11);
      return true;
    }
  );
});

test("saved-not-applied warning survives a reconciliation read failure", async () => {
  const state = {
    settingsApplyWarning: null,
    registrationMode: "free",
    data: {
      settings: {
        model: "grok-4.3",
        persisted_version: 11,
        live_version: 11,
        apply_state: "applied"
      },
      operationsMetrics: null
    }
  };

  markSettingsSavedNotApplied(state, {
    persisted_version: 12,
    live_version: 11
  });
  await assert.rejects(
    reloadSavedNotAppliedSettings({
      state,
      fetchSettingsRequest: async () => {
        throw new Error("reload failed");
      }
    }),
    /reload failed/
  );

  assert.deepEqual(state.settingsApplyWarning, {
    persistedVersion: 12,
    liveVersion: 11,
    persistedValuesReloaded: false
  });
  assert.equal(state.data.settings.model, "grok-4.3");
  const warningMarkup = renderSettingsPage({
    ...state,
    pageLoading: false,
    formBusy: false,
    data: {
      ...state.data,
      models: []
    }
  });
  assert.match(warningMarkup, /设置已保存，尚未应用/);
  assert.match(warningMarkup, /当前表单可能仍显示提交前内容/);
  assert.match(warningMarkup, /表单模型（可能过期）/);
  assert.match(warningMarkup, /保存版本 12/);
  assert.match(warningMarkup, /运行版本为 11/);

  applySettingsResponseToState(state, {
    model: "grok-4.4",
    persisted_version: 12,
    live_version: 12,
    apply_state: "applied"
  });
  assert.equal(state.settingsApplyWarning, null);
});

test("saved-not-applied reconciliation replaces stale settings", async () => {
  const state = {
    registrationMode: "free",
    data: {
      settings: {
        model: "grok-4.3",
        persisted_version: 11,
        live_version: 11,
        apply_state: "applied"
      },
      operationsMetrics: { captured_at: "stale" }
    }
  };
  const persistedSettings = {
    model: "grok-4.4",
    registration_mode: "invite",
    operations_metrics_enabled: false,
    persisted_version: 12,
    live_version: 11,
    apply_state: "saved_not_applied"
  };

  const notification = await reloadSavedNotAppliedSettings({
    state,
    fetchSettingsRequest: async () => persistedSettings,
    errorDetails: {
      persisted_version: 12,
      live_version: 11
    }
  });

  assert.equal(state.data.settings, persistedSettings);
  assert.equal(state.data.settings.model, "grok-4.4");
  assert.equal(state.registrationMode, "invite");
  assert.equal(state.data.operationsMetrics, null);
  assert.equal(notification.title, "设置已保存，尚未应用");
  assert.match(notification.message, /版本 12/);
  assert.match(notification.message, /版本为 11/);
  assert.doesNotMatch(notification.title, /保存失败/);
});

test("settings submit reloads persisted state after partial success", async () => {
  const state = {
    formBusy: false,
    settingsApplyWarning: null,
    registrationMode: "free",
    data: {
      settings: { model: "grok-4.3" },
      operationsMetrics: { captured_at: "stale" }
    }
  };
  const persistedSettings = {
    model: "grok-4.4",
    registration_mode: "invite",
    operations_metrics_enabled: false,
    persisted_version: 12,
    live_version: 11,
    apply_state: "saved_not_applied"
  };
  const notifications = [];
  let renderCount = 0;
  const partialSuccessError = Object.assign(new Error("settings were saved but are not active"), {
    code: savedNotAppliedCondition,
    details: {
      persisted_version: 12,
      live_version: 11
    }
  });
  const settingsEvents = createSettingsEvents({
    state,
    renderApplication() {
      renderCount += 1;
    },
    handleSessionError() {
      return false;
    },
    updateSettingsRequest: async () => {
      throw partialSuccessError;
    },
    fetchSettingsRequest: async () => persistedSettings,
    readFormData: () => ({
      cpa_base_url: "http://127.0.0.1:8317",
      upstream_protocol: "responses",
      model: "grok-4.4",
      timeout_seconds: "120",
      mcp_global_search_concurrency: "16",
      mcp_user_search_concurrency: "4",
      proxy_url: "",
      cpa_api_key: ""
    }),
    notify(title, message, type) {
      notifications.push({ title, message, type });
    }
  });
  const formElement = {
    elements: {
      proxy_enabled: { checked: false },
      registration_mode: { value: "invite" },
      debug: { checked: false },
      operations_metrics_enabled: { checked: false }
    }
  };

  await settingsEvents.submitSettings(formElement);

  assert.equal(state.formBusy, false);
  assert.equal(state.data.settings, persistedSettings);
  assert.equal(state.registrationMode, "invite");
  assert.equal(state.settingsApplyWarning, null);
  assert.equal(renderCount, 2);
  assert.equal(notifications.length, 1);
  assert.equal(notifications[0].title, "设置已保存，尚未应用");
  assert.equal(notifications[0].type, "error");
  assert.doesNotMatch(notifications[0].title, /保存失败/);
});
