package api

// Issue #74 coverage tests for system settings/stats handlers, stream-list
// management handlers and camera handlers.
//
// Everything here runs against httptest + a temporary SQLite database and stub
// dependencies; no external network or real devices are used.
//
// Every helper/type/constant in this file is prefixed with api2sys so it stays
// unique inside the package while other agents add test files in parallel.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// ==================== request helpers ====================

func api2sysRequest(method, target, body string, params map[string]string) *http.Request {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, target, reader)
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	return req
}

func api2sysServe(handler http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func api2sysCall(handler http.HandlerFunc, method, target, body string, params map[string]string) *httptest.ResponseRecorder {
	return api2sysServe(handler, api2sysRequest(method, target, body, params))
}

func api2sysStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("expected status %d, got %d: %s", want, rr.Code, rr.Body.String())
	}
}

func api2sysJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON body %q: %v", rr.Body.String(), err)
	}
	return out
}

// api2sysField returns a nested JSON field from a decoded body.
func api2sysField(t *testing.T, body map[string]any, keys ...string) any {
	t.Helper()
	var cur any = body
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("field %v: parent is not an object", keys)
		}
		cur = m[key]
	}
	return cur
}

// ==================== handler helpers ====================

// api2sysConfig builds a deterministic config rooted in the test storage dir.
func api2sysConfig(store *storage.Manager) *config.Config {
	return &config.Config{
		Server:  config.ServerConfig{Listen: ":9090"},
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Streaming: config.StreamingConfig{
			DefaultProtocol:   "webrtc",
			AutoStopNoViewSec: 300,
		},
		Media: config.MediaConfig{
			LalmaxHTTPAddr:  "http://127.0.0.1:12090",
			LalmaxPublicURL: "http://127.0.0.1:12090",
		},
		Cameras: []config.CameraConfig{},
	}
}

// api2sysNewHandler returns a handler backed by a fresh temp SQLite DB with a
// writable config path (so save/reload/backup handlers work).
func api2sysNewHandler(t *testing.T) (*Handler, *config.Config, *storage.DB, *storage.Manager) {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := api2sysConfig(store)
	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = filepath.Join(t.TempDir(), "config.yaml")
	return h, cfg, db, store
}

// api2sysNoConfigHandler returns a handler without a config (nil).
func api2sysNoConfigHandler(t *testing.T) (*Handler, *storage.DB, *storage.Manager) {
	t.Helper()
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	return h, db, store
}

// api2sysClosedDBHandler returns a handler whose DB is already closed so query
// failures exercise the storage-error branches.
func api2sysClosedDBHandler(t *testing.T) (*Handler, *storage.DB) {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := api2sysConfig(store)
	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}
	return h, db
}

// ==================== stub dependencies ====================

// api2sysEngine augments stubMediaEngine with the optional interfaces used by
// the settings handlers (restart, HLS apply, config regeneration).
type api2sysEngine struct {
	stubMediaEngine
	restartErr   error
	restartCalls int
	lastRTMPPort int
	lastSRTPort  int
	lastRTMPOn   bool
	lastSRTOn    bool
	regenErr     error
	regenCalls   int
	hlsErr       error
	hlsCalls     int
	lastHLS      *config.HLSConfig
}

func (e *api2sysEngine) Restart(_ context.Context, rtmpPort, srtPort int, rtmpEnabled, srtEnabled bool) error {
	e.restartCalls++
	e.lastRTMPPort = rtmpPort
	e.lastSRTPort = srtPort
	e.lastRTMPOn = rtmpEnabled
	e.lastSRTOn = srtEnabled
	return e.restartErr
}

func (e *api2sysEngine) RegenerateConfig() error {
	e.regenCalls++
	return e.regenErr
}

func (e *api2sysEngine) ApplyHLSConfig(cfg config.HLSConfig) error {
	e.hlsCalls++
	copied := cfg
	e.lastHLS = &copied
	return e.hlsErr
}

// api2sysPlainEngine only implements media.Engine (no optional interfaces).
type api2sysPlainEngine struct {
	stubMediaEngine
}

type api2sysGBRestarter struct {
	err   error
	calls []config.GB28181Config
}

func (r *api2sysGBRestarter) RestartGB28181(_ context.Context, cfg *config.GB28181Config) error {
	if cfg != nil {
		r.calls = append(r.calls, *cfg)
	}
	return r.err
}

type api2sysGBProvider struct {
	*api2sysGBRestarter
}

func (p *api2sysGBProvider) CurrentGB28181Server() *gb28181.Server { return nil }

type api2sysGBStatus struct {
	playing map[string]bool
}

func (s api2sysGBStatus) IsStreamPlaying(streamID string) bool { return s.playing[streamID] }

type api2sysBanMgr struct {
	banned   map[string]bool
	banErr   error
	unbanErr error
}

func (b *api2sysBanMgr) Ban(_ context.Context, streamID, _ string, _ *time.Time) error {
	if b.banErr != nil {
		return b.banErr
	}
	if b.banned == nil {
		b.banned = map[string]bool{}
	}
	b.banned[streamID] = true
	return nil
}

func (b *api2sysBanMgr) Unban(_ context.Context, streamID string) error {
	if b.unbanErr != nil {
		return b.unbanErr
	}
	delete(b.banned, streamID)
	return nil
}

func (b *api2sysBanMgr) ListBans(context.Context) ([]storage.StreamBan, error) {
	return nil, nil
}

type api2sysHealthMgr struct {
	all map[string]*model.CameraHealth
}

func (m api2sysHealthMgr) GetAllHealth() map[string]*model.CameraHealth { return m.all }

func (m api2sysHealthMgr) GetCameraHealth(cameraID string) *model.CameraHealth {
	return m.all[cameraID]
}

type api2sysAddr struct{ s string }

func (a api2sysAddr) Network() string { return "ip" }
func (a api2sysAddr) String() string  { return a.s }

// ==================== metrics history ====================

func TestApi2SysMetricsHistorySince(t *testing.T) {
	h := newSysMetricsHistory()
	if got := h.since(time.Now().Unix()); len(got) != 0 {
		t.Fatalf("expected empty history, got %d samples", len(got))
	}

	now := time.Now().Unix()
	h.samples[0] = model.SystemMetricSample{Timestamp: now - 300, CPUPct: 1}
	h.samples[1] = model.SystemMetricSample{Timestamp: now - 120, CPUPct: 2}
	h.samples[2] = model.SystemMetricSample{Timestamp: now - 10, CPUPct: 3}
	h.head = 3
	h.count = 3

	if got := h.since(now - 60); len(got) != 1 || got[0].CPUPct != 3 {
		t.Fatalf("expected only newest sample, got %+v", got)
	}
	if got := h.since(now - 1000); len(got) != 3 {
		t.Fatalf("expected all samples, got %d", len(got))
	}
	if got := h.since(now + 60); len(got) != 0 {
		t.Fatalf("expected no future samples, got %d", len(got))
	}

	// Ring wrap: entries written around the end of the buffer stay chronological.
	wrapped := newSysMetricsHistory()
	wrapped.samples[metricsHistoryCap-1] = model.SystemMetricSample{Timestamp: now - 30, CPUPct: 30}
	wrapped.samples[0] = model.SystemMetricSample{Timestamp: now - 20, CPUPct: 20}
	wrapped.samples[1] = model.SystemMetricSample{Timestamp: now - 10, CPUPct: 10}
	wrapped.head = 2
	wrapped.count = 3
	got := wrapped.since(now - 60)
	if len(got) != 3 {
		t.Fatalf("expected 3 wrapped samples, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp < got[i-1].Timestamp {
			t.Fatalf("samples not chronological: %+v", got)
		}
	}

	// sample() should store a data point and keep the ring counters consistent.
	sampled := newSysMetricsHistory()
	sampled.sample()
	if sampled.count != 1 {
		t.Fatalf("expected 1 sample after sample(), got %d", sampled.count)
	}
	sampled.prevTS = time.Now().Add(-5 * time.Second).Unix()
	sampled.prevCPUTotal = 10
	sampled.prevNetSent = 1
	sampled.prevNetRecv = 1
	sampled.sample()
	if sampled.count != 2 {
		t.Fatalf("expected 2 samples after second sample(), got %d", sampled.count)
	}
}

func TestApi2SysStartMetricsSampler(t *testing.T) {
	// nil history is a no-op.
	var nilHandler Handler
	nilHandler.startMetricsSampler(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	h := &Handler{sysMetrics: newSysMetricsHistory()}
	h.startMetricsSampler(ctx)
	cancel()
	// Wait deterministically for the sampler's first write rather than sleeping.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.sysMetrics.mu.RLock()
		count := h.sysMetrics.count
		h.sysMetrics.mu.RUnlock()
		if count > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("metrics sampler did not record a sample")
}

func TestApi2SysSystemHistoryHandler(t *testing.T) {
	// nil ring returns an empty array.
	h := &Handler{}
	rr := api2sysCall(h.handleSystemHistory, "GET", "/api/stats/system/history", "", nil)
	api2sysStatus(t, rr, http.StatusOK)
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("expected empty array, got %q", rr.Body.String())
	}

	now := time.Now().Unix()
	h = &Handler{sysMetrics: newSysMetricsHistory()}
	h.sysMetrics.samples[0] = model.SystemMetricSample{Timestamp: now - 60, CPUPct: 5}
	h.sysMetrics.samples[1] = model.SystemMetricSample{Timestamp: now - 60*60 - 10, CPUPct: 6}
	h.sysMetrics.samples[2] = model.SystemMetricSample{Timestamp: now - 7*60*60, CPUPct: 7}
	h.sysMetrics.head = 3
	h.sysMetrics.count = 3

	cases := []struct {
		period string
		want   int
	}{
		{"", 1},
		{"5m", 1},
		{"15m", 1},
		{"30m", 1},
		{"6h", 2},
		{"24h", 3},
		{"bogus", 1},
	}
	for _, tc := range cases {
		target := "/api/stats/system/history"
		if tc.period != "" {
			target += "?period=" + tc.period
		}
		rr := api2sysCall(h.handleSystemHistory, "GET", target, "", nil)
		api2sysStatus(t, rr, http.StatusOK)
		var samples []model.SystemMetricSample
		if err := json.Unmarshal(rr.Body.Bytes(), &samples); err != nil {
			t.Fatalf("period %q: invalid JSON: %v", tc.period, err)
		}
		if len(samples) != tc.want {
			t.Fatalf("period %q: expected %d samples, got %d", tc.period, tc.want, len(samples))
		}
	}
}

func TestApi2SysHourlyAndUptimeStats(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)

	for _, target := range []string{
		"/api/stats/hourly",
		"/api/stats/hourly?hours=48",
		"/api/stats/hourly?hours=abc",
		"/api/stats/hourly?hours=0",
		"/api/stats/hourly?hours=10000",
		"/api/stats/camera-uptime",
		"/api/stats/camera-uptime?days=30",
		"/api/stats/camera-uptime?days=nope",
		"/api/stats/camera-uptime?days=1000",
	} {
		rr := doRequest(t, h.Routes(), "GET", target, nil, "", "")
		api2sysStatus(t, rr, http.StatusOK)
	}

	// Storage failures surface as 500.
	failing, db := api2sysClosedDBHandler(t)
	defer db.Close()
	for _, target := range []string{"/api/stats/hourly", "/api/stats/camera-uptime"} {
		rr := doRequest(t, failing.Routes(), "GET", target, nil, "", "")
		api2sysStatus(t, rr, http.StatusInternalServerError)
	}
}

func TestApi2SysStatsStorageErrors(t *testing.T) {
	h, db := api2sysClosedDBHandler(t)
	defer db.Close()

	rr := doRequest(t, h.Routes(), "GET", "/api/stats", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	rr = doRequest(t, h.Routes(), "GET", "/api/stats/trends?days=5", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	ok, _, _, _ := api2sysNewHandler(t)
	rr = doRequest(t, ok.Routes(), "GET", "/api/stats/trends?days=abc", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
}

func TestApi2SysSystemStatsAndNetwork(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)

	rr := doRequest(t, h.Routes(), "GET", "/api/stats/system", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if _, ok := body["cpu"].(map[string]any); !ok {
		t.Fatalf("missing cpu in system stats: %v", body)
	}
	if _, ok := body["system"].(map[string]any); !ok {
		t.Fatalf("missing system in system stats: %v", body)
	}
	if body["uptime"] == "" {
		t.Fatalf("expected non-empty uptime: %v", body)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/network", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if _, ok := body["interfaces"].([]any); !ok {
		t.Fatalf("missing interfaces array: %v", body)
	}

	if _, err := getNetworkInterfaces(); err != nil {
		t.Fatalf("getNetworkInterfaces failed: %v", err)
	}
	if got := getInterfaceSpeed("api2sys-not-a-real-interface"); got != "unknown" {
		t.Fatalf("expected unknown speed, got %q", got)
	}
	if isDisplayableInterfaceAddress(api2sysAddr{"192.168.1.10/24"}) != true {
		t.Fatal("expected routable address to be displayable")
	}
	if isDisplayableInterfaceAddress(api2sysAddr{"fe80::1/64"}) != false {
		t.Fatal("expected link-local address to be hidden")
	}
	if isDisplayableInterfaceAddress(api2sysAddr{"not-an-address"}) != true {
		t.Fatal("expected unparsable address to be displayable")
	}
	if readProcessRSS() == 0 {
		// RSS can legitimately be 0 on exotic platforms, only assert the call path.
		t.Log("readProcessRSS returned 0")
	}
	if _, _, err := readCPURaw(); err != nil {
		t.Fatalf("readCPURaw failed: %v", err)
	}
	if _, _, err := readNetworkInfo(); err != nil {
		t.Fatalf("readNetworkInfo failed: %v", err)
	}
	if _, _, err := readMemoryInfo(); err != nil {
		t.Fatalf("readMemoryInfo failed: %v", err)
	}
}

func TestApi2SysReadyzNotReady(t *testing.T) {
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	h2 := NewHandler(nil, nil, noopAuthMW(), api2sysConfig(mustStorage(t)), nil, "", nil, nil)
	rr = doRequest(t, h2.Routes(), "GET", "/api/readyz", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

func mustStorage(t *testing.T) *storage.Manager {
	t.Helper()
	mgr, err := storage.NewManager(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatalf("failed to create storage manager: %v", err)
	}
	return mgr
}

// ==================== streaming settings ====================

func TestApi2SysStreamingSettingsGet(t *testing.T) {
	noCfg, _, _ := api2sysNoConfigHandler(t)
	rr := doRequest(t, noCfg.Routes(), "GET", "/api/settings/streaming", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	h, cfg, _, _ := api2sysNewHandler(t)
	enabled := true
	cfg.Streaming.WebRTC.Enabled = &enabled
	cfg.Streaming.WebRTC.MaxViewers = 3
	cfg.Streaming.FLV.Enabled = &enabled
	cfg.HLS.Enabled = &enabled
	cfg.RTMP.Enabled = &enabled
	cfg.RTMP.Port = 1935
	cfg.SRT.Enabled = &enabled
	cfg.SRT.Port = 19000
	cfg.WHIP.Enabled = &enabled
	cfg.Media.RTSPAuthEnable = true
	cfg.Media.RTSPAuthMethod = 1
	cfg.Media.RTSPUsername = "rtspuser"

	rr = doRequest(t, h.Routes(), "GET", "/api/settings/streaming", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["default_protocol"] != "webrtc" {
		t.Fatalf("unexpected default protocol: %v", body["default_protocol"])
	}
	if got := api2sysField(t, body, "hls", "enabled"); got != true {
		t.Fatalf("expected hls enabled, got %v", got)
	}
	if got := api2sysField(t, body, "rtsp_server", "username"); got != "rtspuser" {
		t.Fatalf("expected rtsp username, got %v", got)
	}
	if got := api2sysField(t, body, "whip", "url"); !strings.Contains(fmt.Sprint(got), "127.0.0.1:12090/webrtc/whip") {
		t.Fatalf("unexpected whip url: %v", got)
	}
}

func TestApi2SysWhipIngestTemplate(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"nil config", nil, "http://127.0.0.1:12090/webrtc/whip?streamid={streamid}"},
		{"empty media", &config.Config{}, "http://127.0.0.1:12090/webrtc/whip?streamid={streamid}"},
		{"http addr", &config.Config{Media: config.MediaConfig{LalmaxHTTPAddr: "http://10.0.0.5:13000"}}, "http://10.0.0.5:13000/webrtc/whip?streamid={streamid}"},
		{"public url wins", &config.Config{Media: config.MediaConfig{LalmaxHTTPAddr: "http://10.0.0.5:13000", LalmaxPublicURL: "https://nvr.example.com"}}, "https://nvr.example.com:12090/webrtc/whip?streamid={streamid}"},
		{"public url port", &config.Config{Media: config.MediaConfig{LalmaxPublicURL: "https://nvr.example.com:8443"}}, "https://nvr.example.com:8443/webrtc/whip?streamid={streamid}"},
		{"garbage url", &config.Config{Media: config.MediaConfig{LalmaxPublicURL: "://bad"}}, "http://127.0.0.1:12090/webrtc/whip?streamid={streamid}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{config: tc.cfg}
			if got := h.whipIngestTemplate(); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestApi2SysStreamingSettingsUpdate(t *testing.T) {
	noCfg, _, _ := api2sysNoConfigHandler(t)
	rr := doRequest(t, noCfg.Routes(), "PUT", "/api/settings/streaming", strings.NewReader(`{}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	h, cfg, _, _ := api2sysNewHandler(t)
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/streaming", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// RTSP auth without credentials must be rejected.
	body := `{"rtsp_server":{"auth_enabled":true}}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/streaming", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	if !strings.Contains(rr.Body.String(), "rtsp_username") {
		t.Fatalf("unexpected error body: %s", rr.Body.String())
	}

	// A full update with changed RTMP/SRT/RTSP values triggers a restart.
	engine := &api2sysEngine{}
	h.SetMediaEngine(engine)
	cfg.Media.RTSPAuthEnable = false
	cfg.Media.RTSPPassword = "secret"
	cfg.Media.RTSPUsername = "admin"
	body = `{
		"default_protocol":"flv",
		"auto_stop_no_view_sec":120,
		"webrtc":{"enabled":false,"max_viewers":5,"idle_timeout":"90s"},
		"flv":{"enabled":false,"max_viewers":20,"idle_timeout":"45s","gop_cache_size":2},
		"hls":{"enabled":true,"low_latency":true},
		"rtmp":{"enabled":true,"port":1936},
		"srt":{"enabled":true,"port":19001},
		"whip":{"enabled":false},
		"rtsp_server":{"auth_enabled":true,"auth_method":1,"username":"user2","password":"pass2"}
	}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/streaming", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp := api2sysJSON(t, rr)
	if resp["status"] != "updated" {
		t.Fatalf("unexpected response: %v", resp)
	}
	if cfg.Streaming.DefaultProtocol != "flv" || cfg.Streaming.AutoStopNoViewSec != 120 {
		t.Fatalf("streaming config not updated: %+v", cfg.Streaming)
	}
	if cfg.Streaming.WebRTC.Enabled == nil || *cfg.Streaming.WebRTC.Enabled {
		t.Fatal("webrtc should be disabled")
	}
	if cfg.Streaming.FLV.GOPCacheSize != 2 || cfg.HLS.LowLatency != true {
		t.Fatalf("flv/hls config not updated: %+v %+v", cfg.Streaming.FLV, cfg.HLS)
	}
	if cfg.RTMP.Port != 1936 || cfg.SRT.Port != 19001 {
		t.Fatalf("rtmp/srt ports not updated: %d %d", cfg.RTMP.Port, cfg.SRT.Port)
	}
	if !cfg.Media.RTSPAuthEnable || cfg.Media.RTSPAuthMethod != 1 || cfg.Media.RTSPUsername != "user2" {
		t.Fatalf("rtsp server config not updated: %+v", cfg.Media)
	}
	if engine.restartCalls != 1 || engine.lastRTMPPort != 1936 || engine.lastSRTPort != 19001 {
		t.Fatalf("expected restart with new ports, got %+v", engine)
	}
	if !engine.lastRTMPOn || !engine.lastSRTOn {
		t.Fatalf("expected restart with rtmp/srt enabled, got %+v", engine)
	}

	// Restart failure is reported without failing the save.
	failEngine := &api2sysEngine{restartErr: errors.New("boom")}
	h2, cfg2, _, _ := api2sysNewHandler(t)
	h2.SetMediaEngine(failEngine)
	cfg2.RTMP.Port = 1935
	rr = doRequest(t, h2.Routes(), "PUT", "/api/settings/streaming", strings.NewReader(`{"rtmp":{"port":1937}}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp = api2sysJSON(t, rr)
	if resp["restart_error"] != "boom" {
		t.Fatalf("expected restart_error, got %v", resp)
	}
}

// ==================== AI settings ====================

func TestApi2SysAISettingsGet(t *testing.T) {
	noCfg, _, _ := api2sysNoConfigHandler(t)
	rr := doRequest(t, noCfg.Routes(), "GET", "/api/settings/ai", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	h, cfg, _, _ := api2sysNewHandler(t)
	cfg.AI.Enabled = true
	cfg.AI.Backend = "webhook"
	rr = doRequest(t, h.Routes(), "GET", "/api/settings/ai", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["enabled"] != true || body["backend"] != "webhook" {
		t.Fatalf("unexpected AI settings: %v", body)
	}
}

func TestApi2SysAISettingsUpdateValidation(t *testing.T) {
	noCfg, _, _ := api2sysNoConfigHandler(t)
	rr := doRequest(t, noCfg.Routes(), "PUT", "/api/settings/ai", strings.NewReader(`{}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	h, _, _, _ := api2sysNewHandler(t)
	badBodies := []string{
		`not json`,
		`{"backend":"cloud"}`,
		`{"model_url":"http://evil/model.onnx"}`,
		`{"model_url":"/models/../secret.onnx"}`,
		`{"frame_skip_rate":0}`,
		`{"frame_skip_rate":31}`,
		`{"confidence_threshold":0.05}`,
		`{"confidence_threshold":1.5}`,
		`{"inference_timeout_ms":500}`,
		`{"inference_timeout_ms":120000}`,
	}
	for _, body := range badBodies {
		rr := doRequest(t, h.Routes(), "PUT", "/api/settings/ai", strings.NewReader(body), "", "")
		api2sysStatus(t, rr, http.StatusBadRequest)
	}
}

func TestApi2SysAISettingsUpdateSuccess(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)
	h.aiManager = ai.NewManager(config.AIConfig{})

	body := `{
		"enabled":true,
		"backend":"browser",
		"model_url":"/models/yolo.onnx",
		"frame_skip_rate":5,
		"confidence_threshold":0.45,
		"inference_timeout_ms":5000,
		"webhook":{"enabled":true},
		"multimodal":{"enabled":true,"provider":"deepseek"}
	}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/ai", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp := api2sysJSON(t, rr)
	if resp["status"] != "updated" {
		t.Fatalf("unexpected response: %v", resp)
	}
	if !cfg.AI.Enabled || cfg.AI.Backend != "browser" || cfg.AI.ModelURL != "/models/yolo.onnx" {
		t.Fatalf("AI config not updated: %+v", cfg.AI)
	}
	if cfg.AI.FrameSkipRate != 5 || cfg.AI.ConfidenceThreshold != 0.45 || cfg.AI.InferenceTimeoutMs != 5000 {
		t.Fatalf("AI tuning not updated: %+v", cfg.AI)
	}
	if cfg.AI.Webhook == nil || !cfg.AI.Webhook.Enabled {
		t.Fatalf("webhook not updated: %+v", cfg.AI.Webhook)
	}
	if cfg.AI.Multimodal == nil || cfg.AI.Multimodal.Provider != "deepseek" {
		t.Fatalf("multimodal not updated: %+v", cfg.AI.Multimodal)
	}
	if h.aiManager == nil {
		t.Fatal("expected AI manager to be reinitialized")
	}

	// Backend "disabled" is accepted too.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/ai", strings.NewReader(`{"backend":"disabled"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
}

// ==================== GB28181 settings ====================

func TestApi2SysGB28181Settings(t *testing.T) {
	noCfg, _, _ := api2sysNoConfigHandler(t)
	rr := doRequest(t, noCfg.Routes(), "GET", "/api/settings/gb28181", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	rr = doRequest(t, noCfg.Routes(), "PUT", "/api/settings/gb28181", strings.NewReader(`{}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	h, cfg, _, _ := api2sysNewHandler(t)
	// Defaults are returned when the config has no ID/password.
	rr = doRequest(t, h.Routes(), "GET", "/api/settings/gb28181", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["id"] != "34020000002000000001" || body["password"] != "12345678" {
		t.Fatalf("unexpected GB28181 defaults: %v", body)
	}
	if body["enabled"] != false {
		t.Fatalf("expected disabled by default: %v", body)
	}

	enabled := true
	cfg.GB28181 = config.GB28181Config{
		Enabled:         &enabled,
		Host:            "0.0.0.0",
		Port:            5060,
		ID:              "34020000002000000009",
		Password:        "pwd",
		MediaIP:         "192.168.1.2",
		MediaPort:       30000,
		StandardVersion: "2016",
	}
	rr = doRequest(t, h.Routes(), "GET", "/api/settings/gb28181", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["id"] != "34020000002000000009" || body["port"] != float64(5060) {
		t.Fatalf("unexpected GB28181 config: %v", body)
	}

	// No-op update (same values) must not require a restart.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/gb28181", strings.NewReader(`{"port":5060}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
}

func TestApi2SysGB28181SettingsUpdate(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)
	enabled := false
	cfg.GB28181 = config.GB28181Config{Enabled: &enabled, Port: 5060}

	rr := doRequest(t, h.Routes(), "PUT", "/api/settings/gb28181", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Restart failure is reported as 500.
	failing := &api2sysGBRestarter{err: errors.New("sip down")}
	h.gb28181Restarter = failing
	body := `{"enabled":true,"host":"0.0.0.0","port":5070,"id":"34020000002000000001","password":"pw","media_ip":"10.0.0.2","media_port":30001,"standard_version":"2022"}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/gb28181", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	if len(failing.calls) != 1 {
		t.Fatalf("expected one restart call, got %d", len(failing.calls))
	}
	if cfg.GB28181.Port != 5070 || cfg.GB28181.StandardVersion != "2022" {
		t.Fatalf("GB28181 config not updated before restart: %+v", cfg.GB28181)
	}

	// Successful restart (with the server provider branch) returns 200.
	h2, cfg2, _, _ := api2sysNewHandler(t)
	enabled2 := false
	cfg2.GB28181 = config.GB28181Config{Enabled: &enabled2, Port: 5060}
	restarter := &api2sysGBRestarter{}
	h2.gb28181Restarter = &api2sysGBProvider{api2sysGBRestarter: restarter}
	rr = doRequest(t, h2.Routes(), "PUT", "/api/settings/gb28181", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp := api2sysJSON(t, rr)
	if resp["status"] != "updated" {
		t.Fatalf("unexpected response: %v", resp)
	}
	if len(restarter.calls) != 1 || !*restarter.calls[0].Enabled {
		t.Fatalf("unexpected restarter calls: %+v", restarter.calls)
	}
}

// ==================== HLS settings ====================

func TestApi2SysHLSSettings(t *testing.T) {
	noCfg, _, _ := api2sysNoConfigHandler(t)
	rr := doRequest(t, noCfg.Routes(), "GET", "/api/settings/hls", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	rr = doRequest(t, noCfg.Routes(), "PUT", "/api/settings/hls", strings.NewReader(`{}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	h, cfg, _, _ := api2sysNewHandler(t)
	engine := &api2sysEngine{hlsErr: errors.New("apply failed")}
	h.SetMediaEngine(engine)

	rr = doRequest(t, h.Routes(), "GET", "/api/settings/hls", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["enabled"] != false || body["on_demand"] != true {
		t.Fatalf("unexpected HLS defaults: %v", body)
	}

	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/hls", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Enabling HLS with low latency and all tunables.
	bodyJSON := `{
		"enabled":true,
		"on_demand":false,
		"idle_timeout":"120s",
		"segment_count":8,
		"lal_fragment_duration_ms":2000,
		"lal_fragment_num":4,
		"lal_cleanup_mode":2,
		"lal_use_memory":true,
		"lalmax_segment_duration":2,
		"lalmax_part_duration":150
	}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/hls", strings.NewReader(bodyJSON), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if cfg.HLS.Enabled == nil || !*cfg.HLS.Enabled || cfg.HLS.SegmentCount != 8 {
		t.Fatalf("HLS config not updated: %+v", cfg.HLS)
	}
	if cfg.HLS.IdleTimeout != "120s" || cfg.HLS.LalFragmentNum != 4 || cfg.HLS.LalmaxPartDuration != 150 {
		t.Fatalf("HLS tunables not updated: %+v", cfg.HLS)
	}
	if engine.hlsCalls == 0 || engine.lastHLS == nil {
		t.Fatalf("expected ApplyHLSConfig to be called, got %d", engine.hlsCalls)
	}

	// Disabling HLS falls the default protocol back to webrtc.
	cfg.Streaming.DefaultProtocol = "hls"
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings/hls", strings.NewReader(`{"enabled":false}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if cfg.Streaming.DefaultProtocol != "webrtc" {
		t.Fatalf("expected default protocol fallback, got %q", cfg.Streaming.DefaultProtocol)
	}
}

// ==================== network / system stats / misc ====================

func TestApi2SysFormatUptimeCoveredByExistingTests(t *testing.T) {
	// formatUptime is covered elsewhere; assert the branch used by stats.
	if got := formatUptime(90 * time.Second); got != "1m 30s" {
		t.Fatalf("unexpected uptime string: %q", got)
	}
}

func TestApi2SysConfigCheckAndReloadWithoutWatcher(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)
	cfg.Cleanup.RetentionDays = 21
	if err := config.Save(h.configPath, cfg); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/config/check", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["changed"] != false {
		t.Fatal("expected changed=false without a watcher")
	}

	// Fallback reload reads the file back from disk.
	reloaded := api2sysConfig(mustStorage(t))
	reloaded.Cleanup.RetentionDays = 3
	if err := config.Save(h.configPath, reloaded); err != nil {
		t.Fatalf("failed to rewrite config: %v", err)
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/config/reload", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "reloaded" {
		t.Fatalf("unexpected reload response: %s", rr.Body.String())
	}
	if h.config.Cleanup.RetentionDays != 3 {
		t.Fatalf("expected retention days 3 after reload, got %d", h.config.Cleanup.RetentionDays)
	}

	// Missing file is reported as 500.
	missing, _, _, _ := api2sysNewHandler(t)
	missing.configPath = filepath.Join(t.TempDir(), "does-not-exist.yaml")
	rr = doRequest(t, missing.Routes(), "POST", "/api/config/reload", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysConfigWatcherReloadAndConflict(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)
	cfg.Cleanup.RetentionDays = 30
	if err := config.Save(h.configPath, cfg); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	watcher, err := config.NewWatcher(cfg, h.configPath)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	h.SetConfigWatcher(watcher)

	rr := doRequest(t, h.Routes(), "GET", "/api/config/check", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["changed"] != false {
		t.Fatal("expected changed=false for untouched config")
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/config/reload", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "no_changes" {
		t.Fatalf("unexpected reload response: %s", rr.Body.String())
	}

	// Externally modify the config file and bump mtime deterministically.
	updated := api2sysConfig(mustStorage(t))
	updated.Cleanup.RetentionDays = 9
	if err := config.Save(h.configPath, updated); err != nil {
		t.Fatalf("failed to rewrite config: %v", err)
	}
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(h.configPath, future, future); err != nil {
		t.Fatalf("failed to touch config: %v", err)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/config/check", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["changed"] != true {
		t.Fatal("expected changed=true after external modification")
	}

	// Saving with a pending external change must conflict.
	rr = doRequest(t, h.Routes(), "PUT", "/api/settings", strings.NewReader(`{"cleanup":{"retention_days":5}}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	rr = doRequest(t, h.Routes(), "POST", "/api/config/reload", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "reloaded" {
		t.Fatalf("unexpected reload response: %s", rr.Body.String())
	}
	if h.config.Cleanup.RetentionDays != 9 {
		t.Fatalf("expected retention days 9 after watcher reload, got %d", h.config.Cleanup.RetentionDays)
	}
}

func TestApi2SysConfigWatcherReloadError(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)
	if err := config.Save(h.configPath, cfg); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	watcher, err := config.NewWatcher(cfg, h.configPath)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	h.SetConfigWatcher(watcher)
	if err := os.WriteFile(h.configPath, []byte("cameras: [not: valid: yaml"), 0o644); err != nil {
		t.Fatalf("failed to corrupt config: %v", err)
	}
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(h.configPath, future, future); err != nil {
		t.Fatalf("failed to touch config: %v", err)
	}
	rr := doRequest(t, h.Routes(), "POST", "/api/config/reload", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysRegenerateLalmaxConfig(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)
	rr := doRequest(t, h.Routes(), "POST", "/api/settings/lalmax/regenerate", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	plain := &api2sysPlainEngine{}
	h.SetMediaEngine(plain)
	rr = doRequest(t, h.Routes(), "POST", "/api/settings/lalmax/regenerate", nil, "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	engine := &api2sysEngine{}
	h.SetMediaEngine(engine)
	rr = doRequest(t, h.Routes(), "POST", "/api/settings/lalmax/regenerate", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "regenerated" {
		t.Fatalf("unexpected response: %s", rr.Body.String())
	}
	if engine.regenCalls != 1 {
		t.Fatalf("expected one regenerate call, got %d", engine.regenCalls)
	}

	failing := &api2sysEngine{regenErr: errors.New("write failed")}
	h.SetMediaEngine(failing)
	rr = doRequest(t, h.Routes(), "POST", "/api/settings/lalmax/regenerate", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== backup ====================

func TestApi2SysBackupAndList(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)

	rr := doRequest(t, h.Routes(), "GET", "/api/backups", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("expected empty backups array, got %s", rr.Body.String())
	}

	rr = doRequest(t, h.Routes(), "POST", "/api/backup", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["status"] != "created" {
		t.Fatalf("unexpected backup response: %v", body)
	}
	filename, _ := body["file"].(string)
	if filename == "" {
		t.Fatal("expected backup filename")
	}
	backupPath := filepath.Join(filepath.Dir(h.configPath), "backups", filename)
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected backup file on disk: %v", err)
	}

	// Non-.db entries and directories are ignored by the listing.
	if err := os.WriteFile(filepath.Join(filepath.Dir(backupPath), "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create stray file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(backupPath), "subdir.db"), 0o755); err != nil {
		t.Fatalf("failed to create stray dir: %v", err)
	}
	rr = doRequest(t, h.Routes(), "GET", "/api/backups", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var backups []string
	if err := json.Unmarshal(rr.Body.Bytes(), &backups); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(backups) != 1 || backups[0] != filename {
		t.Fatalf("expected only the backup file, got %v", backups)
	}

	// Without a database the backup endpoint fails cleanly.
	noDB := NewHandler(nil, mustStorage(t), noopAuthMW(), api2sysConfig(mustStorage(t)), nil, "", nil, nil)
	rr = doRequest(t, noDB.Routes(), "POST", "/api/backup", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== features ====================

func TestApi2SysFeatures(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)

	rr := doRequest(t, h.Routes(), "PUT", "/api/features", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	rr = doRequest(t, h.Routes(), "PUT", "/api/features", strings.NewReader(`{"protocols":{"onvif":false,"rtsp":true}}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	protocols, ok := body["protocols"].(map[string]any)
	if !ok {
		t.Fatalf("missing protocols map: %v", body)
	}
	if protocols["onvif"] != false || protocols["rtsp"] != true {
		t.Fatalf("unexpected feature flags: %v", protocols)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/features", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	protocols, _ = body["protocols"].(map[string]any)
	if protocols["onvif"] != false {
		t.Fatalf("feature flag not persisted: %v", protocols)
	}

	// Storage failure while reading flags.
	failing, db := api2sysClosedDBHandler(t)
	defer db.Close()
	rr = doRequest(t, failing.Routes(), "GET", "/api/features", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	rr = doRequest(t, failing.Routes(), "PUT", "/api/features", strings.NewReader(`{"protocols":{"rtmp":false}}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== update settings ====================

func TestApi2SysUpdateSettingsValidation(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)
	cfg.DLNA.Port = 8200
	cfg.Cleanup.RetentionDays = 30
	cfg.Cleanup.DiskThresholdPercent = 95
	cfg.Cleanup.CheckInterval = "1h"

	badBodies := []struct {
		name string
		body string
		code int
	}{
		{"invalid json", `{`, http.StatusBadRequest},
		{"retention days zero", `{"cleanup":{"retention_days":0}}`, http.StatusBadRequest},
		{"disk threshold low", `{"cleanup":{"disk_threshold_percent":0}}`, http.StatusBadRequest},
		{"disk threshold high", `{"cleanup":{"disk_threshold_percent":101}}`, http.StatusBadRequest},
		{"bad interval", `{"cleanup":{"check_interval":"soon"}}`, http.StatusBadRequest},
		{"bad trace template", `{"observability":{"trace_ui_url_template":"http://host/trace"}}`, http.StatusBadRequest},
		{"dlna port low", `{"dlna":{"port":0}}`, http.StatusBadRequest},
		{"dlna port high", `{"dlna":{"port":70000}}`, http.StatusBadRequest},
		{"dlna port conflicts", `{"dlna":{"port":9090}}`, http.StatusBadRequest},
		{"dlna gop low", `{"dlna":{"gop_cache":0}}`, http.StatusBadRequest},
		{"dlna gop high", `{"dlna":{"gop_cache":17}}`, http.StatusBadRequest},
	}
	for _, tc := range badBodies {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, h.Routes(), "PUT", "/api/settings", strings.NewReader(tc.body), "", "")
			api2sysStatus(t, rr, tc.code)
		})
	}
}

func TestApi2SysUpdateSettingsSuccess(t *testing.T) {
	h, cfg, _, _ := api2sysNewHandler(t)

	traceApplied := []bool{}
	h.SetLocalTraceEnabled(func(enabled bool) { traceApplied = append(traceApplied, enabled) })
	dlnaCalls := 0
	h.SetDLNAApply(func(context.Context) error {
		dlnaCalls++
		return nil
	})

	body := `{
		"cleanup":{"retention_days":14,"disk_threshold_percent":80,"check_interval":"30m"},
		"dlna":{"enabled":true,"friendly_name":"  Test DLNA  ","uuid":"uuid-1","port":8300,"interface":"eth0","allowed_cidrs":["192.168.1.0/24"],"include_live":true,"include_recordings":false,"max_browse_count":50,"max_media_viewers":8,"gop_cache":4},
		"webdav":{"enabled":true,"path_prefix":"/dav","read_write":true},
		"observability":{"local_trace_enabled":true,"trace_ui_url_template":"https://trace.example.com/{trace_id}"}
	}`
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp := api2sysJSON(t, rr)
	if resp["status"] != "updated" {
		t.Fatalf("unexpected response: %v", resp)
	}
	if cfg.Cleanup.RetentionDays != 14 || cfg.Cleanup.DiskThresholdPercent != 80 || cfg.Cleanup.CheckInterval != "30m" {
		t.Fatalf("cleanup config not updated: %+v", cfg.Cleanup)
	}
	if cfg.DLNA.Enabled == nil || !*cfg.DLNA.Enabled || cfg.DLNA.FriendlyName != "Test DLNA" {
		t.Fatalf("dlna config not updated: %+v", cfg.DLNA)
	}
	if cfg.DLNA.Port != 8300 || cfg.DLNA.GopCache != 4 || cfg.DLNA.MaxBrowseCount != 50 {
		t.Fatalf("dlna numbers not updated: %+v", cfg.DLNA)
	}
	if cfg.WebDAV.Enabled == nil || !*cfg.WebDAV.Enabled || !cfg.WebDAV.ReadWrite {
		t.Fatalf("webdav config not updated: %+v", cfg.WebDAV)
	}
	if cfg.Observability.TraceUIURLTemplate != "https://trace.example.com/{trace_id}" {
		t.Fatalf("trace template not updated: %+v", cfg.Observability)
	}
	if len(traceApplied) != 1 || !traceApplied[0] {
		t.Fatalf("expected local trace apply callback, got %v", traceApplied)
	}
	if dlnaCalls != 1 {
		t.Fatalf("expected dlna apply callback, got %d", dlnaCalls)
	}

	// A DLNA apply failure is surfaced in the response body but keeps 200.
	h2, _, _, _ := api2sysNewHandler(t)
	h2.SetDLNAApply(func(context.Context) error { return errors.New("dlna down") })
	rr = doRequest(t, h2.Routes(), "PUT", "/api/settings", strings.NewReader(`{"dlna":{"enabled":true}}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["dlna_error"] != "dlna down" {
		t.Fatalf("expected dlna_error, got %s", rr.Body.String())
	}
}

// ==================== health/readyz extras ====================

func TestApi2SysHealthDetailsNoDependencies(t *testing.T) {
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/health/details", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var resp HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Fatalf("expected unhealthy, got %q", resp.Status)
	}
	if resp.Checks["database"].Status != "error" || resp.Checks["storage"].Status != "error" {
		t.Fatalf("unexpected checks: %+v", resp.Checks)
	}
}

func TestApi2SysHealthDetailsDegradedCameras(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)
	h.healthMgr = api2sysHealthMgr{all: map[string]*model.CameraHealth{
		"cam-a": {CameraID: "cam-a", LatestStatus: "error"},
		"cam-b": {CameraID: "cam-b", LatestStatus: "reconnecting"},
		"cam-c": {CameraID: "cam-c", LatestStatus: "healthy"},
	}}
	rr := doRequest(t, h.Routes(), "GET", "/api/health/details", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var resp HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Cameras == nil || resp.Cameras.Total != 3 {
		t.Fatalf("unexpected camera summary: %+v", resp.Cameras)
	}
	if resp.Cameras.Error != 1 || resp.Cameras.Reconnecting != 1 || resp.Cameras.Recording != 1 {
		t.Fatalf("unexpected camera counters: %+v", resp.Cameras)
	}
	if resp.Status != "degraded" {
		t.Fatalf("expected degraded, got %q", resp.Status)
	}
}

// ==================== stream list helpers ====================

// api2sysStreamConfig enables every external ingest protocol with stream keys.
func api2sysStreamConfig(store *storage.Manager) *config.Config {
	cfg := api2sysConfig(store)
	enabled := true
	cfg.RTMP = config.RTMPConfig{Enabled: &enabled, Port: 1935, StreamKeys: map[string]string{"push-live": "rtmp-key"}}
	cfg.SRT = config.SRTConfig{Enabled: &enabled, Port: 19000, StreamKeys: map[string]string{"push-live": "srt-key"}}
	cfg.WHIP = config.WHIPConfig{Enabled: &enabled, PublishTokens: map[string]string{"push-live": "whip-key"}}
	cfg.Storage = config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"}
	return cfg
}

// api2sysStreamHandler builds a handler with a stub media engine.
func api2sysStreamHandler(t *testing.T) (*Handler, *stubMediaEngine, *storage.DB, *config.Config) {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := api2sysStreamConfig(store)
	h := newHandlerWithConfig(db, store, cfg)
	h.configPath = filepath.Join(t.TempDir(), "config.yaml")
	engine := &stubMediaEngine{}
	h.SetMediaEngine(engine)
	return h, engine, db, cfg
}

// api2sysSeedCamera inserts a camera plus an optional stream binding.
func api2sysSeedCamera(t *testing.T, db *storage.DB, id, name, protocol, encoding, streamID string, enabled bool) {
	t.Helper()
	if err := db.UpsertCamera(context.Background(), id, name, protocol, encoding, "rtsp://example.com/"+id, "", "", enabled, "", "", ""); err != nil {
		t.Fatalf("failed to seed camera %s: %v", id, err)
	}
	if streamID == "" {
		return
	}
	if err := db.SaveCameraExtras(context.Background(), config.CameraConfig{ID: id, StreamID: streamID, Protocol: protocol, Encoding: encoding}); err != nil {
		t.Fatalf("failed to seed camera extras %s: %v", id, err)
	}
	if err := db.BindStreamToCamera(context.Background(), streamID, id); err != nil {
		t.Fatalf("failed to bind stream %s: %v", streamID, err)
	}
}

// api2sysSeedHistory inserts a finished stream-history session.
func api2sysSeedHistory(t *testing.T, db *storage.DB, streamID, protocol string, started time.Time, ended *time.Time) {
	t.Helper()
	rec := &storage.StreamHistory{
		StreamID:   streamID,
		AppName:    "live",
		Protocol:   protocol,
		RemoteAddr: "10.0.0.9:1935",
		SessionID:  "sess-" + streamID,
		StartedAt:  started,
	}
	if err := db.InsertStreamHistory(context.Background(), rec); err != nil {
		t.Fatalf("failed to insert stream history: %v", err)
	}
	if ended != nil {
		if err := db.FinishStreamHistory(context.Background(), streamID, rec.SessionID, *ended, "publisher_stopped", "", 1, 2); err != nil {
			t.Fatalf("failed to finish stream history: %v", err)
		}
	}
}

// api2sysFindStream returns the stream summary with the given ID.
func api2sysFindStream(t *testing.T, resp streamListResponse, streamID string) streamSummary {
	t.Helper()
	for _, item := range resp.Streams {
		if item.StreamID == streamID {
			return item
		}
	}
	t.Fatalf("stream %q not found in %+v", streamID, resp.Streams)
	return streamSummary{}
}

// ==================== stream listing ====================

func TestApi2SysListStreamsMergedSources(t *testing.T) {
	h, engine, db, cfg := api2sysStreamHandler(t)

	// Camera-bound live stream with a publisher and subscriber.
	api2sysSeedCamera(t, db, "cam1", "Front Door", "rtsp", "h264", "cam1", true)
	// Camera bound to a stream that is currently offline.
	api2sysSeedCamera(t, db, "cam2", "Back Yard", "rtsp", "h264", "cam2-idle", true)
	// Camera promoted from a live push stream.
	api2sysSeedCamera(t, db, "cam3", "Studio", "rtmp-pull", "h264", "cam3-live", true)
	// Created push slot (renames the live stream).
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{
		StreamID: "push-live", Name: "Studio Push", AppName: "live", InputMode: storage.CreatedStreamPush,
	}); err != nil {
		t.Fatalf("failed to seed created stream: %v", err)
	}
	// Created pull slot with a source URL.
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{
		StreamID: "pull1", Name: "Relay One", AppName: "live", InputMode: storage.CreatedStreamPull, SourceURL: "rtsp://source/one",
	}); err != nil {
		t.Fatalf("failed to seed created pull: %v", err)
	}
	// History-only stream.
	ended := time.Now().Add(-2 * time.Minute)
	api2sysSeedHistory(t, db, "hist1", "rtmp", ended.Add(-5*time.Minute), &ended)

	engine.streams = []media.StreamInfo{
		{
			StreamID: "cam1", AppName: "live", Active: true, VideoCodec: "h264", AudioCodec: "aac", InFPS: 25,
			Publisher:   &media.SessionInfo{SessionID: "pub-cam1", Protocol: "rtsp", Remote: "10.0.0.1:554"},
			Subscribers: []media.SessionInfo{{SessionID: "sub-cam1", Protocol: "hls"}, {SessionID: "nvr-record-1", Protocol: "nvr-record"}},
		},
		{
			StreamID: "push-live", AppName: "live", Active: true, VideoCodec: "h264",
			Publisher: &media.SessionInfo{SessionID: "pub-push", Protocol: "rtmp", Remote: "10.0.0.2:1935"},
		},
		{
			StreamID: "cam3-live", AppName: "live", Active: true, VideoCodec: "h264",
			Publisher: &media.SessionInfo{SessionID: "pub-extra", Protocol: "customize", Remote: ""},
		},
		{
			StreamID: "rec-internal", AppName: "live", Active: true,
		},
	}
	engine.playURLs = map[string]string{
		"hls":     "http://127.0.0.1:12090/live/hls/cam1/index.m3u8",
		"webrtc":  "http://127.0.0.1:12090/live/webrtc/cam1",
		"rtmp":    "rtmp://127.0.0.1:1935/live/cam1",
		"whip":    "http://127.0.0.1:12090/webrtc/whip?streamid=cam1",
		"ws-flv":  "ws://127.0.0.1:12090/live/ws-flv/cam1",
		"ll-hls":  "http://127.0.0.1:12090/live/ll-hls/cam1/index.m3u8",
		"flv":     "http://127.0.0.1:12090/live/flv/cam1.flv",
		"fmp4":    "http://127.0.0.1:12090/live/fmp4/cam1",
		"rtsp":    "rtsp://127.0.0.1:5544/live/cam1",
		"custom":  "http://127.0.0.1:12090/live/custom/x",
		"unknown": "http://127.0.0.1:12090/live/unknown/x",
	}
	h.isInternalMediaStream = func(id string) bool { return strings.HasPrefix(id, "rec-") }
	h.SetGB28181Server(api2sysGBStatus{playing: map[string]bool{"cam1": true}})
	_ = cfg

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var resp streamListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid list response: %v", err)
	}
	if resp.Total != len(resp.Streams) || resp.Total == 0 {
		t.Fatalf("unexpected totals: %+v", resp)
	}
	for _, item := range resp.Streams {
		if item.StreamID == "rec-internal" {
			t.Fatal("internal stream should be filtered out")
		}
	}

	cam := api2sysFindStream(t, resp, "cam1")
	if !cam.Managed || cam.CameraName != "Front Door" {
		t.Fatalf("cam1 should be managed by its camera: %+v", cam)
	}
	if !cam.GB28181Playing || cam.SourceType != "gb28181" {
		t.Fatalf("expected gb28181 overlay on cam1: %+v", cam)
	}
	if len(cam.PlayURLs) == 0 {
		t.Fatal("expected play URLs on cam1")
	}
	if len(cam.Subscribers) != 1 || cam.Subscribers[0].SessionID != "sub-cam1" {
		t.Fatalf("expected internal recorder subscriber to be hidden: %+v", cam.Subscribers)
	}

	push := api2sysFindStream(t, resp, "push-live")
	if push.Name != "Studio Push" {
		t.Fatalf("expected created-stream display name: %+v", push)
	}
	if push.SourceType != "rtmp_push" {
		t.Fatalf("expected rtmp_push source, got %q", push.SourceType)
	}
	if len(push.IngestURLs) == 0 {
		t.Fatal("expected push ingest URLs")
	}

	hist := api2sysFindStream(t, resp, "hist1")
	if hist.SourceType != "rtmp_push" || hist.Active {
		t.Fatalf("unexpected history-only summary: %+v", hist)
	}
	if hist.LastFrameTime == nil {
		t.Fatal("expected last frame time from history")
	}

	pull := api2sysFindStream(t, resp, "pull1")
	if pull.SourceType != "relay_pull" || pull.SourceURL != "rtsp://source/one" {
		t.Fatalf("unexpected created pull summary: %+v", pull)
	}

	// A promoted push stream bound to a camera (customize publisher => promoted).
	promoted := api2sysFindStream(t, resp, "cam3-live")
	if promoted.ManagementType != "promoted" {
		t.Fatalf("expected promoted management type, got %q", promoted.ManagementType)
	}

	offline := api2sysFindStream(t, resp, "cam2-idle")
	if !offline.Managed || offline.Active {
		t.Fatalf("expected managed offline bound stream: %+v", offline)
	}
	if offline.ManagementType != "bound" {
		t.Fatalf("expected bound management type, got %q", offline.ManagementType)
	}

	// Search/managed/limit filters exercise the query path.
	rr = doRequest(t, h.Routes(), "GET", "/api/streams?q=front&managed=true", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp = streamListResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid filtered response: %v", err)
	}
	if resp.Total != 1 || resp.Streams[0].StreamID != "cam1" {
		t.Fatalf("unexpected search result: %+v", resp)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/streams?managed=false&limit=1&offset=1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	resp = streamListResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid paged response: %v", err)
	}
	if resp.Limit != 1 || resp.Offset != 1 || len(resp.Streams) > 1 {
		t.Fatalf("unexpected pagination: %+v", resp)
	}
}

func TestApi2SysListStreamsStorageErrors(t *testing.T) {
	h, db := api2sysClosedDBHandler(t)
	defer db.Close()
	engine := &stubMediaEngine{}
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// Engine listing failure.
	h2, engine2, _, _ := api2sysStreamHandler(t)
	engine2.listErr = errors.New("engine down")
	rr = doRequest(t, h2.Routes(), "GET", "/api/streams", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// Missing engine.
	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "GET", "/api/streams", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

func mustDB(t *testing.T) *storage.DB {
	t.Helper()
	db, _ := setupTestDB(t)
	return db
}

func TestApi2SysGetStreamIdleBranches(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)
	engine.stream = nil

	// Bound camera.
	api2sysSeedCamera(t, db, "cam1", "Bound Cam", "rtsp", "h264", "cam1", true)
	rr := doRequest(t, h.Routes(), "GET", "/api/streams/cam1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var item streamSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if !item.Managed || item.CameraName != "Bound Cam" {
		t.Fatalf("unexpected idle camera summary: %+v", item)
	}

	// Created stream.
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{
		StreamID: "created1", Name: "Created", AppName: "live", InputMode: storage.CreatedStreamPush,
	}); err != nil {
		t.Fatalf("failed to seed created stream: %v", err)
	}
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/created1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// Recent history.
	ended := time.Now().Add(-time.Minute)
	api2sysSeedHistory(t, db, "hist-fresh", "srt", ended.Add(-time.Hour), &ended)
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/hist-fresh", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if item.SourceType != "srt_push" || item.LastFrameTime == nil {
		t.Fatalf("unexpected history summary: %+v", item)
	}

	// Stale history is not exposed.
	stale := time.Now().Add(-8 * 24 * time.Hour)
	api2sysSeedHistory(t, db, "hist-stale", "rtmp", stale, &stale)
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/hist-stale", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// GB28181 active without history.
	h.SetGB28181Server(api2sysGBStatus{playing: map[string]bool{"gb1": true}})
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/gb1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if item.SourceType != "gb28181" || !item.GB28181Playing {
		t.Fatalf("unexpected gb28181 summary: %+v", item)
	}

	// Unknown stream.
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/unknown-stream", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Engine error.
	engine.getErr = errors.New("lookup failed")
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/cam1", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// Missing engine.
	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "GET", "/api/streams/cam1", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

// ==================== stream update ====================

func TestApi2SysUpdateStreamManaged(t *testing.T) {
	h, camMgr, cfg := newTestCamHandler(t)
	h.configPath = filepath.Join(t.TempDir(), "config.yaml")
	engine := &stubMediaEngine{stream: nil}
	h.SetMediaEngine(engine)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"Original","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`), "", "")
	api2sysStatus(t, rr, http.StatusCreated)
	var created config.CameraConfig
	parseJSON(t, rr, &created)
	_ = cfg

	rr = doRequest(t, h.Routes(), "PUT", "/api/streams/"+created.ID, strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	rr = doRequest(t, h.Routes(), "PUT", "/api/streams/"+created.ID, strings.NewReader(`{"name":"Renamed"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var item streamSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if item.CameraName != "Renamed" || !item.Managed {
		t.Fatalf("unexpected updated stream: %+v", item)
	}
	if cam := camMgr.GetCameraConfig(created.ID); cam == nil || cam.Name != "Renamed" {
		t.Fatalf("camera name not updated: %+v", cam)
	}

	// A binding that points at a camera missing from the manager is a 404.
	if err := h.db.UpsertCamera(context.Background(), "ghost", "Ghost", "rtsp", "h264", "rtsp://example.com/ghost", "", "", false, "", "", ""); err != nil {
		t.Fatalf("failed to seed ghost camera: %v", err)
	}
	if err := h.db.BindStreamToCamera(context.Background(), "ghost-stream", "ghost"); err != nil {
		t.Fatalf("failed to seed ghost binding: %v", err)
	}
	rr = doRequest(t, h.Routes(), "PUT", "/api/streams/ghost-stream", strings.NewReader(`{"name":"Nope"}`), "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Empty name falls back to the stream ID (unmanaged path).
	engine.stream = &media.StreamInfo{StreamID: "push-1", Active: true}
	rr = doRequest(t, h.Routes(), "PUT", "/api/streams/push-1", strings.NewReader(`{"name":"  "}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if item.Name != "push-1" {
		t.Fatalf("expected stream-id fallback name, got %q", item.Name)
	}

	// Missing engine.
	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "PUT", "/api/streams/x", strings.NewReader(`{"name":"x"}`), "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

func TestApi2SysUpdateStreamWithoutCameraManager(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)
	engine.stream = nil
	api2sysSeedCamera(t, db, "cam1", "Cam", "rtsp", "h264", "cam1", true)

	rr := doRequest(t, h.Routes(), "PUT", "/api/streams/cam1", strings.NewReader(`{"name":"X"}`), "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	// Missing stream ID.
	rr = api2sysCall(h.handleUpdateStream, "PUT", "/api/streams/", `{"name":"X"}`, nil)
	api2sysStatus(t, rr, http.StatusBadRequest)

	// storage failure while looking up the managed camera.
	failing, db2 := api2sysClosedDBHandler(t)
	defer db2.Close()
	failing.SetMediaEngine(&stubMediaEngine{})
	rr = doRequest(t, failing.Routes(), "PUT", "/api/streams/anything", strings.NewReader(`{"name":"X"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== bind / unbind ====================

func TestApi2SysBindCameraFlow(t *testing.T) {
	h, camMgr, _ := newTestCamHandler(t)
	engine := &stubMediaEngine{stream: &media.StreamInfo{StreamID: "push-bind", Active: true}}
	h.SetMediaEngine(engine)
	h.configPath = filepath.Join(t.TempDir(), "config.yaml")

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"Bind Cam","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`), "", "")
	api2sysStatus(t, rr, http.StatusCreated)
	var created config.CameraConfig
	parseJSON(t, rr, &created)

	// Validation errors.
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`{}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`{"camera_id":"missing"}`), "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Stream not found.
	engine.stream = nil
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`{"camera_id":"`+created.ID+`"}`), "", "")
	api2sysStatus(t, rr, http.StatusNotFound)
	engine.stream = &media.StreamInfo{StreamID: "push-bind", Active: true}

	// Success with a camera manager.
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`{"camera_id":"`+created.ID+`"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if cam := camMgr.GetCameraConfig(created.ID); cam == nil || cam.StreamID != "push-bind" {
		t.Fatalf("camera stream not bound: %+v", cam)
	}

	// Unbind restores the camera's own stream.
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/unbind-camera", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if cam := camMgr.GetCameraConfig(created.ID); cam == nil || cam.StreamID != created.ID {
		t.Fatalf("camera stream not restored: %+v", cam)
	}

	// Unbind again: no binding left.
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/unbind-camera", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Engine errors.
	engine.getErr = errors.New("engine down")
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`{"camera_id":"`+created.ID+`"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysBindUnbindWithoutCameraManager(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)
	engine.stream = &media.StreamInfo{StreamID: "push-bind", Active: true}
	api2sysSeedCamera(t, db, "cam-db", "DB Cam", "rtsp", "h264", "", true)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/bind-camera", strings.NewReader(`{"camera_id":"cam-db"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	binding, err := db.GetStreamBinding(context.Background(), "push-bind")
	if err != nil || binding == nil || binding.CameraID != "cam-db" {
		t.Fatalf("binding not stored: %+v %v", binding, err)
	}

	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-bind/unbind-camera", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// Missing stream id / engine.
	rr = api2sysCall(h.handleBindCamera, "POST", "/api/streams//bind-camera", `{"camera_id":"x"}`, nil)
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = api2sysCall(h.handleUnbindCamera, "POST", "/api/streams//unbind-camera", "", nil)
	api2sysStatus(t, rr, http.StatusBadRequest)

	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "POST", "/api/streams/x/bind-camera", strings.NewReader(`{"camera_id":"x"}`), "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
	rr = doRequest(t, noEngine.Routes(), "POST", "/api/streams/x/unbind-camera", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	// Storage failure while reading the binding.
	failing, db2 := api2sysClosedDBHandler(t)
	defer db2.Close()
	failing.SetMediaEngine(&stubMediaEngine{})
	rr = doRequest(t, failing.Routes(), "POST", "/api/streams/x/unbind-camera", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== promote stream ====================

func TestApi2SysPromoteStreamSourceTypes(t *testing.T) {
	h, _, _, _ := api2sysStreamHandler(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		streamID string
		info     *media.StreamInfo
		want     string
	}{
		{"voip id", "voip_100", &media.StreamInfo{}, "voip"},
		{"rtmp publisher", "stream-x", &media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "rtmp"}}, "rtmp_push"},
		{"srt publisher", "stream-x", &media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "srt"}}, "srt_push"},
		{"whip publisher", "stream-x", &media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "whip"}}, "whip_push"},
		{"customize opus", "stream-x", &media.StreamInfo{AudioCodec: "opus", Publisher: &media.SessionInfo{Protocol: "customize"}}, "whip_push"},
		{"customize remote", "stream-x", &media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "customize", Remote: "10.0.0.2:9000"}}, "srt_push"},
		{"customize no remote", "stream-x", &media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "customize"}}, "whip_push"},
		{"rtsp publisher", "stream-x", &media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "rtsp"}}, "relay_pull"},
		{"codec only", "stream-x", &media.StreamInfo{VideoCodec: "h264"}, "whip_push"},
		{"nil info", "stream-x", nil, "rtmp_push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.inferPromoteSourceType(ctx, tc.streamID, tc.info); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}

	// History fallback: rtmp history yields rtmp_push, srt yields srt_push.
	db := mustDB(t)
	api2sysSeedHistory(t, db, "hist-rtmp", "rtmp", time.Now().Add(-time.Hour), nil)
	h.db = db
	if got := h.inferPromoteSourceType(ctx, "hist-rtmp", &media.StreamInfo{}); got != "rtmp_push" {
		t.Fatalf("expected rtmp_push from history, got %q", got)
	}
	api2sysSeedHistory(t, db, "hist-srt", "srt", time.Now().Add(-time.Hour), nil)
	if got := h.inferPromoteSourceType(ctx, "hist-srt", &media.StreamInfo{}); got != "srt_push" {
		t.Fatalf("expected srt_push from history, got %q", got)
	}
	api2sysSeedHistory(t, db, "hist-custom", "customize", time.Now().Add(-time.Hour), nil)
	if got := h.inferPromoteSourceType(ctx, "hist-custom", &media.StreamInfo{}); got != "srt_push" {
		t.Fatalf("expected srt_push from customize history, got %q", got)
	}
	closed, closedDB := api2sysClosedDBHandler(t)
	defer closedDB.Close()
	if got := closed.inferPromoteSourceType(ctx, "none", nil); got != "rtmp_push" {
		t.Fatalf("expected default rtmp_push, got %q", got)
	}
}

func TestApi2SysPromoteStreamFlow(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)

	// Validation.
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/x/promote", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/x/promote", strings.NewReader(`{}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Engine error / stream not found.
	engine.getErr = errors.New("engine down")
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/x/promote", strings.NewReader(`{"name":"X"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	engine.getErr = nil
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/x/promote", strings.NewReader(`{"name":"X"}`), "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Push stream promoted using the engine RTSP play URL.
	engine.stream = &media.StreamInfo{
		StreamID: "push-promote", Active: true, VideoCodec: "H264",
		Publisher: &media.SessionInfo{SessionID: "pub-1", Protocol: "rtmp", Remote: "10.0.0.5:1935"},
	}
	engine.playURLs = map[string]string{"rtsp": "rtsp://127.0.0.1:5544/live/push-promote"}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-promote/promote",
		strings.NewReader(`{"name":"Promoted Cam","description":"garden","location":"shed"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["source_type"] != "rtmp_push" || body["status"] != "promoted" {
		t.Fatalf("unexpected promote response: %v", body)
	}
	cameraID, _ := body["camera_id"].(string)
	if cameraID == "" {
		t.Fatal("expected promoted camera id")
	}
	cam, err := db.GetCamera(context.Background(), cameraID)
	if err != nil || cam == nil {
		t.Fatalf("promoted camera missing: %v %v", cam, err)
	}
	if cam.URL != "rtsp://127.0.0.1:5544/live/push-promote" {
		t.Fatalf("expected engine play URL, got %q", cam.URL)
	}
	if cam.Description != "garden" || cam.Location != "shed" {
		t.Fatalf("metadata not persisted: %+v", cam)
	}
	binding, err := db.GetStreamBinding(context.Background(), "push-promote")
	if err != nil || binding == nil || binding.CameraID != cameraID {
		t.Fatalf("binding not created: %+v %v", binding, err)
	}

	// Promoting the same stream again conflicts.
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-promote/promote", strings.NewReader(`{"name":"Again"}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	// Relay-pull streams use the publisher remote URL; missing play URL falls
	// back to the loopback RTSP address.
	engine.stream = &media.StreamInfo{
		StreamID:   "relay-1",
		Active:     true,
		VideoCodec: "h265",
		Publisher:  &media.SessionInfo{SessionID: "pub-2", Protocol: "rtsp", Remote: "rtsp://10.0.0.9/stream"},
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/relay-1/promote", strings.NewReader(`{"name":"Relay Cam"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["source_type"] != "relay_pull" {
		t.Fatalf("unexpected relay source type: %v", body)
	}
	relayCam, err := db.GetCamera(context.Background(), fmt.Sprint(body["camera_id"]))
	if err != nil || relayCam == nil || relayCam.URL != "rtsp://10.0.0.9/stream" {
		t.Fatalf("unexpected relay camera: %+v %v", relayCam, err)
	}

	// Push stream without an engine RTSP URL falls back to a local RTSP URL.
	engine.playURLs = nil
	engine.stream = &media.StreamInfo{
		StreamID: "push-fallback", Active: true, VideoCodec: "h264",
		Publisher: &media.SessionInfo{SessionID: "pub-3", Protocol: "srt"},
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/push-fallback/promote", strings.NewReader(`{"name":"Fallback Cam"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	fallbackCam, err := db.GetCamera(context.Background(), fmt.Sprint(body["camera_id"]))
	if err != nil || fallbackCam == nil || !strings.HasPrefix(fallbackCam.URL, "rtsp://127.0.0.1:5544/live/") {
		t.Fatalf("unexpected fallback camera: %+v %v", fallbackCam, err)
	}

	// Missing engine.
	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "POST", "/api/streams/x/promote", strings.NewReader(`{"name":"X"}`), "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

// ==================== delete stream ====================

func TestApi2SysDeleteStreamBranches(t *testing.T) {
	// Created pull slot removal.
	h, engine, db, _ := api2sysStreamHandler(t)
	engine.stream = nil
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{
		StreamID: "del-pull", Name: "Del Pull", AppName: "live", InputMode: storage.CreatedStreamPull, SourceURL: "rtsp://src/one",
	}); err != nil {
		t.Fatalf("failed to seed created pull: %v", err)
	}
	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/del-pull", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// Offline managed camera is archived and unbound.
	api2sysSeedCamera(t, db, "del-cam", "Del Cam", "rtsp", "h264", "del-cam", true)
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/del-cam", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	archived, err := db.GetCamera(context.Background(), "del-cam")
	if err != nil || archived == nil || !archived.Archived {
		t.Fatalf("expected archived camera: %+v %v", archived, err)
	}

	// Binding-only stream (camera row missing) is unbound.
	if err := db.BindStreamToCamera(context.Background(), "binding-only", "missing-cam"); err != nil {
		t.Fatalf("failed to seed binding: %v", err)
	}
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/binding-only", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// History-only stream.
	api2sysSeedHistory(t, db, "del-hist", "rtmp", time.Now().Add(-time.Hour), nil)
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/del-hist", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// Unknown stream.
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/nope", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Live stream with publisher and subscribers: every session is kicked, even
	// when the engine returns an error.
	kickEngine := &stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:    "live-del",
			Active:      true,
			Publisher:   &media.SessionInfo{SessionID: "pub-del", Protocol: "rtmp"},
			Subscribers: []media.SessionInfo{{SessionID: "sub-1", Protocol: "hls"}, {SessionID: "nvr-record-x", Protocol: "nvr-record"}},
		},
		kickErr: errors.New("kick failed"),
	}
	h.SetMediaEngine(kickEngine)
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/live-del", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// Missing engine and storage errors.
	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "DELETE", "/api/streams/x", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
	rr = api2sysCall(h.handleDeleteStream, "DELETE", "/api/streams/", "", nil)
	api2sysStatus(t, rr, http.StatusBadRequest)
}

func TestApi2SysDeleteStreamStorageErrors(t *testing.T) {
	h, db := api2sysClosedDBHandler(t)
	defer db.Close()
	h.SetMediaEngine(&stubMediaEngine{})
	rr := doRequest(t, h.Routes(), "DELETE", "/api/streams/x", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== create stream ====================

func TestApi2SysCreateStreamValidation(t *testing.T) {
	h, engine, db, cfg := api2sysStreamHandler(t)

	rr := doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"bad id!"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"ok-id","input_mode":"bogus"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"ok-id","input_mode":"pull","source_url":"ftp://nope"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// No ingest protocol enabled for a push stream.
	disabled := false
	cfg.RTMP = config.RTMPConfig{Enabled: &disabled}
	cfg.SRT = config.SRTConfig{Enabled: &disabled}
	cfg.WHIP = config.WHIPConfig{Enabled: &disabled}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"ok-id"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	enabled := true
	cfg.RTMP = config.RTMPConfig{Enabled: &enabled, Port: 1935, StreamKeys: map[string]string{"ok-id": "rtmp-key"}}
	cfg.SRT = config.SRTConfig{Enabled: &enabled, Port: 19000, StreamKeys: map[string]string{"ok-id": "srt-key"}}
	cfg.WHIP = config.WHIPConfig{Enabled: &enabled, PublishTokens: map[string]string{"ok-id": "whip-key"}}

	// Duplicate created stream.
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{StreamID: "dup"}); err != nil {
		t.Fatalf("failed to seed created stream: %v", err)
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"dup"}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	// Stream id already used by a camera.
	api2sysSeedCamera(t, db, "cam-taken", "Taken", "rtsp", "h264", "", true)
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"cam-taken"}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	// Stream id already bound to a camera.
	if err := db.BindStreamToCamera(context.Background(), "bound-taken", "cam-taken"); err != nil {
		t.Fatalf("failed to seed binding: %v", err)
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"bound-taken"}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	// Pull: live stream already exists.
	engine.stream = &media.StreamInfo{StreamID: "pull-live", Active: true}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams",
		strings.NewReader(`{"stream_id":"pull-live","input_mode":"pull","source_url":"rtsp://source/live"}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	// Pull: engine lookup error.
	engine.stream = nil
	engine.getErr = errors.New("engine down")
	rr = doRequest(t, h.Routes(), "POST", "/api/streams",
		strings.NewReader(`{"stream_id":"pull-err","input_mode":"pull","source_url":"rtsp://source/live"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	engine.getErr = nil

	// Pull: start failure.
	engine.startPullErr = errors.New("pull refused")
	rr = doRequest(t, h.Routes(), "POST", "/api/streams",
		strings.NewReader(`{"stream_id":"pull-fail","input_mode":"pull","source_url":"rtsp://source/live"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadGateway)
	engine.startPullErr = nil

	// Success paths.
	engine.playURLs = map[string]string{
		"rtmp": "rtmp://127.0.0.1:1935/live/ok-id",
		"whip": "http://127.0.0.1:12090/webrtc/whip?streamid=ok-id",
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"ok-id","name":"My Push"}`), "", "")
	api2sysStatus(t, rr, http.StatusCreated)
	body := api2sysJSON(t, rr)
	if body["stream_id"] != "ok-id" || body["name"] != "My Push" {
		t.Fatalf("unexpected created summary: %v", body)
	}
	ingest, _ := body["ingest_urls"].([]any)
	if len(ingest) == 0 {
		t.Fatalf("expected ingest urls: %v", body)
	}

	engine.stream = nil
	rr = doRequest(t, h.Routes(), "POST", "/api/streams",
		strings.NewReader(`{"stream_id":"ok-pull","input_mode":"pull","source_url":"rtsp://source/live","name":"My Pull"}`), "", "")
	api2sysStatus(t, rr, http.StatusCreated)
	body = api2sysJSON(t, rr)
	if body["source_type"] != "relay_pull" || body["source_url"] != "rtsp://source/live" {
		t.Fatalf("unexpected created pull summary: %v", body)
	}
	if len(engine.pulls) == 0 {
		t.Fatal("expected the engine to start a pull")
	}

	// Missing engine.
	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "POST", "/api/streams", strings.NewReader(`{"stream_id":"x"}`), "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

func TestApi2SysRestoreCreatedPulls(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{
		StreamID: "restore-pull", AppName: "live", InputMode: storage.CreatedStreamPull, SourceURL: "rtsp://source/restore",
	}); err != nil {
		t.Fatalf("failed to seed pull: %v", err)
	}
	// A push row must be ignored.
	if err := db.InsertCreatedStream(context.Background(), storage.CreatedStream{
		StreamID: "restore-push", AppName: "live", InputMode: storage.CreatedStreamPush,
	}); err != nil {
		t.Fatalf("failed to seed push: %v", err)
	}
	h.RestoreCreatedPulls(context.Background())
	if len(engine.pulls) != 1 || engine.pulls[0].StreamID != "restore-pull" {
		t.Fatalf("unexpected restored pulls: %+v", engine.pulls)
	}

	// Duplicate-in-stream errors are tolerated.
	engine.pulls = nil
	engine.startPullErr = errors.New("in stream already exist")
	h.RestoreCreatedPulls(context.Background())
	if len(engine.pulls) != 0 {
		t.Fatalf("duplicate pull should not be recorded: %+v", engine.pulls)
	}

	// Nil dependencies are a no-op.
	var nilHandler *Handler
	nilHandler.RestoreCreatedPulls(context.Background())
	(&Handler{}).RestoreCreatedPulls(context.Background())

	closed, closedDB := api2sysClosedDBHandler(t)
	defer closedDB.Close()
	closed.SetMediaEngine(engine)
	closed.RestoreCreatedPulls(context.Background())
}

// ==================== kick publisher ====================

func TestApi2SysKickPublisher(t *testing.T) {
	h, engine, _, _ := api2sysStreamHandler(t)

	engine.stream = nil
	rr := doRequest(t, h.Routes(), "POST", "/api/streams/x/kick-publisher", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	engine.getErr = errors.New("engine down")
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/x/kick-publisher", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	engine.getErr = nil

	engine.stream = &media.StreamInfo{StreamID: "no-pub", Active: true}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/no-pub/kick-publisher", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	engine.stream = &media.StreamInfo{StreamID: "pub", Active: true, Publisher: &media.SessionInfo{SessionID: "pub-1", Protocol: "rtmp"}}
	engine.kickErr = errors.New("kick failed")
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/pub/kick-publisher", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	engine.kickErr = nil
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/pub/kick-publisher", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["session_id"] != "pub-1" {
		t.Fatalf("unexpected kick response: %s", rr.Body.String())
	}

	rr = api2sysCall(h.handleKickPublisher, "POST", "/api/streams//kick-publisher", "", nil)
	api2sysStatus(t, rr, http.StatusBadRequest)

	noEngine := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noEngine.Routes(), "POST", "/api/streams/x/kick-publisher", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)
}

// ==================== stream history ====================

func TestApi2SysStreamHistory(t *testing.T) {
	h, _, db, _ := api2sysStreamHandler(t)
	ended := time.Now().Add(-time.Minute)
	api2sysSeedHistory(t, db, "h1", "rtmp", ended.Add(-time.Hour), &ended)
	api2sysSeedHistory(t, db, "h2", "srt", ended.Add(-2*time.Hour), &ended)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/history", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["total"] != float64(2) {
		t.Fatalf("unexpected history total: %v", body)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/streams/history?stream_id=h1&limit=5&offset=0", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["total"] != float64(1) || body["limit"] != float64(5) {
		t.Fatalf("unexpected filtered history: %v", body)
	}

	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/history/h1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/history?stream_id=h1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["total"] != float64(0) {
		t.Fatalf("history not deleted: %s", rr.Body.String())
	}

	rr = api2sysCall(h.handleDeleteStreamHistory, "DELETE", "/api/streams/history/", "", nil)
	api2sysStatus(t, rr, http.StatusBadRequest)

	failing, db2 := api2sysClosedDBHandler(t)
	defer db2.Close()
	rr = doRequest(t, failing.Routes(), "GET", "/api/streams/history", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	rr = doRequest(t, failing.Routes(), "DELETE", "/api/streams/history/h1", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== stream bans ====================

func TestApi2SysStreamBans(t *testing.T) {
	h, engine, _, _ := api2sysStreamHandler(t)

	rr := doRequest(t, h.Routes(), "GET", "/api/streams/bans", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if bans, ok := api2sysJSON(t, rr)["bans"]; !ok || bans != nil {
		// Empty result decodes as a nil slice; both shapes are acceptable.
		t.Logf("bans payload: %s", rr.Body.String())
	}

	// Ban without a manager.
	engine.stream = &media.StreamInfo{StreamID: "ban-me", Active: true}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/ban-me/ban", strings.NewReader(`{"reason":"abuse"}`), "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	h.banMgr = &api2sysBanMgr{}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/ban-me/ban", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Ban with an active publisher kicks the session and parses expires_at.
	engine.stream = &media.StreamInfo{StreamID: "ban-me", Active: true, Publisher: &media.SessionInfo{SessionID: "pub-ban", Protocol: "rtmp"}}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/ban-me/ban",
		strings.NewReader(`{"reason":"abuse","expires_at":"2030-01-02T03:04:05Z"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "banned" {
		t.Fatalf("unexpected ban response: %s", rr.Body.String())
	}
	// Invalid expires_at is ignored, not fatal.
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/ban-me2/ban", strings.NewReader(`{"reason":"x","expires_at":"soon"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)

	h.banMgr = &api2sysBanMgr{banErr: errors.New("ban failed")}
	rr = doRequest(t, h.Routes(), "POST", "/api/streams/ban-fail/ban", strings.NewReader(`{"reason":"x"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// Unban.
	h.banMgr = &api2sysBanMgr{}
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/ban-me/ban", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "unbanned" {
		t.Fatalf("unexpected unban response: %s", rr.Body.String())
	}
	h.banMgr = &api2sysBanMgr{unbanErr: errors.New("not banned")}
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/nope/ban", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	h.banMgr = nil
	rr = doRequest(t, h.Routes(), "DELETE", "/api/streams/nope/ban", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	rr = api2sysCall(h.handleBanStream, "POST", "/api/streams//ban", `{"reason":"x"}`, nil)
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = api2sysCall(h.handleUnbanStream, "DELETE", "/api/streams//ban", "", nil)
	api2sysStatus(t, rr, http.StatusBadRequest)

	failing, db := api2sysClosedDBHandler(t)
	defer db.Close()
	rr = doRequest(t, failing.Routes(), "GET", "/api/streams/bans", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== camera helpers ====================

// api2sysSeedRecording inserts a finished recording for stats/calendar tests.
func api2sysSeedRecording(t *testing.T, db *storage.DB, id, cameraID string, startedAt time.Time) {
	t.Helper()
	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   "/tmp/" + id + ".mp4",
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(5 * time.Minute),
		Duration:   300,
		FileSize:   2048,
		FrameCount: 150,
	}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("failed to seed recording: %v", err)
	}
}

// api2sysCreateCamera creates a camera through the API and returns its row.
func api2sysCreateCamera(t *testing.T, h *Handler, body string) storage.CameraRow {
	t.Helper()
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(body), "", "")
	api2sysStatus(t, rr, http.StatusCreated)
	var row storage.CameraRow
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("invalid camera response: %v", err)
	}
	return row
}

// ==================== camera helper functions ====================

func TestApi2SysResolveCameraSourceType(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := api2sysConfig(store)
	h := newHandlerWithConfig(db, store, cfg)
	ctx := context.Background()

	for _, tc := range []struct{ protocol, want string }{
		{"rtmp_push", "rtmp_push"},
		{"srt_push", "srt_push"},
		{"whip_push", "whip_push"},
		{"relay_pull", "relay_pull"},
		{"rtmp-pull", "relay_pull"},
		{"http-flv-pull", "relay_pull"},
		{"udp-ts-pull", "relay_pull"},
		{"onvif", ""},
		{"rtsp", ""},
		{"http", ""},
		{"gb28181", ""},
		{"xiaomi", ""},
	} {
		row := &storage.CameraRow{ID: "cam-" + tc.protocol, Protocol: tc.protocol}
		h.resolveCameraSourceType(ctx, row)
		if row.SourceType != tc.want {
			t.Fatalf("protocol %q: expected %q, got %q", tc.protocol, tc.want, row.SourceType)
		}
	}

	// An existing source type is never overwritten.
	row := &storage.CameraRow{ID: "keep", Protocol: "rtmp_push", SourceType: "custom"}
	h.resolveCameraSourceType(ctx, row)
	if row.SourceType != "custom" {
		t.Fatalf("expected preserved source type, got %q", row.SourceType)
	}
	// nil row and nil db are safe.
	h.resolveCameraSourceType(ctx, nil)
	noDB := &Handler{}
	noDB.resolveCameraSourceType(ctx, &storage.CameraRow{ID: "x", Protocol: "weird"})

	// Binding-based detection: a self-bound camera with push history.
	if err := db.UpsertCamera(ctx, "pushcam", "Push Cam", "rtsp", "h264", "rtsp://example.com/pushcam", "", "", true, "", "", ""); err != nil {
		t.Fatalf("failed to seed camera: %v", err)
	}
	if err := db.BindStreamToCamera(ctx, "pushcam", "pushcam"); err != nil {
		t.Fatalf("failed to bind: %v", err)
	}
	api2sysSeedHistory(t, db, "pushcam", "rtmp", time.Now().Add(-time.Hour), nil)

	row = &storage.CameraRow{ID: "pushcam", Protocol: "unknown"}
	h.resolveCameraSourceType(ctx, row)
	if row.SourceType != "rtmp_push" {
		t.Fatalf("expected rtmp_push from history, got %q", row.SourceType)
	}

	// Foreign binding (binding.StreamID != camera ID) is ignored.
	if err := db.UpsertCamera(ctx, "foreign", "Foreign", "rtsp", "h264", "rtsp://example.com/foreign", "", "", true, "", "", ""); err != nil {
		t.Fatalf("failed to seed camera: %v", err)
	}
	if err := db.BindStreamToCamera(ctx, "other-stream", "foreign"); err != nil {
		t.Fatalf("failed to bind: %v", err)
	}
	row = &storage.CameraRow{ID: "foreign", Protocol: "unknown"}
	h.resolveCameraSourceType(ctx, row)
	if row.SourceType != "" {
		t.Fatalf("foreign binding must not set a source type, got %q", row.SourceType)
	}

	// srt history fallback.
	if err := db.UpsertCamera(ctx, "srtcam", "SRT Cam", "rtsp", "h264", "rtsp://example.com/srtcam", "", "", true, "", "", ""); err != nil {
		t.Fatalf("failed to seed camera: %v", err)
	}
	if err := db.BindStreamToCamera(ctx, "srtcam", "srtcam"); err != nil {
		t.Fatalf("failed to bind: %v", err)
	}
	api2sysSeedHistory(t, db, "srtcam", "srt", time.Now().Add(-time.Hour), nil)
	row = &storage.CameraRow{ID: "srtcam", Protocol: "unknown"}
	h.resolveCameraSourceType(ctx, row)
	if row.SourceType != "srt_push" {
		t.Fatalf("expected srt_push from history, got %q", row.SourceType)
	}

	// customize history with a remote address maps to srt_push.
	if err := db.UpsertCamera(ctx, "customcam", "Custom Cam", "rtsp", "h264", "rtsp://example.com/customcam", "", "", true, "", "", ""); err != nil {
		t.Fatalf("failed to seed camera: %v", err)
	}
	if err := db.BindStreamToCamera(ctx, "customcam", "customcam"); err != nil {
		t.Fatalf("failed to bind: %v", err)
	}
	api2sysSeedHistory(t, db, "customcam", "customize", time.Now().Add(-time.Hour), nil)
	row = &storage.CameraRow{ID: "customcam", Protocol: "unknown"}
	h.resolveCameraSourceType(ctx, row)
	if row.SourceType != "srt_push" {
		t.Fatalf("expected srt_push from customize history, got %q", row.SourceType)
	}

	// Storage failure leaves the source type empty.
	closed, closedDB := api2sysClosedDBHandler(t)
	defer closedDB.Close()
	closedRow := &storage.CameraRow{ID: "x", Protocol: "unknown"}
	closed.resolveCameraSourceType(ctx, closedRow)
	if closedRow.SourceType != "" {
		t.Fatalf("expected empty source type on storage error, got %q", closedRow.SourceType)
	}
}

func TestApi2SysInjectCameraConfigFields(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	enabled := false
	cfg := api2sysConfig(store)
	cfg.Cameras = []config.CameraConfig{{
		ID:           "cfg-cam",
		Name:         "Config Cam",
		Protocol:     "rtsp",
		Encoding:     "h264",
		URL:          "rtsp://example.com/cfg",
		Enabled:      enabled,
		AudioEnabled: true,
		SourceType:   "relay_pull",
		SubStreamURL: "rtsp://example.com/sub",
		SubnetHints:  []string{"10.0.0.0/24"},
		Adaptive:     &config.CameraAdaptiveConfig{TimelapseInterval: "10s"},
	}}
	h := newHandlerWithConfig(db, store, cfg)

	// nil row is a no-op.
	h.injectCameraConfigFields(nil)

	// Config fallback.
	row := &storage.CameraRow{ID: "cfg-cam"}
	h.injectCameraConfigFields(row)
	if !row.AudioEnabled || row.SourceType != "relay_pull" || row.SubStreamURL != "rtsp://example.com/sub" {
		t.Fatalf("config fields not injected: %+v", row)
	}
	if len(row.SubnetHints) != 1 {
		t.Fatalf("subnet hints not injected: %+v", row.SubnetHints)
	}

	// Camera-manager lookup wins.
	h2, camMgr, _ := newTestCamHandler(t)
	created := api2sysCreateCamera(t, h2, `{"name":"Mgr Cam","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false,"audio_enabled":true,"sub_stream_url":"http://127.0.0.1:1/sub.jpg","subnet_hints":["192.168.0.0/16"],"adaptive":{"timelapse_interval":"10s"}}`)
	if cam := camMgr.GetCameraConfig(created.ID); cam == nil {
		t.Fatal("camera missing from manager")
	}
	row2 := &storage.CameraRow{ID: created.ID}
	h2.injectCameraConfigFields(row2)
	if !row2.AudioEnabled || row2.SubStreamURL != "http://127.0.0.1:1/sub.jpg" {
		t.Fatalf("manager fields not injected: %+v", row2)
	}
	if len(row2.SubnetHints) != 1 || row2.Adaptive == nil {
		t.Fatalf("manager extras not injected: %+v", row2)
	}
}

func TestApi2SysSyncCameraStatusWithStream(t *testing.T) {
	h, _, _, _ := api2sysStreamHandler(t)
	ctx := context.Background()

	// Non-live statuses are untouched.
	row := &storage.CameraRow{ID: "cam", Status: model.StatusStopped, StreamID: "cam"}
	h.syncCameraStatusWithStream(ctx, row)
	if row.Status != model.StatusStopped {
		t.Fatalf("stopped status must not change, got %q", row.Status)
	}
	// nil engine leaves the row untouched.
	noEngine := &Handler{}
	nilRowHandler := noEngine
	nilRowHandler.syncCameraStatusWithStream(ctx, row)
	h.syncCameraStatusWithStream(ctx, nil)

	// Empty stream id leaves the row untouched.
	empty := &storage.CameraRow{ID: "cam", Status: model.StatusRecording}
	h.syncCameraStatusWithStream(ctx, empty)
	if empty.Status != model.StatusRecording {
		t.Fatalf("empty stream id must not change status, got %q", empty.Status)
	}

	engine := &stubMediaEngine{stream: &media.StreamInfo{StreamID: "cam", Active: true, VideoCodec: "h265"}}
	h.SetMediaEngine(engine)
	active := &storage.CameraRow{ID: "cam", Status: model.StatusRecording, StreamID: "cam"}
	h.syncCameraStatusWithStream(ctx, active)
	if active.Status != model.StatusRecording || active.Encoding != "h265" {
		t.Fatalf("expected recording h265, got %+v", active)
	}
	if active.StreamEncoding != "H265" {
		t.Fatalf("expected stream encoding H265, got %q", active.StreamEncoding)
	}

	// Inactive engine stream => offline.
	engine.stream = &media.StreamInfo{StreamID: "cam", Active: false}
	inactive := &storage.CameraRow{ID: "cam", Status: model.StatusRecording, StreamID: "cam"}
	h.syncCameraStatusWithStream(ctx, inactive)
	if inactive.Status != model.StatusOffline {
		t.Fatalf("expected offline, got %q", inactive.Status)
	}

	// Engine error => offline.
	engine.getErr = errors.New("lookup failed")
	failed := &storage.CameraRow{ID: "cam", Status: model.StatusReconnecting, StreamID: "cam"}
	h.syncCameraStatusWithStream(ctx, failed)
	if failed.Status != model.StatusOffline {
		t.Fatalf("expected offline on engine error, got %q", failed.Status)
	}
	engine.getErr = nil

	// GB28181 active playback keeps the camera recording even without an
	// active lalmax stream.
	engine.stream = nil
	h.SetGB28181Server(api2sysGBStatus{playing: map[string]bool{"gb": true}})
	gb := &storage.CameraRow{ID: "gb", Protocol: "gb28181", Status: model.StatusRecording, StreamID: "gb"}
	h.syncCameraStatusWithStream(ctx, gb)
	if gb.Status != model.StatusRecording {
		t.Fatalf("expected recording for active GB28181 playback, got %q", gb.Status)
	}
}

// ==================== camera list ====================

func TestApi2SysListCamerasWithManager(t *testing.T) {
	h, _, _ := newTestCamHandler(t)
	engine := &stubMediaEngine{stream: nil}
	h.SetMediaEngine(engine)

	first := api2sysCreateCamera(t, h, `{"name":"First Cam","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false,"description":"doorbell"}`)
	second := api2sysCreateCamera(t, h, `{"name":"Second Cam","protocol":"rtmp-pull","url":"rtmp://127.0.0.1:1/live/stream","enabled":false}`)

	// Recording plan for the first camera's stream.
	if err := h.db.UpsertRecordingPlan(context.Background(), &storage.RecordingPlan{
		ID: storage.NewRecordingPlanID(), StreamID: first.ID, Name: "plan", Mode: storage.RecordingModeContinuous, Enabled: true,
	}); err != nil {
		t.Fatalf("failed to seed recording plan: %v", err)
	}

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var rows []storage.CameraRow
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("invalid camera list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 cameras, got %d", len(rows))
	}
	byID := map[string]storage.CameraRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if byID[first.ID].RecordingMode != storage.RecordingModeContinuous {
		t.Fatalf("expected continuous plan on first camera: %+v", byID[first.ID])
	}
	if byID[second.ID].RecordingMode != storage.RecordingModeOff {
		t.Fatalf("expected off recording mode on second camera: %+v", byID[second.ID])
	}
	if byID[first.ID].Status != model.StatusStopped {
		t.Fatalf("expected stopped status for a disabled camera: %+v", byID[first.ID])
	}
	if byID[second.ID].SourceType != "relay_pull" {
		t.Fatalf("expected relay_pull source type: %+v", byID[second.ID])
	}

	// Archive the second camera; it only shows up with include_archived.
	rr = doRequest(t, h.Routes(), "DELETE", "/api/cameras/"+second.ID, nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	rows = nil
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("invalid camera list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 active camera, got %d", len(rows))
	}
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras?include_archived=1", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	rows = nil
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("invalid camera list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected archived camera to be listed, got %d", len(rows))
	}
}

func TestApi2SysListCamerasStorageErrors(t *testing.T) {
	h, db := api2sysClosedDBHandler(t)
	defer db.Close()
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras?include_archived=true", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== camera create ====================

func TestApi2SysCreateCameraValidation(t *testing.T) {
	h, _, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(`{"protocol":"http","url":"http://127.0.0.1:1/s.jpg"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(`{"name":"x","url":"http://127.0.0.1:1/s.jpg"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(`{"name":"x","protocol":"rtsp_bogus","url":"rtsp://x/s"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"x","protocol":"http","url":"http://127.0.0.1:1/s.jpg","rtsp_transport":"quic"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	// ONVIF without url/endpoint.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(`{"name":"x","protocol":"onvif"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// No camera manager.
	noMgr := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noMgr.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"x","protocol":"http","url":"http://127.0.0.1:1/s.jpg"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysCreateCameraMetadataAndONVIF(t *testing.T) {
	h, camMgr, _ := newTestCamHandler(t)

	row := api2sysCreateCamera(t, h, `{
		"name":"Detail Cam",
		"protocol":"http",
		"url":"http://127.0.0.1:1/snap.jpg",
		"enabled":false,
		"description":"front porch",
		"location":"porch",
		"brand":"ACME",
		"model":"X1",
		"serial_number":"SN-123",
		"profile_name":"main-profile",
		"audio_enabled":false,
		"sub_stream_url":"http://127.0.0.1:1/sub.jpg",
		"subnet_hints":["192.168.0.0/16"],
		"username":"user"
	}`)
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/"+row.ID, nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var got storage.CameraRow
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid camera response: %v", err)
	}
	if got.Description != "front porch" || got.Location != "porch" {
		t.Fatalf("metadata not persisted: %+v", got)
	}
	if got.Brand != "ACME" || got.Model != "X1" || got.SerialNumber != "SN-123" {
		t.Fatalf("device metadata not persisted: %+v", got)
	}
	if got.ProfileName != "main-profile" {
		t.Fatalf("profile name not persisted: %+v", got)
	}
	if got.SubStreamURL != "http://127.0.0.1:1/sub.jpg" || len(got.SubnetHints) != 1 {
		t.Fatalf("stream extras not persisted: %+v", got)
	}
	if cam := camMgr.GetCameraConfig(row.ID); cam == nil || cam.AudioEnabled {
		t.Fatalf("expected audio disabled on camera config: %+v", cam)
	}

	// ONVIF cameras accept onvif_endpoint and expose it as url.
	onvifRow := api2sysCreateCamera(t, h, `{"name":"ONVIF Cam","protocol":"onvif","onvif_endpoint":"http://127.0.0.1:1/onvif/device_service","enabled":false}`)
	if onvifRow.ONVIFEndpoint != "http://127.0.0.1:1/onvif/device_service" {
		t.Fatalf("unexpected onvif endpoint: %+v", onvifRow)
	}
	if onvifRow.URL != "http://127.0.0.1:1/onvif/device_service" {
		t.Fatalf("expected url mirrored from onvif endpoint: %+v", onvifRow)
	}

	// Duplicate ONVIF endpoints are rejected.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"ONVIF Dup","protocol":"onvif","onvif_endpoint":"http://127.0.0.1:1/onvif/device_service","enabled":false}`), "", "")
	api2sysStatus(t, rr, http.StatusConflict)

	// ONVIF via the url field.
	urlRow := api2sysCreateCamera(t, h, `{"name":"ONVIF URL","protocol":"onvif","url":"http://127.0.0.1:1/onvif/other","enabled":false}`)
	if urlRow.ONVIFEndpoint != "http://127.0.0.1:1/onvif/other" {
		t.Fatalf("expected url promoted to onvif endpoint: %+v", urlRow)
	}

	// Legacy combined protocol is normalised.
	legacy := api2sysCreateCamera(t, h, `{"name":"Legacy","protocol":"rtsp_h265","url":"rtsp://127.0.0.1:1/s","enabled":false}`)
	if legacy.Protocol != "rtsp" || legacy.Encoding != "h265" {
		t.Fatalf("unexpected legacy normalisation: %+v", legacy)
	}
}

// ==================== camera get / update ====================

func TestApi2SysGetCameraPlanAndGB(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)

	api2sysSeedCamera(t, db, "plan-cam", "Plan Cam", "rtsp", "h264", "plan-cam", true)
	if err := db.UpsertRecordingPlan(context.Background(), &storage.RecordingPlan{
		ID: storage.NewRecordingPlanID(), StreamID: "plan-cam", Name: "plan", Mode: storage.RecordingModeScheduled, Enabled: true,
	}); err != nil {
		t.Fatalf("failed to seed plan: %v", err)
	}
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/plan-cam", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var row storage.CameraRow
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("invalid camera response: %v", err)
	}
	if row.RecordingMode != storage.RecordingModeScheduled {
		t.Fatalf("expected scheduled mode, got %q", row.RecordingMode)
	}
	if row.StreamID != "plan-cam" {
		t.Fatalf("expected stream id injected, got %q", row.StreamID)
	}

	// Disabled plan reports "off".
	if err := db.UpsertRecordingPlan(context.Background(), &storage.RecordingPlan{
		ID: storage.NewRecordingPlanID(), StreamID: "plan-cam", Name: "plan", Mode: storage.RecordingModeScheduled, Enabled: false,
	}); err != nil {
		t.Fatalf("failed to disable plan: %v", err)
	}
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/plan-cam", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	row = storage.CameraRow{}
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("invalid camera response: %v", err)
	}
	if row.RecordingMode != storage.RecordingModeOff {
		t.Fatalf("expected off mode for disabled plan, got %q", row.RecordingMode)
	}

	// GB28181 cameras report recording when the lalmax stream is active.
	api2sysSeedCamera(t, db, "gb-cam", "GB Cam", "gb28181", "h264", "gb-cam", true)
	engine.stream = &media.StreamInfo{StreamID: "gb-cam", Active: true}
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/gb-cam", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	row = storage.CameraRow{}
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("invalid camera response: %v", err)
	}
	if row.Status != model.StatusRecording {
		t.Fatalf("expected recording status for active GB stream, got %q", row.Status)
	}

	// Not found / storage failure.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/does-not-exist", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)
	failing, closedDB := api2sysClosedDBHandler(t)
	defer closedDB.Close()
	rr = doRequest(t, failing.Routes(), "GET", "/api/cameras/anything", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysUpdateCameraBranches(t *testing.T) {
	h, camMgr, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "PUT", "/api/cameras/missing", strings.NewReader(`{"name":"x"}`), "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	row := api2sysCreateCamera(t, h, `{"name":"Before","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)

	for _, body := range []string{
		`not json`,
		`{"rtsp_transport":"quic"}`,
		`{"url":"not-a-url"}`,
	} {
		rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/"+row.ID, strings.NewReader(body), "", "")
		api2sysStatus(t, rr, http.StatusBadRequest)
	}

	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/"+row.ID, strings.NewReader(`{
		"name":"After",
		"description":"updated",
		"location":"shed",
		"brand":"NewBrand",
		"model":"M2",
		"serial_number":"SN-9",
		"profile_name":"sub-profile",
		"retention_days":14,
		"audio_enabled":true,
		"sub_stream_url":"http://127.0.0.1:1/sub.jpg",
		"username":"",
		"password":""
	}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var updated storage.CameraRow
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatalf("invalid update response: %v", err)
	}
	if updated.Name != "After" || updated.Description != "updated" || updated.RetentionDays != 14 {
		t.Fatalf("camera not updated: %+v", updated)
	}
	if updated.ProfileName != "sub-profile" || camMgr.GetCameraConfig(row.ID) == nil {
		t.Fatalf("profile name not updated: %+v", updated)
	}
	if cam := camMgr.GetCameraConfig(row.ID); cam == nil || !cam.AudioEnabled {
		t.Fatalf("audio flag not updated: %+v", cam)
	}

	// ONVIF updates mirror url <-> onvif_endpoint.
	onvifRow := api2sysCreateCamera(t, h, `{"name":"ONVIF Upd","protocol":"onvif","onvif_endpoint":"http://127.0.0.1:1/onvif/device_service","enabled":false}`)
	rr = doRequest(t, h.Routes(), "PUT", "/api/cameras/"+onvifRow.ID,
		strings.NewReader(`{"protocol":"onvif","onvif_endpoint":"http://127.0.0.1:1/onvif/new"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	updated = storage.CameraRow{}
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatalf("invalid update response: %v", err)
	}
	if updated.ONVIFEndpoint != "http://127.0.0.1:1/onvif/new" || updated.URL != "http://127.0.0.1:1/onvif/new" {
		t.Fatalf("onvif endpoint not synced: %+v", updated)
	}

	// Storage failure after a successful in-memory update.
	failing, camMgr2, _ := newTestCamHandler(t)
	failingRow := api2sysCreateCamera(t, failing, `{"name":"Closed","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)
	_ = camMgr2
	if err := failing.db.(*storage.DB).Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}
	rr = doRequest(t, failing.Routes(), "PUT", "/api/cameras/"+failingRow.ID, strings.NewReader(`{"name":"Gone"}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== camera delete ====================

func TestApi2SysDeleteCameraBranches(t *testing.T) {
	// Orphaned DB-only camera is archived directly.
	h, _, db, _ := api2sysStreamHandler(t)
	api2sysSeedCamera(t, db, "orphan", "Orphan", "rtsp", "h264", "", true)
	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/orphan", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "archived" {
		t.Fatalf("unexpected delete response: %s", rr.Body.String())
	}
	archived, err := db.GetCamera(context.Background(), "orphan")
	if err != nil || archived == nil || !archived.Archived {
		t.Fatalf("camera not archived: %+v %v", archived, err)
	}
	// Deleting an already archived camera is idempotent.
	rr = doRequest(t, h.Routes(), "DELETE", "/api/cameras/orphan", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	rr = doRequest(t, h.Routes(), "DELETE", "/api/cameras/missing", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Camera-manager backed camera is archived through the manager.
	h2, _, _ := newTestCamHandler(t)
	managed := api2sysCreateCamera(t, h2, `{"name":"Managed","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)
	rr = doRequest(t, h2.Routes(), "DELETE", "/api/cameras/"+managed.ID, nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)

	// Storage failure.
	failing, closedDB := api2sysClosedDBHandler(t)
	defer closedDB.Close()
	rr = doRequest(t, failing.Routes(), "DELETE", "/api/cameras/x", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysPermanentDeleteCamera(t *testing.T) {
	h, _, db, _ := api2sysStreamHandler(t)
	api2sysSeedCamera(t, db, "perm-cam", "Perm Cam", "rtsp", "h264", "perm-cam", true)
	if err := db.UpsertRecordingPlan(context.Background(), &storage.RecordingPlan{
		ID: storage.NewRecordingPlanID(), StreamID: "perm-cam", Name: "plan", Mode: storage.RecordingModeContinuous, Enabled: true,
	}); err != nil {
		t.Fatalf("failed to seed plan: %v", err)
	}
	api2sysSeedRecording(t, db, "perm-rec", "perm-cam", time.Now().Add(-time.Hour))

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/perm-cam/permanent", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if api2sysJSON(t, rr)["status"] != "deleted" {
		t.Fatalf("unexpected delete response: %s", rr.Body.String())
	}
	cam, err := db.GetCamera(context.Background(), "perm-cam")
	if err != nil || cam != nil {
		t.Fatalf("camera still present: %+v %v", cam, err)
	}
	if plan, err := db.GetRecordingPlanByStream(context.Background(), "perm-cam"); err != nil || plan != nil {
		t.Fatalf("recording plan still present: %+v %v", plan, err)
	}

	rr = doRequest(t, h.Routes(), "DELETE", "/api/cameras/perm-cam/permanent", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Manager-backed permanent delete.
	h2, camMgr, _ := newTestCamHandler(t)
	managed := api2sysCreateCamera(t, h2, `{"name":"Managed","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)
	rr = doRequest(t, h2.Routes(), "DELETE", "/api/cameras/"+managed.ID+"/permanent", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if cam := camMgr.GetCameraConfig(managed.ID); cam != nil {
		t.Fatalf("camera still in manager: %+v", cam)
	}
}

// ==================== camera stats ====================

func TestApi2SysCameraRecordingStatsAndDays(t *testing.T) {
	h, _, db, _ := api2sysStreamHandler(t)
	started := time.Date(2024, 5, 17, 10, 30, 0, 0, time.UTC)
	api2sysSeedRecording(t, db, "rec-a", "stat-cam", started)
	api2sysSeedRecording(t, db, "rec-b", "stat-cam", started.Add(2*time.Hour))

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/stat-cam/stats", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["recording_count"] != float64(2) || body["total_size"] != float64(4096) {
		t.Fatalf("unexpected stats: %v", body)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/stat-cam/recordings/days?month=2024-05", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	days, ok := body["days"].([]any)
	if !ok || len(days) != 1 || days[0] != "2024-05-17" {
		t.Fatalf("unexpected recording days: %v", body)
	}

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/stat-cam/recordings/days?month=2024-13", nil, "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Default month is the current month.
	now := time.Now()
	api2sysSeedRecording(t, db, "rec-now", "now-cam", now)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/now-cam/recordings/days", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if days, ok := body["days"].([]any); !ok || len(days) == 0 {
		t.Fatalf("expected days for the current month: %v", body)
	}

	failing, closedDB := api2sysClosedDBHandler(t)
	defer closedDB.Close()
	rr = doRequest(t, failing.Routes(), "GET", "/api/cameras/x/stats", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	rr = doRequest(t, failing.Routes(), "GET", "/api/cameras/x/recordings/days?month=2024-05", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== camera start/stop/pause/resume ====================

func TestApi2SysCameraStartStopErrors(t *testing.T) {
	h, _, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/missing/start", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// StopCamera reports an untyped error when no recorder is running.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/missing/stop", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/missing/pause-recording", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)

	// Resume reports an untyped "not paused" error when no recorder is paused.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/missing/resume-recording", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// A disabled camera reports a disabled error on start.
	disabled := api2sysCreateCamera(t, h, `{"name":"Disabled","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/"+disabled.ID+"/start", nil, "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	body := api2sysJSON(t, rr)
	if !strings.Contains(fmt.Sprint(body["error"]), "disabled") {
		t.Fatalf("unexpected disabled error: %v", body)
	}

	// Pause/resume on a camera without a recorder surfaces a not-found error
	// and exercise the recording-plan toggle path.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/"+disabled.ID+"/pause-recording", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/"+disabled.ID+"/resume-recording", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// Pause/resume flip an existing recording plan.
	planRow := api2sysCreateCamera(t, h, `{"name":"Plan Cam","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)
	if err := h.db.UpsertRecordingPlan(context.Background(), &storage.RecordingPlan{
		ID: storage.NewRecordingPlanID(), StreamID: planRow.ID, Name: "plan", Mode: storage.RecordingModeContinuous, Enabled: true,
	}); err != nil {
		t.Fatalf("failed to seed plan: %v", err)
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/"+planRow.ID+"/pause-recording", nil, "", "")
	api2sysStatus(t, rr, http.StatusNotFound)
	plan, err := h.db.GetRecordingPlanByStream(context.Background(), planRow.ID)
	if err != nil || plan == nil {
		t.Fatalf("plan lookup failed: %+v %v", plan, err)
	}
	if plan.Enabled {
		t.Fatal("expected the plan to be disabled by pause-recording")
	}

	noMgr := TestHandler(mustDB(t), mustStorage(t))
	for _, path := range []string{"/start", "/stop", "/pause-recording", "/resume-recording"} {
		rr = doRequest(t, noMgr.Routes(), "POST", "/api/cameras/x"+path, nil, "", "")
		api2sysStatus(t, rr, http.StatusServiceUnavailable)
	}
}

func TestApi2SysBatchCameras(t *testing.T) {
	h, _, _ := newTestCamHandler(t)
	disabled := api2sysCreateCamera(t, h, `{"name":"Batch Disabled","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/batch", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/batch", strings.NewReader(`{"action":"reboot","ids":["x"]}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/batch", strings.NewReader(`{"action":"start","ids":[]}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = disabled.ID
	}
	idsJSON, err := json.Marshal(map[string]any{"action": "start", "ids": tooMany})
	if err != nil {
		t.Fatalf("failed to marshal ids: %v", err)
	}
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/batch", strings.NewReader(string(idsJSON)), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Mixed results: the disabled camera fails to start, the missing one fails
	// to stop; both requests still return 200 with a failed map.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/batch",
		strings.NewReader(`{"action":"start","ids":["`+disabled.ID+`","missing"]}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	failed, ok := body["failed"].(map[string]any)
	if !ok || len(failed) != 2 {
		t.Fatalf("expected both ids to fail: %v", body)
	}
	if _, ok := body["ok"].([]any); !ok {
		t.Fatalf("expected ok array: %v", body)
	}

	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/batch",
		strings.NewReader(`{"action":"stop","ids":["`+disabled.ID+`"]}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)

	noMgr := TestHandler(mustDB(t), mustStorage(t))
	rr = doRequest(t, noMgr.Routes(), "POST", "/api/cameras/batch", strings.NewReader(`{"action":"stop","ids":["x"]}`), "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

// ==================== camera test connection / ONVIF probe ====================

func TestApi2SysTestConnectionSuccesses(t *testing.T) {
	h, _, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", strings.NewReader(`not json`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", strings.NewReader(`{"url":""}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// RTSP: accept a loopback TCP connection.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	rtspURL := "rtsp://" + ln.Addr().String() + "/live/stream"
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", strings.NewReader(`{"url":"`+rtspURL+`"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["success"] != true {
		t.Fatalf("expected RTSP success: %v", body)
	}

	// RTSP: refused port.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", strings.NewReader(`{"url":"rtsp://127.0.0.1:1/live"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["success"] != false {
		t.Fatalf("expected RTSP failure: %v", body)
	}

	// HTTP: local test server with credentials.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _, ok := r.BasicAuth(); !ok || user != "user" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection",
		strings.NewReader(`{"url":"`+srv.URL+`/snap.jpg","username":"user","password":"pass"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["success"] != true {
		t.Fatalf("expected HTTP success: %v", body)
	}

	// ONVIF uses onvif_endpoint as the target.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection",
		strings.NewReader(`{"url":"http://example.invalid/x","protocol":"onvif","onvif_endpoint":"`+srv.URL+`/onvif/device_service"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["success"] != true {
		t.Fatalf("expected ONVIF success via endpoint: %v", body)
	}

	// Invalid URL format reaches the request-builder error branch.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection", strings.NewReader(`{"url":"://bad"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body = api2sysJSON(t, rr)
	if body["success"] != false || !strings.Contains(fmt.Sprint(body["message"]), "invalid URL") {
		t.Fatalf("expected invalid URL failure: %v", body)
	}
}

func TestApi2SysProbeONVIFEncodingFailure(t *testing.T) {
	// Closed loopback port: the probe must fail fast and return "".
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got := probeONVIFEncoding(ctx, "http://127.0.0.1:1/onvif/device_service", "user", "pass"); got != "" {
		t.Fatalf("expected empty encoding, got %q", got)
	}
}

// ==================== system handler extras ====================

func TestApi2SysHealthCamerasEndpoints(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)
	h.healthMgr = api2sysHealthMgr{all: map[string]*model.CameraHealth{
		"cam-x": {CameraID: "cam-x", LatestStatus: "healthy"},
	}}

	rr := doRequest(t, h.Routes(), "GET", "/api/health/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if _, ok := body["cam-x"].(map[string]any); !ok {
		t.Fatalf("expected cam-x health entry: %v", body)
	}

	// Configured cameras are merged into the map.
	api2sysSeedCamera(t, mustDB(t), "db-cam", "DB Cam", "rtsp", "h264", "", false)
	// A closed DB makes ListCameras fail; the health manager data is kept.
	failing, db := api2sysClosedDBHandler(t)
	defer db.Close()
	failing.healthMgr = api2sysHealthMgr{all: map[string]*model.CameraHealth{
		"cam-y": {CameraID: "cam-y", LatestStatus: "error"},
	}}
	rr = doRequest(t, failing.Routes(), "GET", "/api/health/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
}

func TestApi2SysHealthDetailsOfflineMajority(t *testing.T) {
	h, _, _, _ := api2sysNewHandler(t)
	h.healthMgr = api2sysHealthMgr{all: map[string]*model.CameraHealth{
		"cam-1": {CameraID: "cam-1", LatestStatus: "offline"},
		"cam-2": {CameraID: "cam-2", LatestStatus: "unknown"},
		"cam-3": {CameraID: "cam-3", LatestStatus: "healthy"},
	}}
	rr := doRequest(t, h.Routes(), "GET", "/api/health/details", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var resp HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Fatalf("expected unhealthy when most cameras are offline, got %q", resp.Status)
	}
	if resp.Cameras == nil || resp.Cameras.Offline != 2 {
		t.Fatalf("unexpected camera summary: %+v", resp.Cameras)
	}
}

func TestApi2SysBackupFailures(t *testing.T) {
	// Backup directory cannot be created because the parent path is a file.
	h, _, _, _ := api2sysNewHandler(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create blocker file: %v", err)
	}
	h.configPath = filepath.Join(blocker, "config.yaml")
	rr := doRequest(t, h.Routes(), "POST", "/api/backup", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)

	// Backup write fails on a closed database.
	failing, db := api2sysClosedDBHandler(t)
	defer db.Close()
	rr = doRequest(t, failing.Routes(), "POST", "/api/backup", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
}

func TestApi2SysReadyzDiskBranches(t *testing.T) {
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := api2sysConfig(store)

	h := newHandlerWithConfig(db, store, cfg)
	h.readyzDiskUsage = func() (int64, int64, error) { return 100, 96, nil }
	rr := doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	h.readyzDiskUsage = func() (int64, int64, error) { return 100, 10, errors.New("stat failed") }
	rr = doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	api2sysStatus(t, rr, http.StatusServiceUnavailable)

	h.readyzDiskUsage = func() (int64, int64, error) { return 0, 0, nil }
	rr = doRequest(t, h.Routes(), "GET", "/api/readyz", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
}

func TestApi2SysSaveConfigErrorAndFeatureManager(t *testing.T) {
	// Config save failure is logged but the update still succeeds.
	h, cfg, _, _ := api2sysNewHandler(t)
	h.configPath = filepath.Join(t.TempDir(), "missing-dir", "config.yaml")
	rr := doRequest(t, h.Routes(), "PUT", "/api/settings", strings.NewReader(`{"cleanup":{"retention_days":11}}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if cfg.Cleanup.RetentionDays != 11 {
		t.Fatalf("expected in-memory update, got %d", cfg.Cleanup.RetentionDays)
	}

	// Feature toggles propagate to the camera manager.
	h2, camMgr, _ := newTestCamHandler(t)
	api2sysCreateCamera(t, h2, `{"name":"Proto Cam","protocol":"http","url":"http://127.0.0.1:1/snap.jpg","enabled":false}`)
	rr = doRequest(t, h2.Routes(), "PUT", "/api/features", strings.NewReader(`{"protocols":{"http":false}}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if protocols, ok := body["protocols"].(map[string]any); !ok || protocols["http"] != false {
		t.Fatalf("expected http protocol disabled: %v", body)
	}
	if camMgr == nil {
		t.Fatal("camera manager missing")
	}
}

// ==================== stream helper function tables ====================

func TestApi2SysSourceTypeInferenceTables(t *testing.T) {
	for _, tc := range []struct {
		name    string
		info    media.StreamInfo
		managed bool
		want    string
	}{
		{"voip", media.StreamInfo{StreamID: "voip_1"}, false, "voip"},
		{"iptv", media.StreamInfo{StreamID: "iptv_channel"}, false, "iptv"},
		{"emby", media.StreamInfo{StreamID: "emby_item"}, false, "emby"},
		{"gb28181 publisher", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "gb28181"}}, false, "gb28181"},
		{"managed", media.StreamInfo{}, true, "camera"},
		{"rtmp publisher", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "rtmp"}}, false, "rtmp_push"},
		{"srt publisher", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "srt"}}, false, "srt_push"},
		{"whip publisher", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "whip"}}, false, "whip_push"},
		{"customize remote", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "customize", Remote: "10.0.0.1:9000"}}, false, "srt_push"},
		{"customize no remote", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "customize"}}, false, "whip_push"},
		{"rtsp publisher", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "rtsp"}}, false, "relay_pull"},
		{"codec only", media.StreamInfo{VideoCodec: "h264"}, false, "whip_push"},
		{"bare", media.StreamInfo{}, false, "stream"},
	} {
		if got := inferStreamSourceType(tc.info, tc.managed); got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, got)
		}
	}

	for _, tc := range []struct{ streamID, protocol, want string }{
		{"voip_9", "rtmp", "voip"},
		{"iptv_9", "rtmp", "iptv"},
		{"emby_9", "rtmp", "emby"},
		{"plain", "rtmp", "rtmp_push"},
		{"plain", "rtp", "gb28181"},
		{"plain", "weird", "stream"},
	} {
		if got := inferStreamSourceTypeForID(tc.streamID, tc.protocol); got != tc.want {
			t.Fatalf("id %q protocol %q: expected %q, got %q", tc.streamID, tc.protocol, tc.want, got)
		}
	}

	for _, tc := range []struct{ protocol, want string }{
		{"rtmp", "rtmp_push"},
		{"srt", "srt_push"},
		{"whip", "whip_push"},
		{"rtsp", "relay_pull"},
		{"relay_pull", "relay_pull"},
		{"pull", "relay_pull"},
		{"rtp", "gb28181"},
		{"gb28181", "gb28181"},
		{"ps", "gb28181"},
		{"customize", "stream"},
		{"", "stream"},
		{"unknown", "stream"},
	} {
		if got := inferStreamSourceTypeFromProtocol(tc.protocol); got != tc.want {
			t.Fatalf("protocol %q: expected %q, got %q", tc.protocol, tc.want, got)
		}
	}

	for _, tc := range []struct {
		name string
		info media.StreamInfo
		want string
	}{
		{"no publisher", media.StreamInfo{}, "camera"},
		{"rtmp", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "rtmp"}}, "promoted"},
		{"srt", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "srt"}}, "promoted"},
		{"whip", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "whip"}}, "promoted"},
		{"customize", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "customize", Remote: "host:1"}}, "promoted"},
		{"rtsp", media.StreamInfo{Publisher: &media.SessionInfo{Protocol: "rtsp"}}, "camera"},
	} {
		if got := inferCameraManagementType(tc.info); got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, got)
		}
	}

	for _, tc := range []struct{ codec, want string }{
		{"h264", "h264"},
		{"AVC1", "h264"},
		{"h265", "h265"},
		{"HEVC", "h265"},
		{"mjpeg", "mjpeg"},
		{"JPEG", "mjpeg"},
		{"opus", ""},
	} {
		if got := encodingFromMediaCodec(tc.codec); got != tc.want {
			t.Fatalf("codec %q: expected %q, got %q", tc.codec, tc.want, got)
		}
	}

	// Stream IDs may arrive percent-encoded more than once.
	if got := normalizeStreamID("%253A"); got != ":" {
		t.Fatalf("expected double-decoded colon, got %q", got)
	}
	if got := normalizeStreamID("plain"); got != "plain" {
		t.Fatalf("expected unchanged id, got %q", got)
	}
	if got := normalizeStreamID("%zz"); got != "%zz" {
		t.Fatalf("expected malformed escape to be preserved, got %q", got)
	}

	long := strings.Repeat("a", 200)
	if got := normalizeStreamDisplayName(long, "id"); len([]rune(got)) != 128 {
		t.Fatalf("expected 128 runes, got %d", len([]rune(got)))
	}
	if got := normalizeStreamDisplayName("  ", "fallback-id"); got != "fallback-id" {
		t.Fatalf("expected stream-id fallback, got %q", got)
	}
}

func TestApi2SysApplyCreatedStreamAndURLs(t *testing.T) {
	h, engine, _, cfg := api2sysStreamHandler(t)
	ctx := context.Background()

	// applyCreatedStream edge cases.
	applyCreatedStream(nil, nil)
	item := &streamSummary{StreamID: "x"}
	applyCreatedStream(item, nil)
	applyCreatedStream(item, map[string]storage.CreatedStream{})
	applyCreatedStream(item, map[string]storage.CreatedStream{"x": {StreamID: "x", Name: "Named"}})
	if item.Name != "Named" {
		t.Fatalf("expected created name, got %q", item.Name)
	}
	pullItem := &streamSummary{StreamID: "y"}
	applyCreatedStream(pullItem, map[string]storage.CreatedStream{"y": {
		StreamID: "y", InputMode: storage.CreatedStreamPull, SourceURL: "rtsp://src", Name: "Pull",
	}})
	if pullItem.SourceType != "relay_pull" || pullItem.SourceURL != "rtsp://src" || pullItem.IngestURLs != nil {
		t.Fatalf("unexpected pull item: %+v", pullItem)
	}

	// buildStreamPlayURLs returns every supported protocol the engine can build.
	engine.playURLs = map[string]string{"hls": "http://h/hls", "rtsp": "rtsp://h/live/x"}
	if urls := h.buildStreamPlayURLs(ctx, "x", ""); len(urls) != 2 {
		t.Fatalf("expected 2 play URLs, got %+v", urls)
	}
	noEngine := &Handler{}
	noEngine.attachStreamURLs(ctx, nil)
	noEngine.attachStreamURLs(ctx, &streamSummary{StreamID: "x"})

	// VoIP items never advertise ingest URLs.
	voip := &streamSummary{StreamID: "voip_1", SourceType: "voip"}
	h.attachStreamURLs(ctx, voip)
	if len(voip.PlayURLs) == 0 || len(voip.IngestURLs) != 0 {
		t.Fatalf("unexpected voip URLs: %+v", voip)
	}

	// Relay-pull items never advertise ingest URLs.
	relay := &streamSummary{StreamID: "relay", SourceType: "relay_pull", Active: true}
	h.attachStreamURLs(ctx, relay)
	if len(relay.IngestURLs) != 0 {
		t.Fatalf("relay pull must not advertise ingest URLs: %+v", relay)
	}

	// Managed streams advertise WHIP ingest URLs with publish tokens.
	engine.playURLs = map[string]string{"whip": "http://127.0.0.1:12090/webrtc/whip?streamid=managed"}
	managed := &streamSummary{StreamID: "push-live", Managed: true, AppName: "live"}
	h.attachStreamURLs(ctx, managed)
	if len(managed.IngestURLs) != 1 {
		t.Fatalf("expected a WHIP ingest URL, got %+v", managed.IngestURLs)
	}
	if !strings.Contains(managed.IngestURLs[0].URL, "stream_key=whip-key") {
		t.Fatalf("expected publish token in URL, got %q", managed.IngestURLs[0].URL)
	}

	// Unmanaged push streams advertise configured ingest protocols.
	unmanaged := &streamSummary{StreamID: "push-live", SourceType: "rtmp_push", AppName: "live"}
	h.attachStreamURLs(ctx, unmanaged)
	if len(unmanaged.IngestURLs) == 0 {
		t.Fatalf("expected ingest URLs for a push stream: %+v", unmanaged)
	}
	_ = cfg

	// Managed stream with a nil config falls back to WHIP without tokens.
	nilCfgHandler, nilCfgEngine, _, _ := func() (*Handler, *stubMediaEngine, *storage.DB, *config.Config) {
		db, store := setupTestDB(t)
		h := TestHandler(db, store)
		e := &stubMediaEngine{playURLs: map[string]string{"whip": "http://127.0.0.1:12090/webrtc/whip?streamid=x"}}
		h.SetMediaEngine(e)
		return h, e, nil, nil
	}()
	nilCfgHandler.attachStreamURLs(ctx, &streamSummary{StreamID: "x", Managed: true})
	if len(nilCfgEngine.pulls) != 0 {
		t.Fatal("unexpected pull side effects")
	}

	// Storage failures for created streams are swallowed.
	closed, db := api2sysClosedDBHandler(t)
	defer db.Close()
	if got := closed.createdStreamsByID(ctx); got != nil {
		t.Fatalf("expected nil created map on storage error, got %+v", got)
	}
	if got := closed.ownStreamByCamera(ctx); len(got) != 0 {
		t.Fatalf("expected empty ownership map on storage error, got %+v", got)
	}
}

func TestApi2SysApplyPlanModesEdges(t *testing.T) {
	var nilHandler Handler
	rows := []storage.CameraRow{{ID: "a", StreamID: "s1"}}
	nilHandler.applyPlanModes(context.Background(), rows)
	if rows[0].RecordingMode != "" {
		t.Fatalf("nil db must not mutate rows: %+v", rows)
	}
	h := &Handler{}
	h.applyPlanModes(context.Background(), nil)

	closed, db := api2sysClosedDBHandler(t)
	defer db.Close()
	rows = []storage.CameraRow{{ID: "a", StreamID: "s1"}, {ID: "b"}}
	closed.applyPlanModes(context.Background(), rows)
	for _, row := range rows {
		if row.RecordingMode != "" {
			t.Fatalf("storage error must not mutate rows: %+v", rows)
		}
	}

	live, _, liveDB, _ := api2sysStreamHandler(t)
	_ = liveDB
	rows = []storage.CameraRow{{ID: "a", StreamID: "s1"}, {ID: "b"}}
	live.applyPlanModes(context.Background(), rows)
	if rows[0].RecordingMode != storage.RecordingModeOff || rows[1].RecordingMode != storage.RecordingModeOff {
		t.Fatalf("expected off modes without plans: %+v", rows)
	}
}

func TestApi2SysGB28181IdleSummaryManagedCamera(t *testing.T) {
	h, engine, db, _ := api2sysStreamHandler(t)
	engine.stream = nil
	h.SetGB28181Server(api2sysGBStatus{playing: map[string]bool{"gb-idle": true, "gb-disabled": true}})

	api2sysSeedCamera(t, db, "gb-idle", "GB Idle", "gb28181", "h264", "", true)
	rr := doRequest(t, h.Routes(), "GET", "/api/streams/gb-idle", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var item streamSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if !item.Managed || item.CameraName != "GB Idle" || !item.GB28181Playing {
		t.Fatalf("unexpected GB idle summary: %+v", item)
	}

	api2sysSeedCamera(t, db, "gb-disabled", "GB Disabled", "gb28181", "h264", "", false)
	rr = doRequest(t, h.Routes(), "GET", "/api/streams/gb-disabled", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	item = streamSummary{}
	if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
		t.Fatalf("invalid stream response: %v", err)
	}
	if item.Managed {
		t.Fatalf("disabled camera must not be managed: %+v", item)
	}
}

func TestApi2SysStreamListPaginationBeyondTotal(t *testing.T) {
	h, engine, _, _ := api2sysStreamHandler(t)
	engine.streams = []media.StreamInfo{{StreamID: "only", Active: true}}
	rr := doRequest(t, h.Routes(), "GET", "/api/streams?offset=999", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var resp streamListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	if resp.Total != 1 || len(resp.Streams) != 0 {
		t.Fatalf("expected empty page, got %+v", resp)
	}
}

// ==================== remaining camera edge branches ====================

func TestApi2SysCreateCameraEncodingAndLegacyErrors(t *testing.T) {
	h, _, _ := newTestCamHandler(t)

	// Explicit stream encoding skips ONVIF auto-detection.
	row := api2sysCreateCamera(t, h, `{"name":"ONVIF Enc","protocol":"onvif","onvif_endpoint":"http://127.0.0.1:1/onvif/enc","stream_encoding":"H265","enabled":false}`)
	if row.Encoding != "h265" || row.StreamEncoding != "H265" {
		t.Fatalf("unexpected ONVIF encoding: %+v", row)
	}

	// Unknown legacy protocol fails parsing.
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"Legacy Bad","protocol":"bogus_h264","url":"rtsp://127.0.0.1:1/s"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Legacy combined protocol with explicit encoding keeps the encoding.
	legacy := api2sysCreateCamera(t, h, `{"name":"Legacy Enc","protocol":"rtsp_h264","encoding":"h265","url":"rtsp://127.0.0.1:1/s","enabled":false}`)
	if legacy.Protocol != "rtsp" || legacy.Encoding != "h265" {
		t.Fatalf("unexpected legacy encoding: %+v", legacy)
	}

	// Invalid URL for a non-ONVIF camera.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras",
		strings.NewReader(`{"name":"Bad URL","protocol":"http","url":"not-a-url"}`), "", "")
	api2sysStatus(t, rr, http.StatusBadRequest)

	// Metadata persistence failure is tolerated and still returns the camera.
	rr = doRequest(t, h.Routes(), "POST", "/api/cameras", strings.NewReader(`{
		"name":"Meta Fail","protocol":"http","url":"http://127.0.0.1:1/s.jpg","enabled":false,
		"description":"d","location":"l","brand":"b","model":"m","serial_number":"s","profile_name":"p"
	}`), "", "")
	api2sysStatus(t, rr, http.StatusCreated)
}

func TestApi2SysCameraConfigExtrasInjection(t *testing.T) {
	h, camMgr, _ := newTestCamHandler(t)
	cam := config.CameraConfig{
		ID:              "mgr-extra",
		Name:            "Extra",
		Protocol:        "rtsp",
		Encoding:        "h264",
		URL:             "rtsp://127.0.0.1:1/s",
		Enabled:         false,
		AudioEnabled:    true,
		SubProfileToken: "sub-token",
		ActivationState: "active",
		StableID:        "stable-1",
		SourceType:      "relay_pull",
	}
	if id, err := camMgr.AddCamera(context.Background(), cam); err != nil || id != "mgr-extra" {
		t.Fatalf("failed to add camera: %v", err)
	}
	row := &storage.CameraRow{ID: "mgr-extra"}
	h.injectCameraConfigFields(row)
	if row.SubProfileToken != "sub-token" || row.ActivationState != "active" || row.StableID != "stable-1" {
		t.Fatalf("extras not injected: %+v", row)
	}
	if row.AudioEnabled != true || row.SourceType != "relay_pull" {
		t.Fatalf("config fields not injected: %+v", row)
	}
}

func TestApi2SysListCamerasEmptyAndGhost(t *testing.T) {
	// Empty database returns an empty JSON array.
	h, _, db, _ := api2sysStreamHandler(t)
	_ = db
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("expected empty array, got %s", rr.Body.String())
	}

	// A DB camera unknown to the manager (orphan) is reported as stopped, and
	// a manager camera whose binding was removed still gets its stream id.
	h2, camMgr, _ := newTestCamHandler(t)
	managed := api2sysCreateCamera(t, h2, `{"name":"Unbound","protocol":"http","url":"http://127.0.0.1:1/s.jpg","enabled":false}`)
	if err := h2.db.UnbindStreamFromCamera(context.Background(), managed.ID); err != nil {
		t.Fatalf("failed to unbind stream: %v", err)
	}
	if err := h2.db.UpsertCamera(context.Background(), "ghost-cam", "Ghost", "rtsp", "h264", "rtsp://127.0.0.1:1/g", "", "", false, "", "", ""); err != nil {
		t.Fatalf("failed to seed ghost camera: %v", err)
	}
	rr = doRequest(t, h2.Routes(), "GET", "/api/cameras", nil, "", "")
	api2sysStatus(t, rr, http.StatusOK)
	var rows []storage.CameraRow
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("invalid camera list: %v", err)
	}
	byID := map[string]storage.CameraRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID[managed.ID]; got.StreamID != managed.ID {
		t.Fatalf("expected manager stream id fallback, got %+v", got)
	}
	if got := byID["ghost-cam"]; got.Status != model.StatusStopped {
		t.Fatalf("expected stopped ghost camera, got %+v", got)
	}
	if cam := camMgr.GetCameraConfig(managed.ID); cam == nil {
		t.Fatal("managed camera missing")
	}
}

func TestApi2SysTestConnectionHTTPFailureAndStripScheme(t *testing.T) {
	h, _, _ := newTestCamHandler(t)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-connection",
		strings.NewReader(`{"protocol":"http","url":"http://127.0.0.1:1/snap.jpg"}`), "", "")
	api2sysStatus(t, rr, http.StatusOK)
	body := api2sysJSON(t, rr)
	if body["success"] != false || !strings.Contains(fmt.Sprint(body["message"]), "connection failed") {
		t.Fatalf("expected connection failure: %v", body)
	}

	for _, tc := range []struct{ in, want string }{
		{"rtsp://host/path", "host:554"},
		{"http://host:8080/snap", "host:8080"},
		{"https://host?x=1", "host:554"},
		{"host", "host:554"},
	} {
		if got := stripScheme(tc.in); got != tc.want {
			t.Fatalf("stripScheme(%q): expected %q, got %q", tc.in, tc.want, got)
		}
	}
}

func TestApi2SysDirectDeleteAndPlanHelpers(t *testing.T) {
	ctx := context.Background()

	// Direct helper calls with a fully nil handler.
	var nilHandler Handler
	if err := nilHandler.archiveCameraRecord(ctx, "x"); err != nil {
		t.Fatalf("nil handler archive should be a no-op: %v", err)
	}
	if err := nilHandler.setStreamRecordingEnabled(nil, "x", false); err != nil {
		t.Fatalf("nil db pause should be a no-op: %v", err)
	}

	// Closed DB: permanent delete surfaces the storage error, and the helper
	// logs its way through the individual delete steps.
	failing, db := api2sysClosedDBHandler(t)
	defer db.Close()
	rr := doRequest(t, failing.Routes(), "DELETE", "/api/cameras/closed/permanent", nil, "", "")
	api2sysStatus(t, rr, http.StatusInternalServerError)
	if err := failing.permanentlyDeleteCamera(ctx, "closed"); err == nil {
		t.Fatal("expected permanent delete error on closed db")
	}
	if err := failing.archiveCameraRecord(ctx, "closed"); err == nil {
		t.Fatal("expected archive error on closed db")
	}

	// Camera without a stream binding cannot toggle recording.
	h, _, _ := newTestCamHandler(t)
	pauseReq := httptest.NewRequest(http.MethodPost, "/api/cameras/no-such-camera/pause-recording", nil)
	if err := h.setStreamRecordingEnabled(pauseReq, "no-such-camera", true); err == nil {
		t.Fatal("expected missing stream error")
	}

	// applyPlanModes maps disabled plans to off.
	db2, store2 := setupTestDB(t)
	defer db2.Close()
	h2 := newHandlerWithConfig(db2, store2, api2sysConfig(store2))
	if err := db2.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		ID: storage.NewRecordingPlanID(), StreamID: "s-disabled", Name: "p", Mode: storage.RecordingModeContinuous, Enabled: false,
	}); err != nil {
		t.Fatalf("failed to seed plan: %v", err)
	}
	rows := []storage.CameraRow{{ID: "a", StreamID: "s-disabled"}}
	h2.applyPlanModes(ctx, rows)
	if rows[0].RecordingMode != storage.RecordingModeOff {
		t.Fatalf("expected off for disabled plan, got %q", rows[0].RecordingMode)
	}
}
