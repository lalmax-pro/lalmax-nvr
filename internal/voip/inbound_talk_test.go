package voip

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestInboundWebRTCTalkFullDuplexAndDetach(t *testing.T) {
	s, added, _, _ := newTestServer(t, 5000, 15000)
	terminal, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	port := terminal.LocalAddr().(*net.UDPAddr).Port
	invite := strings.Replace(request("INVITE", "inbound-talk"), "m=audio 40000", "m=audio "+strconv.Itoa(port), 1)
	invite = strings.Replace(invite, "RTP/AVP 8\r\na=rtpmap:8 PCMA/8000", "RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16", 1)
	if response := exchange(t, s, invite); !strings.Contains(response, "SIP/2.0 200 OK") {
		t.Fatal(response)
	}
	pub := <-added
	s.handleMessage(inDialogRequest(s, "ACK", "inbound-talk"), TransportAddr{Type: TransportUDP})
	// SIP media learns the symmetric RTP destination from the first endpoint packet.
	initial := make([]byte, 12+4)
	initial[0], initial[1] = 0x80, 8
	binary.BigEndian.PutUint16(initial[2:], 1)
	binary.BigEndian.PutUint32(initial[4:], 160)
	binary.BigEndian.PutUint32(initial[8:], 777)
	copy(initial[12:], []byte{0x11, 0x22, 0x33, 0x44})
	if _, err = terminal.WriteToUDP(initial, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pub.AudioPort()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	token, err := s.ClaimInboundTalk("inbound-talk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AttachInboundTalk(context.Background(), "inbound-talk", "wrong-token", ""); err == nil {
		t.Fatal("invalid token accepted")
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000}, "microphone", "browser")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	remoteAudio := make(chan *rtp.Packet, 8)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case remoteAudio <- packet:
			default:
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gather
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	answer, err := s.AttachInboundTalk(ctx, "inbound-talk", token, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AttachInboundTalk(ctx, "inbound-talk", token, pc.LocalDescription().SDP); err == nil {
		t.Fatal("second browser accepted")
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pc.ConnectionState() != webrtc.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		t.Fatalf("WebRTC did not connect: %s", pc.ConnectionState())
	}

	microphone := []byte{0xD5, 0x55, 0xD5, 0x55}
	for i := 0; i < 5; i++ {
		if err = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SequenceNumber: uint16(i), Timestamp: uint32(i * 160), SSRC: 555}, Payload: microphone}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	terminal.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := terminal.ReadFromUDP(buf)
	if err != nil {
		t.Fatal("browser RTP did not reach SIP terminal:", err)
	}
	var outbound rtp.Packet
	if err = outbound.Unmarshal(buf[:n]); err != nil || outbound.PayloadType != 8 || string(outbound.Payload) != string(microphone) {
		t.Fatalf("incorrect browser-to-SIP RTP: packet=%+v err=%v", outbound, err)
	}
	if err = s.SendDTMF("inbound-talk", "5"); err != nil {
		t.Fatal(err)
	}
	terminal.SetReadDeadline(time.Now().Add(time.Second))
	var dtmf *rtp.Packet
	for dtmf == nil {
		n, _, readErr := terminal.ReadFromUDP(buf)
		if readErr != nil {
			t.Fatal("DTMF RTP did not reach SIP terminal:", readErr)
		}
		var packet rtp.Packet
		if err = packet.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if packet.PayloadType == 101 {
			dtmf = &packet
		}
	}
	if len(dtmf.Payload) != 4 || dtmf.Payload[0] != 5 {
		t.Fatalf("incorrect SIP DTMF event: %+v", dtmf)
	}

	remotePayload := []byte{0x11, 0x22, 0x33, 0x44}
	for i := 0; i < 5; i++ {
		packet := make([]byte, 12+len(remotePayload))
		packet[0], packet[1] = 0x80, 8
		binary.BigEndian.PutUint16(packet[2:], uint16(i+2))
		binary.BigEndian.PutUint32(packet[4:], uint32((i+2)*160))
		binary.BigEndian.PutUint32(packet[8:], 777)
		copy(packet[12:], remotePayload)
		if _, err = terminal.WriteToUDP(packet, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: pub.AudioPort()}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case packet := <-remoteAudio:
		if string(packet.Payload) != string(remotePayload) {
			t.Fatalf("terminal audio payload changed: %v", packet.Payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIP terminal RTP did not reach browser")
	}

	if !s.DetachInboundTalk("inbound-talk", token) {
		t.Fatal("detach failed")
	}
	if s.dialogManager.Get("inbound-talk") == nil {
		t.Fatal("leaving browser talk ended the inbound SIP call")
	}
	if !s.Status().Calls[0].TalkAvailable || s.Status().Calls[0].BrowserReady {
		t.Fatalf("detached SIP call state mismatch: %+v", s.Status().Calls[0])
	}
	if got := exchange(t, s, inDialogRequest(s, "BYE", "inbound-talk")); !strings.Contains(got, "200 OK") {
		t.Fatal(got)
	}
}
