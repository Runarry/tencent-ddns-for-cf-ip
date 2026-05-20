import { json, requireSession } from "../_lib/auth.js";

export async function onRequest({ request, env, params }) {
  const unauthorized = await requireSession(request, env);
  if (unauthorized) {
    return unauthorized;
  }
  if (!env.BACKEND_BASE_URL || !env.BACKEND_BEARER_TOKEN) {
    return json({ error: "backend is not configured" }, { status: 500 });
  }

  const path = Array.isArray(params.path) ? params.path.join("/") : params.path || "";
  const incomingURL = new URL(request.url);
  const target = new URL(`/api/${path}`, env.BACKEND_BASE_URL);
  target.search = incomingURL.search;

  const headers = new Headers(request.headers);
  headers.set("authorization", `Bearer ${env.BACKEND_BEARER_TOKEN}`);
  headers.set("x-forwarded-proto", incomingURL.protocol.replace(":", ""));
  headers.set("x-forwarded-host", incomingURL.host);
  headers.delete("cookie");
  headers.delete("host");
  headers.delete("content-length");

  const response = await fetch(target, {
    method: request.method,
    headers,
    body: request.method === "GET" || request.method === "HEAD" ? undefined : request.body,
    redirect: "manual",
  });

  const proxiedHeaders = new Headers(response.headers);
  proxiedHeaders.delete("set-cookie");
  proxiedHeaders.delete("www-authenticate");
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: proxiedHeaders,
  });
}
