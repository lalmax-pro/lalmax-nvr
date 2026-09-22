package recorder

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestEventManagerTriggerAndEnd(t *testing.T) {
	var mu sync.Mutex
	var resumed, paused []string
	m := NewEventManager(20*time.Millisecond, time.Second,
		func(_ context.Context, id string) error {
			mu.Lock()
			resumed = append(resumed, id)
			mu.Unlock()
			return nil
		},
		func(_ context.Context, id string) error {
			mu.Lock()
			paused = append(paused, id)
			mu.Unlock()
			return nil
		},
		func(string) bool { return true },
	)
	m.Trigger("cam-1", "mqtt")
	if !m.IsActive("cam-1") {
		t.Fatal("expected active session")
	}
	m.End("cam-1", "mqtt")
	if m.IsActive("cam-1") {
		t.Fatal("expected session closed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resumed) != 1 || len(paused) != 1 {
		t.Fatalf("resume=%v pause=%v", resumed, paused)
	}
}

func TestSchedulerSkipsActiveEvent(t *testing.T) {
	db := newSchedulerTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "cam-event", Mode: storage.RecordingModeEvent, Enabled: true,
	}))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "cam-off", Mode: storage.RecordingModeOff, Enabled: true,
	}))
	planner := NewRecordingPlanner(db)
	require.NoError(t, planner.Refresh(ctx))

	engine := &stubTaskEngine{byID: map[string]*media.StreamInfo{
		"cam-event": {StreamID: "cam-event", VideoCodec: "h264", Active: true},
		"cam-off":   {StreamID: "cam-off", VideoCodec: "h264", Active: true},
	}}
	tasks := NewTaskManager(engine, nil, nil, nil, nil, time.Second)
	t.Cleanup(tasks.StopAll)
	require.NoError(t, tasks.Ensure(ctx, "cam-event"))
	require.NoError(t, tasks.Ensure(ctx, "cam-off"))

	s := NewRecordingScheduler(db)
	s.SetPlanner(planner)
	s.SetTasks(tasks)
	s.SetAliveStreams(func(context.Context) ([]string, error) {
		return []string{"cam-event", "cam-off"}, nil
	})
	s.SetEventActive(func(id string) bool { return id == "cam-event" })
	s.check(ctx)

	require.True(t, tasks.Running("cam-event"))
	require.False(t, tasks.Running("cam-off"))
}
