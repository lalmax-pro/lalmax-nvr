package media

import (
	"context"
	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
	rtpmedia "github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
	"sync"
	"testing"
)

type voipTestEngine struct {
	Engine
	pub     *voipTestPub
	removed int
	mu      sync.Mutex
}

func (e *voipTestEngine) AddCustomizePubSession(context.Context, string) (CustomizePubSession, error) {
	return e.pub, nil
}
func (e *voipTestEngine) DelCustomizePubSession(context.Context, CustomizePubSession) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removed++
	return nil
}

type voipTestPub struct {
	asc     []byte
	option  base.AvPacketStreamOption
	packets []base.AvPacket
	resets  int
}

func (p *voipTestPub) WithOption(f func(*base.AvPacketStreamOption)) { f(&p.option) }
func (p *voipTestPub) FeedAudioSpecificConfig(asc []byte) error      { p.asc = asc; return nil }
func (p *voipTestPub) FeedAvPacket(pkt base.AvPacket) error {
	p.packets = append(p.packets, pkt)
	return nil
}
func (p *voipTestPub) ResetMedia() error            { p.resets++; return nil }
func (*voipTestPub) FeedRtmpMsg(base.RtmpMsg) error { return nil }

func TestVoIPPublisherAACAndConcurrentRemove(t *testing.T) {
	session, err := maxvoip.NewPubSession(maxvoip.PubSessionConfig{
		StreamName: "voip-test", MediaIP: "127.0.0.1", PortAllocator: rtpmedia.NewPortAllocator(41000, 42000),
		Negotiated: sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 97, Codec: base.AvPacketPtAac, ClockRate: 48000, CodecName: "MPEG4-GENERIC", Fmtp: "mode=AAC-hbr;config=1190"}},
		OnAvPacket: func(base.AvPacket) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Dispose()
	engine := &voipTestEngine{pub: &voipTestPub{}}
	p := NewVoIPPublisher(engine)
	if err := p.Add(session); err != nil {
		t.Fatal(err)
	}
	if len(engine.pub.asc) != 2 || engine.pub.asc[0] != 0x11 || engine.pub.asc[1] != 0x90 {
		t.Fatalf("wrong ASC: %x", engine.pub.asc)
	}
	if engine.pub.option.VideoFormat != base.AvPacketStreamVideoFormatAvcc {
		t.Fatal("wrong RTP video format")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Remove(session); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if engine.removed != 1 {
		t.Fatalf("removed publisher %d times", engine.removed)
	}
}

func TestVoIPReplacementKeepsPublisherAndDropsOldFrames(t *testing.T) {
	allocator := rtpmedia.NewPortAllocator(42000, 42100)
	makeSession := func(video bool) *maxvoip.PubSession {
		n := sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}}
		if video {
			n.Video = &sdp.Payload{PayloadType: 96, Codec: base.AvPacketPtAvc, ClockRate: 90000, CodecName: "H264"}
		}
		session, err := maxvoip.NewPubSession(maxvoip.PubSessionConfig{StreamName: "voip-replace", MediaIP: "127.0.0.1", PortAllocator: allocator, Negotiated: n, OnAvPacket: func(base.AvPacket) {}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Dispose() })
		return session
	}
	old, next := makeSession(false), makeSession(true)
	engine := &voipTestEngine{pub: &voipTestPub{}}
	publisher := NewVoIPPublisher(engine)
	if err := publisher.Add(old); err != nil {
		t.Fatal(err)
	}
	entry := publisher.sessions[old]
	oldFeed := entry.feedFor(old)
	oldFeed(base.AvPacket{Timestamp: 900, PayloadType: base.AvPacketPtG711A})
	if err := publisher.Replace(old, next); err != nil {
		t.Fatal(err)
	}
	oldFeed(base.AvPacket{Timestamp: 9999, PayloadType: base.AvPacketPtG711A})
	entry.feedFor(next)(base.AvPacket{Timestamp: 0, PayloadType: base.AvPacketPtG711A})
	if len(engine.pub.packets) != 2 || engine.pub.packets[1].Timestamp != 901 || engine.pub.resets != 1 {
		t.Fatalf("replacement timeline or stale packet: %+v resets=%d", engine.pub.packets, engine.pub.resets)
	}
	if err := publisher.Remove(old); err != nil {
		t.Fatal(err)
	}
	if engine.removed != 0 {
		t.Fatal("old session removed active publisher")
	}
	if err := publisher.Remove(next); err != nil {
		t.Fatal(err)
	}
	if engine.removed != 1 {
		t.Fatal("new publisher not removed")
	}
}
