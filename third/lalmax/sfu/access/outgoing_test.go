package access

import (
	"context"
	"errors"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"testing"
	"time"
)

func TestOutgoingRequiresCurrentPolicyAndCleansUp(t *testing.T) {
	ctx := context.Background()
	m := sfu.NewManager()
	room, _ := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	publisher, _ := room.JoinSession(sfu.Participant{ID: "publisher"})
	pub, _ := room.PublishSession("publisher", publisher, "audio", "microphone", "audio/opus")
	_ = room.EnablePublicationRTP("publisher", publisher, pub.ID, 2, 4)
	auth := &policy{deny: map[Operation]bool{}}
	viewer, err := JoinRoom(ctx, room, auth, "viewer", sfu.Participant{ID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if err = viewer.Subscribe(ctx, pub.ID); err != nil {
		t.Fatal(err)
	}
	local, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "test")
	if err != nil {
		t.Fatal(err)
	}
	auth.set(Subscribe, true)
	if _, err = viewer.StartOutgoing(ctx, pub.ID, local); !errors.Is(err, ErrDenied) {
		t.Fatalf("denied bind: %v", err)
	}
	auth.set(Subscribe, false)
	done, stopped, err := viewer.StartOutgoingWithLifetime(ctx, pub.ID, local)
	if err != nil {
		t.Fatal(err)
	}
	if err = viewer.Unsubscribe(ctx, pub.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("unsubscribe did not synchronously cancel the feedback lifetime")
	}
	if viewer.OutgoingActive(pub.ID) {
		t.Fatal("unsubscribed binding still active")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("binding did not stop")
	}
	if len(room.Snapshot().Subscriptions) != 0 {
		t.Fatal("subscription remained")
	}
}
