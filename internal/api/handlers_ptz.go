package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// --- Unified PTZ endpoints ---

func (h *Handler) handlePTZMove(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	var req struct {
		Mode string  `json:"mode"`
		Pan  float64 `json:"pan"`
		Tilt float64 `json:"tilt"`
		Zoom float64 `json:"zoom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Mode != "continuous" && req.Mode != "absolute" && req.Mode != "relative" {
		writeError(w, http.StatusBadRequest, "mode must be continuous, absolute, or relative")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	switch camera.Protocol {
	case "gb28181":
		h.handleGB28181PTZMove(w, r, cameraID, req.Mode, req.Pan, req.Tilt, req.Zoom)
	case "onvif":
		h.handleONVIFPTZMove(w, r, cameraID, req.Mode, req.Pan, req.Tilt, req.Zoom)
	default:
		writeError(w, http.StatusBadRequest, "PTZ control is only available for ONVIF or GB28181 cameras")
	}
}

func (h *Handler) handlePTZStop(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	switch camera.Protocol {
	case "gb28181":
		h.handleGB28181PTZStop(w, r, cameraID)
	case "onvif":
		h.handleONVIFPTZStop(w, r, cameraID)
	default:
		writeError(w, http.StatusBadRequest, "PTZ control is only available for ONVIF or GB28181 cameras")
	}
}

func (h *Handler) handlePTZGetPresets(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	switch camera.Protocol {
	case "gb28181":
		// GB28181 doesn't support presets
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"presets": []interface{}{},
		})
	case "onvif":
		h.handleONVIFPTZGetPresets(w, r, cameraID)
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"presets": []interface{}{},
		})
	}
}

func (h *Handler) handlePTZCreatePreset(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	if camera.Protocol != "onvif" {
		writeError(w, http.StatusBadRequest, "PTZ presets are only available for ONVIF cameras")
		return
	}

	h.handleONVIFPTZCreatePreset(w, r, cameraID)
}

func (h *Handler) handlePTZGoToPreset(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	if camera.Protocol != "onvif" {
		writeError(w, http.StatusBadRequest, "PTZ presets are only available for ONVIF cameras")
		return
	}

	h.handleONVIFPTZGoToPreset(w, r, cameraID)
}

func (h *Handler) handlePTZDeletePreset(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	if camera.Protocol != "onvif" {
		writeError(w, http.StatusBadRequest, "PTZ presets are only available for ONVIF cameras")
		return
	}

	h.handleONVIFPTZDeletePreset(w, r, cameraID)
}

func (h *Handler) handlePTZStatus(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "invalid camera ID")
		return
	}

	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	switch camera.Protocol {
	case "onvif":
		h.handleONVIFPTZStatus(w, r, cameraID)
	case "gb28181":
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"moving": false,
		})
	default:
		writeError(w, http.StatusBadRequest, "PTZ control is only available for ONVIF or GB28181 cameras")
	}
}

// --- Helper functions ---

func (h *Handler) getCameraIDFromRequest(r *http.Request) string {
	cameraID := chi.URLParam(r, "id")
	if decoded, err := url.PathUnescape(cameraID); err == nil {
		cameraID = decoded
	}
	return cameraID
}

func (h *Handler) getCameraByID(ctx context.Context, cameraID string) (*storage.CameraRow, error) {
	if h.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	camera, err := h.db.GetCamera(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	if camera == nil {
		return nil, fmt.Errorf("camera not found")
	}
	return camera, nil
}

// --- GB28181 PTZ handlers ---

func (h *Handler) handleGB28181PTZMove(w http.ResponseWriter, r *http.Request, cameraID, mode string, pan, tilt, zoom float64) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 not enabled")
		return
	}

	parts := strings.SplitN(cameraID, ":", 2)
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, "invalid GB28181 camera ID format")
		return
	}
	deviceID, channelID := parts[0], parts[1]

	direction := "stop"
	speed := 0.5
	if mode == "continuous" {
		if pan > 0 {
			direction = "right"
			speed = pan
		} else if pan < 0 {
			direction = "left"
			speed = -pan
		} else if tilt > 0 {
			direction = "up"
			speed = tilt
		} else if tilt < 0 {
			direction = "down"
			speed = -tilt
		} else if zoom > 0 {
			direction = "zoomin"
			speed = zoom
		} else if zoom < 0 {
			direction = "zoomout"
			speed = -zoom
		}
	}

	if err := h.sendGB28181PTZControl(r.Context(), deviceID, channelID, direction, speed); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "ptz.move", "camera", cameraID, "GB28181 PTZ move command sent", map[string]any{"mode": mode})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181PTZStop(w http.ResponseWriter, r *http.Request, cameraID string) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 not enabled")
		return
	}

	parts := strings.SplitN(cameraID, ":", 2)
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, "invalid GB28181 camera ID format")
		return
	}
	deviceID, channelID := parts[0], parts[1]

	if err := h.sendGB28181PTZControl(r.Context(), deviceID, channelID, "stop", 0); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "ptz.stop", "camera", cameraID, "GB28181 PTZ stop command sent", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- ONVIF PTZ handlers ---

func (h *Handler) handleONVIFPTZMove(w http.ResponseWriter, r *http.Request, cameraID, mode string, pan, tilt, zoom float64) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	vec := onvif.PTZVector{Pan: pan, Tilt: tilt, Zoom: zoom}
	switch mode {
	case "continuous":
		err = ptz.ContinuousMove(r.Context(), vec)
	case "absolute":
		err = ptz.AbsoluteMove(r.Context(), vec)
	case "relative":
		err = ptz.RelativeMove(r.Context(), vec)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ command failed: %v", err))
		return
	}
	h.logSuccess(r, "ptz.move", "camera", cameraID, "PTZ move command sent", map[string]any{"mode": mode})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleONVIFPTZStop(w http.ResponseWriter, r *http.Request, cameraID string) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.Stop(r.Context(), true, true); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("PTZ stop failed: %v", err))
		return
	}
	h.logSuccess(r, "ptz.stop", "camera", cameraID, "PTZ stop command sent", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *Handler) handleONVIFPTZGetPresets(w http.ResponseWriter, r *http.Request, cameraID string) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	presets, err := ptz.GetPresets(r.Context())
	if err != nil {
		if isONVIFNotSupported(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"presets": []onvif.PTZPreset{},
			})
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get PTZ presets failed: %v", err))
		return
	}
	if presets == nil {
		presets = []onvif.PTZPreset{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"presets": presets,
	})
}

func (h *Handler) handleONVIFPTZCreatePreset(w http.ResponseWriter, r *http.Request, cameraID string) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	token, err := ptz.SetPreset(r.Context(), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create preset failed: %v", err))
		return
	}
	h.logSuccess(r, "ptz.preset.create", "camera", cameraID, "PTZ preset created", map[string]any{"name": req.Name, "token": token})
	writeJSON(w, http.StatusOK, map[string]string{"token": token, "name": req.Name})
}

func (h *Handler) handleONVIFPTZGoToPreset(w http.ResponseWriter, r *http.Request, cameraID string) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "preset token is required")
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.GoToPreset(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("goto preset failed: %v", err))
		return
	}
	h.logSuccess(r, "ptz.preset.goto", "camera", cameraID, "PTZ goto preset", map[string]any{"token": token})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleONVIFPTZDeletePreset(w http.ResponseWriter, r *http.Request, cameraID string) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "preset token is required")
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	if err := ptz.RemovePreset(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("delete preset failed: %v", err))
		return
	}
	h.logSuccess(r, "ptz.preset.delete", "camera", cameraID, "PTZ preset deleted", map[string]any{"token": token})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleONVIFPTZStatus(w http.ResponseWriter, r *http.Request, cameraID string) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	ptz, err := h.camMgr.GetONVIFPTZController(r.Context(), cameraID)
	if err != nil {
		handleONVIFPTZError(w, cameraID, err)
		return
	}
	pos, moving, err := ptz.GetStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("get PTZ status failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"position": pos,
		"moving":   moving,
	})
}
