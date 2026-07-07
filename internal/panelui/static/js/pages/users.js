import { renderEmptyRow } from "../components/metric-card.js";
import { filteredUsers, isAdmin, state } from "../state.js";
import { escapeAttr, escapeHTML, formatNumber, limitText, rpmText, shortID } from "../utils.js";

export function renderUsers() {
  if (!isAdmin()) {
    return `
      <div class="page-head">
        <div>
          <h2>User Management</h2>
          <p>Admin role is required to view and edit users.</p>
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
  const users = filteredUsers();
  return `
    <div class="page-head">
      <div>
        <h2>User Management</h2>
        <p>Adjust user status, roles, tier-derived RPM and success limit.</p>
      </div>
      <button class="button secondary" data-action="refresh" type="button"><span class="material-symbols-outlined">refresh</span><span>Refresh</span></button>
    </div>
    <section class="card table-card">
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Username</th>
              <th>Role</th>
              <th>Tier</th>
              <th>Status</th>
              <th>RPM</th>
              <th>Success Limit</th>
      <th class="right">Actions</th>
            </tr>
          </thead>
          <tbody>
            ${users.length ? users.map(renderUserRow).join("") : renderEmptyRow("group", "No users", "Registered users will appear here.")}
          </tbody>
        </table>
      </div>
    </section>`;
}

export function renderUserRow(user) {
  const tierBadge = user.tier_name
    ? `<span class="badge off">${escapeHTML(user.tier_name)}</span>`
    : `<span class="muted">—</span>`;
  const deleteButton = state.user && state.user.id === user.id
    ? ""
    : `<button class="mini-icon danger" data-action="delete-user" data-user-id="${escapeAttr(user.id)}" title="Delete" type="button"><span class="material-symbols-outlined">delete</span></button>`;
  return `
    <tr>
      <td>
        <strong>${escapeHTML(user.username)}</strong>
        <div class="hint mono">${escapeHTML(shortID(user.id))}</div>
      </td>
      <td><span class="badge ${user.role === "admin" ? "" : "off"}">${escapeHTML(user.role)}</span></td>
      <td>${tierBadge}</td>
      <td><span class="badge ${user.enabled ? "" : "error"}">${user.enabled ? "Enabled" : "Disabled"}</span></td>
      <td class="mono">${rpmText(user.rpm)}</td>
      <td>${formatNumber(user.success_calls)} <span class="muted">/ ${limitText(user.success_limit)}</span></td>
      <td class="right">
        <span class="row-actions">
          <button class="mini-icon" data-action="user-usage" data-user-id="${escapeAttr(user.id)}" title="Usage" type="button"><span class="material-symbols-outlined">bar_chart</span></button>
          <button class="mini-icon" data-action="edit-user" data-user-id="${escapeAttr(user.id)}" title="Edit" type="button"><span class="material-symbols-outlined">edit</span></button>
          ${deleteButton}
        </span>
      </td>
    </tr>`;
}
