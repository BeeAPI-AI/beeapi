# BeeAPI · GetBeeAPI

BeeAPI 官方访问入口，以及面向 Claude Code、Claude Desktop、Codex、Gemini CLI、Grok Build、OpenCode、OpenClaw 和 Hermes 的快捷配置工具。

## BeeAPI 官方网址与 API 入口

| 域名 | 官方链接 | 用途 |
|---|---|---|
| `beeapi.ai` | [打开 BeeAPI](https://beeapi.ai/) | 官网与 API 入口 |
| `beeapi.dev` | [打开 BeeAPI](https://beeapi.dev/) | 官网与 API 入口 |

两个域名均为 BeeAPI 官方可用入口。不同地区、DNS 服务商和网络线路的可达性可能不同；如果其中一个入口暂时无法打开，可以直接尝试另一个。

建议收藏本仓库作为 BeeAPI 官方地址与 GetBeeAPI 安装方式的长期索引。登录、充值和粘贴 API Key 前，请确认浏览器地址属于上表列出的官方域名。

## GetBeeAPI CLI

GetBeeAPI 是把 BeeAPI 快速接入现有 AI 工具的跨平台配置器，不是一个新的智能体。首次运行 `beeapi` 时会先选择简体中文或 English，并保存为全局界面语言；随后按下面的三步完成设置：

1. 从内置入口开始探测 `/healthz`，再通过可用入口读取 `/api/v1/public/api-endpoints` 并验证官方列表。正常可访问时直接选择最快入口；只有用户选择不可访问的域名时才尝试 Cloudflare IP 优选和受管 Hosts，优选失败则自动回退到已有可用入口继续设置。
2. 通过 BeeAPI OAuth 连接账户。`beeapi.ai` 与 `beeapi.dev` 各自形成独立 OAuth 安全域，CLI 会始终在用户选中的域名完成 discovery、授权、Token、设备码与账户 API；切换域名必须重新授权。桌面端使用 Authorization Code + PKCE 与本机回调，SSH/无桌面环境使用标准 Device Grant 并始终显示完整授权网址。CLI 先读取账户余额、API Key 元数据与各 Key 的模型能力；用户在终端明确选中 Key 后才执行一次性导出。也可直接粘贴单个 API Key 作为兼容回退。
3. 检测 Claude Code、Claude Desktop（Code 模式）、Codex、Gemini CLI、Grok Build、OpenCode、OpenClaw 与 Hermes，多选目标工具，并为每个工具选择密钥配置与模型；模型默认顺序采用 BeeAPI 的 Key 路由/商家市场优先级，所选 Key 不兼容时会要求更换其他 Key；为这组选择命名后，备份原配置并写入，失败自动回滚。

首次设置完成后，再输入 `beeapi` 会打开 Shell 风格功能主页，直接显示当前方案、账户余额、入口以及每个工具正在使用的 Key 与模型。日常操作只需输入方案名称和菜单编号：可以创建、编辑或一键切换多套配置方案，也可以查看全部 Key 状态、重新授权、管理网络入口或恢复备份，不会重复执行首次向导或语言选择。语言可以在“更多设置 → 界面语言”中切换，也可以执行 `beeapi language zh-CN` 或 `beeapi language en`。

CLI 永远不会询问 BeeAPI 账号密码，也不会读取网页登录 Cookie。网页明确显示 OAuth 权限；账户 Token 采用 DPoP 设备绑定，只能读取获准的账户资料、余额、Key 元数据与模型能力，不能调用模型。用户在终端选中 Key 后，BeeAPI 通过短期幂等窗口一次性交付该 Key；CLI 先写入系统凭据存储，再发送 ACK，不额外创建 Key。网页批准、Token 交换或 Key 导出遇到瞬时断线时会按同一请求安全恢复；账户或 Key 已保存后，后续步骤失败可从断点继续。协议见 [OAuth 账户连接契约](docs/device-authorization.md)。不方便使用网页授权时，可直接使用单 Key 兼容模式。

## 安装

项目主页与图形化安装说明：[https://getbeeapi.com](https://getbeeapi.com/)

Linux / macOS：

```sh
curl -fsSL https://getbeeapi.com/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://getbeeapi.com/install.ps1 | iex
```

安装器会校验发行包 SHA-256，并安装到用户目录。Unix 安装器会把安装目录幂等写入当前 shell 的启动文件；Windows 安装器会更新用户 PATH。通过管道安装时，Unix 安装器从 `/dev/tty` 启动交互向导。打开新终端后可直接输入 `beeapi`。

## 本地开发

```sh
go test -buildvcs=false ./cmd/... ./internal/...
go run ./cmd/beeapi help
npm install
npm run dev
npm run build
```

常用命令：

```text
beeapi                         首次运行初始化；以后打开功能主页
beeapi setup                   重新运行首次设置
beeapi status                  查看当前连接与工具配置
beeapi login                   重新授权或更新密钥配置
beeapi logout                  撤销 OAuth 账户连接（保留本机 Key 与工具配置）
beeapi detect                  检测 AI CLI 环境
beeapi configure               使用已保存凭据重新配置
beeapi network status          检查两个内置入口
beeapi network optimize        优选并验证 Cloudflare IP
beeapi network restore         删除受管 Hosts 记录
beeapi rollback latest         恢复最近一次配置备份
beeapi language [zh-CN|en]     查看或切换界面语言
beeapi update                  检查、校验并安装最新 CLI
codex                          直接用已选 BeeAPI Key 与模型启动 Codex
gemini                         直接启动 Gemini CLI
grok                           直接启动 Grok Build
hermes                         直接启动 Hermes
beeapi run codex               兼容/排障入口；通常无需使用
beeapi run claude-desktop      打开 Claude Desktop 的 Code 模式
beeapi token print --agent codex 仅向 Codex 的 BeeAPI provider 提供已分配的 Key
```

配置完成后可直接使用工具原本的命令，不需要 `beeapi run`、工具自身的额外 profile 或 Shell 函数。GetBeeAPI 的“配置方案”只是对入口、Key、模型和目标工具的命名组合；切换时先完整备份原文件，再只更新默认配置中的 BeeAPI 连接字段，权限、MCP、主题、插件等无关设置保留。`beeapi run <工具>` 仍保留为兼容和排障入口。升级自 v0.2.x 时，现有配置会自动迁移为“默认配置”，不会立即重写工具文件；旧版受管 Shell 命令区块会在下一次配置写入时先备份再移除。

详细流程见 [架构说明](docs/architecture.md)。

## 安全边界

- CLI 与测速组件优先通过 `getbeeapi.com` 的固定白名单发行缓存下载，缓存异常时回退对应 GitHub Release；两条路径都必须通过同一 SHA-256 摘要校验。
- 正式版本无参数启动时至多每 24 小时检查一次更新，不会自动安装；只有用户运行 `beeapi update` 才下载当前平台发行包，并在 SHA-256 校验和安全解包通过后替换程序。
- 优选 IP 必须以 BeeAPI 域名作为 SNI，通过 TLS 证书与业务接口双重验证后才能写入 Hosts。
- CloudflareSpeedTest 只负责 TCP 443 初筛；CLI 会并发复验前 20 个候选的 `/healthz` 实际延迟。全部失败时不会写 Hosts，而是继续使用已探测到的可用域名。
- Hosts 只写入带 `getbeeapi managed` 标记的区块；写入前备份，可独立移除。
- 原有工具配置会先做逐文件完整备份；随后只修改 BeeAPI provider、API 地址、所选 Key、模型与必要的鉴权选择字段，不清空权限、MCP、主题或其他 provider。
- 每个获准导出的账户 Key 都在本机独立存储并可分配给不同工具；Codex 通过 `beeapi token print --agent codex` 按工具读取，不把 Key 明文写进 `config.toml`，现有 `auth.json` 保持不动。
- 命名配置方案只保存凭据 ID，不复制 API Key；切换方案会生成一批完整备份，只有工具文件与本地方案状态都成功更新后才生效。
- OAuth 连接可直接读取账户级余额；仅粘贴 API Key 的连接使用 API Key 鉴权的只读 `/v1/usage`。两种方式都不会发送模型请求或产生用量；首页使用短期缓存，详情页可主动刷新并检查每个 Key 的可用状态。
- 凭据优先进入系统钥匙串；不可用时退回相互隔离、权限为 `0600` 的本地文件。
- OAuth code、Token、DPoP 私钥与导出事务均绑定所选 issuer，不会跨 `beeapi.ai` / `beeapi.dev` 重试；`api.*` 域名仅用于对应域的 discovery。旧 `getbeeapi-cli / cli:configure` 协议不再回退，旧客户端必须升级后重新授权。
