const COOKIE_NAME = "cf_admin_session";
const MAX_AGE_SECONDS = 24 * 60 * 60;

export function json(value, init = {}) {
  return new Response(JSON.stringify(value), {
    ...init,
    headers: {
      "content-type": "application/json; charset=utf-8",
      ...(init.headers || {}),
    },
  });
}

export async function requireSession(request, env) {
  const cookie = parseCookies(request.headers.get("cookie") || "")[COOKIE_NAME];
  if (!cookie || !(await verifySession(cookie, env))) {
    return json({ error: "unauthorized" }, { status: 401 });
  }
  return null;
}

export async function verifyPassword(password, env) {
  const expected = String(env.ADMIN_PASSWORD_HASH || "").trim().toLowerCase();
  if (!expected) {
    return false;
  }
  const actual = await sha256Hex(password || "");
  return constantEqual(actual, expected);
}

export async function sessionCookie(env) {
  const issuedAt = Math.floor(Date.now() / 1000).toString();
  const signature = await hmacHex(env.SESSION_SECRET || "", issuedAt);
  return `${COOKIE_NAME}=${issuedAt}.${signature}; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=${MAX_AGE_SECONDS}`;
}

export function clearSessionCookie() {
  return `${COOKIE_NAME}=; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=0`;
}

async function verifySession(value, env) {
  const [issuedAt, signature] = value.split(".");
  if (!issuedAt || !signature || !env.SESSION_SECRET) {
    return false;
  }
  const issued = Number(issuedAt);
  const now = Math.floor(Date.now() / 1000);
  if (!Number.isFinite(issued) || issued > now || now - issued > MAX_AGE_SECONDS) {
    return false;
  }
  const expected = await hmacHex(env.SESSION_SECRET, issuedAt);
  return constantEqual(signature, expected);
}

function parseCookies(header) {
  const cookies = {};
  for (const part of header.split(";")) {
    const [rawKey, ...rest] = part.trim().split("=");
    if (!rawKey) {
      continue;
    }
    cookies[rawKey] = rest.join("=");
  }
  return cookies;
}

async function sha256Hex(value) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return hex(digest);
}

async function hmacHex(secret, value) {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const signature = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(value));
  return hex(signature);
}

function hex(buffer) {
  return [...new Uint8Array(buffer)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

function constantEqual(a, b) {
  if (a.length !== b.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.length; i += 1) {
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return diff === 0;
}
