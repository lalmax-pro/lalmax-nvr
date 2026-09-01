package recorder

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

var eventLogger = slog.Default().With("component", "event-recording")

// EventManager holds active event-recording sessions and drives Pause/Resume.
type EventManager struct {
	resume      func(ctx context.Context, cameraID string) error
	pause       func(ctx context.Context, cameraID string) error
	isEventMode func(cameraID string) bool
	postRoll    time.Duration
	maxDur      time.Duration

	mu       sync.Mutex
	sessions map[string]*eventSession
}

type eventSession struct {
	source    string
	startedAt time.Time
	expiresAt time.Time
	timer     *time.Timer
}

func NewEventManager(postRoll, maxDur time.Duration, resume, pause func(ctx context.Context, cameraID string) error, isEventMode func(cameraID string) bool) *EventManager {
	if postRoll <= 0 {
		postRoll = 30 * time.Second
	}
	if maxDur <= 0 {
		maxDur = 5 * time.Minute
	}
	return &EventManager{
		resume:      resume,
		pause:       pause,
		isEventMode: isEventMode,
		postRoll:    postRoll,
		maxDur:      maxDur,
		sessions:    make(map[string]*eventSession),
	}
}

// IsActive reports whether cameraID currently has an event recording window.
func (m *EventManager) IsActive(cameraID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[cameraID]
	return ok && time.Now().Before(sess.expiresAt)
}

// Trigger starts or extends an event recording window.
func (m *EventManager) Trigger(cameraID, source string) {
	if m == nil || cameraID == "" {
		return
	}
	if m.isEventMode != nil && !m.isEventMode(cameraID) {
		return
	}
	now := time.Now()
	m.mu.Lock()
	sess, ok := m.sessions[cameraID]
	if !ok {
		sess = &eventSession{source: source, startedAt: now}
		m.sessions[cameraID] = sess
	}
	deadline := now.Add(m.postRoll)
	maxEnd := sess.startedAt.Add(m.maxDur)
	if deadline.After(maxEnd) {
		deadline = maxEnd
	}
	sess.expiresAt = deadline
	sess.source = source
	if sess.timer != nil {
		sess.timer.Stop()
	}
	delay := time.Until(deadline)
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	sess.timer = time.AfterFunc(delay, func() { m.End(cameraID, "timeout") })
	m.mu.Unlock()

	if m.resume != nil {
		if err := m.resume(context.Background(), cameraID); err != nil {
			eventLogger.Debug("event resume failed", "camera_id", cameraID, "error", err)
		}
	}
	eventLogger.Info("event recording triggered", "camera_id", cameraID, "source", source)
}

// End closes the event window and pauses recording.
func (m *EventManager) End(cameraID, reason string) {
	if m == nil || cameraID == "" {
		return
	}
	m.mu.Lock()
	sess, ok := m.sessions[cameraID]
	if ok {
		if sess.timer != nil {
			sess.timer.Stop()
		}
		delete(m.sessions, cameraID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if m.pause != nil {
		if err := m.pause(context.Background(), cameraID); err != nil {
			eventLogger.Debug("event pause failed", "camera_id", cameraID, "error", err)
		}
	}
	eventLogger.Info("event recording ended", "camera_id", cameraID, "reason", reason)
}

// Stop cancels all sessions without pausing (used on shutdown).
func (m *EventManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		if sess.timer != nil {
			sess.timer.Stop()
		}
		delete(m.sessions, id)
	}
}
