package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// ==================== 业务分组 / 虚拟组织 API ====================

// GroupTreeNodeGB 分组树节点 (含通道叶子)。
type GroupTreeNodeGB struct {
	Type          string                  `json:"type"` // "group" | "channel"
	ID            int64                   `json:"id,omitempty"`
	DeviceID      string                  `json:"device_id"`
	Name          string                  `json:"name"`
	ParentID      int64                   `json:"parent_id,omitempty"`
	BusinessGroup string                  `json:"business_group,omitempty"`
	IsLeaf        bool                    `json:"is_leaf"`
	ChannelID     string                  `json:"channel_id,omitempty"`
	Status        string                  `json:"status,omitempty"`
	Channel       *storage.GBChannelBrief `json:"channel,omitempty"`
}

// handleGBGroupTree GET /api/gb28181/groups/tree?parent=&has_channel=
func (h *Handler) handleGBGroupTree(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var parentID int64
	if p := r.URL.Query().Get("parent"); p != "" {
		parentID, _ = strconv.ParseInt(p, 10, 64)
	}
	hasChannel := r.URL.Query().Get("has_channel") == "true"

	groups, err := h.db.ListGBGroupsByParent(ctx, parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}
	nodes := make([]GroupTreeNodeGB, 0, len(groups))
	for _, g := range groups {
		nodes = append(nodes, GroupTreeNodeGB{
			Type:          "group",
			ID:            g.ID,
			DeviceID:      g.DeviceID,
			Name:          g.Name,
			ParentID:      g.ParentID,
			BusinessGroup: g.BusinessGroup,
			IsLeaf:        false,
		})
	}

	if hasChannel && parentID > 0 {
		if parent, err := h.db.GetGBGroup(ctx, parentID); err == nil && parent != nil {
			channels, err := h.db.ListChannelsByParentID(ctx, parent.DeviceID)
			if err == nil {
				for i := range channels {
					ch := channels[i]
					nodes = append(nodes, GroupTreeNodeGB{
						Type:      "channel",
						DeviceID:  ch.DeviceID,
						ChannelID: ch.ChannelID,
						Name:      ch.Name,
						Status:    ch.Status,
						IsLeaf:    true,
						Channel:   &ch,
					})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// CreateGroupRequestGB 新建分组请求 (215业务分组或216虚拟组织)。
type CreateGroupRequestGB struct {
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	ParentDeviceID string `json:"parent_device_id"`
	BusinessGroup  string `json:"business_group"`
	CivilCode      string `json:"civil_code"`
}

// handleGBGroupCreate POST /api/gb28181/groups
func (h *Handler) handleGBGroupCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req CreateGroupRequestGB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	code := gb28181.DecodeGBCode(req.DeviceID)
	if code == nil {
		writeError(w, http.StatusBadRequest, "device_id must be a 20-digit GB code")
		return
	}

	group := &storage.GBGroup{
		DeviceID:  req.DeviceID,
		Name:      req.Name,
		CivilCode: req.CivilCode,
	}

	switch code.TypeCode {
	case gb28181.GBTypeBusinessGroup: // 215: 业务分组顶层
		group.BusinessGroup = req.DeviceID
	case gb28181.GBTypeVirtualOrg: // 216: 虚拟组织
		if req.BusinessGroup == "" {
			writeError(w, http.StatusBadRequest, "business_group is required for virtual organization")
			return
		}
		bg, _ := h.db.GetGBBusinessGroup(ctx, req.BusinessGroup)
		if bg == nil {
			writeError(w, http.StatusBadRequest, "business group not found")
			return
		}
		group.BusinessGroup = req.BusinessGroup
		group.ParentDeviceID = req.ParentDeviceID
		if req.ParentDeviceID != "" {
			parent, _ := h.db.GetGBGroupByDeviceID(ctx, req.ParentDeviceID, req.BusinessGroup)
			if parent == nil {
				writeError(w, http.StatusBadRequest, "parent group not found")
				return
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "device_id type code must be 215 (business group) or 216 (virtual organization)")
		return
	}

	if dup, _ := h.db.GetGBGroupByDeviceID(ctx, group.DeviceID, group.BusinessGroup); dup != nil {
		writeError(w, http.StatusConflict, "group already exists")
		return
	}
	if _, err := h.db.CreateGBGroup(ctx, group); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create group")
		return
	}
	h.logSuccess(r, "gb28181.group.create", "gb28181", group.DeviceID, "GB28181 group created", map[string]any{"name": group.Name})
	writeJSON(w, http.StatusOK, map[string]any{"group": group, "status": "ok"})
}

// UpdateGroupRequestGB 更新分组请求 (仅支持改名)。
type UpdateGroupRequestGB struct {
	Name string `json:"name"`
}

// handleGBGroupUpdate PUT /api/gb28181/groups/{id}
func (h *Handler) handleGBGroupUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	group, err := h.db.GetGBGroup(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	var req UpdateGroupRequestGB
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != "" {
		group.Name = req.Name
	}
	if err := h.db.UpdateGBGroup(ctx, group); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update group")
		return
	}
	h.logSuccess(r, "gb28181.group.update", "gb28181", group.DeviceID, "GB28181 group updated", map[string]any{"name": group.Name})
	writeJSON(w, http.StatusOK, map[string]any{"group": group, "status": "ok"})
}

// handleGBGroupDelete DELETE /api/gb28181/groups/{id}
// 215: 删除整个业务分组下所有节点; 216: 级联删除子树。通道挂接一并清除。
func (h *Handler) handleGBGroupDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid group id")
		return
	}
	group, err := h.db.GetGBGroup(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}

	var toDelete []storage.GBGroup
	if gb28181.IsBusinessGroup(group.DeviceID) {
		all, err := h.db.ListGBGroupsByBusinessGroup(ctx, group.DeviceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve business group")
			return
		}
		toDelete = all
		_ = h.db.DetachChannelsFromBusinessGroup(ctx, group.DeviceID)
	} else {
		descendants, err := h.db.GetGBGroupDescendants(ctx, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve children")
			return
		}
		toDelete = append([]storage.GBGroup{*group}, descendants...)
		for _, g := range toDelete {
			_ = h.db.DetachChannelsFromGroupByParent(ctx, g.DeviceID)
		}
	}

	ids := make([]int64, 0, len(toDelete))
	for _, g := range toDelete {
		ids = append(ids, g.ID)
	}
	if err := h.db.DeleteGBGroups(ctx, ids); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete group")
		return
	}
	h.logSuccess(r, "gb28181.group.delete", "gb28181", group.DeviceID, "GB28181 group deleted", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ChannelGroupRequest 通道挂接/解绑业务分组请求。
type ChannelGroupRequest struct {
	ParentID      string               `json:"parent_id"`
	BusinessGroup string               `json:"business_group"`
	Channels      []storage.ChannelKey `json:"channels"`
}

// handleGBChannelAttachGroup POST /api/gb28181/channels/group
func (h *Handler) handleGBChannelAttachGroup(w http.ResponseWriter, r *http.Request) {
	var req ChannelGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ParentID == "" || req.BusinessGroup == "" || len(req.Channels) == 0 {
		writeError(w, http.StatusBadRequest, "parent_id, business_group and channels are required")
		return
	}
	if err := h.db.AttachChannelsToGroup(r.Context(), req.ParentID, req.BusinessGroup, req.Channels); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to attach channels")
		return
	}
	h.logSuccess(r, "gb28181.channel.attach_group", "gb28181", req.ParentID, "channels attached to group", map[string]any{"count": len(req.Channels), "business_group": req.BusinessGroup})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGBChannelDetachGroup DELETE /api/gb28181/channels/group
func (h *Handler) handleGBChannelDetachGroup(w http.ResponseWriter, r *http.Request) {
	var req ChannelGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Channels) == 0 {
		writeError(w, http.StatusBadRequest, "channels are required")
		return
	}
	if err := h.db.DetachChannelsFromGroup(r.Context(), req.Channels); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach channels")
		return
	}
	h.logSuccess(r, "gb28181.channel.detach_group", "gb28181", "", "channels detached from group", map[string]any{"count": len(req.Channels)})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
