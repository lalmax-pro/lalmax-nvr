package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

// SFUBridgeRegistry is supplied by the trusted runtime during bootstrap.
type SFUBridgeRegistry interface {
	Create(context.Context, string, string) (sfubridge.Status, error)
	Get(context.Context, string, string) (sfubridge.Status, error)
	Playback(context.Context, string, string) (string, string, error)
	Stop(context.Context, string, string) error
	Remove(context.Context, string, string) error
}

type sfuAVBridgeRegistry interface {
	CreateAV(context.Context, string, string, string) (sfubridge.Status, error)
}
type sfuIdempotentBridgeRegistry interface {
	CreateIdempotent(context.Context, string, string, string, string) (sfubridge.Status, error)
}
type sfuBridgeRequestResolver interface {
	ResolveIdempotent(context.Context, string, string) (sfubridge.Status, error)
}
type sfuBridgeLister interface {
	List(context.Context, string) ([]sfubridge.Status, error)
}

// SetSFUBridgeRegistry must be called before serving requests.
func (h *Handler) SetSFUBridgeRegistry(registry SFUBridgeRegistry) { h.sfuBridges = registry }

// SetSFUSignaling must be called before the handler starts serving requests.
func (h *Handler) SetSFUSignaling(signaling http.Handler) { h.sfuSignaling = signaling }

type SFUTicketIssuer interface {
	Issue(context.Context, *model.User) (string, error)
}

type sfuRoomTicketIssuer interface {
	IssueForRoom(context.Context, *model.User, string) (string, error)
}

type sfuGenerationTicketIssuer interface {
	IssueForRoomGeneration(context.Context, *model.User, string, string) (string, error)
}

func (h *Handler) SetSFUTicketIssuer(issuer SFUTicketIssuer) { h.sfuTickets = issuer }

func (h *Handler) handleSFUSignaling(w http.ResponseWriter, r *http.Request) {
	if h.sfuSignaling == nil {
		writeError(w, http.StatusServiceUnavailable, "SFU signaling unavailable")
		return
	}
	h.sfuSignaling.ServeHTTP(w, r)
}

func (h *Handler) createSFUTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := middleware.ContextUser(r)
	if _, ok := sfuPrincipal(r); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if h.sfuTickets == nil {
		writeError(w, http.StatusServiceUnavailable, "SFU signaling unavailable")
		return
	}
	var input struct {
		RoomID         string `json:"room_id,omitempty"`
		RoomGeneration string `json:"room_generation,omitempty"`
	}
	if !sfuDecodeJSON(w, r, &input) {
		return
	}
	var ticket string
	var err error
	if input.RoomGeneration != "" {
		if len(input.RoomGeneration) != 32 {
			writeError(w, http.StatusBadRequest, "invalid room generation")
			return
		}
		issuer, ok := h.sfuTickets.(sfuGenerationTicketIssuer)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "room generation validation unavailable")
			return
		}
		roomID := input.RoomID
		if roomID == "" {
			roomID = "default"
		}
		ticket, err = issuer.IssueForRoomGeneration(r.Context(), user, roomID, input.RoomGeneration)
	} else if input.RoomID == "" || input.RoomID == "default" {
		ticket, err = h.sfuTickets.Issue(r.Context(), user)
	} else if issuer, ok := h.sfuTickets.(sfuRoomTicketIssuer); ok {
		ticket, err = issuer.IssueForRoom(r.Context(), user, input.RoomID)
	} else {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}
	if err != nil {
		if errors.Is(err, sfu.ErrRoomNotFound) {
			writeError(w, http.StatusNotFound, "room not found")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "SFU ticket unavailable")
		return
	}
	var servers []config.SFUICEServer
	if h.config != nil {
		servers = h.config.SFU.ICEServers
	}
	writeJSON(w, http.StatusCreated, struct {
		Ticket     string                `json:"ticket"`
		ExpiresIn  int                   `json:"expires_in"`
		ICEServers []config.SFUICEServer `json:"ice_servers"`
	}{ticket, 30, servers})
}

func sfuPrincipal(r *http.Request) (string, bool) {
	user := middleware.ContextUser(r)
	if user == nil || !user.Enabled || user.ID < 0 {
		return "", false
	}
	if user.ID > 0 {
		return "user:" + strconv.FormatInt(user.ID, 10), true
	}
	if user.Username == "" {
		return "", false
	}
	return "legacy:" + user.Username, true
}
func (h *Handler) sfuBridgeRequest(w http.ResponseWriter, r *http.Request) (SFUBridgeRegistry, string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	principal, ok := sfuPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, "", false
	}
	if h.sfuBridges == nil {
		writeError(w, http.StatusServiceUnavailable, "SFU bridge service unavailable")
		return nil, "", false
	}
	return h.sfuBridges, principal, true
}
func sfuDecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "application/json required")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}
func sfuBridgeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sfubridge.ErrNotFound), errors.Is(err, sfu.ErrPublicationNotFound):
		writeError(w, http.StatusNotFound, "bridge or publication not found")
	case errors.Is(err, sfubridge.ErrDenied), errors.Is(err, access.ErrDenied):
		writeError(w, http.StatusForbidden, "operation denied")
	case errors.Is(err, sfubridge.ErrInvalid), errors.Is(err, access.ErrInvalid), errors.Is(err, access.ErrBridgeCodec):
		writeError(w, http.StatusBadRequest, "invalid bridge request")
	case errors.Is(err, sfubridge.ErrLimit):
		writeError(w, http.StatusTooManyRequests, "bridge limit reached")
	case errors.Is(err, sfubridge.ErrNotFinished):
		writeError(w, http.StatusConflict, "bridge has not stopped")
	case errors.Is(err, sfubridge.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency key reused with different publications")
	case errors.Is(err, sfubridge.ErrClosed), errors.Is(err, access.ErrClosed), errors.Is(err, sfu.ErrRoomClosed):
		writeError(w, http.StatusServiceUnavailable, "SFU bridge service unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "bridge operation failed")
	}
}

type sfuBridgeStatusResponse struct {
	ID                 string          `json:"id"`
	PublicationID      string          `json:"publication_id"`
	AudioPublicationID string          `json:"audio_publication_id,omitempty"`
	StreamID           string          `json:"stream_id"`
	State              sfubridge.State `json:"state"`
}

func sfuStatus(s sfubridge.Status) sfuBridgeStatusResponse {
	return sfuBridgeStatusResponse{ID: s.ID, PublicationID: s.PublicationID, AudioPublicationID: s.AudioPublicationID, StreamID: s.StreamID, State: s.State}
}
func (h *Handler) getSFUBridgeRequest(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validSFUIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "invalid idempotency key")
		return
	}
	resolver, ok := registry.(sfuBridgeRequestResolver)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "SFU bridge request lookup unavailable")
		return
	}
	status, err := resolver.ResolveIdempotent(r.Context(), principal, keys[0])
	if errors.Is(err, sfubridge.ErrRequestPending) {
		writeJSON(w, http.StatusAccepted, map[string]string{"state": "pending"})
		return
	}
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sfuStatus(status))
}
func (h *Handler) listSFUBridges(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	lister, ok := registry.(sfuBridgeLister)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "SFU bridge list unavailable")
		return
	}
	statuses, err := lister.List(r.Context(), principal)
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	response := make([]sfuBridgeStatusResponse, 0, len(statuses))
	for _, status := range statuses {
		response = append(response, sfuStatus(status))
	}
	writeJSON(w, http.StatusOK, response)
}
func (h *Handler) createSFUBridge(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		PublicationID      string `json:"publication_id"`
		AudioPublicationID string `json:"audio_publication_id,omitempty"`
	}
	if !sfuDecodeJSON(w, r, &input) {
		return
	}
	if input.PublicationID == "" || len(input.PublicationID) > 256 || strings.TrimSpace(input.PublicationID) != input.PublicationID {
		writeError(w, http.StatusBadRequest, "invalid publication ID")
		return
	}
	if input.AudioPublicationID != "" && (len(input.AudioPublicationID) > 256 || strings.TrimSpace(input.AudioPublicationID) != input.AudioPublicationID || input.AudioPublicationID == input.PublicationID) {
		writeError(w, http.StatusBadRequest, "invalid audio publication ID")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) > 1 || (len(keys) == 1 && !validSFUIdempotencyKey(keys[0])) {
		writeError(w, http.StatusBadRequest, "invalid idempotency key")
		return
	}
	var status sfubridge.Status
	var err error
	if len(keys) == 1 {
		idempotent, ok := registry.(sfuIdempotentBridgeRegistry)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "idempotent bridge creation unavailable")
			return
		}
		status, err = idempotent.CreateIdempotent(r.Context(), principal, input.PublicationID, input.AudioPublicationID, keys[0])
	} else if input.AudioPublicationID == "" {
		status, err = registry.Create(r.Context(), principal, input.PublicationID)
	} else {
		combined, ok := registry.(sfuAVBridgeRegistry)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "combined bridge unavailable")
			return
		}
		status, err = combined.CreateAV(r.Context(), principal, input.PublicationID, input.AudioPublicationID)
	}
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sfuStatus(status))
}

func validSFUIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~') {
			return false
		}
	}
	return true
}
func (h *Handler) getSFUBridge(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	status, err := registry.Get(r.Context(), principal, chi.URLParam(r, "id"))
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sfuStatus(status))
}
func (h *Handler) getSFUBridgePlayback(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	stream, token, err := registry.Playback(r.Context(), principal, chi.URLParam(r, "id"))
	if err != nil {
		sfuBridgeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		StreamID string `json:"stream_id"`
		Token    string `json:"token"`
	}{stream, token})
}
func (h *Handler) stopSFUBridge(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	var body struct{}
	if !sfuDecodeJSON(w, r, &body) {
		return
	}
	if err := registry.Stop(r.Context(), principal, chi.URLParam(r, "id")); err != nil {
		sfuBridgeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) removeSFUBridge(w http.ResponseWriter, r *http.Request) {
	registry, principal, ok := h.sfuBridgeRequest(w, r)
	if !ok {
		return
	}
	if err := registry.Remove(r.Context(), principal, chi.URLParam(r, "id")); err != nil {
		sfuBridgeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
