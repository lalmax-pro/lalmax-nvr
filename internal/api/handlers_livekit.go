package api

import (
	"context"
	"net/http"
)

// LiveKitJoin is an ephemeral credential for one room and participant.
type LiveKitJoin struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	Room      string `json:"room"`
	Identity  string `json:"identity"`
	ExpiresIn int    `json:"expires_in"`
}

// LiveKitJoinIssuer must revalidate the principal and authorize the requested room.
type LiveKitJoinIssuer interface {
	IssueLiveKitJoin(context.Context, string, string) (LiveKitJoin, error)
}

func (h *Handler) SetLiveKitJoinIssuer(issuer LiveKitJoinIssuer) { h.liveKitJoin = issuer }

func (h *Handler) createLiveKitJoin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := sfuPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if h.liveKitJoin == nil {
		writeError(w, http.StatusServiceUnavailable, "LiveKit unavailable")
		return
	}
	var input struct {
		Room string `json:"room"`
	}
	if !sfuDecodeJSON(w, r, &input) {
		return
	}
	if input.Room == "" {
		input.Room = "default"
	}
	join, err := h.liveKitJoin.IssueLiveKitJoin(r.Context(), principal, input.Room)
	if err != nil {
		writeError(w, http.StatusForbidden, "room join denied")
		return
	}
	writeJSON(w, http.StatusCreated, join)
}
