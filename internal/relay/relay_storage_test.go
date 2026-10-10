package relay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestManagerPersistsTasksThroughRepository(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Init(context.Background()))

	manager := NewManager(db, nil)
	t.Cleanup(manager.Stop)

	startedAt := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	task := &Task{
		ID:        "task-1",
		StreamID:  "stream-1",
		TargetURL: "rtmp://example.test/live",
		Status:    TaskStatusRunning,
		CreatedAt: startedAt.Add(-time.Minute),
		StartedAt: &startedAt,
	}
	require.NoError(t, manager.saveTask(task))

	got, err := manager.loadTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, task, got)

	task.Status = TaskStatusStopped
	task.StoppedAt = &startedAt
	require.NoError(t, manager.saveTask(task))
	got, err = manager.loadTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, task, got)

	all, err := manager.loadAllTasks()
	require.NoError(t, err)
	require.Equal(t, []*Task{task}, all)

	require.NoError(t, manager.deleteTask(task.ID))
	_, err = manager.loadTask(task.ID)
	require.Error(t, err)
}
