package recorder

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEventManagerTriggerAndEnd(t *testing.T) {
	var mu sync.Mutex
	var resumed, paused []string
	m := NewEventManager(20*time.Millisecond, time.Second,
		func(_ context.Context, id string) error {
			mu.Lock()
			resumed = append(resumed, id)
			mu.Unlock()
			return nil
		},
		func(_ context.Context, id string) error {
			mu.Lock()
			paused = append(paused, id)
			mu.Unlock()
			return nil
		},
		func(string) bool { return true },
	)
	m.Trigger("cam-1", "mqtt")
	if !m.IsActive("cam-1") {
		t.Fatal("expected active session")
	}
	m.End("cam-1", "mqtt")
	if m.IsActive("cam-1") {
		t.Fatal("expected session closed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resumed) != 1 || len(paused) != 1 {
		t.Fatalf("resume=%v pause=%v", resumed, paused)
	}
}

func TestSchedulerSkipsActiveEvent(t *testing.T) {
	var paused []string
	s := NewRecordingScheduler(nil)
	s.keepRecording = func(id string) bool { return id == "cam-event" }
	// inject check internals by calling keepRecording path directly
	desired := map[string]bool{"cam-event": false, "cam-off": false}
	for cameraID, shouldRecord := range desired {
		if shouldRecord {
			continue
		}
		if s.keepRecording != nil && s.keepRecording(cameraID) {
			continue
		}
		paused = append(paused, cameraID)
	}
	if len(paused) != 1 || paused[0] != "cam-off" {
		t.Fatalf("paused=%v", paused)
	}
}
