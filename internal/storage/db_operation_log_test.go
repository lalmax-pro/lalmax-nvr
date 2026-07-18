package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestOperationLogsCRUDAndFilters(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "operation-log.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	createdAt := time.Now().UTC().Add(-time.Minute)
	_, err = db.InsertOperationLog(ctx, model.OperationLog{
		UserID:     7,
		Username:   "admin",
		Action:     "config.update",
		Resource:   "config",
		ResourceID: "hls",
		Status:     "success",
		Metadata:   `{"changed":["enabled"]}`,
		IPAddress:  "127.0.0.1",
		CreatedAt:  createdAt,
	})
	require.NoError(t, err)
	_, err = db.InsertOperationLog(ctx, model.OperationLog{
		ActorType:  "system",
		Action:     "device.online",
		Resource:   "device",
		ResourceID: "34020000001320000001",
		Status:     "success",
	})
	require.NoError(t, err)

	logs, total, err := db.ListOperationLogs(ctx, OperationLogsFilter{Action: "config.update"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, logs, 1)
	require.Equal(t, int64(7), logs[0].UserID)
	require.Equal(t, "admin", logs[0].Username)
	require.Equal(t, "hls", logs[0].ResourceID)
	require.Equal(t, `{"changed":["enabled"]}`, logs[0].Metadata)

	logs, total, err = db.ListOperationLogs(ctx, OperationLogsFilter{Resource: "device", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, logs, 1)
	require.Equal(t, "device.online", logs[0].Action)
}
