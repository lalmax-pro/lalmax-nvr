package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	lalmaxserver "github.com/q191201771/lalmax/server"
	"github.com/stretchr/testify/require"
)

type listStreamsEngine struct {
	media.Engine
	streams []media.StreamInfo
	err     error
}

func (e listStreamsEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	return e.streams, e.err
}

func TestCloneHelpers(t *testing.T) {
	require.Nil(t, cloneBool(nil))
	value := true
	copied := cloneBool(&value)
	require.NotNil(t, copied)
	require.True(t, *copied)
	value = false
	require.True(t, *copied)

	source := map[string]string{"cam": "token"}
	cloned := cloneStringMap(source)
	require.Equal(t, source, cloned)
	source["cam"] = "mutated"
	require.Equal(t, "token", cloned["cam"])
	require.Empty(t, cloneStringMap(nil))
}

func TestDockerStorageDirUsesEnvThenFallback(t *testing.T) {
	t.Setenv("NVR_DATA_DIR", "")
	require.Equal(t, "", dockerStorageDir())

	dir := t.TempDir()
	t.Setenv("NVR_DATA_DIR", dir)
	require.Equal(t, dir, dockerStorageDir())
}

func TestResolveConfigPath(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "lalmax-nvr.yaml")
	require.Equal(t, original, resolveConfigPath(original))

	require.NoError(t, os.WriteFile(original+".target", []byte(" \n"), 0o644))
	require.Equal(t, original, resolveConfigPath(original))

	missing := filepath.Join(dir, "missing.yaml")
	require.NoError(t, os.WriteFile(original+".target", []byte(missing+"\n"), 0o644))
	require.Equal(t, original, resolveConfigPath(original))

	target := filepath.Join(dir, "canonical.yaml")
	require.NoError(t, os.WriteFile(target, []byte("version: 1.0\n"), 0o644))
	require.NoError(t, os.WriteFile(original+".target", []byte(target+"\n"), 0o644))
	require.Equal(t, target, resolveConfigPath(original))
}

func TestAutoInitConfigWritesDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NVR_DATA_DIR", dir)
	t.Setenv("NVR_PASSWORD", "unit-test-password")
	cfgPath := filepath.Join(dir, "config", "lalmax-nvr.yaml")

	cfg := autoInitConfig(cfgPath)
	require.Equal(t, dir, cfg.Storage.RootDir)
	require.Equal(t, "admin", cfg.Auth.Username)
	require.Equal(t, "unit-test-password", cfg.Auth.Password)
	require.FileExists(t, cfgPath)

	loaded, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, dir, loaded.Storage.RootDir)
	require.Equal(t, ":9090", loaded.Server.Listen)
}

func TestToLalMaxOptsFiltersNonOptions(t *testing.T) {
	keep := lalmaxserver.WithHTTPPlayAuth(nil)
	got := toLalMaxOpts([]interface{}{keep, "ignored", 7, nil})
	require.Len(t, got, 1)
	require.Empty(t, toLalMaxOpts(nil))
}

func TestNewMediaEngineHTTPMode(t *testing.T) {
	_, err := newMediaEngine(&config.Config{Media: config.MediaConfig{Mode: "http"}}, "token")
	require.ErrorContains(t, err, "lalmax base URL is required")

	engine, err := newMediaEngine(&config.Config{Media: config.MediaConfig{
		Mode:            "http",
		LalmaxHTTPAddr:  "http://127.0.0.1:8080",
		LalmaxPublicURL: "http://127.0.0.1:8080",
	}}, "token")
	require.NoError(t, err)
	_, ok := engine.(*media.LalmaxHTTP)
	require.True(t, ok)
}

func TestAliveRecordingStreams(t *testing.T) {
	app := &App{}
	ids, err := app.aliveRecordingStreams(context.Background())
	require.NoError(t, err)
	require.Nil(t, ids)

	app.mediaEngine = listStreamsEngine{err: errors.New("engine down")}
	_, err = app.aliveRecordingStreams(context.Background())
	require.ErrorContains(t, err, "engine down")

	app.mediaEngine = listStreamsEngine{streams: []media.StreamInfo{
		{StreamID: ""},
		{StreamID: "cam_sub", Active: true},
		{StreamID: "idle"},
		{StreamID: "live", Active: true},
		{StreamID: "vod", VideoCodec: "H264"},
	}}
	ids, err = app.aliveRecordingStreams(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"live", "vod"}, ids)
}

func TestEventActiveWithoutManager(t *testing.T) {
	app := &App{}
	require.False(t, app.eventActive("cam"))
}

func TestRestartGB28181DisabledAndEmptyID(t *testing.T) {
	hook := newGBWHEPOnDemandHook(nil, nil)
	hook.SetEnabled(true)
	app := &App{gbWHEPHook: hook, gb28181Svr: &gb28181.Server{}}

	disabled := false
	require.NoError(t, app.RestartGB28181(context.Background(), &config.GB28181Config{Enabled: &disabled, ID: "34020000002000000001"}))
	require.Nil(t, app.CurrentGB28181Server())

	hook.SetEnabled(true)
	enabled := true
	require.NoError(t, app.RestartGB28181(context.Background(), &config.GB28181Config{Enabled: &enabled}))
	require.Nil(t, app.CurrentGB28181Server())

	err := app.RestartGB28181(context.Background(), &config.GB28181Config{
		Enabled:  &enabled,
		ID:       "short",
		Password: "secret",
		MediaIP:  "192.0.2.10",
		Port:     5060,
	})
	require.ErrorContains(t, err, "gb28181 config")
	require.Nil(t, app.CurrentGB28181Server())

	err = app.RestartGB28181(context.Background(), &config.GB28181Config{
		Enabled:  &enabled,
		ID:       "34020000002000000001",
		Password: "",
		MediaIP:  "192.0.2.10",
		Port:     5060,
	})
	require.ErrorContains(t, err, "password is required")
	require.Nil(t, app.CurrentGB28181Server())

	err = app.RestartGB28181(context.Background(), &config.GB28181Config{
		Enabled:  &enabled,
		ID:       "34020000002000000001",
		Password: "secret",
		MediaIP:  "192.0.2.10",
		Port:     0,
	})
	require.ErrorContains(t, err, "1-65535")
	require.Nil(t, app.CurrentGB28181Server())
}

func TestWireRecordTasksRequiresEngineAndCameraManager(t *testing.T) {
	app := &App{cfg: &config.Config{}}
	app.wireRecordTasks()
	require.Nil(t, app.recTasks)

	app.mediaEngine = listStreamsEngine{}
	app.wireRecordTasks()
	require.Nil(t, app.recTasks)
}

func TestRecordTaskHealthHooksWithoutManagers(t *testing.T) {
	app := &App{}
	app.onRecordTaskStart("cam", nil)
	app.onRecordTaskStop("cam", nil)
}
