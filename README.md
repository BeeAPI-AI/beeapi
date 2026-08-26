# GetBeeAPI

GetBeeAPI 是面向 BeeAPI 用户的跨平台线路优选与多智能体配置助手。安装后运行 `beeapi`，按下面的顺序工作：

1. 检测 `beeapi.ai` 与 `beeapi.dev`；需要优选时，先筛选可访问的 Cloudflare IP，再按速度与稳定性排序，通过 TLS 与 API 复验后按需写入受管 Hosts。
2. 让用户在 BeeAPI 网站授权登录并选择一枚已有 Key，或直接粘贴 API Key 作为兼容回退。
3. 检测本机 Claude Code、Codex、Gemini CLI、OpenCode 与 OpenClaw。
4. 多选目标工具和模型，备份原配置后写入；失败自动回滚。

CLI 永远不会询问 BeeAPI 账号密码。网站设备授权需要 BeeAPI 服务端实现 [设备授权契约](docs/device-authorization.md)；在接口上线前，可直接使用 API Key 模式。

## 安装

Linux / macOS：

```sh
curl -fsSL https://getbeeapi.com/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://getbeeapi.com/install.ps1 | iex
```

安装器会校验发行包 SHA-256，并安装到用户目录。通过管道安装时，Unix 安装器从 `/dev/tty` 启动交互向导。

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
beeapi                         完整向导
beeapi detect                  检测 AI CLI 环境
beeapi configure               使用已保存凭据重新配置
beeapi network status          检查两个内置入口
beeapi network optimize        优选并验证 Cloudflare IP
beeapi network restore         删除受管 Hosts 记录
beeapi rollback latest         恢复最近一次配置备份
beeapi run codex               用 BeeAPI profile 启动 Codex
```

详细流程见 [架构说明](docs/architecture.md)。

## 安全边界

- 只从 XIU2/CloudflareSpeedTest 的 GitHub Release API 下载测速组件，并校验 GitHub 提供的 SHA-256 摘要。
- 优选 IP 必须以 BeeAPI 域名作为 SNI，通过 TLS 证书与业务接口双重验证后才能写入 Hosts。
- Hosts 只写入带 `getbeeapi managed` 标记的区块；写入前备份，可独立移除。
- 原有智能体配置会先做逐文件备份。Codex 使用独立 `beeapi` profile，不覆盖官方 ChatGPT 登录。
- 凭据优先进入系统钥匙串；不可用时退回权限为 `0600` 的本地文件。
