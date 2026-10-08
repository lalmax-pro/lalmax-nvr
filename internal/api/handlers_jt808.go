package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/jt808"
)

type jt1078AudioPusher interface {
	PushJT1078Audio(sim string, channel byte, pt byte, payload []byte) error
}

func (h *Handler) handleJT808ListTerminals(w http.ResponseWriter, _ *http.Request) {
	if h.jt808Server == nil {
		writeJSON(w, http.StatusOK, map[string]any{"terminals": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminals": h.jt808Server.ListTerminals()})
}

func (h *Handler) handleJT808Play(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key       string `json:"key"`
		Channel   byte   `json:"channel"`
		Transport string `json:"transport,omitempty"`
		DataType  byte   `json:"data_type"`
		Stream    byte   `json:"stream"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	streamID, err := h.jt808Server.Play(jt808.PlayInput{
		Key:       req.Key,
		Channel:   req.Channel,
		Transport: req.Transport,
		DataType:  req.DataType,
		Stream:    req.Stream,
	})
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	h.logSuccess(r, "jt808.play", "jt808", req.Key, "JT808 0x9101 sent", map[string]any{"stream_id": streamID})
	writeJSON(w, http.StatusOK, map[string]any{"stream_id": streamID})
}

func (h *Handler) handleJT808StopPlay(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key     string `json:"key"`
		Channel byte   `json:"channel"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	if err := h.jt808Server.StopPlay(jt808.StopPlayInput{Key: req.Key, Channel: req.Channel}); err != nil {
		writeJT808Err(w, err)
		return
	}
	h.logSuccess(r, "jt808.stop", "jt808", req.Key, "JT808 0x9102 sent", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJT808Control(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key     string `json:"key"`
		Channel byte   `json:"channel"`
		Control byte   `json:"control"`
		CloseAV byte   `json:"close_av"`
		Stream  byte   `json:"stream"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	err := h.jt808Server.LiveControl(jt808.LiveControlInput{
		Key: req.Key, Channel: req.Channel, Control: req.Control, CloseAV: req.CloseAV, Stream: req.Stream,
	})
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	h.logSuccess(r, "jt808.control", "jt808", req.Key, "JT808 0x9102 sent", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJT808Loss(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key     string `json:"key"`
		Channel byte   `json:"channel"`
		Rate    byte   `json:"rate"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	if err := h.jt808Server.ReportLoss(jt808.LossInput{Key: req.Key, Channel: req.Channel, Rate: req.Rate}); err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJT808QueryAV(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	key := r.URL.Query().Get("key")
	if !requireJT808Key(w, key) {
		return
	}
	prop, err := h.jt808Server.QueryAV(key)
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prop)
}

func (h *Handler) handleJT808Playback(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key        string `json:"key"`
		Channel    byte   `json:"channel"`
		Transport  string `json:"transport"`
		AVType     byte   `json:"av_type"`
		StreamType byte   `json:"stream_type"`
		Storage    byte   `json:"storage"`
		Mode       byte   `json:"mode"`
		Speed      byte   `json:"speed"`
		Start      string `json:"start"`
		End        string `json:"end"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	start, ok := parseJT808TimeField(w, req.Start)
	if !ok {
		return
	}
	end, ok := parseJT808TimeField(w, req.End)
	if !ok {
		return
	}
	id, err := h.jt808Server.Playback(jt808.PlaybackInput{
		Key: req.Key, Channel: req.Channel, Transport: req.Transport,
		AVType: req.AVType, StreamType: req.StreamType, Storage: req.Storage,
		Mode: req.Mode, Speed: req.Speed, Start: start, End: end,
	})
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	h.logSuccess(r, "jt808.playback", "jt808", req.Key, "JT808 0x9201 sent", map[string]any{"stream_id": id})
	writeJSON(w, http.StatusOK, map[string]any{"stream_id": id})
}

func (h *Handler) handleJT808PlaybackControl(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key     string `json:"key"`
		Channel byte   `json:"channel"`
		Control byte   `json:"control"`
		Speed   byte   `json:"speed"`
		DragTo  string `json:"drag_to"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	var drag time.Time
	if strings.TrimSpace(req.DragTo) != "" {
		var ok bool
		drag, ok = parseJT808TimeField(w, req.DragTo)
		if !ok {
			return
		}
	}
	err := h.jt808Server.PlaybackControl(jt808.PlaybackControlInput{
		Key: req.Key, Channel: req.Channel, Control: req.Control, Speed: req.Speed, DragTo: drag,
	})
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJT808Resources(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key        string `json:"key"`
		Channel    byte   `json:"channel"`
		Start      string `json:"start"`
		End        string `json:"end"`
		AVType     byte   `json:"av_type"`
		StreamType byte   `json:"stream_type"`
		Storage    byte   `json:"storage"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	start, ok := parseJT808TimeField(w, req.Start)
	if !ok {
		return
	}
	end, ok := parseJT808TimeField(w, req.End)
	if !ok {
		return
	}
	items, err := h.jt808Server.QueryResources(jt808.ResourceQuery{
		Key: req.Key, Channel: req.Channel, Start: start, End: end,
		AVType: req.AVType, StreamType: req.StreamType, Storage: req.Storage,
	})
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": items})
}

func (h *Handler) handleJT808Upload(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key        string `json:"key"`
		Channel    byte   `json:"channel"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
		User       string `json:"user"`
		Password   string `json:"password"`
		Path       string `json:"path"`
		Start      string `json:"start"`
		End        string `json:"end"`
		AVType     byte   `json:"av_type"`
		StreamType byte   `json:"stream_type"`
		Storage    byte   `json:"storage"`
		Condition  byte   `json:"condition"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	start, ok := parseJT808TimeField(w, req.Start)
	if !ok {
		return
	}
	end, ok := parseJT808TimeField(w, req.End)
	if !ok {
		return
	}
	seq, err := h.jt808Server.Upload(jt808.UploadInput{
		Key: req.Key, Channel: req.Channel, Host: req.Host, Port: req.Port,
		User: req.User, Password: req.Password, Path: req.Path,
		Start: start, End: end, AVType: req.AVType, StreamType: req.StreamType,
		Storage: req.Storage, Condition: req.Condition,
	})
	if err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"serial": seq})
}

func (h *Handler) handleJT808UploadControl(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key     string `json:"key"`
		Serial  uint16 `json:"serial"`
		Control byte   `json:"control"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	if err := h.jt808Server.UploadControl(jt808.UploadControlInput{Key: req.Key, Serial: req.Serial, Control: req.Control}); err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJT808PTZ(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	var req struct {
		Key     string `json:"key"`
		Channel byte   `json:"channel"`
		Action  string `json:"action"`
		Direct  byte   `json:"direct"`
		Speed   byte   `json:"speed"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	if err := h.jt808Server.PTZ(jt808.PTZInput{
		Key: req.Key, Channel: req.Channel, Action: req.Action, Direct: req.Direct, Speed: req.Speed,
	}); err != nil {
		writeJT808Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleJT808Downlink(w http.ResponseWriter, r *http.Request) {
	if !h.jt808Ready(w) {
		return
	}
	pusher, ok := h.mediaEngine.(jt1078AudioPusher)
	if !ok || pusher == nil {
		writeError(w, http.StatusServiceUnavailable, "JT1078 downlink not available")
		return
	}
	var req struct {
		Key     string `json:"key"`
		Channel byte   `json:"channel"`
		PT      byte   `json:"pt"`
		Payload string `json:"payload"`
	}
	if !decodeJT808(w, r, &req) || !requireJT808Key(w, req.Key) {
		return
	}
	if req.Channel == 0 {
		req.Channel = 1
	}
	if req.PT == 0 {
		req.PT = 6
	}
	raw, err := base64.StdEncoding.DecodeString(req.Payload)
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "payload must be base64 audio")
		return
	}
	if err := pusher.PushJT1078Audio(req.Key, req.Channel, req.PT, raw); err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "empty") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) jt808Ready(w http.ResponseWriter) bool {
	if h.jt808Server == nil {
		writeError(w, http.StatusServiceUnavailable, "JT808 server not available")
		return false
	}
	return true
}

func decodeJT808(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func requireJT808Key(w http.ResponseWriter, key string) bool {
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return false
	}
	return true
}

func writeJT808Err(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, jt808.ErrBadInput) {
		status = http.StatusBadRequest
	}
	writeError(w, status, err.Error())
}

func parseJT808TimeField(w http.ResponseWriter, raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		writeError(w, http.StatusBadRequest, "start and end are required")
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true
		}
	}
	writeError(w, http.StatusBadRequest, "invalid time")
	return time.Time{}, false
}
