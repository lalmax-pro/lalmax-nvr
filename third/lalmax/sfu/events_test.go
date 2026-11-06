package sfu

import (
	"errors"
	"sync"
	"testing"
)

func TestWatchSnapshotAndOrderedChanges(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	if err := r.Join(Participant{ID: "p"}); err != nil {
		t.Fatal(err)
	}
	snapshot, events, cancel, err := r.Watch(8)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if snapshot.Sequence != 1 || len(snapshot.Participants) != 1 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if err := r.Join(Participant{ID: "v"}); err != nil {
		t.Fatal(err)
	}
	p, err := r.Publish("p", "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Subscribe("v", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Leave("p"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"participant_joined", "track_published", "subscription_changed", "participant_left"} {
		e := <-events
		if e.Type != want || e.Snapshot.Sequence != snapshot.Sequence+uint64(i)+1 {
			t.Fatalf("event %d: %+v", i, e)
		}
		if want == "participant_left" && (len(e.Snapshot.Publications) != 0 || len(e.Snapshot.Subscriptions) != 0) {
			t.Fatalf("cleanup not visible: %+v", e)
		}
	}
}
func TestWatchOverflowAndClose(t *testing.T) {
	m := NewManager()
	key := Key{"n", "r"}
	r, _ := m.Create(key, 0)
	if _, _, _, err := r.Watch(0); !errors.Is(err, ErrInvalidWatchBuffer) {
		t.Fatal(err)
	}
	_, events, cancel, err := r.Watch(1)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_ = r.Join(Participant{ID: "a"})
	_ = r.Join(Participant{ID: "b"})
	<-events
	if _, ok := <-events; ok {
		t.Fatal("overflow must close watch")
	}
	snap, closed, cancel2, err := r.Watch(1)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()
	if err := m.Close(key, snap.Generation); err != nil {
		t.Fatal(err)
	}
	e := <-closed
	if e.Type != "room_closed" || !e.Snapshot.Closed || len(e.Snapshot.Participants) != 0 {
		t.Fatalf("close: %+v", e)
	}
	if _, ok := <-closed; ok {
		t.Fatal("watch remains open")
	}
}
func TestConcurrentWatchSnapshotDoesNotMissJoin(t *testing.T) {
	for i := 0; i < 100; i++ {
		m := NewManager()
		r, _ := m.Create(Key{"n", "r"}, 0)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.Join(Participant{ID: "p"}) }()
		snap, events, cancel, err := r.Watch(1)
		if err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if len(snap.Participants) == 0 {
			e := <-events
			if len(e.Snapshot.Participants) != 1 || e.Snapshot.Sequence != snap.Sequence+1 {
				t.Fatal("missing join")
			}
		} else if len(snap.Participants) != 1 {
			t.Fatal("invalid snapshot")
		}
		cancel()
	}
}
