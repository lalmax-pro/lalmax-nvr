const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

const capabilities = [
  {
    number: "01",
    title: "统一媒体链路",
    text: "基于 lal / lalmax 统一接入、转协议与分发。受支持的来源只进入媒体链路一次，直播与录像复用同一路流。",
  },
  {
    number: "02",
    title: "多源设备与流接入",
    text: "支持 RTSP、ONVIF、GB28181、SIP VoIP、RTMP / SRT / WHIP、HTTP JPEG 与小米 P2P，按来源选择拉流、推流或信令桥接。",
  },
  {
    number: "03",
    title: "多协议实时预览",
    text: "WebCodecs、fMP4、WebRTC、HTTP-FLV、HLS / LL-HLS，按终端编码能力、延迟要求与网络条件选择播放方式。",
  },
  {
    number: "04",
    title: "GB28181 国标能力",
    text: "覆盖 SIP 设备注册、目录管理、平台级联、录像检索回放、下载与语音对讲，支持实际项目对接验证。",
  },
  {
    number: "05",
    title: "计划录像与连续回放",
    text: "按 stream_id 配置连续 / 定时 / 事件 / 自适应录像；提供 MP4 分段、时间轴、连续 VOD、保留、锁定与合并。",
  },
  {
    number: "06",
    title: "AI 检测与事件联动",
    text: "支持浏览器 ONNX、Webhook 检测与可选多模态分析；事件可联动录像、Webhook 和云台预置位。",
  },
  {
    number: "07",
    title: "健康监控与自愈",
    text: "提供多层健康探测、事件记录与可配置的自动修复；结合指标、日志和 OpenTelemetry 定位异常。",
  },
  {
    number: "08",
    title: "跨平台部署",
    text: "Docker、预构建二进制与源码构建，覆盖 Linux、macOS、Windows 和 ARM；核心服务无需 CGO，可选媒体工具按需安装。",
  },
  {
    number: "09",
    title: "Home Assistant 与集成",
    text: "将流发布为 HA 摄像头，支持事件、录像、PTZ 与可选 WebRTC；并提供 MQTT、DLNA、WebDAV / FTP 等集成。",
  },
];

const gbItems = [
  {
    tag: "DEVICE MGMT",
    title: "设备注册与目录管理",
    text: "设备通过 REGISTER 接入，支持通道目录、在线状态、业务分组与行政区划管理。",
  },
  {
    tag: "CASCADE",
    title: "多级平台级联",
    text: "作为下级平台向国标平台注册，支持级联转发与跨域互通。",
  },
  {
    tag: "PLAYBACK",
    title: "录像回放与下载",
    text: "按时间范围检索国标设备录像，支持回放控制与文件下载。",
  },
  {
    tag: "INTERCOM",
    title: "语音对讲与报警",
    text: "双向语音对讲通道，设备报警上送平台，事件联动可配置。",
  },
];

const specRows = [
  { k: "运行时", v: "Go 1.26 · 核心单二进制 · CGO_ENABLED=0" },
  { k: "数据库", v: "SQLite · PostgreSQL · MySQL" },
  { k: "媒体引擎", v: "嵌入式 lal / lalmax · 统一接入与分发" },
  { k: "录像", v: "MP4 分段 · 连续 VOD · 保留与锁定" },
  { k: "内容源", v: "IPTV / M3U · Emby · 本地上传视频" },
  { k: "可观测", v: "Prometheus · OpenTelemetry · 健康事件" },
  { k: "集成", v: "Home Assistant · MQTT · DLNA · WebDAV / FTP" },
  { k: "授权", v: "MIT License · 开源可商用" },
];

const specChips = [
  "Linux", "macOS", "Windows", "ARM / 树莓派", "x86_64", "Docker",
];

const protocols = [
  "RTSP", "ONVIF", "GB28181", "SIP VoIP", "RTMP", "SRT", "WHIP / WHEP",
  "WebRTC", "HLS", "HTTP-FLV", "fMP4", "WebCodecs", "IPTV", "小米 P2P",
];

const wikiUrl = "https://github.com/lalmax-pro/lalmax-nvr/wiki";

export default function Home() {
  return (
    <main>
      {/* 导航 */}
      <nav className="nav wrap">
        <a className="brand" href="#top">
          <span className="brand-mark">
            <i />
            <i />
            <i />
          </span>
          <span>
            lalmax<span className="brand-accent">-nvr</span>
          </span>
        </a>
        <div className="nav-links">
          <a href="#features">能力</a>
          <a href="#gb">国标</a>
          <a href="#architecture">架构</a>
          <a href={wikiUrl} target="_blank" rel="noreferrer">知识库</a>
          <a href="#quickstart">快速开始</a>
          <a href={`${basePath}/docs/`}>API</a>
        </div>
        <a
          className="nav-github"
          href="https://github.com/lalmax-pro/lalmax-nvr"
          target="_blank"
          rel="noreferrer"
        >
          GitHub ↗
        </a>
      </nav>

      {/* Hero */}
      <section className="hero wrap" id="top">
        <div className="hero-copy">
          <p className="eyebrow">
            <span /> OPEN SOURCE · NETWORK VIDEO RECORDER
          </p>
          <h1>
            让每一路视频
            <br />
            都成为<span>可靠的实时数据。</span>
          </h1>
          <p className="hero-lede">
            lalmax-nvr 是开源、跨平台的网络视频录像机。一个 Go 核心二进制连接摄像头、编码器与内容源，统一管理实时播放、计划录像、国标平台、智能事件和第三方集成。
          </p>
          <div className="hero-actions">
            <a className="button primary" href="#quickstart">
              快速开始
            </a>
            <a
              className="button quiet"
              href="https://github.com/lalmax-pro/lalmax-nvr"
              target="_blank"
              rel="noreferrer"
            >
              查看源码 ↗
            </a>
          </div>
          <div className="hero-proof">
            <div>
              <b>单二进制</b>
              <span>核心服务 · CGO_ENABLED=0</span>
            </div>
            <div>
              <b>多协议</b>
              <span>WebRTC · WebCodecs · HLS / FLV</span>
            </div>
            <div>
              <b>MIT</b>
              <span>开源可商用</span>
            </div>
          </div>
        </div>
        <div className="hero-visual">
          <div className="hero-screenshot">
            <div className="browser-bar">
              <span className="browser-dot red" />
              <span className="browser-dot yellow" />
              <span className="browser-dot green" />
              <span className="browser-url">localhost:9090</span>
            </div>
            <img src={`${basePath}/dashboard.jpg`} alt="lalmax-nvr 控制台预览" />
          </div>
        </div>
      </section>

      {/* 协议条 */}
      <section className="protocols">
        <div className="wrap protocols-inner">
          <span className="protocols-label">支持协议</span>
          {protocols.map((p) => (
            <span key={p}>{p}</span>
          ))}
        </div>
      </section>

      {/* 核心能力 */}
      <section className="section wrap" id="features">
        <div className="section-head">
          <p className="eyebrow">
            <span /> 核心能力
          </p>
          <h2>从设备接入到智能回放，一套系统覆盖。</h2>
          <p>
            把设备、流、录像与事件收敛到统一的产品模型；按需启用外部检测、实时转码和第三方集成，而不是把每种来源都当作同一种协议。
          </p>
        </div>
        <div className="capability-grid">
          {capabilities.map((item) => (
            <article className="capability" key={item.number}>
              <div className="capability-num">{item.number}</div>
              <h3>{item.title}</h3>
              <p>{item.text}</p>
            </article>
          ))}
        </div>
      </section>

      {/* 国标 + 规格 合并区 */}
      <section className="feature-band" id="gb">
        <div className="wrap">
          <div className="section-head">
            <p className="eyebrow">
              <span /> GB28181 · 国标能力
            </p>
            <h2>覆盖国标接入、级联与回放。</h2>
            <p>
              提供 GB/T 28181 的 SIP 设备管理、平台级联、录像检索回放与报警事件能力。无论是设备注册接入，还是向上级平台级联，都在同一套界面里完成；具体兼容性以实际设备对接为准。
            </p>
          </div>
          <div className="gb-grid">
            {gbItems.map((item) => (
              <div className="gb-item" key={item.tag}>
                <em>{item.tag}</em>
                <b>{item.title}</b>
                <p>{item.text}</p>
              </div>
            ))}
          </div>

          <div className="specs-grid">
            <div>
              <h3>技术规格</h3>
              <p className="specs-desc">
                核心服务由 Go 实现并禁用 CGO，媒体引擎与 Web 控制台随同一个可执行文件分发。默认使用纯 Go SQLite，也可连接 PostgreSQL / MySQL；可选转码、快照与媒体检测按需使用 FFmpeg / GStreamer。
              </p>
              <div className="spec-chips">
                {specChips.map((c) => (
                  <span key={c}>{c}</span>
                ))}
              </div>
            </div>
            <div className="spec-list">
              {specRows.map((row) => (
                <div className="spec-row" key={row.k}>
                  <span>{row.k}</span>
                  <span>{row.v}</span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </section>

      {/* 架构 */}
      <section className="architecture wrap" id="architecture">
        <div className="arch-inner">
          <div className="arch-text">
            <p className="eyebrow">
              <span /> 统一媒体引擎
            </p>
            <h2>统一接入，直播与录像各取所需。</h2>
            <p>
              lal / lalmax 负责媒体接入、协议转换与流分发；NVR 层专注设备生命周期、录像计划、存储和 Web 控制台。职责分层，受支持的来源进入统一媒体组，直播与录像复用同一路流。
            </p>
            <a
              className="button quiet"
              href={`${wikiUrl}/Architecture`}
              target="_blank"
              rel="noreferrer"
            >
              阅读系统架构规范 →
            </a>
          </div>
          <div className="arch-diagram">
            {/* 设备接入层 */}
            <div className="arch-inputs">
              <div className="arch-layer-tag">设备接入</div>
              <div className="arch-input-grid">
                {["RTSP / ONVIF", "GB28181", "SIP VoIP", "RTMP / SRT / WHIP"].map((p) => (
                  <div className="arch-input-chip" key={p}>{p}</div>
                ))}
              </div>
            </div>

            <div className="arch-vline"><span>单次接入</span></div>

            {/* lalmax 引擎层 */}
            <div className="arch-engine">
              <div className="arch-engine-title">lal / lalmax 媒体引擎</div>
              <div className="arch-engine-desc">媒体接入 · 转协议 · 流分发</div>
              <div className="arch-engine-badge">同一路流复用分发与录像</div>
            </div>

            <div className="arch-split" />

            {/* 消费者层 */}
            <div className="arch-consumers">
              <div className="arch-consumer">
                <div className="arch-consumer-icon">REC</div>
                <b>录像存储</b>
                <small>MP4 分段 · 时间线 · 独立保留期</small>
              </div>
              <div className="arch-consumer">
                <div className="arch-consumer-icon">LIVE</div>
                <b>实时预览</b>
                <small>WebRTC · HLS · WebCodecs · fMP4</small>
              </div>
              <div className="arch-consumer">
                <div className="arch-consumer-icon">AI</div>
                <b>智能分析</b>
                <small>YOLO 检测 · 多模态 · 事件推送</small>
              </div>
            </div>

            <div className="arch-merge" />

            {/* 输出层 */}
            <div className="arch-output">
              <b>Web 控制台</b>
              <small>设备与流管理 · 录像计划 · 健康事件 · 国标级联 · OpenAPI 集成</small>
            </div>
          </div>
        </div>
      </section>

      {/* 快速开始 */}
      <section className="quickstart" id="quickstart">
        <div className="wrap">
          <div className="section-head">
            <p className="eyebrow">
              <span /> 快速开始
            </p>
            <h2>从启动到第一路流。</h2>
            <p>
              使用 Docker 或预构建二进制启动，默认监听 9090。完成管理员初始化后接入设备或推流；需要录像时，再为对应的 stream_id 创建录像计划。
            </p>
          </div>
          <div className="qs-grid">
            <div className="code-block">
              <span className="comment"># Docker 方式（推荐）</span>
              {"\n"}
              <span className="cmd">$</span> docker compose up -d
              {"\n"}
              <span className="comment"># 浏览器打开 http://localhost:9090 完成初始化</span>
            </div>
            <div className="code-block">
              <span className="comment"># 预构建二进制：初始化后启动</span>
              {"\n"}
              <span className="cmd">$</span> ./lalmax-nvr init --data-dir ./data --config config/lalmax-nvr.yaml
              {"\n"}
              <span className="cmd">$</span> ./lalmax-nvr -config config/lalmax-nvr.yaml
              {"\n"}
              <span className="comment"># 或从源码构建并启动</span>
              {"\n"}
              <span className="cmd">$</span> ./scripts/unix/build.sh &amp;&amp; ./scripts/unix/start.sh
            </div>
          </div>
          <div className="quickstart-links">
            <a href={`${wikiUrl}/Getting-Started`} target="_blank" rel="noreferrer">完整安装与首路流验收 →</a>
            <a href={`${basePath}/docs/`}>浏览 OpenAPI 文档 →</a>
          </div>
        </div>
      </section>

      {/* CTA */}
      <section className="start wrap">
        <p className="eyebrow">
          <span /> 开源可用
        </p>
        <h2>把视频能力，变成你的产品优势。</h2>
        <div className="start-actions">
          <a
            className="button primary"
            href="https://github.com/lalmax-pro/lalmax-nvr"
            target="_blank"
            rel="noreferrer"
          >
            前往 GitHub ↗
          </a>
          <a className="button quiet" href={wikiUrl} target="_blank" rel="noreferrer">
            打开技术知识库 →
          </a>
        </div>
        <p>MIT License · 社区共建 · 欢迎 Star 和 Issue</p>
      </section>

      {/* footer */}
      <footer className="footer wrap">
        <a className="brand" href="#top">
          <span className="brand-mark">
            <i />
            <i />
            <i />
          </span>
          <span>
            lalmax<span className="brand-accent">-nvr</span>
          </span>
        </a>
        <a href={wikiUrl} target="_blank" rel="noreferrer">技术知识库</a>
        <a href={`${basePath}/docs/`}>API 文档</a>
        <span>Built on the lal / lalmax media stack</span>
        <span>© {new Date().getFullYear()} lalmax-nvr</span>
      </footer>
    </main>
  );
}
