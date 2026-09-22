package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetCreatedStreamName_InsertsAndUpdates(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.SetCreatedStreamName(ctx, "room-1", "会议室"))
	got, err := db.GetCreatedStream(ctx, "room-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "会议室", got.Name)
	require.Equal(t, CreatedStreamPush, got.InputMode)
	require.Empty(t, got.SourceURL)

	require.NoError(t, db.SetCreatedStreamName(ctx, "room-1", "大会议室"))
	got, err = db.GetCreatedStream(ctx, "room-1")
	require.NoError(t, err)
	require.Equal(t, "大会议室", got.Name)
}

func TestSetCreatedStreamName_PreservesPullConfig(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.InsertCreatedStream(ctx, CreatedStream{
		StreamID:  "src-1",
		Name:      "门口",
		AppName:   "live",
		InputMode: CreatedStreamPull,
		SourceURL: "rtsp://192.168.1.20/live",
	}))
	require.NoError(t, db.SetCreatedStreamName(ctx, "src-1", "北门"))

	got, err := db.GetCreatedStream(ctx, "src-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "北门", got.Name)
	require.Equal(t, CreatedStreamPull, got.InputMode)
	require.Equal(t, "rtsp://192.168.1.20/live", got.SourceURL)
	require.Equal(t, "live", got.AppName)
}

func TestSetCreatedStreamName_EmptyFallsBackToStreamID(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.SetCreatedStreamName(ctx, "room-1", "  "))
	got, err := db.GetCreatedStream(ctx, "room-1")
	require.NoError(t, err)
	require.Equal(t, "room-1", got.Name)
}
