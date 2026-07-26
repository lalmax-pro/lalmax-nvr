package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// --- Archive endpoints ---

// archiveGroupItem is the JSON response for a single archive group.
type archiveGroupItem struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	RecordingCount       int        `json:"recording_count"`
	TotalSize            int64      `json:"total_size"`
	ArchivedAt           *time.Time `json:"archived_at,omitempty"`
	ArchiveRetentionDays int        `json:"archive_retention_days"`
}

func (h *Handler) handleListArchives(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cameras, err := h.db.ListArchivedCameras(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list archived cameras")
		return
	}
	if cameras == nil {
		cameras = []storage.CameraRow{}
	}

	items := make([]archiveGroupItem, 0, len(cameras))
	for _, cam := range cameras {
		count, totalSize, err := h.db.GetArchiveGroupStats(ctx, cam.ID)
		if err != nil {
			logger.Warn("failed to get archive stats", "camera_id", cam.ID, "error", err)
			count, totalSize = 0, 0
		}
		items = append(items, archiveGroupItem{
			ID:                   cam.ID,
			Name:                 cam.Name,
			RecordingCount:       count,
			TotalSize:            totalSize,
			ArchivedAt:           cam.ArchivedAt,
			ArchiveRetentionDays: cam.ArchiveRetentionDays,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"archives": items})
}

func (h *Handler) handleRestoreArchiveGroup(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}

	cam, err := h.db.GetCamera(ctx, cameraID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil || !cam.Archived {
		writeError(w, http.StatusNotFound, "archived camera not found")
		return
	}

	if err := h.camMgr.RestoreArchivedCamera(ctx, cam); err != nil {
		if errors.As(err, new(*model.CameraAlreadyExistsError)) {
			writeAPIError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to restore camera")
		return
	}

	h.logSuccess(r, "archive.restore", "archive", cameraID, "archived camera restored", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (h *Handler) handleListArchiveRecordings(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	trueVal := true
	filter := model.RecordingFilter{
		CameraID: cameraID,
		Archived: &trueVal,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}
	filter.SortBy = r.URL.Query().Get("sort_by")
	filter.SortOrder = r.URL.Query().Get("order")

	recordings, err := h.db.ListRecordings(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list archived recordings")
		return
	}
	if recordings == nil {
		recordings = []model.Recording{}
	}
	total, err := h.db.CountRecordingsWithFilter(ctx, filter)
	if err != nil {
		total = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": recordings,
		"total":      total,
	})
}

func (h *Handler) handleDeleteArchiveGroup(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	// Verify camera is archived
	cam, err := h.db.GetCamera(ctx, cameraID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil || !cam.Archived {
		writeError(w, http.StatusNotFound, "archived camera not found")
		return
	}

	if err := h.permanentlyDeleteCamera(ctx, cameraID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete camera")
		return
	}

	h.logSuccess(r, "archive.delete", "archive", cameraID, "archived camera group permanently deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleDeleteArchiveRecording(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	recordingID := chi.URLParam(r, "recordingID")
	ctx := r.Context()

	// Get the recording
	rec, err := h.db.GetRecording(ctx, recordingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil || !rec.Archived || rec.CameraID != cameraID {
		writeError(w, http.StatusNotFound, "archived recording not found")
		return
	}

	// Delete file from disk (non-fatal)
	if rec.FilePath != "" {
		if err := h.store.DeleteFile(rec.FilePath); err != nil {
			logger.Warn("failed to delete archived recording file", "file_path", rec.FilePath, "error", err)
		}
	}

	// Delete recording from DB
	if err := h.db.DeleteRecording(ctx, recordingID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Check if this was the last archived recording for this camera
	count, _, err := h.db.GetArchiveGroupStats(ctx, cameraID)
	if err == nil && count == 0 {
		// No more recordings — clean up camera row and directory
		if err := h.db.DeleteCamera(ctx, cameraID); err != nil {
			logger.Warn("failed to delete empty archive camera", "camera_id", cameraID, "error", err)
		}
		if err := h.store.DeleteCameraDir(cameraID); err != nil {
			logger.Warn("failed to remove camera directory", "camera_id", cameraID, "error", err)
		}
	}

	h.logSuccess(r, "archive.recording.delete", "archive", recordingID, "archived recording deleted", map[string]any{"camera_id": cameraID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleSetArchiveRetention(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	ctx := r.Context()

	var body struct {
		RetentionDays int `json:"retention_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.RetentionDays < 0 {
		writeError(w, http.StatusBadRequest, "retention_days must be >= 0")
		return
	}

	if err := h.db.SetArchiveRetention(ctx, cameraID, body.RetentionDays); err != nil {
		writeError(w, http.StatusNotFound, "archived camera not found")
		return
	}
	h.logSuccess(r, "archive.retention.update", "archive", cameraID, "archive retention updated", map[string]any{"retention_days": body.RetentionDays})
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
