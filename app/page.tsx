import Image from "next/image";
import InstallCommands from "./InstallCommands";

const phases = [
  {
    number: "01",
    key: "ROUTE",
    title: "选择可用入口",
    description: "先检查内置域名的健康状态，再读取并验证 BeeAPI 官方入口列表。能直接访问就使用最快入口；确实不可用时才进入 IP 优选。",
    note: "healthz → 官方列表 → 按需优选",
  },
  {
    number: "02",
    key: "AUTH",
    title: "连接 BeeAPI",
    description: "在 BeeAPI 官方页面登录并核对设备。批准后，CLI 一次读取账户当前可用的 API Key，再分别获取模型；也可直接粘贴单个 Key。",
    note: "网页批准设备 → 读取可用 Key → 获取模型",
  },
  {
    number: "03",
    key: "APPLY",
    title: "配置本地工具",
    description: "识别已有 CLI 和配置文件，多选目标工具，为每个工具选择密钥与模型；给这组选择起名，统一备份后写入，任一步失败都可恢复整个批次。",
    note: "detect → key → model → named profile",
  },
];

const agents = [
  { name: "Claude Code", key: "CLAUDE", kind: "编码 CLI", icon: "/tool-icons/claude.svg", detail: "合并现有 settings.json，保留权限与工具设置。" },
  { name: "Claude Desktop", key: "DESKTOP", kind: "Code 模式", icon: "/tool-icons/claude.svg", detail: "与 Claude Code 共享配置，并可从 CLI 直接打开 Code。" },
  { name: "Codex", key: "CODEX", kind: "编码 CLI", icon: "/tool-icons/codex.svg", detail: "只更新默认配置的 BeeAPI provider，不改 ChatGPT 登录与 MCP。" },
  { name: "Gemini CLI", key: "GEMINI", kind: "编码 CLI", icon: "/tool-icons/gemini.svg", detail: "只更新原生 .env 连接变量与鉴权选择。" },
  { name: "Grok Build", key: "GROK", kind: "编码 CLI", icon: "/tool-icons/grok.svg", detail: "在默认配置中增加 BeeAPI model，保留 UI 与 MCP。" },
  { name: "OpenCode", key: "OPENCODE", kind: "编码 CLI", icon: "/tool-icons/opencode.svg", detail: "深度合并 BeeAPI provider 与默认模型。" },
  { name: "OpenClaw", key: "OPENCLAW", kind: "个人智能体", icon: "/tool-icons/openclaw.svg", detail: "同步 provider、模型列表与主模型选择。" },
  { name: "Hermes", key: "HERMES", kind: "智能体 CLI", icon: "/tool-icons/hermes.svg", detail: "局部更新 model 与原生 .env，保留 agent、MCP 等设置。" },
];

const endpoints = [
  { domain: "beeapi.ai", label: "主域名" },
  { domain: "beeapi.dev", label: "备用域名" },
];

const safeguards = [
  { title: "下载可验证", text: "发行文件来源固定；优先走 GetBeeAPI 边缘缓存，失败回退 GitHub，并始终核对 SHA-256。" },
  { title: "线路可验证", text: "候选 IP 仍使用 BeeAPI 域名完成 TLS 握手，证书不匹配立即拒绝。" },
  { title: "修改可恢复", text: "配置与 Hosts 写入前分别备份，受管区块不触碰用户的其他内容。" },
  { title: "授权看得见", text: "网页会明确显示设备和授权范围；CLI 不接触密码、2FA 或网页会话，只在批准后一次读取现有可用 Key。" },
];

const faqs = [
  {
    question: "线路优选具体在选择什么？",
    answer: "正常入口可访问时不会启动优选。只有需要修复或你主动选择时，才先按 TCP 443 延迟与丢包筛选 Cloudflare IP，再用 BeeAPI 域名的 TLS 与 /healthz 复验前排候选；全部失败会自动回退到已有可用入口。",
  },
  {
    question: "它会弄乱我现有的配置吗？",
    answer: "不会。它先完整备份原文件，再只更新 BeeAPI 的 API 地址、Key、模型、provider 与必要鉴权字段；权限、MCP、主题、插件和其他 provider 保留。任一步失败会自动回滚，也可以运行 beeapi rollback latest。",
  },
  {
    question: "线路优选会直接修改 Hosts 吗？",
    answer: "不会。只有官方入口不可用，或你主动希望使用更快线路时，CLI 才会在展示结果后请求写入；原始 Hosts 会先完整备份。",
  },
  {
    question: "不方便使用网页授权怎么办？",
    answer: "选择第二项，粘贴 BeeAPI 控制台生成的单个 API Key 即可。兼容模式仍不会要求账户密码。",
  },
  {
    question: "能保存并切换多套配置吗？",
    answer: "可以。输入 beeapi 后只需给方案起名并按编号选择工具、Key 与模型；切换时先备份，再只更新 BeeAPI 管理字段。主页还会显示账户余额与每个 Key 的可用状态。",
  },
  {
    question: "Claude Desktop 的普通聊天也会切换到 BeeAPI 吗？",
    answer: "不会。GetBeeAPI 配置的是 Claude Desktop 中与 Claude Code 共用设置的 Code 模式；普通 Claude 聊天不支持替换底层模型入口。",
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

export default function Home() {
  return (
    <main id="top">
      <div className="hero-backdrop" aria-hidden="true">
        <span className="glow glow-one" />
        <span className="glow glow-two" />
        <span className="glow glow-three" />
      </div>

      <header className="site-header">
        <div className="header-inner shell">
          <a className="brand" href="#top" aria-label="GetBeeAPI 首页"><Brand /></a>
          <nav aria-label="主导航">
            <a href="#agents">支持工具</a>
            <a href="#workflow">工作方式</a>
            <a href="#endpoints">访问入口</a>
            <a href="#auth">授权登录</a>
          </nav>
          <a className="header-action" href="#install">安装 beeapi</a>
        </div>
      </header>

      <section className="hero shell">
        <div className="status-pill"><span />BEEAPI CONFIGURATOR · 8 TOOLS</div>
        <h1 aria-label="把 BeeAPI，接入你常用的 AI 工具。">把 BeeAPI，接入你<br />常用的 <span>AI 工具。</span></h1>
        <p className="hero-description">
          GetBeeAPI 是 BeeAPI 的快捷配置器。它识别本机已有工具，连接你的 BeeAPI 密钥配置与模型，备份原配置后完成写入——不用再逐个寻找配置文件和字段。
        </p>

        <div className="hero-positioning" aria-label="GetBeeAPI 产品定位">
          <span>不替代你的 AI 工具</span>
          <i aria-hidden="true" />
          <span>只负责把 BeeAPI 接进去</span>
        </div>

        <div className="hero-install" id="install">
          <InstallCommands />
          <div className="install-assurance" aria-label="安装保障">
            <span><i />Linux · macOS · Windows</span>
            <span><i />SHA-256 校验安装包</span>
            <span><i />不输入账户密码</span>
          </div>
        </div>

        <p className="hero-hint">首次输入 <code>beeapi</code> 完成三步设置；以后输入即可切换命名方案、查看余额与 Key 状态。</p>

        <div className="hero-toolrail" aria-label="GetBeeAPI 支持的工具">
          <p>一条命令，配置这些工具</p>
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

        <ol className="hero-flow" aria-label="beeapi 固定执行顺序">
          {phases.map((phase) => (
            <li key={phase.key}>
              <span>{phase.number}</span>
              <strong>{phase.title}</strong>
            </li>
          ))}
        </ol>
      </section>

      <section className="tools-section" id="agents">
        <div className="shell">
          <div className="tools-intro">
            <div>
              <p className="section-kicker">SUPPORTED TOOLS</p>
              <h2>熟悉的工具，<br />现在都能接上 BeeAPI。</h2>
            </div>
            <div className="tools-intro-copy">
              <span><i />当前版本 · 8 项适配</span>
              <p>同时检查可执行文件与已有配置，推荐本机已安装的工具；你也可以预先配置尚未安装的工具。</p>
            </div>
          </div>

          <div className="tool-grid">
            {agents.map((agent, index) => (
              <article className="tool-card" key={agent.key}>
                <div className="tool-card-top">
                  <span className="tool-mark">
                    <Image src={agent.icon} alt="" width={100} height={100} aria-hidden="true" />
                  </span>
                  <span className="supported-badge"><i />已支持</span>
                </div>
                <div className="tool-card-copy">
                  <span>{String(index + 1).padStart(2, "0")} · {agent.kind}</span>
                  <h3>{agent.name}</h3>
                  <p>{agent.detail}</p>
                </div>
                <code>{agent.key}</code>
              </article>
            ))}
          </div>

          <div className="tool-scope-note">
            <span className="tool-scope-icon">i</span>
            <p><strong>关于 Claude Desktop</strong>适配范围是其中的 Code 模式，它与 Claude Code 共享设置；普通 Claude 聊天仍使用 Anthropic 账户模型。</p>
            <code>CLAUDE DESKTOP · CODE</code>
          </div>

          <div className="config-flow" aria-label="配置写入流程">
            <span>环境扫描</span><i />
            <span>多选工具</span><i />
            <span>分配密钥与模型</span><i />
            <span>统一备份</span><i />
            <span>写入验证</span>
          </div>
        </div>
      </section>

      <section className="endpoint-section shell" id="endpoints">
        <div className="endpoint-intro">
          <div>
            <p className="section-kicker">BEEAPI ACCESS</p>
            <h2>BeeAPI 访问入口</h2>
          </div>
          <p>CLI 内置两个候选域名，并在你的网络中实时检查可访问性与响应速度。无法直连或希望进一步提速时，再进入 Cloudflare IP 优选。</p>
        </div>
        <div className="endpoint-list">
          {endpoints.map((endpoint) => (
            <a key={endpoint.domain} href={`https://${endpoint.domain}`} target="_blank" rel="noreferrer">
              <span className="endpoint-dot" aria-hidden="true" />
              <span className="endpoint-name"><strong>{endpoint.domain}</strong><small>{endpoint.label} · CLI 自动检测</small></span>
              <code>https://{endpoint.domain}</code>
              <b>访问 <span>↗</span></b>
            </a>
          ))}
        </div>
        <p className="endpoint-note">可访问情况以用户本机的实时检测结果为准。</p>
      </section>

      <section className="content-section shell" id="workflow">
        <div className="section-heading centered-heading">
          <p className="section-kicker">HOW IT WORKS</p>
          <h2>从 BeeAPI 密钥配置，到工具里的可用模型。</h2>
          <p>首次运行只有三步：可用入口、BeeAPI 连接、工具与模型配置。完成后再次输入 beeapi，会直接进入可切换方案、查看余额与管理 Key 状态的日常主页。</p>
        </div>
        <ol className="workflow-list">
          {phases.map((phase) => (
            <li key={phase.key}>
              <div className="step-meta"><span>{phase.number}</span><b>{phase.key}</b></div>
              <h3>{phase.title}</h3>
              <p>{phase.description}</p>
              <code>{phase.note}</code>
            </li>
          ))}
        </ol>
      </section>

      <section className="content-section shell" id="auth">
        <div className="section-heading split-heading">
          <div>
            <p className="section-kicker">AUTHORIZATION</p>
            <h2>用熟悉的方式，<br />连接 BeeAPI。</h2>
          </div>
          <p>推荐在 BeeAPI 官方网页完成登录、核对并授权设备；批准后 CLI 一次读取账户当前可用的现有 Key，不会额外创建 Key。也可以直接粘贴单个 Key。两种方式都不会让 CLI 接触账号密码。</p>
        </div>

        <div className="auth-grid">
          <article className="choice-card recommended">
            <div className="choice-top">
              <span className="choice-number">01</span>
              <span className="recommended-pill"><i />推荐</span>
            </div>
            <h3>网页登录并批准设备</h3>
            <p>在官方网页核对设备并批准。BeeAPI 会把账户当时可导出的可用 Key 一次性交给 CLI；Key 与工具的选择留在终端完成，不创建重复密钥。</p>
            <div className="browser-preview" aria-label="BeeAPI 网页授权界面示意">
              <div className="browser-preview-bar">
                <span><i />beeapi.ai/cli/authorize</span>
                <b>官方域名</b>
              </div>
              <div className="approval-preview">
                <span className="preview-icon">B</span>
                <p><strong>GetBeeAPI 请求登录</strong><small>设备代码 · HM7K-WQ2P</small></p>
                <span className="mock-button">批准此设备</span>
              </div>
            </div>
            <ul className="quiet-list">
              <li>密码、2FA 与网页 Cookie 不进入 CLI</li>
              <li>SSH 终端会显示完整授权网址与设备码，可在其他设备打开</li>
              <li>授权页会显示本次可读取的 Key 数量与范围</li>
            </ul>
          </article>

          <article className="choice-card fallback">
            <div className="choice-top">
              <span className="choice-number">02</span>
              <span className="fallback-pill">兼容回退</span>
            </div>
            <h3>直接粘贴 API Key</h3>
            <p>如果设备授权暂时不可用，或你已经准备好要使用的 Key，可以直接粘贴。终端会尽量关闭输入回显。</p>
            <div className="key-preview" aria-label="API Key 本地保存示意">
              <span>API KEY</span>
              <code>sk-••••••••••••••••••••••</code>
              <b>仅保存到本机</b>
            </div>
            <ul className="quiet-list">
              <li>优先使用系统钥匙串或凭据存储</li>
              <li>回退文件使用仅限当前用户的权限</li>
            </ul>
          </article>
        </div>

        <div className="service-note">
          <span>BeeAPI 服务端契约</span>
          <p>设备授权使用进程内临时 DPoP 密钥绑定；短期配置令牌只能一次领取批准时账户中可导出的现有 Key，不能访问 /me、模型转发或其他账户管理接口。</p>
          <a href="https://github.com/BeeAPI-AI/beeapi/blob/main/docs/device-authorization.md">查看设计文档 <span>↗</span></a>
        </div>
      </section>

      <section className="network-section" id="network">
        <div className="network-layout shell">
          <div className="section-heading network-copy">
            <p className="section-kicker">SMART ROUTE SELECTION</p>
            <h2>为 BeeAPI 选择<br />能访问、也更快的线路。</h2>
            <p>CloudflareSpeedTest 先筛出 TCP 443 可达的 IP；CLI 再用 BeeAPI 域名的 TLS 与 /healthz 复验前 20 个候选，并按真实 API 响应选择更快线路。</p>
            <div className="fact-row">
              <span><strong>01</strong> TCP 可达性筛选</span>
              <span><strong>02</strong> TLS / API 实测</span>
              <span><strong>03</strong> 失败自动回退</span>
            </div>
          </div>

          <div className="network-panel" aria-label="BeeAPI 线路优选执行步骤">
            <div className="panel-titlebar">
              <span>beeapi route optimize</span>
              <b><i />正在优选</b>
            </div>
            <div className="domain-probes">
              <div><span>beeapi.ai</span><b>可访问性检测</b></div>
              <div><span>beeapi.dev</span><b>可访问性检测</b></div>
            </div>
            <ol className="network-steps">
              <li><span>01</span><p><strong>检查官方域名可用性</strong><small>先判断是否需要优选，并记录可以直接使用的入口</small></p><b>HTTPS</b></li>
              <li><span>02</span><p><strong>筛选 TCP 443 可达 IP</strong><small>按连接延迟与丢包筛出前排候选</small></p><b>可访问</b></li>
              <li><span>03</span><p><strong>实测 TLS 与 /healthz</strong><small>并发复验前 20 个候选，选择真实 API 响应更快的线路</small></p><b>低延迟</b></li>
              <li><span>04</span><p><strong>写入 Hosts 或自动回退</strong><small>业务复验通过才写入；全部失败则继续使用可用域名</small></p><b>可恢复</b></li>
            </ol>
          </div>
        </div>
      </section>

      <section className="content-section shell" id="security">
        <div className="security-shell">
          <div className="section-heading security-heading">
            <p className="section-kicker">SAFE BY DEFAULT</p>
            <h2>连接更顺畅，<br />安全边界不变。</h2>
            <p>每一个会影响系统或凭据的步骤，都有明确验证、授权与恢复边界。</p>
          </div>
          <div className="safeguard-list">
            {safeguards.map((item, index) => (
              <article key={item.title}>
                <span>{String(index + 1).padStart(2, "0")}</span>
                <div><h3>{item.title}</h3><p>{item.text}</p></div>
              </article>
            ))}
          </div>
        </div>
      </section>

      <section className="content-section faq-section shell">
        <div className="section-heading faq-heading">
          <p className="section-kicker">FAQ</p>
          <h2>开始之前，<br />再确认几件事。</h2>
        </div>
        <div className="faq-list">
          {faqs.map((faq, index) => (
            <details key={faq.question} open={index === 0}>
              <summary><span>{String(index + 1).padStart(2, "0")}</span>{faq.question}<b>＋</b></summary>
              <p>{faq.answer}</p>
            </details>
          ))}
        </div>
      </section>

      <section className="final-cta shell">
        <div className="cta-glow" aria-hidden="true" />
        <span className="status-pill"><i />READY FOR BEEAPI</span>
        <h2>你的工具不变，<br />现在接上 BeeAPI。</h2>
        <p>8 项工具适配 · 自动识别 · 独立备份 · 一键回滚</p>
        <a className="primary-button" href="#install">安装 beeapi <span>↑</span></a>
      </section>

      <footer className="site-footer shell">
        <a className="brand" href="#top" aria-label="返回 GetBeeAPI 首页"><Brand /></a>
        <p>为 Claude Code、Codex 等现有 AI 工具快速配置 BeeAPI。</p>
        <nav aria-label="页脚导航">
          <a href="https://github.com/BeeAPI-AI/beeapi">GitHub <span>↗</span></a>
          <a href="#security">安全设计</a>
          <a href="#top">返回顶部 <span>↑</span></a>
        </nav>
      </footer>
    </main>
  );
}
