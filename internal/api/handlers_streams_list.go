package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

const (
	streamListDefaultLimit    = 20
	streamListMaxLimit        = 100
	streamIdleHistoryMaxAge   = 7 * 24 * time.Hour
	streamIdleHistoryMaxItems = 200
)

var pushStreamIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// normalizeStreamID decodes percent-encoded stream IDs from URL path params.
// GB28181 stream IDs contain ":" which may arrive as "%3A" or "%253A" when over-encoded.
func normalizeStreamID(streamID string) string {
	for range 3 {
		decoded, err := url.PathUnescape(streamID)
		if err != nil || decoded == streamID {
			break
		}
		streamID = decoded
	}
	return streamID
}

func streamIDFromRequest(r *http.Request) string {
	return normalizeStreamID(chi.URLParam(r, "stream_id"))
}

type streamListResponse struct {
	Streams []streamSummary `json:"streams"`
	Total   int             `json:"total"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
}

type streamSummary struct {
	Engine         string                `json:"engine"`
	StreamID       string                `json:"stream_id"`
	Name           string                `json:"name,omitempty"`
	AppName        string                `json:"app_name,omitempty"`
	Managed        bool                  `json:"managed"`
	ManagementType string                `json:"management_type,omitempty"`
	CameraID       string                `json:"camera_id,omitempty"`
	CameraName     string                `json:"camera_name,omitempty"`
	SourceType     string                `json:"source_type"`
	Active         bool                  `json:"active"`
	GB28181Playing bool                  `json:"gb28181_playing,omitempty"`
	Publisher      *cameraSessionStatus  `json:"publisher,omitempty"`
	Subscribers    []cameraSessionStatus `json:"subscribers,omitempty"`
	VideoCodec     string                `json:"video_codec,omitempty"`
	AudioCodec     string                `json:"audio_codec,omitempty"`
	InFPS          float64               `json:"in_fps,omitempty"`
	LastFrameTime  *time.Time            `json:"last_frame_time,omitempty"`
	PlayURLs       []streamPlayURL       `json:"play_urls,omitempty"`
	IngestURLs     []streamPlayURL       `json:"ingest_urls,omitempty"`
	SourceURL      string                `json:"source_url,omitempty"`
}

type streamPlayURL struct {
	Protocol string `json:"protocol"`
	URL      string `json:"url"`
	Backend  string `json:"backend"`
}

func (h *Handler) handleListStreams(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream listing unavailable")
		return
	}

	streams, err := h.mediaEngine.ListStreams(r.Context())
	if err != nil {
		logger.Error("list streams failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list streams")
		return
	}

	cameraRows, err := h.db.ListCameras(r.Context())
	if err != nil {
		logger.Error("list cameras for streams failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load camera mapping")
		return
	}

	cameraByID := make(map[string]string, len(cameraRows))
	for _, cam := range cameraRows {
		cameraByID[cam.ID] = cam.Name
	}

	bindings, err := h.db.ListStreamBindings(r.Context())
	if err != nil {
		logger.Error("list stream bindings failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load stream bindings")
		return
	}
	bindingByStreamID := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		bindingByStreamID[binding.StreamID] = binding.CameraID
	}

	items := h.buildMergedStreamList(r.Context(), streams, cameraRows, bindingByStreamID, cameraByID, h.ownStreamByCamera(r.Context()))

	search, managedFilter, limit, offset := parseStreamListParams(r)
	filtered := filterStreamSummaries(items, search, managedFilter)
	page, total := paginateStreamSummaries(filtered, limit, offset)

	writeJSON(w, http.StatusOK, streamListResponse{
		Streams: page,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	})
}

func parseStreamListParams(r *http.Request) (search string, managedFilter *bool, limit, offset int) {
	search = strings.TrimSpace(r.URL.Query().Get("q"))
	if v := strings.TrimSpace(r.URL.Query().Get("managed")); v != "" {
		managed := v == "true" || v == "1"
		managedFilter = &managed
	}

	limit = streamListDefaultLimit
	offset = 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > streamListMaxLimit {
		limit = streamListMaxLimit
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return search, managedFilter, limit, offset
}

func streamDisplayName(item streamSummary) string {
	if item.Name != "" {
		return item.Name
	}
	if item.Managed && item.CameraName != "" {
		return item.CameraName
	}
	return item.StreamID
}

func normalizeStreamDisplayName(name, streamID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = streamID
	}
	if runes := []rune(name); len(runes) > 128 {
		name = string(runes[:128])
	}
	return name
}

func sortStreamSummaries(items []streamSummary) {
	slices.SortFunc(items, func(a, b streamSummary) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		if a.Managed != b.Managed {
			if a.Managed {
				return -1
			}
			return 1
		}
		displayA := streamDisplayName(a)
		displayB := streamDisplayName(b)
		if cmp := strings.Compare(displayA, displayB); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.StreamID, b.StreamID)
	})
}

func (h *Handler) resolveStreamManagement(
	streamID string,
	info *media.StreamInfo,
	bindingByStreamID, cameraByID, ownStreamByCamera map[string]string,
) (cameraID, cameraName, managementType string, managed bool) {
	boundCameraID, ok := bindingByStreamID[streamID]
	if !ok {
		return "", "", "", false
	}
	return boundCameraID, cameraByID[boundCameraID], streamManagementType(ownStreamByCamera[boundCameraID], streamID, info), true
}

// streamManagementType classifies a bound stream.
// The camera's own ingest stream is "camera" (or "promoted" for push sources);
// a foreign stream bound onto a camera is "bound".
func streamManagementType(ownStream, streamID string, info *media.StreamInfo) string {
	if ownStream != streamID {
		return "bound"
	}
	if info != nil && inferCameraManagementType(*info) == "promoted" {
		return "promoted"
	}
	return "camera"
}

// ownStreamByCamera maps camera ID to the stream it ingests from.
func (h *Handler) ownStreamByCamera(ctx context.Context) map[string]string {
	out := make(map[string]string)
	if h.db == nil {
		return out
	}
	configs, err := h.db.ListCameraConfigs(ctx)
	if err != nil {
		logger.Warn("list camera configs for stream ownership failed", "error", err)
		return out
	}
	for _, c := range configs {
		if s := strings.TrimSpace(c.StreamID); s != "" {
			out[c.ID] = s
		}
	}
	return out
}

func (h *Handler) streamSummaryFromMediaInfo(
	ctx context.Context,
	info media.StreamInfo,
	bindingByStreamID, cameraByID, ownStreamByCamera map[string]string,
) streamSummary {
	cameraID, cameraName, managementType, managed := h.resolveStreamManagement(info.StreamID, &info, bindingByStreamID, cameraByID, ownStreamByCamera)
	appName := info.AppName
	if appName == "" {
		appName = "live"
	}
	item := streamSummary{
		Engine:         "lalmax",
		StreamID:       info.StreamID,
		AppName:        info.AppName,
		Managed:        managed,
		ManagementType: managementType,
		SourceType:     inferStreamSourceType(info, managed),
		Active:         info.Active,
		Publisher:      sessionStatusFromInfo(info.Publisher),
		Subscribers:    sessionStatusesFromInfo(info.Subscribers),
		VideoCodec:     info.VideoCodec,
		AudioCodec:     info.AudioCodec,
		InFPS:          info.InFPS,
		LastFrameTime:  timePointer(info.LastFrameTime),
	}
	if managed {
		item.CameraID = cameraID
		item.CameraName = cameraName
	}
	h.attachStreamURLs(ctx, &item)
	h.applyGB28181PlayingState(&item)
	return item
}

func (h *Handler) applyGB28181PlayingState(item *streamSummary) {
	if h.gb28181Svr == nil || item == nil {
		return
	}
	if !h.gb28181Svr.IsStreamPlaying(item.StreamID) {
		return
	}
	item.GB28181Playing = true
	item.Active = true
	if item.SourceType == "camera" {
		item.SourceType = "gb28181"
	}
}

func (h *Handler) buildMergedStreamList(
	ctx context.Context,
	liveStreams []media.StreamInfo,
	cameraRows []storage.CameraRow,
	bindingByStreamID, cameraByID, ownStreamByCamera map[string]string,
) []streamSummary {
	createdByID := h.createdStreamsByID(ctx)
	byID := make(map[string]streamSummary, len(liveStreams)+len(cameraRows)+len(createdByID))

	for _, info := range liveStreams {
		item := h.streamSummaryFromMediaInfo(ctx, info, bindingByStreamID, cameraByID, ownStreamByCamera)
		applyCreatedStream(&item, createdByID)
		byID[info.StreamID] = item
	}

	for streamID, cameraID := range bindingByStreamID {
		if _, ok := byID[streamID]; ok {
			continue
		}
		cameraName := cameraByID[cameraID]
		if cameraName == "" {
			continue
		}
		byID[streamID] = streamSummary{
			Engine:         "lalmax",
			StreamID:       streamID,
			AppName:        "live",
			Managed:        true,
			ManagementType: "bound",
			CameraID:       cameraID,
			CameraName:     cameraName,
			SourceType:     "camera",
			Active:         false,
		}
		item := byID[streamID]
		h.attachStreamURLs(ctx, &item)
		byID[streamID] = item
	}

	since := time.Now().Add(-streamIdleHistoryMaxAge)
	recent, err := h.db.ListRecentStreamSnapshots(ctx, since, streamIdleHistoryMaxItems)
	if err != nil {
		logger.Error("list recent stream snapshots failed", "err", err)
	} else {
		for _, snap := range recent {
			if _, ok := byID[snap.StreamID]; ok {
				continue
			}
			if _, isCamera := cameraByID[snap.StreamID]; isCamera {
				continue
			}
			if _, isBound := bindingByStreamID[snap.StreamID]; isBound {
				continue
			}
			appName := snap.AppName
			if appName == "" {
				appName = "live"
			}
			lastSeen := snap.StartedAt
			if snap.EndedAt != nil {
				lastSeen = *snap.EndedAt
			}
			byID[snap.StreamID] = streamSummary{
				Engine:        "lalmax",
				StreamID:      snap.StreamID,
				AppName:       appName,
				SourceType:    inferStreamSourceTypeForID(snap.StreamID, snap.Protocol),
				Active:        false,
				LastFrameTime: timePointer(lastSeen),
			}
			item := byID[snap.StreamID]
			h.attachStreamURLs(ctx, &item)
			applyCreatedStream(&item, createdByID)
			byID[snap.StreamID] = item
		}
	}

	for id, created := range createdByID {
		if _, ok := byID[id]; ok {
			continue
		}
		if _, isCamera := cameraByID[id]; isCamera {
			continue
		}
		byID[id] = h.summaryFromCreated(ctx, created)
	}

	items := make([]streamSummary, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sortStreamSummaries(items)
	return items
}

func filterStreamSummaries(items []streamSummary, search string, managedFilter *bool) []streamSummary {
	if search == "" && managedFilter == nil {
		return items
	}

	q := strings.ToLower(strings.TrimSpace(search))
	filtered := make([]streamSummary, 0, len(items))
	for _, item := range items {
		if managedFilter != nil && item.Managed != *managedFilter {
			continue
		}
		if q != "" && !streamSummaryMatchesSearch(item, q) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func streamSummaryMatchesSearch(item streamSummary, q string) bool {
	for _, field := range []string{item.StreamID, item.Name, item.CameraName, item.CameraID, item.AppName} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

func paginateStreamSummaries(items []streamSummary, limit, offset int) ([]streamSummary, int) {
	total := len(items)
	if offset >= total {
		return nil, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return items[offset:end], total
}

func pushSourceTypeFromProtocol(protocol string) string {
	switch strings.ToLower(protocol) {
	case "rtmp":
		return "rtmp_push"
	case "srt":
		return "srt_push"
	case "whip", "webrtc", "whip_push":
		return "whip_push"
	default:
		return ""
	}
}

// inferCustomizePushSource maps lal "customize" ingest sessions (WHIP/SRT via AddCustomizePubSession).
func inferCustomizePushSource(info *media.StreamInfo, remoteAddr string) string {
	if info != nil && strings.EqualFold(info.AudioCodec, "opus") {
		return "whip_push"
	}
	if strings.TrimSpace(remoteAddr) == "" {
		return "whip_push"
	}
	return "srt_push"
}

func inferStreamSourceType(info media.StreamInfo, managed bool) string {
	// IPTV channels are pulled by the NVR's HLS client and published into the
	// embedded media engine. Their customize publisher session is an internal
	// implementation detail, not an external WHIP ingest.
	if isIPTVStreamID(info.StreamID) {
		return "iptv"
	}
	if info.Publisher != nil {
		if src := inferStreamSourceTypeFromProtocol(info.Publisher.Protocol); src == "gb28181" {
			return src
		}
	}
	if managed {
		return "camera"
	}
	if info.Publisher != nil {
		if src := pushSourceTypeFromProtocol(info.Publisher.Protocol); src != "" {
			return src
		}
		if strings.EqualFold(info.Publisher.Protocol, "customize") {
			return inferCustomizePushSource(&info, info.Publisher.Remote)
		}
		return inferStreamSourceTypeFromProtocol(info.Publisher.Protocol)
	}
	if info.VideoCodec != "" {
		return inferCustomizePushSource(&info, "")
	}
	return "stream"
}

func isIPTVStreamID(streamID string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(streamID)), "iptv_")
}

func inferStreamSourceTypeForID(streamID, protocol string) string {
	if isIPTVStreamID(streamID) {
		return "iptv"
	}
	return inferStreamSourceTypeFromProtocol(protocol)
}

func inferStreamSourceTypeFromProtocol(protocol string) string {
	if src := pushSourceTypeFromProtocol(protocol); src != "" {
		return src
	}
	switch strings.ToLower(protocol) {
	case "rtsp", "relay_pull", "pull":
		return "relay_pull"
	case "rtp", "gb28181", "ps":
		return "gb28181"
	case "customize":
		return "stream"
	default:
		return "stream"
	}
}

func (h *Handler) inferPromoteSourceType(ctx context.Context, streamID string, info *media.StreamInfo) string {
	if info != nil && info.Publisher != nil {
		if src := pushSourceTypeFromProtocol(info.Publisher.Protocol); src != "" {
			return src
		}
		if strings.EqualFold(info.Publisher.Protocol, "customize") {
			return inferCustomizePushSource(info, info.Publisher.Remote)
		}
		switch strings.ToLower(info.Publisher.Protocol) {
		case "rtsp", "relay_pull", "pull":
			return "relay_pull"
		}
	}
	if info != nil && info.VideoCodec != "" {
		return inferCustomizePushSource(info, "")
	}
	if h.db != nil {
		histories, _, err := h.db.ListStreamHistory(ctx, streamID, 5, 0)
		if err == nil {
			for _, hist := range histories {
				if src := pushSourceTypeFromProtocol(hist.Protocol); src != "" {
					return src
				}
				if strings.EqualFold(hist.Protocol, "customize") {
					return inferCustomizePushSource(info, hist.RemoteAddr)
				}
			}
		}
	}
	return "rtmp_push"
}

func inferCameraManagementType(info media.StreamInfo) string {
	if info.Publisher == nil {
		return "camera"
	}
	switch strings.ToLower(info.Publisher.Protocol) {
	case "rtmp", "srt", "whip", "webrtc", "whip_push":
		return "promoted"
	case "customize":
		if inferCustomizePushSource(&info, info.Publisher.Remote) != "" {
			return "promoted"
		}
		return "camera"
	default:
		return "camera"
	}
}

func sessionStatusFromInfo(session *media.SessionInfo) *cameraSessionStatus {
	if session == nil {
		return nil
	}
	return &cameraSessionStatus{
		SessionID:         session.SessionID,
		Protocol:          session.Protocol,
		Remote:            session.Remote,
		BitrateKbits:      session.BitrateKbits,
		ReadBitrateKbits:  session.ReadBitrateKbits,
		WriteBitrateKbits: session.WriteBitrateKbits,
	}
}

func sessionStatusesFromInfo(sessions []media.SessionInfo) []cameraSessionStatus {
	if len(sessions) == 0 {
		return nil
	}
	items := make([]cameraSessionStatus, 0, len(sessions))
	for _, session := range sessions {
		if media.IsInternalRecorderSession(session.Protocol, session.SessionID) {
			continue
		}
		items = append(items, cameraSessionStatus{
			SessionID:         session.SessionID,
			Protocol:          session.Protocol,
			Remote:            session.Remote,
			BitrateKbits:      session.BitrateKbits,
			ReadBitrateKbits:  session.ReadBitrateKbits,
			WriteBitrateKbits: session.WriteBitrateKbits,
		})
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func timePointer(v time.Time) *time.Time {
	if v.IsZero() {
		return nil
	}
	return &v
}

func (h *Handler) attachStreamURLs(ctx context.Context, item *streamSummary) {
	if item == nil {
		return
	}
	appName := item.AppName
	if appName == "" {
		appName = "live"
	}
	item.PlayURLs = h.buildStreamURLs(ctx, item.StreamID, appName, []string{"hls", "ll-hls", "flv", "ws-flv", "webrtc", "fmp4", "rtmp", "rtsp"})
	if !item.Managed {
		if item.SourceType == "relay_pull" {
			return
		}
		item.IngestURLs = h.buildIngestURLs(ctx, item.StreamID, appName)
		return
	}
	if h.config == nil || h.config.IsWHIPEnabled() {
		item.IngestURLs = h.buildStreamURLs(ctx, item.StreamID, appName, []string{"whip"})
	}
}

// externalIngestProtocols lists publish protocols advertised for an external push stream.
// A nil config keeps the previous WHIP-only advertisement used by tests.
func (h *Handler) externalIngestProtocols() []string {
	if h.config == nil {
		return []string{"whip"}
	}
	var protocols []string
	if h.config.RTMP.Enabled != nil && *h.config.RTMP.Enabled {
		protocols = append(protocols, "rtmp")
	}
	if h.config.SRT.Enabled != nil && *h.config.SRT.Enabled {
		protocols = append(protocols, "srt")
	}
	if h.config.IsWHIPEnabled() {
		protocols = append(protocols, "whip")
	}
	return protocols
}

func (h *Handler) buildIngestURLs(ctx context.Context, streamID, appName string) []streamPlayURL {
	out := make([]streamPlayURL, 0, 3)
	for _, protocol := range h.externalIngestProtocols() {
		if protocol == "srt" {
			if u := h.buildSRTIngestURL(ctx, streamID); u != "" {
				out = append(out, streamPlayURL{Protocol: "srt", URL: u, Backend: "lalmax"})
			}
			continue
		}
		out = append(out, h.buildStreamURLs(ctx, streamID, appName, []string{protocol})...)
	}
	return out
}

func (h *Handler) buildSRTIngestURL(ctx context.Context, streamID string) string {
	port := config.DefaultSRTPort
	if h.config != nil && h.config.SRT.Port > 0 {
		port = h.config.SRT.Port
	}
	host := "127.0.0.1"
	if h.mediaEngine != nil {
		play, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
			StreamID: streamID,
			AppName:  "live",
			Protocol: "rtmp",
		})
		if err == nil && play != nil && play.URL != "" {
			if u, parseErr := url.Parse(play.URL); parseErr == nil && u.Hostname() != "" {
				host = u.Hostname()
			}
		}
	}
	u := url.URL{
		Scheme:   "srt",
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		RawQuery: fmt.Sprintf("streamid=#!::h=%s,m=publish", streamID),
	}
	return u.String()
}

func (h *Handler) createdStreamsByID(ctx context.Context) map[string]storage.CreatedStream {
	rows, err := h.db.ListCreatedStreams(ctx)
	if err != nil {
		logger.Error("list created streams failed", "err", err)
		return nil
	}
	out := make(map[string]storage.CreatedStream, len(rows))
	for _, row := range rows {
		out[row.StreamID] = row
	}
	return out
}

func applyCreatedStream(item *streamSummary, created map[string]storage.CreatedStream) {
	if item == nil || created == nil {
		return
	}
	row, ok := created[item.StreamID]
	if !ok || row.StreamID == "" {
		return
	}
	if row.Name != "" {
		item.Name = row.Name
	}
	if row.InputMode == storage.CreatedStreamPull {
		item.SourceType = "relay_pull"
		item.SourceURL = row.SourceURL
		item.IngestURLs = nil
	}
}

func (h *Handler) summaryFromCreated(ctx context.Context, created storage.CreatedStream) streamSummary {
	appName := created.AppName
	if appName == "" {
		appName = "live"
	}
	item := streamSummary{
		Engine:     "lalmax",
		StreamID:   created.StreamID,
		Name:       created.Name,
		AppName:    appName,
		SourceType: "push",
		Active:     false,
	}
	if created.InputMode == storage.CreatedStreamPull {
		item.SourceType = "relay_pull"
		item.SourceURL = created.SourceURL
	}
	h.attachStreamURLs(ctx, &item)
	if created.InputMode == storage.CreatedStreamPull {
		item.IngestURLs = nil
	}
	return item
}

func (h *Handler) buildStreamPlayURLs(ctx context.Context, streamID, appName string) []streamPlayURL {
	return h.buildStreamURLs(ctx, streamID, appName, []string{"hls", "ll-hls", "flv", "ws-flv", "webrtc", "fmp4", "rtmp", "rtsp"})
}

func (h *Handler) buildStreamURLs(ctx context.Context, streamID, appName string, protocols []string) []streamPlayURL {
	if h.mediaEngine == nil {
		return nil
	}
	if appName == "" {
		appName = "live"
	}
	urls := make([]streamPlayURL, 0, len(protocols))
	for _, protocol := range protocols {
		playURL, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
			StreamID: streamID,
			AppName:  appName,
			Protocol: protocol,
		})
		if err != nil || playURL == nil || playURL.URL == "" {
			continue
		}
		urls = append(urls, streamPlayURL{
			Protocol: protocol,
			URL:      playURL.URL,
			Backend:  "lalmax",
		})
	}
	return urls
}

func (h *Handler) handleGetStream(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream listing unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	item, ok, err := h.streamSummaryForID(r.Context(), streamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get stream")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) streamSummaryForID(ctx context.Context, streamID string) (streamSummary, bool, error) {
	info, err := h.mediaEngine.GetStream(ctx, streamID)
	if err != nil {
		logger.Error("get stream failed", "stream_id", streamID, "err", err)
		return streamSummary{}, false, err
	}

	if info != nil {
		cameraRows, err := h.db.ListCameras(ctx)
		if err != nil {
			logger.Error("list cameras for stream failed", "err", err)
			return streamSummary{}, false, err
		}
		cameraByID := make(map[string]string, len(cameraRows))
		for _, cam := range cameraRows {
			cameraByID[cam.ID] = cam.Name
		}
		bindings, err := h.db.ListStreamBindings(ctx)
		if err != nil {
			logger.Error("list stream bindings failed", "err", err)
			return streamSummary{}, false, err
		}
		bindingByStreamID := make(map[string]string, len(bindings))
		for _, binding := range bindings {
			bindingByStreamID[binding.StreamID] = binding.CameraID
		}
		item := h.streamSummaryFromMediaInfo(ctx, *info, bindingByStreamID, cameraByID, h.ownStreamByCamera(ctx))
		if created, err := h.db.GetCreatedStream(ctx, streamID); err != nil {
			logger.Error("get created stream failed", "stream_id", streamID, "err", err)
		} else {
			applyCreatedStream(&item, map[string]storage.CreatedStream{streamID: derefCreated(created)})
		}
		return item, true, nil
	}

	item, ok := h.buildIdleStreamSummary(ctx, streamID)
	return item, ok, nil
}

type updateStreamRequest struct {
	Name string `json:"name"`
}

// handleUpdateStream updates the display name of a stream.
// Unmanaged streams persist the name on created_streams (inserting a row if needed).
// Managed streams update the bound camera name.
func (h *Handler) handleUpdateStream(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	var req updateStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := normalizeStreamDisplayName(req.Name, streamID)
	ctx := r.Context()

	cameraID, err := h.managedCameraIDForStream(ctx, streamID)
	if err != nil {
		logger.Error("lookup managed camera for stream update failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update stream")
		return
	}
	if cameraID != "" {
		if h.camMgr == nil {
			writeError(w, http.StatusServiceUnavailable, "camera manager not available")
			return
		}
		if _, err := h.camMgr.UpdateCamera(ctx, cameraID, camera.CameraUpdate{Name: &name}); err != nil {
			var cnf *model.CameraNotFoundError
			if errors.As(err, &cnf) {
				writeError(w, http.StatusNotFound, "stream not found")
				return
			}
			logger.Error("update managed stream camera name failed", "stream_id", streamID, "camera_id", cameraID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to update stream")
			return
		}
	} else {
		_, ok, err := h.streamSummaryForID(ctx, streamID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update stream")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "stream not found")
			return
		}
		if err := h.db.SetCreatedStreamName(ctx, streamID, name); err != nil {
			logger.Error("set created stream name failed", "stream_id", streamID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to update stream")
			return
		}
	}

	item, ok, err := h.streamSummaryForID(ctx, streamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get stream")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}
	h.logOperation(r, "stream.update", "stream", streamID, "success", "stream display name updated", map[string]any{"name": name})
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) managedCameraIDForStream(ctx context.Context, streamID string) (string, error) {
	binding, err := h.db.GetStreamBinding(ctx, streamID)
	if err != nil {
		return "", err
	}
	if binding != nil {
		return binding.CameraID, nil
	}
	cam, err := h.db.GetCamera(ctx, streamID)
	if err != nil {
		return "", err
	}
	if cam != nil && !cam.Archived {
		return cam.ID, nil
	}
	return "", nil
}

func (h *Handler) buildIdleStreamSummary(ctx context.Context, streamID string) (streamSummary, bool) {
	binding, err := h.db.GetStreamBinding(ctx, streamID)
	if err != nil {
		logger.Error("get stream binding for idle stream failed", "stream_id", streamID, "err", err)
		return streamSummary{}, false
	}
	if binding != nil {
		boundCam, err := h.db.GetCamera(ctx, binding.CameraID)
		if err != nil {
			logger.Error("get bound camera for idle stream failed", "stream_id", streamID, "err", err)
			return streamSummary{}, false
		}
		if boundCam != nil && !boundCam.Archived {
			item := streamSummary{
				Engine:         "lalmax",
				StreamID:       streamID,
				AppName:        "live",
				Managed:        true,
				ManagementType: streamManagementType(h.ownStreamByCamera(ctx)[binding.CameraID], streamID, nil),
				CameraID:       binding.CameraID,
				CameraName:     boundCam.Name,
				SourceType:     "camera",
				Active:         false,
			}
			h.attachStreamURLs(ctx, &item)
			h.applyGB28181PlayingState(&item)
			return item, true
		}
	}

	created, err := h.db.GetCreatedStream(ctx, streamID)
	if err != nil {
		logger.Error("get created stream for idle stream failed", "stream_id", streamID, "err", err)
		return streamSummary{}, false
	}
	if created != nil {
		return h.summaryFromCreated(ctx, *created), true
	}

	histories, _, err := h.db.ListStreamHistory(ctx, streamID, 1, 0)
	if err != nil {
		logger.Error("list stream history for idle stream failed", "stream_id", streamID, "err", err)
		return streamSummary{}, false
	}
	if len(histories) == 0 {
		if item, ok := h.buildGB28181IdleStreamSummary(ctx, streamID); ok {
			return item, true
		}
		return streamSummary{}, false
	}
	latest := histories[0]
	if time.Since(latest.StartedAt) > streamIdleHistoryMaxAge {
		return streamSummary{}, false
	}
	appName := latest.AppName
	if appName == "" {
		appName = "live"
	}
	lastSeen := latest.StartedAt
	if latest.EndedAt != nil {
		lastSeen = *latest.EndedAt
	}
	item := streamSummary{
		Engine:        "lalmax",
		StreamID:      latest.StreamID,
		AppName:       appName,
		SourceType:    inferStreamSourceTypeForID(latest.StreamID, latest.Protocol),
		Active:        false,
		LastFrameTime: timePointer(lastSeen),
	}
	h.attachStreamURLs(ctx, &item)
	h.applyGB28181PlayingState(&item)
	return item, true
}

func (h *Handler) buildGB28181IdleStreamSummary(ctx context.Context, streamID string) (streamSummary, bool) {
	if h.gb28181Svr == nil || !h.gb28181Svr.IsStreamPlaying(streamID) {
		return streamSummary{}, false
	}
	item := streamSummary{
		Engine:     "lalmax",
		StreamID:   streamID,
		AppName:    "live",
		SourceType: "gb28181",
		Active:     false,
	}
	h.attachStreamURLs(ctx, &item)
	if cam, err := h.db.GetCamera(ctx, streamID); err == nil && cam != nil && cam.Enabled && !cam.Archived {
		item.Managed = true
		item.ManagementType = "camera"
		item.CameraID = cam.ID
		item.CameraName = cam.Name
	}
	h.applyGB28181PlayingState(&item)
	return item, true
}

type bindCameraRequest struct {
	CameraID string `json:"camera_id"`
}

func (h *Handler) handleBindCamera(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	var req bindCameraRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	info, err := h.mediaEngine.GetStream(r.Context(), streamID)
	if err != nil {
		logger.Error("get stream for bind failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get stream")
		return
	}
	if info == nil {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}

	cam, err := h.db.GetCamera(r.Context(), req.CameraID)
	if err != nil {
		logger.Error("get camera for bind failed", "camera_id", req.CameraID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	if h.camMgr != nil {
		if err := h.camMgr.SetCameraStream(r.Context(), req.CameraID, streamID); err != nil {
			logger.Error("bind stream to camera failed", "stream_id", streamID, "camera_id", req.CameraID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to bind stream to camera")
			return
		}
	} else if err := h.db.SetCameraStream(r.Context(), req.CameraID, streamID); err != nil {
		logger.Error("bind stream to camera failed", "stream_id", streamID, "camera_id", req.CameraID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to bind stream to camera")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id": streamID,
		"camera_id": req.CameraID,
		"status":    "bound",
	})
}

func (h *Handler) handleUnbindCamera(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	binding, err := h.db.GetStreamBinding(r.Context(), streamID)
	if err != nil {
		logger.Error("get stream binding failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get stream binding")
		return
	}
	if binding == nil {
		writeError(w, http.StatusNotFound, "stream binding not found")
		return
	}

	// Revert the camera to its own stream (a pull camera ingests live/{camera_id}).
	if h.camMgr != nil {
		if err := h.camMgr.SetCameraStream(r.Context(), binding.CameraID, binding.CameraID); err != nil {
			logger.Error("unbind stream from camera failed", "stream_id", streamID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to unbind stream from camera")
			return
		}
	} else if err := h.db.SetCameraStream(r.Context(), binding.CameraID, binding.CameraID); err != nil {
		logger.Error("unbind stream from camera failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to unbind stream from camera")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id": streamID,
		"status":    "unbound",
	})
}

type promoteStreamRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
}

func (h *Handler) handlePromoteStream(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	var req promoteStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	info, err := h.mediaEngine.GetStream(r.Context(), streamID)
	if err != nil {
		logger.Error("get stream for promote failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get stream")
		return
	}
	if info == nil {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}

	existingBinding, err := h.db.GetStreamBinding(r.Context(), streamID)
	if err != nil {
		logger.Error("check existing stream binding failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to check stream binding")
		return
	}
	if existingBinding != nil {
		writeError(w, http.StatusConflict, "stream already mapped to a camera")
		return
	}

	sourceType := h.inferPromoteSourceType(r.Context(), streamID, info)

	encoding := ""
	if info.VideoCodec != "" {
		encoding = strings.ToLower(info.VideoCodec)
	}

	// Determine URL based on source type
	var cameraURL string
	if sourceType == "relay_pull" {
		// For relay pull streams, use the original source URL
		if info.Publisher != nil && info.Publisher.Remote != "" {
			cameraURL = info.Publisher.Remote
		}
	} else {
		// For push streams (RTMP/SRT), use lal's RTSP play URL
		// The recorder will pull from lal's RTSP server, not from the push client
		if h.mediaEngine != nil {
			playURL, err := h.mediaEngine.BuildPlayURL(r.Context(), media.PlayURLRequest{
				StreamID: streamID,
				AppName:  "live",
				Protocol: "rtsp",
			})
			if err == nil && playURL != nil && playURL.URL != "" {
				cameraURL = playURL.URL
			}
		}
		// Fallback to constructing URL manually
		if cameraURL == "" {
			cameraURL = fmt.Sprintf("rtsp://127.0.0.1:5544/live/%s", streamID)
		}
	}

	protocol := "rtsp"
	cameraID := camera.GenerateCameraID()

	cam := config.CameraConfig{
		ID:         cameraID,
		Name:       req.Name,
		Protocol:   protocol,
		Encoding:   encoding,
		URL:        cameraURL,
		Enabled:    true,
		SourceType: sourceType,
		StreamID:   streamID,
	}
	config.ApplyCameraAudioDefault(&cam)

	if h.camMgr != nil {
		id, err := h.camMgr.AddCamera(r.Context(), cam)
		if err != nil {
			logger.Error("promote stream to camera via CameraManager failed", "stream_id", streamID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to promote stream to camera")
			return
		}
		cameraID = id
	} else if err := h.db.UpsertCamera(r.Context(), cameraID, req.Name, sourceType, encoding, cameraURL, "", "", true, req.Description, req.Location, ""); err != nil {
		logger.Error("promote stream to camera failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to promote stream to camera")
		return
	} else if err := h.db.SaveCameraExtras(r.Context(), cam); err != nil {
		logger.Warn("failed to save promoted camera extras", "camera_id", cameraID, "error", err)
	}

	if err := h.db.BindStreamToCamera(r.Context(), streamID, cameraID); err != nil {
		logger.Error("create stream binding for promoted camera failed", "stream_id", streamID, "camera_id", cameraID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create stream binding")
		return
	}

	if req.Description != "" || req.Location != "" {
		if err := h.db.UpdateCameraMetadata(r.Context(), cameraID, req.Description, req.Location, "", "", "", 0); err != nil {
			logger.Warn("failed to set camera metadata", "camera_id", cameraID, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id":   streamID,
		"camera_id":   cameraID,
		"source_type": sourceType,
		"status":      "promoted",
	})
}

func (h *Handler) handleDeleteStream(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	ctx := r.Context()
	// Capture device ownership before archive/unbind so we do not delete a
	// camera-backed recording plan after deleteOfflineStream clears the binding.
	keepPlan := h.streamHasDevice(ctx, streamID)

	created, err := h.db.GetCreatedStream(ctx, streamID)
	if err != nil {
		logger.Warn("get created stream for delete failed", "stream_id", streamID, "err", err)
	}
	if created != nil && created.InputMode == storage.CreatedStreamPull {
		if err := h.mediaEngine.StopPull(ctx, streamID); err != nil {
			logger.Debug("stop created pull failed", "stream_id", streamID, "err", err)
		}
	}

	info, err := h.mediaEngine.GetStream(ctx, streamID)
	if err != nil {
		// Idle created slots and direct-push leftovers should still be removable
		// even if lalmax returns a transient group lookup error.
		logger.Warn("get stream for delete failed", "stream_id", streamID, "err", err)
		info = nil
	}
	h.disconnectStreamSessions(ctx, streamID, info)

	removedCreated, err := h.db.DeleteCreatedStream(ctx, streamID)
	if err != nil {
		logger.Warn("failed to delete created stream", "stream_id", streamID, "error", err)
	}
	if info == nil && !removedCreated {
		ok, err := h.deleteOfflineStream(ctx, streamID)
		if err != nil {
			logger.Error("delete offline stream failed", "stream_id", streamID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to delete stream")
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "stream not found")
			return
		}
	}
	if err := h.db.DeleteStreamHistory(ctx, streamID); err != nil {
		logger.Warn("failed to clear stream history", "stream_id", streamID, "error", err)
	}
	if !keepPlan {
		h.dropUnmanagedRecordingPlan(ctx, streamID)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id": streamID,
		"status":    "deleted",
	})
}

func (h *Handler) streamHasDevice(ctx context.Context, streamID string) bool {
	if h.db == nil {
		return false
	}
	if cam, err := h.db.GetCamera(ctx, streamID); err == nil && cam != nil {
		return true
	}
	if binding, err := h.db.GetStreamBinding(ctx, streamID); err == nil && binding != nil {
		return true
	}
	return false
}

func (h *Handler) dropUnmanagedRecordingPlan(ctx context.Context, streamID string) {
	if h.db == nil {
		return
	}
	if err := h.db.DeleteRecordingPlanByStream(ctx, streamID); err != nil {
		logger.Warn("failed to delete recording plan for stream", "stream_id", streamID, "error", err)
		return
	}
	if h.recPlanner != nil {
		if err := h.recPlanner.Refresh(ctx); err != nil {
			logger.Warn("failed to refresh recording plans after stream delete", "error", err)
		}
	}
	if h.reconcileRecording != nil {
		h.reconcileRecording(ctx)
	}
}

func (h *Handler) disconnectStreamSessions(ctx context.Context, streamID string, info *media.StreamInfo) {
	if h.mediaEngine == nil {
		return
	}
	if info != nil {
		if info.Publisher != nil && info.Publisher.SessionID != "" {
			if err := h.mediaEngine.KickSession(ctx, info.Publisher.SessionID); err != nil {
				logger.Warn("kick publisher failed", "stream_id", streamID, "session_id", info.Publisher.SessionID, "err", err)
			}
		}
		for _, sub := range info.Subscribers {
			if media.IsInternalRecorderSession(sub.Protocol, sub.SessionID) {
				continue
			}
			if sub.SessionID == "" {
				continue
			}
			if err := h.mediaEngine.KickSession(ctx, sub.SessionID); err != nil {
				logger.Warn("kick subscriber failed", "stream_id", streamID, "session_id", sub.SessionID, "err", err)
			}
		}
	}
	if err := h.mediaEngine.StopPull(ctx, streamID); err != nil {
		logger.Debug("stop pull failed (may not be a pull stream)", "stream_id", streamID, "err", err)
	}
}

// deleteOfflineStream removes stream records that are visible in the list but no longer active in lalmax.
func (h *Handler) deleteOfflineStream(ctx context.Context, streamID string) (bool, error) {
	cam, err := h.db.GetCamera(ctx, streamID)
	if err != nil {
		return false, err
	}
	if cam != nil && !cam.Archived {
		if err := h.archiveCameraRecord(ctx, streamID); err != nil {
			return false, err
		}
		_ = h.db.UnbindStreamFromCamera(ctx, streamID)
		if binding, err := h.db.GetBindingByCameraID(ctx, streamID); err != nil {
			return false, err
		} else if binding != nil {
			_ = h.db.UnbindStreamFromCamera(ctx, binding.StreamID)
		}
		return true, nil
	}

	binding, err := h.db.GetStreamBinding(ctx, streamID)
	if err != nil {
		return false, err
	}
	if binding != nil {
		if err := h.db.UnbindStreamFromCamera(ctx, streamID); err != nil {
			return false, err
		}
		return true, nil
	}

	removed, err := h.db.DeleteCreatedStream(ctx, streamID)
	if err != nil {
		return false, err
	}
	if removed {
		return true, nil
	}

	histories, _, err := h.db.ListStreamHistory(ctx, streamID, 1, 0)
	if err != nil {
		return false, err
	}
	return len(histories) > 0, nil
}

func derefCreated(stream *storage.CreatedStream) storage.CreatedStream {
	if stream == nil {
		return storage.CreatedStream{}
	}
	return *stream
}

type createStreamRequest struct {
	StreamID  string `json:"stream_id"`
	Name      string `json:"name"`
	InputMode string `json:"input_mode"`
	SourceURL string `json:"source_url"`
}

// handleCreateStream creates an external stream.
// Push reserves a slot and returns copyable publish URLs.
// Pull starts a lalmax relay pull from source_url.
func (h *Handler) handleCreateStream(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}
	var req createStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	streamID := strings.TrimSpace(req.StreamID)
	if !pushStreamIDPattern.MatchString(streamID) {
		writeError(w, http.StatusBadRequest, "stream_id must be 1-64 characters of letters, numbers, '_' or '-'")
		return
	}
	inputMode := strings.ToLower(strings.TrimSpace(req.InputMode))
	if inputMode == "" {
		inputMode = storage.CreatedStreamPush
	}
	if inputMode != storage.CreatedStreamPush && inputMode != storage.CreatedStreamPull {
		writeError(w, http.StatusBadRequest, "input_mode must be push or pull")
		return
	}
	sourceURL := strings.TrimSpace(req.SourceURL)
	if inputMode == storage.CreatedStreamPull {
		if !validPullSourceURL(sourceURL) {
			writeError(w, http.StatusBadRequest, "source_url must be an rtsp, rtmp, http, srt, or udp URL")
			return
		}
	} else if len(h.externalIngestProtocols()) == 0 {
		writeError(w, http.StatusBadRequest, "no ingest protocol is enabled")
		return
	}

	ctx := r.Context()
	existing, err := h.db.GetCreatedStream(ctx, streamID)
	if err != nil {
		logger.Error("get created stream failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create stream")
		return
	}
	if existing != nil {
		writeError(w, http.StatusConflict, "stream already exists")
		return
	}
	cam, err := h.db.GetCamera(ctx, streamID)
	if err != nil {
		logger.Error("get camera for create stream failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create stream")
		return
	}
	if cam != nil && !cam.Archived {
		writeError(w, http.StatusConflict, "stream id is already used by a camera")
		return
	}
	binding, err := h.db.GetStreamBinding(ctx, streamID)
	if err != nil {
		logger.Error("get stream binding for create stream failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create stream")
		return
	}
	if binding != nil {
		writeError(w, http.StatusConflict, "stream id is already used by a camera")
		return
	}
	if inputMode == storage.CreatedStreamPull {
		info, err := h.mediaEngine.GetStream(ctx, streamID)
		if err != nil {
			logger.Error("get stream for create pull failed", "stream_id", streamID, "err", err)
			writeError(w, http.StatusInternalServerError, "failed to create stream")
			return
		}
		if info != nil {
			writeError(w, http.StatusConflict, "stream already exists")
			return
		}
	}

	name := normalizeStreamDisplayName(req.Name, streamID)
	created := storage.CreatedStream{
		StreamID:  streamID,
		Name:      name,
		AppName:   "live",
		InputMode: inputMode,
		SourceURL: sourceURL,
	}
	if inputMode == storage.CreatedStreamPull {
		if _, err := h.startCreatedPull(ctx, created); err != nil {
			logger.Error("start created pull failed", "stream_id", streamID, "err", err)
			writeError(w, http.StatusBadGateway, "failed to start pull")
			return
		}
	}
	if err := h.db.InsertCreatedStream(ctx, created); err != nil {
		if inputMode == storage.CreatedStreamPull {
			_ = h.mediaEngine.StopPull(ctx, streamID)
		}
		if storage.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "stream already exists")
			return
		}
		logger.Error("insert created stream failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create stream")
		return
	}

	writeJSON(w, http.StatusCreated, h.summaryFromCreated(ctx, created))
}

// RestoreCreatedPulls restarts relay pulls created from the stream page after lalmax is ready.
func (h *Handler) RestoreCreatedPulls(ctx context.Context) {
	if h == nil || h.mediaEngine == nil || h.db == nil {
		return
	}
	rows, err := h.db.ListCreatedStreams(ctx)
	if err != nil {
		logger.Error("list created pulls failed", "err", err)
		return
	}
	for _, row := range rows {
		if row.InputMode != storage.CreatedStreamPull || strings.TrimSpace(row.SourceURL) == "" {
			continue
		}
		if _, err := h.startCreatedPull(ctx, row); err != nil {
			logger.Error("restore created pull failed", "stream_id", row.StreamID, "err", err)
		}
	}
}

func (h *Handler) startCreatedPull(ctx context.Context, stream storage.CreatedStream) (*media.StreamSession, error) {
	appName := stream.AppName
	if appName == "" {
		appName = "live"
	}
	session, err := h.mediaEngine.StartPull(ctx, media.StartPullRequest{
		StreamID:     stream.StreamID,
		AppName:      appName,
		SourceURL:    stream.SourceURL,
		PullRetryNum: -1,
	})
	if err != nil && isDupInStreamError(err) {
		return &media.StreamSession{StreamID: stream.StreamID, AppName: appName, Protocol: "relay_pull"}, nil
	}
	return session, err
}

func validPullSourceURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "rtmp", "rtmps", "rtsp", "rtsps", "http", "https", "srt", "udp":
		return true
	default:
		return false
	}
}

func isDupInStreamError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "in stream already exist")
}

func (h *Handler) handleKickPublisher(w http.ResponseWriter, r *http.Request) {
	if h.mediaEngine == nil {
		writeError(w, http.StatusServiceUnavailable, "stream management unavailable")
		return
	}

	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	info, err := h.mediaEngine.GetStream(r.Context(), streamID)
	if err != nil {
		logger.Error("get stream for kick publisher failed", "stream_id", streamID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to get stream")
		return
	}
	if info == nil {
		writeError(w, http.StatusNotFound, "stream not found")
		return
	}

	if info.Publisher == nil {
		writeError(w, http.StatusNotFound, "no active publisher for this stream")
		return
	}

	if err := h.mediaEngine.KickSession(r.Context(), info.Publisher.SessionID); err != nil {
		logger.Error("kick publisher failed", "stream_id", streamID, "session_id", info.Publisher.SessionID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to kick publisher")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id":  streamID,
		"session_id": info.Publisher.SessionID,
		"status":     "kicked",
	})
}

func (h *Handler) handleListStreamHistory(w http.ResponseWriter, r *http.Request) {
	streamID := r.URL.Query().Get("stream_id")
	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}

	items, total, err := h.db.ListStreamHistory(r.Context(), streamID, limit, offset)
	if err != nil {
		logger.Error("list stream history failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list stream history")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"history": items,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

func (h *Handler) handleDeleteStreamHistory(w http.ResponseWriter, r *http.Request) {
	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	if err := h.db.DeleteStreamHistory(r.Context(), streamID); err != nil {
		logger.Error("delete stream history failed", "stream_id", streamID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete stream history")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id": streamID,
		"status":    "deleted",
	})
}

func (h *Handler) handleBanStream(w http.ResponseWriter, r *http.Request) {
	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	if h.banMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "ban manager unavailable")
		return
	}

	var req struct {
		Reason    string `json:"reason"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err == nil {
			expiresAt = &t
		}
	}

	if err := h.banMgr.Ban(r.Context(), streamID, req.Reason, expiresAt); err != nil {
		logger.Error("ban stream failed", "stream_id", streamID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to ban stream")
		return
	}

	// Kick any currently active publisher
	if h.mediaEngine != nil {
		info, err := h.mediaEngine.GetStream(r.Context(), streamID)
		if err == nil && info != nil && info.Publisher != nil {
			_ = h.mediaEngine.KickSession(r.Context(), info.Publisher.SessionID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id": streamID,
		"status":    "banned",
	})
}

func (h *Handler) handleUnbanStream(w http.ResponseWriter, r *http.Request) {
	streamID := streamIDFromRequest(r)
	if streamID == "" {
		writeError(w, http.StatusBadRequest, "stream_id is required")
		return
	}

	if h.banMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "ban manager unavailable")
		return
	}

	if err := h.banMgr.Unban(r.Context(), streamID); err != nil {
		logger.Error("unban stream failed", "stream_id", streamID, "error", err)
		writeError(w, http.StatusNotFound, "stream not banned")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"stream_id": streamID,
		"status":    "unbanned",
	})
}

func (h *Handler) handleListBans(w http.ResponseWriter, r *http.Request) {
	bans, err := h.db.ListStreamBans(r.Context())
	if err != nil {
		logger.Error("list bans failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list bans")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"bans": bans,
	})
}
