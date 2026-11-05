package signaling

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

func TestAuthorizedInboundTrackThroughRoom(t *testing.T) {
	h, room := newHandler(t)
	server := testPeer(t)
	type trackIDs struct{ audio, video string }
	ids := make(chan trackIDs, 1)
	started := make(chan error, 1)
	ended := make(chan (<-chan error), 1)
	h.NewSessionNegotiator = func(ctx context.Context, _ Identity, session *access.Session) (Negotiator, error) {
		server.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			pair := <-ids
			if _, err := session.StartIncoming(ctx, pair.video, track, 2, 16); !errors.Is(err, access.ErrCodecMismatch) {
				started <- errors.New("mismatched codec accepted")
				return
			}
			done, err := session.StartMappedIncoming(ctx, track, 2, 16)
			if err == nil {
				ended <- done
			}
			started <- err
		})
		return NewPionNegotiator(server)
	}
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	ws, _, err := dial(t, httpServer.URL, "publisher", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = readType(t, ws, "snapshot")
	if err = ws.WriteJSON(command{ID: "publish", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus", TrackID: "audio"}); err != nil {
		t.Fatal(err)
	}
	published := readType(t, ws, "result")
	if published.Error != "" || published.Publication == nil {
		t.Fatalf("publish: %+v", published)
	}
	id := published.Publication.ID
	if err = ws.WriteJSON(command{ID: "video", Type: "publish", Kind: "video", Source: "camera", Codec: "video/H264"}); err != nil {
		t.Fatal(err)
	}
	video := readType(t, ws, "result")
	if video.Error != "" || video.Publication == nil {
		t.Fatalf("video publication: %+v", video)
	}
	ids <- trackIDs{audio: id, video: video.Publication.ID}
	client := testPeer(t)
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "test")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := client.AddTrack(track)
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
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(client)
	if err = client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("gather timeout")
	}
	if err = ws.WriteJSON(command{ID: "offer", Type: "offer", SDP: client.LocalDescription().SDP}); err != nil {
		t.Fatal(err)
	}
	reply := readType(t, ws, "result")
	if reply.Error != "" {
		t.Fatalf("offer: %+v", reply)
	}
	if err = client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: reply.SDP}); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(8 * time.Second)
	var seq uint16
	send := func() {
		seq++
		if e := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}}); e != nil {
			t.Fatal(e)
		}
	}
	for {
		select {
		case e := <-started:
			if e != nil {
				t.Fatal(e)
			}
			goto bound
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("incoming track not bound")
		}
	}
bound:
	view, err := room.JoinSession(sfu.Participant{ID: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if err = room.SubscribeSession("viewer", view, id); err != nil {
		t.Fatal(err)
	}
	sub, err := room.OpenSubscriptionRTP("viewer", view, id)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for {
		select {
		case packet := <-sub.Packets():
			if packet == nil || len(packet.Payload) != 3 {
				t.Fatalf("invalid RTP: %+v", packet)
			}
			goto received
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("no room RTP")
		}
	}
received:
	done := <-ended
	if err = ws.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, sfu.ErrRoomClosed) && !errors.Is(err, sfu.ErrParticipantNotFound) && !errors.Is(err, io.EOF) {
			t.Fatalf("forwarder shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forwarder did not stop")
	}
}
