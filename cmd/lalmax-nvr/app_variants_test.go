package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	lalmaxserver "github.com/q191201771/lalmax/server"
	"github.com/stretchr/testify/require"
)

func TestNewAppPersistsHashedPassword(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NVR_DATA_DIR", dir)
	t.Setenv("NVR_PASSWORD", "unit-test-password")
	cfgPath := filepath.Join(dir, "config", "lalmax-nvr.yaml")
	cfg := autoInitConfig(cfgPath)
	require.Equal(t, "unit-test-password", cfg.Auth.Password)
	useHTTPMedia(cfg)

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.Empty(t, app.cfg.Auth.Password)
	require.NotEmpty(t, app.cfg.Auth.PasswordHash)

	loaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.Auth.PasswordHash)
	require.Empty(t, loaded.Auth.Password)
	require.NoError(t, app.Stop())
}

func TestNewAppWiresOptionalMQTTClient(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.MQTT.Enabled = true
	cfg.MQTT.Broker = "tcp://127.0.0.1:1"
	cfg.MQTT.ClientID = "nvr-test"
	cfg.MQTT.Topic = "nvr"
	// An unparsable retention only disables health cleanup.
	cfg.Health.Enabled = true
	cfg.Health.EventsRetention = "not-a-duration"

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.mqttClient)
	require.NoError(t, app.Stop())
}

func TestNewAppMountsRemoteLogHandler(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	// The bridge installed by NewApp must not wrap the default handler, which
	// would send every log line through the same writer again. Use a plain
	// discard handler as the local sink.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.RemoteLog.Enabled = true
	cfg.RemoteLog.Endpoint = "http://127.0.0.1:1/insert/jsonline"
	cfg.RemoteLog.Format = "jsonline"
	cfg.Observability.LogLevel = "debug"

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.remoteLogHandler)
	require.NotEqual(t, original, slog.Default())
	require.NoError(t, app.Stop())
	slog.SetDefault(original)
}

func TestNewAppReportsCameraSyncFailure(t *testing.T) {
	cfg, _ := testConfig(t)
	useHTTPMedia(cfg)
	cfg.Cameras = []config.CameraConfig{{
		ID:       "cam1",
		Name:     "Camera 1",
		Protocol: "rtsp",
		URL:      "rtsp://127.0.0.1:554/live",
	}}
	// Cameras migrate to the database and are then stripped from the YAML file;
	// an unwritable config path surfaces that failure.
	unwritable := filepath.Join(t.TempDir(), "missing", "lalmax-nvr.yaml")
	app, err := NewApp(cfg, unwritable)
	require.Nil(t, app)
	require.ErrorContains(t, err, "sync cameras from storage")
}

func TestAppRecordTaskCallbacksWithCamera(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.Health.Enabled = true
	cfg.Cameras = []config.CameraConfig{{
		ID:       "cam1",
		Name:     "Camera 1",
		Protocol: "rtsp",
		URL:      "rtsp://127.0.0.1:554/live",
		Enabled:  false,
	}}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, app.Stop()) }()

	cam := app.camMgr.GetCameraConfig("cam1")
	require.NotNil(t, cam)
	streamID := config.IngestStreamID(*cam)

	// Direct callback invocation covers the health bookkeeping branches.
	app.onRecordTaskStart(streamID, nil)
	app.onRecordTaskStop(streamID, nil)
	require.False(t, app.eventActive(streamID))

	// Driving the recorder through the event manager exercises the task
	// manager callbacks installed by wireRecordTasks.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = app.resumeEventRecording(ctx, "cam1")
	_ = app.pauseEventRecording(ctx, "cam1")
}

func TestAppGBWHEPGuards(t *testing.T) {
	app := &App{}
	require.False(t, app.gbWHEPEligible("34020000002000000001", "34020000001320000001"))
	require.ErrorContains(t, app.startGBWHEP("34020000002000000001", "34020000001320000001", "stream"), "disabled")

	hook := newGBWHEPOnDemandHook(nil, nil)
	require.Equal(t, "nvr-gb28181-whep-on-demand", hook.Name())
	hook.SetEnabled(true)
	// Eligible/start callbacks are nil, so a known channel is still ignored.
	require.NoError(t, hook.OnHookEvent(lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventStreamNotFound,
		Payload: []byte(`{"app":"live","stream":"34020000002000000001:34020000001320000001","schema":"whep"}`),
	}))
	require.Error(t, hook.OnHookEvent(lalmaxserver.HookEvent{
		Event:   lalmaxserver.HookEventStreamNotFound,
		Payload: []byte(`{invalid`),
	}))
}

func TestRestartGB28181CreatesServer(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	enabled := true
	gbCfg := &config.GB28181Config{
		Enabled:  &enabled,
		ID:       "34020000002000000001",
		Password: "secret",
		MediaIP:  "127.0.0.1",
		Port:     freeTCPPort(t),
	}
	require.NoError(t, app.RestartGB28181(context.Background(), gbCfg))
	require.NotNil(t, app.CurrentGB28181Server())
	// Devices that never registered cannot be pulled on demand.
	require.False(t, app.gbWHEPEligible("34020000002000000001", "34020000001320000001"))
	require.ErrorContains(t, app.startGBWHEP("34020000002000000001", "34020000001320000001", "stream"), "not online")

	// Occupying the SIP port makes a restart fail without losing the old server.
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err == nil {
		t.Cleanup(func() { occupied.Close() })
		busy := *gbCfg
		busy.Port = occupied.Addr().(*net.TCPAddr).Port
		require.Error(t, app.RestartGB28181(context.Background(), &busy))
	}
	require.NoError(t, app.Stop())
}

func TestRestartProcessInPlaceReportsExecFailure(t *testing.T) {
	err := restartProcessInPlace(filepath.Join(t.TempDir(), "missing-binary"), []string{"missing-binary"}, nil)
	require.Error(t, err)
}

func TestNewAppFailsOnGB28181ListenError(t *testing.T) {
	occupied, err := net.ListenPacket("udp", "0.0.0.0:0")
	require.NoError(t, err)
	t.Cleanup(func() { occupied.Close() })

	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	enabled := true
	cfg.GB28181 = config.GB28181Config{
		Enabled:  &enabled,
		ID:       "34020000002000000001",
		Password: "secret",
		MediaIP:  "127.0.0.1",
		Port:     occupied.LocalAddr().(*net.UDPAddr).Port,
	}
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "start GB28181 SIP listeners")
}

func TestNewAppFailsOnCleanupConfig(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.Cleanup.CheckInterval = "not-a-duration"
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "cleanup")
}

func TestNewAppRouterVariants(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	enabled := true
	cfg.HLS.Enabled = &enabled
	cfg.HLS.LowLatency = true
	cfg.WHIP.Enabled = &enabled
	cfg.WebDAV.Enabled = &enabled
	cfg.MetricsAuth = config.MetricsAuthConfig{Username: "metrics", Password: "metrics-pass"}

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	handler := app.httpServer.Handler

	// /metrics uses its dedicated credentials.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.NotEqual(t, http.StatusOK, rec.Code)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("metrics", "metrics-pass")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// WebDAV is mounted at the configured prefix and stays behind auth.
	davReq := httptest.NewRequest("PROPFIND", "/dav/", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, davReq)
	require.NotEqual(t, http.StatusNotFound, rec.Code)
	require.NotEqual(t, http.StatusOK, rec.Code)

	require.NoError(t, app.Stop())
}

func TestAppRouterWithWebDAVDisabled(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	disabled := false
	cfg.WebDAV.Enabled = &disabled
	cfg.WHIP.Enabled = &disabled
	cfg.HLS.Enabled = &disabled
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	app.httpServer.Handler.ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/dav/", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, app.Stop())
}
