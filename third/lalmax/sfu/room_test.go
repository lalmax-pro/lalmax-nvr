package sfu

import (
	"errors"
	"sync"
	"testing"
)

func TestRoomIsolationAndGeneration(t *testing.T) {
	m := NewManager()
	key := Key{"tenant", "room"}
	first, err := m.Create(key, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Join(Participant{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	other, err := m.Create(Key{"other", "room"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Snapshot().Participants) != 0 {
		t.Fatal("membership leaked across rooms")
	}
	old := first.Snapshot().Generation
	if err = m.Close(key, old); err != nil {
		t.Fatal(err)
	}
	next, err := m.Create(key, 2)
	if err != nil {
		t.Fatal(err)
	}
	if next.Snapshot().Generation == old {
		t.Fatal("generation reused")
	}
	if err = m.Close(key, old); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("stale close: %v", err)
	}
	if err = first.Join(Participant{ID: "b"}); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("old room join: %v", err)
	}
	if _, ok := m.Get(key); !ok {
		t.Fatal("new room removed")
	}
}
func TestConcurrentJoinEnforcesCapacity(t *testing.T) {
	m := NewManager()
	r, err := m.Create(Key{"n", "r"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			err := r.Join(Participant{ID: id})
			if err != nil && !errors.Is(err, ErrRoomFull) {
				t.Errorf("join: %v", err)
			}
		}(id)
	}
	wg.Wait()
	if got := len(r.Snapshot().Participants); got != 1 {
		t.Fatalf("got %d participants", got)
	}
}
func TestDuplicateIdentityAndSnapshot(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	if err := r.Join(Participant{ID: "a", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Join(Participant{ID: "a"}); !errors.Is(err, ErrParticipantExists) {
		t.Fatalf("duplicate: %v", err)
	}
	s := r.Snapshot()
	s.Participants[0].Name = "changed"
	if r.Snapshot().Participants[0].Name != "alice" {
		t.Fatal("snapshot shares state")
	}
	if err := r.Leave("a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Leave("a"); !errors.Is(err, ErrParticipantNotFound) {
		t.Fatalf("leave: %v", err)
	}
}
