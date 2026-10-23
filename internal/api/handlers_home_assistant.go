package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type homeAssistantStream struct {
	WebRTCSupported     bool   `json:"webrtc_supported"`
	PTZSupported        bool   `json:"ptz_supported"`
	PTZPresetsSupported bool   `json:"ptz_presets_supported"`
	StreamID            string `json:"stream_id"`
	Name                string `json:"name"`
	Active              bool   `json:"active"`
	VideoCodec          string `json:"video_codec"`
	AudioCodec          string `json:"audio_codec"`
	RTSPURL             string `json:"rtsp_url"`
	UnavailableReason   string `json:"unavailable_reason,omitempty"`
}

type homeAssistantCatalog struct {
	InstanceID        string                `json:"instance_id"`
	Enabled           bool                  `json:"enabled"`
	EventsAvailable   bool                  `json:"events_available"`
	SnapshotAvailable bool                  `json:"snapshot_available"`
	Streams           []homeAssistantStream `json:"streams"`
}

func decodeHomeAssistantBody(w http.ResponseWriter, r *http.Request, dst any) error {
	// Reject null/arrays rather than silently treating null as a disabling update.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return fmt.Errorf("expected a JSON object")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func validateHomeAssistantBase(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "rtsp" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("rtsp_base_url must be an RTSP server address without credentials, path or query (rtsp://host:15544)")
	}
	return nil
}

func (h *Handler) handleGetHomeAssistantSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load Home Assistant settings")
		return
	}
	writeJSON(w, 200, cfg)
}

func (h *Handler) handleUpdateHomeAssistantSettings(w http.ResponseWriter, r *http.Request) {
	h.homeAssistantMu.Lock()
	defer h.homeAssistantMu.Unlock()
	var body struct {
		Enabled     bool                               `json:"enabled"`
		RTSPBaseURL string                             `json:"rtsp_base_url"`
		Streams     []storage.HomeAssistantPublication `json:"streams"`
	}
	if err := decodeHomeAssistantBody(w, r, &body); err != nil {
		writeError(w, 400, "invalid Home Assistant settings")
		return
	}
	body.RTSPBaseURL = strings.TrimSpace(body.RTSPBaseURL)
	if err := validateHomeAssistantBase(body.RTSPBaseURL); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if len(body.Streams) > 10000 {
		writeError(w, 400, "at most 10000 publications are supported")
		return
	}
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load Home Assistant settings")
		return
	}
	old := make(map[string]storage.HomeAssistantPublication)
	for _, p := range cfg.Streams {
		old[p.StreamID] = p
	}
	seen := make(map[string]bool)
	for _, p := range body.Streams {
		if strings.TrimSpace(p.StreamID) == "" || len(p.StreamID) > 512 || strings.ContainsAny(p.StreamID, "/?#\r\n") || seen[p.StreamID] || len([]rune(p.Name)) > 128 {
			writeError(w, 400, "invalid or duplicate publication")
			return
		}
		seen[p.StreamID] = true
	}
	for i := range body.Streams {
		p := &body.Streams[i]
		// Capture the actual application; do not trust a client-supplied path.
		if prev, ok := old[p.StreamID]; ok {
			p.AppName = prev.AppName
		} else {
			if h.mediaEngine == nil {
				writeError(w, 503, "media engine unavailable")
				return
			}
			item, ok, err := h.streamSummaryForID(r.Context(), p.StreamID)
			if err != nil {
				writeError(w, 503, "stream lookup unavailable")
				return
			}
			if !ok {
				writeError(w, 400, "publication stream not found")
				return
			}
			p.AppName = item.AppName
		}
		if p.AppName == "" {
			p.AppName = "live"
		}
	}
	cfg.Enabled, cfg.RTSPBaseURL, cfg.Streams = body.Enabled, body.RTSPBaseURL, body.Streams
	if err := h.db.SaveHomeAssistantSettings(r.Context(), cfg); err != nil {
		writeError(w, 500, "failed to save Home Assistant settings")
		return
	}
	writeJSON(w, 200, cfg)
}

func (h *Handler) handleHomeAssistantPublication(w http.ResponseWriter, r *http.Request) {
	h.homeAssistantMu.Lock()
	defer h.homeAssistantMu.Unlock()
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeHomeAssistantBody(w, r, &body); err != nil || body.Enabled == nil {
		writeError(w, 400, "enabled is required")
		return
	}
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load Home Assistant settings")
		return
	}
	id := streamIDFromRequest(r)
	index := slices.IndexFunc(cfg.Streams, func(p storage.HomeAssistantPublication) bool { return p.StreamID == id })
	if *body.Enabled && index < 0 {
		if len(cfg.Streams) >= 10000 {
			writeError(w, 400, "at most 10000 publications are supported")
			return
		}
		if h.mediaEngine == nil {
			writeError(w, 503, "media engine unavailable")
			return
		}
		item, ok, err := h.streamSummaryForID(r.Context(), id)
		if err != nil {
			writeError(w, 503, "stream lookup unavailable")
			return
		}
		if !ok {
			writeError(w, 404, "stream not found")
			return
		}
		app := item.AppName
		if app == "" {
			app = "live"
		}
		cfg.Streams = append(cfg.Streams, storage.HomeAssistantPublication{StreamID: id, AppName: app})
	} else if !*body.Enabled && index >= 0 {
		cfg.Streams = slices.Delete(cfg.Streams, index, index+1)
	}
	if err := h.db.SaveHomeAssistantSettings(r.Context(), cfg); err != nil {
		writeError(w, 500, "failed to save publication")
		return
	}
	writeJSON(w, 200, cfg)
}

func (h *Handler) homeAssistantRTSPURL(ctx context.Context, cfg storage.HomeAssistantSettings, id, app string) (string, error) {
	if h.mediaEngine == nil {
		return "", fmt.Errorf("media engine unavailable")
	}
	token := ""
	if h.config != nil {
		token = h.config.Auth.PlaybackTokens[id]
		if len(h.config.Auth.PlaybackTokens) > 0 && token == "" {
			return "", fmt.Errorf("stream playback token is not configured")
		}
	}
	play, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{StreamID: id, AppName: app, Protocol: "rtsp", Token: token})
	if err != nil {
		return "", err
	}
	if play == nil {
		return "", fmt.Errorf("RTSP unavailable")
	}
	u, err := url.Parse(play.URL)
	if err != nil || u.Scheme != "rtsp" || u.Hostname() == "" {
		return "", fmt.Errorf("invalid RTSP URL")
	}
	if cfg.RTSPBaseURL != "" {
		base, err := url.Parse(cfg.RTSPBaseURL)
		if err != nil {
			return "", err
		}
		u.Host = base.Host
	}
	// RTSP credentials are entered separately in HA, never exposed by the catalog.
	u.User, u.Fragment = nil, ""
	q := url.Values{}
	if token != "" {
		q.Set("token", token)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (h *Handler) handleHomeAssistantCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load Home Assistant settings")
		return
	}
	_, snapshotErr := h.resolveSnapshotFFmpegPath()
	result := homeAssistantCatalog{InstanceID: cfg.InstanceID, Enabled: cfg.Enabled, SnapshotAvailable: snapshotErr == nil, EventsAvailable: h.eventHub != nil, Streams: []homeAssistantStream{}}
	if !cfg.Enabled || len(cfg.Streams) == 0 {
		writeJSON(w, 200, result)
		return
	}
	if h.mediaEngine == nil {
		writeError(w, 503, "media engine unavailable")
		return
	}
	streams, err := h.mediaEngine.ListStreams(r.Context())
	if err != nil {
		writeError(w, 503, "stream listing unavailable")
		return
	}
	byID := make(map[string]media.StreamInfo, len(streams))
	for _, s := range streams {
		byID[s.StreamID] = s
	}
	names := make(map[string]string)
	protocols := make(map[string]string)
	cameras, err := h.db.ListCameras(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load camera names")
		return
	}
	for _, cam := range cameras {
		names[cam.ID] = cam.Name
		protocols[cam.ID] = cam.Protocol
	}
	bindings, err := h.db.ListStreamBindings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load stream names")
		return
	}
	for _, b := range bindings {
		names[b.StreamID] = names[b.CameraID]
		protocols[b.StreamID] = protocols[b.CameraID]
	}
	created, err := h.db.ListCreatedStreams(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load stream names")
		return
	}
	for _, s := range created {
		if s.Name != "" {
			names[s.StreamID] = s.Name
		}
	}
	for _, p := range cfg.Streams {
		s := byID[p.StreamID]
		app := p.AppName
		if s.AppName != "" {
			app = s.AppName
		}
		playURL, err := h.homeAssistantRTSPURL(r.Context(), cfg, p.StreamID, app)
		reason := ""
		if err != nil {
			playURL = ""
			s.Active = false
			reason = "rtsp_unavailable"
		}
		name := p.Name
		if name == "" {
			name = names[p.StreamID]
		}
		if name == "" {
			name = p.StreamID
		}
		result.Streams = append(result.Streams, homeAssistantStream{WebRTCSupported: homeAssistantWebRTCCodec(s.VideoCodec) != "" && (h.config == nil || h.config.Streaming.WebRTC.Enabled == nil || *h.config.Streaming.WebRTC.Enabled), PTZSupported: protocols[p.StreamID] == "onvif" || protocols[p.StreamID] == "gb28181", PTZPresetsSupported: protocols[p.StreamID] == "onvif", StreamID: p.StreamID, Name: name, Active: s.Active, VideoCodec: s.VideoCodec, AudioCodec: s.AudioCodec, RTSPURL: playURL, UnavailableReason: reason})
	}
	// One bounded response, with stable ordering, avoids drifting pages as streams go offline.
	slices.SortFunc(result.Streams, func(a, b homeAssistantStream) int { return strings.Compare(a.StreamID, b.StreamID) })
	writeJSON(w, 200, result)
}

func (h *Handler) handleHomeAssistantSnapshot(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.db.GetHomeAssistantSettings(r.Context())
	if err != nil {
		writeError(w, 500, "failed to load Home Assistant settings")
		return
	}
	id := streamIDFromRequest(r)
	index := slices.IndexFunc(cfg.Streams, func(p storage.HomeAssistantPublication) bool { return p.StreamID == id })
	if !cfg.Enabled || index < 0 {
		writeError(w, 404, "stream is not published to Home Assistant")
		return
	}
	cacheID := "home-assistant:" + id
	if data, ok := h.getCachedSnapshot(cacheID, snapshotCacheTTL); ok {
		serveSnapshotBytes(w, data, "no-store")
		return
	}
	ffmpeg, err := h.resolveSnapshotFFmpegPath()
	if err != nil {
		writeError(w, 503, "FFmpeg is required for stream snapshots; install FFmpeg on the NVR or use HA stream stills")
		return
	}
	// Use the engine's address rather than a potentially hairpinned public override.
	localCfg := cfg
	localCfg.RTSPBaseURL = ""
	playURL, err := h.homeAssistantRTSPURL(r.Context(), localCfg, id, cfg.Streams[index].AppName)
	if err != nil {
		writeError(w, 503, "RTSP playback unavailable")
		return
	}
	u, _ := url.Parse(playURL)
	if h.config != nil && h.config.Media.RTSPAuthEnable {
		u.User = url.UserPassword(h.config.Media.RTSPUsername, h.config.Media.RTSPPassword)
	}
	data, err := captureSnapshotWithFFmpeg(r.Context(), ffmpeg, u.String())
	if err != nil {
		writeError(w, 503, "stream snapshot unavailable")
		return
	}
	h.setCachedSnapshot(cacheID, data)
	serveSnapshotBytes(w, data, "no-store")
}
