package signaling

import (
	"bytes"
	"context"
	"errors"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRoomFactoryMediaAcrossTwoWebSocketSessions(t *testing.T) {
	h, room := newHandler(t)
	pubServer, viewServer := testPeer(t), testPeer(t)
	incomingReady := make(chan error, 1)
	factory := &RoomFactory{
		NewPeer: func(_ context.Context, id Identity) (*webrtc.PeerConnection, error) {
			if id.Principal == "publisher" {
				return pubServer, nil
			}
			return viewServer, nil
		},
		Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
			if p.Codec != "audio/opus" {
				return webrtc.RTPCodecCapability{}, errors.New("unsupported codec")
			}
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
		},
		MaxSubscribers: 4, QueueSize: 32,
		OnInbound: func(_ Identity, _ *webrtc.TrackRemote, err error) { incomingReady <- err },
	}
	h.NewSessionNegotiator = factory.NewNegotiator
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	pubWS, _, err := dial(t, httpServer.URL, "publisher", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer pubWS.Close()
	_ = readType(t, pubWS, "snapshot")
	if err = pubWS.WriteJSON(command{ID: "publish", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus", TrackID: "audio"}); err != nil {
		t.Fatal(err)
	}
	published := readType(t, pubWS, "result")
	if published.Publication == nil {
		t.Fatalf("publish: %+v", published)
	}
	id := published.Publication.ID
	pubClient := testPeer(t)
	input, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "pub")
	if err != nil {
		t.Fatal(err)
	}
	pubSender, err := pubClient.AddTrack(input)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, _, err := pubSender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	negotiateWS(t, pubWS, pubClient)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(8 * time.Second)
	var seq uint16
	send := func() {
		seq++
		if err := input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}}); err != nil {
			t.Fatal(err)
		}
	}
	for {
		select {
		case err := <-incomingReady:
			if err != nil {
				t.Fatal(err)
			}
			goto incoming
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("publisher track not ready")
		}
	}
incoming:
	viewWS, _, err := dial(t, httpServer.URL, "viewer", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer viewWS.Close()
	_ = readType(t, viewWS, "snapshot")
	if err = viewWS.WriteJSON(command{ID: "subscribe", Type: "subscribe", PublicationID: id}); err != nil {
		t.Fatal(err)
	}
	if reply := readType(t, viewWS, "result"); reply.Error != "" {
		t.Fatalf("subscribe: %+v", reply)
	}
	viewClient := testPeer(t)
	if _, err = viewClient.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	received := make(chan []byte, 1)
	viewClient.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		p, _, err := tr.ReadRTP()
		if err == nil {
			received <- p.Payload
		}
	})
	negotiateWS(t, viewWS, viewClient)
	for {
		select {
		case got := <-received:
			if !bytes.Equal(got, []byte{0xf8, 0xff, 0xfe}) {
				t.Fatalf("payload: %x", got)
			}
			goto delivered
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("viewer received no RTP")
		}
	}
delivered:
	if err = viewWS.Close(); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(3 * time.Second)
	for len(room.Snapshot().Subscriptions) != 0 {
		if time.Now().After(until) {
			t.Fatal("subscription leaked after close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRoomFactoryRejectsCodecMismatchAndClosesPeer(t *testing.T) {
	ctx := context.Background()
	manager := sfu.NewManager()
	room, _ := manager.Create(sfu.Key{Namespace: "n", ID: "r"}, 0)
	p, _ := room.JoinSession(sfu.Participant{ID: "p"})
	track, _ := room.PublishSession("p", p, "audio", "microphone", "audio/opus")
	_ = room.EnablePublicationRTP("p", p, track.ID, 2, 4)
	viewer, err := access.JoinRoom(ctx, room, testPolicy{}, "viewer", sfu.Participant{ID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	if err = viewer.Subscribe(ctx, track.ID); err != nil {
		t.Fatal(err)
	}
	peer := testPeer(t)
	factory := &RoomFactory{NewPeer: func(context.Context, Identity) (*webrtc.PeerConnection, error) { return peer, nil }, Codec: func(sfu.Publication) (webrtc.RTPCodecCapability, error) {
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, nil
	}, MaxSubscribers: 2, QueueSize: 4}
	if _, err = factory.NewNegotiator(ctx, Identity{Principal: "viewer"}, viewer); err == nil {
		t.Fatal("mismatched codec accepted")
	}
	if peer.ConnectionState() != webrtc.PeerConnectionStateClosed {
		t.Fatalf("peer not closed: %s", peer.ConnectionState())
	}
	if _, err = (&RoomFactory{}).NewNegotiator(ctx, Identity{}, viewer); err == nil {
		t.Fatal("incomplete config accepted")
	}
}
