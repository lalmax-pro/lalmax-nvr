package recorder

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func newSchedulerTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "sched.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}

func TestRecordingPlanner_ShouldRecord(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()

	planner := NewRecordingPlanner(db)

	// Before the first refresh nothing is known, so callers fall back.
	_, known := planner.ShouldRecord("s1")
	require.False(t, known)

	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "on", Mode: storage.RecordingModeContinuous, Enabled: true,
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "off", Mode: storage.RecordingModeOff, Enabled: true,
	}))
	require.NoError(t, planner.Refresh(ctx))

	record, known := planner.ShouldRecord("on")
	require.True(t, known)
	require.True(t, record)

	record, known = planner.ShouldRecord("off")
	require.True(t, known)
	require.False(t, record)

	// A stream with no plan is known-and-false: no plan means no recording.
	record, known = planner.ShouldRecord("unplanned")
	require.True(t, known)
	require.False(t, record)
}

func TestScheduler_ReconcilesAliveStreams(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "live-1", Mode: storage.RecordingModeContinuous, Enabled: true,
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "live-off", Mode: storage.RecordingModeOff, Enabled: true,
	}))

	planner := NewRecordingPlanner(db)
	require.NoError(t, planner.Refresh(ctx))

	engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
		"live-1": {StreamID: "live-1", VideoCodec: "h264", Active: true},
	}}
	tasks := NewTaskManager(engine, nil, nil, nil, nil, time.Second)
	t.Cleanup(tasks.StopAll)

	s := NewRecordingScheduler(db)
	s.SetPlanner(planner)
	s.SetTasks(tasks)
	s.SetAliveStreams(func(context.Context) ([]string, error) {
		return []string{"live-1", "live-off", "unplanned"}, nil
	})

	s.check(ctx)
	require.True(t, tasks.Running("live-1"))
	require.False(t, tasks.Running("live-off"))
	require.False(t, tasks.Running("unplanned"))

	// A stream leaving lalmax stops its task even when the plan is still on.
	s.SetAliveStreams(func(context.Context) ([]string, error) {
		return nil, nil
	})
	s.check(ctx)
	require.False(t, tasks.Running("live-1"))
}

func TestScheduler_EventWindowKeepsTask(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "motion", Mode: storage.RecordingModeEvent, Enabled: true,
	}))
	planner := NewRecordingPlanner(db)
	require.NoError(t, planner.Refresh(ctx))

	engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
		"motion": {StreamID: "motion", VideoCodec: "h264", Active: true},
	}}
	tasks := NewTaskManager(engine, nil, nil, nil, nil, time.Second)
	t.Cleanup(tasks.StopAll)

	s := NewRecordingScheduler(db)
	s.SetPlanner(planner)
	s.SetTasks(tasks)
	s.SetAliveStreams(func(context.Context) ([]string, error) {
		return []string{"motion"}, nil
	})
	s.SetEventActive(func(id string) bool { return id == "motion" })

	s.check(ctx)
	require.True(t, tasks.Running("motion"))

	s.SetEventActive(func(string) bool { return false })
	s.check(ctx)
	require.False(t, tasks.Running("motion"))
}

func TestScheduler_PlanOffStopsTask(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "bare-push", Mode: storage.RecordingModeContinuous, Enabled: true,
	}))
	planner := NewRecordingPlanner(db)
	require.NoError(t, planner.Refresh(ctx))

	engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
		"bare-push": {StreamID: "bare-push", VideoCodec: "h264", Active: true},
	}}
	tasks := NewTaskManager(engine, nil, nil, nil, nil, time.Second)
	t.Cleanup(tasks.StopAll)

	s := NewRecordingScheduler(db)
	s.SetPlanner(planner)
	s.SetTasks(tasks)
	s.SetAliveStreams(func(context.Context) ([]string, error) {
		return []string{"bare-push"}, nil
	})

	s.check(ctx)
	require.True(t, tasks.Running("bare-push"))

	plan, err := db.GetRecordingPlanByStream(ctx, "bare-push")
	require.NoError(t, err)
	plan.Mode = storage.RecordingModeOff
	require.NoError(t, db.UpsertRecordingPlan(ctx, plan))

	s.check(ctx)
	require.False(t, tasks.Running("bare-push"))
}
