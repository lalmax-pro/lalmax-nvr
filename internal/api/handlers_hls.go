package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// --- HLS streaming endpoints ---

func (h *Handler) handleHLSStream(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	streamID, quality := h.resolvePlayStreamID(r, id)
	w.Header().Set("X-Stream-Quality", quality)
	if h.config != nil && !h.config.IsHLSEnabled() {
		writeError(w, http.StatusServiceUnavailable, "HLS is disabled")
		return
	}
	if h.mediaEngine == nil {
		writeError(w, http.StatusInternalServerError, "HLS not available")
		return
	}
	tail := chi.URLParam(r, "*")
	upstream, err := h.mediaHLSResourceURL(r.Context(), streamID, tail, r.URL.RawQuery)
	if err != nil || upstream == nil {
		writeError(w, http.StatusBadGateway, "failed to build HLS URL")
		return
	}
	if err := h.proxyMediaRequest(w, r, upstream); err != nil {
		logger.Error("failed to proxy HLS request to media engine", "camera_id", id, "tail", tail, "error", err)
		writeError(w, http.StatusBadGateway, "failed to proxy HLS stream")
	}
}

func (h *Handler) handleStopHLSStream(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Snapshot endpoint ---

func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	cameraID := getCameraID(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera id is required")
		return
	}
	h.serveCameraSnapshot(w, r, cameraID)
}
