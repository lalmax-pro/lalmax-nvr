package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestHomeAssistantEventScopeAndLiveRevocation(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "camera", "Camera", "rtsp_h264", "h264", "rtsp://private/source", "", "", false, "", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "published", "camera"))
	cfg, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "published"}}
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	server := httptest.NewServer(h.Routes())
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL + "/api/integrations/home-assistant/events")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	ev := model.Event{ID: 1, CameraID: "camera", Source: "ai", Type: "ai_detection", StartedAt: time.Now().UTC(), Message: "private password", SnapshotPath: "/private/snapshot", Metadata: `{"source_url":"rtsp://private/source","detections":[{"label":"Person"},{"label":"person"},{"label":"car"}]}`}
	h.eventHub.Publish(ev)
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "event: nvr\n", line)
	line, err = reader.ReadString('\n')
	require.NoError(t, err)
	var got homeAssistantEvent
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &got))
	require.Equal(t, "published", got.StreamID)
	require.Equal(t, cfg.InstanceID, got.InstanceID)
	require.Equal(t, []string{"person", "car"}, got.Labels)
	require.NotContains(t, line, "private")
	// A running SSE connection reevaluates scope for each event.
	cfg.Streams = nil
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	filtered, err := h.homeAssistantEvents(ctx, ev)
	require.NoError(t, err)
	require.Empty(t, filtered)
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "published"}}
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	ev.CameraID = "unpublished"
	filtered, err = h.homeAssistantEvents(ctx, ev)
	require.NoError(t, err)
	require.Empty(t, filtered)
	ev.CameraID, ev.Source, ev.Type, ev.Metadata = "camera", "mqtt", "mqtt_trigger", `{"action":"motion"}`
	filtered, err = h.homeAssistantEvents(ctx, ev)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, "motion", filtered[0].Type)
	cfg.Enabled = false
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	filtered, err = h.homeAssistantEvents(ctx, ev)
	require.NoError(t, err)
	require.Empty(t, filtered)
}
