const capabilities = [
  {
    number: "01",
    title: "一套媒体链路",
    text: "基于 lalmax 统一拉流、转协议与分发。摄像、录像与播放从同一条稳定链路获得数据。",
  },
  {
    number: "02",
    title: "跨平台部署",
    text: "提供 Docker、单文件与源码构建方式，轻松落地在 Linux、macOS、Windows 与 ARM 环境。",
  },
  {
    number: "03",
    title: "全协议实时预览",
    text: "WebCodecs、fMP4、WebRTC、HTTP-FLV、HLS / LL-HLS，按场景平衡延迟与兼容性。",
  },
  {
    number: "04",
    title: "设备接入不设限",
    text: "RTSP、ONVIF、GB28181、RTMP / SRT 与 HTTP JPEG 统一管理，设备发现到云台控制一气呵成。",
  },
];

const protocols = ["WebCodecs", "fMP4", "WebRTC", "HTTP-FLV", "HLS", "LL-HLS"];
const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

export default function Home() {
  return (
    <main>
      <div className="page-glow page-glow-one" />
      <div className="page-glow page-glow-two" />

      <nav className="nav wrap" aria-label="主导航">
        <a className="brand" href="#top" aria-label="lalmax-nvr 首页">
          <span className="brand-mark" aria-hidden="true"><i /><i /><i /></span>
          <span>lalmax<span className="brand-accent">-nvr</span></span>
        </a>
        <div className="nav-links">
          <a href="#features">能力</a>
          <a href="#architecture">架构</a>
          <a href="#deploy">部署</a>
          <a href="https://github.com/lalmax-pro/lalmax-nvr" target="_blank" rel="noreferrer">GitHub ↗</a>
        </div>
        <a className="nav-cta" href="#start">立即开始 <span>→</span></a>
      </nav>

      <section className="hero wrap" id="top">
        <div className="hero-copy">
          <p className="eyebrow"><span /> OPEN SOURCE · NETWORK VIDEO RECORDER</p>
          <h1>让每一路视频<br />都成为<span>可靠的实时数据。</span></h1>
          <p className="hero-lede">lalmax-nvr 是面向跨平台部署的网络视频录像机。用一套统一媒体链路，连接摄像头、直播、录像与智能事件。</p>
          <div className="hero-actions">
            <a className="button primary" href="#start">开始部署 <span>→</span></a>
            <a className="button quiet" href="https://github.com/lalmax-pro/lalmax-nvr" target="_blank" rel="noreferrer">查看源码 <span>↗</span></a>
          </div>
          <div className="hero-proof">
            <div><b>01</b><span>单一媒体<br />数据链路</span></div>
            <div><b>&lt;100<span>ms</span></b><span>WebCodecs<br />低延迟预览</span></div>
            <div><b>MIT</b><span>开放、可控<br />自由部署</span></div>
          </div>
        </div>

        <div className="hero-visual" aria-label="lalmax-nvr 视频控制台预览">
          <div className="orb orb-a" /><div className="orb orb-b" />
          <figure className="share-preview">
            <img src={`${basePath}/og.png`} alt="lalmax-nvr 跨平台网络视频录像机产品预览" />
            <figcaption>PRODUCT OVERVIEW · UNIFIED VIDEO OPERATIONS</figcaption>
          </figure>
          <div className="metric-card metric-up"><span>STREAM HEALTH</span><strong>99.98<span>%</span></strong><em>↑ Stable</em></div>
          <div className="metric-card metric-down"><span>PLAYBACK LATENCY</span><strong>86<span>ms</span></strong><em>WebCodecs</em></div>
          <div className="location-card"><span className="pin">⌖</span><span>ALL SYSTEMS<br /><b>NOMINAL</b></span><i /></div>
        </div>
      </section>

      <section className="signal-band" aria-label="支持的播放协议">
        <div className="marquee"><span>ONE PIPELINE</span><i />{protocols.map((protocol) => <span key={protocol}>{protocol}</span>)}<i /><span>ONE PIPELINE</span><i />{protocols.map((protocol) => <span key={`second-${protocol}`}>{protocol}</span>)}</div>
      </section>

      <section className="features wrap" id="features">
        <div className="section-intro"><p className="eyebrow"><span /> WHY LALMAX-NVR</p><h2>视频基础设施，<br /><em>本该如此清晰。</em></h2></div>
        <p className="section-summary">从设备发现到录像归档，从毫秒级预览到国标级联。把复杂的视频工程，收敛成一个掌控自如的产品界面。</p>
        <div className="capability-grid">
          {capabilities.map((item) => <article className="capability" key={item.number}><span>{item.number}</span><h3>{item.title}</h3><p>{item.text}</p><a href="#architecture" aria-label={`了解${item.title}`}>↗</a></article>)}
        </div>
      </section>

      <section className="architecture" id="architecture">
        <div className="wrap architecture-inner">
          <div className="architecture-copy"><p className="eyebrow"><span /> UNIFIED MEDIA ENGINE</p><h2>不重复拉流。<br />每一帧都走在<span>正确的路径</span>上。</h2><p>lalmax 负责媒体中继、协议转换与流分发；NVR 层专注设备生命周期、录像、存储和 Web 控制台。职责分层，链路更短。</p><a className="text-link" href="https://github.com/lalmax-pro/lalmax-nvr#architecture" target="_blank" rel="noreferrer">了解媒体架构 <span>→</span></a></div>
          <div className="pipeline" aria-label="摄像头到媒体引擎再到多协议播放的流程图">
            <div className="pipeline-node source"><span className="node-icon">◉</span><b>CAMERAS</b><small>RTSP · ONVIF · GB28181</small></div>
            <div className="pipeline-line"><i /><i /><i /></div>
            <div className="pipeline-node core"><span className="core-rings"><i /><i /><i /></span><b>lalmax</b><small>MEDIA ENGINE</small></div>
            <div className="pipeline-line"><i /><i /><i /></div>
            <div className="pipeline-node output"><span className="node-icon">▣</span><b>ANY SCREEN</b><small>LIVE · RECORD · ARCHIVE</small></div>
          </div>
        </div>
      </section>

      <section className="deployment wrap" id="deploy">
        <div className="deploy-card">
          <div><p className="eyebrow"><span /> DEPLOY ANYWHERE</p><h2>跑在你的环境，<br />遵循你的节奏。</h2><p>从开发机到边缘节点，从容器编排到单文件服务。lalmax-nvr 提供清爽、一致的部署体验。</p></div>
          <div className="platforms"><span>Linux</span><span>macOS</span><span>Windows</span><span>Docker</span><span>ARM</span><span>x86_64</span></div>
          <div className="terminal"><div className="terminal-bar"><i /><i /><i /><span>quick-start</span></div><code><span>$</span> docker compose up -d<br /><b>✓</b> lalmax-nvr is ready at <em>http://localhost:9090</em></code></div>
        </div>
      </section>

      <section className="start" id="start"><div className="wrap start-inner"><p className="eyebrow"><span /> READY WHEN YOU ARE</p><h2>把视频能力，<br /><em>变成你的产品优势。</em></h2><div><a className="button primary" href="https://github.com/lalmax-pro/lalmax-nvr" target="_blank" rel="noreferrer">前往 GitHub <span>↗</span></a><p>MIT License · 开源可用 · 社区共建</p></div></div></section>

      <footer className="footer wrap"><a className="brand" href="#top"><span className="brand-mark" aria-hidden="true"><i /><i /><i /></span><span>lalmax<span className="brand-accent">-nvr</span></span></a><span>Built on the lalmax media engine</span><span>© {new Date().getFullYear()} lalmax-nvr</span></footer>
    </main>
  );
}
