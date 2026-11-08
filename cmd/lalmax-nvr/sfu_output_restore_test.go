package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestSFUOutputIdentityRestoredWhenSFUNotRunning(t *testing.T) {
	cfg, cfgPath := testConfig(t)
	useHTTPMedia(cfg)
	db, err := storage.New(filepath.Join(cfg.Storage.RootDir, "lalmax-nvr.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	require.NoError(t, db.RegisterSFUBridgeOutput(context.Background(), "sfu_retired"))
	require.NoError(t, db.Close())
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, app.Stop()) })
	require.True(t, app.mediaAuth.InternalPlaybackRequired("sfu_retired"))
	require.Error(t, app.mediaAuth.AuthorizePlayback("rtsp", "sfu_retired", ""))
	require.Error(t, app.mediaAuth.AuthorizePublish("rtmp", "sfu_retired", ""))
}
