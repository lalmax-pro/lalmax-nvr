package signaling

import (
	"bytes"
	"context"
	"errors"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDynamicH264SubscriptionAfterInitialOffer(t *testing.T) {
	for _, source := range []string{"camera", "screen"} {
		t.Run(source, func(t *testing.T) { testDynamicH264Subscription(t, source) })
	}
}

func testDynamicH264Subscription(t *testing.T, source string) {
	h, room := newHandler(t)
	pubServer, viewServer := testPeer(t), testPeer(t)
	ready := make(chan error, 1)
	factory := &RoomFactory{NewPeer: func(_ context.Context, id Identity) (*webrtc.PeerConnection, error) {
		if id.Principal == "publisher" {
			return pubServer, nil
		}
		return viewServer, nil
	}, Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
		if p.Codec != "video/H264" {
			return webrtc.RTPCodecCapability{}, errors.New("unsupported")
		}
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, nil
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
	remoteSSRC := make(chan uint32, 2)
	viewer.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case remoteSSRC <- uint32(tr.SSRC()):
		default:
		}
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			select {
			case received <- p.Payload:
			default:
			}
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
	if err = pubWS.WriteJSON(command{ID: "publish", Type: "publish", Kind: "video", Source: source, Codec: "video/H264", TrackID: "video"}); err != nil {
		t.Fatal(err)
	}
	pub := readType(t, pubWS, "result")
	if pub.Publication == nil {
		t.Fatalf("publish: %+v", pub)
	}
	input, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "publisher")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := publisher.AddTrack(input)
	if err != nil {
		t.Fatal(err)
	}
	feedback := make(chan uint32, 10)
	go func() {
		for {
			packets, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, packet := range packets {
				if pli, ok := packet.(*rtcp.PictureLossIndication); ok {
					select {
					case feedback <- pli.MediaSSRC:
					default:
					}
				}
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
		if err := input.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 3000}, Payload: []byte{0x65, 0x01, 0x02}}); err != nil {
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
	transceiver, err := viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, viewerWS, viewer)
	for {
		select {
		case payload := <-received:
			if !bytes.Equal(payload, []byte{0x65, 0x01, 0x02}) {
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
	remote := <-remoteSSRC
	if err = viewer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: remote}}); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-feedback:
		params := sender.GetParameters()
		if len(params.Encodings) != 1 || actual != uint32(params.Encodings[0].SSRC) {
			t.Fatalf("PLI mapped to wrong publisher SSRC: %d", actual)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("viewer keyframe request did not reach publisher")
	}
	if err = viewerWS.WriteJSON(command{ID: "unsubscribe", Type: "unsubscribe", PublicationID: pub.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := readType(t, viewerWS, "result"); r.Error != "" {
		t.Fatal(r.Error)
	}
	// The RTP sender remains negotiated until the next offer, but the old
	// subscription must no longer be allowed to request upstream keyframes.
	time.Sleep(1100 * time.Millisecond)
	if err = viewer.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: remote}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-feedback:
		t.Fatal("unsubscribed viewer forwarded a keyframe request")
	case <-time.After(400 * time.Millisecond):
	}
	select {
	case <-received:
	default:
	}
	// Cancel and restore the subscription before the next SDP offer. The old
	// stopped sender must be replaced even though the publication ID is equal.
	if err = viewerWS.WriteJSON(command{ID: "quick-resubscribe", Type: "subscribe", PublicationID: pub.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := readType(t, viewerWS, "result"); r.Error != "" {
		t.Fatal(r.Error)
	}
	if err = transceiver.Stop(); err != nil {
		t.Fatal(err)
	}
	transceiver, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, viewerWS, viewer)
	quickDeadline := time.After(8 * time.Second)
	for {
		select {
		case <-received:
			goto quickDelivered
		case <-ticker.C:
			send()
		case <-quickDeadline:
			t.Fatal("quick resubscription received no RTP")
		}
	}
quickDelivered:
	if err = viewerWS.WriteJSON(command{ID: "quick-unsubscribe", Type: "unsubscribe", PublicationID: pub.Publication.ID}); err != nil {
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
	if err = viewerWS.WriteJSON(command{ID: "resubscribe", Type: "subscribe", PublicationID: pub.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	if r := readType(t, viewerWS, "result"); r.Error != "" {
		t.Fatal(r.Error)
	}
	if _, err = viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, viewerWS, viewer)
	deadline = time.After(10 * time.Second)
	for {
		select {
		case payload := <-received:
			if !bytes.Equal(payload, []byte{0x65, 0x01, 0x02}) {
				t.Fatalf("wrong resubscribed RTP %x", payload)
			}
			if err = pubWS.WriteJSON(command{ID: "unpublish", Type: "unpublish", PublicationID: pub.Publication.ID}); err != nil {
				t.Fatal(err)
			}
			if r := readType(t, pubWS, "result"); r.Error != "" {
				t.Fatal(r.Error)
			}
			end := time.Now().Add(3 * time.Second)
			for {
				factory.feedbackMu.Lock()
				remaining := len(factory.feedback)
				factory.feedbackMu.Unlock()
				if remaining == 0 {
					break
				}
				if time.Now().After(end) {
					t.Fatal("unpublished source retained feedback binding")
				}
				time.Sleep(time.Millisecond)
			}
			return
		case <-ticker.C:
			send()
		case <-deadline:
			t.Fatal("resubscription received no RTP")
		}
	}
}
