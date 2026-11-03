package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// main3AuthedApp builds an HTTP-mode app protected by a legacy password login.
func main3AuthedApp(t *testing.T, mutate func(*config.Config)) *App {
	t.Helper()
	cfg, cfgPath := main3HTTPConfig(t)
	cfg.Auth.Password = "main3-password"
	if mutate != nil {
		mutate(cfg)
	}
	app, err := NewApp(cfg, cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, app.Stop()) })
	return app
}

// main3Request performs an authenticated request against the app router.
func main3Request(t *testing.T, app *App, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.SetBasicAuth("admin", "main3-password")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	app.httpServer.Handler.ServeHTTP(rec, req)
	return rec
}

// TestMain3RouterAuthenticatedHooks drives the API handler hooks installed by
// buildRouter: multi-user auth fallbacks, auto-discover apply, local trace
// toggle and the recording-plan reconciler.
func TestMain3RouterAuthenticatedHooks(t *testing.T) {
	app := main3AuthedApp(t, func(cfg *config.Config) {
		enabled := true
		cfg.HLS.Enabled = &enabled
		cfg.HLS.LowLatency = false
		cfg.Auth.PlaybackTokens = map[string]string{"cam1": "play-token"}
	})

	// Any authenticated API call exercises the multi-user auth fallback closures.
	rec := main3Request(t, app, http.MethodGet, "/api/cameras", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// The auto-discover hook must also tolerate a nil service.
	originalDiscover := app.autoDiscover
	app.autoDiscover = nil
	rec = main3Request(t, app, http.MethodPut, "/api/settings/auto-discover",
		[]byte(`{"enabled":false}`))
	require.Equal(t, http.StatusOK, rec.Code)
	app.autoDiscover = originalDiscover

	rec = main3Request(t, app, http.MethodPut, "/api/settings/auto-discover",
		[]byte(`{"enabled":false,"scan_interval":"10m"}`))
	require.Equal(t, http.StatusOK, rec.Code)

	// Local trace toggle runs the observability hook.
	rec = main3Request(t, app, http.MethodPut, "/api/settings",
		[]byte(`{"observability":{"local_trace_enabled":true}}`))
	require.Equal(t, http.StatusOK, rec.Code)

	// Creating a recording plan triggers the recording reconciler hook.
	rec = main3Request(t, app, http.MethodPost, "/api/recording-plans",
		[]byte(`{"stream_id":"plan-stream","name":"plan","mode":"continuous","enabled":true}`))
	require.Less(t, rec.Code, http.StatusInternalServerError)
}

// TestMain3RouterAIDetectionCallback covers the AI detection callback wiring by
// feeding a detection through the webhook endpoint.
func TestMain3RouterAIDetectionCallback(t *testing.T) {
	app := main3AuthedApp(t, func(cfg *config.Config) {
		cfg.AI.Enabled = true
	})

	events := make(chan event.Event, 4)
	require.NoError(t, app.eventBus.Subscribe(event.TopicAIDetection, events, 4))
	defer app.eventBus.Unsubscribe(event.TopicAIDetection, events)

	payload, err := json.Marshal(map[string]any{
		"camera_id": "cam1",
		"pts":       1,
		"timestamp": 2,
		"detections": []map[string]any{{
			"label":      "person",
			"confidence": 0.9,
			"box":        []float32{0, 0, 1, 1},
			"zone_id":    "zone-a",
			"track_id":   7,
		}},
	})
	require.NoError(t, err)
	rec := main3Request(t, app, http.MethodPost, "/api/ai/webhook", payload)
	require.Less(t, rec.Code, http.StatusInternalServerError)

	select {
	case got := <-events:
		detection, ok := got.Data.(event.AIDetection)
		require.True(t, ok)
		require.Equal(t, "cam1", detection.CameraID)
		require.Len(t, detection.Detections, 1)
		require.Equal(t, "person", detection.Detections[0].Label)
		require.Equal(t, "zone-a", detection.Detections[0].ZoneID)
	case <-time.After(10 * time.Second):
		t.Fatal("AI detection callback never published")
	}
}

// TestMain3RouterRestoresTranscodeOutputs covers the internal stream registry
// restore loop of buildRouter.
func TestMain3RouterRestoresTranscodeOutputs(t *testing.T) {
	app := main3AuthedApp(t, nil)
	require.NoError(t, app.db.RegisterTranscodeOutput(context.Background(), "out-1", "cam1"))

	handler := app.buildRouter()
	require.NotNil(t, handler)

	// The rebuilt router still answers authenticated requests.
	req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
	req.SetBasicAuth("admin", "main3-password")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestMain3RouterWithoutMediaRegistry covers the fail-closed router built when
// the internal output registry cannot be read.
func TestMain3RouterWithoutMediaRegistry(t *testing.T) {
	app := main3AuthedApp(t, nil)
	closed, err := storage.Open(context.Background(), storage.DatabaseOptions{
		Driver: storage.DriverSQLite,
		DSN:    filepath.Join(t.TempDir(), "closed.db"),
	})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	original := app.db
	app.db = closed
	handler := app.buildRouter()
	app.db = original

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cameras", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestMain3RouterRebuildsPullManagerAndWatcher covers the Emby pull-manager
// bootstrap branch and the config watcher wiring of buildRouter.
func TestMain3RouterRebuildsPullManagerAndWatcher(t *testing.T) {
	app := main3AuthedApp(t, nil)
	require.NotNil(t, app.iptvSvc)

	watcher, err := config.NewWatcher(app.cfg, app.configPath)
	require.NoError(t, err)
	app.watcher = watcher

	app.embySvc = nil
	app.hlsPuller = nil
	handler := app.buildRouter()
	require.NotNil(t, handler)
	require.NotNil(t, app.hlsPuller)
	require.NotNil(t, app.embySvc)
}
