package recorder

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/observability"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"go.opentelemetry.io/otel/attribute"
)

var schedLogger = slog.Default().With("component", "recording-scheduler")

// RecordingScheduler reconciles recording plans with record tasks.
// A task runs only when the stream is in lalmax and a plan (or an event
// window) wants it written to disk. Device registration is not an input.
type RecordingScheduler struct {
	db      storage.RecordingStateRepository
	planner *RecordingPlanner
	tasks   *TaskManager

	mu               sync.Mutex
	stopCh           chan struct{}
	done             chan struct{}
	kick             chan struct{}
	alive            func(ctx context.Context) ([]string, error)
	eventActive      func(streamID string) bool
	sourceReconciler func(ctx context.Context, desired map[string]bool, eventActive func(string) bool) bool
	// writeSwitch owns disk writing for a stream that is already in lalmax.
	// Returning true means the task manager must not start or stop that stream.
	writeSwitch func(streamID string, want bool) bool
}

func NewRecordingScheduler(db storage.RecordingStateRepository) *RecordingScheduler {
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

// SetWriteSwitch flips disk writing for streams the group recorder already subscribes to.
// A true return tells the scheduler not to start a second record task for that stream.
func (s *RecordingScheduler) SetWriteSwitch(fn func(streamID string, want bool) bool) {
	s.mu.Lock()
	s.writeSwitch = fn
	s.mu.Unlock()
}

// SetSourceReconciler wires external HLS sources whose streams must be started
// before the recorder can observe them. It returns true when a source was
// started and the scheduler should check stream liveness again shortly.
func (s *RecordingScheduler) SetSourceReconciler(fn func(context.Context, map[string]bool, func(string) bool) bool) {
	s.mu.Lock()
	s.sourceReconciler = fn
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
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var retryTimer *time.Timer
	var retryC <-chan time.Time
	retryDelay := time.Second
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()
	reconcile := func() {
		if !s.check(ctx) {
			if retryTimer != nil {
				retryTimer.Stop()
			}
			retryC = nil
			retryDelay = time.Second
			return
		}
		if retryC == nil {
			if retryTimer == nil {
				retryTimer = time.NewTimer(retryDelay)
			} else {
				retryTimer.Reset(retryDelay)
			}
			retryC = retryTimer.C
		}
	}
	reconcile()

	for {
		select {
		case <-s.stopCh:
			schedLogger.Info("recording scheduler stopped")
			return
		case <-ctx.Done():
			schedLogger.Info("recording scheduler stopped (context cancelled)")
			return
		case <-ticker.C:
			reconcile()
		case <-s.kick:
			// A new plan or stream event deserves a prompt retry even if a
			// different stream has already reached the backoff ceiling.
			if retryTimer != nil {
				retryTimer.Stop()
			}
			retryC = nil
			retryDelay = time.Second
			reconcile()
		case <-retryC:
			retryC = nil
			retryDelay *= 2
			if retryDelay > 30*time.Second {
				retryDelay = 30 * time.Second
			}
			reconcile()
		}
	}
}

// check reports whether a desired live stream needs a retry for its codec.
func (s *RecordingScheduler) check(ctx context.Context) bool {
	ctx, span := observability.StartBusinessSpan(ctx, "recording.scheduler.reconcile")
	var checkErr error
	defer func() { observability.EndBusinessSpan(span, checkErr) }()

	s.mu.Lock()
	planner := s.planner
	tasks := s.tasks
	aliveFn := s.alive
	eventActive := s.eventActive
	sourceReconciler := s.sourceReconciler
	writeSwitch := s.writeSwitch
	s.mu.Unlock()

	if tasks == nil {
		return false
	}
	if planner != nil {
		if err := planner.Refresh(ctx); err != nil {
			schedLogger.Error("failed to refresh recording plans", "error", err)
			checkErr = err
			return false
		}
	}
	desired := s.desiredState(ctx, planner)
	if sourceReconciler != nil && sourceReconciler(ctx, desired, eventActive) {
		time.AfterFunc(time.Second, s.ReconcileNow)
	}

	aliveKnown := false
	aliveSet := map[string]bool{}
	if aliveFn != nil {
		ids, err := aliveFn(ctx)
		if err != nil {
			schedLogger.Error("failed to list lalmax streams", "error", err)
			checkErr = err
		} else {
			aliveKnown = true
			for _, id := range ids {
				if id != "" {
					aliveSet[id] = true
				}
			}
		}
	}
	span.SetAttributes(
		attribute.Int("nvr.recording.desired_stream_count", len(desired)),
		attribute.Int("nvr.media.alive_stream_count", len(aliveSet)),
		attribute.Int("nvr.recording.running_task_count", len(tasks.RunningIDs())),
	)

	retryCodec := false
	if aliveKnown {
		for id := range aliveSet {
			want := desired[id] || eventOn(eventActive, id)
			if writeSwitch != nil && writeSwitch(id, want) {
				continue
			}
			if want {
				if err := tasks.Ensure(ctx, id); err != nil {
					schedLogger.Debug("record task ensure failed", "stream_id", id, "error", err)
					if errors.Is(err, ErrStreamCodecNotReady) {
						retryCodec = true
					}
				}
			}
		}
	}

	for _, id := range tasks.RunningIDs() {
		// An operator-started derived recording is not plan-driven, but an
		// offline derived publisher must still terminate its recording.
		want := desired[id] || eventOn(eventActive, id) || tasks.IsExplicitDerived(id)
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
	return retryCodec
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
