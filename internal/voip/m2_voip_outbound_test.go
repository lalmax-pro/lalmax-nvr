package voip

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/base"
	maxvoip "github.com/q191201771/lalmax/voip"
	"github.com/q191201771/lalmax/voip/media"
	"github.com/q191201771/lalmax/voip/sdp"
)

func m2TalkCodecs(t *testing.T) []sdp.Payload {
	t.Helper()
	codecs, err := sdp.TalkCodecs(browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	return codecs
}

func m2OutboundRequest(t *testing.T, callID string) *Message {
	t.Helper()
	m, err := ParseMessage("INVITE sip:1001@127.0.0.1 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-outbound;rport\r\n" +
		"From: <sip:nvr@127.0.0.1:5060>;tag=nvr-tag\r\n" +
		"To: <sip:1001@127.0.0.1>\r\n" +
		"Call-ID: " + callID + "\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Contact: <sip:nvr@127.0.0.1:5060>\r\n" +
		"Content-Type: application/sdp\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func m2OutboundCall(t *testing.T, s *Server, callID string) *outboundCall {
	t.Helper()
	port, rtcpPort, err := s.portAllocator.AllocMedia(false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	return &outboundCall{
		id: callID, token: "token", user: "1001", authUsername: "1001", authPassword: "pass",
		peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9999"},
		request: m2OutboundRequest(t, callID), codecs: m2TalkCodecs(t), security: "rtp",
		port: port, rtcpPort: rtcpPort, state: "dialing", created: now,
		deadline: now.Add(time.Minute), lease: now.Add(time.Minute), interval: sipT1,
	}
}

func m2OutboundResponse(c *outboundCall, status int, toTag string, headers map[string]string, body string) *Message {
	m := &Message{IsRequest: false, StatusCode: status, Headers: map[string][]string{
		"Via": {c.request.GetHeader("Via")}, "From": {c.request.From()},
		"To": {c.request.To()}, "Call-ID": {c.id}, "CSeq": {c.request.CSeq()},
	}}
	if toTag != "" {
		m.SetHeader("To", m.To()+";tag="+toTag)
	}
	for k, v := range headers {
		m.SetHeader(k, v)
	}
	m.Body = body
	return m
}

func TestM2VoipDialValidation(t *testing.T) {
	s, send, read := testOutboundServer(t, TransportUDP)
	_ = send

	if _, err := s.Dial("1001", "rtp", "not a valid offer"); err == nil {
		t.Fatal("invalid browser offer accepted")
	}
	if _, err := s.Dial("1001", "bogus", browserTalkOffer); err == nil {
		t.Fatal("invalid media security accepted")
	}
	if _, err := s.Dial("9999", "rtp", browserTalkOffer); err == nil {
		t.Fatal("offline user accepted")
	}

	result, err := s.Dial("1001", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	readMessage(t, read, MethodInvite)
	if _, err := s.Dial("1001", "rtp", browserTalkOffer); err == nil {
		t.Fatal("second call to busy terminal accepted")
	}

	// The token leases the outgoing call; unknown ids fall through to inbound.
	if !s.KeepTalk(result.CallID, result.TalkToken) {
		t.Fatal("valid token rejected")
	}
	if s.KeepTalk(result.CallID, "wrong") {
		t.Fatal("invalid token accepted")
	}
	if s.KeepTalk("missing-call", result.TalkToken) {
		t.Fatal("unknown call kept alive")
	}
	if s.Hangup(result.CallID) != true {
		t.Fatal("hangup failed")
	}

	// Too many active or retained calls are rejected.
	s.requestMu.Lock()
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("busy-%d", i)
		s.outbound[id] = &outboundCall{id: id, user: fmt.Sprintf("user-%d", i), state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"}}
	}
	s.requestMu.Unlock()
	if _, err := s.Dial("1001", "rtp", browserTalkOffer); err == nil {
		t.Fatal("outgoing call limit not enforced")
	}
	s.requestMu.Lock()
	s.outbound = map[string]*outboundCall{}
	s.requestMu.Unlock()

	// A single-port allocator cannot satisfy an odd/even RTP pair.
	s.requestMu.Lock()
	s.portAllocator = media.NewPortAllocator(40000, 40000)
	s.requestMu.Unlock()
	if _, err := s.Dial("1001", "rtp", browserTalkOffer); err == nil {
		t.Fatal("port exhaustion not reported")
	}
}

func TestM2VoipDialSendAndContactFailures(t *testing.T) {
	t.Run("invalid contact", func(t *testing.T) {
		s, _, _ := testOutboundServer(t, TransportUDP)
		s.registrar.Register("2002", "invalid-contact", "test", 3600)
		s.registrar.SetPeer("2002", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
		if _, err := s.Dial("2002", "rtp", browserTalkOffer); err == nil {
			t.Fatal("invalid registered contact accepted")
		}
	})

	t.Run("send failure", func(t *testing.T) {
		s, _, _ := testOutboundServer(t, TransportUDP)
		s.requestMu.Lock()
		s.udpTransport = nil
		s.requestMu.Unlock()
		if _, err := s.Dial("1001", "rtp", browserTalkOffer); err == nil {
			t.Fatal("signal send failure not reported")
		}
		if len(s.outbound) != 0 {
			t.Fatal("failed dial retained call state")
		}
	})

	t.Run("sdes", func(t *testing.T) {
		s, _, read := testOutboundServer(t, TransportUDP)
		if _, err := s.Dial("1001", "sdes", browserTalkOffer); err != nil {
			t.Fatal(err)
		}
		invite := read()
		if !containsAll(invite, "RTP/SAVP", "a=crypto:1 AES_CM_128_HMAC_SHA1_80") {
			t.Fatalf("sdes invite = %q", invite)
		}
	})

	t.Run("security disabled", func(t *testing.T) {
		s, err := NewServer(ServerConfig{Config: Config{SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 47000, MediaPortMax: 47100}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.registrar.Register("1001", "<sip:1001@127.0.0.1:9999>", "test", 3600)
		s.registrar.SetPeer("1001", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
		if _, err := s.Dial("1001", "dtls", browserTalkOffer); err == nil {
			t.Fatal("dtls without srtp enabled accepted")
		}
		if _, err := s.Dial("1001", "sdes", browserTalkOffer); err == nil {
			t.Fatal("sdes without srtp enabled accepted")
		}
	})

	t.Run("stopped", func(t *testing.T) {
		s, _, _ := testOutboundServer(t, TransportUDP)
		_ = s.Close()
		if _, err := s.Dial("1001", "rtp", browserTalkOffer); err == nil {
			t.Fatal("stopped server accepted a call")
		}
	})
}

func containsAll(value string, parts ...string) bool {
	for _, p := range parts {
		found := false
		for i := 0; i+len(p) <= len(value); i++ {
			if value[i:i+len(p)] == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestM2VoipDialPBXExtension(t *testing.T) {
	s, err := NewServer(ServerConfig{Config: Config{
		SipListenAddr: "127.0.0.1:0", MediaIP: "127.0.0.1", MediaPortMin: 47200, MediaPortMax: 47300,
		PbxServer: "127.0.0.1:1", PbxTransport: "udp", PbxUsername: "1001", PbxPassword: "pass", PbxDomain: "pbx.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.requestMu.Lock()
	s.upstream = &upstreamRegistration{peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"}, state: "registered"}
	s.requestMu.Unlock()

	if _, err := s.Dial("bad extension", "rtp", browserTalkOffer); err == nil {
		t.Fatal("invalid PBX extension accepted")
	}
	result, err := s.Dial("1001", "rtp", browserTalkOffer)
	if err != nil {
		t.Fatal(err)
	}
	if result.CallID == "" {
		t.Fatal("PBX dial returned no call id")
	}
	s.requestMu.Lock()
	c := s.outbound[result.CallID]
	s.requestMu.Unlock()
	if c == nil || c.request.RequestURI != "sip:1001@pbx.test" || c.authUsername != "1001" {
		t.Fatalf("PBX dial request = %+v", c)
	}
	if s.Hangup(result.CallID) != true {
		t.Fatal("PBX call hangup failed")
	}
}

func TestM2VoipMatchingOutboundResponseBranches(t *testing.T) {
	call := &outboundCall{
		peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:5060"},
		request: &Message{IsRequest: true, Method: MethodInvite, RequestURI: "sip:1001@example.test", Headers: map[string][]string{
			"Via": {"SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-a;rport"}, "From": {"<sip:nvr@127.0.0.1>;tag=nvr"},
			"To": {"<sip:1001@example.test;transport=tcp>"}, "Call-ID": {"c"}, "CSeq": {"1 INVITE"},
		}},
	}
	mk := func() *Message {
		return &Message{Headers: map[string][]string{"Via": {"SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-a;rport"}, "From": {"<sip:nvr@127.0.0.1>;tag=nvr"}, "To": {"<sip:1001@example.test>;tag=term"}, "Call-ID": {"c"}, "CSeq": {"1 INVITE"}}}
	}
	if !matchingOutboundResponse(mk(), call, call.peer) {
		t.Fatal("normalized transport response not matched")
	}
	if matchingOutboundResponse(mk(), call, TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:5061"}) {
		t.Fatal("peer mismatch matched")
	}
	wrongFrom := mk()
	wrongFrom.SetHeader("From", "<sip:nvr@127.0.0.1>;tag=other")
	if matchingOutboundResponse(wrongFrom, call, call.peer) {
		t.Fatal("from tag mismatch matched")
	}
	wrongCSeq := mk()
	wrongCSeq.SetHeader("CSeq", "2 INVITE")
	if matchingOutboundResponse(wrongCSeq, call, call.peer) {
		t.Fatal("CSeq mismatch matched")
	}
	wrongTo := mk()
	wrongTo.SetHeader("To", "<sip:1002@example.test>;tag=term")
	if matchingOutboundResponse(wrongTo, call, call.peer) {
		t.Fatal("To mismatch matched")
	}
	wrongVia := mk()
	wrongVia.SetHeader("Via", "SIP/2.0/UDP 127.0.0.1:5060;branch=z9hG4bK-b;rport")
	if matchingOutboundResponse(wrongVia, call, call.peer) {
		t.Fatal("transaction mismatch matched")
	}
}

func TestM2VoipHandleOutboundResponseBranches(t *testing.T) {
	s := m2UnitServer(t)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	s.udpTransport = NewUDPTransport(conn, s.log)
	t.Cleanup(func() { _ = conn.Close() })
	if s.handleOutboundResponse(&Message{Headers: map[string][]string{"Call-ID": {"missing"}}}, TransportAddr{}) {
		t.Fatal("unknown outbound response handled")
	}

	call := m2OutboundCall(t, s, "resp-call")
	s.outbound[call.id] = call

	nonInvite := &Message{Headers: map[string][]string{"Call-ID": {call.id}, "CSeq": {"1 BYE"}}}
	if s.handleOutboundResponse(nonInvite, call.peer) {
		t.Fatal("non-INVITE response handled")
	}

	mismatch := m2OutboundResponse(call, 180, "term", nil, "")
	if !s.handleOutboundResponse(mismatch, TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:1"}) || call.provisional {
		t.Fatal("peer-mismatched response reached the call")
	}

	invalid := m2OutboundResponse(call, 99, "term", nil, "")
	if !s.handleOutboundResponse(invalid, call.peer) {
		t.Fatal("invalid status not consumed")
	}

	// 180 rings, 100 keeps the call provisional, and a cancelled call sends
	// CANCEL on the next provisional response.
	if !s.handleOutboundResponse(m2OutboundResponse(call, 180, "term", nil, ""), call.peer) || call.state != "ringing" || !call.provisional {
		t.Fatalf("180 response state = %v provisional=%v", call.state, call.provisional)
	}
	if !s.handleOutboundResponse(m2OutboundResponse(call, 100, "term", nil, ""), call.peer) || call.state != "ringing" {
		t.Fatalf("100 response state = %v", call.state)
	}
	call.cancelled = true
	if !s.handleOutboundResponse(m2OutboundResponse(call, 180, "term", nil, ""), call.peer) || !call.cancelSent {
		t.Fatal("cancelled call did not send CANCEL")
	}
	if tx := s.clientTransactions[call.id]; tx == nil {
		t.Fatal("CANCEL transaction missing")
	}
	call.cancelled = false
	call.cancelSent = false

	// Digest challenges trigger an authenticated retry for 401 and 407.
	challenge := `Digest realm="lalmax-nvr", nonce="abc", algorithm=MD5, qop="auth"`
	if !s.handleOutboundResponse(m2OutboundResponse(call, 401, "term", map[string]string{"WWW-Authenticate": challenge}, ""), call.peer) || call.authAttempts != 1 {
		t.Fatalf("401 attempts = %d", call.authAttempts)
	}
	if !containsAll(call.request.GetHeader("Authorization"), "username=", "response=") {
		t.Fatalf("authorization not attached: %q", call.request.GetHeader("Authorization"))
	}

	// A challenge with an unsupported algorithm cannot retry and fails the call.
	badAlgCall := m2OutboundCall(t, s, "bad-alg")
	s.outbound[badAlgCall.id] = badAlgCall
	if !s.handleOutboundResponse(m2OutboundResponse(badAlgCall, 401, "term", map[string]string{"WWW-Authenticate": `Digest realm="r", nonce="n", algorithm=MD5-sess`}, ""), badAlgCall.peer) {
		t.Fatal("unsupported algorithm response not consumed")
	}
	if badAlgCall.state != "failed" {
		t.Fatalf("unsupported algorithm call state = %s", badAlgCall.state)
	}

	// An invalid digest challenge qop also fails without a retry.
	badQopCall := m2OutboundCall(t, s, "bad-qop")
	s.outbound[badQopCall.id] = badQopCall
	if !s.handleOutboundResponse(m2OutboundResponse(badQopCall, 407, "term", map[string]string{"Proxy-Authenticate": `Digest realm="r", nonce="n", qop="bogus"`}, ""), badQopCall.peer) {
		t.Fatal("invalid qop response not consumed")
	}
	if badQopCall.state != "failed" {
		t.Fatalf("invalid qop call state = %s", badQopCall.state)
	}

	// A final response without a To tag is ignored.
	noTag := &Message{Headers: map[string][]string{
		"Via": {call.request.GetHeader("Via")}, "From": {call.request.From()}, "To": {call.request.To()},
		"Call-ID": {call.id}, "CSeq": {call.request.CSeq()},
	}}
	noTag.StatusCode = 486
	handled := s.handleOutboundResponse(noTag, call.peer)
	if !handled || call.state == "failed" {
		t.Fatalf("untagged final response: handled=%v state=%s to=%q tag=%q", handled, call.state, noTag.To(), ExtractTag(noTag.To()))
	}

	// Rejections are acknowledged and recorded as failed calls.
	if !s.handleOutboundResponse(m2OutboundResponse(call, 486, "term", nil, ""), call.peer) || call.state != "failed" {
		t.Fatalf("486 call state = %s", call.state)
	}

	// A successful answer establishes the call and creates the media session.
	okCall := m2OutboundCall(t, s, "ok-call")
	okCall.peer = TransportAddr{Type: TransportUDP, Addr: conn.LocalAddr().String()}
	s.outbound[okCall.id] = okCall
	answer := talkAnswer(okCall.port)
	if !s.handleOutboundResponse(m2OutboundResponse(okCall, 200, "term", map[string]string{"Contact": "<sip:1001@127.0.0.1:5060>"}, answer), okCall.peer) {
		t.Fatal("200 response not consumed")
	}
	if okCall.state != DialogStateEstablished || okCall.pub == nil {
		t.Fatalf("established call = state %s pub %v", okCall.state, okCall.pub != nil)
	}
	readPeer := func() string {
		buf := make([]byte, 4096)
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	if got := readPeer(); !containsAll(got, "ACK sip:", "CSeq: 1 ACK") {
		t.Fatalf("initial ACK = %q", got)
	}

	// Repeated 2xx responses are ACKed; a forked dialog is released with BYE.
	forked := m2OutboundResponse(okCall, 200, "forked", map[string]string{"Contact": "<sip:1001@127.0.0.1:5060>"}, answer)
	if !s.handleOutboundResponse(forked, okCall.peer) {
		t.Fatal("forked 2xx not consumed")
	}
	if got := readPeer(); !containsAll(got, "ACK sip:", "To: <sip:1001@127.0.0.1>;tag=forked") {
		t.Fatalf("forked dialog ACK = %q", got)
	}
	if got := readPeer(); !containsAll(got, "BYE sip:", "CSeq: 2 BYE") {
		t.Fatalf("forked dialog BYE = %q", got)
	}
	if !s.handleOutboundResponse(m2OutboundResponse(okCall, 200, "term", nil, answer), okCall.peer) {
		t.Fatal("repeated 2xx not consumed")
	}
	if got := readPeer(); !containsAll(got, "ACK sip:", "CSeq: 1 ACK") {
		t.Fatalf("repeated ACK = %q", got)
	}
	s.releaseOutbound(okCall)

	// An invalid SDP answer fails the call.
	badAnswer := m2OutboundCall(t, s, "bad-answer")
	s.outbound[badAnswer.id] = badAnswer
	if !s.handleOutboundResponse(m2OutboundResponse(badAnswer, 200, "term", nil, "garbage"), badAnswer.peer) {
		t.Fatal("invalid answer not consumed")
	}
	if badAnswer.state != "failed" {
		t.Fatalf("invalid answer state = %s", badAnswer.state)
	}
}

func TestM2VoipOutboundSdesAnswer(t *testing.T) {
	s := m2UnitServer(t)
	call := m2OutboundCall(t, s, "sdes-call")
	call.security = "sdes"
	key, err := media.GenerateSrtpKey()
	if err != nil {
		t.Fatal(err)
	}
	call.key = key
	s.outbound[call.id] = call

	answer := fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/SAVP 8\r\na=rtpmap:8 PCMA/8000\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:%s\r\n",
		call.port, base64.StdEncoding.EncodeToString(make([]byte, 30)))
	if !s.handleOutboundResponse(m2OutboundResponse(call, 200, "term", nil, answer), call.peer) {
		t.Fatal("sdes answer not consumed")
	}
	if call.state != DialogStateEstablished {
		t.Fatalf("sdes call state = %s (%s)", call.state, call.reason)
	}
	s.releaseOutbound(call)

	bad := m2OutboundCall(t, s, "sdes-bad")
	bad.security = "sdes"
	bad.key = key
	s.outbound[bad.id] = bad
	badAnswer := fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/SAVP 8\r\na=rtpmap:8 PCMA/8000\r\na=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:short\r\n", bad.port)
	if !s.handleOutboundResponse(m2OutboundResponse(bad, 200, "term", nil, badAnswer), bad.peer) {
		t.Fatal("invalid sdes answer not consumed")
	}
	if bad.state != "failed" {
		t.Fatalf("invalid sdes answer state = %s", bad.state)
	}
}

func TestM2VoipOutboundLifecycleDirect(t *testing.T) {
	s := m2UnitServer(t)
	records := make(chan CallRecord, 8)
	s.onCallEnded = func(record CallRecord) { records <- record }

	// cancelOutbound on an already failed call is a no-op.
	failed := &outboundCall{id: "failed", state: "failed"}
	s.cancelOutbound(failed, "ignored")

	// cancelOutbound without a provisional response only finishes the call.
	dialing := &outboundCall{id: "dialing", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"}, request: m2OutboundRequest(t, "dialing")}
	s.outbound[dialing.id] = dialing
	s.cancelOutbound(dialing, "cancelled by browser")
	if dialing.state != "ended" || !dialing.cancelled {
		t.Fatalf("cancelled call = %+v", dialing)
	}
	select {
	case record := <-records:
		if record.Outcome != "cancelled" {
			t.Fatalf("cancelled record outcome = %s", record.Outcome)
		}
	default:
		t.Fatal("cancelled call did not emit a record")
	}

	// finishOutbound records rejection and miss outcomes.
	final := &outboundCall{id: "rejected", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"}, request: m2OutboundRequest(t, "rejected"), response: &Message{}, created: time.Now()}
	s.finishOutbound(final, "failed", "SIP 486 Busy Here")
	select {
	case record := <-records:
		if record.Outcome != "rejected" {
			t.Fatalf("rejected record outcome = %s", record.Outcome)
		}
	default:
		t.Fatal("rejected call did not emit a record")
	}
	missed := &outboundCall{id: "missed", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"}, request: m2OutboundRequest(t, "missed"), created: time.Now()}
	s.finishOutbound(missed, "failed", "terminal did not answer")
	select {
	case record := <-records:
		if record.Outcome != "missed" {
			t.Fatalf("missed record outcome = %s", record.Outcome)
		}
	default:
		t.Fatal("missed call did not emit a record")
	}
	// An answered call that ends normally is completed.
	completed := &outboundCall{id: "completed", state: "established", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"}, request: m2OutboundRequest(t, "completed"), response: &Message{}, created: time.Now(), answeredAt: time.Now()}
	s.finishOutbound(completed, "ended", "ended by terminal")
	select {
	case record := <-records:
		if record.Outcome != "completed" {
			t.Fatalf("completed record outcome = %s", record.Outcome)
		}
	default:
		t.Fatal("completed call did not emit a record")
	}

	// releaseOutbound disposes media and frees ports.
	released := m2OutboundCall(t, s, "released")
	released.pub = m2PubSession(t, sdp.Negotiated{Audio: &sdp.Payload{PayloadType: 8, Codec: base.AvPacketPtG711A, ClockRate: 8000, CodecName: "PCMA"}})
	port := released.port
	s.releaseOutbound(released)
	if released.pub != nil || released.port != 0 || released.rtcpPort != 0 {
		t.Fatalf("released call = %+v", released)
	}
	if _, _, err := s.portAllocator.AllocMedia(false); err != nil {
		t.Fatal(err)
	}
	s.portAllocator.Free(port)
	s.portAllocator.Free(port + 1)
}

func TestM2VoipRunOutboundTimers(t *testing.T) {
	s := m2UnitServer(t)
	now := time.Now()

	s.outbound["expired-retain"] = &outboundCall{id: "expired-retain", state: "ended", retainUntil: now.Add(-time.Second)}
	s.outbound["lease"] = &outboundCall{id: "lease", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"},
		request: m2OutboundRequest(t, "lease"), deadline: now.Add(time.Minute), lease: now.Add(-time.Second)}
	s.outbound["deadline"] = &outboundCall{id: "deadline", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"},
		request: m2OutboundRequest(t, "deadline"), deadline: now.Add(-time.Second), lease: now.Add(time.Minute), retainUntil: now.Add(time.Minute)}
	retransmit := &outboundCall{id: "retransmit", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"},
		request: m2OutboundRequest(t, "retransmit"), raw: "INVITE sip:x SIP/2.0\r\n\r\n", deadline: now.Add(time.Minute), lease: now.Add(time.Minute),
		next: now.Add(-time.Second), interval: sipT1, retainUntil: now.Add(time.Minute)}
	s.outbound[retransmit.id] = retransmit
	provisional := &outboundCall{id: "provisional", state: "dialing", peer: TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"},
		request: m2OutboundRequest(t, "provisional"), provisional: true, next: now.Add(-time.Second), deadline: now.Add(time.Minute),
		lease: now.Add(time.Minute), retainUntil: now.Add(time.Minute)}
	s.outbound[provisional.id] = provisional

	s.runOutbound(now)

	if _, ok := s.outbound["expired-retain"]; ok {
		t.Fatal("expired retained call not dropped")
	}
	if s.outbound["lease"].state != "ended" {
		t.Fatalf("lease timeout state = %s", s.outbound["lease"].state)
	}
	if s.outbound["deadline"].reason != "terminal did not answer" {
		t.Fatalf("deadline reason = %s", s.outbound["deadline"].reason)
	}
	if retransmit.interval <= sipT1 || retransmit.next.IsZero() {
		t.Fatalf("retransmission not scheduled: %+v", retransmit)
	}
	if provisional.interval != 0 {
		t.Fatalf("provisional call retransmitted: %+v", provisional)
	}
}

func TestM2VoipHandleOutboundRequestBranches(t *testing.T) {
	s := m2UnitServer(t)
	peer := TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9999"}
	call := m2OutboundCall(t, s, "dialog-call")
	call.state = DialogStateEstablished
	call.peer = peer
	call.response = m2OutboundResponse(call, 200, "term", map[string]string{"Contact": "<sip:1001@127.0.0.1:5060>"}, "")
	s.outbound[call.id] = call

	unknown, _ := ParseMessage("OPTIONS sip:x SIP/2.0\r\nCall-ID: missing\r\n\r\n")
	if s.handleOutboundRequest(unknown, peer) {
		t.Fatal("unknown dialog request handled")
	}
	// Requests before a final response are rejected.
	call.response = nil
	early, _ := ParseMessage("BYE sip:x SIP/2.0\r\nCall-ID: dialog-call\r\nFrom: <sip:x@y>;tag=term\r\nTo: <sip:nvr@z>;tag=nvr-tag\r\nCSeq: 2 BYE\r\n\r\n")
	if !s.handleOutboundRequest(early, peer) {
		t.Fatal("early in-dialog request not consumed")
	}
	call.response = m2OutboundResponse(call, 200, "term", nil, "")

	mkRequest := func(method Method, cseq int, body string) *Message {
		m, err := ParseMessage(fmt.Sprintf("%s sip:1001@127.0.0.1 SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:9999;branch=b\r\nFrom: <sip:1001@127.0.0.1>;tag=term\r\nTo: <sip:nvr@127.0.0.1>;tag=nvr-tag\r\nCall-ID: dialog-call\r\nCSeq: %d %s\r\n\r\n%s", method, cseq, method, body))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if !s.handleOutboundRequest(mkRequest(MethodAck, 1, ""), peer) {
		t.Fatal("ACK not consumed")
	}
	if !s.handleOutboundRequest(mkRequest(MethodOptions, 0, ""), peer) || call.remoteCSeq != 0 {
		t.Fatalf("stale options = %d", call.remoteCSeq)
	}
	if !s.handleOutboundRequest(mkRequest(MethodOptions, 2, ""), peer) || call.remoteCSeq != 2 {
		t.Fatalf("options cseq = %d", call.remoteCSeq)
	}
	if !s.handleOutboundRequest(mkRequest(MethodUpdate, 3, ""), peer) {
		t.Fatal("empty UPDATE not consumed")
	}
	if !s.handleOutboundRequest(mkRequest(MethodUpdate, 4, "v=0"), peer) {
		t.Fatal("UPDATE with body not consumed")
	}
	if !s.handleOutboundRequest(mkRequest(Method("INFO"), 5, ""), peer) {
		t.Fatal("INFO not consumed")
	}
	if !s.handleOutboundRequest(mkRequest(MethodBye, 6, ""), peer) {
		t.Fatal("BYE not consumed")
	}
	if call.state != "ended" || call.reason != "ended by terminal" {
		t.Fatalf("BYE call state = %s (%s)", call.state, call.reason)
	}
}

func TestM2VoipOutboundRemainingBranches(t *testing.T) {
	t.Run("dial local ip fallback", func(t *testing.T) {
		s := m2UnitServer(t)
		s.config.MediaIP, s.config.SipIP = "", ""
		s.registrar.Register("1001", "<sip:1001@127.0.0.1:9>", "ua", 3600)
		s.registrar.SetPeer("1001", TransportAddr{Type: TransportUDP, Addr: "127.0.0.1:9"})
		s.requestMu.Lock()
		s.portAllocator = media.NewPortAllocator(49200, 49300)
		s.requestMu.Unlock()
		if _, err := s.Dial("1001", "rtp", browserTalkOffer); err == nil {
			t.Fatal("dial without signaling transport unexpectedly succeeded")
		}
	})

	t.Run("auth retry send failure", func(t *testing.T) {
		s := m2UnitServer(t)
		s.requestMu.Lock()
		s.portAllocator = media.NewPortAllocator(49400, 49500)
		s.requestMu.Unlock()
		call := m2OutboundCall(t, s, "auth-retry")
		call.authAttempts = 0
		s.outbound[call.id] = call
		challenge := `Digest realm="r", nonce="n", algorithm=MD5, qop="auth"`
		if !s.handleOutboundResponse(m2OutboundResponse(call, 401, "term", map[string]string{"WWW-Authenticate": challenge}, ""), call.peer) {
			t.Fatal("401 not consumed")
		}
		if call.state != "failed" || call.authAttempts != 1 {
			t.Fatalf("send failure state = %s attempts=%d", call.state, call.authAttempts)
		}
	})

	t.Run("new pub session failure", func(t *testing.T) {
		s := m2UnitServer(t)
		call := m2OutboundCall(t, s, "pub-failure")
		call.port, call.rtcpPort = 0, 0
		s.portAllocator = media.NewPortAllocator(40000, 40000)
		s.outbound[call.id] = call
		answer := talkAnswer(40000)
		s.handleOutboundResponse(m2OutboundResponse(call, 200, "term", nil, answer), call.peer)
		if call.state != "failed" {
			t.Fatalf("pub failure state = %s", call.state)
		}
	})

	t.Run("publish hook failure", func(t *testing.T) {
		s := m2UnitServer(t)
		call := m2OutboundCall(t, s, "hook-failure")
		s.outbound[call.id] = call
		s.onPubSession = func(*maxvoip.PubSession) error { return fmt.Errorf("publish failed") }
		s.handleOutboundResponse(m2OutboundResponse(call, 200, "term", nil, talkAnswer(call.port)), call.peer)
		if call.state != "failed" {
			t.Fatalf("hook failure state = %s", call.state)
		}
	})

	t.Run("dialog request routing", func(t *testing.T) {
		s := m2UnitServer(t)
		call := m2OutboundCall(t, s, "route-call")
		response := m2OutboundResponse(call, 200, "term", map[string]string{"Contact": "<sip:1001@host:5060>"}, "")
		response.SetHeader("Record-Route", "<sip:proxy1;lr>, <sip:proxy2;lr>")
		response.AddHeader("Record-Route", "<sip:proxy3;lr>")
		request := s.outboundDialogRequest(call, response, MethodBye, 2)
		if request.RequestURI != "sip:1001@host:5060" {
			t.Fatalf("target = %q", request.RequestURI)
		}
		if got := request.GetHeaderAll("Route"); len(got) != 3 || !strings.Contains(got[0], "proxy3") {
			t.Fatalf("routes = %v", got)
		}

		// A strict-routing proxy becomes the target and the old target a route.
		strict := m2OutboundResponse(call, 200, "term", nil, "")
		strict.SetHeader("Record-Route", "<sip:proxy-strict>")
		request = s.outboundDialogRequest(call, strict, MethodBye, 2)
		if request.RequestURI != "sip:proxy-strict" {
			t.Fatalf("strict target = %q", request.RequestURI)
		}
		if got := request.GetHeader("Route"); !strings.Contains(got, call.request.RequestURI) {
			t.Fatalf("strict routes = %q", got)
		}

		// Without Contact the request URI is reused.
		fallback := m2OutboundResponse(call, 200, "term", nil, "")
		if got := s.outboundDialogRequest(call, fallback, MethodBye, 2).RequestURI; got != call.request.RequestURI {
			t.Fatalf("fallback target = %q", got)
		}
	})

	t.Run("bye and finish guards", func(t *testing.T) {
		s := m2UnitServer(t)
		call := m2OutboundCall(t, s, "guards")
		s.sendOutboundBye(call)
		if call.byeSent {
			t.Fatal("BYE sent without a response")
		}
		call.response = m2OutboundResponse(call, 200, "term", nil, "")
		call.byeSent = true
		s.sendOutboundBye(call)
		call.state = "failed"
		s.finishOutbound(call, "ended", "ignored")
		if call.state != "failed" {
			t.Fatal("failed call was finished again")
		}

		pub := m2AudioPub(t, false)
		call.pub = pub
		deleted := 0
		s.onDelSession = func(*maxvoip.PubSession) error { deleted++; return nil }
		s.releaseOutbound(call)
		if deleted != 1 || call.pub != nil {
			t.Fatalf("release hook deleted=%d pub=%v", deleted, call.pub != nil)
		}

		if !sameSIPURIIgnoringTransport("sip:a@b;foo=1;transport=tcp", "sip:a@b;foo=1") {
			t.Fatal("non-transport parameters not preserved")
		}
	})
}
