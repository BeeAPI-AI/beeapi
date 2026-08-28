# BeeAPI CLI 设备授权契约

## 凭据边界

GetBeeAPI 使用 RFC 8628 风格的设备授权，但模型调用凭据与 CLI 登录凭据始终分离：

| 凭据 | 用途 | 是否进入 CLI |
| --- | --- | --- |
| 网页 JWT / OAuth / 2FA | 登录 BeeAPI、查看并批准设备 | 否 |
| 账户原 API Key | 用户已有的路由、分组与有效期配置来源 | 明文不进入 |
| 短期 CLI 令牌 | 一次领取网页已批准的设备凭据 | 是，进程内短暂保存 |
| 设备专用 API Key | Claude Code、Codex 等模型调用 | 是，独立安全保存 |

用户在 BeeAPI 官方批准页选择 1–10 个现有密钥配置。批准本身只记录选择；CLI 领取时，服务端为每个选择创建新的设备专用子 Key，复制来源 Key 的有效路由与必要元数据。设备 Key 可在账户页单独撤销，账户原 Key 明文不会被 reveal 或传给 CLI。

CLI 是 public client，不配置 `client_secret`，不接收账号密码，也不读取网页登录 Cookie。每次授权在进程内生成临时 P-256 密钥，设备码、轮询和领取请求都以同一 JWK 生成 DPoP proof；CLI 退出后不持久化该私钥。

## 完整流程

```text
CLI 创建 device code
  → 浏览器打开 BeeAPI /cli/authorize
  → 用户登录、核对设备与 user_code
  → 用户选择 1–10 个密钥配置并批准
  → CLI 轮询得到短期 DPoP 令牌
  → CLI 一次领取对应的设备专用 Key
  → 逐个请求 /v1/models
  → 用户为每个本地工具选择密钥配置与模型
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

创建请求的 proof 必须包含正确的 `typ`、`alg`、公开 JWK、`htm=POST`、无查询参数的绝对 `htu`、时间窗口内的 `iat` 与一次性 `jti`。服务端保存 JWK thumbprint，并拒绝 proof 重放。

## 2. 网页批准

授权页面位于 BeeAPI 官方域名的 `/cli/authorize`。页面应显示应用名、设备名、平台、发起时间、粗略地区与 `user_code`，并要求用户选择至少 1 个、最多 10 个可用密钥配置。

账户管理接口只接受 JWT 网页会话：

```text
GET  /api/v1/me/cli-authorizations/lookup?user_code=...
GET  /api/v1/me/cli-authorizations/:ref
POST /api/v1/me/cli-authorizations/:ref/approve
POST /api/v1/me/cli-authorizations/:ref/deny
POST /api/v1/me/cli-authorizations/:ref/revoke
```

批准请求提交 `user_code`、`selection_ids` 和可选的 `step_up_code`。启用 2FA 的账户执行 step-up。批准成功只提示返回 CLI，网页绝不显示任何 Key 明文。

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

## 4. 一次领取设备凭据

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
    "credentials": [
      {
        "credential_id": "opaque-grant-bound-id",
        "profile_name": "日常开发",
        "source_key_prefix": "sk-source-12ab",
        "device_key_name": "GetBeeAPI · 日常开发",
        "device_key_prefix": "sk-device-34cd",
        "api_key": "ONE-TIME-DEVICE-KEY-PLAINTEXT"
      }
    ],
    "retry_until": "2026-08-28T12:00:00Z"
  }
}
```

领取在事务中创建设备 Key、复制有效路由并记录授权关系。成功载荷只在短暂的 `retry_until` 窗口内以 AEAD/KMS 密文保存，用于响应中断后的幂等重试；窗口结束后不能再次取得明文。所有受保护请求都验证 DPoP 签名、`htm`、`htu`、`iat`、未使用过的 `jti`、令牌绑定 JWK 与 `ath`。

CLI 验证每个返回项都有非空 `credential_id` 与 `api_key`，随后逐个请求 `/v1/models?include_aliases=false`。若任一凭据无效或没有可用模型，首次配置不会写入工具文件。

## 本地多凭据行为

- 每个设备凭据拥有独立的本地 ID 与安全存储槽；opaque ID 不直接用作文件名或钥匙串账户名。
- Linux 优先 Secret Service，macOS 优先 Keychain，Windows 优先当前用户 DPAPI；回退文件权限为 `0600`。
- 用户可为不同工具选择不同凭据和模型。Claude Code 与 Claude Desktop Code 共享设置，因此必须使用同一凭据与模型。
- Codex profile 使用 `beeapi token print --agent codex` 按工具读取已分配的凭据，不把 Key 写进 profile。
- 手动粘贴模式只产生一个本地凭据，不具备网页多选与设备级撤销能力。

## 错误语义

| `reason` / 错误码 | CLI 行为 |
| --- | --- |
| `authorization_pending` | 按当前间隔继续轮询 |
| `slow_down` | 后续轮询间隔增加 5 秒 |
| `access_denied` | 立即停止并提示用户拒绝 |
| `expired_token` | 停止并要求重新发起授权 |
| `invalid_dpop_proof` / `cli.invalid_dpop_proof` | 停止，不能降级绕过 DPoP |
| `profile_unavailable` / `cli.profile_unavailable` | 所选来源配置已失效，重新批准 |
| `cli.claim_unavailable` | 幂等领取窗口已失效，重新授权 |
| `cli.invalid_token` | CLI 令牌无效或过期，重新授权 |

服务端返回 `404`、`405` 或 `501` 时，CLI 可以明确提供“手动粘贴 API Key”兼容回退，但绝不要求用户在终端输入 BeeAPI 账户密码。
