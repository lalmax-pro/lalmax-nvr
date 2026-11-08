package main

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu/signaling"
	"github.com/stretchr/testify/require"
)

func TestNVRSFUEmitsTransportCCAndReceivesFeedback(t *testing.T) { testNVRSFUTWCCFeedback(t, false) }
func TestNVRSFUBandwidthObserverReceivesEstimates(t *testing.T)  { testNVRSFUTWCCFeedback(t, true) }
func TestNVRSFUWorksWithoutTransportCCNegotiation(t *testing.T)  { testNVRSFUWithoutTWCC(t, false) }
func TestNVRSFUBandwidthObserverWithoutTransportCC(t *testing.T) { testNVRSFUWithoutTWCC(t, true) }

func testNVRSFUTWCCFeedback(t *testing.T, observeBandwidth bool) {
	t.Helper()
	estimates := make(chan int, 16)
	newPeer := nvrSFUNewPeer(nil)
	if observeBandwidth {
		newPeer = nvrSFUNewPeerObservingBWE(nil, func(_ signaling.Identity, bitrate int) {
			select {
			case estimates <- bitrate:
			default:
			}
		})
	}
	sender, err := newPeer(context.Background(), signaling.Identity{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close() })
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	viewer, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: nvrSFUVideoFeedback()}, "video", "twcc-test")
	require.NoError(t, err)
	rtpSender, err := sender.AddTrack(track)
	require.NoError(t, err)
	feedback := make(chan uint16, 1)
	go func() {
		for {
			packets, _, err := rtpSender.ReadRTCP()
			if err != nil {
				return
			}
			for _, packet := range packets {
				if report, ok := packet.(*rtcp.TransportLayerCC); ok && report.PacketStatusCount > 0 {
					select {
					case feedback <- report.PacketStatusCount:
					default:
					}
				}
			}
		}
	}()
	extensions := make(chan uint16, 2)
	viewer.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		var id uint8
		for _, extension := range receiver.GetParameters().HeaderExtensions {
			if extension.URI == sdp.TransportCCURI {
				id = uint8(extension.ID)
			}
		}
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if id != 0 {
				if ext := packet.GetExtension(id); len(ext) == 2 {
					select {
					case extensions <- binary.BigEndian.Uint16(ext):
					default:
					}
				}
			}
		}
	})
	offer, err := sender.CreateOffer(nil)
	require.NoError(t, err)
	gather := webrtc.GatheringCompletePromise(sender)
	require.NoError(t, sender.SetLocalDescription(offer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("sender ICE timeout")
	}
	require.NoError(t, viewer.SetRemoteDescription(*sender.LocalDescription()))
	answer, err := viewer.CreateAnswer(nil)
	require.NoError(t, err)
	gather = webrtc.GatheringCompletePromise(viewer)
	require.NoError(t, viewer.SetLocalDescription(answer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("viewer ICE timeout")
	}
	require.NoError(t, sender.SetRemoteDescription(*viewer.LocalDescription()))
	require.Eventually(t, func() bool { return sender.ConnectionState() == webrtc.PeerConnectionStateConnected }, 5*time.Second, 10*time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(12 * time.Second)
	var sequence uint16
	var extensionSequence []uint16
	var gotFeedback bool
	gotEstimate := !observeBandwidth
	for len(extensionSequence) < 2 || !gotFeedback || !gotEstimate {
		select {
		case <-ticker.C:
			sequence++
			require.NoError(t, track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 900, Marker: true}, Payload: []byte{0x41, 0x01}}))
		case sequence := <-extensions:
			if len(extensionSequence) < 2 {
				extensionSequence = append(extensionSequence, sequence)
			}
		case <-feedback:
			gotFeedback = true
		case estimate := <-estimates:
			require.Positive(t, estimate)
			gotEstimate = true
		case <-deadline:
			t.Fatalf("transport-cc extensions=%v feedback=%v estimate=%v", extensionSequence, gotFeedback, gotEstimate)
		}
	}
	require.NotEqual(t, extensionSequence[0], extensionSequence[1], "outgoing transport sequence must advance")
}

func testNVRSFUWithoutTWCC(t *testing.T, observeBandwidth bool) {
	t.Helper()
	newPeer := nvrSFUNewPeer(nil)
	if observeBandwidth {
		newPeer = nvrSFUNewPeerObservingBWE(nil, func(signaling.Identity, int) {})
	}
	sender, err := newPeer(context.Background(), signaling.Identity{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close() })
	media := &webrtc.MediaEngine{}
	require.NoError(t, media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, PayloadType: 96}, webrtc.RTPCodecTypeVideo))
	setting := webrtc.SettingEngine{}
	setting.SetIncludeLoopbackCandidate(true)
	viewer, err := webrtc.NewAPI(webrtc.WithSettingEngine(setting), webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(&interceptor.Registry{})).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = viewer.Close() })
	_, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "without-twcc")
	require.NoError(t, err)
	_, err = sender.AddTrack(track)
	require.NoError(t, err)
	received := make(chan *rtp.Packet, 1)
	viewer.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, err := remote.ReadRTP()
		if err == nil {
			received <- packet
		}
	})
	offer, err := viewer.CreateOffer(nil)
	require.NoError(t, err)
	require.NotContains(t, offer.SDP, sdp.TransportCCURI)
	gather := webrtc.GatheringCompletePromise(viewer)
	require.NoError(t, viewer.SetLocalDescription(offer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("viewer ICE timeout")
	}
	require.NoError(t, sender.SetRemoteDescription(*viewer.LocalDescription()))
	answer, err := sender.CreateAnswer(nil)
	require.NoError(t, err)
	require.NotContains(t, answer.SDP, sdp.TransportCCURI)
	gather = webrtc.GatheringCompletePromise(sender)
	require.NoError(t, sender.SetLocalDescription(answer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("sender ICE timeout")
	}
	require.NoError(t, viewer.SetRemoteDescription(*sender.LocalDescription()))
	require.Eventually(t, func() bool { return sender.ConnectionState() == webrtc.PeerConnectionStateConnected }, 5*time.Second, 10*time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	var sequence uint16
	for {
		select {
		case packet := <-received:
			require.Empty(t, packet.GetExtensionIDs())
			require.Equal(t, []byte{0x41, 0x01}, packet.Payload)
			return
		case <-ticker.C:
			sequence++
			require.NoError(t, track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 900, Marker: true}, Payload: []byte{0x41, 0x01}}))
		case <-deadline:
			t.Fatal("viewer without transport-cc received no RTP")
		}
	}
}
