package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

type createUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
}

type updateUserRequest struct {
	Password    string `json:"password,omitempty"`
	Role        string `json:"role,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type userResponse struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toUserResponse(u *model.User) userResponse {
	return userResponse{
		ID:          u.ID,
		Username:    u.Username,
		Role:        string(u.Role),
		DisplayName: u.DisplayName,
		Enabled:     u.Enabled,
		CreatedAt:   u.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   u.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// handleListUsers handles GET /api/users
func (h *Handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.db.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list users: %v", err))
		return
	}
	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, toUserResponse(u))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGetUser handles GET /api/users/{id}
func (h *Handler) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	u, err := h.db.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get user: %v", err))
		return
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// handleCreateUser handles POST /api/users
func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	role := model.Role(strings.TrimSpace(req.Role))
	if role == "" {
		role = model.RoleUser
	}
	if !role.IsValid() {
		writeError(w, http.StatusBadRequest, "role must be 'super_admin' or 'user'")
		return
	}

	existing, err := h.db.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to check user: %v", err))
		return
	}
	if existing != nil {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}

	hash, err := middleware.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
		return
	}

	now := time.Now().UTC()
	u := &model.User{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         role,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}

	if err := h.db.CreateUser(r.Context(), u); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create user: %v", err))
		return
	}
	h.logOperation(r, "user.create", "user", strconv.FormatInt(u.ID, 10), "success", "user created", map[string]any{"role": u.Role})

	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

// handleUpdateUser handles PUT /api/users/{id}
func (h *Handler) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	u, err := h.db.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get user: %v", err))
		return
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Password != "" {
		if len(req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		hash, err := middleware.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
			return
		}
		u.PasswordHash = hash
	}

	if req.Role != "" {
		role := model.Role(strings.TrimSpace(req.Role))
		if !role.IsValid() {
			writeError(w, http.StatusBadRequest, "role must be 'super_admin' or 'user'")
			return
		}
		u.Role = role
	}

	if req.DisplayName != "" {
		u.DisplayName = strings.TrimSpace(req.DisplayName)
	}

	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}

	if err := h.db.UpdateUser(r.Context(), u); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update user: %v", err))
		return
	}
	h.logOperation(r, "user.update", "user", strconv.FormatInt(u.ID, 10), "success", "user updated", map[string]any{"role": u.Role, "enabled": u.Enabled})

	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// handleDeleteUser handles DELETE /api/users/{id}
func (h *Handler) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	currentUser := middleware.ContextUser(r)
	if currentUser != nil && currentUser.ID == id {
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}

	u, err := h.db.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get user: %v", err))
		return
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if u.Role == model.RoleSuperAdmin {
		hasOther, err := h.db.HasSuperAdmin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to check admin count: %v", err))
			return
		}
		count, _ := h.db.CountUsers(r.Context())
		if hasOther && count <= 1 {
			writeError(w, http.StatusBadRequest, "cannot delete the last super admin")
			return
		}
	}

	if err := h.db.DeleteUser(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete user: %v", err))
		return
	}
	h.logOperation(r, "user.delete", "user", strconv.FormatInt(id, 10), "success", "user deleted", map[string]any{"username": u.Username})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
