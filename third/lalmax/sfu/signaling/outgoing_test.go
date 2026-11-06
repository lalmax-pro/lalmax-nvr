package signaling

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

func TestAuthorizedPionMediaAcrossTwoWebSocketSessions(t *testing.T) {
	h, _ := newHandler(t)
	pubServer, viewServer := testPeer(t), testPeer(t)
	publicationIDs := make(chan string, 2)
	incomingReady := make(chan error, 1)
	outgoingReady := make(chan error, 1)
	outDone := make(chan (<-chan error), 1)
	h.NewSessionNegotiator = func(ctx context.Context, id Identity, session *access.Session) (Negotiator, error) {
		if id.Principal == "publisher" {
			pubServer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
				id := <-publicationIDs
				_, err := session.StartIncoming(ctx, id, track, 4, 32)
				incomingReady <- err
			})
			return NewPionNegotiator(pubServer)
		}
		idTrack := <-publicationIDs
		wrong, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "wrong", "viewer")
		if _, err := session.StartOutgoing(ctx, idTrack, wrong); !errors.Is(err, access.ErrOutgoingCodecMismatch) {
			return nil, errors.New("incorrect codec accepted")
		}
		local, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "viewer")
		if err != nil {
			return nil, err
		}
		sender, err := viewServer.AddTrack(local)
		if err != nil {
			return nil, err
		}
		go func() {
			for {
				if _, _, err := sender.ReadRTCP(); err != nil {
					return
				}
			}
		}()
		done, err := session.StartOutgoing(ctx, idTrack, local)
		if err == nil {
			outDone <- done
		}
		outgoingReady <- err
		if err != nil {
			return nil, err
		}
		return NewPionNegotiator(viewServer)
	}
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	pubWS, _, err := dial(t, httpServer.URL, "publisher", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer pubWS.Close()
	_ = readType(t, pubWS, "snapshot")
	if err = pubWS.WriteJSON(command{ID: "publish", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus"}); err != nil {
		t.Fatal(err)
	}
	published := readType(t, pubWS, "result")
	if published.Publication == nil {
		t.Fatalf("publish: %+v", published)
	}
	id := published.Publication.ID
	publicationIDs <- id
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
	publicationIDs <- id
	negotiateWS(t, viewWS, viewClient)
	if err = <-outgoingReady; err != nil {
		t.Fatal(err)
	}
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
	done := <-outDone
	if err = viewWS.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, rtpforward.ErrSubscriberClosed) && !errors.Is(err, rtpforward.ErrClosed) {
			t.Fatalf("outbound stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("outbound did not stop")
	}
}
func negotiateWS(t *testing.T, ws *websocket.Conn, pc *webrtc.PeerConnection) {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	g := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g:
	case <-time.After(5 * time.Second):
		t.Fatal("gather timeout")
	}
	if err = ws.WriteJSON(command{ID: "offer", Type: "offer", SDP: pc.LocalDescription().SDP}); err != nil {
		t.Fatal(err)
	}
	answer := readType(t, ws, "result")
	if answer.Error != "" || answer.SDP == "" {
		t.Fatalf("offer: %+v", answer)
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Fatal(err)
	}
}
