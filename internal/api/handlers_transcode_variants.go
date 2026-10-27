package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"net/http"
)

type derivedPlaybackProvider interface {
	DerivedPlayURL(context.Context, transcode.SourceRef, string, string) (*media.PlayURL, error)
}

func (h *Handler) SetTranscodePlayback(provider derivedPlaybackProvider) {
	h.transcodePlayback = provider
}

// This endpoint is administrator-only until consumer-specific source grants are
// integrated. A returned token is a bearer credential and must not be cached.
func (h *Handler) handleTranscodePlayURL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.requireTranscodeService(w) {
		return
	}
	if h.transcodePlayback == nil {
		writeError(w, 503, "derived playback unavailable")
		return
	}
	source, id, err := h.transcodeService.PlaybackTarget(r.Context(), chi.URLParam(r, "stream_id"), chi.URLParam(r, "job_id"))
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	protocol := r.URL.Query().Get("protocol")
	if protocol == "" {
		protocol = "rtsp"
	}
	// Only RTSP exposed initially; HLS/WHEP token propagation requires protocol E2E.
	if protocol != "rtsp" {
		writeError(w, 400, "only RTSP derived playback is currently available")
		return
	}
	play, err := h.transcodePlayback.DerivedPlayURL(r.Context(), source, id, protocol)
	if err != nil || play == nil {
		writeError(w, 503, "derived playback unavailable")
		return
	}
	writeJSON(w, 200, play)
}

func (h *Handler) handleTranscodeVariants(w http.ResponseWriter, r *http.Request) {
	if !h.requireTranscodeService(w) {
		return
	}
	variants, err := h.transcodeService.Variants(r.Context(), chi.URLParam(r, "stream_id"))
	if err != nil {
		transcodeAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"variants": variants})
}
