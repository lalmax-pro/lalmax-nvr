package bridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/testutil/rtpforward"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

func TestAVBridgePreservesH264ParametersWhileWaitingForReports(t *testing.T) {
	aFan, _ := rtpforward.New(1, 8)
	defer aFan.Close()
	a, _ := aFan.Subscribe("audio")
	vFan, _ := rtpforward.New(1, 8)
	defer vFan.Close()
	v, _ := vFan.Subscribe("video")
	session := &fakeLalSession{frames: make(chan base.AvPacket, 4)}
	publisher := &fakePublisher{session: session, deleted: make(chan logic.ICustomizePubSessionContext, 1)}
	refs := AVReferences{Audio: ClockReference{NTP: 100 << 32, RTP: 0, Rate: 48000}, Video: ClockReference{NTP: 100 << 32, RTP: 0, Rate: 90000}, AudioSSRC: 42, VideoSSRC: 42}
	var ready atomic.Bool
	var calls atomic.Int32
	seen := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunAVToLal(ctx, a, v, publisher, "combined", func(context.Context) (AVReferences, error) {
			if !ready.Load() {
				if calls.Add(1) == 2 {
					seen <- struct{}{}
				}
				return AVReferences{}, ErrAVClockNotReady
			}
			return refs, nil
		})
	}()
	_ = vFan.WriteRTP(packet(1, 100, true, stap([]byte{0x67, 1}, []byte{0x68, 2})...))
	_ = vFan.WriteRTP(packet(2, 100, true, 0x09, 0xf0))
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("parameters not read")
	}
	ready.Store(true)
	_ = vFan.WriteRTP(packet(3, 200, true, 0x65, 0xb0))
	select {
	case frame := <-session.frames:
		if frame.PayloadType != base.AvPacketPtAvc {
			t.Fatal("not video")
		}
	case <-time.After(time.Second):
		t.Fatal("keyframe without repeated parameter sets was not emitted")
	}
	cancel()
	<-done
}

func TestRunAVToLalUsesSenderClockAndReleasesSources(t *testing.T) {
	aFan, _ := rtpforward.New(1, 8)
	defer aFan.Close()
	a, _ := aFan.Subscribe("audio")
	vFan, _ := rtpforward.New(1, 8)
	defer vFan.Close()
	v, _ := vFan.Subscribe("video")
	session := &fakeLalSession{frames: make(chan base.AvPacket, 4)}
	publisher := &fakePublisher{session: session, deleted: make(chan logic.ICustomizePubSessionContext, 1)}
	refs := AVReferences{Audio: ClockReference{NTP: 100 << 32, RTP: 48000, Rate: 48000}, Video: ClockReference{NTP: 100 << 32, RTP: 90000, Rate: 90000}, AudioSSRC: 42, VideoSSRC: 42}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunAVToLal(ctx, a, v, publisher, "combined", func(context.Context) (AVReferences, error) { return refs, nil })
	}()
	if err := vFan.WriteRTP(packet(1, 90000, true, stap([]byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 0xb0})...)); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-session.frames:
		if f.PayloadType != base.AvPacketPtAvc || f.Timestamp != 0 {
			t.Fatalf("video: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("video timeout")
	}
	if err := aFan.WriteRTP(packet(1, 52800, true, 0xf8, 0xff, 0xfe)); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-session.frames:
		if f.PayloadType != base.AvPacketPtOpus || f.Timestamp != 100 {
			t.Fatalf("audio: %+v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("audio timeout")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not stop")
	}
	if <-publisher.deleted != session {
		t.Fatal("session not released")
	}
	if !errors.Is(a.Err(), rtpforward.ErrSubscriberClosed) || !errors.Is(v.Err(), rtpforward.ErrSubscriberClosed) {
		t.Fatal("subscribers not released")
	}
}
