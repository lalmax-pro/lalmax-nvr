package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSFUBridgeOutputUsesPersistentRegistry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sfu-output.db")
	db, err := New(path)
	require.NoError(t, err)
	require.NoError(t, db.Init(ctx))
	found, err := db.IsSFUBridgeOutput(ctx, "sfu-unregistered")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, db.RegisterTranscodeOutput(ctx, "sfu-transcode", "source"))
	found, err = db.IsSFUBridgeOutput(ctx, "sfu-transcode")
	require.NoError(t, err)
	require.False(t, found, "another internal output is not an SFU bridge")
	require.NoError(t, db.RegisterSFUBridgeOutput(ctx, "sfu-bridge"))
	require.NoError(t, db.Close())
	reopened, err := New(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, reopened.Init(ctx))
	found, err = reopened.IsSFUBridgeOutput(ctx, "sfu-bridge")
	require.NoError(t, err)
	require.True(t, found)
}
