package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestTranscodeOutputIdentitySurvivesDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outputs.db")
	db, err := New(path)
	require.NoError(t, err)
	require.NoError(t, db.Init(ctx))
	require.NoError(t, db.RegisterTranscodeOutput(ctx, "random-physical-id", "source"))
	require.NoError(t, db.Close())
	reopened, err := New(path)
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, reopened.Init(ctx))
	ids, err := reopened.ListTranscodeOutputIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"random-physical-id"}, ids)
	policy := auth.NewMediaAuthorizer()
	for _, id := range ids {
		policy.RestoreInternalStream(id)
	}
	require.True(t, policy.InternalPlaybackRequired("random-physical-id"))
	require.Error(t, policy.AuthorizePlayback("rtsp", "random-physical-id", ""))
	require.Error(t, policy.AuthorizePublish("rtmp", "random-physical-id", "old-token"))
	require.False(t, policy.InternalPlaybackRequired("user-nvr_tc_camera"), "never infer registry membership from name prefixes")
}
