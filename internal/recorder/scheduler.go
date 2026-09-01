package recorder

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var schedLogger = slog.Default().With("component", "recording-scheduler")

// RecordingScheduler periodically reconciles each camera's recorder against its
// recording_mode + weekly schedule: continuous → always recording, off/event →
// paused, scheduled → recording only within the camera's time windows.
type RecordingScheduler struct {
	db            *storage.DB
	mu            sync.Mutex
	stopCh        chan struct{}
	done          chan struct{}
	keepRecording func(cameraID string) bool
}

func NewRecordingScheduler(db *storage.DB) *RecordingScheduler {
	return &RecordingScheduler{
		db:     db,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// SetKeepRecording skips pause when the callback reports an active event window.
func (s *RecordingScheduler) SetKeepRecording(fn func(cameraID string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keepRecording = fn
}

// Start begins the scheduler loop. It checks every 30 seconds.
func (s *RecordingScheduler) Start(ctx context.Context, pauseFn, resumeFn func(ctx context.Context, cameraID string) error) {
	go s.run(ctx, pauseFn, resumeFn)
}

func (s *RecordingScheduler) Stop() {
	close(s.stopCh)
	<-s.done
}

func (s *RecordingScheduler) run(ctx context.Context, pauseFn, resumeFn func(ctx context.Context, cameraID string) error) {
	defer close(s.done)

	schedLogger.Info("recording scheduler started")
	s.check(ctx, pauseFn, resumeFn)

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
			s.check(ctx, pauseFn, resumeFn)
		}
	}
}

func (s *RecordingScheduler) check(ctx context.Context, pauseFn, resumeFn func(ctx context.Context, cameraID string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Desired recording state per camera, derived from recording_mode + schedule.
	desired, err := s.db.GetDesiredRecordingState(ctx)
	if err != nil {
		schedLogger.Error("failed to get desired recording state", "error", err)
		return
	}

	for cameraID, shouldRecord := range desired {
		if shouldRecord {
			if err := resumeFn(ctx, cameraID); err != nil {
				schedLogger.Debug("resume failed (camera may already be recording)", "camera_id", cameraID, "error", err)
			}
			continue
		}
		if s.keepRecording != nil && s.keepRecording(cameraID) {
			continue
		}
		if err := pauseFn(ctx, cameraID); err != nil {
			schedLogger.Debug("pause failed (camera may already be paused)", "camera_id", cameraID, "error", err)
		}
	}
}
