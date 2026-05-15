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
- 可公开一个长路径订阅地址，将输入的 3x-ui/v2ray 分享字符串批量替换为当前优选域名。
- 每个 IP 都会 ping，ping 不通或平均延迟超过默认 `800ms` 会被丢弃。
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

订阅参数：

- `subscription.enabled`：是否启用公开订阅地址。
- `subscription.public_token`：公开订阅路径 token，访问地址为 `/sub/<public_token>`，无需 Bearer Token。
- `subscription.shares`：原始分享字符串列表，支持 `vmess`、`vless`、`trojan`、`ss`、`hysteria`、`hysteria2`。
- `subscription.format`：当前固定为 `base64`。

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
```

订阅地址无需鉴权，但应使用足够长的随机路径：

```bash
curl http://localhost:8080/sub/replace-with-a-long-random-subscription-token
```

订阅内容会把分享链接的实际连接地址替换为当前优选 FQDN，例如 `cf-ctcc-01.cdn.q.example.com`；`sni`、`host`、`path` 等传输参数保持原值。

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
