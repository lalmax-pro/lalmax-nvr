package signaling

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
)

func TestSenderReportSSRCReuseIsRejected(t *testing.T) {
	c := &senderReportCollector{targets: map[uint32]*senderReportTarget{}, seen: map[uint32]struct{}{}}
	if !c.bind(7, &senderReportTarget{publication: "old"}) {
		t.Fatal("first binding rejected")
	}
	if c.bind(7, &senderReportTarget{publication: "new"}) {
		t.Fatal("SSRC reuse accepted")
	}
	if _, ok := c.targets[7]; ok {
		t.Fatal("ambiguous mapping retained")
	}
	if c.bind(7, &senderReportTarget{publication: "another"}) {
		t.Fatal("reused SSRC became valid again")
	}
}

func TestIncomingSenderReportReachesSubscribedRoom(t *testing.T) {
	h, room := newHandler(t)
	serverPC := testPeer(t)
	ssrcs := make(chan uint32, 1)
	factory := &RoomFactory{
		NewPeer: func(context.Context, Identity) (*webrtc.PeerConnection, error) { return serverPC, nil },
		Codec: func(sfu.Publication) (webrtc.RTPCodecCapability, error) {
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
		},
		MaxSubscribers: 4, QueueSize: 8,
		OnInbound: func(_ Identity, track *webrtc.TrackRemote, err error) {
			if err == nil {
				ssrcs <- uint32(track.SSRC())
			}
		},
	}
	h.NewSessionNegotiator = factory.NewNegotiator
	server := httptest.NewServer(h)
	defer server.Close()
	ws, _, err := dial(t, server.URL, "member", "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = readType(t, ws, "snapshot")
	if err = ws.WriteJSON(command{ID: "pub", Type: "publish", Kind: "audio", Source: "microphone", Codec: "audio/opus", TrackID: "mic"}); err != nil {
		t.Fatal(err)
	}
	p := readType(t, ws, "result").Publication
	if p == nil {
		t.Fatal("publish failed")
	}
	viewer, err := room.JoinSession(sfu.Participant{ID: "report-reader"})
	if err != nil {
		t.Fatal(err)
	}
	if err = room.SubscribeSession("report-reader", viewer, p.ID); err != nil {
		t.Fatal(err)
	}
	pc := testPeer(t)
	defer pc.Close()
	local, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "mic", "stream")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pc.AddTrack(local); err != nil {
		t.Fatal(err)
	}
	negotiateWS(t, ws, pc)
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Second)
	var source uint32
	for source == 0 {
		select {
		case source = <-ssrcs:
		case <-ticker.C:
			_ = local.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, Payload: []byte{0xf8, 0xff, 0xfe}})
		case <-timeout:
			t.Fatal("source did not bind")
		}
	}
	// Sender reports are still consumed after a quiet interval.
	time.Sleep(1200 * time.Millisecond)
	for {
		if err = pc.WriteRTCP([]rtcp.Packet{&rtcp.SenderReport{SSRC: source, NTPTime: 0x123456789abcdef0, RTPTime: 12345}}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-timeout:
			t.Fatal("sender report not recorded")
		}
		report, ok, err := room.SenderReportSession("report-reader", viewer, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && report.NTP == 0x123456789abcdef0 {
			if report.SSRC != source || report.RTP != 12345 {
				t.Fatalf("incorrect report: %+v", report)
			}
			break
		}
	}
	_ = ws.Close()
	_ = pc.Close()
	deadline := time.After(3 * time.Second)
	for {
		factory.reportMu.Lock()
		readers := len(factory.reportReaders)
		factory.reportMu.Unlock()
		if readers == 0 {
			break
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("RTCP reader was not released")
		}
	}
}
