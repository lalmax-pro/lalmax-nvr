package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
)

type SFURoomManager interface {
	ListRooms(context.Context, string) ([]sfu.Room, error)
	CreateRoom(context.Context, string, string) (sfu.Room, error)
	CloseRoom(context.Context, string, string, string) error
}

func (h *Handler) SetSFURoomManager(rooms SFURoomManager) { h.sfuRooms = rooms }

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
	if rooms == nil {
		rooms = []sfu.Room{}
	}
	writeJSON(w, http.StatusOK, rooms)
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
	writeJSON(w, http.StatusCreated, room)
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
