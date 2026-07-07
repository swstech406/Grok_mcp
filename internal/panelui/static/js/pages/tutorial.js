export function renderConfigurationTutorial() {
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

      <div class="tutorial-column">
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
      </div>

      <div class="tutorial-column">
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
      </div>

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
