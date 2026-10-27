package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

func (h *Handler) handleTranscodeCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	reports := []transcode.CapabilityReport{}
	cfg := transcode.Config{}
	if h.config != nil {
		cfg = h.config.Transcoding
	}
	if h.transcodeProbe != nil {
		for _, backend := range []string{"ffmpeg", "gstreamer"} {
			if report, ok := h.transcodeProbe.CurrentResult(cfg.Tools(), transcode.DefaultProfile(backend)); ok {
				reports = append(reports, report)
			}
		}
	}
	enabled := h.config != nil && h.config.Transcoding.Enabled
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "reports": reports})
}

func (h *Handler) handleTranscodeProbe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.transcodeProbe == nil {
		writeError(w, 503, "transcoding capability service unavailable")
		return
	}
	var request struct {
		Backend string `json:"backend"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, "expected backend selection")
		return
	}
	if decoder.Decode(new(any)) != io.EOF || (request.Backend != "ffmpeg" && request.Backend != "gstreamer") {
		writeError(w, 400, "backend must be ffmpeg or gstreamer")
		return
	}
	cfg := transcode.Config{}
	if h.config != nil {
		cfg = h.config.Transcoding
	}
	// Executable paths come exclusively from trusted server configuration, never
	// from an HTTP payload. Detection remains available when transcoding is off.
	report, err := h.transcodeProbe.Probe(r.Context(), cfg.Tools(), transcode.DefaultProfile(request.Backend))
	if err != nil {
		status := http.StatusInternalServerError
		var domain *transcode.Error
		if errors.As(err, &domain) {
			switch domain.Code {
			case "probe_busy":
				status = http.StatusConflict
			case "invalid_spec":
				status = http.StatusBadRequest
			case "probe_canceled":
				status = http.StatusRequestTimeout
			}
			writeJSON(w, status, map[string]any{"error": domain.Message, "code": domain.Code})
			return
		}
		writeError(w, status, "capability check failed")
		return
	}
	writeJSON(w, http.StatusOK, report)
}
