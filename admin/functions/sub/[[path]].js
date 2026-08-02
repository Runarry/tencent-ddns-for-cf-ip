export async function onRequest({ request, env, params }) {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response("Method Not Allowed", {
      status: 405,
      headers: { allow: "GET, HEAD" },
    });
  }

  if (!env.BACKEND_BASE_URL) {
    return new Response("Backend is not configured", { status: 500 });
  }

  const path = Array.isArray(params.path) ? params.path.join("/") : params.path || "";
  const incomingURL = new URL(request.url);
  const target = new URL(`/sub/${path}`, env.BACKEND_BASE_URL);
  target.search = incomingURL.search;

  const headers = new Headers(request.headers);
  headers.set("x-forwarded-proto", incomingURL.protocol.replace(":", ""));
  headers.set("x-forwarded-host", incomingURL.host);
  headers.delete("cookie");
  headers.delete("host");
  headers.delete("content-length");

  const response = await fetch(target, {
    method: request.method,
    headers,
    redirect: "manual",
  });

  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}
