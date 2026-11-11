package access

import (
	"context"
	"errors"
	"github.com/q191201771/lalmax/sfu"
	"testing"
)

func TestSubscribedPublicationsChecksPolicyAndGeneration(t *testing.T) {
	ctx := context.Background()
	m := sfu.NewManager()
	room, _ := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	auth := &policy{deny: map[Operation]bool{}}
	pub, _ := JoinRoom(ctx, room, auth, "p", sfu.Participant{ID: "p"})
	viewer, _ := JoinRoom(ctx, room, auth, "v", sfu.Participant{ID: "v"})
	track, _ := pub.Publish(ctx, "audio", "microphone", "audio/opus")
	if err := viewer.Subscribe(ctx, track.ID); err != nil {
		t.Fatal(err)
	}
	pubs, err := viewer.SubscribedPublications(ctx)
	if err != nil || len(pubs) != 1 || pubs[0].ID != track.ID {
		t.Fatalf("publications: %+v %v", pubs, err)
	}
	auth.set(Subscribe, true)
	if _, err = viewer.SubscribedPublications(ctx); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	auth.set(Subscribe, false)
	_ = room.Leave("v")
	_, _ = room.JoinSession(sfu.Participant{ID: "v"})
	if _, err = viewer.SubscribedPublications(ctx); !errors.Is(err, sfu.ErrStaleSession) {
		t.Fatalf("stale session: %v", err)
	}
}
