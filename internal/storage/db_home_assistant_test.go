package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHomeAssistantIdentitySurvivesReopenAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nvr.db")
	db, err := New(path)
	require.NoError(t, err)
	require.NoError(t, db.Init(ctx))
	cfg, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	identity := cfg.InstanceID
	cfg.Enabled = true
	cfg.InstanceID = "must-not-replace"
	cfg.RTSPBaseURL = "rtsp://nvr.lan:15544"
	cfg.Streams = []HomeAssistantPublication{{StreamID: "push-1", AppName: "custom", Name: "Door"}}
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	require.NoError(t, db.Close())
	db, err = New(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(ctx))
	cfg, err = db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, identity, cfg.InstanceID)
	require.True(t, cfg.Enabled)
	require.Len(t, cfg.Streams, 1)
	backup := filepath.Join(t.TempDir(), "backup.db")
	require.NoError(t, db.Backup(ctx, backup))
	restored, err := New(backup)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	require.NoError(t, restored.Init(ctx))
	got, err := restored.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, cfg, got)
}
