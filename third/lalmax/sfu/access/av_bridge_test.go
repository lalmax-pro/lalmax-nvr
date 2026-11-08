package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/q191201771/lalmax/sfu"
)

func TestAVBridgeAuthorizesTracksAndStopsOnUnsubscribe(t *testing.T) {
	ctx := context.Background()
	room, _ := sfu.NewManager().Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	gen, _ := room.JoinSession(sfu.Participant{ID: "source"})
	audio, _ := room.PublishSession("source", gen, "audio", "microphone", "audio/opus")
	video, _ := room.PublishSession("source", gen, "video", "camera", "video/H264")
	otherGen, _ := room.JoinSession(sfu.Participant{ID: "other"})
	otherAudio, _ := room.PublishSession("other", otherGen, "audio", "microphone", "audio/opus")
	_ = room.EnablePublicationRTPWithCodec("source", gen, audio.ID, 4, 8, sfu.NegotiatedCodec{MimeType: "audio/opus", ClockRate: 48000, Channels: 2})
	_ = room.EnablePublicationRTPWithCodec("source", gen, video.ID, 4, 8, sfu.NegotiatedCodec{MimeType: "video/H264", ClockRate: 90000})
	auth := &policy{deny: map[Operation]bool{}}
	session, err := JoinRoom(ctx, room, auth, "alice", sfu.Participant{ID: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.Subscribe(ctx, audio.ID); err != nil {
		t.Fatal(err)
	}
	if err = session.Subscribe(ctx, video.ID); err != nil {
		t.Fatal(err)
	}
	publisher := &bridgeTestPublisher{opened: make(chan string, 1), closed: make(chan struct{})}
	if _, err = session.StartAVBridgeWithContexts(ctx, ctx, otherAudio.ID, video.ID, "target", publisher); !errors.Is(err, ErrBridgeCodec) {
		t.Fatalf("different senders accepted: %v", err)
	}
	auth.set(ManageBridge, true)
	if _, err = session.StartAVBridgeWithContexts(ctx, ctx, audio.ID, video.ID, "target", publisher); !errors.Is(err, ErrDenied) {
		t.Fatalf("policy: %v", err)
	}
	auth.set(ManageBridge, false)
	done, err := session.StartAVBridgeWithContexts(ctx, ctx, audio.ID, video.ID, "target", publisher)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not opened")
	}
	if err = session.Unsubscribe(ctx, audio.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge not stopped")
	}
	select {
	case <-publisher.closed:
	default:
		t.Fatal("publisher not released")
	}
}
