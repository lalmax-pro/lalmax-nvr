package sfubridge

import (
	"context"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/q191201771/lalmax/sfu"
)

func TestRegistryCombinedBridgeLifecycle(t *testing.T) {
	ctx := context.Background()
	room, _ := sfu.NewManager().Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	gen, _ := room.JoinSession(sfu.Participant{ID: "source"})
	a, _ := room.PublishSession("source", gen, "audio", "microphone", "audio/opus")
	v, _ := room.PublishSession("source", gen, "video", "camera", "video/H264")
	_ = room.EnablePublicationRTPWithCodec("source", gen, a.ID, 4, 8, sfu.NegotiatedCodec{MimeType: "audio/opus", ClockRate: 48000, Channels: 2})
	_ = room.EnablePublicationRTPWithCodec("source", gen, v.ID, 4, 8, sfu.NegotiatedCodec{MimeType: "video/H264", ClockRate: 90000})
	pub := &testPublisher{opened: make(chan string, 1), closed: make(chan struct{}, 1)}
	s, err := New(room, &testPolicy{}, pub, auth.NewMediaAuthorizer(), func(context.Context, sfu.Publication) error { return nil }, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	r, err := NewRegistry(s, func(context.Context, ManagementRequest) error { return nil }, 1)
	if err != nil {
		t.Fatal(err)
	}
	status, err := r.CreateAV(ctx, "operator", v.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.PublicationID != v.ID || status.AudioPublicationID != a.ID {
		t.Fatalf("wrong status: %+v", status)
	}
	select {
	case <-pub.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	if err = r.Stop(ctx, "operator", status.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		got, err := r.Get(ctx, "operator", status.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == StateStopped {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("bridge did not stop")
		}
	}
	if err = r.Remove(ctx, "operator", status.ID); err != nil {
		t.Fatal(err)
	}
}
