(function () {
  const API_BASE = "/panel/v1";
  const storage = {
    token: "grok_mcp_panel_token",
    user: "grok_mcp_panel_user"
  };

  const routes = ["dashboard", "keys", "usage", "users", "tiers", "tutorial", "account"];
  const routeMeta = {
    dashboard: { label: "Dashboard", icon: "dashboard" },
    keys: { label: "Keys", icon: "vpn_key" },
    usage: { label: "Usage Stats", icon: "bar_chart" },
    users: { label: "User Management", icon: "group", admin: true },
    tiers: { label: "Tier Management", icon: "workspace_premium", admin: true },
    tutorial: { label: "Configuration Tutorial", icon: "menu_book", bottom: true },
    account: { label: "Account Settings", icon: "settings", bottom: true }
  };

  const state = {
    ready: false,
    loading: false,
    authMode: "login",
    route: readRoute(),
    token: getStored(storage.token),
    user: readJSON(storage.user),
    keys: [],
    users: [],
    tiers: [],
    usage: emptyUsage(),
    selectedKeyID: "all",
    sinceMode: "24h",
    search: "",
    modal: null,
    toast: null
  };

  const app = document.getElementById("app");

  document.addEventListener("submit", onSubmit);
  document.addEventListener("click", onClick);
  document.addEventListener("change", onChange);
  document.addEventListener("input", onInput);
  window.addEventListener("hashchange", async () => {
    state.route = readRoute();
    await loadRouteData();
    render();
  });

  bootstrap();

  async function bootstrap() {
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

  async function loadRouteData() {
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
        state.usage = await loadAggregatedUsage("24h");
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
      }
    } catch (err) {
      handleAPIError(err);
    } finally {
      state.loading = false;
    }
  }

  async function loadKeys() {
    const data = await api("/keys");
    state.keys = Array.isArray(data.keys) ? data.keys : [];
  }

  async function loadUsers() {
    const data = await api("/admin/users");
    state.users = Array.isArray(data.users) ? data.users : [];
  }

  async function loadTiers() {
    const data = await api("/admin/tiers");
    state.tiers = Array.isArray(data.tiers) ? data.tiers : [];
  }

  function rpmText(rpm) {
    return limitText(rpm);
  }

  async function loadAggregatedUsage(mode) {
    const since = sinceQuery(mode);
    const data = await api(`/usage${since}`);
    return normalizeUsage(data);
  }

  async function loadUsageForSelection() {
    const since = sinceQuery(state.sinceMode);
    if (state.selectedKeyID === "all") {
      return loadAggregatedUsage(state.sinceMode);
    }
    const data = await api(`/keys/${encodeURIComponent(state.selectedKeyID)}/usage${since}`);
    return normalizeUsage(data);
  }

  async function api(path, options = {}) {
    const headers = {
      "Accept": "application/json"
    };
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
    }
    if (options.auth !== false && state.token) {
      headers.Authorization = `Bearer ${state.token}`;
    }
    const res = await fetch(`${API_BASE}${path}`, {
      method: options.method || "GET",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body)
    });
    if (res.status === 204) {
      return null;
    }
    const text = await res.text();
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = { error: text };
      }
    }
    if (!res.ok) {
      const err = new Error(data && data.error ? data.error : `HTTP ${res.status}`);
      err.status = res.status;
      err.data = data;
      throw err;
    }
    return data;
  }

  function render() {
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

  function renderAuth() {
    const active = state.authMode;
    return `
      <main class="auth-screen">
        <section class="auth-card" aria-label="MCP Central 登录">
          <div class="auth-head">
            <div class="auth-logo"><span class="material-symbols-outlined">hub</span></div>
            <h1 class="auth-title">MCP Central</h1>
            <p class="auth-subtitle">Protocol Management Platform</p>
          </div>
          <div class="auth-body">
            <div class="auth-tabs" data-active="${active}">
              <span class="tab-indicator"></span>
              <button class="tab-button ${active === "login" ? "active" : ""}" data-action="auth-tab" data-tab="login" type="button">Login</button>
              <button class="tab-button ${active === "register" ? "active" : ""}" data-action="auth-tab" data-tab="register" type="button">Register</button>
            </div>
            <form id="login-form" class="form-stack ${active === "login" ? "" : "hidden"}">
              <div class="field">
                <label for="login-username">Username / ID</label>
                <div class="input-shell">
                  <span class="material-symbols-outlined">person</span>
                  <input id="login-username" name="username" class="input with-icon mono" autocomplete="username" placeholder="admin" required>
                </div>
              </div>
              <div class="field">
                <label for="login-password">Password</label>
                <div class="input-shell">
                  <span class="material-symbols-outlined">lock</span>
                  <input id="login-password" name="password" class="input with-icon mono" type="password" autocomplete="current-password" placeholder="••••••••" required>
                </div>
              </div>
              <button class="button" type="submit">
                <span>Authenticate</span>
                <span class="material-symbols-outlined">arrow_forward</span>
              </button>
            </form>
            <form id="register-form" class="form-stack ${active === "register" ? "" : "hidden"}">
              <div class="field">
                <label for="register-username">Desired Username</label>
                <div class="input-shell">
                  <span class="material-symbols-outlined">badge</span>
                  <input id="register-username" name="username" class="input with-icon mono" autocomplete="username" placeholder="new_user" required>
                </div>
              </div>
              <div class="field">
                <label for="register-password">Create Password</label>
                <div class="input-shell">
                  <span class="material-symbols-outlined">key</span>
                  <input id="register-password" name="password" class="input with-icon mono" type="password" autocomplete="new-password" placeholder="至少 8 位" minlength="8" required>
                </div>
              </div>
              <button class="button secondary" type="submit">
                <span class="material-symbols-outlined">person_add</span>
                <span>Create Account</span>
              </button>
            </form>
          </div>
        </section>
      </main>`;
  }

  function renderShell() {
    return `
      <div class="app-shell">
        ${renderSidebar()}
        <header class="topbar">
          <button class="icon-button mobile-menu" data-action="go" data-route="dashboard" title="Dashboard" type="button">
            <span class="material-symbols-outlined">developer_board</span>
          </button>
          <label class="search-box">
            <span class="material-symbols-outlined">search</span>
            <input id="global-search" value="${escapeHTML(state.search)}" placeholder="Search resources, logs..." autocomplete="off">
          </label>
          <div class="top-actions">
            <button class="icon-button" data-action="refresh" title="刷新" type="button"><span class="material-symbols-outlined">notifications</span></button>
            <button class="avatar" data-action="go" data-route="account" title="${escapeHTML(state.user.username)}" type="button">${escapeHTML(initials(state.user.username))}</button>
          </div>
        </header>
        <main class="main">
          <div class="content">
            ${state.loading ? renderInlineLoading() : renderRoute()}
          </div>
        </main>
      </div>`;
  }

  function renderSidebar() {
    const top = routes.filter((route) => !routeMeta[route].bottom).map(renderNavLink).join("");
    const bottom = routes.filter((route) => routeMeta[route].bottom).map(renderNavLink).join("");
    return `
      <aside class="sidebar">
        <div class="brand">
          <div class="brand-mark">
            <span class="material-symbols-outlined">developer_board</span>
          </div>
          <div class="brand-copy">
            <h1>MCP Central</h1>
            <p>Protocol Management</p>
          </div>
        </div>
        <nav class="nav-list" aria-label="主导航">
          ${top}
          <div class="nav-bottom">${bottom}</div>
        </nav>
      </aside>`;
  }

  function renderNavLink(route) {
    const meta = routeMeta[route];
    const locked = meta.admin && !isAdmin();
    return `
      <a class="nav-link ${state.route === route ? "active" : ""} ${locked ? "locked" : ""}" href="#/${route}" title="${escapeHTML(meta.label)}">
        <span class="material-symbols-outlined">${meta.icon}</span>
        <span>${escapeHTML(meta.label)}</span>
      </a>`;
  }

  function renderRoute() {
    if (state.route === "dashboard") return renderDashboard();
    if (state.route === "keys") return renderKeys();
    if (state.route === "usage") return renderUsage();
    if (state.route === "users") return renderUsers();
    if (state.route === "tiers") return renderTiers();
    if (state.route === "tutorial") return renderConfigurationTutorial();
    if (state.route === "account") return renderAccount();
    return renderDashboard();
  }

  function renderDashboard() {
    const usage = state.usage;
    const successPct = percentOf(state.user.success_calls, state.user.success_limit);
    const recentMinuteCalls = countRecordsInWindow(usage.records, 60 * 1000);
    const rpmPct = percentOf(recentMinuteCalls, state.user.rpm);
    const rpmProgress = state.user.rpm > 0 ? rpmPct : null;
    const successRate = usage.total_calls > 0 ? Math.round((usage.success_calls / usage.total_calls) * 1000) / 10 : 100;
    const dashboardAlert = buildDashboardAlert(usage.records);
    return `
      ${renderDashboardAlert(dashboardAlert)}
      <section class="grid metric-grid">
        ${metricCard("Rate Per Minute<br>(RPM)", `${formatNumber(recentMinuteCalls)} <span class="muted">/ ${rpmText(state.user.rpm)}</span>`, "speed", "User-level shared rate limit", rpmPct >= 90 ? "bad" : "good", rpmProgress)}
        ${metricCard("Success Rate", `${successRate}%`, "check_circle", usage.total_calls ? "Based on completed calls" : "No traffic yet", "good", null)}
        ${metricCard("Success Quota", `${formatNumber(state.user.success_calls)} <span class="muted">/ ${limitText(state.user.success_limit)}</span>`, "check_circle", quotaNote(successPct), successPct >= 90 ? "bad" : "good", successPct)}
      </section>
      <section class="grid viz-grid">
        <div class="card panel">
          <div class="panel-head">
            <h3>Traffic Volume</h3>
            <button class="button secondary small" data-action="go" data-route="usage" type="button">Last 24 Hours</button>
          </div>
          ${renderBars(usage.records)}
        </div>
        ${renderToolUsage(usage)}
      </section>
      ${renderRecentActivity(usage.records, true)}`;
  }

  function renderKeys() {
    const filtered = filteredKeys();
    return `
      <div class="page-head">
        <div>
          <h2>API Keys</h2>
          <p>Manage your active Model Context Protocol keys and permissions.</p>
        </div>
        <button class="button" data-action="open-create-key" type="button">
          <span class="material-symbols-outlined">add</span>
          <span>Create New Key</span>
        </button>
      </div>
      <section class="card table-card">
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Prefix</th>
                <th>Status</th>
                <th>Created Date</th>
                <th>Last Used</th>
                <th class="right">Actions</th>
              </tr>
            </thead>
            <tbody>
              ${filtered.length ? filtered.map(renderKeyRow).join("") : renderEmptyRow("vpn_key", "No API keys yet", "Create a key to connect an MCP client.")}
            </tbody>
          </table>
        </div>
      </section>`;
  }

  function renderConfigurationTutorial() {
    return `
      <div class="page-head" lang="en">
        <div>
          <h2>MCP Configuration Tutorial</h2>
          <p>Connect Claude Code, Codex CLI, and Cursor to this Streamable HTTP MCP server.</p>
        </div>
        <button class="button secondary" data-action="go" data-route="keys" type="button">
          <span class="material-symbols-outlined">vpn_key</span>
          <span>Manage API Keys</span>
        </button>
      </div>

      <section class="grid tutorial-grid" lang="en">
        <article class="card tutorial-card tutorial-card-wide">
          <div class="tutorial-card-head">
            <span class="material-symbols-outlined">settings_ethernet</span>
            <div>
              <h3>Common Setup</h3>
              <p>Use the MCP endpoint and an MCP API key generated in this panel.</p>
            </div>
          </div>
          <ol class="tutorial-steps">
            <li>Start the Grok MCP HTTP server and make sure it is reachable from your MCP client.</li>
            <li>Create or copy an MCP API key from the <strong>API Keys</strong> page.</li>
            <li>Use the MCP endpoint path <span class="mono">/mcp</span>. Do not use <span class="mono">/panel/v1</span>; that path is only for the panel API.</li>
            <li>Send the MCP API key as a bearer token in the <span class="mono">Authorization</span> header.</li>
          </ol>
          <pre class="tutorial-code"><code>MCP Endpoint: http://localhost:8080/mcp
Authorization: Bearer YOUR_MCP_API_KEY</code></pre>
          <p class="hint">The panel JWT is only for signing in to this dashboard. MCP clients must use an MCP API key.</p>
        </article>

        <article class="card tutorial-card">
          <div class="tutorial-card-head">
            <span class="material-symbols-outlined">terminal</span>
            <div>
              <h3>Claude Code</h3>
              <p>Add this server as an HTTP MCP server with a bearer token header.</p>
            </div>
          </div>
          <ol class="tutorial-steps">
            <li>Open a terminal in the project where Claude Code should use this server.</li>
            <li>Replace the endpoint and token placeholders below.</li>
            <li>List MCP servers in Claude Code and confirm the server is connected.</li>
          </ol>
          <pre class="tutorial-code"><code>claude mcp add grok-search \\
  --transport http \\
  --header "Authorization: Bearer YOUR_MCP_API_KEY" \\
  http://localhost:8080/mcp

claude mcp list</code></pre>
        </article>

        <article class="card tutorial-card">
          <div class="tutorial-card-head">
            <span class="material-symbols-outlined">code_blocks</span>
            <div>
              <h3>Codex CLI</h3>
              <p>Configure Codex with direct Streamable HTTP if supported, or use a stdio bridge.</p>
            </div>
          </div>
          <ol class="tutorial-steps">
            <li>Open <span class="mono">~/.codex/config.toml</span>.</li>
            <li>Use direct HTTP if your installed Codex version supports remote MCP servers.</li>
            <li>If direct HTTP is not available, use the <span class="mono">mcp-remote</span> bridge fallback.</li>
          </ol>
          <pre class="tutorial-code"><code>[mcp_servers.grok_search]
transport = "streamable_http"
url = "http://localhost:8080/mcp"
headers = { Authorization = "Bearer YOUR_MCP_API_KEY" }</code></pre>
          <pre class="tutorial-code"><code>[mcp_servers.grok_search]
command = "npx"
args = [
  "-y",
  "mcp-remote",
  "http://localhost:8080/mcp",
  "--header",
  "Authorization: Bearer YOUR_MCP_API_KEY"
]</code></pre>
        </article>

        <article class="card tutorial-card">
          <div class="tutorial-card-head">
            <span class="material-symbols-outlined">developer_board</span>
            <div>
              <h3>Cursor</h3>
              <p>Add this server to the global or project MCP configuration file.</p>
            </div>
          </div>
          <ol class="tutorial-steps">
            <li>Open <span class="mono">~/.cursor/mcp.json</span> for global setup, or <span class="mono">.cursor/mcp.json</span> for project setup.</li>
            <li>Add the server configuration below.</li>
            <li>Save the file, then restart Cursor or reload MCP tools.</li>
          </ol>
          <pre class="tutorial-code"><code>{
  "mcpServers": {
    "grok-search": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_MCP_API_KEY"
      }
    }
  }
}</code></pre>
        </article>

        <article class="card tutorial-card">
          <div class="tutorial-card-head">
            <span class="material-symbols-outlined">travel_explore</span>
            <div>
              <h3>Available Tools</h3>
              <p>Once connected, the MCP client can call these read-only search tools.</p>
            </div>
          </div>
          <ul class="tutorial-tool-list">
            <li><span class="mono">grok_web_search</span><span>Real-time public web search through Grok web search.</span></li>
            <li><span class="mono">grok_x_search</span><span>Real-time X post search through Grok X search.</span></li>
          </ul>
        </article>

        <article class="card tutorial-card tutorial-card-wide">
          <div class="tutorial-card-head">
            <span class="material-symbols-outlined">troubleshoot</span>
            <div>
              <h3>Troubleshooting</h3>
              <p>Use these checks when a client cannot discover or call the tools.</p>
            </div>
          </div>
          <ul class="tutorial-check-list">
            <li>Confirm the server is reachable at <span class="mono">http://localhost:8080/mcp</span> from the client machine.</li>
            <li>Confirm the client sends <span class="mono">Authorization: Bearer YOUR_MCP_API_KEY</span>.</li>
            <li>Confirm the token is an MCP API key from this panel, not a panel login token.</li>
            <li>Restart or reload the MCP client after editing its configuration file.</li>
            <li>Use <span class="mono">/mcp</span> for MCP traffic. Use <span class="mono">/panel/v1</span> only for dashboard API traffic.</li>
          </ul>
        </article>
      </section>`;
  }

  function renderKeyRow(key) {
    return `
      <tr>
        <td><strong>${escapeHTML(key.name || "Untitled Key")}</strong></td>
        <td class="mono muted">${escapeHTML(key.key_prefix || "mcp_...")}</td>
        <td>
          <label class="toggle" title="${key.enabled ? "Enabled" : "Disabled"}">
            <input type="checkbox" data-key-toggle="${escapeAttr(key.id)}" ${key.enabled ? "checked" : ""}>
            <span></span>
          </label>
        </td>
        <td>${formatDate(key.created_at)}</td>
        <td class="muted">${key.last_used_at ? relativeTime(key.last_used_at) : "Never"}</td>
        <td class="right">
          <span class="row-actions">
            <button class="mini-icon" data-action="key-usage" data-key-id="${escapeAttr(key.id)}" title="Usage" type="button"><span class="material-symbols-outlined">bar_chart</span></button>
            <button class="mini-icon" data-action="edit-key" data-key-id="${escapeAttr(key.id)}" title="Edit" type="button"><span class="material-symbols-outlined">edit</span></button>
            <button class="mini-icon danger" data-action="delete-key" data-key-id="${escapeAttr(key.id)}" title="Delete" type="button"><span class="material-symbols-outlined">delete</span></button>
          </span>
        </td>
      </tr>`;
  }

  function renderUsage() {
    const usage = state.usage;
    return `
      <div class="page-head">
        <div>
          <h2>Usage Stats</h2>
          <p>Review MCP tool calls, latency and success counters.</p>
        </div>
        <div class="toolbar">
          <select class="select" id="usage-key-select" aria-label="选择 Key">
            <option value="all" ${state.selectedKeyID === "all" ? "selected" : ""}>All Keys</option>
            ${state.keys.map((key) => `<option value="${escapeAttr(key.id)}" ${state.selectedKeyID === key.id ? "selected" : ""}>${escapeHTML(key.name || key.key_prefix)}</option>`).join("")}
          </select>
          <select class="select" id="usage-since-select" aria-label="选择时间范围">
            <option value="24h" ${state.sinceMode === "24h" ? "selected" : ""}>Last 24 Hours</option>
            <option value="7d" ${state.sinceMode === "7d" ? "selected" : ""}>Last 7 Days</option>
            <option value="all" ${state.sinceMode === "all" ? "selected" : ""}>All Time</option>
          </select>
          <button class="button secondary" data-action="refresh" type="button"><span class="material-symbols-outlined">refresh</span><span>Refresh</span></button>
        </div>
      </div>
      <section class="grid metric-grid">
        ${metricCard("Total Calls", formatNumber(usage.total_calls), "data_usage", "Selected range", "good", null)}
        ${metricCard("Success Calls", formatNumber(usage.success_calls), "check_circle", `${successPercent(usage)} success`, "good", null)}
        ${metricCard("Failed Calls", formatNumber(Math.max(0, usage.total_calls - usage.success_calls)), "error", "Not counted as success quota", usage.total_calls === usage.success_calls ? "good" : "bad", null)}
        ${metricCard("Active Keys", formatNumber(state.keys.filter((k) => k.enabled).length), "vpn_key", `${state.keys.length} total keys`, "good", null)}
      </section>
      <section class="grid viz-grid">
        <div class="card panel">
          <div class="panel-head">
            <h3>Traffic Volume</h3>
            <span class="mono muted">${escapeHTML(rangeLabel(state.sinceMode))}</span>
          </div>
          ${renderBars(usage.records)}
        </div>
        ${renderToolUsage(usage)}
      </section>
      ${renderRecentActivity(filteredRecords(usage.records), false)}`;
  }

  function renderUsers() {
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

  function renderUserRow(user) {
    const tierBadge = user.tier_name
      ? `<span class="badge off">${escapeHTML(user.tier_name)}</span>`
      : `<span class="muted">—</span>`;
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
          </span>
        </td>
      </tr>`;
  }

  function renderAccount() {
    const tierBadge = state.user.tier_name
      ? `<span class="badge off">${escapeHTML(state.user.tier_name)}</span>`
      : "";
    return `
      <div class="page-head">
        <div>
          <h2>Account Settings</h2>
          <p>Review the active session, RPM and success limit.</p>
        </div>
        <button class="button secondary" data-action="logout" type="button"><span class="material-symbols-outlined">logout</span><span>Logout</span></button>
      </div>
      <section class="grid settings-grid">
        <div class="card panel">
          <div class="panel-head">
            <h3>Profile</h3>
            <span class="badge ${state.user.enabled ? "" : "error"}">${state.user.enabled ? "Enabled" : "Disabled"}</span>
          </div>
          <div class="summary-list">
            ${summaryItem("Username", escapeHTML(state.user.username))}
            ${summaryItem("Role", `<span class="badge ${state.user.role === "admin" ? "" : "off"}">${escapeHTML(state.user.role)}</span>`)}
            ${tierBadge ? summaryItem("Tier", tierBadge) : ""}
            ${summaryItem("User ID", `<span class="mono">${escapeHTML(state.user.id)}</span>`)}
            ${summaryItem("Created", formatDateTime(state.user.created_at))}
            ${summaryItem("Updated", formatDateTime(state.user.updated_at))}
          </div>
        </div>
        <div class="card panel">
          <div class="panel-head">
            <h3>Quotas</h3>
          </div>
          <div class="quota-list">
            <div class="quota-item">
              <div class="field-row">
                <span class="field-label">RPM</span>
                <span class="mono">${rpmText(state.user.rpm)} req/min</span>
              </div>
              <span class="hint">每分钟请求上限，所有 Key 共享</span>
            </div>
            ${quotaProgress("Success Limit", state.user.success_calls, state.user.success_limit, "successful calls")}
          </div>
        </div>
      </section>`;
  }

  function metricCard(title, value, icon, note, tone, progress) {
    return `
      <div class="card metric-card">
        <div class="metric-top">
          <span class="metric-title">${title}</span>
          <span class="material-symbols-outlined muted">${icon}</span>
        </div>
        <div>
          <div class="metric-value">${value}</div>
          ${progress === null ? "" : `<div class="progress" style="margin-top: 16px;"><div class="progress-bar" style="width: ${clamp(progress, 0, 100)}%;"></div></div>`}
          <div class="metric-note ${tone || ""}">
            <span class="material-symbols-outlined" style="font-size: 16px;">${tone === "bad" ? "trending_up" : "trending_flat"}</span>
            <span>${escapeHTML(note || "Stable")}</span>
          </div>
        </div>
      </div>`;
  }

  function renderTiers() {
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

  function renderTierRow(tier) {
    return `
      <tr>
        <td><strong>${escapeHTML(tier.name)}</strong><div class="hint mono">${escapeHTML(shortID(tier.id))}</div></td>
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

  function tierOptions(selectedID) {
    return (state.tiers || [])
      .map((t) => `<option value="${escapeAttr(t.id)}" ${selectedID === t.id ? "selected" : ""}>${escapeHTML(t.name)} (L${t.level})</option>`)
      .join("");
  }

  function renderCreateTierModal() {
    return `
      <div class="modal-backdrop" data-action="close-modal">
        <section class="modal" role="dialog" aria-modal="true" aria-label="Create Tier" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>Create Tier</h3>
            <p>新建一个等级预设。</p>
            <form id="create-tier-form" class="form-stack" style="margin-top: 24px;">
              <div class="field">
                <label for="create-tier-name">Name</label>
                <input id="create-tier-name" name="name" class="input" placeholder="tier7" required>
              </div>
              <div class="field">
                <label for="create-tier-level">Level</label>
                <input id="create-tier-level" name="level" class="input mono" type="number" min="0" value="0">
              </div>
              <div class="field">
                <label for="create-tier-rpm">RPM</label>
                <input id="create-tier-rpm" name="rpm" class="input mono" type="number" min="0" value="0">
                <span class="hint">0 means unlimited RPM.</span>
              </div>
              <div class="field">
                <label for="create-tier-success">Success Limit</label>
                <input id="create-tier-success" name="success_limit" class="input mono" type="number" min="0" value="0">
                <span class="hint">0 means unlimited.</span>
              </div>
              <div class="modal-actions">
                <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
                <button class="button" type="submit"><span class="material-symbols-outlined">add</span><span>Create</span></button>
              </div>
            </form>
          </div>
        </section>
      </div>`;
  }

  function renderEditTierModal(tier) {
    if (!tier) return "";
    return `
      <div class="modal-backdrop" data-action="close-modal">
        <section class="modal" role="dialog" aria-modal="true" aria-label="Edit Tier" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>Edit Tier</h3>
            <p>${escapeHTML(tier.name)} preset values.</p>
            <form id="edit-tier-form" class="form-stack" style="margin-top: 24px;">
              <input type="hidden" name="id" value="${escapeAttr(tier.id)}">
              <div class="field">
                <label for="edit-tier-name">Name</label>
                <input id="edit-tier-name" name="name" class="input" value="${escapeAttr(tier.name || "")}" required>
              </div>
              <div class="field">
                <label for="edit-tier-level">Level</label>
                <input id="edit-tier-level" name="level" class="input mono" type="number" min="0" value="${Number(tier.level) || 0}">
              </div>
              <div class="field">
                <label for="edit-tier-rpm">RPM</label>
                <input id="edit-tier-rpm" name="rpm" class="input mono" type="number" min="0" value="${Number(tier.rpm) || 0}">
              </div>
              <div class="field">
                <label for="edit-tier-success">Success Limit</label>
                <input id="edit-tier-success" name="success_limit" class="input mono" type="number" min="0" value="${Number(tier.success_limit) || 0}">
                <span class="hint">0 means unlimited.</span>
              </div>
              <div class="modal-actions">
                <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
                <button class="button" type="submit"><span class="material-symbols-outlined">save</span><span>Save</span></button>
              </div>
            </form>
          </div>
        </section>
      </div>`;
  }

  function renderDashboardAlert(alert) {
    if (!alert) return "";
    return `
      <section class="alert">
        <span class="material-symbols-outlined">warning</span>
        <div>
          <h3>${escapeHTML(alert.title)}</h3>
          <p>${escapeHTML(alert.body)}</p>
        </div>
        <button class="button secondary" data-action="go" data-route="account" type="button">Review Quotas</button>
      </section>`;
  }

  function renderBars(records) {
    const buckets = bucketRecords(records || []);
    const max = Math.max(1, ...buckets);
    return `
      <div class="bar-chart" aria-label="流量柱状图">
        ${buckets.map((value) => `<div class="bar" title="${value} calls" style="height: ${Math.max(8, Math.round((value / max) * 92))}%;"></div>`).join("")}
      </div>
      <div class="chart-axis"><span>00:00</span><span>06:00</span><span>12:00</span><span>18:00</span><span>Now</span></div>`;
  }

  function renderToolUsage(usage) {
    const rows = Object.entries(usage.by_tool || {}).sort((a, b) => b[1] - a[1]);
    const total = rows.reduce((sum, row) => sum + row[1], 0);
    const top = rows[0] || ["No Data", 0];
    const pct = total ? Math.round((top[1] / total) * 100) : 0;
    const rest = Math.max(0, 100 - pct);
    // When only two tools exist, show the second tool's real name instead of "Other Tools".
    const second = rows[1];
    const secondLabel = rows.length > 2 ? "Other Tools" : (second ? second[0] : null);
    return `
      <div class="card panel donut-wrap">
        <div class="panel-head">
          <h3>Tool Usage</h3>
        </div>
        <div class="donut" style="--donut-value: ${pct}%">
          <div class="donut-inner">
            <div>
              <strong>${pct}%</strong>
              <span>${escapeHTML(trimToolName(top[0]))}</span>
            </div>
          </div>
        </div>
        <div class="legend">
          <div class="legend-row">
            <span class="legend-name"><span class="dot"></span>${escapeHTML(top[0])}</span>
            <span class="mono">${pct}%</span>
          </div>
          ${secondLabel ? `
          <div class="legend-row">
            <span class="legend-name"><span class="dot light"></span>${escapeHTML(secondLabel)}</span>
            <span class="mono">${rest}%</span>
          </div>` : ""}
        </div>
      </div>`;
  }

  function renderRecentActivity(records, compact) {
    const rows = filteredRecords(records || []).slice(0, compact ? 5 : 500);
    return `
      <section class="card table-card">
        <div class="table-head">
          <h3>Recent Activity</h3>
          <button class="button ghost small" data-action="go" data-route="usage" type="button">View All Logs</button>
        </div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>TOOL NAME</th>
                <th>REQUEST ID</th>
                <th>TIMESTAMP</th>
                <th>LATENCY</th>
                <th class="right">STATUS</th>
              </tr>
            </thead>
            <tbody>
              ${rows.length ? rows.map(renderActivityRow).join("") : renderEmptyRow("receipt_long", "No usage records", "MCP tools/call activity will appear here.")}
            </tbody>
          </table>
        </div>
      </section>`;
  }

  function renderActivityRow(record) {
    return `
      <tr>
        <td class="mono" style="color: var(--primary);">${escapeHTML(record.tool_name || "unknown")}</td>
        <td class="muted">${escapeHTML(`req_${String(record.id || "").padStart(8, "0").slice(-8)}`)}</td>
        <td>${relativeTime(record.timestamp)}</td>
        <td>${record.duration_ms ? `${formatNumber(record.duration_ms)}ms` : "--"}</td>
        <td class="right"><span class="badge ${record.success ? "" : "error"}">${record.success ? "Success" : "Failed"}</span></td>
      </tr>`;
  }

  function renderEmptyRow(icon, title, text) {
    return `
      <tr>
        <td colspan="8">
          <div class="empty">
            <div>
              <span class="material-symbols-outlined">${icon}</span>
              <h3>${escapeHTML(title)}</h3>
              <p>${escapeHTML(text)}</p>
            </div>
          </div>
        </td>
      </tr>`;
  }

  function renderModal() {
    if (!state.modal) return "";
    if (state.modal.type === "create-key") return renderCreateKeyModal();
    if (state.modal.type === "key-created") return renderKeyCreatedModal(state.modal);
    if (state.modal.type === "edit-key") return renderEditKeyModal(state.modal.key);
    if (state.modal.type === "edit-user") return renderEditUserModal(state.modal.user);
    if (state.modal.type === "create-tier") return renderCreateTierModal();
    if (state.modal.type === "edit-tier") return renderEditTierModal(state.modal.tier);
    if (state.modal.type === "user-usage") return renderUserUsageModal(state.modal.user, state.modal.usage);
    return "";
  }

  function renderCreateKeyModal() {
    return `
      <div class="modal-backdrop" data-action="close-modal">
        <section class="modal" role="dialog" aria-modal="true" aria-label="Create New Key" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>Create New Key</h3>
            <p>Create a client key for the current user. The raw key will be shown once.</p>
            <form id="create-key-form" class="form-stack" style="margin-top: 24px;">
              <div class="field">
                <label for="key-name">Key Name</label>
                <input id="key-name" name="name" class="input" placeholder="Production Backend" required>
              </div>
              <div class="modal-actions">
                <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
                <button class="button" type="submit"><span class="material-symbols-outlined">add</span><span>Create</span></button>
              </div>
            </form>
          </div>
        </section>
      </div>`;
  }

  function renderKeyCreatedModal(modal) {
    const copyFailed = Boolean(modal.copyFailed);
    const copySucceeded = Boolean(modal.copySucceeded);
    const copyNote = copyFailed
      ? "浏览器拒绝自动复制。密钥已选中，请按 Ctrl+C 手动复制。"
      : copySucceeded
        ? "密钥已复制到剪贴板。此密钥只显示一次，请立即保存。"
        : "此密钥只显示一次，可以点击复制按钮或直接选中文本复制。";
    return `
      <div class="modal-backdrop">
        <section class="modal" role="dialog" aria-modal="true" aria-label="New API Key Created" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>New API Key Created</h3>
            <p>Your new key '${escapeHTML(modal.key.name)}' is ready to use.</p>
            <div class="warning-box">
              <span class="material-symbols-outlined">warning</span>
              <div>
                <strong>Save this key now.</strong>
                <p>For your security, it will only be shown once. If you lose it, you will need to generate a new key.</p>
              </div>
            </div>
            <div class="key-copy">
              <label class="field-label" for="created-api-key">Secret Key</label>
              <div class="copy-shell ${copyFailed ? "manual" : ""}">
                <input id="created-api-key" class="input mono subtle" value="${escapeAttr(modal.apiKey)}" readonly>
                <button class="mini-icon" data-action="copy-created-key" title="Copy" type="button"><span class="material-symbols-outlined">content_copy</span></button>
              </div>
              <p class="hint ${copyFailed ? "manual-copy-note" : copySucceeded ? "auto-copy-note" : ""}">${copyNote}</p>
            </div>
            <div class="modal-actions">
              <button class="button secondary" data-action="close-modal" type="button">I've Saved It</button>
              <button class="button" data-action="copy-created-key" type="button"><span class="material-symbols-outlined">content_copy</span><span>Copy Key</span></button>
            </div>
          </div>
        </section>
      </div>`;
  }

  function renderEditKeyModal(key) {
    if (!key) return "";
    return `
      <div class="modal-backdrop" data-action="close-modal">
        <section class="modal" role="dialog" aria-modal="true" aria-label="Edit Key" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>Edit API Key</h3>
            <p>Update the key label or disable access immediately.</p>
            <form id="edit-key-form" class="form-stack" style="margin-top: 24px;">
              <input type="hidden" name="id" value="${escapeAttr(key.id)}">
              <div class="field">
                <label for="edit-key-name">Name</label>
                <input id="edit-key-name" name="name" class="input" value="${escapeAttr(key.name || "")}" required>
              </div>
              <div class="field-row">
                <span>
                  <strong>Enabled</strong>
                  <span class="hint" style="display: block;">Disabled keys cannot call /mcp.</span>
                </span>
                <label class="toggle">
                  <input type="checkbox" name="enabled" ${key.enabled ? "checked" : ""}>
                  <span></span>
                </label>
              </div>
              <div class="modal-actions">
                <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
                <button class="button" type="submit"><span class="material-symbols-outlined">save</span><span>Save</span></button>
              </div>
            </form>
          </div>
        </section>
      </div>`;
  }

  function renderEditUserModal(user) {
    if (!user) return "";
    return `
      <div class="modal-backdrop" data-action="close-modal">
        <section class="modal" role="dialog" aria-modal="true" aria-label="Edit User" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>Edit User</h3>
            <p>${escapeHTML(user.username)} access and tier assignment.</p>
            <form id="edit-user-form" class="form-stack" style="margin-top: 24px;">
              <input type="hidden" name="id" value="${escapeAttr(user.id)}">
              <div class="field-row">
                <span>
                  <strong>Enabled</strong>
                  <span class="hint" style="display: block;">Disabled users cannot log in or use keys.</span>
                </span>
                <label class="toggle"><input type="checkbox" name="enabled" ${user.enabled ? "checked" : ""}><span></span></label>
              </div>
              <div class="field">
                <label for="edit-user-role">Role</label>
                <select id="edit-user-role" name="role" class="select">
                  <option value="user" ${user.role === "user" ? "selected" : ""}>user</option>
                  <option value="admin" ${user.role === "admin" ? "selected" : ""}>admin</option>
                </select>
              </div>
              <div class="field-row">
                <span>
                  <strong>Revoke Tokens</strong>
                  <span class="hint" style="display: block;">强制该用户所有已签发的登录令牌立即失效（强制下线）。</span>
                </span>
                <label class="toggle"><input type="checkbox" name="revoke_tokens"><span></span></label>
              </div>
              <div class="field">
                <label for="edit-user-tier">Tier</label>
                <select id="edit-user-tier" name="tier_id" class="select" required>
                  ${tierOptions(user.tier_id || "")}
                </select>
                <span class="hint">必须选择 tier；限额（RPM / success limit）完全由 tier 决定，用户不再保留独立限额。调整 tier 预设请到 Tier Management 页。</span>
              </div>
              <div class="modal-actions">
                <button class="button secondary" data-action="close-modal" type="button">Cancel</button>
                <button class="button" type="submit"><span class="material-symbols-outlined">save</span><span>Save</span></button>
              </div>
            </form>
          </div>
        </section>
      </div>`;
  }

  function renderUserUsageModal(user, usage) {
    return `
      <div class="modal-backdrop" data-action="close-modal">
        <section class="modal" role="dialog" aria-modal="true" aria-label="User Usage" data-modal>
          <button class="icon-button modal-close" data-action="close-modal" type="button"><span class="material-symbols-outlined">close</span></button>
          <div class="modal-body">
            <h3>User Usage</h3>
            <p>${escapeHTML(user.username)} aggregate usage.</p>
            <div class="grid metric-grid" style="grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 24px 0;">
              ${metricCard("Total Calls", formatNumber(usage.total_calls), "data_usage", "All user keys", "good", null)}
              ${metricCard("Success Calls", formatNumber(usage.success_calls), "check_circle", `${successPercent(usage)} success`, "good", null)}
            </div>
            ${renderRecentActivity(usage.records || [], true)}
          </div>
        </section>
      </div>`;
  }

  function renderInlineLoading() {
    return `<section class="card empty"><div><div class="spinner"></div><p>Loading current view...</p></div></section>`;
  }

  function renderLoading(text) {
    return `<main class="loading-screen"><div><div class="spinner"></div><p>${escapeHTML(text)}</p></div></main>`;
  }

  function renderToast() {
    if (!state.toast) return "";
    return `
      <aside class="toast ${state.toast.type}">
        <span class="material-symbols-outlined">${state.toast.type === "error" ? "error" : "check_circle"}</span>
        <div>${escapeHTML(state.toast.message)}</div>
      </aside>`;
  }

  async function onSubmit(event) {
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    event.preventDefault();

    if (form.id === "login-form") {
      await submitLogin(form);
    } else if (form.id === "register-form") {
      await submitRegister(form);
    } else if (form.id === "create-key-form") {
      await submitCreateKey(form);
    } else if (form.id === "edit-key-form") {
      await submitEditKey(form);
    } else if (form.id === "edit-user-form") {
      await submitEditUser(form);
    } else if (form.id === "create-tier-form") {
      await submitCreateTier(form);
    } else if (form.id === "edit-tier-form") {
      await submitEditTier(form);
    }
  }

  async function submitLogin(form) {
    const data = new FormData(form);
    try {
      const resp = await api("/auth/login", {
        method: "POST",
        auth: false,
        body: {
          username: String(data.get("username") || "").trim(),
          password: String(data.get("password") || "")
        }
      });
      state.token = resp.token;
      state.user = resp.user;
      setStored(storage.token, state.token);
      setStored(storage.user, JSON.stringify(state.user));
      notify("登录成功。", "success");
      navigate("dashboard");
      await loadRouteData();
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function submitRegister(form) {
    const data = new FormData(form);
    const username = String(data.get("username") || "").trim();
    const password = String(data.get("password") || "");
    try {
      await api("/auth/register", {
        method: "POST",
        auth: false,
        body: { username, password }
      });
      notify("注册成功，正在登录。", "success");
      const loginForm = new FormData();
      loginForm.set("username", username);
      loginForm.set("password", password);
      const resp = await api("/auth/login", {
        method: "POST",
        auth: false,
        body: { username, password }
      });
      state.token = resp.token;
      state.user = resp.user;
      setStored(storage.token, state.token);
      setStored(storage.user, JSON.stringify(state.user));
      navigate("dashboard");
      await loadRouteData();
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function submitCreateKey(form) {
    const data = new FormData(form);
    try {
      const resp = await api("/keys", {
        method: "POST",
        body: { name: String(data.get("name") || "").trim() }
      });
      await loadKeys();
      state.modal = { type: "key-created", key: resp.key, apiKey: resp.api_key };
      window.clearTimeout(notify.timer);
      state.toast = null;
      render();
      await copyCreatedKey({ automatic: true });
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function submitEditKey(form) {
    const data = new FormData(form);
    const id = String(data.get("id") || "");
    try {
      await api(`/keys/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: {
          name: String(data.get("name") || "").trim(),
          enabled: data.get("enabled") === "on"
        }
      });
      state.modal = null;
      await loadKeys();
      notify("Key 已更新。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function submitEditUser(form) {
    const data = new FormData(form);
    const id = String(data.get("id") || "");
    const body = {
      enabled: data.get("enabled") === "on",
      role: String(data.get("role") || "user"),
      tier_id: String(data.get("tier_id") || "")
    };
    if (data.get("revoke_tokens") === "on") {
      body.revoke_tokens = true;
    }
    try {
      await api(`/admin/users/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body
      });
      state.modal = null;
      await loadUsers();
      notify("用户已更新。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function submitCreateTier(form) {
    const data = new FormData(form);
    try {
      await api("/admin/tiers", {
        method: "POST",
        body: {
          name: String(data.get("name") || "").trim(),
          level: Number(data.get("level") || 0),
          rpm: Number(data.get("rpm") || 0),
          success_limit: Number(data.get("success_limit") || 0)
        }
      });
      state.modal = null;
      await loadTiers();
      notify("等级已创建。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function submitEditTier(form) {
    const data = new FormData(form);
    const id = String(data.get("id") || "");
    try {
      await api(`/admin/tiers/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: {
          name: String(data.get("name") || "").trim(),
          level: Number(data.get("level") || 0),
          rpm: Number(data.get("rpm") || 0),
          success_limit: Number(data.get("success_limit") || 0)
        }
      });
      state.modal = null;
      await loadTiers();
      notify("等级已更新。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function deleteTier(id) {
    const tier = state.tiers.find((item) => item.id === id);
    if (!tier) return;
    if (!window.confirm(`Delete tier "${tier.name}"?`)) return;
    try {
      await api(`/admin/tiers/${encodeURIComponent(id)}`, { method: "DELETE" });
      await loadTiers();
      notify("等级已删除。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function onClick(event) {
    const actionEl = event.target.closest("[data-action]");
    if (!actionEl) return;
    const action = actionEl.dataset.action;

    if (action === "auth-tab") {
      state.authMode = actionEl.dataset.tab === "register" ? "register" : "login";
      render();
    } else if (action === "go") {
      navigate(actionEl.dataset.route || "dashboard");
    } else if (action === "refresh") {
      await loadRouteData();
      render();
    } else if (action === "open-create-key") {
      state.modal = { type: "create-key" };
      render();
    } else if (action === "close-modal") {
      if (!event.target.closest("[data-modal]") || actionEl.classList.contains("modal-close") || actionEl.classList.contains("button")) {
        state.modal = null;
        render();
      }
    } else if (action === "copy-created-key") {
      await copyCreatedKey();
    } else if (action === "edit-key") {
      const key = state.keys.find((item) => item.id === actionEl.dataset.keyId);
      state.modal = { type: "edit-key", key };
      render();
    } else if (action === "delete-key") {
      await deleteKey(actionEl.dataset.keyId);
    } else if (action === "key-usage") {
      state.selectedKeyID = actionEl.dataset.keyId || "all";
      navigate("usage");
    } else if (action === "edit-user") {
      const user = state.users.find((item) => item.id === actionEl.dataset.userId);
      state.modal = { type: "edit-user", user };
      render();
    } else if (action === "open-create-tier") {
      state.modal = { type: "create-tier" };
      render();
    } else if (action === "edit-tier") {
      const tier = state.tiers.find((item) => item.id === actionEl.dataset.tierId);
      state.modal = { type: "edit-tier", tier };
      render();
    } else if (action === "delete-tier") {
      await deleteTier(actionEl.dataset.tierId);
    } else if (action === "user-usage") {
      await openUserUsage(actionEl.dataset.userId);
    } else if (action === "logout") {
      clearSession();
      notify("已退出登录。", "success");
      render();
    }
  }

  async function onChange(event) {
    const target = event.target;
    if (!(target instanceof HTMLElement)) return;

    if (target.matches("[data-key-toggle]")) {
      const checkbox = target;
      await updateKeyEnabled(checkbox.dataset.keyToggle, checkbox.checked);
    } else if (target.id === "usage-key-select") {
      state.selectedKeyID = target.value;
      await loadRouteData();
      render();
    } else if (target.id === "usage-since-select") {
      state.sinceMode = target.value;
      await loadRouteData();
      render();
    }
  }

  function onInput(event) {
    const target = event.target;
    if (!(target instanceof HTMLInputElement)) return;
    if (target.id === "global-search") {
      state.search = target.value;
      render();
      const next = document.getElementById("global-search");
      if (next) {
        next.focus();
        next.setSelectionRange(next.value.length, next.value.length);
      }
    }
  }

  async function updateKeyEnabled(id, enabled) {
    try {
      await api(`/keys/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: { enabled }
      });
      const key = state.keys.find((item) => item.id === id);
      if (key) key.enabled = enabled;
      notify(enabled ? "Key 已启用。" : "Key 已禁用。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      await loadKeys();
      render();
    }
  }

  async function deleteKey(id) {
    const key = state.keys.find((item) => item.id === id);
    if (!key) return;
    if (!window.confirm(`Delete API key "${key.name || key.key_prefix}"?`)) return;
    try {
      await api(`/keys/${encodeURIComponent(id)}`, { method: "DELETE" });
      await loadKeys();
      notify("Key 已删除。", "success");
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function openUserUsage(id) {
    const user = state.users.find((item) => item.id === id);
    if (!user) return;
    try {
      const usage = await api(`/admin/users/${encodeURIComponent(id)}/usage`);
      state.modal = { type: "user-usage", user, usage: normalizeUsage(usage) };
      render();
    } catch (err) {
      notify(errorText(err), "error");
      render();
    }
  }

  async function copyCreatedKey(options = {}) {
    const input = document.getElementById("created-api-key");
    const value = input ? input.value : state.modal && state.modal.apiKey;
    if (!value) return;
    let copied = copyTextWithSelection(value, input);
    if (!copied) {
      try {
        if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") {
          throw new Error("clipboard unavailable");
        }
        await navigator.clipboard.writeText(value);
        copied = true;
      } catch {
        copied = false;
      }
    }

    if (state.modal && state.modal.type === "key-created") {
      state.modal.copyFailed = !copied;
      state.modal.copySucceeded = copied;
    }

    if (copied) {
      notify(options.automatic ? "Key 已自动复制到剪贴板。" : "已复制到剪贴板。", "success");
    } else {
      window.clearTimeout(notify.timer);
      state.toast = null;
    }

    render();
    if (state.modal && state.modal.type === "key-created" && state.modal.copyFailed) {
      selectCreatedKey();
    }
    return copied;
  }

  function copyTextWithSelection(value, input) {
    const target = input || document.createElement("textarea");
    let appended = false;

    if (!input) {
      target.value = value;
      target.setAttribute("readonly", "");
      target.style.position = "fixed";
      target.style.left = "-9999px";
      target.style.top = "0";
      document.body.appendChild(target);
      appended = true;
    }

    try {
      target.focus({ preventScroll: true });
      target.select();
      target.setSelectionRange(0, target.value.length);
      return typeof document.execCommand === "function" && document.execCommand("copy");
    } catch {
      return false;
    } finally {
      if (appended) {
        target.remove();
      }
    }
  }

  function selectCreatedKey() {
    const input = document.getElementById("created-api-key");
    if (!input) return;
    input.focus({ preventScroll: true });
    input.select();
    input.setSelectionRange(0, input.value.length);
  }

  function navigate(route) {
    const next = routes.includes(route) ? route : "dashboard";
    if (routeMeta[next].admin && !isAdmin()) {
      state.route = next;
      window.location.hash = `#/${next}`;
      render();
      return;
    }
    if (window.location.hash !== `#/${next}`) {
      window.location.hash = `#/${next}`;
    } else {
      state.route = next;
      loadRouteData().then(render);
    }
  }

  function handleAPIError(err) {
    if (err && err.status === 401) {
      clearSession();
      notify("登录已失效，请重新登录。", "error");
    } else {
      notify(errorText(err), "error");
    }
  }

  function clearSession() {
    state.token = "";
    state.user = null;
    state.keys = [];
    state.users = [];
    state.usage = emptyUsage();
    removeStored(storage.token);
    removeStored(storage.user);
  }

  function notify(message, type) {
    const id = `${Date.now()}-${Math.random()}`;
    state.toast = { id, message, type: type || "success" };
    window.clearTimeout(notify.timer);
    notify.timer = window.setTimeout(() => {
      if (state.toast && state.toast.id === id) {
        state.toast = null;
        render();
      }
    }, 3600);
  }

  function readRoute() {
    const raw = window.location.hash.replace(/^#\/?/, "");
    return routes.includes(raw) ? raw : "dashboard";
  }

  function isAdmin() {
    return state.user && state.user.role === "admin";
  }

  function emptyUsage() {
    return { total_calls: 0, success_calls: 0, by_tool: {}, records: [] };
  }

  function normalizeUsage(data) {
    return {
      total_calls: Number(data && data.total_calls) || 0,
      success_calls: Number(data && data.success_calls) || 0,
      by_tool: data && data.by_tool ? data.by_tool : {},
      records: Array.isArray(data && data.records) ? data.records : []
    };
  }

  function aggregateUsage(parts) {
    const usage = emptyUsage();
    for (const part of parts.map(normalizeUsage)) {
      usage.total_calls += part.total_calls;
      usage.success_calls += part.success_calls;
      for (const [tool, count] of Object.entries(part.by_tool || {})) {
        usage.by_tool[tool] = (usage.by_tool[tool] || 0) + Number(count || 0);
      }
      usage.records.push(...(part.records || []));
    }
    usage.records.sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
    return usage;
  }

  function sinceQuery(mode) {
    if (mode === "all") return "";
    const now = Date.now();
    const ms = mode === "7d" ? 7 * 24 * 60 * 60 * 1000 : 24 * 60 * 60 * 1000;
    return `?since=${encodeURIComponent(new Date(now - ms).toISOString())}`;
  }

  function rangeLabel(mode) {
    if (mode === "7d") return "Last 7 Days";
    if (mode === "all") return "All Time";
    return "Last 24 Hours";
  }

  function bucketRecords(records) {
    const buckets = new Array(8).fill(0);
    const now = Date.now();
    const start = now - 24 * 60 * 60 * 1000;
    for (const record of records) {
      const ts = new Date(record.timestamp).getTime();
      if (!Number.isFinite(ts) || ts < start || ts > now) continue;
      const index = Math.min(7, Math.max(0, Math.floor(((ts - start) / (now - start)) * 8)));
      buckets[index] += 1;
    }
    if (buckets.every((item) => item === 0)) {
      return [1, 2, 3, 5, 4, 6, 3, 2].map(() => 0);
    }
    return buckets;
  }

  function filteredKeys() {
    const q = state.search.trim().toLowerCase();
    if (!q) return state.keys;
    return state.keys.filter((key) => [key.name, key.key_prefix, key.id].some((value) => String(value || "").toLowerCase().includes(q)));
  }

  function filteredUsers() {
    const q = state.search.trim().toLowerCase();
    if (!q) return state.users;
    return state.users.filter((user) => [user.username, user.role, user.id].some((value) => String(value || "").toLowerCase().includes(q)));
  }

  function filteredRecords(records) {
    const q = state.search.trim().toLowerCase();
    const sorted = [...(records || [])].sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
    if (!q) return sorted;
    return sorted.filter((record) => [record.tool_name, record.key_id, record.id, record.success ? "success" : "failed"].some((value) => String(value || "").toLowerCase().includes(q)));
  }

  function buildDashboardAlert(records) {
    const successLimitAlert = buildSuccessQuotaDashboardAlert();
    if (successLimitAlert) {
      return successLimitAlert;
    }
    return buildRPMDashboardAlert(records);
  }

  function buildSuccessQuotaDashboardAlert() {
    const successLimit = Number(state.user.success_limit) || 0;
    if (successLimit <= 0) {
      return null;
    }
    const successLimitPercent = percentOf(state.user.success_calls, successLimit);
    if (successLimitPercent < 90) {
      return null;
    }
    return {
      title: "Success Limit Near Capacity",
      body: `You are currently using ${Math.round(successLimitPercent)}% of your success limit.`
    };
  }

  function buildRPMDashboardAlert(records) {
    const rpmLimit = Number(state.user.rpm) || 0;
    if (rpmLimit <= 0) {
      return null;
    }
    const recentMinuteCalls = countRecordsInWindow(records, 60 * 1000);
    const rpmWarningThreshold = Math.max(1, Math.ceil(rpmLimit * 0.9));
    if (recentMinuteCalls < rpmWarningThreshold) {
      return null;
    }
    return {
      title: "Rate Limit Near Capacity",
      body: `${formatNumber(recentMinuteCalls)} calls in the last 60 seconds are approaching the configured user-level RPM limit.`
    };
  }

  function countRecordsInWindow(records, windowMs) {
    const now = Date.now();
    const earliestAllowedTimestamp = now - windowMs;
    return (records || []).reduce((count, record) => {
      const timestamp = new Date(record.timestamp).getTime();
      if (!Number.isFinite(timestamp) || timestamp < earliestAllowedTimestamp || timestamp > now) {
        return count;
      }
      return count + 1;
    }, 0);
  }

  function quotaProgress(label, used, limit, note) {
    const pct = percentOf(used, limit);
    return `
      <div class="quota-item">
        <div class="field-row">
          <span class="field-label">${escapeHTML(label)}</span>
          <span class="mono">${formatNumber(used)}${limit ? ` / ${formatNumber(limit)}` : " / unlimited"}</span>
        </div>
        <div class="progress"><div class="progress-bar" style="width: ${limit ? clamp(pct, 0, 100) : 100}%;"></div></div>
        <span class="hint">${escapeHTML(note)}</span>
      </div>`;
  }

  function summaryItem(label, value) {
    return `<div class="summary-item"><span class="summary-label">${escapeHTML(label)}</span><span>${value}</span></div>`;
  }

  function quotaNote(pct) {
    if (!Number.isFinite(pct) || pct === 0) return "Unlimited or unused";
    return `${Math.round(pct)}% used`;
  }

  function percentOf(value, limit) {
    const n = Number(value) || 0;
    const l = Number(limit) || 0;
    if (l <= 0) return 0;
    return (n / l) * 100;
  }

  function successPercent(usage) {
    if (!usage.total_calls) return "100%";
    return `${Math.round((usage.success_calls / usage.total_calls) * 1000) / 10}%`;
  }

  function limitText(limit) {
    const n = Number(limit) || 0;
    return n <= 0 ? "∞" : formatNumber(n);
  }

  function formatNumber(value) {
    const n = Number(value) || 0;
    return new Intl.NumberFormat("en-US").format(n);
  }

  function formatDate(value) {
    if (!value) return "--";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "--";
    return date.toLocaleDateString("en-US", { year: "numeric", month: "short", day: "2-digit" });
  }

  function formatDateTime(value) {
    if (!value) return "--";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "--";
    return date.toLocaleString("en-US");
  }

  function relativeTime(value) {
    if (!value) return "Never";
    const ts = new Date(value).getTime();
    if (!Number.isFinite(ts)) return "Never";
    const diff = Date.now() - ts;
    const abs = Math.abs(diff);
    const minute = 60 * 1000;
    const hour = 60 * minute;
    const day = 24 * hour;
    const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
    if (abs < minute) return "Just now";
    if (abs < hour) return rtf.format(-Math.round(diff / minute), "minute");
    if (abs < day) return rtf.format(-Math.round(diff / hour), "hour");
    return rtf.format(-Math.round(diff / day), "day");
  }

  function trimToolName(name) {
    if (!name || name === "No Data") return name || "No Data";
    const parts = String(name).split(".");
    return parts[parts.length - 1] || name;
  }

  function shortID(id) {
    const text = String(id || "");
    return text.length > 12 ? `${text.slice(0, 6)}...${text.slice(-4)}` : text;
  }

  function initials(username) {
    const text = String(username || "U").trim();
    return text ? text[0].toUpperCase() : "U";
  }

  function clamp(value, min, max) {
    return Math.min(max, Math.max(min, Number(value) || 0));
  }

  function escapeHTML(value) {
    return String(value ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function escapeAttr(value) {
    return escapeHTML(value);
  }

  function getStored(key) {
    try {
      return window.localStorage.getItem(key) || "";
    } catch {
      return "";
    }
  }

  function setStored(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch {
      return undefined;
    }
  }

  function removeStored(key) {
    try {
      window.localStorage.removeItem(key);
    } catch {
      return undefined;
    }
  }

  function readJSON(key) {
    const raw = getStored(key);
    if (!raw) return null;
    try {
      return JSON.parse(raw);
    } catch {
      return null;
    }
  }

  function errorText(err) {
    if (!err) return "请求失败。";
    if (err.status === 401) return "认证失败，请检查账号、密码或 Token。";
    if (err.status === 403) return "权限不足或用户已被禁用。";
    if (err.status === 409) return "用户名已存在。";
    if (err.status === 429) return "请求被限流或额度已耗尽。";
    return err.message || "请求失败。";
  }
})();
