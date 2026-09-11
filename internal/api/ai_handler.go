package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/multimodal"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/webhook"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// --- AI Manager interface for testability ---

// AIManagerInterface abstracts the AI Manager for the handler.
// Webhook mode: external ai-detector pushes detection results via POST /api/ai/webhook.
type AIManagerInterface interface {
	Status() ai.Status
	OnDetection(cb webhook.CallbackFunc) string
	UnregisterCallback(id string) bool
	StopAll()
	GetReceiver() *webhook.Receiver
	GetMultimodalManager() *multimodal.Manager
}

// --- AI status types ---

// aiStatusResponse is the JSON response for GET /api/ai/status.
type aiStatusResponse struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"` // "webhook", "disabled"
	Reason    string `json:"reason"`  // human-readable status explanation
}

// --- AI handlers ---

// handleGetAIStatus handles GET /api/ai/status.
func (h *Handler) handleGetAIStatus(w http.ResponseWriter, r *http.Request) {
	if h.aiManager == nil {
		writeJSON(w, http.StatusOK, aiStatusResponse{
			Available: false,
			Backend:   "disabled",
			Reason:    "AI 模块未初始化",
		})
		return
	}

	status := h.aiManager.Status()
	writeJSON(w, http.StatusOK, aiStatusResponse{
		Available: status.Available,
		Backend:   status.Backend,
		Reason:    status.Reason,
	})
}

func (h *Handler) handleListAIDetections(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	filter := aiHistoryFilterFromRequest(r)
	items, total, err := h.db.ListAIDetections(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list AI detections")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"detections": items,
		"total":      total,
	})
}

func (h *Handler) handleListAIAnalyses(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	filter := aiHistoryFilterFromRequest(r)
	items, total, err := h.db.ListAIAnalyses(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list AI analyses")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"analyses": items,
		"total":    total,
	})
}

func aiHistoryFilterFromRequest(r *http.Request) storage.AIHistoryFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return storage.AIHistoryFilter{
		CameraID: q.Get("camera_id"),
		Label:    q.Get("label"),
		Limit:    limit,
		Offset:   offset,
	}
}

// handleAIEvents handles GET /api/ai/events.
// SSE stream that pushes detection events to the frontend.
func (h *Handler) handleAIEvents(w http.ResponseWriter, r *http.Request) {
	if h.aiManager == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	eventCh := make(chan webhook.DetectionResult, 16)
	var eventClosed atomic.Bool

	// Register detection callback.
	callbackID := h.aiManager.OnDetection(func(result webhook.DetectionResult) {
		if eventClosed.Load() {
			return
		}
		select {
		case eventCh <- result:
		default:
			logger.Warn("AI SSE: dropping detection event, client too slow")
		}
	})

	// Heartbeat ticker.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	defer func() {
		eventClosed.Store(true)
		close(eventCh)
	}()
	defer h.aiManager.UnregisterCallback(callbackID)

	for {
		select {
		case <-ctx.Done():
			return
		case result := <-eventCh:
			data, err := json.Marshal(result)
			if err != nil {
				logger.Warn("AI SSE: failed to marshal detection", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// handleAIWebhook handles POST /api/ai/webhook.
// Receives detection events from external AI services.
func (h *Handler) handleAIWebhook(w http.ResponseWriter, r *http.Request) {
	if h.aiManager == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	receiver := h.aiManager.GetReceiver()
	if receiver == nil {
		writeError(w, http.StatusServiceUnavailable, "AI webhook not configured")
		return
	}

	receiver.HandleHTTP()(w, r)
}

// handleAIMultimodalStatus handles GET /api/ai/multimodal/status.
// Returns the status of the multimodal analysis subsystem.
func (h *Handler) handleAIMultimodalStatus(w http.ResponseWriter, r *http.Request) {
	if h.aiManager == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	mm := h.aiManager.GetMultimodalManager()
	if mm == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":  false,
			"provider": "",
			"reason":   "多模态分析未配置",
		})
		return
	}

	status := mm.Status()
	writeJSON(w, http.StatusOK, status)
}

// handleAIMultimodalEvents handles GET /api/ai/multimodal/events.
// SSE stream that pushes multimodal analysis results to the frontend.
func (h *Handler) handleAIMultimodalEvents(w http.ResponseWriter, r *http.Request) {
	if h.aiManager == nil {
		writeError(w, http.StatusServiceUnavailable, "AI detection not available")
		return
	}

	mm := h.aiManager.GetMultimodalManager()
	if mm == nil {
		writeError(w, http.StatusServiceUnavailable, "Multimodal analysis not configured")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	eventCh := make(chan multimodal.AnalysisResult, 16)
	var eventClosed atomic.Bool

	// Register analysis callback.
	callbackID := mm.OnResult(func(result multimodal.AnalysisResult) {
		if eventClosed.Load() {
			return
		}
		select {
		case eventCh <- result:
		default:
			logger.Warn("Multimodal SSE: dropping analysis event, client too slow")
		}
	})

	// Heartbeat ticker.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	defer func() {
		eventClosed.Store(true)
		close(eventCh)
	}()
	defer mm.UnregisterCallback(callbackID)

	for {
		select {
		case <-ctx.Done():
			return
		case result := <-eventCh:
			data, err := json.Marshal(result)
			if err != nil {
				logger.Warn("Multimodal SSE: failed to marshal result", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
