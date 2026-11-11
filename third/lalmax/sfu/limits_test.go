package sfu

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestPublicationLimitConcurrentAndReleases(t *testing.T) {
	m := NewManager()
	r, err := m.CreateWithLimits(Key{"n", "r"}, RoomLimits{MaxPublicationsPerParticipant: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Join(Participant{ID: "p"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Publish("p", "video", "camera", "video/H264")
			if err != nil && !errors.Is(err, ErrPublicationLimit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s := r.Snapshot()
	if len(s.Publications) != 1 {
		t.Fatalf("publications: %d", len(s.Publications))
	}
	if err := r.Unpublish("p", s.Publications[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish("p", "video", "camera", "video/H264"); err != nil {
		t.Fatal(err)
	}
}
func TestSubscriptionAndWatcherLimitsRelease(t *testing.T) {
	m := NewManager()
	r, err := m.CreateWithLimits(Key{"n", "r"}, RoomLimits{MaxSubscriptions: 1, MaxWatchers: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p", "a", "b"} {
		_ = r.Join(Participant{ID: id})
	}
	p, _ := r.Publish("p", "video", "camera", "video/H264")
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			err := r.Subscribe(id, p.ID)
			if err != nil && !errors.Is(err, ErrSubscriptionLimit) {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	s := r.Snapshot()
	if len(s.Subscriptions) != 1 {
		t.Fatalf("subscriptions: %d", len(s.Subscriptions))
	}
	winner := s.Subscriptions[0].ParticipantID
	if err := r.Unsubscribe(winner, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Subscribe(winner, p.ID); err != nil {
		t.Fatal(err)
	}
	_, _, cancel, err := r.Watch(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = r.Watch(1); !errors.Is(err, ErrWatcherLimit) {
		t.Fatalf("watch limit: %v", err)
	}
	cancel()
	_, _, cancel2, err := r.Watch(1)
	if err != nil {
		t.Fatal(err)
	}
	cancel2()
}
func TestLimitsValidationAndRoomIsolation(t *testing.T) {
	m := NewManager()
	for i, l := range []RoomLimits{{MaxParticipants: -1}, {MaxSubscriptions: -1}, {MaxWatchers: -1}, {MaxPublicationsPerParticipant: -1}} {
		if _, err := m.CreateWithLimits(Key{"n", fmt.Sprint(i)}, l); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("invalid limits: %v", err)
		}
	}
	a, _ := m.CreateWithLimits(Key{"n", "a"}, RoomLimits{MaxPublicationsPerParticipant: 1})
	b, _ := m.CreateWithLimits(Key{"n", "b"}, RoomLimits{MaxPublicationsPerParticipant: 1})
	for _, r := range []*Room{a, b} {
		_ = r.Join(Participant{ID: "p"})
		if _, err := r.Publish("p", "audio", "microphone", "audio/opus"); err != nil {
			t.Fatal(err)
		}
	}
}
