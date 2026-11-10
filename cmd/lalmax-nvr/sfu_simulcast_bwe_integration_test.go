package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/signaling"
	"github.com/stretchr/testify/require"
)

// This integration connects the Pion TWCC/GCC observer to Room layer
// selection through real source and subscriber PeerConnections. A lossy
// subscriber must fall to RID l while an unaffected subscriber stays at m.
func TestNVRSFUGCCSelectsSimulcastLayerPerViewer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key := sfu.Key{Namespace: "test", ID: "gcc-simulcast"}
	rooms := sfu.NewManager()
	room, err := rooms.Create(key, 8)
	require.NoError(t, err)

	publisherSession, err := access.JoinRoom(ctx, room, pacingRoomPolicy{}, "publisher", sfu.Participant{ID: "publisher"})
	require.NoError(t, err)
	defer publisherSession.Close()
	publication, err := publisherSession.PublishMapped(ctx, "video", "camera", "video/H264", "camera-track")
	require.NoError(t, err)

	var healthyEstimate, impairedEstimate atomic.Int64
	callbackErrors := make(chan error, 8)
	newPeer := nvrSFUNewPeerWithPacing(nil, func(id signaling.Identity, bitrate int) {
		if err := applyNVRSFUBandwidthEstimate(rooms, id, bitrate); err != nil {
			select {
			case callbackErrors <- err:
			default:
			}
		}
		switch id.Participant.ID {
		case "viewer-healthy":
			healthyEstimate.Store(int64(bitrate))
		case "viewer-impaired":
			impairedEstimate.Store(int64(bitrate))
		}
	}, false)
	inboundLayers := make(chan string, 3)
	inboundErrors := make(chan error, 3)
	factory := &signaling.RoomFactory{
		NewPeer:        newPeer,
		MaxSubscribers: 8,
		QueueSize:      64,
		Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
			if p.Kind != "video" || p.Codec != "video/H264" {
				return webrtc.RTPCodecCapability{}, errors.New("unsupported codec")
			}
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: p.Negotiated.FMTP, RTCPFeedback: nvrSFUVideoFeedback()}, nil
		},
		OnInbound: func(_ signaling.Identity, track *webrtc.TrackRemote, err error) {
			if err != nil {
				select {
				case inboundErrors <- err:
				default:
				}
				return
			}
			select {
			case inboundLayers <- track.RID():
			default:
			}
		},
	}

	publisherIdentity := signaling.Identity{Principal: "publisher", Room: key, Participant: sfu.Participant{ID: "publisher"}}
	publisherNegotiator, err := factory.NewNegotiator(ctx, publisherIdentity, publisherSession)
	require.NoError(t, err)
	defer publisherNegotiator.Close()
	publisher, tracks := pacingRoomSimulcastPublisher(t)
	sender, err := publisher.AddTrack(tracks["l"])
	require.NoError(t, err)
	for _, rid := range []string{"m", "h"} {
		require.NoError(t, sender.AddEncoding(tracks[rid]))
	}
	go func() {
		for {
			if _, _, readErr := sender.ReadRTCP(); readErr != nil {
				return
			}
		}
	}()
	publisherAnswer, err := publisherNegotiator.Answer(ctx, pacingRoomOffer(t, publisher))
	require.NoError(t, err)
	require.NoError(t, publisher.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: publisherAnswer}))
	require.Eventually(t, func() bool { return publisher.ConnectionState() == webrtc.PeerConnectionStateConnected }, 8*time.Second, 10*time.Millisecond)

	var midID, ridID uint8
	for _, extension := range sender.GetParameters().HeaderExtensions {
		switch extension.URI {
		case sdp.SDESMidURI:
			midID = uint8(extension.ID)
		case sdp.SDESRTPStreamIDURI:
			ridID = uint8(extension.ID)
		}
	}
	require.NotZero(t, midID, "MID extension was not negotiated")
	require.NotZero(t, ridID, "RID extension was not negotiated")
	mid := publisher.GetTransceivers()[0].Mid()
	require.NotEmpty(t, mid)

	stopSource := make(chan struct{})
	defer close(stopSource)
	sourceErrors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		values := map[string]byte{"l": 1, "m": 2, "h": 3}
		sequences := make(map[string]uint16, 3)
		for {
			select {
			case <-stopSource:
				return
			case <-ticker.C:
				for _, rid := range []string{"l", "m", "h"} {
					sequences[rid]++
					payload := make([]byte, 1200)
					payload[0] = 0x65
					payload[1] = values[rid]
					packet := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequences[rid], Timestamp: uint32(sequences[rid]) * 900, Marker: true}, Payload: payload}
					if err := packet.Header.SetExtension(midID, []byte(mid)); err != nil {
						select {
						case sourceErrors <- err:
						default:
						}
						return
					}
					if err := packet.Header.SetExtension(ridID, []byte(rid)); err != nil {
						select {
						case sourceErrors <- err:
						default:
						}
						return
					}
					if err := tracks[rid].WriteRTP(packet); err != nil {
						select {
						case sourceErrors <- err:
						default:
						}
						return
					}
				}
			}
		}
	}()

	bound := make(map[string]bool, 3)
	bindDeadline := time.NewTimer(10 * time.Second)
	defer bindDeadline.Stop()
	for len(bound) != 3 {
		select {
		case rid := <-inboundLayers:
			bound[rid] = true
		case err := <-inboundErrors:
			t.Fatalf("failed to bind incoming simulcast layer: %v", err)
		case err := <-sourceErrors:
			t.Fatalf("failed to write incoming simulcast RTP: %v", err)
		case <-bindDeadline.C:
			t.Fatalf("timed out binding all simulcast layers: %v", bound)
		}
	}

	type viewer struct {
		layer atomic.Uint32
		loss  *pacingRoomLoss
	}
	newViewer := func(id string, impaired bool, estimate *atomic.Int64) *viewer {
		participant := sfu.Participant{ID: id}
		session, joinErr := access.JoinRoom(ctx, room, pacingRoomPolicy{}, id, participant)
		require.NoError(t, joinErr)
		t.Cleanup(func() { session.Close() })
		require.NoError(t, session.Subscribe(ctx, publication.ID))
		identity := signaling.Identity{Principal: id, Room: key, Participant: participant}
		negotiator, negotiateErr := factory.NewNegotiator(ctx, identity, session)
		require.NoError(t, negotiateErr)
		t.Cleanup(func() { _ = negotiator.Close() })
		var pc *webrtc.PeerConnection
		var loss *pacingRoomLoss
		if impaired {
			pc, loss = pacingRoomClientWithLoss(t, 2)
		} else {
			pc, _ = pacingRoomClient(t, false)
		}
		state := &viewer{loss: loss}
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				packet, _, readErr := track.ReadRTP()
				if readErr != nil {
					return
				}
				if len(packet.Payload) > 1 {
					state.layer.Store(uint32(packet.Payload[1]))
				}
			}
		})
		_, addErr := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, addErr)
		answer, answerErr := negotiator.Answer(ctx, pacingRoomOffer(t, pc))
		require.NoError(t, answerErr)
		require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
		require.Eventually(t, func() bool { return pc.ConnectionState() == webrtc.PeerConnectionStateConnected }, 8*time.Second, 10*time.Millisecond)
		return state
	}

	healthy := newViewer("viewer-healthy", false, &healthyEstimate)
	impaired := newViewer("viewer-impaired", true, &impairedEstimate)
	require.Eventually(t, func() bool {
		return healthyEstimate.Load() > 625_000 && impairedEstimate.Load() > 625_000 && healthy.layer.Load() == 2 && impaired.layer.Load() == 2
	}, 12*time.Second, 20*time.Millisecond, "unimpaired TWCC estimates did not raise both viewers to RID m (healthy=%d/%d impaired=%d/%d)", healthy.layer.Load(), healthyEstimate.Load(), impaired.layer.Load(), impairedEstimate.Load())

	impaired.loss.armed.Store(true)
	require.Eventually(t, func() bool {
		return impaired.loss.dropped.Load() > 0 && impairedEstimate.Load() < 625_000 && impaired.layer.Load() == 1 && healthy.layer.Load() == 2
	}, 12*time.Second, 20*time.Millisecond, "GCC loss feedback did not isolate the layer downgrade (healthy=%d/%d impaired=%d/%d drops=%d)", healthy.layer.Load(), healthyEstimate.Load(), impaired.layer.Load(), impairedEstimate.Load(), impaired.loss.dropped.Load())
	select {
	case err := <-callbackErrors:
		t.Fatalf("failed to apply per-viewer bandwidth estimate: %v", err)
	default:
	}
	t.Logf("healthy RID=%d estimate=%d bps; impaired RID=%d estimate=%d bps; simulated drops=%d", healthy.layer.Load(), healthyEstimate.Load(), impaired.layer.Load(), impairedEstimate.Load(), impaired.loss.dropped.Load())
}

func pacingRoomSimulcastPublisher(t *testing.T) (*webrtc.PeerConnection, map[string]*webrtc.TrackLocalStaticRTP) {
	t.Helper()
	media := &webrtc.MediaEngine{}
	require.NoError(t, media.RegisterDefaultCodecs())
	require.NoError(t, webrtc.ConfigureSimulcastExtensionHeaders(media))
	registry := &interceptor.Registry{}
	require.NoError(t, webrtc.RegisterDefaultInterceptors(media, registry))
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings), webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(registry)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	capability := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f"}
	tracks := make(map[string]*webrtc.TrackLocalStaticRTP, 3)
	for _, rid := range []string{"l", "m", "h"} {
		track, createErr := webrtc.NewTrackLocalStaticRTP(capability, "camera-track", "publisher", webrtc.WithRTPStreamID(rid))
		require.NoError(t, createErr)
		tracks[rid] = track
	}
	return pc, tracks
}
