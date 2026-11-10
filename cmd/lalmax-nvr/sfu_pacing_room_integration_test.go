package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/signaling"
	"github.com/stretchr/testify/require"
)

type pacingRoomPolicy struct{}

func (pacingRoomPolicy) Authorize(context.Context, access.Request) error { return nil }

type pacingRoomLoss struct {
	net.PacketConn
	armed     atomic.Bool
	packets   atomic.Uint32
	dropped   atomic.Uint32
	dropEvery uint32
}

func (c *pacingRoomLoss) ReadFrom(buf []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(buf)
		if err != nil {
			return n, addr, err
		}
		if n >= 12 && buf[0]>>6 == 2 && buf[1]&0x7f >= 96 && c.armed.Load() && c.packets.Add(1)%c.dropEvery == 0 {
			c.dropped.Add(1)
			continue
		}
		return n, addr, nil
	}
}

type pacingRoomResult struct {
	Type, ID, Error, SDP string
	Publication          *struct{ ID string }
	Snapshot             *struct {
		Publications []struct {
			ID       string
			RTPReady bool
		}
	}
}

func pacingRoomRead(t *testing.T, ws *websocket.Conn, id string) pacingRoomResult {
	t.Helper()
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(8*time.Second)))
	for {
		var result pacingRoomResult
		require.NoError(t, ws.ReadJSON(&result))
		if result.Type == "result" && result.ID == id {
			return result
		}
	}
}
func pacingRoomOffer(t *testing.T, pc *webrtc.PeerConnection) string {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	require.NoError(t, err)
	gather := webrtc.GatheringCompletePromise(pc)
	require.NoError(t, pc.SetLocalDescription(offer))
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("ICE gathering timed out")
	}
	return pc.LocalDescription().SDP
}
func pacingRoomClient(t *testing.T, loss bool) (*webrtc.PeerConnection, *pacingRoomLoss) {
	t.Helper()
	if loss {
		return pacingRoomClientWithLoss(t, 3)
	}
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return pc, nil
}

func pacingRoomClientWithLoss(t *testing.T, dropEvery uint32) (*webrtc.PeerConnection, *pacingRoomLoss) {
	t.Helper()
	require.GreaterOrEqual(t, dropEvery, uint32(2))
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	impaired := &pacingRoomLoss{PacketConn: conn, dropEvery: dropEvery}
	mux := webrtc.NewICEUDPMux(nil, impaired)
	t.Cleanup(func() { _ = mux.Close() })
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetICEUDPMux(mux)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return pc, impaired
}

func TestNVRSFUTWCCRoomIsolatesImpairedViewer(t *testing.T)  { testNVRSFURoomLoss(t, 0) }
func TestNVRSFUBWERoomIsolatesImpairedViewer(t *testing.T)   { testNVRSFURoomLoss(t, 1) }
func TestNVRSFUPacedRoomIsolatesImpairedViewer(t *testing.T) { testNVRSFURoomLoss(t, 2) }

func testNVRSFURoomLoss(t *testing.T, mode int) {
	t.Helper()
	key := sfu.Key{Namespace: "test", ID: "paced-room"}
	rooms := sfu.NewManager()
	room, err := rooms.Create(key, 8)
	require.NoError(t, err)
	var healthyEstimate, lossyEstimate atomic.Bool
	newPeer := nvrSFUNewPeer(nil)
	if mode > 0 {
		newPeer = nvrSFUNewPeerWithPacing(nil, func(id signaling.Identity, bitrate int) {
			if bitrate > 0 {
				if id.Principal == "healthy" {
					healthyEstimate.Store(true)
				}
				if id.Principal == "lossy" {
					lossyEstimate.Store(true)
				}
			}
		}, mode == 2)
	}
	factory := &signaling.RoomFactory{NewPeer: newPeer, MaxSubscribers: 8, QueueSize: 64,
		Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
			if p.Codec != "video/H264" {
				return webrtc.RTPCodecCapability{}, errors.New("unsupported")
			}
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: p.Negotiated.FMTP, RTCPFeedback: nvrSFUVideoFeedback()}, nil
		},
	}
	handler := &signaling.Handler{Rooms: rooms, Policy: pacingRoomPolicy{}, NewSessionNegotiator: factory.NewNegotiator,
		Authenticate: func(r *http.Request) (signaling.Identity, error) {
			id := r.URL.Query().Get("id")
			if id == "" {
				return signaling.Identity{}, errors.New("missing")
			}
			return signaling.Identity{Principal: id, Participant: sfu.Participant{ID: id}, Room: key}, nil
		},
		Filter: func(_ context.Context, _ signaling.Identity, snapshot sfu.Snapshot) (sfu.Snapshot, error) {
			return snapshot, nil
		},
		CheckOrigin: func(*http.Request) bool { return true },
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dial := func(id string) *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"?id="+id, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = ws.Close() })
		return ws
	}
	pubWS := dial("publisher")
	require.NoError(t, pubWS.WriteJSON(map[string]any{"id": "publish", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "video"}))
	published := pacingRoomRead(t, pubWS, "publish")
	require.Empty(t, published.Error)
	require.NotNil(t, published.Publication)
	publisher, _ := pacingRoomClient(t, false)
	input, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "paced-test")
	require.NoError(t, err)
	sender, err := publisher.AddTrack(input)
	require.NoError(t, err)
	go func() {
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	require.NoError(t, pubWS.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": pacingRoomOffer(t, publisher)}))
	answer := pacingRoomRead(t, pubWS, "offer")
	require.Empty(t, answer.Error)
	require.NoError(t, publisher.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}))
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	var sent atomic.Uint32
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var sequence uint16
		payload := make([]byte, 1000)
		payload[0] = 0x41
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sequence++
				if input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 1800, Marker: true}, Payload: payload}) == nil {
					sent.Add(1)
				}
			}
		}
	}()
	require.Eventually(t, func() bool {
		for _, p := range room.Snapshot().Publications {
			if p.ID == published.Publication.ID && p.RTPReady {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	subscribe := func(id string, impaired bool) (<-chan uint16, *pacingRoomLoss) {
		ws := dial(id)
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "subscribe", "type": "subscribe", "publication_id": published.Publication.ID}))
		result := pacingRoomRead(t, ws, "subscribe")
		require.Empty(t, result.Error)
		pc, loss := pacingRoomClient(t, impaired)
		_, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, err)
		packets := make(chan uint16, 512)
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				packet, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				select {
				case packets <- packet.SequenceNumber:
				default:
				}
			}
		})
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": pacingRoomOffer(t, pc)}))
		result = pacingRoomRead(t, ws, "offer")
		require.Empty(t, result.Error)
		require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: result.SDP}))
		return packets, loss
	}
	healthy, _ := subscribe("healthy", false)
	lossy, loss := subscribe("lossy", true)
	for _, packets := range []<-chan uint16{healthy, lossy} {
		select {
		case <-packets:
		case <-time.After(5 * time.Second):
			t.Fatal("viewer received no RTP")
		}
	}
	drain := func(ch <-chan uint16) {
		for {
			select {
			case <-ch:
			default:
				return
			}
		}
	}
	drain(healthy)
	drain(lossy)
	start := sent.Load()
	loss.armed.Store(true)
	deadline := time.After(2 * time.Second)
	healthySeen := map[uint16]bool{}
	lossySeen := map[uint16]bool{}
measure:
	for {
		select {
		case sequence := <-healthy:
			healthySeen[sequence] = true
		case sequence := <-lossy:
			lossySeen[sequence] = true
		case <-deadline:
			break measure
		}
	}
	loss.armed.Store(false)
	transmitted := sent.Load() - start
	require.Greater(t, transmitted, uint32(20))
	require.Greater(t, loss.dropped.Load(), uint32(0))
	require.GreaterOrEqual(t, len(healthySeen), int(transmitted)*6/10, "healthy viewer stopped under another viewer's loss")
	require.GreaterOrEqual(t, len(lossySeen), int(transmitted)*2/10, "impaired viewer stopped receiving")
	if mode > 0 {
		require.Eventually(t, func() bool { return healthyEstimate.Load() && lossyEstimate.Load() }, 3*time.Second, 10*time.Millisecond, "both viewers must produce a bandwidth estimate")
	}
	t.Logf("sent=%d healthy=%d impaired=%d udp_drops=%d", transmitted, len(healthySeen), len(lossySeen), loss.dropped.Load())
}
