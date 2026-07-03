package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/civilcode"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// ==================== 行政区划 API ====================

// RegionTreeNode 区划树节点 (含通道叶子)。
type RegionTreeNode struct {
	Type      string                  `json:"type"` // "region" | "channel"
	ID        int64                   `json:"id,omitempty"`
	DeviceID  string                  `json:"device_id"`
	Name      string                  `json:"name"`
	ParentID  int64                   `json:"parent_id,omitempty"`
	IsLeaf    bool                    `json:"is_leaf"`
	ChannelID string                  `json:"channel_id,omitempty"` // type=channel 时有效
	Status    string                  `json:"status,omitempty"`
	Channel   *storage.GBChannelBrief `json:"channel,omitempty"`
}

// handleGBRegionTree GET /api/gb28181/regions/tree?parent=&has_channel=
func (h *Handler) handleGBRegionTree(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var parentID int64
	if p := r.URL.Query().Get("parent"); p != "" {
		parentID, _ = strconv.ParseInt(p, 10, 64)
	}
	hasChannel := r.URL.Query().Get("has_channel") == "true"

	regions, err := h.db.ListGBRegionsByParent(ctx, parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list regions")
		return
	}
	nodes := make([]RegionTreeNode, 0, len(regions))
	for _, rg := range regions {
		nodes = append(nodes, RegionTreeNode{
			Type:     "region",
			ID:       rg.ID,
			DeviceID: rg.DeviceID,
			Name:     rg.Name,
			ParentID: rg.ParentID,
			IsLeaf:   false,
		})
	}

	// 展开父节点下挂接的通道作为叶子
	if hasChannel && parentID > 0 {
		if parent, err := h.db.GetGBRegion(ctx, parentID); err == nil && parent != nil {
			channels, err := h.db.ListChannelsByCivilCode(ctx, parent.DeviceID)
			if err == nil {
				for i := range channels {
					ch := channels[i]
					nodes = append(nodes, RegionTreeNode{
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

// CreateRegionRequest 新建区划请求。
type CreateRegionRequest struct {
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	ParentDeviceID string `json:"parent_device_id"`
}

// handleGBRegionCreate POST /api/gb28181/regions
func (h *Handler) handleGBRegionCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRegionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DeviceID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "device_id and name are required")
		return
	}
	if existing, _ := h.db.GetGBRegionByDeviceID(r.Context(), req.DeviceID); existing != nil {
		writeError(w, http.StatusConflict, "region already exists")
		return
	}
	region := &storage.GBRegion{
		DeviceID:       req.DeviceID,
		Name:           req.Name,
		ParentDeviceID: req.ParentDeviceID,
	}
	if _, err := h.db.CreateGBRegion(r.Context(), region); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create region")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": region, "status": "ok"})
}

// UpdateRegionRequest 更新区划请求。
type UpdateRegionRequest struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}

// handleGBRegionUpdate PUT /api/gb28181/regions/{id}
func (h *Handler) handleGBRegionUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid region id")
		return
	}
	region, err := h.db.GetGBRegion(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "region not found")
		return
	}
	var req UpdateRegionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	oldDeviceID := region.DeviceID
	if req.Name != "" {
		region.Name = req.Name
	}
	if req.DeviceID != "" && req.DeviceID != region.DeviceID {
		if dup, _ := h.db.GetGBRegionByDeviceID(ctx, req.DeviceID); dup != nil {
			writeError(w, http.StatusConflict, "region already exists")
			return
		}
		region.DeviceID = req.DeviceID
	}
	if err := h.db.UpdateGBRegion(ctx, region); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update region")
		return
	}
	// 编码变化: 同步引用旧编码的通道
	if region.DeviceID != oldDeviceID {
		channels, _ := h.db.ListChannelsByCivilCode(ctx, oldDeviceID)
		keys := make([]storage.ChannelKey, 0, len(channels))
		for _, ch := range channels {
			keys = append(keys, storage.ChannelKey{DeviceID: ch.DeviceID, ChannelID: ch.ChannelID})
		}
		_ = h.db.AttachChannelsToRegion(ctx, region.DeviceID, keys)
	}
	writeJSON(w, http.StatusOK, map[string]any{"region": region, "status": "ok"})
}

// handleGBRegionDelete DELETE /api/gb28181/regions/{id}
// 级联删除子节点, 并将引用这些区划的通道 civil_code 置空。
func (h *Handler) handleGBRegionDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid region id")
		return
	}
	region, err := h.db.GetGBRegion(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "region not found")
		return
	}
	descendants, err := h.db.GetGBRegionDescendants(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve children")
		return
	}
	ids := []int64{region.ID}
	all := append([]storage.GBRegion{*region}, descendants...)
	for _, d := range descendants {
		ids = append(ids, d.ID)
	}
	// 通道置空
	for _, rg := range all {
		_ = h.db.DetachChannelsFromRegionByCode(ctx, rg.DeviceID)
	}
	if err := h.db.DeleteGBRegions(ctx, ids); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete region")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGBRegionSync POST /api/gb28181/regions/sync
func (h *Handler) handleGBRegionSync(w http.ResponseWriter, r *http.Request) {
	count, err := h.db.SyncRegionsFromChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync regions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": count, "status": "ok"})
}

// ChannelRegionRequest 通道挂接/解绑区划请求。
type ChannelRegionRequest struct {
	CivilCode string               `json:"civil_code"`
	Channels  []storage.ChannelKey `json:"channels"`
}

// handleGBChannelAttachRegion POST /api/gb28181/channels/region
func (h *Handler) handleGBChannelAttachRegion(w http.ResponseWriter, r *http.Request) {
	var req ChannelRegionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CivilCode == "" || len(req.Channels) == 0 {
		writeError(w, http.StatusBadRequest, "civil_code and channels are required")
		return
	}
	if err := h.db.AttachChannelsToRegion(r.Context(), req.CivilCode, req.Channels); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to attach channels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGBChannelDetachRegion DELETE /api/gb28181/channels/region
func (h *Handler) handleGBChannelDetachRegion(w http.ResponseWriter, r *http.Request) {
	var req ChannelRegionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Channels) == 0 {
		writeError(w, http.StatusBadRequest, "channels are required")
		return
	}
	if err := h.db.DetachChannelsFromRegion(r.Context(), req.Channels); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach channels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGBCivilCodeCandidates GET /api/gb28181/regions/candidates?parent=
// 从内置 GB/T 2260 标准库返回某父级下的候选区划 (供新建节点时挑选)。
func (h *Handler) handleGBCivilCodeCandidates(w http.ResponseWriter, r *http.Request) {
	parent := r.URL.Query().Get("parent")
	children := civilcode.Children(parent)
	if children == nil {
		children = []civilcode.Code{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": children})
}

// handleGBCivilCodeDescription GET /api/gb28181/regions/description?civil_code=
// 返回某编码的完整层级描述，如 "广东省/广州市/天河区"。
func (h *Handler) handleGBCivilCodeDescription(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("civil_code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "civil_code is required")
		return
	}
	c, ok := civilcode.Get(code)
	name := ""
	if ok {
		name = c.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"civil_code":  code,
		"name":        name,
		"description": civilcode.Description(code),
		"found":       ok,
	})
}

// AddByCivilCodeRequest 按标准编码新增区划 (含父链)。
type AddByCivilCodeRequest struct {
	CivilCode string `json:"civil_code"`
}

// handleGBRegionAddByCivilCode POST /api/gb28181/regions/by-civil-code
func (h *Handler) handleGBRegionAddByCivilCode(w http.ResponseWriter, r *http.Request) {
	var req AddByCivilCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CivilCode == "" {
		writeError(w, http.StatusBadRequest, "civil_code is required")
		return
	}
	if _, ok := civilcode.Get(req.CivilCode); !ok {
		writeError(w, http.StatusBadRequest, "civil_code not found in standard table")
		return
	}
	created, err := h.db.AddRegionByCivilCode(r.Context(), req.CivilCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add region")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "status": "ok"})
}

// handleGBCatalogChannels GET /api/gb28181/catalog/channels?q=&unassigned_region=&unassigned_group=
// 目录页右侧通道列表 (供挂接选择)。
func (h *Handler) handleGBCatalogChannels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	unassignedRegion := r.URL.Query().Get("unassigned_region") == "true"
	unassignedGroup := r.URL.Query().Get("unassigned_group") == "true"
	channels, err := h.db.SearchChannelsBrief(r.Context(), q, unassignedRegion, unassignedGroup)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}
	if channels == nil {
		channels = []storage.GBChannelBrief{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}
