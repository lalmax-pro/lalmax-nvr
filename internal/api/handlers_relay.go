package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleListRelayTasks returns all relay push tasks.
func (h *Handler) handleListRelayTasks(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	tasks, err := h.relayMgr.ListTasks()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, tasks)
}

// handleCreateRelayTask creates a new relay push task.
func (h *Handler) handleCreateRelayTask(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	var req struct {
		StreamID  string `json:"stream_id"`
		TargetURL string `json:"target_url"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.StreamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}
	if req.TargetURL == "" {
		writeError(w, http.StatusBadRequest, "target_url is required")
		return
	}

	task, err := h.relayMgr.CreateTask(r.Context(), req.StreamID, req.TargetURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, task)
}

// handleGetRelayTask returns a relay push task by ID.
func (h *Handler) handleGetRelayTask(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	taskID := chi.URLParam(r, "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	task, err := h.relayMgr.GetTask(taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// handleDeleteRelayTask deletes a relay push task.
func (h *Handler) handleDeleteRelayTask(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	taskID := chi.URLParam(r, "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	if err := h.relayMgr.DeleteTask(taskID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleStartRelayTask starts a relay push task.
func (h *Handler) handleStartRelayTask(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	taskID := chi.URLParam(r, "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	if err := h.relayMgr.StartTask(r.Context(), taskID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// handleStopRelayTask stops a relay push task.
func (h *Handler) handleStopRelayTask(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	taskID := chi.URLParam(r, "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	if err := h.relayMgr.StopTask(taskID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleGetRelayTaskStats returns statistics for a relay push task.
func (h *Handler) handleGetRelayTaskStats(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	taskID := chi.URLParam(r, "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	stats, err := h.relayMgr.GetTaskStats(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// handleGetRelayTaskStatsHistory returns historical statistics for a relay push task.
func (h *Handler) handleGetRelayTaskStatsHistory(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "relay manager not available")
		return
	}

	taskID := chi.URLParam(r, "id")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	// Parse duration parameter (default: 1 hour)
	durationStr := r.URL.Query().Get("duration")
	duration := time.Hour
	if durationStr != "" {
		if d, err := strconv.Atoi(durationStr); err == nil {
			duration = time.Duration(d) * time.Minute
		}
	}

	history, err := h.relayMgr.GetTaskStatsHistory(taskID, duration)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, history)
}