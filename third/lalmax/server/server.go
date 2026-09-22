package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/q191201771/lalmax/srt"

	"github.com/q191201771/lalmax/rtc"

	"github.com/q191201771/lalmax/gb28181/rtppub"
	"github.com/q191201771/lalmax/udpts"

	maxlogic "github.com/q191201771/lalmax/logic"

	httpfmp4 "github.com/q191201771/lalmax/fmp4/http-fmp4"

	"github.com/q191201771/lalmax/fmp4/hls"

	config "github.com/q191201771/lalmax/config"

	"github.com/gin-gonic/gin"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/naza/pkg/nazalog"
)

type LalMaxServer struct {
	lalsvr      logic.ILalServer
	conf        *config.Config
	stats       *maxlogic.StatAggregator
	notifyHub   *HttpNotify
	srtsvr      *srt.SrtServer
	rtcsvr      *rtc.RtcServer
	router      *gin.Engine
	routerTls   *gin.Engine
	httpServer  *http.Server
	httpsServer *http.Server
	httpfmp4svr *httpfmp4.HttpFmp4Server
	hlssvr      *hls.HlsServer
	rtpPubMgr   *rtppub.Manager
	udptsMgr    *udpts.Manager
	recorder    *ffmpegRecorder

	mu       sync.Mutex
	started  bool
	ready    bool
	cancel   context.CancelFunc
	runDone  chan error
	hookOnce sync.Once
}

// LalMaxServerOption allows customizing LalMaxServer behavior.
type LalMaxServerOption func(*lalMaxServerOptions)

type lalMaxServerOptions struct {
	authentication logic.IAuthentication
}

// WithAuthentication injects a custom IAuthentication into the underlying lal server.
// This allows rejecting pub/sub sessions before they are created (e.g. for banning streams).
func WithAuthentication(auth logic.IAuthentication) LalMaxServerOption {
	return func(o *lalMaxServerOptions) {
		o.authentication = auth
	}
}

// AddCustomizePubSession registers a custom publish session for feeding frames directly into lal.
// Returns an ICustomizePubSessionContext that can be used to FeedAvPacket/FeedRtmpMsg.
func (s *LalMaxServer) AddCustomizePubSession(streamName string) (logic.ICustomizePubSessionContext, error) {
	return s.lalsvr.AddCustomizePubSession(streamName)
}

// DelCustomizePubSession removes a custom publish session.
func (s *LalMaxServer) DelCustomizePubSession(ctx logic.ICustomizePubSessionContext) {
	s.lalsvr.DelCustomizePubSession(ctx)
}

func NewLalMaxServer(conf *config.Config, opts ...LalMaxServerOption) (*LalMaxServer, error) {
	var serverOpts lalMaxServerOptions
	for _, opt := range opts {
		opt(&serverOpts)
	}

	notifyHub := NewHttpNotify(conf.HttpNotifyConfig, conf.ServerId)
	lalsvr := logic.NewLalServer(func(option *logic.Option) {
		if len(conf.LalRawContent) != 0 {
			option.ConfRawContent = conf.LalRawContent
		} else {
			option.ConfFilename = conf.LalSvrConfigPath
		}
		option.NotifyHandler = notifyHub
		if serverOpts.authentication != nil {
			option.Authentication = serverOpts.authentication
		}
	})

	maxsvr := &LalMaxServer{
		lalsvr:    lalsvr,
		conf:      conf,
		stats:     maxlogic.NewStatAggregator(maxlogic.GetGroupManagerInstance()),
		notifyHub: notifyHub,
		rtpPubMgr: rtppub.NewManager(lalsvr, conf.GB28181Config.MediaConfig),
		udptsMgr:  udpts.NewManager(lalsvr),
		recorder:  newFfmpegRecorder(""),
	}

	// 注入 sub 数量查询，用于 on_stream_none_reader 判断
	notifyHub.SetSubCountFn(func(streamName string) int {
		for _, g := range lalsvr.StatAllGroup() {
			if g.StreamName == streamName {
				return len(g.StatSubs)
			}
		}
		return 0
	})

	if conf.SrtConfig.Enable {
		maxsvr.srtsvr = srt.NewSrtServer(conf.SrtConfig.Addr, lalsvr, func(option *srt.SrtOption) {
			option.Latency = 300
			option.PeerLatency = 300
		})
	}

	if conf.RtcConfig.Enable {
		var err error
		maxsvr.rtcsvr, err = rtc.NewRtcServer(conf.RtcConfig, lalsvr)
		if err != nil {
			nazalog.Error("create rtc svr failed, err:", err)
			return nil, err
		}
		maxsvr.rtcsvr.SetStreamNotFoundFn(func(app, stream, schema string) {
			notifyHub.NotifyStreamNotFound(ZlmOnStreamNotFoundPayload{
				MediaServerID: conf.ServerId,
				App:           app,
				Stream:        stream,
				Schema:        schema,
				Vhost:         "__defaultVhost__",
			})
		})
	}

	if conf.Fmp4Config.Http.Enable {
		maxsvr.httpfmp4svr = httpfmp4.NewHttpFmp4Server()
	}

	if conf.Fmp4Config.Hls.Enable {
		maxsvr.hlssvr = hls.NewHlsServer(conf.Fmp4Config.Hls)
	}

	maxsvr.router = gin.Default()
	maxsvr.InitRouter(maxsvr.router)
	if conf.HttpConfig.EnableHttps {
		maxsvr.routerTls = gin.Default()
		maxsvr.InitRouter(maxsvr.routerTls)
	}

	return maxsvr, nil
}

func (s *LalMaxServer) initHookSession() {
	s.hookOnce.Do(func() {
		s.lalsvr.WithOnHookSession(func(uniqueKey string, streamName string) logic.ICustomizeHookSessionContext {
			key := maxlogic.StreamKeyFromStreamName(streamName)
			group, created := maxlogic.GetGroupManagerInstance().GetOrCreateGroupByStreamName(uniqueKey, streamName, s.hlssvr, s.conf.LogicConfig.GopCacheNum, s.conf.LogicConfig.SingleGopMaxFrameNum)
			group.BindActiveHook(key, func(activeKey maxlogic.StreamKey) {
				if s.notifyHub == nil || !activeKey.Valid() {
					return
				}
				s.notifyHub.NotifyStreamActive(HookGroupInfo{
					AppName:    activeKey.AppName,
					StreamName: activeKey.StreamName,
				})
			})
			group.BindStopHook(key, func(stopKey maxlogic.StreamKey) {
				if s.notifyHub == nil || !stopKey.Valid() {
					return
				}
				s.notifyHub.NotifyGroupStop(HookGroupInfo{
					AppName:    stopKey.AppName,
					StreamName: stopKey.StreamName,
				})
			})
			if created && s.notifyHub != nil {
				s.notifyHub.NotifyGroupStart(HookGroupInfo{
					AppName:    key.AppName,
					StreamName: key.StreamName,
				})
			}
			return group
		})
	})
}

func (s *LalMaxServer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.started = true
	s.ready = true
	s.cancel = cancel
	s.runDone = make(chan error, 1)
	s.mu.Unlock()

	s.initHookSession()

	if s.srtsvr != nil {
		go s.srtsvr.Run(runCtx)
	}

	go s.runPeriodicUpdate(runCtx)
	go s.runPeriodicKeepalive(runCtx)

	if s.conf.HttpConfig.ListenAddr != "" {
		s.httpServer = &http.Server{Addr: s.conf.HttpConfig.ListenAddr, Handler: s.router}
		go func(server *http.Server) {
			nazalog.Infof("lalmax http listen. addr=%s", server.Addr)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				nazalog.Infof("lalmax http stop. addr=%s err=%v", server.Addr, err)
			}
		}(s.httpServer)
	}

	if s.conf.HttpConfig.EnableHttps {
		s.httpsServer = &http.Server{
			Addr:         s.conf.HttpConfig.HttpsListenAddr,
			Handler:      s.routerTls,
			TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		}
		go func(server *http.Server) {
			nazalog.Infof("lalmax https listen. addr=%s", server.Addr)
			if err := server.ListenAndServeTLS(s.conf.HttpConfig.HttpsCertFile, s.conf.HttpConfig.HttpsKeyFile); err != nil && err != http.ErrServerClosed {
				nazalog.Infof("lalmax https stop. addr=%s err=%v", server.Addr, err)
			}
		}(s.httpsServer)
	}

	go func() {
		err := s.lalsvr.RunLoop()
		s.mu.Lock()
		s.ready = false
		s.mu.Unlock()
		s.runDone <- err
	}()

	return nil
}

func (s *LalMaxServer) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	runDone := s.runDone
	httpServer := s.httpServer
	httpsServer := s.httpsServer
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if httpServer != nil {
		if err := httpServer.Shutdown(ctx); err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	if httpsServer != nil {
		if err := httpsServer.Shutdown(ctx); err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	if s.srtsvr != nil {
		s.srtsvr.Shutdown()
	}
	if s.rtcsvr != nil {
		_ = s.rtcsvr.Close()
	}
	if s.rtpPubMgr != nil {
		s.rtpPubMgr.StopAll()
	}
	if s.udptsMgr != nil {
		s.udptsMgr.StopAll()
	}

	disposeDone := make(chan struct{})
	go func() {
		s.lalsvr.Dispose()
		close(disposeDone)
	}()
	select {
	case <-disposeDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runDone:
		return nil
	}
}

// Close stops listeners immediately. Unlike Shutdown it does not wait for
// in-flight HTTP requests or lal Dispose, so protocol restarts are not
// blocked by live viewers.
func (s *LalMaxServer) Close() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	httpServer := s.httpServer
	httpsServer := s.httpsServer
	s.started = false
	s.ready = false
	s.httpServer = nil
	s.httpsServer = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if httpServer != nil {
		_ = httpServer.Close()
	}
	if httpsServer != nil {
		_ = httpsServer.Close()
	}
	if s.srtsvr != nil {
		s.srtsvr.Shutdown()
	}
	if s.rtcsvr != nil {
		_ = s.rtcsvr.Close()
	}
	if s.rtpPubMgr != nil {
		s.rtpPubMgr.StopAll()
	}
	if s.udptsMgr != nil {
		s.udptsMgr.StopAll()
	}
	go s.lalsvr.Dispose()
}

func (s *LalMaxServer) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

// HTTPHandler returns the in-process gin handler for lalmax HTTP play/API routes.
// Callers can ServeHTTP without looping back through TCP.
func (s *LalMaxServer) HTTPHandler() http.Handler {
	if s == nil {
		return nil
	}
	return s.router
}

func (s *LalMaxServer) Wait() error {
	s.mu.Lock()
	runDone := s.runDone
	s.mu.Unlock()
	if runDone == nil {
		return nil
	}
	return <-runDone
}

func (s *LalMaxServer) Run() (err error) {
	if err := s.Start(context.Background()); err != nil {
		return err
	}
	return s.Wait()
}

func (s *LalMaxServer) runPeriodicUpdate(ctx context.Context) {
	if s == nil || s.notifyHub == nil || s.lalsvr == nil {
		return
	}

	intervalSec := s.conf.HttpNotifyConfig.UpdateIntervalSec
	if intervalSec <= 0 {
		return
	}

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.notifyHub.NotifyUpdate(base.UpdateInfo{
				Groups: s.lalsvr.StatAllGroup(),
			})
		}
	}
}

// runPeriodicKeepalive ZLM 兼容：定时发送 on_server_keepalive
func (s *LalMaxServer) runPeriodicKeepalive(ctx context.Context) {
	if s == nil || s.notifyHub == nil {
		return
	}

	intervalSec := s.conf.HttpNotifyConfig.KeepaliveIntervalSec
	if intervalSec <= 0 {
		return
	}

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.notifyHub.NotifyServerKeepalive()
		}
	}
}

func (s *LalMaxServer) HookHub() *HttpNotify {
	return s.notifyHub
}

func (s *LalMaxServer) RegisterHookPlugin(plugin HookPlugin, options HookPluginOptions) (func(), error) {
	if s == nil || s.notifyHub == nil {
		return nil, fmt.Errorf("hook hub not initialized")
	}
	return s.notifyHub.RegisterPlugin(plugin, options)
}

// SetHlsEnabled toggles TS-HLS (lal) and LL-HLS (fmp4) generation at runtime.
// Pull/pub sessions used for recording are not interrupted.
func (s *LalMaxServer) SetHlsEnabled(enable bool) {
	if s == nil {
		return
	}
	if s.lalsvr != nil {
		s.lalsvr.SetHlsEnabled(enable)
	}
	s.conf.Fmp4Config.Hls.Enable = enable
	if s.hlssvr != nil {
		s.hlssvr.SetEnabled(enable)
	}
}

// SetHlsOnDemand toggles on-demand HLS slicing for TS-HLS and LL-HLS.
func (s *LalMaxServer) SetHlsOnDemand(onDemand bool, idleTimeoutMs int) {
	if s == nil {
		return
	}
	if s.lalsvr != nil {
		s.lalsvr.SetHlsOnDemand(onDemand, idleTimeoutMs)
	}
	s.conf.Fmp4Config.Hls.OnDemand = onDemand
	if idleTimeoutMs > 0 {
		s.conf.Fmp4Config.Hls.OnDemandIdleTimeoutMs = idleTimeoutMs
	}
	if s.hlssvr != nil {
		s.hlssvr.SetOnDemand(onDemand, idleTimeoutMs)
	}
}
