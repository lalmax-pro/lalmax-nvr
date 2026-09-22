package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func TestListServiceLogs_FiltersAndDefaults(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	ring := middleware.NewLogRing(16)
	h.logRing = ring
	ring.Append(middleware.LogEntry{Time: time.Now().UTC(), Level: "INFO", Message: "camera started", Attrs: map[string]any{"camera_id": "cam-1"}})
	ring.Append(middleware.LogEntry{Time: time.Now().UTC(), Level: "ERROR", Message: "failed to start recorder", Attrs: map[string]any{"camera_id": "cam-2"}})

	resp := doRequest(t, h.Routes(), "GET", "/api/service-logs?level=error", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, resp.Code)
	var body struct {
		Logs      []middleware.LogEntry `json:"logs"`
		Count     int                   `json:"count"`
		Capacity  int                   `json:"capacity"`
		Truncated bool                  `json:"truncated"`
	}
	parseJSON(t, resp, &body)
	require.Equal(t, 1, body.Count)
	require.Equal(t, "ERROR", body.Logs[0].Level)
	require.Equal(t, 16, body.Capacity)
	require.False(t, body.Truncated)

	resp = doRequest(t, h.Routes(), "GET", "/api/service-logs?q=cam-1", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, resp.Code)
	parseJSON(t, resp, &body)
	require.Equal(t, 1, body.Count)
	require.Equal(t, "camera started", body.Logs[0].Message)
}

func TestListServiceLogs_InvalidLimit(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	resp := doRequest(t, h.Routes(), "GET", "/api/service-logs?limit=abc", nil, "admin", "secret")
	require.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestListServiceLogs_ReadsLogFile(t *testing.T) {
	t.Helper()
	t.Parallel()

	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	path := filepath.Join(t.TempDir(), "lalmax-nvr.log")
	body := "time=2026-09-22T15:21:18.438+08:00 level=INFO msg=from-file\n" +
		"[GIN] 2026/09/22 - 15:20:48 | 200 | 1ms | 127.0.0.1 | GET \"/api/stat/all_group\"\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	h.SetServiceLogPath(path)

	resp := doRequest(t, h.Routes(), "GET", "/api/service-logs?level=info&limit=20", nil, "admin", "secret")
	require.Equal(t, http.StatusOK, resp.Code)
	var bodyJSON struct {
		Logs   []middleware.LogEntry `json:"logs"`
		Source string                `json:"source"`
		Path   string                `json:"path"`
	}
	parseJSON(t, resp, &bodyJSON)
	require.Equal(t, "file", bodyJSON.Source)
	require.Equal(t, path, bodyJSON.Path)
	require.Len(t, bodyJSON.Logs, 2)
	require.Equal(t, "from-file", bodyJSON.Logs[0].Message)
	require.Contains(t, bodyJSON.Logs[1].Message, "all_group")
}

func TestRingHandlerLevelMatchesSnapshot(t *testing.T) {
	t.Helper()
	t.Parallel()

	ring := middleware.NewLogRing(8)
	logger := slog.New(middleware.NewRingHandler(ring, slog.LevelInfo))
	logger.Info("hello", "component", "api")
	got := ring.Snapshot(middleware.LogRingFilter{Limit: 10})
	require.Len(t, got, 1)
	require.Equal(t, "hello", got[0].Message)
}
