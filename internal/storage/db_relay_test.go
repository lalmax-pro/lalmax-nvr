package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelayTaskRepository(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	require.NoError(t, db.Ping(ctx))

	startedAt := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	task := RelayTaskRecord{
		ID:        "task-1",
		StreamID:  "stream-1",
		TargetURL: "rtmp://example.test/live",
		Status:    "running",
		CreatedAt: startedAt.Add(-time.Minute),
		StartedAt: &startedAt,
	}
	require.NoError(t, db.SaveRelayTask(ctx, task))

	got, err := db.GetRelayTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task, *got)

	task.Status = "stopped"
	task.StoppedAt = &startedAt
	require.NoError(t, db.SaveRelayTask(ctx, task))
	got, err = db.GetRelayTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task, *got)

	rows, err := db.ListRelayTasks(ctx)
	require.NoError(t, err)
	require.Equal(t, []RelayTaskRecord{task}, rows)

	require.NoError(t, db.DeleteRelayTask(ctx, task.ID))
	_, err = db.GetRelayTask(ctx, task.ID)
	require.Error(t, err)
}
