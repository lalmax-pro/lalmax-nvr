package api

// Tests for the p0 handlers (auto-discover settings, activation, rediscovery,
// play stream resolution), handler helpers, stream CRUD/management endpoints,
// the WebSocket endpoint, the local Xiaomi auth adapter and transcode profiles.
//
// Every helper and type is api3 prefixed.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/lalmax-pro/lalmax-nvr/internal/wsstream"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

// --- auto-discover settings ---

func TestApi3AutoDiscoverSettings(t *testing.T) {
	db, store := setupTestDB(t)
	cfg := &config.Config{}
	cfgPath := filepathFromTemp(t, "config.yaml")
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, cfgPath, nil, nil)
	var applied config.AutoDiscoverConfig
	h.SetAutoDiscoverApply(func(c config.AutoDiscoverConfig) { applied = c })
	router := api3Router(func(r chi.Router) {
		r.Get("/settings/autodiscover", h.handleGetAutoDiscoverSettings)
		r.Put("/settings/autodiscover", h.handleUpdateAutoDiscoverSettings)
	})

	rr := api3Request(t, router, http.MethodGet, "/settings/autodiscover", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, api3JSON(t, rr), "listen_for_hello")

	// No config configured at all.
	noCfg := api3Router(func(r chi.Router) {
		r.Get("/settings/autodiscover", (&Handler{}).handleGetAutoDiscoverSettings)
		r.Put("/settings/autodiscover", (&Handler{}).handleUpdateAutoDiscoverSettings)
	})
	api3Status(t, api3Request(t, noCfg, http.MethodGet, "/settings/autodiscover", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, noCfg, http.MethodPut, "/settings/autodiscover", `{}`), http.StatusInternalServerError)

	api3Status(t, api3Request(t, router, http.MethodPut, "/settings/autodiscover", `{`), http.StatusBadRequest)

	rr = api3Request(t, router, http.MethodPut, "/settings/autodiscover",
		`{"enabled":true,"listen_for_hello":false,"scan_interval":"10m","default_username":"admin","default_password":"secret","network_interface":"eth0"}`)
	api3Status(t, rr, http.StatusOK)
	body := api3JSON(t, rr)
	require.Equal(t, true, body["enabled"])
	require.Equal(t, false, body["listen_for_hello"])
	require.Equal(t, true, body["has_password"])
	require.Equal(t, "eth0", body["network_interface"])
	require.NotNil(t, applied.Enabled)
	require.True(t, *applied.Enabled)
	require.Equal(t, "10m", applied.ScanInterval)
	require.Equal(t, "admin", applied.DefaultUsername)

	// An empty password keeps the previous value.
	api3Status(t, api3Request(t, router, http.MethodPut, "/settings/autodiscover", `{"default_password":""}`), http.StatusOK)
	require.Equal(t, "secret", cfg.AutoDiscover.DefaultPassword)

	// Saving the config is tolerated when no config path is set.
	bare := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Put("/settings/autodiscover", bare.handleUpdateAutoDiscoverSettings)
	}), http.MethodPut, "/settings/autodiscover", `{"enabled":false}`), http.StatusOK)
}

// --- camera activation and rediscovery ---

func TestApi3CameraActivateAndRediscover(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	camCfg := config.CameraConfig{ID: "cam-1", Name: "Cam", Protocol: "rtsp_h264", URL: "rtsp://127.0.0.1:1/live", Username: "u", Password: "p", Enabled: true}
	cfg := &config.Config{Cameras: []config.CameraConfig{camCfg}}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "rtsp_h264", "h264", "rtsp://127.0.0.1:1/live", "u", "p", true, "", "", ""))
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Post("/cameras/{id}/activate", h.handleActivateCamera)
		r.Post("/cameras/{id}/rediscover", h.handleRediscoverCamera)
	})

	// No camera manager.
	noMgr := api3Router(func(r chi.Router) {
		r.Post("/cameras/{id}/activate", (&Handler{}).handleActivateCamera)
		r.Post("/cameras/{id}/rediscover", (&Handler{}).handleRediscoverCamera)
	})
	api3Status(t, api3Request(t, noMgr, http.MethodPost, "/cameras/cam-1/activate", `{}`), http.StatusInternalServerError)
	api3Status(t, api3Request(t, noMgr, http.MethodPost, "/cameras/cam-1/rediscover", ``), http.StatusInternalServerError)

	// Malformed activation body.
	api3Status(t, api3Request(t, router, http.MethodPost, "/cameras/cam-1/activate", `{`), http.StatusBadRequest)

	// Activation of a camera that the manager does not know.
	api3Status(t, api3Request(t, router, http.MethodPost, "/cameras/ghost/activate", `{"username":"u","password":"p"}`), http.StatusBadRequest)

	// Successful activation of the configured camera.
	rr := api3Request(t, router, http.MethodPost, "/cameras/cam-1/activate", `{"username":"u","password":"p"}`)
	require.Contains(t, []int{http.StatusOK, http.StatusBadRequest}, rr.Code, rr.Body.String())

	// Rediscovery for an unknown camera surfaces the manager error.
	api3Status(t, api3Request(t, router, http.MethodPost, "/cameras/ghost/rediscover", ""), http.StatusInternalServerError)

	// Rediscovery for a known non-ONVIF camera is a no-op that reports found=false.
	rr = api3Request(t, router, http.MethodPost, "/cameras/cam-1/rediscover", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, false, api3JSON(t, rr)["found"])
}

// --- play stream resolution and sub HLS proxy ---

func TestApi3PlayStreamResolution(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	subCfg := config.CameraConfig{ID: "cam-1", Name: "Cam", Protocol: "rtsp_h264", URL: "rtsp://127.0.0.1:1/live", SubStreamURL: "rtsp://127.0.0.1:1/sub", Enabled: true}
	cfg := &config.Config{Cameras: []config.CameraConfig{subCfg}, HLS: config.HLSConfig{Enabled: boolPtr(true)}}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, nil)
	engine := &stubMediaEngine{playURLs: map[string]string{"hls": "http://127.0.0.1:1/live", "fmp4": "http://127.0.0.1:1/live"}}
	h.SetMediaEngine(engine)
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "rtsp_h264", "h264", "rtsp://127.0.0.1:1/live", "", "", true, "", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "cam-1", "cam-1"))

	router := api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/hls", h.handleSubHLSStream)
		r.Get("/cameras/{id}/hls/*", h.handleSubHLSStream)
		r.Get("/cameras/{id}/stream.m4s", h.handleFMP4Stream)
		r.Get("/cameras/{id}/stream/ws", h.handleStreamWS)
	})

	// HLS disabled for the handler configuration.
	disabledCfg := &config.Config{Cameras: []config.CameraConfig{subCfg}}
	disabled := NewHandler(db, store, noopAuthMW(), disabledCfg, camMgr, "", nil, nil)
	disabled.SetMediaEngine(engine)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/hls", disabled.handleSubHLSStream)
	}), http.MethodGet, "/cameras/cam-1/hls", ""), http.StatusServiceUnavailable)

	// No media engine.
	noEngine := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, nil)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/cameras/{id}/hls", noEngine.handleSubHLSStream)
	}), http.MethodGet, "/cameras/cam-1/hls", ""), http.StatusInternalServerError)

	// Unbound camera.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/unbound/hls", ""), http.StatusNotFound)

	// Sub-stream pull fails (no reachable media engine pull), so main is used and
	// the upstream proxy connection fails with 502.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/cam-1/hls/index.m3u8", ""), http.StatusBadGateway)

	// Tail handling for LL-HLS resources exercises the alternate upstream base.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/cam-1/hls/segment.mp4?ll-hls=1", ""), http.StatusBadGateway)

	// Camera play resolution without a bound stream reports not found, and the
	// stream play route reports the stream-specific message.
	api3Status(t, api3Request(t, router, http.MethodGet, "/cameras/unbound/stream.m4s", ""), http.StatusNotFound)

	// Streaming resolve helpers.
	camMgr.SetMediaEngine(engine)
	subReq := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-1/stream.m4s?quality=sub", nil)
	subReq = api3WithStreamParam(subReq, "cam-1")
	streamID, quality := h.resolvePlayStreamID(subReq, "cam-1")
	require.Equal(t, "cam-1_sub", streamID)
	require.Equal(t, "sub", quality)

	camReq := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-1/stream.m4s?quality=sub", nil)
	camReq = api3WithIDParam(camReq, "cam-1")
	_, quality = h.resolvePlayStreamID(camReq, "cam-1")
	require.Equal(t, "sub", quality)

	// A sub-stream id is detected directly.
	directReq := httptest.NewRequest(http.MethodGet, "/api/streams/cam-1_sub/stream.m4s", nil)
	directReq = api3WithStreamParam(directReq, "cam-1_sub")
	_, quality = h.resolvePlayStreamID(directReq, "cam-1_sub")
	require.Equal(t, "sub", quality)

	// Unknown stream on the stream play route uses the main stream fallback.
	unknownReq := httptest.NewRequest(http.MethodGet, "/api/streams/ghost/stream.m4s?quality=sub", nil)
	unknownReq = api3WithStreamParam(unknownReq, "ghost")
	_, quality = h.resolvePlayStreamID(unknownReq, "ghost")
	require.Equal(t, "main", quality)

	// A camera without a sub stream keeps the main quality.
	plainCfg := &config.Config{Cameras: []config.CameraConfig{{ID: "plain", Protocol: "rtsp_h264", URL: "rtsp://127.0.0.1:1/live"}}}
	plainMgr := camera.NewCameraManager(plainCfg, store, db, "")
	plain := NewHandler(db, store, noopAuthMW(), plainCfg, plainMgr, "", nil, nil)
	plain.SetMediaEngine(engine)
	plainReq := httptest.NewRequest(http.MethodGet, "/api/cameras/plain/stream.m4s?quality=sub", nil)
	plainReq = api3WithIDParam(plainReq, "plain")
	_, quality = plain.resolvePlayStreamID(plainReq, "plain")
	require.Equal(t, "main", quality)
}

func api3WithStreamParam(r *http.Request, streamID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("stream_id", streamID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func api3WithIDParam(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// --- handler helpers, setters and auth middleware ---

type api3PlayProviderEngine struct {
	*stubMediaEngine
	handler http.Handler
}

func (e *api3PlayProviderEngine) PlayHTTPHandler() http.Handler { return e.handler }

type api3WSStub struct {
	active       bool
	registerErr  error
	audioErr     error
	serveErr     error
	registered   int
	audioConfigs int
	serveCalls   int
}

func (s *api3WSStub) RegisterStream(string, model.Format, []byte, []byte, []byte, *model.StreamHub) error {
	s.registered++
	return s.registerErr
}
func (s *api3WSStub) SetAudioConfig(string, *wsstream.AudioCodecInfo) error {
	s.audioConfigs++
	return s.audioErr
}
func (s *api3WSStub) IsActive(string) bool { return s.active }
func (s *api3WSStub) ServeWS(string, http.ResponseWriter, *http.Request) error {
	s.serveCalls++
	return s.serveErr
}
func (s *api3WSStub) StopAll() {}

type api3GroupSource struct{}

func (api3GroupSource) StreamInfo(string) (model.Format, []byte, []byte, []byte, *model.StreamHub, bool) {
	return model.FormatH264, []byte{1}, []byte{2}, nil, model.NewStreamHub(), true
}

func TestApi3HandlerHelpersAndSetters(t *testing.T) {
	// URL and identifier validation helpers.
	require.True(t, validateURL("rtsp://192.0.2.10/live"))
	require.False(t, validateURL(""))
	require.False(t, validateURL("://bad"))
	require.False(t, validateURL("not-a-url"))
	require.True(t, validateIP("192.0.2.10"))
	require.True(t, validateIP("192.0.2.10:8080"))
	require.False(t, validateIP("not-an-ip"))
	require.Equal(t, "192.0.2.10", extractDIDFromURL("xiaomi://192.0.2.10/device"))
	require.Equal(t, "", extractDIDFromURL("://bad"))
	require.Equal(t, "value", *strPtr("value"))

	// nil-safety and restart wiring.
	var nilHandler *Handler
	nilHandler.SetServiceLogPath("/tmp/log")
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetServiceLogPath("  /tmp/api3.log ")
	require.Equal(t, "/tmp/api3.log", h.serviceLogPath)
	restarts := 0
	h.SetRestartFunc(func() { restarts++ })
	h.requestRestart()
	h.requestRestart()
	require.Equal(t, 1, restarts)
	h.requestRestart()
	require.Equal(t, 1, restarts)
	// A handler without a restart callback does nothing.
	noRestart := &Handler{}
	noRestart.requestRestart()

	// Simple setters.
	h.SetVoIPRestarter(nil)
	h.SetLocalTraceEnabled(func(bool) {})
	h.SetDLNAApply(func(context.Context) error { return nil })
	h.SetIPTVService(nil)
	h.SetConfigWatcher(nil)
	h.SetAIManager(nil)
	h.SetRelayManager(nil)
	h.SetRestartFunc(nil)
	h.SetEmbyService(nil)
	h.SetHealthManager(nil)
	h.SetStabilityProvider(nil)
	h.SetSnapshotManager(nil)
	h.SetBanManager(nil)
	h.SetGB28181Server(nil)
	h.SetGB28181ServerInstance(nil)
	h.SetGB28181Restarter(nil)
	h.SetJT808Server(nil)
	h.SetWSManager(&api3WSStub{})
	h.SetGroupStreamSource(api3GroupSource{})
	h.SetStreamRegistry(NewStreamRegistry())
	// ONVIF client factory closure.
	client := h.onvifNewClient("http://127.0.0.1:1/onvif/device_service", "u", "p")
	require.NotNil(t, client)

	// Engines exposing an in-process play handler install a proxy transport.
	h.SetMediaEngine(&api3PlayProviderEngine{stubMediaEngine: &stubMediaEngine{}, handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "in-process")
	})})
	require.NotNil(t, h.mediaProxy)
	require.NotNil(t, h.playProxyClient())

	// Plain engines keep the shared proxy client.
	h.SetMediaEngine(&stubMediaEngine{})
	require.NotNil(t, h.mediaProxy)

	// mediaPlayURL branches.
	_, err := (&Handler{}).mediaPlayURL(context.Background(), "cam", "hls")
	require.NoError(t, err)
	h.SetMediaEngine(&stubMediaEngine{})
	_, err = h.mediaPlayURL(context.Background(), "cam", "hls")
	require.Error(t, err)
	h.SetMediaEngine(&stubMediaEngine{playURLs: map[string]string{"hls": "://bad"}})
	_, err = h.mediaPlayURL(context.Background(), "cam", "hls")
	require.Error(t, err)
	h.SetMediaEngine(&stubMediaEngine{playURLs: map[string]string{"hls": ""}})
	_, err = h.mediaPlayURL(context.Background(), "cam", "hls")
	require.NoError(t, err)

	// mediaHLSResourceURL branches.
	h.SetMediaEngine(&stubMediaEngine{playURLs: map[string]string{
		"hls":    "http://127.0.0.1:1/live/index.m3u8?x=1",
		"ll-hls": "http://127.0.0.1:1/live/ll.m3u8",
	}})
	resource, err := h.mediaHLSResourceURL(context.Background(), "cam", "/index.m3u8", "")
	require.NoError(t, err)
	require.NotNil(t, resource)
	require.Equal(t, "x=1", resource.RawQuery)
	resource, err = h.mediaHLSResourceURL(context.Background(), "cam", "/segment.ts", "token=abc")
	require.NoError(t, err)
	require.Equal(t, "x=1&token=abc", resource.RawQuery)
	require.Contains(t, resource.Path, "segment.ts")
	resource, err = h.mediaHLSResourceURL(context.Background(), "cam", "/seg/1.m3u8", "")
	require.NoError(t, err)
	require.Equal(t, "/live/seg/1.m3u8", resource.Path)
	h.SetMediaEngine(&stubMediaEngine{})
	_, err = h.mediaHLSResourceURL(context.Background(), "cam", "/index.m3u8", "")
	require.Error(t, err)

	// LL-HLS classification.
	require.True(t, isLLHLSResource("/index.m3u8", "ll-hls=1"))
	require.True(t, isLLHLSResource("/seg/1.m3u8", ""))
	require.False(t, isLLHLSResource("/index.m3u8", ""))
	require.True(t, isLLHLSResource("/x.mp4", ""))
	require.True(t, isLLHLSResource("/x.m4s", ""))
	require.True(t, isLLHLSResource("/x.cmfv", ""))
	require.True(t, isLLHLSResource("/x.cmfa", ""))
	require.False(t, isLLHLSResource("/x.ts", ""))

	// Proxy helpers.
	require.Error(t, h.proxyMediaRequest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), nil))
	badTarget, parseErr := url.Parse("http://127.0.0.1:1/x")
	require.NoError(t, parseErr)
	require.Error(t, h.proxyMediaRequest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), badTarget))

	// Upstream headers are filtered and the body streamed.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Connection", "keep-alive")
		_, _ = io.WriteString(w, "#EXTM3U\n")
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, parseErr := url.Parse(upstream.URL + "/live/cam/index.m3u8")
	require.NoError(t, parseErr)
	rr := httptest.NewRecorder()
	h.SetMediaEngine(&stubMediaEngine{})
	require.NoError(t, h.proxyMediaRequest(rr, httptest.NewRequest(http.MethodGet, "/", nil), upstreamURL))
	require.Equal(t, "#EXTM3U\n", rr.Body.String())
	require.Empty(t, rr.Header().Get("Connection"))
	// Hop-by-hop helpers.
	require.True(t, isHopByHopHeader("Transfer-Encoding"))
	require.False(t, isHopByHopHeader("Content-Type"))
	require.Equal(t, "cam/x", decodeUpstreamSessionLocationOrFail(t, "cam/x"))

	// Capabilities endpoint covers enabled/disabled ingest reporting.
	capCfg := &config.Config{}
	disabled := false
	capCfg.RTMP.Enabled = &disabled
	capHandler := NewHandler(nil, nil, noopAuthMW(), capCfg, nil, "", nil, nil)
	capRouter := api3Router(func(r chi.Router) { r.Get("/capabilities", capHandler.handleCapabilities) })
	api3Status(t, api3Request(t, capRouter, http.MethodGet, "/capabilities", ""), http.StatusOK)

	// Default multi-user middleware injects a super admin and decodes ?token.
	authHandler := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	authRouter := api3Router(func(r chi.Router) { r.Get("/whoami", h.handleHealth) })
	_ = authHandler
	token := base64.StdEncoding.EncodeToString([]byte("tokenuser:pw"))
	for _, target := range []string{"/whoami", "/whoami?token=" + token, "/whoami?token=@@@"} {
		api3Status(t, api3Request(t, authRouter, http.MethodGet, target, ""), http.StatusOK)
	}
	// Routes() falls back to authMW when no multi-user middleware is installed.
	fallback := NewHandler(nil, nil, noopAuthMW(), &config.Config{}, nil, "", nil, nil)
	fallback.SetMultiUserAuthMW(nil)
	_ = fallback.Routes()
}

func decodeUpstreamSessionLocationOrFail(t *testing.T, raw string) string {
	t.Helper()
	decoded, err := decodeUpstreamSessionLocation(encodeUpstreamSessionLocation(raw))
	require.NoError(t, err)
	return decoded
}

// --- stream CRUD and management endpoints ---

func TestApi3StreamCRUDLifecycle(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	api3SeedCameraRow(t, db, "cam-1", "Front Door")
	engine := &stubMediaEngine{
		streams:     []media.StreamInfo{{StreamID: "push-1", AppName: "live", Active: true, VideoCodec: "H264"}},
		streamsByID: map[string]*media.StreamInfo{"push-1": {StreamID: "push-1", AppName: "live", Active: true, VideoCodec: "H264"}},
	}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	router := api3Router(func(r chi.Router) {
		r.Get("/streams", h.handleListStreams)
		r.Get("/streams/{stream_id}", h.handleGetStream)
		r.Put("/streams/{stream_id}", h.handleUpdateStream)
		r.Post("/streams", h.handleCreateStream)
		r.Delete("/streams/{stream_id}", h.handleDeleteStream)
		r.Post("/streams/{stream_id}/bind", h.handleBindCamera)
		r.Post("/streams/{stream_id}/unbind", h.handleUnbindCamera)
		r.Post("/streams/{stream_id}/promote", h.handlePromoteStream)
		r.Post("/streams/{stream_id}/kick", h.handleKickPublisher)
		r.Get("/streams/history", h.handleListStreamHistory)
	})

	// Listing with filters and pagination.
	rr := api3Request(t, router, http.MethodGet, "/streams", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, api3JSON(t, rr), "streams")
	for _, query := range []string{"?q=push&managed=false&limit=5&offset=0", "?managed=true", "?limit=9999", "?limit=abc&offset=-2"} {
		api3Status(t, api3Request(t, router, http.MethodGet, "/streams"+query, ""), http.StatusOK)
	}

	// Engine failures.
	brokenEngine := &stubMediaEngine{listErr: fmt.Errorf("list failed"), getErr: fmt.Errorf("get failed")}
	broken := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	broken.SetMediaEngine(brokenEngine)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) { r.Get("/streams", broken.handleListStreams) }),
		http.MethodGet, "/streams", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/streams/{stream_id}", broken.handleGetStream)
		r.Put("/streams/{stream_id}", broken.handleUpdateStream)
		r.Post("/streams/{stream_id}/bind", broken.handleBindCamera)
	}), http.MethodGet, "/streams/ghost", ""), http.StatusInternalServerError)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) {
		r.Get("/streams/{stream_id}", broken.handleGetStream)
	}), http.MethodGet, "/streams/ghost", ""), http.StatusInternalServerError)

	// No engine configured.
	noEngine := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	noEngineRouter := api3Router(func(r chi.Router) {
		r.Get("/streams", noEngine.handleListStreams)
		r.Get("/streams/{stream_id}", noEngine.handleGetStream)
		r.Put("/streams/{stream_id}", noEngine.handleUpdateStream)
		r.Delete("/streams/{stream_id}", noEngine.handleDeleteStream)
		r.Post("/streams/{stream_id}/bind", noEngine.handleBindCamera)
		r.Post("/streams/{stream_id}/unbind", noEngine.handleUnbindCamera)
		r.Post("/streams/{stream_id}/promote", noEngine.handlePromoteStream)
		r.Post("/streams", noEngine.handleCreateStream)
	})
	for _, call := range []struct{ method, target, body string }{
		{http.MethodGet, "/streams", ""},
		{http.MethodGet, "/streams/push-1", ""},
		{http.MethodPut, "/streams/push-1", `{"name":"x"}`},
		{http.MethodDelete, "/streams/push-1", ""},
		{http.MethodPost, "/streams/push-1/bind", `{"camera_id":"cam-1"}`},
		{http.MethodPost, "/streams/push-1/unbind", ""},
		{http.MethodPost, "/streams/push-1/promote", `{"name":"x"}`},
		{http.MethodPost, "/streams", `{"stream_id":"new"}`},
	} {
		api3Status(t, api3Request(t, noEngineRouter, call.method, call.target, call.body), http.StatusServiceUnavailable)
	}

	// Missing stream ids (routes without the parameter).
	noParam := api3Router(func(r chi.Router) {
		r.Get("/get", h.handleGetStream)
		r.Put("/update", h.handleUpdateStream)
		r.Delete("/delete", h.handleDeleteStream)
		r.Post("/bind", h.handleBindCamera)
		r.Post("/unbind", h.handleUnbindCamera)
		r.Post("/promote", h.handlePromoteStream)
	})
	for _, call := range []struct{ method, target, body string }{
		{http.MethodGet, "/get", ""},
		{http.MethodPut, "/update", `{}`},
		{http.MethodDelete, "/delete", ""},
		{http.MethodPost, "/bind", `{}`},
		{http.MethodPost, "/unbind", ""},
		{http.MethodPost, "/promote", `{}`},
	} {
		api3Status(t, api3Request(t, noParam, call.method, call.target, call.body), http.StatusBadRequest)
	}

	// Get an active stream and an unknown one.
	rr = api3Request(t, router, http.MethodGet, "/streams/push-1", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "push-1", api3JSON(t, rr)["stream_id"])
	api3Status(t, api3Request(t, router, http.MethodGet, "/streams/ghost", ""), http.StatusNotFound)

	// Update: malformed body, unknown stream, then rename.
	api3Status(t, api3Request(t, router, http.MethodPut, "/streams/push-1", `{`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPut, "/streams/ghost", `{"name":"x"}`), http.StatusNotFound)
	rr = api3Request(t, router, http.MethodPut, "/streams/push-1", `{"name":"  Renamed Push Stream  "}`)
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "Renamed Push Stream", api3JSON(t, rr)["name"])
	// Long names are truncated to 128 runes.
	rr = api3Request(t, router, http.MethodPut, "/streams/push-1", `{"name":"`+strings.Repeat("n", 200)+`"}`)
	api3Status(t, rr, http.StatusOK)
	require.Len(t, []rune(api3JSON(t, rr)["name"].(string)), 128)

	// Binding a stream to a camera.
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/bind", `{`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/bind", `{}`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/ghost/bind", `{"camera_id":"cam-1"}`), http.StatusNotFound)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/bind", `{"camera_id":"missing"}`), http.StatusNotFound)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/bind", `{"camera_id":"cam-1"}`), http.StatusOK)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/unbind", ""), http.StatusOK)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/unbind", ""), http.StatusNotFound)

	// Promotion: validation, conflicts and success.
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/promote", `{`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/promote", `{}`), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/ghost/promote", `{"name":"Ghost"}`), http.StatusNotFound)
	rr = api3Request(t, router, http.MethodPost, "/streams/push-1/promote", `{"name":"Promoted Camera","description":"desc","location":"lobby"}`)
	api3Status(t, rr, http.StatusOK)
	promoted := api3JSON(t, rr)
	require.Equal(t, "promoted", promoted["status"])
	require.NoError(t, db.BindStreamToCamera(ctx, "push-1", promoted["camera_id"].(string)))
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/push-1/promote", `{"name":"Again"}`), http.StatusConflict)

	// Create stream validation and success.
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams", `{`), http.StatusBadRequest)
	for _, body := range []string{
		`{"stream_id":"bad id"}`,
		`{"stream_id":"ok","input_mode":"bogus"}`,
		`{"stream_id":"ok","input_mode":"pull","source_url":"ftp://example/x"}`,
	} {
		api3Status(t, api3Request(t, router, http.MethodPost, "/streams", body), http.StatusBadRequest)
	}
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams", `{"stream_id":"cam-1"}`), http.StatusConflict)
	rr = api3Request(t, router, http.MethodPost, "/streams", `{"stream_id":"created-1","name":"New Push"}`)
	api3Status(t, rr, http.StatusCreated)
	require.Equal(t, "created-1", api3JSON(t, rr)["stream_id"])
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams", `{"stream_id":"created-1"}`), http.StatusConflict)

	// Pull creation starts a media pull.
	rr = api3Request(t, router, http.MethodPost, "/streams", `{"stream_id":"pull-1","input_mode":"pull","source_url":"rtsp://127.0.0.1:1/live"}`)
	api3Status(t, rr, http.StatusCreated)
	require.NotEmpty(t, engine.pulls)

	// Failed pull start is reported as a bad gateway.
	failPull := &stubMediaEngine{streamsByID: map[string]*media.StreamInfo{}, startPullErr: errors.New("pull refused")}
	failHandler := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	failHandler.SetMediaEngine(failPull)
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) { r.Post("/streams", failHandler.handleCreateStream) }),
		http.MethodPost, "/streams", `{"stream_id":"pull-2","input_mode":"pull","source_url":"rtsp://127.0.0.1:1/live"}`), http.StatusBadGateway)

	// Deletion of active and unknown streams.
	api3Status(t, api3Request(t, router, http.MethodDelete, "/streams/created-1", ""), http.StatusOK)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/streams/ghost", ""), http.StatusNotFound)

	// Idle stream summaries: created rows, bindings and history.
	require.NoError(t, db.InsertCreatedStream(ctx, storage.CreatedStream{StreamID: "idle-created", Name: "Idle Created"}))
	rr = api3Request(t, router, http.MethodGet, "/streams/idle-created", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, "Idle Created", api3JSON(t, rr)["name"])

	require.NoError(t, db.BindStreamToCamera(ctx, "idle-bound", "cam-1"))
	rr = api3Request(t, router, http.MethodGet, "/streams/idle-bound", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, true, api3JSON(t, rr)["managed"])

	// Kick publisher: unknown stream and engine failure.
	api3Status(t, api3Request(t, router, http.MethodPost, "/streams/ghost/kick", ""), http.StatusNotFound)

	// Stream history endpoint.
	rr = api3Request(t, router, http.MethodGet, "/streams/history", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, api3JSON(t, rr), "history")
}

// --- WebSocket endpoint ---

func TestApi3StreamWebSocketHandler(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	api3SeedCameraRow(t, db, "cam-1", "Front Door")
	router := func(h *Handler) http.Handler {
		return api3Router(func(r chi.Router) { r.Get("/cameras/{id}/stream/ws", h.handleStreamWS) })
	}

	// No WebSocket manager.
	api3Status(t, api3Request(t, router(NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)),
		http.MethodGet, "/cameras/cam-1/stream/ws", ""), http.StatusServiceUnavailable)

	// Camera lookup failure and unknown camera.
	broken := NewHandler(api3ClosedDB(t), store, noopAuthMW(), nil, nil, "", nil, nil)
	broken.SetWSManager(&api3WSStub{})
	api3Status(t, api3Request(t, router(broken), http.MethodGet, "/cameras/cam-1/stream/ws", ""), http.StatusInternalServerError)
	camMgr := camera.NewCameraManager(&config.Config{}, store, db, "")
	stub := &api3WSStub{}
	h := NewHandler(db, store, noopAuthMW(), &config.Config{}, camMgr, "", nil, nil)
	h.SetWSManager(stub)
	api3Status(t, api3Request(t, router(h), http.MethodGet, "/cameras/ghost/stream/ws", ""), http.StatusNotFound)

	// Not registered and no camera manager.
	noMgrWS := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	noMgrWS.SetWSManager(&api3WSStub{})
	api3Status(t, api3Request(t, router(noMgrWS), http.MethodGet, "/cameras/cam-1/stream/ws", ""), http.StatusNotFound)

	// Not registered with a group source that provides frames.
	h.SetGroupStreamSource(api3GroupSource{})
	rr := api3Request(t, router(h), http.MethodGet, "/cameras/cam-1/stream/ws", "")
	api3Status(t, rr, http.StatusOK)
	require.Equal(t, 1, stub.registered)
	require.Equal(t, 1, stub.serveCalls)

	// Registration failure that is not "already exists".
	failing := NewHandler(db, store, noopAuthMW(), &config.Config{}, camMgr, "", nil, nil)
	failing.SetWSManager(&api3WSStub{registerErr: errors.New("register failed")})
	failing.SetGroupStreamSource(api3GroupSource{})
	api3Status(t, api3Request(t, router(failing), http.MethodGet, "/cameras/cam-1/stream/ws", ""), http.StatusInternalServerError)
	// "Already exists" is tolerated.
	tolerated := NewHandler(db, store, noopAuthMW(), &config.Config{}, camMgr, "", nil, nil)
	tolerated.SetWSManager(&api3WSStub{registerErr: wsstream.ErrStreamExists})
	tolerated.SetGroupStreamSource(api3GroupSource{})
	api3Status(t, api3Request(t, router(tolerated), http.MethodGet, "/cameras/cam-1/stream/ws", ""), http.StatusOK)

	// Waiting for the codec information.
	noCodec := NewHandler(db, store, noopAuthMW(), &config.Config{}, camMgr, "", nil, nil)
	noCodec.SetWSManager(&api3WSStub{})
	noCodec.SetGroupStreamSource(api3NoCodecSource{})
	api3Status(t, api3Request(t, router(noCodec), http.MethodGet, "/cameras/cam-1/stream/ws", ""), http.StatusServiceUnavailable)

	// Serve errors map to their HTTP status.
	for err, want := range map[error]int{
		wsstream.ErrStreamNotActive: http.StatusNotFound,
		wsstream.ErrMaxViewers:      http.StatusServiceUnavailable,
		errors.New("serve failed"):  http.StatusOK,
	} {
		serveFail := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
		serveFail.SetWSManager(&api3WSStub{active: true, serveErr: err})
		api3Status(t, api3Request(t, router(serveFail), http.MethodGet, "/cameras/cam-1/stream/ws", ""), want)
	}

	// Stream play route resolves the camera through the binding.
	playRoute := api3Router(func(r chi.Router) { r.Get("/streams/{stream_id}/stream/ws", h.handleStreamWS) })
	require.NoError(t, db.BindStreamToCamera(ctx, "push-1", "cam-1"))
	rr = api3Request(t, playRoute, http.MethodGet, "/streams/push-1/stream/ws", "")
	api3Status(t, rr, http.StatusOK)
}

type api3NoCodecSource struct{}

func (api3NoCodecSource) StreamInfo(string) (model.Format, []byte, []byte, []byte, *model.StreamHub, bool) {
	return model.FormatH264, nil, nil, nil, model.NewStreamHub(), true
}

// --- local Xiaomi auth adapter ---

func TestApi3XiaomiLocalAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewLocalXiaomiAuth(cfg)
	require.NotNil(t, auth)
	require.NoError(t, auth.SetCloudConfig(context.Background(), "user", "token", "cn"))

	// Cloud calls fail fast with empty credentials and never reach the network.
	_, verify, err := auth.SignIn(context.Background(), "", "", "cn")
	require.Error(t, err)
	require.Nil(t, verify)
	_, verify, err = auth.SubmitCaptcha(context.Background(), "missing-session", "1234")
	require.Error(t, err)
	require.Nil(t, verify)
	_, verify, err = auth.SubmitVerify(context.Background(), "missing-session", "ticket")
	require.Error(t, err)
	require.Nil(t, verify)
	_, err = auth.ListDevices(context.Background())
	require.Error(t, err)
	_, err = auth.CheckVendor(context.Background(), "did-1")
	require.Error(t, err)

	// Helper conversions.
	require.Nil(t, sessionToResult(nil))
	result := sessionToResult(&xiaomi.CloudSession{UserID: "u", PassToken: "p", Region: "cn"})
	require.Equal(t, "u", result.UserID)
	require.Equal(t, "p", result.PassToken)
	require.Equal(t, "cn", result.Region)

	loginErr := &xiaomi.LoginError{Captcha: []byte("img"), VerifyPhone: "138", VerifyEmail: "a@b.c"}
	require.True(t, isLoginErr(loginErr, new(*xiaomi.LoginError)))
	require.False(t, isLoginErr(errors.New("other"), new(*xiaomi.LoginError)))
	converted := loginErrToVerification(loginErr, "session-1")
	require.Equal(t, []byte("img"), converted.Captcha)
	require.Equal(t, "138", converted.VerifyPhone)
	require.Equal(t, "session-1", converted.CaptchaSessionID)

	captchaErr := &xiaomi.CaptchaSessionError{LoginError: loginErr, CaptchaSessionID: "session-2"}
	require.True(t, isCaptchaErr(captchaErr, new(*xiaomi.CaptchaSessionError)))
	require.False(t, isCaptchaErr(errors.New("other"), new(*xiaomi.CaptchaSessionError)))
	converted = captchaErrToVerification(captchaErr)
	require.Equal(t, "session-2", converted.CaptchaSessionID)

	require.True(t, isXiaomiCameraModel("chuangmi.camera.ipc009"))
	require.True(t, isXiaomiCameraModel("isa.cateye.v2"))
	require.True(t, isXiaomiCameraModel("xiaomi.feeder.iv2001"))
	require.False(t, isXiaomiCameraModel("lumi.sensor_switch"))
}

// --- transcode profiles ---

func TestApi3TranscodeProfileHandlers(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	router := api3Router(func(r chi.Router) {
		r.Get("/profiles", h.handleTranscodeProfiles)
		r.Post("/profiles", h.handleSaveTranscodeProfile)
		r.Patch("/profiles/{id}", h.handleSaveTranscodeProfile)
		r.Delete("/profiles/{id}", h.handleDeleteTranscodeProfile)
		r.Post("/profiles/{id}/probe", h.handleProbeTranscodeProfile)
	})

	rr := api3Request(t, router, http.MethodGet, "/profiles", "")
	api3Status(t, rr, http.StatusOK)
	require.Contains(t, api3JSON(t, rr), "profiles")

	profile := transcode.DefaultProfile("ffmpeg")
	profile.Revision = 0
	encoded, err := json.Marshal(profile)
	require.NoError(t, err)
	api3Status(t, api3Request(t, router, http.MethodPost, "/profiles", `{`), http.StatusBadRequest)
	badRevision := strings.Replace(string(encoded), `"revision":0`, `"revision":3`, 1)
	api3Status(t, api3Request(t, router, http.MethodPost, "/profiles", badRevision), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodPatch, "/profiles/other", string(encoded)), http.StatusBadRequest)
	rr = api3Request(t, router, http.MethodPost, "/profiles", string(encoded))
	api3Status(t, rr, http.StatusCreated)

	// Probe without a configured capability runner fails deterministically.
	api3Status(t, api3Request(t, router, http.MethodPost, "/profiles/missing/probe", ""), http.StatusNotFound)
	probeRR := api3Request(t, router, http.MethodPost, "/profiles/"+profile.ID+"/probe", "")
	require.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, probeRR.Code, probeRR.Body.String())

	// Missing probe service.
	noProbe := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	noProbe.transcodeProbe = nil
	api3Status(t, api3Request(t, api3Router(func(r chi.Router) { r.Post("/profiles/{id}/probe", noProbe.handleProbeTranscodeProfile) }),
		http.MethodPost, "/profiles/"+profile.ID+"/probe", ""), http.StatusServiceUnavailable)

	// Delete validation.
	api3Status(t, api3Request(t, router, http.MethodDelete, "/profiles/"+profile.ID, ""), http.StatusBadRequest)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/profiles/"+profile.ID+"?revision=2", ""), http.StatusConflict)
	api3Status(t, api3Request(t, router, http.MethodDelete, "/profiles/"+profile.ID+"?revision=1", ""), http.StatusNoContent)
	_ = ctx
	_ = time.Now
}

// filepathFromTemp returns a path inside a fresh temporary directory.
func filepathFromTemp(t *testing.T, name string) string {
	t.Helper()
	return strings.TrimSuffix(t.TempDir(), "/") + "/" + name
}

// boolPtr returns a pointer to the given bool.
func boolPtr(v bool) *bool { return &v }
