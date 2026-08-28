# GetBeeAPI

GetBeeAPI 是把 BeeAPI 快速接入现有 AI 工具的跨平台配置器，不是一个新的智能体。首次运行 `beeapi` 时按下面的三步完成设置：

1. 从内置入口开始探测 `/healthz`，再通过可用入口读取 `/api/v1/public/api-endpoints` 并验证官方列表。正常可访问时直接选择最快入口；只有用户选择不可访问的域名时才尝试 Cloudflare IP 优选和受管 Hosts，优选失败则自动回退到已有可用入口继续设置。
2. 在 BeeAPI 网站登录、核对并授权当前设备。批准后 CLI 一次读取账户中当前可导出的可用 API Key，再分别获取可用模型；也可直接粘贴单个 API Key 作为兼容回退。
3. 检测 Claude Code、Claude Desktop（Code 模式）、Codex、Gemini CLI、Grok Build、OpenCode、OpenClaw 与 Hermes，多选目标工具，并为每个工具选择密钥配置与模型；模型默认顺序采用 BeeAPI 的 Key 路由/商家市场优先级，所选 Key 不兼容时会要求更换其他 Key；备份原配置后写入，失败自动回滚。

首次设置完成后，再输入 `beeapi` 会打开带状态摘要的功能主页，可以启动已配置工具、更新配置、切换 Key、管理网络入口或恢复备份，不会重复执行首次向导。

CLI 永远不会询问 BeeAPI 账号密码，也不会读取网页登录 Cookie。网页会明确说明授权范围；批准后，BeeAPI 通过一次性领取流程把账户当时可导出的现有 API Key 交给 CLI，不额外创建 Key。协议见 [设备授权契约](docs/device-authorization.md)。不方便使用网页授权时，可直接使用单 Key 兼容模式。

## 安装

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
beeapi detect                  检测 AI CLI 环境
beeapi configure               使用已保存凭据重新配置
beeapi network status          检查两个内置入口
beeapi network optimize        优选并验证 Cloudflare IP
beeapi network restore         删除受管 Hosts 记录
beeapi rollback latest         恢复最近一次配置备份
beeapi run codex               用 BeeAPI profile 启动 Codex
beeapi run grok                用独立 GROK_HOME 启动 Grok Build
beeapi run hermes              用独立 HERMES_HOME 启动 Hermes
beeapi run claude-desktop      打开 Claude Desktop 的 Code 模式
beeapi token print --agent codex 仅向 Codex profile 提供其已分配的 Key
```

详细流程见 [架构说明](docs/architecture.md)。

## 安全边界

- CLI 与测速组件优先通过 `getbeeapi.com` 的固定白名单发行缓存下载，缓存异常时回退对应 GitHub Release；两条路径都必须通过同一 SHA-256 摘要校验。
- 优选 IP 必须以 BeeAPI 域名作为 SNI，通过 TLS 证书与业务接口双重验证后才能写入 Hosts。
- CloudflareSpeedTest 只负责 TCP 443 初筛；CLI 会并发复验前 20 个候选的 `/healthz` 实际延迟。全部失败时不会写 Hosts，而是继续使用已探测到的可用域名。
- Hosts 只写入带 `getbeeapi managed` 标记的区块；写入前备份，可独立移除。
- 原有工具配置会先做逐文件备份。Codex 使用独立 `beeapi` profile；Grok Build 与 Hermes 使用 GetBeeAPI 专用配置目录，不覆盖各自的官方登录与默认配置。
- 每个获准导出的账户 Key 都在本机独立存储并可分配给不同工具；Codex 通过 `beeapi token print --agent codex` 按工具读取，不把 Key 写进 profile。
- 凭据优先进入系统钥匙串；不可用时退回相互隔离、权限为 `0600` 的本地文件。
