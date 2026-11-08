package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

type routerTestPolicy struct{}

func (routerTestPolicy) Authorize(context.Context, access.Request) error { return nil }

type routerTestPublisher struct{ opened, closed chan string }
type routerTestSession struct {
	logic.ICustomizePubSessionContext
	id string
}

func (*routerTestSession) WithOption(func(*base.AvPacketStreamOption)) {}
func (*routerTestSession) FeedAvPacket(base.AvPacket) error            { return nil }
func (p *routerTestPublisher) AddCustomizePubSession(id string) (logic.ICustomizePubSessionContext, error) {
	p.opened <- id
	return &routerTestSession{id: id}, nil
}
func (p *routerTestPublisher) DelCustomizePubSession(s logic.ICustomizePubSessionContext) {
	p.closed <- s.(*routerTestSession).id
}
func TestNVRSFUBridgeRouterRevokesClosedRoomOnly(t *testing.T) {
	ctx := context.Background()
	rooms := sfu.NewManager()
	grants := auth.NewMediaAuthorizer()
	publisher := &routerTestPublisher{opened: make(chan string, 4), closed: make(chan string, 4)}
	router := newNVRSFUBridgeRouter(func(context.Context, string) error { return nil })
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := router.Close(c); err != nil {
			t.Error(err)
		}
	})
	makeRoom := func(id, kind, source, codec string) (*sfu.Room, string) {
		room, err := rooms.Create(sfu.Key{Namespace: "nvr", ID: id}, 4)
		if err != nil {
			t.Fatal(err)
		}
		session, err := room.JoinSession(sfu.Participant{ID: "source"})
		if err != nil {
			t.Fatal(err)
		}
		pub, err := room.PublishSession("source", session, kind, source, codec)
		if err != nil {
			t.Fatal(err)
		}
		if err = room.EnablePublicationRTP("source", session, pub.ID, 4, 8); err != nil {
			t.Fatal(err)
		}
		service, err := sfubridge.New(room, routerTestPolicy{}, publisher, grants, func(context.Context, sfu.Publication) error { return nil }, 2)
		if err != nil {
			t.Fatal(err)
		}
		reg, err := sfubridge.NewRegistry(service, func(context.Context, sfubridge.ManagementRequest) error { return nil }, 4)
		if err != nil {
			t.Fatal(err)
		}
		if err = router.add(room, reg); err != nil {
			t.Fatal(err)
		}
		return room, pub.ID
	}
	one, video := makeRoom("one", "video", "camera", "video/H264")
	_, audio := makeRoom("two", "audio", "microphone", "audio/opus")
	if _, err := router.CreateAV(ctx, "admin", video, audio); !errors.Is(err, sfu.ErrPublicationNotFound) {
		t.Fatalf("cross-room AV: %v", err)
	}
	first, err := router.Create(ctx, "admin", video)
	if err != nil {
		t.Fatal(err)
	}
	second, err := router.Create(ctx, "admin", audio)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-publisher.opened:
		case <-time.After(3 * time.Second):
			t.Fatal("publisher did not start")
		}
	}
	_, token, err := router.Playback(ctx, "admin", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := router.Playback(ctx, "admin", second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = router.remove(ctx, one); err != nil {
		t.Fatal(err)
	}
	if err = grants.AuthorizePlayback("rtsp", first.StreamID, token); err == nil {
		t.Fatal("closed room grant still valid")
	}
	if err = grants.AuthorizePlayback("rtsp", second.StreamID, otherToken); err != nil {
		t.Fatalf("other room grant revoked: %v", err)
	}
	if _, err = router.Get(ctx, "admin", first.ID); !errors.Is(err, sfubridge.ErrNotFound) {
		t.Fatalf("closed room bridge: %v", err)
	}
	if _, err = router.Get(ctx, "admin", second.ID); err != nil {
		t.Fatal(err)
	}
}
