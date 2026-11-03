package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// main3HTTPConfig builds a default configuration backed by 127.0.0.1 media.
func main3HTTPConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	return cfg, cfgPath
}

// TestMain3RemoteLogLevels covers the log level mapping of the remote log
// bridge for every supported level.
func TestMain3RemoteLogLevels(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, level := range []string{"warn", "error", "trace", "debug"} {
		level := level
		t.Run(level, func(t *testing.T) {
			cfg, cfgPath := main3HTTPConfig(t)
			cfg.RemoteLog.Enabled = true
			cfg.RemoteLog.Endpoint = "http://127.0.0.1:1/insert/jsonline"
			cfg.RemoteLog.Format = "jsonline"
			cfg.Observability.LogLevel = level
			app, err := NewApp(cfg, cfgPath)
			require.NoError(t, err)
			require.NotNil(t, app.remoteLogHandler)
			require.NoError(t, app.Stop())
		})
	}
	slog.SetDefault(original)
}

// TestMain3NewAppWarnsAboutDisabledPublishAuth covers the RTMP/RTSP publish
// authentication warnings emitted at bootstrap.
func TestMain3NewAppWarnsAboutDisabledPublishAuth(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	rtmpOn := true
	rtmpAuthOff := false
	cfg.RTMP.Enabled = &rtmpOn
	cfg.RTMP.PublishAuthEnabled = &rtmpAuthOff
	cfg.Media.RTSPAuthEnable = false
	rtspPublishOff := false
	cfg.Media.RTSPPublishAuthEnabled = &rtspPublishOff

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NoError(t, app.Stop())
}

// TestMain3NewAppPersistsPasswordFailure covers the non-fatal save failure
// after the legacy password is auto-hashed.
func TestMain3NewAppPersistsPasswordFailure(t *testing.T) {
	cfg, _ := main3HTTPConfig(t)
	cfg.Auth.Password = "unit-test-password"
	unwritable := filepath.Join(main3BlockerFile(t), "config", "lalmax-nvr.yaml")

	app, err := NewApp(cfg, unwritable)
	require.NoError(t, err)
	require.NotEmpty(t, app.cfg.Auth.PasswordHash)
	require.Empty(t, app.cfg.Auth.Password)
	require.NoError(t, app.Stop())
}

// TestMain3NewAppReportsOrphanedFiles covers the orphan reconciliation branch of
// the storage step.
func TestMain3NewAppReportsOrphanedFiles(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	camDir := filepath.Join(cfg.Storage.RootDir, "cam1")
	require.NoError(t, os.MkdirAll(camDir, 0o755))
	orphan := filepath.Join(camDir, "cam1_20240101_120000_123.mp4")
	require.NoError(t, os.WriteFile(orphan, []byte("0123456789"), 0o644))
	cfg.Cameras = []config.CameraConfig{{
		ID:       "cam1",
		Name:     "Camera 1",
		Protocol: "rtsp",
		URL:      "rtsp://127.0.0.1:554/live",
		Enabled:  true,
	}}

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NoError(t, app.Stop())
}

// TestMain3NewAppIgnoresTempCleanupFailure covers the best-effort temp file
// cleanup warning: a read-only .tmp directory cannot be removed.
func TestMain3NewAppIgnoresTempCleanupFailure(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	tmpDir := filepath.Join(cfg.Storage.RootDir, "stale.tmp")
	require.NoError(t, os.MkdirAll(tmpDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "segment.mp4"), []byte("data"), 0o644))
	require.NoError(t, os.Chmod(tmpDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(tmpDir, 0o755) })

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NoError(t, app.Stop())
}

// TestMain3RestartGB28181ReenablesHook covers the hook re-enable branch of a
// successful GB28181 restart.
func TestMain3RestartGB28181ReenablesHook(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, app.Stop()) }()

	hook := newGBWHEPOnDemandHook(nil, nil)
	hook.SetEnabled(false)
	app.gbWHEPHook = hook

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
}

// TestMain3NewAppGB28181SinglePort covers the single-port startup log branch.
func TestMain3NewAppGB28181SinglePort(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	enabled := true
	cfg.GB28181 = config.GB28181Config{
		Enabled:   &enabled,
		ID:        "34020000002000000001",
		Password:  "secret",
		MediaIP:   "127.0.0.1",
		Port:      freeTCPPort(t),
		MediaPort: freeTCPPort(t),
	}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	require.NotNil(t, app.gb28181Svr)
	require.NoError(t, app.Stop())
}

// TestMain3DBInitFailure covers the database initialisation failure path: an
// index that shadows a table created by the schema migration makes it fail.
func TestMain3DBInitFailure(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
	dbPath := filepath.Join(cfg.Storage.RootDir, "lalmax-nvr.db")
	db, err := storage.Open(context.Background(), storage.DatabaseOptions{
		Driver: storage.DriverSQLite,
		DSN:    dbPath,
	})
	require.NoError(t, err)
	_, err = db.DB().Exec("CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);")
	require.NoError(t, err)
	_, err = db.DB().Exec("CREATE INDEX feature_flags ON schema_meta(key);")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	app, err := NewApp(cfg, cfgPath)
	require.Nil(t, app)
	require.ErrorContains(t, err, "db init")
}
