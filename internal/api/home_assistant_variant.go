package api

import (
	"context"
	"fmt"
	"net/url"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// Resolve a selected output while keeping the publication's source identity.
// No task is started by catalog polling; unavailable selected output stays offline.
func (h *Handler) homeAssistantVariant(ctx context.Context, cfg storage.HomeAssistantSettings, p storage.HomeAssistantPublication) (string, media.StreamInfo, error) {
	if h.transcodeService == nil || h.transcodePlayback == nil || h.mediaEngine == nil {
		return "", media.StreamInfo{}, fmt.Errorf("transcoding unavailable")
	}
	source, id, err := h.transcodeService.PlaybackTarget(ctx, p.StreamID, p.TranscodeJobID)
	if err != nil {
		return "", media.StreamInfo{}, err
	}
	play, err := h.transcodePlayback.DerivedPlayURL(ctx, source, id, "rtsp")
	if err != nil || play == nil {
		return "", media.StreamInfo{}, fmt.Errorf("derived RTSP unavailable")
	}
	info, err := h.mediaEngine.GetStream(ctx, id)
	if err != nil || info == nil || !info.Active {
		return "", media.StreamInfo{}, fmt.Errorf("derived output offline")
	}
	u, err := url.Parse(play.URL)
	if err != nil || u.Scheme != "rtsp" || u.Hostname() == "" {
		return "", media.StreamInfo{}, fmt.Errorf("invalid derived RTSP URL")
	}
	if cfg.RTSPBaseURL != "" {
		base, err := url.Parse(cfg.RTSPBaseURL)
		if err != nil {
			return "", media.StreamInfo{}, err
		}
		u.Host = base.Host
	}
	u.User = nil
	u.Fragment = ""
	return u.String(), *info, nil
}
