package api

import (
	"encoding/json"
	"net/http"
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

func (h *Handler) resolvePlayStreamID(r *http.Request, cameraID string) (streamID, quality string) {
	streamID = cameraID
	quality = "main"
	wantSub := r.URL.Query().Get("quality") == "sub"
	if !wantSub {
		return streamID, quality
	}
	if h.camMgr == nil || !h.camMgr.HasSubStream(cameraID) {
		return streamID, quality
	}
	if err := h.camMgr.EnsureSubStream(r.Context(), cameraID); err != nil {
		logger.Debug("sub-stream fallback to main", "camera_id", cameraID, "error", err)
		return streamID, quality
	}
	return media.SubStreamID(cameraID), "sub"
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
	streamID, quality := id, "main"
	if h.camMgr != nil && h.camMgr.HasSubStream(id) {
		if err := h.camMgr.EnsureSubStream(r.Context(), id); err != nil {
			logger.Debug("sub HLS fallback to main", "camera_id", id, "error", err)
		} else {
			streamID = media.SubStreamID(id)
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
