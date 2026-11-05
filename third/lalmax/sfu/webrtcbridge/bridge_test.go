package webrtcbridge

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
)

func pc(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, a := range addresses {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ip = n.IP.To4()
			break
		}
	}
	if ip == nil {
		t.Skip("WebRTC integration test requires a non-loopback IPv4 interface")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	mux := webrtc.NewICEUDPMux(nil, conn)
	t.Cleanup(func() { _ = mux.Close() })
	setting := webrtc.SettingEngine{}
	setting.SetICEUDPMux(mux)
	setting.SetNAT1To1IPs([]string{ip.String()}, webrtc.ICECandidateTypeHost)
	p, err := webrtc.NewAPI(webrtc.WithSettingEngine(setting)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func connect(t *testing.T, sender, receiver *webrtc.PeerConnection) {
	t.Helper()
	offer, err := sender.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	g := webrtc.GatheringCompletePromise(sender)
	if err = sender.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g:
	case <-time.After(5 * time.Second):
		t.Fatal("sender ICE timeout")
	}
	if err = receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	g = webrtc.GatheringCompletePromise(receiver)
	if err = receiver.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver ICE timeout")
	}
	if err = sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		t.Fatal(err)
	}
}
func addTrack(t *testing.T, p *webrtc.PeerConnection) *webrtc.TrackLocalStaticRTP {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "test")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := p.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	return track
}
func TestRTPThroughPionRoomAndPion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := sfu.NewManager()
	room, err := m.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	publisher, _ := room.JoinSession(sfu.Participant{ID: "p"})
	viewer, _ := room.JoinSession(sfu.Participant{ID: "v"})
	pub, err := room.PublishSession("p", publisher, "audio", "microphone", "audio/opus")
	if err != nil {
		t.Fatal(err)
	}
	if err = room.EnablePublicationRTP("p", publisher, pub.ID, 2, 32); err != nil {
		t.Fatal(err)
	}
	if err = room.SubscribeSession("v", viewer, pub.ID); err != nil {
		t.Fatal(err)
	}
	sub, err := room.OpenSubscriptionRTP("v", viewer, pub.ID)
	if err != nil {
		t.Fatal(err)
	}
	inSender, inReceiver := pc(t), pc(t)
	outSender, outReceiver := pc(t), pc(t)
	input := addTrack(t, inSender)
	output := addTrack(t, outSender)
	received := make(chan *rtp.Packet, 1)
	outReceiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, err := track.ReadRTP()
		if err == nil {
			select {
			case received <- packet:
			default:
			}
		}
	})
	inboundErr := make(chan error, 1)
	inReceiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		inboundErr <- ForwardInbound(ctx, track, room, "p", publisher, pub.ID)
	})
	outboundErr := make(chan error, 1)
	go func() { outboundErr <- ForwardOutbound(ctx, sub, output) }()
	connect(t, outSender, outReceiver)
	connect(t, inSender, inReceiver)
	payload := []byte{0xf8, 0xff, 0xfe}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	var seq uint16
	for {
		select {
		case got := <-received:
			if !bytes.Equal(got.Payload, payload) {
				t.Fatalf("payload %x", got.Payload)
			}
			if len(got.GetExtensionIDs()) != 0 {
				t.Fatalf("inbound extensions leaked: %v", got.GetExtensionIDs())
			}
			cancel()
			select {
			case err := <-inboundErr:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("inbound cancellation: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("inbound did not stop")
			}
			select {
			case err := <-outboundErr:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("outbound cancellation: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("outbound did not stop")
			}
			return
		case err := <-inboundErr:
			t.Fatalf("inbound stopped: %v", err)
		case err := <-outboundErr:
			t.Fatalf("outbound stopped: %v", err)
		case <-ticker.C:
			seq++
			if err := input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: payload}); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatalf("timeout: input=%s/%s output=%s/%s", inSender.ConnectionState(), inReceiver.ConnectionState(), outSender.ConnectionState(), outReceiver.ConnectionState())
		}
	}
}
