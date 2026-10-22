package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

func (h *Handler) homeAssistantPTZCamera(w http.ResponseWriter, r *http.Request) *storage.CameraRow {
	if !h.requireHomeAssistantPublication(w, r) {
		return nil
	}
	id := streamIDFromRequest(r)
	binding, err := h.db.GetStreamBinding(r.Context(), id)
	if err != nil {
		writeError(w, 500, "failed to resolve camera binding")
		return nil
	}
	if binding != nil {
		id = binding.CameraID
	}
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, 500, "failed to resolve PTZ camera")
		return nil
	}
	if cam == nil || (cam.Protocol != "onvif" && cam.Protocol != "gb28181") {
		writeError(w, 400, "published stream has no supported PTZ camera")
		return nil
	}
	return cam
}

func (h *Handler) handleHomeAssistantPTZPresets(w http.ResponseWriter, r *http.Request) {
	cam := h.homeAssistantPTZCamera(w, r)
	if cam == nil {
		return
	}
	if cam.Protocol != "onvif" {
		writeJSON(w, 200, map[string]any{"presets": []onvif.PTZPreset{}})
		return
	}
	if h.camMgr == nil {
		writeError(w, 503, "camera manager unavailable")
		return
	}
	controller, err := h.camMgr.GetONVIFPTZController(r.Context(), cam.ID)
	if err != nil {
		writeError(w, 502, "PTZ device unavailable")
		return
	}
	presets, err := controller.GetPresets(r.Context())
	if err != nil {
		writeError(w, 502, "PTZ presets unavailable")
		return
	}
	if presets == nil {
		presets = []onvif.PTZPreset{}
	}
	writeJSON(w, 200, map[string]any{"presets": presets})
}

func (h *Handler) handleHomeAssistantPTZ(w http.ResponseWriter, r *http.Request) {
	cam := h.homeAssistantPTZCamera(w, r)
	if cam == nil {
		return
	}
	var req struct {
		Mode        string  `json:"mode"`
		Pan         float64 `json:"pan"`
		Tilt        float64 `json:"tilt"`
		Zoom        float64 `json:"zoom"`
		Duration    float64 `json:"duration"`
		PresetToken string  `json:"preset_token"`
	}
	if err := decodeHomeAssistantBody(w, r, &req); err != nil {
		writeError(w, 400, "invalid PTZ command")
		return
	}
	if req.Mode != "continuous" && req.Mode != "absolute" && req.Mode != "relative" && req.Mode != "stop" && req.Mode != "goto_preset" {
		writeError(w, 400, "invalid PTZ mode")
		return
	}
	if math.Abs(req.Pan) > 1 || math.Abs(req.Tilt) > 1 || math.Abs(req.Zoom) > 1 || (req.Mode == "absolute" && req.Zoom < 0) {
		writeError(w, 400, "PTZ axes must be normalized to -1..1; absolute zoom is 0..1")
		return
	}
	if req.Duration == 0 {
		req.Duration = 0.5
	}
	if req.Duration < 0.1 || req.Duration > 5 {
		writeError(w, 400, "duration must be between 0.1 and 5 seconds")
		return
	}
	if req.Mode == "goto_preset" && (req.PresetToken == "" || len(req.PresetToken) > 128 || strings.ContainsAny(req.PresetToken, "<>&\"'\r\n")) {
		writeError(w, 400, "invalid preset token")
		return
	}
	var move func(context.Context) error
	var stop func(context.Context) error
	if cam.Protocol == "onvif" {
		if h.camMgr == nil {
			writeError(w, 503, "camera manager unavailable")
			return
		}
		controller, err := h.camMgr.GetONVIFPTZController(r.Context(), cam.ID)
		if err != nil {
			writeError(w, 502, "PTZ device unavailable")
			return
		}
		stop = func(ctx context.Context) error { return controller.Stop(ctx, true, true) }
		vec := onvif.PTZVector{Pan: req.Pan, Tilt: req.Tilt, Zoom: req.Zoom}
		move = func(ctx context.Context) error {
			switch req.Mode {
			case "continuous":
				return controller.ContinuousMove(ctx, vec)
			case "absolute":
				return controller.AbsoluteMove(ctx, vec)
			case "relative":
				return controller.RelativeMove(ctx, vec)
			case "goto_preset":
				return controller.GoToPreset(ctx, req.PresetToken)
			default:
				return stop(ctx)
			}
		}
	} else {
		if req.Mode != "continuous" && req.Mode != "stop" {
			writeError(w, 400, "GB28181 supports continuous movement and stop only")
			return
		}
		if h.gb28181Server == nil {
			writeError(w, 503, "GB28181 unavailable")
			return
		}
		parts := strings.SplitN(cam.ID, ":", 2)
		if len(parts) != 2 {
			writeError(w, 400, "invalid GB28181 channel")
			return
		}
		direction, speed, axes := "stop", 0.0, 0
		for i, axis := range []float64{req.Pan, req.Tilt, req.Zoom} {
			if axis == 0 {
				continue
			}
			axes++
			speed = math.Abs(axis)
			direction = []string{"right", "up", "zoomin"}[i]
			if axis < 0 {
				direction = []string{"left", "down", "zoomout"}[i]
			}
		}
		if axes > 1 {
			writeError(w, 400, "GB28181 supports one movement axis per command")
			return
		}
		stop = func(ctx context.Context) error { return h.sendGB28181PTZControl(ctx, parts[0], parts[1], "stop", 0) }
		move = func(ctx context.Context) error {
			if req.Mode == "stop" {
				return stop(ctx)
			}
			return h.sendGB28181PTZControl(ctx, parts[0], parts[1], direction, speed)
		}
	}
	moveErr := move(r.Context())
	if req.Mode == "continuous" {
		if moveErr == nil {
			timer := time.NewTimer(time.Duration(req.Duration * float64(time.Second)))
			select {
			case <-timer.C:
			case <-r.Context().Done():
			}
			timer.Stop()
		}
		// Stop uses the captured device even if publication is withdrawn or the
		// caller disconnects during the pulse. Do not reauthorize a cleanup stop.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		stopErr := stop(ctx)
		cancel()
		if moveErr == nil {
			moveErr = stopErr
		}
	}
	if moveErr != nil {
		writeError(w, 502, "PTZ command or automatic stop failed")
		return
	}
	h.logSuccess(r, "home_assistant.ptz", "camera", cam.ID, fmt.Sprintf("Home Assistant PTZ %s completed", req.Mode), nil)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
