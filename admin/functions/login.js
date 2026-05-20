import { json, sessionCookie, verifyPassword } from "./_lib/auth.js";

export async function onRequestPost({ request, env }) {
  let body;
  try {
    body = await request.json();
  } catch {
    return json({ error: "invalid JSON body" }, { status: 400 });
  }

  if (!(await verifyPassword(body.password, env))) {
    return json({ error: "invalid password" }, { status: 401 });
  }

  return json(
    { ok: true },
    {
      headers: {
        "set-cookie": await sessionCookie(env),
      },
    },
  );
}
