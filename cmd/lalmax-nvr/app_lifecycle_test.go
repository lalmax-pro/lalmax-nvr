package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
	"github.com/stretchr/testify/require"
)

// testConfig builds a default configuration whose storage lives in a temporary
// directory, mirroring a fresh auto-initialised installation.
func testConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NVR_DATA_DIR", dir)
	t.Setenv("NVR_PASSWORD", "")
	cfgPath := filepath.Join(dir, "config", "lalmax-nvr.yaml")
	return autoInitConfig(cfgPath), cfgPath
}

// useHTTPMedia switches the app to the externally managed lalmax HTTP engine.
// The embedded engine binds its RTC UDP mux port when constructed and only
// releases it after being started, so a single test drives the embedded engine
// and every other case uses the non-binding HTTP engine.
func useHTTPMedia(cfg *config.Config) {
	cfg.Media.Mode = "http"
	cfg.Media.LalmaxHTTPAddr = "http://127.0.0.1:8080"
	cfg.Media.LalmaxPublicURL = "http://127.0.0.1:8080"
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(freeTCPPort(t)))
}

func waitForTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener %s never became reachable", addr)
}

func TestNewAppWiresRuntimeAndRouter(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.httpServer)
	require.NotNil(t, app.apiHandler)
	require.NotNil(t, app.mediaEngine)
	require.NotNil(t, app.recTasks)
	require.NotNil(t, app.dlna)
	require.NotNil(t, app.iptvSvc)
	require.NotNil(t, app.embySvc)
	require.Equal(t, cfg.Server.Listen, app.httpServer.Addr)

	handler := app.httpServer.Handler

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/docs/", rec.Header().Get("Location"))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	require.Less(t, rec.Code, http.StatusInternalServerError)

	// /metrics is behind authentication; a passwordless instance rejects it.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.NotEqual(t, http.StatusOK, rec.Code)

	// Unknown paths fall through to the SPA file server.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
	require.Less(t, rec.Code, http.StatusInternalServerError)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/health", nil))
	require.Less(t, rec.Code, http.StatusInternalServerError)

	require.NoError(t, app.Stop())
}

func TestNewAppRejectsInvalidMediaAuth(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.Media.RTSPAuthEnable = true
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "invalid media config")
}

func TestNewAppFailsWhenStorageDirCannotBeCreated(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))
	cfg.Storage.RootDir = filepath.Join(blocker, "storage")
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "create storage dir")
}

func TestNewAppFailsOnDatabaseOpen(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.Storage.DatabaseDriver = "not-a-driver"
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "db open")
}

func TestNewAppRejectsInvalidJT808Config(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.JT808.Enabled = true
	cfg.JT808.MediaIP = ""
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "jt808 config")
}

func TestNewAppCreatesJT808ServerWithFTPDefaults(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.JT808.Enabled = true
	cfg.JT808.Port = freeTCPPort(t)
	cfg.JT808.MediaIP = "127.0.0.1"
	cfg.JT808.AuthCode = "secret"
	enabled := true
	cfg.FTP.Enabled = &enabled
	cfg.FTP.Port = 2121
	cfg.Auth.Username = "ftp-user"
	cfg.Auth.Password = "ftp-pass"
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.jt808Svr)
	require.NoError(t, app.Stop())
}

func TestNewAppRejectsInvalidGB28181Config(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	enabled := true
	cfg.GB28181 = config.GB28181Config{
		Enabled:  &enabled,
		ID:       "34020000002000000001",
		Password: "",
		MediaIP:  "127.0.0.1",
		Port:     5060,
	}
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "gb28181 config")
}

func TestNewAppSkipsGB28181WithoutID(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	enabled := true
	cfg.GB28181 = config.GB28181Config{Enabled: &enabled}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.Nil(t, app.gb28181Svr)
	require.NoError(t, app.Stop())
}

func TestNewAppStartsGB28181Server(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	enabled := true
	cfg.GB28181 = config.GB28181Config{
		Enabled:  &enabled,
		ID:       "34020000002000000001",
		Password: "secret",
		MediaIP:  "127.0.0.1",
		Port:     freeTCPPort(t),
	}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.gb28181Svr)
	require.NotNil(t, app.CurrentGB28181Server())
	require.NoError(t, app.Stop())
}

func TestNewAppFailsOnMediaEngine(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	cfg.Media.Mode = "http"
	cfg.Media.LalmaxHTTPAddr = ""
	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "media engine")
}

func TestStartFailsWhenJT808PortIsBusy(t *testing.T) {
	// The JT808 listener binds 0.0.0.0, so occupy the wildcard address.
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	t.Cleanup(func() { occupied.Close() })

	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.JT808.Enabled = true
	cfg.JT808.Port = occupied.Addr().(*net.TCPAddr).Port
	cfg.JT808.MediaIP = "127.0.0.1"
	cfg.JT808.AuthCode = "secret"
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	startErr := make(chan error, 1)
	go func() { startErr <- app.Start() }()
	select {
	case err := <-startErr:
		require.ErrorContains(t, err, "start JT808 signaling")
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not fail on an occupied JT808 port")
	}
	require.NoError(t, app.Stop())
}

func TestStartFailsOnInvalidVoIPConfig(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	cfg.VoIP.Enable = true
	cfg.VoIP.SipListenAddr = "not-an-address"
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	err = app.Start()
	require.ErrorContains(t, err, "start VoIP signaling")
	require.NoError(t, app.Stop())
}

func TestAppStartAndGracefulShutdown(t *testing.T) {
	// The embedded media engine and FTP server may create files in the working
	// directory, so keep them out of the repository.
	t.Chdir(t.TempDir())
	cfg, cfgPath := testConfig(t)
	addr := freeTCPAddr(t)
	cfg.Server.Listen = addr
	// The FTP listener's dependency races on start/stop; it is unrelated to the
	// shutdown ordering asserted here.
	ftpDisabled := false
	cfg.FTP.Enabled = &ftpDisabled
	cfg.JT808.Enabled = true
	cfg.JT808.Port = freeTCPPort(t)
	cfg.JT808.MediaIP = "127.0.0.1"
	cfg.JT808.AuthCode = "secret"
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	// The embedded engine is the only configuration that registers the WHEP
	// on-demand hook and the group writer.
	_, embedded := app.mediaEngine.(*media.EmbeddedLalmax)
	require.True(t, embedded)
	require.NotNil(t, app.gbWHEPHook)
	require.NotNil(t, app.recTasks)
	require.NotNil(t, app.groupWriter)
	require.NotNil(t, app.jt808Svr)
	t.Cleanup(func() { signal.Reset(syscall.SIGTERM) })

	done := make(chan error, 1)
	go func() { done <- app.Start() }()
	waitForTCP(t, addr)

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after SIGTERM")
	}
}

func TestAppRecordTaskHooksAndEventState(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, app.Stop()) }()

	require.False(t, app.eventActive("missing-camera"))
	app.onRecordTaskStart("missing-camera", nil)
	app.onRecordTaskStop("missing-camera", nil)
	require.Error(t, app.resumeEventRecording(context.Background(), "missing-camera"))
	require.Error(t, app.pauseEventRecording(context.Background(), "missing-camera"))
}

func TestAppRestartVoIPRequiresEmbeddedMedia(t *testing.T) {
	app := &App{mediaEngine: listStreamsEngine{}}
	next := voipConfigForTest(t)
	require.ErrorContains(t, app.RestartVoIP(context.Background(), &next), "requires embedded media")
	app.stopVoIP()
}

func TestNewMediaEngineRejectsBrokenEmbeddedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lalmax.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	_, err := newMediaEngine(&config.Config{Media: config.MediaConfig{LalmaxConfigPath: path}}, "token")
	require.Error(t, err)
}

func voipConfigForTest(t *testing.T) voip.Config {
	t.Helper()
	cfg := voip.Config{Enable: true, SipListenAddr: "127.0.0.1:0"}
	cfg.Normalize()
	require.NoError(t, cfg.Validate())
	return cfg
}
