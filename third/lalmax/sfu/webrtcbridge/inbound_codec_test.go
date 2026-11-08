package webrtcbridge

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
)

type changingTrack struct {
	codec   webrtc.RTPCodecParameters
	packets []*rtp.Packet
	next    webrtc.RTPCodecParameters
}

func (t *changingTrack) Codec() webrtc.RTPCodecParameters { return t.codec }
func (t *changingTrack) SetReadDeadline(time.Time) error  { return nil }
func (t *changingTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	if len(t.packets) == 0 {
		return nil, nil, io.EOF
	}
	p := t.packets[0]
	t.packets = t.packets[1:]
	if len(t.packets) == 0 {
		t.codec = t.next
	}
	return p, nil, nil
}
func TestInboundStopsBeforeForwardingChangedCodec(t *testing.T) {
	rooms := sfu.NewManager()
	room, err := rooms.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pubSession, _ := room.JoinSession(sfu.Participant{ID: "p"})
	viewerSession, _ := room.JoinSession(sfu.Participant{ID: "v"})
	p, _ := room.PublishSession("p", pubSession, "video", "camera", "video/H264")
	if err = room.EnablePublicationRTP("p", pubSession, p.ID, 2, 4); err != nil {
		t.Fatal(err)
	}
	if err = room.SubscribeSession("v", viewerSession, p.ID); err != nil {
		t.Fatal(err)
	}
	sub, err := room.OpenSubscriptionRTP("v", viewerSession, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	old := webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/H264", ClockRate: 90000, SDPFmtpLine: "profile-level-id=42e01f"}, PayloadType: 96}
	next := old
	next.SDPFmtpLine = "profile-level-id=64001f"
	track := &changingTrack{codec: old, next: next, packets: []*rtp.Packet{{Header: rtp.Header{PayloadType: 96}, Payload: []byte{1}}, {Header: rtp.Header{PayloadType: 96}, Payload: []byte{2}}}}
	if err = forwardInbound(context.Background(), track, room, "p", pubSession, p.ID, old); !errors.Is(err, ErrInboundCodecChanged) {
		t.Fatalf("change: %v", err)
	}
	select {
	case p := <-sub.Packets():
		if p.Payload[0] != 1 {
			t.Fatalf("wrong packet: %v", p.Payload)
		}
	default:
		t.Fatal("first packet lost")
	}
	select {
	case p := <-sub.Packets():
		t.Fatalf("changed codec forwarded: %v", p)
	default:
	}
}
