package main

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

type roomLossConn struct {
	net.PacketConn
	armed   atomic.Bool
	packets atomic.Uint32
	mu      sync.Mutex
	seen    map[uint16]bool
	burst   int
	dropped chan uint16
}

func (c *roomLossConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(buf)
		if err != nil {
			return n, addr, err
		}
		if n >= 12 && buf[0]>>6 == 2 && buf[1]&0x7f >= 96 && c.armed.Load() && c.packets.Add(1) >= 5 {
			sequence := binary.BigEndian.Uint16(buf[2:4])
			c.mu.Lock()
			shouldDrop := len(c.seen) < c.burst && !c.seen[sequence]
			if shouldDrop {
				c.seen[sequence] = true
			}
			c.mu.Unlock()
			if shouldDrop {
				c.dropped <- sequence
				continue
			}
		}
		return n, addr, nil
	}
}
func roomLossViewer(t *testing.T, burst int) (*webrtc.PeerConnection, *roomLossConn) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	loss := &roomLossConn{PacketConn: conn, dropped: make(chan uint16, burst), burst: burst, seen: make(map[uint16]bool)}
	mux := webrtc.NewICEUDPMux(nil, loss)
	t.Cleanup(func() { _ = mux.Close() })
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetICEUDPMux(mux)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return pc, loss
}

func TestRoomNACKRecoversOneViewerWithoutInterruptingAnother(t *testing.T) { testRoomBurstLoss(t, 1) }
func TestRoomNACKRecoversBurstWithoutInterruptingAnother(t *testing.T)     { testRoomBurstLoss(t, 10) }

func testRoomBurstLoss(t *testing.T, burst int) {
	t.Helper()
	_, origin := setup(t)
	cookie := login(t, origin, testSecret, origin).Cookies()[0]
	pubWS, _, err := dial(t, origin, origin, cookie)
	require.NoError(t, err)
	defer pubWS.Close()
	require.NoError(t, pubWS.WriteJSON(map[string]any{"id": "publish", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "video"}))
	published := readResult(t, pubWS, "publish")
	require.Empty(t, published.Error)
	require.NotNil(t, published.Publication)
	publisher := demoPeer(t)
	input, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "test")
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
	require.NoError(t, pubWS.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": gatheredOffer(t, publisher)}))
	answer := readResult(t, pubWS, "offer")
	require.Empty(t, answer.Error)
	require.NoError(t, publisher.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}))
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		var seq uint16
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				seq++
				_ = input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 900, Marker: true}, Payload: []byte{0x41, 0x01}})
			}
		}
	}()
	setupViewer := func(pc *webrtc.PeerConnection) (*websocket.Conn, <-chan uint16) {
		ws, _, err := dial(t, origin, origin, cookie)
		require.NoError(t, err)
		_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			var state signalResult
			require.NoError(t, ws.ReadJSON(&state))
			ready := false
			if state.Snapshot != nil {
				for _, p := range state.Snapshot.Publications {
					if p.ID == published.Publication.ID && p.RTPReady {
						ready = true
					}
				}
			}
			if ready {
				break
			}
		}
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "subscribe", "type": "subscribe", "publication_id": published.Publication.ID}))
		result := readResult(t, ws, "subscribe")
		require.Empty(t, result.Error)
		_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, err)
		received := make(chan uint16, 512)
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				pkt, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				select {
				case received <- pkt.SequenceNumber:
				default:
				}
			}
		})
		require.NoError(t, ws.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": gatheredOffer(t, pc)}))
		result = readResult(t, ws, "offer")
		require.Empty(t, result.Error)
		require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: result.SDP}))
		return ws, received
	}
	lossy, loss := roomLossViewer(t, burst)
	lossyWS, lossyPackets := setupViewer(lossy)
	defer lossyWS.Close()
	healthy := demoPeer(t)
	healthyWS, healthyPackets := setupViewer(healthy)
	defer healthyWS.Close()
	for _, ch := range []<-chan uint16{lossyPackets, healthyPackets} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("viewer did not receive video")
		}
	}
	loss.armed.Store(true)
	missing := make(map[uint16]bool, burst)
	var first uint16
	for i := 0; i < burst; i++ {
		select {
		case sequence := <-loss.dropped:
			if i == 0 {
				first = sequence
			}
			require.Equal(t, first+uint16(i), sequence, "loss must be a consecutive burst")
			missing[sequence] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d packets dropped", i, burst)
		}
	}
	deadline := time.After(5 * time.Second)
	later := false
	recovered := make(map[uint16]bool, burst)
	healthySeen := make([]bool, burst+5)
	healthyCount := 0
	for len(recovered) < burst || healthyCount < len(healthySeen) {
		select {
		case sequence := <-lossyPackets:
			if sequence > first {
				later = true
			}
			if missing[sequence] {
				if sequence == first {
					require.True(t, later, "first retransmission must arrive after a newer packet")
				}
				recovered[sequence] = true
			}
		case sequence := <-healthyPackets:
			if sequence >= first && int(sequence-first) < len(healthySeen) && !healthySeen[sequence-first] {
				healthySeen[sequence-first] = true
				healthyCount++
			}
		case <-deadline:
			t.Fatalf("viewer loss recovery: first=%d recovered=%d/%d healthy=%v", first, len(recovered), burst, healthySeen)
		}
	}
}
