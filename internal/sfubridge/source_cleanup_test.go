package sfubridge

import (
	"context"
	"testing"
	"time"
)

// Exercise source lifecycle through the real bridge service, including media
// authorization and the room's system participant/subscription cleanup.
func TestBridgeSourceTerminationReleasesResources(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*Service, string) error
	}{
		{"unpublish", func(s *Service, id string) error { return s.room.Unpublish("source", id) }},
		{"leave", func(s *Service, _ string) error { return s.room.Leave("source") }},
		{"service close", func(s *Service, _ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return s.Close(ctx)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, publisher, grants, pub := setup(t)
			handle, err := service.Start(context.Background(), "operator", pub)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-publisher.opened:
			case <-time.After(3 * time.Second):
				t.Fatal("publisher not opened")
			}
			token := handle.PlaybackToken
			if err = grants.AuthorizePlayback("rtsp", handle.StreamID, token); err != nil {
				t.Fatal(err)
			}
			if err = tc.stop(service, pub); err != nil {
				t.Fatal(err)
			}
			select {
			case <-handle.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("bridge did not stop")
			}
			select {
			case <-publisher.closed:
			case <-time.After(3 * time.Second):
				t.Fatal("publisher not released")
			}
			if err = grants.AuthorizePlayback("rtsp", handle.StreamID, token); err == nil {
				t.Fatal("playback grant remained active")
			}
			snapshot := service.room.Snapshot()
			for _, member := range snapshot.Participants {
				if member.ID != "source" {
					t.Fatalf("bridge participant leaked: %+v", member)
				}
			}
			if len(snapshot.Subscriptions) != 0 {
				t.Fatalf("subscriptions leaked: %+v", snapshot.Subscriptions)
			}
		})
	}
}

func TestBridgePublisherFailureReleasesResources(t *testing.T) {
	service, _, _, grants, pub := setup(t)
	service.publisher = failingPublisher{}
	handle, err := service.Start(context.Background(), "operator", pub)
	if err != nil {
		t.Fatal(err)
	}
	token := handle.PlaybackToken
	select {
	case err = <-handle.Done():
		if err == nil {
			t.Fatal("publisher failure was not reported")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed bridge did not finish")
	}
	if err = grants.AuthorizePlayback("rtsp", handle.StreamID, token); err == nil {
		t.Fatal("failed bridge grant remained active")
	}
	snapshot := service.room.Snapshot()
	for _, member := range snapshot.Participants {
		if member.ID != "source" {
			t.Fatalf("bridge participant leaked: %+v", member)
		}
	}
	if len(snapshot.Subscriptions) != 0 {
		t.Fatalf("subscriptions leaked: %+v", snapshot.Subscriptions)
	}
}
