"use client";

import Image from "next/image";
import { useEffect, useMemo, useState } from "react";
import InstallCommands from "./InstallCommands";

type Locale = "zh" | "en";
type Theme = "light" | "dark";
type Localized = { zh: string; en: string };

type InstallStats = {
  metric: "verified_installations";
  successful_installs: number;
  updated_at: string;
};

const localized = (zh: string, en: string): Localized => ({ zh, en });

const phases = [
  {
    number: "01",
    key: "ROUTE",
    title: localized("选择可用入口", "Choose a reachable endpoint"),
    description: localized(
      "先检查内置域名的健康状态，再读取并验证 BeeAPI 官方入口列表。能直接访问就使用最快入口；确实不可用时才进入 IP 优选。",
      "Check the built-in domains first, then retrieve and verify BeeAPI's official endpoint list. The fastest reachable endpoint wins; IP optimization runs only when direct access fails.",
    ),
    note: localized("healthz → 官方列表 → 按需优选", "healthz → official list → optimize if needed"),
  },
  {
    number: "02",
    key: "AUTH",
    title: localized("连接 BeeAPI", "Connect BeeAPI"),
    description: localized(
      "在 BeeAPI 官方页面完成 OAuth 授权。CLI 先读取账户余额、Key 元数据与模型能力；你在终端选中 Key 后，才会一次性领取并安全保存。",
      "Authorize on BeeAPI's official site. The CLI reads account balance, Key metadata, and model capabilities first, then exports only the Key you choose and stores it safely.",
    ),
    note: localized("OAuth → 查看 Key / 模型 → 选择后领取", "OAuth → inspect Keys/models → export selection"),
  },
  {
    number: "03",
    key: "APPLY",
    title: localized("配置本地工具", "Configure local tools"),
    description: localized(
      "识别已有 CLI 和配置文件，多选目标工具，为每个工具选择密钥与模型；给这组选择起名，统一备份后写入，任一步失败都可恢复整个批次。",
      "Detect installed CLIs and config files, choose tools, assign a Key and model to each, name the profile, then back up and apply the batch with full recovery on failure.",
    ),
    note: localized("detect → key → model → named profile", "detect → key → model → named profile"),
  },
];

const agents = [
  { name: "Claude Code", key: "CLAUDE", kind: localized("编码 CLI", "Coding CLI"), icon: "/tool-icons/claude.svg", detail: localized("合并现有 settings.json，保留权限与工具设置。", "Merges settings.json while preserving permissions and tool settings.") },
  { name: "Claude Desktop", key: "DESKTOP", kind: localized("Code 模式", "Code mode"), icon: "/tool-icons/claude.svg", detail: localized("与 Claude Code 共享配置，并可从 CLI 直接打开 Code。", "Shares Claude Code configuration and can open Code directly from the CLI.") },
  { name: "Codex", key: "CODEX", kind: localized("编码 CLI", "Coding CLI"), icon: "/tool-icons/codex.svg", detail: localized("只更新默认配置的 BeeAPI provider，不改 ChatGPT 登录与 MCP。", "Updates only the BeeAPI provider in the default profile, leaving ChatGPT login and MCP untouched.") },
  { name: "Gemini CLI", key: "GEMINI", kind: localized("编码 CLI", "Coding CLI"), icon: "/tool-icons/gemini.svg", detail: localized("只更新原生 .env 连接变量与鉴权选择。", "Updates only native .env connection variables and authentication selection.") },
  { name: "Grok Build", key: "GROK", kind: localized("编码 CLI", "Coding CLI"), icon: "/tool-icons/grok.svg", detail: localized("在默认配置中增加 BeeAPI model，保留 UI 与 MCP。", "Adds a BeeAPI model to the default config while preserving UI and MCP settings.") },
  { name: "OpenCode", key: "OPENCODE", kind: localized("编码 CLI", "Coding CLI"), icon: "/tool-icons/opencode.svg", detail: localized("深度合并 BeeAPI provider 与默认模型。", "Deep-merges the BeeAPI provider and default model.") },
  { name: "OpenClaw", key: "OPENCLAW", kind: localized("个人智能体", "Personal agent"), icon: "/tool-icons/openclaw.svg", detail: localized("同步 provider、模型列表与主模型选择。", "Synchronizes the provider, model catalog, and primary model selection.") },
  { name: "Hermes", key: "HERMES", kind: localized("智能体 CLI", "Agent CLI"), icon: "/tool-icons/hermes.svg", detail: localized("局部更新 model 与原生 .env，保留 agent、MCP 等设置。", "Updates the model and native .env fields while preserving agent and MCP settings.") },
];

const endpoints = [
  { domain: "beeapi.ai", label: localized("主域名", "Primary domain") },
  { domain: "beeapi.dev", label: localized("备用域名", "Alternate domain") },
];

const safeguards = [
  { title: localized("下载可验证", "Verified downloads"), text: localized("发行文件来源固定；优先走 GetBeeAPI 边缘缓存，失败回退 GitHub，并始终核对 SHA-256。", "Release sources are pinned. Downloads prefer the GetBeeAPI edge cache, fall back to GitHub, and always verify SHA-256.") },
  { title: localized("线路可验证", "Verified routes"), text: localized("候选 IP 仍使用 BeeAPI 域名完成 TLS 握手，证书不匹配立即拒绝。", "Candidate IPs still complete TLS using the BeeAPI hostname; certificate mismatches are rejected immediately.") },
  { title: localized("修改可恢复", "Recoverable changes"), text: localized("配置与 Hosts 写入前分别备份，受管区块不触碰用户的其他内容。", "Configuration and Hosts are backed up before writes, and managed blocks leave unrelated settings untouched.") },
  { title: localized("授权看得见", "Visible authorization"), text: localized("网页明确列出账户、余额与 Key 读取范围；CLI 不接触密码、2FA 或 Cookie，只领取你随后在终端明确选中的 Key。", "The consent page lists account, balance, and Key access. The CLI never touches passwords, 2FA, or cookies, and exports only the Key you select.") },
];

const faqs = [
  {
    question: localized("线路优选具体在选择什么？", "What does route optimization select?"),
    answer: localized("正常入口可访问时不会启动优选。只有需要修复或你主动选择时，才先按 TCP 443 延迟与丢包筛选 Cloudflare IP，再用 BeeAPI 域名的 TLS 与 /healthz 复验前排候选；全部失败会自动回退到已有可用入口。", "Optimization stays off while an official endpoint is reachable. When repair is needed or requested, Cloudflare IPs are ranked by TCP 443 latency and loss, then finalists are rechecked with BeeAPI TLS and /healthz. If all fail, the CLI returns to a known-good endpoint."),
  },
  {
    question: localized("它会弄乱我现有的配置吗？", "Will it overwrite my existing configuration?"),
    answer: localized("不会。它先完整备份原文件，再只更新 BeeAPI 的 API 地址、Key、模型、provider 与必要鉴权字段；权限、MCP、主题、插件和其他 provider 保留。任一步失败会自动回滚，也可以运行 beeapi rollback latest。", "No. It backs up the original file, then updates only BeeAPI's API URL, Key, model, provider, and required auth fields. Permissions, MCP, themes, plugins, and other providers remain intact. Failures roll back automatically, and beeapi rollback latest is always available."),
  },
  {
    question: localized("线路优选会直接修改 Hosts 吗？", "Does route optimization edit Hosts immediately?"),
    answer: localized("不会。只有官方入口不可用，或你主动希望使用更快线路时，CLI 才会在展示结果后请求写入；原始 Hosts 会先完整备份。", "No. The CLI asks before writing only when official endpoints are unreachable or you explicitly request a faster route, and it backs up the original Hosts file first."),
  },
  {
    question: localized("不方便使用网页授权怎么办？", "What if browser authorization is inconvenient?"),
    answer: localized("选择第二项，粘贴 BeeAPI 控制台生成的单个 API Key 即可。兼容模式仍不会要求账户密码。", "Choose the fallback option and paste a single API Key created in the BeeAPI console. Compatibility mode never asks for your account password."),
  },
  {
    question: localized("能保存并切换多套配置吗？", "Can I save and switch multiple configurations?"),
    answer: localized("可以。输入 beeapi 后只需给方案起名并按编号选择工具、Key 与模型；切换时先备份，再只更新 BeeAPI 管理字段。主页还会显示账户余额与每个 Key 的可用状态。", "Yes. Run beeapi, name a profile, then choose tools, Keys, and models by number. Switching creates a backup and changes only BeeAPI-managed fields. The home screen also shows balance and each Key's availability."),
  },
  {
    question: localized("Claude Desktop 的普通聊天也会切换到 BeeAPI 吗？", "Does regular Claude Desktop chat switch to BeeAPI?"),
    answer: localized("不会。GetBeeAPI 配置的是 Claude Desktop 中与 Claude Code 共用设置的 Code 模式；普通 Claude 聊天不支持替换底层模型入口。", "No. GetBeeAPI configures Claude Desktop's Code mode, which shares Claude Code settings. Regular Claude chat does not support replacing its model endpoint."),
  },
];

function Brand() {
  return (
    <span className="brand-lockup">
      <span className="brand-symbol" aria-hidden="true">B</span>
      <span>GetBeeAPI</span>
    </span>
  );
}

function storePreference(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
    return true;
  } catch {
    return false;
  }
}

export default function HomePage() {
  const [locale, setLocale] = useState<Locale>("zh");
  const [theme, setTheme] = useState<Theme>("light");
  const [stats, setStats] = useState<InstallStats | null>(null);
  const [statsUnavailable, setStatsUnavailable] = useState(false);
  const text = (zh: string, en: string) => locale === "zh" ? zh : en;
  const pick = (value: Localized) => value[locale];

  useEffect(() => {
    const timer = window.setTimeout(() => {
      try {
        const savedLocale = localStorage.getItem("getbeeapi-locale");
        if (savedLocale === "zh" || savedLocale === "en") setLocale(savedLocale);
        const activeTheme = document.documentElement.dataset.theme;
        if (activeTheme === "dark" || activeTheme === "light") setTheme(activeTheme);
      } catch {
        return;
      }
    }, 0);
    return () => window.clearTimeout(timer);
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale === "zh" ? "zh-CN" : "en";
  }, [locale]);

  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/install-stats", { signal: controller.signal })
      .then((response) => {
        if (!response.ok) throw new Error(`stats returned ${response.status}`);
        return response.json() as Promise<InstallStats>;
      })
      .then((value) => {
        if (value.metric !== "verified_installations" || !Number.isFinite(value.successful_installs)) {
          throw new Error("invalid stats response");
        }
        setStats(value);
      })
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) setStatsUnavailable(true);
      });
    return () => controller.abort();
  }, []);

  const installCount = useMemo(() => {
    if (!stats) return statsUnavailable ? "—" : "···";
    return new Intl.NumberFormat(locale === "zh" ? "zh-CN" : "en-US").format(stats.successful_installs);
  }, [locale, stats, statsUnavailable]);

  function toggleLocale() {
    const next: Locale = locale === "zh" ? "en" : "zh";
    setLocale(next);
    storePreference("getbeeapi-locale", next);
  }

  function toggleTheme() {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    document.documentElement.dataset.theme = next;
    storePreference("getbeeapi-theme", next);
  }

  return (
    <main id="top">
      <div className="hero-backdrop" aria-hidden="true">
        <span className="glow glow-one" />
        <span className="glow glow-two" />
        <span className="glow glow-three" />
      </div>

      <header className="site-header">
        <div className="header-inner shell">
          <a className="brand" href="#top" aria-label={text("GetBeeAPI 首页", "GetBeeAPI home")}><Brand /></a>
          <nav aria-label={text("主导航", "Primary navigation")}>
            <a href="#agents">{text("支持工具", "Tools")}</a>
            <a href="#workflow">{text("工作方式", "Workflow")}</a>
            <a href="#endpoints">{text("访问入口", "Endpoints")}</a>
            <a href="#auth">{text("授权登录", "Authorization")}</a>
          </nav>
          <div className="header-actions">
            <button className="utility-button language-toggle" onClick={toggleLocale} type="button" aria-label={text("切换到英文", "Switch to Chinese")}>
              <span aria-hidden="true">{locale === "zh" ? "EN" : "中"}</span>
              <span className="utility-label">{locale === "zh" ? "English" : "中文"}</span>
            </button>
            <button className="utility-button theme-toggle" onClick={toggleTheme} type="button" aria-label={theme === "dark" ? text("切换到亮色模式", "Switch to light mode") : text("切换到深色模式", "Switch to dark mode")}>
              <span className="theme-glyph" aria-hidden="true">{theme === "dark" ? "☀" : "☾"}</span>
              <span className="utility-label">{theme === "dark" ? text("亮色", "Light") : text("深色", "Dark")}</span>
            </button>
            <a className="header-action" href="#install">{text("安装 beeapi", "Install beeapi")}</a>
          </div>
        </div>
      </header>

      <section className="hero shell">
        <div className="status-pill"><span />BEEAPI CONFIGURATOR · 8 TOOLS</div>
        <h1 aria-label={text("把 BeeAPI，接入你常用的 AI 工具。", "Connect BeeAPI to the AI tools you already use.")}>
          {text("把 BeeAPI，接入你", "Connect BeeAPI to the")}<br />
          {text("常用的 ", "AI tools you ")}<span>{text("AI 工具。", "already use.")}</span>
        </h1>
        <p className="hero-description">
          {text(
            "GetBeeAPI 是 BeeAPI 的快捷配置器。它识别本机已有工具，连接你的 BeeAPI 密钥配置与模型，备份原配置后完成写入——不用再逐个寻找配置文件和字段。",
            "GetBeeAPI is a fast configurator for BeeAPI. It detects your existing tools, connects BeeAPI Keys and models, backs up current settings, and writes only the fields that matter.",
          )}
        </p>

        <div className="hero-positioning" aria-label={text("GetBeeAPI 产品定位", "GetBeeAPI positioning")}>
          <span>{text("不替代你的 AI 工具", "Your AI tools stay the same")}</span>
          <i aria-hidden="true" />
          <span>{text("只负责把 BeeAPI 接进去", "GetBeeAPI connects BeeAPI to them")}</span>
        </div>

        <div className="hero-install" id="install">
          <InstallCommands locale={locale} />
          <div className="install-assurance" aria-label={text("安装保障", "Installation safeguards")}>
            <span><i />Linux · macOS · Windows</span>
            <span><i />{text("SHA-256 校验安装包", "SHA-256 verified packages")}</span>
            <span><i />{text("不输入账户密码", "No account password")}</span>
          </div>
        </div>

        <p className="hero-hint">
          {text("首次输入 ", "Run ")}<code>beeapi</code>{text(" 完成三步设置；以后输入即可切换方案、查看余额与 Key 状态，也可用 ", " for a three-step setup. Run it again to switch profiles, inspect balance and Key status, or use ")}<code>beeapi update</code>{text(" 安全更新。", " for verified updates.")}
        </p>

        <div className="hero-toolrail" aria-label={text("GetBeeAPI 支持的工具", "Tools supported by GetBeeAPI")}>
          <p>{text("一条命令，配置这些工具", "One command configures all these tools")}</p>
          <div className="hero-tools">
            {agents.map((agent) => (
              <div key={agent.key} className="hero-tool">
                <span className="tool-mark">
                  <Image src={agent.icon} alt="" width={100} height={100} aria-hidden="true" />
                </span>
                <span>{agent.name}</span>
              </div>
            ))}
          </div>
        </div>

        <ol className="hero-flow" aria-label={text("beeapi 固定执行顺序", "beeapi setup sequence")}>
          {phases.map((phase) => (
            <li key={phase.key}>
              <span>{phase.number}</span>
              <strong>{pick(phase.title)}</strong>
            </li>
          ))}
        </ol>

        <div className="hero-metrics" aria-label={text("GetBeeAPI 项目数据", "GetBeeAPI project statistics")}>
          <div className="metric-item metric-live">
            <span><i aria-hidden="true" />{text("成功安装", "Verified installs")}</span>
            <strong aria-live="polite">{installCount}</strong>
            <small>{statsUnavailable ? text("计数服务待接入", "Counter not connected yet") : text("安装并验证成功后计数", "Counted after install verification")}</small>
          </div>
          <div className="metric-item"><span>{text("已适配工具", "Supported tools")}</span><strong>8</strong><small>{text("可检测，也可预配置", "Detected or preconfigured")}</small></div>
          <div className="metric-item"><span>{text("桌面系统", "Desktop systems")}</span><strong>3</strong><small>Linux · macOS · Windows</small></div>
          <div className="metric-item"><span>{text("官方入口", "Official endpoints")}</span><strong>2</strong><small>beeapi.ai · beeapi.dev</small></div>
        </div>
        <p className="metric-disclaimer">{text("仅在安装包校验、写入并通过版本验证后计数；只汇总版本、系统、架构与日期，不保存 IP、账号或 Key。", "Counted only after checksum, installation, and version verification succeed. Only version, OS, architecture, and date are aggregated; no IP, account, or Key is stored.")}</p>
      </section>

      <section className="tools-section" id="agents">
        <div className="shell">
          <div className="tools-intro">
            <div>
              <p className="section-kicker">SUPPORTED TOOLS</p>
              <h2>{text("熟悉的工具，", "The tools you know,")}<br />{text("现在都能接上 BeeAPI。", "now connected to BeeAPI.")}</h2>
            </div>
            <div className="tools-intro-copy">
              <span><i />{text("当前版本 · 8 项适配", "Current release · 8 integrations")}</span>
              <p>{text("同时检查可执行文件与已有配置，推荐本机已安装的工具；你也可以预先配置尚未安装的工具。", "The CLI checks executables and existing config files, recommends installed tools, and can preconfigure tools you plan to install later.")}</p>
            </div>
          </div>

          <div className="tool-grid">
            {agents.map((agent, index) => (
              <article className="tool-card" key={agent.key}>
                <div className="tool-card-top">
                  <span className="tool-mark"><Image src={agent.icon} alt="" width={100} height={100} aria-hidden="true" /></span>
                  <span className="supported-badge"><i />{text("已支持", "Supported")}</span>
                </div>
                <div className="tool-card-copy">
                  <span>{String(index + 1).padStart(2, "0")} · {pick(agent.kind)}</span>
                  <h3>{agent.name}</h3>
                  <p>{pick(agent.detail)}</p>
                </div>
                <code>{agent.key}</code>
              </article>
            ))}
          </div>

          <div className="tool-scope-note">
            <span className="tool-scope-icon">i</span>
            <p><strong>{text("关于 Claude Desktop", "About Claude Desktop")}</strong>{text("适配范围是其中的 Code 模式，它与 Claude Code 共享设置；普通 Claude 聊天仍使用 Anthropic 账户模型。", "The integration covers Code mode, which shares Claude Code settings. Regular Claude chat continues to use Anthropic account models.")}</p>
            <code>CLAUDE DESKTOP · CODE</code>
          </div>

          <div className="config-flow" aria-label={text("配置写入流程", "Configuration flow")}>
            <span>{text("环境扫描", "Scan")}</span><i />
            <span>{text("多选工具", "Choose tools")}</span><i />
            <span>{text("分配密钥与模型", "Assign Keys and models")}</span><i />
            <span>{text("统一备份", "Back up")}</span><i />
            <span>{text("写入验证", "Apply and verify")}</span>
          </div>
        </div>
      </section>

      <section className="endpoint-section shell" id="endpoints">
        <div className="endpoint-intro">
          <div><p className="section-kicker">BEEAPI ACCESS</p><h2>{text("BeeAPI 访问入口", "BeeAPI endpoints")}</h2></div>
          <p>{text("CLI 内置两个候选域名，并在你的网络中实时检查可访问性与响应速度。无法直连或希望进一步提速时，再进入 Cloudflare IP 优选。", "The CLI includes two official domains and checks reachability and response time from your network. Cloudflare IP optimization is available only when direct access fails or you request it.")}</p>
        </div>
        <div className="endpoint-list">
          {endpoints.map((endpoint) => (
            <a key={endpoint.domain} href={`https://${endpoint.domain}`} target="_blank" rel="noreferrer">
              <span className="endpoint-dot" aria-hidden="true" />
              <span className="endpoint-name"><strong>{endpoint.domain}</strong><small>{pick(endpoint.label)} · {text("CLI 自动检测", "Auto-detected by CLI")}</small></span>
              <code>https://{endpoint.domain}</code>
              <b>{text("访问", "Open")} <span>↗</span></b>
            </a>
          ))}
        </div>
        <p className="endpoint-note">{text("可访问情况以用户本机的实时检测结果为准。", "Availability is determined by real-time checks on your device.")}</p>
      </section>

      <section className="content-section shell" id="workflow">
        <div className="section-heading centered-heading">
          <p className="section-kicker">HOW IT WORKS</p>
          <h2>{text("从 BeeAPI 密钥配置，到工具里的可用模型。", "From BeeAPI Keys to working models in your tools.")}</h2>
          <p>{text("首次运行只有三步：可用入口、BeeAPI 连接、工具与模型配置。完成后再次输入 beeapi，会直接进入可切换方案、查看余额与管理 Key 状态的日常主页。", "First run takes three steps: choose an endpoint, connect BeeAPI, then configure tools and models. After setup, beeapi opens a daily dashboard for switching profiles, checking balance, and managing Key status.")}</p>
        </div>
        <ol className="workflow-list">
          {phases.map((phase) => (
            <li key={phase.key}>
              <div className="step-meta"><span>{phase.number}</span><b>{phase.key}</b></div>
              <h3>{pick(phase.title)}</h3><p>{pick(phase.description)}</p><code>{pick(phase.note)}</code>
            </li>
          ))}
        </ol>
      </section>

      <section className="content-section shell" id="auth">
        <div className="section-heading split-heading">
          <div><p className="section-kicker">AUTHORIZATION</p><h2>{text("用熟悉的方式，", "A familiar way to")}<br />{text("连接 BeeAPI。", "connect BeeAPI.")}</h2></div>
          <p>{text("推荐通过 BeeAPI OAuth 连接账户：网页只负责登录并确认权限，CLI 先读取余额、Key 名称与模型能力；只有你在终端选中 Key 后，才会一次性领取。不会创建新 Key，也可以直接粘贴单个 Key 回退。", "Connect through BeeAPI OAuth: the web page handles login and consent, while the CLI reads balance, Key names, and model capabilities. It exports only the Key you select, creates no new Key, and supports direct paste as a fallback.")}</p>
        </div>

        <div className="auth-grid">
          <article className="choice-card recommended">
            <div className="choice-top"><span className="choice-number">01</span><span className="recommended-pill"><i />{text("推荐", "Recommended")}</span></div>
            <h3>{text("OAuth 连接 BeeAPI 账户", "Connect your BeeAPI account with OAuth")}</h3>
            <p>{text("桌面端使用浏览器 + PKCE 回到本机，SSH 使用设备码并始终显示完整网址。授权后先在终端查看 Key 与模型，选择哪一个，才领取哪一个。", "Desktop uses browser + PKCE with a loopback callback. SSH uses the Device Flow and always prints the full URL. Inspect Keys and models in the terminal, then export only your selection.")}</p>
            <div className="browser-preview" aria-label={text("BeeAPI 网页授权界面示意", "BeeAPI authorization preview")}>
              <div className="browser-preview-bar"><span><i />beeapi.dev/oauth/authorize</span><b>{text("官方域名", "Official domain")}</b></div>
              <div className="approval-preview">
                <span className="preview-icon">B</span>
                <p><strong>{text("GetBeeAPI 请求连接账户", "GetBeeAPI wants to connect")}</strong><small>{text("账户资料 · 余额 · Key 与模型", "Profile · balance · Keys and models")}</small></p>
                <span className="mock-button">{text("同意并继续", "Allow and continue")}</span>
              </div>
            </div>
            <ul className="quiet-list">
              <li>{text("密码、2FA 与网页 Cookie 不进入 CLI", "Passwords, 2FA, and web cookies never enter the CLI")}</li>
              <li>{text("SSH 终端始终显示完整授权网址与设备码，可在其他设备打开", "SSH always shows the full authorization URL and device code for use on another device")}</li>
              <li>{text("账户 Token 采用 DPoP 绑定，不能用于模型调用", "Account Tokens are DPoP-bound and cannot call models")}</li>
              <li>{text("API Key 先选择、再一次性领取、保存成功后立即确认清除", "API Keys are selected first, exported once, and cleared immediately after safe storage")}</li>
              <li>{text("令牌交换与 Key 导出响应中断时复用原请求恢复；保存后可从本地断点继续", "Interrupted token exchange or Key export resumes safely with the original request")}</li>
            </ul>
          </article>

          <article className="choice-card fallback">
            <div className="choice-top"><span className="choice-number">02</span><span className="fallback-pill">{text("兼容回退", "Fallback")}</span></div>
            <h3>{text("直接粘贴 API Key", "Paste an API Key")}</h3>
            <p>{text("如果设备授权暂时不可用，或你已经准备好要使用的 Key，可以直接粘贴。终端会尽量关闭输入回显。", "If OAuth is temporarily unavailable or you already know which Key to use, paste it directly. The terminal hides input whenever possible.")}</p>
            <div className="key-preview" aria-label={text("API Key 本地保存示意", "Local API Key storage preview")}><span>API KEY</span><code>sk-••••••••••••••••••••••</code><b>{text("仅保存到本机", "Stored locally only")}</b></div>
            <ul className="quiet-list"><li>{text("优先使用系统钥匙串或凭据存储", "Uses the system keychain or credential store when available")}</li><li>{text("回退文件使用仅限当前用户的权限", "Fallback files are restricted to the current user")}</li></ul>
          </article>
        </div>

        <div className="service-note">
          <span>{text("BeeAPI 服务端契约", "BeeAPI server contract")}</span>
          <p>{text("OAuth public client 使用 PKCE、标准 Device Grant、轮换 Refresh Token 与持久 DPoP 设备绑定。账户 Token 只允许读取已授权的账户资源，不能进入模型转发；API Key 选择性导出采用短期、幂等、保存后 ACK 的一次性交付。", "The OAuth public client uses PKCE, standard Device Grant, rotating Refresh Tokens, and persistent DPoP device binding. Account Tokens can read only authorized account resources and cannot call inference; selective API Key export is short-lived, idempotent, and acknowledged after safe storage.")}</p>
          <a href="https://github.com/BeeAPI-AI/beeapi/blob/main/docs/device-authorization.md">{text("查看设计文档", "Read the design")} <span>↗</span></a>
        </div>
      </section>

      <section className="network-section" id="network">
        <div className="network-layout shell">
          <div className="section-heading network-copy">
            <p className="section-kicker">SMART ROUTE SELECTION</p>
            <h2>{text("为 BeeAPI 选择", "Choose a BeeAPI route")}<br />{text("能访问、也更快的线路。", "that is reachable and fast.")}</h2>
            <p>{text("CloudflareSpeedTest 先筛出 TCP 443 可达的 IP；CLI 再用 BeeAPI 域名的 TLS 与 /healthz 复验前 20 个候选，并按真实 API 响应选择更快线路。", "CloudflareSpeedTest filters IPs reachable on TCP 443. The CLI then retests the top 20 with BeeAPI TLS and /healthz, choosing by real API response time.")}</p>
            <div className="fact-row"><span><strong>01</strong> {text("TCP 可达性筛选", "TCP reachability")}</span><span><strong>02</strong> {text("TLS / API 实测", "TLS / API checks")}</span><span><strong>03</strong> {text("失败自动回退", "Automatic fallback")}</span></div>
          </div>

          <div className="network-panel" aria-label={text("BeeAPI 线路优选执行步骤", "BeeAPI route optimization steps")}>
            <div className="panel-titlebar"><span>beeapi route optimize</span><b><i />{text("正在优选", "Optimizing")}</b></div>
            <div className="domain-probes"><div><span>beeapi.ai</span><b>{text("可访问性检测", "Reachability")}</b></div><div><span>beeapi.dev</span><b>{text("可访问性检测", "Reachability")}</b></div></div>
            <ol className="network-steps">
              <li><span>01</span><p><strong>{text("检查官方域名可用性", "Check official domains")}</strong><small>{text("先判断是否需要优选，并记录可以直接使用的入口", "Determine whether optimization is needed and keep known-good endpoints")}</small></p><b>HTTPS</b></li>
              <li><span>02</span><p><strong>{text("筛选 TCP 443 可达 IP", "Filter IPs reachable on TCP 443")}</strong><small>{text("按连接延迟与丢包筛出前排候选", "Rank candidates by connection latency and packet loss")}</small></p><b>{text("可访问", "REACHABLE")}</b></li>
              <li><span>03</span><p><strong>{text("实测 TLS 与 /healthz", "Verify TLS and /healthz")}</strong><small>{text("并发复验前 20 个候选，选择真实 API 响应更快的线路", "Retest the top 20 concurrently and choose by actual API response")}</small></p><b>{text("低延迟", "LOW LATENCY")}</b></li>
              <li><span>04</span><p><strong>{text("写入 Hosts 或自动回退", "Write Hosts or fall back")}</strong><small>{text("业务复验通过才写入；全部失败则继续使用可用域名", "Write only after application checks pass; otherwise use a reachable domain")}</small></p><b>{text("可恢复", "RECOVERABLE")}</b></li>
            </ol>
          </div>
        </div>
      </section>

      <section className="content-section shell" id="security">
        <div className="security-shell">
          <div className="section-heading security-heading"><p className="section-kicker">SAFE BY DEFAULT</p><h2>{text("连接更顺畅，", "A smoother connection,")}<br />{text("安全边界不变。", "with the same security boundaries.")}</h2><p>{text("每一个会影响系统或凭据的步骤，都有明确验证、授权与恢复边界。", "Every step that touches the system or credentials has explicit verification, consent, and recovery boundaries.")}</p></div>
          <div className="safeguard-list">
            {safeguards.map((item, index) => <article key={item.title.zh}><span>{String(index + 1).padStart(2, "0")}</span><div><h3>{pick(item.title)}</h3><p>{pick(item.text)}</p></div></article>)}
          </div>
        </div>
      </section>

      <section className="content-section faq-section shell">
        <div className="section-heading faq-heading"><p className="section-kicker">FAQ</p><h2>{text("开始之前，", "Before you start,")}<br />{text("再确认几件事。", "a few useful answers.")}</h2></div>
        <div className="faq-list">
          {faqs.map((faq, index) => <details key={faq.question.zh} open={index === 0}><summary><span>{String(index + 1).padStart(2, "0")}</span>{pick(faq.question)}<b>＋</b></summary><p>{pick(faq.answer)}</p></details>)}
        </div>
      </section>

      <section className="final-cta shell">
        <div className="cta-glow" aria-hidden="true" />
        <span className="status-pill"><i />READY FOR BEEAPI</span>
        <h2>{text("你的工具不变，", "Keep your tools.")}<br />{text("现在接上 BeeAPI。", "Connect them to BeeAPI.")}</h2>
        <p>{text("8 项工具适配 · 自动识别 · 独立备份 · 一键回滚", "8 integrations · automatic detection · isolated backups · one-step rollback")}</p>
        <a className="primary-button" href="#install">{text("安装 beeapi", "Install beeapi")} <span>↑</span></a>
      </section>

      <footer className="site-footer shell">
        <a className="brand" href="#top" aria-label={text("返回 GetBeeAPI 首页", "Back to GetBeeAPI home")}><Brand /></a>
        <p>{text("为 Claude Code、Codex 等现有 AI 工具快速配置 BeeAPI。", "Configure BeeAPI for Claude Code, Codex, and the AI tools you already use.")}</p>
        <nav aria-label={text("页脚导航", "Footer navigation")}><a href="https://github.com/BeeAPI-AI/beeapi">GitHub <span>↗</span></a><a href="#security">{text("安全设计", "Security")}</a><a href="#top">{text("返回顶部", "Back to top")} <span>↑</span></a></nav>
      </footer>
    </main>
  );
}
