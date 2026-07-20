import { escapeHTML, formatDateTime } from "../utils.js";
import { renderIcon } from "../components/icons.js";
import { renderPageHeading } from "../components/loading.js";

export function renderSettingsPage(state) {
  if (state.pageLoading && !state.data.settings) {
    return `${renderPageHeading("服务设置", "热更新上游连接、搜索并发、默认模型、代理与注册策略。")}
      <div class="settings-layout"><div class="skeleton" style="height:620px;border-radius:16px"></div><div class="skeleton" style="height:330px;border-radius:16px"></div></div>`;
  }

  const settings = state.data.settings || {};
  const pendingApplyWarning = state.settingsApplyWarning || null;
  const settingsNotApplied = Boolean(pendingApplyWarning)
    || settings.apply_state === "saved_not_applied"
    || settings.persisted_version !== settings.live_version;
  const persistedValuesReloaded = !pendingApplyWarning || pendingApplyWarning.persistedValuesReloaded;
  const persistedVersion = formatSettingsVersion(
    pendingApplyWarning?.persistedVersion ?? settings.persisted_version
  );
  const liveVersion = formatSettingsVersion(
    pendingApplyWarning?.liveVersion ?? settings.live_version
  );
  const upstreamProtocol = settings.upstream_protocol || "responses";
  const modelOptions = state.data.models || [];
  const knownModels = new Set(modelOptions.map((model) => model.id));
  const modelChoices = settings.model && !knownModels.has(settings.model)
    ? [{ id: settings.model }, ...modelOptions]
    : modelOptions;

  return `
    ${renderPageHeading("服务设置", "热更新上游连接、搜索并发、默认模型、代理、注册策略与运维观测。")}
    ${settingsNotApplied ? `<div class="settings-apply-warning" role="status">
      <span class="settings-apply-warning-icon">${renderIcon("warning")}</span>
      <div><strong>设置已保存，尚未应用</strong><p>${persistedValuesReloaded
        ? `当前表单显示持久化版本 ${escapeHTML(persistedVersion)}，最后完整确认的运行版本为 ${escapeHTML(liveVersion)}。请重试保存并应用，或重启服务以加载持久化设置。`
        : `保存版本 ${escapeHTML(persistedVersion)} 已确认，但最新持久化值重新读取失败；当前表单可能仍显示提交前内容。最后完整确认的运行版本为 ${escapeHTML(liveVersion)}。`}</p></div>
    </div>` : ""}
    <div class="settings-layout">
      <form class="data-card" data-form="settings">
        <section class="settings-section">
          <div class="settings-section-copy"><h3>上游连接</h3><p>配置 CPA 服务地址、访问凭证与请求协议。留空 API Key 将保留当前值。</p></div>
          <div class="form-grid">
            <label class="field-group is-full"><span class="field-label">上游协议</span><select class="select-input" name="upstream_protocol" required>
              <option value="responses" ${upstreamProtocol === "responses" ? "selected" : ""}>OpenAI Responses（/v1/responses）</option>
              <option value="chat_completions" ${upstreamProtocol === "chat_completions" ? "selected" : ""}>OpenAI Chat Completions（/v1/chat/completions）</option>
              <option value="anthropic_messages" ${upstreamProtocol === "anthropic_messages" ? "selected" : ""}>Anthropic Messages（/v1/messages）</option>
            </select><span class="field-hint">协议切换会立即应用到同一套 CPA 连接配置。</span></label>
            <label class="field-group is-full"><span class="field-label">CPA Base URL</span><input class="text-input" name="cpa_base_url" type="url" value="${escapeHTML(settings.cpa_base_url || "")}" placeholder="http://127.0.0.1:8317" required></label>
            <label class="field-group is-full"><span class="field-label"><span>CPA API Key</span><span class="field-hint">${settings.cpa_api_key_set ? `已配置 ${escapeHTML(settings.cpa_api_key_preview || "")}` : "尚未配置"}</span></span><input class="text-input" name="cpa_api_key" type="password" autocomplete="new-password" placeholder="留空以保留现有密钥"></label>
          </div>
        </section>
        <section class="settings-section">
          <div class="settings-section-copy"><h3>模型与超时</h3><p>设置连接、TLS 握手和响应头各阶段超时；已建立的 SSE 流不受该值限制。</p></div>
          <div class="form-grid form-grid-align-fields">
            <label class="field-group"><span class="field-label"><span>默认模型</span><button class="button button-ghost button-sm" type="button" data-action="load-models">拉取模型</button></span>
              ${modelChoices.length > 0 ? `<select class="select-input" name="model" required>${modelChoices.map((model) => `<option value="${escapeHTML(model.id)}" ${model.id === settings.model ? "selected" : ""}>${escapeHTML(model.id)}</option>`).join("")}</select>` : `<input class="text-input" name="model" type="text" value="${escapeHTML(settings.model || "")}" placeholder="grok-4.3" required>`}
            </label>
            <label class="field-group"><span class="field-label">连接/TLS/响应头超时（秒）</span><input class="text-input" name="timeout_seconds" type="number" min="1" step="1" value="${escapeHTML(settings.timeout_seconds || 120)}" required></label>
          </div>
        </section>
        <section class="settings-section">
          <div class="settings-section-copy"><h3>搜索并发</h3><p>限制同时进行的流式搜索。容量耗尽时立即返回 503，不在服务内排队。</p></div>
          <div class="form-grid form-grid-align-fields">
            <label class="field-group"><span class="field-label">全局搜索并发</span><input class="text-input" name="mcp_global_search_concurrency" type="number" min="1" step="1" value="${escapeHTML(settings.mcp_global_search_concurrency || 16)}" required><span class="field-hint">整个 grok-search-mcp 进程允许的同时在途搜索数。</span></label>
            <label class="field-group"><span class="field-label">单用户搜索并发</span><input class="text-input" name="mcp_user_search_concurrency" type="number" min="1" step="1" value="${escapeHTML(settings.mcp_user_search_concurrency || 4)}" required><span class="field-hint">同一用户所有 API Key 共享，且不得超过全局上限。</span></label>
          </div>
        </section>
        <section class="settings-section">
          <div class="settings-section-copy"><h3>网络代理</h3><p>在上游网络需要代理时启用。代理地址支持 HTTP 或 HTTPS。</p></div>
          <div>
            <label class="switch-row"><span class="switch-copy"><strong>启用显式代理</strong><span>关闭时使用默认网络路径</span></span><span class="switch"><input name="proxy_enabled" type="checkbox" ${settings.proxy_enabled ? "checked" : ""}><span class="switch-track"></span></span></label>
            <label class="field-group"><span class="field-label">代理 URL</span><input class="text-input" name="proxy_url" type="url" value="${escapeHTML(settings.proxy_url || "")}" placeholder="http://127.0.0.1:7890"></label>
          </div>
        </section>
        <section class="settings-section">
          <div class="settings-section-copy"><h3>访问策略与运维观测</h3><p>控制公开注册入口、调试日志与数据库运行指标。调试模式可能记录更多请求信息。</p></div>
          <div class="form-grid">
            <label class="field-group"><span class="field-label">注册模式</span><select class="select-input" name="registration_mode">
              <option value="free" ${settings.registration_mode === "free" ? "selected" : ""}>自由注册</option>
              <option value="invite" ${settings.registration_mode === "invite" ? "selected" : ""}>邀请注册</option>
              <option value="disabled" ${settings.registration_mode === "disabled" ? "selected" : ""}>关闭注册</option>
            </select></label>
            <label class="switch-row"><span class="switch-copy"><strong>调试模式</strong><span>输出扩展诊断信息</span></span><span class="switch"><input name="debug" type="checkbox" ${settings.debug ? "checked" : ""}><span class="switch-track"></span></span></label>
            <label class="switch-row"><span class="switch-copy"><strong>启用数据库运行指标</strong><span>开启后显示 SQLite、Usage 队列与 WAL 性能指标</span></span><span class="switch"><input name="operations_metrics_enabled" type="checkbox" ${settings.operations_metrics_enabled ? "checked" : ""}><span class="switch-track"></span></span></label>
          </div>
        </section>
        <footer class="settings-footer"><button class="button button-primary" type="submit" ${state.formBusy ? "disabled" : ""}>${state.formBusy ? `${renderIcon("refresh")} 正在保存` : `${renderIcon("check")} 保存并应用`}</button></footer>
      </form>

      <aside class="info-card">
        <div class="info-card-top"><span class="info-card-icon">${renderIcon("shield")}</span><h3>运行时热更新</h3><p>${settingsNotApplied
          ? (persistedValuesReloaded
            ? "已保存配置与当前运行配置不一致。表单值来自持久化存储，运行状态以已确认版本为准。"
            : "设置已保存但尚未应用，且最新持久化值暂时无法重新读取。请勿将当前表单视为已保存值。")
          : "这些设置保存后会立即应用到上游客户端和搜索并发控制，无需重启 grok-search-mcp 服务。"}</p></div>
        <div class="info-list">
          <div class="info-row"><span>服务版本</span><strong>${escapeHTML(settings.version || "未知")}</strong></div>
          <div class="info-row"><span>已保存设置版本</span><strong>${escapeHTML(persistedVersion)}</strong></div>
          <div class="info-row"><span>运行设置版本</span><strong>${escapeHTML(liveVersion)}</strong></div>
          <div class="info-row"><span>应用状态</span><strong>${settingsNotApplied ? "已保存，尚未应用" : "已保存并应用"}</strong></div>
          <div class="info-row"><span>上游协议</span><strong>${escapeHTML(getUpstreamProtocolLabel(upstreamProtocol))}</strong></div>
          <div class="info-row"><span>${settingsNotApplied ? (persistedValuesReloaded ? "已保存模型" : "表单模型（可能过期）") : "当前模型"}</span><strong>${escapeHTML(settings.model || "未配置")}</strong></div>
          <div class="info-row"><span>搜索并发</span><strong>${escapeHTML(`${settings.mcp_global_search_concurrency || 16} / 用户 ${settings.mcp_user_search_concurrency || 4}`)}</strong></div>
          <div class="info-row"><span>API Key</span><strong>${settings.cpa_api_key_set ? "已安全配置" : "未配置"}</strong></div>
          <div class="info-row"><span>代理</span><strong>${settings.proxy_enabled ? "已启用" : "直连"}</strong></div>
          <div class="info-row"><span>注册</span><strong>${escapeHTML(getRegistrationModeLabel(settings.registration_mode))}</strong></div>
          <div class="info-row"><span>最后更新</span><strong>${escapeHTML(formatDateTime(settings.updated_at))}</strong></div>
        </div>
      </aside>
    </div>
  `;
}

function formatSettingsVersion(versionValue) {
  const numericVersion = Number(versionValue);
  return Number.isSafeInteger(numericVersion) && numericVersion > 0
    ? String(numericVersion)
    : "未知";
}

function getRegistrationModeLabel(mode) {
  const labels = { free: "自由注册", invite: "邀请注册", disabled: "关闭注册" };
  return labels[mode] || "未知";
}

function getUpstreamProtocolLabel(protocol) {
  const labels = {
    responses: "OpenAI Responses",
    chat_completions: "OpenAI Chat Completions",
    anthropic_messages: "Anthropic Messages"
  };
  return labels[protocol] || "未知";
}
