package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lalmax/sfu"
)

type SFURoomManager interface {
	ListRooms(context.Context, string) ([]sfu.Snapshot, error)
	CreateRoom(context.Context, string, string) (sfu.Snapshot, error)
	CloseRoom(context.Context, string, string, string) error
}

func (h *Handler) SetSFURoomManager(rooms SFURoomManager) { h.sfuRooms = rooms }

type sfuRoomResponse struct {
	ID           string `json:"id"`
	Generation   string `json:"generation"`
	Participants int    `json:"participants"`
}

func sfuRoomStatus(s sfu.Snapshot) sfuRoomResponse {
	participants := 0
	for _, p := range s.Participants {
		if !strings.HasPrefix(p.ID, "bridge_") {
			participants++
		}
	}
	return sfuRoomResponse{ID: s.Key.ID, Generation: s.Generation, Participants: participants}
}
func (h *Handler) sfuRoomRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := sfuPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	if h.sfuRooms == nil {
		writeError(w, http.StatusServiceUnavailable, "SFU rooms unavailable")
		return "", false
	}
	return principal, true
}
func sfuRoomError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sfubridge.ErrDenied):
		writeError(w, http.StatusForbidden, "operation denied")
	case errors.Is(err, sfu.ErrInvalidID):
		writeError(w, http.StatusBadRequest, "invalid room request")
	case errors.Is(err, sfu.ErrRoomExists):
		writeError(w, http.StatusConflict, "room already exists")
	case errors.Is(err, sfu.ErrRoomNotFound):
		writeError(w, http.StatusNotFound, "room not found")
	case errors.Is(err, sfubridge.ErrLimit):
		writeError(w, http.StatusTooManyRequests, "room limit reached")
	case errors.Is(err, sfubridge.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, "SFU rooms unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "room operation failed")
	}
}
func (h *Handler) listSFURooms(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.sfuRoomRequest(w, r)
	if !ok {
		return
	}
	rooms, err := h.sfuRooms.ListRooms(r.Context(), principal)
	if err != nil {
		sfuRoomError(w, err)
		return
	}
	out := make([]sfuRoomResponse, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, sfuRoomStatus(room))
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) createSFURoom(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.sfuRoomRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if !sfuDecodeJSON(w, r, &input) {
		return
	}
	room, err := h.sfuRooms.CreateRoom(r.Context(), principal, input.ID)
	if err != nil {
		sfuRoomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sfuRoomStatus(room))
}
func (h *Handler) closeSFURoom(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.sfuRoomRequest(w, r)
	if !ok {
		return
	}
	if err := h.sfuRooms.CloseRoom(r.Context(), principal, chi.URLParam(r, "id"), r.URL.Query().Get("generation")); err != nil {
		sfuRoomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
