# BeeAPI CLI 登录与 API Key 选择契约

## 两种凭据必须分开

设备授权得到的是**短期 CLI 登录令牌**，不是模型调用用的 BeeAPI API Key：

| 凭据 | 用途 | 有效期 | 可访问范围 |
| --- | --- | --- | --- |
| CLI 登录令牌 | 进入一次本机配置流程 | 建议 5 分钟，无 refresh token | 查看 Key 摘要，并导出用户选择的一枚 Key |
| BeeAPI API Key | Claude Code、Codex 等模型调用 | 沿用该 Key 自身规则 | 仅 `/v1`、`/v1beta` 等模型接口 |

完整流程是：设备授权登录 → 获取现有 Key 的安全摘要列表 → 用户在 CLI 选择一个 Key → 一次性导出所选 Key → CLI 登录令牌立即失效 → 保存 Key 并继续环境识别。设备授权不会自动创建新的模型 Key。

CLI 是 public client，不配置 `client_secret`，不接收账号密码，不读取网页登录 Cookie。真正的登录、OAuth、2FA 和批准全部发生在 BeeAPI 官方域名，例如 `https://beeapi.ai/cli/authorize`；`getbeeapi.com` 不承载账户凭据。

## 上线前置安全修复

1. 整个 `/api/v1/me` 路由组在现有 `Required` 后增加 `JWTOnly`。普通 `sk-` Key 只能进入模型调用面，不能调用列表、创建、修改、删除或 reveal 等账户管理接口。
2. CLI 登录令牌采用独立凭据类型和独立中间件，例如前缀为 `beecli-` 的高熵 opaque token。通用 `Required` 必须拒绝它；只有 `/api/v1/cli/*` 接受它。
3. 登录页的 `next` 必须经过统一站内路径验证，并绑定到密码登录、OAuth `state` 和 2FA challenge。拒绝绝对 URL、`//`、反斜杠及编码绕过。
4. 导出接口必须是一次性权限，不能把 CLI 登录令牌升级为普通网页 JWT，也不能让它访问 `/me` 的通用 reveal 接口。

## 页面信息架构

| 入口 | 用途 |
| --- | --- |
| `/cli/authorize` | 登录、核对用户码和设备、批准或拒绝本次 CLI 登录 |
| `/profile` | “CLI 登录与授权设备”摘要、最近活动和管理入口 |
| `/profile/authorized-devices` | 登录记录、已导出的 Key 前缀、最近使用和撤销 |

授权页保持独立，账户页只放摘要。授权页应显示 GetBeeAPI、设备名、平台、发起时间、国家/地区级粗略位置，并醒目要求核对 `user_code`。

权限文案建议直接写成：

> 允许这台设备查看你的 API Key 名称与前缀，并把你在 CLI 中选择的一枚现有 Key 导出到本机。最多导出一枚；本次登录随后失效。

“拒绝”和“批准”并列。已启用 2FA 的账户复用现有 `VerifyStepUp`。批准成功只显示“可以返回 CLI”，绝不在网页显示 Key 明文。

## 1. 创建设备授权

```http
POST /api/v1/auth/device/code
Content-Type: application/json

{
  "client_id": "getbeeapi-cli",
  "scope": "api-keys:list api-keys:export-one",
  "device_name": "DESKTOP-01",
  "platform": "windows/amd64"
}
```

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "device_code": "256-bit-or-stronger-random-secret",
    "user_code": "BEE7-K9Q2",
    "verification_uri": "https://beeapi.ai/cli/authorize",
    "verification_uri_complete": "https://beeapi.ai/cli/authorize?user_code=BEE7-K9Q2",
    "expires_in": 600,
    "interval": 5
  }
}
```

要求：

- `device_code` 至少 256 bit 随机熵，数据库只保存带服务端 pepper 的哈希。
- `user_code` 仅用于人类核对，排除易混淆字符，并分别按 IP、`client_id` 与用户码限流。
- 授权 grant 默认 10 分钟过期；轮询至少间隔 5 秒。
- `client_id` 使用服务端 allowlist；public client 不配置无法保密的 `client_secret`。

上述 JSON 是兼容 BeeAPI 现有风格的“RFC 8628 风格”接口。若声明严格兼容 RFC 8628，请改用 form 请求、标准 OAuth `error` 响应，并在 Token 请求中使用 `grant_type=urn:ietf:params:oauth:grant-type:device_code`。GetBeeAPI CLI 同时接受 BeeAPI envelope 和标准 OAuth JSON 响应。

## 2. 批准并领取短期 CLI 登录令牌

批准只把 grant 原子地改为 `approved` 并绑定 `user_id`；此时不创建也不导出模型 API Key。

CLI 轮询：

```http
POST /api/v1/auth/device/token
Content-Type: application/json

{
  "client_id": "getbeeapi-cli",
  "device_code": "256-bit-or-stronger-random-secret"
}
```

批准后返回短期 CLI 登录令牌：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "access_token": "beecli-short-lived-opaque-secret",
    "token_type": "Bearer",
    "expires_in": 300,
    "scope": "api-keys:list api-keys:export-one"
  }
}
```

不要返回网页 JWT、refresh token 或 BeeAPI 模型 API Key。服务端保存登录令牌的哈希、过期时间、允许导出次数和已消费状态。

## 3. 列出用户现有 API Key 的安全摘要

```http
GET /api/v1/cli/api-keys
Authorization: Bearer beecli-short-lived-opaque-secret
```

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "items": [
      {
        "id": 42,
        "name": "日常开发",
        "key_prefix": "sk-bee-12ab",
        "status": "active",
        "group_name": "官方默认分组",
        "expires_at": null,
        "exportable": true
      }
    ]
  }
}
```

列表不返回明文、哈希、完整使用记录或其他管理能力。不可导出的旧 Key 仍可显示，但必须标记 `exportable=false`，CLI 不允许选择。

## 4. 一次性导出用户选择的 Key

```http
POST /api/v1/cli/api-keys/42/export
Authorization: Bearer beecli-short-lived-opaque-secret
```

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "api_key": "ONE-TIME-PLAINTEXT-OF-THE-SELECTED-KEY"
  }
}
```

接口只允许导出属于当前用户、状态有效且 `exportable=true` 的 Key。成功响应必须在同一个事务中把 CLI 登录令牌标记为 consumed；之后列表和第二次导出都返回拒绝。CLI 收到后立即保存到系统钥匙串，再清除内存中的登录令牌。

BeeAPI 现有 Key 若依赖 `key_plain` 才能 reveal，这一长期明文存储属于既有安全债务。设备授权不应新增另一份长期明文：为了处理成功响应途中断线，可以把一次性交付载荷用 AEAD/KMS 加密保存极短的幂等窗口，随后销毁。

## 错误语义

| `reason` / `error` | 含义 | CLI 行为 |
| --- | --- | --- |
| `authorization_pending` | 用户尚未操作 | 按原间隔继续轮询 |
| `slow_down` | 轮询过快 | 此后间隔永久增加至少 5 秒 |
| `access_denied` | 用户拒绝 | 立即停止 |
| `expired_token` | 设备码或 CLI 登录令牌过期 | 重新发起授权 |
| `insufficient_scope` | 尝试越权 | 停止并记录安全事件 |
| `already_consumed` | 已导出过一枚 Key | 不重试 |

## 数据模型建议

```text
device_authorizations
  id / client_id / user_id
  device_code_hash / user_code_hash
  device_name / platform / requested_scope
  status: pending | approved | denied | token_issued | consumed | expired | revoked
  cli_token_hash / cli_token_expires_at / export_count
  selected_api_key_id
  last_polled_at / next_poll_at / poll_interval_seconds
  encrypted_delivery_payload / delivery_payload_expires_at
  created_at / approved_at / issued_at / consumed_at / revoked_at
  request_ip_hmac / request_country / request_region / user_agent
```

IP 地址空间熵很低，不能使用无密钥 SHA 哈希，应使用带服务端密钥的 HMAC；粗略地区在创建时解析，不保存完整 IP。日志只记录 grant ID、状态和所选 Key ID，不记录设备码、CLI 登录令牌或 Key 明文。

## 上线验收顺序

1. 给 `/api/v1/me` 增加 JWT-only 集成测试与保护。
2. 完成安全的 `next`、OAuth `state` 与 2FA 回跳。
3. 实现设备 grant、短期 CLI 登录令牌及独立认证中间件。
4. 实现只读摘要列表和“一次导出一枚”的专用 `/cli` API。
5. 完成独立批准页、账户摘要与授权设备管理页。
6. 覆盖未批准、拒绝、过期、重复导出、跨用户 ID、暴力猜码和超频轮询测试。

服务端未上线时返回 `404`、`405` 或 `501`，CLI 会提供“手动粘贴 API Key”回退，绝不要求账号密码。

