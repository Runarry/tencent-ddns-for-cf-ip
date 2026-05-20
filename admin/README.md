# Cloudflare Pages Admin

This directory contains the standalone Cloudflare Pages + Functions admin tool.

## Required Variables

Set these only in the Cloudflare Pages dashboard:

`Workers & Pages` -> your Pages project -> `Settings` -> `Variables and Secrets`.

- `BACKEND_BASE_URL`: HTTPS origin exposed by Cloudflare Tunnel for the Go service.
- `BACKEND_BEARER_TOKEN`: `api.bearer_token` from the Go backend.
- `ADMIN_PASSWORD_HASH`: lowercase SHA-256 hex digest of the admin password.
- `SESSION_SECRET`: long random value used to sign the HttpOnly session cookie.

Do not add these values to `wrangler.toml`. This project intentionally does not keep a
Wrangler config file in `admin/`, so Cloudflare dashboard remains the source of truth
for secrets and environment-specific backend addresses.

Generate a password hash with PowerShell:

```powershell
$password = "replace-with-admin-password"
$bytes = [System.Text.Encoding]::UTF8.GetBytes($password)
$hash = [System.Security.Cryptography.SHA256]::HashData($bytes)
[Convert]::ToHexString($hash).ToLowerInvariant()
```

## Local Development

For local development, create `admin/.dev.vars` and keep it untracked:

```dotenv
BACKEND_BASE_URL=http://localhost:8080
BACKEND_BEARER_TOKEN=replace-with-go-api-token
ADMIN_PASSWORD_HASH=sha256-hex-of-admin-password
SESSION_SECRET=replace-with-long-random-session-secret
```

```powershell
cd admin
npx wrangler pages dev public --compatibility-date=2026-05-20
```

## Deploy

Deploy code only:

```powershell
cd admin
npx wrangler pages deploy public --project-name tencent-ddns-admin
```

After changing dashboard variables or secrets, redeploy the Pages project so Functions
receive the updated bindings.
