# GetBeeAPI 架构与执行顺序

## 无参数启动状态

`beeapi` 无参数运行时先读取本地生命周期状态：

```text
beeapi
  ├─ 未初始化 → 显示 Logo → 首次选择并保存界面语言 → 三步首次设置
  │    ├─ 1. 选择可用入口，必要时优选 IP
  │    ├─ 2. 网站授权设备/粘贴单 Key，读取既有 Key 与模型能力
  │    └─ 3. 识别工具、分配密钥与模型、备份并写入
  └─ 已初始化 → 显示 Logo、当前方案/余额/工具摘要与功能主页
       ├─ 快速切换、新建或编辑命名配置方案
       ├─ 管理方案名称与删除未使用方案
       ├─ 查看账户余额及全部 Key 状态
       ├─ 启动已配置的 AI 工具
       ├─ 重新连接 / 更新密钥
       └─ 更多设置
            ├─ 切换界面语言
            ├─ 检测或优化网络入口
            ├─ 检查本机工具
            ├─ 恢复配置备份
            └─ 用户明确选择时重新运行首次设置
```

完成状态记录在本地配置的 schema 与初始化时间中，界面语言以 `zh-CN` 或 `en` 写入同一个本地配置。只有真正未初始化的交互式首次运行才显示语言选择；旧版用户按系统语言平滑补齐，不会打断 `token print` 等由工具调用的非交互命令。旧版已经包含入口和凭据后端的配置会自动视为已初始化，并在只改本地状态的前提下迁移为对应语言的“默认配置”或“Default”。迁移本身不触碰任何工具文件。凭据文件丢失不会让 CLI 假装成首次安装，用户仍可从主页选择“重新连接”。

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

默认采用 [设备授权](device-authorization.md)。CLI 只持有 `device_code` 并轮询，不接触用户名、密码、OAuth 凭据、2FA 验证码或网页 Cookie。设备授权请求与短期登录令牌使用同一枚进程内临时 P-256 DPoP 密钥绑定。用户在官方页面核对并授权设备后，CLI 用短期令牌一次领取账户当时可导出的现有 API Key，不创建新的 Key；随后逐个读取模型能力，并在终端为工具分配 Key 与模型。

如果服务端尚未实现设备授权，或用户主动选择兼容模式，CLI 接受粘贴的 API Key。输入尽量关闭终端回显。

凭据保存顺序：

- Linux：Secret Service（`secret-tool`），否则权限 `0600` 文件。
- macOS：Keychain，否则权限 `0600` 文件。
- Windows：当前用户 DPAPI，否则受保护文件。

余额与 Key 状态通过 API Key 鉴权的 `GET /v1/usage` 读取。该接口只查询钱包和 Key 元数据，不经过模型路由且不计费。首页只读取当前使用的一个 Key 并缓存 30 秒；“密钥与余额”页面并发检查全部本地 Key，展示账户余额、Key 可用性、路由分组和到期日，不输出 Key 明文。

## 命名配置方案与切换

配置方案保存 BeeAPI 入口、目标工具、每个工具的凭据 ID 与模型，不保存 API Key 明文，也不复制目标工具的整份配置文件。首次设置创建第一套命名方案；旧版配置升级时自动生成“默认配置”。用户可以继续创建“工作开发”“日常对话”等方案，并通过 `beeapi` 首页的编号切换。

切换采用两阶段写入：先把本批次涉及的全部工具文件放进同一个备份，再通过各格式的字段级合并器写入 BeeAPI 管理字段；全部工具文件成功后才更新本地活动方案映射。如果本地状态保存失败，会立即使用本次备份恢复工具文件。不同方案可以覆盖不同工具，所以状态同时记录每个工具正在使用的方案和入口；首页在它们不一致时显示“混合配置”。正在被任何工具使用的方案不能删除。

## 环境识别与配置

检测结果只用于排序和默认选择，不会隐藏未安装的工具，方便用户预配置。当前支持：

| 工具 | 检测依据 | 写入方式 |
| --- | --- | --- |
| Claude Code | `claude` 或 `~/.claude/settings.json` | 深度合并 `settings.json` 的 BeeAPI 环境变量 |
| Claude Desktop（Code） | Claude Desktop 应用或本地配置 | 使用与 Claude Code 共享的 `~/.claude/settings.json`；`beeapi run claude-desktop` 通过官方深链打开 Code 模式 |
| Codex | `codex` 或 `~/.codex/config.toml` | 语法级更新默认 `config.toml` 的 BeeAPI provider、模型与地址，使用 `beeapi token print --agent codex` 取凭据；保留其他 TOML 段与 `auth.json` |
| Gemini CLI | `gemini` 或 `~/.gemini/settings.json` | 只更新 `~/.gemini/.env` 的 BeeAPI 连接变量及 `settings.json` 的 API Key 鉴权选择，保留其余设置 |
| Grok Build | `grok` 或 `~/.grok/config.toml` | 在默认 `config.toml` 中更新 `model.beeapi` 与默认模型，保留 UI、MCP 和其他模型 |
| OpenCode | `opencode` 或本地配置 | 深度合并 BeeAPI provider |
| OpenClaw | `openclaw` 或本地配置 | 深度合并 BeeAPI provider 与默认模型 |
| Hermes | `hermes` 或 `~/.hermes/config.yaml` | 只更新默认 `config.yaml` 的 `model` 连接字段及 `~/.hermes/.env` 的 OpenAI-compatible 凭据，保留 agent、MCP 等段落 |

每个工具可以选择不同的账户现有 API Key 及该 Key 可用的模型。CLI 先按服务端返回的协议与客户端适配性筛选，再把服务端 `priority` 最高项标为默认：API Key 路由优先级是第一层，同一路由内沿用 BeeAPI 商家市场的模型排列。交互模式始终先列出 API Key（不兼容项保留展示但不可选），再列出所选 Key 的兼容模型，由用户分别确认；首次设置的 `--yes` 或命令行重配置等非交互路径才自动采用默认项。旧服务端缺少专用能力接口时才回退到基础模型列表。Claude Code 与 Claude Desktop Code 因共享同一设置文件，必须共用 Key 与模型。所有目标配置文件在第一次写入前归入同一个完整备份清单；任一写入失败就回滚整个批次。

配置写入采用与 CC Switch 相同的“投影区”思路：只接管 BeeAPI 连接所需字段。JSON 只深度合并目标键；TOML 只更新顶层模型选择和 BeeAPI 专属表；`.env` 只替换指定变量；Hermes YAML 只更新 `model` 的三个连接字段。注释与无关段落尽量原样保留，写入保持幂等。Codex 的 `auth.json` 不参与修改，API Key 由原生 provider auth command 从本地凭据槽按需读取。工具因此可以直接以 `codex`、`gemini`、`grok` 或 `hermes` 启动，不再依赖 Shell 注入；旧版 Shell 管理区块会先备份再移除。

Claude Desktop 的适配范围是其中的 Code 模式：Anthropic 官方说明 Code 模式与 Claude Code 共享设置，因此能够使用 BeeAPI 配置。普通 Claude 聊天仍由 Anthropic 账户提供模型，不支持用本地配置替换底层 API，GetBeeAPI 不会把 MCP 连接伪装成模型替换。

## 发布与安装

标签 `v*` 触发发行流程，为 Linux、macOS、Windows 的 `amd64` 与 `arm64` 构建静态二进制，生成每个归档的 SHA-256 文件并发布到 GitHub Releases。安装器优先从 `getbeeapi.com/releases/...` 的固定白名单 Worker 路由获取边缘缓存，失败时回退 GitHub；无论来源都在解压前校验同一 SHA-256。该 Worker 只允许已知平台发行文件与 CFST 文件名，不接受任意上游 URL。

Unix 安装器按 zsh、bash、POSIX sh 或 fish 幂等写入 PATH；Windows 安装器更新用户 PATH。安装脚本无法修改父 shell 的当前进程环境，因此首次安装后应打开新终端，或按安装器提示加载对应启动文件。
