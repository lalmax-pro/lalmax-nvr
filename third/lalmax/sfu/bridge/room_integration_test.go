package bridge

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/sfu"
)

type groupHook struct{ messages chan base.RtmpMsg }

func (h *groupHook) OnMsg(m base.RtmpMsg) { h.messages <- m.Clone() }
func (h *groupHook) OnStop()              {}

// This exercises the trusted assembly boundary; Room access policy and target
// authorization are still the embedding application's responsibility.
func TestRoomToLalGroupAndViewerIsolation(t *testing.T) {
	t.Chdir(t.TempDir())
	manager := sfu.NewManager()
	room, err := manager.Create(sfu.Key{Namespace: "test", ID: "room"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pubSession, err := room.JoinSession(sfu.Participant{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	bridgeSession, err := room.JoinSession(sfu.Participant{ID: "bridge"})
	if err != nil {
		t.Fatal(err)
	}
	viewerSession, err := room.JoinSession(sfu.Participant{ID: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := room.PublishSession("source", pubSession, "video", "camera", "video/H264")
	if err != nil {
		t.Fatal(err)
	}
	if err = room.EnablePublicationRTP("source", pubSession, pub.ID, 2, 8); err != nil {
		t.Fatal(err)
	}
	for _, id := range []struct{ id, session string }{{"bridge", bridgeSession}, {"viewer", viewerSession}} {
		if err = room.SubscribeSession(id.id, id.session, pub.ID); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := room.OpenSubscriptionRTP("bridge", bridgeSession, pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := room.OpenSubscriptionRTP("viewer", viewerSession, pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	server := logic.NewServerManager(func(o *logic.Option) {
		o.ConfRawContent = []byte(`{"hls":{"enable":true,"on_demand":true,"url_pattern":"/hls/"}}`)
	})
	hook := &groupHook{messages: make(chan base.RtmpMsg, 8)}
	ready := make(chan struct{})
	server.WithOnHookSession(func(_, _ string) logic.ICustomizeHookSessionContext { close(ready); return hook })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunH264ToLal(ctx, sub, server, "room-video") }()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher not registered")
	}
	sps, _ := hex.DecodeString("67640032ad84010c20086100430802184010c200843b5014005ad370101014000003000400000300ca100002")
	pps, _ := hex.DecodeString("68ee3cb0")
	if err = room.WritePublicationRTP("source", pubSession, pub.ID, packet(1, 1, true, stap(sps, pps, []byte{0x65, 0x88})...)); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []byte{base.RtmpAvcPacketTypeSeqHeader, base.RtmpAvcPacketTypeNalu} {
		deadline := time.After(3 * time.Second)
	video:
		for {
			select {
			case msg := <-hook.messages:
				if msg.Header.MsgTypeId != base.RtmpTypeIdVideo {
					continue
				}
				if len(msg.Payload) < 2 || msg.Payload[1] != typ {
					t.Fatalf("unexpected RTMP message: %+v", msg.Header)
				}
				break video
			case <-deadline:
				t.Fatal("no group video")
			}
		}
	}
	select {
	case <-viewer.Packets():
	case <-time.After(3 * time.Second):
		t.Fatal("viewer did not receive RTP")
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	group := server.GetGroup("live", "room-video")
	if group == nil || group.HasInSession() {
		t.Fatal("publisher not released")
	}
	if err = room.WritePublicationRTP("source", pubSession, pub.ID, packet(2, 9001, true, 0x41, 0xc0)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-viewer.Packets():
	case <-time.After(3 * time.Second):
		t.Fatal("viewer interrupted by bridge stop")
	}
}
