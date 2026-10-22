package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestHomeAssistantRecordingsScopeRangeAndRevocation(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	cfg, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "published"}}
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	path := filepath.Join(store.RootDir(), "recording.mp4")
	require.NoError(t, os.WriteFile(path, []byte("0123456789abcdef"), 0600))
	now := time.Now().UTC()
	for _, rec := range []model.Recording{
		{ID: "selected", CameraID: "same-owner", StreamID: "published", FilePath: path, StartedAt: now, EndedAt: now.Add(time.Minute), Format: model.FormatH264},
		{ID: "other", CameraID: "same-owner", StreamID: "hidden", FilePath: path, StartedAt: now, EndedAt: now.Add(time.Minute), Format: model.FormatH264},
	} {
		seedRecording(t, db, &rec)
	}
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	router := h.Routes()
	base := "/api/integrations/home-assistant/streams/published/recordings"
	rr := doRequest(t, router, "GET", base+"?limit=1", nil, "", "")
	require.Equal(t, 200, rr.Code)
	var page struct {
		Recordings []homeAssistantRecording `json:"recordings"`
		Total      int                      `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &page))
	require.Equal(t, 1, page.Total)
	require.Len(t, page.Recordings, 1)
	require.Equal(t, "selected", page.Recordings[0].ID)
	require.NotContains(t, rr.Body.String(), "file_path")
	require.NotContains(t, rr.Body.String(), "same-owner")
	// Exercise independent cache keys when the same owner records different streams.
	rows, err := db.ListRecordings(ctx, model.RecordingFilter{StreamID: "hidden", Limit: 1})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "other", rows[0].ID)
	require.Equal(t, 404, doRequest(t, router, "GET", base+"/other/file", nil, "", "").Code)
	request := httptest.NewRequest("GET", base+"/selected/file", nil)
	request.Header.Set("Range", "bytes=4-7")
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, request)
	require.Equal(t, http.StatusPartialContent, rr.Code)
	require.Equal(t, "4567", rr.Body.String())
	require.Equal(t, "bytes 4-7/16", rr.Header().Get("Content-Range"))
	rr = doRequest(t, router, "HEAD", base+"/selected/file", nil, "", "")
	require.Equal(t, 200, rr.Code)
	require.Empty(t, rr.Body.String())
	for _, query := range []string{"?limit=101", "?offset=-1", "?offset=abc"} {
		require.Equal(t, 400, doRequest(t, router, "GET", base+query, nil, "", "").Code)
	}
	cfg.Enabled = false
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	require.Equal(t, 404, doRequest(t, router, "GET", base+"/selected/file", nil, "", "").Code)
	require.Equal(t, 404, doRequest(t, router, "GET", base, nil, "", "").Code)
}
