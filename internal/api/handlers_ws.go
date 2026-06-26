package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/lalmax-pro/lalmax-nvr/internal/wsstream"
)

// --- WebSocket streaming endpoint ---

// handleStreamWS handles GET /api/cameras/{id}/stream/ws
// It upgrades the HTTP connection to a WebSocket and streams binary-encoded
// video frames (CodecInfo first, then VideoFrame messages).
func (h *Handler) handleStreamWS(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)

	if h.wsMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "WebSocket streaming not available")
		return
	}

	// Check camera exists
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		slog.Error("WS: failed to get camera", "camera_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// On-demand registration: if WebSocket stream not registered, register it
	if !h.wsMgr.IsActive(id) {
		if h.camMgr == nil {
			writeError(w, http.StatusNotFound, "WebSocket stream not active")
			return
		}
		rec := h.camMgr.GetRecorder(id)
		if rec == nil {
			slog.Warn("WS: recorder not running", "camera_id", id)
			writeError(w, http.StatusBadRequest, "camera recorder not running")
			return
		}

		codec, sps, pps, vps := getCodecParams(rec)
		slog.Info("WS: on-demand register", "camera_id", id, "codec", codec, "has_sps", sps != nil, "has_pps", pps != nil)
		if sps == nil || pps == nil {
			writeError(w, http.StatusServiceUnavailable, "waiting for video stream")
			return
		}

		hub := getStreamHub(rec)
		if err := h.wsMgr.RegisterStream(id, codec, sps, pps, vps, hub); err != nil {
			if !errors.Is(err, wsstream.ErrStreamExists) {
				slog.Error("WS: failed to register", "camera_id", id, "error", err)
				writeError(w, http.StatusInternalServerError, "failed to register WebSocket stream")
				return
			}
		} else {
			// Fresh registration — attach audio track if the recorder has one.
			if aCodec, aConfig := getAudioParams(rec); aCodec != "" {
				if aci, ok := wsstream.BuildAudioCodecInfo(aCodec, aConfig); ok {
					if err := h.wsMgr.SetAudioConfig(id, aci); err != nil {
						slog.Warn("WS: failed to attach audio", "camera_id", id, "error", err)
					}
				} else {
					slog.Warn("WS: unusable audio config", "camera_id", id, "codec", aCodec)
				}
			}
		}
	}

	slog.Info("WS: serving", "camera_id", id)

	// Serve WebSocket stream (blocks until client disconnects)
	if err := h.wsMgr.ServeWS(id, w, r); err != nil {
		if errors.Is(err, wsstream.ErrStreamNotActive) {
			writeError(w, http.StatusNotFound, "WebSocket stream not active")
			return
		}
		if errors.Is(err, wsstream.ErrMaxViewers) {
			writeError(w, http.StatusServiceUnavailable, "maximum WebSocket viewers reached")
			return
		}
		slog.Error("WS: serve failed", "camera_id", id, "error", err, "error_type", fmt.Sprintf("%T", err))
	}
}
