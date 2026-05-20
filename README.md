# Tencent DDNS for Cloudflare IP

一个 Go 服务：从站长工具 CloudFlare 优选 IP 页面或 API 获取 IP，执行本机 ping 过滤后，同步到 DNSPod 指定域名下的一组受控解析记录。

## 功能

- 默认抓取公开页面 `https://api.uouin.com/cloudflare.html`，不需要站长工具收费 API；也可切换到 API 模式。
- 定时更新，默认每 10 分钟同步一次。
- 手动触发更新 API。
- 使用 Bearer Token 保护非健康检查接口。
- 只管理 `managed_prefix` 和 `managed_base_subdomain` 生成的记录，例如 `cf-cctcc-01.cdn.q.example.com`，不会修改其他 DNS 记录。
- 默认域名可自动使用当前最快的电信节点，例如更新 `cdn.q.example.com` 到最快 `ctcc` IP。
- 支持通配符 CNAME 回落：删除 `cf-cctcc-01.cdn.q.example.com` 后，可由 `*.cdn.q.example.com` 回落到 `cdn.q.example.com`。
- 可公开多个长路径订阅地址，将输入的 3x-ui/v2ray 分享字符串批量替换为当前优选域名。
- 可选独立 Cloudflare Pages/Workers 管理工具，用于查看优选 IP、查看测速结果、运行时管理订阅。
- 每个 IP 都会 ping，ping 不通或平均延迟超过默认 `800ms` 会被丢弃。
- 可选真实 HTTPS 测速：ping 预筛后连接候选 IP、使用你的 Cloudflare 业务域名下载固定字节数，并按实测速率更新受控 DNS 记录排序。
- JSON 状态文件持久化，适合 Docker 挂载 `/data`。

## 快速开始

```powershell
Copy-Item config.example.yaml config.yaml
```

编辑 `config.yaml`，填入站长工具账号、DNSPod Secret、域名和 API token。

```powershell
docker compose up -d --build
```

容器执行 ICMP ping 需要 `NET_RAW` 能力，`docker-compose.yml` 已配置。

## 配置

敏感信息可以写入 `config.yaml`，也可以用环境变量覆盖：

- `PROVIDER_SOURCE`：`web` 或 `api`，默认 `web`。
- `PROVIDER_URL`：当前来源使用的地址，网页模式默认 `https://api.uouin.com/cloudflare.html`。
- `PROVIDER_USERNAME`：仅 API 模式需要。
- `PROVIDER_KEY`：仅 API 模式需要。
- `DNSPOD_SECRET_ID`
- `DNSPOD_SECRET_KEY`
- `DNSPOD_DOMAIN`
- `API_BEARER_TOKEN`

常用同步参数：

- `SYNC_INTERVAL`：同步间隔，例如 `10m`。
- `SYNC_MANAGED_PREFIX`：受控记录前缀，默认 `cf`。
- `SYNC_MANAGED_BASE_SUBDOMAIN`：受控记录所在子域，例如 `cdn.q`。
- `SYNC_DEFAULT_NODEID`：默认域名使用哪个线路的最快节点，默认 `ctcc`。
- `SYNC_MAX_RECORDS_PER_NODE`：每个线路最多发布记录数，默认 `5`。
- `SYNC_PING_THRESHOLD_MS`：最大允许延迟，默认 `800`。
- `SYNC_SPEED_TEST_ENABLED`：是否启用真实 HTTPS 测速，默认 `false`。
- `SYNC_SPEED_TEST_URL`：测速使用的真实 Cloudflare HTTPS URL，例如 `https://cdn.example.com/probe.bin`。
- `SYNC_SPEED_TEST_DOWNLOAD_BYTES`：每个候选 IP 最多下载字节数，默认 `1048576`。
- `SYNC_SPEED_TEST_TIMEOUT`：单个 IP 测速超时，默认 `8s`。
- `SYNC_SPEED_TEST_CONCURRENCY`：测速并发，默认 `8`。
- `SYNC_SPEED_TEST_CANDIDATES_PER_NODE`：每条线路测速候选数，默认 `SYNC_MAX_RECORDS_PER_NODE * 3`。

订阅参数：

- `subscriptions`：公开订阅列表，每一项独立生成一个订阅地址。
- `subscriptions[].enabled`：是否启用该订阅。
- `subscriptions[].name`：订阅名称，仅用于配置识别和诊断。
- `subscriptions[].public_token`：公开订阅路径 token，访问地址为 `/sub/<public_token>?key=<key>`，无需 Bearer Token，至少 16 个字符。
- `subscriptions[].key`：订阅 query 参数鉴权 key，启用订阅时必填，建议使用足够长的随机值。
- `subscriptions[].nodeids`：可选线路过滤，例如 `["ctcc"]`；为空时使用全部非 fallback 优选域名。请求可用 `nodeids=ctcc,bgp` 动态收窄线路范围。
- `subscriptions[].shares`：原始分享字符串列表，支持 `vmess`、`vless`、`trojan`、`ss`、`hysteria`、`hysteria2`。
- `subscriptions[].format`：当前固定为 `base64`。

运行时订阅管理：

- `state.subscriptions_file`：Cloudflare 管理工具新增/编辑的订阅持久化文件，默认 `/data/subscriptions.json`。
- `config.yaml` 里的 `subscriptions` 仍然兼容且只读；管理 API 只修改 `state.subscriptions_file`。

网页模式配置：

```yaml
provider:
  source: "web"
  url: "https://api.uouin.com/cloudflare.html"
  nodeids: ["ctcc", "bgp", "cucc"]
```

API 模式配置：

```yaml
provider:
  source: "api"
  url: "https://api.uouin.com/app/cloudflare"
  username: "your-uouin-username"
  key: "your-uouin-key"
  nodeids: ["ctcc", "bgp", "cucc"]
```

## API

健康检查无需鉴权：

```bash
curl http://localhost:8080/healthz
```

其他 API 需要 Bearer Token：

```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/status
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/records
curl -X POST -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/update
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/config
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/admin/subscriptions
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" http://localhost:8080/api/v1/admin/subscriptions
```

订阅地址无需 Bearer Token，但必须带该订阅配置的 `key`。每个 `subscriptions` 项都有自己的地址：

```bash
curl "http://localhost:8080/sub/replace-with-a-long-random-subscription-token?key=replace-with-a-long-random-subscription-key"
curl "http://localhost:8080/sub/replace-with-a-long-random-subscription-token?key=replace-with-a-long-random-subscription-key&nodeids=ctcc"
```

订阅内容会把分享链接的实际连接地址替换为当前优选 FQDN，例如 `cf-ctcc-01.cdn.q.example.com`；`sni`、`host`、`path` 等传输参数保持原值。配置了 `nodeids` 时，只会使用匹配线路的优选 FQDN；请求里的 `nodeids` 只能在配置允许范围内继续收窄。

## Cloudflare 管理工具

`admin/` 目录包含一个独立的 Cloudflare Pages + Functions 管理工具：

- Pages 前端提供总览、订阅管理、优选 IP 列表和测速结果列表。
- Functions 使用独立管理密码登录，签发 HttpOnly session cookie。
- `/api/*` 由 Function 代理到 Go 后端，并在服务端注入 `BACKEND_BEARER_TOKEN`，浏览器不会拿到 Go API token。
- Go 后端仍负责订阅生成、优选 IP、测速和 DNS 同步。

生产环境建议通过 Cloudflare Tunnel 暴露 Go 后端，再配置 Pages 环境变量：

- `BACKEND_BASE_URL`：Tunnel 后的 Go 后端 HTTPS 地址。
- `BACKEND_BEARER_TOKEN`：Go 后端 `api.bearer_token`。
- `ADMIN_PASSWORD_HASH`：管理密码的 SHA-256 hex。
- `SESSION_SECRET`：用于签名 session cookie 的长随机字符串。

这些值只在 Cloudflare Dashboard 的 `Workers & Pages` -> Pages 项目 -> `Settings` -> `Variables and Secrets` 中配置。不要写入 `admin/wrangler.toml`；项目刻意不保留 Wrangler 配置文件，避免 Wrangler 部署接管或覆盖后台里的密钥和后端地址。

本地调试：

```powershell
cd admin
npx wrangler pages dev public --compatibility-date=2026-05-20
```

部署时显式指定输出目录和项目名：

```powershell
cd admin
npx wrangler pages deploy public --project-name tencent-ddns-admin
```

## DNS 记录命名

示例配置：

```yaml
dnspod:
  domain: "example.com"

sync:
  managed_prefix: "cf"
  managed_base_subdomain: "cdn.q"
  node_labels:
    ctcc: "cctcc"
  default_nodeid: "ctcc"
  fallback:
    enabled: true
    wildcard_subdomain: "*.cdn.q"
    target: "cdn.q.example.com"
    type: "CNAME"
```

会生成和维护：

- `cf-cctcc-01.cdn.q.example.com`
- `cf-cctcc-02.cdn.q.example.com`
- `cf-cmcc-01.cdn.q.example.com`
- `cdn.q.example.com`，使用当前最快的 `ctcc` IP。
- `*.cdn.q.example.com CNAME cdn.q.example.com`，用于精确记录删除后的 DNS 回落。

每次同步只会删除或更新匹配 `cf-<label>-<number>.cdn.q` 的记录，以及配置中的 `cdn.q` 默认记录和 `*.cdn.q` 通配符回落记录。`www`、`@`、`cf-custom` 等不匹配此模式的记录不会被工具处理。

## 本地验证

```powershell
$env:GOCACHE=(Join-Path (Get-Location) '.gocache')
$env:GOPATH=(Join-Path (Get-Location) '.gopath')
go test ./...
go build ./cmd/server
```
