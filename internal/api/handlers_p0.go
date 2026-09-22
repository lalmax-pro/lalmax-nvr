package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

func (h *Handler) SetAutoDiscoverApply(fn func(config.AutoDiscoverConfig)) {
	h.autoDiscoverApply = fn
}

func (h *Handler) handleGetAutoDiscoverSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	cfg := h.config.AutoDiscover
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":           cfg.IsEnabled(),
		"listen_for_hello":  cfg.ListenForHello == nil || *cfg.ListenForHello,
		"scan_interval":     cfg.ScanInterval,
		"default_username":  cfg.DefaultUsername,
		"has_password":      strings.TrimSpace(cfg.DefaultPassword) != "",
		"network_interface": cfg.NetworkInterface,
	})
}

func (h *Handler) handleUpdateAutoDiscoverSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		Enabled          *bool   `json:"enabled"`
		ListenForHello   *bool   `json:"listen_for_hello"`
		ScanInterval     *string `json:"scan_interval"`
		DefaultUsername  *string `json:"default_username"`
		DefaultPassword  *string `json:"default_password"`
		NetworkInterface *string `json:"network_interface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Enabled != nil {
		h.config.AutoDiscover.Enabled = body.Enabled
	}
	if body.ListenForHello != nil {
		h.config.AutoDiscover.ListenForHello = body.ListenForHello
	}
	if body.ScanInterval != nil {
		h.config.AutoDiscover.ScanInterval = *body.ScanInterval
	}
	if body.DefaultUsername != nil {
		h.config.AutoDiscover.DefaultUsername = *body.DefaultUsername
	}
	if body.DefaultPassword != nil && *body.DefaultPassword != "" {
		h.config.AutoDiscover.DefaultPassword = *body.DefaultPassword
	}
	if body.NetworkInterface != nil {
		h.config.AutoDiscover.NetworkInterface = *body.NetworkInterface
	}
	if !h.saveConfig(w) {
		return
	}
	if h.autoDiscoverApply != nil {
		h.autoDiscoverApply(h.config.AutoDiscover)
	}
	h.handleGetAutoDiscoverSettings(w, r)
}

func (h *Handler) handleActivateCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := getCameraID(r)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.camMgr.ActivateCamera(r.Context(), id, body.Username, body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logOperation(r, "camera.activate", "camera", id, "success", "camera activated", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

func (h *Handler) handleRediscoverCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := getCameraID(r)
	found, err := h.camMgr.RediscoverAndReconnect(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": found})
}

func playResourceID(r *http.Request) string {
	if s := chi.URLParam(r, "stream_id"); s != "" {
		return normalizeStreamID(s)
	}
	return getCameraID(r)
}

func isStreamPlayRoute(r *http.Request) bool {
	return chi.URLParam(r, "stream_id") != ""
}

func playAPIPrefix(r *http.Request, resourceID string) string {
	if isStreamPlayRoute(r) {
		return "/api/streams/" + url.PathEscape(resourceID)
	}
	return "/api/cameras/" + resourceID
}

// cameraIngestStreamID returns the lalmax stream a camera ingests from.
// Returns "" when the camera has no bound stream; callers must not fall back
// to the camera ID (streams are separate entities).
func (h *Handler) cameraIngestStreamID(ctx context.Context, cameraID string) string {
	if h.db != nil {
		if b, err := h.db.GetBindingByCameraID(ctx, cameraID); err == nil && b != nil && b.StreamID != "" {
			return b.StreamID
		}
	}
	if h.camMgr != nil {
		if cam := h.camMgr.GetCameraConfig(cameraID); cam != nil {
			if s := strings.TrimSpace(cam.StreamID); s != "" {
				return s
			}
		}
	}
	return ""
}

func (h *Handler) cameraIDForStream(ctx context.Context, streamID string) string {
	if h.db == nil {
		return ""
	}
	b, err := h.db.GetStreamBinding(ctx, streamID)
	if err != nil || b == nil {
		return ""
	}
	return b.CameraID
}

// writePlayStreamNotFound reports a play request whose resource has no stream.
func writePlayStreamNotFound(w http.ResponseWriter, r *http.Request) {
	if isStreamPlayRoute(r) {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}
	writeError(w, http.StatusNotFound, "no stream bound to camera")
}

func (h *Handler) resolvePlayStreamID(r *http.Request, resourceID string) (streamID, quality string) {
	quality = "main"
	wantSub := r.URL.Query().Get("quality") == "sub"
	cameraID := resourceID
	if isStreamPlayRoute(r) {
		streamID = resourceID
		if media.IsSubStreamID(streamID) {
			return streamID, "sub"
		}
		if !wantSub {
			return streamID, quality
		}
		if bound := h.cameraIDForStream(r.Context(), streamID); bound != "" {
			cameraID = bound
		} else {
			cameraID = media.MainStreamID(streamID)
		}
	} else {
		streamID = h.cameraIngestStreamID(r.Context(), resourceID)
		if !wantSub {
			return streamID, quality
		}
	}
	if h.camMgr == nil || !h.camMgr.HasSubStream(cameraID) {
		return streamID, quality
	}
	if err := h.camMgr.EnsureSubStream(r.Context(), cameraID); err != nil {
		logger.Debug("sub-stream fallback to main", "camera_id", cameraID, "error", err)
		return streamID, quality
	}
	return media.SubStreamID(streamID), "sub"
}

func (h *Handler) handleSubHLSStream(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.config != nil && !h.config.IsHLSEnabled() {
		writeError(w, http.StatusServiceUnavailable, "HLS is disabled")
		return
	}
	if h.mediaEngine == nil {
		writeError(w, http.StatusInternalServerError, "HLS not available")
		return
	}
	streamID, quality := h.cameraIngestStreamID(r.Context(), id), "main"
	if streamID == "" {
		writePlayStreamNotFound(w, r)
		return
	}
	if h.camMgr != nil && h.camMgr.HasSubStream(id) {
		if err := h.camMgr.EnsureSubStream(r.Context(), id); err != nil {
			logger.Debug("sub HLS fallback to main", "camera_id", id, "error", err)
		} else {
			streamID = media.SubStreamID(streamID)
			quality = "sub"
		}
	}
	w.Header().Set("X-Stream-Quality", quality)
	tail := chi.URLParam(r, "*")
	upstream, err := h.mediaHLSResourceURL(r.Context(), streamID, tail, r.URL.RawQuery)
	if err != nil || upstream == nil {
		writeError(w, http.StatusBadGateway, "failed to build HLS URL")
		return
	}
	if err := h.proxyMediaRequest(w, r, upstream); err != nil {
		logger.Error("failed to proxy sub HLS request", "camera_id", id, "error", err)
		writeError(w, http.StatusBadGateway, "failed to proxy HLS stream")
	}
}
