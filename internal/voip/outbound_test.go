package voip

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
)

const browserTalkOffer = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 8\r\na=sendrecv\r\na=rtpmap:8 PCMA/8000\r\n"

func testOutboundServer(t *testing.T, kind TransportType) (*Server, func(string), func() string) {
	t.Helper()
	cert, key, roots := testSIPCertificate(t)
	cfg := Config{SipListenAddr: "127.0.0.1:0", SipIP: "127.0.0.1", MediaIP: "127.0.0.1", MediaPortMin: 42000, MediaPortMax: 42999, SrtpEnable: true, SipTlsCertFile: cert, SipTlsKeyFile: key, RtpTimeoutMs: 10000}
	switch kind {
	case TransportTCP:
		cfg.SipTcpListenAddr = "127.0.0.1:0"
	case TransportTLS:
		cfg.SipTlsListenAddr = "127.0.0.1:0"
	case TransportWS:
		cfg.SipWsListenAddr = "127.0.0.1:0"
	case TransportWSS:
		cfg.SipWssListenAddr = "127.0.0.1:0"
	case TransportDTLS:
		cfg.SipDtlsListenAddr = "127.0.0.1:0"
	}
	s, err := NewServer(ServerConfig{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	send, read, closePeer := transportPeer(t, s, kind, roots)
	t.Cleanup(func() { closePeer(); s.Close() })
	m, _ := ParseMessage(request("REGISTER", "register-out"))
	m.SetHeader("From", "<sip:1001@localhost>;tag=reg")
	m.SetHeader("Contact", "<sip:1001@192.0.2.100:5060>")
	send(serializePeerRequest(m))
	if !strings.HasPrefix(read(), "SIP/2.0 200") {
		t.Fatal("register failed")
	}
	return s, send, read
}
func readMessage(t *testing.T, read func() string, method Method) *Message {
	t.Helper()
	m, err := ParseMessage(read())
	if err != nil || m.Method != method {
		t.Fatalf("wanted %s got %+v err=%v", method, m, err)
	}
	return m
}
func talkAnswer(port int) string {
	return fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\na=sendrecv\r\n", port)
}
func talkAnswerAt(ip string, port int) string {
	return fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 %s\r\ns=-\r\nc=IN IP4 %s\r\nt=0 0\r\nm=audio %d RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-16\r\na=sendrecv\r\n", ip, ip, port)
}
func waitOutboundState(t *testing.T, s *Server, id string, want DialogState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range s.Status().Calls {
			if c.CallID == id && c.State == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing state %s: %+v", want, s.Status())
}

func TestMatchingOutboundResponseAllowsNormalizedFromURI(t *testing.T) {
	peer := TransportAddr{Type: TransportTLS, Addr: "127.0.0.1:5061"}
	request := &Message{IsRequest: true, Method: MethodInvite, RequestURI: "sips:6002@localhost", Headers: map[string][]string{
		"Via":  {"SIP/2.0/TLS 127.0.0.1:5071;branch=z9hG4bK-test;rport"},
		"From": {"<sip:6001@127.0.0.1:5071>;tag=from-tag"}, "To": {"<sips:6002@localhost>"},
		"Call-ID": {"call-test@lalmax-nvr"}, "CSeq": {"1 INVITE"},
	}}
	response := &Message{IsRequest: false, StatusCode: 401, Headers: map[string][]string{
		"Via":  {"SIP/2.0/TLS 127.0.0.1:5071;branch=z9hG4bK-test;rport=5061;received=172.17.0.1"},
		"From": {"<sip:6001@127.0.0.1>;tag=from-tag"}, "To": {"<sips:6002@localhost>;tag=to-tag"},
		"Call-ID": {"call-test@lalmax-nvr"}, "CSeq": {"1 INVITE"},
	}}
	call := &outboundCall{peer: peer, request: request}
	if !matchingOutboundResponse(response, call, peer) {
		t.Fatal("response with normalized From URI should match by transaction and tag")
	}

	tcpPeer := TransportAddr{Type: TransportTCP, Addr: "127.0.0.1:5060"}
	tcpRequest := *request
	tcpRequest.RequestURI = "sip:6002@localhost;transport=tcp"
	tcpRequest.Headers = cloneHeaders(request.Headers)
	tcpRequest.SetHeader("Via", "SIP/2.0/TCP 127.0.0.1:5071;branch=z9hG4bK-tcp;rport")
	tcpRequest.SetHeader("To", "<sip:6002@localhost;transport=tcp>")
	tcpResponse := *response
	tcpResponse.Headers = cloneHeaders(response.Headers)
	tcpResponse.SetHeader("Via", "SIP/2.0/TCP 127.0.0.1:5071;branch=z9hG4bK-tcp;rport=5060;received=127.0.0.1")
	tcpResponse.SetHeader("To", "<sip:6002@localhost>;tag=to-tag")
	tcpCall := &outboundCall{peer: tcpPeer, request: &tcpRequest}
	if !matchingOutboundResponse(&tcpResponse, tcpCall, tcpPeer) {
		t.Fatal("TCP challenge response without transport parameter should match the INVITE")
	}

	wrongTag := *response
	wrongTag.Headers = cloneHeaders(response.Headers)
	wrongTag.SetHeader("From", "<sip:6001@127.0.0.1>;tag=other-tag")
	if matchingOutboundResponse(&wrongTag, call, peer) {
		t.Fatal("response with a different From tag must not match")
	}
}

func TestContactAddressSupportsSIPAndSIPS(t *testing.T) {
	for _, tc := range []struct {
		contact string
		want    string
	}{
		{"<sip:6001@127.0.0.1:5071;transport=tcp>", "6001@127.0.0.1:5071"},
		{"<sips:6001@127.0.0.1:5071>", "6001@127.0.0.1:5071"},
	} {
		if got := contactAddress(tc.contact); got != tc.want {
			t.Errorf("contactAddress(%q) = %q, want %q", tc.contact, got, tc.want)
		}
	}
}

func TestOutboundRegisteredFlowsAndCancelRace(t *testing.T) {
	for _, kind := range []TransportType{TransportUDP, TransportTCP, TransportTLS, TransportWS, TransportWSS, TransportDTLS} {
		t.Run(string(kind), func(t *testing.T) {
			s, send, read := testOutboundServer(t, kind)
			result, err := s.Dial("1001", "rtp", browserTalkOffer)
			if err != nil {
				t.Fatal(err)
			}
			invite := readMessage(t, read, MethodInvite)
			// Private Contact is used in the request URI while packets use REGISTER's flow.
			if !strings.Contains(invite.RequestURI, "192.0.2.100") {
				t.Fatal(invite.RequestURI)
			}
			send(BuildResponseWithContact(invite, 180, "Ringing", "", "terminal", "<sip:1001@192.0.2.100:5060>"))
			waitOutboundState(t, s, result.CallID, "ringing")
			if !s.Hangup(result.CallID) {
				t.Fatal("cancel failed")
			}
			cancel := readMessage(t, read, MethodCancel)
			if cancel.GetHeader("Via") != invite.GetHeader("Via") {
				t.Fatal("CANCEL branch changed")
			}
			send(BuildResponse(cancel, 200, "OK", ""))
			// A 200 racing CANCEL must be ACKed and immediately terminated by BYE.
			accepted := BuildResponseWithContact(invite, 200, "OK", talkAnswer(45000), "terminal", "<sip:1001@192.0.2.100:5060>")
			send(accepted)
			readMessage(t, read, MethodAck)
			bye := readMessage(t, read, MethodBye)
			if bye.CSeq() != "2 BYE" {
				t.Fatal(bye.CSeq())
			}
			send(BuildResponse(bye, 200, "OK", ""))
			send(accepted)
			readMessage(t, read, MethodAck)
			if len(s.Status().Calls) != 1 || s.Status().Calls[0].State != "ended" {
				t.Fatal(s.Status())
			}
		})
	}
}
func TestOutboundRejectTimeoutLeaseAndValidation(t *testing.T) {
	s, send, read := testOutboundServer(t, TransportUDP)
	if _, err := s.Dial("offline", "rtp", browserTalkOffer); err == nil {
		t.Fatal("offline call accepted")
	}
	if _, err := s.Dial("1001", "bogus", browserTalkOffer); err == nil {
		t.Fatal("invalid security")
	}
	result, err := s.Dial("1001", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	invite := readMessage(t, read, MethodInvite)
	if s.KeepTalk(result.CallID, "wrong") {
		t.Fatal("invalid token accepted")
	}
	send(BuildResponseWithContact(invite, 486, "Busy Here", "", "terminal", ""))
	readMessage(t, read, MethodAck)
	waitOutboundState(t, s, result.CallID, "failed")
	result, err = s.Dial("1001", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	readMessage(t, read, MethodInvite)
	s.requestMu.Lock()
	s.outbound[result.CallID].deadline = time.Now().Add(-time.Second)
	s.runOutbound(time.Now())
	s.requestMu.Unlock()
	waitOutboundState(t, s, result.CallID, "failed")
	result, err = s.Dial("1001", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	readMessage(t, read, MethodInvite)
	s.requestMu.Lock()
	s.outbound[result.CallID].lease = time.Now().Add(-time.Second)
	s.runOutbound(time.Now())
	s.requestMu.Unlock()
	waitOutboundState(t, s, result.CallID, "ended")
}

func TestOutboundWebRTCRTPFullDuplex(t *testing.T) {
	s, send, read := testOutboundServer(t, TransportUDP)
	terminal, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000}, "microphone", "test")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		b := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(b); err != nil {
				return
			}
		}
	}()
	remotePackets := make(chan *rtp.Packet, 4)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			p, _, e := tr.ReadRTP()
			if e != nil {
				return
			}
			select {
			case remotePackets <- p:
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
	result, err := s.Dial("1001", "rtp", pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	invite := readMessage(t, read, MethodInvite)
	accepted := BuildResponseWithContact(invite, 200, "OK", talkAnswer(terminal.LocalAddr().(*net.UDPAddr).Port), "terminal", "<sip:1001@localhost>")
	send(accepted)
	readMessage(t, read, MethodAck)
	waitOutboundState(t, s, result.CallID, DialogStateEstablished)
	if !s.Status().Calls[0].DTMFAvailable {
		t.Fatal("outbound SIP answer did not expose negotiated telephone-event")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	answer, err := s.AttachTalk(ctx, result.CallID, result.TalkToken, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AttachTalk(ctx, result.CallID, result.TalkToken, pc.LocalDescription().SDP); err == nil {
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
		t.Fatal(pc.ConnectionState())
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
	n, source, err := terminal.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	var packet rtp.Packet
	if err = packet.Unmarshal(buf[:n]); err != nil {
		t.Fatal(err)
	}
	if packet.PayloadType != 8 || packet.SSRC == 555 || string(packet.Payload) != string(microphone) || packet.Extension {
		t.Fatalf("incorrect bridge: %+v", packet)
	}
	remote := []byte{0x11, 0x22, 0x33, 0x44}
	for i := 0; i < 5; i++ {
		raw, _ := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SequenceNumber: uint16(100 + i), Timestamp: uint32(i * 160), SSRC: 777}, Payload: remote}).Marshal()
		terminal.WriteToUDP(raw, source)
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case p := <-remotePackets:
		if string(p.Payload) != string(remote) {
			t.Fatalf("payload changed: %v", p.Payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("browser received no terminal audio")
	}
	if err = s.SendDTMF(result.CallID, "#"); err != nil {
		t.Fatal(err)
	}
	terminal.SetReadDeadline(time.Now().Add(time.Second))
	for {
		n, _, err = terminal.ReadFromUDP(buf)
		if err != nil {
			t.Fatal("outbound DTMF RTP did not reach SIP terminal:", err)
		}
		if err = packet.Unmarshal(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if packet.PayloadType == 101 {
			if len(packet.Payload) != 4 || packet.Payload[0] != 11 {
				t.Fatalf("incorrect outbound DTMF packet: %+v", packet)
			}
			break
		}
	}
	if !s.Hangup(result.CallID) {
		t.Fatal("hangup failed")
	}
	bye := readMessage(t, read, MethodBye)
	send(BuildResponse(bye, 200, "OK", ""))
}

func TestOutboundRTPSourceUsesNegotiatedMediaAddress(t *testing.T) {
	s, send, read := testOutboundServer(t, TransportUDP)
	mediaIP := net.IP(nil)
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range interfaces {
		ip, _, parseErr := net.ParseCIDR(addr.String())
		if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() {
			mediaIP = ip.To4()
			break
		}
	}
	if mediaIP == nil {
		t.Skip("test requires a non-loopback IPv4 address")
	}
	terminal, err := net.ListenUDP("udp4", &net.UDPAddr{IP: mediaIP})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	result, err := s.Dial("1001", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	invite := readMessage(t, read, MethodInvite)
	send(BuildResponseWithContact(invite, 200, "OK", talkAnswerAt(mediaIP.String(), terminal.LocalAddr().(*net.UDPAddr).Port), "terminal", "<sip:1001@localhost>"))
	readMessage(t, read, MethodAck)
	waitOutboundState(t, s, result.CallID, DialogStateEstablished)
	call := s.Status().Calls[0]
	packet, err := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SequenceNumber: 1, Timestamp: 160, SSRC: 777}, Payload: []byte{0xD5, 0x55}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	_, err = terminal.WriteToUDP(packet, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: call.AudioPort})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls := s.Status().Calls; len(calls) == 1 && calls[0].AudioReceivedPackets > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("RTP from negotiated media address 127.0.0.2 was rejected: %+v", s.Status().Calls)
}

func TestOutboundSecureMediaAndCodecs(t *testing.T) {
	for _, codec := range []sdp.Payload{{PayloadType: 111, CodecName: "opus", ClockRate: 48000, Codec: base.AvPacketPtOpus}, {PayloadType: 8, CodecName: "PCMA", ClockRate: 8000, Codec: base.AvPacketPtG711A}, {PayloadType: 0, CodecName: "PCMU", ClockRate: 8000, Codec: base.AvPacketPtG711U}} {
		for _, security := range []string{"rtp", "sdes", "dtls-active", "dtls-passive"} {
			t.Run(codec.CodecName+"/"+security, func(t *testing.T) { testSecureDuplex(t, codec, security) })
		}
	}
}
func testSecureDuplex(t *testing.T, codec sdp.Payload, mode string) {
	t.Helper()
	s, send, read := testOutboundServer(t, TransportUDP)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	capability := webrtc.RTPCodecCapability{MimeType: "audio/" + codec.CodecName, ClockRate: uint32(codec.ClockRate)}
	if codec.CodecName == "opus" {
		capability.Channels = 2
	}
	track, err := webrtc.NewTrackLocalStaticRTP(capability, "mic", "test")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		b := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(b); err != nil {
				return
			}
		}
	}()
	browserPackets := make(chan *rtp.Packet, 8)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			p, _, e := tr.ReadRTP()
			if e != nil {
				return
			}
			select {
			case browserPackets <- p:
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
	security := strings.Split(mode, "-")[0]
	result, err := s.Dial("1001", security, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	invite := readMessage(t, read, MethodInvite)
	var sipPort int
	for _, line := range strings.Split(invite.Body, "\r\n") {
		if strings.HasPrefix(line, "m=audio ") {
			fmt.Sscanf(line, "m=audio %d", &sipPort)
		}
	}
	var crypto *media.SrtpContext
	var dtlsConfig *media.DTLSSRTPConfig
	opt := sdp.AnswerOptions{MediaIP: "127.0.0.1", SessionID: 123}
	if security == "sdes" {
		var remoteLine string
		for _, line := range strings.Split(invite.Body, "\r\n") {
			if strings.HasPrefix(line, "a=crypto:") {
				remoteLine = line
			}
		}
		remoteKey, e := extractSrtpKeyMaterial(remoteLine)
		if e != nil {
			t.Fatal(e)
		}
		key, e := media.GenerateSrtpKey()
		if e != nil {
			t.Fatal(e)
		}
		crypto, e = media.CreateSrtpContextPair(key, remoteKey, "AES_CM_128_HMAC_SHA1_80")
		if e != nil {
			t.Fatal(e)
		}
		defer crypto.Close()
		opt.AudioCrypto = media.BuildCryptoAttribute(1, "AES_CM_128_HMAC_SHA1_80", key)
	}
	if security == "dtls" {
		cert, fingerprint, e := media.GenerateDTLSCertificate()
		if e != nil {
			t.Fatal(e)
		}
		opt.DTLSFingerprint = fingerprint
		var remoteFingerprint string
		for _, line := range strings.Split(invite.Body, "\r\n") {
			if strings.HasPrefix(line, "a=fingerprint:") {
				remoteFingerprint = strings.TrimPrefix(line, "a=fingerprint:")
			}
		}
		dtlsConfig = &media.DTLSSRTPConfig{Certificate: cert, RemoteFingerprint: remoteFingerprint, Client: mode == "dtls-active"}
	}
	terminalPackets := make(chan *rtp.Packet, 8)
	terminal, err := media.NewRtpSession(media.RtpSessionConfig{MediaIP: "127.0.0.1", Port: 0, RTCPMux: false, Payload: &codec, SrtpContext: crypto, DTLSSRTP: dtlsConfig, RemoteRTPAddress: fmt.Sprintf("127.0.0.1:%d", sipPort), TimeoutMs: 10000, OnRTP: func(p *rtp.Packet) {
		select {
		case terminalPackets <- p:
		default:
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	opt.AudioPort = terminal.Port()
	opt.AudioRTCPPort = terminal.Port() + 1
	answerSDP := sdp.BuildTalkOffer([]sdp.Payload{codec}, opt, security)
	if security == "dtls" {
		answerSDP = strings.ReplaceAll(answerSDP, "a=setup:actpass", "a=setup:"+strings.TrimPrefix(mode, "dtls-"))
	}
	accepted := BuildResponseWithContact(invite, 200, "OK", answerSDP, "terminal", "<sip:1001@localhost>")
	send(accepted)
	readMessage(t, read, MethodAck)
	waitOutboundState(t, s, result.CallID, DialogStateEstablished)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	answer, err := s.AttachTalk(ctx, result.CallID, result.TalkToken, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pc.ConnectionState() != webrtc.PeerConnectionStateConnected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		t.Fatal(pc.ConnectionState())
	}
	payload := []byte{0xF8, 0xFF, 0xFE}
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
				_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * uint32(codec.ClockRate/50), SSRC: 456}, Payload: payload})
				seq++
			}
		}
	}()
	select {
	case p := <-terminalPackets:
		if p.PayloadType != uint8(codec.PayloadType) || string(p.Payload) != string(payload) {
			t.Fatalf("wrong encrypted microphone bridge: %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not receive microphone")
	}
	for i := 0; i < 5; i++ {
		if err := terminal.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(i), Timestamp: uint32(i * codec.ClockRate / 50), SSRC: 333}, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case p := <-browserPackets:
		if string(p.Payload) != string(payload) {
			t.Fatal("remote payload changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("browser did not receive remote audio")
	}
	acceptedMessage, _ := ParseMessage(accepted)
	s.requestMu.Lock()
	bye := s.outboundDialogRequest(s.outbound[result.CallID], acceptedMessage, MethodBye, 10)
	s.requestMu.Unlock()
	bye.SetHeader("From", acceptedMessage.To())
	bye.SetHeader("To", invite.From())
	send(serializeRequest(bye))
	response, _ := ParseMessage(read())
	if response.StatusCode != 200 {
		t.Fatal(response)
	}
	waitOutboundState(t, s, result.CallID, "ended")
}
