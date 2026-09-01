package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// --- HTTP-FLV streaming endpoint ---

// handleFLVStream handles GET /api/cameras/{id}/stream.flv
func (h *Handler) handleFLVStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	streamID, quality := h.resolvePlayStreamID(r, id)
	w.Header().Set("X-Stream-Quality", quality)
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "FLV streaming not available")
		return
	}
	upstream, err := h.mediaPlayURL(r.Context(), streamID, "flv")
	if err != nil || upstream == nil {
		writeError(w, http.StatusBadGateway, "failed to build FLV URL")
		return
	}
	if r.URL.RawQuery != "" {
		if upstream.RawQuery == "" {
			upstream.RawQuery = r.URL.RawQuery
		} else {
			upstream.RawQuery = upstream.RawQuery + "&" + r.URL.RawQuery
		}
	}
	if err := h.proxyMediaRequest(w, r, upstream); err != nil {
		logger.Error("failed to proxy FLV request to media engine", "camera_id", id, "error", err)
		writeError(w, http.StatusBadGateway, "failed to proxy FLV stream")
	}
}
