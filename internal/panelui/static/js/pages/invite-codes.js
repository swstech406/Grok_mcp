import { renderEmptyRow } from "../components/metric-card.js";
import { filteredInviteCodes, isAdmin, state } from "../state.js";
import { escapeAttr, escapeHTML, formatDate, formatNumber, shortID } from "../utils.js";

export function renderInviteCodes() {
  if (!isAdmin()) {
    return `
      <div class="page-head">
        <div>
          <h2>Invitation Codes</h2>
          <p>Admin role is required to manage invitation codes.</p>
        </div>
      </div>
      <section class="card empty">
        <div>
          <span class="material-symbols-outlined">lock</span>
          <h3>Admin required</h3>
          <p>当前账号没有管理员权限。</p>
        </div>
      </section>`;
  }

  const inviteCodes = filteredInviteCodes();
  const registrationMode = state.serverSettings?.registration_mode || "free";
  const inviteModeEnabled = registrationMode === "invite";
  return `
    <div class="page-head">
      <div>
        <h2>Invitation Codes</h2>
        <p>生成和管理注册邀请码。邀请码仅在 Server Settings 启用邀请注册模式时生效。</p>
      </div>
      <span class="row-actions">
        <button class="button secondary" data-action="refresh" type="button"><span class="material-symbols-outlined">refresh</span><span>Refresh</span></button>
        <button class="button" data-action="open-create-invite-code" type="button"><span class="material-symbols-outlined">add</span><span>New Invite Code</span></button>
      </span>
    </div>

    <section class="card settings-card" style="margin-bottom: 18px;">
      <div class="tutorial-card-head">
        <span class="material-symbols-outlined">${inviteModeEnabled ? "mark_email_read" : "info"}</span>
        <div>
          <h3>Registration Mode: ${escapeHTML(registrationModeLabel(registrationMode))}</h3>
          <p>${inviteModeEnabled ? "当前启用邀请注册，注册时必须提供有效邀请码。" : "当前没有启用邀请注册，因此这些邀请码不会影响注册。"}</p>
        </div>
      </div>
    </section>

    <section class="card table-card">
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Invite Code</th>
              <th>Usage</th>
              <th>Status</th>
              <th>Created</th>
              <th>ID</th>
              <th class="right">Actions</th>
            </tr>
          </thead>
          <tbody>
            ${inviteCodes.length ? inviteCodes.map(renderInviteCodeRow).join("") : renderEmptyRow("confirmation_number", "No invite codes", "Create an invite code when invite-only registration is needed.")}
          </tbody>
        </table>
      </div>
    </section>`;
}

function renderInviteCodeRow(inviteCode) {
  const registrationLimit = Number(inviteCode.registration_limit) || 0;
  const registrationCount = Number(inviteCode.registration_count) || 0;
  const canCopyInviteCode = Boolean(inviteCode.code);
  return `
    <tr>
      <td class="mono"><strong>${escapeHTML(inviteCode.code || inviteCode.code_prefix || "Legacy code unavailable")}</strong></td>
      <td class="mono">${formatNumber(registrationCount)} / ${formatNumber(registrationLimit)}</td>
      <td>
        <label class="toggle" title="${inviteCode.enabled ? "Enabled" : "Disabled"}">
          <input type="checkbox" data-invite-code-toggle="${escapeAttr(inviteCode.id)}" ${inviteCode.enabled ? "checked" : ""}>
          <span></span>
        </label>
      </td>
      <td>${formatDate(inviteCode.created_at)}</td>
      <td class="mono muted">${escapeHTML(shortID(inviteCode.id))}</td>
      <td class="right">
        <span class="row-actions">
          <button class="mini-icon" data-action="copy-invite-code" data-invite-code-id="${escapeAttr(inviteCode.id)}" title="${canCopyInviteCode ? "Copy Invite Code" : "Plaintext unavailable for this legacy invite code"}" type="button" ${canCopyInviteCode ? "" : "disabled"}><span class="material-symbols-outlined">content_copy</span></button>
          <button class="mini-icon" data-action="edit-invite-code" data-invite-code-id="${escapeAttr(inviteCode.id)}" title="Edit" type="button"><span class="material-symbols-outlined">edit</span></button>
          <button class="mini-icon danger" data-action="delete-invite-code" data-invite-code-id="${escapeAttr(inviteCode.id)}" title="Delete" type="button"><span class="material-symbols-outlined">delete</span></button>
        </span>
      </td>
    </tr>`;
}

function registrationModeLabel(mode) {
  if (mode === "invite") return "Invite Code Registration";
  if (mode === "disabled") return "Registration Disabled";
  return "Free Registration";
}
