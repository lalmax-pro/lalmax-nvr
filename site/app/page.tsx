const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

const capabilities = [
  {
    number: "01",
    title: "一套媒体链路",
    text: "基于 lalmax 统一拉流、转协议与分发。摄像、录像与播放从同一条稳定链路获得数据，不重复拉流。",
  },
  {
    number: "02",
    title: "全协议设备接入",
    text: "RTSP、ONVIF、GB28181、RTMP / SRT、HTTP JPEG 与小米 P2P 统一纳管，从设备发现到云台控制一气呵成。",
  },
  {
    number: "03",
    title: "毫秒级实时预览",
    text: "WebCodecs、fMP4、WebRTC、HTTP-FLV、HLS / LL-HLS 六种协议，按场景在延迟与兼容性之间自由取舍。",
  },
  {
    number: "04",
    title: "GB28181 国标全链路",
    text: "SIP 设备注册、平台级联、录像回放与语音对讲端到端覆盖，国内安防项目开箱即用。",
  },
  {
    number: "05",
    title: "智能录像与回放",
    text: "自动 MP4 分段，按摄像头配置连续 / 定时 / 事件录像与独立保留期；时间线缩放、日历定位、批量下载。",
  },
  {
    number: "06",
    title: "AI 智能检测分析",
    text: "对接 YOLO 检测引擎与多模态大模型，检测结果实时推送、落库可追溯，录像检索更聪明。",
  },
  {
    number: "07",
    title: "健康监控与自愈",
    text: "多层摄像头健康探测、自动恢复与连接质量指标（uptime / MTBF），故障可观测、可定位。",
  },
  {
    number: "08",
    title: "跨平台部署",
    text: "Docker、单文件与源码构建，零依赖、CGO_ENABLED=0，从容落地 Linux、macOS、Windows 与 ARM 环境。",
  },
];

const gbItems = [
  {
    tag: "DEVICE MGMT",
    title: "设备注册与目录同步",
    text: "国标设备一键注册，通道目录、在线状态与平台信息实时同步。",
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
  { k: "运行时", v: "Go 1.26 · 单二进制 · CGO_ENABLED=0" },
  { k: "存储", v: "SQLite（纯 Go）· MP4 分段录像 · 独立保留期" },
  { k: "媒体引擎", v: "嵌入式 lal / lalmax · 统一拉流与分发" },
  { k: "可观测", v: "Prometheus · OpenTelemetry · 健康自愈" },
  { k: "集成", v: "MQTT · WebDAV · FTP · REST API · SSE 事件" },
  { k: "授权", v: "MIT License · 开源可商用" },
];

const specChips = [
  "Linux", "macOS", "Windows", "ARM / 树莓派", "x86_64", "Docker",
];

const protocols = [
  "RTSP", "ONVIF", "GB28181", "RTMP", "SRT", "WebRTC",
  "HLS", "HTTP-FLV", "fMP4", "WebCodecs", "小米 P2P",
];

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
          <a href="#quickstart">快速开始</a>
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
            lalmax-nvr 是面向跨平台部署的网络视频录像机。一个 Go 二进制连接摄像头、直播、录像、国标平台与智能事件，单机部署即可组成一套完整的视频基础设施。
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
              <span>CGO_ENABLED=0 · 零运行时依赖</span>
            </div>
            <div>
              <b>&lt;100ms</b>
              <span>WebCodecs 低延迟预览</span>
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
            <img src={`${basePath}/dashboard.png`} alt="lalmax-nvr 控制台预览" />
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
            把复杂的视频工程收敛成清晰的产品界面——设备发现、实时预览、录像归档、智能检测与故障自愈，全部内置，无需拼凑中间件。
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
            <h2>国标全链路，开箱即用。</h2>
            <p>
              完整实现 GB/T 28181 的 SIP 设备管理、平台级联、录像回放与报警上送。无论是接入国标设备，还是向上级平台级联，都在同一套界面里完成。
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
                全部 Go 实现，编译期禁用 CGO。媒体引擎、录像、国标信令、AI 事件与 Web 控制台打包在同一个可执行文件里，连数据库都是纯 Go 的——没有运行时依赖，没有安装步骤。
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
            <h2>不重复拉流，每一帧都走在正确的路径上。</h2>
            <p>
              lalmax 负责媒体中继、协议转换与流分发；NVR 层专注设备生命周期、录像、存储和 Web 控制台。职责分层，链路更短，每路媒体流只进入引擎一次。
            </p>
            <a
              className="button quiet"
              href="https://github.com/lalmax-pro/lalmax-nvr#architecture"
              target="_blank"
              rel="noreferrer"
            >
              了解媒体架构 →
            </a>
          </div>
          <div className="arch-diagram">
            {/* 设备接入层 */}
            <div className="arch-inputs">
              <div className="arch-layer-tag">设备接入</div>
              <div className="arch-input-grid">
                {["RTSP", "ONVIF", "GB28181", "RTMP / SRT"].map((p) => (
                  <div className="arch-input-chip" key={p}>{p}</div>
                ))}
              </div>
            </div>

            <div className="arch-vline"><span>单次接入</span></div>

            {/* lalmax 引擎层 */}
            <div className="arch-engine">
              <div className="arch-engine-title">lalmax 媒体引擎</div>
              <div className="arch-engine-desc">统一拉流 · 转协议 · 流分发</div>
              <div className="arch-engine-badge">每路媒体流只接入一次</div>
            </div>

            <div className="arch-split" />

            {/* 消费者层 */}
            <div className="arch-consumers">
              <div className="arch-consumer">
                <div className="arch-consumer-icon">⏺</div>
                <b>录像存储</b>
                <small>MP4 分段 · 时间线 · 独立保留期</small>
              </div>
              <div className="arch-consumer">
                <div className="arch-consumer-icon">▶</div>
                <b>实时预览</b>
                <small>WebRTC · HLS · WebCodecs · fMP4</small>
              </div>
              <div className="arch-consumer">
                <div className="arch-consumer-icon">◎</div>
                <b>智能分析</b>
                <small>YOLO 检测 · 多模态 · 事件推送</small>
              </div>
            </div>

            <div className="arch-merge" />

            {/* 输出层 */}
            <div className="arch-output">
              <b>Web 控制台</b>
              <small>设备管理 · 回放检索 · 健康监控 · 国标级联 · API 集成</small>
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
            <h2>两分钟跑起来。</h2>
            <p>
              Docker 一键启动，或下载单文件直接运行。默认监听 9090 端口，浏览器打开即可完成初始配置。
            </p>
          </div>
          <div className="qs-grid">
            <div className="code-block">
              <span className="comment"># Docker 方式（推荐）</span>
              {"\n"}
              <span className="cmd">$</span> docker compose up -d
              {"\n"}
              <span className="ok">✓</span> lalmax-nvr ready at http://localhost:9090
            </div>
            <div className="code-block">
              <span className="comment"># 单文件 / 源码方式</span>
              {"\n"}
              <span className="cmd">$</span> ./lalmax-nvr -c config.yaml
              {"\n"}
              <span className="comment"># 或从源码构建</span>
              {"\n"}
              <span className="cmd">$</span> ./scripts/unix/build.sh &amp;&amp; ./scripts/unix/start.sh
            </div>
          </div>
        </div>
      </section>

      {/* CTA */}
      <section className="start wrap">
        <p className="eyebrow">
          <span /> 开源可用
        </p>
        <h2>把视频能力，变成你的产品优势。</h2>
        <a
          className="button primary"
          href="https://github.com/lalmax-pro/lalmax-nvr"
          target="_blank"
          rel="noreferrer"
        >
          前往 GitHub ↗
        </a>
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
        <span>Built on the lalmax media engine</span>
        <span>© {new Date().getFullYear()} lalmax-nvr</span>
      </footer>
    </main>
  );
}
