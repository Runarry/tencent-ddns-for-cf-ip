import { json, requireSession } from "./_lib/auth.js";

export async function onRequestGet({ request, env }) {
  const unauthorized = await requireSession(request, env);
  if (unauthorized) {
    return unauthorized;
  }
  return json({ ok: true });
}
