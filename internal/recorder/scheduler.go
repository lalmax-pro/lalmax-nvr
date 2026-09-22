package recorder

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var schedLogger = slog.Default().With("component", "recording-scheduler")

// RecordingScheduler reconciles recording plans with record tasks.
// A task runs only when the stream is in lalmax and a plan (or an event
// window) wants it written to disk. Device registration is not an input.
type RecordingScheduler struct {
	db      *storage.DB
	planner *RecordingPlanner
	tasks   *TaskManager

	mu          sync.Mutex
	stopCh      chan struct{}
	done        chan struct{}
	kick        chan struct{}
	alive       func(ctx context.Context) ([]string, error)
	eventActive func(streamID string) bool
}

func NewRecordingScheduler(db *storage.DB) *RecordingScheduler {
	return &RecordingScheduler{
		db:     db,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
		kick:   make(chan struct{}, 1),
	}
}

// SetPlanner supplies the desired recording state.
func (s *RecordingScheduler) SetPlanner(p *RecordingPlanner) {
	s.mu.Lock()
	s.planner = p
	s.mu.Unlock()
}

// SetTasks wires the record-task owner. Without it the scheduler does nothing.
func (s *RecordingScheduler) SetTasks(t *TaskManager) {
	s.mu.Lock()
	s.tasks = t
	s.mu.Unlock()
}

// SetAliveStreams supplies the stream IDs currently present in lalmax.
func (s *RecordingScheduler) SetAliveStreams(fn func(ctx context.Context) ([]string, error)) {
	s.mu.Lock()
	s.alive = fn
	s.mu.Unlock()
}

// SetEventActive reports streams whose event window should keep a task running.
func (s *RecordingScheduler) SetEventActive(fn func(streamID string) bool) {
	s.mu.Lock()
	s.eventActive = fn
	s.mu.Unlock()
}

// Start begins the scheduler loop. It reconciles immediately, then every 30 seconds.
func (s *RecordingScheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

// ReconcileNow asks the loop to reconcile without waiting for the next tick.
func (s *RecordingScheduler) ReconcileNow() {
	if s == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *RecordingScheduler) Stop() {
	close(s.stopCh)
	<-s.done
}

func (s *RecordingScheduler) run(ctx context.Context) {
	defer close(s.done)

	schedLogger.Info("recording scheduler started")
	s.check(ctx)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			schedLogger.Info("recording scheduler stopped")
			return
		case <-ctx.Done():
			schedLogger.Info("recording scheduler stopped (context cancelled)")
			return
		case <-ticker.C:
			s.check(ctx)
		case <-s.kick:
			s.check(ctx)
		}
	}
}

func (s *RecordingScheduler) check(ctx context.Context) {
	s.mu.Lock()
	planner := s.planner
	tasks := s.tasks
	aliveFn := s.alive
	eventActive := s.eventActive
	s.mu.Unlock()

	if tasks == nil {
		return
	}
	if planner != nil {
		if err := planner.Refresh(ctx); err != nil {
			schedLogger.Error("failed to refresh recording plans", "error", err)
			return
		}
	}
	desired := s.desiredState(ctx, planner)

	aliveKnown := false
	aliveSet := map[string]bool{}
	if aliveFn != nil {
		ids, err := aliveFn(ctx)
		if err != nil {
			schedLogger.Error("failed to list lalmax streams", "error", err)
		} else {
			aliveKnown = true
			for _, id := range ids {
				if id != "" {
					aliveSet[id] = true
				}
			}
		}
	}

	if aliveKnown {
		for id := range aliveSet {
			if desired[id] || eventOn(eventActive, id) {
				if err := tasks.Ensure(ctx, id); err != nil {
					schedLogger.Debug("record task ensure failed", "stream_id", id, "error", err)
				}
			}
		}
	}

	for _, id := range tasks.RunningIDs() {
		want := desired[id] || eventOn(eventActive, id)
		if aliveKnown && !aliveSet[id] {
			if err := tasks.Stop(ctx, id, ReasonStreamDown); err != nil {
				schedLogger.Debug("record task stop failed", "stream_id", id, "reason", ReasonStreamDown, "error", err)
			}
			continue
		}
		if !want {
			if err := tasks.Stop(ctx, id, ReasonPlanInactive); err != nil {
				schedLogger.Debug("record task stop failed", "stream_id", id, "reason", ReasonPlanInactive, "error", err)
			}
		}
	}
}

func eventOn(fn func(string) bool, streamID string) bool {
	return fn != nil && fn(streamID)
}

func (s *RecordingScheduler) desiredState(ctx context.Context, planner *RecordingPlanner) map[string]bool {
	if planner != nil {
		return planner.Desired()
	}
	if s.db == nil {
		return nil
	}
	desired, err := s.db.DesiredRecordingStreams(ctx)
	if err != nil {
		schedLogger.Error("failed to get desired recording state", "error", err)
		return nil
	}
	return desired
}
