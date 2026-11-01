package event

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

// s2EventRepo records inserted events and can be forced to fail. The archiver
// inserts from its own goroutine, so the recorded slice is mutex protected.
type s2EventRepo struct {
	mu     sync.Mutex
	events []model.Event
	err    error
}

func (r *s2EventRepo) InsertEvent(_ context.Context, event model.Event) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return 0, r.err
	}
	r.events = append(r.events, event)
	return int64(len(r.events)), nil
}

// snapshot returns a copy of the inserted events.
func (r *s2EventRepo) snapshot() []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Event(nil), r.events...)
}

// count returns how many events were inserted.
func (r *s2EventRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func TestS2LiveHubFanOut(t *testing.T) {
	hub := NewLiveHub()
	ch := hub.Subscribe()
	other := hub.Subscribe()

	ev := model.Event{ID: 1, Type: "test"}
	hub.Publish(ev)
	select {
	case got := <-ch:
		require.Equal(t, ev.ID, got.ID)
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the event")
	}
	select {
	case got := <-other:
		require.Equal(t, ev.ID, got.ID)
	case <-time.After(time.Second):
		t.Fatal("second subscriber did not receive the event")
	}

	// Publishing to a full buffer drops events instead of blocking.
	for i := 0; i < cap(ch)+5; i++ {
		hub.Publish(model.Event{ID: int64(i + 2)})
	}
	require.True(t, len(ch) <= cap(ch))

	// Unsubscribe removes and closes the channel exactly once.
	fresh := NewLiveHub()
	doomed := fresh.Subscribe()
	fresh.Unsubscribe(doomed)
	_, open := <-doomed
	require.False(t, open)
	fresh.Unsubscribe(doomed) // second call is a no-op
	fresh.Unsubscribe(make(chan model.Event))

	// Publishing without subscribers is safe.
	NewLiveHub().Publish(model.Event{ID: 42})
}

func TestS2NewArchiverRequiresBusAndRepo(t *testing.T) {
	require.Nil(t, NewArchiver(nil, &s2EventRepo{}))
	require.Nil(t, NewArchiver(NewEventBus(4), nil))
}

func TestS2ArchiverStart(t *testing.T) {
	// A nil archiver must not panic.
	var nilArchiver *Archiver
	nilArchiver.Start(context.Background())

	// Cancelled context: subscribe, then exit immediately.
	bus := NewEventBus(8)
	repo := &s2EventRepo{}
	archiver := NewArchiver(bus, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	archiver.Start(ctx)

	// Events published on subscribed topics are archived before shutdown.
	bus = NewEventBus(8)
	repo = &s2EventRepo{}
	archiver = NewArchiver(bus, repo)
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		archiver.Start(ctx)
		close(done)
	}()
	reconnect := RecorderReconnected{
		CameraID:       "cam1",
		DisconnectedAt: "2026-01-01T00:00:00Z",
		ReconnectedAt:  "2026-01-01T00:00:30Z",
		Downtime:       "30s",
		RecordingID:    "rec1",
		RetryCount:     2,
		GapReason:      "network",
	}
	deadline := time.Now().Add(2 * time.Second)
	for repo.count() == 0 && time.Now().Before(deadline) {
		bus.Publish(context.Background(), TopicRecorderReconnected, reconnect)
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("archiver did not stop after cancellation")
	}
	archived := repo.snapshot()
	require.Len(t, archived, 1)
	require.Equal(t, "recorder_reconnected", archived[0].Type)
	require.Equal(t, "rec1", archived[0].RecordingID)
	require.Equal(t, "Recorder reconnected after 30s", archived[0].Message)
}

func TestS2ArchiverHandleDispatch(t *testing.T) {
	repo := &s2EventRepo{}
	archiver := &Archiver{db: repo}

	// Unsupported payloads are ignored.
	archiver.handle(context.Background(), Event{Topic: "unknown.topic", Data: struct{}{}})

	// Recorder reconnection.
	archiver.handle(context.Background(), Event{Topic: TopicRecorderReconnected, Data: RecorderReconnected{
		CameraID:      "cam1",
		ReconnectedAt: "2026-02-03T04:05:06Z",
		Downtime:      "5s",
	}})
	require.Len(t, repo.events, 1)
	require.Equal(t, model.EventSourceRecorder, repo.events[0].Source)
	require.Equal(t, 2026, repo.events[0].StartedAt.Year())

	// MQTT trigger with a zero timestamp falls back to now.
	archiver.handle(context.Background(), Event{Topic: TopicMQTTTrigger, Data: MQTTTrigger{CameraID: "cam2", Action: "record"}})
	require.Len(t, repo.events, 2)
	require.Equal(t, "MQTT action: record", repo.events[1].Message)
	require.False(t, repo.events[1].StartedAt.IsZero())
}

func TestS2ArchiverAIDetectionVariants(t *testing.T) {
	t.Run("blank labels default to object", func(t *testing.T) {
		repo := &s2EventRepo{}
		archiver := &Archiver{db: repo}
		archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: AIDetection{
			CameraID:   "cam1",
			Timestamp:  time.Now().UnixMilli(),
			Detections: []AIDetectionObject{{Label: "  "}, {Label: "person"}},
		}})
		require.Len(t, repo.events, 1)
		require.Equal(t, "AI detected: object x1, person x1", repo.events[0].Message)
	})

	t.Run("millisecond timestamp", func(t *testing.T) {
		repo := &s2EventRepo{}
		archiver := &Archiver{db: repo}
		millis := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).UnixMilli()
		archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: AIDetection{
			CameraID:   "cam1",
			Timestamp:  millis,
			Detections: []AIDetectionObject{{Label: "car"}},
		}})
		require.Len(t, repo.events, 1)
		require.Equal(t, millis, repo.events[0].StartedAt.UnixMilli())
	})

	t.Run("zero timestamp uses now", func(t *testing.T) {
		repo := &s2EventRepo{}
		archiver := &Archiver{db: repo}
		archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: AIDetection{
			CameraID:   "cam1",
			Detections: []AIDetectionObject{{Label: "car"}},
		}})
		require.Len(t, repo.events, 1)
		require.False(t, repo.events[0].StartedAt.IsZero())
	})

	t.Run("insert failure is not debounced", func(t *testing.T) {
		repo := &s2EventRepo{err: errors.New("insert exploded")}
		archiver := &Archiver{db: repo}
		ev := AIDetection{CameraID: "cam1", Detections: []AIDetectionObject{{Label: "car"}}}
		archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: ev})
		// A failed insert must not mark the camera/class set as recently archived.
		require.Empty(t, archiver.lastAI)
		repo.err = nil
		archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: ev})
		require.Len(t, repo.events, 1)
	})

	t.Run("stale debounce keys are pruned", func(t *testing.T) {
		repo := &s2EventRepo{}
		archiver := &Archiver{db: repo, lastAI: map[string]time.Time{
			"stale\x00car": time.Now().Add(-time.Hour),
		}}
		archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: AIDetection{
			CameraID:   "cam1",
			Detections: []AIDetectionObject{{Label: "car"}},
		}})
		require.Len(t, repo.events, 1)
		_, stillThere := archiver.lastAI["stale\x00car"]
		require.False(t, stillThere)
	})
}

func TestS2ArchiverMQTTAndRecorderErrors(t *testing.T) {
	repo := &s2EventRepo{err: errors.New("insert exploded")}
	archiver := &Archiver{db: repo}

	// Invalid payloads are ignored before touching the database.
	archiver.handle(context.Background(), Event{Topic: TopicMQTTTrigger, Data: MQTTTrigger{Action: "stop"}})
	archiver.handle(context.Background(), Event{Topic: TopicMQTTTrigger, Data: MQTTTrigger{CameraID: "cam1"}})

	// Insert failures are logged, not fatal.
	archiver.handle(context.Background(), Event{Topic: TopicMQTTTrigger, Data: MQTTTrigger{CameraID: "cam1", Action: "stop"}})
	archiver.handle(context.Background(), Event{Topic: TopicRecorderReconnected, Data: RecorderReconnected{CameraID: "cam1"}})
	archiver.handle(context.Background(), Event{Topic: TopicAIDetection, Data: AIDetection{
		CameraID:   "cam1",
		Detections: []AIDetectionObject{{Label: "car"}},
	}})
	require.Empty(t, repo.events)
}

func TestS2ParseEventTime(t *testing.T) {
	// Empty value falls back to now.
	require.False(t, parseEventTime("").IsZero())

	for _, value := range []string{
		"2026-01-02T03:04:05.123456789Z",
		"2026-01-02T03:04:05Z",
		"2026-01-02 03:04:05.123456789",
		"2026-01-02 03:04:05",
	} {
		got := parseEventTime(value)
		require.Equal(t, 2026, got.Year(), "value=%q", value)
		require.Equal(t, time.UTC, got.Location(), "value=%q", value)
	}

	require.False(t, parseEventTime("not-a-timestamp").IsZero())
}
