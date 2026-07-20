export const savedNotAppliedCondition = "settings_saved_not_applied";

export function applySettingsResponseToState(state, settingsResponse) {
  state.data.settings = settingsResponse;
  state.settingsApplyWarning = null;
  state.registrationMode = settingsResponse?.registration_mode || state.registrationMode;
  if (!settingsResponse?.operations_metrics_enabled) {
    state.data.operationsMetrics = null;
  }
}

export function markSettingsSavedNotApplied(state, errorDetails = null) {
  state.settingsApplyWarning = {
    persistedVersion: errorDetails?.persisted_version ?? null,
    liveVersion: errorDetails?.live_version ?? null,
    persistedValuesReloaded: false
  };
}

export async function reloadSavedNotAppliedSettings({
  state,
  fetchSettingsRequest,
  errorDetails = null
}) {
  const persistedSettings = await fetchSettingsRequest();
  applySettingsResponseToState(state, persistedSettings);
  return {
    settings: persistedSettings,
    title: "设置已保存，尚未应用",
    message: buildSavedNotAppliedMessage(persistedSettings, errorDetails)
  };
}

export function buildSavedNotAppliedMessage(settingsResponse, errorDetails = null) {
  const persistedVersion = settingsResponse?.persisted_version ?? errorDetails?.persisted_version;
  const liveVersion = settingsResponse?.live_version ?? errorDetails?.live_version;
  if (persistedVersion !== undefined && liveVersion !== undefined) {
    return `已加载保存的版本 ${persistedVersion}，最后完整确认的运行版本为 ${liveVersion}。请重试应用或重启服务。`;
  }
  return "新设置已写入持久化存储，但当前运行服务尚未确认应用。请重试应用或重启服务。";
}
