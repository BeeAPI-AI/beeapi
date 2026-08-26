import InstallCommands from "./InstallCommands";

const phases = [
  {
    number: "01",
    key: "ROUTE",
    title: "线路优选",
    description: "先检查 beeapi.ai 与 beeapi.dev；需要时从可访问的 Cloudflare IP 中，选择延迟更低、表现更稳定的一条。",
    note: "可访问性 → 速度排序 → 安全复验",
  },
  {
    number: "02",
    key: "AUTH",
    title: "登录并选择 Key",
    description: "在 BeeAPI 官方页面登录并选择一枚已有 Key，也可以直接粘贴 API Key 作为兼容方式。",
    note: "网页授权 / API Key",
  },
  {
    number: "03",
    key: "DETECT",
    title: "发现本地工具",
    description: "识别已安装的 CLI 与现有配置，给出适合当前环境的建议，也允许提前准备其他工具。",
    note: "binary + config",
  },
  {
    number: "04",
    key: "APPLY",
    title: "完成配置",
    description: "为多个智能体选择模型，统一备份后写入；任一步失败，都可以恢复整个配置批次。",
    note: "backup → merge → verify",
  },
];

const agents = [
  { name: "Claude Code", key: "CLAUDE", detail: "合并现有 settings.json" },
  { name: "Codex", key: "CODEX", detail: "使用独立 beeapi profile" },
  { name: "Gemini CLI", key: "GEMINI", detail: "安全注入运行环境" },
  { name: "OpenCode", key: "OPENCODE", detail: "写入兼容 provider" },
  { name: "OpenClaw", key: "OPENCLAW", detail: "同步 provider 与模型" },
];

const endpoints = [
  { domain: "beeapi.ai", label: "主域名" },
  { domain: "beeapi.dev", label: "备用域名" },
];

const safeguards = [
  { title: "下载可验证", text: "测速组件只取自 XIU2 官方 GitHub Release，并核对发布摘要。" },
  { title: "线路可验证", text: "候选 IP 仍使用 BeeAPI 域名完成 TLS 握手，证书不匹配立即拒绝。" },
  { title: "修改可恢复", text: "配置与 Hosts 写入前分别备份，受管区块不触碰用户的其他内容。" },
  { title: "凭据有边界", text: "短期 CLI 登录令牌只负责选择一枚现有 Key，不替代模型 API Key。" },
];

const faqs = [
  {
    question: "线路优选具体在选择什么？",
    answer: "先筛选能够正常访问 BeeAPI 的 Cloudflare IP，再根据延迟、下载速度与多次探测结果排序。最终候选还要通过 BeeAPI 域名的 TLS 与 API 响应验证。",
  },
  {
    question: "它会直接覆盖我现有的配置吗？",
    answer: "不会。写入前会按文件创建同批次备份，JSON 配置采用深度合并；任一工具写入失败会自动回滚，也可以运行 beeapi rollback latest。",
  },
  {
    question: "线路优选会直接修改 Hosts 吗？",
    answer: "不会。只有官方入口不可用，或你主动希望使用更快线路时，CLI 才会在展示结果后请求写入；原始 Hosts 会先完整备份。",
  },
  {
    question: "BeeAPI 还没上线网页授权怎么办？",
    answer: "选择第二项，粘贴 BeeAPI 控制台生成的 API Key 即可。CLI 不会因此退回到账户密码登录。",
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
            <a href="#workflow">CLI 能力</a>
            <a href="#endpoints">访问入口</a>
            <a href="#auth">授权登录</a>
            <a href="#agents">支持工具</a>
          </nav>
          <a className="header-action" href="#install">安装 beeapi</a>
        </div>
      </header>

      <section className="hero shell">
        <div className="status-pill"><span />为 BeeAPI 用户打造的跨平台 CLI</div>
        <h1 aria-label="BeeAPI，从一条命令开始。">BeeAPI，<br />从<span>一条命令</span>开始。</h1>
        <p className="hero-description">
          beeapi 会先选择可访问且响应更快的 BeeAPI 入口，再完成网页登录、Key 选择，并为 Codex、Claude Code 等工具准备好配置。
        </p>

        <div className="hero-install" id="install">
          <InstallCommands />
          <div className="install-assurance" aria-label="安装保障">
            <span><i />Linux · macOS · Windows</span>
            <span><i />SHA-256 校验安装包</span>
            <span><i />不输入账户密码</span>
          </div>
        </div>

        <p className="hero-hint">安装完成后输入 <code>beeapi</code>，按自己的需要完成选择。</p>

        <ol className="hero-flow" aria-label="beeapi 固定执行顺序">
          {phases.map((phase) => (
            <li key={phase.key}>
              <span>{phase.number}</span>
              <strong>{phase.title}</strong>
            </li>
          ))}
        </ol>
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
          <h2>一个 CLI，接好 BeeAPI 的每一步。</h2>
          <p>选择合适的访问线路，连接 BeeAPI 账户，再根据本地环境为常用 AI 工具生成配置。</p>
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
          <p>推荐在 BeeAPI 官方网页完成登录与 Key 选择；如果更习惯现有 Key，也可以直接粘贴。两种方式都不会让 CLI 接触账号密码。</p>
        </div>

        <div className="auth-grid">
          <article className="choice-card recommended">
            <div className="choice-top">
              <span className="choice-number">01</span>
              <span className="recommended-pill"><i />推荐</span>
            </div>
            <h3>网页登录并选择 Key</h3>
            <p>浏览器批准设备登录后，CLI 只显示 Key 名称与前缀供你选择。确认后，一次性交付你选中的那一枚到本机。</p>
            <div className="browser-preview" aria-label="BeeAPI 网页授权界面示意">
              <div className="browser-preview-bar">
                <span><i />beeapi.ai/cli/authorize</span>
                <b>官方域名</b>
              </div>
              <div className="approval-preview">
                <span className="preview-icon">B</span>
                <p><strong>GetBeeAPI 请求登录</strong><small>设备代码 · HM7K-WQ2P</small></p>
                <span className="mock-button">批准登录</span>
              </div>
            </div>
            <ul className="quiet-list">
              <li>密码、2FA 与网页 Cookie 不进入 CLI</li>
              <li>短期登录令牌与模型 API Key 完全分离</li>
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
          <p>设备授权、短期 CLI 登录令牌、Key 摘要列表与“一次导出一枚”相互隔离；普通 API Key 不能访问账户接口。</p>
          <a href="https://github.com/BeeAPI-AI/beeapi/blob/main/docs/device-authorization.md">查看设计文档 <span>↗</span></a>
        </div>
      </section>

      <section className="network-section" id="network">
        <div className="network-layout shell">
          <div className="section-heading network-copy">
            <p className="section-kicker">SMART ROUTE SELECTION</p>
            <h2>为 BeeAPI 选择<br />能访问、也更快的线路。</h2>
            <p>CloudflareSpeedTest 会先排除不可访问的 IP，再根据延迟与稳定性排序。最终候选还要通过 BeeAPI 域名的 TLS 证书与 API 响应验证。</p>
            <div className="fact-row">
              <span><strong>01</strong> 可访问性筛选</span>
              <span><strong>02</strong> 速度与稳定性排序</span>
              <span><strong>03</strong> TLS / API 复验</span>
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
              <li><span>02</span><p><strong>筛选可访问的 Cloudflare IP</strong><small>剔除超时、拒绝连接与响应异常的候选</small></p><b>可访问</b></li>
              <li><span>03</span><p><strong>按速度与稳定性排序</strong><small>综合延迟、下载速度与多次探测结果</small></p><b>低延迟</b></li>
              <li><span>04</span><p><strong>复验并按需写入 Hosts</strong><small>通过 BeeAPI 域名的 TLS 与 API 验证后再应用</small></p><b>可恢复</b></li>
            </ol>
          </div>
        </div>
      </section>

      <section className="content-section shell" id="agents">
        <div className="section-heading centered-heading agent-heading">
          <p className="section-kicker">LOCAL ENVIRONMENT</p>
          <h2>识别你已经在用的工具。</h2>
          <p>同时检查可执行文件与已有配置。检测结果用于推荐，最终配置哪些工具仍由你多选决定。</p>
        </div>

        <div className="agent-panel">
          <div className="agent-panel-top">
            <span><i />环境扫描完成</span>
            <b>已支持 {agents.length} 个工具</b>
          </div>
          <div className="agent-list">
            {agents.map((agent, index) => (
              <article key={agent.key}>
                <span>{String(index + 1).padStart(2, "0")}</span>
                <div><h3>{agent.name}</h3><p>{agent.detail}</p></div>
                <code>{agent.key}</code>
                <b>可配置</b>
              </article>
            ))}
          </div>
          <div className="config-flow" aria-label="配置写入流程">
            <span>环境扫描</span><i />
            <span>多选工具</span><i />
            <span>匹配模型</span><i />
            <span>统一备份</span><i />
            <span>写入验证</span>
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
        <h2>把 BeeAPI 接入<br />你常用的 AI 工具。</h2>
        <p>线路优选 → 账户连接 → 工具识别 → 配置完成</p>
        <a className="primary-button" href="#install">安装 beeapi <span>↑</span></a>
      </section>

      <footer className="site-footer shell">
        <a className="brand" href="#top" aria-label="返回 GetBeeAPI 首页"><Brand /></a>
        <p>为 BeeAPI 用户打造的线路优选与多智能体配置 CLI。</p>
        <nav aria-label="页脚导航">
          <a href="https://github.com/BeeAPI-AI/beeapi">GitHub <span>↗</span></a>
          <a href="#security">安全设计</a>
          <a href="#top">返回顶部 <span>↑</span></a>
        </nav>
      </footer>
    </main>
  );
}
