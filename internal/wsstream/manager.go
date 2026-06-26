package wsstream

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

var wsLogger atomic.Pointer[slog.Logger]

func init() {
	wsLogger.Store(slog.Default().With("component", "ws-stream-manager"))
}

const (
	defaultMaxViewers   = 10
	defaultWriteBufSize = 100
	defaultIdleTimeout  = 60 * time.Second
)

// Errors returned by the Manager.
var (
	ErrStreamExists   = errors.New("wsstream: stream already registered")
	ErrStreamNotActive = errors.New("wsstream: stream not active")
	ErrMaxViewers     = errors.New("wsstream: max viewers reached")
)

// frameMsg is an internal frame representation passed through the per-stream channel.
type frameMsg struct {
	pts        int64
	au         [][]byte
	isKeyframe bool
}

// viewerConn represents a connected WebSocket client.
type viewerConn struct {
	id     int64
	conn   *websocket.Conn
	ch     chan []byte // encoded binary messages
	cancel context.CancelFunc
}

// streamEntry holds per-camera WebSocket streaming state.
type streamEntry struct {
	codec      model.Format
	sps        []byte
	pps        []byte
	vps        []byte
	viewers    map[int64]*viewerConn
	viewerSeq  atomic.Int64
	viewerMu   sync.Mutex
	frameCh    chan frameMsg
	cancel     context.CancelFunc
	hub        *model.StreamHub
	hubSubID   string
	dropCount  atomic.Int64

	// Audio (optional). audioInfo is nil until SetAudioConfig is called.
	audioInfo     atomic.Pointer[AudioCodecInfo]
	hubAudioSubID string
}

// upgrader is the WebSocket upgrader used by ServeWS.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Manager manages WebSocket binary streams with per-camera stream entries.
// It subscribes to StreamHub for live frames and serves them over WebSocket
// connections as binary-encoded VideoFrame messages. CodecInfo is sent as
// the first message on each connection.
type Manager struct {
	mu           sync.RWMutex
	streams      map[string]*streamEntry
	maxViewers   int
	writeBufSize int
	idleTimeout  time.Duration
}

// Option configures a Manager.
type Option func(*Manager)

// WithMaxViewers sets the maximum concurrent viewers per stream.
func WithMaxViewers(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.maxViewers = n
		}
	}
}

// WithWriteBufSize sets the per-stream write buffer size.
func WithWriteBufSize(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.writeBufSize = n
		}
	}
}

// WithIdleTimeout sets the idle timeout for WebSocket viewers.
func WithIdleTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.idleTimeout = d
		}
	}
}

// NewManager creates a new WebSocket stream Manager.
func NewManager(opts ...Option) *Manager {
	m := &Manager{
		streams:      make(map[string]*streamEntry),
		maxViewers:   defaultMaxViewers,
		writeBufSize: defaultWriteBufSize,
		idleTimeout:  defaultIdleTimeout,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// RegisterStream registers a camera stream for WebSocket output.
// The recorder's StreamHub is used to receive live frames.
func (m *Manager) RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.streams[camID]; ok {
		return ErrStreamExists
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{
		codec:    codec,
		sps:       sps,
		pps:       pps,
		vps:       vps,
		viewers:   make(map[int64]*viewerConn),
		frameCh:   make(chan frameMsg, m.writeBufSize),
		cancel:    cancel,
		hub:       hub,
	}

	// Subscribe to recorder's StreamHub for live frames
	if hub != nil {
		hubSubID := "ws-" + camID
		entry.hubSubID = hubSubID
		_ = hub.Subscribe(hubSubID, func(pts int64, au [][]byte) {
			m.writeFrame(camID, pts, au)
		})
	}

	m.streams[camID] = entry
	go m.writeLoop(ctx, camID, entry)

	wsLogger.Load().Info("WebSocket stream registered", "camera_id", camID, "codec", string(codec), "hub", hub != nil)
	return nil
}

// SetAudioConfig attaches an audio track to an already-registered stream and
// begins forwarding audio frames to viewers. It is safe to call once per
// stream, immediately after RegisterStream. A nil config is a no-op.
func (m *Manager) SetAudioConfig(camID string, aci *AudioCodecInfo) error {
	if aci == nil {
		return nil
	}

	m.mu.Lock()
	entry, ok := m.streams[camID]
	if !ok {
		m.mu.Unlock()
		return ErrStreamNotActive
	}
	entry.audioInfo.Store(aci)

	// Subscribe to the hub's audio stream exactly once.
	subscribe := entry.hub != nil && entry.hubAudioSubID == ""
	if subscribe {
		entry.hubAudioSubID = "ws-audio-" + camID
	}
	subID := entry.hubAudioSubID
	hub := entry.hub
	m.mu.Unlock()

	if subscribe {
		_ = hub.SubscribeAudio(subID, func(pts int64, _ model.AudioCodec, data []byte) {
			m.writeAudio(entry, pts, data)
		})
		wsLogger.Load().Info("WebSocket audio track attached", "camera_id", camID, "codec", aci.Codec, "sample_rate", aci.SampleRate, "channels", aci.Channels)
	}
	return nil
}

// writeAudio encodes an audio frame and fans it out to all viewers (non-blocking).
func (m *Manager) writeAudio(entry *streamEntry, pts int64, data []byte) {
	if len(data) == 0 {
		return
	}
	encoded, err := EncodeAudioFrame(&AudioFrame{PTS: pts, Data: data})
	if err != nil {
		return
	}
	entry.viewerMu.Lock()
	for _, v := range entry.viewers {
		select {
		case v.ch <- encoded:
		default:
			// Slow client — drop audio frame (counted with video drops).
			entry.dropCount.Add(1)
		}
	}
	entry.viewerMu.Unlock()
}

// UnregisterStream removes a camera stream and disconnects all viewers.
func (m *Manager) UnregisterStream(camID string) {
	m.mu.Lock()
	entry, ok := m.streams[camID]
	if ok {
		// Unsubscribe from recorder's StreamHub while holding the lock
		// to prevent race with hub callback accessing entry after removal.
		if entry.hub != nil && entry.hubSubID != "" {
			entry.hub.Unsubscribe(entry.hubSubID)
		}
		if entry.hub != nil && entry.hubAudioSubID != "" {
			entry.hub.UnsubscribeAudio(entry.hubAudioSubID)
		}
		delete(m.streams, camID)
	}
	m.mu.Unlock()

	if ok {
		entry.cancel()
		entry.viewerMu.Lock()
		eosMsg := []byte{byte(MsgTypeEOS)}
		for _, v := range entry.viewers {
			// Send EOS to viewer before closing
			_ = v.conn.WriteMessage(websocket.BinaryMessage, eosMsg)
			v.cancel()
			close(v.ch)
		}
		entry.viewerMu.Unlock()
		wsLogger.Load().Info("WebSocket stream unregistered", "camera_id", camID)
		if cnt := entry.dropCount.Load(); cnt > 0 {
			wsLogger.Load().Info("stream drop count", "camera_id", camID, "total_drops", cnt)
		}
	}
}

// IsActive returns whether a stream is registered.
func (m *Manager) IsActive(camID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.streams[camID]
	return ok
}

// ViewerCount returns the number of active viewers for a stream.
func (m *Manager) ViewerCount(camID string) int {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	entry.viewerMu.Lock()
	defer entry.viewerMu.Unlock()
	return len(entry.viewers)
}

// WriteH264 queues an H.264 access unit for WebSocket output. Non-blocking.
func (m *Manager) WriteH264(camID string, pts int64, au [][]byte) {
	m.writeFrame(camID, pts, au)
}

// WriteH265 queues an H.265 access unit for WebSocket output. Non-blocking.
func (m *Manager) WriteH265(camID string, pts int64, au [][]byte) {
	m.writeFrame(camID, pts, au)
}

func (m *Manager) writeFrame(camID string, pts int64, au [][]byte) {
	if len(au) == 0 {
		return
	}

	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return
	}

	// Scan every NAL in the access unit for an IDR. A keyframe AU is often led
	// by SPS/PPS (H.264) or VPS/SPS/PPS (H.265), so inspecting only au[0] would
	// misclassify keyframes as delta frames and the decoder would never start.
	isKeyframe := false
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		var naluType int
		if entry.codec == model.FormatH265 {
			// H.265: forbidden(1) | nal_unit_type(6) | ...
			naluType = int((nalu[0] >> 1) & 0x3F)
			// H.265 IRAP: BLA_W_LP..CRA_NUT = 16..21
			if naluType >= 16 && naluType <= 21 {
				isKeyframe = true
				break
			}
		} else {
			// H.264: forbidden(1) | nal_ref_idc(2) | nal_unit_type(5)
			naluType = int(nalu[0] & 0x1F)
			// H.264 IDR = 5
			if naluType == 5 {
				isKeyframe = true
				break
			}
		}
	}

	// Non-blocking send
	select {
	case entry.frameCh <- frameMsg{pts: pts, au: au, isKeyframe: isKeyframe}:
	default:
		// Buffer full, drop frame
		cnt := entry.dropCount.Add(1)
		if cnt%100 == 0 {
			wsLogger.Load().Warn("frames dropped", "camera_id", camID, "total_drops", cnt)
	}
		}
}

// writeLoop drains frames from the channel and distributes to all viewers.
func (m *Manager) writeLoop(ctx context.Context, camID string, entry *streamEntry) {
	defer func() {
		if r := recover(); r != nil {
			wsLogger.Load().Warn("WebSocket writeLoop panic recovered", "camera_id", camID, "error", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-entry.frameCh:
			encoded, err := EncodeVideoFrame(&VideoFrame{
				PTS:        msg.pts,
				IsKeyframe: msg.isKeyframe,
				NALUs:      msg.au,
			})
			if err != nil {
				wsLogger.Load().Warn("WebSocket encode frame error", "camera_id", camID, "error", err)
				continue
			}

			// Distribute to all viewers (non-blocking per viewer)
			entry.viewerMu.Lock()
			for _, v := range entry.viewers {
				select {
				case v.ch <- encoded:
				default:
					// Slow client — drop frame
					cnt := entry.dropCount.Add(1)
					if cnt%100 == 0 {
						wsLogger.Load().Warn("frames dropped", "camera_id", camID, "total_drops", cnt)
					}
				}
			}
			entry.viewerMu.Unlock()
	}
}
}

// ServeWS handles a WebSocket upgrade request for a camera stream.
// On connect, it sends CodecInfo as the first message, then streams
// VideoFrame messages as they arrive from the StreamHub.
func (m *Manager) ServeWS(camID string, w http.ResponseWriter, r *http.Request) error {
	m.mu.RLock()
	entry, ok := m.streams[camID]
	m.mu.RUnlock()

	if !ok {
		return ErrStreamNotActive
	}

	// Check viewer limit
	entry.viewerMu.Lock()
	if len(entry.viewers) >= m.maxViewers {
		entry.viewerMu.Unlock()
		return ErrMaxViewers
	}
	entry.viewerMu.Unlock()

	// Upgrade HTTP to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	// Build and send CodecInfo as first message
	codecStr := string(entry.codec)
	profile := byte(0)
	level := byte(0)
	if len(entry.sps) > 1 {
		profile = entry.sps[1]
	}
	if len(entry.sps) > 3 {
		level = entry.sps[3]
	}

	ci := &CodecInfo{
		Codec:   codecStr,
		Profile: profile,
		Level:   level,
		SPS:     entry.sps,
		PPS:     entry.pps,
		VPS:     entry.vps,
	}

	ciData, err := EncodeCodecInfo(ci)
	if err != nil {
		conn.Close()
		return err
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, ciData); err != nil {
		conn.Close()
		return err
	}

	// Send AudioCodecInfo next, if this stream has an audio track.
	if aci := entry.audioInfo.Load(); aci != nil {
		if aciData, err := EncodeAudioCodecInfo(aci); err == nil {
			if err := conn.WriteMessage(websocket.BinaryMessage, aciData); err != nil {
				conn.Close()
				return err
			}
		}
	}

	// Register viewer
	viewerCtx, viewerCancel := context.WithCancel(r.Context())
	viewerID := entry.viewerSeq.Add(1)
	viewerCh := make(chan []byte, m.writeBufSize)
	viewer := &viewerConn{
		id:     viewerID,
		conn:   conn,
		ch:     viewerCh,
		cancel: viewerCancel,
	}

	entry.viewerMu.Lock()
	entry.viewers[viewerID] = viewer
	entry.viewerMu.Unlock()

	wsLogger.Load().Debug("WebSocket viewer connected", "camera_id", camID, "viewer_id", viewerID)

	// Cleanup on exit
	defer func() {
		viewerCancel()
		entry.viewerMu.Lock()
		delete(entry.viewers, viewerID)
		entry.viewerMu.Unlock()
		_ = conn.Close()
		wsLogger.Load().Debug("WebSocket viewer disconnected", "camera_id", camID, "viewer_id", viewerID)
	}()

	// Set pong handler to reset read deadline on pong frames.
	// This allows the read pump below to detect truly dead clients
	// without killing active connections that just don't send data.
	pongTimeout := m.idleTimeout
	if pongTimeout < 60*time.Second {
		pongTimeout = 60 * time.Second
	}
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})
	// Set initial read deadline
	conn.SetReadDeadline(time.Now().Add(pongTimeout))

	// Start read pump to detect client disconnect via read deadline.
	// The read deadline is extended by the pong handler above.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				wsLogger.Load().Warn("WebSocket read pump panic recovered", "error", r)
			}
		}()
		for {
			select {
			case <-viewerCtx.Done():
				return
			default:
			}
			// ReadMessage blocks until a message arrives or deadline expires.
			// On pong frames, the pong handler resets the deadline.
			// On deadline expiry (no pong within timeout), this returns error and cancels.
			_, _, err := conn.ReadMessage()
			if err != nil {
				viewerCancel()
				return
			}
		}
	}()

	// Start ping sender to keep connection alive and trigger pong responses.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				wsLogger.Load().Warn("WebSocket ping sender panic recovered", "error", r)
			}
		}()
		pingInterval := pongTimeout / 4
		if pingInterval < 10*time.Second {
			pingInterval = 10 * time.Second
		}
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-viewerCtx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Start idle watchdog
	lastActivity := time.Now()
	idleTicker := time.NewTicker(m.idleTimeout / 2)
	defer idleTicker.Stop()
	go func() {
		for {
			select {
			case <-viewerCtx.Done():
				return
			case <-idleTicker.C:
				if time.Since(lastActivity) > m.idleTimeout {
					// Send EOS before closing so frontend can show offline status
					_ = conn.WriteMessage(websocket.BinaryMessage, []byte{byte(MsgTypeEOS)})
					viewerCancel()
					return
				}
			}
		}
		}()


	// Write frames to WebSocket until disconnect
	for {
		select {
		case <-viewerCtx.Done():
			return nil
		case data, ok := <-viewerCh:
			if !ok {
				return nil // channel closed
			}
			lastActivity = time.Now()
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				if !strings.Contains(err.Error(), "use of closed") {
					wsLogger.Load().Warn("WebSocket write error", "camera_id", camID, "viewer_id", viewerID, "error", err)
				}
				return nil
			}
		}
	}
}

// StopAll stops all active WebSocket streams.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.streams))
	for id := range m.streams {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.UnregisterStream(id)
	}
}

// Ensure Manager satisfies expected interface.
var _ interface {
	RegisterStream(camID string, codec model.Format, sps, pps, vps []byte, hub *model.StreamHub) error
	UnregisterStream(camID string)
	IsActive(camID string) bool
	ViewerCount(camID string) int
	WriteH264(camID string, pts int64, au [][]byte)
	WriteH265(camID string, pts int64, au [][]byte)
	ServeWS(camID string, w http.ResponseWriter, r *http.Request) error
	StopAll()
} = (*Manager)(nil)
