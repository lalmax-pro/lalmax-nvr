package ban

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

var logger = slog.Default().With("component", "ban")

// Manager enforces stream bans via lal's IAuthentication interface.
// All protocols (RTMP, RTSP, SRT, WHIP, GB28181) are checked uniformly.
type Manager struct {
	db                     storage.StreamBanRepository
	kickFunc               func(ctx context.Context, sessionID string) error
	mediaAuth              *auth.MediaAuthorizer
	rtmpPublishAuthEnabled bool
	rtspPublishAuthEnabled bool
}

func NewManager(db storage.StreamBanRepository, mediaAuth *auth.MediaAuthorizer) *Manager {
	if mediaAuth == nil {
		mediaAuth = auth.NewMediaAuthorizer()
	}
	return &Manager{db: db, mediaAuth: mediaAuth, rtmpPublishAuthEnabled: true, rtspPublishAuthEnabled: true}
}

// SetKickFunc sets the function used to kick currently active banned streams.
func (m *Manager) SetKickFunc(fn func(ctx context.Context, sessionID string) error) {
	m.kickFunc = fn
}

// SetRTMPPublishAuthEnabled toggles credential checks for RTMP publishing only.
// Disabling authentication still enforces the stream ban list, failing closed
// if the ban store cannot be queried.
func (m *Manager) SetRTMPPublishAuthEnabled(enabled bool) {
	m.rtmpPublishAuthEnabled = enabled
}

// SetRTSPPublishAuthEnabled controls the optional shared RTSP publish-token
// policy independently of native RTSP Basic/Digest authentication. Bans remain
// enforced (fail closed) when this token check is disabled.
func (m *Manager) SetRTSPPublishAuthEnabled(enabled bool) {
	m.rtspPublishAuthEnabled = enabled
}

// SetRTMPStreamKeys installs the required publish token for each RTMP stream ID.
func (m *Manager) SetRTMPStreamKeys(keys map[string]string) {
	m.SetPublishTokens("rtmp", keys)
}

// SetPublishTokens installs per-stream publish credentials for one protocol.
func (m *Manager) SetPublishTokens(protocol string, keys map[string]string) {
	m.mediaAuth.SetPublishTokens(protocol, keys)
}

// OnPubStart checks if the stream is banned. Returns error to reject the session.
func (m *Manager) OnPubStart(info base.PubStartInfo) error {
	if strings.EqualFold(info.Protocol, base.SessionProtocolRtmpStr) {
		if !m.rtmpPublishAuthEnabled && !m.mediaAuth.InternalPlaybackRequired(info.StreamName) {
			return m.rejectBannedStream(info.StreamName, info.Protocol, true)
		}
		token, err := auth.QueryToken(info.UrlParam, "stream_key")
		if err != nil {
			return fmt.Errorf("invalid RTMP publish parameters for stream %q", info.StreamName)
		}
		return m.AuthorizePublish("rtmp", info.StreamName, token)
	}
	if strings.EqualFold(info.Protocol, base.SessionProtocolRtspStr) {
		if !m.rtspPublishAuthEnabled && !m.mediaAuth.InternalPlaybackRequired(info.StreamName) {
			return m.rejectBannedStream(info.StreamName, info.Protocol, true)
		}
		if m.mediaAuth.SharedPublishRequired() || m.mediaAuth.InternalPlaybackRequired(info.StreamName) {
			token, err := auth.QueryToken(info.UrlParam, "token")
			if err != nil {
				return fmt.Errorf("invalid RTSP publish parameters for stream %q", info.StreamName)
			}
			return m.AuthorizePublish("rtsp", info.StreamName, token)
		}
	}
	return m.rejectBannedStream(info.StreamName, info.Protocol, false)
}

// AuthorizePublish checks a protocol token and rejects banned stream IDs.
func (m *Manager) AuthorizePublish(protocol, streamID, token string) error {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if err := m.mediaAuth.AuthorizePublish(protocol, streamID, token); err != nil {
		return err
	}
	return m.rejectBannedStream(streamID, protocol, true)
}

// MediaAuthorizer returns the shared credential policy used by protocol
// adapters. Ban checks remain the responsibility of Manager.
func (m *Manager) MediaAuthorizer() *auth.MediaAuthorizer {
	return m.mediaAuth
}

func (m *Manager) rejectBannedStream(streamID, protocol string, failClosed bool) error {
	ban, err := m.db.GetStreamBan(context.Background(), streamID)
	if err != nil {
		logger.Error("failed to check ban", "stream", streamID, "error", err)
		if !failClosed {
			return nil
		}
		return fmt.Errorf("failed to authorize %s stream %q", protocol, streamID)
	}
	if ban != nil {
		reason := ban.Reason
		if reason == "" {
			reason = "banned"
		}
		logger.Info("rejected banned stream", "stream", streamID, "protocol", protocol, "reason", reason)
		return fmt.Errorf("stream %q is banned: %s", streamID, reason)
	}
	return nil
}

// OnSubStart applies the shared stream playback-token policy when configured.
func (m *Manager) OnSubStart(info base.SubStartInfo) error {
	if !m.mediaAuth.PlaybackRequired() && !m.mediaAuth.InternalPlaybackRequired(info.StreamName) {
		return nil
	}
	token, err := auth.QueryToken(info.UrlParam, "token")
	if err != nil {
		return fmt.Errorf("invalid %s playback parameters for stream %q", info.Protocol, info.StreamName)
	}
	return m.mediaAuth.AuthorizePlayback(info.Protocol, info.StreamName, token)
}

// OnHls applies the shared stream playback-token policy to HLS playlists and
// segments when configured.
func (m *Manager) OnHls(streamName, urlParam string) error {
	if !m.mediaAuth.PlaybackRequired() && !m.mediaAuth.InternalPlaybackRequired(streamName) {
		return nil
	}
	token, err := auth.QueryToken(urlParam, "token")
	if err != nil {
		return fmt.Errorf("invalid HLS playback parameters for stream %q", streamName)
	}
	return m.mediaAuth.AuthorizePlayback("hls", streamName, token)
}

// Ban adds a ban for a stream.
func (m *Manager) Ban(ctx context.Context, streamID, reason string, expiresAt *time.Time) error {
	ban := &storage.StreamBan{
		StreamID:  streamID,
		Reason:    reason,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
	if err := m.db.InsertStreamBan(ctx, ban); err != nil {
		return err
	}
	logger.Info("stream banned", "stream_id", streamID, "reason", reason)
	return nil
}

// Unban removes a ban for a stream.
func (m *Manager) Unban(ctx context.Context, streamID string) error {
	return m.db.DeleteStreamBan(ctx, streamID)
}

// ListBans returns all active bans.
func (m *Manager) ListBans(ctx context.Context) ([]storage.StreamBan, error) {
	return m.db.ListStreamBans(ctx)
}

// Ensure Manager implements logic.IAuthentication
var _ logic.IAuthentication = (*Manager)(nil)
