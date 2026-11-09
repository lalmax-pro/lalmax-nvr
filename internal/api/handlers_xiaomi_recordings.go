// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

func (h *Handler) handleXiaomiSDRecordings(w http.ResponseWriter, r *http.Request) {
	cameraID := h.getCameraIDFromRequest(r)
	camera, err := h.getCameraByID(r.Context(), cameraID)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if camera.Protocol != "xiaomi" {
		writeError(w, http.StatusBadRequest, "camera is not a Xiaomi device")
		return
	}

	storageChannel := uint64(0)
	if value := r.URL.Query().Get("channel"); value != "" {
		storageChannel, err = strconv.ParseUint(value, 10, 8)
		if err != nil {
			writeError(w, http.StatusBadRequest, "channel must be an integer between 0 and 255")
			return
		}
	}
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager is unavailable")
		return
	}
	recorder, ok := h.camMgr.GetRecorder(cameraID).(interface {
		ReadSDRecordings(context.Context, uint32) ([]xiaomi.SDRecording, error)
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "Xiaomi recording access is unavailable for this camera")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	clips, err := recorder.ReadSDRecordings(ctx, uint32(storageChannel))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, "failed to read Xiaomi SD recordings: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"camera_id":       cameraID,
		"storage_channel": storageChannel,
		"recordings":      clips,
		"total":           len(clips),
	})
}
