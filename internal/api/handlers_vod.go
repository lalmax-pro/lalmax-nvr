package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/vod"
)

var vodFragmentRE = regexp.MustCompile(`^f(\d+)-(\d+)\.m4s$`)

func (h *Handler) handleRecordingsTimeline(w http.ResponseWriter, r *http.Request) {
	cameraID := r.URL.Query().Get("camera_id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}
	start, end, ok := parseStartEnd(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "start and end must be RFC3339 timestamps")
		return
	}
	entries, err := h.db.ListRecordingTimeline(r.Context(), cameraID, start, end, 10000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list timeline")
		return
	}
	if entries == nil {
		entries = []storage.TimelineEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"camera_id": cameraID,
		"entries":   entries,
	})
}

func (h *Handler) handleVODPlaylist(w http.ResponseWriter, r *http.Request) {
	cameraID := getCameraID(r)
	start, end, ok := parseStartEnd(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "start and end must be RFC3339 timestamps")
		return
	}
	recs, err := h.db.ListRecordings(r.Context(), model.RecordingFilter{
		CameraID:  cameraID,
		StartTime: start,
		EndTime:   end,
		SortBy:    "started_at",
		SortOrder: "asc",
		Limit:     10000,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	var items []vod.PlaylistItem
	for i := range recs {
		rec := recs[i]
		if !vod.IsVODFormat(rec.Format) {
			continue
		}
		info, err := merge.ParseSegment(rec.FilePath)
		if err != nil || info == nil || len(info.Samples) == 0 {
			continue
		}
		info.FilePath = rec.FilePath
		frags := vod.SplitFragments(info)
		if len(frags) == 0 {
			continue
		}
		items = append(items, vod.PlaylistItem{Recording: &rec, Info: info, Frags: frags})
	}

	body := vod.BuildPlaylist(cameraID, items, fmt.Sprintf("/api/cameras/%s/playback/{recId}", cameraID))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (h *Handler) handleVODInit(w http.ResponseWriter, r *http.Request) {
	info, rec, ok := h.loadVODSegment(w, r)
	if !ok {
		return
	}
	_ = rec
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=60")
	if err := vod.WriteInit(w, info); err != nil {
		logger.Warn("vod init failed", "error", err, "recording_id", rec.ID)
	}
}

func (h *Handler) handleVODFragment(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "fragment")
	m := vodFragmentRE.FindStringSubmatch(name)
	if m == nil {
		writeError(w, http.StatusNotFound, "fragment not found")
		return
	}
	first, _ := strconv.Atoi(m[1])
	last, _ := strconv.Atoi(m[2])
	info, rec, ok := h.loadVODSegment(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "video/iso.segment")
	w.Header().Set("Cache-Control", "private, max-age=60")
	if err := vod.WriteFragment(w, info, 1, first, last); err != nil {
		logger.Warn("vod fragment failed", "error", err, "recording_id", rec.ID)
	}
}

func (h *Handler) loadVODSegment(w http.ResponseWriter, r *http.Request) (*merge.SegmentInfo, *model.Recording, bool) {
	cameraID := getCameraID(r)
	recID := chi.URLParam(r, "recId")
	rec, err := h.db.GetRecording(r.Context(), recID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return nil, nil, false
	}
	if rec == nil || rec.CameraID != cameraID {
		writeError(w, http.StatusNotFound, "recording not found")
		return nil, nil, false
	}
	if !vod.IsVODFormat(rec.Format) {
		writeError(w, http.StatusNotFound, "recording is not fMP4 VOD")
		return nil, nil, false
	}
	info, err := merge.ParseSegment(rec.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "segment missing")
		return nil, nil, false
	}
	info.FilePath = rec.FilePath
	return info, rec, true
}

func parseStartEnd(r *http.Request) (time.Time, time.Time, bool) {
	start, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	end, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil || end.Before(start) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}
