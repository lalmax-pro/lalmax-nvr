package signaling

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
)

func TestDynamicSubscriptionAfterInitialOffer(t *testing.T) {
	h, room := newHandler(t)
	pubServer, viewServer := testPeer(t), testPeer(t)
	ready := make(chan error, 1)
	factory := &RoomFactory{NewPeer: func(_ context.Context, id Identity) (*webrtc.PeerConnection, error) {
		if id.Principal == "publisher" {
			return pubServer, nil
		}
		return viewServer, nil
	}, Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
		if p.Codec != "audio/opus" {
			return webrtc.RTPCodecCapability{}, errors.New("unsupported")
		}
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
	}, MaxSubscribers: 4, QueueSize: 32, OnInbound: func(_ Identity, _ *webrtc.TrackRemote, err error) { ready <- err }}
	h.NewSessionNegotiator = factory.NewNegotiator
	server := httptest.NewServer(h)
	defer server.Close()
	viewerWS, _, err := dial(t, server.URL, "viewer", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer viewerWS.Close()
	_ = readType(t, viewerWS, "snapshot")
	viewer := testPeer(t)
	if _, err = viewer.CreateDataChannel("control", nil); err != nil {
		t.Fatal(err)
	}
	received := make(chan []byte, 1)
	viewer.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		p, _, err := tr.ReadRTP()
		if err == nil {
			received <- p.Payload
		}
	})
	negotiateWS(t, viewerWS, viewer)
	pubWS, _, err := dial(t, server.URL, "publisher", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer pubWS.Close()
	_ = readType(t, pubWS, "snapshot")
	publisher := testPeer(t)
	if _, err = publisher.CreateDataChannel("control", nil); err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, pubWS, publisher)
	if err = pubWS.WriteJSON(command{ID: "publish", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus", TrackID: "audio"}); err != nil {
		t.Fatal(err)
	}
	pub := readType(t, pubWS, "result")
	if pub.Publication == nil {
		t.Fatalf("publish: %+v", pub)
	}
	input, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "publisher")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := publisher.AddTrack(input)
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
	negotiateWS(t, pubWS, publisher)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	var seq uint16
	send := func() {
		seq++
		if err := input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}}); err != nil {
			t.Fatal(err)
		}
	}
	for {
		select {
		case err := <-ready:
			if err != nil {
				t.Fatal(err)
			}
			goto ready
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("source not ready")
		}
	}
ready:
	var mediaReady bool
	for _, p := range room.Snapshot().Publications {
		if p.ID == pub.Publication.ID {
			mediaReady = p.RTPReady
		}
	}
	if !mediaReady {
		t.Fatal("publication readiness not exposed")
	}
	if err = viewerWS.WriteJSON(command{ID: "subscribe", Type: "subscribe", PublicationID: pub.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := readType(t, viewerWS, "result"); r.Error != "" {
		t.Fatal(r.Error)
	}
	transceiver, err := viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, viewerWS, viewer)
	for {
		select {
		case payload := <-received:
			if !bytes.Equal(payload, []byte{0xf8, 0xff, 0xfe}) {
				t.Fatalf("wrong RTP %x", payload)
			}
			goto delivered
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("new subscription received no RTP")
		}
	}
delivered:
	if err = viewerWS.WriteJSON(command{ID: "unsubscribe", Type: "unsubscribe", PublicationID: pub.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := readType(t, viewerWS, "result"); r.Error != "" {
		t.Fatal(r.Error)
	}
	if err = transceiver.Stop(); err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, viewerWS, viewer)
	if len(room.Snapshot().Subscriptions) != 0 {
		t.Fatal("subscription retained")
	}
}
