package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestHomeAssistantSelectedVariantDoesNotServeOriginalSnapshotCache(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	cfg, err := db.GetHomeAssistantSettings(context.Background())
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "camera", AppName: "live", TranscodeJobID: "selected-output"}}
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.setCachedSnapshot("home-assistant:camera", []byte("old-original-image"))
	h.setCachedSnapshot("home-assistant:camera:", []byte("old-original-image"))
	request := httptest.NewRequest("GET", "/api/integrations/home-assistant/streams/camera/snapshot", nil)
	response := httptest.NewRecorder()
	h.Routes().ServeHTTP(response, request)
	require.Equal(t, 503, response.Code)
	require.NotContains(t, response.Body.String(), "old-original-image")
}

func TestHomeAssistantSelectedVariantOfflineDoesNotFallbackToOriginal(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	cfg, err := db.GetHomeAssistantSettings(context.Background())
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "camera", AppName: "live", TranscodeJobID: "selected-output"}}
	require.NoError(t, db.SaveHomeAssistantSettings(context.Background(), cfg))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&haTestEngine{&stubMediaEngine{}})
	response := doRequest(t, h.Routes(), "GET", "/api/integrations/home-assistant", nil, "", "")
	require.Equal(t, 200, response.Code)
	var catalog homeAssistantCatalog
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalog))
	require.Len(t, catalog.Streams, 1)
	require.Equal(t, "camera", catalog.Streams[0].StreamID)
	require.Empty(t, catalog.Streams[0].RTSPURL)
	require.False(t, catalog.Streams[0].Active)
	require.False(t, catalog.Streams[0].WebRTCSupported)
}
