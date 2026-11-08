package signaling

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
)

// Exercise the signaling factory and actual Pion RTP transports in both
// directions without allocating a lal Group or a custom publisher.
func TestTwoParticipantsExchangeAudioAndVideo(t *testing.T) {
	h, room := newHandler(t)
	serverPeers := map[string]*webrtc.PeerConnection{"member-0": testPeer(t), "member-1": testPeer(t)}
	factory := &RoomFactory{NewPeer: func(_ context.Context, id Identity) (*webrtc.PeerConnection, error) {
		peer := serverPeers[id.Principal]
		if peer == nil {
			return nil, fmt.Errorf("unexpected identity")
		}
		return peer, nil
	},
		Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
			switch p.Codec {
			case "audio/opus":
				return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
			case "video/H264":
				return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, nil
			default:
				return webrtc.RTPCodecCapability{}, fmt.Errorf("unexpected codec")
			}
		}, MaxSubscribers: 8, QueueSize: 64,
	}
	h.NewSessionNegotiator = factory.NewNegotiator
	server := httptest.NewServer(h)
	defer server.Close()
	type receivedPacket struct {
		id   string
		kind webrtc.RTPCodecType
		tag  byte
	}
	type client struct {
		ws      *websocket.Conn
		pc      *webrtc.PeerConnection
		tracks  []*webrtc.TrackLocalStaticRTP
		ids     []string
		packets chan receivedPacket
	}
	participants := make([]*client, 2)
	for i := range participants {
		name := fmt.Sprintf("member-%d", i)
		ws, _, err := dial(t, server.URL, name, "https://trusted.example")
		if err != nil {
			t.Fatal(err)
		}
		defer ws.Close()
		_ = readType(t, ws, "snapshot")
		pc := testPeer(t)
		c := &client{ws: ws, pc: pc, packets: make(chan receivedPacket, 64)}
		pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			for {
				packet, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				if len(packet.Payload) == 0 {
					continue
				}
				select {
				case c.packets <- receivedPacket{id: track.ID(), kind: track.Kind(), tag: packet.Payload[len(packet.Payload)-1]}:
				default:
				}
			}
		})
		if _, err = pc.CreateDataChannel("control", nil); err != nil {
			t.Fatal(err)
		}
		for _, track := range []struct {
			kind, source, codec string
			capability          webrtc.RTPCodecCapability
		}{
			{"audio", "microphone", "audio/opus", webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}},
			{"video", "camera", "video/H264", webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}},
		} {
			trackID := fmt.Sprintf("%s-%s", name, track.kind)
			if err = ws.WriteJSON(command{ID: trackID, Type: "publish", Kind: track.kind, Source: track.source, Codec: track.codec, TrackID: trackID}); err != nil {
				t.Fatal(err)
			}
			published := readType(t, ws, "result")
			if published.Error != "" || published.Publication == nil {
				t.Fatalf("publish: %+v", published)
			}
			c.ids = append(c.ids, published.Publication.ID)
			local, err := webrtc.NewTrackLocalStaticRTP(track.capability, trackID, name)
			if err != nil {
				t.Fatal(err)
			}
			sender, err := pc.AddTrack(local)
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
			c.tracks = append(c.tracks, local)
		}
		negotiateWS(t, ws, pc)
		participants[i] = c
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint16
	send := func() {
		sequence++
		for i, c := range participants {
			for j, track := range c.tracks {
				payload := []byte{0xf8, 0xff, byte(1 + i*2 + j)}
				if j == 1 {
					payload = []byte{0x65, 0x88, byte(1 + i*2 + j)}
				}
				if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 3000}, Payload: payload}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	deadline := time.After(10 * time.Second)
	for {
		ready := 0
		for _, p := range room.Snapshot().Publications {
			if p.RTPReady {
				ready++
			}
		}
		if ready == 4 {
			break
		}
		select {
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("four media publications not ready")
		}
	}
	for i, c := range participants {
		other := participants[1-i]
		for j, id := range other.ids {
			if err := c.ws.WriteJSON(command{ID: fmt.Sprintf("sub-%d", j), Type: "subscribe", PublicationID: id}); err != nil {
				t.Fatal(err)
			}
			if r := readType(t, c.ws, "result"); r.Error != "" {
				t.Fatal(r.Error)
			}
			kind := webrtc.RTPCodecTypeAudio
			if j == 1 {
				kind = webrtc.RTPCodecTypeVideo
			}
			if _, err := c.pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
				t.Fatal(err)
			}
		}
		negotiateWS(t, c.ws, c.pc)
	}
	seen := [2]map[string]bool{{}, {}}
	deadline = time.After(10 * time.Second)
	for len(seen[0]) < 2 || len(seen[1]) < 2 {
		select {
		case packet := <-participants[0].packets:
			if packet.id == participants[1].ids[0] && packet.kind == webrtc.RTPCodecTypeAudio && packet.tag == 3 {
				seen[0][packet.id] = true
			}
			if packet.id == participants[1].ids[1] && packet.kind == webrtc.RTPCodecTypeVideo && packet.tag == 4 {
				seen[0][packet.id] = true
			}
		case packet := <-participants[1].packets:
			if packet.id == participants[0].ids[0] && packet.kind == webrtc.RTPCodecTypeAudio && packet.tag == 1 {
				seen[1][packet.id] = true
			}
			if packet.id == participants[0].ids[1] && packet.kind == webrtc.RTPCodecTypeVideo && packet.tag == 2 {
				seen[1][packet.id] = true
			}
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatalf("missing bidirectional RTP: %+v", seen)
		}
	}
	if len(room.Snapshot().Subscriptions) != 4 {
		t.Fatalf("expected four subscriptions, got %+v", room.Snapshot().Subscriptions)
	}
}
