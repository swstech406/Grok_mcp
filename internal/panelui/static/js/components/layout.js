import { escapeHTML, getInitials } from "../utils.js";
import { renderIcon } from "./icons.js";

export function renderShell(state, currentMetadata, currentPageHTML) {
  const isAdmin = state.user?.role === "admin";
  return `
    <div class="app-shell ${state.sidebarOpen ? "is-sidebar-open" : ""}">
      <button class="sidebar-backdrop" type="button" data-action="close-sidebar" aria-label="关闭导航"></button>
      ${renderSidebar(state, isAdmin)}
      <div class="main-shell">
        <header class="topbar">
          <div class="topbar-left">
            <button class="icon-button mobile-menu-button" type="button" data-action="toggle-sidebar" aria-label="打开导航">${renderIcon("menu")}</button>
            <span class="breadcrumb">${escapeHTML(currentMetadata.section)} / <strong>${escapeHTML(currentMetadata.title)}</strong></span>
          </div>
          <div class="topbar-actions">
            <span class="system-status"><span class="status-dot"></span>服务已连接</span>
            <button class="icon-button ${state.refreshing ? "is-spinning" : ""}" type="button" data-action="refresh-page" aria-label="刷新当前页面" ${state.refreshing ? "disabled" : ""}>${renderIcon("refresh")}</button>
          </div>
        </header>
        <main class="page-wrap page-enter">
          ${currentPageHTML}
        </main>
      </div>
    </div>
  `;
}

function renderSidebar(state, isAdmin) {
  const renderNavigationItem = (page, label, icon) => `
    <button class="nav-item ${state.currentPage === page ? "is-active" : ""}" type="button" data-action="navigate" data-page="${page}">
      ${renderIcon(icon)}<span>${escapeHTML(label)}</span>
    </button>
  `;

  return `
    <aside class="sidebar">
      <div class="brand-lockup">
        <span class="brand-symbol" aria-hidden="true"></span>
        <span>Grok MCP<small>Control plane</small></span>
      </div>

      <div class="sidebar-scroll">
        <section class="nav-section">
          <p class="nav-section-label">Workspace</p>
          <nav class="nav-list" aria-label="工作台导航">
            ${renderNavigationItem("overview", "总览", "home")}
            ${renderNavigationItem("keys", "API 密钥", "key")}
            ${renderNavigationItem("tutorial", "配置教程", "code")}
            ${renderNavigationItem("usage", "调用分析", "chart")}
          </nav>
        </section>

        ${isAdmin ? `
          <section class="nav-section">
            <p class="nav-section-label">Administration</p>
            <nav class="nav-list" aria-label="系统管理导航">
              ${renderNavigationItem("users", "用户管理", "users")}
              ${renderNavigationItem("tiers", "配额方案", "layers")}
              ${renderNavigationItem("invites", "邀请码", "ticket")}
              ${renderNavigationItem("settings", "服务设置", "settings")}
            </nav>
          </section>
        ` : ""}
      </div>

      <footer class="sidebar-footer">
        <div class="sidebar-user">
          <button
            class="sidebar-user-profile ${state.currentPage === "account" ? "is-active" : ""}"
            type="button"
            data-action="navigate"
            data-page="account"
            aria-label="查看账户信息"
          >
            <span class="user-avatar">${escapeHTML(getInitials(state.user?.username))}</span>
            <span class="sidebar-user-copy">
              <strong>${escapeHTML(state.user?.username || "User")}</strong>
              <span>${state.user?.role === "admin" ? "管理员" : escapeHTML(state.user?.tier_name || "普通用户")}</span>
            </span>
          </button>
          <button class="logout-button" type="button" data-action="logout" aria-label="退出登录">${renderIcon("logout")}</button>
        </div>
      </footer>
    </aside>
  `;
}
