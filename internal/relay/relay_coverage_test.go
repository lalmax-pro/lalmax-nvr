package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/rtmp"
	"github.com/stretchr/testify/require"
)

type memoryRelayRepo struct {
	mu      sync.Mutex
	tasks   map[string]storage.RelayTaskRecord
	saveErr error
	listErr error
}

func newMemoryRelayRepo() *memoryRelayRepo {
	return &memoryRelayRepo{tasks: map[string]storage.RelayTaskRecord{}}
}

func (r *memoryRelayRepo) SaveRelayTask(_ context.Context, task storage.RelayTaskRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	r.tasks[task.ID] = task
	return nil
}

func (r *memoryRelayRepo) GetRelayTask(_ context.Context, taskID string) (*storage.RelayTaskRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	task, ok := r.tasks[taskID]
	if !ok {
		return nil, errors.New("missing")
	}
	copied := task
	return &copied, nil
}

func (r *memoryRelayRepo) ListRelayTasks(context.Context) ([]storage.RelayTaskRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]storage.RelayTaskRecord, 0, len(r.tasks))
	for _, task := range r.tasks {
		out = append(out, task)
	}
	return out, nil
}

func (r *memoryRelayRepo) DeleteRelayTask(_ context.Context, taskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, taskID)
	return nil
}

type relayEngine struct {
	media.Engine
	mu      sync.Mutex
	streams map[string]media.StreamInfo
	listErr error
	getErr  error
	playErr error
	playURL string
}

func (e *relayEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listErr != nil {
		return nil, e.listErr
	}
	out := make([]media.StreamInfo, 0, len(e.streams))
	for _, stream := range e.streams {
		out = append(out, stream)
	}
	return out, nil
}

func (e *relayEngine) GetStream(_ context.Context, streamID string) (*media.StreamInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.getErr != nil {
		return nil, e.getErr
	}
	stream, ok := e.streams[streamID]
	if !ok {
		return nil, errors.New("missing stream")
	}
	copied := stream
	return &copied, nil
}

func (e *relayEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.playErr != nil {
		return nil, e.playErr
	}
	return &media.PlayURL{URL: e.playURL, Protocol: req.Protocol}, nil
}

type acceptAllRTMP struct{}

func (acceptAllRTMP) OnRtmpConnect(*rtmp.ServerSession, rtmp.ObjectPairArray) {}
func (acceptAllRTMP) OnNewRtmpPubSession(*rtmp.ServerSession) error           { return nil }
func (acceptAllRTMP) OnDelRtmpPubSession(*rtmp.ServerSession)                 {}
func (acceptAllRTMP) OnNewRtmpSubSession(*rtmp.ServerSession) error           { return nil }
func (acceptAllRTMP) OnDelRtmpSubSession(*rtmp.ServerSession)                 {}

func startLocalRTMP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	server := rtmp.NewServer(addr, acceptAllRTMP{})
	require.NoError(t, server.Listen())
	go func() { _ = server.RunLoop() }()
	t.Cleanup(server.Dispose)
	return addr
}

func stopCollector(m *Manager) {
	m.Stop()
}

func putActive(m *Manager, id string, task *activeTask) {
	m.mu.Lock()
	m.tasks[id] = task
	m.mu.Unlock()
}

func TestRelayTaskLifecycle(t *testing.T) {
	repo := newMemoryRelayRepo()
	engine := &relayEngine{streams: map[string]media.StreamInfo{
		"cam": {StreamID: "cam", Active: true, InFPS: 25},
	}}
	manager := NewManager(repo, engine)
	stopCollector(manager)
	t.Cleanup(func() {
		manager.mu.RLock()
		ids := make([]string, 0, len(manager.tasks))
		for id := range manager.tasks {
			ids = append(ids, id)
		}
		manager.mu.RUnlock()
		for _, id := range ids {
			_ = manager.StopTask(id)
		}
	})
	ctx := context.Background()

	_, err := manager.CreateTask(ctx, "missing", "rtmp://example/live")
	require.ErrorContains(t, err, "not found")
	engine.listErr = errors.New("list")
	_, err = manager.CreateTask(ctx, "cam", "rtmp://example/live")
	require.ErrorContains(t, err, "list streams")
	engine.listErr = nil
	repo.saveErr = errors.New("save")
	_, err = manager.CreateTask(ctx, "cam", "rtmp://example/live")
	require.ErrorContains(t, err, "save task")
	repo.saveErr = nil

	created, err := manager.CreateTask(ctx, "cam", "rtmp://127.0.0.1:1/live/out")
	require.NoError(t, err)
	require.Contains(t, created.ID, "relay_")

	got, err := manager.GetTask(created.ID)
	require.NoError(t, err)
	require.Equal(t, "cam", got.StreamID)
	putActive(manager, created.ID, &activeTask{task: created})
	cached, err := manager.GetTask(created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, cached.ID)

	repo.mu.Lock()
	repo.tasks["db-only"] = storage.RelayTaskRecord{
		ID: "db-only", StreamID: "cam", TargetURL: "rtmp://db",
		Status: string(TaskStatusStopped), CreatedAt: time.Now(),
	}
	repo.mu.Unlock()
	listed, err := manager.ListTasks()
	require.NoError(t, err)
	require.Len(t, listed, 2)
	manager.mu.Lock()
	delete(manager.tasks, created.ID)
	manager.mu.Unlock()
	repo.listErr = errors.New("list tasks")
	_, err = manager.ListTasks()
	require.Error(t, err)
	repo.listErr = nil
	_, err = manager.GetTask("absent")
	require.Error(t, err)

	require.Error(t, manager.StartTask(ctx, "absent"))
	engine.getErr = errors.New("stream down")
	require.ErrorContains(t, manager.StartTask(ctx, created.ID), "get stream")
	engine.getErr = nil
	engine.streams["cam"] = media.StreamInfo{StreamID: "cam", Active: false}
	require.ErrorContains(t, manager.StartTask(ctx, created.ID), "not active")
	engine.streams["cam"] = media.StreamInfo{StreamID: "cam", Active: true, InFPS: 25}
	engine.playErr = errors.New("no url")
	require.ErrorContains(t, manager.StartTask(ctx, created.ID), "build play URL")
	engine.playErr = nil

	stopping := make(chan struct{})
	putActive(manager, created.ID, &activeTask{task: &Task{ID: created.ID, Status: TaskStatusStopped}, done: stopping})
	require.ErrorContains(t, manager.StartTask(ctx, created.ID), "still stopping")
	close(stopping)
	manager.mu.Lock()
	delete(manager.tasks, created.ID)
	manager.mu.Unlock()

	putActive(manager, "busy", &activeTask{task: &Task{ID: "busy", Status: TaskStatusRunning}})
	require.ErrorContains(t, manager.StartTask(ctx, "busy"), "already running")

	engine.playURL = "rtmp://127.0.0.1:1/live/src"
	require.NoError(t, manager.StartTask(ctx, created.ID))
	waitTask(t, manager, created.ID)
	failed, err := manager.loadTask(created.ID)
	require.NoError(t, err)
	require.Equal(t, TaskStatusError, failed.Status)
	require.NotEmpty(t, failed.ErrorMsg)
	require.NoError(t, manager.DeleteTask(created.ID))
	_, err = manager.GetTask(created.ID)
	require.Error(t, err)

	require.ErrorContains(t, manager.StopTask("absent"), "not found")
	putActive(manager, "idle", &activeTask{task: &Task{ID: "idle", Status: TaskStatusStopped}})
	require.ErrorContains(t, manager.StopTask("idle"), "not running")
}

func TestRelayPushPullAndCancel(t *testing.T) {
	addr := startLocalRTMP(t)
	repo := newMemoryRelayRepo()
	engine := &relayEngine{streams: map[string]media.StreamInfo{
		"cam": {StreamID: "cam", Active: true, InFPS: 12},
	}, playURL: "rtmp://127.0.0.1:1/live/src"}
	manager := NewManager(repo, engine)
	stopCollector(manager)
	t.Cleanup(func() {
		manager.mu.RLock()
		ids := make([]string, 0, len(manager.tasks))
		for id := range manager.tasks {
			ids = append(ids, id)
		}
		manager.mu.RUnlock()
		for _, id := range ids {
			_ = manager.StopTask(id)
		}
	})
	ctx := context.Background()

	hang, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() {
		for {
			conn, err := hang.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	t.Cleanup(func() { _ = hang.Close() })
	cancelled, err := manager.CreateTask(ctx, "cam", "rtmp://"+hang.Addr().String()+"/live/hang")
	require.NoError(t, err)
	require.NoError(t, manager.StartTask(ctx, cancelled.ID))
	require.NoError(t, manager.StopTask(cancelled.ID))
	cancelledTask, err := manager.loadTask(cancelled.ID)
	require.NoError(t, err)
	require.Equal(t, TaskStatusStopped, cancelledTask.Status)

	pullFail, err := manager.CreateTask(ctx, "cam", "rtmp://"+addr+"/live/dst")
	require.NoError(t, err)
	require.NoError(t, manager.StartTask(ctx, pullFail.ID))
	waitTask(t, manager, pullFail.ID)
	failed, err := manager.loadTask(pullFail.ID)
	require.NoError(t, err)
	require.Equal(t, TaskStatusError, failed.Status)
	require.NoError(t, manager.DeleteTask(pullFail.ID))

	engine.playURL = "rtmp://" + addr + "/live/src"
	running, err := manager.CreateTask(ctx, "cam", "rtmp://"+addr+"/live/out")
	require.NoError(t, err)
	require.NoError(t, manager.StartTask(ctx, running.ID))
	require.Eventually(t, func() bool {
		task, err := manager.loadTask(running.ID)
		return err == nil && task.Status == TaskStatusRunning && task.StartedAt != nil
	}, 8*time.Second, 20*time.Millisecond)
	manager.mu.RLock()
	pullSession := manager.tasks[running.ID].pullSession
	manager.mu.RUnlock()
	_ = pullSession.Dispose()
	waitTask(t, manager, running.ID)
	ended, err := manager.loadTask(running.ID)
	require.NoError(t, err)
	require.NotEqual(t, TaskStatusRunning, ended.Status)

	pushed, err := manager.CreateTask(ctx, "cam", "rtmp://"+addr+"/live/push-end")
	require.NoError(t, err)
	require.NoError(t, manager.StartTask(ctx, pushed.ID))
	require.Eventually(t, func() bool {
		task, err := manager.loadTask(pushed.ID)
		return err == nil && task.Status == TaskStatusRunning
	}, 8*time.Second, 20*time.Millisecond)
	manager.mu.RLock()
	pushSession := manager.tasks[pushed.ID].session
	manager.mu.RUnlock()
	_ = pushSession.Dispose()
	waitTask(t, manager, pushed.ID)
	pushEnded, err := manager.loadTask(pushed.ID)
	require.NoError(t, err)
	require.NotEqual(t, TaskStatusRunning, pushEnded.Status)
}

func TestRelayStatsHelpers(t *testing.T) {
	repo := newMemoryRelayRepo()
	engine := &relayEngine{streams: map[string]media.StreamInfo{
		"cam": {StreamID: "cam", Active: true, InFPS: 25},
	}}
	manager := NewManager(repo, engine)
	stopCollector(manager)
	ctx := context.Background()

	started := time.Now().Add(-2 * time.Second)
	stoppedAt := time.Now()
	stopped := &Task{
		ID: "stopped", StreamID: "cam", TargetURL: "rtmp://example/live",
		Status: TaskStatusStopped, CreatedAt: started, StartedAt: &started, StoppedAt: &stoppedAt,
	}
	require.NoError(t, manager.saveTask(stopped))
	putActive(manager, stopped.ID, &activeTask{task: stopped})
	stats, err := manager.GetTaskStats(ctx, stopped.ID)
	require.NoError(t, err)
	require.Positive(t, stats.Duration)

	session := rtmp.NewPushSession(func(option *rtmp.PushSessionOption) { option.PushTimeoutMs = 100 })
	t.Cleanup(func() { _ = session.Dispose() })
	live := &Task{ID: "live", StreamID: "cam", TargetURL: "rtmp://example/live", Status: TaskStatusRunning, StartedAt: &started}
	require.NoError(t, manager.saveTask(live))
	putActive(manager, live.ID, &activeTask{task: live, session: session})
	liveStats, err := manager.GetTaskStats(ctx, live.ID)
	require.NoError(t, err)
	require.Equal(t, 25.0, liveStats.VideoFPS)
	require.Positive(t, liveStats.Duration)

	engine.getErr = errors.New("down")
	dbOnly := &Task{ID: "db-live", StreamID: "cam", Status: TaskStatusRunning, StartedAt: &started}
	require.NoError(t, manager.saveTask(dbOnly))
	noSession, err := manager.GetTaskStats(ctx, dbOnly.ID)
	require.NoError(t, err)
	require.Zero(t, noSession.VideoFPS)
	engine.getErr = nil

	_, err = manager.GetTaskStats(ctx, "absent")
	require.Error(t, err)
	manager.collectAllStats()
	manager.collectTaskStats(ctx, &Task{ID: "ghost", StreamID: "missing"}, &activeTask{})
	manager.collectTaskStats(ctx, live, &activeTask{task: live, session: session})

	history, err := manager.GetTaskStatsHistory(live.ID, time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, history.Samples)
	_, err = manager.GetTaskStatsHistory("absent", time.Minute)
	require.Error(t, err)
	empty, err := manager.GetTaskStatsHistory(stopped.ID, time.Minute)
	require.NoError(t, err)
	require.Empty(t, empty.Samples)
	require.NoError(t, manager.CleanupOldStats(time.Hour))

	manager.updateTaskStatus("missing", TaskStatusError, "x")
	manager.updateTaskStartedAt("missing", &started)
	manager.updateTaskStoppedAt("missing", &stoppedAt)
	manager.stopActiveTask(nil)
	cancelCalled := false
	manager.stopActiveTask(&activeTask{cancel: func() { cancelCalled = true }})
	require.True(t, cancelCalled)

	manager.evictIdleRings()
	manager.mu.Lock()
	manager.rings["old"] = &statsRing{lastSeen: time.Now().Add(-2 * time.Hour).Unix()}
	manager.rings["fresh"] = &statsRing{lastSeen: time.Now().Unix()}
	manager.mu.Unlock()
	manager.evictIdleRings()
	manager.mu.RLock()
	_, oldKept := manager.rings["old"]
	_, freshKept := manager.rings["fresh"]
	manager.mu.RUnlock()
	require.False(t, oldKept)
	require.True(t, freshKept)
}

func TestStatsRingParseTimeAndStop(t *testing.T) {
	ring := &statsRing{}
	require.Empty(t, ring.since(time.Time{}))
	now := time.Now()
	for i := 0; i < statsRingCap+2; i++ {
		ring.append(StatSample{Timestamp: now.Add(time.Duration(i) * time.Second), TotalBytes: int64(i)})
	}
	latest := now.Add(time.Duration(statsRingCap+1) * time.Second)
	require.Len(t, ring.since(latest), 1)
	require.NotEmpty(t, ring.since(now.Add(-time.Second)))

	require.Nil(t, timeToDB(nil))
	require.Nil(t, timeToDB(ptrTime(time.Time{})))
	stored := timeToDB(ptrTime(time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("CST", 8*3600))))
	require.Contains(t, *stored, "2026-01-01")

	parsed, err := parseTime("")
	require.NoError(t, err)
	require.True(t, parsed.IsZero())
	parsed, err = parseTime("2026-01-02 03:04:05.000000006")
	require.NoError(t, err)
	require.Equal(t, 6, parsed.Nanosecond())
	_, err = parseTime("2026-01-02 03:04:05")
	require.NoError(t, err)
	_, err = parseTime("2026-01-02T03:04:05Z")
	require.NoError(t, err)
	_, err = parseTime("2026-01-02T03:04:05.1Z")
	require.NoError(t, err)
	_, err = parseTime("not-a-time")
	require.Error(t, err)
	require.Contains(t, generateTaskID(), "relay_")

	manager := NewManager(newMemoryRelayRepo(), &relayEngine{})
	done := make(chan struct{})
	close(done)
	putActive(manager, "run", &activeTask{task: &Task{ID: "run", Status: TaskStatusRunning}, done: done})
	putActive(manager, "nodone", &activeTask{task: &Task{ID: "nodone", Status: TaskStatusRunning}})
	manager.Stop()
	manager.Stop()
}

func waitTask(t *testing.T, manager *Manager, taskID string) {
	t.Helper()
	manager.mu.RLock()
	done := manager.tasks[taskID].done
	manager.mu.RUnlock()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("task %s did not finish", taskID)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
