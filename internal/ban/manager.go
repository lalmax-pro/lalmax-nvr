package ban

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

var logger = slog.Default().With("component", "ban")

// Manager enforces stream bans via lal's IAuthentication interface.
// All protocols (RTMP, RTSP, SRT, WHIP, GB28181) are checked uniformly.
type Manager struct {
	db       storage.StreamBanRepository
	kickFunc func(ctx context.Context, sessionID string) error
}

func NewManager(db storage.StreamBanRepository) *Manager {
	return &Manager{db: db}
}

// SetKickFunc sets the function used to kick currently active banned streams.
func (m *Manager) SetKickFunc(fn func(ctx context.Context, sessionID string) error) {
	m.kickFunc = fn
}

// OnPubStart checks if the stream is banned. Returns error to reject the session.
func (m *Manager) OnPubStart(info base.PubStartInfo) error {
	ban, err := m.db.GetStreamBan(context.Background(), info.StreamName)
	if err != nil {
		logger.Error("failed to check ban", "stream", info.StreamName, "error", err)
		return nil // don't block on DB errors
	}
	if ban != nil {
		reason := ban.Reason
		if reason == "" {
			reason = "banned"
		}
		logger.Info("rejected banned stream", "stream", info.StreamName, "protocol", info.Protocol, "reason", reason)
		return fmt.Errorf("stream %q is banned: %s", info.StreamName, reason)
	}
	return nil
}

// OnSubStart allows all subscriptions.
func (m *Manager) OnSubStart(info base.SubStartInfo) error {
	return nil
}

// OnHls allows all HLS requests.
func (m *Manager) OnHls(streamName, urlParam string) error {
	return nil
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
