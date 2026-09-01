package merge

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHourWindowUTC(t *testing.T) {
	at := time.Date(2026, 5, 1, 14, 37, 11, 0, time.UTC)
	start, end := hourWindowUTC(at)
	require.Equal(t, time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC), end)
}

func TestMergeHour_AppendsPendingToBucket(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)
	ctx := context.Background()

	hour := time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC)
	camID := "cam-roll"
	require.NoError(t, env.db.UpsertCamera(ctx, camID, "Roll", "rtsp", "h264", "rtsp://x", "", "", true, "", "", "", "tcp"))

	p1 := env.insertMergeableRecording(t, "p1", camID, hour.Add(1*time.Minute), hour.Add(90*time.Second))
	p2 := env.insertMergeableRecording(t, "p2", camID, hour.Add(2*time.Minute), hour.Add(150*time.Second))
	_ = p1
	_ = p2

	cfg := config.MergeConfig{Enabled: true, MinSegmentsToMerge: 3, BatchLimit: 100, MinSegmentAge: "10m"}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: camID, Enabled: true}})

	require.NoError(t, mgr.MergeHour(ctx, camID, hour.Add(3*time.Minute)))

	recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: camID})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.True(t, recs[0].Merged)
	bucketID := recs[0].ID

	env.insertMergeableRecording(t, "p3", camID, hour.Add(4*time.Minute), hour.Add(270*time.Second))
	require.NoError(t, mgr.MergeHour(ctx, camID, hour.Add(5*time.Minute)))

	recs, err = env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: camID})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.True(t, recs[0].Merged)
	require.NotEqual(t, bucketID, recs[0].ID)
}

func TestMergeHour_SPSChangeOpensNewBucket(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)
	ctx := context.Background()

	hour := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	camID := "cam-sps"
	require.NoError(t, env.db.UpsertCamera(ctx, camID, "SPS", "rtsp", "h264", "rtsp://x", "", "", true, "", "", "", "tcp"))

	env.insertMergeableRecording(t, "a1", camID, hour.Add(1*time.Minute), hour.Add(90*time.Second))
	env.insertMergeableRecording(t, "a2", camID, hour.Add(2*time.Minute), hour.Add(150*time.Second))

	cfg := config.MergeConfig{Enabled: true, MinSegmentsToMerge: 3, BatchLimit: 100}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: camID, Enabled: true}})
	require.NoError(t, mgr.MergeHour(ctx, camID, hour.Add(3*time.Minute)))

	// Different SPS/PPS — must not append onto the existing hour file.
	sps := []byte{0x67, 0x42, 0x00, 0x1e, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x81}
	other := createTestH264SegmentWithParams(t, env.dir, sps, pps)
	tempPath, finalPath, err := env.store.CreateSegment(camID, "h264")
	require.NoError(t, err)
	data, err := os.ReadFile(other)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0644))
	require.NoError(t, env.store.CloseSegment(tempPath, finalPath))
	fi, err := os.Stat(finalPath)
	require.NoError(t, err)
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "b1", CameraID: camID, FilePath: finalPath, Format: model.FormatH264,
		StartedAt: hour.Add(4 * time.Minute), EndedAt: hour.Add(270 * time.Second),
		Duration: 30, FileSize: fi.Size(), FrameCount: 2,
	}))

	require.NoError(t, mgr.MergeHour(ctx, camID, hour.Add(5*time.Minute)))
	recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: camID})
	require.NoError(t, err)
	require.Len(t, recs, 2, "incompatible SPS should leave a separate pending/merged file")
}

func TestRollingCoordinator_DebounceBatches(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hour := time.Now().UTC().Truncate(time.Hour)
	camID := "cam-deb"
	require.NoError(t, env.db.UpsertCamera(ctx, camID, "Deb", "rtsp", "h264", "rtsp://x", "", "", true, "", "", "", "tcp"))
	env.insertMergeableRecording(t, "d1", camID, hour.Add(1*time.Minute), hour.Add(90*time.Second))
	env.insertMergeableRecording(t, "d2", camID, hour.Add(2*time.Minute), hour.Add(150*time.Second))

	enabled := true
	cfg := config.MergeConfig{Enabled: true, RollingEnabled: &enabled, RollingDebounce: "20ms", MinSegmentsToMerge: 3, BatchLimit: 100}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: camID, Enabled: true}})
	bus := event.NewEventBus(8)
	coord := NewRollingCoordinator(mgr, func() config.MergeConfig { return cfg }, bus)

	done := make(chan struct{})
	go func() {
		coord.Start(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	bus.Publish(ctx, event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: camID, StartedAt: hour.Add(1 * time.Minute).Format(time.RFC3339Nano),
	})
	bus.Publish(ctx, event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID: camID, StartedAt: hour.Add(2 * time.Minute).Format(time.RFC3339Nano),
	})

	require.Eventually(t, func() bool {
		recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: camID})
		return err == nil && len(recs) == 1 && recs[0].Merged
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	<-done
}
