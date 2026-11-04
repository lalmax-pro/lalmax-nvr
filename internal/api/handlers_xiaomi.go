package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
)

// --- Xiaomi cloud endpoints ---

func (h *Handler) handleXiaomiAuth(w http.ResponseWriter, r *http.Request) {
	h.xiaomiAuthMu.Lock()
	defer h.xiaomiAuthMu.Unlock()
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Region   string `json:"region,omitempty"`
		Account  string `json:"account,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	account := normalizeXiaomiAccount(req.Account)
	if err := validXiaomiAccount(account); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if account != "default" {
		if _, ok := h.cloudProxy.(xiaomiFullConfigProxy); !ok {
			writeError(w, http.StatusNotImplemented, "named account management unavailable")
			return
		}
	}
	region := req.Region
	if region == "" {
		region = "cn"
	}

	result, verification, err := h.cloudProxy.SignIn(r.Context(), req.Username, req.Password, region)
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("authentication failed: %v", err))
		return
	}

	if verification != nil {
		h.rememberXiaomiChallenge(verification, account)
		writeJSON(w, http.StatusAccepted, verificationToResponse(verification))
		return
	}

	// Store token in config
	if err := h.saveXiaomiAccountToken(r.Context(), account, result); err != nil {
		logger.Error("failed to save Xiaomi credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save Xiaomi credentials")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"user_id": result.UserID,
	})
}

func (h *Handler) handleXiaomiCaptcha(w http.ResponseWriter, r *http.Request) {
	h.xiaomiAuthMu.Lock()
	defer h.xiaomiAuthMu.Unlock()
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		SessionID   string `json:"session_id"`
		CaptchaCode string `json:"captcha_code"`
		Account     string `json:"account,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.CaptchaCode == "" {
		writeError(w, http.StatusBadRequest, "session_id and captcha_code are required")
		return
	}

	account, err := h.challengeAccount(req.SessionID, req.Account)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, verification, err := h.cloudProxy.SubmitCaptcha(r.Context(), req.SessionID, req.CaptchaCode)
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("captcha verification failed: %v", err))
		return
	}

	if verification != nil {
		h.rememberXiaomiChallenge(verification, account)
		writeJSON(w, http.StatusAccepted, verificationToResponse(verification))
		return
	}

	// Store token in config
	if err := h.saveXiaomiAccountToken(r.Context(), account, result); err != nil {
		logger.Error("failed to save Xiaomi credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save Xiaomi credentials")
		return
	}

	delete(h.xiaomiChallenges, req.SessionID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"user_id": result.UserID,
	})
}

func (h *Handler) handleXiaomiVerify(w http.ResponseWriter, r *http.Request) {
	h.xiaomiAuthMu.Lock()
	defer h.xiaomiAuthMu.Unlock()
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
		Ticket    string `json:"ticket"`
		Account   string `json:"account,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" || req.Ticket == "" {
		writeError(w, http.StatusBadRequest, "session_id and ticket are required")
		return
	}

	account, err := h.challengeAccount(req.SessionID, req.Account)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, verification, err := h.cloudProxy.SubmitVerify(r.Context(), req.SessionID, req.Ticket)
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Sprintf("verification failed: %v", err))
		return
	}

	if verification != nil {
		h.rememberXiaomiChallenge(verification, account)
		writeJSON(w, http.StatusAccepted, verificationToResponse(verification))
		return
	}

	// Store token in config
	if err := h.saveXiaomiAccountToken(r.Context(), account, result); err != nil {
		logger.Error("failed to save Xiaomi credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save Xiaomi credentials")
		return
	}

	delete(h.xiaomiChallenges, req.SessionID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"user_id": result.UserID,
	})
}

func (h *Handler) handleXiaomiStatus(w http.ResponseWriter, r *http.Request) {
	cfg := h.xiaomiConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": cfg.UserID != "" && cfg.Token != "",
		"region":        cfg.Region,
	})
}

func (h *Handler) handleXiaomiLogout(w http.ResponseWriter, r *http.Request) {
	h.xiaomiAuthMu.Lock()
	defer h.xiaomiAuthMu.Unlock()
	account := normalizeXiaomiAccount(r.URL.Query().Get("account"))
	if err := validXiaomiAccount(account); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if account != "default" {
		if err := h.removeXiaomiAccount(r.Context(), account); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.cancelXiaomiChallenges(account)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if err := h.saveXiaomiConfig(r.Context(), config.XiaomiConfig{}); err != nil {
		logger.Error("failed to clear Xiaomi credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to clear Xiaomi credentials")
		return
	}
	h.cancelXiaomiChallenges("default")
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *Handler) handleXiaomiDevices(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	// Get stored token from config
	accountID := r.URL.Query().Get("account")
	account, exists := h.xiaomiConfig().Account(accountID)
	if !exists || account.UserID == "" || account.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
			"devices":       []CloudDeviceInfo{},
			"message":       "not authenticated",
		})
		return
	}

	devices, err := h.listXiaomiDevices(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to get devices: %v", err))
		return
	}
	if devices == nil {
		devices = []CloudDeviceInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"devices":       devices,
	})
}

func (h *Handler) listXiaomiDevices(ctx context.Context, id string) ([]CloudDeviceInfo, error) {
	if id == "" || id == "default" {
		return h.cloudProxy.ListDevices(ctx)
	}
	if proxy, ok := h.cloudProxy.(interface {
		ListDevicesForAccount(context.Context, string) ([]CloudDeviceInfo, error)
	}); ok {
		return proxy.ListDevicesForAccount(ctx, id)
	}
	return nil, errors.New("named Xiaomi accounts unavailable")
}

func (h *Handler) handleXiaomiAccounts(w http.ResponseWriter, r *http.Request) {
	cfg := h.xiaomiConfig()
	type accountInfo struct {
		ID            string `json:"id"`
		Region        string `json:"region"`
		Authenticated bool   `json:"authenticated"`
	}
	accounts := []accountInfo{{ID: "default", Region: cfg.Region, Authenticated: cfg.UserID != "" && cfg.Token != ""}}
	for id, account := range cfg.Accounts {
		accounts = append(accounts, accountInfo{ID: id, Region: account.Region, Authenticated: account.UserID != "" && account.Token != ""})
	}
	sort.Slice(accounts[1:], func(i, j int) bool { return accounts[i+1].ID < accounts[j+1].ID })
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

// handleXiaomiSync syncs Xiaomi cloud device info (name, model, MAC) to NVR camera config.
// It fetches all devices from Xiaomi cloud, matches them to existing NVR cameras by DID,
// and updates name, model, brand, and serial_number (MAC). Returns count of synced cameras.
func (h *Handler) handleXiaomiSync(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}
	accountID := r.URL.Query().Get("account")
	account, exists := h.xiaomiConfig().Account(accountID)
	if !exists || account.Token == "" {
		writeError(w, http.StatusUnauthorized, "xiaomi cloud not authenticated")
		return
	}
	if h.camMgr == nil {
		writeError(w, http.StatusInternalServerError, "camera manager not available")
		return
	}

	devices, err := h.listXiaomiDevices(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to get devices: %v", err))
		return
	}

	// Build DID → CloudDeviceInfo lookup
	deviceByDID := make(map[string]*CloudDeviceInfo, len(devices))
	for i := range devices {
		deviceByDID[devices[i].DID] = &devices[i]
	}

	synced := 0
	for i := range h.config.Cameras {
		cam := &h.config.Cameras[i]
		if cam.Protocol != "xiaomi" {
			continue
		}
		if source, err := url.Parse(cam.URL); err == nil {
			cameraAccount := source.Query().Get("account")
			if cameraAccount == "default" {
				cameraAccount = ""
			}
			selectedAccount := accountID
			if selectedAccount == "default" {
				selectedAccount = ""
			}
			if cameraAccount != selectedAccount {
				continue
			}
		}

		did := cam.DID
		if did == "" {
			did = extractDIDFromURL(cam.URL)
		}
		if did == "" {
			continue
		}

		dev, ok := deviceByDID[did]
		if !ok {
			continue
		}

		updates := camera.CameraUpdate{
			Name:         &dev.Name,
			Model:        &dev.Model,
			Brand:        strPtr("Xiaomi"),
			SerialNumber: &dev.MAC,
		}

		if _, err := h.camMgr.UpdateCamera(context.Background(), cam.ID, updates); err != nil {
			logger.Warn("failed to sync xiaomi camera", "camera_id", cam.ID, "did", did, "error", err)
			continue
		}
		synced++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"synced": synced,
		"total":  len(deviceByDID),
	})
}

func (h *Handler) handleCheckVendor(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     "unknown",
			"compatible": true,
		})
		return
	}
	accountID := r.URL.Query().Get("account")
	account, exists := h.xiaomiConfig().Account(accountID)
	if !exists || account.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     "unknown",
			"compatible": true,
		})
		return
	}

	did := r.URL.Query().Get("did")
	if did == "" {
		writeError(w, http.StatusBadRequest, "did parameter required")
		return
	}

	var vendor string
	var err error
	if accountID == "" || accountID == "default" {
		vendor, err = h.cloudProxy.CheckVendor(r.Context(), did)
	} else if proxy, ok := h.cloudProxy.(interface {
		CheckVendorForAccount(context.Context, string, string) (string, error)
	}); ok {
		vendor, err = proxy.CheckVendorForAccount(r.Context(), accountID, did)
	} else {
		err = errors.New("named Xiaomi accounts unavailable")
	}
	if err != nil {
		// For errors, return unknown/compatible (don't block on uncertainty)
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     "unknown",
			"compatible": true,
		})
		return
	}

	if vendor != "cs2" {
		writeJSON(w, http.StatusOK, map[string]any{
			"vendor":     vendor,
			"compatible": false,
			"message":    fmt.Sprintf("This device uses unsupported transport %q. Only CS2 protocol cameras are supported.", vendor),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"vendor":     "cs2",
		"compatible": true,
	})
}

func (h *Handler) xiaomiConfig() config.XiaomiConfig {
	h.xiaomiMu.Lock()
	defer h.xiaomiMu.Unlock()
	if h.config == nil {
		return config.XiaomiConfig{}
	}
	return h.config.Xiaomi
}

// saveXiaomiToken persists a candidate before publishing it to running cameras.
// Failed writes leave both the in-memory config and cloud credentials unchanged.
func (h *Handler) saveXiaomiToken(ctx context.Context, result *CloudAuthResult) error {
	if result == nil || result.UserID == "" || result.PassToken == "" {
		return errors.New("missing Xiaomi auth result")
	}
	return h.saveXiaomiConfig(ctx, config.XiaomiConfig{UserID: result.UserID, Token: result.PassToken, Region: result.Region})
}

func (h *Handler) saveXiaomiConfig(ctx context.Context, credentials config.XiaomiConfig) error {
	h.xiaomiMu.Lock()
	defer h.xiaomiMu.Unlock()
	if h.config == nil {
		return errors.New("missing Xiaomi configuration")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := h.config.Xiaomi
	candidate := *h.config
	candidate.Xiaomi = credentials
	candidate.Xiaomi.Accounts = maps.Clone(previous.Accounts)
	if err := config.Save(h.configPath, &candidate); err != nil {
		return err
	}
	if h.cloudProxy != nil {
		if err := h.cloudProxy.SetCloudConfig(ctx, credentials.UserID, credentials.Token, credentials.Region); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			restoreErr := h.cloudProxy.SetCloudConfig(rollbackCtx, previous.UserID, previous.Token, previous.Region)
			candidate.Xiaomi = previous
			return errors.Join(err, restoreErr, config.Save(h.configPath, &candidate))
		}
	}
	h.config.Xiaomi = candidate.Xiaomi
	return nil
}

// verificationToResponse converts a CloudVerificationRequired to an API response map.
func verificationToResponse(v *CloudVerificationRequired) map[string]any {
	resp := map[string]any{
		"status": "verification_required",
	}
	if len(v.Captcha) > 0 {
		resp["captcha"] = base64.StdEncoding.EncodeToString(v.Captcha)
	}
	if v.VerifyPhone != "" {
		resp["verify_phone"] = v.VerifyPhone
	}
	if v.VerifyEmail != "" {
		resp["verify_email"] = v.VerifyEmail
	}
	if v.CaptchaSessionID != "" {
		resp["session_id"] = v.CaptchaSessionID
	}
	return resp
}

// HandleXiaomiTalkWS handles WebSocket connections for Xiaomi camera two-way audio.
// Browser sends PCMA audio via WebSocket, server forwards to camera speaker via MISS protocol.
func (h *Handler) handleXiaomiTalkWS(w http.ResponseWriter, r *http.Request) {
	if h.cloudProxy == nil {
		writeError(w, http.StatusServiceUnavailable, "xiaomi cloud not available")
		return
	}

	cameraID := r.URL.Query().Get("camera_id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	// Find camera config
	var cam *config.CameraConfig
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == cameraID {
			cam = &h.config.Cameras[i]
			break
		}
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if cam.Protocol != "xiaomi" {
		writeError(w, http.StatusBadRequest, "camera is not a xiaomi camera")
		return
	}

	// Resolve MISS URL
	did := cam.DID
	if did == "" {
		did = extractDIDFromURL(cam.URL)
	}
	if did == "" {
		writeError(w, http.StatusBadRequest, "camera has no DID")
		return
	}

	cfg := h.xiaomiConfig()
	accountID := ""
	if source, err := url.Parse(cam.URL); err == nil {
		accountID = source.Query().Get("account")
	}
	account, ok := cfg.Account(accountID)
	if !ok {
		writeError(w, http.StatusBadRequest, "xiaomi account not found")
		return
	}
	cloudCfg := xiaomi.XiaomiCloudConfig{
		UserID: account.UserID, Token: account.Token, Region: account.Region,
	}
	missURL, err := xiaomi.ResolveMISSURLContext(r.Context(), cloudCfg, did, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to resolve MISS URL: %v", err))
		return
	}

	// Connect to camera
	client, err := xiaomi.NewMISSClientContext(r.Context(), missURL, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to connect to camera: %v", err))
		return
	}
	defer client.Conn.Close()
	stopCancel := context.AfterFunc(r.Context(), func() { _ = client.Conn.Close() })
	defer stopCancel()

	// Start speaker
	if err := client.StartSpeaker(); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to start speaker: %v", err))
		return
	}
	defer client.StopSpeaker()

	// Upgrade to WebSocket
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	h.logSuccess(r, "talk.start", "camera", cameraID, "Xiaomi talk session started", nil)
	logger.Info("Xiaomi talk WebSocket connected", "camera_id", cameraID)

	// Send status
	if err := conn.WriteJSON(map[string]any{
		"status":        "connected",
		"camera_id":     cameraID,
		"speaker_codec": client.SpeakerCodec(),
	}); err != nil {
		logger.Error("Failed to send status message", "error", err)
		return
	}

	// Read audio from WebSocket and send to camera
	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.Error("Xiaomi talk WebSocket read error", "error", err)
			}
			break
		}

		if messageType == websocket.BinaryMessage {
			if err := client.WriteAudio(client.SpeakerCodec(), message); err != nil {
				logger.Error("Failed to write audio to camera", "camera_id", cameraID, "error", err)
				break
			}
		}
	}

	logger.Info("Xiaomi talk WebSocket disconnected", "camera_id", cameraID)
}
