package sfu

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentGetOrCreateUsesOneGeneration(t *testing.T) {
	m := NewManager()
	key := Key{Namespace: "n", ID: "r"}
	limits := RoomLimits{MaxParticipants: 4}
	const n = 50
	rooms := make(chan *Room, n)
	created := make(chan bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			room, isNew, err := m.GetOrCreateWithLimits(key, limits)
			if err != nil {
				t.Error(err)
				return
			}
			rooms <- room
			created <- isNew
		}()
	}
	wg.Wait()
	close(rooms)
	close(created)
	var first *Room
	for room := range rooms {
		if first == nil {
			first = room
		}
		if room != first {
			t.Fatal("multiple room instances")
		}
	}
	count := 0
	for isNew := range created {
		if isNew {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("created %d times", count)
	}
	if _, _, err := m.GetOrCreateWithLimits(key, RoomLimits{MaxParticipants: 5}); !errors.Is(err, ErrRoomConfigMismatch) {
		t.Fatal(err)
	}
}

func TestIdleCollectionWaitsForLastLeaveAndUsesGeneration(t *testing.T) {
	m := NewManager()
	key := Key{Namespace: "n", ID: "r"}
	room, _ := m.Create(key, 2)
	old := room.Snapshot().Generation
	if _, err := m.CollectIdle(0); !errors.Is(err, ErrInvalidIdleTimeout) {
		t.Fatal(err)
	}
	if got := m.collectIdleAt(room.idleSince.Add(time.Second), 2*time.Second); len(got) != 0 {
		t.Fatal("collected too early")
	}
	session, _ := room.JoinSession(Participant{ID: "p"})
	if got := m.collectIdleAt(time.Now().Add(time.Hour), time.Second); len(got) != 0 {
		t.Fatal("active room collected")
	}
	if err := room.LeaveSession("p", session); err != nil {
		t.Fatal(err)
	}
	idle := room.idleSince
	_, events, cancel, err := room.Watch(1)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if got := m.collectIdleAt(idle.Add(time.Second), 2*time.Second); len(got) != 0 {
		t.Fatal("grace period ignored")
	}
	got := m.collectIdleAt(idle.Add(2*time.Second), 2*time.Second)
	if len(got) != 1 || got[0].Key != key || got[0].Generation != old {
		t.Fatalf("collected: %+v", got)
	}
	if event, ok := <-events; !ok || event.Type != "room_closed" || !event.Snapshot.Closed {
		t.Fatalf("missing close event: %+v", event)
	}
	if _, ok := <-events; ok {
		t.Fatal("watch not closed")
	}
	if err := room.Join(Participant{ID: "p"}); !errors.Is(err, ErrRoomClosed) {
		t.Fatal(err)
	}
	next, _ := m.Create(key, 2)
	if next.Snapshot().Generation == old {
		t.Fatal("generation reused")
	}
	if err := m.Close(key, old); !errors.Is(err, ErrRoomNotFound) {
		t.Fatal("old generation closed replacement")
	}
}

func TestJoinRacingIdleCollectionIsAtomic(t *testing.T) {
	for i := 0; i < 100; i++ {
		m := NewManager()
		key := Key{Namespace: "n", ID: "r"}
		room, _ := m.Create(key, 0)
		now := room.idleSince.Add(time.Minute)
		var wg sync.WaitGroup
		wg.Add(2)
		var joinErr error
		var removed []CollectedRoom
		go func() { defer wg.Done(); joinErr = room.Join(Participant{ID: "p"}) }()
		go func() { defer wg.Done(); removed = m.collectIdleAt(now, time.Second) }()
		wg.Wait()
		if joinErr == nil {
			if len(removed) != 0 || room.Snapshot().Closed {
				t.Fatal("joined room was collected")
			}
		} else if !errors.Is(joinErr, ErrRoomClosed) || len(removed) != 1 {
			t.Fatalf("unexpected result: %v %+v", joinErr, removed)
		}
	}
}
