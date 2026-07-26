package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

// handleGB28181ListDevices returns all registered GB28181 devices.
func (h *Handler) handleGB28181ListDevices(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeJSON(w, http.StatusOK, map[string]any{"devices": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": h.gb28181Server.ListDevices()})
}

func (h *Handler) handleGB28181Play(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
		StreamID  string `json:"stream_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	streamID := req.StreamID
	if streamID == "" {
		streamID = gb28181.StreamID(req.DeviceID, req.ChannelID)
	}
	ssrc, err := h.gb28181Server.Play(&gb28181.PlayInput{
		DeviceID:   req.DeviceID,
		ChannelID:  req.ChannelID,
		StreamMode: 0,
		InternalID: streamID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.ensureGB28181Camera(r.Context(), req.DeviceID, req.ChannelID, streamID); err != nil {
		slog.Warn("GB28181 play succeeded but camera registration failed",
			"device_id", req.DeviceID,
			"channel_id", req.ChannelID,
			"stream_id", streamID,
			"error", err,
		)
	}
	h.logSuccess(r, "gb28181.play", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 live play started", map[string]any{"stream_id": streamID})
	writeJSON(w, http.StatusOK, map[string]any{"ssrc": ssrc, "stream_id": streamID})
}

func (h *Handler) ensureGB28181Camera(ctx context.Context, deviceID, channelID, streamID string) error {
	if h.db == nil {
		return fmt.Errorf("database unavailable")
	}
	if h.camMgr != nil {
		if cam := h.camMgr.GetCameraConfig(streamID); cam != nil && cam.Enabled {
			return nil
		}
	}
	existing, err := h.db.GetCamera(ctx, streamID)
	if err != nil {
		return err
	}
	if existing != nil && !existing.Archived {
		return nil
	}

	cameraURL := ""
	if h.mediaEngine != nil {
		playURL, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
			StreamID: streamID,
			AppName:  "live",
			Protocol: "rtsp",
		})
		if err == nil && playURL != nil && playURL.URL != "" {
			cameraURL = playURL.URL
		}
	}
	if cameraURL == "" {
		cameraURL = fmt.Sprintf("rtsp://127.0.0.1:5544/live/%s", streamID)
	}
	if existing != nil && existing.Archived {
		if err := h.db.UnarchiveCameraDB(ctx, streamID); err != nil {
			return err
		}
	}
	return h.db.UpsertCamera(ctx, streamID, fmt.Sprintf("GB28181 %s", channelID), "gb28181", "h264", cameraURL, "", "", true, "", "", "")
}

func (h *Handler) handleGB28181StopPlay(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	if err := h.gb28181Server.StopPlay(&gb28181.StopPlayInput{DeviceID: req.DeviceID, ChannelID: req.ChannelID}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.stop", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 live play stopped", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181RecordInfo(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	startTime, err := parseTime(req.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid start_time")
		return
	}
	endTime, err := parseTime(req.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid end_time")
		return
	}
	records, err := h.gb28181Server.QueryRecordInfo(req.DeviceID, req.ChannelID, startTime, endTime)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (h *Handler) handleGB28181Playback(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
		StreamID  string `json:"stream_id,omitempty"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	startTime, err := parseTime(req.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid start_time")
		return
	}
	endTime, err := parseTime(req.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid end_time")
		return
	}
	streamID := req.StreamID
	if streamID == "" {
		streamID = gb28181.StreamID(req.DeviceID, req.ChannelID) + "_pb"
	}
	ssrc, err := h.gb28181Server.Playback(&gb28181.PlaybackInput{
		DeviceID:   req.DeviceID,
		ChannelID:  req.ChannelID,
		StreamMode: 0,
		InternalID: streamID,
		StartTime:  startTime,
		EndTime:    endTime,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.playback", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 playback started", map[string]any{"stream_id": streamID})
	writeJSON(w, http.StatusOK, map[string]any{
		"ssrc":      ssrc,
		"stream_id": streamID,
		"urls":      h.buildGB28181PlayURLs(r.Context(), streamID, "rtp"),
	})
}

func (h *Handler) buildGB28181PlayURLs(ctx context.Context, streamID, appName string) []map[string]string {
	if h.mediaEngine == nil {
		return nil
	}
	protocols := []string{"ws-flv", "flv", "hls", "ll-hls", "webrtc", "fmp4", "rtmp", "rtsp"}
	urls := make([]map[string]string, 0, len(protocols))
	for _, protocol := range protocols {
		playURL, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
			StreamID: streamID,
			AppName:  appName,
			Protocol: protocol,
		})
		if err != nil || playURL == nil || playURL.URL == "" {
			continue
		}
		urls = append(urls, map[string]string{
			"protocol": protocol,
			"url":      playURL.URL,
		})
	}
	return urls
}

func (h *Handler) handleGB28181PlaySpeed(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string  `json:"device_id"`
		ChannelID string  `json:"channel_id"`
		Speed     float32 `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	if req.Speed != 0.5 && req.Speed != 1 && req.Speed != 2 && req.Speed != 4 {
		writeError(w, http.StatusBadRequest, "speed must be 0.5, 1, 2, or 4")
		return
	}
	if err := h.gb28181Server.PlaySpeed(&gb28181.PlaySpeedInput{DeviceID: req.DeviceID, ChannelID: req.ChannelID, Speed: req.Speed}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.playback.speed", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 playback speed changed", map[string]any{"speed": req.Speed})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181PlaySeek(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
		SeekTime  int64  `json:"seek_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	if err := h.gb28181Server.PlaySeek(&gb28181.PlaySeekInput{DeviceID: req.DeviceID, ChannelID: req.ChannelID, SeekTime: req.SeekTime}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.playback.seek", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 playback seek", map[string]any{"seek_time": req.SeekTime})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181PlayPause(w http.ResponseWriter, r *http.Request) {
	h.handleGB28181PlayPauseState(w, r, false)
}

func (h *Handler) handleGB28181PlayResume(w http.ResponseWriter, r *http.Request) {
	h.handleGB28181PlayPauseState(w, r, true)
}

func (h *Handler) handleGB28181PlayPauseState(w http.ResponseWriter, r *http.Request, resume bool) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	in := &gb28181.PlayPauseInput{DeviceID: req.DeviceID, ChannelID: req.ChannelID}
	var err error
	if resume {
		err = h.gb28181Server.PlayResume(in)
	} else {
		err = h.gb28181Server.PlayPause(in)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	action := "gb28181.playback.pause"
	message := "GB28181 playback paused"
	if resume {
		action = "gb28181.playback.resume"
		message = "GB28181 playback resumed"
	}
	h.logSuccess(r, action, "gb28181", req.DeviceID+"/"+req.ChannelID, message, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) gb28181DeviceVersion(ctx context.Context, deviceID string) gb28181.GBVersion {
	if h.db != nil {
		if row, err := h.db.GetGB28181Device(ctx, deviceID); err == nil && row != nil {
			if version := gb28181.NormalizeGBVersion(row.GBVersion); version != gb28181.GBVersionUnknown {
				return version
			}
		}
	}
	if h.config != nil {
		if version := gb28181.NormalizeGBVersion(h.config.GB28181.StandardVersion); version != gb28181.GBVersionUnknown {
			return version
		}
	}
	return gb28181.GBVersion2016
}

func buildGB28181PTZCommand(direction string, speed float64) string {
	if direction == "stop" {
		return gb28181.BuildStop()
	}
	return gb28181.BuildContinuousMove(direction, speed)
}

func (h *Handler) sendGB28181PTZControl(ctx context.Context, deviceID, channelID, direction string, speed float64) error {
	if h.gb28181Server == nil {
		return fmt.Errorf("GB28181 server not available")
	}
	switch h.gb28181DeviceVersion(ctx, deviceID) {
	case gb28181.GBVersion2022:
		return h.sendGB28181PTZControl2022(deviceID, channelID, direction, speed)
	default:
		return h.sendGB28181PTZControl2016(deviceID, channelID, direction, speed)
	}
}

func (h *Handler) sendGB28181PTZControl2016(deviceID, channelID, direction string, speed float64) error {
	ptzCmd := buildGB28181PTZCommand(direction, speed)
	if ptzCmd == "" {
		return fmt.Errorf("invalid direction")
	}
	return h.gb28181Server.PTZControl(deviceID, channelID, ptzCmd)
}

func (h *Handler) sendGB28181PTZControl2022(deviceID, channelID, direction string, speed float64) error {
	ptzCmd := buildGB28181PTZCommand(direction, speed)
	if ptzCmd == "" {
		return fmt.Errorf("invalid direction")
	}
	return h.gb28181Server.PTZControl(deviceID, channelID, ptzCmd)
}

func (h *Handler) sendGB28181RecordControl(ctx context.Context, deviceID, channelID, command string) error {
	if h.gb28181Server == nil {
		return fmt.Errorf("GB28181 server not available")
	}
	switch h.gb28181DeviceVersion(ctx, deviceID) {
	case gb28181.GBVersion2022:
		return h.sendGB28181RecordControl2022(deviceID, channelID, command)
	default:
		return h.sendGB28181RecordControl2016(deviceID, channelID, command)
	}
}

func (h *Handler) sendGB28181RecordControl2016(deviceID, channelID, command string) error {
	return h.gb28181Server.RecordControl(deviceID, channelID, command)
}

func (h *Handler) sendGB28181RecordControl2022(deviceID, channelID, command string) error {
	return h.gb28181Server.RecordControl(deviceID, channelID, command)
}

func (h *Handler) handleGB28181ListPlatforms(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeJSON(w, http.StatusOK, map[string]any{"platforms": []any{}})
		return
	}
	pm := h.gb28181Server.GetPlatforms()
	if pm == nil {
		writeJSON(w, http.StatusOK, map[string]any{"platforms": []any{}})
		return
	}
	platforms := pm.ListPlatforms()
	result := make([]map[string]any, 0, len(platforms))
	for _, p := range platforms {
		result = append(result, map[string]any{
			"id":           p.Config.ID,
			"name":         p.Config.Name,
			"enable":       p.Config.Enable,
			"server_gb_id": p.Config.ServerGBID,
			"server_ip":    p.Config.ServerIP,
			"server_port":  p.Config.ServerPort,
			"device_gb_id": p.Config.DeviceGBID,
			"device_ip":    p.Config.DeviceIP,
			"device_port":  p.Config.DevicePort,
			"transport":    p.Config.Transport,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"platforms": result})
}

func (h *Handler) handleGB28181AddPlatform(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		Name            string `json:"name"`
		Enable          bool   `json:"enable"`
		ServerGBID      string `json:"server_gb_id"`
		ServerGBDomain  string `json:"server_gb_domain"`
		ServerIP        string `json:"server_ip"`
		ServerPort      int    `json:"server_port"`
		DeviceGBID      string `json:"device_gb_id"`
		DeviceGBDomain  string `json:"device_gb_domain"`
		DeviceIP        string `json:"device_ip"`
		DevicePort      int    `json:"device_port"`
		Username        string `json:"username"`
		Password        string `json:"password"`
		Transport       string `json:"transport"`
		CharacterSet    string `json:"character_set"`
		Expires         int    `json:"expires"`
		KeepTimeout     int    `json:"keep_timeout"`
		MaxTimeoutCount int    `json:"max_timeout_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ServerGBID == "" || req.ServerIP == "" {
		writeError(w, http.StatusBadRequest, "server_gb_id and server_ip are required")
		return
	}
	if req.DeviceGBID == "" {
		req.DeviceGBID = h.gb28181Server.GetConfig().ID
	}
	if req.DeviceIP == "" {
		req.DeviceIP = h.gb28181Server.GetConfig().MediaIP
	}
	if req.DevicePort == 0 {
		req.DevicePort = h.gb28181Server.GetConfig().Port
	}
	if req.Expires == 0 {
		req.Expires = 3600
	}
	if req.KeepTimeout == 0 {
		req.KeepTimeout = 60
	}
	if req.MaxTimeoutCount == 0 {
		req.MaxTimeoutCount = 3
	}
	if req.Transport == "" {
		req.Transport = "UDP"
	}
	pm := h.gb28181Server.GetPlatforms()
	if pm == nil {
		writeError(w, http.StatusInternalServerError, "platform manager not initialized")
		return
	}
	cfg := &gb28181.PlatformConfig{
		Name:            req.Name,
		Enable:          req.Enable,
		ServerGBID:      req.ServerGBID,
		ServerGBDomain:  req.ServerGBDomain,
		ServerIP:        req.ServerIP,
		ServerPort:      req.ServerPort,
		DeviceGBID:      req.DeviceGBID,
		DeviceGBDomain:  req.DeviceGBDomain,
		DeviceIP:        req.DeviceIP,
		DevicePort:      req.DevicePort,
		Username:        req.Username,
		Password:        req.Password,
		Transport:       req.Transport,
		CharacterSet:    req.CharacterSet,
		Expires:         req.Expires,
		KeepTimeout:     req.KeepTimeout,
		MaxTimeoutCount: req.MaxTimeoutCount,
	}
	if err := pm.AddPlatform(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.platform.create", "gb28181", strconv.FormatInt(cfg.ID, 10), "GB28181 platform added", map[string]any{"name": cfg.Name})
	writeJSON(w, http.StatusOK, map[string]any{"id": cfg.ID, "status": "ok"})
}

func (h *Handler) handleGB28181DeletePlatform(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	pm := h.gb28181Server.GetPlatforms()
	if pm == nil {
		writeError(w, http.StatusInternalServerError, "platform manager not initialized")
		return
	}
	if err := pm.RemovePlatform(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.platform.delete", "gb28181", strconv.FormatInt(id, 10), "GB28181 platform deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181StartBroadcast(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	bm := h.gb28181Server.GetBroadcastManager()
	if bm == nil {
		writeError(w, http.StatusInternalServerError, "broadcast manager not initialized")
		return
	}
	bs, err := bm.StartBroadcast(req.DeviceID, req.ChannelID, h.gb28181Server.GetDeviceStore())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.broadcast.start", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 broadcast started", nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": bs.DeviceID + "_" + bs.ChannelID,
		"port":       bs.RTPPort,
		"ssrc":       bs.SSRC,
	})
}

func (h *Handler) handleGB28181StopBroadcast(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	bm := h.gb28181Server.GetBroadcastManager()
	if bm == nil {
		writeError(w, http.StatusInternalServerError, "broadcast manager not initialized")
		return
	}
	if err := bm.StopBroadcast(req.DeviceID, req.ChannelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.broadcast.stop", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 broadcast stopped", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181ListPlatformEvents(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}, "total": 0})
		return
	}
	var platformID int64
	if raw := r.URL.Query().Get("platform_id"); raw != "" {
		platformID, _ = strconv.ParseInt(raw, 10, 64)
	}
	eventType := r.URL.Query().Get("event_type")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	events, total, err := h.db.ListPlatformEvents(r.Context(), platformID, eventType, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total})
}

func (h *Handler) handleGB28181GetPlatformStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"platforms": []any{}})
		return
	}
	statuses, err := h.db.GetPlatformStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"platforms": statuses})
}

func (h *Handler) handleGB28181ListAlarms(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"alarms": []any{}, "total": 0})
		return
	}
	alarms, total, err := h.db.ListAlarms(r.Context(), r.URL.Query().Get("device_id"), queryInt(r, "limit", 50), queryInt(r, "offset", 0))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if alarms == nil {
		writeJSON(w, http.StatusOK, map[string]any{"alarms": []any{}, "total": total})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alarms": alarms, "total": total})
}

func (h *Handler) handleGB28181StartDownload(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	startTime, err := parseTime(req.StartTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid start_time")
		return
	}
	endTime, err := parseTime(req.EndTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid end_time")
		return
	}
	dm := h.gb28181Server.GetDownloadManager()
	if dm == nil {
		writeError(w, http.StatusInternalServerError, "download manager not initialized")
		return
	}
	ds, err := dm.StartDownload(req.DeviceID, req.ChannelID, startTime, endTime, h.gb28181Server.GetDeviceStore())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.download.start", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 download started", map[string]any{"download_id": ds.ID})
	writeJSON(w, http.StatusOK, map[string]any{"download_id": ds.ID, "file_path": ds.FilePath, "status": ds.Status})
}

func (h *Handler) handleGB28181StopDownload(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID   string `json:"device_id"`
		ChannelID  string `json:"channel_id"`
		DownloadID int64  `json:"download_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dm := h.gb28181Server.GetDownloadManager()
	if dm == nil {
		writeError(w, http.StatusInternalServerError, "download manager not initialized")
		return
	}
	if err := dm.StopDownload(req.DeviceID, req.ChannelID, req.DownloadID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logSuccess(r, "gb28181.download.stop", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 download stopped", map[string]any{"download_id": req.DownloadID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleGB28181BatchDownload(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		ChannelID string `json:"channel_id"`
		Segments  []struct {
			StartTime string `json:"start_time"`
			EndTime   string `json:"end_time"`
		} `json:"segments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" || len(req.Segments) == 0 {
		writeError(w, http.StatusBadRequest, "device_id, channel_id and segments are required")
		return
	}
	dm := h.gb28181Server.GetDownloadManager()
	if dm == nil {
		writeError(w, http.StatusInternalServerError, "download manager not initialized")
		return
	}
	results := make([]map[string]any, 0, len(req.Segments))
	errors := make([]string, 0)
	for i, seg := range req.Segments {
		startTime, err := parseTime(seg.StartTime)
		if err != nil {
			errors = append(errors, fmt.Sprintf("segment %d: invalid start_time", i))
			continue
		}
		endTime, err := parseTime(seg.EndTime)
		if err != nil {
			errors = append(errors, fmt.Sprintf("segment %d: invalid end_time", i))
			continue
		}
		ds, err := dm.StartDownload(req.DeviceID, req.ChannelID, startTime, endTime, h.gb28181Server.GetDeviceStore())
		if err != nil {
			errors = append(errors, fmt.Sprintf("segment %d: %v", i, err))
			continue
		}
		results = append(results, map[string]any{
			"download_id": ds.ID,
			"file_path":   ds.FilePath,
			"status":      ds.Status,
			"start_time":  seg.StartTime,
			"end_time":    seg.EndTime,
		})
	}
	h.logSuccess(r, "gb28181.download.batch", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 batch download started", map[string]any{"count": len(results)})
	writeJSON(w, http.StatusOK, map[string]any{"downloads": results, "errors": errors, "total": len(results)})
}

func (h *Handler) handleGB28181ListDownloads(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"downloads": []any{}, "total": 0})
		return
	}
	downloads, total, err := h.db.ListDownloads(
		r.Context(),
		r.URL.Query().Get("device_id"),
		r.URL.Query().Get("channel_id"),
		queryInt(r, "limit", 50),
		queryInt(r, "offset", 0),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if downloads == nil {
		writeJSON(w, http.StatusOK, map[string]any{"downloads": []any{}, "total": total})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloads": downloads, "total": total})
}

func (h *Handler) handleGB28181TalkWS(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	channelID := r.URL.Query().Get("channel_id")
	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	transportMode := gb28181.TransportUDP
	switch r.URL.Query().Get("transport") {
	case "1":
		transportMode = gb28181.TransportTCPPassive
	case "2":
		transportMode = gb28181.TransportTCPActive
	}
	tm := h.gb28181Server.GetTalkManager()
	if tm == nil {
		writeError(w, http.StatusInternalServerError, "talk manager not initialized")
		return
	}
	session, exists := tm.GetSession(deviceID, channelID)
	if !exists {
		var err error
		session, err = tm.StartTalk(deviceID, channelID, transportMode, h.gb28181Server.GetDeviceStore())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = tm.StopTalk(deviceID, channelID)
		return
	}
	defer conn.Close()
	defer func() { _ = tm.StopTalk(deviceID, channelID) }()
	if err := conn.WriteJSON(map[string]any{
		"status":     "connected",
		"session_id": deviceID + "_" + channelID,
		"port":       session.RTPPort,
		"transport":  transportMode,
	}); err != nil {
		return
	}
	select {
	case <-session.ReadyCh:
	case <-time.After(30 * time.Second):
		_ = conn.WriteJSON(map[string]string{"error": "session timeout"})
		return
	}
	h.logSuccess(r, "talk.start", "gb28181", deviceID+"/"+channelID, "GB28181 talk websocket connected", nil)
	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("GB28181 talk websocket read error", "error", err)
			}
			break
		}
		if messageType == websocket.BinaryMessage {
			if err := session.SendAudioData(message); err != nil {
				slog.Error("failed to send GB28181 talk audio data", "error", err)
			}
		}
	}
}

func (h *Handler) handleGB28181StartTalkWhip(w http.ResponseWriter, r *http.Request) {
	h.handleGB28181TalkWhipState(w, r, true)
}

func (h *Handler) handleGB28181StopTalkWhip(w http.ResponseWriter, r *http.Request) {
	h.handleGB28181TalkWhipState(w, r, false)
}

func (h *Handler) handleGB28181TalkWhipState(w http.ResponseWriter, r *http.Request, start bool) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}
	var req struct {
		StreamName string `json:"stream_name"`
		DeviceID   string `json:"device_id"`
		ChannelID  string `json:"channel_id"`
		Transport  string `json:"transport,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}
	tm := h.gb28181Server.GetTalkManager()
	if tm == nil {
		writeError(w, http.StatusInternalServerError, "talk manager not initialized")
		return
	}
	if !start {
		if err := tm.StopTalk(req.DeviceID, req.ChannelID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.logSuccess(r, "talk.stop", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 talk stopped", nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
		return
	}
	transportMode := gb28181.TransportUDP
	switch req.Transport {
	case "1":
		transportMode = gb28181.TransportTCPPassive
	case "2":
		transportMode = gb28181.TransportTCPActive
	}
	session, exists := tm.GetSession(req.DeviceID, req.ChannelID)
	if !exists {
		var err error
		session, err = tm.StartTalk(req.DeviceID, req.ChannelID, transportMode, h.gb28181Server.GetDeviceStore())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.logSuccess(r, "talk.start", "gb28181", req.DeviceID+"/"+req.ChannelID, "GB28181 talk started", nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "started",
		"session_id":  req.DeviceID + "_" + req.ChannelID,
		"port":        session.RTPPort,
		"transport":   transportMode,
		"stream_name": req.StreamName,
	})
}

func queryInt(r *http.Request, key string, fallback int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// handleGB28181PTZControl 处理PTZ控制请求
func (h *Handler) handleGB28181PTZControl(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	var body struct {
		Direction string  `json:"direction"`
		Speed     float64 `json:"speed"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.sendGB28181PTZControl(r.Context(), deviceID, channelID, body.Direction, body.Speed); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.ptz.control", "gb28181", deviceID+"/"+channelID, "GB28181 PTZ control", map[string]any{"direction": body.Direction})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181PTZPositionControl 处理PTZ精准位置控制请求
func (h *Handler) handleGB28181PTZPositionControl(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	var body struct {
		HorizontalAngle float64 `json:"horizontal_angle"`
		VerticalAngle   float64 `json:"vertical_angle"`
		ZoomLevel       float64 `json:"zoom_level"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.gb28181Server.PTZPositionControl(deviceID, channelID, body.HorizontalAngle, body.VerticalAngle, body.ZoomLevel); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.ptz.position", "gb28181", deviceID+"/"+channelID, "GB28181 PTZ position control", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181PTZQueryPosition 处理PTZ位置查询请求
func (h *Handler) handleGB28181PTZQueryPosition(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	if err := h.gb28181Server.QueryPTZPosition(deviceID, channelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181PresetSet 处理设置预置位请求
func (h *Handler) handleGB28181PresetSet(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")
	presetIDStr := chi.URLParam(r, "presetID")

	if deviceID == "" || channelID == "" || presetIDStr == "" {
		writeError(w, http.StatusBadRequest, "deviceID, channelID and presetID are required")
		return
	}

	presetID, err := strconv.Atoi(presetIDStr)
	if err != nil || presetID < 1 || presetID > 255 {
		writeError(w, http.StatusBadRequest, "presetID must be between 1 and 255")
		return
	}

	if err := h.gb28181Server.SetPreset(deviceID, channelID, presetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.preset.set", "gb28181", deviceID+"/"+channelID, "GB28181 preset set", map[string]any{"preset_id": presetID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181PresetCall 处理调用预置位请求
func (h *Handler) handleGB28181PresetCall(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")
	presetIDStr := chi.URLParam(r, "presetID")

	if deviceID == "" || channelID == "" || presetIDStr == "" {
		writeError(w, http.StatusBadRequest, "deviceID, channelID and presetID are required")
		return
	}

	presetID, err := strconv.Atoi(presetIDStr)
	if err != nil || presetID < 1 || presetID > 255 {
		writeError(w, http.StatusBadRequest, "presetID must be between 1 and 255")
		return
	}

	if err := h.gb28181Server.CallPreset(deviceID, channelID, presetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.preset.call", "gb28181", deviceID+"/"+channelID, "GB28181 preset called", map[string]any{"preset_id": presetID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181PresetDelete 处理删除预置位请求
func (h *Handler) handleGB28181PresetDelete(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")
	presetIDStr := chi.URLParam(r, "presetID")

	if deviceID == "" || channelID == "" || presetIDStr == "" {
		writeError(w, http.StatusBadRequest, "deviceID, channelID and presetID are required")
		return
	}

	presetID, err := strconv.Atoi(presetIDStr)
	if err != nil || presetID < 1 || presetID > 255 {
		writeError(w, http.StatusBadRequest, "presetID must be between 1 and 255")
		return
	}

	if err := h.gb28181Server.DeletePreset(deviceID, channelID, presetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.preset.delete", "gb28181", deviceID+"/"+channelID, "GB28181 preset deleted", map[string]any{"preset_id": presetID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181CruiseAddPoint 处理加入巡航点请求
func (h *Handler) handleGB28181CruiseAddPoint(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	var body struct {
		CruiseID int `json:"cruise_id"`
		PresetID int `json:"preset_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.CruiseID < 0 || body.CruiseID > 255 {
		writeError(w, http.StatusBadRequest, "cruise_id must be between 0 and 255")
		return
	}
	if body.PresetID < 1 || body.PresetID > 255 {
		writeError(w, http.StatusBadRequest, "preset_id must be between 1 and 255")
		return
	}

	if err := h.gb28181Server.CruiseAddPoint(deviceID, channelID, body.CruiseID, body.PresetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.cruise.add_point", "gb28181", deviceID+"/"+channelID, "GB28181 cruise point added", map[string]any{"cruise_id": body.CruiseID, "preset_id": body.PresetID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181CruiseDeletePoint 处理删除巡航点请求
func (h *Handler) handleGB28181CruiseDeletePoint(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	var body struct {
		CruiseID int `json:"cruise_id"`
		PresetID int `json:"preset_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.CruiseID < 0 || body.CruiseID > 255 {
		writeError(w, http.StatusBadRequest, "cruise_id must be between 0 and 255")
		return
	}
	if body.PresetID < 0 || body.PresetID > 255 {
		writeError(w, http.StatusBadRequest, "preset_id must be between 0 and 255")
		return
	}

	if err := h.gb28181Server.CruiseDeletePoint(deviceID, channelID, body.CruiseID, body.PresetID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.cruise.delete_point", "gb28181", deviceID+"/"+channelID, "GB28181 cruise point deleted", map[string]any{"cruise_id": body.CruiseID, "preset_id": body.PresetID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181CruiseStart 处理开始巡航请求
func (h *Handler) handleGB28181CruiseStart(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")
	cruiseIDStr := chi.URLParam(r, "cruiseID")

	if deviceID == "" || channelID == "" || cruiseIDStr == "" {
		writeError(w, http.StatusBadRequest, "deviceID, channelID and cruiseID are required")
		return
	}

	cruiseID, err := strconv.Atoi(cruiseIDStr)
	if err != nil || cruiseID < 0 || cruiseID > 255 {
		writeError(w, http.StatusBadRequest, "cruiseID must be between 0 and 255")
		return
	}

	if err := h.gb28181Server.CruiseStart(deviceID, channelID, cruiseID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.cruise.start", "gb28181", deviceID+"/"+channelID, "GB28181 cruise started", map[string]any{"cruise_id": cruiseID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181CruiseStop 处理停止巡航请求
func (h *Handler) handleGB28181CruiseStop(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")
	cruiseIDStr := chi.URLParam(r, "cruiseID")

	if deviceID == "" || channelID == "" || cruiseIDStr == "" {
		writeError(w, http.StatusBadRequest, "deviceID, channelID and cruiseID are required")
		return
	}

	cruiseID, err := strconv.Atoi(cruiseIDStr)
	if err != nil || cruiseID < 0 || cruiseID > 255 {
		writeError(w, http.StatusBadRequest, "cruiseID must be between 0 and 255")
		return
	}

	if err := h.gb28181Server.CruiseStop(deviceID, channelID, cruiseID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.cruise.stop", "gb28181", deviceID+"/"+channelID, "GB28181 cruise stopped", map[string]any{"cruise_id": cruiseID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181Snapshot 处理图像抓拍请求
func (h *Handler) handleGB28181Snapshot(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	if err := h.gb28181Server.Snapshot(deviceID, channelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "snapshot.take", "gb28181", deviceID+"/"+channelID, "GB28181 snapshot taken", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181DeviceReset 处理设备复位请求
func (h *Handler) handleGB28181DeviceReset(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	if err := h.gb28181Server.DeviceReset(deviceID, channelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.device.reset", "gb28181", deviceID+"/"+channelID, "GB28181 device reset", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181RecordControl 处理录像控制请求
func (h *Handler) handleGB28181RecordControl(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	var body struct {
		Command string `json:"command"` // "record" or "stop"
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Command != "record" && body.Command != "stop" {
		writeError(w, http.StatusBadRequest, "command must be 'record' or 'stop'")
		return
	}

	if err := h.sendGB28181RecordControl(r.Context(), deviceID, channelID, body.Command); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.record.control", "gb28181", deviceID+"/"+channelID, "GB28181 record control", map[string]any{"command": body.Command})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181HomePosition 处理看守位设置请求
func (h *Handler) handleGB28181HomePosition(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	var body struct {
		Enabled     int `json:"enabled"`      // 0=禁用, 1=启用
		ResetTime   int `json:"reset_time"`   // 自动归位时间(秒)
		PresetIndex int `json:"preset_index"` // 预置位编号(1-255)
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Enabled != 0 && body.Enabled != 1 {
		writeError(w, http.StatusBadRequest, "enabled must be 0 or 1")
		return
	}
	if body.PresetIndex < 1 || body.PresetIndex > 255 {
		writeError(w, http.StatusBadRequest, "preset_index must be between 1 and 255")
		return
	}

	if err := h.gb28181Server.SetHomePosition(deviceID, channelID, body.Enabled, body.ResetTime, body.PresetIndex); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.logSuccess(r, "gb28181.home_position", "gb28181", deviceID+"/"+channelID, "GB28181 home position updated", map[string]any{"enabled": body.Enabled})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181DeviceInfoQuery 处理设备信息查询请求
func (h *Handler) handleGB28181DeviceInfoQuery(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	if err := h.gb28181Server.DeviceInfoQuery(deviceID, channelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181DeviceStatusQuery 处理设备状态查询请求
func (h *Handler) handleGB28181DeviceStatusQuery(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	if err := h.gb28181Server.DeviceStatusQuery(deviceID, channelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGB28181DeviceConfigQuery 处理设备配置查询请求
func (h *Handler) handleGB28181DeviceConfigQuery(w http.ResponseWriter, r *http.Request) {
	if h.gb28181Server == nil {
		writeError(w, http.StatusServiceUnavailable, "GB28181 server not available")
		return
	}

	deviceID := chi.URLParam(r, "deviceID")
	channelID := chi.URLParam(r, "channelID")

	if deviceID == "" || channelID == "" {
		writeError(w, http.StatusBadRequest, "deviceID and channelID are required")
		return
	}

	if err := h.gb28181Server.DeviceConfigQuery(deviceID, channelID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// buildPTZCommand 构建PTZ命令
func buildPTZCommand(direction string, speed float64) string {
	// 使用gb28181包中的BuildContinuousMove函数
	return buildGB28181PTZCommand(direction, speed)
}
