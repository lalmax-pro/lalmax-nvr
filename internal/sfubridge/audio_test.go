package sfubridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lalmax/sfu"
)

type audioPublisher struct {
	opened chan struct{}
	frames chan base.AvPacket
	closed chan struct{}
}

func (p *audioPublisher) AddCustomizePubSession(string) (logic.ICustomizePubSessionContext, error) {
	close(p.opened)
	return &audioSession{frames: p.frames}, nil
}
func (p *audioPublisher) DelCustomizePubSession(logic.ICustomizePubSessionContext) { close(p.closed) }

type audioSession struct {
	logic.ICustomizePubSessionContext
	frames chan base.AvPacket
}

func (*audioSession) WithOption(func(*base.AvPacketStreamOption)) {}
func (s *audioSession) FeedAvPacket(p base.AvPacket) error        { s.frames <- p; return nil }

func TestAudioBridgeForwardsAndCleansUp(t *testing.T) {
	ctx := context.Background()
	room, err := sfu.NewManager().Create(sfu.Key{Namespace: "nvr", ID: "audio"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := room.JoinSession(sfu.Participant{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := room.PublishSession("source", gen, "audio", "microphone", "audio/opus")
	if err != nil {
		t.Fatal(err)
	}
	if err = room.EnablePublicationRTPWithCodec("source", gen, pub.ID, 4, 8, sfu.NegotiatedCodec{MimeType: "audio/opus", ClockRate: 48000, Channels: 2}); err != nil {
		t.Fatal(err)
	}
	publisher := &audioPublisher{opened: make(chan struct{}), frames: make(chan base.AvPacket, 1), closed: make(chan struct{})}
	s, err := New(room, &testPolicy{}, publisher, auth.NewMediaAuthorizer(), func(_ context.Context, p sfu.Publication) error {
		if p.Kind != "audio" {
			return errors.New("wrong source")
		}
		return nil
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	h, err := s.Start(ctx, "operator", pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-publisher.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher did not start")
	}
	pkt := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1, SequenceNumber: 1}, Payload: []byte{0xf8, 0xff, 0xfe}}
	if err = room.WritePublicationRTP("source", gen, pub.ID, pkt); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-publisher.frames:
		if frame.PayloadType != base.AvPacketPtOpus || string(frame.Payload) != string(pkt.Payload) {
			t.Fatalf("unexpected frame: %+v", frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("audio was not forwarded")
	}
	h.Stop()
	select {
	case <-h.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
	select {
	case <-publisher.closed:
	default:
		t.Fatal("publisher was not closed")
	}
}
