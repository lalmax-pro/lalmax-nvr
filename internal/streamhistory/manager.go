package streamhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	lalmaxserver "github.com/q191201771/lalmax/server"
)

var logger = slog.Default().With("component", "stream-history")

// Manager subscribes to lalmax pub start/stop events and records stream history.
type Manager struct {
	db           *storage.DB
	engine       media.Engine
	cancelFunc   context.CancelFunc
	mu           sync.Mutex
	iptvSessions map[string]string
}

func NewManager(db *storage.DB, engine media.Engine) *Manager {
	return &Manager{db: db, engine: engine, iptvSessions: make(map[string]string)}
}

// RecordHLSPullState persists IPTV online sessions based on actual video
// delivery, since native HLS pulls do not emit regular publisher events.
func (m *Manager) RecordHLSPullState(streamID string, online bool, at time.Time) {
	if m == nil || m.db == nil || streamID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	m.mu.Lock()
	if online {
		if _, exists := m.iptvSessions[streamID]; exists {
			m.mu.Unlock()
			return
		}
		sessionID := fmt.Sprintf("iptv-hls-%s-%d", streamID, at.UnixNano())
		m.iptvSessions[streamID] = sessionID
		m.mu.Unlock()
		history := &storage.StreamHistory{
			StreamID:  streamID,
			AppName:   "live",
			Protocol:  "HLS_PULL",
			SessionID: sessionID,
			StartedAt: at,
		}
		if err := m.db.InsertStreamHistory(context.Background(), history); err != nil {
			logger.Error("failed to insert IPTV pull history", "stream_id", streamID, "error", err)
		}
		return
	}
	sessionID, exists := m.iptvSessions[streamID]
	delete(m.iptvSessions, streamID)
	m.mu.Unlock()
	if exists {
		if err := m.db.FinishStreamHistory(context.Background(), sessionID, at, 0, 0); err != nil {
			logger.Warn("failed to finish IPTV pull history", "stream_id", streamID, "error", err)
		}
	}
}

// Start begins listening for pub events and recording history.
func (m *Manager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancelFunc = cancel

	// Run in goroutine to avoid blocking startup (lalmax may not be ready yet)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			events, err := m.engine.SubscribeEvents(ctx, media.EventFilter{
				Types: []media.EventType{
					media.EventPublisherStarted,
					media.EventPublisherStopped,
				},
			})
			if err != nil {
				logger.Warn("failed to subscribe to stream events, retrying in 5s", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}

			m.run(ctx, events)
			// If events channel closed, retry after a delay
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
	}()
	return nil
}

func (m *Manager) Stop() {
	if m.cancelFunc != nil {
		m.cancelFunc()
	}
}

func (m *Manager) run(ctx context.Context, events <-chan media.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Type {
			case media.EventPublisherStarted:
				m.handlePubStart(ev)
			case media.EventPublisherStopped:
				m.handlePubStop(ev)
			}
		}
	}
}

type pubStartPayload struct {
	SessionId  string `json:"session_id"`
	Protocol   string `json:"protocol"`
	RemoteAddr string `json:"remote_addr"`
	AppName    string `json:"app_name"`
	StreamName string `json:"stream_name"`
}

type pubStopPayload struct {
	SessionId     string `json:"session_id"`
	Protocol      string `json:"protocol"`
	RemoteAddr    string `json:"remote_addr"`
	AppName       string `json:"app_name"`
	StreamName    string `json:"stream_name"`
	ReadBytesSum  uint64 `json:"read_bytes_sum"`
	WroteBytesSum uint64 `json:"wrote_bytes_sum"`
}

func (m *Manager) handlePubStart(ev media.Event) {
	var payload pubStartPayload
	if err := json.Unmarshal(ev.Raw, &payload); err != nil {
		logger.Warn("failed to parse pub_start payload", "error", err)
		return
	}

	h := &storage.StreamHistory{
		StreamID:   ev.StreamID,
		AppName:    ev.AppName,
		Protocol:   ev.Protocol,
		RemoteAddr: payload.RemoteAddr,
		SessionID:  ev.SessionID,
		StartedAt:  ev.At,
	}

	if err := m.db.InsertStreamHistory(context.Background(), h); err != nil {
		logger.Error("failed to insert stream history", "stream_id", ev.StreamID, "error", err)
		return
	}
	logger.Info("stream history recorded", "stream_id", ev.StreamID, "protocol", ev.Protocol, "remote", payload.RemoteAddr)
}

func (m *Manager) handlePubStop(ev media.Event) {
	var payload pubStopPayload
	if err := json.Unmarshal(ev.Raw, &payload); err != nil {
		// If we can't parse the payload, still try to finish with zero bytes
		payload = pubStopPayload{}
	}

	endedAt := ev.At
	if endedAt.IsZero() {
		endedAt = time.Now()
	}

	if err := m.db.FinishStreamHistory(context.Background(), ev.SessionID, endedAt, payload.ReadBytesSum, payload.WroteBytesSum); err != nil {
		logger.Warn("failed to finish stream history", "session_id", ev.SessionID, "error", err)
	}
}

// RegisterAsHookPlugin registers with lalmax's hook plugin system as a fallback event source.
// This provides more detailed payload (RemoteAddr, bytes) compared to the SSE-based events.
func (m *Manager) RegisterAsHookPlugin(server *lalmaxserver.LalMaxServer) (func(), error) {
	plugin := &hookPlugin{mgr: m}
	return server.RegisterHookPlugin(plugin, lalmaxserver.HookPluginOptions{
		BufferSize: 128,
		Filter: lalmaxserver.NewHookEventFilter("", "", "", []string{
			lalmaxserver.HookEventPubStart,
			lalmaxserver.HookEventPubStop,
		}),
	})
}

type hookPlugin struct {
	mgr *Manager
}

func (p *hookPlugin) Name() string { return "stream-history" }

func (p *hookPlugin) OnHookEvent(event lalmaxserver.HookEvent) error {
	switch event.Event {
	case lalmaxserver.HookEventPubStart:
		var payload pubStartPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil
		}
		h := &storage.StreamHistory{
			StreamID:   payload.StreamName,
			AppName:    payload.AppName,
			Protocol:   payload.Protocol,
			RemoteAddr: payload.RemoteAddr,
			SessionID:  payload.SessionId,
			StartedAt:  time.Now(),
		}
		if err := p.mgr.db.InsertStreamHistory(context.Background(), h); err != nil {
			logger.Error("hook: failed to insert stream history", "stream_id", payload.StreamName, "error", err)
		}

	case lalmaxserver.HookEventPubStop:
		var payload pubStopPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil
		}
		if err := p.mgr.db.FinishStreamHistory(context.Background(), payload.SessionId, time.Now(), payload.ReadBytesSum, payload.WroteBytesSum); err != nil {
			logger.Warn("hook: failed to finish stream history", "session_id", payload.SessionId, "error", err)
		}
	}
	return nil
}
