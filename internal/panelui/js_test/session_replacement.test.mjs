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

const {
  changePassword,
  panelAPI,
  revokeSessions
} = await importStandaloneModule("../static/js/api.js");

const panelSessionStorageKey = "grok-search-mcp-panel-session";

function createSessionResponse(token, expiresAt, username = "panel-user") {
  return new Response(JSON.stringify({
    token,
    expires_at: expiresAt,
    user: {
      id: "user-1",
      username
    }
  }), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function resetSession(token = "old-token", expiresAt = "2030-01-01T00:00:00Z") {
  sessionValues.clear();
  panelAPI.clearSession();
  panelAPI.saveSession(token, expiresAt);
}

test("password change publishes one replacement session record", async (testContext) => {
  resetSession();
  const originalFetch = globalThis.fetch;
  testContext.after(() => {
    globalThis.fetch = originalFetch;
  });

  globalThis.fetch = async (requestURL, requestOptions) => {
    assert.equal(requestURL, "/panel/v1/me/change-password");
    assert.equal(requestOptions.method, "POST");
    assert.equal(requestOptions.headers.get("Authorization"), "Bearer old-token");
    assert.deepEqual(JSON.parse(requestOptions.body), {
      current_password: "old-password",
      new_password: "new-password"
    });
    return createSessionResponse("replacement-token", "2030-02-01T00:00:00Z");
  };

  const replacementSession = await changePassword({
    current_password: "old-password",
    new_password: "new-password"
  });

  assert.equal(replacementSession.token, "replacement-token");
  assert.equal(panelAPI.token, "replacement-token");
  assert.equal(panelAPI.expiresAt, "2030-02-01T00:00:00Z");
  assert.deepEqual(JSON.parse(sessionValues.get(panelSessionStorageKey)), {
    token: "replacement-token",
    expires_at: "2030-02-01T00:00:00Z"
  });
  assert.equal(sessionValues.has("grok-search-mcp-panel-token"), false);
  assert.equal(sessionValues.has("grok-search-mcp-panel-token-expiry"), false);
});

test("failed replacement storage keeps in-memory session unchanged", async (testContext) => {
  resetSession();
  const originalFetch = globalThis.fetch;
  const originalSetItem = globalThis.sessionStorage.setItem;
  testContext.after(() => {
    globalThis.fetch = originalFetch;
    globalThis.sessionStorage.setItem = originalSetItem;
  });

  globalThis.fetch = async () => createSessionResponse(
    "replacement-token",
    "2030-02-01T00:00:00Z"
  );
  globalThis.sessionStorage.setItem = (key, value) => {
    if (key === panelSessionStorageKey && String(value).includes("replacement-token")) {
      throw new Error("simulated session storage failure");
    }
    sessionValues.set(key, String(value));
  };

  await assert.rejects(
    changePassword({
      current_password: "old-password",
      new_password: "new-password"
    }),
    /simulated session storage failure/
  );
  assert.equal(panelAPI.token, "old-token");
  assert.equal(panelAPI.expiresAt, "2030-01-01T00:00:00Z");
});

test("revoke-all replaces the current browser session", async (testContext) => {
  resetSession();
  const originalFetch = globalThis.fetch;
  testContext.after(() => {
    globalThis.fetch = originalFetch;
  });

  globalThis.fetch = async (requestURL, requestOptions) => {
    assert.equal(requestURL, "/panel/v1/me/revoke-sessions");
    assert.equal(requestOptions.method, "POST");
    assert.equal(requestOptions.headers.get("Authorization"), "Bearer old-token");
    assert.equal(requestOptions.body, undefined);
    return createSessionResponse("post-revocation-token", "2030-03-01T00:00:00Z");
  };

  await revokeSessions();

  assert.equal(panelAPI.token, "post-revocation-token");
  assert.deepEqual(JSON.parse(sessionValues.get(panelSessionStorageKey)), {
    token: "post-revocation-token",
    expires_at: "2030-03-01T00:00:00Z"
  });
});
