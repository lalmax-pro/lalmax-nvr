package recorder

import (
	"context"
	"sync"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// RecordingPlanner holds the desired recording state for every planned stream.
// It is refreshed from recording_plans and consulted by the camera manager and
// the recording scheduler, so a stream is recorded only when a plan says so.
type RecordingPlanner struct {
	db *storage.DB

	mu      sync.RWMutex
	desired map[string]bool
	modes   map[string]string
	loaded  bool
}

func NewRecordingPlanner(db *storage.DB) *RecordingPlanner {
	return &RecordingPlanner{db: db, desired: make(map[string]bool), modes: make(map[string]string)}
}

// Refresh reloads the desired recording state from recording plans.
func (p *RecordingPlanner) Refresh(ctx context.Context) error {
	if p == nil || p.db == nil {
		return nil
	}
	desired, err := p.db.DesiredRecordingStreams(ctx)
	if err != nil {
		return err
	}
	plans, err := p.db.ListRecordingPlans(ctx)
	if err != nil {
		return err
	}
	modes := make(map[string]string, len(plans))
	for _, plan := range plans {
		modes[plan.StreamID] = plan.Mode
	}

	p.mu.Lock()
	p.desired = desired
	p.modes = modes
	p.loaded = true
	p.mu.Unlock()
	return nil
}

// Mode returns the recording mode planned for a stream.
// The bool is false when no plan exists for it.
func (p *RecordingPlanner) Mode(streamID string) (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	mode, ok := p.modes[streamID]
	return mode, ok
}

// ShouldRecord reports whether the stream should be recording right now.
// The second result is false when no plan state has been loaded yet.
func (p *RecordingPlanner) ShouldRecord(streamID string) (record bool, known bool) {
	if p == nil {
		return false, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.loaded {
		return false, false
	}
	return p.desired[streamID], true
}

// Desired returns a copy of the current desired state.
func (p *RecordingPlanner) Desired() map[string]bool {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]bool, len(p.desired))
	for k, v := range p.desired {
		out[k] = v
	}
	return out
}
