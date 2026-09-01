package api

import (
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// --- WHEP (WebRTC-HTTP Egress Protocol) endpoints ---

// handleCreateWHEPSession handles POST /api/cameras/{id}/stream/webrtc
// It accepts an SDP offer and returns an SDP answer with a session URL.
func (h *Handler) handleCreateWHEPSession(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	streamID, quality := h.resolvePlayStreamID(r, id)
	w.Header().Set("X-Stream-Quality", quality)

	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "WebRTC not available: media engine not enabled")
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "application/sdp" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/sdp")
		return
	}

	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}

	if cam == nil {
		info, err := h.mediaEngine.GetStream(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get stream")
			return
		}
		if info == nil {
			writeError(w, http.StatusNotFound, "camera not found")
			return
		}
	}

	upstream, err := h.mediaPlayURL(r.Context(), streamID, "webrtc")
	if err != nil || upstream == nil {
		writeError(w, http.StatusServiceUnavailable, "WebRTC not available")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream.String(), io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to build WHEP request")
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to proxy WebRTC session")
		return
	}
	defer resp.Body.Close()
	copyHeaderFiltered(w.Header(), resp.Header)
	location := resp.Header.Get("Location")
	if location != "" {
		if resolved := resolveUpstreamLocation(upstream, location); resolved != "" {
			token := encodeUpstreamSessionLocation(resolved)
			w.Header().Set("Location", "/api/cameras/"+id+"/stream/webrtc/"+token)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleDeleteWHEPSession handles DELETE /api/cameras/{id}/stream/webrtc/{session}
// It tears down an active WHEP session.
func (h *Handler) handleDeleteWHEPSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session")

	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "WebRTC not available: media engine not enabled")
		return
	}

	upstreamLocation, err := decodeUpstreamSessionLocation(sessionID)
	if err != nil || upstreamLocation == "" {
		writeError(w, http.StatusBadRequest, "invalid session token")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodDelete, upstreamLocation, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to build delete request")
		return
	}
	req.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to proxy delete session")
		return
	}
	defer resp.Body.Close()
	copyHeaderFiltered(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func resolveUpstreamLocation(base *url.URL, location string) string {
	if location == "" {
		return ""
	}
	ref, err := url.Parse(location)
	if err != nil {
		return location
	}
	if base == nil {
		return ref.String()
	}
	return base.ResolveReference(ref).String()
}
