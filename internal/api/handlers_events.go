package api

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

func (h *Handler) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := storage.EventsFilter{
		CameraID: q.Get("camera_id"),
		Source:   q.Get("source"),
		Type:     q.Get("type"),
		Status:   q.Get("status"),
		Since:    q.Get("since"),
		Until:    q.Get("until"),
	}

	if v := q.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit parameter")
			return
		}
		filter.Limit = limit
	}
	if v := q.Get("offset"); v != "" {
		offset, err := strconv.Atoi(v)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset parameter")
			return
		}
		filter.Offset = offset
	}

	events, total, err := h.db.ListEvents(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func (h *Handler) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEventID(w, r)
	if !ok {
		return
	}
	event, err := h.db.GetEvent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get event")
		return
	}
	if event == nil {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (h *Handler) handleAcknowledgeEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEventID(w, r)
	if !ok {
		return
	}
	if err := h.db.AcknowledgeEvent(r.Context(), id, time.Now().UTC()); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to acknowledge event")
		return
	}
	h.logSuccess(r, "event.ack", "event", strconv.FormatInt(id, 10), "event acknowledged", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
}

func (h *Handler) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseEventID(w, r)
	if !ok {
		return
	}
	if err := h.db.DeleteEvent(r.Context(), id); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete event")
		return
	}
	h.logSuccess(r, "event.delete", "event", strconv.FormatInt(id, 10), "event deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func parseEventID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return 0, false
	}
	return id, true
}
