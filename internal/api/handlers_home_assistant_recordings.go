package api

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type homeAssistantRecording struct {
	ID        string       `json:"id"`
	StartedAt time.Time    `json:"started_at"`
	EndedAt   time.Time    `json:"ended_at"`
	Duration  float64      `json:"duration"`
	Format    model.Format `json:"format"`
	FileSize  int64        `json:"file_size"`
}

func haRecording(rec model.Recording) homeAssistantRecording {
	return homeAssistantRecording{rec.ID, rec.StartedAt, rec.EndedAt, rec.Duration, rec.Format, rec.FileSize}
}

func (h *Handler) homeAssistantPublished(ctx context.Context, id string) (bool, error) {
	cfg, err := h.db.GetHomeAssistantSettings(ctx)
	return cfg.Enabled && slices.ContainsFunc(cfg.Streams, func(p storage.HomeAssistantPublication) bool { return p.StreamID == id }), err
}

func (h *Handler) requireHomeAssistantPublication(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	ok, err := h.homeAssistantPublished(r.Context(), streamIDFromRequest(r))
	if err != nil {
		writeError(w, 500, "failed to load publications")
		return false
	}
	if !ok {
		writeError(w, 404, "stream is not published to Home Assistant")
		return false
	}
	return true
}

func (h *Handler) handleHomeAssistantRecordings(w http.ResponseWriter, r *http.Request) {
	if !h.requireHomeAssistantPublication(w, r) {
		return
	}
	limit, offset := 100, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "limit must be between 1 and 100")
			return
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1000000 {
			writeError(w, 400, "invalid offset")
			return
		}
	}
	filter := model.RecordingFilter{StreamID: streamIDFromRequest(r), Limit: limit, Offset: offset, SortBy: "started_at", SortOrder: "desc"}
	recordings, err := h.db.ListRecordings(r.Context(), filter)
	if err != nil {
		writeError(w, 500, "failed to list recordings")
		return
	}
	total, err := h.db.CountRecordingsWithFilter(r.Context(), filter)
	if err != nil {
		writeError(w, 500, "failed to count recordings")
		return
	}
	result := []homeAssistantRecording{}
	for _, rec := range recordings {
		if strings.EqualFold(filepath.Ext(rec.FilePath), ".mp4") {
			result = append(result, haRecording(rec))
		}
	}
	writeJSON(w, 200, map[string]any{"recordings": result, "total": total, "offset": offset, "limit": limit})
}

func (h *Handler) getHomeAssistantRecording(w http.ResponseWriter, r *http.Request) *model.Recording {
	if !h.requireHomeAssistantPublication(w, r) {
		return nil
	}
	rec, err := h.db.GetRecording(r.Context(), chi.URLParam(r, "recording_id"))
	if err != nil {
		writeError(w, 500, "failed to load recording")
		return nil
	}
	sid := streamIDFromRequest(r)
	if rec == nil || rec.Archived || (rec.StreamID != sid && (rec.StreamID != "" || rec.CameraID != sid)) || !strings.EqualFold(filepath.Ext(rec.FilePath), ".mp4") {
		writeError(w, 404, "recording not found for this publication")
		return nil
	}
	return rec
}

func (h *Handler) handleHomeAssistantRecording(w http.ResponseWriter, r *http.Request) {
	if rec := h.getHomeAssistantRecording(w, r); rec != nil {
		writeJSON(w, 200, haRecording(*rec))
	}
}

func (h *Handler) handleHomeAssistantRecordingFile(w http.ResponseWriter, r *http.Request) {
	rec := h.getHomeAssistantRecording(w, r)
	if rec == nil {
		return
	}
	path, err := storage.ValidatePath(h.store.RootDir(), rec.FilePath)
	if err != nil {
		writeError(w, 404, "recording file unavailable")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "inline")
	// ServeFile preserves HEAD, byte ranges and conditional reads for seeking.
	http.ServeFile(w, r, path)
}
