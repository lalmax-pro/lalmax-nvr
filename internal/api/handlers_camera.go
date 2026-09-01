package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// --- Camera and stats endpoints ---

// cameraRowForAPI normalizes camera rows for API responses.
// For ONVIF cameras, it exposes onvif_endpoint as url so the frontend
// can use a single url field for all protocols.
func cameraRowForAPI(row *storage.CameraRow) {
	row.RTSPTransport = config.NormalizeRTSPTransport(row.RTSPTransport)
	if row.Protocol == "onvif" && row.URL == "" && row.ONVIFEndpoint != "" {
		row.URL = row.ONVIFEndpoint
	}
}

func (h *Handler) injectCameraConfigFields(row *storage.CameraRow) {
	if row == nil {
		return
	}
	if h.camMgr != nil {
		if cam := h.camMgr.GetCameraConfig(row.ID); cam != nil {
			row.AudioEnabled = cam.AudioEnabled
			row.SourceType = cam.SourceType
			row.SubStreamURL = cam.SubStreamURL
			row.SubnetHints = cam.SubnetHints
			row.Adaptive = cam.Adaptive
			if cam.SubProfileToken != "" {
				row.SubProfileToken = cam.SubProfileToken
			}
			if cam.ActivationState != "" {
				row.ActivationState = cam.ActivationState
			}
			if cam.StableID != "" {
				row.StableID = cam.StableID
			}
			return
		}
	}
	if h.config != nil {
		for _, cam := range h.config.Cameras {
			if cam.ID == row.ID {
				row.AudioEnabled = cam.AudioEnabled
				row.SourceType = cam.SourceType
				row.SubStreamURL = cam.SubStreamURL
				row.SubnetHints = cam.SubnetHints
				row.Adaptive = cam.Adaptive
				return
			}
		}
	}
}

func (h *Handler) resolveCameraSourceType(ctx context.Context, row *storage.CameraRow) {
	if row == nil || row.SourceType != "" {
		return
	}
	switch row.Protocol {
	case "rtmp_push", "srt_push", "whip_push", "relay_pull":
		row.SourceType = row.Protocol
		return
	case "rtmp-pull", "http-flv-pull":
		row.SourceType = "relay_pull"
		return
	}
	if h.db == nil {
		return
	}
	binding, err := h.db.GetBindingByCameraID(ctx, row.ID)
	if err != nil || binding == nil {
		return
	}
	if binding.StreamID != row.ID {
		return
	}
	histories, _, err := h.db.ListStreamHistory(ctx, row.ID, 5, 0)
	if err == nil {
		for _, hist := range histories {
			if src := pushSourceTypeFromProtocol(hist.Protocol); src != "" {
				row.SourceType = src
				return
			}
			if strings.EqualFold(hist.Protocol, "customize") {
				row.SourceType = inferCustomizePushSource(nil, hist.RemoteAddr)
				return
			}
		}
	}
	row.SourceType = "rtmp_push"
}

func (h *Handler) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cameras, err := h.db.ListCameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cameras")
		return
	}
	// The recordings page can request archived cameras as recording sources.
	// Normal camera/device management keeps the existing active-only default.
	if v := r.URL.Query().Get("include_archived"); v == "true" || v == "1" {
		archived, archiveErr := h.db.ListArchivedCameras(r.Context())
		if archiveErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to list archived cameras")
			return
		}
		cameras = append(cameras, archived...)
	}
	if cameras == nil {
		cameras = []storage.CameraRow{}
	}
	// Inject recorder status from CameraManager
	if h.camMgr != nil {
		statusMap := h.camMgr.Status()
		for i := range cameras {
			if s, ok := statusMap[cameras[i].ID]; ok {
				cameras[i].Status = s
			} else {
				cameras[i].Status = model.StatusStopped
			}
		}
		// Inject recording_paused flag
		for i := range cameras {
			cameras[i].RecordingPaused = h.camMgr.RecordingPaused(cameras[i].ID)
		}
		// Inject error details from CameraManager
		if h.camMgr != nil {
			for i := range cameras {
				if detail := h.camMgr.GetErrorDetail(cameras[i].ID); detail != nil {
					cameras[i].ErrorType = &detail.Type
					cameras[i].ErrorDetail = &detail.Message
				}
			}
		}
		// Inject last_seen from DB
		lastSeenMap, err := h.db.GetAllLastRecordingTimes(r.Context())
		if err == nil {
			for i := range cameras {
				if t, ok := lastSeenMap[cameras[i].ID]; ok {
					cameras[i].LastSeen = t
				}
			}
		}
		// Override status/codec for cameras backed by lalmax streams.
		// A relay can be active even while an incorrectly configured recorder is
		// reconnecting, so expose the live stream state and codec to the UI.
		if h.mediaEngine != nil {
			for i := range cameras {
				if cameras[i].Status == model.StatusRecording || cameras[i].Status == model.StatusReconnecting {
					streamInfo, err := h.mediaEngine.GetStream(r.Context(), cameras[i].ID)
					if err == nil && streamInfo != nil {
						if streamInfo.Active {
							cameras[i].Status = model.StatusRecording
						} else {
							cameras[i].Status = model.StatusOffline
						}
						if enc := encodingFromMediaCodec(streamInfo.VideoCodec); enc != "" {
							cameras[i].Encoding = enc
							if cameras[i].StreamEncoding == "" {
								cameras[i].StreamEncoding = strings.ToUpper(enc)
							}
						}
					}
				}
			}
		}
	}
	for i := range cameras {
		h.injectCameraConfigFields(&cameras[i])
		h.resolveCameraSourceType(r.Context(), &cameras[i])
		cameraRowForAPI(&cameras[i])
	}
	writeJSON(w, http.StatusOK, cameras)
}

// --- Camera CRUD endpoints ---

var validProtocols = map[string]bool{
	// New transport-only protocols
	"rtsp":  true,
	"http":  true,
	"onvif": true,
	// Plugin protocols
	"xiaomi": true,
	// Pull protocols (relay to lalmax)
	"rtmp-pull":     true,
	"http-flv-pull": true,
	// Legacy combined protocols (accepted, will be normalized)
	"rtsp_h264":  true,
	"rtsp_h265":  true,
	"rtsp_mjpeg": true,
	"http_jpeg":  true,
}

func (h *Handler) handleCreateCamera(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string                       `json:"name"`
		Protocol        string                       `json:"protocol"`
		URL             string                       `json:"url"`
		RTSPTransport   string                       `json:"rtsp_transport"`
		Username        string                       `json:"username"`
		Password        string                       `json:"password"`
		Enabled         *bool                        `json:"enabled"`
		Description     string                       `json:"description"`
		Location        string                       `json:"location"`
		Brand           string                       `json:"brand"`
		Model           string                       `json:"model"`
		SerialNumber    string                       `json:"serial_number"`
		ONVIFEndpoint   string                       `json:"onvif_endpoint"`
		ProfileToken    string                       `json:"profile_token"`
		ProfileName     string                       `json:"profile_name"`
		StreamEncoding  string                       `json:"stream_encoding"`
		Encoding        string                       `json:"encoding"`
		AudioEnabled    *bool                        `json:"audio_enabled"`
		SubStreamURL    string                       `json:"sub_stream_url"`
		SubProfileToken string                       `json:"sub_profile_token"`
		SubnetHints     []string                     `json:"subnet_hints"`
		RecordingMode   string                       `json:"recording_mode"`
		Adaptive        *config.CameraAdaptiveConfig `json:"adaptive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Protocol == "" {
		writeError(w, http.StatusBadRequest, "protocol is required")
		return
	}
	if !validProtocols[body.Protocol] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q, must be one of: rtsp, http, onvif, xiaomi", body.Protocol))
		return
	}
	if !config.IsValidRTSPTransport(body.RTSPTransport) {
		writeError(w, http.StatusBadRequest, "rtsp_transport must be tcp or udp")
		return
	}
	// ONVIF cameras: accept url OR onvif_endpoint
	if body.Protocol == "onvif" {
		endpoint := body.ONVIFEndpoint
		if endpoint == "" {
			endpoint = body.URL
		}
		if endpoint == "" {
			writeError(w, http.StatusBadRequest, "url or onvif_endpoint is required for ONVIF cameras")
			return
		}
		body.ONVIFEndpoint = endpoint
		body.URL = "" // Don't store in url field for ONVIF
		// Check for duplicate ONVIF endpoint
		if h.db != nil {
			existingCams, _ := h.db.ListCameras(r.Context())
			for _, ec := range existingCams {
				if ec.Protocol == "onvif" && ec.ONVIFEndpoint == body.ONVIFEndpoint {
					writeError(w, http.StatusConflict, "ONVIF camera with this endpoint already exists")
					return
				}
			}
		}
	} else if body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	// Validate URL format for non-ONVIF cameras
	if body.Protocol != "onvif" && !validateURL(body.URL) {
		writeError(w, http.StatusBadRequest, "invalid URL format")
		return
	}
	if body.RecordingMode != "" && !isValidRecordingMode(body.RecordingMode) {
		writeError(w, http.StatusBadRequest, "recording_mode must be continuous, scheduled, off, event, or adaptive")
		return
	}
	// Normalize protocol — handle legacy combined formats
	proto := body.Protocol
	enc := body.Encoding
	if strings.Contains(proto, "_") {
		parsedProto, parsedEnc, err := model.ParseLegacyProtocol(proto)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid protocol %q", proto))
			return
		}
		proto = parsedProto
		if enc == "" {
			enc = parsedEnc
		}
	}
	// Set default encoding if still empty
	if enc == "" {
		switch proto {
		case "rtsp":
			enc = "h264"
		case "http":
			enc = "jpeg"
		case "onvif":
			// Auto-detect encoding from ONVIF device profiles
			if body.StreamEncoding == "" {
				if detected := probeONVIFEncoding(r.Context(), body.ONVIFEndpoint, body.Username, body.Password); detected != "" {
					body.StreamEncoding = detected
					enc = strings.ToLower(detected)
					logger.Info("auto-detected ONVIF encoding", "camera", body.Name, "encoding", enc)
				}
			} else {
				enc = strings.ToLower(body.StreamEncoding)
			}
		}
	}

	cam := config.CameraConfig{
		Name:            body.Name,
		Protocol:        proto,
		Encoding:        enc,
		RTSPTransport:   config.NormalizeRTSPTransport(body.RTSPTransport),
		URL:             body.URL,
		Username:        body.Username,
		Password:        body.Password,
		ONVIFEndpoint:   body.ONVIFEndpoint,
		ProfileToken:    body.ProfileToken,
		StreamEncoding:  body.StreamEncoding,
		SubStreamURL:    body.SubStreamURL,
		SubProfileToken: body.SubProfileToken,
		SubnetHints:     body.SubnetHints,
		RecordingMode:   body.RecordingMode,
		Adaptive:        body.Adaptive,
	}
	if body.Enabled != nil {
		cam.Enabled = *body.Enabled
	} else {
		cam.Enabled = true
	}
	if body.AudioEnabled != nil {
		cam.AudioEnabled = *body.AudioEnabled
	} else {
		config.ApplyCameraAudioDefault(&cam)
	}

	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id, err := h.camMgr.AddCamera(r.Context(), cam)
	if err != nil {
		var cae *model.CameraAlreadyExistsError
		if errors.As(err, &cae) {
			writeAPIError(w, http.StatusConflict, err)
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to add camera: %v", err))
		}
		return
	}
	h.logOperation(r, "camera.create", "camera", id, "success", "camera created", map[string]any{"protocol": proto})
	// Persist DB-only metadata fields
	if body.Description != "" || body.Location != "" || body.Brand != "" || body.Model != "" || body.SerialNumber != "" {
		if err := h.db.UpdateCameraMetadata(r.Context(), id, body.Description, body.Location, body.Brand, body.Model, body.SerialNumber, 0); err != nil {
			logger.Warn("failed to set camera metadata", "camera_id", id, "error", err)
		}
	}
	// Persist profile_name for ONVIF cameras
	if body.ProfileName != "" {
		if err := h.db.UpdateCameraProfileName(r.Context(), id, body.ProfileName); err != nil {
			logger.Warn("failed to set profile name", "camera_id", id, "error", err)
		}
	}
	// Return CameraRow with status
	row, err := h.db.GetCamera(r.Context(), id)
	if row != nil {
		if h.camMgr != nil {
			row.Status = h.camMgr.CameraStatus(id)
		}
		// Inject last_seen from DB
		lastSeen, err := h.db.GetLastRecordingTime(r.Context(), id)
		if err == nil {
			row.LastSeen = lastSeen
		}
		h.injectCameraConfigFields(row)
		h.resolveCameraSourceType(r.Context(), row)
		cameraRowForAPI(row)
		writeJSON(w, http.StatusCreated, row)
	} else {
		cam.ID = id
		writeJSON(w, http.StatusCreated, cam)
	}
}

func (h *Handler) handleGetCamera(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	row, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	// Inject recorder status
	if h.camMgr != nil {
		row.Status = h.camMgr.CameraStatus(id)
		// Override status for cameras backed by lalmax streams that are idle
		if h.mediaEngine != nil && (row.Status == model.StatusRecording || row.Status == model.StatusReconnecting) {
			streamInfo, err := h.mediaEngine.GetStream(r.Context(), id)
			if err == nil && streamInfo != nil {
				if streamInfo.Active {
					row.Status = model.StatusRecording
				} else {
					row.Status = model.StatusOffline
				}
				if enc := encodingFromMediaCodec(streamInfo.VideoCodec); enc != "" {
					row.Encoding = enc
					if row.StreamEncoding == "" {
						row.StreamEncoding = strings.ToUpper(enc)
					}
				}
			}
		}
	}
	// For GB28181 cameras, check if the stream is active in lalmax
	if row.Protocol == "gb28181" && h.mediaEngine != nil {
		streamInfo, err := h.mediaEngine.GetStream(r.Context(), id)
		if err == nil && streamInfo != nil && streamInfo.Active {
			row.Status = model.StatusRecording
		} else if row.Status == model.StatusError {
			// If recorder doesn't exist but stream could be active, set to offline instead of error
			row.Status = model.StatusOffline
		}
	}
	// Inject last_seen from DB
	lastSeen, err := h.db.GetLastRecordingTime(r.Context(), id)
	if err == nil {
		row.LastSeen = lastSeen
	}
	h.injectCameraConfigFields(row)
	h.resolveCameraSourceType(r.Context(), row)
	cameraRowForAPI(row)
	writeJSON(w, http.StatusOK, row)
}

func encodingFromMediaCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "h264", "avc", "avc1":
		return string(model.FormatH264)
	case "h265", "hevc", "hev1", "hvc1":
		return string(model.FormatH265)
	case "mjpeg", "jpeg":
		return string(model.FormatMJPEG)
	default:
		return ""
	}
}

func (h *Handler) handleUpdateCamera(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}
	id := getCameraID(r)

	var body struct {
		Name            *string                      `json:"name"`
		URL             *string                      `json:"url"`
		Protocol        *string                      `json:"protocol"`
		Encoding        *string                      `json:"encoding"`
		RTSPTransport   *string                      `json:"rtsp_transport"`
		Username        *string                      `json:"username"`
		Password        *string                      `json:"password"`
		Enabled         *bool                        `json:"enabled"`
		Description     *string                      `json:"description"`
		Location        *string                      `json:"location"`
		Brand           *string                      `json:"brand"`
		Model           *string                      `json:"model"`
		SerialNumber    *string                      `json:"serial_number"`
		RetentionDays   *int                         `json:"retention_days"`
		ONVIFEndpoint   *string                      `json:"onvif_endpoint"`
		ProfileToken    *string                      `json:"profile_token"`
		StreamEncoding  *string                      `json:"stream_encoding"`
		AudioEnabled    *bool                        `json:"audio_enabled"`
		RecordingMode   *string                      `json:"recording_mode"`
		SubStreamURL    *string                      `json:"sub_stream_url"`
		SubProfileToken *string                      `json:"sub_profile_token"`
		SubnetHints     *[]string                    `json:"subnet_hints"`
		Adaptive        *config.CameraAdaptiveConfig `json:"adaptive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.RecordingMode != nil && !isValidRecordingMode(*body.RecordingMode) {
		writeError(w, http.StatusBadRequest, "recording_mode must be continuous, scheduled, off, event, or adaptive")
		return
	}

	// Harden credential updates: empty string from frontend means "don't update"
	username := body.Username
	if username != nil && *username == "" {
		username = nil
	}
	password := body.Password
	if password != nil && *password == "" {
		password = nil
	}

	updates := camera.CameraUpdate{
		Name:            body.Name,
		URL:             body.URL,
		Protocol:        body.Protocol,
		Encoding:        body.Encoding,
		RTSPTransport:   body.RTSPTransport,
		Username:        username,
		Password:        password,
		Enabled:         body.Enabled,
		Description:     body.Description,
		Location:        body.Location,
		Brand:           body.Brand,
		Model:           body.Model,
		SerialNumber:    body.SerialNumber,
		RetentionDays:   body.RetentionDays,
		ONVIFEndpoint:   body.ONVIFEndpoint,
		ProfileToken:    body.ProfileToken,
		StreamEncoding:  body.StreamEncoding,
		AudioEnabled:    body.AudioEnabled,
		RecordingMode:   body.RecordingMode,
		SubStreamURL:    body.SubStreamURL,
		SubProfileToken: body.SubProfileToken,
		SubnetHints:     body.SubnetHints,
		Adaptive:        body.Adaptive,
	}
	if body.RTSPTransport != nil && !config.IsValidRTSPTransport(*body.RTSPTransport) {
		writeError(w, http.StatusBadRequest, "rtsp_transport must be tcp or udp")
		return
	}

	// Validate URL format if URL is being updated
	if body.URL != nil && *body.URL != "" {
		if body.Protocol == nil || *body.Protocol != "onvif" {
			if !validateURL(*body.URL) {
				writeError(w, http.StatusBadRequest, "invalid URL format")
				return
			}
		}
	}

	// For ONVIF cameras, sync url and onvif_endpoint
	if body.Protocol != nil && *body.Protocol == "onvif" {
		if updates.URL != nil && *updates.URL != "" {
			updates.ONVIFEndpoint = updates.URL
			updates.URL = nil
		}
		if updates.ONVIFEndpoint != nil && *updates.ONVIFEndpoint != "" {
			updates.URL = updates.ONVIFEndpoint
		}
	}

	_, err := h.camMgr.UpdateCamera(r.Context(), id, updates)
	if err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update camera: %v", err))
		return
	}
	h.logOperation(r, "camera.update", "camera", id, "success", "camera configuration updated", nil)
	// Return updated CameraRow with status
	row, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if row != nil {
		if h.camMgr != nil {
			row.Status = h.camMgr.CameraStatus(id)
		}
		// Inject last_seen from DB
		lastSeen, err := h.db.GetLastRecordingTime(r.Context(), id)
		if err == nil {
			row.LastSeen = lastSeen
		}
		h.injectCameraConfigFields(row)
		cameraRowForAPI(row)
		writeJSON(w, http.StatusOK, row)
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	}
}

func (h *Handler) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	ctx := r.Context()

	cam, err := h.db.GetCamera(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if cam.Archived {
		writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
		return
	}

	if err := h.archiveCameraRecord(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive camera")
		return
	}

	h.logSuccess(r, "camera.delete", "camera", id, "camera archived", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "archived"})
}

// archiveCameraRecord stops recording and archives a camera in DB/config.
func (h *Handler) archiveCameraRecord(ctx context.Context, id string) error {
	if h.camMgr != nil {
		if h.camMgr.GetCameraConfig(id) != nil {
			if err := h.camMgr.ArchiveCamera(ctx, id); err != nil {
				logger.Warn("failed to archive camera via manager, archiving in DB", "camera_id", id, "error", err)
				if dbErr := h.db.ArchiveCameraDB(ctx, id); dbErr != nil {
					return dbErr
				}
				if _, recErr := h.db.ArchiveAllRecordings(ctx, id); recErr != nil {
					logger.Warn("failed to archive recordings", "camera_id", id, "error", recErr)
				}
			}
			return nil
		}
		logger.Info("archiving orphaned camera directly in DB", "camera_id", id)
	}

	if h.db == nil {
		return nil
	}
	if err := h.db.ArchiveCameraDB(ctx, id); err != nil {
		return err
	}
	if _, err := h.db.ArchiveAllRecordings(ctx, id); err != nil {
		logger.Warn("failed to archive recordings", "camera_id", id, "error", err)
	}
	return nil
}

func (h *Handler) handlePermanentDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	ctx := r.Context()

	cam, err := h.db.GetCamera(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	if err := h.permanentlyDeleteCamera(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to permanently delete camera")
		return
	}

	h.logSuccess(r, "camera.permanent_delete", "camera", id, "camera permanently deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) permanentlyDeleteCamera(ctx context.Context, cameraID string) error {
	if h.camMgr != nil && h.camMgr.GetCameraConfig(cameraID) != nil {
		if err := h.camMgr.RemoveCamera(ctx, cameraID); err != nil {
			return err
		}
	}

	if h.db != nil {
		if err := h.db.RemoveGroupChannelsByDeviceID(ctx, cameraID); err != nil {
			logger.Warn("failed to remove camera from groups", "camera_id", cameraID, "error", err)
		}
		if _, err := h.db.DeleteRecordingsByCamera(ctx, cameraID); err != nil {
			return err
		}
		if err := h.db.DeleteCamera(ctx, cameraID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}

	if h.store != nil {
		if err := h.store.DeleteCameraDir(cameraID); err != nil {
			logger.Warn("failed to remove camera directory", "camera_id", cameraID, "error", err)
		}
	}

	return nil
}

func (h *Handler) handleCameraRecordingStats(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	count, totalSize, err := h.db.GetCameraRecordingStats(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"recording_count": count, "total_size": totalSize})
}

// handleCameraRecordingDays returns the days of a month that have recordings for a camera.
// Query param: month=YYYY-MM (defaults to the current month).
func (h *Handler) handleCameraRecordingDays(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	month := r.URL.Query().Get("month")
	if month == "" {
		month = time.Now().Format("2006-01")
	}
	if !isValidMonth(month) {
		writeError(w, http.StatusBadRequest, "month must be in YYYY-MM format")
		return
	}
	days, err := h.db.GetRecordingDays(r.Context(), id, month)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording days")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"days": days})
}

// isValidMonth validates a "YYYY-MM" string.
func isValidMonth(s string) bool {
	_, err := time.Parse("2006-01", s)
	return err == nil
}

func (h *Handler) handleStartCamera(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StartCamera(r.Context(), id); err != nil {
		switch {
		case errors.As(err, new(*model.CameraNotFoundError)):
			writeAPIError(w, http.StatusNotFound, err)
		case errors.As(err, new(*model.CameraDisabledError)):
			writeAPIError(w, http.StatusBadRequest, err)
		case errors.As(err, new(*model.CameraAlreadyRunningError)):
			writeAPIError(w, http.StatusConflict, err)
		case isONVIFAuthError(err):
			writeError(w, http.StatusBadRequest, "failed to start camera: ONVIF authentication failed, please verify username and password")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	h.logOperation(r, "camera.start", "camera", id, "success", "camera started", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func isONVIFAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "authentication error") ||
		strings.Contains(msg, "requires authentication information") ||
		strings.Contains(msg, "401")
}

func isONVIFNotSupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "action not supported")
}

func (h *Handler) handleStopCamera(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	if err := h.camMgr.StopCamera(r.Context(), id); err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	h.logOperation(r, "camera.stop", "camera", id, "success", "camera stopped", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *Handler) handlePauseRecording(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	// Persist intent as recording_mode='off' so the scheduler doesn't resume it on the next tick.
	if h.db != nil {
		if err := h.db.UpdateCameraRecordingMode(r.Context(), id, storage.RecordingModeOff); err != nil {
			logger.Warn("failed to persist recording_mode=off", "camera_id", id, "error", err)
		}
	}
	if err := h.camMgr.PauseRecording(r.Context(), id); err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (h *Handler) handleResumeRecording(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}
	// Manual resume sets the camera back to continuous recording.
	if h.db != nil {
		if err := h.db.UpdateCameraRecordingMode(r.Context(), id, storage.RecordingModeContinuous); err != nil {
			logger.Warn("failed to persist recording_mode=continuous", "camera_id", id, "error", err)
		}
	}
	if err := h.camMgr.ResumeRecording(r.Context(), id); err != nil {
		var cnf *model.CameraNotFoundError
		if errors.As(err, &cnf) {
			writeAPIError(w, http.StatusNotFound, err)
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recording"})
}

// isValidRecordingMode reports whether mode is an accepted recording_mode value.
func isValidRecordingMode(mode string) bool {
	switch mode {
	case storage.RecordingModeContinuous, storage.RecordingModeScheduled, storage.RecordingModeOff, storage.RecordingModeEvent, storage.RecordingModeAdaptive:
		return true
	default:
		return false
	}
}

// handleGetRecordingSchedule returns a camera's weekly recording schedule.
func (h *Handler) handleGetRecordingSchedule(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	ranges, err := h.db.GetCameraSchedule(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording schedule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ranges": ranges})
}

// handleSetRecordingSchedule replaces a camera's weekly recording schedule.
func (h *Handler) handleSetRecordingSchedule(w http.ResponseWriter, r *http.Request) {
	id := getCameraID(r)
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	var body struct {
		Ranges []storage.CameraScheduleRange `json:"ranges"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	for _, rg := range body.Ranges {
		if rg.DayOfWeek < 0 || rg.DayOfWeek > 6 || !isValidHHMM(rg.StartTime) || !isValidHHMM(rg.EndTime) || rg.StartTime >= rg.EndTime {
			writeError(w, http.StatusBadRequest, "invalid schedule range (day 0-6, HH:MM, start < end)")
			return
		}
	}
	if err := h.db.SetCameraSchedule(r.Context(), id, body.Ranges); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save recording schedule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// isValidHHMM validates a "HH:MM" 24-hour time string.
func isValidHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	hh := s[0:2]
	mm := s[3:5]
	if hh < "00" || hh > "23" || mm < "00" || mm > "59" {
		return false
	}
	return true
}

// handleTestConnection attempts to connect to a camera URL with a short timeout.
// Returns success/failure, a human-readable message, and the latency in milliseconds.
func (h *Handler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocol      string `json:"protocol"`
		URL           string `json:"url"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		Encoding      string `json:"encoding"`
		ONVIFEndpoint string `json:"onvif_endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	target := body.URL
	if body.Protocol == "onvif" && body.ONVIFEndpoint != "" {
		target = body.ONVIFEndpoint
	}

	startTime := time.Now()

	switch {
	case strings.HasPrefix(target, "rtsp://"):
		// RTSP: try TCP connection to the host:port
		conn, err := net.DialTimeout("tcp", stripScheme(target), 3*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("connection refused: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		conn.Close()

	default:
		// HTTP/ONVIF: try HEAD/GET request with timeout
		client := &http.Client{Timeout: 3 * time.Second}
		// For URLs with credentials, inject them
		req, err := http.NewRequestWithContext(r.Context(), http.MethodHead, target, nil)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("invalid URL: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		if body.Username != "" {
			req.SetBasicAuth(body.Username, body.Password)
		}
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("connection failed: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		resp.Body.Close()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "connection successful",
		"latency_ms": time.Since(startTime).Milliseconds(),
	})
}

// stripScheme extracts host:port from a URL string for TCP dialing.
func stripScheme(rawURL string) string {
	// Remove scheme
	u := strings.TrimPrefix(rawURL, "rtsp://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	// Strip path and query
	if idx := strings.IndexAny(u, "/?"); idx >= 0 {
		u = u[:idx]
	}
	// Default port
	if !strings.Contains(u, ":") {
		u = u + ":554"
	}
	return u
}

// probeONVIFEncoding connects to an ONVIF device and retrieves the encoding
// from the first media profile. Returns "H264" or "H265", or empty string on failure.
func probeONVIFEncoding(ctx context.Context, endpoint, username, password string) string {
	client := onvif.NewClient(endpoint, username, password)
	if err := client.Connect(ctx); err != nil {
		return ""
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		return ""
	}
	return profiles[0].Encoding
}
