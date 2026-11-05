package main

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type signalResult struct {
	Type        string               `json:"type"`
	ID          string               `json:"id"`
	Error       string               `json:"error"`
	SDP         string               `json:"sdp"`
	Publication *struct{ ID string } `json:"publication"`
}

func readResult(t *testing.T, ws *websocket.Conn, id string) signalResult {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(8 * time.Second))
	for {
		var msg signalResult
		if err := ws.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "result" && msg.ID == id {
			return msg
		}
	}
}
func demoPeer(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var ip net.IP
	for _, a := range addresses {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			ip = n.IP.To4()
			break
		}
	}
	if ip == nil {
		t.Skip("requires non-loopback IPv4 interface")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	mux := webrtc.NewICEUDPMux(nil, conn)
	t.Cleanup(func() { _ = mux.Close() })
	setting := webrtc.SettingEngine{}
	setting.SetICEUDPMux(mux)
	setting.SetNAT1To1IPs([]string{ip.String()}, webrtc.ICECandidateTypeHost)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(setting)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}
func gatheredOffer(t *testing.T, pc *webrtc.PeerConnection) string {
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
		t.Fatal("ICE gather timeout")
	}
	return pc.LocalDescription().SDP
}
func TestDemoServerOpusThroughTwoPeers(t *testing.T) {
	_, origin := setup(t)
	resp := login(t, origin, testSecret, origin)
	cookie := resp.Cookies()[0]
	publisher, _, err := dial(t, origin, origin, cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	if err = publisher.WriteJSON(map[string]any{"id": "publish", "type": "publish", "kind": "audio", "source": "microphone", "codec": "audio/opus", "track_id": "audio"}); err != nil {
		t.Fatal(err)
	}
	published := readResult(t, publisher, "publish")
	if published.Error != "" || published.Publication == nil {
		t.Fatalf("publish: %+v", published)
	}
	pubPeer := demoPeer(t)
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "test")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := pubPeer.AddTrack(track)
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
	offer := gatheredOffer(t, pubPeer)
	if err = publisher.WriteJSON(map[string]any{"id": "offer", "type": "offer", "sdp": offer}); err != nil {
		t.Fatal(err)
	}
	answer := readResult(t, publisher, "offer")
	if answer.Error != "" {
		t.Fatalf("publish offer: %+v", answer)
	}
	if err = pubPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		var seq uint16
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				seq++
				_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}})
			}
		}
	}()
	viewer, _, err := dial(t, origin, origin, cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	if err = viewer.WriteJSON(map[string]any{"id": "subscribe", "type": "subscribe", "publication_id": published.Publication.ID}); err != nil {
		t.Fatal(err)
	}
	result := readResult(t, viewer, "subscribe")
	if result.Error != "" {
		t.Fatalf("subscribe: %+v", result)
	}
	viewPeer := demoPeer(t)
	if _, err = viewPeer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte, 1)
	viewPeer.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, e := remote.ReadRTP()
		if e == nil {
			got <- packet.Payload
		}
	})
	viewOffer := gatheredOffer(t, viewPeer)
	deadline := time.Now().Add(6 * time.Second)
	for {
		if err = viewer.WriteJSON(map[string]any{"id": "view-offer", "type": "offer", "sdp": viewOffer}); err != nil {
			t.Fatal(err)
		}
		reply := readResult(t, viewer, "view-offer")
		if reply.Error == "" {
			if err = viewPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: reply.SDP}); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("view offer: %+v", reply)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case payload := <-got:
		if !bytes.Equal(payload, []byte{0xf8, 0xff, 0xfe}) {
			t.Fatalf("payload %x", payload)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no relayed audio")
	}
}
