package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu/signaling"
)

func TestNVRSFUTURNRelay(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const user, password, realm = "test-user", "test-password", "sfu-ice-test"
	key := turn.GenerateAuthKey(user, realm, password)
	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       realm,
		AuthHandler: func(req *turn.RequestAttributes) (string, []byte, bool) { return user, key, req.Username == user },
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: listener,
			PermissionHandler:     func(_ net.Addr, peer net.IP) bool { return peer.IsLoopback() },
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.IPv4(127, 0, 0, 1), Address: "127.0.0.1"},
		}},
	})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer server.Close()
	ice := webrtc.ICEServer{URLs: []string{fmt.Sprintf("turn:%s?transport=udp", listener.LocalAddr())}, Username: user, Credential: password, CredentialType: webrtc.ICECredentialTypePassword}
	answerer, err := nvrSFUNewPeer([]webrtc.ICEServer{ice})(context.Background(), signaling.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	defer answerer.Close()
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	offerer, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{ice}, ICETransportPolicy: webrtc.ICETransportPolicyRelay})
	if err != nil {
		t.Fatal(err)
	}
	defer offerer.Close()
	video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: nvrSFUVideoFeedback()}, "relay-video", "relay-test")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := answerer.AddTrack(video)
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
	if _, err = offerer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	packets := make(chan *rtp.Packet, 1)
	offerer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, err := track.ReadRTP()
		if err == nil {
			packets <- packet
		}
	})
	channel, err := offerer.CreateDataChannel("relay-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	channel.OnOpen(func() { close(opened) })
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(offerer)
	if err = offerer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(10 * time.Second):
		t.Fatal("offer ICE gathering timed out")
	}
	if err = answerer.SetRemoteDescription(*offerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather = webrtc.GatheringCompletePromise(answerer)
	if err = answerer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(10 * time.Second):
		t.Fatal("answer ICE gathering timed out")
	}
	if err = offerer.SetRemoteDescription(*answerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("TURN relay connection timed out")
	}
	pair, err := offerer.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local.Typ != webrtc.ICECandidateTypeRelay {
		t.Fatalf("connection did not use TURN relay: %v %v", pair, err)
	}
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint16
	for {
		select {
		case packet := <-packets:
			if len(packet.Payload) != 2 || packet.Payload[0] != 0x41 || packet.Payload[1] != 0x01 {
				t.Fatalf("unexpected relay RTP: %x", packet.Payload)
			}
			return
		case <-ticker.C:
			sequence++
			if err := video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 1800, Marker: true}, Payload: []byte{0x41, 0x01}}); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("no video RTP through TURN relay")
		}
	}
}
