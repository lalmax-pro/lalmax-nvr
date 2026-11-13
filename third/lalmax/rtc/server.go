package rtc

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"strings"
	"time"

	config "github.com/q191201771/lalmax/config"
	maxlogic "github.com/q191201771/lalmax/logic"

	"github.com/gin-gonic/gin"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

// StreamNotFoundFn 流不存在时的回调，触发 on_stream_not_found 通知上层拉流
type StreamNotFoundFn func(app, stream, schema string)

type RtcServer struct {
	config               config.RtcConfig
	lalServer            logic.ILalServer
	publishAuthorizer    func(streamID, token string) error
	subscriberAuthorizer func(protocol, streamID, token string) error
	udpMux               ice.UDPMux
	tcpMux               ice.TCPMux
	streamNotFoundFn     StreamNotFoundFn
	whepSessions         sync.Map // session UUID -> *whepSession
}

// SetPublisherAuthorizer installs an admission check for WHIP publishers.
func (s *RtcServer) SetPublisherAuthorizer(authorizer func(streamID, token string) error) {
	s.publishAuthorizer = authorizer
}

// SetSubscriberAuthorizer installs an admission check for WebRTC playback.
func (s *RtcServer) SetSubscriberAuthorizer(authorizer func(protocol, streamID, token string) error) {
	s.subscriberAuthorizer = authorizer
}

func (s *RtcServer) authorizeSubscriber(c *gin.Context, protocol, streamID string) bool {
	if s.subscriberAuthorizer == nil {
		return true
	}
	if err := s.subscriberAuthorizer(protocol, streamID, c.Request.URL.Query().Get("token")); err != nil {
		nazalog.Warnf("reject unauthorized WebRTC subscriber, protocol:%s, stream:%s, error:%v", protocol, streamID, err)
		c.String(http.StatusUnauthorized, "unauthorized subscriber")
		return false
	}
	return true
}

// SetStreamNotFoundFn 注入流不存在回调
func (s *RtcServer) SetStreamNotFoundFn(fn StreamNotFoundFn) {
	s.streamNotFoundFn = fn
}

func (s *RtcServer) Close() error {
	s.whepSessions.Range(func(key, value any) bool {
		if session, ok := value.(*whepSession); ok { session.Close() }
		s.whepSessions.Delete(key)
		return true
	})
	var firstErr error
	if s.udpMux != nil {
		if err := s.udpMux.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.tcpMux != nil {
		if err := s.tcpMux.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// whepMediaReady means that WHEP can offer a codec-backed media track.
// A created Group alone is not enough: GB28181/RTP may have opened its
// receiver without receiving a sequence header yet.
func whepMediaReady(group *maxlogic.Group) bool {
	if group == nil {
		return false
	}
	video := group.GetVideoSeqHeaderMsg()
	if video != nil && (video.IsAvcKeySeqHeader() || video.IsHevcKeySeqHeader()) {
		return true
	}
	audio := group.GetAudioSeqHeaderMsg()
	if audio == nil {
		return false
	}
	switch audio.AudioCodecId() {
	case base.RtmpSoundFormatG711A, base.RtmpSoundFormatG711U, base.RtmpSoundFormatOpus:
		return true
	default:
		return false
	}
}

// GB28181 camera streams carry a 20-digit device ID and a 20-digit
// channel ID. They must negotiate a video track even if G711 audio arrived
// first. Ordinary standalone/VoIP audio-only streams remain supported.
func whepGB28181VideoStream(streamID string) bool {
	if len(streamID) != 41 || streamID[20] != ':' { return false }
	for i := 0; i < len(streamID); i++ {
		if i == 20 { continue }
		if streamID[i] < '0' || streamID[i] > '9' { return false }
	}
	return true
}

func whepMediaReadyForStream(group *maxlogic.Group, streamID string) bool {
	if group == nil { return false }
	if whepGB28181VideoStream(streamID) {
		header := group.GetVideoSeqHeaderMsg()
		return header != nil && (header.IsAvcKeySeqHeader() || header.IsHevcKeySeqHeader())
	}
	return whepMediaReady(group)
}

// waitStreamReady requests a missing publisher once and waits for the requested
// protocol's readiness. WHEP must see a supported codec sequence header before
// SDP negotiation; RTSP/other call sites retain their existing Group semantics.
func (s *RtcServer) waitStreamReady(appName, streamid, schema string) bool {
	key := maxlogic.NewStreamKey(appName, streamid)
	groups := maxlogic.GetGroupManagerInstance()
	ready := func(group *maxlogic.Group) bool {
		if group == nil {
			return false
		}
		if schema == "whep" {
			return whepMediaReadyForStream(group, streamid)
		}
		return true
	}

	_, group := groups.GetGroup(key)
	if ready(group) {
		return true
	}
	// An existing Group can be waiting for media after SIP INVITE. Do not
	// trigger another INVITE just because its codec headers are not ready.
	if group == nil && s.streamNotFoundFn != nil {
		nazalog.Infof("stream not found, triggering on_stream_not_found. app=%s, stream=%s", appName, streamid)
		s.streamNotFoundFn(appName, streamid, schema)
	}

	wait := 5 * time.Second
	if schema == "whep" && len(streamid) == 41 && strings.Count(streamid, ":") == 1 {
		wait = 12 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		_, group = groups.GetGroup(key)
		if ready(group) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func NewRtcServer(config config.RtcConfig, lal logic.ILalServer) (*RtcServer, error) {
	var udpMux ice.UDPMux
	var tcpMux ice.TCPMux
	listenIP := net.IPv4zero
	if config.ICEListenIP != "" {
		parsed := net.ParseIP(config.ICEListenIP)
		if parsed == nil {
			return nil, fmt.Errorf("invalid rtc ice_listen_ip %q", config.ICEListenIP)
		}
		listenIP = parsed
	}

	if config.ICEUDPMuxPort != 0 {
		var udplistener *net.UDPConn

		udplistener, err := net.ListenUDP("udp", &net.UDPAddr{
			IP:   listenIP,
			Port: config.ICEUDPMuxPort,
		})

		if err != nil {
			nazalog.Error(err)
			return nil, err
		}
		nazalog.Infof("webrtc ice udp listen. port=%d", config.ICEUDPMuxPort)
		udpMux = webrtc.NewICEUDPMux(nil, udplistener)
	}
	if config.WriteChanSize == 0 {
		config.WriteChanSize = 1024
	}
	if config.ICETCPMuxPort != 0 {
		var tcplistener *net.TCPListener

		tcplistener, err := net.ListenTCP("tcp", &net.TCPAddr{
			IP:   listenIP,
			Port: config.ICETCPMuxPort,
		})

		if err != nil {
			nazalog.Error(err)
			return nil, err
		}
		nazalog.Infof("webrtc ice tcp listen. port=%d", config.ICETCPMuxPort)
		tcpMux = webrtc.NewICETCPMux(nil, tcplistener, 20)
	}

	svr := &RtcServer{
		config:    config,
		lalServer: lal,
		udpMux:    udpMux,
		tcpMux:    tcpMux,
	}

	return svr, nil
}

func (s *RtcServer) HandleWHIP(c *gin.Context) {
	streamid := c.Request.URL.Query().Get("streamid")
	if streamid == "" {
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	if s.publishAuthorizer != nil {
		token := c.Request.URL.Query().Get("stream_key")
		if authorization := strings.Fields(c.GetHeader("Authorization")); len(authorization) == 2 && strings.EqualFold(authorization[0], "Bearer") {
			token = authorization[1]
		}
		if err := s.publishAuthorizer(streamid, token); err != nil {
			c.Header("WWW-Authenticate", "Bearer")
			c.String(http.StatusUnauthorized, "unauthorized publisher")
			return
		}
	}

	body, err := c.GetRawData()
	if err != nil {
		nazalog.Error(err)
		c.Status(http.StatusBadRequest)
		return
	}

	if len(body) == 0 {
		nazalog.Error("invalid body")
		c.Status(http.StatusNoContent)
		return
	}

	pc, err := newPeerConnection(s.config.ICEHostNATToIPs, s.udpMux, s.tcpMux)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	whipsession := NewWhipSession(streamid, pc, s.lalServer)
	if whipsession == nil {
		c.Status(http.StatusInternalServerError)
		pc.Close()
		return
	}

	c.Header("Location", fmt.Sprintf("whip/%s", whipsession.subscriberId))

	sdp := whipsession.GetAnswerSDP(string(body))
	if sdp == "" {
		c.Status(http.StatusInternalServerError)
		whipsession.Close()
		return
	}

	go whipsession.Run()

	c.Data(http.StatusCreated, "application/sdp", []byte(sdp))
}

// ServeWHIPPublishPage 返回内嵌推流页：浏览器直接打开 WHIP URL 即可通过 WHIP POST 建立 WebRTC 推流（与 ServeWHEPPlayPage 对称）。
func (s *RtcServer) ServeWHIPPublishPage(c *gin.Context) {
	if c.Request.URL.Query().Get("streamid") == "" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusBadRequest, "<!doctype html><meta charset=utf-8><title>WHIP</title><p>缺少查询参数 <code>streamid</code>。示例：<code>/webrtc/whip?streamid=test110</code></p>")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Accept-Post", "application/sdp")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
	c.Header("Access-Control-Expose-Headers", "Location")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(buildWHIPPublishHTML()))
}

func buildWHIPPublishHTML() string {
	return "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>WHIP Publisher</title><style>body{margin:0;background:#0f172a;color:#e2e8f0;font:14px/1.4 system-ui}main{max-width:960px;margin:0 auto;padding:24px}video{width:100%;max-height:360px;background:#000;border-radius:12px}pre{white-space:pre-wrap;background:#111827;padding:12px;border-radius:12px;min-height:72px}</style></head><body><main><p>本页使用摄像头/麦克风通过 WHIP 推流（H264+Opus）。请允许浏览器媒体权限。</p><video id=\"preview\" autoplay muted playsinline></video><pre id=\"log\">connecting...</pre></main><script>(async()=>{const log=(m)=>{document.getElementById('log').textContent=m};const preview=document.getElementById('preview');try{if(!navigator.mediaDevices||!navigator.mediaDevices.getUserMedia){log('当前环境不支持 getUserMedia');return}const stream=await navigator.mediaDevices.getUserMedia({video:true,audio:true});preview.srcObject=stream;const pc=new RTCPeerConnection();stream.getTracks().forEach(t=>pc.addTrack(t,stream));const offer=await pc.createOffer();await pc.setLocalDescription(offer);await new Promise(r=>{if(pc.iceGatheringState==='complete')return r();pc.addEventListener('icegatheringstatechange',()=>{if(pc.iceGatheringState==='complete')r()})});const res=await fetch(location.href,{method:'POST',headers:{'Content-Type':'application/sdp'},body:pc.localDescription.sdp});if(!res.ok){log('WHIP 失败: '+res.status+' '+await res.text());return}const answer=await res.text();await pc.setRemoteDescription({type:'answer',sdp:answer});log('WHIP 已连接，正在推流: '+location.href)}catch(e){log('错误: '+(e&&e.message?e.message:String(e)))}})();</script></body></html>"
}

func (s *RtcServer) HandleJessibuca(c *gin.Context) {
	streamid := c.Param("streamid")
	if streamid == "" {
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeSubscriber(c, "jessibuca", streamid) {
		return
	}
	appName := c.Query("app_name")

	body, err := c.GetRawData()
	if err != nil {
		nazalog.Error(err)
		c.Status(http.StatusBadRequest)
		return
	}

	if len(body) == 0 {
		nazalog.Error("invalid body")
		c.Status(http.StatusNoContent)
		return
	}

	if !s.waitStreamReady(appName, streamid, "rtsp") {
		nazalog.Errorf("stream not ready after waiting. app=%s, stream=%s", appName, streamid)
		c.Status(http.StatusNotFound)
		return
	}

	pc, err := newPeerConnection(s.config.ICEHostNATToIPs, s.udpMux, s.tcpMux)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	jessibucaSession := NewJessibucaSession(appName, streamid, s.config.WriteChanSize, pc, s.lalServer)
	if jessibucaSession == nil {
		c.Status(http.StatusInternalServerError)
		pc.Close()
		return
	}

	c.Header("Location", fmt.Sprintf("jessibucaflv/%s", jessibucaSession.subscriberId))

	sdp := jessibucaSession.GetAnswerSDP(string(body))
	if sdp == "" {
		c.Status(http.StatusInternalServerError)
		jessibucaSession.Close()
		return
	}

	go jessibucaSession.Run()

	c.Data(http.StatusCreated, "application/sdp", []byte(sdp))
}

// ServeWHEPPlayPage 返回内嵌播放页（与 topsmedia/pkg/httpflv handleWHEPPage + buildWHEPPage 对齐）。规范地址：GET /webrtc/whep?streamid=...
func (s *RtcServer) ServeWHEPPlayPage(c *gin.Context) {
	if c.Request.URL.Query().Get("streamid") == "" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusBadRequest, "<!doctype html><meta charset=utf-8><title>WHEP</title><p>缺少查询参数 <code>streamid</code>。示例：<code>/webrtc/whep?streamid=test110</code> 或带 <code>app_name</code>：<code>/webrtc/whep?streamid=live/test110&amp;app_name=live</code></p>")
		return
	}
	// 与 httpflv.handleWHEPPage 响应头一致（Gin 由框架管理 Connection，不设 close）
	c.Header("Cache-Control", "no-store")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
	c.Header("Access-Control-Expose-Headers", "Location")
	c.Header("Accept-Post", "application/sdp")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(buildWHEPPlayHTML()))
}

// buildWHEPPlayHTML serves a diagnostic WHEP player that also handles remote
// tracks with no MediaStream association (RTCTrackEvent.streams may be empty).
func buildWHEPPlayHTML() string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>WHEP Player</title>
<style>
html,body{width:100%;height:100%;margin:0;overflow:hidden}
body{background:#000;color:#e2e8f0;font:14px/1.4 system-ui}
main{position:fixed;inset:0;display:flex;align-items:center;justify-content:center;overflow:hidden}
video{display:block;width:100%;height:100%;object-fit:contain;background:#000}
pre{display:none;position:absolute;bottom:16px;left:16px;right:16px;z-index:1;max-height:35vh;overflow:auto;white-space:pre-wrap;background:#111827;padding:12px;border-radius:12px;min-height:72px}
</style>
</head>
<body>
<main>
<video id="video" autoplay playsinline controls muted></video>
<pre id="status">Preparing WebRTC playback...</pre>
</main>
<script>
(async () => {
  const video = document.getElementById('video');
  const status = document.getElementById('status');
  // Default: video-only player. Append &debug=1 to reveal live diagnostics.
  if (new URLSearchParams(location.search).get('debug') === '1') status.style.display = 'block';
  const pc = new RTCPeerConnection();
  const media = new MediaStream();

  function updateStatus(detail = '') {
    const parts = [
      'Peer: ' + pc.connectionState,
      'ICE: ' + pc.iceConnectionState,
      'Video track: ' + (media.getVideoTracks().length ? 'attached' : 'waiting'),
      'Video size: ' + video.videoWidth + 'x' + video.videoHeight
    ];
    if (detail) parts.push(detail);
    status.textContent = parts.join('\n');
  }

  function playVideo() {
    if (!media.getVideoTracks().length) return;
    video.play().catch(err => updateStatus('video.play(): ' + err.message));
  }

  function attachTrack(track) {
    if (!track || media.getTracks().includes(track)) return;
    media.addTrack(track);
    // A remote RTCTrackEvent may have streams=[], so always use our own
    // MediaStream. Never depend on event.streams[0] existing.
    if (video.srcObject !== media) video.srcObject = media;
    track.addEventListener('unmute', playVideo);
    if (track.kind === 'video') playVideo();
    updateStatus('Received ' + track.kind + ' track');
  }

  pc.ontrack = event => {
    if (event.streams && event.streams[0]) {
      for (const track of event.streams[0].getTracks()) attachTrack(track);
    }
    attachTrack(event.track);
  };

  // Also handle browsers which expose the receiver track before firing
  // the corresponding ontrack callback.
  function attachReceiverTracks() {
    for (const receiver of pc.getReceivers()) {
      if (receiver.track && receiver.track.readyState === 'live') {
        attachTrack(receiver.track);
      }
    }
  }

  pc.onconnectionstatechange = () => updateStatus();
  pc.oniceconnectionstatechange = () => updateStatus();
  video.onloadedmetadata = playVideo;
  video.onplaying = () => updateStatus('Video is playing');
  video.onerror = () => updateStatus('Video element error');
  pc.addTransceiver('video', {direction: 'recvonly'});
  pc.addTransceiver('audio', {direction: 'recvonly'});

  async function waitForICEGathering() {
    if (pc.iceGatheringState === 'complete') return;
    await new Promise(resolve => {
      const complete = () => {
        if (pc.iceGatheringState !== 'complete') return;
        clearTimeout(timeout);
        pc.removeEventListener('icegatheringstatechange', complete);
        resolve();
      };
      const timeout = setTimeout(() => {
        pc.removeEventListener('icegatheringstatechange', complete);
        resolve();
      }, 5000);
      pc.addEventListener('icegatheringstatechange', complete);
    });
  }

  try {
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    await waitForICEGathering();
    const response = await fetch(location.href, {
      method: 'POST',
      headers: {'Content-Type': 'application/sdp'},
      body: pc.localDescription.sdp
    });
    if (!response.ok) throw new Error('WHEP HTTP ' + response.status + ': ' + await response.text());
    const answer = await response.text();
    await pc.setRemoteDescription({type: 'answer', sdp: answer});
    attachReceiverTracks();
    updateStatus('SDP negotiated; waiting for media');
    setInterval(async () => {
      attachReceiverTracks();
      try {
        const stats = await pc.getStats();
        let bytes = 0;
        let frames = 0;
        stats.forEach(item => {
          if (item.type === 'inbound-rtp' && (item.kind === 'video' || item.mediaType === 'video')) {
            bytes += item.bytesReceived || 0;
            frames += item.framesDecoded || 0;
          }
        });
        updateStatus('Video bytes: ' + bytes + ' | decoded frames: ' + frames);
      } catch (err) {
        updateStatus('Stats unavailable: ' + err.message);
      }
    }, 2000);
  } catch (err) {
    status.style.display = 'block';
    updateStatus('WHEP failed: ' + err.message);
    pc.close();
  }
  window.addEventListener('pagehide', () => pc.close(), {once: true});
})();
</script>
</body>
</html>`
}

func (s *RtcServer) HandleWHEP(c *gin.Context) {
	streamid := c.Request.URL.Query().Get("streamid")
	if streamid == "" {
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeSubscriber(c, "whep", streamid) {
		return
	}
	appName := c.Request.URL.Query().Get("app_name")

	body, err := c.GetRawData()
	if err != nil {
		nazalog.Error(err)
		c.Status(http.StatusBadRequest)
		return
	}

	if len(body) == 0 {
		nazalog.Error("invalid body")
		c.Status(http.StatusNoContent)
		return
	}

	if !s.waitStreamReady(appName, streamid, "whep") {
		nazalog.Errorf("stream not ready after waiting. app=%s, stream=%s", appName, streamid)
		c.Status(http.StatusNotFound)
		return
	}

	pc, err := newPeerConnection(s.config.ICEHostNATToIPs, s.udpMux, s.tcpMux)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	whepsession := NewWhepSession(appName, streamid, s.config.WriteChanSize, pc, s.lalServer)
	if whepsession == nil {
		c.Status(http.StatusInternalServerError)
		pc.Close()
		return
	}

	location := "/webrtc/whep/" + whepsession.subscriberId
	if token := c.Request.URL.Query().Get("token"); token != "" {
		location += "?token=" + url.QueryEscape(token)
	}
	c.Header("Location", location)
	c.Header("Access-Control-Expose-Headers", "Location")

	sdp := whepsession.GetAnswerSDP(string(body))
	if sdp == "" {
		c.Status(http.StatusInternalServerError)
		whepsession.Close()
		return
	}

	whepsession.onRunEnd = func() { s.whepSessions.Delete(whepsession.subscriberId) }
	s.whepSessions.Store(whepsession.subscriberId, whepsession)
	go whepsession.Run()

	c.Data(http.StatusCreated, "application/sdp", []byte(sdp))
}


// HandleWHEPDelete terminates the actual session created by a WHEP POST.
// The session-specific Location is a random UUID rather than a stream ID;
// enforce the same stream token policy as when the session was created.
func (s *RtcServer) HandleWHEPDelete(c *gin.Context) {
	id := c.Param("sessionid")
	value, exists := s.whepSessions.Load(id)
	if !exists {
		c.Status(http.StatusNotFound)
		return
	}
	session := value.(*whepSession)
	if !s.authorizeSubscriber(c, "whep", session.streamID) {
		return
	}
	value, exists = s.whepSessions.LoadAndDelete(id)
	if !exists {
		c.Status(http.StatusNotFound)
		return
	}
	value.(*whepSession).Close()
	c.Status(http.StatusNoContent)
}

// HandleZlmWebrtcPlay ZLM 兼容 WebRTC 播放，返回 SDP answer
// 为什么独立方法：ZLM 信令格式为 JSON {"code":0,"sdp":"..."}，与 WHEP 纯 SDP 不同
func (s *RtcServer) HandleZlmWebrtcPlay(app, stream, token, offer string) (string, error) {
	if s.subscriberAuthorizer != nil {
		if err := s.subscriberAuthorizer("zlm-webrtc", stream, token); err != nil {
			return "", fmt.Errorf("unauthorized subscriber: %w", err)
		}
	}
	if !s.waitStreamReady(app, stream, "rtsp") {
		return "", fmt.Errorf("stream not found: %s/%s", app, stream)
	}

	pc, err := newPeerConnection(s.config.ICEHostNATToIPs, s.udpMux, s.tcpMux)
	if err != nil {
		return "", fmt.Errorf("create peer connection: %w", err)
	}

	session := NewWhepSession(app, stream, s.config.WriteChanSize, pc, s.lalServer)
	if session == nil {
		pc.Close()
		return "", fmt.Errorf("create session failed: %s/%s", app, stream)
	}

	sdp := session.GetAnswerSDP(offer)
	if sdp == "" {
		session.Close()
		return "", fmt.Errorf("generate answer sdp failed")
	}

	go session.Run()
	return sdp, nil
}
