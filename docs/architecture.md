# GetBeeAPI 架构与执行顺序

## 无参数主流程

`beeapi` 无参数运行时，执行顺序是产品契约，不应交换：

```text
beeapi
  ├─ 1. 访问线路选择
  │    ├─ 并发探测 beeapi.ai / beeapi.dev
  │    ├─ 选择延迟最低的可用入口
  │    └─ 均不可用 → CFST 优选 → TLS/业务校验 → 备份并写 Hosts
  ├─ 2. 登录
  │    ├─ 网站设备授权（默认）
  │    └─ 粘贴 API Key（兼容回退）
  ├─ 3. 环境识别
  │    ├─ 检查可执行文件
  │    └─ 检查现有配置文件
  └─ 4. 多选配置
       ├─ 获取当前 Key 可用模型
       ├─ 按工具推荐模型
       ├─ 备份现有配置
       └─ 写入或失败回滚
```

先修网络是必要条件：如果 BeeAPI 域名在当前网络不可达，网站授权和 API Key 校验都会失败。登录必须在环境识别之前，避免扫描完成后才发现无法获取模型和配置数据。

## 访问线路选择

内置入口只有 BeeAPI 官方返回的 `beeapi.ai` 与 `beeapi.dev`。探测使用 `/api/v1/public/api-endpoints`，并发执行，按成功状态和延迟排序。

当两个入口均不可达时：

1. 从 XIU2/CloudflareSpeedTest 官方 GitHub Release API 选择当前平台的发行包。
2. 验证 Release API 提供的 SHA-256 digest，限制下载与解压大小，只释放所需二进制、IP 段和许可证文件。
3. 使用目标 BeeAPI 域名的 `/api/v1/public/api-endpoints` 进行 HTTPing；按实际 API 延迟、丢包与稳定性排序，不做与 AI API 场景无关的大文件带宽测速。
4. 用候选 IP 建立连接，同时保留 BeeAPI 域名作为 URL 与 TLS SNI；只有证书和业务接口都成功才接受。
5. 备份 Hosts，并只写入带以下标记的受管区块：

```text
# >>> getbeeapi managed: beeapi.ai
104.16.0.1 beeapi.ai
# <<< getbeeapi managed: beeapi.ai
```

`beeapi network restore` 只删除受管区块，不触碰其他 Hosts 内容。系统 Hosts 需要管理员权限；交互流程应明确展示请求原因。

## 登录与凭据

默认采用 [设备授权](device-authorization.md)。CLI 只持有 `device_code` 并轮询，不接触用户名、密码、OAuth 凭据、2FA 验证码或网页 Cookie。批准后先取得一个短期 CLI 登录令牌，用它列出账户中现有 API Key 的安全摘要；用户选择其中一个后，CLI 一次性导出该 Key，并立即结束登录会话。CLI 登录令牌与模型 API Key 是两种完全不同的凭据。

如果服务端尚未实现设备授权，或用户主动选择兼容模式，CLI 接受粘贴的 API Key。输入尽量关闭终端回显。

凭据保存顺序：

- Linux：Secret Service（`secret-tool`），否则权限 `0600` 文件。
- macOS：Keychain，否则权限 `0600` 文件。
- Windows：当前用户 DPAPI，否则受保护文件。

## 环境识别与配置

检测结果只用于排序和默认选择，不会隐藏未安装的工具，方便用户预配置。当前支持：

| 工具 | 检测依据 | 写入方式 |
| --- | --- | --- |
| Claude Code | `claude` 或 `~/.claude/settings.json` | 深度合并 `settings.json` 的 BeeAPI 环境变量 |
| Claude Desktop（Code） | Claude Desktop 应用或本地配置 | 使用与 Claude Code 共享的 `~/.claude/settings.json`；`beeapi run claude-desktop` 通过官方深链打开 Code 模式 |
| Codex | `codex` 或 `~/.codex/config.toml` | 新建 `~/.codex/beeapi.config.toml` profile，使用 `beeapi token print` 命令取凭据 |
| Gemini CLI | `gemini` 或 `~/.gemini/settings.json` | 写受保护 env 文件，由 `beeapi run gemini` 注入 |
| Grok Build | `grok` 或 `~/.grok/config.toml` | 写 GetBeeAPI 专用 `GROK_HOME`，由 `beeapi run grok` 注入凭据并启动 |
| OpenCode | `opencode` 或本地配置 | 深度合并 BeeAPI provider |
| OpenClaw | `openclaw` 或本地配置 | 深度合并 BeeAPI provider 与默认模型 |
| Hermes | `hermes` 或 `~/.hermes/config.yaml` | 写 GetBeeAPI 专用 `HERMES_HOME` custom provider，由 `beeapi run hermes` 注入凭据并启动 |

每个工具可以选择不同模型。所有目标文件在第一次写入前归入同一个备份清单；任一写入失败就回滚整个批次。

Claude Desktop 的适配范围是其中的 Code 模式：Anthropic 官方说明 Code 模式与 Claude Code 共享设置，因此能够使用 BeeAPI 配置。普通 Claude 聊天仍由 Anthropic 账户提供模型，不支持用本地配置替换底层 API，GetBeeAPI 不会把 MCP 连接伪装成模型替换。

## 发布与安装

标签 `v*` 触发发行流程，为 Linux、macOS、Windows 的 `amd64` 与 `arm64` 构建静态二进制，生成每个归档的 SHA-256 文件并发布到 GitHub Releases。官网安装脚本只从 HTTPS 下载并在解压前校验摘要。
