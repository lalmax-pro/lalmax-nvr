package sfu

import (
	"errors"
	"sync"
	"testing"
)

func TestStaleSessionCannotRemoveRejoinedParticipant(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	old, err := r.JoinSession(Participant{ID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.LeaveSession("p", old); err != nil {
		t.Fatal(err)
	}
	current, err := r.JoinSession(Participant{ID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if old == current {
		t.Fatal("generation reused")
	}
	publication, err := r.Publish("p", "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	if err = r.LeaveSession("p", old); !errors.Is(err, ErrStaleSession) {
		t.Fatalf("stale leave: %v", err)
	}
	after := r.Snapshot()
	if after.Sequence != before.Sequence || len(after.Participants) != 1 || len(after.Publications) != 1 || after.Publications[0].ID != publication.ID {
		t.Fatalf("stale cleanup mutated room: %+v", after)
	}
	if err = r.LeaveSession("p", current); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentStaleLeaveAndJoin(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	old, _ := r.JoinSession(Participant{ID: "p"})
	_ = r.LeaveSession("p", old)
	current, _ := r.JoinSession(Participant{ID: "p"})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.LeaveSession("p", old); !errors.Is(err, ErrStaleSession) {
				t.Errorf("stale: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(r.Snapshot().Participants) != 1 {
		t.Fatal("current removed")
	}
	if err := r.LeaveSession("p", current); err != nil {
		t.Fatal(err)
	}
}
