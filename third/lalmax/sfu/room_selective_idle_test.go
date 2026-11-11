package sfu

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCloseIfIdleProtectsGenerationAndMembers(t *testing.T) {
	m := NewManager()
	key := Key{Namespace: "nvr", ID: "room"}
	room, err := m.Create(key, 4)
	if err != nil {
		t.Fatal(err)
	}
	generation := room.Snapshot().Generation
	if closed, err := m.CloseIfIdle(key, generation, 0); closed || !errors.Is(err, ErrInvalidIdleTimeout) {
		t.Fatalf("invalid ttl: %v %v", closed, err)
	}
	if closed, err := m.CloseIfIdle(key, "old", time.Minute); closed || !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("generation: %v %v", closed, err)
	}
	room.mu.Lock()
	room.idleSince = time.Now().Add(-2 * time.Minute)
	room.mu.Unlock()
	if err = room.Join(Participant{ID: "member"}); err != nil {
		t.Fatal(err)
	}
	if closed, err := m.CloseIfIdle(key, generation, time.Minute); closed || err != nil {
		t.Fatalf("active room: %v %v", closed, err)
	}
	if err = room.Leave("member"); err != nil {
		t.Fatal(err)
	}
	if closed, err := m.CloseIfIdle(key, generation, time.Minute); closed || err != nil {
		t.Fatalf("grace period: %v %v", closed, err)
	}
	room.mu.Lock()
	room.idleSince = time.Now().Add(-2 * time.Minute)
	room.mu.Unlock()
	if closed, err := m.CloseIfIdle(key, generation, time.Minute); !closed || err != nil {
		t.Fatalf("idle room: %v %v", closed, err)
	}
	replacement, err := m.Create(key, 4)
	if err != nil {
		t.Fatal(err)
	}
	if closed, err := m.CloseIfIdle(key, generation, time.Minute); closed || !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("stale cleanup: %v %v", closed, err)
	}
	if replacement.Snapshot().Closed {
		t.Fatal("replacement closed")
	}
}
func TestCloseIfIdleRacesJoin(t *testing.T) {
	for i := 0; i < 100; i++ {
		m := NewManager()
		key := Key{Namespace: "nvr", ID: "room"}
		room, _ := m.Create(key, 4)
		generation := room.Snapshot().Generation
		room.mu.Lock()
		room.idleSince = time.Now().Add(-time.Hour)
		room.mu.Unlock()
		var wg sync.WaitGroup
		wg.Add(2)
		var joinErr, closeErr error
		var closed bool
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; joinErr = room.Join(Participant{ID: "member"}) }()
		go func() { defer wg.Done(); <-start; closed, closeErr = m.CloseIfIdle(key, generation, time.Minute) }()
		close(start)
		wg.Wait()
		if closeErr != nil || (closed && !errors.Is(joinErr, ErrRoomClosed)) || (!closed && joinErr != nil) {
			t.Fatalf("closed %v join %v close %v", closed, joinErr, closeErr)
		}
	}
}
