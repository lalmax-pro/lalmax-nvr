package rtc

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Closing a session must preserve the server's shared ICE port for subsequent calls.
func TestPeerConnectionReceivesRTPAfterSharedMuxReuse(t *testing.T) {
	ip := testLocalIPv4(t)
	serverMux := testUDPMux(t, ip)
	clientMux := testUDPMux(t, ip)
	for _, name := range []string{"first_call", "next_call"} {
		t.Run(name, func(t *testing.T) {
			receiver, err := newPeerConnection([]string{ip.String()}, serverMux, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = receiver.Close() })
			sender, err := newPeerConnection([]string{ip.String()}, clientMux, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sender.Close() })
			track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeOpus, ClockRate: 48000,
			}, "audio", "voip")
			if err != nil {
				t.Fatal(err)
			}
			rtpSender, err := sender.AddTrack(track)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for {
					if _, _, err := rtpSender.ReadRTCP(); err != nil {
						return
					}
				}
			}()
			received := make(chan []byte, 1)
			receiver.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
				if packet, _, err := remote.ReadRTP(); err == nil {
					received <- packet.Payload
				}
			})
			offer, err := sender.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			setGatheredDescription(t, sender.PeerConnection, offer)
			if err := receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
				t.Fatal(err)
			}
			answer, err := receiver.CreateAnswer(nil)
			if err != nil {
				t.Fatal(err)
			}
			setGatheredDescription(t, receiver.PeerConnection, answer)
			if err := sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
				t.Fatal(err)
			}
			payload := []byte{0xf8, 0xff, 0xfe}
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.NewTimer(10 * time.Second)
			defer timeout.Stop()
			var sequence uint16
			for {
				select {
				case got := <-received:
					if !bytes.Equal(got, payload) {
						t.Fatalf("received RTP payload %x, want %x", got, payload)
					}
					return
				case <-ticker.C:
					sequence++
					if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{
						Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960,
					}, Payload: payload}); err != nil {
						t.Fatal(err)
					}
				case <-timeout.C:
					t.Fatalf("no RTP received: sender=%s receiver=%s", sender.ConnectionState(), receiver.ConnectionState())
				}
			}
		})
	}
}

func setGatheredDescription(t *testing.T, pc *webrtc.PeerConnection, description webrtc.SessionDescription) {
	t.Helper()
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(description); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("ICE gathering timed out")
	}
}

func testUDPMux(t *testing.T, ip net.IP) ice.UDPMux {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	mux := webrtc.NewICEUDPMux(nil, conn)
	t.Cleanup(func() { _ = mux.Close() })
	return mux
}

func testLocalIPv4(t *testing.T) net.IP {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if ipNet, ok := address.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.To4()
		}
	}
	t.Skip("WebRTC integration test requires a non-loopback IPv4 interface")
	return nil
}
