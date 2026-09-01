# BeeAPI OAuth 账户连接契约

GetBeeAPI 默认使用 **BeeAPI OAuth Account Connection v1**。它负责连接账户、读取余额与 API Key 元数据，并在用户明确选择后一次性交付现有 API Key；它不创建新 Key，也不把账户 Token 当作模型调用凭据。

## 凭据边界

| 凭据 | 用途 | 本地保存 |
| --- | --- | --- |
| 网页 JWT / 第三方登录 / 2FA | 登录 BeeAPI、查看并批准 OAuth 请求 | 不进入 CLI |
| `boa_` OAuth Access Token | 读取获准的账户资源 | 与 DPoP 私钥一起安全保存 |
| `bor_` Refresh Token | 轮换短期 Access Token | 与 DPoP 私钥一起安全保存 |
| 账户现有 `sk-` API Key | Claude Code、Codex 等模型调用 | 仅保存用户在终端选中的 Key |

账户 Token 只能进入 `/api/v1/oauth/**`，不能进入 `/v1`、`/v1beta`、`/api/v1/me`、管理端或模型代理。普通 `sk-` Key 也不能反向访问 OAuth 账户资源。CLI 是 public client，没有 `client_secret`，不接收账户密码、2FA 验证码或网页 Cookie。

OAuth Token 与 P-256 DPoP 私钥存进同一个系统凭据记录；普通 `oauth-account.json` 只保存 issuer、client ID、scope、账户展示信息和凭据引用。API Key 各自进入独立凭据槽。Linux 优先 Secret Service，macOS 优先 Keychain，Windows 优先当前用户 DPAPI；回退文件权限仅限当前用户。

## 双 issuer 与发现

两个官方 Web 入口是相互独立的 OAuth 安全域，但共享同一套 BeeAPI 账户、余额、API Key 与路由数据：

| 用户选择的入口 | discovery alias | 必须返回的 issuer |
| --- | --- | --- |
| `https://beeapi.ai` | `https://api.beeapi.ai` | `https://beeapi.ai` |
| `https://beeapi.dev` | `https://api.beeapi.dev` | `https://beeapi.dev` |

CLI 从用户选中的入口读取 discovery，并逐字保存返回的 issuer。`api.*` 只允许读取 discovery，且只能永久跳转到同后缀的 canonical issuer；授权、Token、设备码、撤销、批准和账户资源请求都不能发送到 `api.*`。`.ai` 签发的 code、device code、Access/Refresh Token、DPoP replay ID 与 Key export 不能在 `.dev` 重试，反向亦然。切换入口必须重新授权。

```http
GET /.well-known/oauth-authorization-server
```

关键 metadata（以下以 `.dev` 为例；从 `.ai` 发现时所有域名都必须是 `.ai`）：

```json
{
  "issuer": "https://beeapi.dev",
  "authorization_endpoint": "https://beeapi.dev/oauth/authorize",
  "device_authorization_endpoint": "https://beeapi.dev/oauth/device/code",
  "token_endpoint": "https://beeapi.dev/oauth/token",
  "revocation_endpoint": "https://beeapi.dev/oauth/revoke",
  "response_types_supported": ["code"],
  "grant_types_supported": [
    "authorization_code",
    "refresh_token",
    "urn:ietf:params:oauth:grant-type:device_code"
  ],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "scopes_supported": [
    "account:profile:read",
    "account:balance:read",
    "api_keys:read",
    "api_keys:export",
    "offline_access"
  ],
  "dpop_signing_alg_values_supported": ["ES256"],
  "authorization_response_iss_parameter_supported": true
}
```

## 桌面：Authorization Code + PKCE

桌面环境监听随机 `127.0.0.1` 端口，生成高熵 `state`、PKCE verifier 和 S256 challenge，再打开：

```http
GET https://beeapi.dev/oauth/authorize
  ?response_type=code
  &client_id=getbeeapi-cli-v2
  &redirect_uri=http://127.0.0.1:<random>/oauth/callback
  &scope=account:profile:read account:balance:read api_keys:read api_keys:export offline_access
  &state=<random>
  &code_challenge=<S256>
  &code_challenge_method=S256
  &device_name=<hostname>
  &platform=<os/arch>
```

BeeAPI 登录页安全保留站内授权请求。批准或拒绝后，浏览器回到精确的 loopback URI，并携带 `state` 与所选 issuer，例如 `iss=https://beeapi.dev`。CLI 在接受 `code` 前同时验证 exact path、state 和 issuer；`.ai` 流程绝不接受 `.dev` 的 `iss`，反向亦然。回调页面使用固定 HTML，不反射服务端错误描述，并提醒用户等终端确认“账户已连接”后再结束流程。

CLI 随后使用同一枚 DPoP 密钥交换 Token：

```http
POST https://beeapi.dev/oauth/token
Content-Type: application/x-www-form-urlencoded
DPoP: <ES256 proof>

grant_type=authorization_code&client_id=getbeeapi-cli-v2&code=...&redirect_uri=...&code_verifier=...
```

授权码交换响应若在传输中断开，CLI 会在同一进程内使用原 code、redirect URI、verifier 与同一 DPoP 密钥重试；服务端在短期 AEAD 窗口内返回同一 Token family，不签发第二套令牌。账户 Token 一经安全保存，即使尚未完成 Key 选择，下次运行也会默认继续该连接；只有高敏导出权限已经过期时才重新打开网页确认。

## SSH / 无桌面：标准 Device Grant

SSH 不启动本机浏览器，而是使用同一套 scope 和 DPoP 密钥请求：

```http
POST https://beeapi.dev/oauth/device/code
Content-Type: application/x-www-form-urlencoded
DPoP: <ES256 proof>

client_id=getbeeapi-cli-v2&scope=...&device_name=...&platform=linux/amd64
```

CLI 必须始终打印 `verification_uri_complete` 和 `user_code`，让用户在自己的电脑或手机打开；`--no-open` 和浏览器启动失败也不能隐藏网址。轮询：

```http
POST https://beeapi.dev/oauth/token
Content-Type: application/x-www-form-urlencoded
DPoP: <使用同一密钥的 ES256 proof>

grant_type=urn:ietf:params:oauth:grant-type:device_code&client_id=getbeeapi-cli-v2&device_code=...
```

CLI 至少遵守 5 秒间隔，`authorization_pending` 继续等待，`slow_down` 将后续间隔永久增加 5 秒。暂时网络错误和 5xx 不丢弃本次授权；`access_denied`、`expired_token` 与 `invalid_grant` 才结束。

两种流程成功后都返回同一种 Token：

```json
{
  "access_token": "boa_...",
  "refresh_token": "bor_...",
  "token_type": "DPoP",
  "expires_in": 600,
  "scope": "account:profile:read account:balance:read api_keys:read api_keys:export offline_access"
}
```

Refresh Token 单次使用并轮换，检测 reuse 时撤销整个 family；响应中断可在短期窗口内幂等恢复。刷新后的 scope 不再包含高敏 `api_keys:export`，因此以后导出新 Key 必须重新执行交互授权。

## 账户与 Key 资源

每个受保护请求都使用当前连接 issuer 下的 URL，并据此生成 exact DPoP `htu`：

```http
Authorization: DPoP boa_...
DPoP: <proof with htm, exact htu, iat, unique jti, ath>
```

资源路径：

```text
GET /api/v1/oauth/account
GET /api/v1/oauth/account/balance
GET /api/v1/oauth/api-keys
GET /api/v1/oauth/api-keys/:id/model-options
```

账户资料包含数字 `id`、`email`、可选 `username` / `avatar` 与 `preferred_locale`。余额为：

```json
{ "available": 12.5, "currency": "USD" }
```

Key 元数据不包含明文：

```json
{
  "items": [
    {
      "id": 42,
      "name": "日常开发",
      "key_prefix": "sk-live-12ab",
      "status": "enabled",
      "expires_at": null,
      "last_used_at": null,
      "exportable": true,
      "unavailable_reason": "",
      "route_groups": []
    }
  ]
}
```

`model-options` 返回该 Key 的真实有效路由所支持的 `id`、`protocols`、`capabilities`、`recommended_for`、`priority` 及上下文限制。服务端的唯一递减 `priority` 已编码 API Key 路由优先级和 BeeAPI 商家市场排序；CLI 去重、按目标工具协议过滤，并仅依据该服务端 `priority` 排序，不叠加客户端模型名称猜测或私有偏好。

## 选择性一次性导出

CLI 先展示账户、余额、每个 Key 的状态及模型能力。用户在终端输入一个或多个编号后，CLI 才请求对应数字 ID（最多 10 个）：

```http
POST /api/v1/oauth/api-key-exports
Authorization: DPoP boa_...
DPoP: <proof>
Idempotency-Key: <至少 16 字符的高熵随机值>
Content-Type: application/json

{ "api_key_ids": [42, 57] }
```

响应：

```json
{
  "export_id": "boex_...",
  "credentials": [
    {
      "api_key_id": 42,
      "key_name": "日常开发",
      "key_prefix": "sk-live-12ab",
      "status": "enabled",
      "expires_at": null,
      "api_key": "sk-PLAINTEXT-ONCE"
    }
  ],
  "skipped": [
    {
      "api_key_id": 57,
      "key_name": "旧密钥",
      "key_prefix": "sk-old-34cd",
      "reason": "plaintext_unavailable"
    }
  ],
  "retry_until": "2026-09-01T12:10:00Z"
}
```

相同 `Idempotency-Key` 与相同 ID 集合可在 `retry_until` 前恢复同一 AEAD 密文响应；CLI 遇到暂时网络错误或 5xx 时必须复用完全相同的幂等键和 ID 集合。相同 Key 配不同请求返回冲突。历史只保存哈希、已失效、已删除或明文校验不一致的 Key 进入 `skipped`，服务端不会尝试重建明文。

CLI 的提交顺序不可改变：

```text
收到导出结果
  → 每枚 Key 写入系统凭据存储
  → 写入仅含凭据引用、export_id 和 retry_until 的 pending checkpoint
  → POST /api/v1/oauth/api-key-exports/:export_id/ack
  → 服务端清除导出密文
  → 读取/复用模型能力
  → 选择工具与模型
  → 统一备份并配置
  → 完成后清除 checkpoint
```

ACK 幂等。ACK 或后续配置中断时，下一次 `beeapi` 从 checkpoint 继续，不需要再次网页授权；恢复时必须先逐一读取并验证全部本地 Key，确认都可用后才能补发 ACK 或放弃已过期的服务端恢复窗口。用户也可以选择丢弃 checkpoint 并重新授权。若 ACK 返回 `oauth.export_acknowledged`，CLI 视为收尾完成；若本地 Key 已保存而服务端返回 `oauth.export_unavailable`，CLI 清理过期导出引用并继续使用本地 Key。没有可验证的本地 checkpoint 时绝不能把 `export_unavailable` 当作成功。

## 错误语义

| reason / OAuth error | CLI 行为 |
| --- | --- |
| `authorization_pending` | 按当前间隔继续轮询 |
| `slow_down` | 后续间隔增加 5 秒 |
| `access_denied` | 立即停止并说明用户拒绝 |
| `expired_token` / `invalid_grant` | 重新发起授权 |
| `oauth.invalid_dpop_proof` | 停止；不得降级为 Bearer |
| `oauth.insufficient_scope` | 重新交互授权所需 scope |
| `oauth.step_up_required` | 返回网页完成交互或 2FA step-up |
| `oauth.idempotency_conflict` | 停止本次导出；重新选择时生成新幂等键，不复用冲突请求 |
| `oauth.export_acknowledged` | 视本地 checkpoint 情况完成清理，不再领取 |
| `oauth.export_unavailable` | 短期恢复窗口已过；若本地未保存则重新授权 |
| `plaintext_unavailable` | 展示并跳过该 Key |

## 兼容回退与升级

旧 `getbeeapi-cli / cli:configure` 设备协议已经退役，新 CLI 不再请求其 JSON device-code、token 或 credentials claim 端点。OAuth discovery 返回 `404`、`405`、`501`、官方前端 SPA HTML 或空 metadata 时，只能提示用户重新选择另一个官方入口并开始一套新授权，或由用户明确选择粘贴单个 API Key；禁止旧协议降级和跨域重试。只要 metadata 已出现任何 OAuth 字段，issuer、端点、回调 issuer 能力或 DPoP 能力不可信就必须失败关闭。

旧客户端 ID `getbeeapi-cli` 与 scope `cli:configure` 由 BeeAPI 服务端拒绝，用户必须升级到使用 `getbeeapi-cli-v2` 的 OAuth 版本。本地旧 OAuth 元数据或缺少 issuer 绑定的令牌记录也不会被复用，升级后需要重新授权；已经保存的普通 `sk-` Key 和工具配置不受影响。

`getbeeapi.com` 只提供产品页、安装器、固定白名单发行缓存与 Release metadata，不承载账户登录、OAuth 回调中转、Token 或 API Key。
