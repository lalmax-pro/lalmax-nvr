package relay

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"
)

type relayPlayURLTestEngine struct {
	token string
}

func (e *relayPlayURLTestEngine) BuildPlayURL(_ context.Context, req media.PlayURLRequest) (*media.PlayURL, error) {
	e.token = req.Token
	return &media.PlayURL{URL: "rtmp://127.0.0.1:11935/live/issue66?token=" + req.Token, Protocol: "rtmp"}, nil
}

func TestRelaySourcePlayURLUsesConfiguredPlaybackToken(t *testing.T) {
	engine := &relayPlayURLTestEngine{}
	url, err := buildRelaySourcePlayURL(context.Background(), engine, "issue66", "playback-token")
	require.NoError(t, err)
	require.Equal(t, "playback-token", engine.token)
	require.Equal(t, "rtmp://127.0.0.1:11935/live/issue66?token=playback-token", url)
}

func TestRelayStatsCountMediaAndRetainFinalSnapshot(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Init(context.Background()))

	manager := NewManager(db, nil)
	t.Cleanup(manager.Stop)
	task := &Task{ID: "stats-task", StreamID: "stream-1", Status: TaskStatusError}
	at := &activeTask{task: task, lastStatsAt: time.Now().Add(-2 * time.Second)}
	manager.mu.Lock()
	manager.tasks[task.ID] = at
	manager.mu.Unlock()

	for i := 0; i < 30; i++ {
		trackRelayMedia(at, base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{0x17, 0x01, 0, 0, 1}})
	}
	for i := 0; i < 100; i++ {
		trackRelayMedia(at, base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio}, Payload: []byte{0xaf, 0x01, 1, 2}})
	}
	manager.collectTaskStats(context.Background(), task, at)

	stats, err := manager.GetTaskStats(context.Background(), task.ID)
	require.NoError(t, err)
	require.InDelta(t, 15, stats.VideoFPS, 2)
	require.InDelta(t, 50, stats.AudioFPS, 4)
	require.Greater(t, stats.VideoPayloadBitrate, 0)
	require.Greater(t, stats.AudioPayloadBitrate, 0)
	require.Equal(t, "output_wire;input_media_payload_components", stats.BitrateScope)
	require.True(t, at.hasStats, "final snapshot must remain available after a non-running task state")
}

func TestManagerPersistsTasksThroughRepository(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "relay.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Init(context.Background()))

	manager := NewManager(db, nil)
	t.Cleanup(manager.Stop)

	startedAt := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	task := &Task{
		ID:        "task-1",
		StreamID:  "stream-1",
		TranscodeJobID: "job-1",
		TargetURL: "rtmp://example.test/live",
		Status:    TaskStatusRunning,
		CreatedAt: startedAt.Add(-time.Minute),
		StartedAt: &startedAt,
	}
	require.NoError(t, manager.saveTask(task))

	got, err := manager.loadTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, task, got)

	task.Status = TaskStatusStopped
	task.StoppedAt = &startedAt
	require.NoError(t, manager.saveTask(task))
	got, err = manager.loadTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, task, got)

	all, err := manager.loadAllTasks()
	require.NoError(t, err)
	require.Equal(t, []*Task{task}, all)

	require.NoError(t, manager.deleteTask(task.ID))
	_, err = manager.loadTask(task.ID)
	require.Error(t, err)
}

func TestDerivedRelayRequiresTrustedLogicalResolver(t *testing.T) {
    db, err := storage.New(filepath.Join(t.TempDir(), "relay-derived.db"))
    require.NoError(t,err)
    t.Cleanup(func(){_ = db.Close()})
    require.NoError(t,db.Init(context.Background()))
    manager:=NewManager(db,nil)
    t.Cleanup(manager.Stop)
    if _,err:=manager.CreateDerivedTask(context.Background(),"cam","job","rtmp://example.test/live");err==nil {
        t.Fatal("derived relay accepted without an authorized resolver")
    }
    calls:=0
    manager.SetDerivedResolver(func(_ context.Context, source, job string)(string,string,error){
        calls++
        if source!="cam" || job!="job" {t.Fatalf("unexpected source/job %q/%q",source,job)}
        return "private-output","rtmp://127.0.0.1:11935/live/private-output?token=only-runtime",nil
    }, func(id string)bool{return id=="private-output"})
    if _,err:=manager.CreateTask(context.Background(),"private-output","rtmp://example.test/live");err==nil {
        t.Fatal("internal output allowed through legacy relay task API")
    }
    task,err:=manager.CreateDerivedTask(context.Background(),"cam","job","rtmp://example.test/live")
    require.NoError(t,err)
    if calls!=1 {t.Fatalf("expected one source authorization, got %d",calls)}
    saved,err:=manager.loadTask(task.ID)
    require.NoError(t,err)
    if saved.StreamID!="cam" || saved.TranscodeJobID!="job" {
        t.Fatalf("logical provenance not persisted: %+v",saved)
    }
    if saved.StreamID=="private-output" {t.Fatal("persisted temporary physical stream ID")}
}
