package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// --- handleListRecordings with sorting ---

func TestListRecordings_SortByDuration(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "cam-1", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "cam-1", "h264", now.Add(-time.Hour), false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?sort_by=duration&order=asc", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Recordings, 2)
}

func TestListRecordings_Search(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	seedRecording(t, db, makeRecording("rec-1", "front-door", "h264", now, false))
	seedRecording(t, db, makeRecording("rec-2", "back-yard", "h264", now, false))

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?search=front", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp recordingsResponse
	parseJSON(t, rr, &resp)
	require.Len(t, resp.Recordings, 1)
	require.Equal(t, "rec-1", resp.Recordings[0].ID)
}

func TestListRecordings_IncludesArchivedDeviceRecordings(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("archived-rec", "archived-cam", "h264", now, false)
	seedRecording(t, db, rec)
	_, err := db.ArchiveAllRecordings(context.Background(), "archived-cam")
	require.NoError(t, err)

	activeRR := doRequest(t, h.Routes(), "GET", "/api/recordings?camera_id=archived-cam", nil, "", "")
	require.Equal(t, http.StatusOK, activeRR.Code)
	var active recordingsResponse
	parseJSON(t, activeRR, &active)
	require.Empty(t, active.Recordings)

	archiveRR := doRequest(t, h.Routes(), "GET", "/api/recordings?camera_id=archived-cam&archived=true", nil, "", "")
	require.Equal(t, http.StatusOK, archiveRR.Code)
	var archived recordingsResponse
	parseJSON(t, archiveRR, &archived)
	require.Len(t, archived.Recordings, 1)
	require.Equal(t, "archived-rec", archived.Recordings[0].ID)
}

func TestListRecordings_InvalidLimit(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?limit=-1", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code) // negative limit ignored
}

func TestListRecordings_InvalidOffset(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings?offset=-1", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code) // negative offset ignored
}

// --- handleGetRecording edge cases ---

func TestGetRecording_EdgeCases(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-test", "cam-1", "h264", now, false)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-test", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var got map[string]interface{}
	parseJSON(t, rr, &got)
	require.Equal(t, "rec-test", got["id"])
	require.Equal(t, "cam-1", got["camera_id"])
}

// --- handleDownloadRecording tests ---

func TestDownloadRecording_Missing(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent/download", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDownloadRecording_NoFilePath(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-nofile", "cam-1", "h264", now, false)
	rec.FilePath = ""
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-nofile/download", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDownloadRecording_FileNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-gone", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "nonexistent.mp4")
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-gone/download", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestDownloadRecording_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-dl", "cam-1", "h264", now, false)
	rec.FilePath = filepath.Join(store.RootDir(), "rec-dl.mp4")
	require.NoError(t, os.WriteFile(rec.FilePath, []byte("test-video-data"), 0644))
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-dl/download", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "test-video-data", rr.Body.String())
}

// --- handleListFrames tests ---

func TestListFrames_NoRecording(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/nonexistent/frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestListFrames_NotMJPEG(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-h264", "cam-1", "h264", now, false)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-h264/frames", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestListFrames_MJPEGDirWithImages(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(store.RootDir(), "mjpeg-frames")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "frame001.jpg"), []byte("jpg-data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "frame002.jpg"), []byte("jpg-data"), 0644))

	rec := makeRecording("rec-mjpeg", "cam-1", "mjpeg", now, false)
	rec.FilePath = dir
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-mjpeg/frames", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	parseJSON(t, rr, &resp)
	frames, ok := resp["frames"].([]interface{})
	require.True(t, ok)
	require.Len(t, frames, 2)
}

func TestListFrames_MJPEGNotDir(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	filePath := filepath.Join(store.RootDir(), "single.jpg")
	require.NoError(t, os.WriteFile(filePath, []byte("jpg-data"), 0644))

	rec := makeRecording("rec-single", "cam-1", "mjpeg", now, false)
	rec.FilePath = filePath
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/rec-single/frames", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code) // not a directory
}

// --- handleDeleteRecording with path traversal ---

func TestDeleteRecording_PathTraversal(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("rec-trav", "cam-1", "h264", now, false)
	rec.FilePath = "../../../etc/passwd"
	seedRecording(t, db, rec)

	// Delete should still work (deletes from DB), file deletion fails gracefully
	rr := doRequest(t, h.Routes(), "DELETE", "/api/recordings/rec-trav", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
}

// --- Batch delete with actual recordings ---

func TestBatchDeleteRecordings_Success(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	// Create files
	rec1 := makeRecording("batch-1", "cam-1", "h264", now, false)
	rec1.FilePath = filepath.Join(store.RootDir(), "batch-1.mp4")
	require.NoError(t, os.WriteFile(rec1.FilePath, []byte("data1"), 0644))
	seedRecording(t, db, rec1)

	rec2 := makeRecording("batch-2", "cam-1", "h264", now, false)
	rec2.FilePath = filepath.Join(store.RootDir(), "batch-2.mp4")
	require.NoError(t, os.WriteFile(rec2.FilePath, []byte("data2"), 0644))
	seedRecording(t, db, rec2)

	body, _ := json.Marshal(map[string][]string{"ids": {"batch-1", "batch-2"}})
	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/batch-delete", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Verify deleted from DB
	got, _ := db.GetRecording(context.Background(), "batch-1")
	require.Nil(t, got)
	got2, _ := db.GetRecording(context.Background(), "batch-2")
	require.Nil(t, got2)
}

func TestLockRecording_BlocksDelete(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	now := time.Now().UTC().Truncate(time.Second)
	rec := makeRecording("lock-1", "cam-1", "h264", now, false)
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/lock-1/lock", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, h.Routes(), "DELETE", "/api/recordings/lock-1", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	got, err := db.GetRecording(context.Background(), "lock-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Locked)
}

func TestListRecordingSources_IncludesPlanAndOrphanStreams(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	ctx := context.Background()

	require.NoError(t, db.UpsertCamera(ctx, "cam-front", "Front Door", "rtsp", "h264", "rtsp://127.0.0.1/front", "", "", true, "", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "front-stream", "cam-front"))
	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		ID:       "plan-obs",
		StreamID: "obs-1",
		Name:     "OBS",
		Mode:     storage.RecordingModeContinuous,
		Enabled:  true,
	}))
	now := time.Now().UTC().Truncate(time.Second)
	orphan := makeRecording("rec-orphan", "ghost-stream", "h264", now, false)
	orphan.StreamID = "ghost-stream"
	seedRecording(t, db, orphan)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/sources?include_archived=true", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Sources []recordingSource `json:"sources"`
	}
	parseJSON(t, rr, &resp)

	byID := map[string]recordingSource{}
	for _, src := range resp.Sources {
		byID[src.ID] = src
	}
	require.Contains(t, byID, "cam-front")
	require.Equal(t, "camera", byID["cam-front"].Kind)
	require.Equal(t, "front-stream", byID["cam-front"].StreamID)
	require.Equal(t, "Front Door", byID["cam-front"].Name)

	require.Contains(t, byID, "obs-1")
	require.Equal(t, "plan", byID["obs-1"].Kind)
	require.Equal(t, "OBS", byID["obs-1"].Name)

	require.Contains(t, byID, "ghost-stream")
	require.Equal(t, "stream", byID["ghost-stream"].Kind)
}
