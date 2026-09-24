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

func newPlanTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "plans.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}

// windowCoveringNow builds a weekly window that contains the current moment.
func windowCoveringNow(t *testing.T) ScheduleWindow {
	t.Helper()
	now := time.Now()
	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	if start.Weekday() != now.Weekday() || end.Weekday() != now.Weekday() {
		return ScheduleWindow{DayOfWeek: int(now.Weekday()), StartTime: "00:00", EndTime: "23:59"}
	}
	return ScheduleWindow{
		DayOfWeek: int(now.Weekday()),
		StartTime: start.Format("15:04"),
		EndTime:   end.Format("15:04"),
	}
}

func TestRecording_StreamIDRoundTrip(t *testing.T) {
	db := newPlanTestDB(t)
	ctx := context.Background()
	now := time.Now()

	// Plan-only stream: owner and stream are distinct.
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "r1", CameraID: "owner-1", StreamID: "obs-1",
		FilePath: "/x.mp4", Format: model.FormatH264, StartedAt: now,
	}))
	got, err := db.GetRecording(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, "obs-1", got.StreamID)
	require.Equal(t, "owner-1", got.CameraID)

	// Camera-backed recording: stream falls back to the camera ID.
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "r2", CameraID: "cam-2",
		FilePath: "/y.mp4", Format: model.FormatH264, StartedAt: now,
	}))
	got2, err := db.GetRecording(ctx, "r2")
	require.NoError(t, err)
	require.Equal(t, "cam-2", got2.StreamID)

	list, err := db.ListRecordings(ctx, model.RecordingFilter{})
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestRecordingPlan_CRUD(t *testing.T) {
	db := newPlanTestDB(t)
	ctx := context.Background()

	plan := &RecordingPlan{
		StreamID: "obs-1",
		Name:     "OBS 1",
		Mode:     RecordingModeScheduled,
		Enabled:  true,
		Windows:  []ScheduleWindow{{DayOfWeek: 1, StartTime: "09:00", EndTime: "17:00"}},
	}
	require.NoError(t, db.UpsertRecordingPlan(ctx, plan))
	require.NotEmpty(t, plan.ID)

	got, err := db.GetRecordingPlanByStream(ctx, "obs-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, plan.ID, got.ID)
	require.Equal(t, "OBS 1", got.Name)
	require.Equal(t, RecordingModeScheduled, got.Mode)
	require.True(t, got.Enabled)
	require.Len(t, got.Windows, 1)
	require.Equal(t, "09:00", got.Windows[0].StartTime)

	plans, err := db.ListRecordingPlans(ctx)
	require.NoError(t, err)
	require.Len(t, plans, 1)

	require.NoError(t, db.DeleteRecordingPlan(ctx, plan.ID))
	got, err = db.GetRecordingPlanByStream(ctx, "obs-1")
	require.NoError(t, err)
	require.Nil(t, got)

	require.ErrorIs(t, db.DeleteRecordingPlan(ctx, plan.ID), sql.ErrNoRows)
}

func TestRecordingPlan_OnePlanPerStream(t *testing.T) {
	db := newPlanTestDB(t)
	ctx := context.Background()

	first := &RecordingPlan{StreamID: "obs-1", Mode: RecordingModeContinuous, Enabled: true}
	require.NoError(t, db.UpsertRecordingPlan(ctx, first))

	// A different plan claiming the same stream replaces the first.
	second := &RecordingPlan{StreamID: "obs-1", Mode: RecordingModeOff, Enabled: false}
	require.NoError(t, db.UpsertRecordingPlan(ctx, second))

	plans, err := db.ListRecordingPlans(ctx)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.Equal(t, second.ID, plans[0].ID)
	require.Equal(t, RecordingModeOff, plans[0].Mode)

	// Upserting by ID updates in place and replaces windows.
	second.Mode = RecordingModeScheduled
	second.Windows = []ScheduleWindow{{DayOfWeek: 2, StartTime: "08:00", EndTime: "12:00"}}
	require.NoError(t, db.UpsertRecordingPlan(ctx, second))

	got, err := db.GetRecordingPlan(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, RecordingModeScheduled, got.Mode)
	require.Len(t, got.Windows, 1)
}

func TestDesiredRecordingStreams(t *testing.T) {
	db := newPlanTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{
		StreamID: "cont", Mode: RecordingModeContinuous, Enabled: true,
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{
		StreamID: "off", Mode: RecordingModeOff, Enabled: true,
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{
		StreamID: "disabled", Mode: RecordingModeContinuous, Enabled: false,
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{
		StreamID: "in-window", Mode: RecordingModeScheduled, Enabled: true,
		Windows: []ScheduleWindow{windowCoveringNow(t)},
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{
		StreamID: "out-window", Mode: RecordingModeScheduled, Enabled: true,
		Windows: []ScheduleWindow{{DayOfWeek: int(time.Now().Add(48 * time.Hour).Weekday()), StartTime: "00:00", EndTime: "00:01"}},
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &RecordingPlan{
		StreamID: "event", Mode: RecordingModeEvent, Enabled: true,
	}))

	desired, err := db.DesiredRecordingStreams(ctx)
	require.NoError(t, err)

	require.True(t, desired["cont"])
	require.False(t, desired["off"])
	require.False(t, desired["disabled"])
	require.True(t, desired["in-window"])
	require.False(t, desired["out-window"])
	require.False(t, desired["event"])
}

func TestScheduleWindowsActive_Overnight(t *testing.T) {
	// Monday 22:00 → Tuesday 06:00.
	win := []ScheduleWindow{{DayOfWeek: int(time.Monday), StartTime: "22:00", EndTime: "06:00"}}
	monday := time.Date(2026, 4, 6, 0, 0, 0, 0, time.Local) // a Monday
	require.Equal(t, time.Monday, monday.Weekday())

	require.False(t, ScheduleWindowsActive(win, monday.Add(21*time.Hour+59*time.Minute)))
	require.True(t, ScheduleWindowsActive(win, monday.Add(22*time.Hour)))
	require.True(t, ScheduleWindowsActive(win, monday.Add(23*time.Hour+30*time.Minute)))
	require.True(t, ScheduleWindowsActive(win, monday.Add(24*time.Hour)))  // Tuesday 00:00
	require.True(t, ScheduleWindowsActive(win, monday.Add(29*time.Hour)))  // Tuesday 05:00
	require.False(t, ScheduleWindowsActive(win, monday.Add(30*time.Hour))) // Tuesday 06:00
	require.False(t, ScheduleWindowsActive(win, monday.Add(12*time.Hour))) // Monday noon
}
