# GetBeeAPI 架构与执行顺序

## 无参数启动状态

`beeapi` 无参数运行时先读取本地生命周期状态：

```text
beeapi
  ├─ 未初始化 → 显示 Logo → 三步首次设置
  │    ├─ 1. 选择可用入口，必要时优选 IP
  │    ├─ 2. 网站选择配置并授权/粘贴单 Key，获取可用模型
  │    └─ 3. 识别工具、分配密钥与模型、备份并写入
  └─ 已初始化 → 显示 Logo、连接摘要与功能主页
       ├─ 启动已配置的 AI 工具
       ├─ 配置或更新 AI 工具
       ├─ 重新连接 / 更新密钥配置
       ├─ 检测或优化网络入口
       ├─ 检查本机工具
       ├─ 恢复配置备份
       └─ 用户明确选择时重新运行首次设置
```

完成状态记录在本地配置的 schema 与初始化时间中；旧版已经包含入口和凭据后端的配置会自动视为已初始化。凭据文件丢失不会让 CLI 假装成首次安装，用户仍可从主页选择“重新连接”。

## 访问线路选择

CLI 内置 `beeapi.ai` 与 `beeapi.dev` 作为引导入口。它先并发请求每个入口的 `/healthz`；找到可用入口后，再请求 `/api/v1/public/api-endpoints` 获取服务端维护的官方列表，合并去重并对列表中的每个域名重新执行 `/healthz` 探测。正常情况下直接使用最快可用入口，不启动 CFST。

只有当所有入口都不可用，或用户明确选择了一个不可用入口时，才进入修复流程：

1. 通过 GetBeeAPI 的固定白名单边缘缓存读取 XIU2/CloudflareSpeedTest 官方 Release 信息；缓存不可用时回退 GitHub API。
2. 验证 Release API 提供的 SHA-256 digest，限制下载与解压大小，只释放所需二进制、IP 段和许可证文件。
3. 使用 TCP 443 模式筛出当前网络能够连接的 Cloudflare IP，并按延迟与丢包排序；不对数千个候选直接执行域名 HTTPing，也不做与 AI API 场景无关的大文件带宽测速。
4. 并发复验前 20 个候选：连接候选 IP，同时保留 BeeAPI 域名作为 URL 与 TLS SNI；只接受证书正确且 `/healthz` 返回 `{"status":"ok"}` 的候选，再按实际 API 响应时间选择最快结果。
5. 如果全部候选都无法通过业务复验，则判定 Hosts 无法修复当前的域名/SNI 阻断；已有其他可用入口时自动回退并继续设置。
6. 备份 Hosts，并只写入带以下标记的受管区块：

```text
# >>> getbeeapi managed: beeapi.ai
104.16.0.1 beeapi.ai
# <<< getbeeapi managed: beeapi.ai
```

`beeapi network restore` 只删除受管区块，不触碰其他 Hosts 内容。系统 Hosts 需要管理员权限；交互流程应明确展示请求原因。

## 登录与凭据

默认采用 [设备授权](device-authorization.md)。CLI 只持有 `device_code` 并轮询，不接触用户名、密码、OAuth 凭据、2FA 验证码或网页 Cookie。设备授权请求与短期登录令牌使用同一枚进程内临时 P-256 DPoP 密钥绑定。用户在官方批准页选择 1–10 个现有密钥配置；服务端据此创建新的设备专用子 Key，CLI 用短期令牌一次领取这些新 Key，并逐个请求 `/v1/models`。账户原 Key 明文永不进入 CLI。

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
| Codex | `codex` 或 `~/.codex/config.toml` | 新建 `~/.codex/beeapi.config.toml` profile，使用 `beeapi token print --agent codex` 按工具取凭据 |
| Gemini CLI | `gemini` 或 `~/.gemini/settings.json` | 写受保护 env 文件，由 `beeapi run gemini` 注入 |
| Grok Build | `grok` 或 `~/.grok/config.toml` | 写 GetBeeAPI 专用 `GROK_HOME`，由 `beeapi run grok` 注入凭据并启动 |
| OpenCode | `opencode` 或本地配置 | 深度合并 BeeAPI provider |
| OpenClaw | `openclaw` 或本地配置 | 深度合并 BeeAPI provider 与默认模型 |
| Hermes | `hermes` 或 `~/.hermes/config.yaml` | 写 GetBeeAPI 专用 `HERMES_HOME` custom provider，由 `beeapi run hermes` 注入凭据并启动 |

每个工具可以选择不同的设备凭据及该凭据可用的模型。Claude Code 与 Claude Desktop Code 因共享同一设置文件，必须共用凭据与模型。所有目标文件在第一次写入前归入同一个备份清单；任一写入失败就回滚整个批次。

Claude Desktop 的适配范围是其中的 Code 模式：Anthropic 官方说明 Code 模式与 Claude Code 共享设置，因此能够使用 BeeAPI 配置。普通 Claude 聊天仍由 Anthropic 账户提供模型，不支持用本地配置替换底层 API，GetBeeAPI 不会把 MCP 连接伪装成模型替换。

## 发布与安装

标签 `v*` 触发发行流程，为 Linux、macOS、Windows 的 `amd64` 与 `arm64` 构建静态二进制，生成每个归档的 SHA-256 文件并发布到 GitHub Releases。安装器优先从 `getbeeapi.com/releases/...` 的固定白名单 Worker 路由获取边缘缓存，失败时回退 GitHub；无论来源都在解压前校验同一 SHA-256。该 Worker 只允许已知平台发行文件与 CFST 文件名，不接受任意上游 URL。

Unix 安装器按 zsh、bash、POSIX sh 或 fish 幂等写入 PATH；Windows 安装器更新用户 PATH。安装脚本无法修改父 shell 的当前进程环境，因此首次安装后应打开新终端，或按安装器提示加载对应启动文件。
