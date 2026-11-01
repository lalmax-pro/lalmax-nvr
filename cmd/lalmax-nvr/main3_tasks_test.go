package main

import (
	"context"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// main3RecordingApp builds an app with a small camera fleet and health enabled.
func main3RecordingApp(t *testing.T) *App {
	t.Helper()
	cfg, cfgPath := main3HTTPConfig(t)
	cfg.Health.Enabled = true
	cfg.Health.EventsRetention = "24h"
	cfg.Cameras = []config.CameraConfig{
		{
			ID:       "adapt-cam",
			Name:     "Adaptive camera",
			Protocol: "rtsp",
			URL:      "rtsp://127.0.0.1:554/live",
			Encoding: string(model.FormatH264),
			Enabled:  true,
		},
		{
			ID:       "mjpeg-cam",
			Name:     "MJPEG camera",
			Protocol: "rtsp",
			URL:      "rtsp://127.0.0.1:554/mjpeg",
			Encoding: string(model.FormatMJPEG),
			Enabled:  true,
		},
		{
			ID:       "xiaomi-cam",
			Name:     "Xiaomi camera",
			Protocol: "xiaomi",
			URL:      "http://127.0.0.1:1/xiaomi",
			Enabled:  true,
		},
	}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, app.Stop()) })
	return app
}

// TestMain3RecordTaskHooks covers the record-task callbacks and the
// task-manager closure wiring installed by wireRecordTasks.
func TestMain3RecordTaskHooks(t *testing.T) {
	app := main3RecordingApp(t)
	require.NotNil(t, app.recTasks)

	// Unknown streams have no camera: the callbacks return early.
	app.onRecordTaskStart("no-such-stream", nil)
	app.onRecordTaskStop("no-such-stream", nil)

	ctx := context.Background()

	// Sub-streams are never recorded.
	require.NoError(t, app.recTasks.Ensure(ctx, "adapt-cam_sub"))

	// A stream without a camera falls back to a plan-only owner.
	require.Error(t, app.recTasks.Ensure(ctx, "unowned-stream"))

	// Cameras excluded by protocol or encoding are skipped outright.
	require.NoError(t, app.recTasks.Ensure(ctx, "xiaomi-cam"))
	require.NoError(t, app.recTasks.Ensure(ctx, "mjpeg-cam"))

	// An adaptive plan exercises the adaptive interval lookup for a camera.
	require.NoError(t, app.db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		ID:       storage.NewRecordingPlanID(),
		StreamID: "adapt-cam",
		Name:     "adaptive",
		Mode:     storage.RecordingModeAdaptive,
		Enabled:  true,
	}))
	require.NoError(t, app.recPlanner.Refresh(ctx))
	_ = app.recTasks.Ensure(ctx, "adapt-cam")
	require.NoError(t, app.recTasks.Stop(ctx, "adapt-cam", "test"))

	// A known continuous plan makes ShouldRecord answer true.
	require.NoError(t, app.db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		ID:       storage.NewRecordingPlanID(),
		StreamID: "plan-stream",
		Name:     "continuous",
		Mode:     storage.RecordingModeContinuous,
		Enabled:  true,
	}))
	require.NoError(t, app.recPlanner.Refresh(ctx))
	app.recTasks.OnStreamUp(ctx, "plan-stream")
	require.NoError(t, app.recTasks.Stop(ctx, "plan-stream", "test"))

	// Without a planner the should-record closure falls back to event state.
	planner := app.recPlanner
	app.recPlanner = nil
	app.recTasks.OnStreamUp(ctx, "event-stream")
	app.recPlanner = planner
}

// TestMain3EventActiveWithOpenWindow covers eventActive when an event window is
// open for the stream itself.
func TestMain3EventActiveWithOpenWindow(t *testing.T) {
	app := main3RecordingApp(t)
	require.NotNil(t, app.eventMgr)
	require.False(t, app.eventActive("adapt-cam"))

	// Mark the camera as event-mode so Trigger opens a window.
	app.camMgr.SetRecordingModeSource(func(streamID string) (string, bool) {
		return storage.RecordingModeEvent, true
	})
	app.eventMgr.SetControls(func(context.Context, string) error { return nil }, func(context.Context, string) error { return nil })
	app.eventMgr.Trigger("adapt-cam", "test")
	t.Cleanup(func() { app.eventMgr.End("adapt-cam", "cleanup") })
	require.True(t, app.eventActive("adapt-cam"))
}

// TestMain3WireRecordTasksFallsBackToDefaultSegmentDuration covers the invalid
// segment duration fallback.
func TestMain3WireRecordTasksFallsBackToDefaultSegmentDuration(t *testing.T) {
	app := main3RecordingApp(t)
	app.cfg.Storage.SegmentDuration = "not-a-duration"
	app.wireRecordTasks()
	require.NotNil(t, app.recTasks)
}

// TestMain3MergeManagerConfigurationCallbacks covers the merge-manager
// configuration closures by querying pending merge counts.
func TestMain3MergeManagerConfigurationCallbacks(t *testing.T) {
	app := main3RecordingApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"adapt-cam", "ghost-camera"} {
		require.NoError(t, app.db.InsertRecording(ctx, &model.Recording{
			ID:        "rec-" + id,
			CameraID:  id,
			FilePath:  "/tmp/" + id + ".mp4",
			Format:    model.FormatH264,
			StartedAt: now.Add(-2 * time.Hour),
			EndedAt:   now.Add(-time.Hour),
			Duration:  3600,
			FileSize:  1024,
			Merged:    false,
		}))
	}

	counts := app.mergeMgr.PendingCounts(ctx)
	require.NotNil(t, counts)
}

// TestMain3StartFailsVoIPWithJT808Running covers the VoIP startup failure path
// that also has to stop an already started JT808 server.
func TestMain3StartFailsVoIPWithJT808Running(t *testing.T) {
	cfg, cfgPath := main3HTTPConfig(t)
	cfg.JT808.Enabled = true
	cfg.JT808.Port = freeTCPPort(t)
	cfg.JT808.MediaIP = "127.0.0.1"
	cfg.JT808.AuthCode = "secret"
	cfg.VoIP.Enable = true
	cfg.VoIP.SipListenAddr = "not-an-address"

	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	err = app.Start()
	require.ErrorContains(t, err, "start VoIP signaling")
	require.NoError(t, app.Stop())
}

// TestMain3RecordTaskSkipAndAdaptiveBranches covers the remaining task-manager
// closure branches: adaptive plans for streams without a camera and streams
// whose media is already written by the group recorder.
func TestMain3RecordTaskSkipAndAdaptiveBranches(t *testing.T) {
	app := main3RecordingApp(t)
	ctx := context.Background()

	// An adaptive plan for a stream that has no camera resolves to a zero
	// interval instead of dereferencing a camera.
	require.NoError(t, app.db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		ID:       storage.NewRecordingPlanID(),
		StreamID: "unowned-adaptive",
		Name:     "adaptive",
		Mode:     storage.RecordingModeAdaptive,
		Enabled:  true,
	}))
	require.NoError(t, app.recPlanner.Refresh(ctx))
	require.Error(t, app.recTasks.Ensure(ctx, "unowned-adaptive"))

	// A group writer that already owns the camera stream makes the task manager
	// skip it entirely.
	groupWriter := recorder.NewGroupWriter(app.store, app.db, app.eventBus,
		app.mediaEngine, 30*time.Second, app.metrics)
	app.groupWriter = groupWriter
	t.Cleanup(func() {
		app.groupWriter = nil
		groupWriter.Close()
	})
	require.NoError(t, app.recTasks.Ensure(ctx, "adapt-cam"))
	require.False(t, app.recTasks.Running("adapt-cam"))
}
