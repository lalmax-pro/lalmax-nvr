package sfu

import (
	"errors"
	"sync"
	"testing"
)

func TestStaleSessionCannotMutateTrackState(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	old, _ := r.JoinSession(Participant{ID: "p"})
	_ = r.LeaveSession("p", old)
	current, _ := r.JoinSession(Participant{ID: "p"})
	viewer, _ := r.JoinSession(Participant{ID: "v"})
	p, err := r.PublishSession("p", current, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.SubscribeSession("v", viewer, p.ID); err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	if _, err = r.PublishSession("p", old, "audio", "microphone", "audio/opus"); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("publish: %v", err)
	}
	if err = r.UnpublishSession("p", old, p.ID); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("unpublish: %v", err)
	}
	if err = r.SubscribeSession("p", old, p.ID); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("subscribe: %v", err)
	}
	if err = r.UnsubscribeSession("p", old, p.ID); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("unsubscribe: %v", err)
	}
	after := r.Snapshot()
	if after.Sequence != before.Sequence || len(after.Publications) != 1 || len(after.Subscriptions) != 1 {
		t.Fatalf("stale operation mutated state: %+v", after)
	}
	if err = r.UnsubscribeSession("v", viewer, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = r.UnpublishSession("p", current, p.ID); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentSessionReplacementRejectsOldOperations(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	old, _ := r.JoinSession(Participant{ID: "p"})
	_ = r.LeaveSession("p", old)
	_, _ = r.JoinSession(Participant{ID: "p"})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.PublishSession("p", old, "audio", "microphone", "audio/opus")
			if !errors.Is(err, ErrStaleSession) {
				t.Errorf("got %v", err)
			}
		}()
	}
	wg.Wait()
	if len(r.Snapshot().Publications) != 0 {
		t.Fatal("stale session published")
	}
}
func TestSessionOperationsMissingAndClosed(t *testing.T) {
	m := NewManager()
	key := Key{"n", "r"}
	r, _ := m.Create(key, 0)
	session, _ := r.JoinSession(Participant{ID: "p"})
	if _, err := r.PublishSession("p", "", "audio", "microphone", "audio/opus"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("empty: %v", err)
	}
	if err := r.SubscribeSession("missing", session, "track"); !errors.Is(err, ErrParticipantNotFound) {
		t.Fatalf("missing: %v", err)
	}
	_ = m.Close(key, r.Snapshot().Generation)
	if err := r.UnsubscribeSession("p", session, "track"); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("closed: %v", err)
	}
}
