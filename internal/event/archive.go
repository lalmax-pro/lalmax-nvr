package event

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// Archiver persists runtime EventBus messages into the unified product event table.
type Archiver struct {
	bus *EventBus
	db  storage.EventRepository
}

func NewArchiver(bus *EventBus, db storage.EventRepository) *Archiver {
	if bus == nil || db == nil {
		return nil
	}
	return &Archiver{bus: bus, db: db}
}

func (a *Archiver) Start(ctx context.Context) {
	if a == nil {
		return
	}
	ch := make(chan Event, 64)
	if err := a.bus.Subscribe(TopicRecorderReconnected, ch, 64); err != nil {
		slog.Warn("event archiver subscribe failed", "topic", TopicRecorderReconnected, "error", err)
		return
	}
	defer a.bus.Unsubscribe(TopicRecorderReconnected, ch)

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-ch:
			a.handle(ctx, evt)
		}
	}
}

func (a *Archiver) handle(ctx context.Context, evt Event) {
	switch data := evt.Data.(type) {
	case RecorderReconnected:
		a.handleRecorderReconnected(ctx, data)
	default:
		slog.Debug("event archiver ignored unsupported event", "topic", evt.Topic)
	}
}

func (a *Archiver) handleRecorderReconnected(ctx context.Context, ev RecorderReconnected) {
	startedAt := parseEventTime(ev.ReconnectedAt)
	metadata, _ := json.Marshal(map[string]any{
		"disconnected_at": ev.DisconnectedAt,
		"reconnected_at":  ev.ReconnectedAt,
		"downtime":        ev.Downtime,
		"retry_count":     ev.RetryCount,
		"gap_reason":      ev.GapReason,
	})

	_, err := a.db.InsertEvent(ctx, model.Event{
		CameraID:    ev.CameraID,
		Source:      model.EventSourceRecorder,
		Type:        "recorder_reconnected",
		Severity:    model.EventSeverityWarning,
		Status:      model.EventStatusOpen,
		Message:     "Recorder reconnected after " + ev.Downtime,
		Metadata:    string(metadata),
		RecordingID: ev.RecordingID,
		StartedAt:   startedAt,
		CreatedAt:   startedAt,
	})
	if err != nil {
		slog.Warn("failed to archive recorder reconnected event", "camera_id", ev.CameraID, "error", err)
	}
}

func parseEventTime(value string) time.Time {
	if value == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}
