import test from "node:test";
import assert from "node:assert/strict";

import { onRequest } from "./[[path]].js";

const env = {
  BACKEND_BASE_URL: "https://backend.example.com",
  BACKEND_BEARER_TOKEN: "must-not-be-forwarded",
};

async function withMockFetch(mock, callback) {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = mock;
  try {
    await callback();
  } finally {
    globalThis.fetch = originalFetch;
  }
}

test("GET proxies the subscription path and query without management auth", async () => {
  let proxied;
  await withMockFetch(
    async (target, init) => {
      proxied = { target: target.toString(), init };
      return new Response("subscription body", {
        status: 206,
        statusText: "Partial Content",
        headers: {
          "content-type": "text/plain",
          "x-backend-header": "preserved",
        },
      });
    },
    async () => {
      const response = await onRequest({
        request: new Request("https://pages.example.com/sub/public-token?key=sensitive%20value&format=clash", {
          headers: {
            cookie: "admin_session=must-not-leak",
            host: "ignored.example.com",
          },
        }),
        env,
        params: { path: ["public-token"] },
      });

      assert.equal(response.status, 206);
      assert.equal(response.statusText, "Partial Content");
      assert.equal(await response.text(), "subscription body");
      assert.equal(response.headers.get("content-type"), "text/plain");
      assert.equal(response.headers.get("x-backend-header"), "preserved");
    },
  );

  assert.equal(
    proxied.target,
    "https://backend.example.com/sub/public-token?key=sensitive%20value&format=clash",
  );
  assert.equal(proxied.init.method, "GET");
  assert.equal(proxied.init.redirect, "manual");
  assert.equal(proxied.init.headers.has("authorization"), false);
  assert.equal(proxied.init.headers.has("cookie"), false);
  assert.equal(proxied.init.headers.has("host"), false);
  assert.equal(proxied.init.headers.get("x-forwarded-proto"), "https");
  assert.equal(proxied.init.headers.get("x-forwarded-host"), "pages.example.com");
});

test("HEAD is proxied without a request body", async () => {
  let proxied;
  await withMockFetch(
    async (target, init) => {
      proxied = { target: target.toString(), init };
      return new Response(null, {
        status: 200,
        headers: { "content-length": "123" },
      });
    },
    async () => {
      const response = await onRequest({
        request: new Request("http://pages.example.com:8788/sub/token?key=secret", {
          method: "HEAD",
        }),
        env,
        params: { path: "token" },
      });

      assert.equal(response.status, 200);
      assert.equal(response.headers.get("content-length"), "123");
      assert.equal(await response.text(), "");
    },
  );

  assert.equal(proxied.init.method, "HEAD");
  assert.equal(proxied.init.body, undefined);
  assert.equal(proxied.init.headers.get("x-forwarded-proto"), "http");
  assert.equal(proxied.init.headers.get("x-forwarded-host"), "pages.example.com:8788");
});

test("non-GET and non-HEAD methods return 405 without contacting the backend", async () => {
  let fetchCalled = false;
  await withMockFetch(
    async () => {
      fetchCalled = true;
      throw new Error("unexpected fetch");
    },
    async () => {
      const response = await onRequest({
        request: new Request("https://pages.example.com/sub/token?key=do-not-echo", {
          method: "POST",
          body: "ignored",
        }),
        env,
        params: { path: "token" },
      });

      assert.equal(response.status, 405);
      assert.equal(response.headers.get("allow"), "GET, HEAD");
      assert.equal((await response.text()).includes("do-not-echo"), false);
    },
  );

  assert.equal(fetchCalled, false);
});

test("missing backend configuration returns a generic error", async () => {
  const response = await onRequest({
    request: new Request("https://pages.example.com/sub/token?key=do-not-echo"),
    env: {},
    params: { path: "token" },
  });

  assert.equal(response.status, 500);
  assert.equal((await response.text()).includes("do-not-echo"), false);
});
