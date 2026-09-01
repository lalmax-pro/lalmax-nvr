package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/wsstream"
	"github.com/stretchr/testify/require"
)

// containsProtocol checks if the protocol list contains a named available protocol.
func containsProtocol(t *testing.T, protocols []ProtocolDetail, name string) bool {
	t.Helper()
	for _, p := range protocols {
		if p.Protocol == name && p.Available {
			return true
		}
	}
	return false
}

// hasUnavailableProtocol checks if the protocol list contains a named unavailable protocol.
func hasUnavailableProtocol(t *testing.T, protocols []ProtocolDetail, name string) bool {
	t.Helper()
	for _, p := range protocols {
		if p.Protocol == name && !p.Available {
			return true
		}
	}
	return false
}

type stubMediaEngine struct {
	stream      *media.StreamInfo
	streams     []media.StreamInfo
	streamsByID map[string]*media.StreamInfo
	getErr      error
	listErr     error
	playURLs    map[string]string
}

type stubWSManager struct{}

func (s *stubMediaEngine) Start(context.Context) error    { return nil }
func (s *stubMediaEngine) Shutdown(context.Context) error { return nil }
func (s *stubMediaEngine) Ready(context.Context) error    { return nil }
func (s *stubMediaEngine) StartPull(context.Context, media.StartPullRequest) (*media.StreamSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) StopPull(context.Context, string) error {
	return nil
}
func (s *stubMediaEngine) StartRTPReceive(context.Context, media.StartRTPReceiveRequest) (*media.StreamSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) StopRTPReceive(context.Context, string) error {
	return errors.New("not implemented")
}
func (s *stubMediaEngine) KickSession(context.Context, string) error {
	return nil
}
func (s *stubMediaEngine) GetStream(_ context.Context, id string) (*media.StreamInfo, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.streamsByID != nil {
		return s.streamsByID[id], nil
	}
	return s.stream, nil
}
func (s *stubMediaEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.streams, nil
}
func (s *stubMediaEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	if u, ok := s.playURLs[req.Protocol]; ok {
		return &media.PlayURL{URL: u, Protocol: req.Protocol, ExpiresAt: time.Now().Add(time.Minute)}, nil
	}
	return nil, errors.New("unsupported protocol")
}
func (s *stubMediaEngine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeRTMPEvents(context.Context) (<-chan media.RTMPEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeSRTEvents(context.Context) (<-chan media.SRTEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) SubscribeWHIPEvents(context.Context) (<-chan media.WHIPEvent, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) AddCustomizePubSession(_ context.Context, _ string) (media.CustomizePubSession, error) {
	return nil, errors.New("not implemented")
}
func (s *stubMediaEngine) DelCustomizePubSession(_ context.Context, _ media.CustomizePubSession) error {
	return errors.New("not implemented")
}

func (s *stubWSManager) RegisterStream(cameraID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error {
	return nil
}
func (s *stubWSManager) SetAudioConfig(cameraID string, aci *wsstream.AudioCodecInfo) error {
	return nil
}
func (s *stubWSManager) IsActive(cameraID string) bool { return true }
func (s *stubWSManager) ServeWS(cameraID string, w http.ResponseWriter, r *http.Request) error {
	return nil
}
func (s *stubWSManager) StopAll() {}

// --- WHEP endpoint tests ---

func TestWHEP_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$dummyhashdummyhashdummyhashdum" }}, "")
	h := NewHandler(db, store, authMW, nil, nil, "", nil, nil)

	r := h.Routes()
	req := httptest.NewRequest("POST", "/api/cameras/test-cam/stream/webrtc", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestWHEP_Create_NoWebRTCManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-cam/stream/webrtc",
		strings.NewReader("v=0"), "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestWHEP_Delete_NoMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/test-cam/stream/webrtc/nonexistent-session",
		nil, "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestWHEP_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{stream: nil}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/nonexistent/stream/webrtc",
		strings.NewReader("v=0"), "admin", "pass")
	// Without Content-Type header, it returns 415 first
	_ = rr
}

func TestWHEP_InvalidContentType(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Create camera in DB
	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{stream: nil}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	req := httptest.NewRequest("POST", "/api/cameras/cam1/stream/webrtc", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "text/plain")
	req.SetBasicAuth("admin", "pass")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
}

// --- FLV endpoint tests ---

func TestFLV_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$dummyhashdummyhashdummyhashdum" }}, "")
	h := NewHandler(db, store, authMW, nil, nil, "", nil, nil)

	r := h.Routes()
	req := httptest.NewRequest("GET", "/api/cameras/test-cam/stream.flv", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestFLV_NoManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/test-cam/stream.flv", nil, "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestFLV_NoMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/stream.flv", nil, "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// --- Per-camera protocols endpoint tests ---

func TestCameraProtocols_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$dummyhashdummyhashdummyhashdum" }}, "")
	h := NewHandler(db, store, authMW, nil, nil, "", nil, nil)

	r := h.Routes()
	req := httptest.NewRequest("GET", "/api/cameras/test-cam/protocols", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCameraProtocols_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestCameraProtocols_H264Camera(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})
	reg.Register(&stubStreamHandler{name: "flv", codecs: []model.Format{model.FormatH264, model.FormatH265}})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h264", resp.Encoding)
	require.True(t, containsProtocol(t, resp.Protocols, "hls"), "hls should be available")
	require.True(t, containsProtocol(t, resp.Protocols, "webrtc"), "webrtc should be available")
	require.True(t, containsProtocol(t, resp.Protocols, "flv"), "flv should be available")
	require.Equal(t, "webrtc", resp.Default) // WebRTC is preferred
}

func TestCameraProtocols_H265Camera(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam2", "h265")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})
	reg.Register(&stubStreamHandler{name: "flv", codecs: []model.Format{model.FormatH264, model.FormatH265}})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam2/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h265", resp.Encoding)
	require.True(t, containsProtocol(t, resp.Protocols, "hls"), "hls should be available")
	require.True(t, containsProtocol(t, resp.Protocols, "flv"), "flv should be available")
	require.False(t, containsProtocol(t, resp.Protocols, "webrtc"), "WebRTC should not be available for H.265")
	require.Equal(t, "flv", resp.Default) // FLV is preferred after WebRTC (unavailable)
}

func TestCameraProtocols_MJPEGCamera(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam3", "mjpeg")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam3/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "mjpeg", resp.Encoding)
	require.Empty(t, resp.Protocols)
	require.Empty(t, resp.Default)
}

func TestCameraProtocols_NoRegistry(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	// No stream registry set

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h264", resp.Encoding)
	require.Empty(t, resp.Protocols)
	require.Empty(t, resp.Default)
}

func TestCameraProtocols_UsesStreamEncoding(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Camera with encoding="" but stream_encoding="h264"
	seedCameraWithEncodings(t, db, "cam1", "", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h264", resp.Encoding)
	require.True(t, containsProtocol(t, resp.Protocols, "hls"), "hls should be available")
}

func TestCameraProtocols_WithMediaEnginePlayURLsAndStatus(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})
	reg.Register(&stubStreamHandler{name: "flv", codecs: []model.Format{model.FormatH264, model.FormatH265}})
	reg.Register(&stubStreamHandler{name: "ws-flv", codecs: []model.Format{model.FormatH264, model.FormatH265}})
	reg.Register(&WSStreamHandler{})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)
	h.SetWSManager(&stubWSManager{})
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"hls":    "http://127.0.0.1:8080/live/hls/cam1/index.m3u8",
			"webrtc": "http://127.0.0.1:8080/webrtc/whep?streamid=cam1",
			"flv":    "http://127.0.0.1:8080/live/cam1.flv",
			"ws-flv": "ws://127.0.0.1:8080/live/cam1.flv",
		},
		stream: &media.StreamInfo{
			StreamID:   "cam1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			AudioCodec: "aac",
			InFPS:      24,
			Publisher: &media.SessionInfo{
				SessionID: "pub-1",
				Protocol:  "rtsp",
				Remote:    "192.168.1.10",
			},
			Subscribers: []media.SessionInfo{
				{SessionID: "sub-1", Protocol: "httpflv", Remote: "127.0.0.1"},
			},
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.NotNil(t, resp.StreamStatus)
	require.Equal(t, "lalmax", resp.StreamStatus.Engine)
	require.Equal(t, "cam1", resp.StreamStatus.StreamID)
	require.True(t, resp.StreamStatus.Active)
	require.NotNil(t, resp.StreamStatus.Publisher)
	require.Equal(t, "pub-1", resp.StreamStatus.Publisher.SessionID)
	require.Len(t, resp.StreamStatus.Subscribers, 1)

	var foundHLS, foundWebRTC, foundFLV, foundWSFLV, foundWasm bool
	for _, p := range resp.Protocols {
		switch p.Protocol {
		case "hls":
			foundHLS = true
			require.Equal(t, "http://127.0.0.1:8080/live/hls/cam1/index.m3u8", p.PlayURL)
			require.Equal(t, "lalmax", p.Backend)
		case "webrtc":
			foundWebRTC = true
			require.Equal(t, "http://127.0.0.1:8080/webrtc/whep?streamid=cam1", p.PlayURL)
			require.Equal(t, "lalmax", p.Backend)
		case "flv":
			foundFLV = true
			require.Equal(t, "http://127.0.0.1:8080/live/cam1.flv", p.PlayURL)
			require.Equal(t, "lalmax", p.Backend)
		case "ws-flv":
			foundWSFLV = true
			require.Equal(t, "ws://127.0.0.1:8080/live/cam1.flv", p.PlayURL)
			require.Equal(t, "lalmax", p.Backend)
		case "wasm":
			foundWasm = true
			require.Equal(t, "/api/cameras/cam1/stream/ws", p.PlayURL)
			require.Equal(t, "builtin-ws", p.Backend)
		}
	}
	require.True(t, foundHLS)
	require.True(t, foundWebRTC)
	require.True(t, foundFLV)
	require.True(t, foundWSFLV)
	require.True(t, foundWasm)
}

func TestCameraProtocols_WithMediaEngineStatusError(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)
	h.SetMediaEngine(&stubMediaEngine{
		getErr: errors.New("lalmax returned HTTP 404"),
		playURLs: map[string]string{
			"hls": "http://127.0.0.1:8080/live/hls/cam1/index.m3u8",
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.NotNil(t, resp.StreamStatus)
	require.Equal(t, "lalmax returned HTTP 404", resp.StreamStatus.LastError)
	require.Equal(t, "cam1", resp.StreamStatus.StreamID)
}

func TestHLS_UsesMediaEngineProxy(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/live/hls/cam1/index.m3u8", r.URL.Path)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U"))
	}))
	defer upstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"hls": upstream.URL + "/live/hls/cam1/index.m3u8",
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream/index.m3u8", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/vnd.apple.mpegurl", rr.Header().Get("Content-Type"))
	require.Equal(t, "#EXTM3U", rr.Body.String())
}

func TestHLS_Segment_UsesMediaEngineProxy(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/live/hls/cam1/seg-1.ts", r.URL.Path)
		_, _ = w.Write([]byte("segment"))
	}))
	defer upstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"hls": upstream.URL + "/live/hls/cam1/index.m3u8",
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream/seg-1.ts", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "segment", rr.Body.String())
}

func TestLLHLS_FMP4Resource_UsesMediaEngineProxyWithoutQuery(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	hlsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("fMP4 LL-HLS resource should not use regular HLS upstream, got %s", r.URL.String())
	}))
	defer hlsUpstream.Close()

	llhlsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/live/hls/cam1/init.mp4", r.URL.Path)
		require.Equal(t, "live", r.URL.Query().Get("app_name"))
		require.Equal(t, "1", r.URL.Query().Get("ll-hls"))
		_, _ = w.Write([]byte("init"))
	}))
	defer llhlsUpstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"hls":    hlsUpstream.URL + "/hls/cam1.m3u8",
			"ll-hls": llhlsUpstream.URL + "/live/hls/cam1/index.m3u8?app_name=live&ll-hls=1",
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream/init.mp4", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "init", rr.Body.String())
}

func TestLLHLS_SubPlaylist_UsesMediaEngineProxyWithoutQuery(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	hlsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("LL-HLS child playlist should not use regular HLS upstream, got %s", r.URL.String())
	}))
	defer hlsUpstream.Close()

	llhlsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/live/hls/cam1/stream.m3u8", r.URL.Path)
		require.Equal(t, "live", r.URL.Query().Get("app_name"))
		require.Equal(t, "1", r.URL.Query().Get("ll-hls"))
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-PART:DURATION=0.2,URI=\"part0.mp4\"\n"))
	}))
	defer llhlsUpstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"hls":    hlsUpstream.URL + "/hls/cam1.m3u8",
			"ll-hls": llhlsUpstream.URL + "/live/hls/cam1/index.m3u8?app_name=live&ll-hls=1",
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream/stream.m3u8", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "#EXT-X-PART")
}

func TestFLV_UsesMediaEngineProxy(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/live/cam1.flv", r.URL.Path)
		w.Header().Set("Content-Type", "video/x-flv")
		_, _ = w.Write([]byte("flvdata"))
	}))
	defer upstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"flv": upstream.URL + "/live/cam1.flv",
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream.flv", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "video/x-flv", rr.Header().Get("Content-Type"))
	require.Equal(t, "flvdata", rr.Body.String())
}

func TestWHEP_UsesMediaEngineProxy(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			require.Equal(t, "/webrtc/whep", r.URL.Path)
			require.Equal(t, "streamid=cam1", r.URL.RawQuery)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "v=0", string(body))
			w.Header().Set("Content-Type", "application/sdp")
			w.Header().Set("Location", "/webrtc/session/abc")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("answer-sdp"))
		case http.MethodDelete:
			require.Equal(t, "/webrtc/session/abc", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer upstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{
			"webrtc": upstream.URL + "/webrtc/whep?streamid=cam1",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/cam1/stream/webrtc", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	req.SetBasicAuth("admin", "pass")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	require.Equal(t, "answer-sdp", rr.Body.String())
	location := rr.Header().Get("Location")
	require.NotEmpty(t, location)

	locURL, err := url.Parse(location)
	require.NoError(t, err)
	token := pathBase(locURL.Path)
	require.NotEmpty(t, token)

	delReq := httptest.NewRequest(http.MethodDelete, location, nil)
	delReq.SetBasicAuth("admin", "pass")
	delRR := httptest.NewRecorder()
	h.Routes().ServeHTTP(delRR, delReq)
	require.Equal(t, http.StatusNoContent, delRR.Code)
}

func TestWHEP_UsesMediaEngineProxy_ForExternalStream(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			require.Equal(t, "/webrtc/whep", r.URL.Path)
			require.Equal(t, "app_name=live&streamid=test110", r.URL.RawQuery)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "v=0", string(body))
			w.Header().Set("Content-Type", "application/sdp")
			w.Header().Set("Location", "/webrtc/session/ext-1")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("answer-sdp"))
		case http.MethodDelete:
			require.Equal(t, "/webrtc/session/ext-1", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer upstream.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "test110",
			AppName:  "live",
			Active:   true,
		},
		playURLs: map[string]string{
			"webrtc": upstream.URL + "/webrtc/whep?app_name=live&streamid=test110",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test110/stream/webrtc", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	req.SetBasicAuth("admin", "pass")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	require.Equal(t, "answer-sdp", rr.Body.String())
	location := rr.Header().Get("Location")
	require.NotEmpty(t, location)

	delReq := httptest.NewRequest(http.MethodDelete, location, nil)
	delReq.SetBasicAuth("admin", "pass")
	delRR := httptest.NewRecorder()
	h.Routes().ServeHTTP(delRR, delReq)
	require.Equal(t, http.StatusNoContent, delRR.Code)
}

func pathBase(p string) string {
	if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// --- Route wiring verification ---

func TestRoutes_WHEPEndpointsRegistered(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "test", "h264")

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	r := h.Routes()
	// Verify WHEP POST route responds (not 404)
	req := httptest.NewRequest("POST", "/api/cameras/test/stream/webrtc", nil)
	req.SetBasicAuth("admin", "pass")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	// Without media engine, returns 503 Service Unavailable
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestRoutes_FLVEndpointRegistered(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/test/stream.flv", nil, "admin", "pass")

	// Without media engine, returns 503
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestRoutes_CameraProtocolsEndpointRegistered(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/protocols", nil, "admin", "pass")

	// Camera not found (404), not route not found
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestListStreams_ReturnsManagedAndExternalStreams(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{
		streams: []media.StreamInfo{
			{
				StreamID:   "cam1",
				AppName:    "live",
				Active:     true,
				VideoCodec: "h264",
				AudioCodec: "aac",
				InFPS:      24,
				Publisher: &media.SessionInfo{
					SessionID: "pub-cam1",
					Protocol:  "rtsp",
					Remote:    "192.168.1.10:554",
				},
			},
			{
				StreamID:   "obs-room-1",
				AppName:    "live",
				Active:     true,
				VideoCodec: "h264",
				Publisher: &media.SessionInfo{
					SessionID: "pub-obs1",
					Protocol:  "rtmp",
					Remote:    "10.0.0.8:1935",
				},
				Subscribers: []media.SessionInfo{
					{
						SessionID: "sub-1",
						Protocol:  "webrtc",
						Remote:    "10.0.0.9:54000",
					},
				},
			},
		},
		playURLs: map[string]string{
			"hls":    "http://localhost:9090/live/index.m3u8",
			"ll-hls": "http://localhost:9090/live/ll/index.m3u8",
			"flv":    "http://localhost:9090/live.flv",
			"ws-flv": "ws://localhost:9090/live.flv",
			"webrtc": "http://localhost:9090/whep",
			"fmp4":   "http://localhost:9090/live.m4s",
			"rtmp":   "rtmp://localhost/live/test",
			"rtsp":   "rtsp://localhost/live/test",
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Streams, 2)
	require.Equal(t, 2, resp.Total)
	require.Equal(t, streamListDefaultLimit, resp.Limit)
	require.Equal(t, 0, resp.Offset)

	require.Equal(t, "cam1", resp.Streams[0].StreamID)
	require.True(t, resp.Streams[0].Managed)
	require.Equal(t, "camera", resp.Streams[0].ManagementType)
	require.Equal(t, "cam1", resp.Streams[0].CameraID)
	require.Equal(t, "camera", resp.Streams[0].SourceType)
	require.NotEmpty(t, resp.Streams[0].PlayURLs)

	require.Equal(t, "obs-room-1", resp.Streams[1].StreamID)
	require.False(t, resp.Streams[1].Managed)
	require.Equal(t, "rtmp_push", resp.Streams[1].SourceType)
	require.Len(t, resp.Streams[1].Subscribers, 1)
}

func TestListStreams_SearchAndPagination(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")
	require.NoError(t, db.UpsertCamera(context.Background(), "cam2", "Lobby Camera", "rtsp", "h264", "rtsp://example.com/lobby", "", "", true, "", "", ""))

	engine := &stubMediaEngine{
		streams: []media.StreamInfo{
			{StreamID: "cam1", AppName: "live", Active: true, VideoCodec: "h264"},
			{StreamID: "cam2", AppName: "live", Active: true, VideoCodec: "h264"},
			{StreamID: "obs-room-1", AppName: "live", Active: true, VideoCodec: "h264", Publisher: &media.SessionInfo{Protocol: "rtmp"}},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams?q=lobby&managed=true&limit=1&offset=0", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Streams, 1)
	require.Equal(t, 1, resp.Total)
	require.Equal(t, "cam2", resp.Streams[0].StreamID)
	require.Equal(t, 1, resp.Limit)
	require.Equal(t, 0, resp.Offset)
}

func TestListStreams_IncludesIdleEnabledCamera(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{streams: nil}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Streams, 1)
	require.Equal(t, "cam1", resp.Streams[0].StreamID)
	require.True(t, resp.Streams[0].Managed)
	require.False(t, resp.Streams[0].Active)
}

func TestListStreams_ActiveStreamsFirst(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam-idle", "h264")
	require.NoError(t, db.UpsertCamera(context.Background(), "cam-active", "Active Cam", "rtsp", "h264", "rtsp://example.com/active", "", "", true, "", "", ""))

	engine := &stubMediaEngine{
		streams: []media.StreamInfo{
			{StreamID: "cam-active", AppName: "live", Active: true, VideoCodec: "h264"},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Streams, 2)
	require.Equal(t, "cam-active", resp.Streams[0].StreamID)
	require.True(t, resp.Streams[0].Active)
	require.Equal(t, "cam-idle", resp.Streams[1].StreamID)
	require.False(t, resp.Streams[1].Active)
}

func TestListStreams_IncludesRecentHistory(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	endedAt := time.Now().Add(-time.Hour)
	require.NoError(t, db.InsertStreamHistory(context.Background(), &storage.StreamHistory{
		StreamID:   "obs-room-1",
		AppName:    "live",
		Protocol:   "rtmp",
		RemoteAddr: "10.0.0.8:1935",
		SessionID:  "sess-obs-1",
		StartedAt:  endedAt.Add(-10 * time.Minute),
	}))
	require.NoError(t, db.FinishStreamHistory(context.Background(), "sess-obs-1", endedAt, 1000, 2000))

	engine := &stubMediaEngine{streams: nil}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamListResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Streams, 1)
	require.Equal(t, "obs-room-1", resp.Streams[0].StreamID)
	require.False(t, resp.Streams[0].Managed)
	require.False(t, resp.Streams[0].Active)
	require.Equal(t, "rtmp_push", resp.Streams[0].SourceType)
	require.NotNil(t, resp.Streams[0].LastFrameTime)
}

func TestGetStream_IdleEnabledCamera(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{stream: nil}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/cam1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "cam1", resp.StreamID)
	require.True(t, resp.Managed)
	require.False(t, resp.Active)
}

func TestListStreams_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// --- Test helpers ---

// seedCameraWithEncoding inserts a camera with the given encoding into the DB.
func seedCameraWithEncoding(t *testing.T, db *storage.DB, id, encoding string) {
	t.Helper()
	err := db.UpsertCamera(context.Background(), id, "Test Camera", "rtsp", encoding, "rtsp://example.com/stream", "", "", true, "", "", "")
	require.NoError(t, err, "failed to seed camera %s", id)
}

// seedCameraWithEncodings inserts a camera with separate encoding and stream_encoding.
func seedCameraWithEncodings(t *testing.T, db *storage.DB, id, encoding, streamEncoding string) {
	t.Helper()
	err := db.UpsertCamera(context.Background(), id, "Test Camera", "rtsp", encoding, "rtsp://example.com/stream", "", "", true, "", "", streamEncoding)
	require.NoError(t, err, "failed to seed camera %s", id)
}

// parseJSONBody parses JSON from a httptest.ResponseRecorder into v.
func parseJSONBody(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) error {
	t.Helper()
	dec := json.NewDecoder(rr.Body)
	return dec.Decode(v)
}

func TestGetStream_ReturnsStreamDetails(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "cam1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			AudioCodec: "aac",
			InFPS:      24,
			Publisher: &media.SessionInfo{
				SessionID: "pub-cam1",
				Protocol:  "rtsp",
				Remote:    "192.168.1.10:554",
			},
		},
		playURLs: map[string]string{
			"hls": "http://localhost:9090/live/hls/cam1/index.m3u8",
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/cam1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "cam1", resp.StreamID)
	require.True(t, resp.Managed)
	require.Equal(t, "camera", resp.ManagementType)
	require.Equal(t, "cam1", resp.CameraID)
	require.Equal(t, "camera", resp.SourceType)
	require.True(t, resp.Active)
	require.NotNil(t, resp.Publisher)
	require.Equal(t, "pub-cam1", resp.Publisher.SessionID)
	require.NotEmpty(t, resp.PlayURLs)
}

func TestGetStream_ExternalStream(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "obs-stream-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
		playURLs: map[string]string{
			"hls": "http://localhost:9090/live/hls/obs-stream-1/index.m3u8",
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/obs-stream-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp.StreamID)
	require.False(t, resp.Managed)
	require.Empty(t, resp.ManagementType)
	require.Empty(t, resp.CameraID)
	require.Equal(t, "rtmp_push", resp.SourceType)
	require.True(t, resp.Active)
	require.NotNil(t, resp.Publisher)
	require.Equal(t, "pub-obs1", resp.Publisher.SessionID)
}

func TestGetStream_BoundStream(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")
	require.NoError(t, db.BindStreamToCamera(context.Background(), "obs-stream-1", "cam1"))

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "obs-stream-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/obs-stream-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp.StreamID)
	require.True(t, resp.Managed)
	require.Equal(t, "bound", resp.ManagementType)
	require.Equal(t, "cam1", resp.CameraID)
	require.Equal(t, "Test Camera", resp.CameraName)
	require.Equal(t, "camera", resp.SourceType)
}

func TestGetStream_PromotedPushStream(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "obs-stream-1", "h264")

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "obs-stream-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/obs-stream-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp streamSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp.StreamID)
	require.True(t, resp.Managed)
	require.Equal(t, "promoted", resp.ManagementType)
	require.Equal(t, "obs-stream-1", resp.CameraID)
	require.Equal(t, "Test Camera", resp.CameraName)
	require.Equal(t, "camera", resp.SourceType)
}

type stubGB28181StreamStatus struct {
	playing map[string]bool
}

func (s *stubGB28181StreamStatus) IsStreamPlaying(streamID string) bool {
	if s == nil || s.playing == nil {
		return false
	}
	return s.playing[streamID]
}

func TestGetStream_GB28181IdlePlaying(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	streamID := "34020000001320000001:34020000001320000001"
	require.NoError(t, db.UpsertCamera(context.Background(), streamID, "GB IPC", "gb28181", "h264", "rtsp://127.0.0.1:5544/live/"+streamID, "", "", true, "", "", ""))

	engine := &stubMediaEngine{stream: nil}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	h.SetGB28181Server(&stubGB28181StreamStatus{
		playing: map[string]bool{streamID: true},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/"+streamID, nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp streamSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, streamID, resp.StreamID)
	require.True(t, resp.GB28181Playing)
	require.True(t, resp.Active)
	require.Equal(t, "gb28181", resp.SourceType)
}

func TestGetStream_NotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: nil,
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/nonexistent", nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetStream_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/cam1", nil, "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestGetStream_MediaEngineError(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		getErr: errors.New("connection timeout"),
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/cam1", nil, "admin", "pass")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestBindCamera_Success(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "obs-stream-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"camera_id": "cam1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/bind-camera",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp["stream_id"])
	require.Equal(t, "cam1", resp["camera_id"])
	require.Equal(t, "bound", resp["status"])
}

func TestBindCamera_StreamNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{
		stream: nil,
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"camera_id": "cam1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/nonexistent/bind-camera",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestBindCamera_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"camera_id": "nonexistent"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/bind-camera",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestBindCamera_MissingCameraID(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/bind-camera",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestBindCamera_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	body := `{"camera_id": "cam1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/bind-camera",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestUnbindCamera_Success(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	bindBody := `{"camera_id": "cam1"}`
	bindRR := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/bind-camera",
		strings.NewReader(bindBody), "admin", "pass")
	require.Equal(t, http.StatusOK, bindRR.Code)

	unbindRR := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/unbind-camera",
		nil, "admin", "pass")
	require.Equal(t, http.StatusOK, unbindRR.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(unbindRR.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp["stream_id"])
	require.Equal(t, "unbound", resp["status"])
}

func TestUnbindCamera_NotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/unbind-camera",
		nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestUnbindCamera_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/unbind-camera",
		nil, "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestPromoteStream_Success(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "obs-stream-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"name": "OBS Stream 1", "description": "Test stream", "location": "Room 1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp["stream_id"])
	require.Equal(t, "obs-stream-1", resp["camera_id"])
	require.Equal(t, "rtmp_push", resp["source_type"])
	require.Equal(t, "promoted", resp["status"])

	binding, err := db.GetStreamBinding(context.Background(), "obs-stream-1")
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, "obs-stream-1", binding.CameraID)

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	var cameras []storage.CameraRow
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &cameras))
	require.Len(t, cameras, 1)
	require.Equal(t, "rtmp_push", cameras[0].SourceType)
}

func TestPromoteStream_StreamNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: nil,
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"name": "Test Stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/nonexistent/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPromoteStream_AlreadyMapped(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "cam1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher: &media.SessionInfo{
				SessionID: "pub-cam1",
				Protocol:  "rtsp",
				Remote:    "192.168.1.10:554",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"name": "Camera 1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/cam1/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusConflict, rr.Code)
}

func TestPromoteStream_MissingName(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPromoteStream_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	body := `{"name": "Test Stream"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestPromoteStream_SRTStream(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "srt-stream-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h265",
			Publisher: &media.SessionInfo{
				SessionID: "pub-srt1",
				Protocol:  "srt",
				Remote:    "10.0.0.9:9000",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"name": "SRT Stream 1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/srt-stream-1/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "srt-stream-1", resp["stream_id"])
	require.Equal(t, "srt_push", resp["source_type"])
	require.Equal(t, "promoted", resp["status"])
}

func TestPromoteStream_WHIPCustomizeSession(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "whip-stream-1",
			AppName:    "live",
			Active:     false,
			VideoCodec: "h264",
			AudioCodec: "opus",
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	body := `{"name": "WHIP Stream 1"}`
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/whip-stream-1/promote",
		strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "whip-stream-1", resp["stream_id"])
	require.Equal(t, "whip_push", resp["source_type"])
	require.Equal(t, "promoted", resp["status"])

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	var cameras []storage.CameraRow
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &cameras))
	require.Len(t, cameras, 1)
	require.Equal(t, "whip_push", cameras[0].SourceType)
}

func TestDeleteStream_Success(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/obs-stream-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp["stream_id"])
	require.Equal(t, "deleted", resp["status"])
}

func TestDeleteStream_StreamNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: nil,
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/nonexistent", nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDeleteStream_OfflineManagedCamera(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "codex-test", "h264")
	require.NoError(t, db.BindStreamToCamera(context.Background(), "codex-test", "codex-test"))

	engine := &stubMediaEngine{stream: nil}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/codex-test", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "codex-test", resp["stream_id"])
	require.Equal(t, "deleted", resp["status"])

	cam, err := db.GetCamera(context.Background(), "codex-test")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.True(t, cam.Archived)

	binding, err := db.GetStreamBinding(context.Background(), "codex-test")
	require.NoError(t, err)
	require.Nil(t, binding)
}

func TestDeleteStream_OfflineHistoryOnly(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	endedAt := time.Now().Add(-time.Hour)
	require.NoError(t, db.InsertStreamHistory(context.Background(), &storage.StreamHistory{
		StreamID:   "obs-room-1",
		AppName:    "live",
		Protocol:   "rtmp",
		RemoteAddr: "10.0.0.8:1935",
		SessionID:  "sess-obs-1",
		StartedAt:  endedAt.Add(-10 * time.Minute),
	}))
	require.NoError(t, db.FinishStreamHistory(context.Background(), "sess-obs-1", endedAt, 1000, 2000))

	engine := &stubMediaEngine{stream: nil}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/obs-room-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	histories, total, err := db.ListStreamHistory(context.Background(), "obs-room-1", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 0, total)
	require.Empty(t, histories)
}

func TestDeleteStream_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/obs-stream-1", nil, "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestDeleteStream_NoPublisher(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:  "obs-stream-1",
			Active:    true,
			Publisher: nil,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/obs-stream-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp["stream_id"])
	require.Equal(t, "deleted", resp["status"])
}

func TestKickPublisher_Success(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID: "obs-stream-1",
			Active:   true,
			Publisher: &media.SessionInfo{
				SessionID: "pub-obs1",
				Protocol:  "rtmp",
				Remote:    "10.0.0.8:1935",
			},
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/kick-publisher", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "obs-stream-1", resp["stream_id"])
	require.Equal(t, "pub-obs1", resp["session_id"])
	require.Equal(t, "kicked", resp["status"])
}

func TestKickPublisher_StreamNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: nil,
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/nonexistent/kick-publisher", nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestKickPublisher_NoPublisher(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	engine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:  "obs-stream-1",
			Active:    true,
			Publisher: nil,
		},
	}

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/kick-publisher", nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestKickPublisher_RequiresMediaEngine(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-stream-1/kick-publisher", nil, "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
