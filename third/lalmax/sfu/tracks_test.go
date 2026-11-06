package sfu

import (
	"errors"
	"sync"
	"testing"
)

func TestPublicationAndSubscriptionsLifecycle(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "one"}, 0)
	other, _ := m.Create(Key{"n", "two"}, 0)
	for _, id := range []string{"publisher", "viewer"} {
		if err := r.Join(Participant{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	_ = other.Join(Participant{ID: "viewer"})
	p, err := r.Publish("publisher", "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Subscribe("viewer", p.ID); !errors.Is(err, ErrPublicationNotFound) {
		t.Fatalf("cross-room: %v", err)
	}
	if err := r.Subscribe("publisher", p.ID); !errors.Is(err, ErrOwnPublication) {
		t.Fatalf("own: %v", err)
	}
	if err := r.Subscribe("viewer", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Subscribe("viewer", p.ID); !errors.Is(err, ErrSubscriptionExists) {
		t.Fatalf("duplicate: %v", err)
	}
	snapshot := r.Snapshot()
	if len(snapshot.Publications) != 1 || len(snapshot.Subscriptions) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	snapshot.Publications[0].Codec = "changed"
	if r.Snapshot().Publications[0].Codec != "video/H264" {
		t.Fatal("snapshot shares state")
	}
	if err := r.Unpublish("viewer", p.ID); !errors.Is(err, ErrPublicationNotFound) {
		t.Fatalf("non-owner unpublish: %v", err)
	}
	if err := r.Leave("publisher"); err != nil {
		t.Fatal(err)
	}
	snapshot = r.Snapshot()
	if len(snapshot.Publications) != 0 || len(snapshot.Subscriptions) != 0 {
		t.Fatalf("publisher cleanup: %+v", snapshot)
	}
}
func TestConcurrentSubscriptionAndUnpublish(t *testing.T) {
	m := NewManager()
	r, _ := m.Create(Key{"n", "r"}, 0)
	_ = r.Join(Participant{ID: "p"})
	_ = r.Join(Participant{ID: "v"})
	p, err := r.Publish("p", "audio", "microphone", "audio/opus")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			err := r.Subscribe("v", p.ID)
			if err != nil && !errors.Is(err, ErrSubscriptionExists) && !errors.Is(err, ErrPublicationNotFound) {
				t.Error(err)
			}
			_ = r.Unsubscribe("v", p.ID)
		}
	}()
	go func() {
		defer wg.Done()
		if err := r.Unpublish("p", p.ID); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	s := r.Snapshot()
	if len(s.Publications) != 0 || len(s.Subscriptions) != 0 {
		t.Fatalf("stale state: %+v", s)
	}
}
func TestCloseRemovesTracks(t *testing.T) {
	m := NewManager()
	key := Key{"n", "r"}
	r, _ := m.Create(key, 0)
	_ = r.Join(Participant{ID: "p"})
	_ = r.Join(Participant{ID: "v"})
	p, _ := r.Publish("p", "video", "screen", "video/H264")
	_ = r.Subscribe("v", p.ID)
	if err := m.Close(key, r.Snapshot().Generation); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if !s.Closed || len(s.Publications) != 0 || len(s.Subscriptions) != 0 {
		t.Fatalf("closed room retained state: %+v", s)
	}
}
