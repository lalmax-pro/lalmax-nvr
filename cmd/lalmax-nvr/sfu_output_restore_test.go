package main

import (
	"context"
	"path/filepath"
	"strings"
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

func TestSFURecordingSourceTimeUsesRegisteredBridgeIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "output.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(ctx))
	app := &App{db: db}
	bridgeID := "sfu_" + strings.Repeat("a", 48)
	otherID := "sfu_" + strings.Repeat("b", 48)
	require.NoError(t, db.RegisterSFUBridgeOutput(ctx, bridgeID))
	require.NoError(t, db.RegisterTranscodeOutput(ctx, otherID, "source"))
	got, err := app.sfuRecordingSourceTime(ctx, bridgeID)
	require.NoError(t, err)
	require.True(t, got)
	got, err = app.sfuRecordingSourceTime(ctx, otherID)
	require.NoError(t, err)
	require.False(t, got)
	got, err = app.sfuRecordingSourceTime(ctx, "ordinary-camera")
	require.NoError(t, err)
	require.False(t, got)
	app.db = nil
	_, err = app.sfuRecordingSourceTime(ctx, bridgeID)
	require.Error(t, err)
}
