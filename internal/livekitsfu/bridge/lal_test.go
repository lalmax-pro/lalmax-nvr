package bridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/testutil/rtpforward"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

type fakePublisher struct {
	session *fakeLalSession
	addErr  error
	deleted chan logic.ICustomizePubSessionContext
}

func (p *fakePublisher) AddCustomizePubSession(string) (logic.ICustomizePubSessionContext, error) {
	if p.addErr != nil {
		return nil, p.addErr
	}
	return p.session, nil
}
func (p *fakePublisher) DelCustomizePubSession(s logic.ICustomizePubSessionContext) { p.deleted <- s }

type fakeLalSession struct {
	logic.ICustomizePubSessionContext
	format base.AvPacketStreamVideoFormat
	frames chan base.AvPacket
}

func (s *fakeLalSession) WithOption(fn func(*base.AvPacketStreamOption)) {
	var o base.AvPacketStreamOption
	fn(&o)
	s.format = o.VideoFormat
}
func (s *fakeLalSession) FeedAvPacket(p base.AvPacket) error { s.frames <- p; return nil }

func TestRunH264ToLalFeedsAndCleansUp(t *testing.T) {
	f, _ := rtpforward.New(1, 4)
	defer f.Close()
	sub, _ := f.Subscribe("bridge")
	session := &fakeLalSession{frames: make(chan base.AvPacket, 1)}
	publisher := &fakePublisher{session: session, deleted: make(chan logic.ICustomizePubSessionContext, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunH264ToLal(ctx, sub, publisher, "target") }()
	if err := f.WriteRTP(packet(1, 1, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 0xb0})...)); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-session.frames:
		if frame.PayloadType != base.AvPacketPtAvc || session.format != base.AvPacketStreamVideoFormatAnnexb {
			t.Fatal("incorrect frame or format")
		}
	case <-time.After(time.Second):
		t.Fatal("no frame")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	if <-publisher.deleted != session {
		t.Fatal("wrong session removed")
	}
	if !errors.Is(sub.Err(), rtpforward.ErrSubscriberClosed) {
		t.Fatal("subscription not closed")
	}
}
func TestRunH264ToLalConflictReleasesSubscription(t *testing.T) {
	f, _ := rtpforward.New(1, 1)
	defer f.Close()
	sub, _ := f.Subscribe("bridge")
	want := errors.New("occupied")
	publisher := &fakePublisher{addErr: want, deleted: make(chan logic.ICustomizePubSessionContext, 1)}
	if err := RunH264ToLal(context.Background(), sub, publisher, "target"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if !errors.Is(sub.Err(), rtpforward.ErrSubscriberClosed) {
		t.Fatal("subscription not closed")
	}
	select {
	case <-publisher.deleted:
		t.Fatal("deleted unowned publisher")
	default:
	}
}
