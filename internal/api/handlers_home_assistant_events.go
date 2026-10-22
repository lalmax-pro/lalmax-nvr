package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// homeAssistantEvent deliberately excludes event messages, arbitrary metadata,
// filesystem paths and upstream addresses from the integration feed.
type homeAssistantEvent struct {
	ID         int64     `json:"id"`
	InstanceID string    `json:"instance_id"`
	StreamID   string    `json:"stream_id"`
	Type       string    `json:"type"`
	Source     string    `json:"source"`
	StartedAt  time.Time `json:"started_at"`
	Labels     []string  `json:"labels"`
}

func (h *Handler) homeAssistantEvents(ctx context.Context, ev model.Event) ([]homeAssistantEvent, error) {
	cfg, err := h.db.GetHomeAssistantSettings(ctx)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	bindings, err := h.db.ListStreamBindings(ctx)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(bindings))
	for _, b := range bindings {
		owners[b.StreamID] = b.CameraID
	}
	kind := ""
	labels := []string{}
	switch {
	case ev.Source == model.EventSourceAI && ev.Type == "ai_detection":
		kind = "ai_detection"
		var metadata struct {
			Detections []struct {
				Label string `json:"label"`
			} `json:"detections"`
		}
		if json.Unmarshal([]byte(ev.Metadata), &metadata) == nil {
			for _, d := range metadata.Detections {
				label := strings.ToLower(strings.TrimSpace(d.Label))
				if label != "" && len(label) <= 64 && !slices.Contains(labels, label) {
					labels = append(labels, label)
				}
				if len(labels) == 32 {
					break
				}
			}
		}
	case ev.Source == model.EventSourceMQTT && ev.Type == "mqtt_trigger":
		kind = "mqtt_trigger"
		var metadata struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(ev.Metadata), &metadata) == nil && metadata.Action == "motion" {
			kind = "motion"
		}
	case ev.Source == model.EventSourceRecorder && ev.Type == "recorder_reconnected":
		kind = "recorder_reconnected"
	case ev.Source == model.EventSourceHealth:
		kind = "health"
	}
	if kind == "" {
		return nil, nil
	}
	var result []homeAssistantEvent
	for _, p := range cfg.Streams {
		owner := owners[p.StreamID]
		if owner == "" {
			owner = p.StreamID
		}
		if owner == ev.CameraID {
			result = append(result, homeAssistantEvent{ID: ev.ID, InstanceID: cfg.InstanceID, StreamID: p.StreamID, Type: kind, Source: ev.Source, StartedAt: ev.StartedAt, Labels: labels})
		}
	}
	return result, nil
}

func (h *Handler) handleHomeAssistantEvents(w http.ResponseWriter, r *http.Request) {
	if h.eventHub == nil {
		writeError(w, 503, "event stream unavailable")
		return
	}
	if _, err := h.db.GetHomeAssistantSettings(r.Context()); err != nil {
		writeError(w, 500, "failed to load publications")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}
	ch := h.eventHub.Subscribe()
	defer h.eventHub.Unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			events, err := h.homeAssistantEvents(r.Context(), ev)
			if err != nil {
				return
			}
			for _, event := range events {
				data, _ := json.Marshal(event)
				if _, err := fmt.Fprintf(w, "event: nvr\ndata: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
