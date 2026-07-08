import { renderEmptyRow } from "../components/metric-card.js";
import { isAdmin, state } from "../state.js";
import { escapeAttr, escapeHTML, formatNumber, limitText, rpmText } from "../utils.js";

export function renderTiers() {
  if (!isAdmin()) {
    return `
      <div class="page-head">
        <div>
          <h2>Tier Management</h2>
          <p>Admin role is required to manage tiers.</p>
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
  const tiers = state.tiers || [];
  return `
    <div class="page-head">
      <div>
        <h2>Tier Management</h2>
        <p>管理 tier0~tier6 等级预设，注册用户默认分配 tier0。</p>
      </div>
      <span class="row-actions">
        <button class="button secondary" data-action="refresh" type="button"><span class="material-symbols-outlined">refresh</span><span>Refresh</span></button>
        <button class="button" data-action="open-create-tier" type="button"><span class="material-symbols-outlined">add</span><span>New Tier</span></button>
      </span>
    </div>
    <section class="card table-card">
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Level</th>
              <th>RPM</th>
              <th>Success Limit</th>
              <th>Users</th>
              <th class="right">Actions</th>
            </tr>
          </thead>
          <tbody>
            ${tiers.length ? tiers.map(renderTierRow).join("") : renderEmptyRow("workspace_premium", "No tiers", "Create a tier preset to get started.")}
          </tbody>
        </table>
      </div>
    </section>`;
}

export function renderTierRow(tier) {
  return `
    <tr>
      <td><strong>${escapeHTML(tier.name)}</strong></td>
      <td><span class="badge off">L${tier.level}</span></td>
      <td class="mono">${rpmText(tier.rpm)}</td>
      <td class="mono">${limitText(tier.success_limit)}</td>
      <td>${formatNumber(tier.user_count || 0)}</td>
      <td class="right">
        <span class="row-actions">
          <button class="mini-icon" data-action="edit-tier" data-tier-id="${escapeAttr(tier.id)}" title="Edit" type="button"><span class="material-symbols-outlined">edit</span></button>
          <button class="mini-icon" data-action="delete-tier" data-tier-id="${escapeAttr(tier.id)}" title="Delete" type="button"><span class="material-symbols-outlined">delete</span></button>
        </span>
      </td>
    </tr>`;
}

export function tierOptions(selectedID) {
  return (state.tiers || [])
    .map((t) => `<option value="${escapeAttr(t.id)}" ${selectedID === t.id ? "selected" : ""}>${escapeHTML(t.name)} (L${t.level})</option>`)
    .join("");
}
