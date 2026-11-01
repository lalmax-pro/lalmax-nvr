package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func archiveTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "archive.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}

func TestArchiveCameraAndRecordings(t *testing.T) {
	ctx := context.Background()
	db := archiveTestDB(t)
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp", "h264", "rtsp://127.0.0.1/live", "", "", true, "", "", ""))
	now := time.Now().UTC()
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "r1", CameraID: "cam1", FilePath: "/r1.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(time.Minute), Duration: 60, FileSize: 100,
	}))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "r2", CameraID: "cam1", FilePath: "/r2.mp4", Format: model.FormatH264,
		StartedAt: now.Add(time.Minute), EndedAt: now.Add(2 * time.Minute), Duration: 60, FileSize: 50,
	}))

	count, size, err := db.GetCameraRecordingStats(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, int64(150), size)

	require.NoError(t, db.ArchiveCameraDB(ctx, "cam1"))
	archived, err := db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	require.Len(t, archived, 1)
	require.Equal(t, "cam1", archived[0].ID)

	n, err := db.ArchiveAllRecordings(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	count, size, err = db.GetArchiveGroupStats(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, int64(150), size)
	count, size, err = db.GetCameraRecordingStats(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, 0, count)
	require.Equal(t, int64(0), size)

	require.NoError(t, db.SetArchiveRetention(ctx, "cam1", 14))
	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, 14, cam.ArchiveRetentionDays)

	n, err = db.UnarchiveAllRecordings(ctx, "cam1")
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	require.NoError(t, db.UnarchiveCameraDB(ctx, "cam1"))

	require.ErrorIs(t, db.UnarchiveCameraDB(ctx, "cam1"), sql.ErrNoRows)
	require.ErrorIs(t, db.SetArchiveRetention(ctx, "cam1", 7), sql.ErrNoRows)
	require.ErrorIs(t, db.UnarchiveCameraDB(ctx, "missing"), sql.ErrNoRows)

	n, err = db.ArchiveAllRecordings(ctx, "missing")
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	n, err = db.UnarchiveAllRecordings(ctx, "missing")
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
}
