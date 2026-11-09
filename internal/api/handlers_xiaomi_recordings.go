// SPDX-License-Identifier: MIT

package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
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

func (h *Handler) handleXiaomiSDRecordingPlayback(w http.ResponseWriter, r *http.Request) {
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
	start, err := strconv.ParseInt(chi.URLParam(r, "start"), 10, 64)
	if err != nil || start <= 0 || start > 4_102_444_800 {
		writeError(w, http.StatusBadRequest, "start must be a valid recording timestamp")
		return
	}
	channel := uint64(0)
	if value := r.URL.Query().Get("channel"); value != "" {
		channel, err = strconv.ParseUint(value, 10, 8)
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
		ReadSDRecordingFile(context.Context, uint32, int64) ([]byte, error)
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "Xiaomi recording access is unavailable for this camera")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	file, err := recorder.ReadSDRecordingFile(ctx, uint32(channel), start)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, "failed to read Xiaomi SD recording: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Disposition", `inline; filename="xiaomi-recording-`+strconv.FormatInt(start, 10)+`.mp4"`)
	http.ServeContent(w, r, "xiaomi-recording.mp4", time.Unix(start, 0), bytes.NewReader(file))
}
