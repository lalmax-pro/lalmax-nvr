package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// recordingPlanRequest is the create/update body for a recording plan.
type recordingPlanRequest struct {
	StreamID string                   `json:"stream_id"`
	Name     string                   `json:"name"`
	Mode     string                   `json:"mode"`
	Enabled  *bool                    `json:"enabled"`
	Windows  []storage.ScheduleWindow `json:"windows"`
}

var validRecordingModes = map[string]bool{
	storage.RecordingModeContinuous: true,
	storage.RecordingModeScheduled:  true,
	storage.RecordingModeOff:        true,
	storage.RecordingModeEvent:      true,
	storage.RecordingModeAdaptive:   true,
}

// refreshRecordingPlans reloads the shared planner so plan edits take effect at once.
func (h *Handler) refreshRecordingPlans(r *http.Request) {
	if h.recPlanner == nil {
		return
	}
	if err := h.recPlanner.Refresh(r.Context()); err != nil {
		logger.Warn("failed to refresh recording plans", "error", err)
	}
	if h.reconcileRecording != nil {
		h.reconcileRecording(r.Context())
	}
}

func (h *Handler) handleListRecordingPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.db.ListRecordingPlans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list recording plans")
		return
	}
	if plans == nil {
		plans = []storage.RecordingPlan{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (h *Handler) handleGetRecordingPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plan, err := h.db.GetRecordingPlan(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording plan")
		return
	}
	if plan == nil {
		writeError(w, http.StatusNotFound, "recording plan not found")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h *Handler) handleCreateRecordingPlan(w http.ResponseWriter, r *http.Request) {
	var body recordingPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	plan, ok := buildRecordingPlan(w, body, "")
	if !ok {
		return
	}
	existing, err := h.db.GetRecordingPlanByStream(r.Context(), plan.StreamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up recording plan")
		return
	}
	if existing != nil {
		writeError(w, http.StatusConflict, "a recording plan already exists for this stream")
		return
	}
	if err := h.db.UpsertRecordingPlan(r.Context(), plan); err != nil {
		logger.Error("failed to create recording plan", "stream_id", plan.StreamID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create recording plan")
		return
	}
	h.refreshRecordingPlans(r)
	h.logSuccess(r, "recording_plan.create", "recording_plan", plan.ID, "recording plan created", nil)
	writeJSON(w, http.StatusCreated, plan)
}

func (h *Handler) handleUpdateRecordingPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := h.db.GetRecordingPlan(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording plan")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "recording plan not found")
		return
	}

	var body recordingPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	plan, ok := buildRecordingPlan(w, body, id)
	if !ok {
		return
	}
	// The stream is immutable; an omitted stream_id keeps the existing one.
	if plan.StreamID == "" {
		plan.StreamID = existing.StreamID
	}
	if strings.TrimSpace(body.Mode) == "" {
		plan.Mode = existing.Mode
	}
	if plan.Name == "" {
		plan.Name = existing.Name
	}
	if body.Enabled == nil {
		plan.Enabled = existing.Enabled
	}
	if body.Windows == nil {
		plan.Windows = existing.Windows
	}
	plan.ID = id

	if err := h.db.UpsertRecordingPlan(r.Context(), plan); err != nil {
		logger.Error("failed to update recording plan", "plan_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update recording plan")
		return
	}
	h.refreshRecordingPlans(r)
	h.logSuccess(r, "recording_plan.update", "recording_plan", id, "recording plan updated", nil)
	writeJSON(w, http.StatusOK, plan)
}

func (h *Handler) handleDeleteRecordingPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.db.DeleteRecordingPlan(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			writeError(w, http.StatusNotFound, "recording plan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete recording plan")
		return
	}
	h.refreshRecordingPlans(r)
	h.logSuccess(r, "recording_plan.delete", "recording_plan", id, "recording plan deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// buildRecordingPlan validates the request and returns the plan to persist.
func buildRecordingPlan(w http.ResponseWriter, body recordingPlanRequest, id string) (*storage.RecordingPlan, bool) {
	streamID := strings.TrimSpace(body.StreamID)
	mode := strings.TrimSpace(body.Mode)
	if mode == "" {
		mode = storage.RecordingModeContinuous
	}
	if streamID == "" && id == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return nil, false
	}
	if !validRecordingModes[mode] {
		writeError(w, http.StatusBadRequest, "mode must be one of continuous, scheduled, off, event, adaptive")
		return nil, false
	}
	for _, win := range body.Windows {
		if win.DayOfWeek < 0 || win.DayOfWeek > 6 {
			writeError(w, http.StatusBadRequest, "window day_of_week must be 0..6")
			return nil, false
		}
		if !isHHMM(win.StartTime) || !isHHMM(win.EndTime) {
			writeError(w, http.StatusBadRequest, "window times must be HH:MM")
			return nil, false
		}
	}
	plan := &storage.RecordingPlan{
		ID:       id,
		StreamID: streamID,
		Name:     strings.TrimSpace(body.Name),
		Mode:     mode,
		Enabled:  true,
		Windows:  body.Windows,
	}
	if body.Enabled != nil {
		plan.Enabled = *body.Enabled
	}
	return plan, true
}

func isHHMM(v string) bool {
	if len(v) != 5 || v[2] != ':' {
		return false
	}
	for i, c := range v {
		if i == 2 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SetRecordingPlanner wires the shared plan state so edits apply immediately.
func (h *Handler) SetRecordingPlanner(p *recorder.RecordingPlanner) {
	h.recPlanner = p
}

// SetRecordingReconciler runs after a plan edit so record tasks update at once.
func (h *Handler) SetRecordingReconciler(fn func(context.Context)) {
	h.reconcileRecording = fn
}
