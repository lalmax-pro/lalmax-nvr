package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

func xiaomiMotorVector(mode string, pan, tilt, zoom float64) (string, int, error) {
	if mode != "continuous" {
		return "", 0, fmt.Errorf("Xiaomi PTZ supports continuous movement only")
	}
	for _, v := range []float64{pan, tilt, zoom} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -1 || v > 1 {
			return "", 0, fmt.Errorf("PTZ axes must be between -1 and 1")
		}
	}
	if zoom != 0 {
		return "", 0, fmt.Errorf("Xiaomi PTZ does not support optical zoom")
	}
	if pan == 0 && tilt == 0 {
		return "stop", 0, nil
	}
	var direction string
	magnitude := math.Abs(pan)
	if math.Abs(tilt) > magnitude {
		magnitude = math.Abs(tilt)
		if tilt > 0 {
			direction = "up"
		} else {
			direction = "down"
		}
	} else if pan > 0 {
		direction = "right"
	} else {
		direction = "left"
	}
	return direction, max(1, int(math.Round(magnitude*10))), nil
}

func (h *Handler) sendXiaomiMotor(ctx context.Context, cameraID, direction string, speed int) error {
	if h.xiaomiMotorDispatch != nil {
		return h.xiaomiMotorDispatch(ctx, cameraID, direction, speed)
	}
	if h.camMgr == nil {
		return xiaomi.ErrNotConnected
	}
	motor, ok := h.camMgr.GetRecorder(cameraID).(interface {
		MotorControl(context.Context, string, int) error
	})
	if !ok {
		return xiaomi.ErrNotConnected
	}
	return motor.MotorControl(ctx, direction, speed)
}

func writeXiaomiMotorError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, xiaomi.ErrNotConnected) {
		status = http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	writeError(w, status, fmt.Sprintf("Xiaomi motor command failed: %v", err))
}

func (h *Handler) handleXiaomiPTZMove(w http.ResponseWriter, r *http.Request, cameraID, mode string, pan, tilt, zoom float64) {
	direction, speed, err := xiaomiMotorVector(mode, pan, tilt, zoom)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.sendXiaomiMotor(r.Context(), cameraID, direction, speed); err != nil {
		writeXiaomiMotorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleXiaomiPTZStop(w http.ResponseWriter, r *http.Request, cameraID string) {
	if err := h.sendXiaomiMotor(r.Context(), cameraID, "stop", 0); err != nil {
		writeXiaomiMotorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleXiaomiPTZCapabilities(w http.ResponseWriter, r *http.Request) {
	id := h.getCameraIDFromRequest(r)
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}
	camera, err := h.getCameraByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if camera.Protocol != "xiaomi" {
		writeError(w, http.StatusBadRequest, "camera is not a Xiaomi device")
		return
	}
	model := camera.Model
	if h.camMgr != nil {
		if recorder, ok := h.camMgr.GetRecorder(id).(interface{ MotorProfile() xiaomi.MotorProfile }); ok {
			if profile := recorder.MotorProfile(); profile.Model != "" {
				writeJSON(w, http.StatusOK, profile)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, xiaomi.MotorProfileForModel(model))
}
