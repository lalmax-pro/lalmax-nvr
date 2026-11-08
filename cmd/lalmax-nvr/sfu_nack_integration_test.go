package main

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu/signaling"
	"github.com/stretchr/testify/require"
)

// Drop one encrypted RTP datagram before SRTP sees it, modeling network loss.
type nackLossConn struct {
	net.PacketConn
	dropped *atomic.Bool
}

func (c *nackLossConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(buf)
		if err != nil {
			return n, addr, err
		}
		if n >= 12 && buf[0]>>6 == 2 && buf[1]&0x7f == 96 && binary.BigEndian.Uint16(buf[2:4]) == 104 && c.dropped.CompareAndSwap(false, true) {
			continue
		}
		return n, addr, nil
	}
}

func TestNVRSFURecoversViewerPacketLossWithNACK(t *testing.T) {
	testNVRSFUNackRecovery(t, false, false)
}

func TestNVRSFUBandwidthObserverCoexistsWithNACK(t *testing.T) {
	testNVRSFUNackRecovery(t, true, false)
}

func TestNVRSFUPacingCoexistsWithNACK(t *testing.T) { testNVRSFUNackRecovery(t, true, true) }

func testNVRSFUNackRecovery(t *testing.T, observeBandwidth, enablePacing bool) {
	t.Helper()
	newPeer := nvrSFUNewPeer(nil)
	if observeBandwidth {
		newPeer = nvrSFUNewPeerWithPacing(nil, func(signaling.Identity, int) {}, enablePacing)
	}
	sender, err := newPeer(context.Background(), signaling.Identity{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close() })
	engine := &webrtc.MediaEngine{}
	require.NoError(t, engine.RegisterDefaultCodecs())
	registry := &interceptor.Registry{}
	var dropped atomic.Bool
	require.NoError(t, webrtc.RegisterDefaultInterceptors(engine, registry))
	setting := webrtc.SettingEngine{}
	setting.SetIncludeLoopbackCandidate(true)
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	mux := webrtc.NewICEUDPMux(nil, &nackLossConn{PacketConn: conn, dropped: &dropped})
	t.Cleanup(func() { _ = mux.Close() })
	setting.SetICEUDPMux(mux)
	setting.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	receiver, err := webrtc.NewAPI(webrtc.WithSettingEngine(setting), webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(registry)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = receiver.Close() })
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: nvrSFUVideoFeedback()}, "video", "nack-test")
	require.NoError(t, err)
	rtpSender, err := sender.AddTrack(track)
	require.NoError(t, err)
	var nacks atomic.Int32
	firstNack := make(chan *rtcp.TransportLayerNack, 1)
	go func() {
		for {
			packets, _, err := rtpSender.ReadRTCP()
			if err != nil {
				return
			}
			for _, packet := range packets {
				if nack, ok := packet.(*rtcp.TransportLayerNack); ok {
					nacks.Add(1)
					select {
					case firstNack <- nack:
					default:
					}
				}
			}
		}
	}()
	received := make(chan uint16, 64)
	receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			received <- pkt.SequenceNumber
		}
	})
	offer, err := sender.CreateOffer(nil)
	require.NoError(t, err)
	require.Contains(t, offer.SDP, "a=rtcp-fb:96 nack\r\n")
	gather := webrtc.GatheringCompletePromise(sender)
	require.NoError(t, sender.SetLocalDescription(offer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("sender gathering timed out")
	}
	require.NoError(t, receiver.SetRemoteDescription(*sender.LocalDescription()))
	answer, err := receiver.CreateAnswer(nil)
	require.NoError(t, err)
	require.True(t, strings.Contains(answer.SDP, "nack\r\n"), "NACK must be negotiated")
	gather = webrtc.GatheringCompletePromise(receiver)
	require.NoError(t, receiver.SetLocalDescription(answer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver gathering timed out")
	}
	require.NoError(t, sender.SetRemoteDescription(*receiver.LocalDescription()))
	require.Eventually(t, func() bool { return sender.ConnectionState() == webrtc.PeerConnectionStateConnected }, 5*time.Second, 10*time.Millisecond)
	for i := uint16(100); i < 120; i++ {
		require.NoError(t, track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: i, Timestamp: uint32(i) * 3000, Marker: true}, Payload: []byte{0x41, 0x01}}))
		time.Sleep(5 * time.Millisecond)
	}
	deadline := time.After(5 * time.Second)
	sawLater := false
	var seen []uint16
	for {
		select {
		case seq := <-received:
			seen = append(seen, seq)
			if seq > 104 {
				sawLater = true
			}
			if seq == 104 {
				require.True(t, sawLater, "recovered packet should arrive after the gap")
				require.True(t, dropped.Load())
				select {
				case nack := <-firstNack:
					var requested bool
					for _, pair := range nack.Nacks {
						pair.Range(func(sequence uint16) bool {
							if sequence == 104 {
								requested = true
							}
							return true
						})
					}
					require.True(t, requested, "NACK must request the lost packet")
				case <-time.After(time.Second):
					t.Fatal("no NACK observed by sender")
				}
				return
			}
		case <-deadline:
			select {
			case nack := <-firstNack:
				t.Logf("first nack: %+v sender: %+v", nack, rtpSender.GetParameters().Encodings)
			default:
			}
			t.Fatalf("NACK did not recover packet: dropped=%v nacks=%d received=%v", dropped.Load(), nacks.Load(), seen)
		}
	}
}
