package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

type recordingContractRepository interface {
	RecordingRepository
	GetRecording(context.Context, string) (*model.Recording, error)
	GetRecordingDays(context.Context, string, string) ([]string, error)
	GetHourlyRecordingStats(context.Context, int) ([]model.HourlyStats, error)
	ListExpiredRecordings(context.Context, int) ([]model.Recording, error)
	ListCameraMergeWindows(context.Context, string, time.Duration, time.Duration) ([]MergeWindow, error)
	ListSingletonPendingRecordings(context.Context, string, time.Duration, time.Duration) ([]*model.Recording, error)
}

// TestSQLiteRepositoryContract runs the backend-neutral camera and recording
// contract against the current SQLite adapter. Other adapters can call the
// same helper from their own tests.
func TestSQLiteRepositoryContract(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "contract.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Init(context.Background()))
	runCameraRecordingRepositoryContract(t, db, db)
}

func runCameraRecordingRepositoryContract(t *testing.T, cameras CameraRepository, recordings recordingContractRepository) {
	t.Helper()
	ctx := context.Background()
	cameraID := "contract-camera"
	require.NoError(t, cameras.UpsertCamera(ctx, cameraID, "Contract camera", "rtsp", "h264", "rtsp://example.test/live", "", "", true, "", "", ""))

	camera, err := cameras.GetCamera(ctx, cameraID)
	require.NoError(t, err)
	require.NotNil(t, camera)
	require.Equal(t, cameraID, camera.ID)
	require.Equal(t, "Contract camera", camera.Name)
	require.Equal(t, "rtsp", camera.Protocol)

	startedAt := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	recording := &model.Recording{
		ID:        fmt.Sprintf("contract-recording-%d", time.Now().UnixNano()),
		CameraID:  cameraID,
		FilePath:  "/recordings/contract.mp4",
		Format:    model.FormatH264,
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(time.Minute),
		Duration:  60,
		FileSize:  4096,
	}
	require.NoError(t, recordings.InsertRecording(ctx, recording))

	got, err := recordings.GetRecording(ctx, recording.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, recording.ID, got.ID)
	require.Equal(t, cameraID, got.CameraID)
	require.Equal(t, recording.FilePath, got.FilePath)
	require.Equal(t, recording.Format, got.Format)
	require.Equal(t, int64(4096), got.FileSize)

	days, err := recordings.GetRecordingDays(ctx, cameraID, "2025-01")
	require.NoError(t, err)
	require.Equal(t, []string{"2025-01-02"}, days)
	_, err = recordings.GetHourlyRecordingStats(ctx, 24)
	require.NoError(t, err)
	_, err = recordings.ListExpiredRecordings(ctx, 30)
	require.NoError(t, err)
	_, err = recordings.ListCameraMergeWindows(ctx, cameraID, time.Minute, time.Hour)
	require.NoError(t, err)
	_, err = recordings.ListSingletonPendingRecordings(ctx, cameraID, time.Minute, time.Hour)
	require.NoError(t, err)
}
