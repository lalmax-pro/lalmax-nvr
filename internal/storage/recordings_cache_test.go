package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRecordingsQueryCache_HitAndInvalidate(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "cache.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	now := time.Now().UTC()
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "r1", CameraID: "cam1", FilePath: "/r1.mp4", Format: model.FormatH264, StartedAt: now,
	}))

	filter := model.RecordingFilter{CameraID: "cam1"}
	first, err := db.ListRecordings(ctx, filter)
	require.NoError(t, err)
	require.Len(t, first, 1)

	first[0].FilePath = "mutated"
	second, err := db.ListRecordings(ctx, filter)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, "/r1.mp4", second[0].FilePath, "cached rows must be cloned")

	n, err := db.CountRecordingsWithFilter(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "r2", CameraID: "cam1", FilePath: "/r2.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Second),
	}))

	afterInsert, err := db.ListRecordings(ctx, filter)
	require.NoError(t, err)
	require.Len(t, afterInsert, 2)
	n, err = db.CountRecordingsWithFilter(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	require.NoError(t, db.DeleteRecording(ctx, "r1"))
	afterDelete, err := db.ListRecordings(ctx, filter)
	require.NoError(t, err)
	require.Len(t, afterDelete, 1)
	require.Equal(t, "r2", afterDelete[0].ID)
}

func TestRecordingsQueryCache_PageKeysDiffer(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "cache-page.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	now := time.Now().UTC()
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "a", CameraID: "cam", FilePath: "/a.mp4", Format: model.FormatH264, StartedAt: now,
	}))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "b", CameraID: "cam", FilePath: "/b.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Second),
	}))

	page1, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam", Limit: 1, Offset: 0, SortBy: "started_at", SortOrder: "asc"})
	require.NoError(t, err)
	page2, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "cam", Limit: 1, Offset: 1, SortBy: "started_at", SortOrder: "asc"})
	require.NoError(t, err)
	require.Len(t, page1, 1)
	require.Len(t, page2, 1)
	require.NotEqual(t, page1[0].ID, page2[0].ID)
}
