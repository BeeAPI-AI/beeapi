# BeeAPI CLI 设备授权契约

## 凭据边界

GetBeeAPI 使用 RFC 8628 风格的设备授权，但模型调用凭据与 CLI 登录凭据始终分离：

| 凭据 | 用途 | 是否进入 CLI |
| --- | --- | --- |
| 网页 JWT / OAuth / 2FA | 登录 BeeAPI、查看并批准设备 | 否 |
| 短期 CLI 令牌 | 一次领取本次设备授权批准的 Key 集合 | 是，进程内短暂保存 |
| 账户现有 API Key | Claude Code、Codex 等模型调用 | 批准后一次性交付并独立安全保存 |

用户在 BeeAPI 官方页面核对并批准设备，不在网页选择 Key。批准时服务端快照账户当时的 API Key；CLI 领取时返回其中仍然可用且可导出的现有 Key，不创建新的 Key。不可导出的 Key 以 `skipped` 元数据返回，不能从哈希伪造或重建明文。

CLI 是 public client，不配置 `client_secret`，不接收账号密码，也不读取网页登录 Cookie。每次授权在进程内生成临时 P-256 密钥，设备码、轮询和领取请求都以同一 JWK 生成 DPoP proof；CLI 退出后不持久化该私钥。

## 完整流程

```text
CLI 创建 device code
  → 浏览器打开 BeeAPI /cli/authorize
  → 用户登录、核对设备与 user_code
  → 用户确认授权此设备读取账户当前可用 Key
  → CLI 轮询得到短期 DPoP 令牌
  → CLI 一次领取账户现有可用 Key 与跳过说明
  → 逐个读取模型与协议能力
  → 用户为每个本地工具选择 Key 与模型
  → 凭据安全保存，配置统一备份后写入
```

`getbeeapi.com` 只提供官网、安装器和固定白名单发行缓存，不承载 BeeAPI 账户登录或授权数据。

## 1. 创建设备授权

```http
POST /api/v1/auth/device/code
Content-Type: application/json
DPoP: <ES256 proof>

{
  "client_id": "getbeeapi-cli",
  "scope": "cli:configure",
  "device_name": "DESKTOP-01",
  "platform": "windows/amd64"
}
```

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "device_code": "high-entropy-secret",
    "user_code": "BEE7-K9Q2",
    "verification_uri": "https://beeapi.ai/cli/authorize",
    "verification_uri_complete": "https://beeapi.ai/cli/authorize?user_code=BEE7-K9Q2",
    "expires_in": 600,
    "interval": 5
  }
}
```

CLI 必须始终在终端显示完整的 `verification_uri_complete` 与 `user_code`，再尝试自动打开浏览器。SSH、无桌面环境或使用 `--no-open` 时不尝试本机跳转，而是提示用户在自己的电脑或手机浏览器打开该网址；自动打开失败也不能隐藏链接。若创建设备码接口返回 `404`、`405` 或 `501`，服务端没有生成 `device_code`，此时普通账户登录页不能代替本次设备授权，CLI 应明确说明并提供 API Key 回退。

创建请求的 proof 必须包含正确的 `typ`、`alg`、公开 JWK、`htm=POST`、无查询参数的绝对 `htu`、时间窗口内的 `iat` 与一次性 `jti`。服务端保存 JWK thumbprint，并拒绝 proof 重放。

## 2. 网页批准

授权页面位于 BeeAPI 官方域名的 `/cli/authorize`。页面应显示应用名、设备名、平台、发起时间、粗略地区、`user_code`，以及当前可导出和不可导出的 Key 数量。页面只提供批准或拒绝，并明确说明批准后该设备将取得账户在本次批准时可导出的现有 API Key。

账户管理接口只接受 JWT 网页会话：

```text
GET  /api/v1/me/cli-authorizations/lookup?user_code=...
GET  /api/v1/me/cli-authorizations/:ref
POST /api/v1/me/cli-authorizations/:ref/approve
POST /api/v1/me/cli-authorizations/:ref/deny
POST /api/v1/me/cli-authorizations/:ref/revoke
```

批准请求提交 `user_code` 和可选的 `step_up_code`，不提交 Key 选择。启用 2FA 的账户执行 step-up。批准成功只提示返回 CLI，网页绝不显示任何 Key 明文。

## 3. 轮询短期 CLI 令牌

```http
POST /api/v1/auth/device/token
Content-Type: application/json
DPoP: <使用同一临时密钥的 ES256 proof>

{
  "client_id": "getbeeapi-cli",
  "device_code": "high-entropy-secret"
}
```

批准后返回：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "access_token": "bclt_short_lived_secret",
    "token_type": "DPoP",
    "expires_in": 300,
    "scope": "cli:configure"
  }
}
```

CLI 遵守服务端返回的轮询间隔；收到 `slow_down` 后永久增加至少 5 秒。短期令牌没有 refresh token，不能进入 `/me`、模型转发或其他账户接口。

## 4. 一次领取账户现有 Key

```http
POST /api/v1/cli/credentials/claim
Authorization: DPoP bclt_short_lived_secret
DPoP: <ES256 proof，包含 ath=base64url(sha256(access_token))>
Content-Type: application/json

{}
```

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "credential_mode": "existing_key_export_v2",
    "credentials": [
      {
        "credential_id": "opaque-key-bound-id",
        "key_name": "日常开发",
        "key_prefix": "sk-live-12ab",
        "status": "enabled",
        "expires_at": null,
        "api_key": "EXISTING-API-KEY-PLAINTEXT"
      }
    ],
    "skipped": [
      {
        "credential_id": "opaque-key-bound-id-2",
        "key_name": "旧密钥",
        "key_prefix": "sk-old-34cd",
        "status": "enabled",
        "expires_at": null,
        "reason": "plaintext_unavailable"
      }
    ],
    "retry_until": "2026-08-28T12:00:00Z"
  }
}
```

领取资格至少要求 Key 属于批准账户、未删除、状态为 `enabled`、未过期、未自动停用、不是旧版设备专用 Key、明文存在且与哈希一致。成功载荷只在短暂的 `retry_until` 窗口内以 AEAD/KMS 密文保存，用于响应中断后的幂等重试；窗口结束后不能再次取得明文。所有受保护请求都验证 DPoP 签名、`htm`、`htu`、`iat`、未使用过的 `jti`、令牌绑定 JWK 与 `ath`。

CLI 验证每个返回项都有非空 `credential_id` 与 `api_key`，对 `skipped` 显示明确原因，再逐个读取模型能力。若没有任何可用凭据，首次配置不会写入工具文件。

## 5. 模型能力

标准 `/v1/models` 只能可靠提供模型 ID 等基础字段，不能证明某条 BeeAPI 路由是否支持 Codex 所需的 Responses、Anthropic Messages 或 Gemini 协议。CLI 应优先调用 API-Key 鉴权的专用模型选项接口，读取 `protocols`、`capabilities`、`recommended_for` 与 `priority`；其中 `recommended_for` 同时约束协议与客户端适配性，`priority` 是服务端给出的最终顺序：先尊重该 API Key 的路由优先级，同一路由内再沿用 BeeAPI 商家市场的模型排列（通用模型先于辅助模型、同系列聚合、新版本优先、同版本基础模型先于 `mini` 等变体）。CLI 只按更高的 `priority` 优先，不再用模型名称、上下文长度或本地偏好覆盖服务端顺序。旧服务端没有该接口时才回退 `/v1/models?include_aliases=false`，并把结果称为“可用模型”，不能声称已完成协议适配。

## 本地多凭据行为

- 每个获准导出的账户 Key 拥有独立的本地 ID 与安全存储槽；opaque ID 不直接用作文件名或钥匙串账户名。
- Linux 优先 Secret Service，macOS 优先 Keychain，Windows 优先当前用户 DPAPI；回退文件权限为 `0600`。
- 用户可为不同工具选择不同凭据和模型。若当前或刚输入的 Key 没有目标工具所需的兼容模型，CLI 会说明原因并列出其他兼容 Key 供重新选择；只有一个兼容 Key 时自动选用，所有 Key 都不兼容时才停止。Claude Code 与 Claude Desktop Code 共享设置，因此必须使用同一凭据与模型。
- Codex 默认配置中的 BeeAPI provider 使用 `beeapi token print --agent codex` 按工具读取已分配的凭据，不把 Key 明文写进 `config.toml`，也不修改 `auth.json`。
- 手动粘贴模式只产生一个本地凭据。
- 设备授权撤销只能阻止再次领取，不能让已经导出的共享 Key 自动失效；需要停用原 Key 才能使其失效。

## 错误语义

| `reason` / 错误码 | CLI 行为 |
| --- | --- |
| `authorization_pending` | 按当前间隔继续轮询 |
| `slow_down` | 后续轮询间隔增加 5 秒 |
| `access_denied` | 立即停止并提示用户拒绝 |
| `expired_token` | 停止并要求重新发起授权 |
| `invalid_dpop_proof` / `cli.invalid_dpop_proof` | 停止，不能降级绕过 DPoP |
| `credential_unavailable` / `cli.credential_unavailable` | 批准快照中的 Key 已不可用，按 `skipped` 原因处理 |
| `cli.claim_unavailable` | 幂等领取窗口已失效，重新授权 |
| `cli.invalid_token` | CLI 令牌无效或过期，重新授权 |

服务端返回 `404`、`405` 或 `501` 时，CLI 可以明确提供“手动粘贴 API Key”兼容回退，但绝不要求用户在终端输入 BeeAPI 账户密码。
