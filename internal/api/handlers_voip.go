package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
)

func (h *Handler) handleGetVoIPSettings(w http.ResponseWriter, r *http.Request) {
	h.voipSettingsMu.Lock()
	defer h.voipSettingsMu.Unlock()
	if h.config == nil {
		writeError(w, 500, "config not available")
		return
	}
	cfg := h.config.VoIP.Clone()
	cfg.Normalize()
	hasPbxPassword := cfg.PbxPassword != ""
	cfg.PbxPassword = ""
	users := make([]map[string]any, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		users = append(users, map[string]any{"username": u.Username, "has_password": u.Password != "", "record_calls": u.RecordCalls})
	}
	writeJSON(w, 200, struct {
		voip.Config
		Users          []map[string]any `json:"users"`
		Supported      bool             `json:"supported"`
		PbxHasPassword bool             `json:"pbx_has_password"`
	}{Config: cfg, Users: users, Supported: h.config.Media.Mode == "embedded", PbxHasPassword: hasPbxPassword})
}

func (h *Handler) handleUpdateVoIPSettings(w http.ResponseWriter, r *http.Request) {
	h.voipSettingsMu.Lock()
	defer h.voipSettingsMu.Unlock()
	if h.config == nil {
		writeError(w, 500, "config not available")
		return
	}
	old := h.config.VoIP.Clone()
	next := old.Clone()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeError(w, 400, "invalid VoIP settings: "+err.Error())
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		writeError(w, 400, "VoIP settings must be an object")
		return
	}
	if _, ok := fields["users"]; ok {
		next.Users = nil
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&next); err != nil {
		writeError(w, 400, "invalid VoIP settings: "+err.Error())
		return
	}
	// An omitted/blank password keeps the existing secret for the same username.
	for i := range next.Users {
		if next.Users[i].Password == "" {
			for _, u := range old.Users {
				if u.Username == next.Users[i].Username {
					next.Users[i].Password = u.Password
					break
				}
			}
		}
	}
	if next.PbxPassword == "" && next.PbxServer == old.PbxServer && next.PbxUsername == old.PbxUsername {
		next.PbxPassword = old.PbxPassword
	}
	next.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if next.Enable && h.config.Media.Mode != "embedded" {
		writeError(w, 400, "VoIP requires embedded media mode")
		return
	}
	if h.configWatcher != nil && h.configWatcher.CheckExternalChange() {
		writeError(w, 409, "config file was modified externally; please reload before saving")
		return
	}
	changed := !reflect.DeepEqual(old, next)
	if changed && h.voipRestarter == nil {
		writeError(w, 503, "VoIP runtime is not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if changed {
		if err := h.voipRestarter.RestartVoIP(ctx, &next); err != nil {
			writeError(w, 500, "VoIP configuration was not applied: "+err.Error())
			return
		}
	}
	h.config.VoIP = next
	var err error
	if h.configWatcher != nil {
		err = h.configWatcher.Save(false)
	} else {
		err = config.Save(h.configPath, h.config)
	}
	if err != nil {
		h.config.VoIP = old
		if changed {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 15*time.Second)
			restoreErr := h.voipRestarter.RestartVoIP(rollbackCtx, &old)
			rollbackCancel()
			if restoreErr != nil {
				err = fmt.Errorf("%v; runtime restore failed: %w", err, restoreErr)
			}
		}
		status := http.StatusInternalServerError
		if errors.Is(err, config.ErrConfigModified) {
			status = http.StatusConflict
		}
		writeError(w, status, "VoIP settings were not saved: "+err.Error())
		return
	}
	h.logSuccess(r, "config.update", "config", "voip", "VoIP settings updated", nil)
	writeJSON(w, 200, map[string]string{"status": "updated"})
}

type voipRuntime interface {
	VoIPStatus() voip.Status
	HangupVoIP(string) bool
}

func (h *Handler) handleVoIPStatus(w http.ResponseWriter, r *http.Request) {
	if runtime, ok := h.voipRestarter.(voipRuntime); ok {
		writeJSON(w, 200, runtime.VoIPStatus())
		return
	}
	writeJSON(w, 200, voip.Status{Endpoints: []voip.EndpointStatus{}, Calls: []voip.CallStatus{}})
}

func (h *Handler) handleVoIPCallHistory(w http.ResponseWriter, r *http.Request) {
	limit := 20
	offset := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(value, 100)
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = value
	}
	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []storage.VoIPCall{}, "total": 0, "limit": limit, "offset": offset})
		return
	}
	items, total, err := h.db.ListVoIPCalls(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load VoIP call history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) handleVoIPHangup(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipRuntime)
	if !ok {
		writeError(w, 503, "VoIP runtime is not available")
		return
	}
	id := voipCallID(r)
	if !runtime.HangupVoIP(id) {
		writeError(w, 404, "VoIP call not found")
		return
	}
	h.logSuccess(r, "voip.hangup", "voip_call", id, "VoIP call ended", nil)
	writeJSON(w, 200, map[string]string{"status": "ended"})
}

type voipIncomingCallRuntime interface {
	AnswerVoIPCall(string) error
	RejectVoIPCall(string) error
}

func (h *Handler) handleVoIPAnswer(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipIncomingCallRuntime)
	if !ok {
		writeError(w, 503, "VoIP runtime is not available")
		return
	}
	if err := runtime.AnswerVoIPCall(voipCallID(r)); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	h.logSuccess(r, "voip.answer", "voip_call", voipCallID(r), "VoIP incoming call answered", nil)
	writeJSON(w, 200, map[string]string{"status": "answered"})
}

func (h *Handler) handleVoIPReject(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipIncomingCallRuntime)
	if !ok {
		writeError(w, 503, "VoIP runtime is not available")
		return
	}
	if err := runtime.RejectVoIPCall(voipCallID(r)); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	h.logSuccess(r, "voip.reject", "voip_call", voipCallID(r), "VoIP incoming call rejected", nil)
	writeJSON(w, 200, map[string]string{"status": "rejected"})
}

type voipTalkRuntime interface {
	DialVoIP(string, string, string) (voip.DialResult, error)
	ClaimVoIPTalk(string) (string, error)
	AttachVoIPTalk(context.Context, string, string, string) (string, error)
	KeepVoIPTalk(string, string) bool
	DetachVoIPTalk(string, string) bool
}

func (h *Handler) handleVoIPTalkToken(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipTalkRuntime)
	if !ok {
		writeError(w, 503, "VoIP talk runtime is not available")
		return
	}
	token, err := runtime.ClaimVoIPTalk(voipCallID(r))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"talk_token": token})
}

func (h *Handler) handleVoIPTalkDetach(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipTalkRuntime)
	if !ok {
		writeError(w, 503, "VoIP talk runtime is not available")
		return
	}
	var req struct {
		Token string `json:"talk_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.Token == "" {
		writeError(w, 400, "talk_token is required")
		return
	}
	if !runtime.DetachVoIPTalk(voipCallID(r), req.Token) {
		writeError(w, 404, "VoIP browser talk session not found")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "detached"})
}

type voipDTMFRuntime interface {
	SendVoIPDTMF(string, string) error
}

func (h *Handler) handleVoIPDTMF(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipDTMFRuntime)
	if !ok {
		writeError(w, 503, "VoIP runtime is not available")
		return
	}
	var req struct {
		Digit string `json:"digit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || !validVoIPDTMFDigit(req.Digit) {
		writeError(w, 400, "digit must be one of 0-9, *, or #")
		return
	}
	if err := runtime.SendVoIPDTMF(voipCallID(r), req.Digit); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "sent"})
}

func validVoIPDTMFDigit(digit string) bool {
	return len(digit) == 1 && strings.Contains("0123456789*#", digit)
}

func (h *Handler) handleVoIPDial(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipTalkRuntime)
	if !ok {
		writeError(w, 503, "VoIP talk runtime is not available")
		return
	}
	var req struct {
		User     string `json:"user"`
		Security string `json:"security"`
		SDP      string `json:"sdp"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.User == "" || req.SDP == "" {
		writeError(w, 400, "user and browser SDP are required")
		return
	}
	call, err := runtime.DialVoIP(req.User, req.Security, req.SDP)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	h.logSuccess(r, "voip.dial", "voip_call", call.CallID, "VoIP outgoing call started", map[string]any{"user": req.User})
	writeJSON(w, 202, call)
}
func (h *Handler) handleVoIPTalk(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipTalkRuntime)
	if !ok {
		writeError(w, 503, "VoIP talk runtime is not available")
		return
	}
	var req struct {
		SDP   string `json:"sdp"`
		Token string `json:"talk_token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.SDP == "" || req.Token == "" {
		writeError(w, 400, "browser SDP and talk_token are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	answer, err := runtime.AttachVoIPTalk(ctx, voipCallID(r), req.Token, req.SDP)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"sdp": answer, "type": "answer"})
}
func (h *Handler) handleVoIPTalkKeepalive(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.voipRestarter.(voipTalkRuntime)
	if !ok {
		writeError(w, 503, "VoIP talk runtime is not available")
		return
	}
	var req struct {
		Token string `json:"talk_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.Token == "" {
		writeError(w, 400, "talk_token is required")
		return
	}
	if !runtime.KeepVoIPTalk(voipCallID(r), req.Token) {
		writeError(w, 404, "VoIP talk call not found")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "active"})
}

func voipCallID(r *http.Request) string {
	id := chi.URLParam(r, "call_id")
	if decoded, err := url.PathUnescape(id); err == nil {
		return decoded
	}
	return id
}
