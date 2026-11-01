package recorder

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func TestCovTimelapse_DefaultsMetricsAndHub(t *testing.T) {
	rec := NewTimelapseRecorder(TimelapseRecorderConfig{CameraID: "tl-defaults", Metrics: metrics.NewMetrics()}, newHJStore(t))
	require.Equal(t, 5*time.Second, rec.interval)
	require.Equal(t, 30, rec.outputFPS)
	require.Equal(t, "h264", rec.videoCodec)
	require.Equal(t, DefaultSegmentDur, rec.segmentDur)
	require.Nil(t, rec.GetHub())
	rec.Hub = model.NewStreamHub()
	require.NotNil(t, rec.GetHub())
	require.Equal(t, model.StatusStopped, rec.Status())

	rec.incActive()
	rec.decActive()
	rec.recordSegmentCreated()
	rec.recordError("capture")

	// Without a metrics registry the helpers are no-ops.
	plain := NewTimelapseRecorder(TimelapseRecorderConfig{CameraID: "tl-plain"}, newHJStore(t))
	plain.incActive()
	plain.decActive()
	plain.recordSegmentCreated()
	plain.recordError("capture")

	// Start/Stop drive the active gauge and the final flush.
	running := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID: "tl-lifecycle",
		Metrics:  metrics.NewMetrics(),
	}, newHJStore(t))
	require.NoError(t, running.Start(context.Background()))
	require.ErrorContains(t, running.Start(context.Background()), "already running")
	require.Equal(t, model.StatusRecording, running.Status())
	require.NoError(t, running.Stop())
	require.Equal(t, model.StatusStopped, running.Status())
}

func TestCovTimelapse_OnFrameBranches(t *testing.T) {
	store := newHJStore(t)
	rec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID:   "tl-onframe",
		Interval:   time.Millisecond,
		SegmentDur: time.Hour,
		Metrics:    metrics.NewMetrics(),
	}, store)
	rec.runCtx = context.Background()

	// Invalid JPEG markers and truncated frames are ignored.
	rec.OnFrame(nil)
	rec.OnFrame([]byte{0xFF})
	rec.OnFrame([]byte{0x00, 0x01})
	rec.OnFrame([]byte{0xD8, 0xFF, 0x00})
	frames, _, _ := store.snapshot()
	require.Zero(t, frames)

	// Frames arriving before the capture interval elapses are skipped.
	rec.interval = time.Hour
	rec.lastCapture.Store(time.Now().UnixNano())
	rec.OnFrame(generateTestJPEG())
	frames, _, _ = store.snapshot()
	require.Zero(t, frames)

	// The first frame after the interval opens a segment and is written.
	rec.interval = time.Millisecond
	rec.lastCapture.Store(0)
	rec.OnFrame(generateTestJPEG())
	frames, _, _ = store.snapshot()
	require.Equal(t, 1, frames)
	require.Equal(t, 1, rec.frameCount)

	// A failing segment store is logged and skipped.
	badStore := newHJStore(t)
	badStore.createErr = errors.New("disk full")
	badRec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID: "tl-create-err",
		Interval: time.Millisecond,
	}, badStore)
	badRec.runCtx = context.Background()
	badRec.OnFrame(generateTestJPEG())
	require.Empty(t, badRec.curTempPath)

	writeStore := newHJStore(t)
	writeStore.writeErr = errors.New("write failed")
	writeRec := NewTimelapseRecorder(TimelapseRecorderConfig{
		CameraID: "tl-write-err",
		Interval: time.Millisecond,
	}, writeStore)
	writeRec.runCtx = context.Background()
	writeRec.OnFrame(generateTestJPEG())
	require.Zero(t, writeRec.frameCount)
}

func TestCovTimelapse_MergePaths(t *testing.T) {
	t.Run("merge without an open segment", func(t *testing.T) {
		rec := NewTimelapseRecorder(TimelapseRecorderConfig{CameraID: "tl-idle"}, newHJStore(t))
		rec.runCtx = context.Background()
		rec.segMu.Lock()
		rec.triggerMerge()
		rec.segMu.Unlock()
		rec.mergeWg.Wait()
	})

	t.Run("merge completes and registers the recording", func(t *testing.T) {
		store := newHJStore(t)
		db := &hjDB{}
		rec := NewTimelapseRecorder(TimelapseRecorderConfig{
			CameraID:   "tl-merge",
			SegmentDur: time.Hour,
			DB:         db,
			Metrics:    metrics.NewMetrics(),
		}, store)
		rec.runCtx = context.Background()

		tempPath, finalPath, err := store.CreateSegment("tl-merge", "timelapse")
		require.NoError(t, err)
		_, err = store.WriteFrame(tempPath, generateTestJPEG())
		require.NoError(t, err)

		var gotArgs []string
		rec.runFF = func(_ context.Context, ffmpegPath string, args []string, outputPath string) error {
			gotArgs = args
			require.Equal(t, "ffmpeg", ffmpegPath)
			return os.WriteFile(outputPath, []byte("fake mp4"), 0o644)
		}
		rec.segMu.Lock()
		rec.curTempPath = tempPath
		rec.curFinalPath = finalPath
		rec.segStart = time.Now().Add(-time.Minute)
		rec.frameCount = 1
		rec.triggerMerge()
		rec.segMu.Unlock()
		require.Empty(t, rec.curTempPath)
		rec.mergeWg.Wait()

		_, recs := db.snapshot()
		require.GreaterOrEqual(t, len(recs), 1)
		var timelapseRec *model.Recording
		for _, r := range recs {
			if r.Format == "timelapse" {
				timelapseRec = r
			}
		}
		require.NotNil(t, timelapseRec)
		require.Equal(t, "tl-merge", timelapseRec.CameraID)
		require.Equal(t, 1, timelapseRec.FrameCount)
		require.Greater(t, timelapseRec.FileSize, int64(0))
		require.NotEmpty(t, gotArgs)
		require.NoFileExists(t, finalPath, "the JPEG directory is removed after a successful merge")
		require.FileExists(t, finalPath+"_timelapse.mp4")
	})

	t.Run("merge close error", func(t *testing.T) {
		store := newHJStore(t)
		store.closeErr = errors.New("rename failed")
		rec := NewTimelapseRecorder(TimelapseRecorderConfig{CameraID: "tl-close-err"}, store)
		rec.runCtx = context.Background()
		tempPath, finalPath, err := store.CreateSegment("tl-close-err", "timelapse")
		require.NoError(t, err)
		rec.segMu.Lock()
		rec.curTempPath = tempPath
		rec.curFinalPath = finalPath
		rec.frameCount = 1
		rec.triggerMerge()
		rec.segMu.Unlock()
		rec.mergeWg.Wait()
	})

	t.Run("ffmpeg failure and missing output", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			run  ffRunnerFunc
		}{
			{"ffmpeg error", func(context.Context, string, []string, string) error {
				return errors.New("ffmpeg not found")
			}},
			{"output missing", func(context.Context, string, []string, string) error { return nil }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := newHJStore(t)
				db := &hjDB{}
				rec := NewTimelapseRecorder(TimelapseRecorderConfig{
					CameraID: "tl-ff",
					DB:       db,
				}, store)
				rec.runCtx = context.Background()
				rec.runFF = tc.run
				tempPath, finalPath, err := store.CreateSegment("tl-ff", "timelapse")
				require.NoError(t, err)
				rec.segMu.Lock()
				rec.curTempPath = tempPath
				rec.curFinalPath = finalPath
				rec.segStart = time.Now().Add(-time.Minute)
				rec.frameCount = 1
				rec.triggerMerge()
				rec.segMu.Unlock()
				rec.mergeWg.Wait()
				calls, _ := db.snapshot()
				require.Zero(t, calls)
			})
		}
	})

	t.Run("db insert failure", func(t *testing.T) {
		store := newHJStore(t)
		db := &hjDB{err: errors.New("db down")}
		rec := NewTimelapseRecorder(TimelapseRecorderConfig{
			CameraID: "tl-db-err",
			DB:       db,
			Metrics:  metrics.NewMetrics(),
		}, store)
		rec.runCtx = context.Background()
		rec.runFF = func(_ context.Context, _ string, _ []string, outputPath string) error {
			return os.WriteFile(outputPath, []byte("mp4"), 0o644)
		}
		tempPath, finalPath, err := store.CreateSegment("tl-db-err", "timelapse")
		require.NoError(t, err)
		rec.segMu.Lock()
		rec.curTempPath = tempPath
		rec.curFinalPath = finalPath
		rec.segStart = time.Now().Add(-time.Minute)
		rec.frameCount = 1
		rec.triggerMerge()
		rec.segMu.Unlock()
		rec.mergeWg.Wait()
		calls, recs := db.snapshot()
		require.Equal(t, 1, calls)
		require.Empty(t, recs)
	})
}
