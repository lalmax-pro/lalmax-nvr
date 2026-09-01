package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleFMP4Stream handles GET /api/cameras/{id}/stream.m4s
// It proxies the HTTP fMP4 stream from the lalmax media engine.
// The response is a continuous fMP4 byte stream: init segment (ftyp+moov)
// followed by fragmented moof+mdat parts.
func (h *Handler) handleFMP4Stream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	streamID, quality := h.resolvePlayStreamID(r, id)
	w.Header().Set("X-Stream-Quality", quality)
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "fMP4 streaming not available: media engine disabled")
		return
	}

	upstream, err := h.mediaPlayURL(r.Context(), streamID, "fmp4")
	if err != nil || upstream == nil {
		writeError(w, http.StatusBadGateway, "failed to resolve fMP4 stream URL")
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
		logger.Debug("fMP4 stream ended", "camera_id", id, "error", err)
	}
}
