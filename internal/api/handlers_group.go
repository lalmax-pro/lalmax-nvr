package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// ==================== Group API ====================

// handleListGroups handles GET /api/groups.
func (h *Handler) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.db.ListDeviceGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}
	if groups == nil {
		groups = []storage.DeviceGroup{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": groups})
}

// handleGetGroup handles GET /api/groups/{id}.
func (h *Handler) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}

	group, err := h.db.GetDeviceGroup(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}

	// Get channel count
	total, online, _ := h.db.GetGroupChannelStats(r.Context(), id)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group":         group,
		"channel_total": total,
		"channel_online": online,
	})
}

// CreateGroupRequest is the request body for creating a group.
type CreateGroupRequest struct {
	Name      string `json:"name"`
	ParentID  int64  `json:"parent_id"`
	SortOrder int    `json:"sort_order"`
}

// handleCreateGroup handles POST /api/groups.
func (h *Handler) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req CreateGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Calculate level based on parent
	level := 0
	if req.ParentID > 0 {
		parent, err := h.db.GetDeviceGroup(r.Context(), req.ParentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "parent group not found")
			return
		}
		level = parent.Level + 1
	}

	group := &storage.DeviceGroup{
		Name:      req.Name,
		ParentID:  req.ParentID,
		Level:     level,
		SortOrder: req.SortOrder,
	}

	id, err := h.db.CreateDeviceGroup(r.Context(), group)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create group")
		return
	}

	group.ID = id
	h.logSuccess(r, "group.create", "group", strconv.FormatInt(id, 10), "device group created", map[string]any{"name": group.Name})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group":  group,
		"status": "ok",
	})
}

// UpdateGroupRequest is the request body for updating a group.
type UpdateGroupRequest struct {
	Name      string `json:"name"`
	ParentID  *int64 `json:"parent_id"`
	SortOrder *int   `json:"sort_order"`
}

// handleUpdateGroup handles PUT /api/groups/{id}.
func (h *Handler) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}

	group, err := h.db.GetDeviceGroup(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}

	var req UpdateGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != "" {
		group.Name = req.Name
	}
	if req.ParentID != nil {
		group.ParentID = *req.ParentID
		// Recalculate level
		if *req.ParentID > 0 {
			parent, err := h.db.GetDeviceGroup(r.Context(), *req.ParentID)
			if err == nil {
				group.Level = parent.Level + 1
			}
		} else {
			group.Level = 0
		}
	}
	if req.SortOrder != nil {
		group.SortOrder = *req.SortOrder
	}

	if err := h.db.UpdateDeviceGroup(r.Context(), group); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update group")
		return
	}

	h.logSuccess(r, "group.update", "group", strconv.FormatInt(id, 10), "device group updated", map[string]any{"name": group.Name})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group":  group,
		"status": "ok",
	})
}

// handleDeleteGroup handles DELETE /api/groups/{id}.
func (h *Handler) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}

	if err := h.db.DeleteDeviceGroup(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete group")
		return
	}

	h.logSuccess(r, "group.delete", "group", idStr, "device group deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleListGroupChannels handles GET /api/groups/{id}/channels.
func (h *Handler) handleListGroupChannels(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}

	channels, err := h.db.ListGroupChannels(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}
	if channels == nil {
		channels = []storage.DeviceGroupChannelDetail{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"channels": channels})
}

// AddGroupChannelRequest is the request body for adding a channel to a group.
type AddGroupChannelRequest struct {
	DeviceID  string `json:"device_id"`
	ChannelID string `json:"channel_id"`
}

// handleAddGroupChannel handles POST /api/groups/{id}/channels.
func (h *Handler) handleAddGroupChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	groupID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}

	var req AddGroupChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}

	if err := h.db.AddGroupChannel(r.Context(), groupID, req.DeviceID, req.ChannelID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add channel to group")
		return
	}

	h.logSuccess(r, "group.channel.add", "group", idStr, "channel added to group", map[string]any{"device_id": req.DeviceID, "channel_id": req.ChannelID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RemoveGroupChannelRequest is the request body for removing a channel from a group.
type RemoveGroupChannelRequest struct {
	DeviceID  string `json:"device_id"`
	ChannelID string `json:"channel_id"`
}

// handleRemoveGroupChannel handles DELETE /api/groups/{id}/channels.
func (h *Handler) handleRemoveGroupChannel(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	groupID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}

	var req RemoveGroupChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.DeviceID == "" || req.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "device_id and channel_id are required")
		return
	}

	if err := h.db.RemoveGroupChannel(r.Context(), groupID, req.DeviceID, req.ChannelID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove channel from group")
		return
	}

	h.logSuccess(r, "group.channel.remove", "group", idStr, "channel removed from group", map[string]any{"device_id": req.DeviceID, "channel_id": req.ChannelID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GroupTreeNode represents a group with its children for tree structure.
type GroupTreeNode struct {
	ID        int64            `json:"id"`
	Name      string           `json:"name"`
	ParentID  int64            `json:"parent_id"`
	Level     int              `json:"level"`
	SortOrder int              `json:"sort_order"`
	Children  []GroupTreeNode  `json:"children,omitempty"`
	Stats     *GroupStats      `json:"stats,omitempty"`
}

// GroupStats represents channel statistics for a group.
type GroupStats struct {
	Total  int `json:"total"`
	Online int `json:"online"`
}

// handleGetGroupTree handles GET /api/groups/tree.
func (h *Handler) handleGetGroupTree(w http.ResponseWriter, r *http.Request) {
	groups, err := h.db.ListDeviceGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}

	// Build tree
	tree := buildGroupTree(groups, 0, r.URL.Query().Get("stats") == "true")

	writeJSON(w, http.StatusOK, map[string]interface{}{"tree": tree})
}

// buildGroupTree builds a tree structure from a flat list of groups.
func buildGroupTree(groups []storage.DeviceGroup, parentID int64, includeStats bool) []GroupTreeNode {
	var nodes []GroupTreeNode
	for _, g := range groups {
		if g.ParentID == parentID {
			node := GroupTreeNode{
				ID:        g.ID,
				Name:      g.Name,
				ParentID:  g.ParentID,
				Level:     g.Level,
				SortOrder: g.SortOrder,
			}
			// Recursively build children
			children := buildGroupTree(groups, g.ID, includeStats)
			if len(children) > 0 {
				node.Children = children
			}
			nodes = append(nodes, node)
		}
	}
	return nodes
}
