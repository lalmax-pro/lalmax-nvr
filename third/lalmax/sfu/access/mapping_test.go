package access

import (
	"context"
	"errors"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"sync"
	"testing"
)

func TestTrackMappingPolicyIsolationAndRelease(t *testing.T) {
	ctx := context.Background()
	m := sfu.NewManager()
	r, _ := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	p := &policy{deny: map[Operation]bool{}}
	a, _ := JoinRoom(ctx, r, p, "a", sfu.Participant{ID: "a"})
	b, _ := JoinRoom(ctx, r, p, "b", sfu.Participant{ID: "b"})
	first, err := a.PublishMapped(ctx, "audio", "microphone", "audio/opus", "mic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.PublishMapped(ctx, "audio", "microphone", "audio/opus", "mic"); !errors.Is(err, ErrTrackIDInUse) {
		t.Fatal(err)
	}
	if _, err = b.PublishMapped(ctx, "audio", "microphone", "audio/opus", "mic"); err != nil {
		t.Fatalf("cross-session collision: %v", err)
	}
	if _, err = a.PublishMapped(ctx, "audio", "microphone", "audio/opus", "\n"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	p.set(Publish, true)
	if _, err = a.PublishMapped(ctx, "audio", "microphone", "audio/opus", "denied"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	p.set(Publish, false)
	if err = a.Unpublish(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.PublishMapped(ctx, "audio", "microphone", "audio/opus", "mic"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.StartMappedIncoming(ctx, &webrtc.TrackRemote{}, 1, 1); !errors.Is(err, ErrTrackNotMapped) {
		t.Fatal(err)
	}
}
func TestConcurrentTrackIDMapping(t *testing.T) {
	ctx := context.Background()
	m := sfu.NewManager()
	r, _ := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	p := &policy{deny: map[Operation]bool{}}
	a, _ := JoinRoom(ctx, r, p, "a", sfu.Participant{ID: "a"})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.PublishMapped(ctx, "audio", "microphone", "audio/opus", "same")
			if err != nil && !errors.Is(err, ErrTrackIDInUse) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(r.Snapshot().Publications) != 1 {
		t.Fatal("duplicate publications")
	}
}
