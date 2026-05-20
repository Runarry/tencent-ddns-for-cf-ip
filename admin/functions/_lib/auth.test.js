import test from "node:test";
import assert from "node:assert/strict";

import { requireSession, sessionCookie, verifyPassword } from "./auth.js";
import { onRequest as proxyRequest } from "../api/[[path]].js";

const env = {
  ADMIN_PASSWORD_HASH: "2bb80d537b1da3e38bd30361aa855686bde0eacd7162fef6a25fe97bf527a25b",
  SESSION_SECRET: "test-session-secret",
  BACKEND_BASE_URL: "https://backend.example.com",
  BACKEND_BEARER_TOKEN: "backend-token",
};

test("password verification uses sha256 hex", async () => {
  assert.equal(await verifyPassword("secret", env), true);
  assert.equal(await verifyPassword("wrong", env), false);
});

test("session cookie authorizes requests", async () => {
  const cookie = await sessionCookie(env);
  const request = new Request("https://admin.example.com/session", {
    headers: { cookie },
  });
  assert.equal(await requireSession(request, env), null);
});

test("missing session is rejected", async () => {
  const request = new Request("https://admin.example.com/session");
  const response = await requireSession(request, env);
  assert.equal(response.status, 401);
});

test("proxy injects backend bearer token", async () => {
  const cookie = await sessionCookie(env);
  let proxied;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (target, init) => {
    proxied = { target: target.toString(), init };
    return new Response(JSON.stringify({ ok: true }), {
      headers: { "content-type": "application/json", "set-cookie": "backend=leak" },
    });
  };
  try {
    const response = await proxyRequest({
      request: new Request("https://admin.example.com/api/v1/status", {
        headers: { cookie },
      }),
      env,
      params: { path: ["v1", "status"] },
    });
    assert.equal(response.status, 200);
    assert.equal(proxied.target, "https://backend.example.com/api/v1/status");
    assert.equal(proxied.init.headers.get("authorization"), "Bearer backend-token");
    assert.equal(response.headers.has("set-cookie"), false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
