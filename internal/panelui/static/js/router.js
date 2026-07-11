import { renderShell as renderLayoutShell } from "./components/layout.js";
import { renderAccountPage } from "./pages/account.js";
import { renderConfigurationGuidePage } from "./pages/configuration-guide.js";
import { renderInvitesPage } from "./pages/invite-codes.js";
import { renderKeysPage } from "./pages/keys.js";
import { renderOverviewPage } from "./pages/overview.js";
import { renderSettingsPage } from "./pages/settings.js";
import { renderTiersPage } from "./pages/tiers.js";
import { renderUsagePage } from "./pages/usage.js";
import { renderUsersPage } from "./pages/users.js";

export const pageMetadata = {
  overview: { title: "总览", section: "工作台" },
  keys: { title: "API 密钥", section: "访问控制" },
  tutorial: { title: "配置教程", section: "访问控制" },
  usage: { title: "调用分析", section: "可观测性" },
  users: { title: "用户管理", section: "系统管理" },
  tiers: { title: "配额方案", section: "系统管理" },
  invites: { title: "邀请码", section: "系统管理" },
  settings: { title: "服务设置", section: "系统管理" },
  account: { title: "账户信息", section: "账户" }
};

export const availablePages = new Set(Object.keys(pageMetadata));
export const adminPages = new Set(["users", "tiers", "invites", "settings"]);

export function readPageFromLocation(locationHash = window.location.hash) {
  const locationPage = locationHash.replace(/^#\/?/, "").trim();
  if (locationPage === "guide") {
    return "tutorial";
  }
  return availablePages.has(locationPage) ? locationPage : "overview";
}

export function renderCurrentPage(state) {
  switch (state.currentPage) {
    case "keys":
      return renderKeysPage(state);
    case "tutorial":
      return renderConfigurationGuidePage();
    case "usage":
      return renderUsagePage(state);
    case "users":
      return renderUsersPage(state);
    case "tiers":
      return renderTiersPage(state);
    case "invites":
      return renderInvitesPage(state);
    case "settings":
      return renderSettingsPage(state);
    case "account":
      return renderAccountPage(state);
    case "overview":
    default:
      return renderOverviewPage(state);
  }
}

export function renderShell(state) {
  const currentMetadata = pageMetadata[state.currentPage] || pageMetadata.overview;
  return renderLayoutShell(state, currentMetadata, renderCurrentPage(state));
}
