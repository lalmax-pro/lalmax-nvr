package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/webhook"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/stretchr/testify/require"
)

// TestAIEventPublisherSurvivesSettingsReload covers the runtime AI settings
// replacement path that previously dropped the event-bus subscription while
// the AI webhook and its own SSE endpoint continued to work.
func TestAIEventPublisherSurvivesSettingsReload(t *testing.T) {
	cfg := &config.Config{AI: config.AIConfig{Enabled: true, Backend: "webhook"}}
	h := &Handler{config: cfg, configPath: filepath.Join(t.TempDir(), "nvr.yaml")}
	bus := event.NewEventBus(16)
	events := make(chan event.Event, 16)
	require.NoError(t, bus.Subscribe(event.TopicAIDetection, events, cap(events)))
	defer bus.Unsubscribe(event.TopicAIDetection, events)

	h.SetAIDetectionPublisher(func(result webhook.DetectionResult) {
		bus.Publish(context.Background(), event.TopicAIDetection, event.AIDetection{
			CameraID: result.CameraID,
			PTS:      result.PTS,
			Timestamp: result.Timestamp,
			Detections: []event.AIDetectionObject{{Label: "car"}},
		})
	})
	first := ai.NewManager(cfg.AI)
	h.SetAIManager(first)
	defer func() { if h.aiManager != nil { h.aiManager.StopAll() } }()

	send := func(mgr *ai.Manager, pts int64) {
		t.Helper()
		body := fmt.Sprintf(`{"camera_id":"ha-test-main","pts":%d,"timestamp":123,"detections":[{"label":"car","confidence":0.9,"box":[0,0,1,1]}]}`, pts)
		w := httptest.NewRecorder()
		mgr.GetReceiver().HandleHTTP()(w, httptest.NewRequest(http.MethodPost, "/api/ai/webhook", bytes.NewBufferString(body)))
		require.Equal(t, http.StatusOK, w.Code)
	}
	assertPublished := func(pts int64) {
		t.Helper()
		select {
		case ev := <-events:
			require.Equal(t, event.TopicAIDetection, ev.Topic)
			result, ok := ev.Data.(event.AIDetection)
			require.True(t, ok)
			require.Equal(t, "ha-test-main", result.CameraID)
			require.Equal(t, pts, result.PTS)
		case <-time.After(3 * time.Second):
			t.Fatal("AI webhook did not publish to the shared event bus")
		}
		select {
		case duplicate := <-events:
			t.Fatalf("duplicate event after one detection: %+v", duplicate)
		case <-time.After(20 * time.Millisecond):
		}
	}

	send(h.aiManager, 1)
	assertPublished(1)

	for i := 0; i < 3; i++ {
		old := h.aiManager
		w := httptest.NewRecorder()
		h.handleUpdateAISettings(w, httptest.NewRequest(http.MethodPut, "/api/settings/ai", bytes.NewBufferString(`{"enabled":true,"backend":"webhook"}`)))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.NotSame(t, old, h.aiManager)
		require.False(t, old.UnregisterCallback("mgr-cb-1"), "old event-bus callback retained after replacement")
		// Reapplying the same manager must not install a duplicate publisher.
		h.SetAIManager(h.aiManager)
		send(h.aiManager, int64(i+2))
		assertPublished(int64(i+2))
	}
}
