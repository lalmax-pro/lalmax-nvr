package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
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

func (h *Handler) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	if h.eventHub == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream not available")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	cameraID := r.URL.Query().Get("camera_id")
	source := r.URL.Query().Get("source")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := h.eventHub.Subscribe()
	defer h.eventHub.Unsubscribe(ch)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	var closed atomic.Bool

	match := func(ev model.Event) bool {
		if cameraID != "" && ev.CameraID != cameraID {
			return false
		}
		if source != "" && ev.Source != source {
			return false
		}
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			closed.Store(true)
			return
		case ev, ok := <-ch:
			if !ok || closed.Load() {
				return
			}
			if !match(ev) {
				continue
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: nvr\ndata: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (h *Handler) handleListAlarmRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.db.ListAlarmRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list alarm rules")
		return
	}
	if rules == nil {
		rules = []model.AlarmRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (h *Handler) handleCreateAlarmRule(w http.ResponseWriter, r *http.Request) {
	var rule model.AlarmRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if rule.Action != model.AlarmActionRecord && rule.Action != model.AlarmActionWebhook && rule.Action != model.AlarmActionGotoPreset {
		writeError(w, http.StatusBadRequest, "action must be record, webhook, or goto_preset")
		return
	}
	if rule.Name == "" {
		rule.Name = rule.Action
	}
	rule.Enabled = true
	if err := h.db.InsertAlarmRule(r.Context(), &rule); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create alarm rule")
		return
	}
	h.logSuccess(r, "alarm.rule.create", "alarm_rule", strconv.FormatInt(rule.ID, 10), "alarm rule created", nil)
	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) handleDeleteAlarmRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "ruleID"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	if err := h.db.DeleteAlarmRule(r.Context(), id); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete alarm rule")
		return
	}
	h.logSuccess(r, "alarm.rule.delete", "alarm_rule", strconv.FormatInt(id, 10), "alarm rule deleted", nil)
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
