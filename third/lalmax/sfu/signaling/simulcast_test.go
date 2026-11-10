package signaling

import (
	"context"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/rtpforward"
)

func simulcastTestPeer(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	if err := webrtc.ConfigureSimulcastExtensionHeaders(engine); err != nil {
		t.Fatal(err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(engine, registry); err != nil {
		t.Fatal(err)
	}
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings), webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(registry)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

func negotiateSimulcastPair(t *testing.T, offerer, answerer *webrtc.PeerConnection) {
	t.Helper()
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	offerGathered := webrtc.GatheringCompletePromise(offerer)
	if err = offerer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-offerGathered:
	case <-time.After(5 * time.Second):
		t.Fatal("simulcast offer ICE gathering timed out")
	}
	if err = answerer.SetRemoteDescription(*offerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	answerGathered := webrtc.GatheringCompletePromise(answerer)
	if err = answerer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-answerGathered:
	case <-time.After(5 * time.Second):
		t.Fatal("simulcast answer ICE gathering timed out")
	}
	if err = offerer.SetRemoteDescription(*answerer.LocalDescription()); err != nil {
		t.Fatal(err)
	}
}

func TestMappedSimulcastLayersReachIndependentRoomSubscribers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rooms := sfu.NewManager()
	room, err := rooms.Create(sfu.Key{Namespace: "test", ID: "simulcast"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := access.JoinRoom(ctx, room, testPolicy{}, "publisher", sfu.Participant{ID: "publisher"})
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	publication, err := publisher.PublishMapped(ctx, "video", "camera", "video/H264", "camera-track")
	if err != nil {
		t.Fatal(err)
	}
	viewerSession, err := room.JoinSession(sfu.Participant{ID: "report-viewer"})
	if err != nil {
		t.Fatal(err)
	}
	defer room.LeaveSession("report-viewer", viewerSession)
	if err = room.SubscribeSession("report-viewer", viewerSession, publication.ID); err != nil {
		t.Fatal(err)
	}
	server := simulcastTestPeer(t)
	type observedLayer struct {
		rid  string
		ssrc uint32
		done <-chan error
	}
	layers := make(chan observedLayer, 3)
	factory := &RoomFactory{}
	server.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		_, done, stopped, startErr := publisher.StartMappedIncomingWithPublication(ctx, track, 4, 32)
		if startErr != nil {
			layers <- observedLayer{rid: "error", done: closedError(startErr)}
			return
		}
		// The actual callback receiver is passed through so RTCP is read from
		// the correct per-RID stream just as RoomFactory does in production.
		factory.trackSenderReports(ctx, receiver, track.RID(), publisher, publication.ID, uint32(track.SSRC()), stopped)
		layers <- observedLayer{rid: track.RID(), ssrc: uint32(track.SSRC()), done: done}
	})
	client := simulcastTestPeer(t)
	capability := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90_000, SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f"}
	rids := []string{"l", "m", "h"}
	values := map[string]byte{"l": 1, "m": 2, "h": 3}
	tracks := make(map[string]*webrtc.TrackLocalStaticRTP, len(rids))
	for _, rid := range rids {
		track, createErr := webrtc.NewTrackLocalStaticRTP(capability, "camera-track", "publisher-stream", webrtc.WithRTPStreamID(rid))
		if createErr != nil {
			t.Fatal(createErr)
		}
		tracks[rid] = track
	}
	sender, err := client.AddTrack(tracks["l"])
	if err != nil {
		t.Fatal(err)
	}
	for _, rid := range []string{"m", "h"} {
		if err = sender.AddEncoding(tracks[rid]); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			if _, _, readErr := sender.ReadRTCP(); readErr != nil {
				return
			}
		}
	}()
	negotiateSimulcastPair(t, client, server)
	var midID, ridID uint8
	for _, extension := range sender.GetParameters().HeaderExtensions {
		switch extension.URI {
		case sdp.SDESMidURI:
			midID = uint8(extension.ID)
		case sdp.SDESRTPStreamIDURI:
			ridID = uint8(extension.ID)
		}
	}
	if midID == 0 || ridID == 0 {
		t.Fatalf("simulcast extensions missing: mid=%d rid=%d", midID, ridID)
	}
	mid := client.GetTransceivers()[0].Mid()
	seq := make(map[string]uint16)
	writeLayers := func() {
		for _, rid := range rids {
			seq[rid]++
			packet := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq[rid], Timestamp: uint32(seq[rid]) * 3000, Marker: true}, Payload: []byte{0x65, values[rid]}}
			if err := packet.Header.SetExtension(midID, []byte(mid)); err != nil {
				t.Errorf("set MID extension: %v", err)
			}
			if err := packet.Header.SetExtension(ridID, []byte(rid)); err != nil {
				t.Errorf("set RID extension: %v", err)
			}
			if writeErr := tracks[rid].WriteRTP(packet); writeErr != nil {
				t.Errorf("write %s RID: %v", rid, writeErr)
			}
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	got := make(map[string]observedLayer, 3)
	deadline := time.After(8 * time.Second)
	for len(got) != 3 {
		select {
		case layer := <-layers:
			if layer.rid == "error" {
				select {
				case err = <-layer.done:
					t.Fatalf("mapped incoming rejected: %v", err)
				default:
					t.Fatal("mapped incoming rejected")
				}
			}
			got[layer.rid] = layer
		case <-ticker.C:
			writeLayers()
		case <-deadline:
			t.Fatalf("timed out binding simulcast RIDs: got %v", mapKeys(got))
		}
	}
	for _, rid := range rids {
		if _, ok := got[rid]; !ok {
			t.Fatalf("missing RID %q; got %v", rid, mapKeys(got))
		}
	}
	for i, rid := range rids {
		ntp := uint64(i + 1)
		if err = client.WriteRTCP([]rtcp.Packet{&rtcp.SenderReport{SSRC: got[rid].ssrc, NTPTime: ntp, RTPTime: uint32(ntp)}}); err != nil {
			t.Fatalf("write %s sender report: %v", rid, err)
		}
		deadline := time.After(2 * time.Second)
		for {
			report, ok, reportErr := room.SenderReportSession("report-viewer", viewerSession, publication.ID)
			if reportErr != nil {
				t.Fatalf("read %s sender report: %v", rid, reportErr)
			}
			if ok && report.NTP == ntp && report.SSRC == got[rid].ssrc {
				break
			}
			select {
			case <-ticker.C:
				writeLayers()
			case <-deadline:
				t.Fatalf("sender report for RID %s was not recorded: got=%+v ok=%v", rid, report, ok)
			}
		}
	}

	viewerASession, err := room.JoinSession(sfu.Participant{ID: "viewer-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer room.LeaveSession("viewer-a", viewerASession)
	viewerBSession, err := room.JoinSession(sfu.Participant{ID: "viewer-b"})
	if err != nil {
		t.Fatal(err)
	}
	defer room.LeaveSession("viewer-b", viewerBSession)
	if err = room.SubscribeSession("viewer-a", viewerASession, publication.ID); err != nil {
		t.Fatal(err)
	}
	if err = room.SubscribeSession("viewer-b", viewerBSession, publication.ID); err != nil {
		t.Fatal(err)
	}
	subA, err := room.OpenSubscriptionRTP("viewer-a", viewerASession, publication.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subA.Close()
	subB, err := room.OpenSubscriptionRTP("viewer-b", viewerBSession, publication.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subB.Close()
	if err = subA.SelectLayer("h"); err != nil {
		t.Fatal(err)
	}
	if err = subB.SelectLayer("m"); err != nil {
		t.Fatal(err)
	}
	for name, sub := range map[string]*rtpforward.LayeredSubscriber{"viewer-a": subA, "viewer-b": subB} {
		select {
		case packet := <-sub.Packets():
			if packet == nil || packet.Payload[1] != values[sub.SelectedLayer()] {
				t.Fatalf("%s received wrong selected RID %q: %v", name, sub.SelectedLayer(), packet)
			}
		case <-ticker.C:
			writeLayers()
		case <-time.After(3 * time.Second):
			t.Fatalf("%s received no selected-layer RTP", name)
		}
	}
	for _, layer := range got {
		select {
		case <-layer.done:
		default:
		}
	}
}

func closedError(err error) <-chan error {
	result := make(chan error, 1)
	result <- err
	close(result)
	return result
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
